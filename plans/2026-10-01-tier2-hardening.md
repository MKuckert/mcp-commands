# PLAN — Tier 2 hardening (REVIEW.md v0.7.0 findings F-7…F-13)

**Branch:** `fix/tier2-hardening` (from `main` @ 15e7da7)
**Target release:** v0.8.1
**Source of truth:** `REVIEW.md` §Tier 2. F-12 is already done — it landed with F-2 in Tier 1 (commit 459a9ba); this branch only marks it in REVIEW.md.
Status: **Done** (quality pass 2026-10-01: one confirmed bug — `replace()` leaked the anchor fd of every surviving tool; fixed via entry/fd reuse on unchanged identity, `TestRegistryReplaceAnchorLifecycle`; gofmt fix in `exec_unix.go`)

## Scope

- [ ] F-7: timeout kills the whole tool **process group** (shell-script grandchildren die with the deadline) + `WaitDelay` backstop
- [ ] F-8: `--api-key-file` flag; warn at startup when the token comes from argv (visible via `/proc/<pid>/cmdline`)
- [ ] F-9: fsnotify failure-mode tests — watched dir deleted, rename, permission change, tool removal from the registry
- [ ] F-10: extract `serverConfig` + `parseCLI` (pulls F-14's server-mode half forward as the stated prerequisite); test every fail-fast branch; one `run()` end-to-end HTTP test
- [ ] F-11: diff the discovered tool set; skip no-op re-registration (no Remove/Add + N `list_changed` notifications for an identical set); re-arm the debounce per event
- [ ] F-13: TOCTOU — re-validate path identity (dev/ino) at exec time, `O_NOFOLLOW` frontmatter read, document the scripts-dir trust boundary in README
- [ ] F-12: mark done in REVIEW.md (code shipped in Tier 1)
- [ ] Version bump `serverVersion` → `0.8.1`; Makefile `VERSION` → `0.8.1` + `buildall` in `.PHONY` (F-23 pulled forward for version consistency)

## Design decisions (deviations/choices vs REVIEW proposal)

1. **F-7**: `executeTool` gets `applyProcessGroup(cmd)`: on Unix `SysProcAttr{Setpgid: true}` + `cmd.Cancel` kills `−pid` with SIGKILL (ECHILD treated as success); `cmd.WaitDelay = 5s` backstop so a grandchild holding a pipe open cannot block `Wait` past the deadline. Windows has no process groups — build-tagged no-op keeps the default direct-kill (cross-compile must stay green: goreleaser ships all three OSes). Test: script spawns a background `sleep` (survives as orphan), 1s deadline → assert timeout result **and** `pgrep` finds no grandchild.
2. **F-8**: precedence `--api-key` > `--api-key-file` > `MCP_COMMANDS_API_KEY` (existing flag-first behavior preserved; file is the new middle tier). The file is read fail-fast in `parseCLI` (unreadable → startup error); its content is whitespace-trimmed. When the **flag** (not file/env) supplies the token, a startup warning names `/proc/<pid>/cmdline`. Flag added to `serverModeFlagNames` for the diagnostic ignored-flags notice; README auth section updated.
3. **F-9**: inotify cannot deliver a deterministic `Errors`-channel event for chmod'd/permission-changed dirs, so the permission test asserts the doc-commented **observable contract** (loop survives a 000-mode dir, still cleanly cancelable) rather than a kernel error we cannot force. Delete/rename/remove tests use the established bounded-retry pattern (lost events are legal). `TestWatchToolsRemovesDeletedTool` asserts the registry drops the tool via the in-memory client session.
4. **F-10**: new `serverConfig` struct + `parseCLI(args, stdout, stderr) (serverConfig, int)` (a `flag.FlagSet` with `ContinueOnError` → every fail-fast branch is testable without a process; `main()` shrinks to ~15 lines). `run(ctx, cfg)` and `runDiagnostic(stdout, stderr, cfg)` take the struct. This is **part of F-14** pulled forward as the dependency REVIEW declares for F-10; the remainder of F-14/F-15 (diagnostic-struct consolidation, `liveEnv` injection) stays Tier 3. `run()`'s zero-tools warning is printed to `os.Stderr` directly (no injection seam yet — F-15), so the e2e test asserts startup/serving/shutdown, not the warning text. E2E: real port on 127.0.0.1, poll `initialize` → 200, `tools/list` sees the discovered tool, cancel → clean return.
5. **F-11**: the diff lives **inside** `toolRegistry.replace`: it now stores `current []discoveredTool` and returns early when `reflect.DeepEqual(current, tools)` — so every call site (startup, watcher) dedupes for free, and `run()`'s startup replace + `watchTools`' initial scan no longer double-register (this kills F-17's notification churn as a side effect; F-17's duplicate *scan* work itself remains Tier 3). The watcher re-arms the debounce timer on **every** event (quiescence semantics) so bursts slower than the 100 ms window are not split across registrations.
6. **F-13**: `discoveredTool` gains `dev/ino` (from the discovery `Stat`; zero on platforms without `syscall.Stat_t`). `executeTool` takes the whole `discoveredTool` (not just the path) and, before `Start`, re-`EvalSymlinks` + `Stat`: must resolve to the same path, be a regular file, keep the exec bit, and match the recorded dev/ino — otherwise a clean `IsError` result ("tool changed since discovery; restart the server"). `extractFrontmatter` opens with `O_NOFOLLOW` (build-tagged constant; 0 on Windows). README gains a trust-boundary paragraph.
7. Version: `0.8.0` → `0.8.1`; Makefile `VERSION` + `buildall` in `.PHONY` (F-23, so `make` reports the shipped version). REVIEW.md rows for F-7…F-13 (+F-12, F-23) marked ✅ with the landing commit, deviations noted in place.
8. `-race` cannot run in this sandbox (ThreadSanitizer unsupported) — race coverage by inspection per the F-22 stand-in note; the registry diff adds a lock-protected read of `current`, same discipline as `names`.

## Tasks

- [ ] T1: plan file (this document) — commit `docs`
- [ ] T2: F-7 process-group kill + `TestExecuteToolKillsProcessGroup` — commit `fix`
- [ ] T3: F-8 `--api-key-file` + warning + `TestResolveAPIKey` update + README — commit `feat`
- [ ] T4: F-9 four fsnotify failure-mode tests — commit `test`
- [ ] T5: F-10 `serverConfig`/`parseCLI` extraction + branch tests + `run()` e2e — commit `refactor`
- [ ] T6: F-11 registry diff + debounce re-arm + `TestRegistryReplaceSkipsIdenticalSet` — commit `perf`
- [ ] T7: F-13 exec-time re-validation + `O_NOFOLLOW` + README trust boundary + tests — commit `fix`
- [ ] T8: version 0.8.1 + Makefile (F-23) + REVIEW.md status marks — commit `chore`
- [ ] T9: quality pass (subagent), findings fixed, plan status → Done

## Verification

- `go vet ./...` + `go test ./...` green (sandbox env: `GOCACHE/GOPATH/TMPDIR` under `/workspace/.goenv`)
- Cross-compile: `GOOS=windows/darwin` builds green (build-tag surface of F-7/F-13)
- Smoke: `--list-tools`; `--call-tool` on a real tool; HTTP mode `initialize` round-trip
- `-race` unavailable here — noted, not blocked on
