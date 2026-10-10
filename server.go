package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const serverName = "mcp-commands"

// prodNotifySignals wraps signal.NotifyContext (SIGINT/SIGTERM cancel the
// context).
func prodNotifySignals(ctx context.Context, sig ...os.Signal) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(ctx, sig...)
}

// liveEnv bundles the live system dependencies of the CLI front end: the
// standard streams, the logger, signal handling, and the TTY-dependent
// behaviors. main constructs one via prodLiveEnv; tests construct local
// fakes and pass them in — no package-level mutable state, so every test
// can t.Parallel().
type liveEnv struct {
	stdout           io.Writer
	log              *slog.Logger
	resolveWrapWidth func(stdout io.Writer) int
	clearScreen      func(stdout io.Writer)
	notifySignals    func(ctx context.Context, sig ...os.Signal) (context.Context, context.CancelFunc)
	newWatcher       func() (*fsnotify.Watcher, error)
	watcherErrors    func(w *fsnotify.Watcher) <-chan error
}

// prodLiveEnv builds the live environment. Every log record goes to stderr
// at the configured minimum level: stdout is reserved for program output
// (the MCP protocol in stdio mode, tool lists, call results, help).
func prodLiveEnv(level slog.Level) liveEnv {
	return liveEnv{
		stdout:           os.Stdout,
		log:              slog.New(newLogHandler(os.Stderr, level)),
		resolveWrapWidth: prodResolveWrapWidth,
		clearScreen:      prodClearScreen,
		notifySignals:    prodNotifySignals,
		newWatcher:       prodNewWatcher,
		watcherErrors:    prodWatcherErrors,
	}
}

