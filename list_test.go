package main

import (
	"strings"
	"testing"
	"time"
)

func TestListParamDecl(t *testing.T) {
	t.Parallel()
	tests := []struct {
		param paramSpec
		want  string
	}{
		{paramSpec{Name: "a", Type: "string", Required: true}, "a:str"},
		{paramSpec{Name: "b", Type: "number"}, "[b:num]"},
		{paramSpec{Name: "c", Type: "boolean", Required: true}, "c:bool"},
		{paramSpec{Name: "d", Type: "string"}, "[d:str]"},
	}
	for _, tt := range tests {
		if got := listParamDecl(tt.param); got != tt.want {
			t.Errorf("listParamDecl(%#v) = %q, want %q", tt.param, got, tt.want)
		}
	}
}

func TestToolListSignature(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		params []paramSpec
		want   string
	}{
		{"none", nil, "none()"},
		{"none", []paramSpec{}, "none()"},
		{"required_only", []paramSpec{
			{Name: "a", Type: "string", Required: true},
			{Name: "b", Type: "number", Required: true},
		}, "required_only(a:str, b:num)"},
		{"optional_only", []paramSpec{
			{Name: "a", Type: "string"},
			{Name: "b", Type: "number"},
		}, "optional_only([a:str], [b:num])"},
		{"mixed", []paramSpec{
			{Name: "a", Type: "string", Required: true},
			{Name: "b", Type: "number"},
			{Name: "c", Type: "boolean"},
		}, "mixed(a:str, [b:num], [c:bool])"},
		// Duplicate name: last declaration wins for type/required, rendered
		// at the first-occurrence position (mirrors buildInputSchema).
		{"duplicate", []paramSpec{
			{Name: "x", Type: "string", Required: true},
			{Name: "a", Type: "string", Required: true},
			{Name: "y", Type: "number"},
			{Name: "a", Type: "number"},
			{Name: "z", Type: "boolean"},
		}, "duplicate(x:str, [a:num], [y:num], [z:bool])"},
	}
	for _, tt := range tests {
		if got := toolListSignature(tt.name, tt.params); got != tt.want {
			t.Errorf("toolListSignature(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestWordWrap(t *testing.T) {
	t.Parallel()
	indent := "     "

	t.Run("short_text_single_line", func(t *testing.T) {
		got := wordWrap("hello world", indent, 160)
		want := []string{"     hello world"}
		if len(got) != 1 || got[0] != want[0] {
			t.Fatalf("wordWrap = %#v, want %#v", got, want)
		}
	})

	t.Run("whitespace_runs_collapse_to_one_space", func(t *testing.T) {
		got := wordWrap("a\t\t  b\n\nc", indent, 160)
		want := []string{"     a b c"}
		if len(got) != 1 || got[0] != want[0] {
			t.Fatalf("wordWrap = %#v, want %#v", got, want)
		}
	})

	t.Run("leading_trailing_whitespace_stripped", func(t *testing.T) {
		got := wordWrap("  a b  ", indent, 160)
		want := []string{"     a b"}
		if len(got) != 1 || got[0] != want[0] {
			t.Fatalf("wordWrap = %#v, want %#v", got, want)
		}
	})

	t.Run("three_lines_at_160", func(t *testing.T) {
		// 35 words of 8 runes: 17 words fit per 155-rune budget
		// (17*8+16=152; the 18th needs 161), so 35 words → 17+17+1.
		var b strings.Builder
		for i := 0; i < 35; i++ {
			if i > 0 {
				b.WriteByte(' ')
			}
			b.WriteString("wordword")
		}
		word := "wordword"
		want := []string{
			"     " + strings.Repeat(word+" ", 16) + word,
			"     " + strings.Repeat(word+" ", 16) + word,
			"     " + word,
		}
		got := wordWrap(b.String(), indent, 160)
		if len(got) != 3 {
			t.Fatalf("wordWrap returned %d lines, want 3: %#v", len(got), got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("line %d = %q, want %q", i, got[i], want[i])
			}
			if n := len([]rune(got[i])); n > 160 {
				t.Errorf("line %d exceeds the 160-rune width: %d runes", i, n)
			}
		}
	})

	t.Run("overlong_token_emitted_whole_unsplit", func(t *testing.T) {
		long := strings.Repeat("x", 200)
		got := wordWrap("a "+long+" b", indent, 160)
		want := []string{"     a", "     " + long, "     b"}
		if len(got) != 3 {
			t.Fatalf("wordWrap returned %d lines, want 3: %q...", len(got), got[:min(len(got), 1)])
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("line %d = %q (len %d), want %q (len %d)", i, got[i], len(got[i]), want[i], len(want[i]))
			}
		}
	})

	t.Run("multibyte_runes_never_torn", func(t *testing.T) {
		// Em-dashes (—) and accented chars; each "word" is 3 runes.
		var b strings.Builder
		for i := 0; i < 60; i++ {
			if i > 0 {
				b.WriteByte(' ')
			}
			b.WriteString("wörld")
		}
		got := wordWrap(b.String(), indent, 160)
		for i, l := range got {
			if n := len([]rune(l)); n > 160 {
				t.Errorf("line %d exceeds the 160-RUNE width: %d runes", i, n)
			}
		}
		joined := strings.Join(strings.Fields(strings.Join(got, " ")), " ")
		if joined != b.String() {
			t.Error("wrapped text lost multibyte content")
		}
	})

	t.Run("empty_input_yields_single_indent_line", func(t *testing.T) {
		got := wordWrap("", indent, 160)
		want := []string{"     "}
		if len(got) != 1 || got[0] != want[0] {
			t.Fatalf("wordWrap(\"\") = %#v, want %#v", got, want)
		}
	})

	t.Run("width_smaller_than_indent", func(t *testing.T) {
		// Degenerate width must not crash; budget floors at 1 rune.
		got := wordWrap("ab cd", "     ", 3)
		if len(got) == 0 {
			t.Fatal("wordWrap returned no lines for non-empty input")
		}
	})

	t.Run("zero_and_negative_width_floor_budget_at_one", func(t *testing.T) {
		// budget = width - len(indent); for width ≤ 0 it is ≤ 0 and floors
		// at 1 rune, so every token lands on its own line — never a panic.
		got := wordWrap("a b c", "", 0)
		want := []string{"a", "b", "c"}
		if len(got) != len(want) {
			t.Fatalf("wordWrap width 0 = %#v, want %#v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("line %d = %q, want %q", i, got[i], want[i])
			}
		}

		gotNeg := wordWrap("ab cd", "     ", -5)
		wantNeg := []string{"     ab", "     cd"}
		if len(gotNeg) != len(wantNeg) {
			t.Fatalf("wordWrap width -5 = %#v, want %#v", gotNeg, wantNeg)
		}
		for i := range wantNeg {
			if gotNeg[i] != wantNeg[i] {
				t.Errorf("line %d = %q, want %q", i, gotNeg[i], wantNeg[i])
			}
		}
	})
}

func TestRenderToolList(t *testing.T) {
	t.Parallel()
	t.Run("no_params_short_description", func(t *testing.T) {
		none := time.Duration(0)
		tools := []discoveredTool{
			{Name: "hello", Description: "Say hello", Timeout: &none},
		}
		got := renderToolList(tools, 5*time.Minute, 160)
		want := "hello()\n     Say hello (timeout: none)\n"
		if got != want {
			t.Fatalf("renderToolList = %q, want %q", got, want)
		}
	})

	t.Run("mixed_params_and_inherited_global_timeout", func(t *testing.T) {
		tools := []discoveredTool{
			{Name: "mixed", Description: "Does things", Params: []paramSpec{
				{Name: "a", Type: "string", Required: true},
				{Name: "b", Type: "number"},
				{Name: "c", Type: "boolean"},
			}},
		}
		got := renderToolList(tools, 5*time.Minute, 160)
		want := "mixed(a:str, [b:num], [c:bool])\n     Does things (timeout: 5m0s)\n"
		if got != want {
			t.Fatalf("renderToolList = %q, want %q", got, want)
		}
	})

	t.Run("empty_description_suffix_alone", func(t *testing.T) {
		none := time.Duration(0)
		tools := []discoveredTool{{Name: "bare", Timeout: &none}}
		got := renderToolList(tools, 5*time.Minute, 160)
		want := "bare()\n     (timeout: none)\n"
		if got != want {
			t.Fatalf("renderToolList = %q, want %q", got, want)
		}
	})

	t.Run("duplicate_param_name_last_wins_first_position", func(t *testing.T) {
		tools := []discoveredTool{
			{Name: "dup", Description: "d", Params: []paramSpec{
				{Name: "x", Type: "string", Required: true},
				{Name: "a", Type: "string", Required: true},
				{Name: "y", Type: "number"},
				{Name: "a", Type: "number"},
				{Name: "z", Type: "boolean"},
			}},
		}
		got := renderToolList(tools, 5*time.Minute, 160)
		want := "dup(x:str, [a:num], [y:num], [z:bool])\n     d (timeout: 5m0s)\n"
		if got != want {
			t.Fatalf("renderToolList = %q, want %q", got, want)
		}
	})

	t.Run("plan_example_byte_exact", func(t *testing.T) {
		none := time.Duration(0)
		twoMin := 2 * time.Minute
		tools := []discoveredTool{
			{
				Name:        "meridian_build",
				Description: "Compile the Meridian workspace (or just meridian-app) on the remote host — the offload target; the long pole is the Bevy dep tree, built once then cached. Timeout is NONE (a full clean build of the Bevy dep tree can far exceed the 5-minute MCP default; re-call to see a slow build's result) — prefer scope=app for incremental work.",
				Timeout:     &none,
				Params: []paramSpec{
					{Name: "profile", Type: "string"},
					{Name: "scope", Type: "string"},
					{Name: "target", Type: "string"},
					{Name: "timings", Type: "boolean"},
				},
			},
			{
				Name:        "meridian_fetch",
				Description: "Check out the pinned commit of the Meridian repo on the remote host (idempotent) and report HEAD/branch, rustc, cargo and Cargo.lock info. Run first on a remote host before any other meridian tool.",
				Timeout:     &twoMin,
				Params:      []paramSpec{{Name: "commit", Type: "string"}},
			},
		}
		want := "meridian_build([profile:str], [scope:str], [target:str], [timings:bool])\n" +
			"     Compile the Meridian workspace (or just meridian-app) on the remote host — the offload target; the long pole is the Bevy dep tree, built once then cached.\n" +
			"     Timeout is NONE (a full clean build of the Bevy dep tree can far exceed the 5-minute MCP default; re-call to see a slow build's result) — prefer scope=app\n" +
			"     for incremental work. (timeout: none)\n" +
			"\n" +
			"meridian_fetch([commit:str])\n" +
			"     Check out the pinned commit of the Meridian repo on the remote host (idempotent) and report HEAD/branch, rustc, cargo and Cargo.lock info. Run first on a\n" +
			"     remote host before any other meridian tool. (timeout: 2m0s)\n"
		if got := renderToolList(tools, 5*time.Minute, 160); got != want {
			t.Fatalf("renderToolList did not match the spec example:\ngot:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("blank_line_between_blocks", func(t *testing.T) {
		tools := []discoveredTool{
			{Name: "a", Description: "one"},
			{Name: "b", Description: "two"},
			{Name: "c", Description: "three"},
		}
		got := renderToolList(tools, 5*time.Minute, 160)
		want := "a()\n     one (timeout: 5m0s)\n\nb()\n     two (timeout: 5m0s)\n\nc()\n     three (timeout: 5m0s)\n"
		if got != want {
			t.Fatalf("renderToolList = %q, want %q", got, want)
		}
	})

	t.Run("zero_tools_empty_output", func(t *testing.T) {
		if got := renderToolList(nil, 5*time.Minute, 160); got != "" {
			t.Fatalf("renderToolList(nil) = %q, want empty", got)
		}
	})
}
