package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"
)

type cliMode int

const (
	modeServer cliMode = iota
	modeListTools
	modeCallTool
)

// serverConfig carries every server-mode option: the fully resolved (fail-
// fast, in parseCLI) result of the server flags. run consumes it as a whole.

type serverConfig struct {
	dir            string
	scriptsDir     string
	watch          bool
	host           string
	port           int
	apiKey         resolvedAPIKey // HTTP mode only; zero value = unauthenticated
	cors           corsConfig
	timeout        time.Duration // 0 = no global timeout (--no-timeout)
	insecureNoAuth bool
	maxConcurrent  int // 0 = default
}

// diagnostic carries every diagnostic-mode option (--list-tools / --call-
// tool). runDiagnostic consumes it as a whole.

type diagnostic struct {
	dir          string
	scriptsDir   string
	listTools    bool
	watch        bool
	callTool     string
	callToolSet  bool // --call-tool present (active even when its value is "")
	params       string
	timeout      time.Duration
	ignoredFlags []string
}

// cliConfig is the fully resolved and validated result of parseCLI. main
// dispatches on mode: the server mode consumes cfg.server, the diagnostic
// modes cfg.diagnostic.

type cliConfig struct {
	version    bool
	mode       cliMode
	server     serverConfig
	diagnostic diagnostic
}

// errMissingRequiredFlags is the sentinel parseCLI returns when --dir/
// --scripts are absent; main prints the usage line for it specifically.

var errMissingRequiredFlags = errors.New("--dir and --scripts are required")

// flagParseError wraps a raw flag-package parse error (undefined flag,
// invalid value, -h) together with the rendered full flag help, so main
// can restore the flag package's user-visible conventions: -h → help on
// stdout, exit 0; other parse errors → error + help on stderr, exit 2 —
// matching the pre-extraction flag.ExitOnError behavior.

type flagParseError struct {
	err   error
	usage string
}

func (e *flagParseError) Error() string { return e.err.Error() }
func (e *flagParseError) Unwrap() error { return e.err }

// usageLine is the one-line usage synopsis printed with errMissingRequiredFlags.
const usageLine = "Usage: mcp-commands --dir <directory> --scripts <directory> [--list-tools [--watch]] | [--call-tool <name> --params <json>] | [--watch] [--host <host>] [--port <port>] [--api-key <token>|--api-key-file <path>] [--allowed-origins <origin[,origin...]>]|[--allow-all-origins] [--disable-localhost-protection] [--insecure-no-auth] [--max-concurrent <n>] [--timeout <duration>] | [--no-timeout]"

