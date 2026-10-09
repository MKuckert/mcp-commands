package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestResolveAPIKey(t *testing.T) {
	tests := []struct {
		name       string
		flag       string
		file       string // content written to a temp file when fileSet
		fileSet    bool
		fileMiss   bool // path set but file missing → error
		wantErr    bool // any explicit-file error (missing, empty, oversized)
		env        string
		want       string
		wantSource apiKeySource
	}{
		{name: "flag_only", flag: "from-flag", want: "from-flag", wantSource: apiKeySourceFlag},
		{name: "env_only", env: "from-env", want: "from-env", wantSource: apiKeySourceEnv},
		{name: "file_only", file: "from-file\n", fileSet: true, want: "from-file", wantSource: apiKeySourceFile},
		{name: "flag_beats_file_and_env", flag: "from-flag", file: "from-file", fileSet: true, env: "from-env", want: "from-flag", wantSource: apiKeySourceFlag},
		{name: "file_beats_env", file: "from-file", fileSet: true, env: "from-env", want: "from-file", wantSource: apiKeySourceFile},
		{name: "empty_file_errors", file: "\n  \n", fileSet: true, env: "from-env", wantErr: true},
		{name: "oversized_file_errors", file: strings.Repeat("a", maxAPIKeyFileBytes+1), fileSet: true, wantErr: true},
		{name: "missing_file_errors", fileMiss: true, wantErr: true},
		{name: "neither_set", want: "", wantSource: apiKeySourceNone},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// t.Setenv to "" counts as empty for resolveAPIKey.
			t.Setenv(apiKeyEnvVar, tt.env)

			var filePath string
			if tt.fileSet || tt.fileMiss {
				if tt.fileMiss {
					filePath = filepath.Join(t.TempDir(), "missing.txt")
				} else {
					filePath = filepath.Join(t.TempDir(), "key.txt")
					if err := os.WriteFile(filePath, []byte(tt.file), 0o600); err != nil {
						t.Fatalf("failed to write key file: %v", err)
					}
				}
			}

			got, err := resolveAPIKey(tt.flag, filePath)
			if tt.fileMiss || tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error for %s, got %v", tt.name, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveAPIKey returned unexpected error: %v", err)
			}
			if got.Token != tt.want {
				t.Errorf("resolveAPIKey token = %q, want %q", got.Token, tt.want)
			}
			if got.Source != tt.wantSource {
				t.Errorf("resolveAPIKey source = %v, want %v", got.Source, tt.wantSource)
			}
		})
	}
}

func (b *bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

// captureWriter is a goroutine-safe strings.Builder for log output capture.
type captureWriter struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (c *captureWriter) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

func (c *captureWriter) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

func TestBearerAuthMiddleware(t *testing.T) {
	t.Parallel()
	const token = "tok"

	tests := []struct {
		name        string
		header      string
		wantStatus  int
		wantReached bool
	}{
		{name: "no_header", header: "", wantStatus: http.StatusUnauthorized, wantReached: false},
		{name: "scheme_only", header: "Bearer", wantStatus: http.StatusUnauthorized, wantReached: false},
		{name: "empty_token", header: "Bearer ", wantStatus: http.StatusUnauthorized, wantReached: false},
		{name: "wrong_scheme", header: "Basic abc", wantStatus: http.StatusUnauthorized, wantReached: false},
		{name: "lowercase_scheme_accepted", header: "bearer " + token, wantStatus: http.StatusOK, wantReached: true},
		{name: "wrong_token", header: "Bearer wrong", wantStatus: http.StatusUnauthorized, wantReached: false},
		{name: "correct_token", header: "Bearer " + token, wantStatus: http.StatusOK, wantReached: true},
		{name: "double_space_token", header: "Bearer  " + token, wantStatus: http.StatusUnauthorized, wantReached: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reached := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached = true
				w.WriteHeader(http.StatusOK)
			})

			req := httptest.NewRequest(http.MethodPost, "/", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			rec := httptest.NewRecorder()

			newBearerAuthHandler(next, token).ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if reached != tt.wantReached {
				t.Errorf("downstream reached = %v, want %v", reached, tt.wantReached)
			}
			if rec.Code == http.StatusUnauthorized && rec.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Errorf("401 without WWW-Authenticate: Bearer, got %q", rec.Header().Get("WWW-Authenticate"))
			}
		})
	}
}

// newTestMCPServer builds an mcp.Server with one trivial tool.
func newTestMCPServer(t *testing.T) *mcp.Server {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	server.AddTool(&mcp.Tool{Name: "noop", Description: "no-op", InputSchema: buildInputSchema([]paramSpec{})}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
	})
	return server
}

