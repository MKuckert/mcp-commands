package main

import (
	"strings"
	"time"
	"unicode"
)

const (
	listWrapWidth = 160     // non-TTY fallback for --list-tools wrapping
	listIndent    = "     " // included in the wrap width budget

)

// listParamDecl renders one parameter for a tool signature: key:shorttype if
// required, [key:shorttype] if optional. Short types: string→str,
// number→num, boolean→bool.
func listParamDecl(p paramSpec) string {
	short := p.Type
	switch p.Type {
	case "string":
		short = "str"
	case "number":
		short = "num"
	case "boolean":
		short = "bool"
	}
	if p.Required {
		return p.Name + ":" + short
	}
	return "[" + p.Name + ":" + short + "]"
}

// toolListSignature renders the name(<decls>) signature line for a tool: one
// declaration per parameter, joined by ", ". Duplicate parameter names are
// deduplicated with the last declaration winning for type/required, rendered
// at the first-occurrence position (mirrors buildInputSchema, whose
// required array keeps first-occurrence order) — so the signature always
// matches the registered schema. A tool with no parameters renders as name().
func toolListSignature(name string, params []paramSpec) string {
	normalized := normalizeParams(params)
	parts := make([]string, len(normalized))
	for i, p := range normalized {
		parts[i] = listParamDecl(p)
	}
	return name + "(" + strings.Join(parts, ", ") + ")"
}

// wordWrap word-wraps s: each returned line is indent + content, where the
// total width always includes the indent (content budget = width − len(indent)).
// Rune-based; greedy (each line is filled as far as it fits); breaks at runs
// of whitespace, which collapse to a single space; never splits a word. A
// single unbreakable token longer than the budget is emitted whole on its own
// line (visible, never truncated).
func wordWrap(s string, indent string, width int) []string {
	budget := width - len(indent)
	if budget < 1 {
		budget = 1
	}
	runes := []rune(s)
	lines := make([]string, 0, 4)
	var cur []rune
	for i, n := 0, len(runes); i < n; {
		for i < n && unicode.IsSpace(runes[i]) {
			i++
		}
		if i >= n {
			break
		}
		start := i
		for i < n && !unicode.IsSpace(runes[i]) {
			i++
		}
		word := runes[start:i]
		if len(word) > budget {
			if len(cur) > 0 {
				lines = append(lines, indent+string(cur))
				cur = nil
			}
			lines = append(lines, indent+string(word))
			continue
		}
		if len(cur) == 0 {
			cur = word
		} else if len(cur)+1+len(word) <= budget {
			cur = append(cur, ' ')
			cur = append(cur, word...)
		} else {
			lines = append(lines, indent+string(cur))
			cur = word
		}
	}
	if len(cur) > 0 {
		lines = append(lines, indent+string(cur))
	}
	if len(lines) == 0 {
		return []string{indent}
	}
	return lines
}

// renderToolList renders the --list-tools output: one block per tool, in
// discovery order, separated by exactly one blank line. Each block starts
// with the name(<decls>) signature line, followed by the registered
// description (frontmatter description plus the timeout suffix, resolved
// with the registry's precedence) word-wrapped to width (which includes the
// indent; the caller supplies resolveWrapWidth's result so the TTY query is
// re-run at every print).
func renderToolList(tools []discoveredTool, globalTimeout time.Duration, width int) string {
	var b strings.Builder
	for i, tool := range tools {
		b.WriteString(toolListSignature(tool.Name, tool.Params))
		b.WriteByte('\n')
		for _, line := range wordWrap(registeredDescription(tool.Description, resolveToolTimeout(tool, globalTimeout)), listIndent, width) {
			b.WriteString(line)
			b.WriteByte('\n')
		}
		if i < len(tools)-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}
