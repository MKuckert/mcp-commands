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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultToolTimeout    = 5 * time.Minute
	maxToolOutputBytes    = 1 << 20
	scanDescriptionLines  = 10
	scanDescriptionPrefix = "Description:"
	watchToolsInterval    = 2 * time.Second
	serverName            = "mcp-commands"
	serverVersion         = "0.1.0"
)

type discoveredTool struct {
	Name        string
	Path        string
	Description string
}

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

func parseToolArguments(raw json.RawMessage) (map[string]any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return map[string]any{}, nil
	}

	var args map[string]any
	if err := json.Unmarshal(trimmed, &args); err == nil {
		return args, nil
	}

	var encoded string
	if err := json.Unmarshal(trimmed, &encoded); err == nil {
		if err := json.Unmarshal([]byte(encoded), &args); err == nil {
			return args, nil
		}
	}

	return nil, fmt.Errorf("arguments must be a JSON object")
}

var argumentKeyPattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)

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

func combineToolOutput(stdout, stderr []byte) string {
	combined := append([]byte{}, stdout...)
	if len(stderr) > 0 {
		if len(combined) > 0 {
			combined = append(combined, '\n')
		}
		combined = append(combined, stderr...)
	}

	if len(combined) <= maxToolOutputBytes {
		return string(combined)
	}

	truncated := string(combined[:maxToolOutputBytes])
	return truncated + fmt.Sprintf("\n[output truncated after %d bytes]", maxToolOutputBytes)
}

type toolRegistry struct {
	server *mcp.Server
	dirAbs string
	mu     sync.Mutex
	names  []string
}

func newToolRegistry(server *mcp.Server, dir string) *toolRegistry {
	return &toolRegistry{server: server, dirAbs: dir}
}

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

func snapshotScriptsDir(scriptsDir string) (string, error) {
	entries, err := os.ReadDir(scriptsDir)
	if err != nil {
		return "", err
	}

	var builder strings.Builder
	for _, entry := range entries {
		info, err := os.Stat(filepath.Join(scriptsDir, entry.Name()))
		if err != nil {
			return "", err
		}

		builder.WriteString(entry.Name())
		builder.WriteByte(':')
		builder.WriteString(info.Mode().String())
		builder.WriteByte(':')
		builder.WriteString(strconv.FormatInt(info.ModTime().UnixNano(), 10))
		builder.WriteByte(':')
		builder.WriteString(strconv.FormatInt(info.Size(), 10))
		builder.WriteByte('\n')
	}

	return builder.String(), nil
}

func watchTools(ctx context.Context, scriptsDir string, registry *toolRegistry, interval time.Duration) error {
	currentSnapshot, err := snapshotScriptsDir(scriptsDir)
	if err != nil {
		return fmt.Errorf("failed to snapshot scripts directory: %w", err)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			nextSnapshot, err := snapshotScriptsDir(scriptsDir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to watch scripts directory: %v\n", err)
				continue
			}
			if nextSnapshot == currentSnapshot {
				continue
			}

			tools, err := discoverTools(scriptsDir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to rediscover tools: %v\n", err)
				continue
			}

			registry.replace(tools)
			currentSnapshot = nextSnapshot
		}
	}
}

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
		Content: []mcp.Content{&mcp.TextContent{Text: stdout.String()}},
	}, nil
}

func main() {
	dirFlag := flag.String("dir", "", "Working directory for tool execution (required)")
	scriptsFlag := flag.String("scripts", "", "Directory containing executable scripts (required)")
	watchFlag := flag.Bool("watch", false, "Enable hot-reload on script directory changes")
	ipFlag := flag.String("ip", "127.0.0.1", "IP address for HTTP server")
	portFlag := flag.Int("port", 0, "Port for HTTP server (don't set or 0 for stdio mode)")
	flag.Parse()

	if *dirFlag == "" || *scriptsFlag == "" {
		fmt.Fprintf(os.Stderr, "Error: --dir and --scripts are required\n")
		fmt.Fprintf(os.Stderr, "Usage: mcp-commands --dir <directory> --scripts <directory> [--watch] [--ip <ip>] [--port <port>]\n")
		os.Exit(1)
	}

	if err := run(context.Background(), *dirFlag, *scriptsFlag, *watchFlag, *ipFlag, *portFlag); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, dir, scriptsDir string, watch bool, ip string, port int) error {
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
		addr := fmt.Sprintf("%s:%d", ip, port)
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
