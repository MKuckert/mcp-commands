# PLAN — U4: Configuration and HTTP hardening

**Branch:** `fix/u4-http-posture` from `main` (v0.9.3). **Target:** v0.9.4.
**Source:** `REVIEW_REPORT.md` §U4 (M5, M6, M7).
**Status:** Approved (all tasks ticked; see review log, round 2).

## Tasks

- [x] M6: `checkHTTPSecurityPolicy` inspects the full startup posture (bind × auth × CORS × TLS) and returns warnings, not one: unauthenticated non-loopback bind (existing `--insecure-no-auth` case), `--allow-all-origins` with no bearer token, and authenticated non-loopback bind without TLS. Warnings are advisory only — no new flag, no refusal (maintainer decision: every warned posture is an explicit operator choice). Policy-decision table test over the posture matrix.
- [x] M5: Fail-fast flag validation in `parseCLI` (server mode): `--port` must be `0..65535` (0 = stdio); `--max-concurrent` must be `0..256` (documented ceiling in the flag help; negative already rejected). `resolveAPIKey`: bound `--api-key-file` reads at 8 KiB and reject an explicit token file that is empty after trimming (no silent fall-through to env/no-auth).
- [x] M7: Bound the *time* a request body may take to arrive. `buildHTTPHandler` wraps `r.Body` in a total-deadline reader (30 s, var so tests can shorten it) inside the existing 10 MiB `MaxBytesReader`; a stalled partial POST gets an error response and the handler slot is released. Stalled partial POST test.
- [x] Bump `main.go` and `Makefile` to 0.9.4; run formatting, `go test ./... -count=1`, `go vet ./...`, and version smoke check.

## Design

- **M5** lives in the two existing validation seams: the server-mode block of `parseCLI` (`flags.go`) and `resolveAPIKey` (`http.go`). The concurrency ceiling constant (`maxConcurrentCap = 256`) sits next to `defaultMaxConcurrentTools` in `registry.go`; `newExecSlot` is untouched — fail-fast at the flag is the single guard. The token-file size bound (`maxAPIKeyFileBytes = 8 << 10`) is a const in `http.go`; an over-size or empty explicit file is an error naming the file, mirroring the missing-file behavior.
- **M7**: `MaxBytesReader` caps size, not time — a client dribbling a few bytes per second pins a connection and its handler indefinitely without ever consuming a tool-execution slot. A *total* deadline is the right bound (a per-read deadline does not stop dribbling); 30 s is generous for the real workload (small JSON; even a full 10 MiB upload should not take long). The deadline reader is a tiny `io.ReadCloser` wrapper (`bodyReadDeadline`) that arms the socket read deadline once per read via `ResponseController.SetReadDeadline` and does **not** clear it per read — the runtime processes deadline clears asynchronously, so a stale clear can land after the next read re-arms the deadline and silently disarm it; the http server resets the read deadline when the connection is released for keep-alive or closed, so leaving it armed is harmless. When no real connection is behind the `ResponseWriter` (a test fake, or a closed/hijacked connection) it falls back to a plain synchronous read — deliberately **not** a read raced in a goroutine against a timer, because that read is uncancellable and would keep writing into the caller's buffer (and leak) after the timeout returns. In production `SetReadDeadline` never fails on a live connection, so the fallback is test-only. `MaxBytesReader` stays outermost so its 413 behavior is unchanged. The timeout is a package `var` (not const) so the stalled-POST test can shorten it; that test is deliberately not `t.Parallel()` because it mutates the var.
- **M6** (done): the pre-existing refusal for unauthenticated non-loopback binds without `--insecure-no-auth` is unchanged; the function now returns `[]string` warnings and the `run()` caller prints each. README documents both new warnings (TLS section, CORS section); the decision is recorded in `REVIEW_REPORT.md` §M6/§U4.

## Review log

### 2026-10-05 — Code Reviewer

**Status: Changes requested** (M5 blocked; M7 and version bump approved)

