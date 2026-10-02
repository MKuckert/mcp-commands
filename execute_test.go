package main

import (
	"bytes"
	"context"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestArgumentsToCLIArgsValidatesKeys(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		args    map[string]any
		wantErr bool
	}{
		{
			name:    "empty_key",
			args:    map[string]any{"": "value"},
			wantErr: true,
		},
		{
			name:    "digit_leading_key",
			args:    map[string]any{"1flag": "value"},
			wantErr: true,
		},
		{
			name:    "space_in_key",
			args:    map[string]any{"bad key": "value"},
			wantErr: true,
		},
		{
			name:    "equals_in_key",
			args:    map[string]any{"bad=key": "value"},
			wantErr: true,
		},
		{
			name:    "leading_dash_key",
			args:    map[string]any{"-flag": "value"},
			wantErr: true,
		},
		{
			name:    "valid_single_char_key",
			args:    map[string]any{"v": "value"},
			wantErr: false,
		},
		{
			name:    "valid_multi_char_key",
			args:    map[string]any{"my-flag": "value", "foo_bar": "value"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := argumentsToCLIArgs(tt.args)
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
		})
	}
}

func TestArgumentsToCLIArgsBooleanAndNilHandling(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		args     map[string]any
		expected []string
		desc     string
	}{
		{
			name:     "boolean_true_emits_flag_only",
			args:     map[string]any{"flag": true},
			expected: []string{"--flag"},
			desc:     "true value → --flag with no value argument",
		},
		{
			name:     "boolean_false_omits_flag",
			args:     map[string]any{"flag": false},
			expected: []string{},
			desc:     "false value → flag omitted entirely",
		},
		{
			name:     "nil_value_omits_flag",
			args:     map[string]any{"flag": nil},
			expected: []string{},
			desc:     "nil value → flag omitted entirely (previously emitted as --flag \"\")",
		},
		{
			name:     "string_true_treated_as_plain_string",
			args:     map[string]any{"flag": "true"},
			expected: []string{"--flag", "true"},
			desc:     "string \"true\" still treated as plain string argument",
		},
		{
			name:     "string_false_treated_as_plain_string",
			args:     map[string]any{"flag": "false"},
			expected: []string{"--flag", "false"},
			desc:     "string \"false\" still treated as plain string argument",
		},
		{
			name:     "mixed_true_false_nil_and_strings",
			args:     map[string]any{"bool_true": true, "bool_false": false, "nil_val": nil, "str_val": "string", "num_val": 42},
			expected: []string{"--bool_true", "--num_val", "42", "--str_val", "string"},
			desc:     "mixed types: true emitted, false/nil omitted, strings/numbers pass through",
		},
		{
			name:     "numeric_values_still_work",
			args:     map[string]any{"count": 42, "ratio": 3.14},
			expected: []string{"--count", "42", "--ratio", "3.14"},
			desc:     "numeric types unaffected",
		},
		{
			name:     "slice_values_still_work",
			args:     map[string]any{"items": []string{"a", "b", "c"}},
			expected: []string{"--items", "a", "--items", "b", "--items", "c"},
			desc:     "slice types unaffected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := argumentsToCLIArgs(tt.args)
			if err != nil {
				t.Fatalf("argumentsToCLIArgs failed: %v", err)
			}

			// Compare as sorted slices because the order may vary due to map iteration
			if len(got) != len(tt.expected) {
				t.Fatalf("expected %d args, got %d: %v (test: %s)", len(tt.expected), len(got), got, tt.desc)
			}

			if len(got) > 0 {
				sort.Strings(got)
				expected := tt.expected
				sort.Strings(expected)
				for i := range expected {
					if got[i] != expected[i] {
						t.Errorf("arg %d: expected %q, got %q (test: %s)", i, expected[i], got[i], tt.desc)
					}
				}
			}
		})
	}
}

func TestExecuteToolRejectsInvalidArgumentKeys(t *testing.T) {
	t.Parallel()
	scriptDir := t.TempDir()
	scriptPath := filepath.Join(scriptDir, "noop.sh")
	writeScript(t, scriptPath, "#!/bin/bash\necho noop\n")

	result, err := executeTool(context.Background(), scriptPath, map[string]any{"1flag": "value"}, 5*time.Second, scriptDir)
	if err != nil {
		t.Fatalf("executeTool returned unexpected error: %v", err)
	}
	if result == nil || !result.IsError {
		t.Fatalf("expected IsError result for invalid argument key, got %#v", result)
	}
	if got := result.Content[0].(*mcp.TextContent).Text; !strings.Contains(got, "invalid argument key") {
		t.Fatalf("expected descriptive invalid-key error, got %q", got)
	}
}

