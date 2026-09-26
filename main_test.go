package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

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
	registry := newToolRegistry(server, "", defaultToolTimeout)
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
	if err := os.WriteFile(addedScriptPath, []byte("#!/bin/bash\necho beta\n"), 0o755); err != nil {
		t.Fatalf("failed to create second script: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		res, err := clientSession.ListTools(ctx, nil)
		if err != nil {
			t.Fatalf("ListTools failed: %v", err)
		}
		if len(res.Tools) == 2 {
			cancel()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("watchTools did not update before deadline: got %d tools", len(res.Tools))
		}
		time.Sleep(20 * time.Millisecond)
	}

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
	registry := newToolRegistry(server, "", defaultToolTimeout)
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

	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\n# Description: beta updated\necho beta now\n"), 0o755); err != nil {
		t.Fatalf("failed to update script: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		res, err := clientSession.ListTools(ctx, nil)
		if err != nil {
			t.Fatalf("ListTools failed: %v", err)
		}
		if len(res.Tools) == 1 && res.Tools[0].Description == "beta updated" {
			cancel()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("watchTools did not refresh updated tool description: %+v", res.Tools)
		}
		time.Sleep(20 * time.Millisecond)
	}

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
		name    string
		raw     string
		want    time.Duration
		wantErr bool
	}{
		{name: "minutes", raw: "5m", want: 5 * time.Minute},
		{name: "seconds", raw: "60s", want: 60 * time.Second},
		{name: "compound", raw: "1h 30m 5s", want: time.Hour + 30*time.Minute + 5*time.Second},
		{name: "compound_no_spaces", raw: "1h30m5s", want: time.Hour + 30*time.Minute + 5*time.Second},
		{name: "micro_us", raw: "250us", want: 250 * time.Microsecond},
		{name: "micro_µs", raw: "250µs", want: 250 * time.Microsecond},
		{name: "nanos", raw: "1000ns", want: time.Microsecond},
		{name: "duplicates_sum", raw: "5m 5m", want: 10 * time.Minute},
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseTimeoutDuration(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseTimeoutDuration(%q) = %v, want error", tt.raw, got)
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
		noTimeout   bool
		want        time.Duration
		wantErr     bool
	}{
		{name: "default_when_unset", timeoutFlag: "", want: defaultToolTimeout},
		{name: "flag_parsed", timeoutFlag: "5s", want: 5 * time.Second},
		{name: "flag_none", timeoutFlag: "NONE", want: 0},
		{name: "no_timeout_beats_flag", timeoutFlag: "5s", noTimeout: true, want: 0},
		{name: "no_timeout_alone", noTimeout: true, want: 0},
		{name: "invalid_flag_errors", timeoutFlag: "bogus", wantErr: true},
		{name: "no_timeout_wins_over_invalid_flag", timeoutFlag: "bogus", noTimeout: true, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveTimeout(tt.timeoutFlag, tt.noTimeout)
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

		params := extractParams(scriptPath)

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

		params := extractParams(scriptPath)

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

		params := extractParams(scriptPath)

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

		params := extractParams(scriptPath)

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

		params := extractParams(scriptPath)

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

		params := extractParams(scriptPath)

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

		params := extractParams(scriptPath)

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

		params := extractParams(scriptPath)

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
		name string
		flag string
		env  string
		want string
	}{
		{name: "flag_only", flag: "from-flag", env: "", want: "from-flag"},
		{name: "env_only", flag: "", env: "from-env", want: "from-env"},
		{name: "both_set_flag_wins", flag: "from-flag", env: "from-env", want: "from-flag"},
		{name: "neither_set", flag: "", env: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// t.Setenv to "" counts as empty for resolveAPIKey.
			t.Setenv(apiKeyEnvVar, tt.env)

			if got := resolveAPIKey(tt.flag); got != tt.want {
				t.Errorf("resolveAPIKey(%q) = %q, want %q", tt.flag, got, tt.want)
			}
		})
	}
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

func TestRequiredParamValidationViaRegistry(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "convert.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\necho done\n"), 0o755); err != nil {
		t.Fatalf("failed to create script: %v", err)
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
	registry := newToolRegistry(server, tmpDir, defaultToolTimeout)

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
