package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/fsnotify/fsnotify"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/term"
)

const (
	defaultToolTimeout        = 5 * time.Minute
	maxToolOutputBytes        = 1 << 20
	defaultMaxConcurrentTools = 16
	maxHTTPBodyBytes          = 10 << 20 // 10 MiB request-body cap
	httpReadHeaderTimeout     = 5 * time.Second
	httpIdleTimeout           = 2 * time.Minute
	execWaitDelay             = 5 * time.Second // post-deadline pipe-close backstop for tool subprocesses
	scanHeaderLines           = 30
	scanDescriptionPrefix     = "Description:"
	scanParamPrefix           = "Param:"
	scanTimeoutPrefix         = "Timeout:"
	watchToolsInterval        = 2 * time.Second
	watchDebounceDelay        = 100 * time.Millisecond
	serverName                = "mcp-commands"
	listWrapWidth             = 160     // non-TTY fallback for --list-tools wrapping
	listIndent                = "     " // included in the wrap width budget
)

var serverVersion = "0.8.0"

const apiKeyEnvVar = "MCP_COMMANDS_API_KEY"

const (
	allowedOriginsEnvVar  = "MCP_COMMANDS_ALLOWED_ORIGINS"
	allowAllOriginsEnvVar = "MCP_COMMANDS_ALLOW_ALL_ORIGINS"
)

type paramSpec struct {
	Name        string // validated against argumentKeyPattern
	Type        string // "string" | "number" | "boolean"
	Required    bool
	Description string
}

type discoveredTool struct {
	Name        string
	Path        string
	Description string
	Params      []paramSpec
	Timeout     *time.Duration // valid per-tool Timeout: value; nil when undeclared (global applies), &0 for NONE/0
}

// discoverTools scans the given directory for executable files and symlinks
// resolving to executables. It skips subdirectories and non-executable files.
// For each valid executable, it extracts the description and parameters, then
// constructs a discoveredTool record for later registration with the MCP server.
func discoverTools(scriptsDir string) ([]discoveredTool, error) {
	entries, err := os.ReadDir(scriptsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read scripts directory: %w", err)
	}

	var tools []discoveredTool

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		filePath := filepath.Join(scriptsDir, entry.Name())
		resolvedPath, err := filepath.EvalSymlinks(filePath)
		if err != nil {
			continue
		}

		fileInfo, err := os.Stat(resolvedPath)
		if err != nil {
			continue
		}

		// Check executable flag
		if fileInfo.Mode()&0111 == 0 {
			continue
		}

		description, params, timeout := extractFrontmatter(resolvedPath)
		name := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))

		tools = append(tools, discoveredTool{
			Name:        name,
			Path:        resolvedPath,
			Description: description,
			Params:      params,
			Timeout:     timeout,
		})
	}

	return tools, nil
}

// extractFrontmatter reads the first scanHeaderLines lines of a file in a
// single pass and collects the tool's frontmatter: the first Description:
// line (first occurrence wins; populates the MCP tool description), all
// Param: annotations (invalid ones log a stderr warning and are skipped),
// and the first Timeout: value (first occurrence wins; nil when undeclared
// so the global applies, &0 for NONE/0; an invalid value logs a stderr
// warning and yields nil so the global applies). An unreadable file yields
// zero values.
func extractFrontmatter(filePath string) (description string, params []paramSpec, timeout *time.Duration) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", []paramSpec{}, nil
	}
	defer file.Close()

	params = []paramSpec{}
	descriptionSeen := false
	timeoutSeen := false
	scanner := bufio.NewScanner(file)
	lineCount := 0

	for scanner.Scan() && lineCount < scanHeaderLines {
		lineCount++
		line := scanner.Text()

		if !descriptionSeen && strings.Contains(line, scanDescriptionPrefix) {
			parts := strings.SplitN(line, scanDescriptionPrefix, 2)
			if len(parts) == 2 {
				description = strings.TrimSpace(parts[1])
			}
			descriptionSeen = true
			continue
		}

		if !timeoutSeen && strings.Contains(line, scanTimeoutPrefix) {
			parts := strings.SplitN(line, scanTimeoutPrefix, 2)
			if len(parts) == 2 {
				if duration, err := parseTimeoutDuration(parts[1]); err != nil {
					// Warn and keep timeout nil (global applies) without
					// interrupting the scan, so later Param: lines are still collected.
					fmt.Fprintf(os.Stderr, "Warning: ignoring invalid Timeout in %s: %v\n", filePath, err)
				} else {
					timeout = &duration
				}
			}
			timeoutSeen = true
			continue
		}

		if strings.Contains(line, scanParamPrefix) {
			parts := strings.SplitN(line, scanParamPrefix, 2)
			if len(parts) == 2 {
				// Invalid annotations log their own stderr warning.
				if param, err := parseParamAnnotation(strings.TrimSpace(parts[1]), filePath, line); err == nil {
					params = append(params, param)
				}
			}
		}
	}

	return description, params, timeout
}

// timeoutUnits maps the allowed duration units to their values. Sub-second
// units are not meaningful for tool timeouts and are deliberately absent;
// tokens are matched prefix-based, so "1h30m5s" and "1h 30m 5s" both parse.
var timeoutUnits = map[string]time.Duration{
	"s": time.Second,
	"m": time.Minute,
	"h": time.Hour,
}

// parseTimeoutDuration parses a formatted timeout duration string: a list of
// <digits><unit> tokens (units s, m, h; e.g. "5m", "60s", "1h 30m 5s",
// "1h30m5s"); whitespace between tokens is optional. Sub-second units,
// decimals, signs, and bare units are rejected. The literal NONE
// (case-insensitive, surrounding whitespace trimmed) and a result of 0 both
// mean "no timeout" and yield 0.
func parseTimeoutDuration(raw string) (time.Duration, error) {
	if strings.EqualFold(strings.TrimSpace(raw), "NONE") {
		return 0, nil
	}
	var total time.Duration
	seen := false
	const maxDuration = time.Duration(math.MaxInt64)
	for rest := raw; len(rest) > 0; {
		rest = strings.TrimLeft(rest, " \t")
		if rest == "" {
			break
		}
		i := 0
		for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
			i++
		}
		if i == 0 {
			return 0, fmt.Errorf("invalid timeout %q: expected <digits><unit> (units s, m, h) or NONE", rest)
		}
		multiplier, after, ok := matchTimeoutUnit(rest[i:])
		if !ok {
			return 0, fmt.Errorf("invalid timeout unit %q: allowed units are s, m, h", rest[i:])
		}
		n, err := strconv.ParseUint(rest[:i], 10, 63)
		if err != nil {
			return 0, fmt.Errorf("invalid timeout amount %q: %v", rest[:i], err)
		}
		// Overflow guards: individually-valid terms can still multiply or sum
		// into int64 wraparound, which would silently yield a POSITIVE
		// duration. Reject before the arithmetic happens.
		if n > uint64(maxDuration)/uint64(multiplier) {
			return 0, fmt.Errorf("timeout duration %q overflows", raw)
		}
		term := time.Duration(n) * multiplier
		if total > maxDuration-term {
			return 0, fmt.Errorf("timeout duration %q overflows", raw)
		}
		total += term
		rest = after
		seen = true
	}
	if !seen {
		return 0, fmt.Errorf("timeout duration %q is empty, expected <digits><unit> (units s, m, h) or NONE", raw)
	}
	return total, nil
}

