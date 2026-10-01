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
- **Safety First:** Prevents shell injection by passing arguments directly to the subprocess via `exec`, avoiding fragile shell evaluation. Enforces a configurable execution timeout (default 5 minutes, per tool, per server, or disabled) and output limits.
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

The HTTP server accepts requests without authentication **when bound to loopback** (the default `127.0.0.1`). You can optionally protect it with a static API token:

```bash
mcp-commands --dir /path/to/workdir --scripts /path/to/scripts --port 8080 --api-key my-secret-token
```

or via the `MCP_COMMANDS_API_KEY` environment variable:

```bash
MCP_COMMANDS_API_KEY=my-secret-token mcp-commands --dir /path/to/workdir --scripts /path/to/scripts --port 8080
```

or from a file (preferred — see the note below):

```bash
chmod 600 /etc/mcp-commands/token
mcp-commands --dir /path/to/workdir --scripts /path/to/scripts --port 8080 --api-key-file /etc/mcp-commands/token
```

Precedence: `--api-key` > `--api-key-file` > `MCP_COMMANDS_API_KEY`. When a token is configured (the server logs `Starting HTTP server on <addr> (API key auth enabled)`), **every** HTTP request must send the token in the `Authorization` header or it is rejected with `401 Unauthorized`. One exception: when `--cors` is enabled, the CORS middleware sits *outside* the auth middleware and answers allowed-origin `OPTIONS` preflights without credentials (browsers cannot send an `Authorization` header on a preflight); every non-preflight request is still authenticated.

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
- **`--api-key-file` is the preferred way to supply the token (0.8.1).** A `--api-key` *value* is world-readable via `/proc/<pid>/cmdline` for the server's lifetime (any local user can run `ps`); a file with `chmod 600` and the environment variable are not. When the token comes from the `--api-key` flag, the server prints a startup warning naming the process-list exposure. The file's content is whitespace-trimmed, and an unreadable `--api-key-file` path is a startup error.
- The scheme is compared case-insensitively (`bearer` works), but the header must be exactly `Bearer <token>` separated by a single space.
- The token is never logged by the server.
- Enabling auth is a breaking change for existing HTTP clients — they must start sending the token.
- **Stdio mode needs no token.** `--api-key` / `--api-key-file` / `MCP_COMMANDS_API_KEY` are ignored when the server runs without `--port`.
- **Non-loopback binds require auth (0.8.0).** Binding to a non-loopback address (`--host 0.0.0.0`, a LAN IP, a non-IP hostname) without a token **refuses to start**:

  ```
  Error: refusing to start unauthenticated HTTP server on non-loopback host "0.0.0.0": set --api-key/--api-key-file (or MCP_COMMANDS_API_KEY), or pass --insecure-no-auth explicitly to accept the risk
  ```

  An unauthenticated HTTP server is a remote command-execution endpoint: anyone who can reach the port can run your scripts as the server user. The escape hatch `--insecure-no-auth` starts the server anyway, printing a loud `WARNING: UNAUTHENTICATED HTTP server bound to …` line and an `UNAUTHENTICATED` note in the startup log. Use it only for trusted networks.

#### Concurrency Cap

The server runs at most `--max-concurrent` tool subprocesses at once (default **16**, `0` selects the default). A call arriving when the cap is full gets a clean in-band MCP error — `mcp-commands is at capacity (16 concurrent tool executions); please retry shortly` — that the client can retry, instead of piling up unbounded subprocesses. Lower it on small hosts; raise it for bursty clients.

#### Browser Clients (Cross Origin Resource Sharing, CORS)

CORS is **off by default** — with no flags set, the server sends no CORS headers at all, so existing curl/desktop clients see no change. To let browser-based MCP clients (web chat UIs, in-browser agents) talk to the streamable HTTP transport, opt in:

```bash
mcp-commands --dir /path/to/workdir --scripts /path/to/scripts --port 8080 --allowed-origins https://app.example.com,https://chat.example.com
```

Configuration (each flag wins over its env var):

