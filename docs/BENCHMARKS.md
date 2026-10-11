# Benchmarks

Reference numbers for the hot paths, recorded from a single `make bench`
run (`go test -run XXX -bench . -benchmem .`). These are **not** baselines:
nothing compares against them, so treat them as scale and shape, not
thresholds. Re-run on your own hardware — the figures will differ.

## Recorded run

```
BenchmarkClientIdentityPeek/single_message-4          	  101762    	 11471 ns/op	   9.41 MB/s	     696 B/op	      10 allocs/op
BenchmarkClientIdentityPeek/batch_of_3-4              	   69958    	 17032 ns/op	  19.26 MB/s	     864 B/op	      13 allocs/op
BenchmarkClientIdentityPeek/initialize-4              	   98094    	 12943 ns/op	  11.98 MB/s	     600 B/op	       8 allocs/op
BenchmarkClientIdentityPeek/large_single_1mib-4       	      60    	17144474 ns/op	  61.17 MB/s	 4230336 B/op	      16 allocs/op
BenchmarkClientIdentityPeek/message_only_stdio-4      	  363003    	  3306 ns/op	  32.66 MB/s	     112 B/op	       2 allocs/op
BenchmarkDiscoverLargeDirectory-4                     	      88    	12613478 ns/op	   2.97 MB/s	 1509366 B/op	    8638 allocs/op
BenchmarkRegistryReload/no_op_rescan-4                	   12715    	 97768 ns/op	    4801 B/op	     200 allocs/op
BenchmarkRegistryReload/one_tool_changed-4            	    2401    	 494639 ns/op	 139087 B/op	     635 allocs/op
BenchmarkExecuteCappedOutput-4                        	      84    	13707254 ns/op	  76.50 MB/s	 7468338 B/op	     102 allocs/op
```

Re-recorded 2026-10-10 with Go 1.27.2 on:

- 4× ARM Cortex-A72 (linux/arm64), 7.6 GiB RAM — a sandboxed VM, not a
  release-representative machine. Expect better ns/op on a desktop/server
  CPU; the *ratios* between benchmarks transfer better than the absolutes.
  Same VM class as the original 2026-10-06 run: the pre-existing benchmarks
  land within noise (Discover 13.19 → 12.61 ms, no-op rescan 96 → 97.8 µs,
  ExecuteCappedOutput 16.26 → 13.71 ms).
- The `-4` suffix is `GOMAXPROCS` (4 cores).

## What each measures

| Benchmark | Path | Read it as |
|---|---|---|
| `ClientIdentityPeek/*` | `clientInfoFromBody` over one POST body (the N1 identity peek — runs on **every** request, before the SDK's own parse) and `clientInfoFromMessage` per stdio line | Per-request overhead of the client-identity feature. The common case (`single_message`) makes **three passes** over the body: the batch attempt scans the whole value before failing on a non-array, then the message is parsed once directly and **again** by the NDJSON fallback (which also runs for non-handshake single messages, with a whole-body `string` copy) — ~11 µs for a small body, ~18 ms + 4.2 MB transient allocation for a 1 MiB body. `batch_of_3` is array scan + one parse per element; `initialize` adds the params parse (the only body that carries clientInfo); `message_only_stdio` is the stdio tee's cost — one parse per message (~3.3 µs small). |
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
