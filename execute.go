package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxToolOutputBytes = 1 << 20

// argumentsToCLIArgs converts a map of parsed arguments into a slice of CLI flags
// formatted for execution. It enforces strict naming rules for keys to prevent
// injection or ambiguity. Boolean values follow POSIX conventions (true -> --flag,
// false -> omitted). Slices are expanded into multiple flags (e.g., --key val1 --key val2).
func argumentsToCLIArgs(args map[string]any) ([]string, error) {
	if len(args) == 0 {
		return nil, nil
	}

	keys := make([]string, 0, len(args))
	for key := range args {
		if !argumentKeyPattern.MatchString(key) {
			// Reject digit-leading keys so we never generate ambiguous flags that can
			// be parsed as positional arguments or mistaken for numeric values.
			return nil, fmt.Errorf("invalid argument key %q: keys must match %s", key, argumentKeyPattern.String())
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)

	cliArgs := make([]string, 0, len(args)*2)
	for _, key := range keys {
		value := args[key]

		// Handle boolean values via type-switch, not string comparison.
		// Per POSIX/GNU conventions: true → emit flag only; false/nil → omit entirely.
		// This does not affect string values "true"/"false", which are still passed as-is.
		if boolVal, isBool := value.(bool); isBool {
			if boolVal {
				// true: emit flag with no value argument
				cliArgs = append(cliArgs, "--"+key)
			}
			// false: omit flag entirely
			continue
		}

		// nil values are omitted entirely (previously emitted as --key "")
		if value == nil {
			continue
		}

		rv := reflect.ValueOf(value)
		if rv.IsValid() && rv.Kind() == reflect.Slice && rv.Type().Elem().Kind() != reflect.Uint8 {
			for i := 0; i < rv.Len(); i++ {
				cliArgs = append(cliArgs, "--"+key, fmt.Sprint(rv.Index(i).Interface()))
			}
			continue
		}

		cliArgs = append(cliArgs, "--"+key, fmt.Sprint(value))
	}

	return cliArgs, nil
}

// textResult builds the single-text CallToolResult that every tool-execution
// outcome takes (text + optional IsError flag).
func textResult(text string, isErr bool) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
		IsError: isErr,
	}
}

// boundedWriter is an io.Writer that accumulates at most limit bytes. Writes
// beyond the limit are consumed (the return value stays len(p), err nil, so
// the subprocess sees a healthy pipe) but not stored; the overflow is
// remembered. This bounds executeTool's memory to O(limit) per stream no
// matter how much a tool prints.
type boundedWriter struct {
	buf       bytes.Buffer
	remaining int
	truncated bool
}

func newBoundedWriter(limit int) *boundedWriter {
	return &boundedWriter{remaining: limit}
}

func (b *boundedWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if b.remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > b.remaining {
		b.buf.Write(p[:b.remaining])
		b.remaining = 0
		b.truncated = true
		return len(p), nil
	}
	// A write that lands exactly on the limit drops nothing: truncated stays false.
	b.buf.Write(p)
	b.remaining -= len(p)
	return len(p), nil
}

// Bytes returns the captured bytes (at most the limit) and whether output
// was dropped to enforce it.
func (b *boundedWriter) Bytes() []byte { return b.buf.Bytes() }

func (b *boundedWriter) Truncated() bool { return b.truncated }

func combineToolOutput(stdout, stderr []byte) string {
	var result bytes.Buffer

	// Write stdout tag if present
	if len(stdout) > 0 {
		result.WriteString("<stdout>\n")
		result.Write(stdout)
		if !bytes.HasSuffix(stdout, []byte("\n")) {
			result.WriteByte('\n')
		}
		result.WriteString("</stdout>")
	}

	// Write stderr tag if present
	if len(stderr) > 0 {
		if result.Len() > 0 {
			result.WriteByte('\n')
		}
		result.WriteString("<stderr>\n")
		result.Write(stderr)
		if !bytes.HasSuffix(stderr, []byte("\n")) {
			result.WriteByte('\n')
		}
		result.WriteString("</stderr>")
	}

	combined := result.Bytes()
	if len(combined) <= maxToolOutputBytes {
		return string(combined)
	}

	// Back off the cut to a UTF-8 rune boundary: a naive byte cut can split a
	// multi-byte rune and hand the LLM invalid bytes. A split rune spans at
	// most 3 bytes, so at most 3 back-offs; for tool output that is already
	// invalid UTF-8 (binary) the bounded loop simply stops, no worse than the
	// raw bytes we would have emitted before.
	cut := maxToolOutputBytes
	for i := 0; i < 3 && cut > 0 && !utf8.Valid(combined[:cut]); i++ {
		cut--
	}
	return string(combined[:cut]) + fmt.Sprintf("\n[output truncated after %d bytes]", maxToolOutputBytes)
}

