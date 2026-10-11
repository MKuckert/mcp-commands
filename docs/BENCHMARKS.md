# Benchmarks

Reference numbers for the hot paths, recorded from a single `make bench`
run (`go test -run XXX -bench . -benchmem .`). These are **not** baselines:
nothing compares against them, so treat them as scale and shape, not
thresholds. Re-run on your own hardware — the figures will differ.

## Recorded run

```
BenchmarkDiscoverLargeDirectory-4                     	      80    	12829412 ns/op	   2.92 MB/s	 1509407 B/op	    8638 allocs/op
BenchmarkRegistryReload/no_op_rescan-4                	   12612	     95447 ns/op	    4801 B/op	     200 allocs/op
BenchmarkRegistryReload/one_tool_changed-4            	    2481	     515236 ns/op	  139056 B/op	     635 allocs/op
BenchmarkExecuteCappedOutput-4                        	      84    	13524235 ns/op	  77.53 MB/s	 7423258 B/op	     101 allocs/op
```

Re-recorded 2026-10-11 with Go 1.27.2 on:

- 4× ARM Cortex-A72 (linux/arm64), 7.6 GiB RAM — a sandboxed VM, not a
  release-representative machine. Expect better ns/op on a desktop/server
  CPU; the *ratios* between benchmarks transfer better than the absolutes.
  Same VM class as the original 2026-10-06 run: the pre-existing benchmarks
  land within run-to-run variance (Discover 13.19 → 12.83 ms, no-op rescan
  96 → 95.4 µs, ExecuteCappedOutput 16.26 → 13.52 ms — two same-machine
  runs of the no-op rescan differ by ~5%).
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