// postInitializeStatus POSTs a JSON-RPC initialize request to url and returns
// the response status code. An empty auth value omits the Authorization header.
func postInitializeStatus(t *testing.T, url, auth string) int {
	t.Helper()
	resp := doInitialize(t, url, auth, "")
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// doInitialize POSTs a JSON-RPC initialize request and returns the response.
// Empty auth/origin values omit the corresponding headers.
func doInitialize(t *testing.T, url, auth, origin string) *http.Response {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"0.0.1"}}}`
	req, err := http.NewRequest(http.MethodPost, url+"/", strings.NewReader(body))
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	return resp
}

func TestBuildHTTPHandlerAuthDisabled(t *testing.T) {
	t.Parallel()
	server := newTestMCPServer(t)
	httpServer := httptest.NewServer(buildHTTPHandler(server, testDiscardLogger, "", corsConfig{}))
	defer httpServer.Close()

	if status := postInitializeStatus(t, httpServer.URL, ""); status != http.StatusOK {
		t.Fatalf("unauthenticated request: status = %d, want 200 (only 401s were being checked before; 400/404/500 used to pass silently)", status)
	}
}

// TestBuildHTTPHandlerClientIdentityPeek covers N1 on the HTTP side: the body
// peek logs the client identity from an initialize request at INFO
// (unknown/unknown when clientInfo is absent), logs at most one record per
// initialize even in a batch, stays silent for every other request, and
// resets the body so the SDK handler still serves the request.
func TestBuildHTTPHandlerClientIdentityPeek(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		body   string
		want   []string
		unwant []string
	}{
		{
			name:   "with clientInfo",
			body:   `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"cursor","version":"1.2.3"}}}`,
			want:   []string{"client connected", "clientName=cursor", "clientVersion=1.2.3"},
			unwant: []string{"unknown"},
		},
		{
			name:   "without clientInfo",
			body:   `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{}}}`,
			want:   []string{"client connected", "clientName=unknown", "clientVersion=unknown"},
			unwant: nil,
		},
		{
			name:   "batch with initialize",
			body:   `[{"jsonrpc":"2.0","id":1,"method":"tools/list"},{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"vscode","version":"2.0"}}}]`,
			want:   []string{"client connected", "clientName=vscode", "clientVersion=2.0"},
			unwant: nil,
		},
		{
			name:   "non-initialize",
			body:   `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
			want:   nil,
			unwant: []string{"client connected"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newTestMCPServer(t)
			var logBuf bytes.Buffer
			log := slog.New(newLogHandler(&logBuf, levelTrace))
			httpServer := httptest.NewServer(buildHTTPHandler(server, log, "", corsConfig{}))
			defer httpServer.Close()

			req, err := http.NewRequest(http.MethodPost, httpServer.URL+"/", strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("failed to build request: %v", err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("POST failed: %v", err)
			}
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, resp.Body)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200 (the peek must not break the request)", resp.StatusCode)
			}

			out := logBuf.String()
			for _, s := range tc.want {
				if !strings.Contains(out, s) {
					t.Errorf("log = %q, want it to contain %s", out, s)
				}
			}
			for _, s := range tc.unwant {
				if strings.Contains(out, s) {
					t.Errorf("log = %q, want it to NOT contain %s", out, s)
				}
			}
			// At most one identity record per request.
			if n := strings.Count(out, "client connected"); n > 1 {
				t.Errorf("client-identity records = %d, want at most 1; log: %q", n, out)
			}
		})
	}
}

// TestBuildHTTPHandlerClientIdentityPeekBoundedByCap pins the locked peek
// placement (inside the size/deadline wrapper): an over-cap initialize body
// is rejected (400) by the MaxBytesReader and produces NO identity record —
// the peek's ReadAll runs through the cap, not around it.
func TestBuildHTTPHandlerClientIdentityPeekBoundedByCap(t *testing.T) {
	t.Parallel()
	server := newTestMCPServer(t)
	var logBuf bytes.Buffer
	log := slog.New(newLogHandler(&logBuf, levelTrace))
	httpServer := httptest.NewServer(buildHTTPHandler(server, log, "", corsConfig{}))
	defer httpServer.Close()

	prefix := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
	body := io.NopCloser(io.MultiReader(
		strings.NewReader(prefix),
		strings.NewReader(strings.Repeat("a", maxHTTPBodyBytes+1<<10)),
	))
	req, err := http.NewRequest(http.MethodPost, httpServer.URL+"/", body)
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if got := logBuf.String(); strings.Contains(got, "client connected") {
		t.Errorf("over-cap body produced an identity record, want none; log: %q", got)
	}
}

func TestBuildHTTPHandlerEndToEnd(t *testing.T) {
	t.Parallel()
	server := newTestMCPServer(t)
	httpServer := httptest.NewServer(buildHTTPHandler(server, testDiscardLogger, "s3cret", corsConfig{}))
	defer httpServer.Close()

	if status := postInitializeStatus(t, httpServer.URL, ""); status != http.StatusUnauthorized {
		t.Errorf("unauthenticated request: status = %d, want 401", status)
	}

	status := postInitializeStatus(t, httpServer.URL, "Bearer s3cret")
	if status == http.StatusUnauthorized {
		t.Errorf("authenticated request: status = %d, want non-401", status)
	}
}

