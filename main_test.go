package main

import (
	"context"
	"encoding/json"
	"fmt"
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
	registry := newToolRegistry(server, "")
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
	registry := newToolRegistry(server, "")
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
		tool      string
		args      map[string]any
		params    []paramSpec
		wantErr   bool
		errSubstr string
	}{
		{
			name: "all_required_present",
			tool: "mytool",
			args: map[string]any{"path": "/tmp/file", "dpi": 150.0},
			params: []paramSpec{
				{Name: "path", Type: "string", Required: true},
				{Name: "dpi", Type: "number", Required: true},
			},
			wantErr: false,
		},
		{
			name: "one_missing_required",
			tool: "mytool",
			args: map[string]any{},
			params: []paramSpec{
				{Name: "path", Type: "string", Required: true},
			},
			wantErr:   true,
			errSubstr: "missing required parameter: path",
		},
		{
			name: "no_required_params",
			tool: "mytool",
			args: map[string]any{},
			params: []paramSpec{
				{Name: "verbose", Type: "boolean", Required: false},
			},
			wantErr: false,
		},
		{
			name:    "empty_params",
			tool:    "mytool",
			args:    map[string]any{},
			params:  []paramSpec{},
			wantErr: false,
		},
		{
			name: "optional_absent_no_error",
			tool: "mytool",
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
			err := validateRequiredParams(tt.tool, tt.args, tt.params)
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

func TestRequiredParamValidationViaRegistry(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "convert.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\necho done\n"), 0o755); err != nil {
		t.Fatalf("failed to create script: %v", err)
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
	registry := newToolRegistry(server, tmpDir)

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