func TestExecuteToolWithWorkingDirectory(t *testing.T) {
	t.Parallel()
	// Create a temporary directory that will be the working directory
	workDir := t.TempDir()

	// Create a script that outputs its current working directory
	scriptDir := t.TempDir()
	scriptPath := filepath.Join(scriptDir, "pwd.sh")
	scriptContent := "#!/bin/bash\npwd"
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0o755); err != nil {
		t.Fatalf("failed to create script: %v", err)
	}

	// Execute the script with the working directory set
	ctx := context.Background()
	result, err := executeTool(ctx, scriptPath, map[string]any{}, 5*time.Second, workDir)
	if err != nil {
		t.Fatalf("executeTool failed: %v", err)
	}

	if result.IsError {
		t.Fatalf("executeTool returned an error: %s", result.Content[0])
	}

	// Extract the working directory from the script's output
	// The output is now wrapped in <stdout>...</stdout> tags
	output := strings.TrimSpace(result.Content[0].(*mcp.TextContent).Text)

	// Remove the tags and extract the actual output
	if strings.HasPrefix(output, "<stdout>") && strings.HasSuffix(output, "</stdout>") {
		output = output[len("<stdout>") : len(output)-len("</stdout>")]
	}
	outputPath := strings.TrimSpace(output)

	// Verify that the output matches the workDir we supplied
	if outputPath != workDir {
		t.Errorf("expected working directory %q, got %q", workDir, outputPath)
	}
}

func TestCombineToolOutput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		stdout         []byte
		stderr         []byte
		expectedOutput string
	}{
		{
			name:           "only_stdout",
			stdout:         []byte("hello world"),
			stderr:         nil,
			expectedOutput: "<stdout>\nhello world\n</stdout>",
		},
		{
			name:           "only_stderr",
			stdout:         nil,
			stderr:         []byte("error message"),
			expectedOutput: "<stderr>\nerror message\n</stderr>",
		},
		{
			name:           "both_stdout_and_stderr",
			stdout:         []byte("output"),
			stderr:         []byte("error"),
			expectedOutput: "<stdout>\noutput\n</stdout>\n<stderr>\nerror\n</stderr>",
		},
		{
			name:           "empty_both",
			stdout:         nil,
			stderr:         nil,
			expectedOutput: "",
		},
		{
			name:           "empty_stdout_with_stderr",
			stdout:         []byte(""),
			stderr:         []byte("error"),
			expectedOutput: "<stderr>\nerror\n</stderr>",
		},
		{
			name:           "stdout_with_trailing_newline",
			stdout:         []byte("hello\n"),
			stderr:         nil,
			expectedOutput: "<stdout>\nhello\n</stdout>",
		},
		{
			name:           "stdout_and_stderr_with_trailing_newlines",
			stdout:         []byte("output\n"),
			stderr:         []byte("error\n"),
			expectedOutput: "<stdout>\noutput\n</stdout>\n<stderr>\nerror\n</stderr>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := combineToolOutput(tt.stdout, tt.stderr)
			if got != tt.expectedOutput {
				t.Errorf("expected %q, got %q", tt.expectedOutput, got)
			}
		})
	}
}

func TestValidateRequiredParams(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		args      map[string]any
		params    []paramSpec
		wantErr   bool
		errSubstr string
	}{
		{
			name: "all_required_present",
			args: map[string]any{"path": "/tmp/file", "dpi": 150.0},
			params: []paramSpec{
				{Name: "path", Type: "string", Required: true},
				{Name: "dpi", Type: "number", Required: true},
			},
			wantErr: false,
		},
		{
			name: "one_missing_required",
			args: map[string]any{},
			params: []paramSpec{
				{Name: "path", Type: "string", Required: true},
			},
			wantErr:   true,
			errSubstr: "missing required parameter: path",
		},
		{
			name: "no_required_params",
			args: map[string]any{},
			params: []paramSpec{
				{Name: "verbose", Type: "boolean", Required: false},
			},
			wantErr: false,
		},
		{
			name:    "empty_params",
			args:    map[string]any{},
			params:  []paramSpec{},
			wantErr: false,
		},
		{
			name: "optional_absent_no_error",
			args: map[string]any{"path": "x"},
			params: []paramSpec{
				{Name: "path", Type: "string", Required: true},
				{Name: "format", Type: "string", Required: false},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRequiredParams(tt.args, tt.params)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errSubstr != "" && !strings.Contains(err.Error(), tt.errSubstr) {
					t.Fatalf("expected error containing %q, got %q", tt.errSubstr, err.Error())
				}
			} else {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
			}
		})
	}
}