// TestBuildHTTPHandlerRejectsOversizedBody covers the maxHTTPBodyBytes cap
// end-to-end: a chunked request whose body exceeds the 10 MiB limit must be
// rejected (400) without being read into memory. Chunked (ContentLength -1)
// so the SDK's io.ReadAll hits the MaxBytesReader limit mid-stream, exactly
// the multi-GB-chunked-body exhaustion vector from the review.
func TestBuildHTTPHandlerRejectsOversizedBody(t *testing.T) {
	t.Parallel()
	server := newTestMCPServer(t)
	httpServer := httptest.NewServer(buildHTTPHandler(server, testDiscardLogger, "", corsConfig{}))
	defer httpServer.Close()

	// Valid JSON-RPC initialize prefix, then enough padding to cross the cap.
	prefix := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
	body := io.NopCloser(io.MultiReader(
		strings.NewReader(prefix),
		strings.NewReader(strings.Repeat("a", maxHTTPBodyBytes+1<<10)),
	))

	req, err := http.NewRequest(http.MethodPost, httpServer.URL, body)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.ContentLength = -1 // chunked transfer encoding
	req.Header.Set("Content-Type", "application/json")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized body: status = %d, want 400", res.StatusCode)
	}
	if res.ContentLength >= maxHTTPBodyBytes {
		t.Fatalf("handler appeared to buffer the whole body (resp Content-Length = %d)", res.ContentLength)
	}
}

func TestResolveCORS(t *testing.T) {
	tests := []struct {
		name           string
		flag           string
		originsSet     bool
		originsEnv     string
		allowAllEnv    string
		allowAllFlag   bool
		allowAllSet    bool
		disableLocalhp bool
		wantOrigins    []string
		wantAllowAll   bool
		wantDisableLHP bool
		wantErr        bool
		errSubstr      string
	}{
		{name: "flag_only", flag: "https://a.example", wantOrigins: []string{"https://a.example"}},
		{name: "env_only", originsEnv: "https://a.example, https://b.example",
			wantOrigins: []string{"https://a.example", "https://b.example"}},
		// An explicitly empty --allowed-origins= is a deliberate "no origins"
		// choice: it must not fall back to the env var (flag always wins).
		{name: "explicit_empty_flag_ignores_env", flag: "", originsSet: true, originsEnv: "https://env.example",
			wantOrigins: []string{}},
		{name: "both_set_flag_wins", flag: "https://flag.example", originsEnv: "https://env.example",
			wantOrigins: []string{"https://flag.example"}},
		{name: "neither_set", wantOrigins: []string{}},
		{name: "comma_space_parsing", flag: "https://a.example, https://b.example",
			wantOrigins: []string{"https://a.example", "https://b.example"}},
		{name: "port_accepted", flag: "https://x.example:8443", wantOrigins: []string{"https://x.example:8443"}},
		// Default ports are valid origins and stored verbatim; canonicalization
		// happens at match time (see canonicalOrigin) so both spellings match.
		{name: "default_port_accepted", flag: "http://x.example:80,https://y.example:443",
			wantOrigins: []string{"http://x.example:80", "https://y.example:443"}},
		{name: "port_only_authority_rejected", flag: "https://:8443", wantErr: true},
		{name: "allow_all_flag", allowAllFlag: true, allowAllSet: true, wantOrigins: []string{}, wantAllowAll: true},
		{name: "allow_all_env_true", allowAllEnv: "TRUE", wantOrigins: []string{}, wantAllowAll: true},
		{name: "allow_all_env_yes", allowAllEnv: "yes", wantOrigins: []string{}, wantAllowAll: true},
		// "0" is not in the recognized set (1/true/yes) → fail loud, per spec.
		{name: "allow_all_env_zero_rejected", allowAllEnv: "0", wantErr: true},
		// An explicit --allow-all-origins[=false] suppresses the env var, so
		// only an unset flag consults it.
		{name: "explicit_false_suppresses_env", allowAllFlag: false, allowAllSet: true, allowAllEnv: "1",
			wantOrigins: []string{}, wantAllowAll: false},
		{name: "unset_flag_reads_env", allowAllFlag: false, allowAllSet: false, allowAllEnv: "1",
			wantOrigins: []string{}, wantAllowAll: true},
		{name: "disable_localhost_protection", flag: "https://a.example", disableLocalhp: true,
			wantOrigins: []string{"https://a.example"}, wantDisableLHP: true},
		{name: "contradictory_origins_and_allow_all", flag: "https://a.example", allowAllFlag: true, allowAllSet: true,
			wantErr: true, errSubstr: "mutually exclusive"},
		{name: "contradiction_reported_before_bad_origin", flag: "notaurl", allowAllFlag: true, allowAllSet: true,
			wantErr: true, errSubstr: "mutually exclusive"},
		{name: "env_origins_and_env_allow_all", originsEnv: "https://a.example", allowAllEnv: "1", wantErr: true,
			errSubstr: "mutually exclusive"},
		{name: "malformed_not_a_url", flag: "notaurl", wantErr: true},
		{name: "malformed_missing_host", flag: "https://", wantErr: true},
		{name: "malformed_scheme", flag: "ftp://x.example", wantErr: true},
		{name: "malformed_userinfo", flag: "https://user@x.example", wantErr: true},
		{name: "malformed_path", flag: "https://x.example/path", wantErr: true},
		{name: "malformed_env_origin", originsEnv: "https://a.example, not-a-url", wantErr: true},
		{name: "allow_all_env_banana", allowAllEnv: "banana", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(allowedOriginsEnvVar, tt.originsEnv)
			t.Setenv(allowAllOriginsEnvVar, tt.allowAllEnv)

			got, err := resolveCORS(tt.flag, tt.originsSet, tt.allowAllFlag, tt.allowAllSet, tt.disableLocalhp)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (%+v)", got)
				}
				if tt.errSubstr != "" && !strings.Contains(err.Error(), tt.errSubstr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tt.errSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got.origins) != len(tt.wantOrigins) {
				t.Fatalf("origins = %v, want %v", got.origins, tt.wantOrigins)
			}
			for i, want := range tt.wantOrigins {
				if got.origins[i] != want {
					t.Errorf("origin[%d] = %q, want %q", i, got.origins[i], want)
				}
			}
			if got.allowAll != tt.wantAllowAll {
				t.Errorf("allowAll = %v, want %v", got.allowAll, tt.wantAllowAll)
			}
			if got.disableLocalhostProtection != tt.wantDisableLHP {
				t.Errorf("disableLocalhostProtection = %v, want %v", got.disableLocalhostProtection, tt.wantDisableLHP)
			}
			if !tt.wantAllowAll && len(tt.wantOrigins) == 0 && got.enabled() {
				t.Errorf("enabled() = true, want false for empty config")
			}
		})
	}
}

