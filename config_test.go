package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func strPtr(s string) *string { return &s }

func boolPtr(b bool) *bool { return &b }

// writeConfigFile writes content to a file named name in a fresh temp dir
// and returns its path.
func writeConfigFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// assertStripped verifies the JSONC-stripping invariants: length and newline
// positions are preserved (so decode-error offsets still point at the
// original file), and the result decodes to wantDoc.
func assertStripped(t *testing.T, in, got []byte, wantDoc string) {
	t.Helper()
	if len(got) != len(in) {
		t.Fatalf("length changed: got %d, want %d (offsets must be preserved)", len(got), len(in))
	}
	for i := 0; i < len(in); i++ {
		if (in[i] == '\n') != (got[i] == '\n') {
			t.Fatalf("newline positions changed at byte %d", i)
		}
	}
	var doc any
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("stripped output is not valid JSON: %v (%q)", err, got)
	}
	var want any
	if err := json.Unmarshal([]byte(wantDoc), &want); err != nil {
		t.Fatalf("test bug: bad wantDoc %q", wantDoc)
	}
	if !bytes.Equal(jsonMustMarshal(t, doc), jsonMustMarshal(t, want)) {
		t.Errorf("stripped document = %s, want %s", doc, wantDoc)
	}
}

func jsonMustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestStripJSONCComments(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      string
		wantDoc string // decoded document after stripping; "" with wantErr = error case
		wantErr bool
	}{
		{name: "empty", in: "", wantDoc: ""},
		{name: "line_comment_to_end", in: `{"a": 1} // trailing`, wantDoc: `{"a": 1}`},
		{name: "line_comment_own_line", in: "// header\n{}\n// footer\n", wantDoc: `{}`},
		{name: "block_comment", in: `{"a": /* x */ 1}`, wantDoc: `{"a": 1}`},
		{name: "block_comment_multiline", in: `{"a": /* one\ntwo */ 1}`, wantDoc: `{"a": 1}`},
		{name: "block_comment_at_eof", in: `{"a": 1} /* tail */`, wantDoc: `{"a": 1}`},
		{name: "slash_in_string_kept", in: `{"a": "x // y /* z */"}`, wantDoc: `{"a": "x // y /* z */"}`},
		{name: "escaped_quote_in_string", in: `{"a": "q\" // not a comment"}`, wantDoc: `{"a": "q\" // not a comment"}`},
		{name: "comment_after_escaped_quote", in: `{"a": "q\""} // real comment`, wantDoc: `{"a": "q\""}`},
		{name: "unterminated_block_errors", in: `{"a": /* oops`, wantErr: true},
		{name: "block_open_at_eof_errors", in: `{"a": 1} /*`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := stripJSONCComments([]byte(tt.in))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("stripJSONCComments(%q) = %q, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("stripJSONCComments(%q) unexpected error: %v", tt.in, err)
			}
			if tt.wantDoc == "" {
				// Empty input stays empty; nothing to decode.
				if len(got) != 0 {
					t.Fatalf("got %q, want empty", got)
				}
				return
			}
			assertStripped(t, []byte(tt.in), got, tt.wantDoc)
		})
	}

	t.Run("no_comments_unchanged", func(t *testing.T) {
		in := `{"a": 1, "b": [2, 3]}`
		got, err := stripJSONCComments([]byte(in))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != in {
			t.Errorf("got %q, want unchanged %q", got, in)
		}
	})
}

