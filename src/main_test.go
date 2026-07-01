package main

import (
	"context"
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
}

func TestSnapshotScriptsDirReturnsErrorOnStatFailure(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("requires non-root")
	}

	// Root can bypass the permission failure this regression test relies on,
	// so skip it when running as uid 0.
	tmpDir := t.TempDir()
	secretDir := filepath.Join(tmpDir, "secret")
	if err := os.Mkdir(secretDir, 0o755); err != nil {
		t.Fatalf("failed to create secret directory: %v", err)
	}

	targetPath := filepath.Join(secretDir, "script.sh")
	if err := os.WriteFile(targetPath, []byte("#!/bin/bash\necho hidden\n"), 0o755); err != nil {
		t.Fatalf("failed to create target script: %v", err)
	}
	if err := os.Chmod(secretDir, 0o000); err != nil {
		t.Fatalf("failed to remove permissions from secret directory: %v", err)
	}
	defer os.Chmod(secretDir, 0o755)

	linkPath := filepath.Join(tmpDir, "script-link.sh")
	if err := os.Symlink(targetPath, linkPath); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}

	if _, err := snapshotScriptsDir(tmpDir); err == nil {
		t.Fatal("expected snapshotScriptsDir to fail when os.Stat cannot access an entry")
	}
}

func TestSnapshotScriptsDirDetectsContentChanges(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "alpha.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\n# Description: alpha\necho alpha\n"), 0o755); err != nil {
		t.Fatalf("failed to create script: %v", err)
	}

	before, err := snapshotScriptsDir(tmpDir)
	if err != nil {
		t.Fatalf("snapshotScriptsDir failed: %v", err)
	}

	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\n# Description: beta\necho beta\n"), 0o755); err != nil {
		t.Fatalf("failed to update script: %v", err)
	}

	after, err := snapshotScriptsDir(tmpDir)
	if err != nil {
		t.Fatalf("snapshotScriptsDir failed after update: %v", err)
	}

	if before == after {
		t.Fatal("expected snapshotScriptsDir to change when file content changes")
	}
}

func TestWatchTools(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "alpha.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\necho alpha\n"), 0o755); err != nil {
		t.Fatalf("failed to create script: %v", err)
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
	registry := newToolRegistry(server, "")
	registry.replace([]discoveredTool{{Name: "alpha", Path: scriptPath, Description: "alpha"}})

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

	before, err := snapshotScriptsDir(tmpDir)
	if err != nil {
		t.Fatalf("snapshotScriptsDir failed: %v", err)
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
	registry := newToolRegistry(server, "")
	registry.replace([]discoveredTool{{Name: "alpha", Path: scriptPath, Description: "alpha"}})

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

	after, err := snapshotScriptsDir(tmpDir)
	if err != nil {
		t.Fatalf("snapshotScriptsDir failed after update: %v", err)
	}
	if before == after {
		t.Fatal("expected snapshotScriptsDir to change when file content changes")
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

	result, err := executeTool(context.Background(), scriptPath, mustJSONMarshal(map[string]any{"1flag": "value"}), 5*time.Second, scriptDir)
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
	result, err := executeTool(ctx, scriptPath, nil, 5*time.Second, workDir)
	if err != nil {
		t.Fatalf("executeTool failed: %v", err)
	}

	if result.IsError {
		t.Fatalf("executeTool returned an error: %s", result.Content[0])
	}

	// Extract the working directory from the script's output
	outputPath := strings.TrimSpace(result.Content[0].(*mcp.TextContent).Text)

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
