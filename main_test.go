package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDiscoverTools(t *testing.T) {
	t.Run("discovers_executable_scripts", func(t *testing.T) {

		// Create a temporary directory with test scripts
		tmpDir := t.TempDir()

		// Create an executable script with description
		scriptPath := filepath.Join(tmpDir, "test_script.sh")
		scriptContent := `#!/bin/bash
# Description: This is a test script
echo "Hello"
`
		if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
			t.Fatalf("Failed to create test script: %v", err)
		}

		// Create a non-executable file
		nonExecPath := filepath.Join(tmpDir, "not_exec.sh")
		if err := os.WriteFile(nonExecPath, []byte("#!/bin/bash\necho hi"), 0644); err != nil {
			t.Fatalf("Failed to create non-executable file: %v", err)
		}

		// Create a subdirectory (should be skipped)
		subDir := filepath.Join(tmpDir, "subdir")
		if err := os.Mkdir(subDir, 0755); err != nil {
			t.Fatalf("Failed to create subdirectory: %v", err)
		}

		tools, err := discoverTools(tmpDir)
		if err != nil {
			t.Fatalf("discoverTools failed: %v", err)
		}

		if len(tools) != 1 {
			t.Errorf("Expected 1 tool, got %d", len(tools))
		}

		if len(tools) > 0 {
			tool := tools[0]
			if tool.Name != "test_script" {
				t.Errorf("Expected tool name 'test_script', got '%s'", tool.Name)
			}
			if tool.Description != "This is a test script" {
				t.Errorf("Expected description 'This is a test script', got '%s'", tool.Description)
			}
		}
	})

	t.Run("handles_missing_description", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create an executable script without description
		scriptPath := filepath.Join(tmpDir, "no_desc.sh")
		scriptContent := `#!/bin/bash
echo "Hello"
`
		if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
			t.Fatalf("Failed to create test script: %v", err)
		}

		tools, err := discoverTools(tmpDir)
		if err != nil {
			t.Fatalf("discoverTools failed: %v", err)
		}

		if len(tools) != 1 {
			t.Errorf("Expected 1 tool, got %d", len(tools))
		}

		if len(tools) > 0 {
			tool := tools[0]
			if tool.Description != "" {
				t.Errorf("Expected empty description, got '%s'", tool.Description)
			}
		}
	})

	t.Run("handles_cpp_style_description", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create an executable C++ style script
		scriptPath := filepath.Join(tmpDir, "cpp_script")
		scriptContent := `#!/bin/bash
// Description: C++ style description
echo "Hello"
`
		if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
			t.Fatalf("Failed to create test script: %v", err)
		}

		tools, err := discoverTools(tmpDir)
		if err != nil {
			t.Fatalf("discoverTools failed: %v", err)
		}

		if len(tools) > 0 {
			tool := tools[0]
			if tool.Description != "C++ style description" {
				t.Errorf("Expected 'C++ style description', got '%s'", tool.Description)
			}
		}
	})

	t.Run("skips_non_executable_files", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create a non-executable file
		nonExecPath := filepath.Join(tmpDir, "script.sh")
		if err := os.WriteFile(nonExecPath, []byte("#!/bin/bash\necho hi"), 0644); err != nil {
			t.Fatalf("Failed to create non-executable file: %v", err)
		}

		tools, err := discoverTools(tmpDir)
		if err != nil {
			t.Fatalf("discoverTools failed: %v", err)
		}

		if len(tools) != 0 {
			t.Errorf("Expected 0 tools, got %d", len(tools))
		}
	})

	t.Run("skips_directories", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create a directory
		subDir := filepath.Join(tmpDir, "subdir")
		if err := os.Mkdir(subDir, 0755); err != nil {
			t.Fatalf("Failed to create subdirectory: %v", err)
		}

		tools, err := discoverTools(tmpDir)
		if err != nil {
			t.Fatalf("discoverTools failed: %v", err)
		}

		if len(tools) != 0 {
			t.Errorf("Expected 0 tools, got %d", len(tools))
		}
	})

	t.Run("handles_nonexistent_directory", func(t *testing.T) {
		nonExistent := "/tmp/nonexistent_dir_12345"
		_, err := discoverTools(nonExistent)
		if err == nil {
			t.Error("Expected error for nonexistent directory, got nil")
		}
	})

	t.Run("removes_extension_from_tool_name", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create various scripts with different extensions
		scripts := []string{"script.sh", "tool.py", "command.rb", "noext"}
		for _, script := range scripts {
			scriptPath := filepath.Join(tmpDir, script)
			if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\necho hi"), 0755); err != nil {
				t.Fatalf("Failed to create test script: %v", err)
			}
		}

		tools, err := discoverTools(tmpDir)
		if err != nil {
			t.Fatalf("discoverTools failed: %v", err)
		}

		if len(tools) != 4 {
			t.Errorf("Expected 4 tools, got %d", len(tools))
		}

		expectedNames := map[string]bool{
			"script":  true,
			"tool":    true,
			"command": true,
			"noext":   true,
		}

		for _, tool := range tools {
			if !expectedNames[tool.Name] {
				t.Errorf("Unexpected tool name: %s", tool.Name)
			}
		}
	})

	t.Run("populates_params_from_annotations", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create an executable script with Param annotations
		scriptPath := filepath.Join(tmpDir, "with_params.sh")
		scriptContent := `#!/bin/bash
# Description: Script with parameters
# Param: input string required "Input file path"
# Param: format string optional "Output format"
echo "Processing"
`
		if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
			t.Fatalf("Failed to create test script: %v", err)
		}

		tools, err := discoverTools(tmpDir)
		if err != nil {
			t.Fatalf("discoverTools failed: %v", err)
		}

		if len(tools) != 1 {
			t.Errorf("Expected 1 tool, got %d", len(tools))
		}

		if len(tools) > 0 {
			tool := tools[0]
			if len(tool.Params) != 2 {
				t.Errorf("Expected 2 params, got %d: %#v", len(tool.Params), tool.Params)
			}
			if len(tool.Params) > 0 && tool.Params[0].Name != "input" {
				t.Errorf("Expected first param name 'input', got '%s'", tool.Params[0].Name)
			}
			if len(tool.Params) > 1 && tool.Params[1].Name != "format" {
				t.Errorf("Expected second param name 'format', got '%s'", tool.Params[1].Name)
			}
		}
	})
}