| Flag | Env | Meaning |
|---|---|---|
| `--allowed-origins <o1,o2,...>` | `MCP_COMMANDS_ALLOWED_ORIGINS` | Comma-separated **exact** origin allowlist (`https://app.example.com`). Origins are validated at startup (must be `http`/`https` + host, no path/userinfo) and the flag fails fast even in stdio mode. |
| `--allow-all-origins` | `MCP_COMMANDS_ALLOW_ALL_ORIGINS` (`1`/`true`/`yes`) | Echo any `Origin`. **Dev convenience only** — safe only with `--api-key` + TLS. The env var is consulted only when the flag is not set at all; an explicit `--allow-all-origins=false` suppresses it. |
| `--disable-localhost-protection` | *(none, deliberate)* | Disables the SDK's DNS-rebinding 403 for servers on loopback. For dev setups where the page is served from a tunnel/LAN hostname that resolves to `127.0.0.1`. This flag intentionally has no env fallback — it is a mode choice, not a secret. |

Notes:
- **Security:** this server executes local scripts, so CORS is **not** a security boundary — it only gates which page's JavaScript can *read* responses. Use the explicit `--allowed-origins` list in production; never `--allow-all-origins` on a public, unauthenticated server.
- **Behavior change in 0.5.0 — the HTTP transport is always stateless:** each request stands on its own. go-sdk v1.6.1 still issues a vestigial `Mcp-Session-Id` header on `initialize` but ignores it on later requests, so clients that stored and resend a session ID keep working. A request missing the `Mcp-Protocol-Version` header defaults to `2025-03-26` (the oldest supported version). `GET` (SSE stream) returns 405.
- **Use a fetch-based client**, e.g. the official MCP TypeScript SDK — raw `EventSource` cannot work in any mode. Fetch clients must send `Accept: application/json, text/event-stream` on POST (the SDK returns 400 otherwise; the TS SDK does both automatically).
- **Do not set `MCPGODEBUG=enableoriginverification=1`** to "fix" CORS failures: it makes the SDK 403 *all* cross-origin requests inside the handler, where the CORS middleware cannot recover.
- **Production requires TLS:** the server is HTTP-only; put a TLS-terminating proxy (Caddy/nginx) in front for browser use — the proxy can also add CORS as an alternative to these flags.
- A client example with the MCP TS SDK:

```js
// The URL must be the full transport endpoint (the mcp-commands HTTP root).
const transport = new StreamableHTTPClientTransport(new URL("https://app.example.com"), {
  requestInit: { headers: { Authorization: `Bearer ${token}` } },
});
const client = new Client({ name: "web-client", version: "1.0.0" });
await client.connect(transport);
```

#### Timeouts

Every tool execution is bounded by a timeout (default: **5 minutes**). When the deadline passes, the script is killed and the tool returns an error result of the form `tool timed out after <duration>` (plus any partial output). Three levels, in precedence order (per tool > CLI > default):

| Level | How | Meaning |
|---|---|---|
| **Per tool** | `Timeout: <duration>` line in the script's frontmatter (first occurrence wins, like `Description:`) | Always wins, even over `--no-timeout`. |
| **Global** | `--timeout <duration>` flag | Applies to every tool without its own `Timeout:`. Parsed at startup; an invalid — or explicitly empty — value exits with an error before the server starts. |
| **Default** | _(no flag, no frontmatter)_ | 5 minutes. |

The duration is a whitespace-separated list of `<digits><unit>` tokens (units `s`, `m`, `h`; e.g. `5m`, `60s`, `1h 30m 5s`; whitespace between tokens is optional). Sub-second units, decimals, and signs are rejected, and values that overflow are rejected with a clear error. `NONE` (case-insensitive) and a result of `0` (e.g. `0s`) mean **no timeout**.

```bash
# Global deadline for all tools: 10 minutes
mcp-commands --dir /path/to/workdir --scripts /path/to/scripts --timeout 10m

# No deadline at all (client cancellation/abort still kills scripts)
mcp-commands --dir /path/to/workdir --scripts /path/to/scripts --no-timeout
```

