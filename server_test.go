package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"github.com/fsnotify/fsnotify"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestRunHTTPEndToEnd: run() served over real HTTP,
// exercised by a real MCP client (auth, list, call, clean shutdown), plus
// the zero-tools warning.
func TestRunHTTPEndToEnd(t *testing.T) {
	t.Parallel()
	t.Run("one_tool_authenticated", func(t *testing.T) {
		dir := t.TempDir()
		scripts := t.TempDir()
		if err := os.WriteFile(filepath.Join(scripts, "hello.sh"), []byte("#!/bin/bash\necho hello-world\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		port := freePort(t)
		buf := &captureWriter{}
		env := liveEnvFor(t, nil, buf)

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- run(ctx, env, serverConfig{dir: dir, scriptsDir: scripts, host: "127.0.0.1", port: port, apiKey: resolvedAPIKey{Token: "sekret", Source: apiKeySourceFlag}, timeout: defaultToolTimeout})
		}()

		endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
		if !waitForHTTP(t, endpoint) {
			t.Fatal("server did not come up")
		}

		client := mcp.NewClient(&mcp.Implementation{Name: "e2e"}, nil)
		transport := &mcp.StreamableClientTransport{
			Endpoint:   endpoint,
			HTTPClient: &http.Client{Transport: &bearerTransport{token: "sekret"}},
			MaxRetries: 0,
		}
		session, err := client.Connect(ctx, transport, nil)
		if err != nil {
			t.Fatalf("client connect: %v", err)
		}
		defer session.Close()

		res, err := session.ListTools(ctx, nil)
		if err != nil {
			t.Fatalf("ListTools: %v", err)
		}
		if len(res.Tools) != 1 || res.Tools[0].Name != "hello" {
			t.Fatalf("tools = %+v, want exactly hello", res.Tools)
		}

		call, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "hello"})
		if err != nil {
			t.Fatalf("CallTool: %v", err)
		}
		if call.IsError || len(call.Content) == 0 {
			t.Fatalf("call = %+v, want non-error output", call)
		}
		if got := call.Content[0].(*mcp.TextContent).Text; !strings.Contains(got, "hello-world") {
			t.Errorf("output = %q, want to contain hello-world", got)
		}

		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("run returned %v after cancel, want nil", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("run did not stop after cancel")
		}
	})

	t.Run("zero_tools_warns", func(t *testing.T) {
		dir := t.TempDir()
		scripts := t.TempDir() // empty
		port := freePort(t)
		buf := &captureWriter{}
		env := liveEnvFor(t, nil, buf)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			done <- run(ctx, env, serverConfig{dir: dir, scriptsDir: scripts, host: "127.0.0.1", port: port, timeout: defaultToolTimeout})
		}()

		endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
		if !waitForHTTP(t, endpoint) {
			t.Fatal("server did not come up")
		}

		deadline := time.Now().Add(5 * time.Second)
		for {
			if strings.Contains(buf.String(), "WARN@") && strings.Contains(buf.String(), "No executable scripts found") {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("zero-tools warning not captured, stderr = %q", buf.String())
			}
			time.Sleep(20 * time.Millisecond)
		}

		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("run returned %v after cancel, want nil", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("run did not stop after cancel")
		}
	})
}

