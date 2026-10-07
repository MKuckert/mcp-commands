# PLAN — Granular logging (slog)

**Branch:** `feature/logging` from `main` (v0.10.0). **Target version:** 0.11.0.
**Source:** issue MKuckert/mcp-commands#23 + `research/logging-libraries.md` + `research/standard-stream-usage.md`.
**Status:** Approved (T1–T12) — fully approved.

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
- [x] **T7 — Rename + env fallback (user change).** `--log-verbosity` → `--log-level` everywhere (flags.go, flags_test.go, server_test.go, README flag table + Logging section, `-h` usage). Add `LOG_LEVEL` env fallback per design §3 (flag > env > default; empty env = unset; validation error names the value and its source). `flags_test.go`: precedence table (flag wins over env; env used when flag absent; invalid env value fails; empty env ignored; default when neither). README: `LOG_LEVEL` row in the environment-variables table.
- [x] **T8 — Reviewer polish.** (a) `server_test.go` `TestLogVerbosityFiltering`: add the missing `wantError` assertion (error record present at `error` level). (b) Stale comment wording: `discover.go:132-138`, `diagnostic.go:59`, `diagnostic.go:132-136` — reword "stderr warning" / "stderr `Error:`" to the structured-log phrasing. (The server-mode double `level=ERROR` on fatal watch termination stays: pre-existing behavior, T2 mandates no behavior change — note it as a follow-up in the PR description.)
- [x] **T12 — Copilot PR-review remediation.** (a) `flags_test.go`: make `TestParseCLI` hermetic — `t.Setenv(logLevelEnvVar, "")` (empty = unset by design) + drop `t.Parallel()`; in `TestLogLevelPrecedence` set the env unconditionally per case (`t.Setenv(logLevelEnvVar, tt.env)`, empty = unset) so ambient `LOG_LEVEL` cannot leak. (b) `README.md` Logging section: document the exception — flag-parse and validation failures occur **before the logger exists** and stay raw `Error: ...` writes (slog formatting does not apply to startup configuration errors). (c) `loghandler.go` contract fixes: resolve `LogValuer` once via `attr.Value.Resolve()` before the group-kind check and rendering (so `LogValue()` results — redaction, group-valued — are honored); group contract — `WithAttrs` bakes the current group prefix into each new attr's key (group-valued attrs get their *name* prefixed), `WithGroup("")` is a no-op, `Handle` applies `h.group` **only to the record's attrs** (so `logger.With("a",1).WithGroup("g").Info("m","b",2)` renders `a=1 g.b=2`); escape `\n`/`\r` in the **message** (rendered as literal `\n`/`\r`) to preserve the one-line-per-record invariant; include `\r` in `renderString`'s quote check (a CR-only value must quote/escape). Update the doc comment. (d) `loghandler_test.go`: cases for a redacting `LogValuer`, the `a=1 g.b=2` ordering, a message containing `\n` and `\r` (single physical line), a CR-only value (quoted), `WithGroup("")` no-op. (e) `plans/` u1–u6 files: the 8 dangling `REVIEW_REPORT.md` references (removed in c5c3ce6) — annotate as removed, e.g. `REVIEW_REPORT.md (removed)`; **do not restore the file**. Note: Copilot finding [4] (add a mutex around `out.Write`) is **rejected** — the handler builds the full record in a `strings.Builder` and performs a **single** `Write` per record (one `write(2)` call, atomic w.r.t. other writes) with no mutable state, exactly like stdlib `slog.TextHandler`; the test's `lockedWriter` orders captured output, it is not a race shim. (Review 2026-10-06: add `t.Setenv(logLevelEnvVar, "")` to `TestParseCLIDiagnosticIgnoresInvalidServerValidation` — it calls `parseCLI` without the flag and is broken by an invalid ambient `LOG_LEVEL`.)
- [x] **T11 — Message rendering (user request).** In `loghandler.go`: render the message **unquoted** (drop the message quoting; value quoting for whitespace/`"`-bearing values stays). Add a ` | ` separator between the message and the attribute list **only when at least one attribute renders** (including `WithAttrs`-derived ones) — a record with no attributes ends right after the message, no trailing separator. Update `loghandler_test.go` expectations, any test asserting the quoted message form, and the README sample lines. Example: `WARN@18:02:11 ignoring invalid Timeout | file=/path/x.sh error="invalid timeout \"abc\""` and `INFO@18:02:11 Starting stdio server`.
- [x] **T10 — Compact record format (user request).** Replace `slog.NewTextHandler` with a small custom `slog.Handler` (new file, e.g. `loghandler.go`) rendering records as `<LEVEL>@<HH:mm:ss> <message> key=value …` — e.g. `WARN@18:02:11 "ignoring invalid Timeout" file=/path/x.sh error="…"`. Level via `rec.Level.String()` (`DEBUG`/`INFO`/`WARN`/`ERROR`); time `rec.Time.Format("15:04:05")`; message and any value containing whitespace are double-quoted, others `%v`; nested groups flatten to `group.key`. The handler takes the minimum `slog.Level` (replaces `HandlerOptions.Level`) and implements `Enabled`; `WithAttrs`/`WithGroup` return copies; must be safe for concurrent use (stateless or guarded). Wire it into `prodLiveEnv` and **every test fake** (all `slog.NewTextHandler` occurrences); update all `level=WARN`/`level=ERROR`/`level=INFO`/`level=DEBUG` assertions to the new shapes (`WARN@` etc. — keep substring style, never exact lines). Add a dedicated handler unit test: all four levels, quoting rules, group flattening, level filtering via `Enabled`, attrs from `WithAttrs`. README Logging section: sample line + one sentence describing the format. Suite + vet green.
- [x] **T9 — Docs precision (user request).** README Logging section: state explicitly that in **HTTP mode stdout is not used at all** (not merely "not for logs") — one sentence, folded into the existing stdout-reservation paragraph.

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

