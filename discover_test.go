package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDiscoverTools(t *testing.T) {
	t.Parallel()
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

		tools, err := discoverTools(tmpDir, testDiscardLogger)
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

		tools, err := discoverTools(tmpDir, testDiscardLogger)
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

		tools, err := discoverTools(tmpDir, testDiscardLogger)
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

		tools, err := discoverTools(tmpDir, testDiscardLogger)
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

		tools, err := discoverTools(tmpDir, testDiscardLogger)
		if err != nil {
			t.Fatalf("discoverTools failed: %v", err)
		}

		if len(tools) != 0 {
			t.Errorf("Expected 0 tools, got %d", len(tools))
		}
	})

	t.Run("handles_nonexistent_directory", func(t *testing.T) {
		nonExistent := filepath.Join(t.TempDir(), "does_not_exist")
		_, err := discoverTools(nonExistent, testDiscardLogger)
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

		tools, err := discoverTools(tmpDir, testDiscardLogger)
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

		tools, err := discoverTools(tmpDir, testDiscardLogger)
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
	t.Parallel()
	t.Run("absent", func(t *testing.T) {
		tmpDir := t.TempDir()
		path := writeTimeoutScript(t, tmpDir, "#!/bin/bash\n# Description: no timeout\necho hi\n")

		_, _, timeout := extractFrontmatter(path, testDiscardLogger)
		if timeout != nil {
			t.Errorf("expected nil timeout for absent Timeout:, got %v", *timeout)
		}
	})

	t.Run("valid_30s", func(t *testing.T) {
		tmpDir := t.TempDir()
		path := writeTimeoutScript(t, tmpDir, "#!/bin/bash\n# Timeout: 30s\necho hi\n")

		_, _, timeout := extractFrontmatter(path, testDiscardLogger)
		if timeout == nil || *timeout != 30*time.Second {
			t.Errorf("expected pointer to 30s, got %#v", timeout)
		}
	})

	t.Run("none", func(t *testing.T) {
		tmpDir := t.TempDir()
		path := writeTimeoutScript(t, tmpDir, "#!/bin/bash\n# Timeout: NONE\necho hi\n")

		_, _, timeout := extractFrontmatter(path, testDiscardLogger)
		if timeout == nil || *timeout != 0 {
			t.Errorf("expected pointer to 0 for NONE, got %#v", timeout)
		}
	})

	t.Run("zero_seconds", func(t *testing.T) {
		tmpDir := t.TempDir()
		path := writeTimeoutScript(t, tmpDir, "#!/bin/bash\n# Timeout: 0s\necho hi\n")

		_, _, timeout := extractFrontmatter(path, testDiscardLogger)
		if timeout == nil || *timeout != 0 {
			t.Errorf("expected pointer to 0 for 0s, got %#v", timeout)
		}
	})

	t.Run("invalid_falls_back", func(t *testing.T) {
		tmpDir := t.TempDir()
		path := writeTimeoutScript(t, tmpDir, "#!/bin/bash\n# Timeout: bogus\necho hi\n")

		// Capture the stderr warning emitted for the invalid value.
		var buf bytes.Buffer
		_, _, timeout := extractFrontmatter(path, testLogger(&buf))

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

		// Suppress the warning (discard logger); assert scan continuity.
		desc, params, timeout := extractFrontmatter(path, testDiscardLogger)

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

		_, _, timeout := extractFrontmatter(path, testDiscardLogger)
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

		_, _, timeout := extractFrontmatter(path, testDiscardLogger)
		if timeout != nil {
			t.Errorf("expected nil timeout for line beyond scan window, got %v", *timeout)
		}
	})
}

func TestDiscoverToolsExtractsTimeout(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	writeTimeoutScript(t, tmpDir, "#!/bin/bash\n# Description: sleeper\n# Timeout: 1m\necho hi\n")

	tools, err := discoverTools(tmpDir, testDiscardLogger)
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
	t.Parallel()
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

		_, params, _ := extractFrontmatter(scriptPath, testDiscardLogger)

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

		_, params, _ := extractFrontmatter(scriptPath, testDiscardLogger)

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

		_, params, _ := extractFrontmatter(scriptPath, testDiscardLogger)

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

		_, params, _ := extractFrontmatter(scriptPath, testDiscardLogger)

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

		_, params, _ := extractFrontmatter(scriptPath, testDiscardLogger)

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

		_, params, _ := extractFrontmatter(scriptPath, testDiscardLogger)

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

		_, params, _ := extractFrontmatter(scriptPath, testDiscardLogger)

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

		_, params, _ := extractFrontmatter(scriptPath, testDiscardLogger)

		if len(params) != 1 {
			t.Errorf("Expected 1 param (beyond window ignored), got %d", len(params))
		}
		if len(params) > 0 && params[0].Name != "valid" {
			t.Errorf("Expected param name 'valid', got %q", params[0].Name)
		}
	})
}