// matchTimeoutUnit matches a unit at the start of s and returns its
// multiplier plus the remainder of the string.
func matchTimeoutUnit(s string) (time.Duration, string, bool) {
	for _, unit := range []string{"s", "m", "h"} {
		if strings.HasPrefix(s, unit) {
			return timeoutUnits[unit], s[len(unit):], true
		}
	}
	return 0, "", false
}

// resolveTimeout resolves the global tool timeout from CLI flags, fail-fast
// before the server starts. --timeout and --no-timeout are mutually exclusive
// (passing both is a startup error); an explicitly-set --timeout must parse
// (an explicit --timeout= is therefore rejected); an unset --timeout yields
// defaultToolTimeout.
func resolveTimeout(timeoutFlag string, timeoutSet, noTimeout bool) (time.Duration, error) {
	if noTimeout && timeoutSet {
		return 0, fmt.Errorf("--timeout and --no-timeout are mutually exclusive")
	}
	if noTimeout {
		return 0, nil
	}
	if timeoutSet {
		return parseTimeoutDuration(timeoutFlag)
	}
	return defaultToolTimeout, nil
}

// parseParamAnnotation parses a single parameter annotation string.
// It expects format: <name> <type> <required|optional> "<description>"
// Returns error if validation fails (warning already logged to stderr).
func parseParamAnnotation(annotation, filePath, fullLine string) (paramSpec, error) {
	// Find the quoted description (everything after the last quote-wrapped string)
	// The description is the last field, wrapped in quotes
	lastQuoteIdx := strings.LastIndex(annotation, "\"")
	firstQuoteIdx := strings.Index(annotation, "\"")

	if firstQuoteIdx < 0 || lastQuoteIdx < 0 || firstQuoteIdx == lastQuoteIdx {
		fmt.Fprintf(os.Stderr, "Warning: skipping invalid Param annotation in %s because %s: %q\n",
			filePath, "description must be quoted", fullLine)
		return paramSpec{}, fmt.Errorf("malformed param annotation")
	}

	// Extract description (strip quotes)
	description := annotation[firstQuoteIdx+1 : lastQuoteIdx]

	// Extract the prefix (before the first quote)
	prefix := strings.TrimSpace(annotation[:firstQuoteIdx])

	// Split prefix into name, type, and required/optional token
	tokens := strings.Fields(prefix)
	if len(tokens) != 3 {
		fmt.Fprintf(os.Stderr, "Warning: skipping invalid Param annotation in %s because %s: %q\n",
			filePath, "expected 3 fields (name, type, required|optional)", fullLine)
		return paramSpec{}, fmt.Errorf("wrong field count")
	}

	name := tokens[0]
	paramType := tokens[1]
	requiredToken := tokens[2]

	// Validate name against argumentKeyPattern
	if !argumentKeyPattern.MatchString(name) {
		fmt.Fprintf(os.Stderr, "Warning: skipping invalid Param annotation in %s because %s: %q\n",
			filePath, fmt.Sprintf("parameter name %q must match %s", name, argumentKeyPattern.String()), fullLine)
		return paramSpec{}, fmt.Errorf("invalid parameter name")
	}

	// Validate type
	var typeOk bool
	switch paramType {
	case "string", "number", "boolean":
		typeOk = true
	default:
		typeOk = false
	}
	if !typeOk {
		fmt.Fprintf(os.Stderr, "Warning: skipping invalid Param annotation in %s because %s: %q\n",
			filePath, fmt.Sprintf("type must be string, number, or boolean, got %q", paramType), fullLine)
		return paramSpec{}, fmt.Errorf("invalid type")
	}

	// Validate required/optional token
	var required bool
	switch requiredToken {
	case "required":
		required = true
	case "optional":
		required = false
	default:
		fmt.Fprintf(os.Stderr, "Warning: skipping invalid Param annotation in %s because %s: %q\n",
			filePath, fmt.Sprintf("status must be 'required' or 'optional', got %q", requiredToken), fullLine)
		return paramSpec{}, fmt.Errorf("invalid required token")
	}

	return paramSpec{
		Name:        name,
		Type:        paramType,
		Required:    required,
		Description: description,
	}, nil
}

// parseToolArguments unmarshals the JSON arguments provided by the MCP client
// into a Go map. It handles empty or null payloads by returning an empty map,
// preventing unmarshal errors when tools are called without arguments.
func parseToolArguments(raw json.RawMessage) (map[string]any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return map[string]any{}, nil
	}

	var args map[string]any
	if err := json.Unmarshal(trimmed, &args); err != nil {
		return nil, fmt.Errorf("arguments must be a JSON object")
	}

	return args, nil
}

var argumentKeyPattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)

// buildInputSchema constructs a JSON Schema for a tool's input parameters.
// It takes a slice of paramSpec and builds a schema with a "properties" object
// and a "required" array (omitted if empty). Duplicate param names are deduplicated:
// the last declaration wins for both properties and required status.
//
// Returns a json.RawMessage containing:
//
//	{"type":"object","properties":{...},"required":[...]}
//
// The "required" key is omitted entirely if no params are required.
func buildInputSchema(params []paramSpec) json.RawMessage {
	properties := make(map[string]any)
	lastRequired := make(map[string]bool)

	// First pass: build properties map and track last-seen required status
	for _, param := range params {
		properties[param.Name] = map[string]any{
			"type":        param.Type,
			"description": param.Description,
		}
		lastRequired[param.Name] = param.Required
	}

	// Build the schema object
	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}

	// Second pass: collect required names (using last-seen required status)
	var requiredNames []string
	seen := make(map[string]bool)
	for _, param := range params {
		if !seen[param.Name] && lastRequired[param.Name] {
			requiredNames = append(requiredNames, param.Name)
			seen[param.Name] = true
		}
	}

	// Only add "required" key if there are required params
	if len(requiredNames) > 0 {
		schema["required"] = requiredNames
	}

	return mustJSONMarshal(schema)
}

