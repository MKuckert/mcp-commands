package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
				if c.server.host != "127.0.0.1" {
					t.Errorf("host = %q, want default 127.0.0.1", c.server.host)
				}
				if c.server.port != 0 {
					t.Errorf("port = %d, want 0 (stdio)", c.server.port)
				}
				if c.server.timeout != defaultToolTimeout {
					t.Errorf("timeout = %v, want default", c.server.timeout)
				}
				if c.server.maxConcurrent != defaultMaxConcurrentTools {
					t.Errorf("maxConcurrent = %d", c.server.maxConcurrent)
				}
				if c.server.apiKey.Token != "" {
					t.Errorf("apiKey = %q, want empty", c.server.apiKey)
				}
			},
		},
		{
			name:     "server_mode_http_full",
			args:     []string{"--dir", "d", "--scripts", "s", "--host", "0.0.0.0", "--port", "9090", "--api-key", "tk", "--timeout", "1h", "--max-concurrent", "4", "--watch", "--insecure-no-auth"},
			wantErr:  "",
			wantMode: modeServer,
			check: func(t *testing.T, c cliConfig) {
				if c.server.host != "0.0.0.0" || c.server.port != 9090 {
					t.Errorf("host/port = %q/%d", c.server.host, c.server.port)
				}
				if c.server.apiKey.Token != "tk" || c.server.apiKey.Source != apiKeySourceFlag {
					t.Errorf("apiKey = %v, want tk/flag", c.server.apiKey)
				}
				if c.server.timeout != time.Hour {
					t.Errorf("timeout = %v", c.server.timeout)
				}
				if c.server.maxConcurrent != 4 {
					t.Errorf("maxConcurrent = %d", c.server.maxConcurrent)
				}
				if !c.server.watch || !c.server.insecureNoAuth {
					t.Errorf("watch/insecure = %v/%v", c.server.watch, c.server.insecureNoAuth)
				}
			},
		},
		{
			name:     "api_key_file_resolved",
			args:     []string{"--dir", "d", "--scripts", "s", "--port", "8080", "--api-key-file", keyFile},
			wantMode: modeServer,
			check: func(t *testing.T, c cliConfig) {
				if c.server.apiKey.Token != "filetoken" || c.server.apiKey.Source != apiKeySourceFile {
					t.Errorf("apiKey = %v, want filetoken/file", c.server.apiKey)
				}
			},
		},
		{
			name:    "api_key_flag_wins_over_file",
			args:    []string{"--dir", "d", "--scripts", "s", "--port", "8080", "--api-key", "flag", "--api-key-file", keyFile},
			wantErr: "",
			check: func(t *testing.T, c cliConfig) {
				if c.server.apiKey.Token != "flag" || c.server.apiKey.Source != apiKeySourceFlag {
					t.Errorf("apiKey = %v, want flag/flag", c.server.apiKey)
				}
			},
		},
		{
			name:    "api_key_file_missing_fails",
			args:    []string{"--dir", "d", "--scripts", "s", "--port", "8080", "--api-key-file", filepath.Join(t.TempDir(), "nope")},
			wantErr: "--api-key-file",
		},
		{
			// Stdio ignores the key sources: an unreadable file must not
			// block startup (README: all key sources ignored without --port).
			name:     "stdio_ignores_unreadable_key_file",
			args:     []string{"--dir", "d", "--scripts", "s", "--api-key-file", filepath.Join(t.TempDir(), "nope")},
			wantMode: modeServer,
			check: func(t *testing.T, c cliConfig) {
				if c.server.apiKey.Source != apiKeySourceNone {
					t.Errorf("apiKey = %v, want zero value in stdio mode", c.server.apiKey)
				}
			},
		},
		{
			name:    "no_timeout",
			args:    []string{"--dir", "d", "--scripts", "s", "--no-timeout"},
			wantErr: "",
			check: func(t *testing.T, c cliConfig) {
				if c.server.timeout != 0 {
					t.Errorf("timeout = %v, want 0", c.server.timeout)
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
				got := strings.Join(c.diagnostic.ignoredFlags, ",")
				if !strings.Contains(got, "--port") || !strings.Contains(got, "--api-key") {
					t.Errorf("ignoredFlags = %v", c.diagnostic.ignoredFlags)
				}
			},
		},
		{
			name:     "call_tool_mode_ignores_watch",
			args:     []string{"--dir", "d", "--scripts", "s", "--call-tool", "x", "--watch", "--port", "80"},
			wantMode: modeCallTool,
			check: func(t *testing.T, c cliConfig) {
				got := strings.Join(c.diagnostic.ignoredFlags, ",")
				if !strings.Contains(got, "--watch") || !strings.Contains(got, "--port") {
					t.Errorf("ignoredFlags = %v", c.diagnostic.ignoredFlags)
				}
				if !c.diagnostic.callToolSet {
					t.Error("callToolSet must be true")
				}
			},
		},
		{
			name:     "call_tool_present_empty_value_active",
			args:     []string{"--dir", "d", "--scripts", "s", "--call-tool", ""},
			wantMode: modeCallTool,
			check: func(t *testing.T, c cliConfig) {
				if !c.diagnostic.callToolSet {
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

// TestRunHTTPEndToEnd: run() served over real HTTP,
// exercised by a real MCP client (auth, list, call, clean shutdown), plus
// the zero-tools warning.
