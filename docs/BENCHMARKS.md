# Benchmarks

Reference numbers for the hot paths, recorded from a single `make bench`
run (`go test -run XXX -bench . -benchmem .`). These are **not** baselines:
nothing compares against them, so treat them as scale and shape, not
thresholds. Re-run on your own hardware — the figures will differ.

## Recorded run

```
BenchmarkDiscoverLargeDirectory-4        81  13194886 ns/op   2.83 MB/s   1509139 B/op   8637 allocs/op
BenchmarkRegistryReload/no_op_rescan-4  12522     96046 ns/op        4801 B/op    200 allocs/op
BenchmarkRegistryReload/one_tool_changed-4  2727  448457 ns/op   105197 B/op    621 allocs/op
BenchmarkExecuteCappedOutput-4            92  16259015 ns/op  64.49 MB/s  5334585 B/op     87 allocs/op
```

Recorded 2026-10-06 with Go 1.27.1 on:

- 4× ARM Cortex-A72 (linux/arm64), 7.6 GiB RAM — a sandboxed VM, not a
  release-representative machine. Expect better ns/op on a desktop/server
  CPU; the *ratios* between benchmarks transfer better than the absolutes.
- The `-4` suffix is `GOMAXPROCS` (4 cores).

## What each measures

| Benchmark | Path | Read it as |
|---|---|---|
| `DiscoverLargeDirectory` | `discoverTools` over a 200-script directory (stat + frontmatter parse) | Startup cost, and the cost of every `--watch` rescan. ~13 ms for 200 scripts is dominated by process-external I/O, not parsing. |
| `RegistryReload/no_op_rescan` | `replaceIfChanged` over 100 tools, set unchanged | The rescan short-circuit (M8): an unchanged rescan costs the per-tool equality diff (~96 µs) and triggers **no** AddTool/RemoveTools churn and **no** `list_changed` notifications. |
| `RegistryReload/one_tool_changed` | same, one tool's description changed | The diff plus one re-registration (~448 µs, variants prebuilt and alternated so fixture cost stays out of the loop), dominated by schema build + validator + `AddTool` — not by the diff. |
| `ExecuteCappedOutput` | 16 concurrent `executeTool` calls from a start barrier, each script emitting 2 MiB (double the 1 MiB cap, so the bounded writer drops on every call) | Sustained **aggregate** throughput of a saturated pool: ~64 MB/s across the 16 overlapping calls (≈ 4 MB/s per call). `testing.B` computes MB/s as `b.N × bytes / wall clock`, and the calls overlap — the figure is pool-wide, not per call. |

## Re-running

```sh
make bench                 # one run, same as above
go test -bench . -benchmem -count=3 .   # triplicate, for a noisier picture
```

Compare two commits with
[`benchstat`](https://pkg.go.dev/golang.org/x/perf/cmd/benchstat):
`go test -bench . -benchmem -count=10 -benchtime=1s | tee new.txt` on each
commit, then `benchstat old.txt new.txt`.
