# REVIEW — mcp-commands v0.7.0

## For future agents (how to process this file)

- This is the **active findings queue** from the 2026-09-29 full-workspace review (5 specialist reviews, all findings verified against source at that date, quality-passed).
- **Work the findings in the Suggested execution order** (Tier 1 → 4). Tier 1 is a release blocker; do not release v0.8+ with F-1..F-4 open.
- Line numbers refer to `main.go`/`main_test.go` as of **v0.7.0 (commit 11c2030)**. After any change, **re-locate by symbol name, not line number** — treat the lines as a starting point only.
- When a finding is fixed, mark its row `F-n ✅ <date> (commit <short-sha>)` in place and note any deviation from the proposed fix. Keep IDs stable; never renumber.
- Findings may interact: F-2 subsumes F-12 (do together), F-10 depends on F-14, F-20 depends on F-14 + F-15. Respect those dependencies.
- Conventions: Conventional Commits, include the related plan in the commit (see `AGENTS.md`), run `go vet ./...` + `go test ./...` (note: `-race` is unsupported in this sandbox — see F-22's stand-in).

---

## Full Workspace Review — mcp-commands v0.7.0

**Date:** 2026-09-29
**Scope:** `main.go` (1468 lines), `main_test.go` (2898 lines), `README.md`, `PLAN.md`, `plans/*`, `Makefile`, `.goreleaser.yaml`, `.github/workflows/release.yml`, `opencode.jsonc`, `tui.jsonc`, `.zed/debug.json`, `go.mod`.
**Method:** five specialist reviews run as parallel subagents — Documentation, Code Structure, Security, Testing, Performance — plus a direct cross-verification of every "High" finding against `main.go` line numbers, followed by a Code-Reviewer quality pass on this document (5 factual corrections applied). Status: **all findings verified in source; no files changed.** Inline IDs (`SEC-n`, `PERF-n`, `structure #n`, `Doc #n`, `Test #n`) refer to those per-agent reports, retained in the original review session transcript (this document is self-contained; the IDs are provenance only). Canonical archive copy: `plans/2026-09-29-full-workspace-review.md`.

## Overall grades (harsh)

| Area | Grade | One-liner |
|---|---|---|
| Correctness (logic) | B+ | Overflow guards, context wiring, injection closure, CORS strictness all sound. |
| Security (operational envelope) | C+ | Protocol boundary is A−; HTTP mode is not production-safe: silent unauthenticated RCE on non-loopback, no concurrency cap, no server/body limits. |
| Performance | B− | One High (unbounded output capture, measured ~3× amplification); everything else localized. |
| Tests | B− | Above-average (42 fns, `go vet` clean, all pass in ~10 s), but the 1 MiB truncation branch, fsnotify failure modes, and all of `main`/`run` are untested; `-race` cannot run here. |
| Code structure | C+ | One 1468-line file, three mutable injection globals, a test-only production field, a dead parameter, a double startup scan, two 9–11-parameter functions. |
| Documentation | B− | No fabricated claims, but `--version`/release install/troubleshooting missing, "raw output" misdescribed, stale Makefile + ZED config, and a committed API key. |

**Overall: B− / "good core, unsafe shell, monolith-shaped."** The logic quality is the strongest asset in the repo. The operational envelope around the HTTP mode is where this would get exploited.

---

## Findings, ranked by severity × complexity

Severity: **H**igh / **M**edium / **L**ow. Complexity: **T**rivial (≤ ~20 lines) / **S**mall / **M**edium / **L**arge. Tiers are ordered "do first" → "do later". Duplicate findings from multiple agents are merged (original IDs kept).

### Tier 1 — High severity, low complexity (do now)

| # | Sev/Comp | Finding | Location | Fix |
|---|---|---|---|---|
| F-1 ✅ 2026-09-30 (commit 459a9ba) | H/T | **Unauthenticated HTTP on non-loopback starts silently** (SEC-2). Auth wraps only `if token != ""`; the SDK's DNS-rebinding protection applies only to loopback binds. `--host 0.0.0.0 --port 8080` without `--api-key` boots a remote command-execution endpoint with no warning in the startup log. | `main.go:1318, 1404, 1437–1460` | When `host` is non-loopback and `token == ""`: refuse to start, or require an explicit `--insecure-no-auth`; at minimum print a loud `WARNING: UNAUTHENTICATED, bound to <non-loopback>` line. Done: refuse-to-start by default + `--insecure-no-auth` escape hatch (both options in the proposal); unparseable/non-IP hosts treated as non-loopback. |
| F-2 ✅ 2026-09-30 (commit 459a9ba) | H/T | **Unbounded stdout/stderr capture** (PERF-1 ≡ SEC-4). `cmd.Stdout/Stderr` are plain `bytes.Buffer`s; the 1 MiB cap is applied only to the final string. Measured: 100 MiB tool output → ~452 MiB RSS (~3× amplification through `combineToolOutput`); `--no-timeout` makes it unbounded in time too; compounds F-4. | `main.go:871–880, 475–508` | Bounded writer per stream (stop appending past `maxToolOutputBytes`, remember truncation flag); `combineToolOutput` then just formats. Also makes truncation UTF-8-safe (see F-12). Done: `boundedWriter` per stream; deviation — `combineToolOutput` keeps the existing final 1 MiB cap (user-visible behavior unchanged) on top of the per-stream bound. F-12 in the same commit. |
| F-3 ✅ 2026-09-30 (commit 459a9ba) | H/T | **No HTTP server limits** (SEC-3 ≡ PERF-3 + PERF-4). `http.Server{Addr, Handler}` has no `ReadHeaderTimeout`/`IdleTimeout` (slow-loris pins goroutines); the SDK does `io.ReadAll(req.Body)` with no limit (multi-GB chunked body → RAM exhaustion). | `main.go:1439, 1296–1310` | Set `ReadHeaderTimeout: 5s`, `IdleTimeout: 2m` (not `ReadTimeout`/`WriteTimeout` — must exceed 5-min tool calls); wrap the handler with `http.MaxBytesReader` (e.g. 10 MiB) before the SDK handler. Done exactly as proposed. |
| F-4 ✅ 2026-09-30 (commit 459a9ba) | H/S | **No cap on concurrent tool executions** (SEC-1). Every `tools/call` spawns a subprocess; with `Timeout: NONE` a few hundred pinned sessions → PID exhaustion / OOM. | `main.go:849–908` | `semaphore.Weighted` (default 8–32, flag-configurable) around Start/Wait; saturated calls return a clean `IsError` "at capacity" result. Done; deviation — hand-rolled buffered-channel slot (`execSlot`) instead of `golang.org/x/sync/semaphore` (no new dependency); default 16, `--max-concurrent` flag; slot acquired after arg validation. |
| F-5 ✅ 2026-09-30 (commit bebf241) | H/T | **Plaintext API key committed** in `opencode.jsonc` (`mnfst_…` for the "Manifest" provider); `.gitignore` does not exclude it. If this repo is ever public (`.goreleaser.yaml` + release workflow suggest it is intended to be), the secret leaks. | `opencode.jsonc`, `.gitignore` | Rotate the key, scrub history if public, add the file to `.gitignore` / move config out of VCS. Done: `git rm --cached` + `.gitignore` (local working copy untouched). Outstanding user actions: rotate the key; scrub history if the repo is made public. |
| F-6 ✅ 2026-09-30 (commit 5c87075) | H/T | **The 1 MiB output-truncation branch has zero tests** — the biggest test gap: `combineToolOutput` > 1 MiB path and `executeTool` with huge stdout are untested, and it is user-visible behavior (a corrupt/truncated rune at the boundary is possible today). | `main.go:501–507`, `main_test.go` | `TestCombineToolOutputTruncation` (2 MiB in, boundary at exactly 1 MiB, warning suffix), `TestExecuteToolHugeStdout`, unicode/no-trailing-newline case. Done: `TestCombineToolOutputTruncation` (incl. rune-split boundary), `TestExecuteToolHugeStdout` (5 MiB), `TestBoundedWriter`, `TestExecSlot`, `TestToolCapacityViaRegistry`, `TestHTTPSecurityPolicy`. |

### Tier 2 — Medium severity, low–medium complexity

| # | Sev/Comp | Finding | Location | Fix |
|---|---|---|---|---|
| F-7 ✅ 2026-10-02 (commit 99fdf3b) | M/M | **Timeout kills only the direct child** (SEC-5 ≡ PERF-5). Shell scripts spawning children (the canonical tool shape) keep grandchildren running after the deadline — the 5 m budget is not a real budget. | `main.go:868` | `SysProcAttr{Setpgid: true}` + kill `−pid` on deadline + `cmd.WaitDelay` backstop. |
| F-8 ✅ 2026-10-02 (commit be0558f) | M/T | **Token on the command line** (SEC-6): world-readable via `/proc/<pid>/cmdline` for the server's lifetime. | `main.go:1320, 1110` | Added `--api-key-file` (content trimmed; unreadable file = startup error); README documents the `/proc` exposure and recommends the file/env over `--api-key <value>`. The flag is **not** deprecated and no runtime warning is printed (user decision: the README note is enough). |
| F-9 ✅ 2026-10-02 (commit 46af946) | M/M | **fsnotify failure modes untested** (Test #2–5): watched dir deleted mid-watch, rename events, permission errors, tool *removal* from the registry. The code's doc comments promise exactly this resilience; it is verified by inspection only. | `main.go:756–831` | `TestWatchChangesWatchedDirDeleted`, `…RenameTriggersChange`, `…PermissionError`, `TestWatchToolsRemovesDeletedTool`. |
| F-10 ✅ 2026-10-02 (commit 71b51e8) | M/M | **`main` and `run` are 100% untested** (Test #19): flag exclusivity, fail-fast branches, HTTP bind, graceful shutdown, zero-tools warning. `main` needs a small refactor (`os.Exit` blocks it); `run` deserves one end-to-end HTTP test. | `main.go:1314–1468` | Extract `parseCLI` (see F-14) + one `httptest` end-to-end against `run`. |
| F-11 ✅ 2026-10-02 (commit d936961) | M/M | **Full re-registration with no change-diff** (PERF-2). The debounce is sound (a burst within the 100 ms window collapses to one scan), but each debounced change — even when the resulting tool set is identical — triggers a full `discoverTools` + `registry.remove-all`/`AddTool`-per-tool + per-tool `json.Marshal`, and every `AddTool` emits a separate SDK `tools/list_changed` notification (N per reload). | `main.go:767–806, 529–580` | Diff new vs current tool set; skip `replace` when identical (also covers PERF-8 schema re-marshal); consider re-arming the debounce per event for slower bursts. |
| F-12 ✅ 2026-09-30 (commit 459a9ba, Tier 1) | M/S | **Truncation can split a UTF-8 rune**, emitting invalid bytes to the LLM. | `main.go:501–507` | Back off to `utf8.FullRune` boundary after truncation (1 line, do as part of F-2). |
| F-13 | M/M | **Symlink/path TOCTOU** (SEC-7): `resolvedPath` is stat/exec-checked at discovery, re-used at exec with no re-validation; a party writable on the scripts dir can swap a symlink target in the window. Also the scripts-dir trust boundary is undocumented. | `main.go:88–118, 868` | Re-`Stat`/`EvalSymlinks` + regular-file + exec-bit at exec time, reject on change; open frontmatter `O_NOFOLLOW`; document "write access to scripts dir = code execution as the server user" in README. |

### Tier 3 — Low severity, structural / maintainability

| # | Sev/Comp | Finding | Location | Fix |
|---|---|---|---|---|
| F-14 | —/M | **9–11 positional parameters** on `run(...)` and `runDiagnostic(...)`; `main()` is 85 lines of flag bookkeeping (3 hand-rolled `*Set` bools + `visited` map). | `main.go:1400, 960, 1314–1398` | Introduce `serverConfig` / `diagnostic` structs (the existing `corsConfig` is the in-file precedent); `main()` becomes ~25 lines. Prerequisite for F-10. |
| F-15 | —/M | **Three mutable package-level function variables** for test injection (`resolveWrapWidth`, `clearScreen`, `notifySignals`) — blocks `t.Parallel()`, and `run()` inconsistently calls `signal.NotifyContext` directly. | `main.go:610, 935, 945` | One `liveEnv` struct injected into `runListTools`/`run`; tests construct a fake. Unblocks parallel tests. |
| F-16 | —/T | **Dead code**: `toolRegistry.lastHandler` (written every `replace`, read only by one test); `watchTools(ctx, dir, registry, interval)` — `interval` never read; `watchToolsInterval` constant exists only to feed it. | `main.go:519, 568, 813, 42` | Delete field (route the test through the in-memory client session like its siblings) + delete param & constant. |
| F-17 | —/T | **Double initial discovery at startup** (structure #7 ≡ PERF-7): `run()` does `discoverTools` + `replace`, then `watchTools` repeats the identical scan + replace (removing/re-adding identical tools, N+1 notifications at boot). | `main.go:1411–1427, 814–820` | Pass already-discovered tools into `watchTools`; skip its initial scan. |
| F-18 | —/S | **Local simplifications**: `parseParamAnnotation` 72 lines with the 5-line warning block duplicated 5× + `var typeOk bool` anti-pattern → `warnParam` helper + set lookup; `extractFrontmatter` `strings.Contains`+`SplitN` → `strings.Cut`; hand-rolled 48-line `parseTimeoutDuration` → anchored-regex token loop (keep overflow guards); four near-identical `CallToolResult` constructions → `textResult(text, isErr)`; loop var `discoveredTool` shadows its type. | `main.go:278–349, 129–183, 199–253, 849–908, 538` | Mechanical; net −~80 lines. |
| F-19 | —/L | **`discoverTools` name collisions**: `a.sh` + `a.py` both register as tool `a`; silent; duplicate `AddTool` unchecked. | `main.go:77–115` | First wins + stderr warning (matches house style). |
| F-20 | —/L | **File split** (structure report): the file is ~6 self-contained subsystems with no shared state beyond 3 types. Proposed 12-file layout: `main.go` (120), `flags.go` (180), `discover.go` (210), `timeout.go` (110), `schema.go` (140), `execute.go` (110), `registry.go` (80), `list.go` (180), `call.go` (130), `http.go` (130), `server.go` (80), `watch.go` (90); test files mirror 1:1. | `main.go` | Do after F-14/F-15 land; mechanical, low risk. |
| F-21 | —/S | **Docs gaps** (Doc #1–5, #1–2 of stale): `--version` undocumented; no consolidated flags/env table (12 flags + 3 env vars scattered); no release-binary install path (releases exist in CI); no troubleshooting section (exec-bit, frontmatter warnings, `--list-tools`); "Raw Output" misdescribed (output is `<stdout>`/`<stderr>`-tagged + truncation notice, not raw); arg-translation example implies insertion order but keys are sorted; auth "every HTTP request" overclaim vs preflight bypass. | `README.md` | ~5 targeted edits. |
| F-22 | —/S | **Test hygiene**: script-fixture writing duplicated (one `writeScript` closure vs 35 ad-hoc `WriteFile`s); injected-env save-swap-restore boilerplate in 2 subtests; hand-rolled 2 s/20 ms polling loops in ≥5 tests (flake risk in CI) → `waitFor(t, …)`; `TestBuildInputSchema` ~200 lines → table; `TestBuildHTTPHandlerAuthDisabled` asserts only "not 401"; no `t.Parallel()` anywhere (blocked by F-15); magic `5*time.Minute` in 28 call sites; no benchmarks exist. | `main_test.go` | ~600–700 line reduction; add `TestRegistryReplaceConcurrency` as the documented stand-in for the unavailable `-race` run (ThreadSanitizer unsupported in this sandbox — race safety rests on inspection: no races found by security/structure agents). |

### Tier 4 — Low severity, trivial

| # | Sev/Comp | Finding | Fix |
|---|---|---|---|
| F-23 ✅ 2026-10-02 (commit 8766109) | L/T | Stale `Makefile`: `VERSION ?= 0.2.0` while code is 0.7.0 — `make` builds report the wrong version (release binaries are unaffected: `.goreleaser.yaml` has no version injection and uses the hardcoded constant). Also `buildall` missing from `.PHONY`. | Bump / add. |
| F-24 | L/T | Stale `.zed/debug.json`: `src/` paths that no longer exist — launch config fails. | Update to repo root. |
| F-25 | L/T | Root `PLAN.md` (369 lines, fully completed, "Approved") not archived, inconsistent with the `plans/` convention; `plans/2026-06-30-initial.md` describes `--ip`, `src/`, `os.Chdir`, 10-line scan — all superseded, no "superseded" banner. | Archive root PLAN.md; one-line banners on superseded plans. |
| F-26 | L/T | README fluff: "AI Usage" section (zero operational value), "adjust package path" vestige. | Cut. |
| F-27 | L/T | `--allow-all-origins` + non-loopback host: add a persistent startup warning (origin-echo footgun; SEC-8). | 3 lines. |
| F-28 | L/T | No TLS: document that HTTP transport is cleartext and must be terminated upstream (SEC-10); optionally `--tls-cert/--tls-key`. | README + flag. |
| F-29 | L/T | Document that tool output is untrusted model input; the `<stdout>` tag wrapper is advisory, and scripts can emit literal `</stdout>` (SEC-9). | README one-paragraph. |
| F-30 | L/T | No vulnerability scanning in CI (deps verified clean by hand today: go-sdk v1.6.1 past both 2026 advisories; fsnotify CVE is a kernel issue). | Add `govulncheck`/`osv-scanner` step to `release.yml`. |
| F-31 | L/T | Remaining test nits: `[]byte` Uint8-exemption in `argumentsToCLIArgs` untested; `buildInputSchema` required-order-on-duplicates unasserted; bearer `"Bearer  tok"` double-space; CORS preflight from *disallowed* origin; GET/DELETE → 405; `wordWrap` width ≤ 0; `TestDiscoverTools` hardcodes `/tmp/nonexistent_dir_12345`; stderr-capture techniques inconsistent (pipe vs `/dev/null`). | Small additions; see Test Report list. |

---

## Per-agent summaries

### Documentation (Librarian)
README is the strongest asset: **every** flag, env var, default, message, and precedence rule checked against `main.go` matched — no fabricated behavior. Defects are completeness (F-21) and staleness (F-23–25), plus the committed API key (F-5, urgent).

### Code structure (Explorer)
"Ship-quality core, monolith-shaped shell." No correctness blockers. Debt: 3 injection globals, test-only `lastHandler` field, dead `interval` param, double startup scan, 9–11-param functions, one 72-line validator with 5× repeated boilerplate. Proposed 12-file split is mechanical once config structs + `liveEnv` land.

### Security (Code Reviewer)
Protocol boundary is genuinely good: argument keys regex-validated, values passed as discrete argv elements with **no shell** (injection closed), constant-time token compare, no token in logs, exact-origin CORS with strict URL validation, integer-overflow-safe duration parsing, stateless HTTP, deps clean. **Not production-safe in HTTP mode** because of F-1..F-4 — all small, self-contained fixes. Residual risk is the inherent trust model: write access to the scripts dir = code execution as the server user (document it).

### Testing (Testing)
Suite is above average: 42 test functions, all pass (~10 s), `go vet` clean, newer sections table-driven with exact assertions, no vacuous tests, injection points proportionate. Gaps: truncation branch (F-6), fsnotify failure modes (F-9), `main`/`run` (F-10), flake-prone timing loops, no `-race` possible in this sandbox. 25 concrete proposed tests are in the agent's report (highest value: F-6, F-9, `TestRegistryReplaceConcurrency`).

### Performance (Explorer)
Verified clean: no goroutine/timer leaks, compiled regex reused, short lock critical sections, stateless HTTP, lean deps, debounced non-re-entrant watcher. One High (F-2, measured ~3× amplification); the remaining items (F-3, F-4, F-7, F-11) are localized hardening edits — the agent rated them Medium, this report grades F-3/F-4 High in the context of the unauthenticated HTTP envelope. No architectural change required.

---

## Suggested execution order

1. **Tier 1 (F-1…F-6)** — ~1 day of work, closes every High. One PR: "harden HTTP mode" (F-1, F-3, F-4) + "bound output capture" (F-2, F-12, F-6) + key rotation (F-5).
2. **Tier 2 (F-7…F-12)** — ~2–3 days: process-group kill, token-from-argv deprecation, fsnotify tests + diff-skip, `run` e2e test.
3. **Tier 3 (F-14…F-22)** — structure PRs in this order: config structs → `liveEnv` → dead-code removal (F-16, F-17) → local simplifications (F-18, F-19) → 12-file split (F-20) (+ mirrored test split, F-22); docs edits (F-21) can ride along anytime.
4. **Tier 4 (F-23…F-31)** — chores, can ride along with anything.

## Verdict

Graded harshly: **B−.** This is a well-engineered core wearing an unsafe shell. The command-execution boundary (the part that matters most for a tool whose purpose is executing user scripts) is closed correctly, and the test suite is honest. But the same code that *should* be a trusted local helper can, with one forgotten flag, become an unauthenticated remote shell on the LAN — with no warning, no caps, and unbounded memory. Fix Tier 1 before the next release; the rest is debt management, not rescue.