func TestCORSHandlerPreflight(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		requestHdr string // Access-Control-Request-Headers; "-" means omit
		wantACRHdr string
	}{
		{name: "echoes_requested_headers", requestHdr: "content-type, authorization", wantACRHdr: "content-type, authorization"},
		{name: "omits_when_not_requested", requestHdr: "-", wantACRHdr: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := corsConfig{origins: []string{"https://app.example.com"}}
			reached := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached = true
				w.WriteHeader(http.StatusTeapot)
			})

			req := httptest.NewRequest(http.MethodOptions, "/", nil)
			req.Header.Set("Origin", "https://app.example.com")
			if tt.requestHdr != "-" {
				req.Header.Set("Access-Control-Request-Headers", tt.requestHdr)
			}
			rec := httptest.NewRecorder()

			newCORSHandler(next, testDiscardLogger, cfg).ServeHTTP(rec, req)

			if rec.Code != http.StatusNoContent {
				t.Errorf("status = %d, want 204", rec.Code)
			}
			if reached {
				t.Error("preflight was forwarded to next")
			}
			if rec.Body.Len() != 0 {
				t.Errorf("body = %q, want empty", rec.Body.String())
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
				t.Errorf("Access-Control-Allow-Origin = %q, want echoed origin", got)
			}
			if got := rec.Header().Get("Access-Control-Allow-Methods"); got != http.MethodPost+", "+http.MethodOptions {
				t.Errorf("Access-Control-Allow-Methods = %q, want %q", got, http.MethodPost+", "+http.MethodOptions)
			}
			if got := rec.Header().Get("Access-Control-Max-Age"); got != "900" {
				t.Errorf("Access-Control-Max-Age = %q, want 900", got)
			}
			if !strings.Contains(rec.Header().Get("Vary"), "Origin") {
				t.Errorf("Vary = %q, want to include Origin", rec.Header().Get("Vary"))
			}
			if got := rec.Header().Get("Access-Control-Allow-Headers"); got != tt.wantACRHdr {
				t.Errorf("Access-Control-Allow-Headers = %q, want %q", got, tt.wantACRHdr)
			}
		})
	}
}