// argumentsToCLIArgs converts a map of parsed arguments into a slice of CLI flags
// formatted for execution. It enforces strict naming rules for keys to prevent
// injection or ambiguity. Boolean values follow POSIX conventions (true -> --flag,
// false -> omitted). Slices are expanded into multiple flags (e.g., --key val1 --key val2).
func argumentsToCLIArgs(args map[string]any) ([]string, error) {
	if len(args) == 0 {
		return nil, nil
	}

	keys := make([]string, 0, len(args))
	for key := range args {
		if !argumentKeyPattern.MatchString(key) {
			// Reject digit-leading keys so we never generate ambiguous flags that can
			// be parsed as positional arguments or mistaken for numeric values.
			return nil, fmt.Errorf("invalid argument key %q: keys must match %s", key, argumentKeyPattern.String())
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)

	cliArgs := make([]string, 0, len(args)*2)
	for _, key := range keys {
		value := args[key]

		// Handle boolean values via type-switch, not string comparison.
		// Per POSIX/GNU conventions: true → emit flag only; false/nil → omit entirely.
		// This does not affect string values "true"/"false", which are still passed as-is.
		if boolVal, isBool := value.(bool); isBool {
			if boolVal {
				// true: emit flag with no value argument
				cliArgs = append(cliArgs, "--"+key)
			}
			// false: omit flag entirely
			continue
		}

		// nil values are omitted entirely (previously emitted as --key "")
		if value == nil {
			continue
		}

		rv := reflect.ValueOf(value)
		if rv.IsValid() && rv.Kind() == reflect.Slice && rv.Type().Elem().Kind() != reflect.Uint8 {
			for i := 0; i < rv.Len(); i++ {
				cliArgs = append(cliArgs, "--"+key, fmt.Sprint(rv.Index(i).Interface()))
			}
			continue
		}

		cliArgs = append(cliArgs, "--"+key, fmt.Sprint(value))
	}

	return cliArgs, nil
}

// boundedWriter is an io.Writer that accumulates at most limit bytes. Writes
// beyond the limit are consumed (the return value stays len(p), err nil, so
// the subprocess sees a healthy pipe) but not stored; the overflow is
// remembered. This bounds executeTool's memory to O(limit) per stream no
// matter how much a tool prints.
type boundedWriter struct {
	buf       bytes.Buffer
	remaining int
	truncated bool
}

func newBoundedWriter(limit int) *boundedWriter {
	return &boundedWriter{remaining: limit}
}

func (b *boundedWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if b.remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > b.remaining {
		b.buf.Write(p[:b.remaining])
		b.remaining = 0
		b.truncated = true
		return len(p), nil
	}
	// A write that lands exactly on the limit drops nothing: truncated stays false.
	b.buf.Write(p)
	b.remaining -= len(p)
	return len(p), nil
}

// Bytes returns the captured bytes (at most the limit) and whether output
// was dropped to enforce it.
func (b *boundedWriter) Bytes() []byte { return b.buf.Bytes() }

func (b *boundedWriter) Truncated() bool { return b.truncated }

// combineToolOutput merges stdout and stderr from a tool execution, wrapping each
// in XML-style tags (<stdout>...</stdout> and <stderr>...</stderr>). It enforces
// a maximum byte limit (maxToolOutputBytes) to prevent overwhelming the MCP client
// with massive outputs, appending a truncation warning if the limit is exceeded.
func combineToolOutput(stdout, stderr []byte) string {
	var result bytes.Buffer

	// Write stdout tag if present
	if len(stdout) > 0 {
		result.WriteString("<stdout>\n")
		result.Write(stdout)
		if !bytes.HasSuffix(stdout, []byte("\n")) {
			result.WriteByte('\n')
		}
		result.WriteString("</stdout>")
	}

	// Write stderr tag if present
	if len(stderr) > 0 {
		if result.Len() > 0 {
			result.WriteByte('\n')
		}
		result.WriteString("<stderr>\n")
		result.Write(stderr)
		if !bytes.HasSuffix(stderr, []byte("\n")) {
			result.WriteByte('\n')
		}
		result.WriteString("</stderr>")
	}

	combined := result.Bytes()
	if len(combined) <= maxToolOutputBytes {
		return string(combined)
	}

	// Back off the cut to a UTF-8 rune boundary: a naive byte cut can split a
	// multi-byte rune and hand the LLM invalid bytes. A split rune spans at
	// most 3 bytes, so at most 3 back-offs; for tool output that is already
	// invalid UTF-8 (binary) the bounded loop simply stops, no worse than the
	// raw bytes we would have emitted before.
	cut := maxToolOutputBytes
	for i := 0; i < 3 && cut > 0 && !utf8.Valid(combined[:cut]); i++ {
		cut--
	}
	return string(combined[:cut]) + fmt.Sprintf("\n[output truncated after %d bytes]", maxToolOutputBytes)
}

// toolRegistry manages the dynamic registration and deregistration of tools
// within the MCP server. It ensures thread-safe updates via a mutex, allowing
// tools to be swapped out at runtime when changes are detected in the scripts directory.
type toolRegistry struct {
	server        *mcp.Server
	dirAbs        string
	globalTimeout time.Duration // applied to tools without a per-tool Timeout:
	slot          *execSlot     // bounds concurrent tool executions
	mu            sync.Mutex
	names         []string
	lastHandler   mcp.ToolHandler
}

func newToolRegistry(server *mcp.Server, dir string, globalTimeout time.Duration, maxConcurrent int) *toolRegistry {
	return &toolRegistry{
		server:        server,
		dirAbs:        dir,
		globalTimeout: globalTimeout,
		slot:          newExecSlot(maxConcurrent),
	}
}

// execSlot bounds how many tool subprocesses may run at once. It is a
// fixed-size buffered channel: each running execution holds one slot.
type execSlot struct {
	sem   chan struct{}
	limit int
}

func newExecSlot(limit int) *execSlot {
	if limit <= 0 {
		limit = defaultMaxConcurrentTools
	}
	return &execSlot{sem: make(chan struct{}, limit), limit: limit}
}

// tryAcquire grabs a slot without blocking: false means the server is at
// capacity (the caller returns a clean "at capacity" tool result).
func (s *execSlot) tryAcquire() bool {
	select {
	case s.sem <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *execSlot) release() { <-s.sem }

// replace unregisters all currently tracked tools and registers a new set of tools.
// It defines the InputSchema dynamically based on each tool's Param declarations,
// allowing tools to accept typed parameters with proper schema validation.
func (r *toolRegistry) replace(tools []discoveredTool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.names) > 0 {
		r.server.RemoveTools(r.names...)
	}

	r.names = make([]string, 0, len(tools))
	for _, discoveredTool := range tools {
		toolName := discoveredTool.Name
		toolPath := discoveredTool.Path
		toolParams := discoveredTool.Params

		// A per-tool Timeout: always wins over the global, even --no-timeout.
		toolTimeout := resolveToolTimeout(discoveredTool, r.globalTimeout)

		description := registeredDescription(discoveredTool.Description, toolTimeout)

		handlerFunc := mcp.ToolHandler(func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			parsedArgs, err := parseToolArguments(req.Params.Arguments)
			if err != nil {
				return nil, err
			}
			if err := validateRequiredParams(parsedArgs, toolParams); err != nil {
				return &mcp.CallToolResult{
					Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
					IsError: true,
				}, nil
			}
			// Capacity check after validation: malformed calls must not
			// consume a slot. At capacity, fail cleanly so clients retry
			// rather than piling up pinned subprocesses.
			if !r.slot.tryAcquire() {
				msg := fmt.Sprintf("mcp-commands is at capacity (%d concurrent tool executions); please retry shortly", r.slot.limit)
				return &mcp.CallToolResult{
					Content: []mcp.Content{&mcp.TextContent{Text: msg}},
					IsError: true,
				}, nil
			}
			defer r.slot.release()
			return executeTool(ctx, toolPath, parsedArgs, toolTimeout, r.dirAbs)
		})

		r.server.AddTool(&mcp.Tool{
			Name:        toolName,
			Description: description,
			InputSchema: buildInputSchema(toolParams),
		}, handlerFunc)

		r.lastHandler = handlerFunc
		r.names = append(r.names, toolName)
	}
}

