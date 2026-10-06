# PLAN — Granular logging (slog)

**Branch:** `feature/logging` from `main` (v0.10.0). **Target version:** 0.11.0.
**Source:** issue MKuckert/mcp-commands#23 + `research/logging-libraries.md` + `research/standard-stream-usage.md`.
**Status:** Approved (T1–T6); T7–T8 in progress (user change: flag renamed, env fallback added).

## Design decisions (locked)

1. **Library:** stdlib `log/slog` with `slog.NewTextHandler`. **No new `go.mod` dependency** — slog is stdlib. Tint TTY colors are deliberately omitted: the issue requires level filtering only, research marks colors optional, and zero dependencies is the stated preference.
2. **Streams:** every log record goes to **stderr, in all modes**. stdout remains program output only: the MCP protocol in stdio mode; tool list, `--call-tool` result text, `-h` help, and `--version` in diagnostics stay raw `fmt` writes to stdout. No level router — one handler on stderr at the configured minimum level (research: "For a simple case one handler on stderr plus a level setting in code is enough").
3. **Flag + env:** `--log-level <level>` (renamed from `--log-verbosity` per user instruction) — string, one of `debug` | `info` | `warn` | `error`; **default `info`** ("info and more" per the issue). Accepted in every mode. **`LOG_LEVEL` env var is a fallback** (name as instructed by the user, no `MCP_COMMANDS_` prefix): consulted only when the flag is not set; an empty env value counts as unset. Precedence: `--log-level` > `LOG_LEVEL` > `info`. An invalid value (from either source) is a fail-fast parse-time validation error on the regular `Error:`/exit 1 path, naming the offending value and its source.
4. **Injection:** `liveEnv` gains `log *slog.Logger`, **replacing the `stderr io.Writer` field** (after this change no production site writes raw to `env.stderr`; keeping a dead field is worse). `prodLiveEnv(level slog.Level)` builds `slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))`. Tests construct loggers over `bytes.Buffer`. Never use `slog.SetDefault` or other global state — keeps `t.Parallel()` safe.
5. **Site mapping:** keep the existing human-facing wording, minus the `Warning:`/`Error:` prefixes (slog renders the level); natural values become structured fields:
   - `Warning: X` → `log.Warn("X", ...)` (discovery, watch, HTTP policy, "no executable scripts")
   - `Error: X` (operational) → `log.Error("X", ...)` (diagnostic run failures, fatal watch termination, server run failures)
   - Banners → `log.Info(...)` (`Starting stdio server`, `Starting HTTP server on ...` with notes as fields)
   - `Note: ignoring server-mode flags in diagnostic mode` → `log.Warn(...)`
   - `main.go` flag-parse errors (before any logger exists) keep direct `fmt.Fprintf(os.Stderr, ...)` writes — the verbosity value itself may be what's invalid.
   - `main.go` `run()` failure → `env.log.Error(...)`, exit code unchanged.
6. **New debug records** (so `--log-level debug` has substance; debug-level only, no new info/warn content): a `discoverTools` summary (count + scripts dir); `watchChanges` rescan firing (after debounce); reattach attempts in the watch loop.
7. **Signatures:** `discoverTools(scriptsDir string, log *slog.Logger)`, `extractFrontmatter(filePath string, log *slog.Logger)`, `warnParam(..., log *slog.Logger)`, `parseParamAnnotation(..., log *slog.Logger)` — the `stderr io.Writer` parameters are replaced by the logger.

## Tasks

- [x] **T1 — Flag.** `flags.go`: add `--log-verbosity` (string, default `info`), validate the value, store the resolved `slog.Level` in `cliConfig`. `flags_test.go`: table test over all four values, the default, and an invalid value (error text names the flag and the bad value).
- [x] **T2 — Logger plumbing + site conversion.** `server.go`: `liveEnv.log *slog.Logger` replaces `stderr`; `prodLiveEnv(level slog.Level)`. `main.go`: resolve level from `cfg`, build env, `run()` failure via `env.log.Error`. Convert every production stderr site in `main.go`, `diagnostic.go`, `discover.go`, `server.go`, `watch.go` per design §5, including the signature changes in §7. No behavior change beyond the text format (`time=... level=WARN msg=...`).
- [x] **T3 — Debug records** per design §6.
- [x] **T4 — Tests.** Update every existing test that asserts the old `Warning:`/`Error:`/banner text: assert on `level=WARN`/`level=ERROR`/`level=INFO` plus the message substring, never exact whole lines (timestamps vary). Test fakes build the logger on a `bytes.Buffer`. Add: level-filtering tests (a debug record is captured at `debug`, absent at `info`; a warn record is captured at `info`, absent at `error`). Full suite `go test ./...` + `go vet ./...` green.
- [x] **T5 — Documentation.** `README.md`: new **Logging** section (levels, `--log-verbosity`, default `info`, stderr-only routing with the stdio-protocol rationale, one sample output line); `--log-verbosity` row in the flags table; update every prose mention of the old `Warning: ...`-to-stderr format (Timeouts, frontmatter `Param:`/`Timeout:`, duplicate names, watch) to describe the new structured format; version references `0.10.0` → `0.11.0`. `Makefile`: `VERSION ?= 0.11.0`. No `go.mod` change (slog is stdlib) — state this in the PR description.
- [x] **T6 — Final.** Full suite + vet green, plan updated, PR ready for the Code Reviewer.
- [ ] **T7 — Rename + env fallback (user change).** `--log-verbosity` → `--log-level` everywhere (flags.go, flags_test.go, server_test.go, README flag table + Logging section, `-h` usage). Add `LOG_LEVEL` env fallback per design §3 (flag > env > default; empty env = unset; validation error names the value and its source). `flags_test.go`: precedence table (flag wins over env; env used when flag absent; invalid env value fails; empty env ignored; default when neither). README: `LOG_LEVEL` row in the environment-variables table.
- [ ] **T8 — Reviewer polish.** (a) `server_test.go` `TestLogVerbosityFiltering`: add the missing `wantError` assertion (error record present at `error` level). (b) Stale comment wording: `discover.go:132-138`, `diagnostic.go:59`, `diagnostic.go:132-136` — reword "stderr warning" / "stderr `Error:`" to the structured-log phrasing. (The server-mode double `level=ERROR` on fatal watch termination stays: pre-existing behavior, T2 mandates no behavior change — note it as a follow-up in the PR description.)