**Checked:** `flags.go` (parseCLI), `http.go` (resolveAPIKey, bodyReadDeadline, buildHTTPHandler), `registry.go` (maxConcurrentCap), `main.go`, `Makefile`, `flags_test.go` (TestParseCLI), `http_test.go` (TestResolveAPIKey, TestHTTPBodyReadDeadline, TestHTTPSecurityPolicy). Ran `go test ./... -count=1` (all pass), `go vet ./...` (clean), `gofmt -l .` (clean), `go run . --version` → `0.9.4`. `go test -race` not run (sandbox TSan limitation); race-safety assessed by inspection — the timer-race fallback's in-flight read goroutine is the only new goroutine and it terminates via the buffered channel; no shared mutable state. 

**Approved:**
- **M7** — `bodyReadDeadline` (http.go) arms the socket read deadline per read and never clears it (comment documents the async-clear race and the server-side reset on conn release — both accurate for net/http); timer-race fallback for non-real conns; `MaxBytesReader` stays outermost so 413 behavior is unchanged; total deadline (30 s var) is the correct bound against dribble. `TestHTTPBodyReadDeadline` is a real integration test (real httptest server → socket-deadline path, 300 ms var, `stallingBody`) asserting bounded elapsed time and a non-200 response, i.e. handler slot released. Ticked.
- **Version bump** — `main.go:12` and `Makefile:4` both `0.9.4`; smoke check prints `0.9.4`. Ticked.

**Defect (M5 — do not tick):**
- `http.go:83` — `os.ReadFile(fileValue)` slurps the **entire** file into memory *before* the `len(data) > maxAPIKeyFileBytes` check at line 87. The plan's Design section requires the *read* to be bounded at 8 KiB, and the const's own comment (http.go:34-35) states the rationale is "must fail loudly instead of **being slurped into memory**" — the implementation satisfies the size *check* but not the read *bound*. Consequence: a `--api-key-file` pointing at a multi-GB file (or a symlink to `/dev/zero`, which `ReadFile` reads without EOF) allocates unboundedly and OOMs the process at startup instead of failing fast. **Fix:** `os.Open` + `io.LimitReader(f, maxAPIKeyFileBytes+1)` (or a `MaxBytesReader`), keep the existing `len(data) > maxAPIKeyFileBytes` check to flag truncation, keep the existing error messages. The existing TestResolveAPIKey cases (empty/oversized/missing) remain valid; no new test is strictly required, though one asserting the read is bounded (not just the result checked) would lock the property in.

**Non-blocking observations (M5):** port/maxConcurrent validation, the documented ceilings in flag help, the empty-after-trim token rejection with a loud error naming the file and the env fallback, and the matching TestParseCLI cases (`port_above_range`/`port_negative`/`port_at_range_edge`, `max_concurrent_negative`/`_above_cap`/`_at_cap`, `api_key_file_empty_fails`) are all correct and complete — only the read-bound defect above blocks the tick. **M7 nit:** in the socket path, a non-timeout error returned *after* the deadline (e.g. a concurrent conn reset) is re-mapped to `errBodyReadTimeout` — cosmetic misclassification, both are server errors; leave as is.

### 2026-10-05 — Code Reviewer (round 2)

**Status: Approved — all tasks ticked.**

**M5 defect resolved.** `http.go` `resolveAPIKey` now opens the token file with `os.Open` + `defer f.Close()` and reads through `io.ReadAll(io.LimitReader(f, maxAPIKeyFileBytes+1))` — the *read* is bounded at cap+1 (8 KiB + 1 byte), so an over-size or unbounded source (multi-GB file, symlink to `/dev/zero`) is detected via the retained `len(data) > maxAPIKeyFileBytes` check without ever allocating past ~8 KiB. The over-size error now reads "exceeds the %d-byte maximum (wrong file?)" and names the file; the empty-after-trim and missing-file (`cannot read --api-key-file: %w`) errors are unchanged and loud. `io` is imported. This matches the Design-section requirement and the const's own rationale.

**Checked:** `http.go` `resolveAPIKey`, `http_test.go` `TestResolveAPIKey` (9 subtests incl. empty/oversized/missing). Ran `go test -run TestResolveAPIKey -count=1 -v` (all pass), `go test ./... -count=1` (green), `go vet ./...` (clean), `gofmt -l .` (clean). The remaining M5 surface — `--port` 0..65535, `--max-concurrent` 0..256 ceiling with documented flag help, empty-token-file rejection, and the matching `TestParseCLI` cases — was approved in round 1 and re-confirmed green.

**M5 is now approved and ticked. With M5, M6, M7, and the version bump all complete, the U4 plan is Approved in full.**