// TestRunHTTPSTLSEndToEnd: run() with tlsCert/tlsKey serves real TLS —
// a client connects over HTTPS, the startup line carries the TLS note, and
// shutdown stays clean (covers the ListenAndServeTLS wiring the parse
// tests only exercise with dummy PEM files).
func TestRunHTTPSTLSEndToEnd(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	scripts := t.TempDir()
	writeScript(t, filepath.Join(scripts, "hello.sh"), "#!/bin/bash\necho hello-world\n")
	certFile, keyFile := selfSignedCert(t, t.TempDir())
	port := freePort(t)
	buf := &captureWriter{}
	env := liveEnvFor(t, nil, buf)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, env, serverConfig{dir: dir, scriptsDir: scripts, host: "127.0.0.1", port: port, tlsCert: certFile, tlsKey: keyFile, timeout: defaultToolTimeout})
	}()

	insecure := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	endpoint := fmt.Sprintf("https://127.0.0.1:%d", port)
	waitFor(t, 10*time.Second, "TLS server did not come up", func() bool {
		resp, err := insecure.Get(endpoint)
		if err != nil {
			return false
		}
		resp.Body.Close()
		return true
	})

	waitFor(t, 5*time.Second, "TLS startup note", func() bool {
		return strings.Contains(buf.String(), "notes=TLS")
	})

	client := mcp.NewClient(&mcp.Implementation{Name: "e2e-tls"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint:   endpoint,
		HTTPClient: insecure,
		MaxRetries: 0,
	}
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer session.Close()

	res, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(res.Tools) != 1 || res.Tools[0].Name != "hello" {
		t.Fatalf("tools = %+v, want exactly hello", res.Tools)
	}

	call, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "hello"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if got := call.Content[0].(*mcp.TextContent).Text; !strings.Contains(got, "hello-world") {
		t.Errorf("output = %q, want to contain hello-world", got)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v after cancel, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not stop after cancel")
	}
}

// selfSignedCert generates a self-signed ECDSA certificate for 127.0.0.1
// and writes the PEM files into dir, returning their paths.
func selfSignedCert(t *testing.T, dir string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "127.0.0.1"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certFile := filepath.Join(dir, "cert.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

// bearerTransport injects the Authorization header on every request.
type bearerTransport struct{ token string }

// writeScript stages an executable script file in the test's tree.
func writeScript(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("failed to write script %s: %v", path, err)
	}
}

// waitFor polls cond (bounded, no fixed sleeps at call sites) until true and
// fails the test with msg if the timeout elapses first.
func waitFor(t *testing.T, timeout time.Duration, msg string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s: %s", timeout, msg)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// testLogger builds a logger on the given sink at debug level, so test
// fakes capture every record the production logger would emit.
func testLogger(sink io.Writer) *slog.Logger {
	return slog.New(newLogHandler(sink, slog.LevelDebug))
}

// testDiscardLogger is a shared logger that drops every record, for call
// sites that must not emit (a logger is immutable, so sharing it across
// parallel tests is safe).
var testDiscardLogger = slog.New(newLogHandler(io.Discard, 0))

// TestLogLevelFiltering pins the minimum-level gating that
// --log-level resolves to: records below the configured level are
// dropped, records at or above it are written.
func TestLogLevelFiltering(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		level     slog.Level
		wantTrace bool
		wantDebug bool
		wantInfo  bool
		wantWarn  bool
		wantError bool
	}{
		{"trace", levelTrace, true, true, true, true, true},
		{"debug", slog.LevelDebug, false, true, true, true, true},
		{"info", slog.LevelInfo, false, false, true, true, true},
		{"warn", slog.LevelWarn, false, false, false, true, true},
		{"error", slog.LevelError, false, false, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := slog.New(newLogHandler(&buf, tc.level))
			log.Log(context.Background(), levelTrace, "trace record")
			log.Debug("debug record")
			log.Info("info record")
			log.Warn("warn record")
			log.Error("error record")
			out := buf.String()
			if got := strings.Contains(out, "trace record"); got != tc.wantTrace {
				t.Errorf("trace record captured = %v, want %v; output: %q", got, tc.wantTrace, out)
			}
			if got := strings.Contains(out, "debug record"); got != tc.wantDebug {
				t.Errorf("debug record captured = %v, want %v; output: %q", got, tc.wantDebug, out)
			}
			if got := strings.Contains(out, "info record"); got != tc.wantInfo {
				t.Errorf("info record captured = %v, want %v; output: %q", got, tc.wantInfo, out)
			}
			if got := strings.Contains(out, "warn record"); got != tc.wantWarn {
				t.Errorf("warn record captured = %v, want %v; output: %q", got, tc.wantWarn, out)
			}
			if got := strings.Contains(out, "error record"); got != tc.wantError {
				t.Errorf("error record captured = %v, want %v; output: %q", got, tc.wantError, out)
			}
		})
	}
}

