//go:build windows

package main

import (
	"os/exec"
	"time"
)

// applyProcessGroup is the Windows counterpart of the Unix process-group
// kill (F-7). Windows has no process groups, so the direct child keeps the
// default CommandContext kill; the WaitDelay backstop still applies so a
// surviving descendant holding a pipe open cannot block cmd.Wait.
func applyProcessGroup(cmd *exec.Cmd, waitDelay time.Duration) {
	cmd.WaitDelay = waitDelay
}
