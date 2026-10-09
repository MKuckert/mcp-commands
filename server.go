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
	server := mcp.NewServer(impl, nil)
	registry := newToolRegistry(server, dirAbs, cfg.timeout, cfg.maxConcurrent)
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
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer shutdownCancel()
			_ = serverHTTP.Shutdown(shutdownCtx)
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
				// reporting site (a log here would double the record).
				cancel()
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
		cancel()
		if serveErr == nil {
			serveErr = <-serveDone
		}
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return fmt.Errorf("failed to start HTTP server: %w", serveErr)
		}
		return nil
	}

	env.log.Info("Starting stdio server")
	serveDone := make(chan error, 1)
	go func() {
		// Stdio: client name and version come from the initialize request, which
		// the SDK's transport API never exposes to the app; the stdin tee
		// captures it (the HTTP side uses newClientIdentityPeekHandler).
		serveDone <- server.Run(sigCtx, &mcp.IOTransport{
			Reader: newStdinIdentityTee(env.log),
			Writer: os.Stdout,
		})
	}()
	select {
	case <-sigCtx.Done():
	case err := <-watchDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			// Not logged here: run() returns it and main() is the single
			// reporting site (a log here would double the record).
			cancel()
			return fmt.Errorf("failed to watch scripts directory: %w", err)
		}
	case serveErr := <-serveDone:
		// The client side ended the session (e.g. stdin closed): stop the
		// watcher and surface the serve result.
		cancel()
		if serveErr != nil && !errors.Is(serveErr, context.Canceled) {
			return fmt.Errorf("stdio server stopped: %w", serveErr)
		}
		return nil
	}
	// The session (and the watcher) is now done: serve.Run returns once the
	// cancelled session closes.
	if serveErr := <-serveDone; serveErr != nil && !errors.Is(serveErr, context.Canceled) {
		return fmt.Errorf("stdio server stopped: %w", serveErr)
	}
	return nil
}

// stdinIdentityTee is a pure byte passthrough over os.Stdin that inspects each
// complete newline-delimited line for an initialize request and logs the MCP
// client identity at INFO (N1, stdio side). It buffers at most the current
// line — the protocol's own newline framing — and the inspection is
// synchronous (a single slog Write per line), so the read path is never
// blocked on anything external. Close is a no-op: os.Stdin must not be closed
// from here.
type stdinIdentityTee struct {
	in      io.Reader
	partial []byte
	log     *slog.Logger
}

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
// it yields, logs the client identity of an initialize request.
func (t *stdinIdentityTee) inspect(p []byte) {
	t.partial = append(t.partial, p...)
	for {
		i := bytes.IndexByte(t.partial, '\n')
		if i < 0 {
			return
		}
		line := t.partial[:i]
		t.partial = t.partial[i+1:]
		if name, version, ok := clientInfoFromMessage(bytes.TrimRight(line, "\r")); ok {
			t.log.Info("client connected", "clientName", name, "clientVersion", version)
		}
	}
}