// createFIFO stages an executable FIFO at path, skipping when the
// platform has no mkfifo.
func createFIFO(t *testing.T, path string) {
	t.Helper()
	if out, err := exec.Command("mkfifo", path).CombinedOutput(); err != nil {
		t.Skipf("mkfifo unavailable: %v: %s", err, out)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("failed to chmod fifo %s: %v", path, err)
	}
}

type discoverResult struct {
	tools []discoveredTool
	err   error
}

// discoverToolsBounded runs discoverTools in a goroutine and fails the test
// if it does not return within the bound — the in-process equivalent of the
// process-deadline proof for the FIFO hang.
func discoverToolsBounded(t *testing.T, dir string) discoverResult {
	t.Helper()
	resCh := make(chan discoverResult, 1)
	go func() {
		tools, err := discoverTools(dir, testDiscardLogger)
		resCh <- discoverResult{tools, err}
	}()
	select {
	case res := <-resCh:
		return res
	case <-time.After(10 * time.Second):
		t.Fatal("discoverTools did not return within 10 s — discovery is blocked on a non-regular file")
		return discoverResult{}
	}
}

// TestDiscoverSymlinkToDirectory: a symlink whose target is a directory
// passes the entry-level IsDir check (it does not follow links) and
// directories carry execute bits, so it must be rejected on its resolved
// target — discovery registers regular files only.
func TestDiscoverSymlinkToDirectory(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	toolDir := filepath.Join(tmpDir, "adir")
	if err := os.Mkdir(toolDir, 0o755); err != nil {
		t.Fatalf("failed to create directory: %v", err)
	}
	writeScript(t, filepath.Join(tmpDir, "real.sh"), "#!/bin/bash\necho real\n")
	if err := os.Symlink(toolDir, filepath.Join(tmpDir, "adir_link.sh")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	tools, err := discoverTools(tmpDir, testDiscardLogger)
	if err != nil {
		t.Fatalf("discoverTools failed: %v", err)
	}
	var names []string
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	if len(tools) != 1 || tools[0].Name != "real" {
		t.Fatalf("discovered %v, want [real] only — the symlink-to-directory must not register", names)
	}
}

// TestDiscoverSkipsFIFO: an executable FIFO used to pass the exec-bit check
// and os.Open blocked forever (REVIEW H1). The regular-file predicate must
// reject it before any open; the deadline bounds the whole test so a
// regression fails fast instead of hanging.
func TestDiscoverSkipsFIFO(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	writeScript(t, filepath.Join(tmpDir, "real.sh"), "#!/bin/bash\necho real\n")
	createFIFO(t, filepath.Join(tmpDir, "fifo.sh"))

	res := discoverToolsBounded(t, tmpDir)
	if res.err != nil {
		t.Fatalf("discoverTools failed: %v", res.err)
	}
	if len(res.tools) != 1 || res.tools[0].Name != "real" {
		t.Fatalf("discovered %d tools, want [real] only — the executable FIFO must not register", len(res.tools))
	}
}

// TestDiscoverSkipsSymlinkToFIFO: EvalSymlinks resolves the link to the
// FIFO, so the same regular-file predicate applies to the target.
func TestDiscoverSkipsSymlinkToFIFO(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	writeScript(t, filepath.Join(tmpDir, "real.sh"), "#!/bin/bash\necho real\n")
	fifoPath := filepath.Join(tmpDir, "outside_fifo")
	createFIFO(t, fifoPath)
	if err := os.Symlink(fifoPath, filepath.Join(tmpDir, "fifo_link.sh")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	res := discoverToolsBounded(t, tmpDir)
	if res.err != nil {
		t.Fatalf("discoverTools failed: %v", res.err)
	}
	if len(res.tools) != 1 || res.tools[0].Name != "real" {
		t.Fatalf("discovered %d tools, want [real] only — the symlink-to-FIFO must not register", len(res.tools))
	}
}

// TestIsToolFile: the discovery predicate itself.
func TestIsToolFile(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("asserts Unix permission-bit semantics")
	}
	tmpDir := t.TempDir()
	execPath := filepath.Join(tmpDir, "exec")
	writeScript(t, execPath, "#!/bin/bash\necho hi\n")
	execInfo, err := os.Stat(execPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !isToolFile(execInfo) {
		t.Error("executable regular file must be a tool file")
	}

	nonExecPath := filepath.Join(tmpDir, "noexec")
	if err := os.WriteFile(nonExecPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	nonExecInfo, err := os.Stat(nonExecPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if isToolFile(nonExecInfo) {
		t.Error("non-executable regular file must not be a tool file")
	}

	fifoPath := filepath.Join(tmpDir, "fifo")
	createFIFO(t, fifoPath)
	fifoInfo, err := os.Stat(fifoPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if isToolFile(fifoInfo) {
		t.Error("FIFO must not be a tool file")
	}

	subDir := filepath.Join(tmpDir, "adir")
	if err := os.Mkdir(subDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dirInfo, err := os.Stat(subDir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if isToolFile(dirInfo) {
		t.Error("directory must not be a tool file")
	}
}

// TestIsWindowsExecutable: the Windows half of the predicate, testable on
// any host because it depends only on the name. Batch files must be excluded
// (no cmd.exe wrapper in the exec path); everything else negative.
func TestIsWindowsExecutable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		want bool
	}{
		{"tool.exe", true},
		{"tool.EXE", true},
		{"tool.com", true},
		{"tool.bat", false},
		{"tool.cmd", false},
		{"tool.ps1", false},
		{"tool.js", false},
		{"tool.py", false},
		{"noext", false},
	}
	for _, tc := range cases {
		if got := isWindowsExecutable(tc.name); got != tc.want {
			t.Errorf("isWindowsExecutable(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestExtractFrontmatterOversizedLine: a frontmatter line beyond the
// scanner's 64 KiB buffer aborts the scan; the incompleteness must be
// disclosed (REVIEW M3), and the metadata read before the abort survives.
func TestExtractFrontmatterOversizedLine(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	content := "#!/bin/bash\n# Description: captured\n" + strings.Repeat("x", 70000) + "\n# Param: after string required \"after the oversized line\"\n"
	path := filepath.Join(tmpDir, "big.sh")
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}

	var buf bytes.Buffer
	desc, params, _ := extractFrontmatter(path, testLogger(&buf))

	if desc != "captured" {
		t.Errorf("description read before the oversized line lost, got %q", desc)
	}
	if len(params) != 0 {
		t.Errorf("metadata after the aborted scan must not be read, got %d params", len(params))
	}
	if !strings.Contains(buf.String(), "incomplete") {
		t.Errorf("warning %q does not disclose the incomplete frontmatter", buf.String())
	}
	if !strings.Contains(buf.String(), filepath.Base(path)) {
		t.Errorf("warning %q does not name the file", buf.String())
	}
}

// TestExtractFrontmatterScanWindowBoundary: line 30 is inside the advertised
// window, line 31 is not, with no slack between the two.
func TestExtractFrontmatterScanWindowBoundary(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	lines := []string{"#!/bin/bash"}
	for i := 0; i < 28; i++ {
		lines = append(lines, "# pad")
	}
	lines = append(lines, "# Param: inside string required \"on line 30\"")
	lines = append(lines, "# Param: outside string required \"on line 31\"")
	path := filepath.Join(tmpDir, "boundary.sh")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, params, _ := extractFrontmatter(path, testDiscardLogger)

	if len(params) != 1 {
		t.Fatalf("expected 1 param, got %d: %#v", len(params), params)
	}
	if params[0].Name != "inside" {
		t.Errorf("expected the line-30 param, got %q", params[0].Name)
	}
}