func TestCanonicalOrigin(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want string
	}{
		{in: "http://blackberry", want: "http://blackberry"},
		{in: "http://blackberry:80", want: "http://blackberry"},
		{in: "http://blackberry:8080", want: "http://blackberry:8080"},
		{in: "https://blackberry", want: "https://blackberry"},
		{in: "https://blackberry:443", want: "https://blackberry"},
		{in: "https://blackberry:8443", want: "https://blackberry:8443"},
		// Host case is normalized; browsers lowercase the host in Origin.
		{in: "https://BlackBerry.Example:443", want: "https://blackberry.example"},
		{in: "https://BlackBerry.Example:8443", want: "https://blackberry.example:8443"},
		{in: "http://BlackBerry:80", want: "http://blackberry"},
		// A leading-zero port is not the default: it is kept verbatim and
		// matches nothing a browser can send (fails closed; documented).
		{in: "http://x.example:080", want: "http://x.example:080"},
		// IPv6 literals keep their brackets; dropping them would make distinct
		// hosts collide (both canonicalizations would read as one).
		{in: "http://[2001:db8::1]", want: "http://[2001:db8::1]"},
		{in: "http://[2001:db8::1]:80", want: "http://[2001:db8::1]"},
		{in: "http://[2001:db8::1]:8080", want: "http://[2001:db8::1]:8080"},
		{in: "http://[2001:db8::1:8080]", want: "http://[2001:db8::1:8080]"},
		{in: "http://[2001:DB8::1]:8080", want: "http://[2001:db8::1]:8080"},
		// Non-http(s) and unparseable origins canonicalize to "" (never match).
		{in: "ftp://x.example", want: ""},
		{in: "notaurl", want: ""},
		{in: "https://", want: ""},
		// Non-origin-shaped URLs are protocol violations (a browser only sends a
		// serialized origin or "null"): fail closed, never drop the components.
		{in: "https://allowed.example/path", want: ""},
		{in: "https://allowed.example/", want: ""},
		{in: "https://allowed.example?x=1", want: ""},
		{in: "https://allowed.example#frag", want: ""},
		{in: "https://user@allowed.example", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := canonicalOrigin(tt.in); got != tt.want {
				t.Errorf("canonicalOrigin(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestCORSHandlerNonPreflight(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		cfg         corsConfig
		origin      string // "-" means omit the header
		wantAllowed bool   // CORS headers present?
		wantReached bool
	}{
		{name: "no_origin_passthrough", cfg: corsConfig{origins: []string{"https://app.example.com"}}, origin: "-",
			wantAllowed: false, wantReached: true},
		{name: "disallowed_origin_passthrough", cfg: corsConfig{origins: []string{"https://app.example.com"}}, origin: "https://evil.example",
			wantAllowed: false, wantReached: true},
		{name: "allowed_origin", cfg: corsConfig{origins: []string{"https://app.example.com"}}, origin: "https://app.example.com",
			wantAllowed: true, wantReached: true},
		{name: "allow_all", cfg: corsConfig{allowAll: true}, origin: "https://any.example",
			wantAllowed: true, wantReached: true},
		// Default-port normalization: the browser omits the default port in the
		// Origin header, so a :80 entry must match a bare http origin — and
		// vice versa. The echoed header is always the request's raw value.
		{name: "config_80_matches_bare_http", cfg: corsConfig{origins: []string{"http://blackberry:80"}}, origin: "http://blackberry",
			wantAllowed: true, wantReached: true},
		{name: "config_bare_matches_80", cfg: corsConfig{origins: []string{"http://blackberry"}}, origin: "http://blackberry:80",
			wantAllowed: true, wantReached: true},
		{name: "config_443_matches_bare_https", cfg: corsConfig{origins: []string{"https://blackberry:443"}}, origin: "https://blackberry",
			wantAllowed: true, wantReached: true},
		// A non-default port in the config must not match a bare origin.
		{name: "config_8080_no_match_bare", cfg: corsConfig{origins: []string{"http://blackberry:8080"}}, origin: "http://blackberry",
			wantAllowed: false, wantReached: true},
		// Real-world non-match inputs: sandboxed/null origins and garbage
		// headers canonicalize to "" and never match.
		{name: "null_origin_no_match", cfg: corsConfig{origins: []string{"https://blackberry"}}, origin: "null",
			wantAllowed: false, wantReached: true},
		{name: "garbage_origin_no_match", cfg: corsConfig{origins: []string{"https://blackberry"}}, origin: "notaurl",
			wantAllowed: false, wantReached: true},
		// Protocol-violating Origin (carries a path): must fail closed, not
		// collapse into the allowlisted host.
		{name: "origin_with_path_no_match", cfg: corsConfig{origins: []string{"https://allowed.example"}}, origin: "https://allowed.example/path",
			wantAllowed: false, wantReached: true},
		// Host case is normalized on both sides.
		{name: "uppercase_request_host_matches", cfg: corsConfig{origins: []string{"https://blackberry"}}, origin: "https://BLACKBERRY",
			wantAllowed: true, wantReached: true},
		// IPv6 regression: distinct hosts must not collide via bracket loss.
		{name: "ipv6_distinct_host_no_match", cfg: corsConfig{origins: []string{"http://[2001:db8::1]:8080"}}, origin: "http://[2001:db8::1:8080]",
			wantAllowed: false, wantReached: true},
		{name: "ipv6_same_host_80_matches_bare", cfg: corsConfig{origins: []string{"http://[2001:db8::1]:80"}}, origin: "http://[2001:db8::1]",
			wantAllowed: true, wantReached: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reached := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached = true
				w.WriteHeader(http.StatusOK)
			})

			req := httptest.NewRequest(http.MethodPost, "/", nil)
			if tt.origin != "-" {
				req.Header.Set("Origin", tt.origin)
			}
			rec := httptest.NewRecorder()

			newCORSHandler(next, testDiscardLogger, tt.cfg).ServeHTTP(rec, req)

			if reached != tt.wantReached {
				t.Fatalf("downstream reached = %v, want %v", reached, tt.wantReached)
			}
			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200 (unchanged)", rec.Code)
			}
			acAO := rec.Header().Get("Access-Control-Allow-Origin")
			if tt.wantAllowed {
				if acAO != tt.origin {
					t.Errorf("Access-Control-Allow-Origin = %q, want %q", acAO, tt.origin)
				}
				if !strings.Contains(rec.Header().Get("Vary"), "Origin") {
					t.Errorf("Vary = %q, want to include Origin", rec.Header().Get("Vary"))
				}
				if got := rec.Header().Get("Access-Control-Expose-Headers"); got != "Mcp-Session-Id, Last-Event-ID" {
					t.Errorf("Access-Control-Expose-Headers = %q, want %q", got, "Mcp-Session-Id, Last-Event-ID")
				}
			} else if acAO != "" {
				t.Errorf("Access-Control-Allow-Origin = %q, want absent", acAO)
			}
		})
	}
}

