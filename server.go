package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/term"
)

const serverName = "mcp-commands"

// prodClearScreen clears the terminal (ANSI erase-screen + cursor-home). It
// is a no-op when stdout is not a TTY, so piped output simply accumulates.
func prodClearScreen(stdout io.Writer) {
	file, ok := stdout.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return
	}
	_, _ = stdout.Write([]byte("\x1b[2J\x1b[H"))
}

// prodNotifySignals wraps signal.NotifyContext (SIGINT/SIGTERM cancel the
// context).
func prodNotifySignals(ctx context.Context, sig ...os.Signal) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(ctx, sig...)
}

// liveEnv bundles the live system dependencies of the CLI front end: the
// standard streams, signal handling, and the TTY-dependent behaviors. main
// constructs one via prodLiveEnv; tests construct local fakes and pass them
// in — no package-level mutable state, so every test can t.Parallel().
type liveEnv struct {
	stdout           io.Writer
	stderr           io.Writer
	resolveWrapWidth func(stdout io.Writer) int
	clearScreen      func(stdout io.Writer)
	notifySignals    func(ctx context.Context, sig ...os.Signal) (context.Context, context.CancelFunc)
	newWatcher       func() (*fsnotify.Watcher, error)
	watcherErrors    func(w *fsnotify.Watcher) <-chan error
}

func prodLiveEnv() liveEnv {
	return liveEnv{
		stdout:           os.Stdout,
		stderr:           os.Stderr,
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

	tools, err := discoverTools(scriptsAbs, env.stderr)
	if err != nil {
		return fmt.Errorf("failed to discover tools: %w", err)
	}

	if len(tools) == 0 {
		fmt.Fprintf(env.stderr, "Warning: No executable scripts found in %s\n", scriptsAbs)
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
		if warning, err := checkHTTPSecurityPolicy(cfg.host, cfg.apiKey.Token, cfg.insecureNoAuth); err != nil {
			return err
		} else if warning != "" {
			fmt.Fprintln(env.stderr, warning)
		}

		handler := buildHTTPHandler(server, cfg.apiKey.Token, cfg.cors)
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
		line := fmt.Sprintf("Starting HTTP server on %s", addr)
		if len(notes) > 0 {
			line += " (" + strings.Join(notes, ", ") + ")"
		}
		fmt.Fprintf(env.stderr, "%s\n", line)
		var serveErr error
		select {
		case <-sigCtx.Done():
			// Signal: drain the serve outcome; a non-closed error (e.g. a
			// bind failure racing the shutdown) is reported below.
		case err := <-watchDone:
			if err != nil && !errors.Is(err, context.Canceled) {
				fmt.Fprintf(env.stderr, "Error: %v\n", err)
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

	fmt.Fprintf(env.stderr, "Starting stdio server\n")
	select {
	case <-sigCtx.Done():
	case err := <-watchDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(env.stderr, "Error: %v\n", err)
			cancel()
			return fmt.Errorf("failed to watch scripts directory: %w", err)
		}
	}
	return server.Run(sigCtx, &mcp.StdioTransport{})
}
