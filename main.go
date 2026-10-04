package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

var serverVersion = "0.9.0"

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

func main() {
	cfg, err := parseCLI(os.Args[1:])
	if err != nil {
		var parseErr *flagParseError
		if errors.As(err, &parseErr) {
			if errors.Is(err, flag.ErrHelp) {
				// Full flag help (every flag and its description), exit 0.
				fmt.Fprint(os.Stdout, parseErr.usage)
				os.Exit(0)
			}
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			fmt.Fprint(os.Stderr, parseErr.usage)
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		if errors.Is(err, errMissingRequiredFlags) {
			fmt.Fprintln(os.Stderr, usageLine)
		}
		os.Exit(1)
	}
	if cfg.version {
		fmt.Println(serverVersion)
		os.Exit(0)
	}
	env := prodLiveEnv()
	if cfg.mode != modeServer {
		os.Exit(runDiagnostic(env, cfg.diagnostic))
	}
	if err := run(context.Background(), env, cfg.server); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
