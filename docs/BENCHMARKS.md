# Benchmarks

Reference numbers for the hot paths, recorded from a single `make bench`
run (`go test -run XXX -bench . -benchmem .`). These are **not** baselines:
nothing compares against them, so treat them as scale and shape, not
thresholds. Re-run on your own hardware — the figures will differ.

## Recorded run

```
BenchmarkClientIdentityPeek/single_message-4          	  349878    	  3420 ns/op	  31.58 MB/s	     112 B/op	       2 allocs/op
BenchmarkClientIdentityPeek/batch_of_3-4              	   68308    	 16837 ns/op	  19.48 MB/s	     864 B/op	      13 allocs/op
BenchmarkClientIdentityPeek/initialize-4              	  146431    	  8706 ns/op	  17.80 MB/s	     368 B/op	       5 allocs/op
BenchmarkClientIdentityPeek/large_single_1mib-4       	     217    	 5492285 ns/op	 190.94 MB/s	 1057136 B/op	       2 allocs/op
BenchmarkClientIdentityPeek/message_only_stdio-4      	  363726    	  3264 ns/op	  33.09 MB/s	     112 B/op	       2 allocs/op
BenchmarkDiscoverLargeDirectory-4                     	      88    	12643982 ns/op	   2.96 MB/s	 1508890 B/op	    8637 allocs/op
BenchmarkRegistryReload/no_op_rescan-4                	   10000    	 102692 ns/op	    4802 B/op	     200 allocs/op
BenchmarkRegistryReload/one_tool_changed-4            	    2374    	 489974 ns/op	 139075 B/op	     635 allocs/op
BenchmarkExecuteCappedOutput-4                        	      85    	13780850 ns/op	  76.09 MB/s	 7423081 B/op	     101 allocs/op
```

Re-recorded 2026-10-10 with Go 1.27.2 on:

- 4× ARM Cortex-A72 (linux/arm64), 7.6 GiB RAM — a sandboxed VM, not a
  release-representative machine. Expect better ns/op on a desktop/server
  CPU; the *ratios* between benchmarks transfer better than the absolutes.
  Same VM class as the original 2026-10-06 run: the pre-existing benchmarks
  land within run-to-run variance (Discover 13.19 → 12.64 ms, no-op rescan
  96 → 102.7 µs, ExecuteCappedOutput 16.26 → 13.78 ms — two same-machine
  runs of the no-op rescan differ by ~5%).
- The `-4` suffix is `GOMAXPROCS` (4 cores).

## What each measures

| Benchmark | Path | Read it as |
|---|---|---|
| `ClientIdentityPeek/*` | `clientInfoFromBody` over one POST body (the N1 identity peek — runs on **every** request, before the SDK's own parse) and `clientInfoFromMessage` per stdio line | Per-request overhead of the client-identity feature, now **one parse** for a single message: the batch attempt is dispatched on the first non-whitespace byte, and a valid non-handshake message never falls through to the NDJSON line scan. ~3.4 µs for a small body, ~5.5 ms + 1 MB transient allocation for a 1 MiB body. (Before the single-parse fix this path made three passes — batch scan + parse + NDJSON re-parse with a whole-body `string` copy: ~11.5 µs small, ~17 ms + 4.2 MB for 1 MiB.) `batch_of_3` is array scan + one parse per element (unchanged by the fix); `initialize` adds the params parse (the only body that carries clientInfo); `message_only_stdio` is the stdio tee's cost — one parse per message. |
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