// formatCommand renders argv as a single shell-style quoted string for the
// `command` record field: every element is double-quoted with escaping and
// the elements are joined with single spaces, so spaces and quotes in a
// path or argument can never be misread as separators.
func formatCommand(argv []string) string {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = strconv.Quote(a)
	}
	return strings.Join(quoted, " ")
}

// maxIdentityFieldBytes bounds a single client-identity field in a tool-call
// record. The HTTP body cap is 10 MiB, so a hostile client can carry megabytes
// in its clientInfo; the log must not become an amplification sink. 256 bytes
// is generous for any real client name/version; over-long values are cut at a
// rune boundary and marked with an explicit truncation indicator.
const maxIdentityFieldBytes = 256

// boundIdentityField cuts s to maxIdentityFieldBytes (at a rune boundary)
// and appends the truncation indicator; short values pass through unchanged.
func boundIdentityField(s string) string {
	if len(s) <= maxIdentityFieldBytes {
		return s
	}
	cut := maxIdentityFieldBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\u2026"
}

// identityAttrs renders the calling client's name and version (from the SDK's
// req.ClientInfo()) as bounded record fields. It returns nil when the identity
// is unavailable — ClientInfo() returned nil, or both fields are empty — so
// the record omits them entirely rather than logging "unknown" per call.
func identityAttrs(info *mcp.Implementation) []any {
	if info == nil {
		return nil
	}
	var attrs []any
	if name := boundIdentityField(info.Name); name != "" {
		attrs = append(attrs, "clientName", name)
	}
	if version := boundIdentityField(info.Version); version != "" {
		attrs = append(attrs, "clientVersion", version)
	}
	return attrs
}

