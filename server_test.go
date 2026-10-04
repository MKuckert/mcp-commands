package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
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
			if strings.Contains(buf.String(), "No executable scripts found") {
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
		return strings.Contains(buf.String(), "(TLS)")
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

// liveEnvFor builds a liveEnv with the given stdout/stderr sinks (tests
// capture) and production behavior for everything else. Tests construct
// local envs — no shared state — so they can run in parallel.
func liveEnvFor(t *testing.T, stdout, stderr io.Writer) liveEnv {
	t.Helper()
	env := prodLiveEnv()
	env.stdout = stdout
	env.stderr = stderr
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