// resolveToolTimeout resolves a tool's effective timeout with the registry's
// precedence: a per-tool Timeout: always wins over the global, even
// --no-timeout. Shared by the registry and the --list-tools renderer so the
// two call sites cannot drift.
func resolveToolTimeout(tool discoveredTool, global time.Duration) time.Duration {
	if tool.Timeout != nil {
		return *tool.Timeout
	}
	return global
}

// registeredDescription assembles the registered tool description: the
// frontmatter description plus the " (timeout: …)" suffix (the suffix alone
// when the description is empty). Shared by the registry and the
// --list-tools renderer so the two never drift apart.
func registeredDescription(desc string, timeout time.Duration) string {
	suffix := timeoutSuffix(timeout)
	if desc == "" {
		return suffix
	}
	return desc + " " + suffix
}

// timeoutSuffix renders the resolved timeout for the registered tool
// description so the LLM knows its budget: "(timeout: 30s)" or
// "(timeout: none)" when no deadline applies.
func timeoutSuffix(timeout time.Duration) string {
	if timeout > 0 {
		return fmt.Sprintf("(timeout: %s)", timeout)
	}
	return "(timeout: none)"
}

// resolveWrapWidth returns the wrap width for --list-tools output: the
// terminal window width (in runes) when stdout is a TTY (re-queried at every
// print so window resizes are honored), falling back to listWrapWidth when
// stdout is not a *os.File, not a terminal, or the query fails.
var resolveWrapWidth = func(stdout io.Writer) int {
	file, ok := stdout.(*os.File)
	if !ok {
		return listWrapWidth
	}
	fd := int(file.Fd())
	if !term.IsTerminal(fd) {
		return listWrapWidth
	}
	width, _, err := term.GetSize(fd)
	if err != nil || width <= 0 {
		return listWrapWidth
	}
	return width
}

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
	decls := make(map[string]string, len(params))
	order := make([]string, 0, len(params))
	for _, p := range params {
		if _, seen := decls[p.Name]; !seen {
			order = append(order, p.Name)
		}
		decls[p.Name] = listParamDecl(p) // last occurrence wins
	}
	parts := make([]string, len(order))
	for i, n := range order {
		parts[i] = decls[n]
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

func mustJSONMarshal(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}

// watchChanges watches dir with fsnotify and invokes onChange once per
// debounced burst of Create/Write/Remove/Rename events. Watcher errors are
// logged to stderr but do not stop the loop, ensuring robust operation even
// if the watched directory is deleted or permissions change. It returns when
// ctx is done or the watcher channels close.
func watchChanges(ctx context.Context, dir string, onChange func()) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create watcher: %w", err)
	}
	defer watcher.Close()

	if err := watcher.Add(dir); err != nil {
		return fmt.Errorf("failed to watch directory: %w", err)
	}

	debounceTimer := time.NewTimer(watchDebounceDelay)
	// NewTimer arms the clock immediately; disarm it right away so onChange
	// only fires after a file event Resets the timer. (Go >= 1.23 Stop drains
	// the channel, so no stale tick can be in flight.)
	debounceTimer.Stop()
	defer debounceTimer.Stop()
	debounceActive := false

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case event, ok := <-watcher.Events:
			if !ok {
				return fmt.Errorf("watcher channel closed unexpectedly")
			}

			// Check if the event is for a file in the scripts directory
			// We look for Create, Write, Remove, and Rename operations
			if event.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Remove|fsnotify.Rename) != 0 {
				if !debounceActive {
					debounceActive = true
					debounceTimer.Reset(watchDebounceDelay)
				}
			}

		case err, ok := <-watcher.Errors:
			if !ok {
				return fmt.Errorf("watcher error channel closed unexpectedly")
			}
			// Log the error but don't crash the watcher
			// This handles cases like permission denied, file not found, etc.
			fmt.Fprintf(os.Stderr, "Warning: file watcher error: %v\n", err)

		case <-debounceTimer.C:
			debounceActive = false
			onChange()
		}
	}
}

// watchTools runs a continuous loop that watches the scripts directory for
// changes, re-discovering and re-registering the tools on every debounced
// change (built on watchChanges; the interval parameter is retained for the
// existing call sites but the debounce is the fixed watchDebounceDelay).
func watchTools(ctx context.Context, scriptsDir string, registry *toolRegistry, interval time.Duration) error {
	// Initial discovery
	tools, err := discoverTools(scriptsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: initial tool discovery failed: %v\n", err)
	} else {
		registry.replace(tools)
	}

	return watchChanges(ctx, scriptsDir, func() {
		// After debounce delay, rediscover tools
		tools, err := discoverTools(scriptsDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to rediscover tools: %v\n", err)
			return
		}

		registry.replace(tools)
	})
}

func validateRequiredParams(args map[string]any, params []paramSpec) error {
	for _, p := range params {
		if p.Required {
			if _, ok := args[p.Name]; !ok {
				return fmt.Errorf("missing required parameter: %s", p.Name)
			}
		}
	}
	return nil
}