// liveEnvFor builds a liveEnv with the given stdout sink and a logger on the
// given log sink (tests capture; io.Discard suppresses) and production
// behavior for everything else. Tests construct local envs — no shared state
// — so they can run in parallel.
func liveEnvFor(t *testing.T, stdout, logSink io.Writer) liveEnv {
	t.Helper()
	env := prodLiveEnv(slog.LevelDebug)
	env.stdout = stdout
	env.log = testLogger(logSink)
	return env
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

func waitForHTTP(t *testing.T, url string) bool {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func TestResolveToolPaths(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	dirAbs, scriptsAbs, err := resolveToolPaths(tmpDir, tmpDir)
	if err != nil {
		t.Fatalf("resolveToolPaths failed: %v", err)
	}
	if dirAbs != tmpDir || scriptsAbs != tmpDir {
		t.Errorf("resolveToolPaths = (%q, %q), want (%q, %q)", dirAbs, scriptsAbs, tmpDir, tmpDir)
	}

	if _, _, err := resolveToolPaths(tmpDir, filepath.Join(tmpDir, "no-such-scripts")); err == nil || !strings.Contains(err.Error(), "scripts path inaccessible") {
		t.Errorf("expected 'scripts path inaccessible' error, got %v", err)
	}
	if _, _, err := resolveToolPaths(filepath.Join(tmpDir, "no-such-dir"), tmpDir); err == nil || !strings.Contains(err.Error(), "dir path inaccessible") {
		t.Errorf("expected 'dir path inaccessible' error, got %v", err)
	}
}

// TestRunWatchSetupFailure (M4) verifies that a watcher creation failure in
// stdio mode surfaces as a process error (run returns it) within a bounded
// time, before the stdio loop would start.
func TestRunWatchSetupFailure(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	writeScript(t, filepath.Join(tmpDir, "alpha.sh"), "#!/bin/bash\necho alpha\n")

	var stderr captureWriter
	env := liveEnvFor(t, io.Discard, &stderr)
	env.newWatcher = func() (*fsnotify.Watcher, error) {
		return nil, errors.New("inotify unavailable")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- run(ctx, env, serverConfig{dir: tmpDir, scriptsDir: tmpDir, watch: true}) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("run returned nil for a watch setup failure")
		}
		if !strings.Contains(err.Error(), "inotify unavailable") {
			t.Fatalf("run error = %v, want the watcher setup failure", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not return for a watch setup failure")
	}
	// run() must not log the setup failure itself: main() is the single
	// reporting site (a log here plus main's would double the record).
	// The failure is still loud in production — main() logs every error
	// run() returns.
	if got := strings.Count(stderr.String(), "ERROR@"); got != 0 {
		t.Errorf("run() logged %d ERROR record(s) for the watch setup failure, want 0:\n%s", got, stderr.String())
	}
}

// TestRunHTTPBindFailure (M4) verifies that a port bind failure in HTTP mode
// returns a bounded error instead of idling forever behind the "Starting"
// banner: the serve outcome must be part of the run() select.
func TestRunHTTPBindFailure(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	writeScript(t, filepath.Join(tmpDir, "alpha.sh"), "#!/bin/bash\necho alpha\n")

	// Occupy the port so ListenAndServe fails.
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("blocker listen: %v", err)
	}
	defer blocker.Close()
	port := blocker.Addr().(*net.TCPAddr).Port

	var stderr captureWriter
	env := liveEnvFor(t, io.Discard, &stderr)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- run(ctx, env, serverConfig{dir: tmpDir, scriptsDir: tmpDir, host: "127.0.0.1", port: port})
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("run returned nil for a bind failure")
		}
		if !strings.Contains(err.Error(), "failed to start HTTP server") {
			t.Fatalf("run error = %v, want the bind failure", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not return for a bind failure (process would hang)")
	}
}

// TestRunWatchFatalMidRun (M4) verifies the run()-level fatal branch in both
// modes: a watcher whose error channel is already closed makes watchChanges
// return immediately, and run() must return the wrapped watch error within a
// bounded time (not serve a permanently stale snapshot, not hang).
func TestRunWatchFatalMidRun(t *testing.T) {
	// Not parallel: pins the process stdin to a pipe that never closes, so
	// the stdio serve side cannot EOF out before the fatal watch branch.
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	oldStdin := os.Stdin
	os.Stdin = inR
	defer func() {
		os.Stdin = oldStdin
		inR.Close()
		inW.Close()
	}()
	tmpDir := t.TempDir()
	writeScript(t, filepath.Join(tmpDir, "alpha.sh"), "#!/bin/bash\necho alpha\n")

	// A free port: the bind must succeed so the watch failure is what run()
	// reports (a busy port would race a bind failure into the same return).
	port := freePort(t)

	for name, port := range map[string]int{"stdio": 0, "http": port} {
		name, port := name, port
		t.Run(name, func(t *testing.T) {
			var stderr captureWriter
			env := liveEnvFor(t, io.Discard, &stderr)
			closed := make(chan error)
			close(closed)
			env.watcherErrors = func(*fsnotify.Watcher) <-chan error { return closed }

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cfg := serverConfig{dir: tmpDir, scriptsDir: tmpDir, watch: true}
			if port > 0 {
				cfg.host = "127.0.0.1"
				cfg.port = port
			}
			done := make(chan error, 1)
			go func() { done <- run(ctx, env, cfg) }()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("run returned nil for a fatal watch termination")
				}
				if !strings.Contains(err.Error(), "failed to watch scripts directory") {
					t.Fatalf("run error = %v, want the wrapped watch failure", err)
				}
				// run() must not log the watch failure itself: main() is the
				// single reporting site, so the captured stderr holds no
				// ERROR record from run() (it would have doubled the record).
				if got := strings.Count(stderr.String(), "ERROR@"); got != 0 {
					t.Fatalf("run() logged %d ERROR record(s) for the fatal watch termination, want 0:\n%s", got, stderr.String())
				}
				// The shutdown record must land with the true reason before
				// run() returns (the process exits right after).
				if got := stderr.String(); !strings.Contains(got, "shutting down") || !strings.Contains(got, "reason=watch-error") {
					t.Fatalf("run() log = %q, want the shutdown record with reason=watch-error", got)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("run did not return for a fatal watch termination")
			}
		})
	}
}

// TestRunStdioWatchServes: with --watch, the stdio server must serve
// concurrently with the watcher — before the run() restructure the server
// only started after the select, so a watched stdio session never answered.
// Not parallel: swaps the process stdin/stdout globals for pipes.
func TestRunStdioWatchServes(t *testing.T) {
	tmpDir := t.TempDir()
	writeScript(t, filepath.Join(tmpDir, "alpha.sh"), "#!/bin/bash\necho alpha\n")

	// Two pipes: client requests -> server stdin; server stdout -> client.
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer inR.Close()
	defer inW.Close()
	defer outR.Close()
	defer outW.Close()
	oldStdin, oldStdout := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	defer func() {
		os.Stdin, os.Stdout = oldStdin, oldStdout
	}()

	var stderr captureWriter
	env := liveEnvFor(t, io.Discard, &stderr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, env, serverConfig{dir: tmpDir, scriptsDir: tmpDir, watch: true})
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "stdio-e2e"}, &mcp.ClientOptions{
		Capabilities: &mcp.ClientCapabilities{},
	})
	session, err := client.Connect(ctx, &mcp.IOTransport{Reader: outR, Writer: inW}, nil)
	if err != nil {
		t.Fatalf("client connect over stdio pipe: %v", err)
	}
	defer session.Close()

	res, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools while watch is live: %v (the server must serve concurrently)", err)
	}
	if len(res.Tools) != 1 || res.Tools[0].Name != "alpha" {
		t.Fatalf("tools = %+v, want alpha", res.Tools)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v after cancel, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not stop after cancel")
	}
}

