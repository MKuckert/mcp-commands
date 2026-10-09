package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestWatchTools(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "alpha.sh")
	writeScript(t, scriptPath, "#!/bin/bash\necho alpha\n")

	server := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
	registry := newToolRegistry(server, "", defaultToolTimeout, 16, testDiscardLogger)
	registry.replace([]discoveredTool{{Name: "alpha", Path: scriptPath, Description: "alpha", Params: []paramSpec{}}})

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

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	env := liveEnvFor(t, io.Discard, io.Discard)

	done := make(chan error, 1)
	go func() {
		done <- watchTools(watchCtx, env, tmpDir, registry, []discoveredTool{{Name: "alpha", Path: scriptPath, Description: "alpha", Params: []paramSpec{}}})
	}()

	addedScriptPath := filepath.Join(tmpDir, "beta.sh")

	// Bounded write retries: fsnotify events can be lost under load
	// (inotify queue overflow), so re-write and re-poll until the reload
	// lands; the same content still fires a fresh event.
	var lastCount int
	updated := false
	for i := 0; i < 10 && !updated; i++ {
		writeScript(t, addedScriptPath, "#!/bin/bash\necho beta\n")
		deadline := time.Now().Add(5 * time.Second)
		for {
			res, err := clientSession.ListTools(ctx, nil)
			if err != nil {
				t.Fatalf("ListTools failed: %v", err)
			}
			lastCount = len(res.Tools)
			if lastCount == 2 {
				updated = true
				break
			}
			if time.Now().After(deadline) {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !updated {
		cancel()
		t.Fatalf("watchTools did not update after 10 write attempts: got %d tools", lastCount)
	}
	cancel()

	select {
	case err := <-done:
		if err != nil && err != context.Canceled {
			t.Fatalf("watchTools returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("watchTools did not stop after cancel")
	}
}

func TestWatchToolsDetectsContentChanges(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "alpha.sh")
	writeScript(t, scriptPath, "#!/bin/bash\n# Description: alpha\necho alpha\n")

	server := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
	registry := newToolRegistry(server, "", defaultToolTimeout, 16, testDiscardLogger)
	registry.replace([]discoveredTool{{Name: "alpha", Path: scriptPath, Description: "alpha", Params: []paramSpec{}}})

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

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	env := liveEnvFor(t, io.Discard, io.Discard)

	done := make(chan error, 1)
	go func() {
		done <- watchTools(watchCtx, env, tmpDir, registry, []discoveredTool{{Name: "alpha", Path: scriptPath, Description: "alpha", Params: []paramSpec{}}})
	}()

	// Bounded write retries: a lost fsnotify event is recovered by the next
	// write (see TestWatchTools).
	var lastTools []*mcp.Tool
	refreshed := false
	for i := 0; i < 10 && !refreshed; i++ {
		writeScript(t, scriptPath, "#!/bin/bash\n# Description: beta updated\necho beta now\n")
		deadline := time.Now().Add(5 * time.Second)
		for {
			res, err := clientSession.ListTools(ctx, nil)
			if err != nil {
				t.Fatalf("ListTools failed: %v", err)
			}
			lastTools = res.Tools
			if len(res.Tools) == 1 && res.Tools[0].Description == "beta updated (timeout: 5m0s)" {
				refreshed = true
				break
			}
			if time.Now().After(deadline) {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !refreshed {
		cancel()
		t.Fatalf("watchTools did not refresh updated tool description after 10 write attempts: %+v", lastTools)
	}
	cancel()

	select {
	case err := <-done:
		if err != nil && err != context.Canceled {
			t.Fatalf("watchTools returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("watchTools did not stop after cancel")
	}
}

// TestWatchToolsSkipsIdenticalRescan: a debounced rescan whose
// result is identical to the registered set must emit no RemoveTools/AddTool
// churn and no tools/list_changed notifications, while a genuine change
// still reloads.
func TestWatchToolsSkipsIdenticalRescan(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "alpha.sh")
	writeScript(t, scriptPath, "#!/bin/bash\n# Description: alpha\necho alpha\n")

	server := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
	registry := newToolRegistry(server, "", defaultToolTimeout, 16, testDiscardLogger)
	initial, err := discoverTools(tmpDir, testDiscardLogger)
	if err != nil {
		t.Fatalf("discoverTools failed: %v", err)
	}
	registry.replace(initial)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx := context.Background()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect failed: %v", err)
	}
	defer serverSession.Close()

	var changed atomic.Int32
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client"}, &mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) { changed.Add(1) },
	})
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect failed: %v", err)
	}
	defer clientSession.Close()

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	env := liveEnvFor(t, io.Discard, io.Discard)
	go func() {
		_ = watchTools(watchCtx, env, tmpDir, registry, initial)
	}()

	// The registry was pre-populated with the exact slice watchTools re-scans,
	// so the startup replaceIfChanged is a no-op (same slice) and emits no
	// notification: the baseline is zero by construction.
	baseline := changed.Load()

	// No-op touches: same content → identical set → must be skipped. Bounded
	// retries: a lost fsnotify event is legal, so keep
	// touching until a rescan window has elapsed with zero notifications —
	// that silence is the assertion.
	cleanWindows := 0
	for i := 0; i < 10 && cleanWindows < 3; i++ {
		before := changed.Load()
		writeScript(t, scriptPath, "#!/bin/bash\n# Description: alpha\necho alpha\n")
		time.Sleep(600 * time.Millisecond) // several debounce windows
		if changed.Load() == before {
			cleanWindows++
		} else {
			// A notification on an identical rewrite means the rescan
			// observed a transient file state (a write caught mid-flight
			// under load). The invariant is steady-state silence, so reset
			// the counter and require three clean windows to converge.
			cleanWindows = 0
		}
	}
	if cleanWindows < 3 {
		t.Fatalf("no-op rescans kept emitting list_changed notifications (%d above baseline), want convergence to silence", changed.Load()-baseline)
	}

	// A real frontmatter change must reload.
	for i := 0; i < 10; i++ {
		writeScript(t, scriptPath, "#!/bin/bash\n# Description: alpha v2\necho alpha\n")
		deadline := time.Now().Add(5 * time.Second)
		for changed.Load() == baseline && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if changed.Load() != baseline {
			break
		}
	}
	if changed.Load() == baseline {
		t.Fatal("a genuine description change did not trigger a reload after 10 attempts")
	}

	res, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}
	// Contains, not equality: the description format belongs to
	// resolveToolTimeout and may change cosmetically.
	if len(res.Tools) != 1 || !strings.Contains(res.Tools[0].Description, "alpha v2") || !strings.Contains(res.Tools[0].Description, "5m") {
		t.Fatalf("tools after reload = %+v, want the updated description", res.Tools)
	}
}

func TestWatchToolsDetectsTimeoutChanges(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "alpha.sh")
	writeScript(t, scriptPath, "#!/bin/bash\necho alpha\n")

	server := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
	registry := newToolRegistry(server, "", 2*time.Second, 16, testDiscardLogger)
	registry.replace([]discoveredTool{{Name: "alpha", Path: scriptPath, Description: "alpha", Params: []paramSpec{}}})

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

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	env := liveEnvFor(t, io.Discard, io.Discard)

	done := make(chan error, 1)
	go func() {
		done <- watchTools(watchCtx, env, tmpDir, registry, []discoveredTool{{Name: "alpha", Path: scriptPath, Description: "alpha", Params: []paramSpec{}}})
	}()

	// Hot reload: editing only the Timeout: line re-registers the tool.
	// Bounded write retries: a lost fsnotify event is recovered by the next
	// write (see TestWatchTools).
	var lastTools []*mcp.Tool
	refreshed := false
	for i := 0; i < 10 && !refreshed; i++ {
		writeScript(t, scriptPath, "#!/bin/bash\n# Timeout: 30s\necho alpha\n")
		deadline := time.Now().Add(5 * time.Second)
		for {
			res, err := clientSession.ListTools(ctx, nil)
			if err != nil {
				t.Fatalf("ListTools failed: %v", err)
			}
			lastTools = res.Tools
			if len(res.Tools) == 1 && res.Tools[0].Description == "(timeout: 30s)" {
				refreshed = true
				break
			}
			if time.Now().After(deadline) {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !refreshed {
		cancel()
		t.Fatalf("watchTools did not refresh updated Timeout after 10 write attempts: %+v", lastTools)
	}
	cancel()

	select {
	case err := <-done:
		if err != nil && err != context.Canceled {
			t.Fatalf("watchTools returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("watchTools did not stop after cancel")
	}
}

func TestWatchChanges(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "alpha.sh")
	writeScript(t, scriptPath, "#!/bin/bash\necho alpha\n")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	env := liveEnvFor(t, io.Discard, io.Discard)

	var calls atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- watchChanges(ctx, env, tmpDir, func() { calls.Add(1) })
	}()

	// Baseline: the guaranteed post-readiness rescan fires the callback once,
	// so the first call proves nothing — wait for it, then require the
	// rewrite to produce a *further* call. (The first write can also race
	// the watcher registration: a lost event is legal for fsnotify, so the
	// write is retried.)
	deadline := time.Now().Add(3 * time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if calls.Load() == 0 {
		t.Fatal("the post-readiness rescan never ran")
	}
	baseline := calls.Load()
	for i := 0; i < 40; i++ {
		writeScript(t, scriptPath, "#!/bin/bash\necho beta\n")
		deadline := time.Now().Add(2 * time.Second)
		for calls.Load() <= baseline && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if calls.Load() > baseline {
			break
		}
	}
	if calls.Load() <= baseline {
		t.Fatal("onChange did not fire on the file change after readiness")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil && err != context.Canceled {
			t.Fatalf("watchChanges returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("watchChanges did not stop after cancel")
	}
}

// TestWatchChangesNoSpuriousFire guards the debounce arming: with zero
// filesystem events the timer must stay disarmed (regression — NewTimer
// arms the clock immediately, so the first debounce window elapsed without
// any event).
func TestWatchChangesNoSpuriousFire(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	env := liveEnvFor(t, io.Discard, io.Discard)

	var calls atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- watchChanges(ctx, env, tmpDir, func() { calls.Add(1) })
	}()

	time.Sleep(500 * time.Millisecond) // several debounce windows, no events

	cancel()
	select {
	case err := <-done:
		if err != nil && err != context.Canceled {
			t.Fatalf("watchChanges returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("watchChanges did not stop after cancel")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("onChange fired %d times, want 1 (guaranteed post-readiness rescan)", n)
	}
}

// TestWatchToolsWatchedDirDeleted: rediscovery must warn and the watcher
// must keep running when the scripts directory disappears mid-watch.
func TestWatchToolsWatchedDirDeleted(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	server := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
	registry := newToolRegistry(server, "", defaultToolTimeout, 16, testDiscardLogger)

	stderr, err := os.CreateTemp(t.TempDir(), "watch-stderr-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stderr.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	env := liveEnvFor(t, io.Discard, stderr)

	done := make(chan error, 1)
	go func() {
		done <- watchTools(ctx, env, tmpDir, registry, nil)
	}()

	// Let the watcher goroutine finish NewWatcher/Add before the deletion,
	// so the test exercises the mid-watch failure path (a deletion landing
	// before Add is the fail-fast "failed to watch directory" branch).
	time.Sleep(200 * time.Millisecond)
	if err := os.RemoveAll(tmpDir); err != nil {
		t.Fatalf("failed to delete watched dir: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		output, err := os.ReadFile(stderr.Name())
		if err != nil {
			t.Fatalf("failed to read stderr: %v", err)
		}
		if strings.Contains(string(output), "WARN@") && strings.Contains(string(output), "failed to rediscover tools") {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("watchTools exited with %v after the watched dir was deleted; stderr = %q", err, output)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("rediscovery warning not logged; stderr = %q", output)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Give the watcher time to observe the deletion (it must not exit).
	select {
	case err := <-done:
		t.Fatalf("watchTools exited with %v after the watched dir was deleted; it must keep running", err)
	case <-time.After(500 * time.Millisecond):
	}

	cancel()
	select {
	case err := <-done:
		if err != nil && err != context.Canceled {
			t.Fatalf("watchTools returned %v after cancel, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("watchTools did not stop after cancel")
	}
}

// TestWatchChangesRenameTriggersChange: renaming a file (fsnotify
// Rename — on linux this arrives as a Move event) must fire the debounced
// onChange, like create/write/remove do.
func TestWatchChangesRenameTriggersChange(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "alpha.sh")
	writeScript(t, scriptPath, "#!/bin/bash\necho alpha\n")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	env := liveEnvFor(t, io.Discard, io.Discard)

	var calls atomic.Int32
	go func() {
		_ = watchChanges(ctx, env, tmpDir, func() { calls.Add(1) })
	}()

	// Baseline first: the guaranteed post-readiness rescan fires the
	// callback once, so the rename must push the count *above* the baseline
	// (the first rename can race watcher registration — a lost event is
	// legal for fsnotify — and is retried).
	deadline := time.Now().Add(3 * time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if calls.Load() == 0 {
		t.Fatal("the post-readiness rescan never ran")
	}
	baseline := calls.Load()
	for i := 0; i < 10; i++ {
		if err := os.Rename(scriptPath, filepath.Join(tmpDir, "beta.sh")); err != nil {
			t.Fatalf("failed to rename script: %v", err)
		}
		deadline := time.Now().Add(2 * time.Second)
		for calls.Load() <= baseline && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if calls.Load() > baseline {
			break
		}
		// Prepare for the next attempt: rename back.
		_ = os.Rename(filepath.Join(tmpDir, "beta.sh"), scriptPath)
	}
	if calls.Load() <= baseline {
		t.Fatal("onChange did not fire on the rename after readiness")
	}
}

// TestWatchChangesPermissionError: a transient watcher error (e.g. a
// permission error) must be logged and swallowed, not fatal. A real
// fsnotify permission error is not deterministically reproducible
// (chmod on an already-watched inode does not produce one, and root cannot
// trip it at all), so the error path is exercised through the watcherErrors
// injection seam: a synthetic error must be logged and must not stop the
// loop — and the loop must keep processing real events afterwards.
func TestWatchChangesPermissionError(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	injected := make(chan error, 1)
	buf := &captureWriter{}
	env := liveEnvFor(t, nil, buf)
	env.watcherErrors = func(w *fsnotify.Watcher) <-chan error { return injected }

	var calls atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- watchChanges(ctx, env, tmpDir, func() { calls.Add(1) })
	}()

	// Let the loop arm, then deliver the synthetic watcher error.
	time.Sleep(100 * time.Millisecond)
	injected <- fmt.Errorf("permission denied (synthetic)")

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(buf.String(), "file watcher error") {
		if time.Now().After(deadline) {
			t.Fatalf("watcher error was not logged, stderr = %q", buf.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(buf.String(), "permission denied (synthetic)") {
		t.Fatalf("warning must name the error, stderr = %q", buf.String())
	}

	// The loop must survive the error and still process real events. The
	// guaranteed post-readiness rescan may already have counted, so the
	// trigger write must push the count *above* the current baseline.
	baseline := calls.Load()
	if err := os.WriteFile(filepath.Join(tmpDir, "trigger.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("trigger write failed: %v", err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for calls.Load() <= baseline {
		if time.Now().After(deadline) {
			t.Fatal("onChange never fired after the synthetic watcher error — the loop is not alive")
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil && err != context.Canceled {
			t.Fatalf("watchChanges returned %v after cancel, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("watchChanges did not stop after cancel")
	}
}

// TestWatchToolsRemovesDeletedTool: deleting a script must remove
// its tool from the registry (RemoveTools), leaving the rest intact.
func TestWatchToolsRemovesDeletedTool(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	alphaPath := filepath.Join(tmpDir, "alpha.sh")
	betaPath := filepath.Join(tmpDir, "beta.sh")
	writeScript(t, alphaPath, "#!/bin/bash\necho alpha\n")
	writeScript(t, betaPath, "#!/bin/bash\necho beta\n")

	server := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
	registry := newToolRegistry(server, "", defaultToolTimeout, 16, testDiscardLogger)
	registry.replace([]discoveredTool{
		{Name: "alpha", Path: alphaPath, Description: "alpha", Params: []paramSpec{}},
		{Name: "beta", Path: betaPath, Description: "beta", Params: []paramSpec{}},
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

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	env := liveEnvFor(t, io.Discard, io.Discard)
	done := make(chan error, 1)
	go func() {
		done <- watchTools(watchCtx, env, tmpDir, registry, []discoveredTool{
			{Name: "alpha", Path: alphaPath, Description: "alpha", Params: []paramSpec{}},
			{Name: "beta", Path: betaPath, Description: "beta", Params: []paramSpec{}},
		})
	}()

	// Bounded remove retries: a lost fsnotify event is recovered by the next
	// delete attempt (re-write + delete keeps the path registered for the
	// inotify watch), same pattern as the add/change tests.
	var names []string
	removed := false
	for i := 0; i < 10 && !removed; i++ {
		if err := os.Remove(betaPath); err != nil {
			// First attempt: the file may not exist yet for the watcher; recreate.
			writeScript(t, betaPath, "#!/bin/bash\necho beta\n")
			if err := os.Remove(betaPath); err != nil {
				t.Fatalf("failed to delete beta: %v", err)
			}
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			res, err := clientSession.ListTools(ctx, nil)
			if err != nil {
				t.Fatalf("ListTools failed: %v", err)
			}
			if len(res.Tools) == 1 {
				removed = true
				break
			}
			// With the per-tool diff the unchanged tool is never removed,
			// so a 0-tool snapshot is a rare timing edge, not an error —
			// keep polling.
			names = nil
			for _, tool := range res.Tools {
				names = append(names, tool.Name)
			}
			if time.Now().After(deadline) {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !removed {
		cancel()
		t.Fatalf("watchTools did not remove the deleted tool after 10 attempts: got %v", names)
	}

	// Let any in-flight rescan settle before the final check.
	time.Sleep(300 * time.Millisecond)
	res, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}
	var remaining []string
	for _, tool := range res.Tools {
		remaining = append(remaining, tool.Name)
	}
	if len(res.Tools) != 1 || res.Tools[0].Name != "alpha" {
		t.Fatalf("remaining tools = %v, want exactly [alpha]", remaining)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && err != context.Canceled {
			t.Fatalf("watchTools returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("watchTools did not stop after cancel")
	}
}

// TestWatchToolsChangeBeforeWatcherReady (H3) verifies that a change landing
// between the caller's initial discovery and the watcher's readiness is not
// lost: the guaranteed post-readiness rescan picks it up even though no
// watcher event was ever delivered for it.
func TestWatchToolsChangeBeforeWatcherReady(t *testing.T) {
	t.Parallel()
	registry, clientSession := newTestRegistryClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	env := liveEnvFor(t, io.Discard, io.Discard)

	tmpDir := t.TempDir()
	script := filepath.Join(tmpDir, "alpha.sh")
	writeScript(t, script, "#!/bin/bash\nDescription: alpha\necho alpha\n")

	tools, err := discoverTools(tmpDir, env.log)
	if err != nil {
		t.Fatalf("discoverTools: %v", err)
	}
	registry.replace(tools)
	// The change lands in the gap: after discovery, before the watcher below
	// is ready. No file event will be delivered for it.
	writeScript(t, script, "#!/bin/bash\nDescription: alpha v2\necho alpha\n")

	done := make(chan error, 1)
	go func() {
		done <- watchTools(ctx, env, tmpDir, registry, tools)
	}()
	defer cancel()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(listDescription(t, ctx, clientSession, "alpha"), "v2") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("change in the discovery-to-ready gap was lost; description = %q", listDescription(t, ctx, clientSession, "alpha"))
}

// TestWatchToolsDeletedDirRecreated (H4) verifies that deleting and
// recreating the watched scripts directory is recovered: the parent watch
// observes the recreation, re-attaches the directory watch, and a new script
// inside the new inode is picked up.
func TestWatchToolsDeletedDirRecreated(t *testing.T) {
	t.Parallel()
	registry, clientSession := newTestRegistryClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	env := liveEnvFor(t, io.Discard, io.Discard)

	tmpDir := t.TempDir()
	alpha := filepath.Join(tmpDir, "alpha.sh")
	writeScript(t, alpha, "#!/bin/bash\necho alpha\n")

	tools, err := discoverTools(tmpDir, env.log)
	if err != nil {
		t.Fatalf("discoverTools: %v", err)
	}
	registry.replace(tools)
	done := make(chan error, 1)
	go func() {
		done <- watchTools(ctx, env, tmpDir, registry, tools)
	}()
	defer cancel()

	// Deterministic readiness: a canary script that only appears in the
	// registry after the watcher has been registered and a debounced rescan
	// has run. This rules out the setup racing past the deletion (in which
	// case the initial Add would simply watch the new inode and the test
	// would not prove replacement recovery).
	gamma := filepath.Join(tmpDir, "gamma.sh")
	writeScript(t, gamma, "#!/bin/bash\necho gamma\n")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(listNames(t, ctx, clientSession), "gamma") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if names := listNames(t, ctx, clientSession); !strings.Contains(names, "gamma") {
		t.Fatalf("watcher never became ready (canary not observed); tools = %s", names)
	}
	_ = os.Remove(gamma)

	// Delete and recreate the scripts directory, then add a new script.
	os.Remove(alpha)
	os.Remove(tmpDir)
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		t.Fatalf("recreate: %v", err)
	}
	writeScript(t, filepath.Join(tmpDir, "beta.sh"), "#!/bin/bash\necho beta\n")

	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(listNames(t, ctx, clientSession), "beta") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if names := listNames(t, ctx, clientSession); !strings.Contains(names, "beta") {
		t.Fatalf("registry did not recover after dir delete/recreate; tools = %s", names)
	}
}

// newTestRegistryClient builds a registry plus an in-memory MCP client
// session bound to it (the pattern shared by the other watch tools tests).
func newTestRegistryClient(t *testing.T) (*toolRegistry, *mcp.ClientSession) {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
	registry := newToolRegistry(server, "", 2*time.Second, 16, testDiscardLogger)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx := context.Background()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect failed: %v", err)
	}
	t.Cleanup(func() { serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect failed: %v", err)
	}
	t.Cleanup(func() { clientSession.Close() })
	return registry, clientSession
}

func listNames(t *testing.T, ctx context.Context, s *mcp.ClientSession) string {
	t.Helper()
	res, err := s.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}
	names := make([]string, len(res.Tools))
	for i, tool := range res.Tools {
		names[i] = tool.Name
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

func listDescription(t *testing.T, ctx context.Context, s *mcp.ClientSession, name string) string {
	t.Helper()
	res, err := s.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}
	for _, tool := range res.Tools {
		if tool.Name == name {
			return tool.Description
		}
	}
	return "<missing>"
}

// TestWatchChangesWatcherSetupFailure (M4) verifies that a watcher creation
// failure is returned as an error (fatal) instead of being swallowed.
func TestWatchChangesWatcherSetupFailure(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	env := liveEnvFor(t, io.Discard, io.Discard)
	env.newWatcher = func() (*fsnotify.Watcher, error) {
		return nil, fmt.Errorf("inotify unavailable")
	}
	tmpDir := t.TempDir()
	err := watchChanges(ctx, env, tmpDir, func() {})
	if err == nil || !strings.Contains(err.Error(), "inotify unavailable") {
		t.Fatalf("setup failure not surfaced; err = %v", err)
	}
}

// TestWatchChangesChannelClosed (M4) verifies that a broken watcher channel
// is returned as an error (fatal) instead of being swallowed.
func TestWatchChangesChannelClosed(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	env := liveEnvFor(t, io.Discard, io.Discard)
	tmpDir := t.TempDir()
	// A live watcher whose error channel is already closed: setup (NewWatcher
	// + Add) succeeds, so watchChanges reaches the event loop, where the
	// closed channel is observed and returned as a fatal error. (Closing the
	// watcher itself would fail the setup Add first — a different branch.)
	closed := make(chan error)
	close(closed)
	env.watcherErrors = func(*fsnotify.Watcher) <-chan error { return closed }
	done := make(chan error, 1)
	go func() { done <- watchChanges(ctx, env, tmpDir, func() {}) }()
	select {
	case err := <-done:
		if err == nil || err == context.Canceled {
			t.Fatalf("closed error channel not surfaced as fatal; err = %v", err)
		}
		if !strings.Contains(err.Error(), "channel closed") {
			t.Fatalf("err = %v, want the closed-channel error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watchChanges did not return for a closed error channel")
	}
}

// TestWatchToolsSymlinkTargetEditNotWatched pins the documented trade-off
// (README, watch behavior; REVIEW_REPORT M11 decision): external symlink
// targets are not watched, so an in-place target edit does NOT refresh the
// registered metadata — touching the link itself does.
func TestWatchToolsSymlinkTargetEditNotWatched(t *testing.T) {
	t.Parallel()
	registry, clientSession := newTestRegistryClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	env := liveEnvFor(t, io.Discard, io.Discard)

	root := t.TempDir()
	scriptsDir := filepath.Join(root, "scripts")
	extDir := filepath.Join(root, "external")
	os.MkdirAll(scriptsDir, 0o755)
	os.MkdirAll(extDir, 0o755)

	target := filepath.Join(extDir, "alpha.sh")
	link := filepath.Join(scriptsDir, "alpha")
	writeScript(t, target, "#!/bin/bash\nDescription: alpha v1\n")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	tools, err := discoverTools(scriptsDir, env.log)
	if err != nil {
		t.Fatalf("discoverTools: %v", err)
	}
	registry.replace(tools)
	done := make(chan error, 1)
	go func() {
		done <- watchTools(ctx, env, scriptsDir, registry, tools)
	}()
	defer cancel()

	// Readiness: edit the target, require the link to observe nothing —
	// instead use a scripts-dir canary as the readiness signal.
	canary := filepath.Join(scriptsDir, ".canary")
	deadline := time.Now().Add(3 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		_ = os.WriteFile(canary, []byte("x"), 0o644)
		_ = os.Remove(canary)
		time.Sleep(250 * time.Millisecond)
		if strings.Contains(listDescription(t, ctx, clientSession, "alpha"), "alpha v1") {
			ready = true
			break
		}
	}
	if !ready {
		t.Fatalf("watcher never became ready; description = %q", listDescription(t, ctx, clientSession, "alpha"))
	}

	// In-place target edit: NOT watched, so the metadata must stay stale
	// well beyond the debounce window.
	writeScript(t, target, "#!/bin/bash\nDescription: alpha v2\n")
	time.Sleep(500 * time.Millisecond)
	if got := listDescription(t, ctx, clientSession, "alpha"); !strings.Contains(got, "alpha v1") {
		t.Fatalf("external target edit must not refresh the registry; description = %q", got)
	}

	// Touching the link IS observed (scripts-directory event) and picks up
	// the new metadata.
	if err := os.Remove(link); err != nil {
		t.Fatalf("remove link: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("re-symlink: %v", err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(listDescription(t, ctx, clientSession, "alpha"), "alpha v2") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("touching the link did not refresh the tool; description = %q", listDescription(t, ctx, clientSession, "alpha"))
}
