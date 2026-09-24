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
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultToolTimeout    = 5 * time.Minute
	maxToolOutputBytes    = 1 << 20
	scanHeaderLines       = 30
	scanDescriptionPrefix = "Description:"
	scanParamPrefix       = "Param:"
	watchToolsInterval    = 2 * time.Second
	watchDebounceDelay    = 100 * time.Millisecond
	serverName            = "mcp-commands"
)

var serverVersion = "0.4.0"

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

		description := extractDescription(resolvedPath)
		params := extractParams(resolvedPath)
		name := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))

		tools = append(tools, discoveredTool{
			Name:        name,
			Path:        resolvedPath,
			Description: description,
			Params:      params,
		})
	}

	return tools, nil
}

// extractDescription reads the first scanHeaderLines of a file and
// looks for a line containing scanDescriptionPrefix ("Description:").
// If found, it returns the string following the prefix. This is used
// to populate the description field of the MCP Tool, providing LLMs
// with context on what the tool does.
func extractDescription(filePath string) string {
	file, err := os.Open(filePath)
	if err != nil {
		return ""
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineCount := 0
	for scanner.Scan() && lineCount < scanHeaderLines {
		lineCount++
		line := scanner.Text()

		if strings.Contains(line, scanDescriptionPrefix) {
			parts := strings.SplitN(line, scanDescriptionPrefix, 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return ""
	}

	return ""
}

// extractParams reads the first scanHeaderLines of a file and parses
// any Param: annotations. It returns a slice of paramSpec with all valid
// parameters. Invalid or malformed annotations produce a stderr warning and
// are skipped without panicking.
//
// The annotation syntax is:
//
//	# Param: <name> <type> <required|optional> "<description>"
//
// Validation includes:
// - name must match argumentKeyPattern
// - type must be one of "string", "number", "boolean"
// - required token must be "required" or "optional"
// - description must be quoted
func extractParams(filePath string) []paramSpec {
	file, err := os.Open(filePath)
	if err != nil {
		return []paramSpec{}
	}
	defer file.Close()

	var params []paramSpec
	scanner := bufio.NewScanner(file)
	lineCount := 0

	for scanner.Scan() && lineCount < scanHeaderLines {
		lineCount++
		line := scanner.Text()

		if !strings.Contains(line, scanParamPrefix) {
			continue
		}

		// Split on the first occurrence of "Param:"
		parts := strings.SplitN(line, scanParamPrefix, 2)
		if len(parts) != 2 {
			continue
		}

		rawAnnotation := strings.TrimSpace(parts[1])

		// Parse the annotation: <name> <type> <required|optional> "<description>"
		param, err := parseParamAnnotation(rawAnnotation, filePath, line)
		if err != nil {
			// Warning already logged in parseParamAnnotation
			continue
		}

		params = append(params, param)
	}

	return params
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

	truncated := string(combined[:maxToolOutputBytes])
	return truncated + fmt.Sprintf("\n[output truncated after %d bytes]", maxToolOutputBytes)
}

// toolRegistry manages the dynamic registration and deregistration of tools
// within the MCP server. It ensures thread-safe updates via a mutex, allowing
// tools to be swapped out at runtime when changes are detected in the scripts directory.
type toolRegistry struct {
	server      *mcp.Server
	dirAbs      string
	mu          sync.Mutex
	names       []string
	lastHandler mcp.ToolHandler
}

func newToolRegistry(server *mcp.Server, dir string) *toolRegistry {
	return &toolRegistry{server: server, dirAbs: dir}
}

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
		toolDescription := discoveredTool.Description
		toolParams := discoveredTool.Params

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
			return executeTool(ctx, toolPath, parsedArgs, defaultToolTimeout, r.dirAbs)
		})

		r.server.AddTool(&mcp.Tool{
			Name:        toolName,
			Description: toolDescription,
			InputSchema: buildInputSchema(toolParams),
		}, handlerFunc)

		r.lastHandler = handlerFunc
		r.names = append(r.names, toolName)
	}
}

func mustJSONMarshal(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}

// watchTools runs a continuous loop that watches the scripts directory for changes
// using fsnotify for event-driven file watching. It implements a debounce mechanism
// (500ms delay) to avoid excessive discoverTools calls from rapid file events,
// which are common on macOS KVO. Errors from the watcher are logged but do not
// crash the server, ensuring robust operation even if the watched directory is
// deleted or permissions change.
func watchTools(ctx context.Context, scriptsDir string, registry *toolRegistry, interval time.Duration) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create watcher: %w", err)
	}
	defer watcher.Close()

	err = watcher.Add(scriptsDir)
	if err != nil {
		return fmt.Errorf("failed to watch directory: %w", err)
	}

	// Initial discovery
	tools, err := discoverTools(scriptsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: initial tool discovery failed: %v\n", err)
	} else {
		registry.replace(tools)
	}

	debounceTimer := time.NewTimer(watchDebounceDelay)
	debounceTimer.Stop()
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
			// After debounce delay, rediscover tools
			tools, err := discoverTools(scriptsDir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to rediscover tools: %v\n", err)
				continue
			}

			registry.replace(tools)
		}
	}
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
// CLI flags, and binds the context to a timeout to prevent hanging tools.
// The output is captured, combined, and returned as an MCP CallToolResult.
func executeTool(ctx context.Context, scriptPath string, args map[string]any, timeout time.Duration, dir string) (*mcp.CallToolResult, error) {
	cliArgs, err := argumentsToCLIArgs(args)
	if err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
			IsError: true,
		}, nil
	}

	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, scriptPath, cliArgs...)
	cmd.Dir = dir

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

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

