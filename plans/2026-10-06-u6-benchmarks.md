# PLAN — U6: Cross-cutting test hygiene (benchmarks)

**Branch:** `fix/u6-benchmarks` from `main` (v0.9.5). **Target:** n/a (no product change).
**Source:** `REVIEW_REPORT.md` §U6 (L3) + priority test matrix item 4.
**Status:** Approved (see review log).

## Tasks

- [x] `BenchmarkDiscoverLargeDirectory` — `discoverTools` over a 200-script directory with realistic frontmatter (description + params); the startup and every hot-reload rescan path.
- [x] `BenchmarkRegistryReload` — `replaceIfChanged` over a 100-tool registry in two sub-benchmarks: the no-op rescan (must stay cheap — this is the M8 short-circuit, doubles as a regression guard) and the one-tool-changed case (per-tool diff cost).
- [x] `BenchmarkExecuteCappedOutput` — 16 concurrent `executeTool` calls at the full exec-slot cap, started from a barrier so the 16-way overlap is enforced; each script emits 2 MiB (double the cap, exercising the bounded writer's drop path); results are asserted `!IsError` with the exact post-truncation payload size, so a failed subprocess can never time the short error path.
- [x] Final: `go test -bench . -benchmem ./...` runs green, `go vet` clean, plan status updated, commit.

## Design

- L3's remaining surface is only the benchmarks: the ordered-argv assertion shipped with U2 (`execute_test.go` compares ordered pairs), and every adversarial/lifecycle test from the matrix shipped with its owning unit (FIFO: U1; precision/duplicates: U2; stalled body: U4).
- Pure `bench_test.go` addition; zero product code, no behavior risk. All three benchmark real production functions (`discoverTools`, `replaceIfChanged`, `executeTool`) — no test scaffolding in the measured path.
- The capped-output benchmark runs 16 workers against a 16-slot `execSlot` (the default cap); a start barrier (acquire → ready → close(start)) makes the 16-way concurrency deterministic. Each script emits 2 MiB — double `maxToolOutputBytes` — so the bounded writer's drop path runs on every call, and the resulting payload is asserted to be exactly `maxToolOutputBytes` plus the truncation marker (a failed subprocess, which `executeTool` reports as `IsError` with a nil Go error, would fail the size assert and cannot silently time an error path).
- Registry benchmarks use a real `mcp.NewServer` (as the registry tests do) so `addToolLocked` exercises the real registration path.

## Review log

- **Rejected (1 loop):** 2 SetBytes-honesty findings. (1) Real: the discover benchmark's `SetBytes(1<<20)` overstated bytes ~27× — `benchScriptBody` is 197 bytes, not ~2 KiB — fixed to `200 * len(benchScriptBody)` with the comment corrected. (2) Misread: the capped-output `SetBytes(1<<20)` is *per call* (b.N counts `executeTool` calls; the 16-deep batching is the saturation condition, not 16× the counted work) — kept, semantics now documented in the comment. Non-blocking notes applied: `//go:build !windows` guard (`.sh` scripts are vacuous/failed on Windows; the windows-latest job compiles test files, and CI never runs benches), plan file status/boxes updated. Re-verified: `-bench . -benchmem -count=3` stable, full suite green, vet clean. **Approved.**
- **Copilot PR review (1 loop):** 2 findings on `BenchmarkExecuteCappedOutput`, both accepted. (1) Saturation was statistical (staggered goroutine starts, no rendezvous) — now enforced with a start barrier; partial batches (`b.N < 16`, remainders) work through the same barrier. (2) The result was never validated — `executeTool` reports a failed subprocess as `IsError` with a nil Go error, so a broken pipeline would have silently timed the short error path; now asserted `!IsError` plus the exact post-truncation payload size (2 MiB emitted → 1 MiB cap + marker). The 1 MiB script was changed to 2 MiB so the truncation path (not just the cap boundary) is exercised and the payload assert is exact. Re-verified: `-bench . -count=3` stable, `-benchtime=1x` (per=1 barrier) passes, full suite green, vet clean. **Approved.**
- **Copilot PR review (2nd loop, on the Makefile/BENCHMARKS.md commit):** 3 findings, all accepted. (1) The `one_tool_changed` loop allocated+formatted its input inside the timed region — replaced with two prebuilt variants alternated via a re-invocation-persistent index (keeps the baseline-collision fix, zero fixture cost in the loop). (2) `docs/BENCHMARKS.md` mislabeled the capped-output MB/s as per-call — `testing.B` reports aggregate (`b.N × bytes / wall`) across the 16 overlapping calls; doc and code comment now say aggregate (≈4.7 MB/s per call). (3) The benchstat link pointed at the wrong package — now `golang.org/x/perf/cmd/benchstat` (the earlier worktree fix had not landed in the commit). Re-verified: `-bench . -count=3` stable, `-benchtime=1x` passes, full suite green, vet clean. **Approved.**
