package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// Development version marker. Release binaries never report it:
// goreleaser injects the tag via ldflags (.goreleaser.yaml), and the
// Makefile injects its own VERSION the same way.
var serverVersion = "commit-local"

// resolveToolPaths resolves --dir/--scripts to absolute paths and verifies
// both are accessible. Shared by the server-mode run() and the diagnostic
// branch so the resolution behavior and error text stay identical in all
// modes.
func resolveToolPaths(dir, scriptsDir string) (dirAbs, scriptsAbs string, err error) {
	scriptsAbs, err = filepath.Abs(scriptsDir)
	if err != nil {
		return "", "", fmt.Errorf("failed to resolve scripts path: %w", err)
	}
	dirAbs, err = filepath.Abs(dir)
	if err != nil {
		return "", "", fmt.Errorf("failed to resolve dir path: %w", err)
	}
	if _, err := os.Stat(scriptsAbs); err != nil {
		return "", "", fmt.Errorf("scripts path inaccessible: %w", err)
	}
	if _, err := os.Stat(dirAbs); err != nil {
		return "", "", fmt.Errorf("dir path inaccessible: %w", err)
	}
	return dirAbs, scriptsAbs, nil
}
