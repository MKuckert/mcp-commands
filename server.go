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
	watcherErrors    func(w *fsnotify.Watcher) <-chan error
}

func prodLiveEnv() liveEnv {
	return liveEnv{
		stdout:           os.Stdout,
		stderr:           os.Stderr,
		resolveWrapWidth: prodResolveWrapWidth,
		clearScreen:      prodClearScreen,
		notifySignals:    prodNotifySignals,
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

	if cfg.watch {
		go func() {
			if err := watchTools(sigCtx, env, scriptsAbs, registry, tools); err != nil && !errors.Is(err, context.Canceled) {
				fmt.Fprintf(env.stderr, "Warning: watch loop stopped: %v\n", err)
			}
		}()
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

		var notes []string
		if cfg.apiKey.Token != "" {
			notes = append(notes, "API key auth enabled")
		} else if !isLoopbackHost(cfg.host) {
			notes = append(notes, "UNAUTHENTICATED")
		}
		serve := serverHTTP.ListenAndServe
		if cfg.tlsCert != "" {
			serve = func() error { return serverHTTP.ListenAndServeTLS(cfg.tlsCert, cfg.tlsKey) }
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
		if err := serve(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("failed to start HTTP server: %w", err)
		}
		return nil
	}

	fmt.Fprintf(env.stderr, "Starting stdio server\n")
	return server.Run(sigCtx, &mcp.StdioTransport{})
}