func TestBuildHTTPHandlerPreflightUnauthenticated(t *testing.T) {
	t.Parallel()
	server := newTestMCPServer(t)
	cors := corsConfig{origins: []string{"https://app.example.com"}}
	httpServer := httptest.NewServer(buildHTTPHandler(server, testDiscardLogger, "s3cret", cors))
	defer httpServer.Close()

	req, err := http.NewRequest(http.MethodOptions, httpServer.URL+"/", nil)
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	req.Header.Set("Origin", "https://app.example.com")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("OPTIONS failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204 (not 401)", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("Access-Control-Allow-Origin = %q, want echoed origin", got)
	}
}

func TestBuildHTTPHandler401CarriesCORS(t *testing.T) {
	t.Parallel()
	server := newTestMCPServer(t)
	cors := corsConfig{origins: []string{"https://app.example.com"}}
	httpServer := httptest.NewServer(buildHTTPHandler(server, testDiscardLogger, "s3cret", cors))
	defer httpServer.Close()

	req, err := http.NewRequest(http.MethodPost, httpServer.URL+"/", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer wrong")
	req.Header.Set("Origin", "https://app.example.com")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("401 Access-Control-Allow-Origin = %q, want echoed origin", got)
	}
	if got := resp.Header.Get("Access-Control-Expose-Headers"); got != "Mcp-Session-Id, Last-Event-ID" {
		t.Errorf("401 Access-Control-Expose-Headers = %q, want %q", got, "Mcp-Session-Id, Last-Event-ID")
	}
}

// TestCORSHandlerPreflightDisallowedOrigin: a preflight OPTIONS from an
// origin NOT on the allowlist must be forwarded to the next handler (no 204
// short-circuit, no echoed CORS headers) — the browser then blocks the read.
func TestCORSHandlerPreflightDisallowedOrigin(t *testing.T) {
	t.Parallel()
	cfg := corsConfig{origins: []string{"https://app.example.com"}}
	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusTeapot)
	})

	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()

	newCORSHandler(next, testDiscardLogger, cfg).ServeHTTP(rec, req)

	if !reached {
		t.Error("preflight from a disallowed origin must be forwarded to next, not answered by the CORS layer")
	}
	if acAO := rec.Header().Get("Access-Control-Allow-Origin"); acAO != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want absent for a disallowed origin", acAO)
	}
	if rec.Code != http.StatusTeapot {
		t.Errorf("status = %d, want 418 (unchanged by the CORS layer)", rec.Code)
	}
}

// TestBuildHTTPHandlerMethodAllowlist: the streamable HTTP transport answers
// GET (SSE) and other non-POST methods with 405 (the README's documented
// behavior). The SDK validates the Accept header before the method, so a
// stream Accept header is set; DELETE is session-scoped and in stateless
// mode fails its session precondition (400) instead, so it is checked
// separately.
func TestBuildHTTPHandlerMethodAllowlist(t *testing.T) {
	t.Parallel()
	server := newTestMCPServer(t)
	httpServer := httptest.NewServer(buildHTTPHandler(server, testDiscardLogger, "", corsConfig{}))
	defer httpServer.Close()

	do := func(method string) (int, string) {
		req, err := http.NewRequest(method, httpServer.URL+"/", nil)
		if err != nil {
			t.Fatalf("failed to build %s request: %v", method, err)
		}
		req.Header.Set("Accept", "application/json, text/event-stream")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s request failed: %v", method, err)
		}
		resp.Body.Close()
		return resp.StatusCode, resp.Header.Get("Allow")
	}

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodPatch} {
		status, allow := do(method)
		if status != http.StatusMethodNotAllowed {
			t.Errorf("%s: status = %d, want 405", method, status)
		}
		if allow != "POST" {
			t.Errorf("%s: Allow = %q, want POST", method, allow)
		}
	}

	// DELETE without a session ID fails the session precondition, not the
	// method allowlist (stateless mode accepts a session ID and 204s).
	if status, _ := do(http.MethodDelete); status != http.StatusBadRequest {
		t.Errorf("DELETE: status = %d, want 400 (missing Mcp-Session-Id)", status)
	}
}