### Code Reviewer — 2026-10-06 (4e3b41b..1084a41) — **APPROVED**

**Plan compliance.** T7: `--log-verbosity` renamed to `--log-level` in every live site — `flags.go` (flag, help text, `usageLine`, `cliConfig` comment), `flags_test.go` (all table cases, usage-line sync test), `server_test.go` (filtering test renamed to `TestLogLevelFiltering`), `README.md` (Logging section, flags table, env-var table). `grep` for `log-verbosity`/`Verbosity` outside `plans/` returns zero hits. `LOG_LEVEL` env fallback (`flags.go:102`, `logLevelEnvVar` const) with the specified precedence: flag > env > `info`, empty env = unset, `fs.Visit`-tracked `logLevelSet` distinguishes "flag absent" from "flag set to a value". Validation error names value **and** source (`%s must be one of … (got %q)`); `flags_test.go` `TestLogLevelPrecedence` covers all five required cases (flag wins, env used when flag absent, invalid env fails naming source, empty env ignored, default when neither). README env-var table gained the `LOG_LEVEL` row under the existing "consulted only when its flag is not set" header. T8: `TestLogLevelFiltering` now carries a `wantError` column + assertion for all four levels (error record present at `error`); the three stale comment sites (`discover.go`, `diagnostic.go` ×2) reworded to structured-record phrasing.

**Security & stability.** `t.Setenv` is confined to non-parallel tests (`TestLogLevelPrecedence` has no `t.Parallel` and is annotated; the env-mutating `TestParseCLI` case at `flags_test.go:396` predates this delta and is likewise non-parallel); `TestLogLevelFiltering` uses `t.Parallel` but no env mutation. `--version` short-circuit verified **before** the log-level resolution in `parseCLI` (`flags.go`: `if *versionFlag { return }` precedes the `os.Getenv(logLevelEnvVar)` block) — live run `LOG_LEVEL=bogus mcp-commands --version` → exit 0, version on stdout, 0 bytes stderr. No new package-level mutable state (the addition is one `const` + one `bool` in `cliConfig`).

