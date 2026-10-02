package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