// resolveAPIKey returns the token from the flag value if non-empty,
// otherwise from MCP_COMMANDS_API_KEY. Returns "" if neither is set.
func resolveAPIKey(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	return os.Getenv(apiKeyEnvVar)
}

// corsConfig holds the resolved CORS and streamable-HTTP mode options for the
// HTTP transport.
type corsConfig struct {
	origins                    []string // exact origin allowlist
	allowAll                   bool     // echo any Origin (dev)
	disableLocalhostProtection bool     // StreamableHTTPOptions.DisableLocalhostProtection
}

// disabled reports whether no CORS behavior is requested.
func (c corsConfig) disabled() bool { return len(c.origins) == 0 && !c.allowAll }

// originSet returns the allowlist as a set for O(1) lookup.
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
	if u.Host == "" {
		return fmt.Errorf("invalid origin %q: missing host", origin)
	}
	if u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("invalid origin %q: must be exactly scheme://host[:port]", origin)
	}
	return nil
}

// resolveCORS resolves flags (win) over env and validates origins. Returns an
// error for malformed origins or a contradictory --allowed-origins +
// --allow-all-origins combination.
func resolveCORS(allowedOriginsFlag string, allowAllFlag, disableLocalhostProtection bool) (corsConfig, error) {
	originsRaw := allowedOriginsFlag
	if originsRaw == "" {
		originsRaw = os.Getenv(allowedOriginsEnvVar)
	}

	allowAll := allowAllFlag
	if !allowAll {
		parsed, err := parseBoolEnv(allowAllOriginsEnvVar, os.Getenv(allowAllOriginsEnvVar))
		if err != nil {
			return corsConfig{}, err
		}
		allowAll = parsed
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

	if len(origins) > 0 && allowAll {
		return corsConfig{}, fmt.Errorf("--allowed-origins and --allow-all-origins are mutually exclusive")
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

// buildHTTPHandler returns the streamable MCP handler. When token is
// non-empty it is wrapped in bearer-token auth middleware; otherwise
// the handler serves requests unauthenticated.
func buildHTTPHandler(server *mcp.Server, token string) http.Handler {
	inner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, nil)
	if token == "" {
		return inner
	}
	return newBearerAuthHandler(inner, token)
}

func main() {
	dirFlag := flag.String("dir", "", "Working directory for tool execution (required)")
	scriptsFlag := flag.String("scripts", "", "Directory containing executable scripts (required)")
	watchFlag := flag.Bool("watch", false, "Enable hot-reload on script directory changes")
	hostFlag := flag.String("host", "127.0.0.1", "IP address for HTTP server")
	portFlag := flag.Int("port", 0, "Port for HTTP server (don't set or 0 for stdio mode)")
	apiKeyFlag := flag.String("api-key", "", "API token required by HTTP clients (or set MCP_COMMANDS_API_KEY)")
	versionFlag := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *versionFlag {
		fmt.Println(serverVersion)
		os.Exit(0)
	}

	if *dirFlag == "" || *scriptsFlag == "" {
		fmt.Fprintf(os.Stderr, "Error: --dir and --scripts are required\n")
		fmt.Fprintf(os.Stderr, "Usage: mcp-commands --dir <directory> --scripts <directory> [--watch] [--host <host>] [--port <port>] [--api-key <token>]\n")
		os.Exit(1)
	}

	if err := run(context.Background(), *dirFlag, *scriptsFlag, *watchFlag, *hostFlag, *portFlag, *apiKeyFlag); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, dir, scriptsDir string, watch bool, host string, port int, apiKey string) error {
	sigCtx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	apiKey = resolveAPIKey(apiKey)

	scriptsAbs, err := filepath.Abs(scriptsDir)
	if err != nil {
		return fmt.Errorf("failed to resolve scripts path: %w", err)
	}
	dirAbs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("failed to resolve dir path: %w", err)
	}

	if _, err := os.Stat(scriptsAbs); err != nil {
		return fmt.Errorf("scripts path inaccessible: %w", err)
	}
	if _, err := os.Stat(dirAbs); err != nil {
		return fmt.Errorf("dir path inaccessible: %w", err)
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
	registry := newToolRegistry(server, dirAbs)
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
		handler := buildHTTPHandler(server, apiKey)
		serverHTTP := &http.Server{Addr: addr, Handler: handler}

		go func() {
			<-sigCtx.Done()
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer shutdownCancel()
			_ = serverHTTP.Shutdown(shutdownCtx)
		}()

		if apiKey != "" {
			fmt.Fprintf(os.Stderr, "Starting HTTP server on %s (API key auth enabled)\n", addr)
		} else {
			fmt.Fprintf(os.Stderr, "Starting HTTP server on %s\n", addr)
		}
		if err := serverHTTP.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("failed to start HTTP server: %w", err)
		}
		return nil
	}

	fmt.Fprintf(os.Stderr, "Starting stdio server\n")
	return server.Run(sigCtx, &mcp.StdioTransport{})
}