**Completeness.** No residual `--log-verbosity` in code, tests, README, or the usage line; `-h` help text is sensible. **Builder self-noted choices, judged:** (1) help text "(or LOG_LEVEL when the flag is absent; …)" mirrors the established `--api-key` ("alternatives: … MCP_COMMANDS_API_KEY") and `--allowed-origins` ("or set MCP_COMMANDS_…") conventions and is consistent with the README's full precedence statement — accepted. (2) Two commits (feat for rename+fallback, test for `wantError`+comment rewording) is a clean Conventional-Commits split — accepted.

**Findings**

None blocking, none non-blocking. The server-mode double `level=ERROR` on fatal watch termination remains, as the plan explicitly defers to the PR description (pre-existing behavior, T2 no-change mandate).

**Verification commands & outcomes** (with `GOCACHE=/workspace/.goenv/gocache GOPATH=/workspace/.goenv/gopath TMPDIR=/workspace/.goenv/tmp`):
- `go vet ./...` — clean
- `gofmt -l .` — no files
- `go test -count=1 ./...` — `ok github.com/mkuckert/mcp-commands 6.833s`
- Live smoke (built binary): `LOG_LEVEL=bogus --version` → exit 0, stdout only; `LOG_LEVEL=bogus --list-tools` → `Error: LOG_LEVEL must be one of debug, info, warn, or error (got "bogus")`, exit 1; `LOG_LEVEL=warn --log-level debug --list-tools` → flag wins (debug summary emitted).

No findings. T7 and T8 ticked; **plan fully approved (T1–T8)** — PR is ready to open.

### Orchestrator — T9 (49b0c50) — **Approved (inline)**

One-sentence README addition ("In HTTP mode stdout is not used at all — no output of any kind is written there."). Inline review instead of a full reviewer cycle: docs-only diff, sentence verified accurate against the code (server-mode stdout touches are only the `-h`/`--version` diagnostic paths; HTTP responses go to the connection, banners to stderr). T9 ticked; **plan fully approved (T1–T9)**.

### Code Reviewer — 2026-10-06 (77f5391..HEAD) — **APPROVED**

**Handler correctness** (`loghandler.go`). `Enabled` gates on `level >= h.minLevel` (minimum level lives in the constructor, replacing `HandlerOptions.Level`); `Handle` does **not** re-check the level, per contract, and is safe if called directly (it just renders). `WithAttrs` appends to the accumulated attrs and `WithGroup` chains group names (`h.group + "." + name`) — both return fresh values, never mutating the receiver, so the handler is stateless apart from immutable fields and concurrent `Handle` calls are safe without a mutex (confirmed by reading; `go test -race` unsupported in this sandbox). Group flattening verified empirically: nested groups render `outer.inner.k=x`, attrs accumulated **before** `WithGroup` are prefixed (`g.a`, `g.b`), and nested `WithGroup` chains compose (`g.h.a`). All `slog.Value` kinds exercised live: `time.Time` → `"2026-10-06 18:02:11 +0000 UTC"` (quoted: whitespace), `[]int` → `"[1 2 3]"`, struct → `"{x y z}"`, `nil` → `<nil>` (unquoted: no whitespace), `KindExt` → `"[v 1 7]"` — no panics, all sensibly rendered via `Value.String()` + whitespace-triggered `strconv.Quote`. Quote escaping verified: a value containing `\"` renders `"has \"quotes\" and spaces"`. Quoting rule applies to both message and values.

**Plan compliance** vs T10. Format `<LEVEL>@HH:mm:ss message key=value …`, time `"15:04:05"`, level via `rec.Level.String()`, one line + trailing newline — all match. All `slog.NewTextHandler` occurrences replaced (`grep` returns zero in `*.go`); `prodLiveEnv` wires `newLogHandler(os.Stderr, level)`, fakes via `testLogger`/`testDiscardLogger`/`TestLogLevelFiltering` (zero = debug, as before). All `level=WARN|ERROR|INFO` assertions migrated to `WARN@`/`ERROR@` substrings in `diagnostic_test.go`, `server_test.go`, `watch_test.go` (never exact lines). Dedicated unit test covers all four levels, quoting rules, group flattening, `Enabled` filtering, `WithAttrs` copy semantics, and concurrent `Handle`. README: format sentence + updated sample in the Logging section.