// executeTool runs the script at scriptPath as a subprocess in the specified
// working directory. It accepts pre-parsed arguments as a map, converts them to
// CLI flags, and binds the context to a timeout to prevent hanging tools.
// The output is captured, combined, and returned as an MCP CallToolResult.
//
// Logging (N2–N4): every completed call logs one DEBUG record with the tool,
// the raw command line, the raw request parameters, the exit code, the
// captured byte counts, the duration, and the truncation flag; the TRACE
// record adds the full raw stdout/stderr. A failed call logs one WARN record
// (same fields plus a failure reason and the full raw output) plus the TRACE
// record. The reason taxonomy: "timeout" (the per-call deadline fired),
// "nonzero-exit" (the script ran and exited non-zero), "canceled" (the
// request context was canceled — a client abort with --no-timeout — and the
// process was killed; exitCode is -1), "start-failed" (the script never
// started: no exec, no output). When clientInfo is non-nil and carries a name
// or version (the SDK's req.ClientInfo()), the records additionally carry
// bounded clientName/clientVersion fields.
func executeTool(ctx context.Context, log *slog.Logger, name, scriptPath string, args map[string]any, rawArgs string, clientInfo *mcp.Implementation, timeout time.Duration, dir string) (*mcp.CallToolResult, error) {
	cliArgs, err := argumentsToCLIArgs(args)
	if err != nil {
		return textResult(err.Error(), true), nil
	}

	execCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	// With timeout == 0 the request context itself is used: client
	// cancellation/abort still kills the script, so "no timeout" means
	// "no deadline", never "uninterruptible".

	cmd := exec.CommandContext(execCtx, scriptPath, cliArgs...)
	cmd.Dir = dir
	armToolProcess(cmd)
	// Backstop: a grandchild that detaches the process group (setsid) and
	// keeps an output pipe open would otherwise block Wait forever; after the
	// delay the pipes are closed and Wait returns.
	cmd.WaitDelay = toolKillWaitDelay

	// Bounded capture: a tool printing gigabytes costs O(1 MiB) per stream,
	// not O(output size); combineToolOutput applies the final cap.
	stdout := newBoundedWriter(maxToolOutputBytes)
	stderr := newBoundedWriter(maxToolOutputBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	// Override CommandContext's direct-child kill before Start so exec's
	// context watcher always kills the complete process group.
	cmd.Cancel = func() error { return killToolProcess(cmd) }
	started := time.Now()
	if err := cmd.Start(); err != nil {
		// The script never ran (missing, not executable, bad interpreter):
		// fail loudly and say why. It emits the same complete attribute set
		// as every other failed outcome (N4) — exitCode -1 (no process),
		// zero captured bytes, empty output, nothing truncated — plus the
		// paired TRACE record, so no failed outcome diverges from the
		// documented WARN/TRACE contract.
		startAttrs := []any{
			"tool", name,
			"path", scriptPath,
			"command", formatCommand(cmd.Args),
			"params", rawArgs,
			"exitCode", -1,
			"stdoutBytes", 0,
			"stderrBytes", 0,
			"duration", time.Since(started),
			"truncated", false,
		}
		startAttrs = append(startAttrs, identityAttrs(clientInfo)...)
		startAttrs = append(startAttrs,
			"reason", "start-failed",
			"error", err,
			"stdout", "",
			"stderr", "")
		log.Warn("tool call failed", startAttrs...)
		log.Log(ctx, levelTrace, "tool call failed", startAttrs...)
		return nil, err
	}

	waitErr := cmd.Wait()
	duration := time.Since(started)
	combinedOutput := combineToolOutput(stdout.Bytes(), stderr.Bytes())

	// The shared record fields (N2): the tool, the raw command line, the raw
	// request parameters (the client's exact JSON, not re-marshaled), the
	// exit code, how much output was captured, how long it took, and whether
	// the 1 MiB stream cap dropped bytes.
	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	attrs := []any{
		"tool", name,
		"path", scriptPath,
		"command", formatCommand(cmd.Args),
		"params", rawArgs,
		"exitCode", exitCode,
		"stdoutBytes", len(stdout.Bytes()),
		"stderrBytes", len(stderr.Bytes()),
		"duration", duration,
		"truncated", stdout.Truncated() || stderr.Truncated(),
	}
	// The calling client's identity (SDK req.ClientInfo()), when available:
	// omitted entirely for a nil/empty identity, bounded otherwise.
	attrs = append(attrs, identityAttrs(clientInfo)...)

	if waitErr != nil {
		// Classify the failure (N4): the deadline, a context cancellation,
		// or a non-zero exit.
		reason := "nonzero-exit"
		switch {
		case errors.Is(execCtx.Err(), context.DeadlineExceeded):
			reason = "timeout"
		case execCtx.Err() != nil:
			reason = "canceled"
		}
		// The raw output is built only here: a successful call never pays
		// for the up-to-2 MiB string copies, and at the default level the
		// TRACE record is disabled anyway.
		failAttrs := append(append(attrs, "reason", reason),
			"stdout", string(stdout.Bytes()),
			"stderr", string(stderr.Bytes()))
		if reason == "timeout" {
			failAttrs = append(failAttrs, "timeout", timeout)
		}
		log.Warn("tool call failed", failAttrs...)
		// The TRACE record is the WARN record plus the raw output — the same
		// complete attribute set, so the two never diverge. (No extra
		// construction cost: failAttrs is already built for the WARN.)
		log.Log(ctx, levelTrace, "tool call failed", failAttrs...)

		if reason == "timeout" {
			message := fmt.Sprintf("tool timed out after %s", timeout)
			if combinedOutput != "" {
				message += "\n" + combinedOutput
			}
			return textResult(message, true), nil
		}

		if combinedOutput == "" {
			combinedOutput = waitErr.Error()
		}

		return textResult(combinedOutput, true), nil
	}

	log.Debug("tool call completed", attrs...)
	// The TRACE record carries the full raw output (up to 2 MiB of string
	// copies); guard the construction so a success logged at the default
	// level does not pay for it — Logger.Log would drop the record as
	// disabled after evaluating its arguments.
	if log.Enabled(ctx, levelTrace) {
		log.Log(ctx, levelTrace, "tool call completed", append(append(attrs, "stdout", string(stdout.Bytes())), "stderr", string(stderr.Bytes()))...)
	}
	return textResult(combinedOutput, false), nil
}