func parseCLI(args []string) (cliConfig, error) {
	fs := flag.NewFlagSet("mcp-commands", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // errors are formatted by the caller

	dirFlag := fs.String("dir", "", "Working directory for tool execution (required)")
	scriptsFlag := fs.String("scripts", "", "Directory containing executable scripts (required)")
	watchFlag := fs.Bool("watch", false, "Watch for tool changes: hot-reload in server mode, live re-print in --list-tools mode (ignored with --call-tool)")
	hostFlag := fs.String("host", "127.0.0.1", "IP address for HTTP server")
	portFlag := fs.Int("port", 0, "Port for HTTP server (don't set or 0 for stdio mode)")
	apiKeyFlag := fs.String("api-key", "", "API token for HTTP mode (alternatives: --api-key-file, MCP_COMMANDS_API_KEY)")
	apiKeyFileFlag := fs.String("api-key-file", "", "Read the API token from a file (content is trimmed; trailing newline ok)")
	insecureNoAuthFlag := fs.Bool("insecure-no-auth", false, "Allow an unauthenticated HTTP server on a non-loopback host (loudly warned; never use in production)")
	maxConcurrentFlag := fs.Int("max-concurrent", defaultMaxConcurrentTools, "Maximum concurrent tool executions (0 for default; calls beyond the cap get a clean at-capacity error)")
	allowedOriginsFlag := fs.String("allowed-origins", "", "Comma-separated exact origin allowlist for CORS (or set MCP_COMMANDS_ALLOWED_ORIGINS)")
	allowAllOriginsFlag := fs.Bool("allow-all-origins", false, "Echo any Origin header for CORS, dev convenience (or set MCP_COMMANDS_ALLOW_ALL_ORIGINS)")
	disableLocalhostProtectionFlag := fs.Bool("disable-localhost-protection", false, "Disable the SDK's DNS-rebinding protection for loopback servers")
	versionFlag := fs.Bool("version", false, "Print version and exit")
	timeoutFlag := fs.String("timeout", "", "Global per-tool timeout as a formatted duration (e.g. 5m, 1h 30m 5s, NONE); default 5m")
	noTimeoutFlag := fs.Bool("no-timeout", false, "Disable the global tool timeout (mutually exclusive with --timeout)")
	listToolsFlag := fs.Bool("list-tools", false, "List the discovered tools (name, signature, description) and exit; no server is started. With --watch: re-print the list live on script changes")
	callToolFlag := fs.String("call-tool", "", "Run one discovered tool by name and exit (debug mode; no server is started)")
	paramsFlag := fs.String("params", "{}", "JSON object of named arguments for --call-tool (default: empty object; required-param validation applies)")

	if err := fs.Parse(args); err != nil {
		// Wrapped so main can restore the flag package's parse-error
		// convention (help to stderr, exit 2) and -h (help to stdout,
		// exit 0); validation errors keep the regular Error:/exit 1 path.
		var usage strings.Builder
		fs.SetOutput(&usage)
		fs.Usage() // "Usage of mcp-commands:" + every flag and description
		fs.SetOutput(io.Discard)
		return cliConfig{}, &flagParseError{err: err, usage: usage.String()}
	}

	cfg := cliConfig{
		server: serverConfig{
			dir:            *dirFlag,
			scriptsDir:     *scriptsFlag,
			watch:          *watchFlag,
			host:           *hostFlag,
			port:           *portFlag,
			insecureNoAuth: *insecureNoAuthFlag,
			maxConcurrent:  *maxConcurrentFlag,
		},
		diagnostic: diagnostic{
			dir:        *dirFlag,
			scriptsDir: *scriptsFlag,
			listTools:  *listToolsFlag,
			watch:      *watchFlag,
			callTool:   *callToolFlag,
			params:     *paramsFlag,
		},
	}

	// --version works without the required flags and skips all validation.
	if *versionFlag {
		cfg.version = true
		return cfg, nil
	}

	if *dirFlag == "" || *scriptsFlag == "" {
		return cliConfig{}, errMissingRequiredFlags
	}

	// Resolved and validated here (all modes, fail-fast); run only consumes it.
	allowAllSet := false
	timeoutSet := false
	callToolSet := false
	visited := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) {
		visited[f.Name] = true
		switch f.Name {
		case "allow-all-origins":
			allowAllSet = true
		case "timeout":
			timeoutSet = true
		case "call-tool":
			callToolSet = true
		}
	})

	var err error
	cfg.server.cors, err = resolveCORS(*allowedOriginsFlag, *allowAllOriginsFlag, allowAllSet, *disableLocalhostProtectionFlag)
	if err != nil {
		return cliConfig{}, err
	}

	// Resolved and validated here (all modes, fail-fast); run only consumes it.
	timeout, err := resolveTimeout(*timeoutFlag, timeoutSet, *noTimeoutFlag)
	if err != nil {
		return cliConfig{}, err
	}
	cfg.server.timeout = timeout
	cfg.diagnostic.timeout = timeout

	callToolActive := *callToolFlag != "" || callToolSet
	cfg.diagnostic.callToolSet = callToolSet
	cfg.mode = modeServer
	if callToolActive {
		cfg.mode = modeCallTool
	} else if *listToolsFlag {
		cfg.mode = modeListTools
	}
	if cfg.diagnostic.listTools && callToolActive {
		return cliConfig{}, errors.New("--list-tools and --call-tool are mutually exclusive")
	}
	if cfg.server.maxConcurrent < 0 {
		return cliConfig{}, fmt.Errorf("--max-concurrent must be >= 0 (got %d)", cfg.server.maxConcurrent)
	}
	if cfg.mode != modeServer {
		// The server-mode flags are always ignored in diagnostic modes
		// (presence is visit-tracked, so default-valued forms like
		// --port=0, --host=127.0.0.1, --watch=false are noticed too).
		for _, name := range serverModeFlagNames {
			if visited[name] {
				cfg.diagnostic.ignoredFlags = append(cfg.diagnostic.ignoredFlags, "--"+name)
			}
		}
		// --watch is honored with --list-tools and ignored with --call-tool;
		// appended last to preserve the pre-refactor notice ordering.
		if cfg.mode == modeCallTool && visited["watch"] {
			cfg.diagnostic.ignoredFlags = append(cfg.diagnostic.ignoredFlags, "--watch")
		}
		return cfg, nil
	}

	// HTTP mode only: in stdio mode the auth sources are documented as
	// ignored, so an unreadable --api-key-file must not block a stdio
	// server from starting. (Diagnostic modes ignore the key flags and
	// report them via the ignored-flags notice.)
	if cfg.server.port > 0 {
		cfg.server.apiKey, err = resolveAPIKey(*apiKeyFlag, *apiKeyFileFlag)
	}
	return cfg, err
}
