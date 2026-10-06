package main

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxHTTPBodyBytes      = 10 << 20 // 10 MiB request-body cap
	httpReadHeaderTimeout = 5 * time.Second
	httpIdleTimeout       = 2 * time.Minute
)

// httpBodyReadTimeout bounds the total time a request body may take to
// arrive. It is a var (not const) so tests can shorten it. A *total*
// deadline is the right bound: a per-read deadline would not stop a client
// that dribbles a few bytes per read.
var httpBodyReadTimeout = 30 * time.Second

var errBodyReadTimeout = errors.New("request body read timed out")

const apiKeyEnvVar = "MCP_COMMANDS_API_KEY"

// maxAPIKeyFileBytes bounds --api-key-file reads: a token is a short
// secret, so anything larger is a misconfiguration (wrong file) and must
// fail loudly instead of being slurped into memory.
const maxAPIKeyFileBytes = 8 << 10 // 8 KiB

const (
	allowedOriginsEnvVar  = "MCP_COMMANDS_ALLOWED_ORIGINS"
	allowAllOriginsEnvVar = "MCP_COMMANDS_ALLOW_ALL_ORIGINS"
)

// apiKeySource identifies where the configured token came from.
type apiKeySource int

const (
	apiKeySourceNone apiKeySource = iota
	apiKeySourceFlag
	apiKeySourceFile
	apiKeySourceEnv
)

func (s apiKeySource) String() string {
	switch s {
	case apiKeySourceFlag:
		return "flag"
	case apiKeySourceFile:
		return "file"
	case apiKeySourceEnv:
		return "env"
	default:
		return "none"
	}
}

// resolvedAPIKey is the HTTP auth token together with its source
// (--api-key > --api-key-file > MCP_COMMANDS_API_KEY). The zero value is
// "no token configured" (unauthenticated).
type resolvedAPIKey struct {
	Token  string
	Source apiKeySource
}

// resolveAPIKey resolves the HTTP auth token, returning an error when a
// --api-key-file was given but unreadable. File content is TrimSpace'd so
// hand-written files with a trailing newline work.
func resolveAPIKey(flagValue, fileValue string) (resolvedAPIKey, error) {
	if flagValue != "" {
		return resolvedAPIKey{Token: flagValue, Source: apiKeySourceFlag}, nil
	}
	if fileValue != "" {
		f, err := os.Open(fileValue)
		if err != nil {
			return resolvedAPIKey{}, fmt.Errorf("cannot read --api-key-file: %w", err)
		}
		defer f.Close()
		// Bound the *read* itself (cap + 1 so an over-size file is detectable)
		// so a misconfigured path — a multi-GB file, a symlink to /dev/zero —
		// cannot be slurped into memory and OOM the process at startup.
		data, err := io.ReadAll(io.LimitReader(f, maxAPIKeyFileBytes+1))
		if err != nil {
			return resolvedAPIKey{}, fmt.Errorf("cannot read --api-key-file: %w", err)
		}
		if len(data) > maxAPIKeyFileBytes {
			return resolvedAPIKey{}, fmt.Errorf("--api-key-file %s exceeds the %d-byte maximum (wrong file?)", fileValue, maxAPIKeyFileBytes)
		}
		token := strings.TrimSpace(string(data))
		if token == "" {
			return resolvedAPIKey{}, fmt.Errorf("--api-key-file %s is empty; a token file must contain a non-empty token (remove the flag to fall back to %s)", fileValue, apiKeyEnvVar)
		}
		return resolvedAPIKey{Token: token, Source: apiKeySourceFile}, nil
	}
	if envToken := os.Getenv(apiKeyEnvVar); envToken != "" {
		return resolvedAPIKey{Token: envToken, Source: apiKeySourceEnv}, nil
	}
	return resolvedAPIKey{}, nil
}

// isLoopbackHost reports whether host binds only to the local machine: the
// 127.0.0.0/8 range, ::1, and the name "localhost". Anything else —
// including unparseable values and non-IP hostnames — is treated as
// non-loopback, the conservative choice: a hostname that resolves outside
// loopback binds externally.
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		return v4[0] == 127
	}
	return ip.Equal(net.ParseIP("::1"))
}