func TestBoundedWriter(t *testing.T) {
	t.Parallel()
	w := newBoundedWriter(10)
	// 7 bytes fit, the next 8-byte write overflows the limit.
	n1, err := w.Write([]byte("0123456"))
	if n1 != 7 || err != nil {
		t.Fatalf("Write(7 bytes) = (%d, %v), want (7, nil)", n1, err)
	}
	n2, err := w.Write([]byte("89012345"))
	if n2 != 8 || err != nil {
		t.Fatalf("Write past limit = (%d, %v), want (8, nil): the subprocess must see a healthy pipe", n2, err)
	}
	if got := w.Bytes(); string(got) != "0123456890" {
		t.Fatalf("captured = %q, want exactly the first 10 bytes", got)
	}
	if !w.Truncated() {
		t.Fatal("Truncated() = false, want true after overflow")
	}
	// A write landing exactly on the limit drops nothing.
	w2 := newBoundedWriter(10)
	if n, err := w2.Write([]byte("0123456789")); n != 10 || err != nil || w2.Truncated() {
		t.Fatalf("exact-limit Write = (%d, %v), Truncated = %v, want (10, nil), false", n, err, w2.Truncated())
	}
	// Writes after the limit are consumed, not stored.
	if n, err := w.Write([]byte("garbage")); n != 7 || err != nil || len(w.Bytes()) != 10 {
		t.Fatalf("post-overflow Write = (%d, %v), len = %d, want (7, nil), 10", n, err, len(w.Bytes()))
	}
}

func TestCombineToolOutputTruncation(t *testing.T) {
	t.Parallel()
	// The 1 MiB branch of combineToolOutput was previously untested.
	suffixLen := len(fmt.Sprintf("\n[output truncated after %d bytes]", maxToolOutputBytes))

	t.Run("2MiB_stdout_is_capped_and_tagged", func(t *testing.T) {
		got := combineToolOutput(bytes.Repeat([]byte("a"), 2<<20), nil)
		if !strings.HasPrefix(got, "<stdout>\n") {
			t.Fatalf("lost the stdout tag: %q…", got[:20])
		}
		if !strings.HasSuffix(got, fmt.Sprintf("[output truncated after %d bytes]", maxToolOutputBytes)) {
			t.Fatalf("missing truncation suffix: …%q", got[len(got)-40:])
		}
		// The cut plus suffix stays at or just under the cap + suffix.
		if len(got) > maxToolOutputBytes+suffixLen {
			t.Fatalf("len = %d, want <= %d", len(got), maxToolOutputBytes+suffixLen)
		}
		if !utf8.ValidString(got) {
			t.Fatal("result is not valid UTF-8")
		}
	})

	t.Run("below_limit_is_untouched", func(t *testing.T) {
		got := combineToolOutput(bytes.Repeat([]byte("b"), 512), nil)
		if strings.Contains(got, "truncated") {
			t.Fatalf("no truncation expected under the limit: %q…", got[:40])
		}
		if len(got) != len("<stdout>\n")+512+1+len("</stdout>") {
			t.Fatalf("len = %d, want %d (512 payload + tags/newline)", len(got), len("<stdout>\n")+512+1+len("</stdout>"))
		}
	})

	t.Run("rune_splitting_cut_backs_off", func(t *testing.T) {
		// Combined = "<stdout>\n" + payload [+ "\n" if no trailing newline] +
		// "</stdout>". Craft the payload so the 1 MiB cut lands inside the first 3-byte rune (tag + pad ends 2 bytes before the cap)
		// then verify the back-off yields valid UTF-8.
		payload := make([]byte, 0, maxToolOutputBytes)
		payload = append(payload, bytes.Repeat([]byte("a"), maxToolOutputBytes-10)...)
		payload = append(payload, bytes.Repeat([]byte("€"), 8)...) // 24 bytes
		got := combineToolOutput(payload, nil)
		if !utf8.ValidString(got) {
			t.Fatalf("truncation split a rune; got invalid UTF-8 (len %d)", len(got))
		}
		if !strings.HasSuffix(got, "bytes]") {
			t.Fatalf("missing truncation suffix: …%q", got[len(got)-40:])
		}
	})
}

func TestExecuteToolHugeStdout(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "spew.sh")
	// 5 MiB of 'a' — well past the 1 MiB cap, cheap to generate.
	body := "#!/bin/sh\nhead -c 5242880 /dev/zero | tr '\\0' a\n"
	if err := os.WriteFile(scriptPath, []byte(body), 0o755); err != nil {
		t.Fatalf("failed to create script: %v", err)
	}

	result, err := executeTool(context.Background(), scriptPath, map[string]any{}, 30*time.Second, tmpDir)
	if err != nil {
		t.Fatalf("executeTool returned unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got IsError result")
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("unexpected content type %T", result.Content[0])
	}
	if len(text.Text) > maxToolOutputBytes+64 {
		t.Fatalf("result text is %d bytes, want <= ~%d", len(text.Text), maxToolOutputBytes)
	}
	if !strings.Contains(text.Text, "truncated after") {
		t.Fatalf("expected a truncation notice, got tail: …%q", text.Text[len(text.Text)-60:])
	}
	if !utf8.ValidString(text.Text) {
		t.Fatal("result text is not valid UTF-8")
	}
}