func TestBuildHTTPHandlerInitializeCORS(t *testing.T) {
	t.Parallel()
	server := newTestMCPServer(t)
	cors := corsConfig{origins: []string{"https://app.example.com"}}
	httpServer := httptest.NewServer(buildHTTPHandler(server, testDiscardLogger, "s3cret", cors))
	defer httpServer.Close()

	resp := doInitialize(t, httpServer.URL, "Bearer s3cret", "https://app.example.com")
	if resp.StatusCode == http.StatusUnauthorized {
		t.Fatalf("authenticated request: status = %d, want non-401", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("Access-Control-Allow-Origin = %q, want echoed origin", got)
	}
	sessionID := resp.Header.Get("Mcp-Session-Id")
	resp.Body.Close()

	// Always stateless: a stored/re-sent session ID must keep working
	// (the SDK ignores it) — even if it is a made-up value.
	req, err := http.NewRequest(http.MethodPost, httpServer.URL+"/", strings.NewReader(
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer s3cret")
	req.Header.Set("Origin", "https://app.example.com")
	if sessionID == "" {
		sessionID = "some-stale-id"
	}
	req.Header.Set("Mcp-Session-Id", sessionID)

	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("tools/list failed: %v", err)
	}
	defer resp2.Body.Close()
	_, _ = io.Copy(io.Discard, resp2.Body)
	if resp2.StatusCode == http.StatusUnauthorized || resp2.StatusCode == http.StatusNotFound {
		t.Errorf("request with session ID %q: status = %d, want served (stateless ignores session IDs)", sessionID, resp2.StatusCode)
	}
	if got := resp2.Header.Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("Access-Control-Allow-Origin = %q, want echoed origin", got)
	}
}

func TestBuildHTTPHandlerCORSDisabled(t *testing.T) {
	t.Parallel()
	server := newTestMCPServer(t)
	httpServer := httptest.NewServer(buildHTTPHandler(server, testDiscardLogger, "", corsConfig{}))
	defer httpServer.Close()

	resp := doInitialize(t, httpServer.URL, "", "")
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode == http.StatusUnauthorized {
		t.Fatalf("unauthenticated server: status = %d, want non-401", resp.StatusCode)
	}
	headers := resp.Header
	if got := headers.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want absent (CORS disabled)", got)
	}
	if got := headers.Get("Access-Control-Expose-Headers"); got != "" {
		t.Errorf("Access-Control-Expose-Headers = %q, want absent (CORS disabled)", got)
	}
	if got := headers.Get("Vary"); got != "" {
		t.Errorf("Vary = %q, want absent (CORS disabled)", got)
	}
}

// stallingBody serves one byte, then blocks until Close. It simulates a
// client that starts a POST and then dribbles nothing.
type stallingBody struct {
	once     sync.Once
	released chan struct{}
	done     bool
}

func (s *stallingBody) Read(p []byte) (int, error) {
	if !s.done {
		s.done = true
		if len(p) == 0 {
			return 0, nil
		}
		p[0] = '{' // partial JSON; the rest never arrives
		return 1, nil
	}
	<-s.released // the deadline must fire before the test releases
	return 0, io.EOF
}

func (s *stallingBody) Close() error {
	s.once.Do(func() { close(s.released) })
	return nil
}

// TestHTTPBodyReadDeadline: a stalled partial POST must be cut off by the
// total body-read deadline, not pin the handler. Deliberately not
// t.Parallel: it mutates the httpBodyReadTimeout package var.
func TestHTTPBodyReadDeadline(t *testing.T) {
	old := httpBodyReadTimeout
	httpBodyReadTimeout = 300 * time.Millisecond
	t.Cleanup(func() { httpBodyReadTimeout = old })

	server := newTestMCPServer(t)
	httpServer := httptest.NewServer(buildHTTPHandler(server, testDiscardLogger, "", corsConfig{}))
	defer httpServer.Close()

	body := &stallingBody{released: make(chan struct{})}
	req, err := http.NewRequest(http.MethodPost, httpServer.URL+"/mcp", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if elapsed > 10*time.Second {
		t.Errorf("stalled body took %v, want bounded by ~%v (the deadline must release the handler)", elapsed, old)
	}
	if resp.StatusCode == http.StatusOK {
		t.Errorf("status = %d, want an error for the timed-out body", resp.StatusCode)
	}
}

func TestHTTPSecurityPolicy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		host         string
		apiKey       string
		acceptsRisk  bool
		allowAll     bool
		tls          bool
		wantErr      bool
		wantWarnings []string // substrings each expected in the joined warnings
	}{
		{name: "loopback no auth", host: "127.0.0.1"},
		{name: "loopback 127 range", host: "127.0.0.2"},
		{name: "loopback v6", host: "::1"},
		{name: "loopback localhost", host: "localhost"},
		{name: "remote with key cleartext", host: "0.0.0.0", apiKey: "s3cret", wantWarnings: []string{"cleartext"}},
		// The remote-bind case: no key, no escape hatch → refuse.
		{name: "remote no auth refuses", host: "0.0.0.0", wantErr: true},
		{name: "lan ip no auth refuses", host: "192.168.1.10", wantErr: true},
		{name: "private ip no auth refuses", host: "10.0.0.5", wantErr: true},
		// Unparseable host → treated as non-loopback (conservative).
		{name: "unparseable host refuses", host: "not-an-ip", wantErr: true},
		// Escape hatch: starts, but loudly.
		{name: "remote no auth escape hatch", host: "0.0.0.0", acceptsRisk: true, wantWarnings: []string{"UNAUTHENTICATED"}},
		// M6: --allow-all-origins with no token — any web page can invoke
		// tools and read their output.
		{name: "loopback allow-all no auth", host: "127.0.0.1", allowAll: true, wantWarnings: []string{"--allow-all-origins"}},
		{name: "remote allow-all escape no tls", host: "0.0.0.0", acceptsRisk: true, allowAll: true, wantWarnings: []string{"UNAUTHENTICATED", "--allow-all-origins"}},
		// Allow-all WITH a token is defensible: the preflight bypass does not
		// bypass the auth on the real call.
		{name: "loopback allow-all with key", host: "127.0.0.1", apiKey: "s3cret", allowAll: true},
		// M6: authenticated remote bind without TLS — cleartext credential.
		{name: "remote key with tls", host: "0.0.0.0", apiKey: "s3cret", tls: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warnings, err := checkHTTPSecurityPolicy(tt.host, tt.apiKey, corsConfig{allowAll: tt.allowAll}, tt.tls, tt.acceptsRisk)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantErr {
				if !strings.Contains(err.Error(), "--insecure-no-auth") {
					t.Errorf("error should mention the escape hatch: %v", err)
				}
				if !strings.Contains(err.Error(), tt.host) {
					t.Errorf("error should name the host: %v", err)
				}
				return
			}
			joined := strings.Join(warnings, "\n")
			for _, want := range tt.wantWarnings {
				if !strings.Contains(joined, want) {
					t.Fatalf("warnings = %q, want them to contain %q", joined, want)
				}
			}
			if len(tt.wantWarnings) == 0 && len(warnings) != 0 {
				t.Fatalf("warnings = %q, want none", joined)
			}
		})
	}
}

