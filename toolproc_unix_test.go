//go:build !windows

// Unix-only tests for the tool process-group handling: process groups and
// kill(-pid) do not exist on Windows, where toolproc_other.go provides a
// direct-kill no-op equivalent.

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// assertGroupKillDelivered verifies that kill(−pid, SIGKILL) reaches the
// whole group in the current environment (spawn a script whose child inherits
// its process group, group-kill, expect both dead). It returns false when the
// environment silently swallows group signals — some sandboxes' seccomp
// profiles block kill(2) toward Go-exec'd children — in which case the
// caller must skip rather than fail.
func assertGroupKillDelivered(t *testing.T) bool {
	t.Helper()
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "grp.sh")
	writeScript(t, scriptPath, "#!/bin/bash\nsleep 60 &\nsleep 60\n")
	cmd := exec.Command(scriptPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := killToolProcess(cmd); err != nil {
		t.Fatalf("group kill failed: %v", err)
	}
	defer cmd.Wait()
	deadline := time.Now().Add(2 * time.Second)
	for {
		alive := syscall.Kill(cmd.Process.Pid, 0) == nil
		if !alive {
			return true // direct child died → group signal was delivered
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestExecuteToolKillsProcessGroup: the timeout must kill the whole
// process group, not only the direct child — a shell script's grandchildren
// (the canonical tool shape) must not outlive their budget.
func TestExecuteToolKillsProcessGroup(t *testing.T) {
	t.Parallel()
	if !assertGroupKillDelivered(t) {
		t.Skip("this sandbox does not deliver process-group signals to Go-exec'd processes; group kill cannot be verified here")
	}
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "grandchild.pid")
	scriptPath := filepath.Join(tmpDir, "spawner.sh")
	script := "#!/bin/bash\nsleep 60 &\necho $! > " + pidFile + "\nsleep 30\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to create script: %v", err)
	}

	result, err := executeTool(context.Background(), testDiscardLogger, "tool", scriptPath, map[string]any{}, "", 2*time.Second, tmpDir)
	if err != nil {
		t.Fatalf("executeTool returned unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected an IsError timeout result")
	}
	if _, ok := result.Content[0].(*mcp.TextContent); !ok {
		t.Fatalf("unexpected content type %T", result.Content[0])
	}
	text := result.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "timed out") {
		t.Fatalf("expected a timeout message, got: %q", text)
	}

	grandPidRaw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("script never wrote the grandchild pid: %v", err)
	}
	grandPid, err := strconv.Atoi(strings.TrimSpace(string(grandPidRaw)))
	if err != nil {
		t.Fatalf("unparseable pid %q: %v", grandPidRaw, err)
	}

	// The grandchild must be dead after executeTool returns.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := syscall.Kill(grandPid, 0); err == nil {
			if time.Now().After(deadline) {
				t.Fatalf("grandchild %d survived past the timeout kill", grandPid)
			}
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if !errors.Is(err, syscall.ESRCH) {
			t.Fatalf("kill(pid, 0) = %v, want ESRCH (process gone)", err)
		}
		break // gone
	}
}
