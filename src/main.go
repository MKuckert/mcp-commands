package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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
		// Skip directories and non-executables
		if entry.IsDir() {
			continue
		}

		// Resolve symlinks
		filePath := filepath.Join(scriptsDir, entry.Name())
		resolvedPath, err := filepath.EvalSymlinks(filePath)
		if err != nil {
			continue
		}

		// Check if the resolved path is executable
		fileInfo, err := os.Stat(resolvedPath)
		if err != nil {
			continue
		}

		if fileInfo.Mode()&0111 == 0 {
			continue
		}

		// Extract description from the first 10 lines
		description := extractDescription(resolvedPath)

		// Tool name is the file basename without extension
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
	for scanner.Scan() && lineCount < 10 {
		lineCount++
		line := scanner.Text()

		// Check for # Description: ... or // Description: ...
		if strings.Contains(line, "Description:") {
			parts := strings.SplitN(line, "Description:", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}

	return ""
}

func main() {
	dirFlag := flag.String("dir", "", "Working directory for tool execution (required)")
	scriptsFlag := flag.String("scripts", "", "Directory containing executable scripts (required)")
	watchFlag := flag.Bool("watch", false, "Enable hot-reload on script directory changes")
	ipFlag := flag.String("ip", "127.0.0.1", "IP address for HTTP server")
	portFlag := flag.Int("port", 0, "Port for HTTP server (0 = stdio mode)")
	flag.Parse()

	// Validate required flags
	if *dirFlag == "" || *scriptsFlag == "" {

		fmt.Fprintf(os.Stderr, "Error: --dir and --scripts are required\n")
		fmt.Fprintf(os.Stderr, "Usage: mcp-commands --dir <directory> --scripts <directory> [--watch] [--ip <ip>] [--port <port>]\n")
		os.Exit(1)
	}

	// Run the server
	if err := run(context.Background(), *dirFlag, *scriptsFlag, *watchFlag, *ipFlag, *portFlag); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, dir, scriptsDir string, watch bool, ip string, port int) error {
	// Create a context that cancels on SIGINT/SIGTERM
	sigCtx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Discover tools
	tools, err := discoverTools(scriptsDir)
	if err != nil {
		return fmt.Errorf("failed to discover tools: %w", err)
	}

	if len(tools) == 0 {
		fmt.Fprintf(os.Stderr, "Warning: No executable scripts found in %s\n", scriptsDir)
	}

	// Create MCP server
	impl := &mcp.Implementation{
		Name:    "mcp-commands",
		Version: "1.0.0",
	}
	server := mcp.NewServer(impl, nil)

	// Register each discovered tool
	for _, discoveredTool := range tools {
		// Capture tool details in closure
		toolName := discoveredTool.Name
		toolPath := discoveredTool.Path
		toolDescription := discoveredTool.Description

		// Hand-crafted input schema accepting any string key-value arguments
		inputSchema := map[string]any{
			"type": "object",
			"additionalProperties": map[string]any{
				"type": "string",
			},
		}

		// Marshal schema to JSON for the Tool.
		// Use json.RawMessage so the schema remains a JSON object, not base64-encoded bytes.
		schemaJSON, err := json.Marshal(inputSchema)
		if err != nil {
			return fmt.Errorf("failed to marshal input schema for tool %s: %w", toolName, err)
		}

		// Create tool definition
		toolDef := &mcp.Tool{
			Name:        toolName,
			Description: toolDescription,
			InputSchema: json.RawMessage(schemaJSON),
		}

		// Create handler closure capturing tool path and name
		// Handler signature: ToolHandler func(context.Context, *CallToolRequest) (*CallToolResult, error)
		handler := func(scriptPath, name string) mcp.ToolHandler {
			return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				// For now, return a placeholder indicating tool was called
				// Task 4 will implement actual execution
				output := fmt.Sprintf("Tool '%s' called with arguments: %v\n", name, req.Params.Arguments)
				result := &mcp.CallToolResult{
					Content: []mcp.Content{
						&mcp.TextContent{Text: output},
					},
				}
				return result, nil
			}
		}(toolPath, toolName)

		// Add tool to server using low-level API
		server.AddTool(toolDef, handler)
		fmt.Fprintf(os.Stderr, "Registered tool: %s\n", toolName)
	}

	fmt.Fprintf(os.Stderr, "Starting mcp-commands server\n")
	fmt.Fprintf(os.Stderr, "Dir: %s\n", dir)
	fmt.Fprintf(os.Stderr, "Scripts: %s\n", scriptsDir)
	fmt.Fprintf(os.Stderr, "Watch: %v\n", watch)
	fmt.Fprintf(os.Stderr, "IP: %s\n", ip)
	fmt.Fprintf(os.Stderr, "Port: %d\n", port)
	fmt.Fprintf(os.Stderr, "Discovered tools: %d\n", len(tools))

	// Wait for signal
	<-sigCtx.Done()
	return nil
}