// executeTool runs the script at scriptPath as a subprocess in the specified
// working directory. It accepts pre-parsed arguments as a map, converts them to
// CLI flags, and binds the context to a timeout to prevent hanging tools. On
// cancellation (deadline or client abort) the whole tool process group is
// killed, so shell-script grandchildren die with the budget (F-7). The output
// is captured, combined, and returned as an MCP CallToolResult.
func executeTool(ctx context.Context, scriptPath string, args map[string]any, timeout time.Duration, dir string) (*mcp.CallToolResult, error) {
	cliArgs, err := argumentsToCLIArgs(args)
	if err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
			IsError: true,
		}, nil
	}

	execCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	// With timeout == 0 the request context itself is used: client
	// cancellation/abort still kills the script, so "no timeout" means
	// "no deadline", never "uninterruptible".

	cmd := exec.CommandContext(execCtx, scriptPath, cliArgs...)
	cmd.Dir = dir

	// A deadline is a budget for the whole process tree, not just the direct
	// child: shell scripts spawn grandchildren, and a surviving descendant
	// can also hold the pipes open past the kill (F-7).
	applyProcessGroup(cmd, execWaitDelay)

	// Bounded capture: a tool printing gigabytes costs O(1 MiB) per stream,
	// not O(output size); combineToolOutput applies the final cap.
	stdout := newBoundedWriter(maxToolOutputBytes)
	stderr := newBoundedWriter(maxToolOutputBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	waitErr := cmd.Wait()
	combinedOutput := combineToolOutput(stdout.Bytes(), stderr.Bytes())

	if waitErr != nil {
		if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
			message := fmt.Sprintf("tool timed out after %s", timeout)
			if combinedOutput != "" {
				message += "\n" + combinedOutput
			}
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: message}},
				IsError: true,
			}, nil
		}

		if combinedOutput == "" {
			combinedOutput = waitErr.Error()
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: combinedOutput}},
			IsError: true,
		}, nil
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: combinedOutput}},
	}, nil
}

// resolveToolPaths resolves --dir/--scripts to absolute paths and verifies
// both are accessible. Shared by the server-mode run() and the diagnostic
// branch so the resolution behavior and error text stay identical in all
// modes.
func resolveToolPaths(dir, scriptsDir string) (dirAbs, scriptsAbs string, err error) {
	scriptsAbs, err = filepath.Abs(scriptsDir)
	if err != nil {
		return "", "", fmt.Errorf("failed to resolve scripts path: %w", err)
	}
	dirAbs, err = filepath.Abs(dir)
	if err != nil {
		return "", "", fmt.Errorf("failed to resolve dir path: %w", err)
	}
	if _, err := os.Stat(scriptsAbs); err != nil {
		return "", "", fmt.Errorf("scripts path inaccessible: %w", err)
	}
	if _, err := os.Stat(dirAbs); err != nil {
		return "", "", fmt.Errorf("dir path inaccessible: %w", err)
	}
	return dirAbs, scriptsAbs, nil
}

// clearScreen clears the terminal (ANSI erase-screen + cursor-home). It is a
// no-op when stdout is not a TTY, so piped output simply accumulates. It is
// a package variable so tests can inject a recorder.
var clearScreen = func(stdout io.Writer) {
	file, ok := stdout.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return
	}
	_, _ = stdout.Write([]byte("\x1b[2J\x1b[H"))
}

// notifySignals wraps signal.NotifyContext as a package variable so tests can
// inject a cancelable context in place of real SIGINT/SIGTERM.
var notifySignals = func(ctx context.Context, sig ...os.Signal) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(ctx, sig...)
}

// serverModeFlagNames lists the flags that configure the MCP server, in a
// stable order for the diagnostic-mode ignored-flags notice. (--watch is
// honored in --list-tools mode; with --call-tool it is added to the notice
// separately.)
var serverModeFlagNames = []string{"host", "port", "api-key", "api-key-file", "allowed-origins", "allow-all-origins", "disable-localhost-protection", "insecure-no-auth", "max-concurrent"}

// runDiagnostic runs a diagnostic mode (--list-tools or --call-tool) and
// returns the process exit code. Diagnostics never start the MCP server:
// the process exits after the diagnostic completes, except the live
// --list-tools --watch mode, which runs until SIGINT/SIGTERM. Result content
// goes to stdout; warnings and operational errors go to stderr.
func runDiagnostic(stdout, stderr io.Writer, dir, scriptsDir string, listTools, watch bool, callTool string, callToolSet bool, paramsRaw string, timeout time.Duration, ignoredFlags []string) int {
	if len(ignoredFlags) > 0 {
		fmt.Fprintf(stderr, "Note: ignoring server-mode flags in diagnostic mode: %s\n", strings.Join(ignoredFlags, ", "))
	}
	if listTools {
		return runListTools(stdout, stderr, dir, scriptsDir, watch, timeout)
	}
	if callTool == "" {
		// Only reachable when --call-tool= was explicitly passed (an
		// omitted flag is handled by main and never reaches here).
		fmt.Fprintln(stderr, "Error: --call-tool requires a non-empty tool name")
		return 1
	}
	dirAbs, scriptsAbs, err := resolveToolPaths(dir, scriptsDir)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	code, err := runCallTool(stdout, scriptsAbs, dirAbs, timeout, callTool, paramsRaw)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
	}
	return code
}