func TestWatchTools(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "alpha.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\necho alpha\n"), 0o755); err != nil {
		t.Fatalf("failed to create script: %v", err)
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
	registry := newToolRegistry(server, "", defaultToolTimeout, 16)
	registry.replace([]discoveredTool{{Name: "alpha", Path: scriptPath, Description: "alpha", Params: []paramSpec{}}})

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx := context.Background()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect failed: %v", err)
	}
	defer serverSession.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect failed: %v", err)
	}
	defer clientSession.Close()

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- watchTools(watchCtx, tmpDir, registry, 20*time.Millisecond)
	}()

	addedScriptPath := filepath.Join(tmpDir, "beta.sh")

	// Bounded write retries (F-22): fsnotify events can be lost under load
	// (inotify queue overflow), so re-write and re-poll until the reload
	// lands; the same content still fires a fresh event.
	var lastCount int
	updated := false
	for i := 0; i < 10 && !updated; i++ {
		if err := os.WriteFile(addedScriptPath, []byte("#!/bin/bash\necho beta\n"), 0o755); err != nil {
			t.Fatalf("failed to create second script: %v", err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			res, err := clientSession.ListTools(ctx, nil)
			if err != nil {
				t.Fatalf("ListTools failed: %v", err)
			}
			lastCount = len(res.Tools)
			if lastCount == 2 {
				updated = true
				break
			}
			if time.Now().After(deadline) {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !updated {
		cancel()
		t.Fatalf("watchTools did not update after 10 write attempts: got %d tools", lastCount)
	}
	cancel()

	select {
	case err := <-done:
		if err != nil && err != context.Canceled {
			t.Fatalf("watchTools returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("watchTools did not stop after cancel")
	}
}

func TestWatchToolsDetectsContentChanges(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "alpha.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\n# Description: alpha\necho alpha\n"), 0o755); err != nil {
		t.Fatalf("failed to create script: %v", err)
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
	registry := newToolRegistry(server, "", defaultToolTimeout, 16)
	registry.replace([]discoveredTool{{Name: "alpha", Path: scriptPath, Description: "alpha", Params: []paramSpec{}}})

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx := context.Background()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect failed: %v", err)
	}
	defer serverSession.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect failed: %v", err)
	}
	defer clientSession.Close()

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- watchTools(watchCtx, tmpDir, registry, 20*time.Millisecond)
	}()

	// Bounded write retries: a lost fsnotify event is recovered by the next
	// write (see TestWatchTools). (F-22)
	var lastTools []*mcp.Tool
	refreshed := false
	for i := 0; i < 10 && !refreshed; i++ {
		if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\n# Description: beta updated\necho beta now\n"), 0o755); err != nil {
			t.Fatalf("failed to update script: %v", err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			res, err := clientSession.ListTools(ctx, nil)
			if err != nil {
				t.Fatalf("ListTools failed: %v", err)
			}
			lastTools = res.Tools
			if len(res.Tools) == 1 && res.Tools[0].Description == "beta updated (timeout: 5m0s)" {
				refreshed = true
				break
			}
			if time.Now().After(deadline) {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !refreshed {
		cancel()
		t.Fatalf("watchTools did not refresh updated tool description after 10 write attempts: %+v", lastTools)
	}
	cancel()

	select {
	case err := <-done:
		if err != nil && err != context.Canceled {
			t.Fatalf("watchTools returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("watchTools did not stop after cancel")
	}
}

func TestParseTimeoutDuration(t *testing.T) {
	tests := []struct {
		name         string
		raw          string
		want         time.Duration
		wantErr      bool
		wantOverflow bool
	}{
		{name: "minutes", raw: "5m", want: 5 * time.Minute},
		{name: "seconds", raw: "60s", want: 60 * time.Second},
		{name: "compound", raw: "1h 30m 5s", want: time.Hour + 30*time.Minute + 5*time.Second},
		{name: "compound_no_spaces", raw: "1h30m5s", want: time.Hour + 30*time.Minute + 5*time.Second},
		{name: "duplicates_sum", raw: "5m 5m", want: 10 * time.Minute},
		{name: "subsecond_ms_rejected", raw: "250ms", wantErr: true},
		{name: "subsecond_us_rejected", raw: "250us", wantErr: true},
		{name: "subsecond_µs_rejected", raw: "250µs", wantErr: true},
		{name: "subsecond_ns_rejected", raw: "1000ns", wantErr: true},
		{name: "compound_subsecond_rejected", raw: "5m 250ms", wantErr: true},
		{name: "none_upper", raw: "NONE", want: 0},
		{name: "none_lower", raw: "none", want: 0},
		{name: "none_padded", raw: "  NONE ", want: 0},
		{name: "zero_seconds", raw: "0s", want: 0},
		{name: "zero_compound", raw: "0m 0s", want: 0},
		{name: "negative_rejected", raw: "-5m", wantErr: true},
		{name: "plus_rejected", raw: "+5m", wantErr: true},
		{name: "decimal_rejected", raw: "1.5h", wantErr: true},
		{name: "bare_unit_rejected", raw: "m5", wantErr: true},
		{name: "digits_only_rejected", raw: "5", wantErr: true},
		{name: "bare_unit_alone_rejected", raw: "h", wantErr: true},
		{name: "empty_rejected", raw: "", wantErr: true},
		{name: "whitespace_only_rejected", raw: "   ", wantErr: true},
		{name: "unknown_unit_rejected", raw: "5x", wantErr: true},
		{name: "compound_with_bad_token_rejected", raw: "5m bogus", wantErr: true},
		// ~300 years total: each 876000h (100y) term fits, the sum does not.
		{name: "sum_overflow_rejected", raw: "876000h 876000h 876000h", wantErr: true, wantOverflow: true},
		// Fits in 63 bits, but seconds→nanoseconds multiplication overflows.
		{name: "single_term_overflow_rejected", raw: "9223372037s", wantErr: true, wantOverflow: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseTimeoutDuration(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseTimeoutDuration(%q) = %v, want error", tt.raw, got)
				}
				if tt.wantOverflow && !strings.Contains(err.Error(), "overflows") {
					t.Errorf("parseTimeoutDuration(%q) error = %v, want \"overflows\"", tt.raw, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseTimeoutDuration(%q) unexpected error: %v", tt.raw, err)
			}
			if got != tt.want {
				t.Errorf("parseTimeoutDuration(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestResolveTimeout(t *testing.T) {
	tests := []struct {
		name        string
		timeoutFlag string
		timeoutSet  bool
		noTimeout   bool
		want        time.Duration
		wantErr     bool
	}{
		{name: "default_when_unset", timeoutFlag: "", want: defaultToolTimeout},
		{name: "flag_parsed", timeoutFlag: "5s", timeoutSet: true, want: 5 * time.Second},
		{name: "flag_none", timeoutFlag: "NONE", timeoutSet: true, want: 0},
		{name: "flag_zero", timeoutFlag: "0s", timeoutSet: true, want: 0},
		{name: "no_timeout_alone", noTimeout: true, want: 0},
		{name: "no_timeout_and_flag_exclusive", timeoutFlag: "5s", timeoutSet: true, noTimeout: true, wantErr: true},
		{name: "no_timeout_and_invalid_flag_exclusive", timeoutFlag: "bogus", timeoutSet: true, noTimeout: true, wantErr: true},
		{name: "invalid_flag_errors", timeoutFlag: "bogus", timeoutSet: true, wantErr: true},
		{name: "explicit_empty_flag_errors", timeoutFlag: "", timeoutSet: true, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveTimeout(tt.timeoutFlag, tt.timeoutSet, tt.noTimeout)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolveTimeout(%q, %v) = %v, want error", tt.timeoutFlag, tt.noTimeout, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveTimeout(%q, %v) unexpected error: %v", tt.timeoutFlag, tt.noTimeout, err)
			}
			if got != tt.want {
				t.Errorf("resolveTimeout(%q, %v) = %v, want %v", tt.timeoutFlag, tt.noTimeout, got, tt.want)
			}
		})
	}
}

func TestArgumentsToCLIArgsValidatesKeys(t *testing.T) {
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
	scriptDir := t.TempDir()
	scriptPath := filepath.Join(scriptDir, "noop.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\necho noop\n"), 0o755); err != nil {
		t.Fatalf("failed to create script: %v", err)
	}

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

func TestParseToolArgumentsRejectsDoubleEncodedJSON(t *testing.T) {
	// This test explicitly documents that the fallback double-decode has been removed.
	// parseToolArguments must reject double-encoded JSON strings and only accept
	// proper JSON objects (or empty/null).

	// Create a valid JSON object that will be JSON-encoded as a string
	validObject := `{"key": "value"}`
	doubleEncodedJSON := []byte(`"` + strings.ReplaceAll(validObject, `"`, `\"`) + `"`)

	_, err := parseToolArguments(doubleEncodedJSON)
	if err == nil {
		t.Fatal("expected parseToolArguments to reject double-encoded JSON, but it succeeded")
	}
	if !strings.Contains(err.Error(), "arguments must be a JSON object") {
		t.Fatalf("expected error matching 'arguments must be a JSON object', got %q", err.Error())
	}

	// Verify that a proper JSON object still works
	_, err = parseToolArguments([]byte(`{"key": "value"}`))
	if err != nil {
		t.Fatalf("expected parseToolArguments to accept proper JSON object, got error: %v", err)
	}

	// Verify that empty input still works
	_, err = parseToolArguments([]byte(`{}`))
	if err != nil {
		t.Fatalf("expected parseToolArguments to accept empty object, got error: %v", err)
	}

	// Verify that null still works
	_, err = parseToolArguments([]byte(`null`))
	if err != nil {
		t.Fatalf("expected parseToolArguments to accept null, got error: %v", err)
	}
}

func TestCombineToolOutput(t *testing.T) {
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

func writeTimeoutScript(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "script.sh")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("failed to create script: %v", err)
	}
	return path
}

func TestExtractFrontmatterTimeout(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		tmpDir := t.TempDir()
		path := writeTimeoutScript(t, tmpDir, "#!/bin/bash\n# Description: no timeout\necho hi\n")

		_, _, timeout := extractFrontmatter(path)
		if timeout != nil {
			t.Errorf("expected nil timeout for absent Timeout:, got %v", *timeout)
		}
	})

	t.Run("valid_30s", func(t *testing.T) {
		tmpDir := t.TempDir()
		path := writeTimeoutScript(t, tmpDir, "#!/bin/bash\n# Timeout: 30s\necho hi\n")

		_, _, timeout := extractFrontmatter(path)
		if timeout == nil || *timeout != 30*time.Second {
			t.Errorf("expected pointer to 30s, got %#v", timeout)
		}
	})

	t.Run("none", func(t *testing.T) {
		tmpDir := t.TempDir()
		path := writeTimeoutScript(t, tmpDir, "#!/bin/bash\n# Timeout: NONE\necho hi\n")

		_, _, timeout := extractFrontmatter(path)
		if timeout == nil || *timeout != 0 {
			t.Errorf("expected pointer to 0 for NONE, got %#v", timeout)
		}
	})

	t.Run("zero_seconds", func(t *testing.T) {
		tmpDir := t.TempDir()
		path := writeTimeoutScript(t, tmpDir, "#!/bin/bash\n# Timeout: 0s\necho hi\n")

		_, _, timeout := extractFrontmatter(path)
		if timeout == nil || *timeout != 0 {
			t.Errorf("expected pointer to 0 for 0s, got %#v", timeout)
		}
	})

	t.Run("invalid_falls_back", func(t *testing.T) {
		tmpDir := t.TempDir()
		path := writeTimeoutScript(t, tmpDir, "#!/bin/bash\n# Timeout: bogus\necho hi\n")

		// Capture the stderr warning emitted for the invalid value.
		oldStderr := os.Stderr
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("failed to create pipe: %v", err)
		}
		os.Stderr = w
		_, _, timeout := extractFrontmatter(path)
		os.Stderr = oldStderr
		_ = w.Close()
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		_ = r.Close()

		if timeout != nil {
			t.Errorf("expected nil timeout for invalid value, got %v", *timeout)
		}
		if !strings.Contains(buf.String(), filepath.Base(path)) {
			t.Errorf("warning %q does not name the script file %q", buf.String(), filepath.Base(path))
		}
		if !strings.Contains(buf.String(), "bogus") {
			t.Errorf("warning %q does not state the invalid value's reason", buf.String())
		}
	})

	t.Run("invalid_does_not_drop_later_params", func(t *testing.T) {
		tmpDir := t.TempDir()
		path := writeTimeoutScript(t, tmpDir, "#!/bin/bash\n# Timeout: bogus\n# Param: name string required \"the name\"\necho hi\n")

		// Suppress the warning; assert scan continuity, not the warning here.
		discard, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if err != nil {
			t.Fatalf("failed to open %s: %v", os.DevNull, err)
		}
		oldStderr := os.Stderr
		os.Stderr = discard
		desc, params, timeout := extractFrontmatter(path)
		os.Stderr = oldStderr
		_ = discard.Close()

		if timeout != nil {
			t.Errorf("expected nil timeout for invalid value, got %v", *timeout)
		}
		if desc != "" || len(params) != 1 || params[0].Name != "name" {
			t.Errorf("scan stopped after invalid Timeout: desc=%q params=%#v", desc, params)
		}
	})

	t.Run("first_occurrence_wins", func(t *testing.T) {
		tmpDir := t.TempDir()
		path := writeTimeoutScript(t, tmpDir, "#!/bin/bash\n# Timeout: 30s\n# Timeout: 5m\necho hi\n")

		_, _, timeout := extractFrontmatter(path)
		if timeout == nil || *timeout != 30*time.Second {
			t.Errorf("expected first occurrence (30s), got %#v", timeout)
		}
	})

	t.Run("beyond_scan_window_ignored", func(t *testing.T) {
		tmpDir := t.TempDir()
		lines := []string{"#!/bin/bash"}
		for i := 0; i < scanHeaderLines; i++ {
			lines = append(lines, fmt.Sprintf("# Line %d", i))
		}
		lines = append(lines, "# Timeout: 30s", "echo done")
		path := writeTimeoutScript(t, tmpDir, strings.Join(lines, "\n"))

		_, _, timeout := extractFrontmatter(path)
		if timeout != nil {
			t.Errorf("expected nil timeout for line beyond scan window, got %v", *timeout)
		}
	})
}

func TestDiscoverToolsExtractsTimeout(t *testing.T) {
	tmpDir := t.TempDir()
	writeTimeoutScript(t, tmpDir, "#!/bin/bash\n# Description: sleeper\n# Timeout: 1m\necho hi\n")

	tools, err := discoverTools(tmpDir)
	if err != nil {
		t.Fatalf("discoverTools failed: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Timeout == nil || *tools[0].Timeout != time.Minute {
		t.Errorf("expected pointer to 1m, got %#v", tools[0].Timeout)
	}
}

func TestExtractParams(t *testing.T) {
	t.Run("happy_path_all_types", func(t *testing.T) {
		tmpDir := t.TempDir()
		scriptPath := filepath.Join(tmpDir, "test.sh")
		scriptContent := `#!/bin/bash
# Description: Test script
# Param: path string required "Path to the input file"
# Param: dpi number optional "DPI for rasterisation (default 150)"
# Param: verbose boolean optional "Print progress"
echo "Hello"
`
		if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
			t.Fatalf("Failed to create test script: %v", err)
		}

		_, params, _ := extractFrontmatter(scriptPath)

		if len(params) != 3 {
			t.Errorf("Expected 3 params, got %d", len(params))
		}

		if len(params) > 0 {
			if params[0].Name != "path" || params[0].Type != "string" || !params[0].Required || params[0].Description != "Path to the input file" {
				t.Errorf("First param mismatch: %#v", params[0])
			}
		}

		if len(params) > 1 {
			if params[1].Name != "dpi" || params[1].Type != "number" || params[1].Required || params[1].Description != "DPI for rasterisation (default 150)" {
				t.Errorf("Second param mismatch: %#v", params[1])
			}
		}

		if len(params) > 2 {
			if params[2].Name != "verbose" || params[2].Type != "boolean" || params[2].Required || params[2].Description != "Print progress" {
				t.Errorf("Third param mismatch: %#v", params[2])
			}
		}
	})

	t.Run("no_params", func(t *testing.T) {
		tmpDir := t.TempDir()
		scriptPath := filepath.Join(tmpDir, "test.sh")
		scriptContent := `#!/bin/bash
# Description: Test script with no params
echo "Hello"
`
		if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
			t.Fatalf("Failed to create test script: %v", err)
		}

		_, params, _ := extractFrontmatter(scriptPath)

		if len(params) != 0 {
			t.Errorf("Expected 0 params, got %d", len(params))
		}
	})

	t.Run("malformed_line_wrong_field_count", func(t *testing.T) {
		tmpDir := t.TempDir()
		scriptPath := filepath.Join(tmpDir, "test.sh")
		scriptContent := `#!/bin/bash
# Param: path string "missing required/optional"
# Param: valid string required "Description"
echo "Hello"
`
		if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
			t.Fatalf("Failed to create test script: %v", err)
		}

		_, params, _ := extractFrontmatter(scriptPath)

		// Only the valid param should be extracted
		if len(params) != 1 {
			t.Errorf("Expected 1 valid param, got %d", len(params))
		}
		if len(params) > 0 && params[0].Name != "valid" {
			t.Errorf("Expected param name 'valid', got %q", params[0].Name)
		}
	})

	t.Run("unknown_type", func(t *testing.T) {
		tmpDir := t.TempDir()
		scriptPath := filepath.Join(tmpDir, "test.sh")
		scriptContent := `#!/bin/bash
# Param: invalid unknowntype required "Bad type"
# Param: valid string required "Good type"
echo "Hello"
`
		if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
			t.Fatalf("Failed to create test script: %v", err)
		}

		_, params, _ := extractFrontmatter(scriptPath)

		if len(params) != 1 {
			t.Errorf("Expected 1 valid param, got %d", len(params))
		}
		if len(params) > 0 && params[0].Name != "valid" {
			t.Errorf("Expected param name 'valid', got %q", params[0].Name)
		}
	})

	t.Run("bad_param_name_digit_leading", func(t *testing.T) {
		tmpDir := t.TempDir()
		scriptPath := filepath.Join(tmpDir, "test.sh")
		scriptContent := `#!/bin/bash
# Param: 1invalid string required "Digit leading name"
# Param: valid string required "Good name"
echo "Hello"
`
		if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
			t.Fatalf("Failed to create test script: %v", err)
		}

		_, params, _ := extractFrontmatter(scriptPath)

		if len(params) != 1 {
			t.Errorf("Expected 1 valid param, got %d", len(params))
		}
		if len(params) > 0 && params[0].Name != "valid" {
			t.Errorf("Expected param name 'valid', got %q", params[0].Name)
		}
	})

	t.Run("description_with_internal_spaces", func(t *testing.T) {
		tmpDir := t.TempDir()
		scriptPath := filepath.Join(tmpDir, "test.sh")
		scriptContent := `#!/bin/bash
# Param: path string required "This is a long description with many spaces"
echo "Hello"
`
		if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
			t.Fatalf("Failed to create test script: %v", err)
		}

		_, params, _ := extractFrontmatter(scriptPath)

		if len(params) != 1 {
			t.Errorf("Expected 1 param, got %d", len(params))
		}
		if len(params) > 0 && params[0].Description != "This is a long description with many spaces" {
			t.Errorf("Expected description preserved, got %q", params[0].Description)
		}
	})

	t.Run("unquoted_description_is_malformed", func(t *testing.T) {
		tmpDir := t.TempDir()
		scriptPath := filepath.Join(tmpDir, "test.sh")
		scriptContent := `#!/bin/bash
# Param: invalid string required No quotes here
# Param: valid string required "Quoted description"
echo "Hello"
`
		if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
			t.Fatalf("Failed to create test script: %v", err)
		}

		_, params, _ := extractFrontmatter(scriptPath)

		if len(params) != 1 {
			t.Errorf("Expected 1 valid param, got %d", len(params))
		}
		if len(params) > 0 && params[0].Name != "valid" {
			t.Errorf("Expected param name 'valid', got %q", params[0].Name)
		}
	})

	t.Run("ignores_param_lines_beyond_scan_window", func(t *testing.T) {
		tmpDir := t.TempDir()
		scriptPath := filepath.Join(tmpDir, "test.sh")
		// Create a script with 35 lines (scanHeaderLines is 30)
		lines := []string{"#!/bin/bash"}
		lines = append(lines, "# Param: valid string required \"Within window\"")
		// Add 32 more lines (total 34, so line 35 is outside)
		for i := 0; i < 32; i++ {
			lines = append(lines, fmt.Sprintf("# Line %d", i))
		}
		lines = append(lines, "# Param: ignored string required \"Beyond window\"")
		lines = append(lines, "echo done")

		scriptContent := strings.Join(lines, "\n")
		if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
			t.Fatalf("Failed to create test script: %v", err)
		}

		_, params, _ := extractFrontmatter(scriptPath)

		if len(params) != 1 {
			t.Errorf("Expected 1 param (beyond window ignored), got %d", len(params))
		}
		if len(params) > 0 && params[0].Name != "valid" {
			t.Errorf("Expected param name 'valid', got %q", params[0].Name)
		}
	})
}

func TestBuildInputSchema(t *testing.T) {
	t.Run("zero_params", func(t *testing.T) {
		schema := buildInputSchema([]paramSpec{})

		var decoded map[string]any
		if err := json.Unmarshal(schema, &decoded); err != nil {
			t.Fatalf("failed to unmarshal schema: %v", err)
		}

		if decoded["type"] != "object" {
			t.Errorf("expected type 'object', got %q", decoded["type"])
		}

		props := decoded["properties"].(map[string]any)
		if len(props) != 0 {
			t.Errorf("expected empty properties, got %d", len(props))
		}

		if _, hasRequired := decoded["required"]; hasRequired {
			t.Error("expected 'required' key to be absent, but it was present")
		}
	})

	t.Run("one_required_string_param", func(t *testing.T) {
		params := []paramSpec{
			{
				Name:        "path",
				Type:        "string",
				Required:    true,
				Description: "Path to the input file",
			},
		}
		schema := buildInputSchema(params)

		var decoded map[string]any
		if err := json.Unmarshal(schema, &decoded); err != nil {
			t.Fatalf("failed to unmarshal schema: %v", err)
		}

		if decoded["type"] != "object" {
			t.Errorf("expected type 'object', got %q", decoded["type"])
		}

		props := decoded["properties"].(map[string]any)
		if len(props) != 1 {
			t.Errorf("expected 1 property, got %d", len(props))
		}

		pathProp := props["path"].(map[string]any)
		if pathProp["type"] != "string" {
			t.Errorf("expected type 'string', got %q", pathProp["type"])
		}
		if pathProp["description"] != "Path to the input file" {
			t.Errorf("expected description 'Path to the input file', got %q", pathProp["description"])
		}

		required := decoded["required"].([]any)
		if len(required) != 1 || required[0] != "path" {
			t.Errorf("expected required=['path'], got %v", required)
		}
	})

	t.Run("mix_required_and_optional", func(t *testing.T) {
		params := []paramSpec{
			{
				Name:        "path",
				Type:        "string",
				Required:    true,
				Description: "Path to the input file",
			},
			{
				Name:        "verbose",
				Type:        "boolean",
				Required:    false,
				Description: "Print progress",
			},
		}
		schema := buildInputSchema(params)

		var decoded map[string]any
		if err := json.Unmarshal(schema, &decoded); err != nil {
			t.Fatalf("failed to unmarshal schema: %v", err)
		}

		props := decoded["properties"].(map[string]any)
		if len(props) != 2 {
			t.Errorf("expected 2 properties, got %d", len(props))
		}

		required := decoded["required"].([]any)
		if len(required) != 1 || required[0] != "path" {
			t.Errorf("expected required=['path'], got %v", required)
		}
	})

	t.Run("duplicate_param_names_last_wins", func(t *testing.T) {
		params := []paramSpec{
			{
				Name:        "flag",
				Type:        "string",
				Required:    true,
				Description: "First declaration",
			},
			{
				Name:        "flag",
				Type:        "boolean",
				Required:    false,
				Description: "Last declaration",
			},
		}
		schema := buildInputSchema(params)

		var decoded map[string]any
		if err := json.Unmarshal(schema, &decoded); err != nil {
			t.Fatalf("failed to unmarshal schema: %v", err)
		}

		props := decoded["properties"].(map[string]any)
		if len(props) != 1 {
			t.Errorf("expected 1 property (last wins), got %d", len(props))
		}

		flagProp := props["flag"].(map[string]any)
		if flagProp["type"] != "boolean" {
			t.Errorf("expected type 'boolean' (last declaration), got %q", flagProp["type"])
		}
		if flagProp["description"] != "Last declaration" {
			t.Errorf("expected description 'Last declaration', got %q", flagProp["description"])
		}

		// Required should reflect the last declaration (false)
		if _, hasRequired := decoded["required"]; hasRequired {
			t.Error("expected 'required' key to be absent (last declaration is optional)")
		}
	})

	t.Run("all_three_types", func(t *testing.T) {
		params := []paramSpec{
			{
				Name:        "path",
				Type:        "string",
				Required:    true,
				Description: "Input path",
			},
			{
				Name:        "dpi",
				Type:        "number",
				Required:    false,
				Description: "DPI value",
			},
			{
				Name:        "verbose",
				Type:        "boolean",
				Required:    true,
				Description: "Verbose mode",
			},
		}
		schema := buildInputSchema(params)

		var decoded map[string]any
		if err := json.Unmarshal(schema, &decoded); err != nil {
			t.Fatalf("failed to unmarshal schema: %v", err)
		}

		props := decoded["properties"].(map[string]any)
		if len(props) != 3 {
			t.Errorf("expected 3 properties, got %d", len(props))
		}

		// Check types
		if props["path"].(map[string]any)["type"] != "string" {
			t.Error("path type mismatch")
		}
		if props["dpi"].(map[string]any)["type"] != "number" {
			t.Error("dpi type mismatch")
		}
		if props["verbose"].(map[string]any)["type"] != "boolean" {
			t.Error("verbose type mismatch")
		}

		// Check required
		required := decoded["required"].([]any)
		requiredSet := make(map[string]bool)
		for _, r := range required {
			requiredSet[r.(string)] = true
		}
		if !requiredSet["path"] {
			t.Error("path should be required")
		}
		if requiredSet["dpi"] {
			t.Error("dpi should not be required")
		}
		if !requiredSet["verbose"] {
			t.Error("verbose should be required")
		}
	})
}