// TestRunStdioClientIdentity covers N1 on the stdio side end-to-end: the
// stdin tee (the SDK's transport API never exposes clientInfo to the app)
// must log the client name and version at INFO from the initialize request
// while the server keeps serving.
func TestRunStdioClientIdentity(t *testing.T) {
	// Not parallel: swaps the process stdin/stdout globals for pipes.
	tmpDir := t.TempDir()
	writeScript(t, filepath.Join(tmpDir, "alpha.sh"), "#!/bin/bash\necho alpha\n")

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer inR.Close()
	defer inW.Close()
	defer outR.Close()
	defer outW.Close()
	oldStdin, oldStdout := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	defer func() {
		os.Stdin, os.Stdout = oldStdin, oldStdout
	}()

	// captureWriter (mutex-guarded): the SDK's stdin reader goroutine emits
	// the identity record while run() logs from its own; a plain
	// strings.Builder would race under -race.
	var stderr captureWriter
	env := liveEnvFor(t, io.Discard, &stderr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, env, serverConfig{dir: tmpDir, scriptsDir: tmpDir, watch: true})
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "stdio-e2e", Version: "9.9.9"}, &mcp.ClientOptions{
		Capabilities: &mcp.ClientCapabilities{},
	})
	session, err := client.Connect(ctx, &mcp.IOTransport{Reader: outR, Writer: inW}, nil)
	if err != nil {
		t.Fatalf("client connect over stdio pipe: %v", err)
	}
	defer session.Close()

	if _, err := session.ListTools(ctx, nil); err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v after cancel, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not stop after cancel")
	}

	out := stderr.String()
	for _, want := range []string{"client connected", "transport=stdio", "clientName=stdio-e2e", "clientVersion=9.9.9"} {
		if !strings.Contains(out, want) {
			t.Errorf("stderr log = %q, want it to contain %s", out, want)
		}
	}
}

