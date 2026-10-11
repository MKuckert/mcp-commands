//go:build !windows

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// writeBenchScript is the benchmark twin of writeScript (no *testing.T).
func writeBenchScript(b *testing.B, path, body string) {
	b.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		b.Fatalf("failed to write script %s: %v", path, err)
	}
}

// benchScriptBody is a realistic annotated script: description, two params,
// one timeout — the common case discovery parses on every rescan.
const benchScriptBody = `#!/bin/bash
# Description: Benchmark tool that does nothing.
# Param: source string required "Source file"
# Param: force boolean optional "Force the operation"
# Timeout: 30s

echo done
`

// BenchmarkDiscoverLargeDirectory measures a full scripts-directory scan
// (stat + frontmatter parse per file) — the startup path and the work of
// every --watch rescan.
func BenchmarkDiscoverLargeDirectory(b *testing.B) {
	dir := b.TempDir()
	const n = 200
	for i := 0; i < n; i++ {
		writeBenchScript(b, filepath.Join(dir, fmt.Sprintf("tool-%03d.sh", i)), benchScriptBody)
	}
	var log *slog.Logger = testDiscardLogger
	// benchScriptBody is ~197 bytes; 200 of them is the work per op.
	b.SetBytes(int64(n) * int64(len(benchScriptBody)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := discoverTools(dir, log); err != nil {
			b.Fatalf("discoverTools: %v", err)
		}
	}
}

// benchToolSet builds n deterministic tools for registry benchmarks.
func benchToolSet(n int) []discoveredTool {
	tools := make([]discoveredTool, n)
	for i := 0; i < n; i++ {
		tools[i] = discoveredTool{
			Name:        fmt.Sprintf("tool-%03d", i),
			Path:        filepath.Join("/scripts", fmt.Sprintf("tool-%03d.sh", i)),
			Description: fmt.Sprintf("Tool number %d.", i),
			Params: []paramSpec{
				{Name: "source", Type: "string", Required: true, Description: "Source file"},
			},
		}
	}
	return tools
}

// BenchmarkRegistryReload measures replaceIfChanged — the hot-reload hot
// path. The no-op case is the M8 short-circuit (a rescan that finds no
// changes must cost the per-tool equality diff and nothing else); the
// changed case measures the diff plus one re-registration.
func BenchmarkRegistryReload(b *testing.B) {
	server := mcp.NewServer(&mcp.Implementation{Name: "bench"}, nil)
	registry := newToolRegistry(server, "", 30*time.Second, 16, testDiscardLogger)
	set := benchToolSet(100)
	registry.replace(set)
	// Testing re-runs a subbenchmark's closure (calibration, then final)
	// while the registry persists across runs; the alternation index must
	// survive re-invocation so the first iteration never matches the
	// baseline the previous run left behind.
	var variant int
	// Two prebuilt one-tool-changed variants, alternated per iteration:
	// replaceIfChanged advances its baseline to the passed set, so each
	// call must differ from the previous one — and fixture construction
	// stays out of the timed loop entirely.
	va := benchToolSet(100)
	va[7].Description = "Tool number 7, variant A."
	vb := benchToolSet(100)
	vb[7].Description = "Tool number 7, variant B."
	variants := [2][]discoveredTool{va, vb}

	b.Run("no_op_rescan", func(b *testing.B) {
		identical := benchToolSet(100)
		for i := 0; i < b.N; i++ {
			if registry.replaceIfChanged(identical) {
				b.Fatal("replaceIfChanged reported a change for an identical set")
			}
		}
	})

	b.Run("one_tool_changed", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			v := variants[variant]
			variant = 1 - variant
			if !registry.replaceIfChanged(v) {
				b.Fatal("replaceIfChanged reported no change for a changed set")
			}
		}
	})
}

// BenchmarkExecuteCappedOutput runs the default-cap (16) exec slot fully
// saturated: 16 concurrent executeTool calls started from a barrier, each
// script emitting 2 MiB — double the 1 MiB cap, so the bounded writer's
// drop path runs on every call.
func BenchmarkExecuteCappedOutput(b *testing.B) {
	dir := b.TempDir()
	scriptPath := filepath.Join(dir, "flood.sh")
	// 2 MiB of 'x' on stdout; the pipe composition is setup cost, not
	// measured — the measured work is the capped capture and combine.
	writeBenchScript(b, scriptPath, "#!/bin/bash\nhead -c 2097152 /dev/zero | tr '\\0' 'x'\n")
	slot := newExecSlot(16)

	// Aggregate throughput: testing.B divides b.N × bytes by wall clock,
	// and up to 16 calls overlap — so the reported MB/s is across the
	// concurrent pool, not per call. Each counted operation is one
	// executeTool call capturing at maxToolOutputBytes.
	b.SetBytes(1 << 20)
	b.ResetTimer()
	const workers = 16
	type outcome struct {
		res *mcp.CallToolResult
		err error
	}
	done := make(chan outcome, workers)
	for i := 0; i < b.N; i += workers {
		per := workers
		if i+per > b.N {
			per = b.N - i
		}
		// Barrier: every worker acquires its slot, then all start
		// executeTool together — the 16-way overlap is enforced, not
		// left to scheduler luck. (16 acquirers against 16 slots: the
		// previous batch fully drained, so tryAcquire cannot fail.)
		start := make(chan struct{})
		ready := make(chan struct{}, per)
		for w := 0; w < per; w++ {
			go func() {
				var out outcome
				if !slot.tryAcquire() {
					out.err = fmt.Errorf("exec slot unavailable with a fully-sized worker pool")
				} else {
					ready <- struct{}{}
					<-start
					out.res, out.err = executeTool(context.Background(), testDiscardLogger, "tool", scriptPath, nil, "", 30*time.Second, dir)
					slot.release()
				}
				done <- out
			}()
		}
		for w := 0; w < per; w++ {
			<-ready
		}
		close(start)
		// executeTool reports a failed subprocess as IsError with a nil Go
		// error, so assert both: the call succeeded and the payload is the
		// exact post-truncation size (a short error path would fail this).
		want := maxToolOutputBytes + len(fmt.Sprintf("\n[output truncated after %d bytes]", maxToolOutputBytes))
		for w := 0; w < per; w++ {
			out := <-done
			if out.err != nil {
				b.Fatalf("executeTool: %v", out.err)
			}
			if out.res == nil || out.res.IsError {
				b.Fatalf("executeTool returned an error result: %+v", out.res)
			}
			if got := len(out.res.Content[0].(*mcp.TextContent).Text); got != want {
				b.Fatalf("payload %d bytes, want %d (capped + truncation marker)", got, want)
			}
		}
	}
}