func TestValidateRequiredParams(t *testing.T) {
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

func TestResolveAPIKey(t *testing.T) {
	tests := []struct {
		name     string
		flag     string
		file     string // content written to a temp file when fileSet
		fileSet  bool
		fileMiss bool // path set but file missing → error
		env      string
		want     string
		wantFlag bool // token must be flagged as coming from --api-key
	}{
		{name: "flag_only", flag: "from-flag", want: "from-flag", wantFlag: true},
		{name: "env_only", env: "from-env", want: "from-env"},
		{name: "file_only", file: "from-file\n", fileSet: true, want: "from-file"},
		{name: "flag_beats_file_and_env", flag: "from-flag", file: "from-file", fileSet: true, env: "from-env", want: "from-flag", wantFlag: true},
		{name: "file_beats_env", file: "from-file", fileSet: true, env: "from-env", want: "from-file"},
		{name: "empty_file_falls_through_to_env", file: "\n  \n", fileSet: true, env: "from-env", want: "from-env"},
		{name: "missing_file_errors", fileMiss: true},
		{name: "neither_set", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// t.Setenv to "" counts as empty for resolveAPIKey.
			t.Setenv(apiKeyEnvVar, tt.env)

			var filePath string
			if tt.fileSet || tt.fileMiss {
				if tt.fileMiss {
					filePath = filepath.Join(t.TempDir(), "missing.txt")
				} else {
					filePath = filepath.Join(t.TempDir(), "key.txt")
					if err := os.WriteFile(filePath, []byte(tt.file), 0o600); err != nil {
						t.Fatalf("failed to write key file: %v", err)
					}
				}
			}

			got, gotFlag, err := resolveAPIKey(tt.flag, filePath)
			if tt.fileMiss {
				if err == nil {
					t.Fatalf("expected an error for the missing key file, got token %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveAPIKey returned unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("resolveAPIKey = %q, want %q", got, tt.want)
			}
			if gotFlag != tt.wantFlag {
				t.Errorf("fromFlag = %v, want %v", gotFlag, tt.wantFlag)
			}
		})
	}
}

// TestAPIKeyFlagWarning pins the F-8 deprecation: a token on the command line
// is world-readable via /proc/<pid>/cmdline, so the warning must name the
// exposure and point at the safe alternatives.
func TestAPIKeyFlagWarning(t *testing.T) {
	if !strings.Contains(apiKeyFlagWarning, "--api-key-file") || !strings.Contains(apiKeyFlagWarning, apiKeyEnvVar) {
		t.Fatalf("warning must point users at the safe alternatives: %q", apiKeyFlagWarning)
	}
	if !strings.Contains(apiKeyFlagWarning, "/proc") {
		t.Fatalf("warning must name the exposure (cmdline): %q", apiKeyFlagWarning)
	}
}

// TestParseCLI pins F-10: every fail-fast branch of the CLI front end.
// These are the branches that were untestable while the logic lived in main()
// (which calls os.Exit); the extraction into parseCLI makes each one assertable.
func TestParseCLI(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "key.txt")
	if err := os.WriteFile(keyFile, []byte("filetoken\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		args     []string
		wantErr  string // substring; empty = success
		wantMode cliMode
		check    func(t *testing.T, c cliConfig)
	}{
		{
			name:    "version_short_circuits_validation",
			args:    []string{"--version"},
			wantErr: "",
			check: func(t *testing.T, c cliConfig) {
				if !c.version {
					t.Error("version not set")
				}
			},
		},
		{
			name:    "missing_dir",
			args:    []string{"--scripts", "s"},
			wantErr: "--dir and --scripts are required",
		},
		{
			name:    "missing_scripts",
			args:    []string{"--dir", "d"},
			wantErr: "--dir and --scripts are required",
		},
		{
			name:    "missing_both",
			args:    []string{},
			wantErr: "--dir and --scripts are required",
		},
		{
			name:     "server_mode_defaults",
			args:     []string{"--dir", "d", "--scripts", "s"},
			wantErr:  "",
			wantMode: modeServer,
			check: func(t *testing.T, c cliConfig) {
				if c.host != "127.0.0.1" {
					t.Errorf("host = %q, want default 127.0.0.1", c.host)
				}
				if c.port != 0 {
					t.Errorf("port = %d, want 0 (stdio)", c.port)
				}
				if c.timeout != defaultToolTimeout {
					t.Errorf("timeout = %v, want default", c.timeout)
				}
				if c.maxConcurrent != defaultMaxConcurrentTools {
					t.Errorf("maxConcurrent = %d", c.maxConcurrent)
				}
				if c.apiKey != "" {
					t.Errorf("apiKey = %q, want empty", c.apiKey)
				}
			},
		},
		{
			name:     "server_mode_http_full",
			args:     []string{"--dir", "d", "--scripts", "s", "--host", "0.0.0.0", "--port", "9090", "--api-key", "tk", "--timeout", "1h", "--max-concurrent", "4", "--watch", "--insecure-no-auth"},
			wantErr:  "",
			wantMode: modeServer,
			check: func(t *testing.T, c cliConfig) {
				if c.host != "0.0.0.0" || c.port != 9090 {
					t.Errorf("host/port = %q/%d", c.host, c.port)
				}
				if c.apiKey != "tk" || !c.apiKeyFromFlag {
					t.Errorf("apiKey = %q fromFlag=%v", c.apiKey, c.apiKeyFromFlag)
				}
				if c.timeout != time.Hour {
					t.Errorf("timeout = %v", c.timeout)
				}
				if c.maxConcurrent != 4 {
					t.Errorf("maxConcurrent = %d", c.maxConcurrent)
				}
				if !c.watch || !c.insecureNoAuth {
					t.Errorf("watch/insecure = %v/%v", c.watch, c.insecureNoAuth)
				}
			},
		},
		{
			name:     "api_key_file_resolved",
			args:     []string{"--dir", "d", "--scripts", "s", "--api-key-file", keyFile},
			wantMode: modeServer,
			check: func(t *testing.T, c cliConfig) {
				if c.apiKey != "filetoken" {
					t.Errorf("apiKey = %q, want trimmed filetoken", c.apiKey)
				}
				if c.apiKeyFromFlag {
					t.Error("apiKeyFromFlag must be false for the file source")
				}
			},
		},
		{
			name:    "api_key_flag_wins_over_file",
			args:    []string{"--dir", "d", "--scripts", "s", "--api-key", "flag", "--api-key-file", keyFile},
			wantErr: "",
			check: func(t *testing.T, c cliConfig) {
				if c.apiKey != "flag" || !c.apiKeyFromFlag {
					t.Errorf("apiKey = %q fromFlag=%v, want flag/true", c.apiKey, c.apiKeyFromFlag)
				}
			},
		},
		{
			name:    "api_key_file_missing_fails",
			args:    []string{"--dir", "d", "--scripts", "s", "--api-key-file", filepath.Join(t.TempDir(), "nope")},
			wantErr: "--api-key-file",
		},
		{
			name:    "no_timeout",
			args:    []string{"--dir", "d", "--scripts", "s", "--no-timeout"},
			wantErr: "",
			check: func(t *testing.T, c cliConfig) {
				if c.timeout != 0 {
					t.Errorf("timeout = %v, want 0", c.timeout)
				}
			},
		},
		{
			name:    "timeout_and_no_timeout_exclusive",
			args:    []string{"--dir", "d", "--scripts", "s", "--timeout", "5m", "--no-timeout"},
			wantErr: "mutually exclusive",
		},
		{
			name:    "max_concurrent_negative",
			args:    []string{"--dir", "d", "--scripts", "s", "--max-concurrent", "-1"},
			wantErr: "--max-concurrent must be >= 0",
		},
		{
			name:    "list_tools_and_call_tool_exclusive",
			args:    []string{"--dir", "d", "--scripts", "s", "--list-tools", "--call-tool", "x"},
			wantErr: "mutually exclusive",
		},
		{
			name:     "list_tools_mode_reports_ignored_server_flags",
			args:     []string{"--dir", "d", "--scripts", "s", "--list-tools", "--port=0", "--api-key", "tk"},
			wantMode: modeListTools,
			check: func(t *testing.T, c cliConfig) {
				got := strings.Join(c.ignoredFlags, ",")
				if !strings.Contains(got, "--port") || !strings.Contains(got, "--api-key") {
					t.Errorf("ignoredFlags = %v", c.ignoredFlags)
				}
			},
		},
		{
			name:     "call_tool_mode_ignores_watch",
			args:     []string{"--dir", "d", "--scripts", "s", "--call-tool", "x", "--watch", "--port", "80"},
			wantMode: modeCallTool,
			check: func(t *testing.T, c cliConfig) {
				got := strings.Join(c.ignoredFlags, ",")
				if !strings.Contains(got, "--watch") || !strings.Contains(got, "--port") {
					t.Errorf("ignoredFlags = %v", c.ignoredFlags)
				}
				if !c.callToolSet {
					t.Error("callToolSet must be true")
				}
			},
		},
		{
			name:     "call_tool_present_empty_value_active",
			args:     []string{"--dir", "d", "--scripts", "s", "--call-tool", ""},
			wantMode: modeCallTool,
			check: func(t *testing.T, c cliConfig) {
				if !c.callToolSet {
					t.Error("callToolSet must be true even with an empty value")
				}
			},
		},
		{
			name:    "cors_exclusive",
			args:    []string{"--dir", "d", "--scripts", "s", "--allowed-origins", "a", "--allow-all-origins"},
			wantErr: "mutually exclusive",
		},
		{
			name:    "bad_timeout",
			args:    []string{"--dir", "d", "--scripts", "s", "--timeout", "bogus"},
			wantErr: "invalid timeout",
		},
		{
			name:    "unknown_flag",
			args:    []string{"--dir", "d", "--scripts", "s", "--no-such-flag"},
			wantErr: "flag provided but not defined",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := parseCLI(tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.mode != tt.wantMode {
				t.Errorf("mode = %v, want %v", cfg.mode, tt.wantMode)
			}
			if tt.check != nil {
				tt.check(t, cfg)
			}
		})
	}
}

// TestRunHTTPEndToEnd pins F-10's second half: run() served over real HTTP,
// exercised by a real MCP client (auth, list, call, clean shutdown), plus
// the zero-tools warning.
func TestRunHTTPEndToEnd(t *testing.T) {
	t.Run("one_tool_authenticated", func(t *testing.T) {
		dir := t.TempDir()
		scripts := t.TempDir()
		if err := os.WriteFile(filepath.Join(scripts, "hello.sh"), []byte("#!/bin/bash\necho hello-world\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		port := freePort(t)
		buf := &captureWriter{}
		t.Cleanup(func() { swapErrOut(buf) })
		swapErrOut(buf)

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- run(ctx, dir, scripts, false, "127.0.0.1", port, "sekret", corsConfig{}, defaultToolTimeout, false, 16)
		}()

		endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
		if !waitForHTTP(t, endpoint) {
			t.Fatal("server did not come up")
		}

		client := mcp.NewClient(&mcp.Implementation{Name: "e2e"}, nil)
		transport := &mcp.StreamableClientTransport{
			Endpoint:   endpoint,
			HTTPClient: &http.Client{Transport: &bearerTransport{token: "sekret"}},
			MaxRetries: 0,
		}
		session, err := client.Connect(ctx, transport, nil)
		if err != nil {
			t.Fatalf("client connect: %v", err)
		}
		defer session.Close()

		res, err := session.ListTools(ctx, nil)
		if err != nil {
			t.Fatalf("ListTools: %v", err)
		}
		if len(res.Tools) != 1 || res.Tools[0].Name != "hello" {
			t.Fatalf("tools = %+v, want exactly hello", res.Tools)
		}

		call, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "hello"})
		if err != nil {
			t.Fatalf("CallTool: %v", err)
		}
		if call.IsError || len(call.Content) == 0 {
			t.Fatalf("call = %+v, want non-error output", call)
		}
		if got := call.Content[0].(*mcp.TextContent).Text; !strings.Contains(got, "hello-world") {
			t.Errorf("output = %q, want to contain hello-world", got)
		}

		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("run returned %v after cancel, want nil", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("run did not stop after cancel")
		}
	})

	t.Run("zero_tools_warns", func(t *testing.T) {
		dir := t.TempDir()
		scripts := t.TempDir() // empty
		port := freePort(t)
		buf := &captureWriter{}
		t.Cleanup(func() { swapErrOut(buf) })
		swapErrOut(buf)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			done <- run(ctx, dir, scripts, false, "127.0.0.1", port, "", corsConfig{}, defaultToolTimeout, false, 16)
		}()

		endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
		if !waitForHTTP(t, endpoint) {
			t.Fatal("server did not come up")
		}

		deadline := time.Now().Add(5 * time.Second)
		for {
			if strings.Contains(buf.String(), "No executable scripts found") {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("zero-tools warning not captured, stderr = %q", buf.String())
			}
			time.Sleep(20 * time.Millisecond)
		}

		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("run returned %v after cancel, want nil", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("run did not stop after cancel")
		}
	})
}

