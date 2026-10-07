# PLAN — Tier 3 fixes (REVIEW.md v0.7.0 findings F-14…F-22)

**Branch:** `fix/tier3-structure` (from `main` @ b88e58c, post-Tier-2)
**Target version:** v0.8.2
**Source of truth:** `REVIEW.md` §Tier 3 (F-14…F-22). F-21 (docs) rides along per REVIEW's "can ride along anytime".
Status: **Approved** (Code Reviewer quality pass against current `main`, 2026-10-07 — all items landed and still hold; see review log below).

## Scope

- [x] F-14: `serverConfig` / `diagnostic` structs — `run` (11 params) and `runDiagnostic` (10 params) take one struct; `main()` shrinks to ~25 lines
- [x] F-15: one `liveEnv` struct (`resolveWrapWidth`, `clearScreen`, `notifySignals`, `watcherErrors`, `errOut`) replaces the three mutable package-level injection vars; unblocks `t.Parallel()`
- [x] F-16: delete dead code — `toolRegistry.lastHandler`, `watchTools`'s unread `interval` param, `watchToolsInterval` constant
- [x] F-17: no double initial discovery at startup — `watchTools` receives the already-discovered set and skips its initial scan when it matches the registry's current set
- [x] F-18: local simplifications — `warnParam` helper (5× duplicated block) + type set lookup in `parseParamAnnotation`; `strings.Cut` in `extractFrontmatter`; `textResult(text, isErr)` helper for the 4 near-identical `CallToolResult` constructions; fix `discoveredTool` loop-var shadowing
- [x] F-19: `discoverTools` name collisions — first wins + stderr warning (house style)
- [x] F-20: 12-file split of `main.go` + 1:1 mirrored test split
- [x] F-21: README docs gaps (~5 targeted edits)
- [x] F-22: test hygiene — `writeScript` helper, `waitFor(t, …)` polling helper, table-driven `TestBuildInputSchema`, `t.Parallel()` where F-15 unblocks, named timeout constant, `TestRegistryReplaceConcurrency` (stand-in for unavailable `-race`)
- [x] Bump `serverVersion` + Makefile `VERSION` → 0.8.2 (superseded by later releases; current 0.11.0)
- [x] `REVIEW.md`: mark F-14…F-22 ✅ with this branch's commits, note deviations — **N/A at review time**: `REVIEW.md` was deleted in `9b51ce8` (PR #24 close-out) after this branch landed; the plan file is the standing record

## Design decisions