`--timeout` and `--no-timeout` are **mutually exclusive**: passing both is a startup error, and an explicitly empty `--timeout=` fails the same way (the flags must be passed deliberately). A script that declares `Timeout: 30s` in its frontmatter always gets 30 seconds regardless of the global setting. An invalid per-tool `Timeout:` value does **not** break discovery: the server logs `Warning: ignoring invalid Timeout in <file>: <reason>` to stderr and the tool falls back to the global timeout. With no deadline, a client abort/cancel still kills the running script — "no timeout" means "no deadline", never "uninterruptible".

The registered tool description carries a `(timeout: 30s)` / `(timeout: none)` suffix so the LLM knows its budget.

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

#### Script Frontmatter

The first 30 lines of a script are scanned once for `Description:`, `Param:`, and `Timeout:` annotations (for `Description:` and `Timeout:` the first occurrence wins and extras are silently ignored; every valid `Param:` line is collected):

- `Description: <text>` — presented to the LLM as the tool description.
- `Param: <name> <type> <required|optional> "<description>"` — declares a typed tool parameter (`string`, `number`, or `boolean`; the description must be quoted). One line per parameter; invalid lines log a warning to stderr and are skipped. The name must match `^[a-zA-Z][a-zA-Z0-9_-]*$`.
- `Timeout: <duration>` — overrides the global/default timeout for this tool only. Accepts the same duration format as `--timeout` (e.g. `30s`, `1h 30m 5s`) or `NONE`/`0s` for no deadline.

A fully annotated example:

```bash
#!/bin/bash
# Description: Runs the long-running render pipeline.
# Param: source string required "Source file to render"
# Timeout: 1h 30m 5s

echo "rendering..."
```

This script is registered as a tool with the description `Runs the long-running render pipeline. (timeout: 1h30m5s)` and is killed after `1h 30m 5s` if it overruns. Without the `Timeout:` line it would inherit the global timeout (`--timeout` flag, default 5 minutes). An invalid `Timeout:` value logs a warning to stderr and falls back to the global timeout, so editing a script's timeout mid-flight never breaks discovery or hot reload.

### Argument Translation Rules

The server translates JSON properties into CLI flags.

- **Strings/Numbers:** `{"key": "value"}` ➡️ `--key value`
- **Booleans:**
  - `{"flag": true}` ➡️ `--flag` (no value, just the flag)
  - `{"flag": false}` ➡️ _(omitted entirely)_
- **Arrays:** `{"items": ["a", "b"]}` ➡️ `--items a --items b`
- **Security:** Keys must match `^[a-zA-Z][a-zA-Z0-9_-]*$`. Invalid keys are rejected to prevent injection.

### Diagnostics

Two self-contained diagnostic modes reuse the exact discovery, frontmatter
parsing and validation of the server — and never start a server.

**List tools** — prints exactly what the LLM sees (the same registered
descriptions, timeout suffixes and parameter signatures the MCP server would
expose):

```console
$ mcp-commands --dir . --scripts ./scripts --list-tools

build(profile:str)
     Build the project with the given profile. (timeout: 5m0s)

run([args:str])
     Run the project. (timeout: none)

status()
     Status of the demo app. (timeout: 10s)
```

- Names without parentheses declare no parameters. Parameters in brackets are
  optional; unbracketed parameters are required.
- The description comes from the frontmatter `Description:` line; the effective
  per-tool timeout is shown (a per-tool `Timeout:` wins over `--timeout`;
  `none` = no deadline).
- Descriptions are word-wrapped (never mid-word) at the terminal window
  width when stdout is a TTY — re-queried on every print, so resizes are
  honored — falling back to a fixed 160-rune width when stdout is piped.
- Add `--watch` for a live list: the list re-prints on every scripts-directory
  change, with the screen cleared first only when stdout is a TTY (piped
  output simply accumulates); the process runs until `Ctrl-C`.

**Call one tool** — run it once, bypassing the MCP protocol:

```console
$ mcp-commands --dir . --scripts ./scripts --call-tool build --params '{"profile":"release"}'

<stdout>
building with release
</stdout>

$ mcp-commands --dir . --scripts ./scripts --call-tool build
missing required parameter: profile
```

The first example's `build.sh` parses the translated `--profile release` flag
and echoes the value; the second runs with no `--params`, which fails
required-parameter validation before the script starts.

