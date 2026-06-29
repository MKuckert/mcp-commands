# Plan: MCP CLI-Bridge (Go)

## Objective

Create a standalone Go binary (`mcp-commands`) that dynamically discovers executable scripts in a target directory, exposes them as MCP tools, and runs them safely in a specified working directory. The implementation uses the official MCP Go SDK for the protocol layer while compiling to a single static binary.

## Requirements & Decisions

- **Frameworks:** Go Standard Library + `github.com/modelcontextprotocol/go-sdk` (v1.6.1+). The SDK handles JSON-RPC framing, MCP version negotiation, stdio/HTTP transports, and tool schema inference — without compromising the static binary requirement (pure Go, no CGO).
- **Execution Context:** A "Discovery Run" scans `--scripts` while still in the original working directory. After registration, `os.Chdir` switches the process to `--dir` before the transport listener starts.
- **Argument Mapping:** JSON `arguments` from the LLM are mapped to CLI arguments in `--key value` format (e.g., `{ "target": "all" }` → `script.sh --target all`). Array values are passed as repeated flags.
- **Error Handling Strategy:**
  - _Startup:_ Exit with a clear error message if `--scripts` or `--dir` paths are missing or inaccessible.
  - _Execution:_ `context.WithTimeout` (default 5 min) on every subprocess. On timeout or non-zero exit code, return `isError: true` with combined stdout+stderr as content. Server stays alive.
  - _Protocol:_ Unknown tool names return a JSON-RPC `-32602 Invalid params` error. Malformed arguments (non-object, stringified JSON) are defensively re-parsed before failing.

## Implementation Steps

> Status Markers: [ ] Open, [/] In Progress, [x] Completed (set after accepted review only!)

- [x] **Task 1: Project Skeleton & CLI Setup**
  - **Description:** Create a new Go module in a subdirectory (`src/`) with `go.mod` (module name `github.com/mkuckert/mcp-commands`), `main.go`, and `go.sum`. Add `github.com/modelcontextprotocol/go-sdk` as the sole external dependency. Parse CLI flags using the `flag` package: `--dir` (required), `--scripts` (required), `--watch` (bool), `--ip` (default `127.0.0.1`), `--port` (default `0`, stdio mode when unset). Exit non-zero with usage on missing required flags.
  - **Review Criteria:** `go build` produces a binary. Running without `--scripts` or `--dir` prints a helpful error and exits non-zero. Running with valid flags starts without crashing.

- [ ] **Task 2: Tool Discovery**
  - **Description:** Implement `func discoverTools(scriptsDir string) ([]discoveredTool, error)`. Walk the `--scripts` directory (non-recursive). For each file: check executable bit (`os.FileMode & 0111`). Read up to 10 lines; extract description from the first line matching `# Description: ...` or `// Description: ...`. Build a `discoveredTool` struct: `{ Name, Path, Description string }`. Tool names are the file basename without extension. Symlinks to executables are followed.
  - **Review Criteria:** Returns correct list for a sample directory. Non-executables and subdirectories are skipped. Missing description comment results in an empty string (not an error).

- [ ] **Task 3: Dynamic Tool Registration**
  - **Description:** For each `discoveredTool`, call `server.AddTool` (low-level variant) with a hand-crafted `inputSchema` of `{"type": "object", "additionalProperties": {"type": "string"}}` to accept any string key-value arguments. The handler is a closure capturing the script path. Initialize the MCP server via `mcp.NewServer` before registration.
  - **Review Criteria:** `tools/list` response contains one entry per discovered script with correct name and description. `tools/call` with an unknown tool name returns a proper MCP error response.

- [ ] **Task 4: Tool Execution**
  - **Description:** Inside each tool handler: convert the `arguments` map to `[]string` (`--key value` pairs, sorted by key for determinism). Build `exec.CommandContext(ctx, scriptPath, args...)` with a 5-minute deadline injected via `context.WithTimeout`. Set `cmd.Dir` to the already-chdir'd working directory. Capture stdout and stderr into separate `bytes.Buffer`. On completion: if exit code != 0 or timeout, return `isError: true` with combined output as `TextContent`. Otherwise return `isError: false` with stdout.
  - **Review Criteria:** A script that echoes its args returns correct output. A non-zero exit returns `isError: true`. A script sleeping > 5 min is killed and returns a timeout error. `cmd.Wait()` is always called to reap the subprocess.

- [ ] **Task 5: Transport & Startup Sequence**
  - **Description:** Full bootstrap order: (1) parse flags, (2) resolve `--scripts` and `--dir` to absolute paths, (3) discover tools, (4) register tools on MCP server, (5) `os.Chdir(--dir)`, (6) start transport. If `--port > 0`, start `mcp.NewStreamableHTTPHandler` on `--ip:--port` via `net/http`; otherwise call `server.Run(ctx, &mcp.StdioTransport{})`. Handle `SIGINT`/`SIGTERM` via `signal.NotifyContext` for clean shutdown.
  - **Review Criteria:** Stdio mode completes a full `initialize` → `tools/list` → `tools/call` round-trip. HTTP mode responds on the configured address. Ctrl-C shuts down without leaving zombie processes.

- [ ] **Task 6: `--watch` Hot-Reload**
  - **Description:** When `--watch` is set, launch a background goroutine that polls the `--scripts` directory every 2 seconds using `os.ReadDir`. On any change (file added, removed, or executable bit changed), re-run discovery and re-register all tools atomically behind a mutex. Send a `notifications/tools/list_changed` notification via the SDK if supported.
  - **Review Criteria:** Adding or removing a script file while the server is running updates `tools/list` within ~2 seconds. No race conditions under `go test -race`.

## Edge Case & Safety Checklist

- **Path traversal:** Resolve `--scripts` and `--dir` to absolute paths via `filepath.Abs` at startup. Script paths are always derived from the pre-resolved directory — no user-supplied paths reach `exec.Command`.
- **Shell injection:** Keys and values are passed as separate `[]string` elements to `exec.Command`, never shell-interpolated.
- **Timeout enforcement:** `exec.CommandContext` sends `SIGKILL` on deadline exceeded. `cmd.Wait()` is always called to reap the subprocess.
- **Empty scripts directory:** Log a warning to stderr and start the server with zero tools (valid MCP state, not a fatal error).
- **`--dir` disappears at runtime:** `os.Stat` check before each tool execution; return `isError: true` with a clear message if the directory is gone.
- **Concurrent tool calls:** Each invocation creates its own `exec.Cmd` with no shared mutable state — safe under concurrency.
- **Large output:** Cap captured output at 10 MB per tool call; truncate with a notice appended if exceeded.
- **Stringified arguments:** Defensively detect if `arguments` arrives as a JSON string instead of an object and re-parse it before returning an error.

## Review Log (Plan Review)

- **Round 1:** Pending
- **Round 2:** N/A
- **Round 3:** N/A

## Final Status (Code Review)

- **Round 1:** Pending
- **Round 2:** N/A
- **Round 3:** N/A