func TestLoadConfigFile(t *testing.T) {
	t.Parallel()
	full := `{"dir": "d", "scripts": "s", "watch": true, "log-level": "debug", "timeout": "1h"}`
	tests := []struct {
		name    string
		file    string // file name (extension matters)
		content string
		wantErr string // substring; empty = success
		check   func(t *testing.T, c *fileConfig)
	}{
		{
			name: "empty_object_all_nil", file: "c.json", content: `{}`,
		},
		{
			name: "full_file", file: "c.json", content: full,
			check: func(t *testing.T, c *fileConfig) {
				if c.Dir == nil || *c.Dir != "d" || c.Scripts == nil || *c.Scripts != "s" {
					t.Errorf("dir/scripts = %v %v", c.Dir, c.Scripts)
				}
				if c.Watch == nil || !*c.Watch {
					t.Error("watch not true")
				}
				if c.LogLevel == nil || *c.LogLevel != "debug" {
					t.Errorf("log-level = %v", c.LogLevel)
				}
				if c.Timeout == nil || *c.Timeout != "1h" {
					t.Errorf("timeout = %v", c.Timeout)
				}
			},
		},
		{
			name: "jsonc_with_comments", file: "c.jsonc",
			content: "// header\n{\n  \"dir\": \"d\" /* inline */,\n  \"watch\": false // tail\n}\n",
			check: func(t *testing.T, c *fileConfig) {
				if c.Dir == nil || *c.Dir != "d" {
					t.Errorf("dir = %v", c.Dir)
				}
				if c.Watch == nil || *c.Watch {
					t.Error("watch not explicitly false")
				}
			},
		},
		{
			name: "uppercase_extension", file: "C.JSON", content: `{"dir": "d"}`,
		},
		{
			name: "wrong_extension", file: "c.yaml", content: `{}`, wantErr: "must have a .json or .jsonc extension",
		},
		{
			name: "no_extension", file: "c", content: `{}`, wantErr: "must have a .json or .jsonc extension",
		},
		{
			name: "invalid_json", file: "c.json", content: `{"dir": }`, wantErr: "invalid JSON",
		},
		{
			name: "trailing_content", file: "c.json", content: `{"dir": "d"} extra`, wantErr: "unexpected content after",
		},
		{
			name: "null_document", file: "c.json", content: `null`, wantErr: "invalid configuration",
		},
		{
			name: "unknown_key_in_server_rejected", file: "c.json", content: `{"server": {"bogus": 1}}`, wantErr: "invalid configuration",
		},
		{
			name: "schema_meta_key_accepted", file: "c.json",
			content: `{"$schema": "https://raw.githubusercontent.com/MKuckert/mcp-commands/main/config.schema.json", "dir": "d"}`,
			check: func(t *testing.T, c *fileConfig) {
				if c.Dir == nil || *c.Dir != "d" {
					t.Errorf("dir = %v", c.Dir)
				}
			},
		},
		{
			name: "wrong_type_rejected", file: "c.json", content: `{"watch": "yes"}`, wantErr: "invalid configuration",
		},
		{
			name: "empty_dir_rejected", file: "c.json", content: `{"dir": ""}`, wantErr: "invalid configuration",
		},
		{
			name: "server_section_decoded", file: "c.json",
			content: `{"dir": "d", "scripts": "s", "server": {"host": "0.0.0.0", "port": 8443, "api-key": "k", "max-concurrent": 7, "allowed-origins": [], "allow-all-origins": false}}`,
			check: func(t *testing.T, c *fileConfig) {
				s := c.Server
				if s == nil {
					t.Fatal("server section not decoded")
				}
				if s.Host == nil || *s.Host != "0.0.0.0" || s.Port == nil || *s.Port != 8443 {
					t.Errorf("host/port = %v %v", s.Host, s.Port)
				}
				if s.APIKey == nil || *s.APIKey != "k" {
					t.Errorf("api-key = %v", s.APIKey)
				}
				if s.MaxConcurrent == nil || *s.MaxConcurrent != 7 {
					t.Errorf("max-concurrent = %v", s.MaxConcurrent)
				}
				if s.AllowedOrigins == nil || len(*s.AllowedOrigins) != 0 {
					t.Errorf("allowed-origins = %v, want present empty", s.AllowedOrigins)
				}
				if s.AllowAllOrigins == nil || *s.AllowAllOrigins {
					t.Errorf("allow-all-origins = %v, want present false", s.AllowAllOrigins)
				}
				if s.TLSKey != nil || s.InsecureNoAuth != nil {
					t.Error("absent keys must stay nil")
				}
			},
		},
		{
			name: "server_wrong_type_rejected", file: "c.json", content: `{"server": {"port": "8080"}}`, wantErr: "invalid configuration",
		},
		{
			name: "server_origins_not_array_rejected", file: "c.json", content: `{"server": {"allowed-origins": "https://a.example"}}`, wantErr: "invalid configuration",
		},
		{
			name: "unterminated_jsonc_comment", file: "c.jsonc", content: `{"dir": /* oops`, wantErr: "unterminated",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeConfigFile(t, tt.file, tt.content)
			got, err := loadConfigFile(path)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("loadConfigFile = %v, want error containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("loadConfigFile unexpected error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, got)
			}
		})
	}

	t.Run("missing_file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "absent.json")
		if _, err := loadConfigFile(path); err == nil || !strings.Contains(err.Error(), "cannot read config file") {
			t.Fatalf("want cannot-read error, got %v", err)
		}
	})

	t.Run("oversized_file", func(t *testing.T) {
		big := strings.Repeat(" ", maxConfigFileBytes+1)
		path := writeConfigFile(t, "big.json", big)
		if _, err := loadConfigFile(path); err == nil || !strings.Contains(err.Error(), "exceeds the") {
			t.Fatalf("want oversize error, got %v", err)
		}
	})
}

