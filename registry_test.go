package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestToolsEqual covers the change-diff: every field a rescan can
// TestToolsEqual covers the change-diff: every field a rescan can
// change must invalidate the skip, and an unchanged set must compare equal.
func TestToolsEqual(t *testing.T) {
	t.Parallel()
	timeout5 := 5 * time.Minute
	timeout0 := time.Duration(0)
	tests := []struct {
		name string
		a, b discoveredTool
		want bool
	}{
		{name: "identical", a: discoveredTool{Name: "t", Path: "/x", Description: "d"}, b: discoveredTool{Name: "t", Path: "/x", Description: "d"}, want: true},
		{name: "nil_timeouts_equal", a: discoveredTool{Name: "t"}, b: discoveredTool{Name: "t"}, want: true},
		{name: "different_names", a: discoveredTool{Name: "a"}, b: discoveredTool{Name: "b"}, want: false},
		{name: "different_paths", a: discoveredTool{Name: "t", Path: "/x"}, b: discoveredTool{Name: "t", Path: "/y"}, want: false},
		{name: "different_descriptions", a: discoveredTool{Name: "t", Description: "one"}, b: discoveredTool{Name: "t", Description: "two"}, want: false},
		{name: "nil_vs_zero_timeout", a: discoveredTool{Name: "t"}, b: discoveredTool{Name: "t", Timeout: &timeout0}, want: false},
		{name: "timeout_values_differ", a: discoveredTool{Name: "t", Timeout: &timeout5}, b: discoveredTool{Name: "t", Timeout: &timeout0}, want: false},
		{name: "params_differ", a: discoveredTool{Name: "t", Params: []paramSpec{{Name: "p", Type: "string", Required: true}}}, b: discoveredTool{Name: "t", Params: []paramSpec{{Name: "p", Type: "number"}}}, want: false},
		{name: "params_reordered", a: discoveredTool{Name: "t", Params: []paramSpec{{Name: "p"}, {Name: "q"}}}, b: discoveredTool{Name: "t", Params: []paramSpec{{Name: "q"}, {Name: "p"}}}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toolsEqual([]discoveredTool{tt.a}, []discoveredTool{tt.b}); got != tt.want {
				t.Errorf("toolsEqual = %v, want %v", got, tt.want)
			}
		})
	}
	if toolsEqual([]discoveredTool{{Name: "a"}, {Name: "b"}}, []discoveredTool{{Name: "a"}}) {
		t.Error("different lengths must not compare equal")
	}
}

