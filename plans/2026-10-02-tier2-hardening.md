# PLAN — Tier 2 fixes (REVIEW.md v0.7.0 findings F-7…F-12, F-23)

**Branch:** `fix/tier2-hardening` (from `main` @ 15e7da7, post-Tier-1)
**Target version:** v0.8.1
**Source of truth:** `REVIEW.md` §Tier 2 (+ F-23 from Tier 4, rides along per its "can ride along with anything" note).
Status: **In progress** — tick boxes as units land.

## Scope

- [x] F-7: process-group kill — `Setpgid` + kill `−pid` on deadline + `cmd.WaitDelay` backstop, so shell-script tools can't leave grandchildren running past their budget
- [x] F-8: deprecate `--api-key <value>` (world-readable via `/proc/<pid>/cmdline`): new `--api-key-file` flag; startup warning when the flag is the token source
- [x] F-9: fsnotify failure-mode tests — watched dir deleted, rename, permission error, tool removal from the registry
- [x] F-10: `main`/`run` coverage — `parseCLI` extraction + one HTTP end-to-end test against `run`
- [x] F-11: diff new vs current tool set; skip re-registration (and N `tools/list_changed` notifications) when the set is unchanged
- [x] F-12: mark done (UTF-8 boundary back-off landed with F-2 in commit 459a9ba, tested in `TestCombineToolOutputTruncation`)
- [ ] F-23: Makefile `VERSION` 0.2.0 → 0.8.1, `buildall` in `.PHONY`; `serverVersion` → `0.8.1`
- [x] Docs: README auth section documents `--api-key-file` + deprecation; usage line updated
- [ ] REVIEW.md: mark F-7…F-12, F-23 ✅ with this branch's commits

## Design decisions (deviations/choices vs REVIEW proposal)

1. **F-7**: `exec.CommandContext` already signals the direct child; add `SysProcAttr{Setpgid: true}` and a goroutine that SIGKILLs the whole group (`kill(-pid)`) when the exec context is done, plus `cmd.WaitDelay` (5 s) as the backstop for grandchildren that detach the group (`setsid`) and hold the output pipes open. `Setpgid` is unix-only, so the attr/kill pair lives in two small build-tagged files (`toolproc_unix.go` / `toolproc_other.go`, the latter a `cmd.Process.Kill()` no-op equivalent) to keep the Windows cross-build (Makefile/`buildall`, goreleaser) working.
2. **F-8**: precedence `--api-key` > `--api-key-file` > `MCP_COMMANDS_API_KEY` (flag wins = back-compat). Resolution moves into `main` (fail-fast); `run` takes the resolved token. Unreadable `--api-key-file` → startup error. Flag source → persistent `Warning: --api-key <value> is visible to other local users via /proc/<pid>/cmdline; prefer --api-key-file or MCP_COMMANDS_API_KEY`. File content is `TrimSpace`d (hand-written files with trailing newlines). `--api-key-file` joins `serverModeFlagNames` for the diagnostic ignored-flags notice.
3. **F-9**: four tests per the finding. The permission-error test guards `t.Skip` when running as root (inotify EACCES is not reproducible as uid 0; the sandbox and CI run as root — the test covers real user installs).
4. **F-10**: light `parseCLI(args) (cliConfig, error)` extraction covering every fail-fast branch in `main` (required flags, exclusivity, `--timeout`/`--no-timeout`, negative `--max-concurrent`, ignored-flags collection, api-key-file read). This is a *precursor* to F-14's `serverConfig`/`diagnostic` structs (Tier 3), not that refactor — `run`/`runDiagnostic` signatures stay scalar. E2E: `run` on an ephemeral free port, real MCP client over `StreamableClientTransport`, initialize → list → call → ctx cancel → graceful return.
5. **F-11**: registry tracks `current []discoveredTool`; new `replaceIfChanged` compares element-wise (timeout pointer deref, params `DeepEqual`) and skips the remove/re-add entirely when identical; `watchTools`' debounced onChange uses it. The "re-arm debounce per event" idea from the finding is *not* done: the fixed 100 ms window already collapses bursts, and re-arming per event can starve the reload indefinitely under a continuous write stream — net negative.
6. **F-12**: no code change — verified already landed with F-2 (Tier 1, 459a9ba): `combineToolOutput` backs off to a rune boundary, `TestCombineToolOutputTruncation` asserts valid UTF-8 at a split boundary.
7. **F-23**: `Makefile` `VERSION ?= 0.2.0` → `0.8.1` and `buildall` added to `.PHONY`; `serverVersion` constant → `0.8.1` (this is the actual version bump for this branch; goreleaser uses the hardcoded constant, so release and `make` builds agree).
8. **F-7 test caveat**: this sandbox's seccomp profile silently swallows `kill(2)` (even single-target SIGKILL) toward processes Go `exec`'d — group kills from a Go caller are untestable here, while the same calls from shell processes work. The test therefore probes delivery first (`assertGroupKillDelivered`) and **skips with a visible reason** in such sandboxes; it runs for real on ordinary systems/CI. Code unchanged: `Setpgid` + `kill(−pid)` + `WaitDelay` is the standard pattern.
9. `-race` cannot run in this sandbox (ThreadSanitizer unsupported) — race coverage by inspection, same convention as Tier 1.

## Verification

- `go vet ./...` + `go test ./...` green (sandbox env: `GOCACHE/GOPATH/TMPDIR` under `/workspace/.goenv`)
- Windows cross-compile sanity: `GOOS=windows GOARCH=amd64 go build` (build-tag files)
- Smoke: `--list-tools`, `--call-tool`, HTTP `--api-key-file` auth, grandchild-kill behavior
