# mcp-commands — Stdout/Stderr Usage Map

Complete inventory of every place the process writes to its own standard streams,
as of branch `feature/logging` (commit c5c3ce6). Subprocess stream capture in
`execute.go` (`boundedWriter`, `cmd.Stdout/Stderr`, `combineToolOutput`) is
internal buffering, not process output, and is listed separately at the end.

The front end receives streams through the `liveEnv` struct (`server.go:42-49`),
which `prodLiveEnv` fills with `os.Stdout`/`os.Stderr` (`server.go:53-60`).
Only `main.go` touches the real `os.*` streams directly; everything else uses
the injected writers.

## Stream conventions in force

- **stdout** = result content (tool list, call result, `-h` help, `--version`)
- **stderr** = warnings, operational errors, server banners

## stdout writes

| Location | What | Context |
|---|---|---|
| `main.go:46` | `fmt.Fprint(os.Stdout, parseErr.usage)` | `-h`: full flag help, exit 0 |
| `main.go:60` | `fmt.Println(serverVersion)` | `--version`, exit 0 |
| `diagnostic.go:100` | `fmt.Fprintln(env.stdout, err.Error())` | `--call-tool`: required-param validation failure (deliberately on stdout: it is the tool result content) |
| `diagnostic.go:120` | `fmt.Fprintln(env.stdout, b.String())` | `--call-tool`: tool result text (always stdout, even on `IsError`) |
| `diagnostic.go:148` | `fmt.Fprint(env.stdout, renderToolList(...))` | `--list-tools`: the tool table (initial print + every watch re-print) |
| `diagnostic.go` (watch loop) | `env.clearScreen(env.stdout)` | `--list-tools --watch`: ANSI erase-screen before re-print; TTY-gated in `prodClearScreen` (`server.go:24-29`), no-op when piped |
| `server.go:29` | `stdout.Write("\x1b[2J\x1b[H")` | same clear-screen escape, only when `term.IsTerminal` |

## stderr writes

### main.go (direct `os.Stderr`)

| Line | What | Context |
|---|---|---|
| `main.go:49` | `Error: %v` + usage text | flag parse error, exit 2 |
| `main.go:53` | `Error: %v` | non-flag parse error, exit 1 |
| `main.go:55` | usage line | missing required flag, exit 1 |
| `main.go:68` | `Error: %v` | server mode `run()` failure, exit 1 |

### diagnostic.go

| Line | What |
|---|---|
| `diagnostic.go:27` | `Note: ignoring server-mode flags in diagnostic mode: …` |
| `diagnostic.go:35` | `Error: --call-tool requires a non-empty tool name` |
| `diagnostic.go:40` | `Error: %v` (path resolution, runDiagnostic) |
| `diagnostic.go:45` | `Error: %v` (call-tool operational failure) |
| `diagnostic.go:140` | `Error: %v` (path resolution, runListTools) |
| `diagnostic.go:146` | `Warning: No executable scripts found in %s` |
| `diagnostic.go:153` | `Error: %v` (discovery failure, runListTools) |
| `diagnostic.go:172` | `Warning: failed to rediscover tools: %v` (watch rescan) |
| `diagnostic.go:187` | `Error: watch loop stopped: %v` (fatal watch termination) |

### discover.go (all passed `stderr io.Writer`)

| Line | What |
|---|---|
| `discover.go:77` | `Warning: ignoring %s: tool name %q already registered by %s` |
| `discover.go:170` | `Warning: ignoring invalid Timeout in %s: %v` |
| `discover.go:191` | `Warning: frontmatter of %s is incomplete (%v); …` |
| `discover.go:204` | `warnParam`: `Warning: skipping invalid Param annotation in %s because %s: %q` (single shape shared by 5 validation sites, `discover.go:217-258`) |

### server.go

| Line | What |
|---|---|
| `server.go:79` | `Warning: No executable scripts found in %s` (server mode) |
| `server.go:114` | HTTP security policy warnings, one per line (dangerous-but-explicit bind/auth/CORS/TLS posture) |
| `server.go:161` | `Starting HTTP server on %s (notes)` banner |
| `server.go:169` | `Error: %v` (fatal watch failure in HTTP mode) |
| `server.go:192` | `Starting stdio server` banner |
| `server.go:199` | `Error: %v` (fatal watch failure in stdio mode) |

### watch.go

| Line | What |
|---|---|
| `watch.go:120` | `Warning: failed to reattach watch on %s: %v` |
| `watch.go:173` | `Warning: file watcher error: %v` |
| `watch.go:204` | `Warning: failed to rediscover tools: %v` |

## Not process output (excluded)

- `http.go:329` — `w.Write("unauthorized")`: HTTP response body, not a stream.
- `execute.go:98-147` — `boundedWriter`/`combineToolOutput`: captures the *child
  script's* stdout/stderr into a buffer for the MCP result text.

## Observations for the logging design

1. **Two ad-hoc text conventions exist**: `Error: …`, `Warning: …`, and bare
   banners/notes. A leveled logger (slog + tint, see `logging-libraries.md`)
   would replace all `fmt.Fprintf(env.stderr, "Warning: …")` sites with
   `log.Warn("…")` etc.
2. **stdout result content must stay raw**: the tool list, call result, help
   text, and version are program *output*, not logs — keep them as direct
   `fmt.Fprint(env.stdout, …)`; only diagnostics move to the logger.
3. **The stdio server is strict**: stdout is reserved for the MCP protocol, so
   in `modeServer` the logger must target stderr only (and the clear-screen
   escape is only legal in `--list-tools --watch`).
4. **~20 call sites** across `main.go`, `diagnostic.go`, `discover.go`,
   `server.go`, `watch.go`; all already funnel through `liveEnv.stderr`
   (injectable for tests) — a logger injected the same way keeps testability.
5. TTY-aware behavior already exists in two places (`prodClearScreen`,
   `prodResolveWrapWidth` in `list.go:22`); a color handler should gate on the
   same `term.IsTerminal` check (tint does this automatically).
