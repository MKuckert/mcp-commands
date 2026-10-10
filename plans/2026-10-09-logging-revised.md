# PLAN — Revised logging (TRACE level, client identity, tool-call detail)

**Branch:** `feature/logging-revised` from `main` (1abaafa). **Target version:** 0.12.0.
**Source:** user request 2026-10-09. Builds on the completed `2026-10-06-granular-logging.md` (slog on stderr, custom one-line handler, `--log-level`/`LOG_LEVEL`).
**Status:** Completed

## Design decisions (locked)

1. **TRACE severity.** The stdlib `slog` defines no TRACE constant (only `LevelDebug`/`LevelInfo`/`LevelWarn`/`LevelError`), so the app names one: `const levelTrace = slog.Level(-8)` in `loghandler.go` (`Level(-8).String()` would render `DEBUG-4`; the handler's `levelName` renders it `TRACE`). No new dependency. `flags.go` `logLevels` gains `"trace": levelTrace` (lowest), `resolveLogLevel`'s error text becomes `…one of trace, debug, info, warn, or error…`, the flag help text gains `trace` (`usageLine` renders only `--log-level <level>` with no level list — unchanged). The shipped completion scripts (`completion/mcp-commands.{bash,zsh,fish}`) hard-code the level list and gain `trace` there too. The handler renders levels via `levelName` (the five app levels map to their names; `levelTrace` renders `TRACE`). Precedence (`--log-level` > `LOG_LEVEL` > `info`) and default unchanged.
2. **Client identity (INFO "client connected").** The MCP `initialize` request carries `clientInfo` (name/version, e.g. Claude Code). Captured at the transport boundary, because the SDK synthesizes session state in our stateless HTTP mode (a bare `notifications/initialized` request gets a fabricated `InitializeParams` without `clientInfo`, so `ServerOptions.InitializedHandler` alone is unreliable):
   - **HTTP:** a middleware in `buildHTTPHandler` peeks POST bodies and, when the batch contains a JSON-RPC `initialize` request, logs the `clientInfo` name/version. **Placement (B1):** inserted *between* the streamable handler and the `MaxBytesReader`+`bodyReadDeadline` wrapper — i.e. wrap the streamable handler with the peek *first*, then apply the size/deadline cap — so the peek's `ReadAll` runs *through* the 10 MiB cap and the 30 s read deadline (exactly where the SDK's own stateless peek runs). Placed outside the cap, the peek would buffer unbounded bodies into memory and read without a time bound. The peek inspects **POST only** (GETs/SSE have no body); a failed or over-cap read **skips the record and passes the request through unchanged** (the SDK's handler then applies its own limits); the body is reset via `io.NopCloser(bytes.NewBuffer(…))` like the SDK.
   - **stdio:** switch `server.Run(sigCtx, &mcp.StdioTransport{})` to `&mcp.IOTransport{Reader: <tee>, Writer: <no-op-closer over os.Stdout>}` (the SDK's `rwc.Close` closes both ends — a raw `os.Stdout` would close fd 1 at session end); the tee reader inspects the newline-delimited `initialize` line the same way. The tee implements `io.ReadCloser` with a no-op `Close` (wraps `os.Stdin`), is a pure byte passthrough (no reordering, no buffering past the protocol's newline framing — the partial-line buffer is capped at 64 KiB and an over-long line is discarded until its newline, so a malformed frame cannot grow it without bound), and its inspection is synchronous (one slog emit, a single `Write`) — no channel hand-off that could stall the read path.
   - Fields: `transport` (`http`/`stdio`), `clientName`, `clientVersion` (absent/empty → `unknown`). One record per `initialize`, at INFO, in both transports.
3. **Tool-call logging, centralized in `executeTool`** (`execute.go`) so the MCP handler and the `--call-tool` diagnostic share it:
   - **DEBUG** on every completed call: tool name+path, raw command line (argv as one quoted string), request parameters (raw JSON), `exitCode`, `stdoutBytes`, `stderrBytes`, `duration`, `truncated` (bounded-capture overflow flag).
   - **TRACE** — the same record plus the **full raw output** (`stdout`, `stderr` attribute values). Multi-line values stay on one physical line: `renderString`/`strconv.Quote` escapes `\n`/`\r`. Size is bounded — `boundedWriter` caps each stream at `maxToolOutputBytes` (1 MiB), so a TRACE record is at most ~2 MiB.
   - **WARN** on failure: same fields plus full raw output (same 1 MiB/stream bound) and a `reason` from the pinned four-class taxonomy: `timeout` (deadline exceeded — checked first, per the existing classification) / `nonzero-exit` (script exited non-zero) / `canceled` (client abort: `ctx` canceled with no deadline, `--no-timeout` case; `exitCode` −1) / `start-failed` (script never started; no output). Timeout records include the effective `timeout` value.
   - **`params` representation:** the **raw arguments, not re-marshaled** (user directive 2026-10-09): the MCP handler passes `req.Params.Arguments` (the client's exact JSON) and `--call-tool` passes its `--params` JSON; `executeTool` receives the raw string as an added parameter (the parsed map stays as it is). This preserves the client's key order/format and avoids a `json.Marshal` re-marshal of the parsed map.
4. **CORS preflight** (`http.go` `newCORSHandler`): **DEBUG** for an allowed preflight (`origin`, echoed `access-control-allow-headers`, `max-age`); **WARN** for a preflight whose origin is rejected (`origin`, `reason` = not-in-allowlist / non-canonical). Non-preflight requests are not logged here.
5. **Additional usable logs** (proposed, each cheap and diagnostic):
   - **INFO** `server.go`: `shutting down` on SIGINT/SIGTERM and `server stopped` on clean serve exit (HTTP and stdio) — brackets the process lifetime. **No signal name** (seam decision): `liveEnv.notifySignals` wraps `signal.NotifyContext`, which does not expose which signal fired; widening the seam (channel + manual cancel, plus every test fake) is not justified for one field — the record carries `reason=signal` only.
   - **DEBUG** `watch.go`: per file event before debounce (`path`, `op`) — makes hot-reload timing visible.
   - **DEBUG** `registry.go` `replaceLocked`: the registration diff (`added`, `removed`, `changed` as name lists) — what a rescan actually changed.
   - **WARN** `registry.go` handler: argument validation failure (`tool`, `params`, `error`) — a malformed tool call as seen by the server.
   - **DEBUG** `registry.go` handler: at-capacity rejection (`tool`, `limit`) — the one current "silent" tool-call outcome.
   - **SDK logger: rejected** (review ruling). Wiring `env.log` into `mcp.ServerOptions.Logger` was proposed and **rejected**: `Server.Connect` unconditionally logs `server connecting` (INFO) at *every* connect, and stateless HTTP connects per *request* — so it would add an INFO record to every tool call at the default level, not one per initialize. Today's nil options map to `slog.DiscardHandler` (deliberate silence); `server.go` gains a comment pinning that choice (T7).

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

Non-slog (documented exception, unchanged): `prod.go:33`/`prod.go:34` — raw `Error: …` + usage writes for flag-parse failures (exit 2); `prod.go:37` — raw `Error: …` for pre-logger validation failures (exit 1); `prod.go:39` — the usage line for missing required flags. In all cases the logger does not exist yet at that point.

## New log entries (planned)

Target sites are where the record will be emitted after the change.

| # | Site (planned) | Event | Severity | Fields |
|---|----------------|-------|----------|--------|
| N1 | `http.go` body-peek middleware / `server.go` stdio stdin tee | client connected (initialize request) | INFO | `transport`, `clientName`, `clientVersion` (each field cut at 256 B, rune-boundary safe, with an explicit truncation indicator — a 10 MiB body must not become a multi-MiB log record) |
| N2 | `execute.go` `executeTool` | tool call completed | DEBUG | `tool`, `command` (raw argv), `params` (raw JSON), `exitCode`, `stdoutBytes`, `stderrBytes`, `duration`, `truncated` |
| N3 | `execute.go` `executeTool` | tool call output (same as N2 + full output) | TRACE | N2 fields + `stdout`, `stderr` (raw, ≤1 MiB each) |
| N4 | `execute.go` `executeTool` | tool call failed | WARN | N2 fields + `reason`, `stdout`, `stderr` (raw); `timeout` on timeout |
| N5 | `registry.go` handler | tool argument validation failed | WARN | `tool`, `params`, `error` |
| N6 | `registry.go` handler | at-capacity rejection | DEBUG | `tool`, `limit` |
| N7 | `http.go` `newCORSHandler` | CORS preflight allowed | DEBUG | `origin`, `requestHeaders`, `maxAge` |
| N8 | `http.go` `newCORSHandler` | CORS preflight origin rejected | WARN | `origin`, `reason` |
| N9 | `server.go` `run` | shutting down (signal) | INFO | `reason` (`signal`; no name — seam limitation) |
| N10 | `server.go` `run` | server stopped (clean exit of serve) | INFO | `mode` (`http`/`stdio`) |
| N11 | `watch.go` `watchChanges` | file event received (pre-debounce) | DEBUG | `path`, `op` |
| N12 | `registry.go` `replaceLocked` | registration diff applied | DEBUG | `added`, `removed`, `changed` (name lists; record skipped when all empty) |
| ~~N13~~ | — (rejected in review) | SDK-internal records would double-log per request in stateless mode | — | — |

## Tasks

- [x] **T1 — TRACE level.** `flags.go`: add `"trace"` to `logLevels`, update `resolveLogLevel` error text and flag help (`usageLine` unchanged — it carries no level list). `completion/mcp-commands.{bash,zsh,fish}`: add `trace` to the hard-coded `--log-level` lists (they track `logLevels`). `flags_test.go`: table case for `trace` (resolves to `levelTrace`), invalid-value text, precedence test extended; verify a TRACE record is captured at `trace` and dropped at `debug` (`server_test.go` filtering test gains a trace column).
- [x] **T2 — Client identity (N1).** HTTP body-peek middleware in `buildHTTPHandler` per Design decision 2: placed between the streamable handler and the size/deadline wrapper (B1), POST only, initialize detection across batched JSON-RPC messages, failed/over-cap read → skip record + pass through, body reset via `io.NopCloser(bytes.NewBuffer(…))`; logs `clientName`/`clientVersion`. Stdio: switch `&mcp.StdioTransport{}` → `&mcp.IOTransport{Reader: tee, Writer: os.Stdout}` with the same inspection; the tee is a no-op-`Close` `io.ReadCloser` wrapping `os.Stdin`, synchronous non-blocking inspection. Tests: HTTP initialize with/without `clientInfo`, batched initialize+other messages, non-initialize POST untouched, over-cap body passes through with no record, peek sits inside the cap (a >10 MiB initialize-shaped body is rejected by `MaxBytesReader`, not buffered unbounded); stdio via `IOTransport` pair.
- [x] **T3 — Tool-call records (N2–N4).** `execute.go`: `executeTool` gains two parameters — a `*slog.Logger` (no package global) and the **raw arguments string** (handler: `req.Params.Arguments`; diagnostic: `--params` JSON; never re-marshaled); capture `start` time, argv (`cmd.Args`), exit code (`cmd.ProcessState`), stream sizes, truncation, duration; log `params` from that raw string; emit DEBUG on completion, TRACE with raw output, WARN with raw output + `reason` over the pinned four classes (`timeout` / `nonzero-exit` / `canceled` / `start-failed`). Update the `registry.go` handler and the `runCallTool` call site (`diagnostic.go`) accordingly. `execute_test.go`: one record per outcome class at the right level (incl. a `canceled` case), `params` renders the raw string verbatim (incl. unsorted key order); TRACE absent at `debug`; WARN present at `warn`; output values stay one physical line (newline in output → `\n`-escaped).
- [x] **T4 — Registry records (N5–N6, N12).** `toolRegistry` gains a `log *slog.Logger` field (populated from `env.log` at the `server.go` `newToolRegistry` call site; no package global); the handler passes it to `executeTool`. Validation-failure WARN, at-capacity DEBUG, `replaceLocked` diff DEBUG (skip when empty). `registry_test.go` accordingly (logger over a `bytes.Buffer`).
- [x] **T5 — CORS preflight records (N7–N8).** `http.go`: DEBUG on allowed preflight, WARN on rejected origin. `http_test.go`: allowed/denied preflight at `debug`; denied origin record at `warn`; non-preflight requests emit nothing.
- [x] **T6 — Lifecycle + rescan records (N9–N11).** `server.go` `shutting down` (INFO, `reason=signal`, no name) / `server stopped` (INFO, `mode`); `watch.go` per-event DEBUG; keep the existing `rescan fired` DEBUG. Tests: `watch_test.go` case asserting the per-event DEBUG record; `server_test.go` cases for the two lifecycle records.
- [x] **T7 — SDK logger: document the nil choice.** `server.go`: `mcp.NewServer(impl, nil)` gains a comment pinning why `ServerOptions.Logger` stays nil — the SDK logs `server connecting` (INFO) at *every* connect and stateless HTTP connects per *request*, so wiring the app logger would double-log every request; nil maps to `slog.DiscardHandler` (deliberate silence). (Original optional wiring rejected in review.)
- [x] **T8 — Docs.** `README.md` Logging section: `trace` in the level list (both `--log-level` and `LOG_LEVEL` rows), the new records (client connected, tool call DEBUG/TRACE/WARN with the output-bound note, CORS preflight, lifecycle), one new sample line; flag table row for `--log-level`; `Makefile` `VERSION ?= 0.12.0`; version refs in README.
- [x] **T9 — Final.** Full suite `go test ./...` + `go vet ./...` + `gofmt -l .` green; plan reviewed and updated.

## Risks & notes

- **One-line invariant.** Raw multi-line output is only safe as an *attribute value*: `renderString` quotes whitespace-bearing values via `strconv.Quote`, which escapes `\n`/`\r`. Never put raw output in the message.
- **Bounded size.** TRACE/WARN output is capped by `boundedWriter` at 1 MiB per stream; a pathological tool cannot grow a log record beyond ~2 MiB.
- **Secrets.** Tool parameters are client (LLM) data; they are logged in DEBUG as the user requested. API keys never appear: they live in flags/env, not in tool arguments.
- **Stateless-mode quirk.** In stateless HTTP, a bare `initialized` notification carries a synthesized `InitializeParams` without `clientInfo` — the reason N1 peeks the `initialize` request at the transport boundary instead of using `InitializedHandler`. Related: the SDK logs `server connecting` (INFO) at every connect, and stateless mode connects per *request* — the reason the SDK logger wiring (former N13) was rejected (T7).
- **Stdio switch.** `IOTransport{os.Stdin, os.Stdout}` is byte-identical in behavior to `StdioTransport` (the SDK's `StdioTransport.Connect` is `newIOConn(rwc{os.Stdin, …os.Stdout})`); the tee must be a pure passthrough (no reordering, no buffering past the protocol's newline framing) to avoid changing protocol timing, and its inspection must not block the read path.
- **Double body buffering.** In stateless HTTP the request body is read and buffered twice per request (the app's peek, then the SDK's own stateless peek) — peak ≈ 2 × min(body, 10 MiB) per request. Acceptable: the cap bounds it, and initialize-shaped bodies are small in practice; noted here so a future single-peek redesign has to beat this explicitly.

## Review log

### Planner — 2026-10-09 (user change, post-approval)

User directive: Design decision 3 must not re-marshal to JSON when another format is feasible. Applied: `params` is now the **raw arguments string** (handler `req.Params.Arguments` / `--params` JSON) passed via a new `executeTool` parameter — preserves client key order/format, no `json.Marshal`. Decision 3 and T3 updated; the N-table's `params (raw JSON)` field is consistent as written. Status stays Approved (user directive, no feasibility impact — the raw string is already in hand at both call sites).

### Plan Reviewer — 2026-10-09 (draft, single revision) — **APPROVED**

**What is right** (all verified against the code and the go-sdk v1.6.1 source, not taken on trust):

- **Catalogue is exact and complete.** All 24 entries checked: file:line, severity, and fields all match (`prod.go:52`, `server.go:73/108/155/187`, `discover.go:77/91/172/193/206`, `diagnostic.go:27/35/40/45/140/146/153/172/187`, `watch.go:119/121/174/178/206`). A grep of all non-test, non-dist Go source found no other production log emission; the only raw-stderr writes are the pre-logger ones in `prod.go`.
- **TRACE mechanism is sound.** `levelTrace` (`slog.Level(-8)`) renders as `TRACE` via the handler's `levelName` (raw `Level(-8).String()` would read `DEBUG-4`); `logHandler.Enabled` is `level >= minLevel` and the rendering is level-agnostic, so no handler change is needed. The `flags.go` changes described (map entry, `resolveLogLevel` text, flag help) are all real seams.
- **Stateless synthesis claim is true.** `streamable.go:422–470`: in stateless mode the SDK peeks the body, and when the batch lacks `initialize` it fabricates `InitializeParams{ProtocolVersion: …}` with **no clientInfo**; `InitializedHandler` (`server.go:1068`) then fires against that fabricated state. A transport-boundary peek is the correct capture point for N1.
- **Stdio switch is a drop-in.** `StdioTransport.Connect` ≡ `newIOConn(rwc{os.Stdin, nopCloserWriter{os.Stdout}})`; `IOTransport.Connect` ≡ `newIOConn(rwc{t.Reader, t.Writer})` (`transport.go:104–127`). The SDK reads via `json.NewDecoder` in a goroutine, so a byte-passthrough tee is timing-safe.
- **Tool-call fields are producible** in `executeTool`: `cmd.Args`, `cmd.ProcessState.ExitCode()`, `boundedWriter` sizes + `Truncated()`, duration. The one-line invariant holds: `renderString` routes whitespace-bearing values through `strconv.Quote`, which escapes `\n`/`\r`. The `--call-tool` path calls the same `executeTool`, so centralization works. The existing timeout classification (deadline checked first) matches the plan's `reason` taxonomy precedence.
- **CORS seams exist.** `newCORSHandler` has exactly the two branches needed (allowed-preflight response path; origin-rejection path including non-canonical origins via `canonicalOrigin` → `""`). Preflights are `OPTIONS` with no body, so they never interact with the body-peek middleware.
- **Tasks cover the table.** Every N1–N12 maps to a task; N13 is handled explicitly in T7. T1–T5 all carry test requirements.

**Findings.**

1. **BLOCKING — B1: the HTTP body-peek middleware has no specified position in the handler chain** (Design decision 2; T2). `buildHTTPHandler` (`http.go`) builds innermost→outermost: streamable handler → `MaxBytesReader`+`bodyReadDeadline` wrapper → bearer auth → CORS. The peek must be inserted **between the streamable handler and the size/deadline wrapper** (wrap the streamable handler with the peek *first*, then apply the cap). Rationale, verified in the code: the peek's `ReadAll` must run *through* the 10 MiB `MaxBytesReader` and the 30 s read-deadline — exactly where the SDK's own peek runs (`streamable.go:430`, inside the handler, below the app's wrappers). Placed anywhere *outside* the cap, the peek buffers the entire unbounded body into memory (a multi-GB body defeats the documented O(10 MiB) guarantee) and its read is untime-bounded. The plan must pin this ordering, plus: the peek inspects POST only (GETs/SSE streams have no body), tolerates a failed/over-cap read (skip the record, pass the request through unchanged), and resets via `io.NopCloser(bytes.NewBuffer(…))` like the SDK. *Required change:* add the placement and failure-tolerance spec to Design decision 2 and T2.
2. **BLOCKING — B2: the shipped completion scripts are not covered by any task.** `completion/mcp-commands.bash` (line 12), `mcp-commands.zsh` (line 14), and `mcp-commands.fish` (line 40) hard-code `debug info warn error` for `--log-level` (comment: "logLevels in flags.go") — `trace` will be missing from the released artifacts. *Required change:* add the three completion files to T1 (they track `logLevels`) or T8.
3. **Non-blocking — the N13 noise analysis understates the cost; reviewer ruling: reject the wiring.** The risk note says N13 would add "a second per-initialize record". Verified against `server.go:1028`: `Server.Connect` unconditionally logs `"server connecting"` (INFO) at **every** connect, and stateless HTTP mode connects per *request* (no sessions retained, `streamable.go:352/512`) — so wiring `env.log` in would add an INFO record to **every** tool call and every request at the default `info` level, not one per initialize. A nil `ServerOptions.Logger` maps to `slog.DiscardHandler` (`server.go:183–184`, `logging.go:93`), so today's silence is deliberate. *Required change:* flip T7 to "document the nil-options choice in `server.go`" (the plan's own fallback branch) and correct the stateless-quirk risk note from "per-initialize" to "per-request".
4. **Non-blocking — T6 has no test requirement** (every other task lists tests). Add: `watch_test.go` case asserting the per-event DEBUG record (N11) and `server_test.go` cases for `shutting down`/`server stopped` (N9/N10).
5. **Non-blocking — WARN `reason` taxonomy misses a fourth class.** A client abort/cancellation with no deadline (`--no-timeout`, ctx cancel) yields non-nil `waitErr` that is neither `DeadlineExceeded` nor a non-zero exit. Pin the classification in T3 — either a `canceled` reason, or fold into `nonzero-exit` with `exitCode` −1 — so the record is unambiguous.
6. **Non-blocking — logger plumbing is unstated.** `executeTool` takes no logger and `toolRegistry` has no logger field; T3/T4 must add `*slog.Logger` to `executeTool`'s signature and to `toolRegistry` (populated from `env.log` at the `server.go` `newToolRegistry` call site). Say so so the builder doesn't reach for a package global.
7. **Non-blocking — catalogue minor inaccuracy (non-slog note).** The note cites `prod.go:31`/`prod.go:37`; the raw writes are at lines 33/34 (flag-parse, exit 2), 37 (validation, exit 1), plus an unlisted `fmt.Fprintln(os.Stderr, usageLine)` at line 39 for missing required flags. (All 24 slog entries themselves are exact.)
8. **Non-blocking — Design decision 1 overstates a `usageLine` change.** `usageLine` renders `--log-level <level>` with no level list; only the flag help string and the README flag table carry the list. Drop the `usageLine` clause (the T8 README flag-table row is the real docs change).
9. **Non-blocking — N9 "with the signal name" needs a seam decision.** `liveEnv.notifySignals` wraps `signal.NotifyContext`, which does not expose which signal fired. Either widen the seam (channel + manual cancel) or log `shutting down` without the name; decide in T6.
10. **Non-blocking — stdio tee typing.** `IOTransport.Reader` is `io.ReadCloser` (the tee type must implement a no-op `Close`; `os.Stdin` already does). Also state that the inspector must not block the read path (a synchronous single-Write slog emit is fine; no channel hand-off that could stall the protocol).
11. **Non-blocking — double body buffering in stateless HTTP.** After the change the body is read+buffered twice per request (app peek, then the SDK's own peek, `streamable.go:430`) — peak ≈ 2 × min(body, 10 MiB) per request. Acceptable; add one sentence to the Risks & notes section.
12. **Non-blocking — pin the `params` representation in T3.** `executeTool` receives the *parsed* map, not the raw JSON; re-marshaling (`json.Marshal`) for the record is faithful (Go sorts keys) and avoids a signature change. State which is used.

**Verdict: APPROVED** — all mechanisms are feasible as designed; close B1 (peek placement) and B2 (completion files) before building, apply the non-blocking notes where they land naturally.

### Plan Reviewer — 2026-10-09 (c181187..ecd56a6, correction round 1) — **APPROVED**

Verified the revision diff and re-checked every claim it made against the source. **All 14 findings from round 1 are closed:**

1. **B1 (peek placement) — closed.** Design decision 2 now pins the position (between the streamable handler and the `MaxBytesReader`+`bodyReadDeadline` wrapper, peek wrapped first), POST-only inspection, failed/over-cap read → skip record + pass through, body reset via `io.NopCloser(bytes.NewBuffer(…))`; T2 mirrors the spec and adds the two matching tests (over-cap body passes with no record; >10 MiB body rejected by the cap, not buffered unbounded). Consistent with `buildHTTPHandler` (`http.go:422`).
2. **B2 (completion files) — closed.** All three files named in decision 1 and T1; the hard-coded `debug info warn error` lists confirmed at `mcp-commands.bash:12`, `mcp-commands.zsh:14`, `mcp-commands.fish:40`.
3. **N13 rejection / per-request — closed.** Decision 5 records the rejection with the verified per-request rationale; T7 flipped to documenting the nil choice; table row struck; stateless-quirk risk note corrected.
4. **T6 tests — closed.** `watch_test.go` per-event case and `server_test.go` lifecycle cases now listed.
5. **`canceled` reason class — closed.** Four-class taxonomy pinned in decision 3 and T3 (incl. a `canceled` test case); implementable as `execute.go:209–224` stands (non-deadline `waitErr`, killed child → exit −1).
6. **Logger plumbing — closed.** `*slog.Logger` on `executeTool` (T3) and a `log` field on `toolRegistry` populated from `env.log` (T4); no package global; `liveEnv.log` (`server.go:35`) and `newToolRegistry` (`registry.go:34`) confirmed as the real seams.
7. **Catalogue non-slog lines — closed.** `prod.go:33/34/37/39` re-verified against the source — all four writes at exactly those lines.
8. **`usageLine` clause — closed.** Dropped from decision 1 (and stated unchanged in T1); the synopsis does carry only `<level>`.
9. **Signal-name seam — closed.** No-name decision recorded with justification; N9 fields and T6 aligned to `reason=signal`.
10. **Tee typing / non-blocking — closed.** `io.ReadCloser` with no-op `Close`, pure passthrough, synchronous single-`Write` inspection, all stated in decision 2 and T2.
11. **Double body buffering — closed.** New "Double body buffering" risk note with the 2 × min(body, 10 MiB) bound.
12. **`params` representation — closed.** `json.Marshal` of the parsed map pinned in decision 3 and T3.

**New findings.** None blocking. One cosmetic note (non-blocking, no change required): the decision-5 heading still reads "(proposed, each cheap and diagnostic)" while its last bullet is a rejected item — the bullet's own "rejected (review ruling)" label makes the state unambiguous, so leave it.

**Verdict: APPROVED** — no blocking findings; the plan is ready for the Builder. T9's "plan reviewed and updated" obligation is satisfied by this entry.

### Builder — 2026-10-09 (Copilot PR review, round 2)

Fixed the open Copilot findings on PR #38 (the first round was already
addressed in `66059b7`):

- **Stdio tee line cap.** `stdinIdentityTee.partial` was unbounded until a
  newline; a malformed over-long frame could grow it without bound. Now capped
  at 64 KiB (`maxIdentityLineBytes`) with a discard-until-newline state; the
  byte passthrough is unaffected (the inspector reads a copy).
- **`transport` field on N1.** Both client-identity records (HTTP peek, stdio
  tee) now carry `transport=http` / `transport=stdio` so the two
  transport-boundary captures are distinguishable.
- **Shutdown-record determinism (HTTP).** The shutdown goroutine now closes an
  ack channel after the record + `Shutdown`; `run()` waits for it before
  returning on the serve-failure and watch-error paths (and before `server
  stopped` on the clean path), so the process can no longer exit before the
  record lands.
- **Shutdown-record determinism (stdio).** The `shutting down` record is now
  emitted after *either* cancellation arm (the clean-watch-stop arm checks
  `sigCtx.Err()`, the serve-exit arm likewise), and a fatal watch error logs
  `reason=watch-error` synchronously from `run()` before returning — the
  record can no longer be skipped by a select arm race.
- **Test races.** `TestRunStdioLifecycleLogs` (and the HTTP lifecycle test)
  now use the mutex-guarded `captureWriter` instead of a polled
  `bytes.Buffer`.
- **Plan drift.** The two `slog.LevelTrace` references (T1, review log) are
  corrected to `levelTrace` (`slog.Level(-8)`) — the stdlib defines no such
  constant; the N1 row and design decision 2 carry the new `transport` field
  and the tee cap.

### Builder — 2026-10-10 (Copilot PR review, rounds 3–4)

Fixed the remaining Copilot findings on PR #38:

- **Same-read tee cap bypass** (`19fb7cf`). The 64 KiB line cap was skipped
  when the over-long line and its newline arrived in one `Read`. `inspect`
  now checks `len(partial)+len(segment)` before appending each pre-newline
  segment; an oversized line is discarded through its newline without ever
  growing `partial` past the cap. `TestStdinIdentityTeeSameReadOversizedLine`
  pins it.
- **Start-failed record contract** (`execute.go`). The `start-failed` path —
  the only failed outcome that lacked the paired TRACE record and the common
  N2 fields — now emits WARN + TRACE from one complete attribute slice:
  `exitCode=-1`, zero byte counts, empty output, `truncated=false`. No
  failed outcome diverges from the documented contract. The start-failed
  test case asserts both records and the full field set.
- **Identity field bound** (`http.go`/`server.go`). `clientName`/
  `clientVersion` were logged unbounded at INFO; a hostile client can carry
  megabytes in `clientInfo` under the 10 MiB body cap. Both call sites now
  run fields through `boundIdentityField` (256 B, rune-boundary cut, explicit
  `…` truncation indicator). Covered by `TestBoundIdentityField`, an
  oversized-name case in the HTTP peek table, and a third handshake with an
  over-long name in `TestStdinIdentityTee`.
- **Test race** (`server_test.go`). `TestRunStdioClientIdentity` passed a
  plain `strings.Builder` to a logger written from the SDK's stdin reader
  goroutine and `run()`'s own; the four sibling run-tests using the same
  pattern were converted as well. All five now use the mutex-protected
  `captureWriter`.
- **Plan state.** T1–T9 marked complete, status flipped to Completed, the N1
  row carries the field bound — the archived plan now matches what shipped.
