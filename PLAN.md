# Plan: MCP Commands Improvements

## Objective

Enhance `mcp-commands` with better tooling support (versioning, cross-compilation), improved user configuration (renamed flag), more structured tool output (XML-style stream markers), and optimized file watching via an event-driven mechanism instead of polling.

## Requirements & Decisions

- **Frameworks:** Standard Go library for core functionality.
- **Chosen Libraries:** `github.com/fsnotify/fsnotify` for event-driven file watching to replace the polling mechanism. (Assuming user accepts this standard approach).
- **Error Handling Strategy:**
  - **Tool Timeout:** Will continue returning a structured error response with `IsError: true` and a clear timeout message.
  - **Tool Exit Codes:** The tool's output combining logic currently discards stderr on a 0 exit code. This will be fixed to always capture and return stderr if present, wrapped in the respective tags.
  - **Watcher Failures:** Errors from `fsnotify` (e.g., file not found, permission denied) will be logged to `os.Stderr` and gracefully skipped, not crashing the entire server.
- **Output Streaming Clarification:** The user requested `<stdin>` tags, but a subprocess produces `stdout` and `stderr`. The plan substitutes `<stdout>` to correctly reflect process output while maintaining the user's intent to split streams with XML-style tags.

## Implementation Steps

> Status Markers: [ ] Open, [/] In Progress, [x] Completed (set after accepted review only!)

- [x] **Task 1: Add `--version` flag & make `serverVersion` build-injectable**
  - **Description:** 
    1. Change `const serverVersion = "0.1.0"` to `var serverVersion = "0.1.0"` in `src/main.go`.
    2. Add a `--version` flag using `flag.Bool("version", false, "...")`.
    3. If `--version` is passed, print the version and exit with `0`.
  - **Review Criteria:** Running `go build -ldflags="-X main.serverVersion=1.2.3" .` followed by `./mcp-commands --version` should output `1.2.3`.

- [x] **Task 2: Add Cross-Compilation Makefile**
   - **Description:** Create a `Makefile` at the project root with targets: `all`, `build`, `test`, `clean`, `lint`, and `cross`. The `cross` target should build binaries for `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`, and `windows/amd64`.
   - **Review Criteria:** Running `make cross` successfully produces 5 executables in a `dist/` directory, correctly naming them by OS and Arch.

- [x] **Task 3: Rename `--ip` to `--host`**
  - **Description:** Rename the CLI flag `--ip` to `--host` in `src/main.go`. Update all references, including usage text. Keep the default value `127.0.0.1`.
  - **Review Criteria:** Running `mcp-commands --host 0.0.0.0` binds the HTTP server to `0.0.0.0`. The old `--ip` flag should no longer be recognized.

- [ ] **Task 4: Add Stream Tags to Process Output**
  - **Description:** Update `combineToolOutput(stdout, stderr []byte)` to wrap output in `<stdout>...</stdout>` and `<stderr>...</stderr>` tags respectively. 
    1. Do not use `<stdin>` (per process I/O definitions).
    2. Ensure `executeTool` captures and passes `stderr` to `combineToolOutput` even when the process exits with `0` (fixing an existing bug).
  - **Review Criteria:** Executing a tool that produces both stdout and stderr returns a string like `<stdout>\nfoo\n</stdout>\n<stderr>\nbar\n</stderr>`.

- [ ] **Task 5: Replace Polling with `fsnotify` in `watchTools`**
  - **Description:**
    1. Run `go get github.com/fsnotify/fsnotify` and `go mod tidy`.
    2. Rewrite `watchTools(ctx, scriptsDir, registry)` in `src/main.go` to use `fsnotify.NewWatcher()` instead of `time.NewTicker()`.
    3. Add a debounce mechanism (e.g., 500ms delay after an event) to avoid excessive `discoverTools` calls on duplicate events (common with macOS KVO).
    4. Update `src/main_test.go` as necessary to pass tests with the new watcher implementation.
  - **Review Criteria:** Changing a script in the directory correctly hot-reloads the tool registry without throwing spurious errors, and CPU usage is not wasted on polling.

## Edge Case & Safety Checklist

- [Empty scripts directory still starts the watcher but watches the empty directory]
- [Huge stdout/stderr output still respects `maxToolOutputBytes` but doesn't truncate tags abruptly if avoidable (or appends them correctly)]
- [Invalid flag inputs correctly trigger `flag.Usage()`]
- [Duplicate events from `fsnotify` are squashed (debounce)]
- [If `fsnotify` watcher crashes or directory is deleted, error is logged gracefully without crashing the MCP HTTP server]

## Review Log (Plan Review)

- **Round 1:** [Approved] The plan is clear, comprehensive, and addresses all stated requirements including error handling and edge cases. The switch to `fsnotify` is well-thought-out with debounce taken into consideration.
- **Round 2:** N/A
- **Round 3:** N/A

## Final Status (Code Review)

- **Round 1:** N/A
- **Round 2:** N/A
- **Round 3:** N/A
