package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"reflect"
	"sort"
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

// executeTool runs the script at scriptPath as a subprocess in the specified
// working directory. It accepts pre-parsed arguments as a map, converts them to
// CLI flags, and binds the context to a timeout to prevent hanging tools.
// The output is captured, combined, and returned as an MCP CallToolResult.
func executeTool(ctx context.Context, scriptPath string, args map[string]any, timeout time.Duration, dir string) (*mcp.CallToolResult, error) {
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
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	waitErr := cmd.Wait()
	combinedOutput := combineToolOutput(stdout.Bytes(), stderr.Bytes())

	if waitErr != nil {
		if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
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

	return textResult(combinedOutput, false), nil
}