**Builder's self-noted deviations, judged.** (1) Whitespace-bearing messages are double-quoted — **this is the plan's stated spec** ("message and any value containing whitespace are double-quoted"); the plan's T10 *example line* showed the message unquoted, which contradicts that spec sentence. Ruling: the behavior is the intent; the example was stale. The T10 example line is corrected above (one line) to `"ignoring invalid Timeout"`. (2) The README auth-section sample (HTTP non-loopback refusal) was also moved to the new format — beyond T10's letter but **required**: it was a stale `time=… level=ERROR msg=…` sample, and leaving it would have left the README internally inconsistent. Accepted.

**Stale-doc sweep.** `grep` for `time=20…`/`level=WARN|ERROR|INFO|DEBUG` in `README.md`/`docs/` → zero hits.

**Findings**

1. **Non-blocking** — `loghandler.go` `writeAttr`: an **empty** `slog.Group` (no members) is silently dropped; the stdlib `TextHandler` would have rendered `key=`. No production site emits an empty group, so this is unobservable today; note it only if empty groups ever appear.
2. **Non-blocking** — the prior reviewer finding (double `level=ERROR` on fatal watch termination in server mode) persists; still deferred to the PR description per the plan, now rendered as two `ERROR@…` records.

**Verification commands & outcomes** (with `GOCACHE=/workspace/.goenv/gocache GOPATH=/workspace/.goenv/gopath TMPDIR=/workspace/.goenv/tmp`):
- `go vet ./...` — clean
- `gofmt -l .` — no files
- `go test -count=1 ./...` — `ok github.com/mkuckert/mcp-commands 6.780s`
- `grep -rn NewTextHandler --include='*.go' .` and `grep -rn 'level=' --include='*.go' .` — zero hits; docs sweep clean (see above).

No blocking findings. T10 ticked; **plan fully approved (T1–T10)** — PR is ready to open.

### Code Reviewer — 2026-10-06 (2e439e6..5ec1359) — **APPROVED**

**T11 spec compliance** (`loghandler.go`). `Handle` renders `rec.Message` verbatim via `fmt.Fprintf(&b, "%s@%s %s", …)` — unquoted even with whitespace (`loghandler.go:47`). The `first` flag (`loghandler.go:48`) is shared across the `h.attrs` loop and the `rec.Attrs` callback, so the ` | ` separator is emitted exactly once, before the first *rendered* attribute, covering record attrs, `WithAttrs`-derived attrs, and group-prefixed keys alike; `writeAttr` (`loghandler.go:82`) leaves `*first` untouched for empty groups, so a record whose attrs all render nothing ends right after the message with no trailing separator or space (`loghandler.go:93-98`). Value quoting/escaping is untouched (`renderString` unchanged); the doc comment for `renderString` was corrected to say *attribute values* only. `Enabled`, `WithAttrs`, `WithGroup`, group flattening, and the concurrency-safety note are all intact (comment reworded in the handler doc to match the new shape).

**Edge cases** — verified empirically with a temporary test (removed after the run): all-attrs-empty-groups renders `INFO@… m\n` (ends after the message, no separator/space); `WithGroup` with attrs inside renders a single ` | ` + flattened `g.g.a=1`; mixed `WithGroup`+`WithAttrs`+record attrs renders exactly one ` | ` then space-separated attrs. All correct.

**Tests** (`loghandler_test.go`). `TestLogHandlerSeparator` covers the three required shapes: record attrs (`m | k=v`), zero attrs (no `|`, no trailing space), and `WithAttrs`-derived (`m | c=1`). `TestLogHandlerQuoting` now asserts the unquoted message (positive `hello world` + negative `"hello world"`) and the unchanged whitespace-value quoting. `TestLogHandlerLevels` assertion tightened to ` hello\n` (pins no trailing separator after the message). Stale-assertion sweep: `grep` for quoted-message patterns (`\"`), `msg=`, `NewTextHandler`, `level=` across `*.go`/`*.md` returns zero hits outside `plans/` and `research/` (historical documents) and the T10-era hits in the plan's own review log.

