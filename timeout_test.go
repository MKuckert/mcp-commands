package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTimeoutScript(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "script.sh")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("failed to create script: %v", err)
	}
	return path
}

func TestParseTimeoutDuration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		raw          string
		want         time.Duration
		wantErr      bool
		wantOverflow bool
	}{
		{name: "minutes", raw: "5m", want: 5 * time.Minute},
		{name: "seconds", raw: "60s", want: 60 * time.Second},
		{name: "compound", raw: "1h 30m 5s", want: time.Hour + 30*time.Minute + 5*time.Second},
		{name: "compound_no_spaces", raw: "1h30m5s", want: time.Hour + 30*time.Minute + 5*time.Second},
		{name: "duplicates_sum", raw: "5m 5m", want: 10 * time.Minute},
		{name: "subsecond_ms_rejected", raw: "250ms", wantErr: true},
		{name: "subsecond_us_rejected", raw: "250us", wantErr: true},
		{name: "subsecond_µs_rejected", raw: "250µs", wantErr: true},
		{name: "subsecond_ns_rejected", raw: "1000ns", wantErr: true},
		{name: "compound_subsecond_rejected", raw: "5m 250ms", wantErr: true},
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
		// ~300 years total: each 876000h (100y) term fits, the sum does not.
		{name: "sum_overflow_rejected", raw: "876000h 876000h 876000h", wantErr: true, wantOverflow: true},
		// Fits in 63 bits, but seconds→nanoseconds multiplication overflows.
		{name: "single_term_overflow_rejected", raw: "9223372037s", wantErr: true, wantOverflow: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseTimeoutDuration(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseTimeoutDuration(%q) = %v, want error", tt.raw, got)
				}
				if tt.wantOverflow && !strings.Contains(err.Error(), "overflows") {
					t.Errorf("parseTimeoutDuration(%q) error = %v, want \"overflows\"", tt.raw, err)
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
	t.Parallel()
	tests := []struct {
		name        string
		timeoutFlag string
		timeoutSet  bool
		noTimeout   bool
		want        time.Duration
		wantErr     bool
	}{
		{name: "default_when_unset", timeoutFlag: "", want: defaultToolTimeout},
		{name: "flag_parsed", timeoutFlag: "5s", timeoutSet: true, want: 5 * time.Second},
		{name: "flag_none", timeoutFlag: "NONE", timeoutSet: true, want: 0},
		{name: "flag_zero", timeoutFlag: "0s", timeoutSet: true, want: 0},
		{name: "no_timeout_alone", noTimeout: true, want: 0},
		{name: "no_timeout_and_flag_exclusive", timeoutFlag: "5s", timeoutSet: true, noTimeout: true, wantErr: true},
		{name: "no_timeout_and_invalid_flag_exclusive", timeoutFlag: "bogus", timeoutSet: true, noTimeout: true, wantErr: true},
		{name: "invalid_flag_errors", timeoutFlag: "bogus", timeoutSet: true, wantErr: true},
		{name: "explicit_empty_flag_errors", timeoutFlag: "", timeoutSet: true, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveTimeout(tt.timeoutFlag, tt.timeoutSet, tt.noTimeout)
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

func TestResolveToolTimeout(t *testing.T) {
	t.Parallel()
	perTool := 30 * time.Second
	none := time.Duration(0)
	tests := []struct {
		name   string
		tool   discoveredTool
		global time.Duration
		want   time.Duration
	}{
		{"per_tool_wins_over_global", discoveredTool{Timeout: &perTool}, 5 * time.Minute, 30 * time.Second},
		{"per_tool_none_wins_over_global", discoveredTool{Timeout: &none}, 5 * time.Minute, 0},
		{"nil_inherits_global", discoveredTool{}, 5 * time.Minute, 5 * time.Minute},
		{"nil_inherits_no_timeout_global", discoveredTool{}, 0, 0},
	}
	for _, tt := range tests {
		if got := resolveToolTimeout(tt.tool, tt.global); got != tt.want {
			t.Errorf("%s: resolveToolTimeout = %v, want %v", tt.name, got, tt.want)
		}
	}
}
