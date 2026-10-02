//go:build unix

package main

import (
	"os/exec"
	"syscall"
)

// armToolProcess starts the tool in its own process group: the script's
// grandchildren (shell pipelines, spawned daemons) inherit the group, so the
// deadline can kill the whole tree, not just the direct child.
func armToolProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killToolProcess SIGKILLs the tool's entire process group (−pid). It is a
// no-op when the process never started.
func killToolProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
