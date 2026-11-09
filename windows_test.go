//go:build windows

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestWindowsExeSmoke is the native leg of H5: it only compiles and runs on
// Windows, so the windows-latest CI job exercises the OS-aware discovery
// predicate (extension-based, no exec bits) and the real exec path against a
// genuine PE binary. The rest of the suite is Unix-oriented (exec bits and
// shebang execution), which is why the CI job runs this test by name.
func TestWindowsExeSmoke(t *testing.T) {
	dir := t.TempDir()
	scriptsDir := filepath.Join(dir, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Build a real executable named tool.exe.
	src := filepath.Join(dir, "tool.go")
	exe := filepath.Join(scriptsDir, "tool.exe")
	prog := "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"windows-smoke-ok\") }\n"
	if err := os.WriteFile(src, []byte(prog), 0o644); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", exe, src)
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build tool.exe: %v\n%s", err, out)
	}

	// A regular file with a batch extension must not be discovered
	// (CreateProcess would run it through cmd.exe and mangle arguments).
	bat := filepath.Join(scriptsDir, "run.bat")
	if err := os.WriteFile(bat, []byte("@echo off\r\necho no\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Discovery: the PE binary registers, the batch file does not.
	var logBuf bytes.Buffer
	tools, err := discoverTools(scriptsDir, testLogger(&logBuf))
	if err != nil {
		t.Fatalf("discoverTools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "tool" {
		t.Fatalf("discovered %d tools %v, want exactly [tool] (log: %s)", len(tools), toolNames(tools), logBuf.String())
	}

	// Invocation through the real exec path.
	res, err := executeTool(context.Background(), exe, map[string]any{}, 30*time.Second, dir)
	if err != nil {
		t.Fatalf("executeTool: %v", err)
	}
	if res == nil || len(res.Content) == 0 {
		t.Fatalf("empty result: %+v", res)
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("unexpected content type %T", res.Content[0])
	}
	if !strings.Contains(text.Text, "windows-smoke-ok") {
		t.Fatalf("unexpected output: %q", text.Text)
	}
}

func toolNames(tools []discoveredTool) []string {
	names := make([]string, len(tools))
	for i, tool := range tools {
		names[i] = tool.Name
	}
	return names
}