func run(ctx context.Context, env liveEnv, cfg serverConfig) error {
	sigCtx, cancel := env.notifySignals(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	started := time.Now()

	dirAbs, scriptsAbs, err := resolveToolPaths(cfg.dir, cfg.scriptsDir)
	if err != nil {
		return err
	}

	tools, err := discoverTools(scriptsAbs, env.log)
	if err != nil {
		return fmt.Errorf("failed to discover tools: %w", err)
	}

	if len(tools) == 0 {
		env.log.Warn("No executable scripts found", "scriptsDir", scriptsAbs)
	}

	impl := &mcp.Implementation{
		Name:    serverName,
		Version: serverVersion,
	}
	// The SDK's internal logger is intentionally nil (resolves to a discard
	// handler): the SDK logs `server connecting` at INFO on *every* connect,
	// and stateless HTTP mode connects per request, so a wired-up SDK logger
	// would flood the operator log with per-request noise the app's own
	// records already cover.
	server := mcp.NewServer(impl, nil)
	registry := newToolRegistry(server, dirAbs, cfg.timeout, cfg.maxConcurrent, env.log)
	registry.replace(tools)

	// --watch is an explicit request: a setup failure, or a fatal mid-run
	// watch termination (channel closure), ends the process with an error
	// rather than serving a permanently stale tool snapshot. Only signal
	// cancellation exits cleanly.
	var watchDone <-chan error
	if cfg.watch {
		watchCh := make(chan error, 1)
		go func() {
			watchCh <- watchTools(sigCtx, env, scriptsAbs, registry, tools)
		}()
		watchDone = watchCh
	}

	if cfg.port > 0 {
		addr := fmt.Sprintf("%s:%d", cfg.host, cfg.port)

		// Fail fast before binding: no silent unauthenticated remote shells.
		// The policy check inspects the full posture (bind × auth × CORS × TLS)
		// and warns loudly about dangerous-but-explicit combinations.
		warnings, err := checkHTTPSecurityPolicy(cfg.host, cfg.apiKey.Token, cfg.cors, cfg.tlsCert != "", cfg.insecureNoAuth)
		if err != nil {
			return err
		}
		for _, w := range warnings {
			env.log.Warn(w)
		}

		handler := buildHTTPHandler(server, env.log, cfg.apiKey.Token, cfg.cors)
		stopReason := make(chan string, 1)
		// Ack from the shutdown goroutine: closed after the shutdown record
		// and Shutdown, so run() can wait for the record to land before
		// returning — the process must not exit first.
		shutdownDone := make(chan struct{})
		serverHTTP := &http.Server{
			Addr:    addr,
			Handler: handler,
			// No Read/WriteTimeout: they would have to exceed the 5-minute
			// tool-call budget. These two guard against slowloris and
			// pinned idle keep-alive connections.
			ReadHeaderTimeout: httpReadHeaderTimeout,
			IdleTimeout:       httpIdleTimeout,
		}

		go func() {
			<-sigCtx.Done()
			// The goroutine fires on ANY cancellation of sigCtx — a signal, or
			// a cancel() from the watch-error and bind-failure paths below — so
			// non-signal exits queue their reason first (buffered, so it is
			// already there by the time cancel() runs) and the record carries
			// the truth, never a fake reason=signal.
			// (No signal name: liveEnv.notifySignals wraps signal.NotifyContext,
			// which does not expose which signal fired.)
			reason := "signal"
			select {
			case r := <-stopReason:
				reason = r
			default:
			}
			env.log.Info("shutting down", "reason", reason)
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer shutdownCancel()
			_ = serverHTTP.Shutdown(shutdownCtx)
			close(shutdownDone)
		}()

		serve := serverHTTP.ListenAndServe
		if cfg.tlsCert != "" {
			serve = func() error { return serverHTTP.ListenAndServeTLS(cfg.tlsCert, cfg.tlsKey) }
		}

		serveDone := make(chan error, 1)
		go func() {
			serveDone <- serve()
		}()

		var notes []string
		if cfg.apiKey.Token != "" {
			notes = append(notes, "API key auth enabled")
		} else if !isLoopbackHost(cfg.host) {
			notes = append(notes, "UNAUTHENTICATED")
		}
		if cfg.tlsCert != "" {
			notes = append(notes, "TLS")
		}
		if cfg.cors.enabled() {
			notes = append(notes, cfg.cors.summary())
		}
		args := []any{"addr", addr}
		if len(notes) > 0 {
			args = append(args, "notes", strings.Join(notes, ", "))
		}
		env.log.Info("Starting HTTP server", args...)
		var serveErr error
		select {
		case <-sigCtx.Done():
			// Signal: drain the serve outcome; a non-closed error (e.g. a
			// bind failure racing the shutdown) is reported below.
		case err := <-watchDone:
			if err != nil && !errors.Is(err, context.Canceled) {
				// Not logged here: run() returns it and main() is the single
				// reporting site (a log here would double the record). The
				// shutdown record must not claim a signal fired.
				stopReason <- "watch-error"
				cancel()
				<-shutdownDone
				<-serveDone
				return fmt.Errorf("failed to watch scripts directory: %w", err)
			}
			// A clean watch stop (signal) falls through: the serve outcome
			// must still be reported.
		case err := <-serveDone:
			// A bind/startup failure (e.g. the port is taken) must surface
			// immediately: without this case the process would idle forever
			// behind a "Starting" banner with no listener.
			serveErr = err
		}
		// A bind/startup failure cancelled the run without a signal: tell the
		// shutdown record so.
		if serveErr != nil {
			stopReason <- "serve-failure"
		}
		cancel()
		// Settle the shutdown record before proceeding: without this wait,
		// a serve-failure return could race the goroutine and the process
		// would exit before the "shutting down" line landed in the log.
		<-shutdownDone
		if serveErr == nil {
			serveErr = <-serveDone
		}
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return fmt.Errorf("failed to start HTTP server: %w", serveErr)
		}
		env.log.Info("server stopped", "mode", "http", "port", cfg.port, "duration", time.Since(started).Round(time.Millisecond))
		return nil
	}

	env.log.Info("Starting stdio server")
	serveDone := make(chan error, 1)
	go func() {
		// Stdio: client name and version come from the handshake request
		// (initialize, or server/discover for the 2026-07-28 protocol), which
		// the SDK's transport API never exposes to the app; the stdin tee
		// captures it (the HTTP side uses newClientIdentityPeekHandler).
		serveDone <- server.Run(sigCtx, &mcp.IOTransport{
			Reader: newStdinIdentityTee(env.log),
			Writer: nopStdoutCloser{Writer: os.Stdout},
		})
	}()
	// One record per clean stop; both exit paths (client disconnect and
	// signal) funnel through it.
	stopped := func() {
		env.log.Info("server stopped", "mode", "stdio", "duration", time.Since(started).Round(time.Millisecond))
	}
	select {
	case <-sigCtx.Done():
		env.log.Info("shutting down", "reason", "signal")
	case err := <-watchDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			// A fatal watch error ends the process: write the shutdown record
			// synchronously from run() (no goroutine to race the exit) and
			// return; main() is the single reporting site for the error itself.
			env.log.Info("shutting down", "reason", "watch-error")
			cancel()
			return fmt.Errorf("failed to watch scripts directory: %w", err)
		}
		// A clean watch stop (the watcher's context is the signal context, so
		// a Canceled result means a signal fired) can win the select over
		// sigCtx.Done(): emit the record here so it is never skipped.
		if sigCtx.Err() != nil {
			env.log.Info("shutting down", "reason", "signal")
		}
		// Falls through: the serve outcome must still be reported.
	case serveErr := <-serveDone:
		// The client side ended the session (e.g. stdin closed). If a
		// signal fired at the same moment (sigCtx done), record it — the
		// arm that wins the select must not swallow the record.
		if sigCtx.Err() != nil {
			env.log.Info("shutting down", "reason", "signal")
		}
		// Stop the watcher and surface the serve result.
		cancel()
		if serveErr != nil && !errors.Is(serveErr, context.Canceled) {
			return fmt.Errorf("stdio server stopped: %w", serveErr)
		}
		stopped()
		return nil
	}
	// The session (and the watcher) is now done: serve.Run returns once the
	// cancelled session closes.
	if serveErr := <-serveDone; serveErr != nil && !errors.Is(serveErr, context.Canceled) {
		return fmt.Errorf("stdio server stopped: %w", serveErr)
	}
	stopped()
	return nil
}