**Docs** (`README.md`). Logging-section format sentence updated (unquoted message, conditional ` | `) and sample line re-shaped (`… Timeout | file=…`); the auth-section sample matches the new rendering exactly — the message is unquoted, and the inner `"0.0.0.0"` quotes are literal characters in the message, which is what the handler emits. Both consistent with the handler's doc comment and the example line in `loghandler.go`.

**Findings**

1. **Non-blocking** — `loghandler_test.go` `TestLogHandlerSeparator` covers single-source attribute sets only; the two *mixed* cases (handler-accumulated `h.attrs` + record attrs in one record; all-attrs-empty-groups) are behaviorally correct (verified above) but uncommitted-tested. The plan's required test cases are all present, so this is a coverage nicety, not a spec gap.
2. **Non-blocking** — the standing follow-up persists: server-mode fatal watch termination emits two `ERROR@…` records (deferred to the PR description per the T2 no-change mandate).

**Verification commands & outcomes** (with `GOCACHE=/workspace/.goenv/gocache GOPATH=/workspace/.goenv/gopath TMPDIR=/workspace/.goenv/tmp`):
- `go vet ./...` — clean
- `gofmt -l .` — no files
- `go test -count=1 ./...` — `ok github.com/mkuckert/mcp-commands 7.113s`
- Temporary edge-case test (`go test -run TestEdgeVerify -v`, file removed after): all-empty-groups → `INFO@… m\n`; WithGroup-prefixed → `INFO@… m2 | g.g.a=1\n`; mixed → `INFO@… m3 | g.c=1 g.k=\"v v\"\n` — all as specified.
- `git status` — clean; no files modified by this review besides the plan.

No blocking findings. T11 ticked; **plan fully approved (T1–T11)** — PR is ready to open.

### Code Reviewer — 2026-10-06 (c09cf7c..1f3f49f) — **REJECTED** (T12)

**What is right.**
- **(a) Hermeticity, the two named tests.** `TestParseCLI` (`flags_test.go:15-18`) drops `t.Parallel()` and adds `t.Setenv(logLevelEnvVar, "")` with a correct rationale comment (empty = unset by the resolution logic). `TestLogLevelPrecedence` (`flags_test.go:459-465`) now sets the env unconditionally per case; the redundant `envSet` field is removed and the `env` field comment updated. Verified: both pass under an ambient `LOG_LEVEL=bogus`.
- **(b) README** (`README.md:262`): the exception sentence is present and accurate — `main.go` performs flag-parse and validation failures as raw `fmt.Fprintf(os.Stderr, "Error: %v\n", …)` writes *before* `prodLiveEnv` builds the logger, so slog formatting and `--log-level` genuinely do not apply.
- **(c/d/e) Handler contract** (`loghandler.go`). `writeAttr` resolves `attr.Value.Resolve()` exactly once and uses the result for both the group-kind check and rendering — `LogValue()` results (redaction, group-valued) are honored. `WithAttrs` bakes the current group prefix into each new attr's key, with group-valued attrs getting their *name* prefixed and children preserved. `WithGroup("")` returns the same handler (no dot appended). `Handle` applies `h.group` only to the record's attrs (`h.attrs` are written with `""`), so `With("a",1).WithGroup("g").Info("m","b",2)` renders `m | a=1 g.b=2` and the no-group path is unprefixed. Message CR/LF escaped via `messageEscaper` (one physical line per record); `\r` added to `renderString`'s quote set. **Finding [4] rejection confirmed intact:** no mutex added — the handler builds the full record in a `strings.Builder` and performs a single `Write` per record, stateless apart from immutable fields; the doc comment states exactly this. All four new tests (`TestLogHandlerLogValuer`, `TestLogHandlerGroupContract`, `TestLogHandlerMessageNewlines`, `TestLogHandlerCRValue`) are present and assert the right shapes; all prior T10/T11 cases pass unbroken.
- **plans/.** All 8 dangling `REVIEW_REPORT.md` refs in u1–u6 are annotated `(removed after merge)` (u1×2, u2, u3, u4×2, u5, u6); zero un-annotated refs remain in `plans/` or anywhere else; `REVIEW_REPORT.md` is **not** restored; `plans/2026-10-06-granular-logging.md` is touched only by the docs-only `c09cf7c` (the T12 task line + status) and is clean in `c09cf7c..HEAD`. Commit hygiene is a clean conventional split (fix / test / docs / docs).