// TestParseCLIConfig: the --config flag and the five top-level shared keys
// (dir, scripts, watch, log-level, timeout) with flag > env > file precedence.
// No t.Parallel: t.Setenv for LOG_LEVEL.
func TestParseCLIConfig(t *testing.T) {
	t.Setenv(logLevelEnvVar, "")

	full := `{"dir": "fd", "scripts": "fs", "watch": true, "log-level": "debug", "timeout": "1h30m"}`
	fullFile := writeConfigFile(t, "full.json", full)
	watchTrueFile := writeConfigFile(t, "watch.json", `{"dir": "d", "scripts": "s", "watch": true}`)
	noDirFile := writeConfigFile(t, "nodir.json", `{"log-level": "debug"}`)
	badLevelFile := writeConfigFile(t, "badlevel.json", `{"dir": "d", "scripts": "s", "log-level": "verbose"}`)
	badTimeoutFile := writeConfigFile(t, "badtimeout.json", `{"dir": "d", "scripts": "s", "timeout": "soon"}`)
	unknownKeyFile := writeConfigFile(t, "unknown.json", `{"dir": "d", "scripts": "s", "server": {"bogus": 1}}`)

	tests := []struct {
		name     string
		args     []string
		setup    func(t *testing.T) // per-case environment (runs before parseCLI)
		wantErr  string             // substring; empty = success
		wantMode cliMode            // zero value = modeServer
		check    func(t *testing.T, c cliConfig)
	}{
		{
			name: "file_supplies_dir_scripts",
			args: []string{"--config", fullFile},
			check: func(t *testing.T, c cliConfig) {
				if c.server.dir != "fd" || c.server.scriptsDir != "fs" {
					t.Errorf("server dir/scripts = %q %q", c.server.dir, c.server.scriptsDir)
				}
				if !c.server.watch {
					t.Error("watch not from file")
				}
				if c.logLevel != slog.LevelDebug {
					t.Errorf("logLevel = %v, want debug", c.logLevel)
				}
				if c.server.timeout != 90*time.Minute {
					t.Errorf("timeout = %v, want 1h30m", c.server.timeout)
				}
			},
		},
		{
			name: "flag_overrides_file_dir",
			args: []string{"--config", fullFile, "--dir", "flagdir"},
			check: func(t *testing.T, c cliConfig) {
				if c.server.dir != "flagdir" || c.server.scriptsDir != "fs" {
					t.Errorf("dir/scripts = %q %q", c.server.dir, c.server.scriptsDir)
				}
			},
		},
		{
			name: "explicit_false_flag_overrides_file_watch",
			args: []string{"--config", watchTrueFile, "--watch=false"},
			check: func(t *testing.T, c cliConfig) {
				if c.server.watch {
					t.Error("watch should be false (explicit flag wins)")
				}
			},
		},
		{
			name:  "env_overrides_file_log_level",
			args:  []string{"--config", fullFile},
			setup: func(t *testing.T) { t.Setenv(logLevelEnvVar, "warn") },
			check: func(t *testing.T, c cliConfig) {
				if c.logLevel != slog.LevelWarn {
					t.Errorf("logLevel = %v, want warn (env over file)", c.logLevel)
				}
			},
		},
		{
			name:    "file_log_level_invalid",
			args:    []string{"--config", badLevelFile},
			wantErr: "config file log-level must be one of",
		},
		{
			name:    "file_timeout_invalid",
			args:    []string{"--config", badTimeoutFile},
			wantErr: "config file timeout:",
		},
		{
			name:    "unknown_key_rejected",
			args:    []string{"--config", unknownKeyFile},
			wantErr: "invalid configuration",
		},
		{
			name:    "missing_dir_scripts_flag_and_file",
			args:    []string{"--config", noDirFile},
			wantErr: "--dir and --scripts are required",
		},
		{
			name:    "version_skips_config_load",
			args:    []string{"--version", "--config", filepath.Join(t.TempDir(), "absent.json")},
			wantErr: "",
			check: func(t *testing.T, c cliConfig) {
				if !c.version {
					t.Error("version not set")
				}
			},
		},
		{
			name:     "diagnostic_list_tools_with_file",
			args:     []string{"--config", fullFile, "--list-tools"},
			wantMode: modeListTools,
			check: func(t *testing.T, c cliConfig) {
				if c.diagnostic.dir != "fd" || c.diagnostic.scriptsDir != "fs" {
					t.Errorf("diagnostic dir/scripts = %q %q", c.diagnostic.dir, c.diagnostic.scriptsDir)
				}
				if !c.diagnostic.watch {
					t.Error("diagnostic watch not from file")
				}
				if c.diagnostic.timeout != 90*time.Minute {
					t.Errorf("diagnostic timeout = %v", c.diagnostic.timeout)
				}
			},
		},
		{
			name: "no_timeout_flag_over_file_timeout",
			args: []string{"--config", fullFile, "--no-timeout"},
			check: func(t *testing.T, c cliConfig) {
				if c.server.timeout != 0 {
					t.Errorf("timeout = %v, want 0", c.server.timeout)
				}
			},
		},
		{
			name: "timeout_flag_over_file_timeout",
			args: []string{"--config", fullFile, "--timeout", "30s"},
			check: func(t *testing.T, c cliConfig) {
				if c.server.timeout != 30*time.Second {
					t.Errorf("timeout = %v, want 30s", c.server.timeout)
				}
			},
		},
		{
			name: "no_config_unchanged_default_timeout",
			args: []string{"--dir", "d", "--scripts", "s"},
			check: func(t *testing.T, c cliConfig) {
				if c.server.timeout != defaultToolTimeout {
					t.Errorf("timeout = %v, want default", c.server.timeout)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setup != nil {
				tt.setup(t)
			}
			c, err := parseCLI(tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("parseCLI = %v, want error containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseCLI unexpected error: %v", err)
			}
			if c.mode != tt.wantMode {
				t.Fatalf("mode = %v, want %v", c.mode, tt.wantMode)
			}
			if tt.check != nil {
				tt.check(t, c)
			}
		})
	}
}

// TestParseCLIServerSection: the config file's server section merges into
// server-mode resolution with flag > env > file precedence, explicit-empty
// semantics, and cross-source validation. No t.Parallel: t.Setenv for the
// CORS env vars.
func TestParseCLIServerSection(t *testing.T) {
	t.Setenv(logLevelEnvVar, "")
	t.Setenv(allowedOriginsEnvVar, "")
	t.Setenv(allowAllOriginsEnvVar, "")

	writeKeyFile := func(t *testing.T, content string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "key.txt")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	writePEM := func(t *testing.T, name string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(p, []byte("pem"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	fullServer := `{"dir": "d", "scripts": "s", "server": {"host": "0.0.0.0", "port": 8080, "api-key": "filekey", "max-concurrent": 4, "insecure-no-auth": true, "allowed-origins": ["https://a.example"], "disable-localhost-protection": true}}`
	fullFile := writeConfigFile(t, "full.json", fullServer)
	originsFile := writeConfigFile(t, "origins.json", `{"dir": "d", "scripts": "s", "server": {"allowed-origins": ["https://f.example"]}}`)
	emptyOriginsFile := writeConfigFile(t, "emptyorigins.json", `{"dir": "d", "scripts": "s", "server": {"allowed-origins": []}}`)
	allowAllFalseFile := writeConfigFile(t, "allowallfalse.json", `{"dir": "d", "scripts": "s", "server": {"allow-all-origins": false}}`)
	allowAllTrueFile := writeConfigFile(t, "allowalltrue.json", `{"dir": "d", "scripts": "s", "server": {"allow-all-origins": true}}`)
	apiKeyFileRef := writeKeyFile(t, "from-keyfile\n")
	apiKeyFileCfg := writeConfigFile(t, "apikeyfile.json", `{"dir": "d", "scripts": "s", "server": {"port": 8080, "api-key-file": "`+apiKeyFileRef+`"}}`)
	cert := writePEM(t, "cert.pem")
	key := writePEM(t, "key.pem")
	tlsSplitFile := writeConfigFile(t, "tlssplit.json", `{"dir": "d", "scripts": "s", "server": {"port": 8443, "tls-cert": "`+cert+`"}}`)
	tlsKeyOnlyFile := writeConfigFile(t, "tlskeyonly.json", `{"dir": "d", "scripts": "s", "server": {"port": 8443, "tls-key": "`+key+`"}}`)
	tlsBadPathFile := writeConfigFile(t, "tlsbad.json", `{"dir": "d", "scripts": "s", "server": {"port": 8443, "tls-cert": "`+filepath.Join(t.TempDir(), "nope")+`", "tls-key": "`+key+`"}}`)
	badPortFile := writeConfigFile(t, "badport.json", `{"dir": "d", "scripts": "s", "server": {"port": 70000}}`)
	badMaxConcFile := writeConfigFile(t, "badmaxconc.json", `{"dir": "d", "scripts": "s", "server": {"max-concurrent": 257}}`)
	stdioBadKeyFile := writeConfigFile(t, "stdio.json", `{"dir": "d", "scripts": "s", "server": {"api-key-file": "`+filepath.Join(t.TempDir(), "nope")+`"}}`)
	diagServerFile := writeConfigFile(t, "diag.json", `{"dir": "d", "scripts": "s", "server": {"port": 8080, "allowed-origins": ["not-a-url"], "allow-all-origins": true}}`)

	tests := []struct {
		name     string
		args     []string
		setup    func(t *testing.T) // per-case environment (runs before parseCLI)
		wantErr  string             // substring; empty = success
		wantMode cliMode            // zero value = modeServer
		check    func(t *testing.T, c cliConfig)
	}{
		{
			name: "file_supplies_server_settings",
			args: []string{"--config", fullFile},
			check: func(t *testing.T, c cliConfig) {
				if c.server.host != "0.0.0.0" || c.server.port != 8080 {
					t.Errorf("host/port = %q/%d", c.server.host, c.server.port)
				}
				if c.server.apiKey.Token != "filekey" || c.server.apiKey.Source != apiKeySourceConfig {
					t.Errorf("apiKey = %v, want filekey/config file", c.server.apiKey)
				}
				if c.server.maxConcurrent != 4 {
					t.Errorf("maxConcurrent = %d", c.server.maxConcurrent)
				}
				if !c.server.insecureNoAuth {
					t.Error("insecure-no-auth not from file")
				}
				if len(c.server.cors.origins) != 1 || c.server.cors.origins[0] != "https://a.example" {
					t.Errorf("cors origins = %v", c.server.cors.origins)
				}
				if !c.server.cors.disableLocalhostProtection {
					t.Error("disable-localhost-protection not from file")
				}
			},
		},
		{
			name: "flags_override_file",
			args: []string{"--config", fullFile, "--host", "127.0.0.1", "--port", "9090", "--max-concurrent", "8", "--api-key", "flagkey"},
			check: func(t *testing.T, c cliConfig) {
				if c.server.host != "127.0.0.1" || c.server.port != 9090 {
					t.Errorf("host/port = %q/%d", c.server.host, c.server.port)
				}
				if c.server.maxConcurrent != 8 {
					t.Errorf("maxConcurrent = %d", c.server.maxConcurrent)
				}
				if c.server.apiKey.Token != "flagkey" || c.server.apiKey.Source != apiKeySourceFlag {
					t.Errorf("apiKey = %v", c.server.apiKey)
				}
			},
		},
		{
			name: "explicit_false_flag_overrides_file_insecure",
			args: []string{"--config", fullFile, "--insecure-no-auth=false"},
			check: func(t *testing.T, c cliConfig) {
				if c.server.insecureNoAuth {
					t.Error("insecure-no-auth should be false (explicit flag wins)")
				}
			},
		},
		{
			name:  "env_beats_file_api_key",
			args:  []string{"--config", fullFile},
			setup: func(t *testing.T) { t.Setenv(apiKeyEnvVar, "from-env") },
			check: func(t *testing.T, c cliConfig) {
				if c.server.apiKey.Token != "from-env" || c.server.apiKey.Source != apiKeySourceEnv {
					t.Errorf("apiKey = %v, want from-env/env", c.server.apiKey)
				}
			},
		},
		{
			name: "file_api_key_file_resolved",
			args: []string{"--config", apiKeyFileCfg},
			check: func(t *testing.T, c cliConfig) {
				if c.server.apiKey.Token != "from-keyfile" || c.server.apiKey.Source != apiKeySourceConfig {
					t.Errorf("apiKey = %v, want from-keyfile/config file", c.server.apiKey)
				}
			},
		},
		{
			name:  "file_present_suppresses_env_origins",
			args:  []string{"--config", originsFile},
			setup: func(t *testing.T) { t.Setenv(allowedOriginsEnvVar, "https://env.example") },
			check: func(t *testing.T, c cliConfig) {
				if len(c.server.cors.origins) != 1 || c.server.cors.origins[0] != "https://f.example" {
					t.Errorf("cors origins = %v, want file value (presence suppresses env)", c.server.cors.origins)
				}
			},
		},
		{
			name: "cors_flag_beats_file_origins",
			args: []string{"--config", originsFile, "--allowed-origins", "https://flag.example"},
			check: func(t *testing.T, c cliConfig) {
				if len(c.server.cors.origins) != 1 || c.server.cors.origins[0] != "https://flag.example" {
					t.Errorf("cors origins = %v, want flag value", c.server.cors.origins)
				}
			},
		},
		{
			name:  "explicit_empty_file_origins_suppress_env",
			args:  []string{"--config", emptyOriginsFile},
			setup: func(t *testing.T) { t.Setenv(allowedOriginsEnvVar, "https://env.example") },
			check: func(t *testing.T, c cliConfig) {
				if len(c.server.cors.origins) != 0 || c.server.cors.allowAll {
					t.Errorf("cors = %v, want empty and not allow-all", c.server.cors)
				}
			},
		},
		{
			name:  "explicit_false_file_allow_all_suppress_env",
			args:  []string{"--config", allowAllFalseFile},
			setup: func(t *testing.T) { t.Setenv(allowAllOriginsEnvVar, "1") },
			check: func(t *testing.T, c cliConfig) {
				if c.server.cors.allowAll {
					t.Error("allow-all should be false (explicit file value wins over env)")
				}
			},
		},
		{
			name: "file_allow_all_true",
			args: []string{"--config", allowAllTrueFile},
			check: func(t *testing.T, c cliConfig) {
				if !c.server.cors.allowAll {
					t.Error("allow-all not from file")
				}
			},
		},
		{
			name:    "cross_source_cors_exclusive_file_origins_flag_allow_all",
			args:    []string{"--config", originsFile, "--allow-all-origins"},
			wantErr: "mutually exclusive",
		},
		{
			name:    "cross_source_cors_exclusive_flag_origins_file_allow_all",
			args:    []string{"--config", allowAllTrueFile, "--allowed-origins", "https://f.example"},
			wantErr: "mutually exclusive",
		},
		{
			name: "tls_pair_across_sources_flag_cert_file_key",
			args: []string{"--config", tlsKeyOnlyFile, "--tls-cert", cert},
			check: func(t *testing.T, c cliConfig) {
				if c.server.tlsCert != cert || c.server.tlsKey != key {
					t.Errorf("tlsCert/tlsKey = %q/%q", c.server.tlsCert, c.server.tlsKey)
				}
			},
		},
		{
			name: "tls_pair_across_sources_file_cert_flag_key",
			args: []string{"--config", tlsSplitFile, "--tls-key", key},
			check: func(t *testing.T, c cliConfig) {
				if c.server.tlsCert != cert || c.server.tlsKey != key {
					t.Errorf("tlsCert/tlsKey = %q/%q", c.server.tlsCert, c.server.tlsKey)
				}
			},
		},
		{
			name:    "tls_file_cert_only_errors_naming_config",
			args:    []string{"--config", tlsSplitFile},
			wantErr: "config file server.tls-cert and server.tls-key must be given together",
		},
		{
			name:    "tls_file_key_only_errors_naming_config",
			args:    []string{"--config", tlsKeyOnlyFile},
			wantErr: "config file server.tls-cert and server.tls-key must be given together",
		},
		{
			name:    "tls_file_bad_path_fails",
			args:    []string{"--config", tlsBadPathFile},
			wantErr: "invalid TLS configuration",
		},
		{
			name:    "file_port_out_of_range",
			args:    []string{"--config", badPortFile},
			wantErr: "config file server.port must be 0 (stdio) or 1..65535",
		},
		{
			name:    "file_max_concurrent_out_of_range",
			args:    []string{"--config", badMaxConcFile},
			wantErr: "config file server.max-concurrent must be 0 (default) or 1..256",
		},
		{
			// Stdio mode (no port) ignores the auth sources: an unreadable
			// config-file api-key-file must not block startup.
			name:     "stdio_ignores_file_api_key_file",
			args:     []string{"--config", stdioBadKeyFile},
			wantMode: modeServer,
			check: func(t *testing.T, c cliConfig) {
				if c.server.port != 0 || c.server.apiKey.Source != apiKeySourceNone {
					t.Errorf("port/apiKey = %d/%v, want stdio + zero value", c.server.port, c.server.apiKey)
				}
			},
		},
		{
			// Diagnostic modes silently ignore the server section (D2): even
			// invalid values do not fail or get reported.
			name:     "diagnostic_silently_ignores_server_section",
			args:     []string{"--config", diagServerFile, "--list-tools"},
			wantMode: modeListTools,
			check: func(t *testing.T, c cliConfig) {
				if len(c.diagnostic.ignoredFlags) != 0 {
					t.Errorf("ignoredFlags = %v, want empty (file values are not reported)", c.diagnostic.ignoredFlags)
				}
			},
		},
		{
			name:     "diagnostic_call_tool_silently_ignores_server_section",
			args:     []string{"--config", diagServerFile, "--call-tool", "hello"},
			wantMode: modeCallTool,
			check: func(t *testing.T, c cliConfig) {
				if len(c.diagnostic.ignoredFlags) != 0 {
					t.Errorf("ignoredFlags = %v, want empty (file values are not reported)", c.diagnostic.ignoredFlags)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setup != nil {
				tt.setup(t)
			}
			c, err := parseCLI(tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("parseCLI = %v, want error containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseCLI unexpected error: %v", err)
			}
			if c.mode != tt.wantMode {
				t.Fatalf("mode = %v, want %v", c.mode, tt.wantMode)
			}
			if tt.check != nil {
				tt.check(t, c)
			}
		})
	}
}
