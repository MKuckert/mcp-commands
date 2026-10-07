# mcp-commands

[Project site](https://mkuckert.github.io/mcp-commands/)

`mcp-commands` is a lightweight [Model Context Protocol (MCP)](https://modelcontextprotocol.io/) server written in Go that dynamically turns local executable scripts into tools accessible by LLMs and MCP clients.

Instead of writing custom MCP servers for every utility or integration, `mcp-commands` allows you to simply place any executable script (Bash, Python, Node.js, compiled Go/Rust, etc.) into a directory. The server discovers them, extracts their descriptions, and exposes them as native MCP tools, automatically handling argument parsing and CLI invocation.

## Use Cases

- **Real tooling for sandboxed harnesses.** Your AI agent runs in a sandbox, a container, or on dedicated hardware, but you want access to the unrestricted, high-performance toolchain on the real machine — a full compiler install, faster builds, hardware-attached utilities. Run `mcp-commands` on that host over HTTP and bridge it into the sandbox: the agent gains the capability, and the sandbox stays the boundary around the harness. The server's own trust boundary is its scripts directory — write access to `--scripts` is code execution as the server user, so keep that directory under your control (see [Untrusted Tool Output](#untrusted-tool-output)).
- **Cross-platform tooling.** The agent harness lives on one machine, the work happens on another — a Linux build box, a Mac with Apple-silicon tooling, a Windows host. The streamable HTTP transport (`--host`, `--port`, auth, TLS, CORS) makes platform-specific commands reachable from wherever the harness runs.
- **Your utility scripts, now tools.** You already maintain a pile of Bash, Python, Node.js, or Ruby scripts. Drop them into the `--scripts` directory and they become native MCP tools — no custom MCP server to write per script.

## Features

- **Language Agnostic:** Expose scripts written in Bash, Python, Ruby, Node.js, or any other executable — compiled binaries are discovered too, they just can't carry frontmatter (`Description:`/`Param:`/`Timeout:` live in the file's first lines).
- **Dynamic Discovery:** Automatically scans a configured directory for executable files and exposes them as MCP tools.
- **Hot Reloading (`--watch`):** Add, modify, or remove scripts on the fly. The server detects changes and updates available tools without needing a restart.
- **Auto-Documentation:** Reads the first few lines of your script for a `Description:` comment and presents it to the LLM to provide context on what the tool does.
- **Smart Argument Translation:** Validates declared tool parameters, then maps JSON arguments into CLI flags (e.g., with `force` and `file` declared, `{"force": true, "file": "data.txt"}` becomes `--file data.txt --force`; flags are sorted alphabetically).
- **Flexible Transport:** Supports standard stdio transport (for standard local MCP clients) and streamable HTTP transport for remote connections.
- **Safety First:** Prevents shell injection by passing arguments directly to the subprocess via `exec`, avoiding fragile shell evaluation. Enforces a configurable execution timeout (default 5 minutes, per tool, per server, or disabled) and output limits.
- **Tagged Output:** Returns the executed script's stdout and stderr wrapped in `<stdout>`/`<stderr>` tags (so the LLM can tell the streams apart). Combined output is capped at 1 MiB, with a trailing truncation notice when the cap is exceeded.

## Installation

**Homebrew** (macOS/Linux):

```bash
brew trust --formula MKuckert/homebrew-tap/mcp-commands
brew install MKuckert/homebrew-tap/mcp-commands
```

(`brew trust` is required once for non-core taps.)

**Go** — ensure you have [Go](https://go.dev/dl/) installed, then run:

```bash
go install github.com/mkuckert/mcp-commands@latest
```

**Installing a release** — prefer a prebuilt binary? Each GitHub release ships `mcp-commands_<version>_<os>_<arch>` archives for linux/darwin/windows × amd64/arm64 (tar.gz, zip for Windows) plus a `checksums.txt`. Download the asset for your platform from [the releases page](https://github.com/mkuckert/mcp-commands/releases), extract it, and place the `mcp-commands` binary on your `PATH`:

```bash
tar -xzf mcp-commands_0.11.1_linux_amd64.tar.gz   # unzip on Windows
```

**Container** — a `Containerfile` is included that wraps a prebuilt release binary (no Go toolchain needed). Build your own image and mount a self-contained scripts directory:

```bash
docker build -t mcp-commands .
docker run -p 8080:8080 -v ./my-scripts:/scripts \
  -e MCP_COMMANDS_API_KEY=... mcp-commands
```

Configuration is via environment variables: `MCP_COMMANDS_HOST` (default `0.0.0.0`), `MCP_COMMANDS_PORT` (default `8080`), `MCP_COMMANDS_DIR` (default `/work`), `MCP_COMMANDS_SCRIPTS` (default `/scripts`), `MCP_COMMANDS_TIMEOUT`, `MCP_COMMANDS_WATCH` (`1`/`true`/`yes`), plus the binary's native `MCP_COMMANDS_API_KEY`, `MCP_COMMANDS_ALLOWED_ORIGINS` / `MCP_COMMANDS_ALLOW_ALL_ORIGINS` (CORS), and `LOG_LEVEL`. Pin a release with `--build-arg MCP_COMMANDS_VERSION=<version>` and a platform with `--platform linux/<amd64|arm64>`. Note the container only sees what you mount — for bridging the host toolchain into a sandbox, run the bare binary on the host over HTTP instead (see [Use Cases](#use-cases)).

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

How the watch behaves:

- Change events are debounced (100 ms) and then the scripts directory is
  rescanned. The registry applies a per-tool diff: tools that did not change
  are left alone, removed tools are unregistered, and added/changed tools are
  registered in place — so a `tools/list` from a client never sees a gap, and
  a no-op rescan emits no `tools/list_changed` notifications.
- Only the scripts directory (and its parent, for deletion/recreation
  recovery) is watched. A symlinked tool whose *target* lives outside the
  directory is therefore not watched directly: calling the tool always runs
  the current file, but its registered *metadata* (description, parameters,
  timeout) stays stale after an in-place target edit until the link itself
  is touched (replace or re-point the symlink — that fires a rescan) or the
  server restarts. Deliberate trade-off: watching every external target
  (and keeping recovery watches for deleted ones) added a large amount of
  complexity for a minor staleness window, so it was dropped.
- Deleting and recreating the scripts directory is recovered automatically
  (the watch re-attaches to the new directory); a permanent deletion degrades
  to the last known tool set with rescan warnings on stderr.
- One inherent inotify limit: if the scripts directory's *parent directory*
  is deleted, the parent watch dies and the parent's recreation is not
  observable — the registry then degrades to the last known tool set until
  the server restarts. Deleting and *recreating the scripts directory
  itself* is recovered automatically, since the surviving parent watch
  observes it.
- If the file watcher cannot be set up (e.g. `inotify` exhausted), the server
  fails to start with a visible error on stderr. `--watch` is an explicit
  request, so a fatal watcher failure at any later point also exits the
  process with a nonzero status (in `http` mode the server stops, too).

**HTTP Server Mode**

```bash
mcp-commands --dir /path/to/workdir --scripts /path/to/scripts --port 8080
```

_Exposes the MCP server over HTTP for remote or web-based clients._

Pass `--host` to bind to a specific IP address (default is `127.0.0.1`, use `0.0.0.0` to bind to all interfaces and make MCP accessible from other devices).

#### Authentication (optional)

The HTTP server accepts requests without authentication **when bound to loopback** (the default `127.0.0.1`). You can optionally protect it with a static API token:

Recommended — from a file (the token never appears in a process listing):

```bash
mcp-commands --dir /path/to/workdir --scripts /path/to/scripts --port 8080 --api-key-file /path/to/token.txt
```

or via the `MCP_COMMANDS_API_KEY` environment variable:

```bash
MCP_COMMANDS_API_KEY=my-secret-token mcp-commands --dir /path/to/workdir --scripts /path/to/scripts --port 8080
```

Precedence: `--api-key` > `--api-key-file` > `MCP_COMMANDS_API_KEY`. **Prefer `--api-key-file` or `MCP_COMMANDS_API_KEY` over `--api-key <value>`**: command-line arguments are world-readable via `/proc/<pid>/cmdline` for the server's entire lifetime. (`--api-key-file` content is trimmed, so a trailing newline in the file is fine; a file over **8 KiB** is rejected as a likely misconfiguration — a token is a short secret — and an empty file is a startup error rather than a silent fall-through to no-auth.) When a token is configured (the server logs `Starting HTTP server` at startup, with an `API key auth enabled` note), every MCP request must send the token in the `Authorization` header or it is rejected with `401 Unauthorized` (CORS preflight `OPTIONS` requests are answered by the CORS layer before authentication):

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
- **Stdio mode needs no token.** `--api-key` / `--api-key-file` / `MCP_COMMANDS_API_KEY` are ignored when the server runs without `--port`.
- **Non-loopback binds require auth (0.8.0).** Binding to a non-loopback address (`--host 0.0.0.0`, a LAN IP, a non-IP hostname) without a token **refuses to start**:

  ```
  ERROR@10:00:00 refusing to start unauthenticated HTTP server on non-loopback host "0.0.0.0": set --api-key (or MCP_COMMANDS_API_KEY), or pass --insecure-no-auth explicitly to accept the risk
  ```

  An unauthenticated HTTP server is a remote command-execution endpoint: anyone who can reach the port can run your scripts as the server user. The escape hatch `--insecure-no-auth` starts the server anyway, logging a loud `UNAUTHENTICATED HTTP server bound to …` warning and an `UNAUTHENTICATED` note in the startup log. Use it only for trusted networks.

#### TLS

The HTTP transport is **cleartext by default**: the bearer token and every request body transit unencrypted. For production, terminate TLS — either put a TLS-terminating proxy (Caddy/nginx) in front of the server, or serve HTTPS directly. Binding a non-loopback host with `--api-key` but without TLS prints a loud startup warning (the bearer token and every body transit unencrypted); behind a TLS-terminating proxy you can disregard it.

```bash
mcp-commands --dir /path/to/workdir --scripts /path/to/scripts --port 8443 --tls-cert /path/to/cert.pem --tls-key /path/to/key.pem
```

- `--tls-cert` and `--tls-key` must be **given together** (exactly one of the two is a startup error), and both files are checked at startup — an unreadable file fails fast before binding.
- In stdio mode (no `--port`) both flags are ignored, like the other HTTP-only options.
- The startup log carries a `TLS` note: `Starting HTTP server … notes=…, TLS`.

#### Concurrency Cap

The server runs at most `--max-concurrent` tool subprocesses at once (default **16**, `0` selects the default). A call arriving when the cap is full gets a clean in-band MCP error — `mcp-commands is at capacity (16 concurrent tool executions); please retry shortly` — that the client can retry, instead of piling up unbounded subprocesses. Lower it on small hosts; raise it for bursty clients.

#### Request Limits

Every HTTP request body is bounded on **size** and on **time**.

- **Size:** a body larger than **10 MiB** is rejected with `400`, and the connection is closed once the response is sent (Go's `MaxBytesReader` flags the request as too large, which forces the connection shut). The body is never buffered past the cap, so a client cannot use an oversized body to exhaust server memory.
- **Time:** a body that takes more than **30 s** in total to arrive is rejected with `400`. This stops a client that opens a POST and then dribbles a few bytes at a time from pinning a connection and its handler slot indefinitely — the size cap alone would not catch that, since a slow drip never grows large.

Both limits are fixed (not flags); they are generous for the real workload (small JSON tool calls) and exist to bound resource use, not to shape traffic.

#### Browser Clients (Cross Origin Resource Sharing, CORS)

CORS is **off by default** — with no flags set, the server sends no CORS headers at all, so existing curl/desktop clients see no change. To let browser-based MCP clients (web chat UIs, in-browser agents) talk to the streamable HTTP transport, opt in:

```bash
mcp-commands --dir /path/to/workdir --scripts /path/to/scripts --port 8080 --allowed-origins https://app.example.com,https://chat.example.com
```

Configuration (each flag wins over its env var):

| Flag | Env | Meaning |
|---|---|---|
| `--allowed-origins <o1,o2,...>` | `MCP_COMMANDS_ALLOWED_ORIGINS` | Comma-separated **exact** origin allowlist (`https://app.example.com`). Origins are validated at startup (must be `http`/`https` + host, no path/userinfo) and the flag fails fast even in stdio mode. Default ports are normalized on match, so `http://app.example.com:80` and `http://app.example.com` are equivalent — matching what browsers send in the `Origin` header (which omits default ports). Non-ASCII (IDN) hostnames are compared as given: browsers send punycode, so list the punycode form (`https://xn--bcher-kva.example`) for Unicode domains. An explicitly empty `--allowed-origins=` means “no origins” — it disables the allowlist and does **not** fall back to the env var. |
| `--allow-all-origins` | `MCP_COMMANDS_ALLOW_ALL_ORIGINS` (`1`/`true`/`yes`) | Echo any `Origin`. **Dev convenience only** — safe only with `--api-key` + TLS. The env var is consulted only when the flag is not set at all; an explicit `--allow-all-origins=false` suppresses it. |
| `--disable-localhost-protection` | *(none, deliberate)* | Disables the SDK's DNS-rebinding 403 for servers on loopback. For dev setups where the page is served from a tunnel/LAN hostname that resolves to `127.0.0.1`. This flag intentionally has no env fallback — it is a mode choice, not a secret. |

Notes:
- **Security:** this server executes local scripts, so CORS is **not** a security boundary — it only gates which page's JavaScript can *read* responses. Use the explicit `--allowed-origins` list in production; never `--allow-all-origins` on a public, unauthenticated server. `--allow-all-origins` combined with **no** `--api-key` prints a loud startup warning: any web page opened in a browser can then invoke tools against the server and read their output.
- **Behavior change in 0.5.0 — the HTTP transport is always stateless:** each request stands on its own. go-sdk v1.6.1 still issues a vestigial `Mcp-Session-Id` header on `initialize` but ignores it on later requests, so clients that stored and resend a session ID keep working. A request missing the `Mcp-Protocol-Version` header defaults to `2025-03-26` (the oldest supported version). `GET` (SSE stream) returns 405.
- **Use a fetch-based client**, e.g. the official MCP TypeScript SDK — raw `EventSource` cannot work in any mode. Send an explicit `Accept: application/json, text/event-stream` header on POST — that is the documented contract (the TS SDK sends both `Accept` values automatically); some clients that send only `*/*` happen to work, but do not rely on it.
- **Do not set `MCPGODEBUG=enableoriginverification=1`** to "fix" CORS failures: it makes the SDK 403 *all* cross-origin requests inside the handler, where the CORS middleware cannot recover.
- **Production requires TLS:** the server is cleartext HTTP by default; put a TLS-terminating proxy (Caddy/nginx) in front for browser use — the proxy can also add CORS as an alternative to these flags — or serve HTTPS directly with `--tls-cert`/`--tls-key` (see [TLS](#tls)).
- A client example with the MCP TS SDK:

```js
// The URL is the mcp-commands transport endpoint — a separate origin from
// the page. It is the *page's* origin that --allowed-origins must list.
const transport = new StreamableHTTPClientTransport(new URL("https://mcp.example.com"), {
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

`--timeout` and `--no-timeout` are **mutually exclusive**: passing both is a startup error, and an explicitly empty `--timeout=` fails the same way (the flags must be passed deliberately). A script that declares `Timeout: 30s` in its frontmatter always gets 30 seconds regardless of the global setting. An invalid per-tool `Timeout:` value does **not** break discovery: the server logs a warning (`ignoring invalid Timeout`, with the file and reason) to stderr and the tool falls back to the global timeout. With no deadline, a client abort/cancel still kills the running script — "no timeout" means "no deadline", never "uninterruptible".

The registered tool description carries a `(timeout: 30s)` / `(timeout: none)` suffix so the LLM knows its budget.

#### Logging

All diagnostics — banners, warnings, and operational errors — are logged to **stderr** as [slog](https://pkg.go.dev/log/slog) records, each rendered on one line as `<LEVEL>@HH:mm:ss message key=value …` — the message is unquoted and, when the record carries attributes, a ` | ` separator joins it to the first `key=value` (a record with no attributes ends right after the message):

```
WARN@18:02:11 ignoring invalid Timeout | file=/path/to/scripts/render.sh error="invalid timeout ..."
```

stdout is reserved for program output only: in stdio mode it carries the MCP protocol itself, and in diagnostics it carries the tool list, `--call-tool` result text, `-h` help, and `--version`. Routing logs to stdout would corrupt the protocol, so every record goes to stderr in all modes. In HTTP mode stdout is not used at all — no output of any kind is written there.

`--log-level <level>` sets the minimum level that is emitted — `debug`, `info` (default), `warn`, or `error`; records below it are dropped. It is accepted in every mode. The `LOG_LEVEL` environment variable is a fallback consulted only when the flag is not set (an empty value counts as unset) — precedence: `--log-level` > `LOG_LEVEL` > `info`. At the default `info`, you see startup banners, warnings, and errors; `debug` additionally logs the discovery summary, each debounced rescan, and watch reattach attempts.

One exception: flag-parse and validation failures occur before the logger exists and are printed as raw `Error: …` lines — slog formatting (and `--log-level`) do not apply to startup configuration errors.

#### Version

`mcp-commands --version` prints the server version and exits. It works without `--dir`/`--scripts` and skips all other validation. Release binaries report their release tag (injected at build time); local source builds without injected ldflags report `commit-local`.

#### Flags and Environment Variables

Consolidated summary — the per-section prose above remains authoritative.

Essentials and modes:

| Flag | Default | Meaning |
|---|---|---|
| `--dir <dir>` | _(required)_ | Working directory where tool scripts are executed. |
| `--scripts <dir>` | _(required)_ | Directory scanned for executable scripts. |
| `--watch` | off | Hot-reload in server mode; live re-print in `--list-tools` mode (ignored with `--call-tool`). |
| `--list-tools` | off | Print a human-readable rendering of the tools the server would register, then exit; no server starts. |
| `--call-tool <name>` | _(none)_ | Run one discovered tool once and exit (debug mode; no server starts). |
| `--params <json>` | `{}` | JSON object of named arguments for `--call-tool`. |
| `--version` | off | Print the version and exit. |
| `--log-level <level>` | `info` | Minimum log level: `debug`, `info`, `warn`, or `error` (or `LOG_LEVEL` when the flag is absent); every record goes to stderr (see [Logging](#logging)). |

HTTP server:

| Flag | Default | Meaning |
|---|---|---|
| `--host <ip>` | `127.0.0.1` | Bind address for HTTP mode. |
| `--port <port>` | `0` (stdio) | A non-zero value (validated `1..65535`) switches to HTTP mode. |
| `--api-key <token>` | _(none)_ | Bearer token. Precedence: `--api-key` > `--api-key-file` > `MCP_COMMANDS_API_KEY`. |
| `--api-key-file <path>` | _(none)_ | Read the token from a file (content trimmed, max **8 KiB**, must be non-empty); the token never appears in a process listing. |
| `--tls-cert <path>` / `--tls-key <path>` | _(none)_ | Serve HTTPS directly (PEM files, required together; startup-checked). Ignored in stdio mode. |
| `--insecure-no-auth` | off | Escape hatch: start an unauthenticated server on a non-loopback host (loudly warned). |
| `--max-concurrent <n>` | `16` (`0` = default) | Cap on simultaneous tool executions (validated `1..256`); saturated calls get a clean at-capacity error. |

CORS (`--allowed-origins`, `--allow-all-origins`, `--disable-localhost-protection`) — see the [CORS section](#browser-clients-cross-origin-resource-sharing-cors) for the full reference, including env fallbacks. Timeouts (`--timeout`, `--no-timeout`) — see the [Timeouts section](#timeouts).

Environment variables (each is consulted only when its flag is not set):

| Variable | Equivalent flag |
|---|---|
| `MCP_COMMANDS_API_KEY` | `--api-key` |
| `MCP_COMMANDS_ALLOWED_ORIGINS` | `--allowed-origins` |
| `MCP_COMMANDS_ALLOW_ALL_ORIGINS` | `--allow-all-origins` (`1`/`true`/`yes`) |
| `LOG_LEVEL` | `--log-level` |

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

The first 30 lines of a script are scanned once for `Description:`, `Param:`, and `Timeout:` annotations (for `Description:` and `Timeout:` the first occurrence wins and extras are ignored — invalid ones warn; every valid `Param:` line is collected):

- `Description: <text>` — presented to the LLM as the tool description.
- `Param: <name> <type> <required|optional> "<description>"` — declares a typed tool parameter (`string`, `number`, or `boolean`; the description must be quoted). One line per parameter; invalid lines log a warning to stderr and are skipped. The name must match `^[a-zA-Z][a-zA-Z0-9_-]*$`. Duplicate names use the last declaration's type, description, and required status, at the first declaration's position.
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

The server validates arguments against the tool's published JSON Schema **before execution** in both MCP and `--call-tool` mode. Only declared parameters are accepted (tools without `Param:` lines accept only `{}`). Values must match their declared `string`, `number`, or `boolean` type; a required parameter cannot be missing or `null`. JSON numbers retain their original decimal spelling, including large integers and exponent notation. A required boolean `false` is valid but emits no flag. The server then translates valid JSON properties into CLI flags.

- **Strings/Numbers:** `{"key": "value"}` ➡️ `--key value`
- **Booleans:**
  - `{"flag": true}` ➡️ `--flag` (no value, just the flag)
  - `{"flag": false}` ➡️ _(omitted entirely)_
- **Arrays:** Not accepted by declared tool parameters (`string`, `number`, and `boolean` only).
- **Security:** Keys must match `^[a-zA-Z][a-zA-Z0-9_-]*$`. Invalid keys are rejected to prevent injection.
- **Deterministic ordering:** Keys are sorted alphabetically before translation, so the CLI flag order is stable and never reflects the LLM's JSON object key order.

### Untrusted Tool Output

Treat tool output as **untrusted model input**. The `<stdout>`/`<stderr>` tags are advisory formatting, not a sandbox: a script can emit a literal `</stdout>` line and thereby inject content that looks like server framing, and anything it prints is handed to the LLM verbatim (up to the 1 MiB cap). The trust boundary is the scripts directory: **write access to `--scripts` is code execution as the server user**, so keep that directory under your control. Do not rely on the tags to keep a misbehaving or hostile script from influencing the model.

### Diagnostics

Two self-contained diagnostic modes reuse the exact discovery, frontmatter
parsing and validation of the server — and never start a server.

**List tools** — prints a human-readable rendering of the registered tools
(names, parameter signatures, descriptions, and effective timeouts) — the
same information the MCP server exposes, in terminal form, not the raw JSON
schema:

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
- Add `--watch` for a live list: the list re-prints only when the tool set
  actually changed, with the screen cleared first only when stdout is a TTY
  (piped output simply accumulates) — an unchanged rescan stays completely
  silent; the process runs until `Ctrl-C`.

**Call one tool** — run it once, bypassing the MCP protocol:

```console
$ mcp-commands --dir . --scripts ./scripts --call-tool build --params '{"profile":"release"}'

<stdout>
building with release
</stdout>

$ mcp-commands --dir . --scripts ./scripts --call-tool build
invalid tool arguments: validating root: missing properties: 'profile'
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

- Server-mode flags (`--host`, `--port`, `--api-key`, `--api-key-file`,
  `--tls-cert`/`--tls-key`, `--insecure-no-auth`, `--max-concurrent`,
  and the CORS flags `--allowed-origins`, `--allow-all-origins`,
  `--disable-localhost-protection`)
  are ignored in both diagnostic modes, and `--watch` is ignored with
  `--call-tool` as well; if you pass any of them explicitly, a single notice
  is printed to stderr. `--watch` is *honored* with `--list-tools` — it is
  the live-list mode described above and never listed as ignored there.

Duplicate parameter names use the last declaration consistently for the schema, list, and validation.

## Troubleshooting

- **Script not discovered.** A file becomes a tool only if it is a regular file (or a symlink resolving to one) with the executable bit set — `chmod +x <script>`. Subdirectories and non-executable files are skipped silently, so a missing tool usually means a missing exec bit. Verify what the server registers with `--list-tools` (below).
- **Frontmatter warnings on stderr.** An invalid `Param:` or `Timeout:` line in a script's first 30 lines is skipped with a warning on stderr. For `Description:` and `Timeout:` the *first occurrence* wins — if the first `Timeout:` is invalid it warns and the global timeout applies, and any later `Timeout:` lines (even valid ones) are ignored; put a single, valid `Timeout:` line first. Discovery and hot reload are not broken.
- **Inspect what the server registered.** `mcp-commands --dir <dir> --scripts <scripts> --list-tools` prints the registered names, signatures, descriptions, and timeout suffixes in a human-readable form, without starting a server.
- **Duplicate tool names.** The tool name is the filename minus its extension, so `a.sh` and `a.py` both register as `a`. The first file in directory order wins and a warning is printed to stderr for each shadowed duplicate — rename one of the files to expose both.

## AI Usage

The implementation of `mcp-commands` is completely done by an AI. The idea and guidance for the plan is mine, the plan writing and code is the AI. It wrote the entire server, including argument parsing, script discovery, and MCP protocol handling, I did the review.

## License

MIT License
