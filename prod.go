package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"golang.org/x/term"
)

// This file collects the production-only glue that static tests cannot
// reach: it talks to the real terminal (TTY detection, window size, screen
// erase) or ends the process (os.Exit). Tests exercise the same behavior
// through liveEnv's injectable seams (see server.go) with local fakes, so
// the rest of the package stays fully testable.
//
// The CI coverage gate excludes this file (see .github/workflows/ci.yml):
// it is the documented, visible remainder, not a hidden carve-out.

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
	env := prodLiveEnv(cfg.logLevel)
	if cfg.mode != modeServer {
		os.Exit(runDiagnostic(env, cfg.diagnostic))
	}
	if err := run(context.Background(), env, cfg.server); err != nil {
		env.log.Error(err.Error())
		os.Exit(1)
	}
}

// prodClearScreen clears the terminal (ANSI erase-screen + cursor-home). It
// is a no-op when stdout is not a TTY, so piped output simply accumulates.
func prodClearScreen(stdout io.Writer) {
	file, ok := stdout.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return
	}
	_, _ = stdout.Write([]byte("\x1b[2J\x1b[H"))
}

// prodResolveWrapWidth returns the wrap width for --list-tools output: the
// terminal window width (in runes) when stdout is a TTY (re-queried at every
// print so window resizes are honored), falling back to listWrapWidth when
// stdout is not a *os.File, not a terminal, or the query fails.
func prodResolveWrapWidth(stdout io.Writer) int {
	file, ok := stdout.(*os.File)
	if !ok {
		return listWrapWidth
	}
	fd := int(file.Fd())
	if !term.IsTerminal(fd) {
		return listWrapWidth
	}
	width, _, err := term.GetSize(fd)
	if err != nil || width <= 0 {
		return listWrapWidth
	}
	return width
}
