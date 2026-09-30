# PLAN — Tier 1 hardening (REVIEW.md v0.7.0 findings F-1…F-6)

**Branch:** `fix/tier1-hardening` (from `main` @ 11c2030)
**Target release:** v0.8.0
**Source of truth:** `REVIEW.md` §Tier 1. F-2 subsumes F-12 (done together per REVIEW).
Status: **In progress** — Plan Reviewer / Code Reviewer tick checkboxes as tasks land.

## Scope

Close every High finding so v0.8.0 is release-safe in HTTP mode:

- [ ] F-1: refuse unauthenticated HTTP on non-loopback host (or explicit `--insecure-no-auth`, with loud warning)
- [ ] F-2 (+F-12): per-stream bounded stdout/stderr capture (no ~3× RSS amplification) + UTF-8-safe final truncation
- [ ] F-3: HTTP server `ReadHeaderTimeout`/`IdleTimeout` + `MaxBytesReader` body cap
- [ ] F-4: concurrency cap on tool executions (`--max-concurrent`, default 16); saturated calls → clean `IsError` result
- [ ] F-5: remove `opencode.jsonc` from VCS + `.gitignore` (key rotation = user action, called out in summary)
- [ ] F-6: tests for truncation branch + huge-stdout `executeTool` + capacity + auth guard
- [ ] Version bump `serverVersion` → `0.8.0`
- [ ] README: document the two new flags + non-loopback auth behavior

## Design decisions (deviations/choices vs REVIEW proposal)

1. **F-1**: fail-fast in `run()` (after `port`/`apiKey` resolved) — refuse to start on non-loopback + no token unless `--insecure-no-auth`; when the flag is given, print a persistent `WARNING: UNAUTHENTICATED…` startup line. Loopback check: `net.ParseIP(host).IsLoopback()` or `host == "localhost"`. Unparseable/empty host treated as **non-loopback** (safe default). Extracted as a testable helper (`httpSecurityPolicy`) — this seeds the F-14 config-struct work.
2. **F-2**: `boundedWriter` per stream stops growing past `maxToolOutputBytes` (memory is now O(2 MiB) worst case regardless of tool output). `combineToolOutput` keeps the existing final 1 MiB cap so user-visible behavior is unchanged; F-12 backoff (`utf8.FullRune`) added in the same spot.
3. **F-3**: `ReadHeaderTimeout: 5s`, `IdleTimeout: 2m` (deliberately **no** `ReadTimeout`/`WriteTimeout` — they must exceed 5-min tool calls). `MaxBytesReader` (10 MiB) wraps the innermost handler, below auth/CORS.
4. **F-4**: hand-rolled weighted slot (buffered channel) on `toolRegistry`, limit from `--max-concurrent` (default 16, validated ≥ 1 in `main`). Non-blocking `tryAcquire`; failure returns `IsError` "at capacity … please retry". Acquired after arg parsing/validation so malformed calls don't consume capacity. Avoids a new `golang.org/x/sync` dependency.
5. **F-5**: `git rm --cached opencode.jsonc` + `.gitignore` entry; the working local copy keeps its (to-be-rotated) key and simply stops being committed. Repo history/remote scrub + key rotation are user actions (called out in the final summary).
6. **F-6**: `TestCombineToolOutputTruncation` (exact 1 MiB no-truncate / 2 MiB truncate / rune-split boundary → valid UTF-8), `TestExecuteToolHugeStdout` (5 MiB `head -c`), `TestToolCapacity` (limit 1, two concurrent calls → second at-capacity `IsError`), `TestHTTPSecurityPolicy` table.
7. `run()`/`main` gain `--insecure-no-auth` / `--max-concurrent` parameters (F-14 config-struct refactor still Tier 3; accepted interim signature growth). New server-mode flags added to `serverModeFlagNames` for the diagnostic ignored-flags notice.
8. `-race` cannot run in this sandbox (ThreadSanitizer unsupported) — race coverage by inspection per F-22 stand-in note; slot is a plain channel, no shared mutation.

## Verification

- `go vet ./...` + `go test ./...` green (sandbox env: `GOCACHE/GOPATH/TMPDIR` under `/workspace/.goenv`)
- Smoke: `--list-tools` still works; `--host 0.0.0.0` without key refuses; with `--insecure-no-auth` warns.