// runCallTool is the --call-tool diagnostic: run a single discovered tool
// through the same execution path as the MCP handler (required-param
// validation, JSON→CLI-arg translation, timeout resolution identical to the
// registry — a per-tool Timeout: wins, Timeout: NONE ⇒ no deadline) and
// print the result text to the given stdout writer (the dispatch passes
// os.Stdout; tests pass a buffer). It returns the process exit code: 0 on
// success; 1 on any failure. Execution failures (missing required param,
// non-zero script exit, timeout) print the tool's result content to stdout
// with a nil error; operational failures (discovery, unknown tool, --params
// parse, unstartable script) yield a non-nil error for the stderr "Error:"
// line and never start the script. A non-object --params is rejected by
// parseToolArguments, which maps an explicitly empty value and JSON null to
// {} (same leniency as the MCP handler).
func runCallTool(stdout io.Writer, scriptsAbs, dirAbs string, globalTimeout time.Duration, name, paramsRaw string) (int, error) {
	tools, err := discoverTools(scriptsAbs)
	if err != nil {
		return 1, fmt.Errorf("failed to discover tools: %w", err)
	}

	var tool discoveredTool
	found := false
	for _, candidate := range tools {
		if candidate.Name == name {
			tool = candidate
			found = true
			break
		}
	}
	if !found {
		msg := fmt.Sprintf("unknown tool %q", name)
		if len(tools) > 0 {
			names := make([]string, len(tools))
			for i, candidate := range tools {
				names[i] = candidate.Name
			}
			msg += "; available tools: " + strings.Join(names, ", ")
		}
		return 1, errors.New(msg)
	}

	args, err := parseToolArguments([]byte(paramsRaw))
	if err != nil {
		return 1, fmt.Errorf("invalid --params %q: %w", paramsRaw, err)
	}

	if err := validateRequiredParams(args, tool.Params); err != nil {
		fmt.Fprintln(stdout, err.Error())
		return 1, nil
	}

	result, err := executeTool(context.Background(), tool.Path, args, resolveToolTimeout(tool, globalTimeout), dirAbs)
	if err != nil {
		return 1, fmt.Errorf("failed to run tool %q: %w", name, err)
	}

	var b strings.Builder
	for _, content := range result.Content {
		text, ok := content.(*mcp.TextContent)
		if !ok {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(text.Text)
	}
	fmt.Fprintln(stdout, b.String())

	if result.IsError {
		return 1, nil
	}
	return 0, nil
}

// runListTools is the --list-tools diagnostic: discover and print the tool
// list (renderToolList, width re-queried at every print) to stdout, then
// exit 0. With watch it becomes a live list: after the initial print, every
// debounced change to the scripts directory clears the screen (TTY only) and
// re-prints the full list with the existing per-scan stderr warnings, until
// the process is signaled. Path resolution errors are a startup failure
// (stderr, exit 1).
func runListTools(stdout, stderr io.Writer, dir, scriptsDir string, watch bool, timeout time.Duration) int {
	_, scriptsAbs, err := resolveToolPaths(dir, scriptsDir)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}

	printList := func(tools []discoveredTool) {
		if len(tools) == 0 {
			fmt.Fprintf(stderr, "Warning: No executable scripts found in %s\n", scriptsAbs)
		}
		fmt.Fprint(stdout, renderToolList(tools, timeout, resolveWrapWidth(stdout)))
	}

	tools, err := discoverTools(scriptsAbs)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	printList(tools)

	if !watch {
		return 0
	}

	sigCtx, cancel := notifySignals(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := watchChanges(sigCtx, scriptsAbs, func() {
		clearScreen(stdout)
		tools, err := discoverTools(scriptsAbs)
		if err != nil {
			fmt.Fprintf(stderr, "Warning: failed to rediscover tools: %v\n", err)
			return
		}
		printList(tools)
	}); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(stderr, "Warning: watch loop stopped: %v\n", err)
	}
	return 0
}

// resolveAPIKey returns the token from --api-key if non-empty, then from the
// --api-key-file content, then from MCP_COMMANDS_API_KEY. Returns "" if none
// is set. (--api-key stays first so existing setups are unchanged; the file
// is the preferred middle tier — a flag value is readable via
// /proc/<pid>/cmdline for the server's lifetime, which main() warns about.)
func resolveAPIKey(flagValue, fileValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if fileValue != "" {
		return fileValue
	}
	return os.Getenv(apiKeyEnvVar)
}

// isLoopbackHost reports whether host binds only to the local machine: the
// 127.0.0.0/8 range, ::1, and the name "localhost". Anything else —
// including unparseable values and non-IP hostnames — is treated as
// non-loopback, the conservative choice: a hostname that resolves outside
// loopback binds externally.
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		return v4[0] == 127
	}
	return ip.Equal(net.ParseIP("::1"))
}

// checkHTTPSecurityPolicy validates the auth posture of an HTTP bind. It
// returns a warning to log when the server runs unauthenticated, and an
// error when it would start unauthenticated on a non-loopback host without
// the explicit --insecure-no-auth escape hatch (an unguarded remote
// command-execution endpoint).
func checkHTTPSecurityPolicy(host, apiKey string, acceptsRisk bool) (warning string, err error) {
	if apiKey != "" || isLoopbackHost(host) {
		return "", nil
	}
	if !acceptsRisk {
		return "", fmt.Errorf("refusing to start unauthenticated HTTP server on non-loopback host %q: set --api-key/--api-key-file (or %s), or pass --insecure-no-auth explicitly to accept the risk", host, apiKeyEnvVar)
	}
	return fmt.Sprintf("WARNING: UNAUTHENTICATED HTTP server bound to %q — anyone who can reach it can execute scripts as the server user (authorized via --insecure-no-auth)", host), nil
}

// corsConfig holds the resolved CORS and streamable-HTTP mode options for the
// HTTP transport.
type corsConfig struct {
	origins                    []string // exact origin allowlist
	allowAll                   bool     // echo any Origin (dev)
	disableLocalhostProtection bool     // StreamableHTTPOptions.DisableLocalhostProtection
}

// enabled reports whether any CORS behavior is requested.
func (c corsConfig) enabled() bool { return len(c.origins) > 0 || c.allowAll }

// originSet returns the allowlist as a map for membership checks.
func (c corsConfig) originSet() map[string]bool {
	set := make(map[string]bool, len(c.origins))
	for _, origin := range c.origins {
		set[origin] = true
	}
	return set
}

// summary is the startup-log fragment for enabled CORS.
func (c corsConfig) summary() string {
	if c.allowAll {
		return "CORS: any origin — dev mode"
	}
	return fmt.Sprintf("CORS: %d origin(s)", len(c.origins))
}

// parseBoolEnv parses a boolean environment variable value: "" → false;
// "1"/"true"/"yes" (case-insensitive) → true; anything else → error (fail
// loud, no silent misparse).
func parseBoolEnv(name, value string) (bool, error) {
	switch strings.ToLower(value) {
	case "":
		return false, nil
	case "1", "true", "yes":
		return true, nil
	default:
		return false, fmt.Errorf("%s must be 1, true, or yes (case-insensitive), got %q", name, value)
	}
}

// validateOrigin requires an exact http/https origin: a parsable URL with an
// http or https scheme, a non-empty host, and no path, userinfo, query, or
// fragment (https://host[:port] only).
func validateOrigin(origin string) error {
	u, err := url.Parse(origin)
	if err != nil {
		return fmt.Errorf("invalid origin %q: %v", origin, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("invalid origin %q: scheme must be http or https", origin)
	}
	if u.Hostname() == "" {
		return fmt.Errorf("invalid origin %q: missing host", origin)
	}
	if u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("invalid origin %q: must be exactly scheme://host[:port]", origin)
	}
	return nil
}

// resolveCORS resolves flags over env and validates origins. Returns an
// error for malformed origins or a contradictory --allowed-origins +
// --allow-all-origins combination. allowedOriginsFlag is empty when the flag
// was not given (the flag's zero value is unset, so "flag wins" is
// well-defined); for the bool allowAllFlag, allowAllSet distinguishes an
// explicit --allow-all-origins[=false] from a plain default, and the env var
// is consulted only when the flag was not set at all.
func resolveCORS(allowedOriginsFlag string, allowAllFlag, allowAllSet, disableLocalhostProtection bool) (corsConfig, error) {
	originsRaw := allowedOriginsFlag
	if originsRaw == "" {
		originsRaw = os.Getenv(allowedOriginsEnvVar)
	}

	allowAll := allowAllFlag
	if !allowAllSet {
		parsed, err := parseBoolEnv(allowAllOriginsEnvVar, os.Getenv(allowAllOriginsEnvVar))
		if err != nil {
			return corsConfig{}, err
		}
		allowAll = parsed
	}

	// Check the contradiction before validating origins, so a bad origin
	// string doesn't mask the more actionable error.
	if len(strings.TrimSpace(originsRaw)) > 0 && allowAll {
		return corsConfig{}, fmt.Errorf("--allowed-origins and --allow-all-origins are mutually exclusive")
	}

	var origins []string
	for _, part := range strings.Split(originsRaw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if err := validateOrigin(part); err != nil {
			return corsConfig{}, err
		}
		origins = append(origins, part)
	}

	return corsConfig{
		origins:                    origins,
		allowAll:                   allowAll,
		disableLocalhostProtection: disableLocalhostProtection,
	}, nil
}

