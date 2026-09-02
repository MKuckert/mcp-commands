# mcp-commands

`mcp-commands` is a lightweight [Model Context Protocol (MCP)](https://modelcontextprotocol.io/) server written in Go that dynamically turns local executable scripts into tools accessible by LLMs and MCP clients.

Instead of writing custom MCP servers for every utility or integration, `mcp-commands` allows you to simply place any executable script (Bash, Python, Node.js, compiled Go/Rust, etc.) into a directory. The server discovers them, extracts their descriptions, and exposes them as native MCP tools, automatically handling argument parsing and CLI invocation.

## Features

- **Language Agnostic:** Expose scripts written in Bash, Python, Ruby, Go, Rust, or any executable binary.
- **Dynamic Discovery:** Automatically scans a configured directory for executable files and exposes them as MCP tools.
- **Hot Reloading (`--watch`):** Add, modify, or remove scripts on the fly. The server detects changes and updates available tools without needing a restart.
- **Auto-Documentation:** Reads the first few lines of your script for a `Description:` comment and presents it to the LLM to provide context on what the tool does.
- **Smart Argument Translation:** Safely maps JSON tool arguments from the LLM into POSIX-compliant CLI flags (e.g., `{"force": true, "file": "data.txt"}` becomes `--force --file data.txt`).
- **Flexible Transport:** Supports standard stdio transport (for standard local MCP clients) and HTTP streaming transport for remote connections.
- **Safety First:** Prevents shell injection by passing arguments directly to the subprocess via `exec`, avoiding fragile shell evaluation. Enforces execution timeouts and output limits.
- **Raw Output:** Returns the raw stdout and stderr (capped to 1 MB) of the executed script, allowing LLMs to process the output directly.

## Installation

Ensure you have [Go](https://go.dev/dl/) installed, then run:

```bash
go install github.com/mkuckert/mcp-commands@latest
```

_(Adjust package path based on your repository structure)_

## Usage

### Starting the Server

The server requires two primary arguments:

1. `--dir`: The working directory where the scripts will be executed.
2. `--scripts`: The directory containing the executable scripts.

**Standard Mode (stdio)**

```bash
mcp-commands --dir /path/to/workdir --scripts /path/to/scripts
```

_This is the default mode expected by local MCP clients like Claude Desktop._

**Hot Reloading Mode**

```bash
mcp-commands --dir /path/to/workdir --scripts /path/to/scripts --watch
```

_Monitors the scripts directory for changes._

**HTTP Server Mode**

```bash
mcp-commands --dir /path/to/workdir --scripts /path/to/scripts --port 8080
```

_Exposes the MCP server over HTTP for remote or web-based clients._

Pass `--host` to bind to a specific IP address (default is `127.0.0.1`, use `0.0.0.0` to bind to all interfaces and make MCP accessible from other devices).

#### Authentication (optional)

By default the HTTP server is **open** — no token required, exactly as before. You can optionally protect it with a static API token:

```bash
mcp-commands --dir /path/to/workdir --scripts /path/to/scripts --port 8080 --api-key my-secret-token
```

or via the `MCP_COMMANDS_API_KEY` environment variable:

```bash
MCP_COMMANDS_API_KEY=my-secret-token mcp-commands --dir /path/to/workdir --scripts /path/to/scripts --port 8080
```

The `--api-key` flag takes precedence over the environment variable. When a token is configured (the server logs `Starting HTTP server on <addr> (API key auth enabled)`), **every** HTTP request must send the token in the `Authorization` header or it is rejected with `401 Unauthorized`:

```bash
curl -s http://localhost:8080 \
  -H "Authorization: Bearer my-secret-token" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"curl","version":"1.0"}}}'
```

MCP clients that support custom headers can be configured the same way, e.g. a generic JSON client config:

```json
{
  "mcpServers": {
    "mcp-commands": {
      "url": "http://localhost:8080",
      "headers": {
        "Authorization": "Bearer my-secret-token"
      }
    }
  }
}
```

Notes:
- The scheme is compared case-insensitively (`bearer` works), but the header must be exactly `Bearer <token>` separated by a single space.
- The token is never logged by the server.
- Enabling auth is a breaking change for existing HTTP clients — they must start sending the token.
- **Stdio mode needs no token.** `--api-key` / `MCP_COMMANDS_API_KEY` are ignored when the server runs without `--port`.

### Creating Tools

Simply create an executable file in your `--scripts` directory.

For example, create a file named `hello-world` in your scripts directory:

```bash
#!/bin/bash
# Description: Prints a greeting message. Accepts a "name" argument.

NAME=${2:-World}
echo "Hello, $NAME!"
```

1. Ensure it is executable: `chmod +x hello-world`
2. The server exposes a tool named `hello-world`.
3. The LLM sees the description: `Prints a greeting message. Accepts a "name" argument.`
4. If the LLM calls it with `{"name": "Alice"}`, the server executes `./hello-world --name Alice`.

### Argument Translation Rules

The server translates JSON properties into CLI flags.

- **Strings/Numbers:** `{"key": "value"}` ➡️ `--key value`
- **Booleans:**
  - `{"flag": true}` ➡️ `--flag` (no value, just the flag)
  - `{"flag": false}` ➡️ _(omitted entirely)_
- **Arrays:** `{"items": ["a", "b"]}` ➡️ `--items a --items b`
- **Security:** Keys must match `^[a-zA-Z][a-zA-Z0-9_-]*$`. Invalid keys are rejected to prevent injection.

## AI Usage

The implementation of `mcp-commands` is completely done by an AI. The idea and guidance for the plan is mine, the plan writing and code is the AI. It wrote the entire server, including argument parsing, script discovery, and MCP protocol handling, I did the review.

## License

MIT License
