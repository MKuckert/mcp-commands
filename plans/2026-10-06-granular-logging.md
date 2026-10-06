# PLAN — Granular logging (slog)

**Branch:** `feature/logging` from `main` (v0.10.0). **Target version:** 0.11.0.
**Source:** issue MKuckert/mcp-commands#23 + `research/logging-libraries.md` + `research/standard-stream-usage.md`.
**Status:** In progress (review pending).

## Design decisions (locked)

1. **Library:** stdlib `log/slog` with `slog.NewTextHandler`. **No new `go.mod` dependency** — slog is stdlib. Tint TTY colors are deliberately omitted: the issue requires level filtering only, research marks colors optional, and zero dependencies is the stated preference.
2. **Streams:** every log record goes to **stderr, in all modes**. stdout remains program output only: the MCP protocol in stdio mode; tool list, `--call-tool` result text, `-h` help, and `--version` in diagnostics stay raw `fmt` writes to stdout. No level router — one handler on stderr at the configured minimum level (research: "For a simple case one handler on stderr plus a level setting in code is enough").
3. **Flag:** `--log-verbosity <level>` — string flag, one of `debug` | `info` | `warn` | `error`; **default `info`** ("info and more" per the issue). Accepted in every mode. No env-var fallback (code-only config is a research requirement). An invalid value is a fail-fast parse-time validation error on the regular `Error:`/exit 1 path.
4. **Injection:** `liveEnv` gains `log *slog.Logger`, **replacing the `stderr io.Writer` field** (after this change no production site writes raw to `env.stderr`; keeping a dead field is worse). `prodLiveEnv(level slog.Level)` builds `slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))`. Tests construct loggers over `bytes.Buffer`. Never use `slog.SetDefault` or other global state — keeps `t.Parallel()` safe.
5. **Site mapping:** keep the existing human-facing wording, minus the `Warning:`/`Error:` prefixes (slog renders the level); natural values become structured fields:
   - `Warning: X` → `log.Warn("X", ...)` (discovery, watch, HTTP policy, "no executable scripts")
   - `Error: X` (operational) → `log.Error("X", ...)` (diagnostic run failures, fatal watch termination, server run failures)
   - Banners → `log.Info(...)` (`Starting stdio server`, `Starting HTTP server on ...` with notes as fields)
   - `Note: ignoring server-mode flags in diagnostic mode` → `log.Warn(...)`
   - `main.go` flag-parse errors (before any logger exists) keep direct `fmt.Fprintf(os.Stderr, ...)` writes — the verbosity value itself may be what's invalid.
   - `main.go` `run()` failure → `env.log.Error(...)`, exit code unchanged.
6. **New debug records** (so `--log-verbosity debug` has substance; debug-level only, no new info/warn content): a `discoverTools` summary (count + scripts dir); `watchChanges` rescan firing (after debounce); reattach attempts in the watch loop.
7. **Signatures:** `discoverTools(scriptsDir string, log *slog.Logger)`, `extractFrontmatter(filePath string, log *slog.Logger)`, `warnParam(..., log *slog.Logger)`, `parseParamAnnotation(..., log *slog.Logger)` — the `stderr io.Writer` parameters are replaced by the logger.

## Tasks

- [ ] **T1 — Flag.** `flags.go`: add `--log-verbosity` (string, default `info`), validate the value, store the resolved `slog.Level` in `cliConfig`. `flags_test.go`: table test over all four values, the default, and an invalid value (error text names the flag and the bad value).
- [ ] **T2 — Logger plumbing + site conversion.** `server.go`: `liveEnv.log *slog.Logger` replaces `stderr`; `prodLiveEnv(level slog.Level)`. `main.go`: resolve level from `cfg`, build env, `run()` failure via `env.log.Error`. Convert every production stderr site in `main.go`, `diagnostic.go`, `discover.go`, `server.go`, `watch.go` per design §5, including the signature changes in §7. No behavior change beyond the text format (`time=... level=WARN msg=...`).
- [ ] **T3 — Debug records** per design §6.
- [ ] **T4 — Tests.** Update every existing test that asserts the old `Warning:`/`Error:`/banner text: assert on `level=WARN`/`level=ERROR`/`level=INFO` plus the message substring, never exact whole lines (timestamps vary). Test fakes build the logger on a `bytes.Buffer`. Add: level-filtering tests (a debug record is captured at `debug`, absent at `info`; a warn record is captured at `info`, absent at `error`). Full suite `go test ./...` + `go vet ./...` green.
- [ ] **T5 — Documentation.** `README.md`: new **Logging** section (levels, `--log-verbosity`, default `info`, stderr-only routing with the stdio-protocol rationale, one sample output line); `--log-verbosity` row in the flags table; update every prose mention of the old `Warning: ...`-to-stderr format (Timeouts, frontmatter `Param:`/`Timeout:`, duplicate names, watch) to describe the new structured format; version references `0.10.0` → `0.11.0`. `Makefile`: `VERSION ?= 0.11.0`. No `go.mod` change (slog is stdlib) — state this in the PR description.
- [ ] **T6 — Final.** Full suite + vet green, plan updated, PR ready for the Code Reviewer.

## Review log

_(pending)_