// newBearerAuthHandler wraps next, requiring an
// "Authorization: Bearer <token>" header that matches token.
// Mismatches get 401 with a WWW-Authenticate: Bearer header.
func newBearerAuthHandler(next http.Handler, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scheme, got, ok := strings.Cut(r.Header.Get("Authorization"), " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || got == "" {
			reject(w)
			return
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			reject(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// reject writes a 401 with the WWW-Authenticate header per RFC 6750.
func reject(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte("unauthorized"))
}

const (
	corsAllowMaxAge = "900" // 15 min; browsers cap at 7200s
	// The go-sdk exposes no Last-Event-ID/Mcp-Session-Id constants, so the
	// exposed header names stay local. (The method list has no CORS
	// constants in net/http, but the methods do — see corsAllowedMethods.)
	corsExposedHeaders = "Mcp-Session-Id, Last-Event-ID"
)

// The transport is always stateless; the SDK 405s GET/DELETE.
var corsAllowedMethods = []string{http.MethodPost, http.MethodOptions}

// newCORSHandler allows cross-origin browser requests from allowed
// origins. Requests without an Origin header, and origins not on the
// allowlist (and not covered by allowAll), pass through with no CORS
// headers — the browser then blocks the response itself.
func newCORSHandler(next http.Handler, cfg corsConfig) http.Handler {
	allowed := cfg.originSet()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" || (!cfg.allowAll && !allowed[origin]) {
			next.ServeHTTP(w, r)
			return
		}
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", origin) // echo, never "*"
		h.Add("Vary", "Origin")
		h.Set("Access-Control-Expose-Headers", corsExposedHeaders)
		if r.Method == http.MethodOptions { // preflight
			h.Set("Access-Control-Allow-Methods", strings.Join(corsAllowedMethods, ", "))
			if achr := r.Header.Get("Access-Control-Request-Headers"); achr != "" {
				h.Set("Access-Control-Allow-Headers", achr)
			}
			h.Set("Access-Control-Max-Age", corsAllowMaxAge)
			w.WriteHeader(http.StatusNoContent)
			return // never call next for preflight
		}
		next.ServeHTTP(w, r)
	})
}

// buildHTTPHandler returns the streamable MCP handler, always constructed
// stateless (the app keeps no per-session state, so protocol sessions are
// vestigial). It is wrapped (innermost) in a request-body size limit
// (maxHTTPBodyBytes); when token is non-empty it is wrapped in bearer-token
// auth middleware; when CORS is enabled it is wrapped (outermost) in the
// CORS middleware, so preflights bypass auth and 401s carry CORS headers.
func buildHTTPHandler(server *mcp.Server, token string, cors corsConfig) http.Handler {
	var h http.Handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, &mcp.StreamableHTTPOptions{
		// Always stateless: the app keeps no per-session state, so protocol
		// sessions are vestigial.
		Stateless:                  true,
		DisableLocalhostProtection: cors.disableLocalhostProtection,
	})
	// Bound request bodies (innermost: below auth and CORS) — a multi-GB
	// chunked body must not be read into memory.
	next := h
	h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxHTTPBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
	if token != "" {
		h = newBearerAuthHandler(h, token)
	}
	if cors.enabled() {
		h = newCORSHandler(h, cors) // outermost: preflight unauthenticated, 401s carry CORS
	}
	return h
}

