//go:build unix

package main

import (
	"fmt"
	"os"
	"syscall"
)

// statIdentity extracts the (device, inode) pair from a stat result. On unix
// this is the authoritative identity anchor for the exec-time recheck.
func statIdentity(info os.FileInfo) (dev, ino uint64) {
	if si, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(si.Dev), si.Ino
	}
	return 0, 0
}

// openToolAnchor opens the tool file at registration time (F-13 layer 1):
// the returned descriptor is the identity anchor — the child execs that
// inode via /dev/fd, so no later swap of the path can change what runs.
// A nil result (with a stderr warning) means the anchor is unavailable and
// the exec-time path check applies instead; registration never fails because
// of it.
func openToolAnchor(tool discoveredTool) *os.File {
	f, err := os.OpenFile(tool.Path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: cannot anchor tool %s (%v); exec-time path check applies\n", tool.Path, err)
		return nil
	}
	return f
}

// prepareExec verifies the tool at exec time (F-13) and returns what the
// child should exec plus the descriptor to hand it. opened reports whether
// the descriptor was opened HERE (and must be closed after the call) or is
// the caller's anchor (which the caller owns):
//
//   - with an anchor fd (layer 1, registry): the descriptor itself is the
//     identity — the child execs the opened inode, so a path swap cannot
//     change what runs. A sanity fstat guards a descriptor that is no
//     longer a regular file;
//
//   - without one (layer 2, diagnostic mode): the path is opened with
//     O_NOFOLLOW — a symlink planted at the (canonical) tool path fails at
//     the open stage — and the opened fd must be a regular, executable
//     file whose (dev, inode) matches discovery.
//
//   - the path is opened with O_NOFOLLOW: a symlink planted at the
//     (canonical) tool path fails at the open stage;
//   - the opened fd is fstat'ed and must be a regular, executable file whose
//     (dev, inode) matches the discovery-time identity (skipped when the
//     identity is unknown, i.e. zero);
//   - the returned script argument is /dev/fd/3 — the opened inode itself —
//     so the file that runs is the file that was verified. A swap after our
//     open cannot change what runs.
//
// The caller must pass the file via cmd.ExtraFiles and close it after
// cmd.Wait.
func prepareExec(tool discoveredTool, anchorFD *os.File) (scriptArg string, scriptFD *os.File, opened bool, err error) {
	if anchorFD != nil {
		fi, err := anchorFD.Stat()
		if err != nil {
			return "", nil, false, fmt.Errorf("anchor descriptor no longer usable: %w", err)
		}
		if !fi.Mode().IsRegular() {
			return "", nil, false, fmt.Errorf("%s is not a regular file", tool.Path)
		}
		return "/dev/fd/3", anchorFD, false, nil
	}

	f, err := os.OpenFile(tool.Path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", nil, false, fmt.Errorf("cannot open %s (replaced or re-symlinked?): %w", tool.Path, err)
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return "", nil, false, fmt.Errorf("cannot stat opened file %s: %w", tool.Path, err)
	}
	if !fi.Mode().IsRegular() {
		f.Close()
		return "", nil, false, fmt.Errorf("%s is not a regular file", tool.Path)
	}
	if fi.Mode()&0111 == 0 {
		f.Close()
		return "", nil, false, fmt.Errorf("%s is not executable", tool.Path)
	}
	if tool.dev != 0 || tool.ino != 0 {
		if si, ok := fi.Sys().(*syscall.Stat_t); ok && (uint64(si.Dev) != tool.dev || si.Ino != tool.ino) {
			f.Close()
			return "", nil, false, fmt.Errorf("%s was replaced after discovery (inode changed)", tool.Path)
		}
	}
	return "/dev/fd/3", f, true, nil
}