// checkHTTPSecurityPolicy validates the startup posture of an HTTP bind:
// the bind host, the auth token, the CORS mode, and TLS. It returns an
// error when the server would start unauthenticated on a non-loopback host
// without the explicit --insecure-no-auth escape hatch (an unguarded remote
// command-execution endpoint), plus a list of warnings for postures that
// start but are dangerous:
//
//   - unauthenticated non-loopback bind (authorized via --insecure-no-auth)
//   - --allow-all-origins with no bearer token: the preflight bypass plus
//     the echoed origin let any web page invoke tools and read their output
//   - authenticated non-loopback bind without TLS: a reusable bearer
//     credential and every body transit in cleartext
//
// Warnings are advisory only by design: every posture it names is an
// explicit operator choice, so the server starts and tells the operator
// loudly what they chose. There is deliberately no additional flag.
func checkHTTPSecurityPolicy(host, apiKey string, cors corsConfig, tlsEnabled, acceptsRisk bool) (warnings []string, err error) {
	if apiKey == "" && !isLoopbackHost(host) {
		if !acceptsRisk {
			return nil, fmt.Errorf("refusing to start unauthenticated HTTP server on non-loopback host %q: set --api-key (or %s), or pass --insecure-no-auth explicitly to accept the risk", host, apiKeyEnvVar)
		}
		warnings = append(warnings, fmt.Sprintf("WARNING: UNAUTHENTICATED HTTP server bound to %q — anyone who can reach it can execute scripts as the server user (authorized via --insecure-no-auth)", host))
	}
	if cors.allowAll && apiKey == "" {
		warnings = append(warnings, "WARNING: --allow-all-origins with no bearer token (set --api-key or "+apiKeyEnvVar+") — any web page opened in a browser can invoke tools against this server and read their output")
	}
	if apiKey != "" && !isLoopbackHost(host) && !tlsEnabled {
		warnings = append(warnings, fmt.Sprintf("WARNING: authenticated HTTP server on non-loopback host %q without TLS — the bearer token and every request/response body transit in cleartext; put a TLS-terminating proxy in front or pass --tls-cert/--tls-key", host))
	}
	return warnings, nil
}

// corsConfig holds the resolved CORS and streamable-HTTP mode options for the
// HTTP transport.
type corsConfig struct {
	origins                    []string // exact origin allowlist
	allowAll                   bool     // echo any Origin (dev)
	disableLocalhostProtection bool     // StreamableHTTPOptions.DisableLocalhostProtection
}

// enabled reports whether any CORS behavior is requested.
func (c corsConfig) enabled() bool { return len(c.origins) > 0 || c.allowAll }

// canonicalOrigin returns the origin in the exact form a browser sends in
// the Origin header — the HTML spec's serialized origin: scheme://host[:port]
// with a lowercased host and the port omitted when it is the scheme's
// default (80 for http, 443 for https). Both the configured allowlist and
// the per-request Origin header are compared in this form, so an operator
// who lists "http://host:80" matches the "http://host" the page actually
// sends, and vice versa. It returns "" for unparseable, non-http(s) origins,
// or those with no host, which callers treat as a non-match.
func canonicalOrigin(origin string) string {
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return ""
	}
	host := strings.ToLower(u.Hostname()) // browsers lowercase the host; Go does not
	if strings.Contains(host, ":") {
		// IPv6 literals keep their brackets: the bracketed form is part of the
		// serialized origin, and dropping them makes distinct hosts collide
		// (http://[2001:db8::1]:8080 vs http://[2001:db8::1:8080]).
		host = "[" + host + "]"
	}
	if port := u.Port(); port != "" {
		if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
			return u.Scheme + "://" + host
		}
		return u.Scheme + "://" + host + ":" + port
	}
	return u.Scheme + "://" + host
}

// originSet returns the allowlist as a canonicalized map for membership
// checks (see canonicalOrigin). A config entry that does not canonicalize
// to a valid origin is dropped: it can never match a request header.
func (c corsConfig) originSet() map[string]bool {
	set := make(map[string]bool, len(c.origins))
	for _, origin := range c.origins {
		if canon := canonicalOrigin(origin); canon != "" {
			set[canon] = true
		}
	}
	return set
}

