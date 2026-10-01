//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

// openToolAnchor opens a script for exec-anchoring purposes: read-only and
// O_NOFOLLOW, so a symlink (however planted, even in place of the canonical
// path) is refused. Discovery calls this at discovery time and the registry
// re-opens it only when the discovery open failed, so a transient failure
// self-heals on reload. Returns nil on failure — the caller falls back to
// the open-time identity check below, and a tool whose open keeps failing
// is exec'd via the layer-2 path, never blocked forever.
//
// The descriptor is the pin: a tool call execs this opened inode (the
// caller passes it via cmd.ExtraFiles and the script runs as /dev/fd/3),
// so a swap of the path after the open cannot change what runs.
func openToolAnchor(path string) *os.File {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil
	}
	return f
}

// statIdentity extracts the (device, inode) pair from file metadata.
func statIdentity(info os.FileInfo) fileID {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return fileID{dev: uint64(st.Dev), ino: uint64(st.Ino)}
	}
	return fileID{}
}

// prepareExec prepares the exec for a tool call. Two layers:
//
//   - anchorFD != nil (the registry anchor, opened with O_NOFOLLOW): the
//     descriptor's identity is re-checked (fstat — the identity was derived
//     from this very descriptor at discovery) and the child is exec'd via
//     /dev/fd/3 — the opened inode itself — so the file that runs is the
//     file that was verified. A swap after the open cannot change what
//     runs.
//
//   - anchorFD == nil (diagnostic mode, the anchor open failed, or a
//     platform without fd exec): the path is opened with O_NOFOLLOW and its
//     identity (fstat, on the opened descriptor — immune to any swap
//     happening after the open) must match the identity recorded at
//     discovery; the script is exec'd from that same descriptor.
//
// The returned scriptFD is passed to the child via cmd.ExtraFiles (fd 3);
// the caller must close it after cmd.Wait.
func prepareExec(tool discoveredTool, anchorFD *os.File) (scriptArg string, scriptFD *os.File, opened bool, err error) {
	if anchorFD != nil {
		fi, err := anchorFD.Stat()
		if err != nil {
			return "", nil, false, fmt.Errorf("tool %s (%s): anchor descriptor no longer usable: %w", tool.Name, tool.Path, err)
		}
		if !fi.Mode().IsRegular() {
			return "", nil, false, fmt.Errorf("tool %s (%s) is not a regular file", tool.Name, tool.Path)
		}
		if tool.id.ino != 0 && statIdentity(fi) != tool.id {
			return "", nil, false, fmt.Errorf("tool %s (%s): anchor identity changed: refusing to execute", tool.Name, tool.Path)
		}
		return "/dev/fd/3", anchorFD, false, nil
	}

	f, err := os.OpenFile(tool.Path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", nil, false, fmt.Errorf("tool %s: cannot open %s (replaced or relinked as a symlink after discovery?): %w", tool.Name, tool.Path, err)
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return "", nil, false, fmt.Errorf("tool %s (%s): cannot stat: %w", tool.Name, tool.Path, err)
	}
	if !fi.Mode().IsRegular() {
		_ = f.Close()
		return "", nil, false, fmt.Errorf("tool %s (%s) is not a regular file", tool.Name, tool.Path)
	}
	if fi.Mode()&0111 == 0 {
		_ = f.Close()
		return "", nil, false, fmt.Errorf("tool %s (%s) is not executable", tool.Name, tool.Path)
	}
	// (0, 0) means unknown identity (synthesized tools in tests); skip.
	if tool.id.ino != 0 && statIdentity(fi) != tool.id {
		_ = f.Close()
		return "", nil, false, fmt.Errorf("tool %s (%s): inode changed since discovery: refusing to execute", tool.Name, tool.Path)
	}
	return tool.Path, f, true, nil
}