// bearerTransport injects the Authorization header on every request.
type bearerTransport struct{ token string }

func (b *bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

// captureWriter is a goroutine-safe strings.Builder for errOut swapping.
type captureWriter struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (c *captureWriter) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

func (c *captureWriter) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

// swapErrOut redirects run()'s stderr sink; pair with t.Cleanup.
func swapErrOut(w io.Writer) {
	errOut = w
}

// freePort returns a port the kernel just freed from a throwaway listener.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

// waitForHTTP polls the endpoint until any HTTP response arrives.
func waitForHTTP(t *testing.T, url string) bool {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func TestBearerAuthMiddleware(t *testing.T) {
	const token = "tok"

	tests := []struct {
		name        string
		header      string
		wantStatus  int
		wantReached bool
	}{
		{name: "no_header", header: "", wantStatus: http.StatusUnauthorized, wantReached: false},
		{name: "scheme_only", header: "Bearer", wantStatus: http.StatusUnauthorized, wantReached: false},
		{name: "empty_token", header: "Bearer ", wantStatus: http.StatusUnauthorized, wantReached: false},
		{name: "wrong_scheme", header: "Basic abc", wantStatus: http.StatusUnauthorized, wantReached: false},
		{name: "lowercase_scheme_accepted", header: "bearer " + token, wantStatus: http.StatusOK, wantReached: true},
		{name: "wrong_token", header: "Bearer wrong", wantStatus: http.StatusUnauthorized, wantReached: false},
		{name: "correct_token", header: "Bearer " + token, wantStatus: http.StatusOK, wantReached: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reached := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached = true
				w.WriteHeader(http.StatusOK)
			})

			req := httptest.NewRequest(http.MethodPost, "/", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			rec := httptest.NewRecorder()

			newBearerAuthHandler(next, token).ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if reached != tt.wantReached {
				t.Errorf("downstream reached = %v, want %v", reached, tt.wantReached)
			}
			if rec.Code == http.StatusUnauthorized && rec.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Errorf("401 without WWW-Authenticate: Bearer, got %q", rec.Header().Get("WWW-Authenticate"))
			}
		})
	}
}

// newTestMCPServer builds an mcp.Server with one trivial tool.
func newTestMCPServer(t *testing.T) *mcp.Server {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	server.AddTool(&mcp.Tool{Name: "noop", Description: "no-op", InputSchema: buildInputSchema([]paramSpec{})}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
	})
	return server
}

