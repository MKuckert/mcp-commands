package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
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
	scanDescriptionLines  = 10
	scanDescriptionPrefix = "Description:"
	watchToolsInterval    = 2 * time.Second
	watchDebounceDelay    = 100 * time.Millisecond
	serverName            = "mcp-commands"
)

var serverVersion = "0.2.0"

type discoveredTool struct {
	Name        string
	Path        string
	Description string
}

// discoverTools scans the given directory for executable files and symlinks
// resolving to executables. It skips subdirectories and non-executable files.
// For each valid executable, it extracts the description and constructs a
// discoveredTool record, which is later registered with the MCP server.
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
		name := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))

		tools = append(tools, discoveredTool{
			Name:        name,
			Path:        resolvedPath,
			Description: description,
		})
	}

	return tools, nil
}

// extractDescription reads the first scanDescriptionLines of a file and
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
	for scanner.Scan() && lineCount < scanDescriptionLines {
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
	server *mcp.Server
	dirAbs string
	mu     sync.Mutex
	names  []string
}

func newToolRegistry(server *mcp.Server, dir string) *toolRegistry {
	return &toolRegistry{server: server, dirAbs: dir}
}

// replace unregisters all currently tracked tools and registers a new set of tools.
// It defines the InputSchema dynamically, allowing tools to accept arbitrary
// string key-value arguments, which are then passed to the execution wrapper.
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

		r.server.AddTool(&mcp.Tool{
			Name:        toolName,
			Description: toolDescription,
			InputSchema: mustJSONMarshal(map[string]any{
				"type": "object",
				"additionalProperties": map[string]any{
					"type": "string",
				},
			}),
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return executeTool(ctx, toolPath, req.Params.Arguments, defaultToolTimeout, r.dirAbs)
		})

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

// executeTool runs the script at scriptPath as a subprocess in the specified
// working directory. It parses the JSON arguments from the MCP request, converts
// them to CLI flags, and binds the context to a timeout to prevent hanging tools.
// The output is captured, combined, and returned as an MCP CallToolResult.
func executeTool(ctx context.Context, scriptPath string, rawArgs json.RawMessage, timeout time.Duration, dir string) (*mcp.CallToolResult, error) {
	parsedArgs, err := parseToolArguments(rawArgs)
	if err != nil {
		return nil, err
	}

	cliArgs, err := argumentsToCLIArgs(parsedArgs)
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

func main() {
	dirFlag := flag.String("dir", "", "Working directory for tool execution (required)")
	scriptsFlag := flag.String("scripts", "", "Directory containing executable scripts (required)")
	watchFlag := flag.Bool("watch", false, "Enable hot-reload on script directory changes")
	hostFlag := flag.String("host", "127.0.0.1", "IP address for HTTP server")
	portFlag := flag.Int("port", 0, "Port for HTTP server (don't set or 0 for stdio mode)")
	versionFlag := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *versionFlag {
		fmt.Println(serverVersion)
		os.Exit(0)
	}

	if *dirFlag == "" || *scriptsFlag == "" {
		fmt.Fprintf(os.Stderr, "Error: --dir and --scripts are required\n")
		fmt.Fprintf(os.Stderr, "Usage: mcp-commands --dir <directory> --scripts <directory> [--watch] [--host <host>] [--port <port>]\n")
		os.Exit(1)
	}

	if err := run(context.Background(), *dirFlag, *scriptsFlag, *watchFlag, *hostFlag, *portFlag); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, dir, scriptsDir string, watch bool, host string, port int) error {
	sigCtx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

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
		handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
			return server
		}, nil)
		serverHTTP := &http.Server{Addr: addr, Handler: handler}

		go func() {
			<-sigCtx.Done()
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer shutdownCancel()
			_ = serverHTTP.Shutdown(shutdownCtx)
		}()

		fmt.Fprintf(os.Stderr, "Starting HTTP server on %s\n", addr)
		if err := serverHTTP.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("failed to start HTTP server: %w", err)
		}
		return nil
	}

	fmt.Fprintf(os.Stderr, "Starting stdio server\n")
	return server.Run(sigCtx, &mcp.StdioTransport{})
}