// summary is the startup-log fragment for enabled CORS.
func (c corsConfig) summary() string {
	if c.allowAll {
		return "CORS: any origin — dev mode"
	}
	return fmt.Sprintf("CORS: %d origin(s)", len(c.origins))
}

// parseBoolEnv parses a boolean environment variable value: "" → false;
// "1"/"true"/"yes" (case-insensitive) → true; anything else → error (fail
// loud, no silent misparse).
func parseBoolEnv(name, value string) (bool, error) {
	switch strings.ToLower(value) {
	case "":
		return false, nil
	case "1", "true", "yes":
		return true, nil
	default:
		return false, fmt.Errorf("%s must be 1, true, or yes (case-insensitive), got %q", name, value)
	}
}

// validateOrigin requires an exact http/https origin: a parsable URL with an
// http or https scheme, a non-empty host, and no path, userinfo, query, or
// fragment (https://host[:port] only).
func validateOrigin(origin string) error {
	u, err := url.Parse(origin)
	if err != nil {
		return fmt.Errorf("invalid origin %q: %v", origin, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("invalid origin %q: scheme must be http or https", origin)
	}
	if u.Hostname() == "" {
		return fmt.Errorf("invalid origin %q: missing host", origin)
	}
	if u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("invalid origin %q: must be exactly scheme://host[:port]", origin)
	}
	return nil
}

// resolveCORS resolves flags over env and validates origins. Returns an
// error for malformed origins or a contradictory --allowed-origins +
// --allow-all-origins combination. allowedOriginsSet distinguishes an
// explicit --allowed-origins[=...] from an unset flag: a non-empty flag
// value always wins, and an explicitly *empty* value is a deliberate
// "no origins" choice that does not consult the env var (mirrors the
// allowAllSet rule for the bool flag).
func resolveCORS(allowedOriginsFlag string, allowedOriginsSet, allowAllFlag, allowAllSet, disableLocalhostProtection bool) (corsConfig, error) {
	originsRaw := allowedOriginsFlag
	if originsRaw == "" && !allowedOriginsSet {
		originsRaw = os.Getenv(allowedOriginsEnvVar)
	}

	allowAll := allowAllFlag
	if !allowAllSet {
		parsed, err := parseBoolEnv(allowAllOriginsEnvVar, os.Getenv(allowAllOriginsEnvVar))
		if err != nil {
			return corsConfig{}, err
		}
		allowAll = parsed
	}

	// Check the contradiction before validating origins, so a bad origin
	// string doesn't mask the more actionable error.
	if len(strings.TrimSpace(originsRaw)) > 0 && allowAll {
		return corsConfig{}, fmt.Errorf("--allowed-origins and --allow-all-origins are mutually exclusive")
	}

	var origins []string
	for _, part := range strings.Split(originsRaw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if err := validateOrigin(part); err != nil {
			return corsConfig{}, err
		}
		origins = append(origins, part)
	}

	return corsConfig{
		origins:                    origins,
		allowAll:                   allowAll,
		disableLocalhostProtection: disableLocalhostProtection,
	}, nil
}

// newBearerAuthHandler wraps next, requiring an
// "Authorization: Bearer <token>" header that matches token.
// Mismatches get 401 with a WWW-Authenticate: Bearer header.
func newBearerAuthHandler(next http.Handler, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scheme, got, ok := strings.Cut(r.Header.Get("Authorization"), " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || got == "" {
			reject(w)
			return
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			reject(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// reject writes a 401 with the WWW-Authenticate header per RFC 6750.
func reject(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte("unauthorized"))
}

const (
	corsAllowMaxAge = "900" // 15 min; browsers cap at 7200s
	// The go-sdk exposes no Last-Event-ID/Mcp-Session-Id constants, so the
	// exposed header names stay local. (The method list has no CORS
	// constants in net/http, but the methods do — see corsAllowedMethods.)
	corsExposedHeaders = "Mcp-Session-Id, Last-Event-ID"
)

var corsAllowedMethods = []string{http.MethodPost, http.MethodOptions}

func newCORSHandler(next http.Handler, cfg corsConfig) http.Handler {
	allowed := cfg.originSet()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" || (!cfg.allowAll && !allowed[canonicalOrigin(origin)]) {
			next.ServeHTTP(w, r)
			return
		}
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", origin) // echo, never "*"
		h.Add("Vary", "Origin")
		h.Set("Access-Control-Expose-Headers", corsExposedHeaders)
		if r.Method == http.MethodOptions { // preflight
			h.Set("Access-Control-Allow-Methods", strings.Join(corsAllowedMethods, ", "))
			if achr := r.Header.Get("Access-Control-Request-Headers"); achr != "" {
				h.Set("Access-Control-Allow-Headers", achr)
			}
			h.Set("Access-Control-Max-Age", corsAllowMaxAge)
			w.WriteHeader(http.StatusNoContent)
			return // never call next for preflight
		}
		next.ServeHTTP(w, r)
	})
}