// TestStdinIdentityTee pins the tee contract: pure byte passthrough (no
// reordering, no buffering past the protocol's newline framing, including
// line fragments split across reads) and one identity record per initialize
// line — silent for every other line.
func TestStdinIdentityTee(t *testing.T) {
	t.Parallel()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer r.Close()
	defer w.Close()

	var logBuf bytes.Buffer
	tee := &stdinIdentityTee{in: r, log: slog.New(newLogHandler(&logBuf, levelTrace))}

	// An over-long line (far past maxIdentityLineBytes, and not a
	// handshake): it must produce no record and must not grow the
	// partial-line buffer without bound, while passing through verbatim.
	bigLine := strings.Repeat("a", maxIdentityLineBytes+5) + "\n"
	initLine := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"cursor","version":"1.2.3"}}}` + "\n"
	discLine := `{"jsonrpc":"2.0","id":3,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/clientInfo":{"name":"claude-code","version":"1.0.6"}}}}` + "\n"
	listLine := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n"
	// An over-long clientInfo name (past maxIdentityFieldBytes): the record
	// must be bounded and carry the truncation indicator.
	longNameLine := `{"jsonrpc":"2.0","id":4,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"` + strings.Repeat("n", maxIdentityFieldBytes+10) + `","version":"1"}}}` + "\n"

	// Write in fragments that split the lines mid-JSON to exercise the
	// partial-line buffer; the over-long line is split mid-line so the
	// cap triggers before its newline arrives.
	go func() {
		for _, chunk := range []string{bigLine[:10], bigLine[10 : maxIdentityLineBytes+3], bigLine[maxIdentityLineBytes+3:], initLine[:10], initLine[10:25], initLine[25:], discLine[:12], discLine[12:], longNameLine[:20], longNameLine[20:], listLine[:5], listLine[5:]} {
			if _, err := w.Write([]byte(chunk)); err != nil {
				return
			}
		}
		w.Close()
	}()

	got, err := io.ReadAll(tee)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(got) != len(bigLine)+len(initLine)+len(discLine)+len(longNameLine)+len(listLine) || string(got) != bigLine+initLine+discLine+longNameLine+listLine {
		t.Fatalf("passthrough = %d bytes, want %d (no reordering, no loss)", len(got), len(bigLine)+len(initLine)+len(discLine)+len(longNameLine)+len(listLine))
	}

	out := logBuf.String()
	if !strings.Contains(out, "client connected") || !strings.Contains(out, "transport=stdio") || !strings.Contains(out, "clientName=cursor") || !strings.Contains(out, "clientVersion=1.2.3") {
		t.Errorf("log = %q, want one identity record for the initialize line", out)
	}
	if !strings.Contains(out, "clientName=claude-code") || !strings.Contains(out, "clientVersion=1.0.6") {
		t.Errorf("log = %q, want one identity record for the server/discover line", out)
	}
	if !strings.Contains(out, "clientName="+strings.Repeat("n", maxIdentityFieldBytes)+"\u2026") {
		t.Errorf("log = %q, want the over-long name bounded with the truncation indicator", out)
	}
	if strings.Contains(out, strings.Repeat("n", maxIdentityFieldBytes+1)) {
		t.Errorf("log = %q, want the over-long name cut to the cap", out)
	}
	if n := strings.Count(out, "client connected"); n != 3 {
		t.Errorf("identity records = %d, want 3 (the over-long line and tools/list must not log); log: %q", n, out)
	}

	// Close is a no-op and safe to call twice.
	if err := tee.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := tee.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// TestStdinIdentityTeeSameReadOversizedLine verifies the cap is enforced
// even when the over-long line and its newline arrive in the same Read, and
// that inspection resumes on the next line in that same byte slice.
func TestStdinIdentityTeeSameReadOversizedLine(t *testing.T) {
	t.Parallel()
	var logBuf bytes.Buffer
	tee := &stdinIdentityTee{log: slog.New(newLogHandler(&logBuf, levelTrace))}
	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"cursor","version":"1.2.3"}}}`
	input := append([]byte(strings.Repeat("x", maxIdentityLineBytes+1)+"\n"), []byte(initialize+"\n")...)

	tee.inspect(input)

	if len(tee.partial) > maxIdentityLineBytes {
		t.Errorf("partial buffer length = %d, exceeds cap %d", len(tee.partial), maxIdentityLineBytes)
	}
	if tee.discard {
		t.Error("discard remains set after the oversized line's newline")
	}
	out := logBuf.String()
	if strings.Count(out, "client connected") != 1 || !strings.Contains(out, "clientName=cursor") {
		t.Errorf("log = %q, want only the valid initialize identity", out)
	}
}

// TestRunLifecycleLogs pins N9/N10: a signal produces a "shutting down"
// INFO and a clean serve exit a "server stopped" INFO with the transport,
// the bound port, and the run duration.
func TestRunLifecycleLogs(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "ok.sh")
	writeScript(t, path, "#!/bin/bash\necho ok\n")
	port := freePort(t)

	// captureWriter (mutex-guarded): run() writes from its goroutines;
	// the read below happens after run() returns, but the locked writer
	// keeps the capture race-free under -race as well.
	logBuf := &captureWriter{}
	var testCancel context.CancelFunc
	env := liveEnv{
		stdout:           io.Discard,
		log:              slog.New(newLogHandler(logBuf, levelTrace)),
		resolveWrapWidth: prodResolveWrapWidth,
		clearScreen:      prodClearScreen,
		notifySignals: func(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
			cctx, c := context.WithCancel(ctx)
			testCancel = c
			return cctx, c
		},
		newWatcher:    prodNewWatcher,
		watcherErrors: prodWatcherErrors,
	}

	done := make(chan error, 1)
	go func() {
		done <- run(t.Context(), env, serverConfig{dir: tmpDir, scriptsDir: tmpDir, host: "127.0.0.1", port: port})
	}()

	if !waitForHTTP(t, fmt.Sprintf("http://127.0.0.1:%d", port)) {
		t.Fatal("server did not come up")
	}
	testCancel()
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}

	out := logBuf.String()
	for _, want := range []string{
		"shutting down",
		"reason=signal",
		"server stopped",
		"mode=http",
		fmt.Sprintf("port=%d", port),
		"duration=",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log = %q, want it to contain %s", out, want)
		}
	}
}

// TestRunStdioLifecycleLogs pins the stdio side of the N9/N10 lifecycle pair:
// a signal produces BOTH records, in order.
func TestRunStdioLifecycleLogs(t *testing.T) {
	tmpDir := t.TempDir()
	writeScript(t, filepath.Join(tmpDir, "ok.sh"), "#!/bin/bash\necho ok\n")

	// Not parallel: swaps the process stdin/stdout globals for pipes.
	// stdin is the READ end (no client writes frames in this test); stdout
	// is a drained pipe.
	oldStdin, oldStdout := os.Stdin, os.Stdout
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	os.Stdin, os.Stdout = stdinR, outW
	defer func() {
		os.Stdin, os.Stdout = oldStdin, oldStdout
		stdinR.Close()
		stdinW.Close()
		outR.Close()
		outW.Close()
	}()
	go func() { _, _ = io.Copy(io.Discard, outR) }()

	// captureWriter (mutex-guarded): run() writes from its goroutines while
	// the polling loop below calls String() concurrently — a plain
	// bytes.Buffer would race.
	logBuf := &captureWriter{}
	var testCancel context.CancelFunc
	env := liveEnv{
		stdout:           io.Discard,
		log:              slog.New(newLogHandler(logBuf, levelTrace)),
		resolveWrapWidth: prodResolveWrapWidth,
		clearScreen:      prodClearScreen,
		notifySignals: func(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
			cctx, c := context.WithCancel(ctx)
			testCancel = c
			return cctx, c
		},
		newWatcher:    prodNewWatcher,
		watcherErrors: prodWatcherErrors,
	}

	done := make(chan error, 1)
	go func() {
		done <- run(t.Context(), env, serverConfig{dir: tmpDir, scriptsDir: tmpDir})
	}()

	// The banner proves the serve goroutine is running before the "signal".
	buf := logBuf.String()
	for !strings.Contains(buf, "Starting stdio server") {
		time.Sleep(10 * time.Millisecond)
		buf = logBuf.String()
	}
	testCancel()
	// No stdin close: the cancelled context ends the session (the SDK's
	// conn close unblocks the read); a close here would race the read and
	// surface as a spurious EBADF.
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}

	out := logBuf.String()
	for _, want := range []string{"shutting down", "reason=signal", "server stopped", `mode=stdio`} {
		if !strings.Contains(out, want) {
			t.Errorf("log = %q, want it to contain %s", out, want)
		}
	}
	if i, j := strings.Index(out, "shutting down"), strings.Index(out, "server stopped"); i < 0 || j < 0 || i > j {
		t.Errorf("want \"shutting down\" before \"server stopped\", got:\n%s", out)
	}
}

// TestRunStdioShutdownRecordArmRace pins the stdio shutdown record against
// the select arm race: a fired signal cancels the signal context and the
// watcher (whose context is that same context) at the same moment, so
// sigCtx.Done(), watchDone, and serveDone can all be ready together and the
// select may pick any arm. The "shutting down" record must land exactly once
// no matter which arm wins.
// Not parallel: swaps the process stdin/stdout globals for pipes.
func TestRunStdioShutdownRecordArmRace(t *testing.T) {
	tmpDir := t.TempDir()
	writeScript(t, filepath.Join(tmpDir, "ok.sh"), "#!/bin/bash\necho ok\n")

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer inR.Close()
	defer inW.Close()
	defer outR.Close()
	defer outW.Close()
	oldStdin, oldStdout := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	defer func() {
		os.Stdin, os.Stdout = oldStdin, oldStdout
	}()
	go func() { _, _ = io.Copy(io.Discard, outR) }()

	logBuf := &captureWriter{}
	env := liveEnv{
		stdout:           io.Discard,
		log:              slog.New(newLogHandler(logBuf, levelTrace)),
		resolveWrapWidth: prodResolveWrapWidth,
		clearScreen:      prodClearScreen,
		// Cancel immediately: sigCtx.Done() is ready from t0, so the
		// watcher's clean stop and the serve exit race it in the select.
		notifySignals: func(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
			cctx, c := context.WithCancel(ctx)
			c()
			return cctx, c
		},
		newWatcher:    prodNewWatcher,
		watcherErrors: prodWatcherErrors,
	}

	done := make(chan error, 1)
	go func() { done <- run(t.Context(), env, serverConfig{dir: tmpDir, scriptsDir: tmpDir, watch: true}) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return")
	}

	out := logBuf.String()
	if n := strings.Count(out, "shutting down"); n != 1 {
		t.Fatalf("shutdown records = %d, want exactly 1 (whichever select arm wins);\n%s", n, out)
	}
	if !strings.Contains(out, "reason=signal") {
		t.Errorf("log = %q, want reason=signal (the cancellation came from the signal)", out)
	}
	if !strings.Contains(out, "server stopped") {
		t.Errorf("log = %q, want the server stopped record", out)
	}
}

// TestRunWatchErrorShutdownReason pins the shutdown record's reason on a
// non-signal exit: a fatal watch error must not log reason=signal.
func TestRunWatchErrorShutdownReason(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	writeScript(t, filepath.Join(tmpDir, "ok.sh"), "#!/bin/bash\necho ok\n")
	port := freePort(t)

	var logBuf bytes.Buffer
	env := liveEnv{
		stdout:           io.Discard,
		log:              slog.New(newLogHandler(&logBuf, levelTrace)),
		resolveWrapWidth: prodResolveWrapWidth,
		clearScreen:      prodClearScreen,
		notifySignals: func(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
			return context.WithCancel(ctx)
		},
		newWatcher:    func() (*fsnotify.Watcher, error) { return nil, errors.New("watcher exploded") },
		watcherErrors: prodWatcherErrors,
	}

	done := make(chan error, 1)
	go func() {
		done <- run(t.Context(), env, serverConfig{dir: tmpDir, scriptsDir: tmpDir, host: "127.0.0.1", port: port, watch: true})
	}()

	// No waitForHTTP: a watcher that fails at setup ends run() within
	// milliseconds — the HTTP server may never bind. run() must simply
	// return the watch error.
	err := <-done
	if err == nil {
		t.Fatal("run succeeded, want the watch error")
	}
	if !strings.Contains(err.Error(), "watcher exploded") {
		t.Fatalf("run error = %q, want the watch failure", err)
	}

	out := logBuf.String()
	if !strings.Contains(out, "shutting down") {
		t.Errorf("log = %q, want a shutdown record", out)
	}
	if !strings.Contains(out, "reason=watch-error") {
		t.Errorf("log = %q, want reason=watch-error", out)
	}
	if strings.Contains(out, "reason=signal") {
		t.Errorf("log = %q, must NOT claim a signal fired", out)
	}
}
