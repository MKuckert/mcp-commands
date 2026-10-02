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

// serverModeFlagNames lists the flags that configure the MCP server, in a
// stable order for the diagnostic-mode ignored-flags notice. (--watch is
// honored in --list-tools mode; with --call-tool it is added to the notice
// separately.)
var serverModeFlagNames = []string{"host", "port", "api-key", "api-key-file", "allowed-origins", "allow-all-origins", "disable-localhost-protection", "insecure-no-auth", "max-concurrent"}

// runDiagnostic runs a diagnostic mode (--list-tools or --call-tool) and
// returns the process exit code. Diagnostics never start the MCP server:
// the process exits after the diagnostic completes, except the live
// --list-tools --watch mode, which runs until SIGINT/SIGTERM. Result content
// goes to stdout; warnings and operational errors go to stderr.
func runDiagnostic(env liveEnv, diag diagnostic) int {
	if len(diag.ignoredFlags) > 0 {
		fmt.Fprintf(env.stderr, "Note: ignoring server-mode flags in diagnostic mode: %s\n", strings.Join(diag.ignoredFlags, ", "))
	}
	if diag.listTools {
		return runListTools(env, diag.dir, diag.scriptsDir, diag.watch, diag.timeout)
	}
	if diag.callTool == "" {
		// Only reachable when --call-tool= was explicitly passed (an
		// omitted flag is handled by main and never reaches here).
		fmt.Fprintln(env.stderr, "Error: --call-tool requires a non-empty tool name")
		return 1
	}
	dirAbs, scriptsAbs, err := resolveToolPaths(diag.dir, diag.scriptsDir)
	if err != nil {
		fmt.Fprintf(env.stderr, "Error: %v\n", err)
		return 1
	}
	code, err := runCallTool(env, scriptsAbs, dirAbs, diag.timeout, diag.callTool, diag.params)
	if err != nil {
		fmt.Fprintf(env.stderr, "Error: %v\n", err)
	}
	return code
}

// runCallTool is the --call-tool diagnostic: run a single discovered tool
// through the same execution path as the MCP handler (required-param
// validation, JSON→CLI-arg translation, timeout resolution identical to the
// registry — a per-tool Timeout: wins, Timeout: NONE ⇒ no deadline) and
// print the result text to the given stdout writer (the dispatch passes
// os.Stdout; tests pass a buffer). It returns the process exit code: 0 on
// success; 1 on any failure. Execution failures (missing required param,
// non-zero script exit, timeout) print the tool's result content to stdout
// with a nil error; operational failures (discovery, unknown tool, --params
// parse, unstartable script) yield a non-nil error for the stderr "Error:"
// line and never start the script. A non-object --params is rejected by
// parseToolArguments, which maps an explicitly empty value and JSON null to
// {} (same leniency as the MCP handler).
func runCallTool(env liveEnv, scriptsAbs, dirAbs string, globalTimeout time.Duration, name, paramsRaw string) (int, error) {
	tools, err := discoverTools(scriptsAbs, env.stderr)
	if err != nil {
		return 1, fmt.Errorf("failed to discover tools: %w", err)
	}

	var tool discoveredTool
	found := false
	for _, candidate := range tools {
		if candidate.Name == name {
			tool = candidate
			found = true
			break
		}
	}
	if !found {
		msg := fmt.Sprintf("unknown tool %q", name)
		if len(tools) > 0 {
			names := make([]string, len(tools))
			for i, candidate := range tools {
				names[i] = candidate.Name
			}
			msg += "; available tools: " + strings.Join(names, ", ")
		}
		return 1, errors.New(msg)
	}

	args, err := parseToolArguments([]byte(paramsRaw))
	if err != nil {
		return 1, fmt.Errorf("invalid --params %q: %w", paramsRaw, err)
	}

	if err := validateRequiredParams(args, tool.Params); err != nil {
		fmt.Fprintln(env.stdout, err.Error())
		return 1, nil
	}

	result, err := executeTool(context.Background(), tool.Path, args, resolveToolTimeout(tool, globalTimeout), dirAbs)
	if err != nil {
		return 1, fmt.Errorf("failed to run tool %q: %w", name, err)
	}

	var b strings.Builder
	for _, content := range result.Content {
		text, ok := content.(*mcp.TextContent)
		if !ok {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(text.Text)
	}
	fmt.Fprintln(env.stdout, b.String())

	if result.IsError {
		return 1, nil
	}
	return 0, nil
}

// runListTools is the --list-tools diagnostic: discover and print the tool
// list (renderToolList, width re-queried at every print) to stdout, then
// exit 0. With watch it becomes a live list: after the initial print, every
// debounced change to the scripts directory clears the screen (TTY only) and
// re-prints the full list with the existing per-scan stderr warnings, until
// the process is signaled. Path resolution errors are a startup failure
// (stderr, exit 1).
func runListTools(env liveEnv, dir, scriptsDir string, watch bool, timeout time.Duration) int {
	_, scriptsAbs, err := resolveToolPaths(dir, scriptsDir)
	if err != nil {
		fmt.Fprintf(env.stderr, "Error: %v\n", err)
		return 1
	}

	printList := func(tools []discoveredTool) {
		if len(tools) == 0 {
			fmt.Fprintf(env.stderr, "Warning: No executable scripts found in %s\n", scriptsAbs)
		}
		fmt.Fprint(env.stdout, renderToolList(tools, timeout, env.resolveWrapWidth(env.stdout)))
	}

	tools, err := discoverTools(scriptsAbs, env.stderr)
	if err != nil {
		fmt.Fprintf(env.stderr, "Error: %v\n", err)
		return 1
	}
	printList(tools)

	if !watch {
		return 0
	}

	sigCtx, cancel := env.notifySignals(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := watchChanges(sigCtx, env, scriptsAbs, func() {
		env.clearScreen(env.stdout)
		tools, err := discoverTools(scriptsAbs, env.stderr)
		if err != nil {
			fmt.Fprintf(env.stderr, "Warning: failed to rediscover tools: %v\n", err)
			return
		}
		printList(tools)
	}); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(env.stderr, "Warning: watch loop stopped: %v\n", err)
	}
	return 0
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
		if cfg.cors.enabled() {
			notes = append(notes, cfg.cors.summary())
		}
		line := fmt.Sprintf("Starting HTTP server on %s", addr)
		if len(notes) > 0 {
			line += " (" + strings.Join(notes, ", ") + ")"
		}
		fmt.Fprintf(env.stderr, "%s\n", line)
		if err := serverHTTP.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("failed to start HTTP server: %w", err)
		}
		return nil
	}

	fmt.Fprintf(env.stderr, "Starting stdio server\n")
	return server.Run(sigCtx, &mcp.StdioTransport{})
}
