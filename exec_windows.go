//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// openToolAnchor (Windows): no /dev/fd exec, so there is no anchor.
func openToolAnchor(_ string) *os.File { return nil }

// prepareExec (Windows): no O_NOFOLLOW, no /dev/fd exec. The identity check
// degrades to a path-level re-verification: the path must resolve, be a
// regular file, and keep its executable attribute. A residual window between
// the check and the exec remains (documented in the README trust-boundary
// section); the inode swap of a persistent file is the F-13 threat this
// covers on unix.
func prepareExec(tool discoveredTool, anchorFD *os.File) (string, *os.File, bool, error) {
	_ = anchorFD // windows never has one
	resolved, err := filepath.EvalSymlinks(tool.Path)
	if err != nil {
		return "", nil, false, fmt.Errorf("tool %s: cannot resolve %s: %w", tool.Name, tool.Path, err)
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return "", nil, false, fmt.Errorf("tool %s (%s): cannot stat: %w", tool.Name, tool.Path, err)
	}
	if !fi.Mode().IsRegular() {
		return "", nil, false, fmt.Errorf("tool %s (%s) is not a regular file", tool.Name, tool.Path)
	}
	if fi.Mode()&0111 == 0 {
		return "", nil, false, fmt.Errorf("tool %s (%s) is not executable", tool.Name, tool.Path)
	}
	return tool.Path, nil, false, nil
}

// statIdentity: Windows file metadata (Win32FileAttributeData) carries no
// (dev, inode); the identity anchor stays zero and the identity check is
// skipped there.
func statIdentity(_ os.FileInfo) fileID { return fileID{} }