1. **F-14**: `cliConfig` is *already* the merged struct (Tier 2's `parseCLI` extraction). F-14's real target is the two consumers: `run(ctx, serverConfig)` and `runDiagnostic(diagnostic)`. Split `cliConfig`'s server-mode fields into `serverConfig` (dir, scriptsDir, watch, host, port, apiKey, cors, timeout, insecureNoAuth, maxConcurrent) and diagnostic fields into `diagnostic` (dir, scriptsDir, listTools, watch, callTool, callToolSet, params, timeout, ignoredFlags) — `cliConfig` keeps `version` + `mode` and embeds/exposes both, so `parseCLI` and `main` are untouched in shape. `main()` becomes ~25 lines. `run`'s direct `signal.NotifyContext` call moves into `liveEnv` (F-15) so the same test seam covers it.
2. **F-15**: `liveEnv{stdout, stderr, errOut, resolveWrapWidth, clearScreen, notifySignals, watcherErrors}` with a `prodLiveEnv()` constructor. `run`, `runDiagnostic`, `runListTools`, `runCallTool`, `watchChanges`, `watchTools` all take it (or it flows through `serverConfig`/`diagnostic`). Tests build fakes locally — no global mutation, so `t.Parallel()` is safe everywhere (applied in F-22 to tests that don't share state).
3. **F-16**: `lastHandler` deleted; the one test that read it is re-routed through the in-memory client session like its siblings. `interval` param + `watchToolsInterval` constant deleted.
4. **F-17**: `run` discovers once and passes the set into `watchTools(ctx, env, scriptsDir, registry, initialTools)`. `watchTools` skips its initial scan when `toolsEqual(registry.current, initialTools)` (the normal boot path) and only scans if the registry was populated some other way.
5. **F-18**: `parseTimeoutDuration` is *already* a token loop with overflow guards (landed in Tier 1/2) — the regex variant from the finding would be a wash, so it stays as-is (noted deviation); the remaining simplifications (warnParam, `strings.Cut`, `textResult`, shadowing) are done.
6. **F-19**: `discoverTools` builds a name→tool first-wins map; a collision logs `Warning: ignoring %s: tool name %q already registered by %s` to stderr (first file in ReadDir order wins — deterministic, matches house style). Duplicate `AddTool` cannot then occur.
7. **F-20** layout (production): `main.go` (version consts + main), `flags.go` (parseCLI, cliConfig/serverConfig/diagnostic, flagParseError, usage, serverModeFlagNames), `discover.go` (discoverTools, extractFrontmatter, paramSpec, collision logic), `timeout.go` (parseTimeoutDuration, matchTimeoutUnit, resolveTimeout, resolveToolTimeout, timeoutSuffix), `schema.go` (buildInputSchema, argumentKeyPattern, argumentsToCLIArgs, validateRequiredParams, parseToolArguments), `execute.go` (boundedWriter, combineToolOutput, executeTool, textResult), `registry.go` (toolRegistry, execSlot, toolsEqual, replace/replaceIfChanged), `list.go` (renderToolList, wordWrap, listParamDecl, toolListSignature, runListTools), `call.go` (runCallTool, runDiagnostic), `http.go` (buildHTTPHandler, bearer, CORS, MaxBytesReader, server limits, security policy, resolveAPIKey, isLoopbackHost), `server.go` (run, liveEnv), `watch.go` (watchChanges, watchTools). `toolproc_unix.go`/`toolproc_other.go` stay. Tests mirror 1:1 (`*_test.go` per production file); shared test helpers live in `main_test.go`.
8. **F-22**: `writeScript(t, dir, name, content, mode)` replaces the ~35 ad-hoc `os.WriteFile` fixture writes; `waitFor(t, timeout, interval, cond)` replaces the hand-rolled 2 s/20 ms polling loops; `TestBuildInputSchema` → table; `TestBuildHTTPHandlerAuthDisabled` asserts 200 + session behavior, not just "not 401"; `t.Parallel()` on tests that are safe under F-15's local fakes; the 28 `5*time.Minute` literals → `defaultToolTimeout`; new `TestRegistryReplaceConcurrency` (goroutines call `replace`/`replaceIfChanged` in parallel; race stand-in documented in REVIEW F-22 — `-race` unsupported in sandbox).
9. **Order** (each a commit, tests green after each): plan + version bump → F-14 → F-15 → F-16+F-17 → F-18+F-19 → F-20 (split) → F-22 (test hygiene) → F-21 (README) → REVIEW.md. F-21 is delegated to a parallel subagent (README only, no code files) and merged after the code lands.

## Verification

- `go vet ./...` + `go test ./...` green after every unit (sandbox env: `GOCACHE/GOPATH/TMPDIR` under `/workspace/.goenv`; `-race` unsupported — inspection + `TestRegistryReplaceConcurrency`)
- `gofmt -l .` clean
- Windows cross-compile sanity: `GOOS=windows GOARCH=amd64 go build ./...`
- Smoke: `--list-tools`, `--call-tool`, HTTP auth, version output


## Status (2026-10-05): complete

All nine items landed on `fix/tier3-structure`, one commit each (see `git log`):
F-14 `b290485` · F-15 `05516c4` · F-16+F-17 `fe8eda1` · F-18+F-19 `9b9fd1f` · F-20 `b6cf5de` · F-22 `3eb592b` · F-21 (README, swept into `b290485`).

Deviations from the plan, all deliberate:
1. **F-20: 11 files, not 12** — the proposed `call.go` merged into `server.go` (`runCallTool`/`runListTools`/`runDiagnostic` are one "run modes" cluster; a 30-line file would be noise).
2. **F-18: `parseTimeoutDuration` left as-is** — it was already the proposed prefix-token loop with overflow guards; no rewrite needed.
3. **F-22: `TestBuildInputSchema` kept as structured `t.Run` subtests** — its assertions are heterogeneous per case; a table would force a weakest-common-denominator assertion set.
4. **F-22: `waitFor` added and the simple loops converted; the ListTools polling loops keep bounded polling** — their cond (call ListTools, capture state, success check) would be longer inside a `waitFor` closure than the loop it replaces.
5. **F-21: README changes committed with the F-14 commit** (`git add -A` timing), so the F-14 commit contains doc changes alongside the struct refactor.

Verification (final state): `gofmt -l` clean, `go vet ./...` clean, `go test ./...` green (~11 s with the new `t.Parallel` suite).

### Code Reviewer — 2026-10-07 (quality pass against current `main` @ 7b59584) — **APPROVED**

Full re-verification of all 9 scope items against the current tree (four feature lines landed after the branch merged: tool-diagnostics, granular logging, U3 watch lifecycle, Containerfile). All items **still hold**: F-14 (`serverConfig` flags.go:25 / `diagnostic` :42 / `cliConfig` :57; `main()` ~35 lines), F-15 (`liveEnv` + `prodLiveEnv`; zero mutable package-level injection vars; fields evolved — `stderr`/`errOut` → `log`, `+ newWatcher` — guarantee intact), F-16 (`lastHandler`/`watchToolsInterval` absent), F-17 (`initialTools` re-assert + diff-skip rescan), F-18 (`warnParam` ×5, `strings.Cut` ×3, `textResult` ×4, `paramTypes`), F-19 (first-wins + structured `log.Warn` — equivalent to the planned stderr warning), F-20 (1:1 mirror; 15 prod files = tier3's 13 + later-feature `diagnostic.go`/`loghandler.go`), F-21 (README 472 lines covers the gaps), F-22 (`writeScript`/`waitFor` in `server_test.go`, `t.Parallel()` ×63, `defaultToolTimeout`, `TestRegistryReplaceConcurrency`).

Post-merge drift is benign feature growth, all annotated in code. The `REVIEW.md` task is N/A (file deleted in `9b51ce8` after the branch landed); the 0.8.2 version target is superseded by 0.11.0.

Verification (current tree): `gofmt -l .` clean · `go vet ./...` clean · `go test -count=1 ./...` → `ok … 6.795s` · `GOOS=windows GOARCH=amd64 go build ./...` OK. (`-race` unsupported in this sandbox; registry mutex unchanged, `TestRegistryReplaceConcurrency` is the stand-in.)

No blocking findings. All checkboxes ticked; status → Approved.