// maxIdentityLineBytes caps the stdin tee's partial-line buffer. A
// handshake frame (initialize / server/discover) is a few hundred bytes at
// most; a line longer than this is malformed input, and the bytes up to its
// newline are discarded so a pathological frame cannot grow the buffer
// without bound. The passthrough is unaffected: inspect only ever reads a
// copy of the bytes, never the protocol stream.
const maxIdentityLineBytes = 64 << 10

// stdinIdentityTee is a pure byte passthrough over os.Stdin that inspects each
// complete newline-delimited line for a handshake request (initialize, or
// server/discover for the 2026-07-28 protocol) and logs the MCP client
// identity at INFO (N1, stdio side). It buffers at most the current line —
// the protocol's own newline framing, capped at maxIdentityLineBytes — and the
// inspection is synchronous (a single slog Write per line), so the read path
// is never blocked on anything external. Close is a no-op: os.Stdin must not
// be closed from here.
type stdinIdentityTee struct {
	in      io.Reader
	partial []byte
	discard bool // the current line exceeded the cap; drop bytes until its newline
	log     *slog.Logger
}

// nopStdoutCloser is an io.WriteCloser whose Close is a no-op — the same
// wrapper the SDK's StdioTransport uses for os.Stdout. The process owns the
// descriptor: a session end must not close it, which would turn every later
// write to fd 1 (test output, coverage reports) into "file already closed".
type nopStdoutCloser struct {
	io.Writer
}

func (nopStdoutCloser) Close() error { return nil }

func newStdinIdentityTee(log *slog.Logger) *stdinIdentityTee {
	return &stdinIdentityTee{in: os.Stdin, log: log}
}

// Read implements io.Reader: every byte is passed through verbatim.
func (t *stdinIdentityTee) Read(p []byte) (int, error) {
	n, err := t.in.Read(p)
	if n > 0 {
		t.inspect(p[:n])
	}
	return n, err
}

// Close implements io.Closer as a no-op (the process owns os.Stdin).
func (t *stdinIdentityTee) Close() error { return nil }

// inspect appends p to the partial-line buffer and, for every complete line
// it yields, logs the client identity of a handshake request. It checks the
// cap before appending each segment, including when an oversized line and its
// newline arrive in the same Read.
func (t *stdinIdentityTee) inspect(p []byte) {
	for len(p) > 0 {
		if t.discard {
			// An over-long line is being dropped: the bytes already passed
			// through untouched; look only for the newline that ends it.
			i := bytes.IndexByte(p, '\n')
			if i < 0 {
				return
			}
			t.discard = false
			t.partial = t.partial[:0]
			p = p[i+1:]
			continue
		}

		newline := bytes.IndexByte(p, '\n')
		segment := p
		if newline >= 0 {
			segment = p[:newline]
		}
		if len(t.partial)+len(segment) > maxIdentityLineBytes {
			// The current line is too long, whether or not its newline
			// arrived in this Read. Discard it without ever appending the
			// oversized segment to partial.
			t.partial = t.partial[:0]
			if newline < 0 {
				t.discard = true
				return
			}
			p = p[newline+1:]
			continue
		}

		t.partial = append(t.partial, segment...)
		if newline < 0 {
			return
		}
		if name, version, ok := clientInfoFromMessage(bytes.TrimRight(t.partial, "\r")); ok {
			t.log.Info("client connected", "transport", "stdio", "clientName", name, "clientVersion", version)
		}
		t.partial = t.partial[:0]
		p = p[newline+1:]
	}
}