// bodyReadDeadline bounds how long the total body read may take (see
// httpBodyReadTimeout). It complements MaxBytesReader, which caps size but
// not time: a client that dribbles a partial body must not be able to pin
// a connection and its handler indefinitely.
//
// The mechanism is the socket read deadline, armed once for the whole
// remaining window via ResponseController.SetReadDeadline and *not* cleared
// per read. (Clearing after each read is not safe: the runtime processes
// deadline clears asynchronously, and a stale clear can land after the next
// read re-arms the deadline, silently disarming it. The http server itself
// resets the read deadline when the connection is released for keep-alive
// or closed, so leaving it armed is harmless.) When the deadline cannot be
// set — no real connection behind the ResponseWriter, e.g. a test fake —
// the read falls back to a plain synchronous read. Racing it in a goroutine
// against a timer is unsafe: the read is uncancellable, so after the timer
// wins and Read returns it would keep writing into the caller's buffer (and
// leak). In production SetReadDeadline never fails on a live connection, so
// this path is test-only; a closed connection's read returns its error
// promptly.
type bodyReadDeadline struct {
	w        http.ResponseWriter
	in       io.ReadCloser
	deadline time.Time
}

func (b *bodyReadDeadline) Read(p []byte) (int, error) {
	if !time.Now().Before(b.deadline) {
		return 0, errBodyReadTimeout
	}
	if http.NewResponseController(b.w).SetReadDeadline(b.deadline) != nil {
		// No real connection behind this ResponseWriter (hijacked, closed, or
		// a test fake): the read cannot be bounded at the socket, so fall
		// back to a plain read. Deliberately not raced in a goroutine against
		// a timer — see the type comment: an uncancellable read must not be
		// left writing into the caller's buffer after Read returns.
		return b.in.Read(p)
	}
	// The socket enforces the deadline; the inner read returns i/o timeout
	// once it elapses. Map that to the sentinel so callers can classify it.
	n, err := b.in.Read(p)
	if err != nil && !time.Now().Before(b.deadline) {
		return 0, errBodyReadTimeout
	}
	return n, err
}

func (b *bodyReadDeadline) Close() error { return b.in.Close() }

// buildHTTPHandler returns the streamable MCP handler, always constructed
// stateless (the app keeps no per-session state, so protocol sessions are
// vestigial). It is wrapped (innermost) in a request-body size limit
// (maxHTTPBodyBytes) and a total body-read deadline (httpBodyReadTimeout);
// when token is non-empty it is wrapped in bearer-token auth middleware;
// when CORS is enabled it is wrapped (outermost) in the CORS middleware,
// so preflights bypass auth and 401s carry CORS headers.
func buildHTTPHandler(server *mcp.Server, token string, cors corsConfig) http.Handler {
	var h http.Handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, &mcp.StreamableHTTPOptions{
		// Always stateless: the app keeps no per-session state, so protocol
		// sessions are vestigial.
		Stateless:                  true,
		DisableLocalhostProtection: cors.disableLocalhostProtection,
	})
	// Bound request bodies (innermost: below auth and CORS) — a multi-GB
	// chunked body must not be read into memory, and a body that dribbles
	// must not pin the handler past the total read deadline.
	next := h
	h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, &bodyReadDeadline{w: w, in: r.Body, deadline: time.Now().Add(httpBodyReadTimeout)}, maxHTTPBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
	if token != "" {
		h = newBearerAuthHandler(h, token)
	}
	if cors.enabled() {
		h = newCORSHandler(h, cors) // outermost: preflight unauthenticated, 401s carry CORS
	}
	return h
}
