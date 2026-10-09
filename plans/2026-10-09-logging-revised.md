# PLAN — Revised logging (TRACE level, client identity, tool-call detail)

**Branch:** `feature/logging-revised` from `main` (1abaafa). **Target version:** 0.12.0.
**Source:** user request 2026-10-09. Builds on the completed `2026-10-06-granular-logging.md` (slog on stderr, custom one-line handler, `--log-level`/`LOG_LEVEL`).
**Status:** Draft — pending Plan Reviewer.

## Design decisions (locked)

1. **TRACE severity.** `slog.LevelTrace` (−8) is stdlib; no new dependency. `flags.go` `logLevels` gains `"trace": slog.LevelTrace` (lowest), `resolveLogLevel`'s error text becomes `…one of trace, debug, info, warn, or error…`, the flag help text and `usageLine` gain `trace`. The handler is level-agnostic (renders `rec.Level.String()` → `TRACE`), so `loghandler.go` needs no change. Precedence (`--log-level` > `LOG_LEVEL` > `info`) and default unchanged.
2. **Client identity (INFO "client connected").** The MCP `initialize` request carries `clientInfo` (name/version, e.g. Claude Code). Captured at the transport boundary, because the SDK synthesizes session state in our stateless HTTP mode (a bare `notifications/initialized` request gets a fabricated `InitializeParams` without `clientInfo`, so `ServerOptions.InitializedHandler` alone is unreliable):
   - **HTTP:** a middleware in `buildHTTPHandler` peeks POST bodies (read + reset — the same pattern the SDK's stateless path uses) and, when the batch contains a JSON-RPC `initialize` request, logs the `clientInfo` name/version.
   - **stdio:** switch `server.Run(sigCtx, &mcp.StdioTransport{})` to `&mcp.IOTransport{Reader: <tee>, Writer: os.Stdout}`; the tee reader inspects the newline-delimited `initialize` line the same way.
   - Fields: `clientName`, `clientVersion` (absent/empty → `unknown`). One record per `initialize`, at INFO, in both transports.
3. **Tool-call logging, centralized in `executeTool`** (`execute.go`) so the MCP handler and the `--call-tool` diagnostic share it:
   - **DEBUG** on every completed call: tool name+path, raw command line (argv as one quoted string), request parameters (raw JSON), `exitCode`, `stdoutBytes`, `stderrBytes`, `duration`, `truncated` (bounded-capture overflow flag).
   - **TRACE** — the same record plus the **full raw output** (`stdout`, `stderr` attribute values). Multi-line values stay on one physical line: `renderString`/`strconv.Quote` escapes `\n`/`\r`. Size is bounded — `boundedWriter` caps each stream at `maxToolOutputBytes` (1 MiB), so a TRACE record is at most ~2 MiB.
   - **WARN** on failure (non-zero exit, timeout, script start failure): same fields plus full raw output (same 1 MiB/stream bound) and a `reason` (`nonzero-exit` / `timeout` / `start-failed`). Timeout records include the effective `timeout` value.
4. **CORS preflight** (`http.go` `newCORSHandler`): **DEBUG** for an allowed preflight (`origin`, echoed `access-control-allow-headers`, `max-age`); **WARN** for a preflight whose origin is rejected (`origin`, `reason` = not-in-allowlist / non-canonical). Non-preflight requests are not logged here.
5. **Additional usable logs** (proposed, each cheap and diagnostic):
   - **INFO** `server.go`: `shutting down` on SIGINT/SIGTERM (with the signal name) and `server stopped` on clean serve exit (HTTP and stdio) — brackets the process lifetime.
   - **DEBUG** `watch.go`: per file event before debounce (`path`, `op`) — makes hot-reload timing visible.
   - **DEBUG** `registry.go` `replaceLocked`: the registration diff (`added`, `removed`, `changed` as name lists) — what a rescan actually changed.
   - **WARN** `registry.go` handler: argument validation failure (`tool`, `params`, `error`) — a malformed tool call as seen by the server.
   - **DEBUG** `registry.go` handler: at-capacity rejection (`tool`, `limit`) — the one current "silent" tool-call outcome.
   - **Optional (flag for review):** wire `env.log` into `mcp.ServerOptions.Logger` so the SDK's own records (e.g. `session initialized`) flow through the same handler/level. Rejected if the reviewer finds the resulting `INFO` pair per initialize noisy; the app-level "client connected" record does not depend on it.

## Existing log entries (catalogue)

All go to stderr via the one-line handler (`loghandler.go`); no production site writes raw to stderr anymore except the pre-logger path.

| # | Site | Event | Severity | Fields |
|---|------|-------|----------|--------|
| 1 | `prod.go:52` | server-mode `run()` failure (single reporting site) | ERROR | — (wrapped error text as message) |
| 2 | `server.go:73` | no executable scripts found at startup | WARN | `scriptsDir` |
| 3 | `server.go:108` | HTTP security-policy warning (one per warning) | WARN | — (message) |
| 4 | `server.go:155` | starting HTTP server | INFO | `addr`, `notes` (auth/TLS/CORS summary) |
| 5 | `server.go:187` | starting stdio server | INFO | — |
| 6 | `discover.go:77` | duplicate tool name ignored | WARN | `file`, `name`, `registeredBy` |
| 7 | `discover.go:91` | tool discovery summary | DEBUG | `count`, `scriptsDir` |
| 8 | `discover.go:172` | invalid `Timeout:` frontmatter ignored | WARN | `file`, `error` |
| 9 | `discover.go:193` | frontmatter incomplete (partial metadata kept) | WARN | `file`, `error` |
| 10 | `discover.go:206` | invalid `Param:` annotation skipped | WARN | `file`, `reason`, `line` |
| 11 | `diagnostic.go:27` | server-mode flags ignored in diagnostic mode | WARN | `flags` |
| 12 | `diagnostic.go:35` | `--call-tool` given an empty name | ERROR | — |
| 13 | `diagnostic.go:40` | `--call-tool`: path resolution failed | ERROR | — (message) |
| 14 | `diagnostic.go:45` | `--call-tool`: operational failure | ERROR | — (message) |
| 15 | `diagnostic.go:140` | `--list-tools`: path resolution failed | ERROR | — (message) |
| 16 | `diagnostic.go:146` | `--list-tools`: no executable scripts found | WARN | `scriptsDir` |
| 17 | `diagnostic.go:153` | `--list-tools`: discovery failed | ERROR | — (message) |
| 18 | `diagnostic.go:172` | `--list-tools --watch`: rediscovery failed | WARN | `error` |
| 19 | `diagnostic.go:187` | `--list-tools --watch`: watch loop stopped (fatal) | ERROR | `error` |
| 20 | `watch.go:119` | reattaching a replaced watched path | DEBUG | `path` |
| 21 | `watch.go:121` | reattach of watched path failed | WARN | `path`, `error` |
| 22 | `watch.go:174` | fsnotify error-channel entry (swallowed) | WARN | `error` |
| 23 | `watch.go:178` | debounced rescan fired | DEBUG | — |
| 24 | `watch.go:206` | server-mode rescan: rediscovery failed | WARN | `error` |

Non-slog (documented exception, unchanged): `prod.go:31`/`prod.go:37` — raw `Error: …` writes for flag-parse (exit 2) and pre-logger validation failures (exit 1); the logger does not exist yet at that point.

## New log entries (planned)

Target sites are where the record will be emitted after the change.

| # | Site (planned) | Event | Severity | Fields |
|---|----------------|-------|----------|--------|
| N1 | `http.go` body-peek middleware / `server.go` stdio stdin tee | client connected (initialize request) | INFO | `clientName`, `clientVersion` |
| N2 | `execute.go` `executeTool` | tool call completed | DEBUG | `tool`, `command` (raw argv), `params` (raw JSON), `exitCode`, `stdoutBytes`, `stderrBytes`, `duration`, `truncated` |
| N3 | `execute.go` `executeTool` | tool call output (same as N2 + full output) | TRACE | N2 fields + `stdout`, `stderr` (raw, ≤1 MiB each) |
| N4 | `execute.go` `executeTool` | tool call failed | WARN | N2 fields + `reason`, `stdout`, `stderr` (raw); `timeout` on timeout |
| N5 | `registry.go` handler | tool argument validation failed | WARN | `tool`, `params`, `error` |
| N6 | `registry.go` handler | at-capacity rejection | DEBUG | `tool`, `limit` |
| N7 | `http.go` `newCORSHandler` | CORS preflight allowed | DEBUG | `origin`, `requestHeaders`, `maxAge` |
| N8 | `http.go` `newCORSHandler` | CORS preflight origin rejected | WARN | `origin`, `reason` |
| N9 | `server.go` `run` | shutting down (signal) | INFO | `signal` |
| N10 | `server.go` `run` | server stopped (clean exit of serve) | INFO | `mode` (`http`/`stdio`) |
| N11 | `watch.go` `watchChanges` | file event received (pre-debounce) | DEBUG | `path`, `op` |
| N12 | `registry.go` `replaceLocked` | registration diff applied | DEBUG | `added`, `removed`, `changed` (name lists; record skipped when all empty) |
| N13 (opt.) | SDK `ServerOptions.Logger` | SDK-internal records (session lifecycle) | SDK-assigned | — |

## Tasks

- [ ] **T1 — TRACE level.** `flags.go`: add `"trace"` to `logLevels`, update `resolveLogLevel` error text, flag help, `usageLine`. `flags_test.go`: table case for `trace` (resolves to `slog.LevelTrace`), invalid-value text, precedence test extended; verify a TRACE record is captured at `trace` and dropped at `debug` (`server_test.go` filtering test gains a trace column).
- [ ] **T2 — Client identity (N1).** HTTP body-peek middleware in `buildHTTPHandler` (read + reset; initialize detection across batched JSON-RPC messages) logging `clientName`/`clientVersion`; stdio switch `&mcp.StdioTransport{}` → `&mcp.IOTransport{Reader: tee, Writer: os.Stdout}` with the same inspection. Tests: HTTP initialize with/without `clientInfo`, batched initialize+other messages, non-initialize POST untouched; stdio via `IOTransport` pair.
- [ ] **T3 — Tool-call records (N2–N4).** `execute.go`: capture `start` time, argv, exit code (`cmd.ProcessState`), stream sizes, truncation; emit DEBUG on completion, TRACE with raw output, WARN with raw output + `reason` on the three failure classes. `execute_test.go`: one record per outcome class at the right level; TRACE absent at `debug`; WARN present at `warn`; output values stay one physical line (newline in output → `\n`-escaped).
- [ ] **T4 — Registry records (N5–N6, N12).** Validation-failure WARN, at-capacity DEBUG, `replaceLocked` diff DEBUG (skip when empty). `registry_test.go` accordingly.
- [ ] **T5 — CORS preflight records (N7–N8).** `http.go`: DEBUG on allowed preflight, WARN on rejected origin. `http_test.go`: allowed/denied preflight at `debug`; denied origin record at `warn`; non-preflight requests emit nothing.
- [ ] **T6 — Lifecycle + rescan records (N9–N11).** `server.go` shutdown/stopped INFO; `watch.go` per-event DEBUG; keep the existing `rescan fired` DEBUG.
- [ ] **T7 — Optional SDK logger (N13).** Wire `env.log` into `mcp.ServerOptions.Logger` if the reviewer keeps it; otherwise document the nil-options choice in `server.go`.
- [ ] **T8 — Docs.** `README.md` Logging section: `trace` in the level list (both `--log-level` and `LOG_LEVEL` rows), the new records (client connected, tool call DEBUG/TRACE/WARN with the output-bound note, CORS preflight, lifecycle), one new sample line; flag table row for `--log-level`; `Makefile` `VERSION ?= 0.12.0`; version refs in README.
- [ ] **T9 — Final.** Full suite `go test ./...` + `go vet ./...` + `gofmt -l .` green; plan reviewed and updated.

## Risks & notes

- **One-line invariant.** Raw multi-line output is only safe as an *attribute value*: `renderString` quotes whitespace-bearing values via `strconv.Quote`, which escapes `\n`/`\r`. Never put raw output in the message.
- **Bounded size.** TRACE/WARN output is capped by `boundedWriter` at 1 MiB per stream; a pathological tool cannot grow a log record beyond ~2 MiB.
- **Secrets.** Tool parameters are client (LLM) data; they are logged in DEBUG as the user requested. API keys never appear: they live in flags/env, not in tool arguments.
- **Stateless-mode quirk.** In stateless HTTP, a bare `initialized` notification carries a synthesized `InitializeParams` without `clientInfo` — the reason N1 peeks the `initialize` request at the transport boundary instead of using `InitializedHandler` (which is also why N13's SDK `session initialized` INFO would be a *second* per-initialize record — review T7 accordingly).
- **Stdio switch.** `IOTransport{os.Stdin, os.Stdout}` is byte-identical in behavior to `StdioTransport` (the SDK's `StdioTransport.Connect` is `newIOConn(rwc{os.Stdin, …os.Stdout})`); the tee must be a pure passthrough (no reordering, no buffering past the protocol's newline framing) to avoid changing protocol timing.
