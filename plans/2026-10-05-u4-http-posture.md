# PLAN — U4: Configuration and HTTP hardening

**Branch:** `fix/u4-http-posture` from `main` (v0.9.3). **Target:** v0.9.4.
**Source:** `REVIEW_REPORT.md` §U4 (M5, M6, M7).
**Status:** In progress.

## Tasks

- [x] M6: `checkHTTPSecurityPolicy` inspects the full startup posture (bind × auth × CORS × TLS) and returns warnings, not one: unauthenticated non-loopback bind (existing `--insecure-no-auth` case), `--allow-all-origins` with no bearer token, and authenticated non-loopback bind without TLS. Warnings are advisory only — no new flag, no refusal (maintainer decision: every warned posture is an explicit operator choice). Policy-decision table test over the posture matrix.
- [ ] M5: Fail-fast flag validation in `parseCLI` (server mode): `--port` must be `0..65535` (0 = stdio); `--max-concurrent` must be `0..256` (documented ceiling in the flag help; negative already rejected). `resolveAPIKey`: bound `--api-key-file` reads at 8 KiB and reject an explicit token file that is empty after trimming (no silent fall-through to env/no-auth).
- [ ] M7: Bound the *time* a request body may take to arrive. `buildHTTPHandler` wraps `r.Body` in a total-deadline reader (30 s, var so tests can shorten it) inside the existing 10 MiB `MaxBytesReader`; a stalled partial POST gets an error response and the handler slot is released. Stalled partial POST test.
- [ ] Bump `main.go` and `Makefile` to 0.9.4; run formatting, `go test ./... -count=1`, `go vet ./...`, and version smoke check.

## Design

- **M5** lives in the two existing validation seams: the server-mode block of `parseCLI` (`flags.go`) and `resolveAPIKey` (`http.go`). The concurrency ceiling constant (`maxConcurrentCap = 256`) sits next to `defaultMaxConcurrentTools` in `registry.go`; `newExecSlot` is untouched — fail-fast at the flag is the single guard. The token-file size bound (`maxAPIKeyFileBytes = 8 << 10`) is a const in `http.go`; an over-size or empty explicit file is an error naming the file, mirroring the missing-file behavior.
- **M7**: `MaxBytesReader` caps size, not time — a client dribbling a few bytes per second pins a connection and its handler indefinitely without ever consuming a tool-execution slot. A *total* deadline is the right bound (a per-read deadline does not stop dribbling); 30 s is generous for the real workload (small JSON; even a full 10 MiB upload should not take long). The deadline reader is a tiny `io.ReadCloser` wrapper; `MaxBytesReader` stays outermost so its 413 behavior is unchanged. The timeout is a package `var` (not const) so the stalled-POST test can shorten it; that test is deliberately not `t.Parallel()` because it mutates the var.
- **M6** (done): the pre-existing refusal for unauthenticated non-loopback binds without `--insecure-no-auth` is unchanged; the function now returns `[]string` warnings and the `run()` caller prints each. README documents both new warnings (TLS section, CORS section); the decision is recorded in `REVIEW_REPORT.md` §M6/§U4.

## Review log

(pending)