**Findings**

1. **BLOCKING** — `flags_test.go:397` `TestParseCLIDiagnosticIgnoresInvalidServerValidation` is not hermetic. It calls `parseCLI` without `--log-level` (lines 401, 413), so the `LOG_LEVEL` env fallback is consulted; with an ambient **invalid** value the required-nil-error assertion at line 401-404 fails. Reproduced: `LOG_LEVEL=bogus go test -run TestParseCLIDiagnosticIgnoresInvalidServerValidation .` → `FAIL`; `LOG_LEVEL=debug` → `ok`. This defeats T12(a)'s stated goal ("so ambient `LOG_LEVEL` cannot leak") for a third env-sensitive test the plan's list missed (it greps for the flag, not for `parseCLI` callers). **Fix:** add `t.Setenv(logLevelEnvVar, "")` at the top of that test (it already uses `t.Setenv`, so no `t.Parallel` concern). The suite is otherwise green without an ambient value, which is why the Builder's own run missed it.

**Verification commands & outcomes** (with `GOCACHE=/workspace/.goenv/gocache GOPATH=/workspace/.goenv/gopath TMPDIR=/workspace/.goenv/tmp`):
- `go test -count=1 ./...` — `ok github.com/mkuckert/mcp-commands 6.500s`
- `go vet ./...` — clean; `gofmt -l .` — no files
- `go test -count=1 -run 'TestLogHandlerLogValuer|TestLogHandlerGroupContract|TestLogHandlerMessageNewlines|TestLogHandlerCRValue' -v .` — all 4 PASS
- `LOG_LEVEL=bogus go test -count=1 -run 'TestParseCLI|TestLogLevelPrecedence' -v .` — both PASS (hermetic); `TestParseCLIDiagnosticIgnoresInvalidServerValidation` in the same ambient env — **FAIL** (finding 1)
- `grep -rn "REVIEW_REPORT" --include='*.md' .` — 8 hits, all annotated; no file restored

T12 not ticked. Correction loop 1 of 3. Status unchanged (T12 in progress).

### Code Reviewer — 2026-10-06 (c09cf7c..934cae4) — **APPROVED** (T12, correction round 1 of 3)

**Delta.** `934cae4` is exactly the requested fix and nothing else: `t.Setenv(logLevelEnvVar, "")` + a two-line rationale comment at the top of `TestParseCLIDiagnosticIgnoresInvalidServerValidation` (`flags_test.go`), 3 insertions, 1 file. No other files touched; no other test or code changes. `git status` clean (plan file aside).

**Verification commands & outcomes** (with `GOCACHE=/workspace/.goenv/gocache GOPATH=/workspace/.goenv/gopath TMPDIR=/workspace/.goenv/tmp`):
- `go test -count=1 ./...` — `ok github.com/mkuckert/mcp-commands 6.212s`
- `LOG_LEVEL=bogus go test -count=1 .` — `ok github.com/mkuckert/mcp-commands 7.066s` (the previously failing test now passes under an invalid ambient `LOG_LEVEL`; the whole suite is hermetic in this respect)
- `go vet ./...` — clean
- `gofmt -l .` — no files

Blocking finding 1 resolved; no new findings. T12 ticked; status set to **fully approved (T1–T12)** — PR is ready to open.

### Standing follow-up resolved (2026-10-07, `fix/watch-double-error`)

The double `ERROR@…` record on a fatal `--watch` termination in server mode is fixed: `run()` no longer logs the raw watch error — it returns it and `main()` is the single reporting site (one `failed to watch scripts directory: <cause>` record). `TestRunWatchFatalMidRun` pins the invariant (zero `ERROR@` records from `run()` itself). Diagnostic mode was already single-logged; no README change (format unchanged, count only).
