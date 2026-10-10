package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestParseCLI: every fail-fast branch of the CLI front end.
// These are the branches that were untestable while the logic lived in main()
// (which calls os.Exit); the extraction into parseCLI makes each one assertable.
func TestParseCLI(t *testing.T) {
	// No t.Parallel: t.Setenv. An empty LOG_LEVEL counts as unset by the
	// resolution logic, so ambient values cannot leak into these cases.
	t.Setenv(logLevelEnvVar, "")
	keyFile := filepath.Join(t.TempDir(), "key.txt")
	if err := os.WriteFile(keyFile, []byte("filetoken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	emptyKeyFile := filepath.Join(t.TempDir(), "empty-key.txt")
	if err := os.WriteFile(emptyKeyFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tlsCertFile := filepath.Join(t.TempDir(), "cert.pem")
	if err := os.WriteFile(tlsCertFile, []byte("pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	tlsKeyFile := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(tlsKeyFile, []byte("pem"), 0o600); err != nil {
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
				if c.logLevel != slog.LevelInfo {
					t.Errorf("logLevel = %v, want the info default", c.logLevel)
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
			name:     "tls_pair_resolved",
			args:     []string{"--dir", "d", "--scripts", "s", "--port", "8443", "--tls-cert", tlsCertFile, "--tls-key", tlsKeyFile},
			wantMode: modeServer,
			check: func(t *testing.T, c cliConfig) {
				if c.server.tlsCert != tlsCertFile || c.server.tlsKey != tlsKeyFile {
					t.Errorf("tlsCert/tlsKey = %q/%q", c.server.tlsCert, c.server.tlsKey)
				}
			},
		},
		{
			name:    "max_concurrent_negative",
			args:    []string{"--dir", "d", "--scripts", "s", "--max-concurrent", "-1"},
			wantErr: "--max-concurrent must be 0 (default) or 1..256",
		},
		{
			name:    "max_concurrent_above_cap",
			args:    []string{"--dir", "d", "--scripts", "s", "--max-concurrent", "257"},
			wantErr: "--max-concurrent must be 0 (default) or 1..256",
		},
		{
			name:     "max_concurrent_at_cap",
			args:     []string{"--dir", "d", "--scripts", "s", "--max-concurrent", "256"},
			wantMode: modeServer,
			check: func(t *testing.T, c cliConfig) {
				if c.server.maxConcurrent != maxConcurrentCap {
					t.Errorf("maxConcurrent = %d, want %d", c.server.maxConcurrent, maxConcurrentCap)
				}
			},
		},
		{
			name:    "port_above_range",
			args:    []string{"--dir", "d", "--scripts", "s", "--port", "65536"},
			wantErr: "--port must be 0 (stdio) or 1..65535",
		},
		{
			name:    "port_negative",
			args:    []string{"--dir", "d", "--scripts", "s", "--port", "-1"},
			wantErr: "--port must be 0 (stdio) or 1..65535",
		},
		{
			name:     "port_at_range_edge",
			args:     []string{"--dir", "d", "--scripts", "s", "--port", "65535"},
			wantMode: modeServer,
			check: func(t *testing.T, c cliConfig) {
				if c.server.port != 65535 {
					t.Errorf("port = %d, want 65535", c.server.port)
				}
			},
		},
		{
			name:    "api_key_file_empty_fails",
			args:    []string{"--dir", "d", "--scripts", "s", "--port", "8080", "--api-key-file", emptyKeyFile},
			wantErr: "is empty",
		},
		{
			name:    "list_tools_and_call_tool_exclusive",
			args:    []string{"--dir", "d", "--scripts", "s", "--list-tools", "--call-tool", "x"},
			wantErr: "mutually exclusive",
		},
		{
			name:     "list_tools_mode_reports_ignored_server_flags",
			args:     []string{"--dir", "d", "--scripts", "s", "--list-tools", "--port=0", "--api-key", "tk", "--tls-cert", tlsCertFile, "--tls-key", tlsKeyFile},
			wantMode: modeListTools,
			check: func(t *testing.T, c cliConfig) {
				got := strings.Join(c.diagnostic.ignoredFlags, ",")
				for _, want := range []string{"--port", "--api-key", "--tls-cert", "--tls-key"} {
					if !strings.Contains(got, want) {
						t.Errorf("ignoredFlags = %v, want to include %s", c.diagnostic.ignoredFlags, want)
					}
				}
				if c.server.tlsCert != "" || c.server.tlsKey != "" {
					t.Errorf("tlsCert/tlsKey = %q/%q, want zero values in diagnostic mode", c.server.tlsCert, c.server.tlsKey)
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
			name:    "tls_cert_only",
			args:    []string{"--dir", "d", "--scripts", "s", "--port", "8443", "--tls-cert", tlsCertFile},
			wantErr: "--tls-cert and --tls-key must be given together",
		},
		{
			name:    "tls_missing_file_fails",
			args:    []string{"--dir", "d", "--scripts", "s", "--port", "8443", "--tls-cert", tlsCertFile, "--tls-key", filepath.Join(t.TempDir(), "nope")},
			wantErr: "invalid TLS configuration",
		},
		{
			// A directory opens fine, so a bare os.Stat check would accept it;
			// the regular-file check is what makes this fail fast.
			name:    "tls_cert_is_directory",
			args:    []string{"--dir", "d", "--scripts", "s", "--port", "8443", "--tls-cert", t.TempDir(), "--tls-key", tlsKeyFile},
			wantErr: "not a regular file",
		},
		{
			// Stdio ignores the TLS options: unreadable files must not block
			// startup (same contract as the key sources).
			name:     "stdio_ignores_unreadable_tls_files",
			args:     []string{"--dir", "d", "--scripts", "s", "--tls-cert", filepath.Join(t.TempDir(), "nope"), "--tls-key", filepath.Join(t.TempDir(), "nope")},
			wantMode: modeServer,
			check: func(t *testing.T, c cliConfig) {
				if c.server.tlsCert != "" || c.server.tlsKey != "" {
					t.Errorf("tlsCert/tlsKey = %q/%q, want zero values in stdio mode", c.server.tlsCert, c.server.tlsKey)
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
		{
			name:    "log_level_trace",
			args:    []string{"--dir", "d", "--scripts", "s", "--log-level", "trace"},
			wantErr: "",
			check: func(t *testing.T, c cliConfig) {
				if c.logLevel != levelTrace {
					t.Errorf("logLevel = %v, want trace", c.logLevel)
				}
			},
		},
		{
			name:    "log_level_debug",
			args:    []string{"--dir", "d", "--scripts", "s", "--log-level", "debug"},
			wantErr: "",
			check: func(t *testing.T, c cliConfig) {
				if c.logLevel != slog.LevelDebug {
					t.Errorf("logLevel = %v, want debug", c.logLevel)
				}
			},
		},
		{
			name:    "log_level_info",
			args:    []string{"--dir", "d", "--scripts", "s", "--log-level", "info"},
			wantErr: "",
			check: func(t *testing.T, c cliConfig) {
				if c.logLevel != slog.LevelInfo {
					t.Errorf("logLevel = %v, want info", c.logLevel)
				}
			},
		},
		{
			name:    "log_level_warn",
			args:    []string{"--dir", "d", "--scripts", "s", "--log-level", "warn"},
			wantErr: "",
			check: func(t *testing.T, c cliConfig) {
				if c.logLevel != slog.LevelWarn {
					t.Errorf("logLevel = %v, want warn", c.logLevel)
				}
			},
		},
		{
			name:    "log_level_error",
			args:    []string{"--dir", "d", "--scripts", "s", "--log-level", "error"},
			wantErr: "",
			check: func(t *testing.T, c cliConfig) {
				if c.logLevel != slog.LevelError {
					t.Errorf("logLevel = %v, want error", c.logLevel)
				}
			},
		},
		{
			// Accepted in every mode: the value resolves in diagnostic mode too.
			name:     "log_level_diagnostic_mode",
			args:     []string{"--dir", "d", "--scripts", "s", "--list-tools", "--log-level", "debug"},
			wantErr:  "",
			wantMode: modeListTools,
			check: func(t *testing.T, c cliConfig) {
				if c.logLevel != slog.LevelDebug {
					t.Errorf("logLevel = %v, want debug", c.logLevel)
				}
			},
		},
		{
			name:    "log_level_invalid",
			args:    []string{"--dir", "d", "--scripts", "s", "--log-level", "verbose"},
			wantErr: `--log-level must be one of trace, debug, info, warn, or error (got "verbose")`,
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

// TestParseCLIDiagnosticIgnoresInvalidServerValidation: diagnostic modes
// document their server flags as ignored, so an invalid CORS flag/env var
// (and an invalid --max-concurrent) must not block --list-tools/--call-tool.
func TestParseCLIDiagnosticIgnoresInvalidServerValidation(t *testing.T) {
	// No --log-level flag: pin the env var to "" (unset by design) so an
	// ambient LOG_LEVEL cannot leak into the resolution logic.
	t.Setenv(logLevelEnvVar, "")
	t.Setenv(allowedOriginsEnvVar, "not-a-url")
	t.Setenv(allowAllOriginsEnvVar, "banana")

	cfg, err := parseCLI([]string{"--dir", "d", "--scripts", "s", "--list-tools", "--max-concurrent", "-1"})
	if err != nil {
		t.Fatalf("diagnostic mode must not run server-only CORS/concurrency validation: %v", err)
	}
	if cfg.mode != modeListTools {
		t.Errorf("mode = %v, want list-tools", cfg.mode)
	}
	if len(cfg.diagnostic.ignoredFlags) == 0 {
		t.Errorf("ignored-flags notice empty — server flags were not reported")
	}

	// The same inputs in server mode must still fail-fast.
	if _, err := parseCLI([]string{"--dir", "d", "--scripts", "s"}); err == nil {
		t.Errorf("server mode with invalid CORS env must fail, got nil")
	}
}

// TestLogLevelPrecedence: the --log-level flag wins over the LOG_LEVEL env
// var; the env var is consulted only when the flag is absent (an empty
// value counts as unset); the default is info. No t.Parallel: t.Setenv.
func TestLogLevelPrecedence(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		env       string // LOG_LEVEL value; empty = unset by the resolution logic
		wantErr   string // substring; empty = success
		wantLevel slog.Level
	}{
		{
			name:      "flag_wins_over_env",
			args:      []string{"--dir", "d", "--scripts", "s", "--log-level", "debug"},
			env:       "warn",
			wantLevel: slog.LevelDebug,
		},
		{
			name:      "flag_trace_wins_over_env",
			args:      []string{"--dir", "d", "--scripts", "s", "--log-level", "trace"},
			env:       "warn",
			wantLevel: levelTrace,
		},
		{
			name:      "env_trace_when_flag_absent",
			args:      []string{"--dir", "d", "--scripts", "s"},
			env:       "trace",
			wantLevel: levelTrace,
		},
		{
			name:      "env_used_when_flag_absent",
			args:      []string{"--dir", "d", "--scripts", "s"},
			env:       "warn",
			wantLevel: slog.LevelWarn,
		},
		{
			name:    "invalid_env_value_fails_naming_the_source",
			args:    []string{"--dir", "d", "--scripts", "s"},
			env:     "verbose",
			wantErr: `LOG_LEVEL must be one of trace, debug, info, warn, or error (got "verbose")`,
		},
		{
			name:      "empty_env_ignored_default_applies",
			args:      []string{"--dir", "d", "--scripts", "s"},
			env:       "",
			wantLevel: slog.LevelInfo,
		},
		{
			name:      "default_when_neither_set",
			args:      []string{"--dir", "d", "--scripts", "s"},
			wantLevel: slog.LevelInfo,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set unconditionally (empty = unset) so an ambient LOG_LEVEL
			// cannot leak into any case.
			t.Setenv(logLevelEnvVar, tt.env)
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
			if cfg.logLevel != tt.wantLevel {
				t.Errorf("logLevel = %v, want %v", cfg.logLevel, tt.wantLevel)
			}
		})
	}
}

// TestUsageLineListsRegisteredFlags: the one-line synopsis printed with
// errMissingRequiredFlags must stay in sync with the flags parseCLI
// registers — a new flag without a synopsis entry fails here.
func TestUsageLineListsRegisteredFlags(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"dir", "scripts", "watch", "host", "port", "api-key", "api-key-file",
		"tls-cert", "tls-key", "insecure-no-auth", "max-concurrent",
		"allowed-origins", "allow-all-origins", "disable-localhost-protection",
		"timeout", "no-timeout", "list-tools", "call-tool", "params",
		"log-level",
	} {
		if !strings.Contains(usageLine, "--"+name) {
			t.Errorf("usageLine is missing --%s:\n%s", name, usageLine)
		}
	}
}

func TestFlagParseErrorUnwrap(t *testing.T) {
	t.Parallel()
	inner := errors.New("inner failure")
	// Wrap in an outer %w chain so errors.As/Is must traverse it:
	// matching the way main() receives the parse error.
	err := fmt.Errorf("parse: %w", &flagParseError{err: inner, usage: "usage text"})
	var parseErr *flagParseError
	if !errors.As(err, &parseErr) {
		t.Fatal("errors.As did not match *flagParseError through the wrap chain")
	}
	if !errors.Is(err, inner) {
		t.Error("errors.Is did not reach the wrapped error via Unwrap")
	}
	if !strings.Contains(err.Error(), "inner failure") {
		t.Errorf("Error() = %q, want it to contain %q", err.Error(), "inner failure")
	}
}
