package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// serverModeFlagNames lists the flags that configure the MCP server, in a
// stable order for the diagnostic-mode ignored-flags notice. (--watch is
// honored in --list-tools mode; with --call-tool it is added to the notice
// separately.)
var serverModeFlagNames = []string{"host", "port", "api-key", "api-key-file", "tls-cert", "tls-key", "allowed-origins", "allow-all-origins", "disable-localhost-protection", "insecure-no-auth", "max-concurrent"}

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

	validator, err := resolveInputSchema(buildInputSchema(tool.Params))
	if err != nil {
		return 1, fmt.Errorf("invalid input schema for tool %q: %w", name, err)
	}
	if err := validateToolArguments(args, validator); err != nil {
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
// debounced change that alters the tool set clears the screen (TTY only) and
// re-prints the full list with the existing per-scan stderr warnings, until
// the process is signaled. A watch setup failure or a fatal watch
// termination (not signal cancellation) is an operational failure: stderr
// "Error:" line, exit 1. Path resolution errors are a startup failure
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

	// Re-prints only when the discovered tool set differs from the last
	// printed one: the guaranteed post-readiness rescan (and any other
	// no-op rescan) therefore stays silent — including quiet startups with
	// external symlink targets, whose paths are derived from the tools and
	// already covered by the tool comparison. Symlink targets outside the
	// directory are watched directly so external-target edits re-print.
	lastPrinted := tools
	var lastTargets []string
	rescan := func() []string {
		newTools, err := discoverTools(scriptsAbs, env.stderr)
		if err != nil {
			fmt.Fprintf(env.stderr, "Warning: failed to rediscover tools: %v\n", err)
			return lastTargets
		}
		if !toolsEqual(lastPrinted, newTools) {
			env.clearScreen(env.stdout)
			printList(newTools)
		}
		lastPrinted = newTools
		lastTargets = externalTargets(newTools, scriptsAbs)
		return lastTargets
	}

	// A watch failure is a real failure: the live list would silently freeze
	// on a stale snapshot, so the process exits 1 (signal cancellation
	// exits 0).
	if err := watchChanges(sigCtx, env, scriptsAbs, rescan); err != nil {
		if !errors.Is(err, context.Canceled) {
			fmt.Fprintf(env.stderr, "Error: watch loop stopped: %v\n", err)
			return 1
		}
	}
	return 0
}