func TestResolvedTimeoutViaRegistry(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	sleepPath := filepath.Join(tmpDir, "sleep5.sh")
	writeScript(t, sleepPath, "#!/bin/bash\nexec sleep 5\n")
	fastPath := filepath.Join(tmpDir, "fast.sh")
	writeScript(t, fastPath, "#!/bin/bash\necho fast\n")

	server := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
	registry := newToolRegistry(server, tmpDir, 2*time.Second, 16)
	oneSecond := time.Second
	noTimeout := time.Duration(0)
	registry.replace([]discoveredTool{
		{Name: "pinned", Path: sleepPath, Description: "pinned tool", Timeout: &oneSecond},
		{Name: "inherited", Path: sleepPath, Description: "inherited tool"}, // nil: inherits global
		{Name: "none", Path: fastPath, Description: "none tool", Timeout: &noTimeout},
		{Name: "bare", Path: fastPath, Timeout: &noTimeout}, // empty description: suffix alone
		{Name: "canceller", Path: sleepPath, Timeout: &noTimeout},
	})

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx := context.Background()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect failed: %v", err)
	}
	defer serverSession.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect failed: %v", err)
	}
	defer clientSession.Close()

	res, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}
	descs := make(map[string]string, len(res.Tools))
	for _, tool := range res.Tools {
		descs[tool.Name] = tool.Description
	}
	wantDescs := map[string]string{
		"pinned":    "pinned tool (timeout: 1s)",
		"inherited": "inherited tool (timeout: 2s)",
		"none":      "none tool (timeout: none)",
		"bare":      "(timeout: none)",
		"canceller": "(timeout: none)",
	}
	for name, want := range wantDescs {
		if got := descs[name]; got != want {
			t.Errorf("description of %q = %q, want %q", name, got, want)
		}
	}

	t.Run("per_tool_timeout_wins", func(t *testing.T) {
		start := time.Now()
		result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "pinned"})
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("CallTool failed: %v", err)
		}
		if result == nil || !result.IsError {
			t.Fatalf("expected IsError result, got %#v", result)
		}
		if got := result.Content[0].(*mcp.TextContent).Text; !strings.Contains(got, "timed out after 1s") {
			t.Fatalf("expected timeout message with 1s, got %q", got)
		}
		if elapsed > 4*time.Second {
			t.Errorf("call took %v, want well under the 5s script duration", elapsed)
		}
	})

	t.Run("global_timeout_inherited", func(t *testing.T) {
		start := time.Now()
		result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "inherited"})
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("CallTool failed: %v", err)
		}
		if result == nil || !result.IsError {
			t.Fatalf("expected IsError result, got %#v", result)
		}
		if got := result.Content[0].(*mcp.TextContent).Text; !strings.Contains(got, "timed out after 2s") {
			t.Fatalf("expected timeout message with 2s, got %q", got)
		}
		if elapsed > 4*time.Second {
			t.Errorf("call took %v, want well under the 5s script duration", elapsed)
		}
	})

	t.Run("none_under_short_global_completes", func(t *testing.T) {
		start := time.Now()
		result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "none"})
		if err != nil {
			t.Fatalf("CallTool failed: %v", err)
		}
		if result == nil || result.IsError {
			t.Fatalf("expected success result, got %#v", result)
		}
		if got := result.Content[0].(*mcp.TextContent).Text; !strings.Contains(got, "fast") {
			t.Fatalf("expected fast output, got %q", got)
		}
		if elapsed := time.Since(start); elapsed > 4*time.Second {
			t.Errorf("call took %v, want well under the 2s global", elapsed)
		}
	})

	t.Run("cancellation_kills_script_without_deadline", func(t *testing.T) {
		callCtx, cancel := context.WithCancel(ctx)
		go func() {
			time.Sleep(500 * time.Millisecond)
			cancel()
		}()
		start := time.Now()
		result, err := clientSession.CallTool(callCtx, &mcp.CallToolParams{Name: "canceller"})
		elapsed := time.Since(start)
		if err == nil && (result == nil || !result.IsError) {
			t.Fatalf("expected error or IsError result after cancellation, got %#v", result)
		}
		if elapsed > 4*time.Second {
			t.Errorf("canceled call took %v, want ~500ms (request ctx kills the script)", elapsed)
		}
	})
}

func TestRequiredParamValidationViaRegistry(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "convert.sh")
	writeScript(t, scriptPath, "#!/bin/bash\necho done\n")

	server := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
	registry := newToolRegistry(server, tmpDir, defaultToolTimeout, 16)

	registry.replace([]discoveredTool{
		{
			Name:        "convert",
			Path:        scriptPath,
			Description: "Convert a file",
			Params: []paramSpec{
				{Name: "path", Type: "string", Required: true, Description: "Input path"},
			},
		},
	})

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx := context.Background()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "client"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer clientSession.Close()

	result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "convert"})
	if err != nil {
		t.Fatalf("CallTool returned unexpected error: %v", err)
	}
	if result == nil || !result.IsError {
		t.Fatalf("expected IsError result, got %#v", result)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "missing required parameter: path") {
		t.Fatalf("expected error message containing 'missing required parameter: path', got %q", text)
	}
}

func TestRegisteredDescription(t *testing.T) {
	t.Parallel()
	tests := []struct {
		desc    string
		timeout time.Duration
		want    string
	}{
		{"do things", 30 * time.Second, "do things (timeout: 30s)"},
		{"do things", 5 * time.Minute, "do things (timeout: 5m0s)"},
		{"do things", 0, "do things (timeout: none)"},
		{"", 30 * time.Second, "(timeout: 30s)"},
		{"", 0, "(timeout: none)"},
	}
	for _, tt := range tests {
		if got := registeredDescription(tt.desc, tt.timeout); got != tt.want {
			t.Errorf("registeredDescription(%q, %v) = %q, want %q", tt.desc, tt.timeout, got, tt.want)
		}
	}
}

