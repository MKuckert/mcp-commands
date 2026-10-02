//go:build !unix

package main

import (
	"os/exec"
)

// armToolProcess: process groups are a unix concept (Setpgid); on other
// platforms the timeout kills the direct child only, same as the stock
// exec.CommandContext.
func armToolProcess(cmd *exec.Cmd) {}

// killToolProcess kills the direct child (the best available on non-unix).
func killToolProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
