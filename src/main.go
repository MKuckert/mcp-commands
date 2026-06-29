package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

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

	// Placeholder for server initialization
	fmt.Fprintf(os.Stderr, "Starting mcp-commands server\n")
	fmt.Fprintf(os.Stderr, "Dir: %s\n", dir)
	fmt.Fprintf(os.Stderr, "Scripts: %s\n", scriptsDir)
	fmt.Fprintf(os.Stderr, "Watch: %v\n", watch)
	fmt.Fprintf(os.Stderr, "IP: %s\n", ip)
	fmt.Fprintf(os.Stderr, "Port: %d\n", port)

	// Wait for signal
	<-sigCtx.Done()
	return nil
}