func TestAPIKeySourceString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		src  apiKeySource
		want string
	}{
		{name: "none", src: apiKeySourceNone, want: "none"},
		{name: "flag", src: apiKeySourceFlag, want: "flag"},
		{name: "file", src: apiKeySourceFile, want: "file"},
		{name: "env", src: apiKeySourceEnv, want: "env"},
		// Unknown values fail closed to "none", never to a source name.
		{name: "unknown", src: apiKeySource(99), want: "none"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.src.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCORSConfigSummary(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cfg  corsConfig
		want string
	}{
		{name: "allow_all", cfg: corsConfig{allowAll: true}, want: "CORS: any origin — dev mode"},
		{name: "single_origin", cfg: corsConfig{origins: []string{"https://a.example"}}, want: "CORS: 1 origin(s)"},
		{name: "multiple_origins", cfg: corsConfig{origins: []string{"https://a.example", "https://b.example"}}, want: "CORS: 2 origin(s)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.summary(); got != tt.want {
				t.Errorf("summary() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestCORSHandlerPreflightLogging pins N7 (allowed preflight → DEBUG with
// origin, requestHeaders, maxAge) and N8 (rejected preflight origin → WARN
// with the reason; no record for origin-less requests).
func TestCORSHandlerPreflightLogging(t *testing.T) {
	t.Parallel()
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("next"))
	})
	var logBuf bytes.Buffer
	log := slog.New(newLogHandler(&logBuf, levelTrace))
	h := newCORSHandler(next, log, corsConfig{origins: []string{"https://app.example"}})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	do := func(t *testing.T, headers map[string]string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodOptions, srv.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp
	}

	t.Run("allowed", func(t *testing.T) {
		resp := do(t, map[string]string{
			"Origin":                         "https://app.example",
			"Access-Control-Request-Method":  http.MethodPost,
			"Access-Control-Request-Headers": "x-api-key",
		})
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("status = %d, want 204", resp.StatusCode)
		}
		out := logBuf.String()
		for _, want := range []string{"CORS preflight allowed", "origin=https://app.example", `requestHeaders=x-api-key`, "maxAge=900"} {
			if !strings.Contains(out, want) {
				t.Errorf("log = %q, want it to contain %s", out, want)
			}
		}
	})

	logBuf.Reset()
	t.Run("rejected-not-in-allowlist", func(t *testing.T) {
		resp := do(t, map[string]string{
			"Origin":                        "https://evil.example",
			"Access-Control-Request-Method": http.MethodPost,
		})
		// Rejected preflight falls through to next: no CORS headers.
		if resp.StatusCode != 200 {
			t.Fatalf("status = %d, want 200 (fell through to next)", resp.StatusCode)
		}
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("Allow-Origin = %q, want empty", got)
		}
		out := logBuf.String()
		if !strings.Contains(out, "CORS preflight rejected") || !strings.Contains(out, `origin=https://evil.example`) || !strings.Contains(out, "reason=not-in-allowlist") {
			t.Errorf("log = %q, want a rejection WARN with origin and reason", out)
		}
	})

	logBuf.Reset()
	t.Run("rejected-non-canonical", func(t *testing.T) {
		do(t, map[string]string{
			"Origin":                        "null",
			"Access-Control-Request-Method": http.MethodPost,
		})
		out := logBuf.String()
		if !strings.Contains(out, "CORS preflight rejected") || !strings.Contains(out, `origin=null`) || !strings.Contains(out, "reason=non-canonical") {
			t.Errorf("log = %q, want a non-canonical rejection WARN", out)
		}
	})

	logBuf.Reset()
	t.Run("no-origin-no-record", func(t *testing.T) {
		do(t, map[string]string{"Access-Control-Request-Method": http.MethodPost})
		if got := logBuf.String(); got != "" {
			t.Errorf("log = %q, want nothing for an origin-less preflight", got)
		}
	})
}