- `--params` is a JSON object (default: `{}`); an explicitly empty
  `--params=` and a JSON `null` payload are both accepted as `{}`; anything
  else that is not a JSON object is a startup error. Required-parameter
  validation applies exactly as in server mode.
- The tool's output is printed verbatim under `<stdout>`/`<stderr>` markers
  (markers appear only for streams that produced output).
- Arguments are translated to CLI flags with the [rules above](#argument-translation-rules).
- The timeout has the same precedence as in server mode: a per-tool
  `Timeout:` wins over `--timeout`; `Timeout: NONE` means the debug call runs
  with no deadline.
- `--call-tool` is mutually exclusive with `--list-tools` (passing both is a
  startup error), and an explicitly empty `--call-tool=` is a startup error —
  it never falls through to server mode.
- Exit code and streams:

  | Outcome | Exit code | Output |
  | --- | --- | --- |
  | Tool succeeds | 0 | result on stdout |
  | Missing required param, non-zero script exit, or timeout | 1 | tool's result on stdout |
  | Unknown tool, invalid `--params`, unstartable script, other operational failure | 1 | reason on stderr, script never started |

- Server-mode flags (`--host`, `--port`, `--api-key`, `--api-key-file`, and the CORS flags
  `--allowed-origins`, `--allow-all-origins`, `--disable-localhost-protection`;
  an unreadable `--api-key-file` path is a startup error like all fail-fast
  validation) are ignored in both diagnostic modes, and `--watch` is ignored with
  `--call-tool` as well; if you pass any of them explicitly, a single notice
  is printed to stderr. `--watch` is *honored* with `--list-tools` — it is
  the live-list mode described above and never listed as ignored there.

Inherited quirk, same as server mode: a parameter name declared more than
once is last-wins in the schema, but validation enforces *any* `required`
declaration of that name.

## Security

**Trust boundary.** The `--scripts` directory is a *trusted* input: every script found in it runs with the server user's full privileges, and a symlink in the directory that resolves to a regular executable file is accepted (it registers under the symlink's name, pointing at the resolved target). Nothing here sandboxes script execution — keep the directory writable only by users you trust, and prefer loopback HTTP + `--api-key`/`--api-key-file` so that only authenticated clients can trigger executions.

**Exec pinning.** A tool call never re-resolves the script path: it execs the file the registry anchored, so a swap can never *silently* redirect a call:

- **Server mode (unix):** at discovery, mcp-commands opens each script (with `O_NOFOLLOW`) and keeps the file descriptor; the record's `(device, inode)` is derived from that same descriptor, and a tool call execs *that opened inode* via `/dev/fd/3`. An in-flight call therefore always runs the inode the registry last registered — even if the path is renamed, replaced, or re-pointed in the meantime. Edits in place are picked up immediately (same inode, new content); a *replacement* is picked up on the next reload, where the watch re-discovers and swaps the anchor to the new file. File descriptors are refcounted so an in-flight call is never starved: a tool whose identity is unchanged reuses its descriptor across reloads, and a descriptor closes when the tool is swapped for a different file or removed (by the last in-flight call, if one is running).
- **`--call-tool` diagnostics:** the same discovery-time descriptor is the exec anchor where available, so the diagnostic mode gets the same `/dev/fd` pinning; where there is none (Windows, failed open) the path is re-verified at exec time instead.
- **Windows:** no `/dev/fd` exec; the path is re-verified at exec time (resolves, regular file, executable bit). A narrow window between check and exec remains — treat a concurrently writable scripts directory as unsafe there.

The `(device, inode)` identity is the anchor where descriptors are not available; on filesystems that aggressively reuse inode numbers after deletion (e.g. tmpfs), a delete-and-recreate *at the same path* can in principle match the recorded identity in the re-verification path — the discovery-time descriptor above is what closes that gap.

## AI Usage

The implementation of `mcp-commands` is completely done by an AI. The idea and guidance for the plan is mine, the plan writing and code is the AI. It wrote the entire server, including argument parsing, script discovery, and MCP protocol handling, I did the review.

## License

MIT License