// postInitializeStatus POSTs a JSON-RPC initialize request to url and returns
// the response status code. An empty auth value omits the Authorization header.
func postInitializeStatus(t *testing.T, url, auth string) int {
	t.Helper()
	resp := doInitialize(t, url, auth, "")
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// doInitialize POSTs a JSON-RPC initialize request and returns the response.
// Empty auth/origin values omit the corresponding headers.
func doInitialize(t *testing.T, url, auth, origin string) *http.Response {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"0.0.1"}}}`
	req, err := http.NewRequest(http.MethodPost, url+"/", strings.NewReader(body))
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	return resp
}

func TestBuildHTTPHandlerAuthDisabled(t *testing.T) {
	server := newTestMCPServer(t)
	httpServer := httptest.NewServer(buildHTTPHandler(server, "", corsConfig{}))
	defer httpServer.Close()

	status := postInitializeStatus(t, httpServer.URL, "")
	if status == http.StatusUnauthorized {
		t.Fatalf("expected unauthenticated server to serve request, got 401")
	}
}

func TestBuildHTTPHandlerEndToEnd(t *testing.T) {
	server := newTestMCPServer(t)
	httpServer := httptest.NewServer(buildHTTPHandler(server, "s3cret", corsConfig{}))
	defer httpServer.Close()

	if status := postInitializeStatus(t, httpServer.URL, ""); status != http.StatusUnauthorized {
		t.Errorf("unauthenticated request: status = %d, want 401", status)
	}

	status := postInitializeStatus(t, httpServer.URL, "Bearer s3cret")
	if status == http.StatusUnauthorized {
		t.Errorf("authenticated request: status = %d, want non-401", status)
	}
}

// TestBuildHTTPHandlerRejectsOversizedBody covers F-3's maxHTTPBodyBytes cap
// end-to-end: a chunked request whose body exceeds the 10 MiB limit must be
// rejected (400) without being read into memory. Chunked (ContentLength -1)
// so the SDK's io.ReadAll hits the MaxBytesReader limit mid-stream, exactly
// the multi-GB-chunked-body exhaustion vector from the review.
func TestBuildHTTPHandlerRejectsOversizedBody(t *testing.T) {
	server := newTestMCPServer(t)
	httpServer := httptest.NewServer(buildHTTPHandler(server, "", corsConfig{}))
	defer httpServer.Close()

	// Valid JSON-RPC initialize prefix, then enough padding to cross the cap.
	prefix := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
	body := io.NopCloser(io.MultiReader(
		strings.NewReader(prefix),
		strings.NewReader(strings.Repeat("a", maxHTTPBodyBytes+1<<10)),
	))

	req, err := http.NewRequest(http.MethodPost, httpServer.URL, body)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.ContentLength = -1 // chunked transfer encoding
	req.Header.Set("Content-Type", "application/json")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized body: status = %d, want 400", res.StatusCode)
	}
	if res.ContentLength >= maxHTTPBodyBytes {
		t.Fatalf("handler appeared to buffer the whole body (resp Content-Length = %d)", res.ContentLength)
	}
}

func TestResolveCORS(t *testing.T) {
	tests := []struct {
		name           string
		flag           string
		originsEnv     string
		allowAllEnv    string
		allowAllFlag   bool
		allowAllSet    bool
		disableLocalhp bool
		wantOrigins    []string
		wantAllowAll   bool
		wantDisableLHP bool
		wantErr        bool
		errSubstr      string
	}{
		{name: "flag_only", flag: "https://a.example", wantOrigins: []string{"https://a.example"}},
		{name: "env_only", originsEnv: "https://a.example, https://b.example",
			wantOrigins: []string{"https://a.example", "https://b.example"}},
		{name: "both_set_flag_wins", flag: "https://flag.example", originsEnv: "https://env.example",
			wantOrigins: []string{"https://flag.example"}},
		{name: "neither_set", wantOrigins: []string{}},
		{name: "comma_space_parsing", flag: "https://a.example, https://b.example",
			wantOrigins: []string{"https://a.example", "https://b.example"}},
		{name: "port_accepted", flag: "https://x.example:8443", wantOrigins: []string{"https://x.example:8443"}},
		{name: "port_only_authority_rejected", flag: "https://:8443", wantErr: true},
		{name: "allow_all_flag", allowAllFlag: true, allowAllSet: true, wantOrigins: []string{}, wantAllowAll: true},
		{name: "allow_all_env_true", allowAllEnv: "TRUE", wantOrigins: []string{}, wantAllowAll: true},
		{name: "allow_all_env_yes", allowAllEnv: "yes", wantOrigins: []string{}, wantAllowAll: true},
		// "0" is not in the recognized set (1/true/yes) → fail loud, per spec.
		{name: "allow_all_env_zero_rejected", allowAllEnv: "0", wantErr: true},
		// An explicit --allow-all-origins[=false] suppresses the env var, so
		// only an unset flag consults it.
		{name: "explicit_false_suppresses_env", allowAllFlag: false, allowAllSet: true, allowAllEnv: "1",
			wantOrigins: []string{}, wantAllowAll: false},
		{name: "unset_flag_reads_env", allowAllFlag: false, allowAllSet: false, allowAllEnv: "1",
			wantOrigins: []string{}, wantAllowAll: true},
		{name: "disable_localhost_protection", flag: "https://a.example", disableLocalhp: true,
			wantOrigins: []string{"https://a.example"}, wantDisableLHP: true},
		{name: "contradictory_origins_and_allow_all", flag: "https://a.example", allowAllFlag: true, allowAllSet: true,
			wantErr: true, errSubstr: "mutually exclusive"},
		{name: "contradiction_reported_before_bad_origin", flag: "notaurl", allowAllFlag: true, allowAllSet: true,
			wantErr: true, errSubstr: "mutually exclusive"},
		{name: "env_origins_and_env_allow_all", originsEnv: "https://a.example", allowAllEnv: "1", wantErr: true,
			errSubstr: "mutually exclusive"},
		{name: "malformed_not_a_url", flag: "notaurl", wantErr: true},
		{name: "malformed_missing_host", flag: "https://", wantErr: true},
		{name: "malformed_scheme", flag: "ftp://x.example", wantErr: true},
		{name: "malformed_userinfo", flag: "https://user@x.example", wantErr: true},
		{name: "malformed_path", flag: "https://x.example/path", wantErr: true},
		{name: "malformed_env_origin", originsEnv: "https://a.example, not-a-url", wantErr: true},
		{name: "allow_all_env_banana", allowAllEnv: "banana", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(allowedOriginsEnvVar, tt.originsEnv)
			t.Setenv(allowAllOriginsEnvVar, tt.allowAllEnv)

			got, err := resolveCORS(tt.flag, tt.allowAllFlag, tt.allowAllSet, tt.disableLocalhp)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (%+v)", got)
				}
				if tt.errSubstr != "" && !strings.Contains(err.Error(), tt.errSubstr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tt.errSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got.origins) != len(tt.wantOrigins) {
				t.Fatalf("origins = %v, want %v", got.origins, tt.wantOrigins)
			}
			for i, want := range tt.wantOrigins {
				if got.origins[i] != want {
					t.Errorf("origin[%d] = %q, want %q", i, got.origins[i], want)
				}
			}
			if got.allowAll != tt.wantAllowAll {
				t.Errorf("allowAll = %v, want %v", got.allowAll, tt.wantAllowAll)
			}
			if got.disableLocalhostProtection != tt.wantDisableLHP {
				t.Errorf("disableLocalhostProtection = %v, want %v", got.disableLocalhostProtection, tt.wantDisableLHP)
			}
			if !tt.wantAllowAll && len(tt.wantOrigins) == 0 && got.enabled() {
				t.Errorf("enabled() = true, want false for empty config")
			}
		})
	}
}

func TestCORSHandlerPreflight(t *testing.T) {
	tests := []struct {
		name       string
		requestHdr string // Access-Control-Request-Headers; "-" means omit
		wantACRHdr string
	}{
		{name: "echoes_requested_headers", requestHdr: "content-type, authorization", wantACRHdr: "content-type, authorization"},
		{name: "omits_when_not_requested", requestHdr: "-", wantACRHdr: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := corsConfig{origins: []string{"https://app.example.com"}}
			reached := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached = true
				w.WriteHeader(http.StatusTeapot)
			})

			req := httptest.NewRequest(http.MethodOptions, "/", nil)
			req.Header.Set("Origin", "https://app.example.com")
			if tt.requestHdr != "-" {
				req.Header.Set("Access-Control-Request-Headers", tt.requestHdr)
			}
			rec := httptest.NewRecorder()

			newCORSHandler(next, cfg).ServeHTTP(rec, req)

			if rec.Code != http.StatusNoContent {
				t.Errorf("status = %d, want 204", rec.Code)
			}
			if reached {
				t.Error("preflight was forwarded to next")
			}
			if rec.Body.Len() != 0 {
				t.Errorf("body = %q, want empty", rec.Body.String())
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
				t.Errorf("Access-Control-Allow-Origin = %q, want echoed origin", got)
			}
			if got := rec.Header().Get("Access-Control-Allow-Methods"); got != http.MethodPost+", "+http.MethodOptions {
				t.Errorf("Access-Control-Allow-Methods = %q, want %q", got, http.MethodPost+", "+http.MethodOptions)
			}
			if got := rec.Header().Get("Access-Control-Max-Age"); got != "900" {
				t.Errorf("Access-Control-Max-Age = %q, want 900", got)
			}
			if !strings.Contains(rec.Header().Get("Vary"), "Origin") {
				t.Errorf("Vary = %q, want to include Origin", rec.Header().Get("Vary"))
			}
			if got := rec.Header().Get("Access-Control-Allow-Headers"); got != tt.wantACRHdr {
				t.Errorf("Access-Control-Allow-Headers = %q, want %q", got, tt.wantACRHdr)
			}
		})
	}
}

func TestCORSHandlerNonPreflight(t *testing.T) {
	tests := []struct {
		name        string
		cfg         corsConfig
		origin      string // "-" means omit the header
		wantAllowed bool   // CORS headers present?
		wantReached bool
	}{
		{name: "no_origin_passthrough", cfg: corsConfig{origins: []string{"https://app.example.com"}}, origin: "-",
			wantAllowed: false, wantReached: true},
		{name: "disallowed_origin_passthrough", cfg: corsConfig{origins: []string{"https://app.example.com"}}, origin: "https://evil.example",
			wantAllowed: false, wantReached: true},
		{name: "allowed_origin", cfg: corsConfig{origins: []string{"https://app.example.com"}}, origin: "https://app.example.com",
			wantAllowed: true, wantReached: true},
		{name: "allow_all", cfg: corsConfig{allowAll: true}, origin: "https://any.example",
			wantAllowed: true, wantReached: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reached := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached = true
				w.WriteHeader(http.StatusOK)
			})

			req := httptest.NewRequest(http.MethodPost, "/", nil)
			if tt.origin != "-" {
				req.Header.Set("Origin", tt.origin)
			}
			rec := httptest.NewRecorder()

			newCORSHandler(next, tt.cfg).ServeHTTP(rec, req)

			if reached != tt.wantReached {
				t.Fatalf("downstream reached = %v, want %v", reached, tt.wantReached)
			}
			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200 (unchanged)", rec.Code)
			}
			acAO := rec.Header().Get("Access-Control-Allow-Origin")
			if tt.wantAllowed {
				if acAO != tt.origin {
					t.Errorf("Access-Control-Allow-Origin = %q, want %q", acAO, tt.origin)
				}
				if !strings.Contains(rec.Header().Get("Vary"), "Origin") {
					t.Errorf("Vary = %q, want to include Origin", rec.Header().Get("Vary"))
				}
				if got := rec.Header().Get("Access-Control-Expose-Headers"); got != "Mcp-Session-Id, Last-Event-ID" {
					t.Errorf("Access-Control-Expose-Headers = %q, want %q", got, "Mcp-Session-Id, Last-Event-ID")
				}
			} else if acAO != "" {
				t.Errorf("Access-Control-Allow-Origin = %q, want absent", acAO)
			}
		})
	}
}

func TestBuildHTTPHandlerPreflightUnauthenticated(t *testing.T) {
	server := newTestMCPServer(t)
	cors := corsConfig{origins: []string{"https://app.example.com"}}
	httpServer := httptest.NewServer(buildHTTPHandler(server, "s3cret", cors))
	defer httpServer.Close()

	req, err := http.NewRequest(http.MethodOptions, httpServer.URL+"/", nil)
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	req.Header.Set("Origin", "https://app.example.com")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("OPTIONS failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204 (not 401)", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("Access-Control-Allow-Origin = %q, want echoed origin", got)
	}
}

func TestBuildHTTPHandler401CarriesCORS(t *testing.T) {
	server := newTestMCPServer(t)
	cors := corsConfig{origins: []string{"https://app.example.com"}}
	httpServer := httptest.NewServer(buildHTTPHandler(server, "s3cret", cors))
	defer httpServer.Close()

	req, err := http.NewRequest(http.MethodPost, httpServer.URL+"/", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer wrong")
	req.Header.Set("Origin", "https://app.example.com")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("401 Access-Control-Allow-Origin = %q, want echoed origin", got)
	}
	if got := resp.Header.Get("Access-Control-Expose-Headers"); got != "Mcp-Session-Id, Last-Event-ID" {
		t.Errorf("401 Access-Control-Expose-Headers = %q, want %q", got, "Mcp-Session-Id, Last-Event-ID")
	}
}

func TestBuildHTTPHandlerInitializeCORS(t *testing.T) {
	server := newTestMCPServer(t)
	cors := corsConfig{origins: []string{"https://app.example.com"}}
	httpServer := httptest.NewServer(buildHTTPHandler(server, "s3cret", cors))
	defer httpServer.Close()

	resp := doInitialize(t, httpServer.URL, "Bearer s3cret", "https://app.example.com")
	if resp.StatusCode == http.StatusUnauthorized {
		t.Fatalf("authenticated request: status = %d, want non-401", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("Access-Control-Allow-Origin = %q, want echoed origin", got)
	}
	sessionID := resp.Header.Get("Mcp-Session-Id")
	resp.Body.Close()

	// Always stateless: a stored/re-sent session ID must keep working
	// (the SDK ignores it) — even if it is a made-up value.
	req, err := http.NewRequest(http.MethodPost, httpServer.URL+"/", strings.NewReader(
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer s3cret")
	req.Header.Set("Origin", "https://app.example.com")
	if sessionID == "" {
		sessionID = "some-stale-id"
	}
	req.Header.Set("Mcp-Session-Id", sessionID)

	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("tools/list failed: %v", err)
	}
	defer resp2.Body.Close()
	_, _ = io.Copy(io.Discard, resp2.Body)
	if resp2.StatusCode == http.StatusUnauthorized || resp2.StatusCode == http.StatusNotFound {
		t.Errorf("request with session ID %q: status = %d, want served (stateless ignores session IDs)", sessionID, resp2.StatusCode)
	}
	if got := resp2.Header.Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("Access-Control-Allow-Origin = %q, want echoed origin", got)
	}
}

func TestBuildHTTPHandlerCORSDisabled(t *testing.T) {
	server := newTestMCPServer(t)
	httpServer := httptest.NewServer(buildHTTPHandler(server, "", corsConfig{}))
	defer httpServer.Close()

	resp := doInitialize(t, httpServer.URL, "", "")
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode == http.StatusUnauthorized {
		t.Fatalf("unauthenticated server: status = %d, want non-401", resp.StatusCode)
	}
	headers := resp.Header
	if got := headers.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want absent (CORS disabled)", got)
	}
	if got := headers.Get("Access-Control-Expose-Headers"); got != "" {
		t.Errorf("Access-Control-Expose-Headers = %q, want absent (CORS disabled)", got)
	}
	if got := headers.Get("Vary"); got != "" {
		t.Errorf("Vary = %q, want absent (CORS disabled)", got)
	}
}

// TestToolsEqual covers the F-11 change-diff: every field a rescan can
// change must invalidate the skip, and an unchanged set must compare equal.
func TestToolsEqual(t *testing.T) {
	timeout5 := 5 * time.Minute
	timeout0 := time.Duration(0)
	tests := []struct {
		name string
		a, b discoveredTool
		want bool
	}{
		{name: "identical", a: discoveredTool{Name: "t", Path: "/x", Description: "d"}, b: discoveredTool{Name: "t", Path: "/x", Description: "d"}, want: true},
		{name: "nil_timeouts_equal", a: discoveredTool{Name: "t"}, b: discoveredTool{Name: "t"}, want: true},
		{name: "different_names", a: discoveredTool{Name: "a"}, b: discoveredTool{Name: "b"}, want: false},
		{name: "different_paths", a: discoveredTool{Name: "t", Path: "/x"}, b: discoveredTool{Name: "t", Path: "/y"}, want: false},
		{name: "different_descriptions", a: discoveredTool{Name: "t", Description: "one"}, b: discoveredTool{Name: "t", Description: "two"}, want: false},
		{name: "nil_vs_zero_timeout", a: discoveredTool{Name: "t"}, b: discoveredTool{Name: "t", Timeout: &timeout0}, want: false},
		{name: "timeout_values_differ", a: discoveredTool{Name: "t", Timeout: &timeout5}, b: discoveredTool{Name: "t", Timeout: &timeout0}, want: false},
		{name: "params_differ", a: discoveredTool{Name: "t", Params: []paramSpec{{Name: "p", Type: "string", Required: true}}}, b: discoveredTool{Name: "t", Params: []paramSpec{{Name: "p", Type: "number"}}}, want: false},
		{name: "params_reordered", a: discoveredTool{Name: "t", Params: []paramSpec{{Name: "p"}, {Name: "q"}}}, b: discoveredTool{Name: "t", Params: []paramSpec{{Name: "q"}, {Name: "p"}}}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toolsEqual([]discoveredTool{tt.a}, []discoveredTool{tt.b}); got != tt.want {
				t.Errorf("toolsEqual = %v, want %v", got, tt.want)
			}
		})
	}
	if toolsEqual([]discoveredTool{{Name: "a"}, {Name: "b"}}, []discoveredTool{{Name: "a"}}) {
		t.Error("different lengths must not compare equal")
	}
}

// TestWatchToolsSkipsIdenticalRescan pins F-11: a debounced rescan whose
// result is identical to the registered set must emit no RemoveTools/AddTool
// churn and no tools/list_changed notifications, while a genuine change
// still reloads.
func TestWatchToolsSkipsIdenticalRescan(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "alpha.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\n# Description: alpha\necho alpha\n"), 0o755); err != nil {
		t.Fatalf("failed to create script: %v", err)
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
	registry := newToolRegistry(server, "", defaultToolTimeout, 16)
	initial, err := discoverTools(tmpDir)
	if err != nil {
		t.Fatalf("discoverTools failed: %v", err)
	}
	registry.replace(initial)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx := context.Background()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect failed: %v", err)
	}
	defer serverSession.Close()

	var changed atomic.Int32
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client"}, &mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) { changed.Add(1) },
	})
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect failed: %v", err)
	}
	defer clientSession.Close()

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		_ = watchTools(watchCtx, tmpDir, registry, 20*time.Millisecond)
	}()

	// The startup replace runs unconditionally (the F-17 double-scan is out
	// of scope); wait for its notification(s) to drain, then take a baseline.
	deadline := time.Now().Add(5 * time.Second)
	for changed.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	baseline := changed.Load()

	// No-op touches: same content → identical set → must be skipped. Bounded
	// retries (the F-22 pattern): a lost fsnotify event is legal, so keep
	// touching until a rescan window has elapsed with zero notifications —
	// that silence is the assertion.
	noopSawChange := false
	for i := 0; i < 10; i++ {
		if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\n# Description: alpha\necho alpha\n"), 0o755); err != nil {
			t.Fatalf("failed to touch script: %v", err)
		}
		time.Sleep(600 * time.Millisecond) // several debounce windows
		if changed.Load() != baseline {
			noopSawChange = true
			break
		}
		if i > 2 { // a few clean windows: the skip is working
			break
		}
	}
	if noopSawChange {
		t.Fatalf("no-op rescan emitted %d list_changed notification(s) above baseline, want 0", changed.Load()-baseline)
	}

	// A real frontmatter change must reload.
	for i := 0; i < 10; i++ {
		if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\n# Description: alpha v2\necho alpha\n"), 0o755); err != nil {
			t.Fatalf("failed to update script: %v", err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for changed.Load() == baseline && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if changed.Load() != baseline {
			break
		}
	}
	if changed.Load() == baseline {
		t.Fatal("a genuine description change did not trigger a reload after 10 attempts")
	}

	res, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}
	if len(res.Tools) != 1 || res.Tools[0].Description != "alpha v2 (timeout: 5m0s)" {
		t.Fatalf("tools after reload = %+v, want the updated description", res.Tools)
	}
}

func TestResolvedTimeoutViaRegistry(t *testing.T) {
	tmpDir := t.TempDir()
	sleepPath := filepath.Join(tmpDir, "sleep5.sh")
	if err := os.WriteFile(sleepPath, []byte("#!/bin/bash\nexec sleep 5\n"), 0o755); err != nil {
		t.Fatalf("failed to create sleep script: %v", err)
	}
	fastPath := filepath.Join(tmpDir, "fast.sh")
	if err := os.WriteFile(fastPath, []byte("#!/bin/bash\necho fast\n"), 0o755); err != nil {
		t.Fatalf("failed to create fast script: %v", err)
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
	registry := newToolRegistry(server, tmpDir, 2*time.Second, 16)
	oneSecond := time.Second
	noTimeout := time.Duration(0)
	registry.replace([]discoveredTool{
		{Name: "pinned", Path: sleepPath, Description: "pinned tool", Timeout: &oneSecond},
		{Name: "inherited", Path: sleepPath, Description: "inherited tool"}, // nil: inherits global
		{Name: "none", Path: fastPath, Description: "none tool", Timeout: &noTimeout},
		{Name: "bare", Path: fastPath, Timeout: &noTimeout}, // empty description: suffix alone
		{Name: "canceller", Path: sleepPath, Timeout: &noTimeout},
	})

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx := context.Background()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect failed: %v", err)
	}
	defer serverSession.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect failed: %v", err)
	}
	defer clientSession.Close()

	res, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}
	descs := make(map[string]string, len(res.Tools))
	for _, tool := range res.Tools {
		descs[tool.Name] = tool.Description
	}
	wantDescs := map[string]string{
		"pinned":    "pinned tool (timeout: 1s)",
		"inherited": "inherited tool (timeout: 2s)",
		"none":      "none tool (timeout: none)",
		"bare":      "(timeout: none)",
		"canceller": "(timeout: none)",
	}
	for name, want := range wantDescs {
		if got := descs[name]; got != want {
			t.Errorf("description of %q = %q, want %q", name, got, want)
		}
	}

	t.Run("per_tool_timeout_wins", func(t *testing.T) {
		start := time.Now()
		result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "pinned"})
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("CallTool failed: %v", err)
		}
		if result == nil || !result.IsError {
			t.Fatalf("expected IsError result, got %#v", result)
		}
		if got := result.Content[0].(*mcp.TextContent).Text; !strings.Contains(got, "timed out after 1s") {
			t.Fatalf("expected timeout message with 1s, got %q", got)
		}
		if elapsed > 4*time.Second {
			t.Errorf("call took %v, want well under the 5s script duration", elapsed)
		}
	})

	t.Run("global_timeout_inherited", func(t *testing.T) {
		start := time.Now()
		result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "inherited"})
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("CallTool failed: %v", err)
		}
		if result == nil || !result.IsError {
			t.Fatalf("expected IsError result, got %#v", result)
		}
		if got := result.Content[0].(*mcp.TextContent).Text; !strings.Contains(got, "timed out after 2s") {
			t.Fatalf("expected timeout message with 2s, got %q", got)
		}
		if elapsed > 4*time.Second {
			t.Errorf("call took %v, want well under the 5s script duration", elapsed)
		}
	})

	t.Run("none_under_short_global_completes", func(t *testing.T) {
		start := time.Now()
		result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "none"})
		if err != nil {
			t.Fatalf("CallTool failed: %v", err)
		}
		if result == nil || result.IsError {
			t.Fatalf("expected success result, got %#v", result)
		}
		if got := result.Content[0].(*mcp.TextContent).Text; !strings.Contains(got, "fast") {
			t.Fatalf("expected fast output, got %q", got)
		}
		if elapsed := time.Since(start); elapsed > 4*time.Second {
			t.Errorf("call took %v, want well under the 2s global", elapsed)
		}
	})

	t.Run("cancellation_kills_script_without_deadline", func(t *testing.T) {
		callCtx, cancel := context.WithCancel(ctx)
		go func() {
			time.Sleep(500 * time.Millisecond)
			cancel()
		}()
		start := time.Now()
		result, err := clientSession.CallTool(callCtx, &mcp.CallToolParams{Name: "canceller"})
		elapsed := time.Since(start)
		if err == nil && (result == nil || !result.IsError) {
			t.Fatalf("expected error or IsError result after cancellation, got %#v", result)
		}
		if elapsed > 4*time.Second {
			t.Errorf("canceled call took %v, want ~500ms (request ctx kills the script)", elapsed)
		}
	})
}

func TestWatchToolsDetectsTimeoutChanges(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "alpha.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\necho alpha\n"), 0o755); err != nil {
		t.Fatalf("failed to create script: %v", err)
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
	registry := newToolRegistry(server, "", 2*time.Second, 16)
	registry.replace([]discoveredTool{{Name: "alpha", Path: scriptPath, Description: "alpha", Params: []paramSpec{}}})

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx := context.Background()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect failed: %v", err)
	}
	defer serverSession.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect failed: %v", err)
	}
	defer clientSession.Close()

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- watchTools(watchCtx, tmpDir, registry, 20*time.Millisecond)
	}()

	// Hot reload: editing only the Timeout: line re-registers the tool.
	// Bounded write retries: a lost fsnotify event is recovered by the next
	// write (see TestWatchTools). (F-22)
	var lastTools []*mcp.Tool
	refreshed := false
	for i := 0; i < 10 && !refreshed; i++ {
		if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\n# Timeout: 30s\necho alpha\n"), 0o755); err != nil {
			t.Fatalf("failed to update script: %v", err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			res, err := clientSession.ListTools(ctx, nil)
			if err != nil {
				t.Fatalf("ListTools failed: %v", err)
			}
			lastTools = res.Tools
			if len(res.Tools) == 1 && res.Tools[0].Description == "(timeout: 30s)" {
				refreshed = true
				break
			}
			if time.Now().After(deadline) {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !refreshed {
		cancel()
		t.Fatalf("watchTools did not refresh updated Timeout after 10 write attempts: %+v", lastTools)
	}
	cancel()

	select {
	case err := <-done:
		if err != nil && err != context.Canceled {
			t.Fatalf("watchTools returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("watchTools did not stop after cancel")
	}
}

func TestRequiredParamValidationViaRegistry(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "convert.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\necho done\n"), 0o755); err != nil {
		t.Fatalf("failed to create script: %v", err)
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
	registry := newToolRegistry(server, tmpDir, defaultToolTimeout, 16)

	registry.replace([]discoveredTool{
		{
			Name:        "convert",
			Path:        scriptPath,
			Description: "Convert a file",
			Params: []paramSpec{
				{Name: "path", Type: "string", Required: true, Description: "Input path"},
			},
		},
	})

	handler := registry.lastHandler
	if handler == nil {
		t.Fatal("lastHandler is nil after replace")
	}

	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "convert",
			Arguments: json.RawMessage(`{}`),
		},
	}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler returned unexpected error: %v", err)
	}
	if result == nil || !result.IsError {
		t.Fatalf("expected IsError result, got %#v", result)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "missing required parameter: path") {
		t.Fatalf("expected error message containing 'missing required parameter: path', got %q", text)
	}
}

func TestListParamDecl(t *testing.T) {
	tests := []struct {
		param paramSpec
		want  string
	}{
		{paramSpec{Name: "a", Type: "string", Required: true}, "a:str"},
		{paramSpec{Name: "b", Type: "number"}, "[b:num]"},
		{paramSpec{Name: "c", Type: "boolean", Required: true}, "c:bool"},
		{paramSpec{Name: "d", Type: "string"}, "[d:str]"},
	}
	for _, tt := range tests {
		if got := listParamDecl(tt.param); got != tt.want {
			t.Errorf("listParamDecl(%#v) = %q, want %q", tt.param, got, tt.want)
		}
	}
}

func TestToolListSignature(t *testing.T) {
	tests := []struct {
		name   string
		params []paramSpec
		want   string
	}{
		{"none", nil, "none()"},
		{"none", []paramSpec{}, "none()"},
		{"required_only", []paramSpec{
			{Name: "a", Type: "string", Required: true},
			{Name: "b", Type: "number", Required: true},
		}, "required_only(a:str, b:num)"},
		{"optional_only", []paramSpec{
			{Name: "a", Type: "string"},
			{Name: "b", Type: "number"},
		}, "optional_only([a:str], [b:num])"},
		{"mixed", []paramSpec{
			{Name: "a", Type: "string", Required: true},
			{Name: "b", Type: "number"},
			{Name: "c", Type: "boolean"},
		}, "mixed(a:str, [b:num], [c:bool])"},
		// Duplicate name: last declaration wins for type/required, rendered
		// at the first-occurrence position (mirrors buildInputSchema).
		{"duplicate", []paramSpec{
			{Name: "x", Type: "string", Required: true},
			{Name: "a", Type: "string", Required: true},
			{Name: "y", Type: "number"},
			{Name: "a", Type: "number"},
			{Name: "z", Type: "boolean"},
		}, "duplicate(x:str, [a:num], [y:num], [z:bool])"},
	}
	for _, tt := range tests {
		if got := toolListSignature(tt.name, tt.params); got != tt.want {
			t.Errorf("toolListSignature(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestWordWrap(t *testing.T) {
	indent := "     "

	t.Run("short_text_single_line", func(t *testing.T) {
		got := wordWrap("hello world", indent, 160)
		want := []string{"     hello world"}
		if len(got) != 1 || got[0] != want[0] {
			t.Fatalf("wordWrap = %#v, want %#v", got, want)
		}
	})

	t.Run("whitespace_runs_collapse_to_one_space", func(t *testing.T) {
		got := wordWrap("a\t\t  b\n\nc", indent, 160)
		want := []string{"     a b c"}
		if len(got) != 1 || got[0] != want[0] {
			t.Fatalf("wordWrap = %#v, want %#v", got, want)
		}
	})

	t.Run("leading_trailing_whitespace_stripped", func(t *testing.T) {
		got := wordWrap("  a b  ", indent, 160)
		want := []string{"     a b"}
		if len(got) != 1 || got[0] != want[0] {
			t.Fatalf("wordWrap = %#v, want %#v", got, want)
		}
	})

	t.Run("three_lines_at_160", func(t *testing.T) {
		// 35 words of 8 runes: 17 words fit per 155-rune budget
		// (17*8+16=152; the 18th needs 161), so 35 words → 17+17+1.
		var b strings.Builder
		for i := 0; i < 35; i++ {
			if i > 0 {
				b.WriteByte(' ')
			}
			b.WriteString("wordword")
		}
		word := "wordword"
		want := []string{
			"     " + strings.Repeat(word+" ", 16) + word,
			"     " + strings.Repeat(word+" ", 16) + word,
			"     " + word,
		}
		got := wordWrap(b.String(), indent, 160)
		if len(got) != 3 {
			t.Fatalf("wordWrap returned %d lines, want 3: %#v", len(got), got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("line %d = %q, want %q", i, got[i], want[i])
			}
			if n := len([]rune(got[i])); n > 160 {
				t.Errorf("line %d exceeds the 160-rune width: %d runes", i, n)
			}
		}
	})

	t.Run("overlong_token_emitted_whole_unsplit", func(t *testing.T) {
		long := strings.Repeat("x", 200)
		got := wordWrap("a "+long+" b", indent, 160)
		want := []string{"     a", "     " + long, "     b"}
		if len(got) != 3 {
			t.Fatalf("wordWrap returned %d lines, want 3: %q...", len(got), got[:min(len(got), 1)])
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("line %d = %q (len %d), want %q (len %d)", i, got[i], len(got[i]), want[i], len(want[i]))
			}
		}
	})

	t.Run("multibyte_runes_never_torn", func(t *testing.T) {
		// Em-dashes (—) and accented chars; each "word" is 3 runes.
		var b strings.Builder
		for i := 0; i < 60; i++ {
			if i > 0 {
				b.WriteByte(' ')
			}
			b.WriteString("wörld")
		}
		got := wordWrap(b.String(), indent, 160)
		for i, l := range got {
			if n := len([]rune(l)); n > 160 {
				t.Errorf("line %d exceeds the 160-RUNE width: %d runes", i, n)
			}
		}
		joined := strings.Join(strings.Fields(strings.Join(got, " ")), " ")
		if joined != b.String() {
			t.Error("wrapped text lost multibyte content")
		}
	})

	t.Run("empty_input_yields_single_indent_line", func(t *testing.T) {
		got := wordWrap("", indent, 160)
		want := []string{"     "}
		if len(got) != 1 || got[0] != want[0] {
			t.Fatalf("wordWrap(\"\") = %#v, want %#v", got, want)
		}
	})

	t.Run("width_smaller_than_indent", func(t *testing.T) {
		// Degenerate width must not crash; budget floors at 1 rune.
		got := wordWrap("ab cd", "     ", 3)
		if len(got) == 0 {
			t.Fatal("wordWrap returned no lines for non-empty input")
		}
	})
}

func TestRenderToolList(t *testing.T) {
	t.Run("no_params_short_description", func(t *testing.T) {
		none := time.Duration(0)
		tools := []discoveredTool{
			{Name: "hello", Description: "Say hello", Timeout: &none},
		}
		got := renderToolList(tools, 5*time.Minute, 160)
		want := "hello()\n     Say hello (timeout: none)\n"
		if got != want {
			t.Fatalf("renderToolList = %q, want %q", got, want)
		}
	})

	t.Run("mixed_params_and_inherited_global_timeout", func(t *testing.T) {
		tools := []discoveredTool{
			{Name: "mixed", Description: "Does things", Params: []paramSpec{
				{Name: "a", Type: "string", Required: true},
				{Name: "b", Type: "number"},
				{Name: "c", Type: "boolean"},
			}},
		}
		got := renderToolList(tools, 5*time.Minute, 160)
		want := "mixed(a:str, [b:num], [c:bool])\n     Does things (timeout: 5m0s)\n"
		if got != want {
			t.Fatalf("renderToolList = %q, want %q", got, want)
		}
	})

	t.Run("empty_description_suffix_alone", func(t *testing.T) {
		none := time.Duration(0)
		tools := []discoveredTool{{Name: "bare", Timeout: &none}}
		got := renderToolList(tools, 5*time.Minute, 160)
		want := "bare()\n     (timeout: none)\n"
		if got != want {
			t.Fatalf("renderToolList = %q, want %q", got, want)
		}
	})

	t.Run("duplicate_param_name_last_wins_first_position", func(t *testing.T) {
		tools := []discoveredTool{
			{Name: "dup", Description: "d", Params: []paramSpec{
				{Name: "x", Type: "string", Required: true},
				{Name: "a", Type: "string", Required: true},
				{Name: "y", Type: "number"},
				{Name: "a", Type: "number"},
				{Name: "z", Type: "boolean"},
			}},
		}
		got := renderToolList(tools, 5*time.Minute, 160)
		want := "dup(x:str, [a:num], [y:num], [z:bool])\n     d (timeout: 5m0s)\n"
		if got != want {
			t.Fatalf("renderToolList = %q, want %q", got, want)
		}
	})

	t.Run("plan_example_byte_exact", func(t *testing.T) {
		none := time.Duration(0)
		twoMin := 2 * time.Minute
		tools := []discoveredTool{
			{
				Name:        "meridian_build",
				Description: "Compile the Meridian workspace (or just meridian-app) on the remote host — the offload target; the long pole is the Bevy dep tree, built once then cached. Timeout is NONE (a full clean build of the Bevy dep tree can far exceed the 5-minute MCP default; re-call to see a slow build's result) — prefer scope=app for incremental work.",
				Timeout:     &none,
				Params: []paramSpec{
					{Name: "profile", Type: "string"},
					{Name: "scope", Type: "string"},
					{Name: "target", Type: "string"},
					{Name: "timings", Type: "boolean"},
				},
			},
			{
				Name:        "meridian_fetch",
				Description: "Check out the pinned commit of the Meridian repo on the remote host (idempotent) and report HEAD/branch, rustc, cargo and Cargo.lock info. Run first on a remote host before any other meridian tool.",
				Timeout:     &twoMin,
				Params:      []paramSpec{{Name: "commit", Type: "string"}},
			},
		}
		want := "meridian_build([profile:str], [scope:str], [target:str], [timings:bool])\n" +
			"     Compile the Meridian workspace (or just meridian-app) on the remote host — the offload target; the long pole is the Bevy dep tree, built once then cached.\n" +
			"     Timeout is NONE (a full clean build of the Bevy dep tree can far exceed the 5-minute MCP default; re-call to see a slow build's result) — prefer scope=app\n" +
			"     for incremental work. (timeout: none)\n" +
			"\n" +
			"meridian_fetch([commit:str])\n" +
			"     Check out the pinned commit of the Meridian repo on the remote host (idempotent) and report HEAD/branch, rustc, cargo and Cargo.lock info. Run first on a\n" +
			"     remote host before any other meridian tool. (timeout: 2m0s)\n"
		if got := renderToolList(tools, 5*time.Minute, 160); got != want {
			t.Fatalf("renderToolList did not match the spec example:\ngot:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("blank_line_between_blocks", func(t *testing.T) {
		tools := []discoveredTool{
			{Name: "a", Description: "one"},
			{Name: "b", Description: "two"},
			{Name: "c", Description: "three"},
		}
		got := renderToolList(tools, 5*time.Minute, 160)
		want := "a()\n     one (timeout: 5m0s)\n\nb()\n     two (timeout: 5m0s)\n\nc()\n     three (timeout: 5m0s)\n"
		if got != want {
			t.Fatalf("renderToolList = %q, want %q", got, want)
		}
	})

	t.Run("zero_tools_empty_output", func(t *testing.T) {
		if got := renderToolList(nil, 5*time.Minute, 160); got != "" {
			t.Fatalf("renderToolList(nil) = %q, want empty", got)
		}
	})
}

func TestRegisteredDescription(t *testing.T) {
	tests := []struct {
		desc    string
		timeout time.Duration
		want    string
	}{
		{"do things", 30 * time.Second, "do things (timeout: 30s)"},
		{"do things", 5 * time.Minute, "do things (timeout: 5m0s)"},
		{"do things", 0, "do things (timeout: none)"},
		{"", 30 * time.Second, "(timeout: 30s)"},
		{"", 0, "(timeout: none)"},
	}
	for _, tt := range tests {
		if got := registeredDescription(tt.desc, tt.timeout); got != tt.want {
			t.Errorf("registeredDescription(%q, %v) = %q, want %q", tt.desc, tt.timeout, got, tt.want)
		}
	}
}

func TestResolveToolTimeout(t *testing.T) {
	perTool := 30 * time.Second
	none := time.Duration(0)
	tests := []struct {
		name   string
		tool   discoveredTool
		global time.Duration
		want   time.Duration
	}{
		{"per_tool_wins_over_global", discoveredTool{Timeout: &perTool}, 5 * time.Minute, 30 * time.Second},
		{"per_tool_none_wins_over_global", discoveredTool{Timeout: &none}, 5 * time.Minute, 0},
		{"nil_inherits_global", discoveredTool{}, 5 * time.Minute, 5 * time.Minute},
		{"nil_inherits_no_timeout_global", discoveredTool{}, 0, 0},
	}
	for _, tt := range tests {
		if got := resolveToolTimeout(tt.tool, tt.global); got != tt.want {
			t.Errorf("%s: resolveToolTimeout = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestWatchChanges(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "alpha.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\necho alpha\n"), 0o755); err != nil {
		t.Fatalf("failed to create script: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- watchChanges(ctx, tmpDir, func() { calls.Add(1) })
	}()

	// Re-write until the change is observed: the first write can race the
	// watcher registration (a lost event is legal for fsnotify), and only
	// writes landing after Add is guaranteed to produce a debounced fire.
	for i := 0; i < 40; i++ {
		if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\necho beta\n"), 0o755); err != nil {
			t.Fatalf("failed to modify script: %v", err)
		}
		deadline := time.Now().Add(2 * time.Second)
		for calls.Load() == 0 && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if calls.Load() > 0 {
			break
		}
	}
	if calls.Load() == 0 {
		t.Fatal("onChange did not fire on file change")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil && err != context.Canceled {
			t.Fatalf("watchChanges returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("watchChanges did not stop after cancel")
	}
}

// TestWatchChangesNoSpuriousFire guards the debounce arming: with zero
// filesystem events the timer must stay disarmed (regression — NewTimer
// arms the clock immediately, so the first debounce window elapsed without
// any event).
func TestWatchChangesNoSpuriousFire(t *testing.T) {
	tmpDir := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- watchChanges(ctx, tmpDir, func() { calls.Add(1) })
	}()

	time.Sleep(500 * time.Millisecond) // several debounce windows, no events

	cancel()
	select {
	case err := <-done:
		if err != nil && err != context.Canceled {
			t.Fatalf("watchChanges returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("watchChanges did not stop after cancel")
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("onChange fired %d times with zero events, want 0", n)
	}
}

// TestWatchChangesWatchedDirDeleted pins F-9: the watcher must log the
// error and keep running when the watched directory disappears mid-watch —
// it must return only on ctx cancellation, not on the deletion.
func TestWatchChangesWatchedDirDeleted(t *testing.T) {
	tmpDir := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- watchChanges(ctx, tmpDir, func() {})
	}()

	// Let the watcher goroutine finish NewWatcher/Add before the deletion,
	// so the test exercises the mid-watch failure path (a deletion landing
	// before Add is the fail-fast "failed to watch directory" branch).
	time.Sleep(200 * time.Millisecond)
	if err := os.RemoveAll(tmpDir); err != nil {
		t.Fatalf("failed to delete watched dir: %v", err)
	}

	// Give the watcher time to observe the deletion (it must not exit).
	select {
	case err := <-done:
		t.Fatalf("watchChanges exited with %v after the watched dir was deleted; it must keep running", err)
	case <-time.After(500 * time.Millisecond):
	}

	cancel()
	select {
	case err := <-done:
		if err != nil && err != context.Canceled {
			t.Fatalf("watchChanges returned %v after cancel, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("watchChanges did not stop after cancel")
	}
}

// TestWatchChangesRenameTriggersChange pins F-9: renaming a file (fsnotify
// Rename — on linux this arrives as a Move event) must fire the debounced
// onChange, like create/write/remove do.
func TestWatchChangesRenameTriggersChange(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "alpha.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\necho alpha\n"), 0o755); err != nil {
		t.Fatalf("failed to create script: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int32
	go func() {
		_ = watchChanges(ctx, tmpDir, func() { calls.Add(1) })
	}()

	// The first rename can race watcher registration (a lost event is legal
	// for fsnotify); retry with a fresh rename until the fire is observed.
	for i := 0; i < 10; i++ {
		if err := os.Rename(scriptPath, filepath.Join(tmpDir, "beta.sh")); err != nil {
			t.Fatalf("failed to rename script: %v", err)
		}
		deadline := time.Now().Add(2 * time.Second)
		for calls.Load() == 0 && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if calls.Load() > 0 {
			break
		}
		// Prepare for the next attempt: rename back.
		_ = os.Rename(filepath.Join(tmpDir, "beta.sh"), scriptPath)
	}
	if calls.Load() == 0 {
		t.Fatal("onChange did not fire on rename")
	}
}

// TestWatchChangesPermissionError pins F-9: a watcher permission error
// (chmod the watched dir unreadable → inotify can no longer track it) must
// be logged and swallowed, not fatal. Skipped when running as root —
// uid 0 bypasses file permissions, so the error is not reproducible (the
// sandbox and CI run as root; real user installs are covered).
func TestWatchChangesPermissionError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: file permissions do not apply, inotify EACCES not reproducible")
	}
	tmpDir := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- watchChanges(ctx, tmpDir, func() { calls.Add(1) })
	}()
	defer func() {
		_ = os.Chmod(tmpDir, 0o755)
		cancel()
		select {
		case err := <-done:
			if err != nil && err != context.Canceled {
				t.Fatalf("watchChanges returned %v after cancel, want context.Canceled", err)
			}
		case <-time.After(time.Second):
			t.Fatal("watchChanges did not stop after cancel")
		}
	}()

	time.Sleep(200 * time.Millisecond) // let the watcher finish NewWatcher/Add
	if err := os.Chmod(tmpDir, 0); err != nil {
		t.Fatalf("chmod failed: %v", err)
	}

	// The watcher must survive the permission error (the documented
	// resilience: "log the error but don't crash").
	select {
	case err := <-done:
		t.Fatalf("watchChanges exited %v after the permission error; it must keep running", err)
	case <-time.After(500 * time.Millisecond):
	}
}

// TestWatchToolsRemovesDeletedTool pins F-9: deleting a script must remove
// its tool from the registry (RemoveTools), leaving the rest intact.
func TestWatchToolsRemovesDeletedTool(t *testing.T) {
	tmpDir := t.TempDir()
	alphaPath := filepath.Join(tmpDir, "alpha.sh")
	betaPath := filepath.Join(tmpDir, "beta.sh")
	if err := os.WriteFile(alphaPath, []byte("#!/bin/bash\necho alpha\n"), 0o755); err != nil {
		t.Fatalf("failed to create alpha: %v", err)
	}
	if err := os.WriteFile(betaPath, []byte("#!/bin/bash\necho beta\n"), 0o755); err != nil {
		t.Fatalf("failed to create beta: %v", err)
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
	registry := newToolRegistry(server, "", defaultToolTimeout, 16)
	registry.replace([]discoveredTool{
		{Name: "alpha", Path: alphaPath, Description: "alpha", Params: []paramSpec{}},
		{Name: "beta", Path: betaPath, Description: "beta", Params: []paramSpec{}},
	})

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx := context.Background()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect failed: %v", err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect failed: %v", err)
	}
	defer clientSession.Close()

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- watchTools(watchCtx, tmpDir, registry, 20*time.Millisecond)
	}()

	// Bounded remove retries: a lost fsnotify event is recovered by the next
	// delete attempt (re-write + delete keeps the path registered for the
	// inotify watch), same pattern as the add/change tests.
	var names []string
	removed := false
	for i := 0; i < 10 && !removed; i++ {
		if err := os.Remove(betaPath); err != nil {
			// First attempt: the file may not exist yet for the watcher; recreate.
			if err := os.WriteFile(betaPath, []byte("#!/bin/bash\necho beta\n"), 0o755); err != nil {
				t.Fatalf("failed to recreate beta: %v", err)
			}
			if err := os.Remove(betaPath); err != nil {
				t.Fatalf("failed to delete beta: %v", err)
			}
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			res, err := clientSession.ListTools(ctx, nil)
			if err != nil {
				t.Fatalf("ListTools failed: %v", err)
			}
			if len(res.Tools) == 1 {
				removed = true
				break
			}
			// The registry swaps remove-then-add, so the live list is
			// transiently empty between the two; a 0-tool snapshot is a
			// valid in-flight state, not an error — keep polling.
			names = nil
			for _, tool := range res.Tools {
				names = append(names, tool.Name)
			}
			if time.Now().After(deadline) {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !removed {
		cancel()
		t.Fatalf("watchTools did not remove the deleted tool after 10 attempts: got %v", names)
	}

	// Let any in-flight rescan settle before the final check.
	time.Sleep(300 * time.Millisecond)
	res, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}
	var remaining []string
	for _, tool := range res.Tools {
		remaining = append(remaining, tool.Name)
	}
	if len(res.Tools) != 1 || res.Tools[0].Name != "alpha" {
		t.Fatalf("remaining tools = %v, want exactly [alpha]", remaining)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && err != context.Canceled {
			t.Fatalf("watchTools returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("watchTools did not stop after cancel")
	}
}

func TestResolveToolPaths(t *testing.T) {
	tmpDir := t.TempDir()

	dirAbs, scriptsAbs, err := resolveToolPaths(tmpDir, tmpDir)
	if err != nil {
		t.Fatalf("resolveToolPaths failed: %v", err)
	}
	if dirAbs != tmpDir || scriptsAbs != tmpDir {
		t.Errorf("resolveToolPaths = (%q, %q), want (%q, %q)", dirAbs, scriptsAbs, tmpDir, tmpDir)
	}

	if _, _, err := resolveToolPaths(tmpDir, filepath.Join(tmpDir, "no-such-scripts")); err == nil || !strings.Contains(err.Error(), "scripts path inaccessible") {
		t.Errorf("expected 'scripts path inaccessible' error, got %v", err)
	}
	if _, _, err := resolveToolPaths(filepath.Join(tmpDir, "no-such-dir"), tmpDir); err == nil || !strings.Contains(err.Error(), "dir path inaccessible") {
		t.Errorf("expected 'dir path inaccessible' error, got %v", err)
	}
}

func TestRunDiagnosticListTools(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("zero_tools_empty_stdout_exit_0", func(t *testing.T) {
		emptyDir := t.TempDir()
		var stdout, stderr bytes.Buffer
		code := runDiagnostic(&stdout, &stderr, emptyDir, emptyDir, true, false, "", false, "", 5*time.Minute, nil)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		if stdout.Len() != 0 {
			t.Errorf("stdout = %q, want empty", stdout.String())
		}
		if !strings.Contains(stderr.String(), "Warning: No executable scripts found in "+emptyDir) {
			t.Errorf("stderr = %q, want the no-scripts warning", stderr.String())
		}
	})

	t.Run("unreadable_scripts_dir_exit_1", func(t *testing.T) {
		missing := filepath.Join(tmpDir, "no-such-scripts")
		var stdout, stderr bytes.Buffer
		code := runDiagnostic(&stdout, &stderr, tmpDir, missing, true, false, "", false, "", 5*time.Minute, nil)
		if code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
		if stdout.Len() != 0 {
			t.Errorf("stdout = %q, want empty", stdout.String())
		}
		if !strings.Contains(stderr.String(), "Error:") || !strings.Contains(stderr.String(), "scripts path inaccessible") {
			t.Errorf("stderr = %q, want the path error", stderr.String())
		}
	})

	t.Run("prints_list_and_ignored_flags_notice", func(t *testing.T) {
		scriptPath := filepath.Join(tmpDir, "alpha.sh")
		if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\n# Description: alpha tool\necho alpha\n"), 0o755); err != nil {
			t.Fatalf("failed to create script: %v", err)
		}
		var stdout, stderr bytes.Buffer
		code := runDiagnostic(&stdout, &stderr, tmpDir, tmpDir, true, false, "", false, "", 5*time.Minute, []string{"--host", "--port"})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		if !strings.Contains(stdout.String(), "alpha()") || !strings.Contains(stdout.String(), "alpha tool (timeout: 5m0s)") {
			t.Errorf("stdout = %q, want the rendered tool", stdout.String())
		}
		wantNotice := "Note: ignoring server-mode flags in diagnostic mode: --host, --port"
		if !strings.Contains(stderr.String(), wantNotice) {
			t.Errorf("stderr = %q, want notice %q", stderr.String(), wantNotice)
		}
		if strings.Count(stderr.String(), "Note: ignoring server-mode flags") != 1 {
			t.Errorf("stderr = %q, want exactly one notice", stderr.String())
		}
	})

	t.Run("live_mode_reprints_on_change", func(t *testing.T) {
		scriptsDir := t.TempDir()
		scriptPath := filepath.Join(scriptsDir, "alpha.sh")
		if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\n# Description: alpha\necho alpha\n"), 0o755); err != nil {
			t.Fatalf("failed to create script: %v", err)
		}

		var clears atomic.Int32
		var cancel context.CancelFunc
		oldClear := clearScreen
		clearScreen = func(io.Writer) { clears.Add(1) }
		oldNotify := notifySignals
		notifySignals = func(ctx context.Context, sig ...os.Signal) (context.Context, context.CancelFunc) {
			c, c2 := context.WithCancel(ctx)
			cancel = c2
			return c, func() {}
		}
		defer func() {
			clearScreen = oldClear
			notifySignals = oldNotify
		}()

		var stdout, stderr bytes.Buffer
		done := make(chan int, 1)
		go func() {
			done <- runDiagnostic(&stdout, &stderr, scriptsDir, scriptsDir, true, true, "", false, "", 5*time.Minute, nil)
		}()

		// Initial print.
		deadline := time.Now().Add(2 * time.Second)
		for !strings.Contains(stdout.String(), "alpha (timeout: 5m0s)") && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if !strings.Contains(stdout.String(), "alpha (timeout: 5m0s)") {
			t.Fatalf("initial print missing: %q", stdout.String())
		}

		if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\n# Description: beta updated\necho beta\n"), 0o755); err != nil {
			t.Fatalf("failed to update script: %v", err)
		}

		deadline = time.Now().Add(2 * time.Second)
		for !strings.Contains(stdout.String(), "beta updated (timeout: 5m0s)") && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if !strings.Contains(stdout.String(), "beta updated (timeout: 5m0s)") {
			t.Fatalf("live re-print missing after change: %q", stdout.String())
		}
		// The re-print uses the re-queried width (non-TTY buffer → 160).
		if !strings.Contains(stdout.String(), "     beta updated (timeout: 5m0s)") {
			t.Errorf("re-print not rendered at the 160-rune fallback: %q", stdout.String())
		}

		cancel()
		select {
		case code := <-done:
			if code != 0 {
				t.Fatalf("live mode exit code = %d, want 0", code)
			}
		case <-time.After(time.Second):
			t.Fatal("live mode did not stop after cancel")
		}
		if clears.Load() == 0 {
			t.Error("injected clearScreen was not called before the re-print")
		}
	})

	t.Run("live_mode_stays_quiet_without_changes", func(t *testing.T) {
		scriptsDir := t.TempDir()
		scriptPath := filepath.Join(scriptsDir, "alpha.sh")
		if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\n# Description: alpha\necho alpha\n"), 0o755); err != nil {
			t.Fatalf("failed to create script: %v", err)
		}

		var clears atomic.Int32
		var cancel context.CancelFunc
		oldClear := clearScreen
		clearScreen = func(io.Writer) { clears.Add(1) }
		oldNotify := notifySignals
		notifySignals = func(ctx context.Context, sig ...os.Signal) (context.Context, context.CancelFunc) {
			c, c2 := context.WithCancel(ctx)
			cancel = c2
			return c, func() {}
		}
		defer func() {
			clearScreen = oldClear
			notifySignals = oldNotify
		}()

		var stdout, stderr bytes.Buffer
		done := make(chan int, 1)
		go func() {
			done <- runDiagnostic(&stdout, &stderr, scriptsDir, scriptsDir, true, true, "", false, "", 5*time.Minute, nil)
		}()

		// Initial print.
		deadline := time.Now().Add(2 * time.Second)
		for !strings.Contains(stdout.String(), "alpha (timeout: 5m0s)") && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if !strings.Contains(stdout.String(), "alpha (timeout: 5m0s)") {
			t.Fatalf("initial print missing: %q", stdout.String())
		}

		// Several debounce windows with zero events: the debounce timer must
		// not fire on its own (regression: it was armed at creation, causing
		// an unsolicited clear + full re-print ~100 ms after start).
		time.Sleep(500 * time.Millisecond)

		if n := strings.Count(stdout.String(), "alpha (timeout: 5m0s)"); n != 1 {
			t.Errorf("list printed %d times with no changes, want exactly 1: %q", n, stdout.String())
		}
		if n := clears.Load(); n != 0 {
			t.Errorf("clearScreen called %d times with no changes, want 0", n)
		}

		cancel()
		select {
		case code := <-done:
			if code != 0 {
				t.Fatalf("live mode exit code = %d, want 0", code)
			}
		case <-time.After(time.Second):
			t.Fatal("live mode did not stop after cancel")
		}
	})
}

func TestRunCallTool(t *testing.T) {
	tmpDir := t.TempDir()
	scriptsDir := filepath.Join(tmpDir, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatalf("failed to create scripts dir: %v", err)
	}
	// A single marker shared by the marker scripts: subtests run in order
	// and each clears it first, so a marker can only come from the run
	// under test.
	marker := filepath.Join(tmpDir, "marker")

	writeScript := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(scriptsDir, name), []byte(body), 0o755); err != nil {
			t.Fatalf("failed to create %s: %v", name, err)
		}
	}
	markerBody := "#!/bin/bash\necho it-ran >> " + marker + "\necho ran\n"
	writeScript("ok.sh", markerBody)
	writeScript("ok_param.sh", "#!/bin/bash\n# Param: path string required \"Input path\"\n"+markerBody)
	writeScript("fail.sh", "#!/bin/bash\necho boom\nexit 3\n")
	writeScript("sleep1.sh", "#!/bin/bash\n# Timeout: 1s\nexec sleep 5\n")
	writeScript("slow_none.sh", "#!/bin/bash\n# Timeout: NONE\nsleep 2\necho done-slow\n")
	writeScript("badinterp", "#!/nonexistent/interpreter\necho never\n")

	markerExists := func() bool {
		_, err := os.Stat(marker)
		return err == nil
	}
	run := func(name, params string, global time.Duration) (code int, err error, stdout string) {
		os.Remove(marker)
		var buf bytes.Buffer
		code, err = runCallTool(&buf, scriptsDir, tmpDir, global, name, params)
		stdout = buf.String()
		return code, err, stdout
	}

	t.Run("unknown_tool_code_1_lists_available_names", func(t *testing.T) {
		code, err, stdout := run("nope", "{}", 5*time.Minute)
		if code != 1 || err == nil {
			t.Fatalf("code = %d, err = %v, want code 1 and a non-nil error", code, err)
		}
		wantMsg := `unknown tool "nope"; available tools: badinterp, fail, ok, ok_param, sleep1, slow_none`
		if !strings.Contains(err.Error(), wantMsg) {
			t.Errorf("err = %q, want it to name the tool and list the available tools (%s)", err, wantMsg)
		}
		if stdout != "" {
			t.Errorf("stdout = %q, want empty (operational failures go to stderr)", stdout)
		}
	})

	t.Run("success_code_0_stdout_carries_output", func(t *testing.T) {
		code, err, stdout := run("ok", `{"x":"y"}`, 5*time.Minute)
		if err != nil {
			t.Fatalf("runCallTool returned error: %v", err)
		}
		if code != 0 {
			t.Fatalf("code = %d, want 0", code)
		}
		if !strings.Contains(stdout, "<stdout>") || !strings.Contains(stdout, "ran") || !strings.Contains(stdout, "</stdout>") {
			t.Errorf("stdout = %q, want the script output", stdout)
		}
		if !markerExists() {
			t.Error("marker missing: the script did not run")
		}
	})

	t.Run("nonzero_exit_code_1_output_printed", func(t *testing.T) {
		code, err, stdout := run("fail", "{}", 5*time.Minute)
		if err != nil {
			t.Fatalf("runCallTool returned error: %v", err)
		}
		if code != 1 {
			t.Fatalf("code = %d, want 1", code)
		}
		if !strings.Contains(stdout, "boom") {
			t.Errorf("stdout = %q, want the script output", stdout)
		}
	})

	t.Run("missing_required_param_not_executed", func(t *testing.T) {
		code, err, stdout := run("ok_param", "{}", 5*time.Minute)
		if err != nil {
			t.Fatalf("runCallTool returned error: %v", err)
		}
		if code != 1 {
			t.Fatalf("code = %d, want 1", code)
		}
		if !strings.Contains(stdout, "missing required parameter: path") {
			t.Errorf("stdout = %q, want the validation message", stdout)
		}
		if markerExists() {
			t.Error("script was executed despite the validation failure")
		}
	})

	t.Run("non_object_params_rejected_before_execution", func(t *testing.T) {
		for _, params := range []string{"42", `["x"]`, `"str"`, `"{\"a\":1}"`} {
			code, err, _ := run("ok", params, 5*time.Minute)
			if code != 1 || err == nil {
				t.Errorf("--params=%s: code = %d, err = %v, want code 1 and a non-nil error", params, code, err)
			}
			if markerExists() {
				t.Errorf("--params=%s: script was executed", params)
			}
		}
	})

	t.Run("empty_and_null_params_equivalent_to_empty_object", func(t *testing.T) {
		for _, params := range []string{"", "null", "{}"} {
			code, err, _ := run("ok", params, 5*time.Minute)
			if code != 0 || err != nil {
				t.Errorf("--params=%q: code = %d, err = %v, want success", params, code, err)
			}
		}
	})

	t.Run("per_tool_timeout_wins_over_global", func(t *testing.T) {
		// sleep1.sh declares `Timeout: 1s` in its frontmatter: the per-tool
		// value must win over the long global.
		start := time.Now()
		code, err, stdout := run("sleep1", "{}", 5*time.Minute)
		if elapsed := time.Since(start); elapsed > 3500*time.Millisecond {
			t.Errorf("timeout case took %v, want ~1s", elapsed)
		}
		if err != nil || code != 1 {
			t.Fatalf("sleep1: code = %d, err = %v, want timeout", code, err)
		}
		if !strings.Contains(stdout, "tool timed out after 1s") {
			t.Errorf("stdout = %q, want the timeout message", stdout)
		}

		// slow_none.sh declares `Timeout: NONE` under a short global: it must
		// run with no deadline and complete.
		start = time.Now()
		code, err, out := run("slow_none", "{}", time.Second)
		if elapsed := time.Since(start); elapsed < time.Second || elapsed > 4*time.Second {
			t.Errorf("NONE case took %v, want ~2s (no deadline)", elapsed)
		}
		if err != nil || code != 0 {
			t.Fatalf("slow_none: code = %d, err = %v, want success", code, err)
		}
		if !strings.Contains(out, "done-slow") {
			t.Errorf("stdout = %q, want the script output", out)
		}
	})

	t.Run("unstartable_script_hard_error", func(t *testing.T) {
		code, err, stdout := run("badinterp", "{}", 5*time.Minute)
		if code != 1 || err == nil {
			t.Fatalf("code = %d, err = %v, want code 1 and a non-nil error", code, err)
		}
		if stdout != "" {
			t.Errorf("stdout = %q, want empty (hard errors go to stderr)", stdout)
		}
	})
}

func TestRunDiagnosticCallTool(t *testing.T) {
	tmpDir := t.TempDir()
	scriptsDir := filepath.Join(tmpDir, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatalf("failed to create scripts dir: %v", err)
	}
	scriptPath := filepath.Join(scriptsDir, "ok.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\necho ran\n"), 0o755); err != nil {
		t.Fatalf("failed to create script: %v", err)
	}

	t.Run("explicitly_empty_call_tool_is_startup_error", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runDiagnostic(&stdout, &stderr, tmpDir, scriptsDir, false, false, "", true, "{}", 5*time.Minute, nil)
		if code != 1 {
			t.Fatalf("code = %d, want 1", code)
		}
		if stdout.Len() != 0 {
			t.Errorf("stdout = %q, want empty", stdout.String())
		}
		if !strings.Contains(stderr.String(), "--call-tool requires a non-empty tool name") {
			t.Errorf("stderr = %q, want the startup error", stderr.String())
		}
	})

	t.Run("runs_tool_and_prints_ignored_flags_notice", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runDiagnostic(&stdout, &stderr, tmpDir, scriptsDir, false, true, "ok", true, `{"x":"y"}`, 5*time.Minute, []string{"--host", "--watch"})
		if code != 0 {
			t.Fatalf("code = %d, want 0 (stderr: %s)", code, stderr.String())
		}
		captured := stdout.String()
		if !strings.Contains(captured, "ran") {
			t.Errorf("stdout = %q, want the script output", captured)
		}
		wantNotice := "Note: ignoring server-mode flags in diagnostic mode: --host, --watch"
		if strings.Count(stderr.String(), "Note: ignoring server-mode flags") != 1 || !strings.Contains(stderr.String(), wantNotice) {
			t.Errorf("stderr = %q, want exactly one notice %q", stderr.String(), wantNotice)
		}
	})

	t.Run("unreadable_scripts_dir_exit_1", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runDiagnostic(&stdout, &stderr, tmpDir, filepath.Join(tmpDir, "no-such"), false, false, "ok", true, "{}", 5*time.Minute, nil)
		if code != 1 {
			t.Fatalf("code = %d, want 1", code)
		}
		if !strings.Contains(stderr.String(), "scripts path inaccessible") {
			t.Errorf("stderr = %q, want the path error", stderr.String())
		}
	})
}

// --- Tier 1 hardening tests (REVIEW.md F-6 and the F-1/F-2/F-4 behaviors) ---

func TestBoundedWriter(t *testing.T) {
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
	// The 1 MiB branch of combineToolOutput was previously untested (F-6).
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

// assertGroupKillDelivered verifies that kill(−pid, SIGKILL) reaches the
// whole group in the current environment (spawn a script whose child inherits
// its process group, group-kill, expect both dead). It returns false when the
// environment silently swallows group signals — some sandboxes' seccomp
// profiles block kill(2) toward Go-exec'd children — in which case the
// caller must skip rather than fail.
func assertGroupKillDelivered(t *testing.T) bool {
	t.Helper()
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "grp.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\nsleep 60 &\nsleep 60\n"), 0o755); err != nil {
		t.Fatalf("failed to create script: %v", err)
	}
	cmd := exec.Command(scriptPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := killToolProcess(cmd); err != nil {
		t.Fatalf("group kill failed: %v", err)
	}
	defer cmd.Wait()
	deadline := time.Now().Add(2 * time.Second)
	for {
		alive := syscall.Kill(cmd.Process.Pid, 0) == nil
		if !alive {
			return true // direct child died → group signal was delivered
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestExecuteToolKillsProcessGroup pins F-7: the timeout must kill the whole
// process group, not only the direct child — a shell script's grandchildren
// (the canonical tool shape) must not outlive their budget.
func TestExecuteToolKillsProcessGroup(t *testing.T) {
	if !assertGroupKillDelivered(t) {
		t.Skip("this sandbox does not deliver process-group signals to Go-exec'd processes; group kill cannot be verified here")
	}
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "grandchild.pid")
	scriptPath := filepath.Join(tmpDir, "spawner.sh")
	script := "#!/bin/bash\nsleep 60 &\necho $! > " + pidFile + "\nsleep 30\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to create script: %v", err)
	}

	result, err := executeTool(context.Background(), scriptPath, map[string]any{}, 2*time.Second, tmpDir)
	if err != nil {
		t.Fatalf("executeTool returned unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected an IsError timeout result")
	}
	if _, ok := result.Content[0].(*mcp.TextContent); !ok {
		t.Fatalf("unexpected content type %T", result.Content[0])
	}
	text := result.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "timed out") {
		t.Fatalf("expected a timeout message, got: %q", text)
	}

	grandPidRaw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("script never wrote the grandchild pid: %v", err)
	}
	grandPid, err := strconv.Atoi(strings.TrimSpace(string(grandPidRaw)))
	if err != nil {
		t.Fatalf("unparseable pid %q: %v", grandPidRaw, err)
	}

	// The grandchild must be dead after executeTool returns.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := syscall.Kill(grandPid, 0); err == nil {
			if time.Now().After(deadline) {
				t.Fatalf("grandchild %d survived past the timeout kill", grandPid)
			}
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if !errors.Is(err, syscall.ESRCH) {
			t.Fatalf("kill(pid, 0) = %v, want ESRCH (process gone)", err)
		}
		break // gone
	}
}

func TestHTTPSecurityPolicy(t *testing.T) {
	tests := []struct {
		host        string
		apiKey      string
		acceptsRisk bool
		wantErr     bool
		wantWarning string // substring expected when a warning is returned
	}{
		{host: "127.0.0.1", apiKey: "", acceptsRisk: false, wantErr: false},
		{host: "127.0.0.2", apiKey: "", acceptsRisk: false, wantErr: false},
		{host: "::1", apiKey: "", acceptsRisk: false, wantErr: false},
		{host: "localhost", apiKey: "", acceptsRisk: false, wantErr: false},
		{host: "0.0.0.0", apiKey: "s3cret", acceptsRisk: false, wantErr: false},
		// The F-1 case: remote bind, no key, no escape hatch → refuse.
		{host: "0.0.0.0", apiKey: "", acceptsRisk: false, wantErr: true},
		{host: "192.168.1.10", apiKey: "", acceptsRisk: false, wantErr: true},
		{host: "10.0.0.5", apiKey: "", acceptsRisk: false, wantErr: true},
		// Unparseable host → treated as non-loopback (conservative).
		{host: "not-an-ip", apiKey: "", acceptsRisk: false, wantErr: true},
		// Escape hatch: starts, but loudly.
		{host: "0.0.0.0", apiKey: "", acceptsRisk: true, wantWarning: "UNAUTHENTICATED"},
	}

	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			warning, err := checkHTTPSecurityPolicy(tt.host, tt.apiKey, tt.acceptsRisk)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantErr {
				if !strings.Contains(err.Error(), "--insecure-no-auth") {
					t.Errorf("error should mention the escape hatch: %v", err)
				}
				if !strings.Contains(err.Error(), tt.host) {
					t.Errorf("error should name the host: %v", err)
				}
			}
			if tt.wantWarning != "" && !strings.Contains(warning, tt.wantWarning) {
				t.Fatalf("warning = %q, want it to contain %q", warning, tt.wantWarning)
			}
		})
	}
}

func TestExecSlot(t *testing.T) {
	s := newExecSlot(2)
	if !s.tryAcquire() || !s.tryAcquire() {
		t.Fatal("expected two acquisitions to succeed with limit 2")
	}
	if s.tryAcquire() {
		t.Fatal("expected the third acquisition to fail at capacity")
	}
	s.release()
	if !s.tryAcquire() {
		t.Fatal("expected an acquisition to succeed after release")
	}
	// Non-positive limits fall back to the default.
	if got := newExecSlot(0).limit; got != defaultMaxConcurrentTools {
		t.Fatalf("newExecSlot(0).limit = %d, want %d", got, defaultMaxConcurrentTools)
	}
}

func TestToolCapacityViaRegistry(t *testing.T) {
	tmpDir := t.TempDir()
	slowPath := filepath.Join(tmpDir, "slow.sh")
	if err := os.WriteFile(slowPath, []byte("#!/bin/bash\nsleep 3\n"), 0o755); err != nil {
		t.Fatalf("failed to create slow script: %v", err)
	}
	noTimeout := time.Duration(0)

	server := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
	registry := newToolRegistry(server, tmpDir, 30*time.Second, 1) // capacity 1
	registry.replace([]discoveredTool{
		{Name: "slow", Path: slowPath, Description: "slow tool", Timeout: &noTimeout},
	})
	handler := registry.lastHandler
	if handler == nil {
		t.Fatal("lastHandler is nil after replace")
	}

	makeReq := func() *mcp.CallToolRequest {
		return &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "slow"}}
	}

	// First call holds the only slot.
	firstDone := make(chan *mcp.CallToolResult, 1)
	go func() {
		res, err := handler(context.Background(), makeReq())
		if err != nil {
			t.Errorf("first call returned error: %v", err)
		}
		firstDone <- res
	}()

	// Wait for the first call to take the only slot: a bounded poll of the
	// slot itself (package-internal) instead of a fixed sleep, so the test
	// cannot flake on a slow scheduler.
	deadline := time.Now().Add(5 * time.Second)
	for registry.slot.tryAcquire() {
		registry.slot.release()
		if time.Now().After(deadline) {
			t.Fatal("first call never acquired the slot")
		}
		time.Sleep(5 * time.Millisecond)
	}

	res, err := handler(context.Background(), makeReq())
	if err != nil {
		t.Fatalf("second call returned an error: %v (want a clean at-capacity result)", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError at-capacity result, got %#v", res)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "at capacity") || !strings.Contains(text, "1 concurrent") {
		t.Fatalf("at-capacity text = %q, want it to name the limit", text)
	}

	// The first call must eventually finish (nothing was blocked or killed).
	select {
	case res1 := <-firstDone:
		if res1.IsError {
			t.Fatalf("first call failed: %v", res1.Content[0])
		}
	case <-time.After(10 * time.Second):
		t.Fatal("first call did not finish")
	}
}