## Review log

### Code Reviewer — 2026-10-06 (bd82c96..439244c) — **APPROVED**

**Plan compliance.** All design decisions honored: stdlib `slog` only, `go.mod`/`go.sum` untouched (diff is 0 lines), no `slog.SetDefault` or other global state anywhere, `liveEnv.stderr` replaced by `log *slog.Logger`, `prodLiveEnv(level slog.Level)` builds one `slog.NewTextHandler(os.Stderr, {Level: level})` handler (no level router). `--log-verbosity` (string, default `info`) resolved via `resolveLogLevel` in every mode; invalid value → `--log-verbosity must be one of debug, info, warn, or error (got %q)` on the regular `Error:`/exit-1 path. All §5 site mappings match (banners → Info, warnings → Warn, operational → Error, `Note:` → Warn, main.go pre-logger parse errors stay raw `fmt.Fprintf(os.Stderr, …)`). All §7 signatures converted. Cross-checked the research inventory site-by-site: every diagnostic stderr site in `main.go`, `diagnostic.go` (9), `discover.go` (8), `server.go` (6), `watch.go` (3) is now a slog record; `grep` confirms zero `env.stderr` / raw `os.Stderr` writes left in non-`main.go` production code. All result-content stdout sites (tool list, call result, `-h`, `--version`, clear-screen) unchanged.

**Security & stability.** Logger is a value-injected, immutable object held in the per-run `liveEnv`; no package-level mutable logger, so `t.Parallel()` remains safe (85 `t.Parallel` sites intact) and the shared `testDiscardLogger` is safe by slog immutability. `slog` itself is goroutine-safe for concurrent `Log` calls from the watch goroutine and serve path. Stdio-protocol invariant verified live: stdio server run produced `level=WARN`/`level=INFO` records on stderr and **0 bytes** on stdout. Exit codes unchanged (verified: invalid verbosity exits 1, `--list-tools` exits 0, version/help exit 0). Watch-lifecycle semantics untouched — the diff is strictly format-level at each site.

**Completeness.** Debug records per §6 present: `discoverTools` summary (`count`+`scriptsDir`), `watchChanges` rescan after debounce, reattach attempts. Level-filtering test `TestLogVerbosityFiltering` pins all four levels × debug/info/warn gating. All old `Warning:`/`Error:`/`Note:`/banner assertions converted to `level=WARN|ERROR` + message substrings (never exact lines); fakes build loggers on `bytes.Buffer`/`captureWriter` via `testLogger`/`liveEnvFor`. Docs: README Logging section (levels, flag, default, stderr-only rationale with the protocol argument, sample record line), `--log-verbosity` row in the flags table, all `Warning: …`-era prose updated (Timeouts, `Param:`/`Timeout:` frontmatter, duplicates, watch, HTTP policy incl. the `WARNING:`-prefix fix in `439244c`), `0.10.0` → `0.11.0` in README and `Makefile`. `go.mod` unchanged.

**Test quality.** Assertions are substring-on-`level=`+message as required; table tests robust; no exact-line assertions introduced. Full suite + vet + gofmt green (commands below).

**Findings**

1. **Non-blocking** — `server.go:174` + `main.go:68`: a fatal watch termination in server mode emits **two** `level=ERROR` records (raw watch error inside `run()`, then the wrapped `failed to watch scripts directory: …` in `main`). The duplication predates this change (two `Error:` lines before) and T2 mandates no behavior change, so keeping it is compliant — but a follow-up could collapse it to one record. Diagnostic-mode watch failure (`diagnostic.go:187`) logs only once; only the server path double-logs.
2. **Non-blocking** — `server_test.go` `TestLogVerbosityFiltering`: the table asserts debug/info/warn capture per level but has no `wantError` field, so the presence of the `error` record at `error` level is unasserted. The plan's required cases (debug at debug/absent at info; warn at info/absent at error) are all covered.
3. **Non-blocking** — stale comment wording: `discover.go:132-138`, `diagnostic.go:59`, `diagnostic.go:132-136` still say "stderr warning" / "stderr `Error:`". Descriptively still true (records do go to stderr); a cosmetic cleanup candidate.

**Verification commands & outcomes** (all in `/workspace/mcp-commands-logging`, with `GOCACHE=/workspace/.goenv/gocache GOPATH=/workspace/.goenv/gopath TMPDIR=/workspace/.goenv/tmp`):
- `go vet ./...` — clean
- `gofmt -l .` — no files
- `go test ./...` — `ok github.com/mkuckert/mcp-commands 6.028s`
- Live smoke: `--log-verbosity verbose` → `Error: --log-verbosity must be one of debug, info, warn, or error (got "verbose")`, exit 1; `--list-tools` at default `info` → no debug record; at `debug` → 1 `level=DEBUG` discovery-summary record; `--version`/`-h` → stdout only; stdio server → banner/warning on stderr, 0 bytes stdout.

No blocking findings. T1–T6 ticked, status set to **Approved**.
