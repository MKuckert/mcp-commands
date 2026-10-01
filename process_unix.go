//go:build !windows

package main

import (
	"os/exec"
	"syscall"
	"time"
)

// applyProcessGroup makes a deadline a real budget for the whole process
// tree, not just the direct child (F-7): the tool runs in its own process
// group, and on cancellation the entire group is SIGKILL'd — the canonical
// tool shape is a shell script that spawns children, and a deadline that only
// killed the direct child left grandchildren running. WaitDelay is a
// backstop: a grandchild that survives the kill can hold the stdout/stderr
// pipes open, which would block cmd.Wait past the deadline.
func applyProcessGroup(cmd *exec.Cmd, waitDelay time.Duration) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// ECHILD: the group is already gone — nothing to do.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && err != syscall.ECHILD {
			return err
		}
		return nil
	}
	cmd.WaitDelay = waitDelay
}
