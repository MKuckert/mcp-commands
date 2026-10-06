package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

func TestRunDiagnosticListTools(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	t.Run("zero_tools_empty_stdout_exit_0", func(t *testing.T) {
		emptyDir := t.TempDir()
		var stdout, stderr bytes.Buffer
		code := runDiagnostic(liveEnvFor(t, &stdout, &stderr), diagnostic{dir: emptyDir, scriptsDir: emptyDir, listTools: true, timeout: defaultToolTimeout})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		if stdout.Len() != 0 {
			t.Errorf("stdout = %q, want empty", stdout.String())
		}
		if !strings.Contains(stderr.String(), "WARN@") ||
			!strings.Contains(stderr.String(), "No executable scripts found") ||
			!strings.Contains(stderr.String(), "scriptsDir="+emptyDir) {
			t.Errorf("stderr = %q, want the no-scripts warning", stderr.String())
		}
	})

	t.Run("unreadable_scripts_dir_exit_1", func(t *testing.T) {
		missing := filepath.Join(tmpDir, "no-such-scripts")
		var stdout, stderr bytes.Buffer
		code := runDiagnostic(liveEnvFor(t, &stdout, &stderr), diagnostic{dir: tmpDir, scriptsDir: missing, listTools: true, timeout: defaultToolTimeout})
		if code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
		if stdout.Len() != 0 {
			t.Errorf("stdout = %q, want empty", stdout.String())
		}
		if !strings.Contains(stderr.String(), "ERROR@") || !strings.Contains(stderr.String(), "scripts path inaccessible") {
			t.Errorf("stderr = %q, want the path error", stderr.String())
		}
	})

	t.Run("prints_list_and_ignored_flags_notice", func(t *testing.T) {
		scriptPath := filepath.Join(tmpDir, "alpha.sh")
		writeScript(t, scriptPath, "#!/bin/bash\n# Description: alpha tool\necho alpha\n")
		var stdout, stderr bytes.Buffer
		code := runDiagnostic(liveEnvFor(t, &stdout, &stderr), diagnostic{dir: tmpDir, scriptsDir: tmpDir, listTools: true, timeout: defaultToolTimeout, ignoredFlags: []string{"--host", "--port"}})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		if !strings.Contains(stdout.String(), "alpha()") || !strings.Contains(stdout.String(), "alpha tool (timeout: 5m0s)") {
			t.Errorf("stdout = %q, want the rendered tool", stdout.String())
		}
		wantNotice := "ignoring server-mode flags in diagnostic mode"
		if !strings.Contains(stderr.String(), wantNotice) || !strings.Contains(stderr.String(), "--host, --port") {
			t.Errorf("stderr = %q, want notice %q", stderr.String(), wantNotice)
		}
		if strings.Count(stderr.String(), "ignoring server-mode flags in diagnostic mode") != 1 {
			t.Errorf("stderr = %q, want exactly one notice", stderr.String())
		}
	})

	t.Run("live_mode_reprints_on_change", func(t *testing.T) {
		scriptsDir := t.TempDir()
		scriptPath := filepath.Join(scriptsDir, "alpha.sh")
		writeScript(t, scriptPath, "#!/bin/bash\n# Description: alpha\necho alpha\n")

		var clears atomic.Int32
		var cancel context.CancelFunc

		var stdout, stderr bytes.Buffer
		env := liveEnvFor(t, &stdout, &stderr)
		env.clearScreen = func(io.Writer) { clears.Add(1) }
		env.notifySignals = func(ctx context.Context, sig ...os.Signal) (context.Context, context.CancelFunc) {
			c, c2 := context.WithCancel(ctx)
			cancel = c2
			return c, func() {}
		}
		done := make(chan int, 1)
		go func() {
			done <- runDiagnostic(env, diagnostic{dir: scriptsDir, scriptsDir: scriptsDir, listTools: true, watch: true, timeout: defaultToolTimeout})
		}()

		// Initial print.
		deadline := time.Now().Add(2 * time.Second)
		for !strings.Contains(stdout.String(), "alpha (timeout: 5m0s)") && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if !strings.Contains(stdout.String(), "alpha (timeout: 5m0s)") {
			t.Fatalf("initial print missing: %q", stdout.String())
		}

		writeScript(t, scriptPath, "#!/bin/bash\n# Description: beta updated\necho beta\n")

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
		writeScript(t, scriptPath, "#!/bin/bash\n# Description: alpha\necho alpha\n")

		var clears atomic.Int32
		var cancel context.CancelFunc

		var stdout, stderr bytes.Buffer
		env := liveEnvFor(t, &stdout, &stderr)
		env.clearScreen = func(io.Writer) { clears.Add(1) }
		env.notifySignals = func(ctx context.Context, sig ...os.Signal) (context.Context, context.CancelFunc) {
			c, c2 := context.WithCancel(ctx)
			cancel = c2
			return c, func() {}
		}
		done := make(chan int, 1)
		go func() {
			done <- runDiagnostic(env, diagnostic{dir: scriptsDir, scriptsDir: scriptsDir, listTools: true, watch: true, timeout: defaultToolTimeout})
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

	t.Run("watch_setup_failure_exit_1", func(t *testing.T) {
		scriptsDir := t.TempDir()
		scriptPath := filepath.Join(scriptsDir, "alpha.sh")
		writeScript(t, scriptPath, "#!/bin/bash\n# Description: alpha\necho alpha\n")

		var stdout, stderr bytes.Buffer
		env := liveEnvFor(t, &stdout, &stderr)
		env.newWatcher = func() (*fsnotify.Watcher, error) {
			return nil, errors.New("inotify unavailable")
		}

		code := runDiagnostic(env, diagnostic{dir: scriptsDir, scriptsDir: scriptsDir, listTools: true, watch: true, timeout: defaultToolTimeout})
		if code != 1 {
			t.Fatalf("exit code = %d, want 1 for a watch setup failure", code)
		}
		if !strings.Contains(stderr.String(), "ERROR@") || !strings.Contains(stderr.String(), "watch loop stopped") {
			t.Errorf("stderr = %q, want the visible watch failure", stderr.String())
		}
	})
}

func TestRunCallTool(t *testing.T) {
	t.Parallel()
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
		var buf, logBuf bytes.Buffer
		code, err = runCallTool(liveEnvFor(t, &buf, &logBuf), scriptsDir, tmpDir, global, name, params)
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
		code, err, stdout := run("ok", `{}`, 5*time.Minute)
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
		if !strings.Contains(stdout, "path") {
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

func TestDiagnosticInputContract(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	writeScript(t, filepath.Join(dir, "empty.sh"), "#!/bin/bash\necho ran >> "+marker+"\n")
	writeScript(t, filepath.Join(dir, "optional.sh"), "#!/bin/bash\n# Param: flag string required \"old\"\n# Param: flag boolean optional \"new\"\necho ran >> "+marker+"\n")
	writeScript(t, filepath.Join(dir, "required.sh"), "#!/bin/bash\n# Param: flag boolean optional \"old\"\n# Param: flag string required \"new\"\necho ran >> "+marker+"\n")
	writeScript(t, filepath.Join(dir, "typed.sh"), "#!/bin/bash\n# Param: path string required \"path\"\n# Param: num number optional \"number\"\n# Param: enabled boolean required \"enabled\"\necho ran >> "+marker+"\nprintf '%s\\n' \"$@\"\n")
	for _, tc := range []struct {
		name, tool, raw, message string
		valid                    bool
	}{
		{"zero_param_undeclared", "empty", `{"admin":true}`, "admin", false},
		{"undeclared", "typed", `{"path":"ok","enabled":false,"admin":true}`, "admin", false},
		{"wrong_scalar", "typed", `{"path":42,"enabled":false}`, "path", false},
		{"wrong_array", "typed", `{"path":[],"enabled":false}`, "path", false},
		{"wrong_object", "typed", `{"path":{},"enabled":false}`, "path", false},
		{"required_null", "typed", `{"path":null,"enabled":false}`, "path", false},
		{"missing_boolean", "typed", `{"path":"ok"}`, "enabled", false},
		{"last_required", "required", `{}`, "flag", false},
		{"zero_param_empty", "empty", `{}`, "", true},
		{"optional_last_wins", "optional", `{}`, "", true},
		{"required_false_and_precise_number", "typed", `{"path":"ok","enabled":false,"num":9007199254740993}`, "9007199254740993", true},
		{"exponent", "typed", `{"path":"ok","enabled":false,"num":1.25e+20}`, "1.25e+20", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_ = os.Remove(marker)
			var stdout, stderr bytes.Buffer
			code, err := runCallTool(liveEnvFor(t, &stdout, &stderr), dir, dir, defaultToolTimeout, tc.tool, tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			if tc.valid != (code == 0) {
				t.Fatalf("code=%d output=%q warnings=%q", code, stdout.String(), stderr.String())
			}
			if !strings.Contains(stdout.String(), tc.message) {
				t.Errorf("output %q missing %q", stdout.String(), tc.message)
			}
			_, statErr := os.Stat(marker)
			if tc.valid && statErr != nil {
				t.Errorf("valid call never ran: %v", statErr)
			}
			if !tc.valid && !os.IsNotExist(statErr) {
				t.Errorf("invalid call ran: %v", statErr)
			}
			if tc.tool == "typed" && tc.valid && strings.Contains(stdout.String(), "--enabled") {
				t.Errorf("false emitted flag: %q", stdout.String())
			}
		})
	}
}

func TestRunDiagnosticCallTool(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	scriptsDir := filepath.Join(tmpDir, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatalf("failed to create scripts dir: %v", err)
	}
	scriptPath := filepath.Join(scriptsDir, "ok.sh")
	writeScript(t, scriptPath, "#!/bin/bash\necho ran\n")

	t.Run("explicitly_empty_call_tool_is_startup_error", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runDiagnostic(liveEnvFor(t, &stdout, &stderr), diagnostic{dir: tmpDir, scriptsDir: scriptsDir, callToolSet: true, params: "{}", timeout: defaultToolTimeout})
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
		code := runDiagnostic(liveEnvFor(t, &stdout, &stderr), diagnostic{dir: tmpDir, scriptsDir: scriptsDir, watch: true, callTool: "ok", callToolSet: true, params: `{}`, timeout: defaultToolTimeout, ignoredFlags: []string{"--host", "--watch"}})
		if code != 0 {
			t.Fatalf("code = %d, want 0 (stderr: %s)", code, stderr.String())
		}
		captured := stdout.String()
		if !strings.Contains(captured, "ran") {
			t.Errorf("stdout = %q, want the script output", captured)
		}
		wantNotice := "ignoring server-mode flags in diagnostic mode"
		if strings.Count(stderr.String(), "ignoring server-mode flags in diagnostic mode") != 1 || !strings.Contains(stderr.String(), wantNotice) || !strings.Contains(stderr.String(), "--host, --watch") {
			t.Errorf("stderr = %q, want exactly one notice %q", stderr.String(), wantNotice)
		}
	})

	t.Run("unreadable_scripts_dir_exit_1", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runDiagnostic(liveEnvFor(t, &stdout, &stderr), diagnostic{dir: tmpDir, scriptsDir: filepath.Join(tmpDir, "no-such"), callTool: "ok", callToolSet: true, params: "{}", timeout: defaultToolTimeout})
		if code != 1 {
			t.Fatalf("code = %d, want 1", code)
		}
		if !strings.Contains(stderr.String(), "scripts path inaccessible") {
			t.Errorf("stderr = %q, want the path error", stderr.String())
		}
	})
}
