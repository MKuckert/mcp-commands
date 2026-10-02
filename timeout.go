package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

const (
	defaultToolTimeout = 5 * time.Minute

	toolKillWaitDelay = 5 * time.Second // Wait backstop after a deadline kill
)

// timeoutUnits maps the allowed duration units to their values. Sub-second
// units are not meaningful for tool timeouts and are deliberately absent;
// tokens are matched prefix-based, so "1h30m5s" and "1h 30m 5s" both parse.
var timeoutUnits = map[string]time.Duration{
	"s": time.Second,
	"m": time.Minute,
	"h": time.Hour,
}

// parseTimeoutDuration parses a formatted timeout duration string: a list of
// <digits><unit> tokens (units s, m, h; e.g. "5m", "60s", "1h 30m 5s",
// "1h30m5s"); whitespace between tokens is optional. Sub-second units,
// decimals, signs, and bare units are rejected. The literal NONE
// (case-insensitive, surrounding whitespace trimmed) and a result of 0 both
// mean "no timeout" and yield 0.
func parseTimeoutDuration(raw string) (time.Duration, error) {
	if strings.EqualFold(strings.TrimSpace(raw), "NONE") {
		return 0, nil
	}
	var total time.Duration
	seen := false
	const maxDuration = time.Duration(math.MaxInt64)
	for rest := raw; len(rest) > 0; {
		rest = strings.TrimLeft(rest, " \t")
		if rest == "" {
			break
		}
		i := 0
		for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
			i++
		}
		if i == 0 {
			return 0, fmt.Errorf("invalid timeout %q: expected <digits><unit> (units s, m, h) or NONE", rest)
		}
		multiplier, after, ok := matchTimeoutUnit(rest[i:])
		if !ok {
			return 0, fmt.Errorf("invalid timeout unit %q: allowed units are s, m, h", rest[i:])
		}
		n, err := strconv.ParseUint(rest[:i], 10, 63)
		if err != nil {
			return 0, fmt.Errorf("invalid timeout amount %q: %v", rest[:i], err)
		}
		// Overflow guards: individually-valid terms can still multiply or sum
		// into int64 wraparound, which would silently yield a POSITIVE
		// duration. Reject before the arithmetic happens.
		if n > uint64(maxDuration)/uint64(multiplier) {
			return 0, fmt.Errorf("timeout duration %q overflows", raw)
		}
		term := time.Duration(n) * multiplier
		if total > maxDuration-term {
			return 0, fmt.Errorf("timeout duration %q overflows", raw)
		}
		total += term
		rest = after
		seen = true
	}
	if !seen {
		return 0, fmt.Errorf("timeout duration %q is empty, expected <digits><unit> (units s, m, h) or NONE", raw)
	}
	return total, nil
}

// matchTimeoutUnit matches a unit at the start of s and returns its
// multiplier plus the remainder of the string.
func matchTimeoutUnit(s string) (time.Duration, string, bool) {
	for _, unit := range []string{"s", "m", "h"} {
		if strings.HasPrefix(s, unit) {
			return timeoutUnits[unit], s[len(unit):], true
		}
	}
	return 0, "", false
}

// resolveTimeout resolves the global tool timeout from CLI flags, fail-fast
// before the server starts. --timeout and --no-timeout are mutually exclusive
// (passing both is a startup error); an explicitly-set --timeout must parse
// (an explicit --timeout= is therefore rejected); an unset --timeout yields
// defaultToolTimeout.
func resolveTimeout(timeoutFlag string, timeoutSet, noTimeout bool) (time.Duration, error) {
	if noTimeout && timeoutSet {
		return 0, fmt.Errorf("--timeout and --no-timeout are mutually exclusive")
	}
	if noTimeout {
		return 0, nil
	}
	if timeoutSet {
		return parseTimeoutDuration(timeoutFlag)
	}
	return defaultToolTimeout, nil
}

// resolveToolTimeout resolves a tool's effective timeout with the registry's
// precedence: a per-tool Timeout: always wins over the global, even
// --no-timeout. Shared by the registry and the --list-tools renderer so the
// two call sites cannot drift.
func resolveToolTimeout(tool discoveredTool, global time.Duration) time.Duration {
	if tool.Timeout != nil {
		return *tool.Timeout
	}
	return global
}

// timeoutSuffix renders the resolved timeout for the registered tool
// description so the LLM knows its budget: "(timeout: 30s)" or
// "(timeout: none)" when no deadline applies.
func timeoutSuffix(timeout time.Duration) string {
	if timeout > 0 {
		return fmt.Sprintf("(timeout: %s)", timeout)
	}
	return "(timeout: none)"
}