func TestExecSlot(t *testing.T) {
	t.Parallel()
	s := newExecSlot(2)
	if !s.tryAcquire() || !s.tryAcquire() {
		t.Fatal("expected two acquisitions to succeed with limit 2")
	}
	if s.tryAcquire() {
		t.Fatal("expected the third acquisition to fail at capacity")
	}
	s.release()
	if !s.tryAcquire() {
		t.Fatal("expected an acquisition to succeed after release")
	}
	// Non-positive limits fall back to the default.
	if got := newExecSlot(0).limit; got != defaultMaxConcurrentTools {
		t.Fatalf("newExecSlot(0).limit = %d, want %d", got, defaultMaxConcurrentTools)
	}
}

func TestToolCapacityViaRegistry(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	slowPath := filepath.Join(tmpDir, "slow.sh")
	writeScript(t, slowPath, "#!/bin/bash\nsleep 3\n")
	noTimeout := time.Duration(0)

	server := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
	registry := newToolRegistry(server, tmpDir, 30*time.Second, 1) // capacity 1
	registry.replace([]discoveredTool{
		{Name: "slow", Path: slowPath, Description: "slow tool", Timeout: &noTimeout},
	})

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx := context.Background()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "client"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer clientSession.Close()

	makeParams := func() *mcp.CallToolParams {
		return &mcp.CallToolParams{Name: "slow"}
	}

	// First call holds the only slot.
	firstDone := make(chan *mcp.CallToolResult, 1)
	go func() {
		res, err := clientSession.CallTool(ctx, makeParams())
		if err != nil {
			t.Errorf("first call returned error: %v", err)
		}
		firstDone <- res
	}()

	// Wait for the first call to take the only slot: a bounded poll of the
	// slot itself (package-internal) instead of a fixed sleep, so the test
	// cannot flake on a slow scheduler.
	deadline := time.Now().Add(5 * time.Second)
	for registry.slot.tryAcquire() {
		registry.slot.release()
		if time.Now().After(deadline) {
			t.Fatal("first call never acquired the slot")
		}
		time.Sleep(5 * time.Millisecond)
	}

	res, err := clientSession.CallTool(ctx, makeParams())
	if err != nil {
		t.Fatalf("second call returned an error: %v (want a clean at-capacity result)", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError at-capacity result, got %#v", res)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "at capacity") || !strings.Contains(text, "1 concurrent") {
		t.Fatalf("at-capacity text = %q, want it to name the limit", text)
	}

	// The first call must eventually finish (nothing was blocked or killed).
	select {
	case res1 := <-firstDone:
		if res1.IsError {
			t.Fatalf("first call failed: %v", res1.Content[0])
		}
	case <-time.After(10 * time.Second):
		t.Fatal("first call did not finish")
	}
}

// TestRegistryReplaceConcurrency: the registry mutex must keep names/current
// consistent under concurrent replace() calls (the watcher and a test may
// both race the registry in future configurations).
func TestRegistryReplaceConcurrency(t *testing.T) {
	t.Parallel()
	server := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
	registry := newToolRegistry(server, "", 30*time.Second, 16)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				var tools []discoveredTool
				for k := 0; k < 50; k++ {
					tools = append(tools, discoveredTool{Name: fmt.Sprintf("tool%d", k), Description: "d"})
				}
				registry.replace(tools)
			}
		}()
	}
	wg.Wait()
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if len(registry.names) != 50 {
		t.Fatalf("registry has %d names after concurrent replaces, want 50", len(registry.names))
	}
	if len(registry.current) != 50 {
		t.Fatalf("registry.current has %d tools after concurrent replaces, want 50", len(registry.current))
	}
}