func main() {
	dirFlag := flag.String("dir", "", "Working directory for tool execution (required)")
	scriptsFlag := flag.String("scripts", "", "Directory containing executable scripts (required)")
	watchFlag := flag.Bool("watch", false, "Watch for tool changes: hot-reload in server mode, live re-print in --list-tools mode (ignored with --call-tool)")
	hostFlag := flag.String("host", "127.0.0.1", "IP address for HTTP server")
	portFlag := flag.Int("port", 0, "Port for HTTP server (don't set or 0 for stdio mode)")
	apiKeyFlag := flag.String("api-key", "", "API token required by HTTP clients (or --api-key-file or MCP_COMMANDS_API_KEY; the flag value is visible in the process list)")
	apiKeyFileFlag := flag.String("api-key-file", "", "File containing the API token (preferred over --api-key, whose value is world-readable in /proc/<pid>/cmdline)")
	insecureNoAuthFlag := flag.Bool("insecure-no-auth", false, "Allow an unauthenticated HTTP server on a non-loopback host (loudly warned; never use in production)")
	maxConcurrentFlag := flag.Int("max-concurrent", defaultMaxConcurrentTools, "Maximum concurrent tool executions (0 for default; calls beyond the cap get a clean at-capacity error)")
	allowedOriginsFlag := flag.String("allowed-origins", "", "Comma-separated exact origin allowlist for CORS (or set MCP_COMMANDS_ALLOWED_ORIGINS)")
	allowAllOriginsFlag := flag.Bool("allow-all-origins", false, "Echo any Origin header for CORS, dev convenience (or set MCP_COMMANDS_ALLOW_ALL_ORIGINS)")
	disableLocalhostProtectionFlag := flag.Bool("disable-localhost-protection", false, "Disable the SDK's DNS-rebinding protection for loopback servers")
	versionFlag := flag.Bool("version", false, "Print version and exit")
	timeoutFlag := flag.String("timeout", "", "Global per-tool timeout as a formatted duration (e.g. 5m, 1h 30m 5s, NONE); default 5m")
	noTimeoutFlag := flag.Bool("no-timeout", false, "Disable the global tool timeout (mutually exclusive with --timeout)")
	listToolsFlag := flag.Bool("list-tools", false, "List the discovered tools (name, signature, description) and exit; no server is started. With --watch: re-print the list live on script changes")
	callToolFlag := flag.String("call-tool", "", "Run one discovered tool by name and exit (debug mode; no server is started)")
	paramsFlag := flag.String("params", "{}", "JSON object of named arguments for --call-tool (default: empty object; required-param validation applies)")
	flag.Parse()

	if *versionFlag {
		fmt.Println(serverVersion)
		os.Exit(0)
	}

	if *dirFlag == "" || *scriptsFlag == "" {
		fmt.Fprintf(os.Stderr, "Error: --dir and --scripts are required\n")
		fmt.Fprintf(os.Stderr, "Usage: mcp-commands --dir <directory> --scripts <directory> [--list-tools [--watch]] | [--call-tool <name> --params <json>] | [--watch] [--host <host>] [--port <port>] [--api-key <token>] [--api-key-file <path>] [--allowed-origins <origin[,origin...]>]|[--allow-all-origins] [--disable-localhost-protection] [--insecure-no-auth] [--max-concurrent <n>] [--timeout <duration>] | [--no-timeout]\n")
		os.Exit(1)
	}

	// --api-key is world-readable in /proc/<pid>/cmdline for the server's
	// lifetime; the file/env tiers are not (F-8). The file is read fail-fast
	// so an unreadable path is a startup error, not a silent no-auth.
	var apiKeyFileValue string
	if *apiKeyFileFlag != "" {
		raw, err := os.ReadFile(*apiKeyFileFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: cannot read --api-key-file %q: %v\n", *apiKeyFileFlag, err)
			os.Exit(1)
		}
		apiKeyFileValue = strings.TrimSpace(string(raw))
	}

	// Resolved and validated here (all modes, fail-fast); run only consumes it.
	allowAllSet := false
	timeoutSet := false
	callToolSet := false
	visited := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) {
		visited[f.Name] = true
		switch f.Name {
		case "allow-all-origins":
			allowAllSet = true
		case "timeout":
			timeoutSet = true
		case "call-tool":
			callToolSet = true
		}
	})
	cors, err := resolveCORS(*allowedOriginsFlag, *allowAllOriginsFlag, allowAllSet, *disableLocalhostProtectionFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Resolved and validated here (all modes, fail-fast); run only consumes it.
	timeout, err := resolveTimeout(*timeoutFlag, timeoutSet, *noTimeoutFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	callToolActive := *callToolFlag != "" || callToolSet
	if *listToolsFlag && callToolActive {
		fmt.Fprintln(os.Stderr, "Error: --list-tools and --call-tool are mutually exclusive")
		os.Exit(1)
	}
	if *maxConcurrentFlag < 0 {
		fmt.Fprintf(os.Stderr, "Error: --max-concurrent must be >= 0 (got %d)\n", *maxConcurrentFlag)
		os.Exit(1)
	}
	if *listToolsFlag || callToolActive {
		// --watch is honored with --list-tools (live list) and ignored with
		// --call-tool; the server-mode flags are always ignored. Presence is
		// visit-tracked, so default-valued forms (--port=0, --host=127.0.0.1,
		// --watch=false) are noticed too.
		var ignored []string
		for _, name := range serverModeFlagNames {
			if visited[name] {
				ignored = append(ignored, "--"+name)
			}
		}
		if callToolActive && visited["watch"] {
			ignored = append(ignored, "--watch")
		}
		os.Exit(runDiagnostic(os.Stdout, os.Stderr, *dirFlag, *scriptsFlag, *listToolsFlag, *watchFlag, *callToolFlag, callToolSet, *paramsFlag, timeout, ignored))
	}

	if err := run(context.Background(), *dirFlag, *scriptsFlag, *watchFlag, *hostFlag, *portFlag, *apiKeyFlag, apiKeyFileValue, cors, timeout, *insecureNoAuthFlag, *maxConcurrentFlag); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, dir, scriptsDir string, watch bool, host string, port int, apiKey, apiKeyFile string, cors corsConfig, timeout time.Duration, insecureNoAuth bool, maxConcurrent int) error {
	sigCtx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// F-8: a flag token is world-readable in /proc/<pid>/cmdline for the
	// server's lifetime; the file/env sources are not. Warn when the flag
	// (not file/env) is the source.
	if apiKey != "" {
		fmt.Fprintln(os.Stderr, "Warning: --api-key on the command line is readable by other processes via /proc/<pid>/cmdline; prefer --api-key-file or MCP_COMMANDS_API_KEY")
	}
	apiKey = resolveAPIKey(apiKey, apiKeyFile)

	dirAbs, scriptsAbs, err := resolveToolPaths(dir, scriptsDir)
	if err != nil {
		return err
	}

	tools, err := discoverTools(scriptsAbs)
	if err != nil {
		return fmt.Errorf("failed to discover tools: %w", err)
	}

	if len(tools) == 0 {
		fmt.Fprintf(os.Stderr, "Warning: No executable scripts found in %s\n", scriptsAbs)
	}

	impl := &mcp.Implementation{
		Name:    serverName,
		Version: serverVersion,
	}
	server := mcp.NewServer(impl, nil)
	registry := newToolRegistry(server, dirAbs, timeout, maxConcurrent)
	registry.replace(tools)

	if watch {
		go func() {
			if err := watchTools(sigCtx, scriptsAbs, registry, watchToolsInterval); err != nil && !errors.Is(err, context.Canceled) {
				fmt.Fprintf(os.Stderr, "Warning: watch loop stopped: %v\n", err)
			}
		}()
	}

	if port > 0 {
		addr := fmt.Sprintf("%s:%d", host, port)

		// Fail fast before binding: no silent unauthenticated remote shells.
		if warning, err := checkHTTPSecurityPolicy(host, apiKey, insecureNoAuth); err != nil {
			return err
		} else if warning != "" {
			fmt.Fprintln(os.Stderr, warning)
		}

		handler := buildHTTPHandler(server, apiKey, cors)
		serverHTTP := &http.Server{
			Addr:    addr,
			Handler: handler,
			// No Read/WriteTimeout: they would have to exceed the 5-minute
			// tool-call budget. These two guard against slowloris and
			// pinned idle keep-alive connections.
			ReadHeaderTimeout: httpReadHeaderTimeout,
			IdleTimeout:       httpIdleTimeout,
		}

		go func() {
			<-sigCtx.Done()
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer shutdownCancel()
			_ = serverHTTP.Shutdown(shutdownCtx)
		}()

		var notes []string
		if apiKey != "" {
			notes = append(notes, "API key auth enabled")
		} else if !isLoopbackHost(host) {
			notes = append(notes, "UNAUTHENTICATED")
		}
		if cors.enabled() {
			notes = append(notes, cors.summary())
		}
		line := fmt.Sprintf("Starting HTTP server on %s", addr)
		if len(notes) > 0 {
			line += " (" + strings.Join(notes, ", ") + ")"
		}
		fmt.Fprintf(os.Stderr, "%s\n", line)
		if err := serverHTTP.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("failed to start HTTP server: %w", err)
		}
		return nil
	}

	fmt.Fprintf(os.Stderr, "Starting stdio server\n")
	return server.Run(sigCtx, &mcp.StdioTransport{})
}
