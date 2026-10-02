package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
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
	registry := newToolRegistry(server, "", defaultToolTimeout, 16)
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
	registry := newToolRegistry(server, "", defaultToolTimeout, 16)
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
	registry := newToolRegistry(server, "", defaultToolTimeout, 16)
	initial, err := discoverTools(tmpDir)
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

	// The startup replace runs unconditionally (the startup double-scan is out
	// of scope); wait for its notification(s) to drain, then take a baseline.
	deadline := time.Now().Add(5 * time.Second)
	for changed.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	baseline := changed.Load()

	// No-op touches: same content → identical set → must be skipped. Bounded
	// retries: a lost fsnotify event is legal, so keep
	// touching until a rescan window has elapsed with zero notifications —
	// that silence is the assertion.
	noopSawChange := false
	for i := 0; i < 10; i++ {
		writeScript(t, scriptPath, "#!/bin/bash\n# Description: alpha\necho alpha\n")
		time.Sleep(600 * time.Millisecond) // several debounce windows
		if changed.Load() != baseline {
			noopSawChange = true
			break
		}
		if i > 2 { // a few clean windows: the skip is working
			break
		}
	}
	if noopSawChange {
		t.Fatalf("no-op rescan emitted %d list_changed notification(s) above baseline, want 0", changed.Load()-baseline)
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
	registry := newToolRegistry(server, "", 2*time.Second, 16)
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

	// Re-write until the change is observed: the first write can race the
	// watcher registration (a lost event is legal for fsnotify), and only
	// writes landing after Add is guaranteed to produce a debounced fire.
	for i := 0; i < 40; i++ {
		writeScript(t, scriptPath, "#!/bin/bash\necho beta\n")
		deadline := time.Now().Add(2 * time.Second)
		for calls.Load() == 0 && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if calls.Load() > 0 {
			break
		}
	}
	if calls.Load() == 0 {
		t.Fatal("onChange did not fire on file change")
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
	if n := calls.Load(); n != 0 {
		t.Errorf("onChange fired %d times with zero events, want 0", n)
	}
}

// TestWatchToolsWatchedDirDeleted: rediscovery must warn and the watcher
// must keep running when the scripts directory disappears mid-watch.

func TestWatchToolsWatchedDirDeleted(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	server := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
	registry := newToolRegistry(server, "", defaultToolTimeout, 16)

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
		if strings.Contains(string(output), "Warning: failed to rediscover tools:") {
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

	// The first rename can race watcher registration (a lost event is legal
	// for fsnotify); retry with a fresh rename until the fire is observed.
	for i := 0; i < 10; i++ {
		if err := os.Rename(scriptPath, filepath.Join(tmpDir, "beta.sh")); err != nil {
			t.Fatalf("failed to rename script: %v", err)
		}
		deadline := time.Now().Add(2 * time.Second)
		for calls.Load() == 0 && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if calls.Load() > 0 {
			break
		}
		// Prepare for the next attempt: rename back.
		_ = os.Rename(filepath.Join(tmpDir, "beta.sh"), scriptPath)
	}
	if calls.Load() == 0 {
		t.Fatal("onChange did not fire on rename")
	}
}

// TestWatchChangesPermissionError: a watcher permission error
// (chmod the watched dir unreadable → inotify can no longer track it) must
// be logged and swallowed, not fatal. Skipped when running as root —
// uid 0 bypasses file permissions, so the error is not reproducible (the
// sandbox and CI run as root; real user installs are covered).
// A real fsnotify permission error is not deterministically reproducible
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
	injected <- errors.New("permission denied (synthetic)")

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

	// The loop must survive the error and still process real events.
	if err := os.WriteFile(filepath.Join(tmpDir, "trigger.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("trigger write failed: %v", err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for calls.Load() == 0 {
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
	registry := newToolRegistry(server, "", defaultToolTimeout, 16)
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
			// The registry swaps remove-then-add, so the live list is
			// transiently empty between the two; a 0-tool snapshot is a
			// valid in-flight state, not an error — keep polling.
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
