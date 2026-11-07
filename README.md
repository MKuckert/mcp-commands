# mcp-commands

[Project site](https://mkuckert.github.io/mcp-commands/)

`mcp-commands` is a lightweight [Model Context Protocol (MCP)](https://modelcontextprotocol.io/) server written in Go that dynamically turns local executable scripts into tools accessible by LLMs and MCP clients.

Instead of writing custom MCP servers for every utility or integration, `mcp-commands` allows you to simply place any executable script (Bash, Python, Node.js, compiled Go/Rust, etc.) into a directory. The server discovers them, extracts their descriptions, and exposes them as native MCP tools, automatically handling argument parsing and CLI invocation.

## Use Cases

- **Real tooling for sandboxed harnesses.** Your agent runs in a sandbox, but you want the unrestricted toolchain on the real machine — run `mcp-commands` on that host over HTTP and bridge it into the sandbox. The server's trust boundary is its scripts directory: write access to `--scripts` is code execution as the server user, so keep it under your control ([Untrusted Tool Output](#output-and-trust)).
- **Cross-platform tooling.** The harness lives on one machine, the work happens on another — a Linux build box, a Mac with Apple-silicon tooling, a Windows host. The streamable HTTP transport (`--host`, `--port`, auth, TLS, CORS) makes platform-specific commands reachable from wherever the harness runs.
- **Your utility scripts, now tools.** You already maintain a pile of Bash, Python, Node.js, or Ruby scripts. Drop them into the `--scripts` directory and they become native MCP tools — no custom MCP server to write per script.

## Quickstart

**1. Install** (pick one):

```bash
# Homebrew (macOS/Linux) — `brew trust` is required once for non-core taps
# (shell completion scripts are installed with the formula — docs/reference/shell-completion.md)
brew trust --formula MKuckert/homebrew-tap/mcp-commands
brew install MKuckert/homebrew-tap/mcp-commands

# Go — requires a Go toolchain
go install github.com/mkuckert/mcp-commands@latest

# Prebuilt release — linux/darwin/windows × amd64/arm64 + checksums.txt (tar.gz, zip for Windows)
# https://github.com/mkuckert/mcp-commands/releases — put the binary on your PATH
```

**2. Write a script** in your scripts directory:

```bash
#!/bin/bash
# Description: Prints a greeting message. Accepts a "name" argument.
# Param: name string optional "Name to greet"

NAME=${2:-World}
echo "Hello, $NAME!"
```

Make it executable (`chmod +x hello-world`) — the server exposes a tool named `hello-world` whose description is the `Description:` line.

**3. Check what the server registers** (no server starts):

```console
$ mcp-commands --dir /path/to/workdir --scripts /path/to/scripts --list-tools

hello-world([name:str])
     Prints a greeting message. Accepts a "name" argument. (timeout: 5m0s)
```

(Full reference for both diagnostic modes: [Diagnostics](#diagnostics).)

**4. Connect a client.** Local clients (Claude Desktop & co.) expect stdio:

```bash
mcp-commands --dir /path/to/workdir --scripts /path/to/scripts
```

```json
{
  "mcpServers": {
    "mcp-commands": {
      "command": "mcp-commands",
      "args": ["--dir", "/path/to/workdir", "--scripts", "/path/to/scripts"]
    }
  }
}
```

Remote clients use HTTP: start with `--port 8080` and point the client at
`http://localhost:8080`. Non-loopback binds require a token — [Authentication](#authentication).

## How It Works

### Discovery

- Any **executable regular file** (or a symlink resolving to one) in `--scripts` becomes a tool. Subdirectories and non-executable files are skipped silently — a missing exec bit is the usual cause of "tool not found".
- The tool name is the **filename minus its extension**: `a.sh` and `a.py` both register as `a`; the first file in directory order wins, with a warning on stderr for each shadowed duplicate.
- With `--watch`, added/changed/removed scripts are picked up without a restart — [behavior details](docs/reference/watch.md).
- Tools are exposed with a JSON Schema built from their `Param:` lines; the LLM sees the `Description:` text plus a `(timeout: …)` suffix.

### Script Frontmatter

The first 30 lines of a script are scanned once for annotations (compiled binaries can't carry frontmatter — they just work, with no description or typed parameters):

- `Description: <text>` — presented to the LLM as the tool description. First occurrence wins; extras are ignored (invalid ones warn).
- `Param: <name> <type> <required|optional> "<description>"` — declares a typed tool parameter (`string`, `number`, or `boolean`; the description must be quoted). One line per parameter; invalid lines log a warning to stderr and are skipped. The name must match `^[a-zA-Z][a-zA-Z0-9_-]*$`. Duplicate names use the last declaration's type, description, and required status, at the first declaration's position.
- `Timeout: <duration>` — overrides the global/default timeout for this tool only (see [Timeouts](#timeouts)).

A fully annotated example:

```bash
#!/bin/bash
# Description: Runs the long-running render pipeline.
# Param: source string required "Source file to render"
# Timeout: 1h 30m 5s

echo "rendering..."
```

This script is registered with the description `Runs the long-running render pipeline. (timeout: 1h30m5s)` and is killed after `1h 30m 5s` if it overruns. An invalid `Timeout:` value logs a warning and falls back to the global timeout, so editing a script mid-flight never breaks discovery or hot reload.

### Argument Translation

The server validates arguments against the tool's published JSON Schema **before execution** (in both MCP and `--call-tool` mode). Only declared parameters are accepted — tools without `Param:` lines accept only `{}`. Values must match their declared type; a required parameter cannot be missing or `null`. JSON numbers retain their original decimal spelling, including large integers and exponent notation. Valid properties are then translated into CLI flags:

- **Strings/Numbers:** `{"key": "value"}` ➡️ `--key value`
- **Booleans:** `{"flag": true}` ➡️ `--flag` (bare flag); `{"flag": false}` ➡️ omitted entirely
- Example: with `force` and `file` declared, `{"force": true, "file": "data.txt"}` becomes `--file data.txt --force`
- **Arrays:** not accepted by declared parameters (`string`, `number`, `boolean` only)
- **Security:** keys must match `^[a-zA-Z][a-zA-Z0-9_-]*$`; invalid keys are rejected to prevent injection
- **Deterministic ordering:** keys are sorted alphabetically before translation, so the CLI flag order is stable and never reflects the LLM's JSON key order

Arguments are passed directly to the subprocess via `exec` — no shell is involved, so there is no shell injection.

### Output and Trust

The script's stdout and stderr are returned wrapped in `<stdout>`/`<stderr>` tags so the LLM can tell the streams apart. Combined output is capped at **1 MiB**, with a trailing truncation notice when the cap is exceeded.

Treat tool output as **untrusted model input**. The tags are advisory formatting, not a sandbox: a script can emit a literal `</stdout>` line and thereby inject content that looks like server framing, and anything it prints is handed to the LLM verbatim. The trust boundary is the scripts directory: **write access to `--scripts` is code execution as the server user**, so keep that directory under your control. Do not rely on the tags to keep a misbehaving or hostile script from influencing the model.

## Running With a Client

### stdio (local clients)

The default mode — no flags beyond `--dir`/`--scripts` needed:

```bash
mcp-commands --dir /path/to/workdir --scripts /path/to/scripts [--watch]
```

### HTTP (remote, cross-machine, sandbox bridge)

```bash
mcp-commands --dir /path/to/workdir --scripts /path/to/scripts --port 8080
```

- `--host` selects the bind address (default `127.0.0.1`; use `0.0.0.0` to reach other devices).
- **Non-loopback binds require auth (0.8.0):** binding to a non-loopback address (`--host 0.0.0.0`, a LAN IP, a non-IP hostname) without a token **refuses to start** — an unauthenticated HTTP server is a remote command-execution endpoint:

  ```
  ERROR@10:00:00 refusing to start unauthenticated HTTP server on non-loopback host "0.0.0.0": set --api-key (or MCP_COMMANDS_API_KEY), or pass --insecure-no-auth explicitly to accept the risk
  ```

  The escape hatch `--insecure-no-auth` starts it anyway with a loud `UNAUTHENTICATED` warning; use it only for trusted networks.
- The client connects to `http://host:port` and sends `Authorization: Bearer <token>` when auth is enabled ([details](#authentication)).
- For browser-based clients, configure CORS — [CORS](#cors-browser-clients).

Both (and most other operational settings) can also be declared once in a [config file](#config-file) passed via `--config <path>`.

## Timeouts

Every tool execution is bounded by a timeout (default: **5 minutes**). When the deadline passes, the script is killed and the tool returns an error of the form `tool timed out after <duration>` (plus any partial output). Three levels, in precedence order:

| Level | How | Meaning |
|---|---|---|
| **Per tool** | `Timeout: <duration>` in the script's frontmatter (first occurrence wins) | Always wins, even over `--no-timeout`. |
| **Global** | `--timeout <duration>` flag | Applies to every tool without its own `Timeout:`. Invalid or explicitly empty values exit with an error before the server starts. |
| **Default** | _(no flag, no frontmatter)_ | 5 minutes. |

The duration is a whitespace-separated list of `<digits><unit>` tokens (units `s`, `m`, `h`; e.g. `5m`, `60s`, `1h 30m 5s` — whitespace between tokens is optional). Sub-second units, decimals, and signs are rejected, and values that overflow are rejected with a clear error. `NONE` (case-insensitive) or a result of `0` (e.g. `0s`) mean **no timeout** — but a client abort/cancel still kills the running script; "no timeout" means "no deadline", never "uninterruptible".

```bash
mcp-commands --dir /path/to/workdir --scripts /path/to/scripts --timeout 10m   # global: 10 minutes
mcp-commands --dir /path/to/workdir --scripts /path/to/scripts --no-timeout    # no deadline at all
```

`--timeout` and `--no-timeout` are **mutually exclusive** — passing both is a startup error. The registered tool description carries a `(timeout: 30s)` / `(timeout: none)` suffix so the LLM knows its budget.

## Production & Security

### Authentication

The HTTP server accepts requests without authentication **when bound to loopback** (the default `127.0.0.1`). Protect it with a static API token:

```bash
# Recommended — from a file (the token never appears in a process listing)
mcp-commands --dir /path/to/workdir --scripts /path/to/scripts --port 8080 --api-key-file /path/to/token.txt

# Or via the environment
MCP_COMMANDS_API_KEY=my-secret-token mcp-commands --dir /path/to/workdir --scripts /path/to/scripts --port 8080
```

- Precedence: `--api-key` > `--api-key-file` > `MCP_COMMANDS_API_KEY`. **Prefer the file or env over `--api-key <value>`**: command-line arguments are world-readable via `/proc/<pid>/cmdline` for the server's entire lifetime.
- `--api-key-file` content is trimmed (a trailing newline is fine); a file over **8 KiB** is rejected as a likely misconfiguration, and an empty file is a startup error rather than a silent fall-through to no-auth.
- When a token is configured, every MCP request must send `Authorization: Bearer <token>` (scheme case-insensitive; exactly one space) or it is rejected with `401`. CORS preflight `OPTIONS` requests are answered by the CORS layer before authentication. The token is never logged. Enabling auth is a breaking change for existing HTTP clients.
- **Stdio mode needs no token** — the flag/env are ignored without `--port`.

```bash
curl -s http://localhost:8080 \
  -H "Authorization: Bearer my-secret-token" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"curl","version":"1.0"}}}'
```

MCP clients that support custom headers are configured the same way, e.g. a generic JSON client config:

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

### TLS

The HTTP transport is **cleartext by default**: the bearer token and every request body transit unencrypted. For production, terminate TLS — put a TLS-terminating proxy (Caddy/nginx) in front, or serve HTTPS directly:

```bash
mcp-commands --dir /path/to/workdir --scripts /path/to/scripts --port 8443 \
  --tls-cert /path/to/cert.pem --tls-key /path/to/key.pem
```

- `--tls-cert` and `--tls-key` must be **given together** (exactly one is a startup error); both files are checked at startup, so an unreadable file fails fast before binding.
- Ignored in stdio mode, like the other HTTP-only options. The startup log carries a `TLS` note when active; binding a non-loopback host with auth but without TLS prints a loud startup warning (disregard it behind a TLS-terminating proxy).

### CORS (Browser Clients)

CORS is **off by default**. To let browser-based clients talk to the streamable HTTP transport:

```bash
mcp-commands --dir /path/to/workdir --scripts /path/to/scripts --port 8080 \
  --allowed-origins https://app.example.com,https://chat.example.com
```

| Flag | Env | Meaning |
|---|---|---|
| `--allowed-origins <o1,o2,...>` | `MCP_COMMANDS_ALLOWED_ORIGINS` | Comma-separated **exact** origin allowlist; validated at startup. Default ports are normalized on match (`http://app.example.com:80` ≡ `http://app.example.com`). An explicitly empty value disables the allowlist. |
| `--allow-all-origins` | `MCP_COMMANDS_ALLOW_ALL_ORIGINS` (`1`/`true`/`yes`) | Echo any `Origin`. **Dev convenience only** — safe only with `--api-key` + TLS. |
| `--disable-localhost-protection` | *(none, deliberate)* | Disables the SDK's DNS-rebinding 403 for loopback servers (dev setups behind a tunnel/LAN hostname). |

Each flag wins over its env var. Key rules: CORS is **not a security boundary** for this server (it only gates which page's JavaScript can read responses); never combine `--allow-all-origins` with no auth; use a fetch-based client (e.g. the MCP TypeScript SDK) with an explicit `Accept: application/json, text/event-stream` header; production requires TLS.

Full reference — origin matching (IDN/punycode), stateless transport behavior, protocol version negotiation, and a TS SDK client example: [docs/reference/cors.md](docs/reference/cors.md).

### Concurrency & Request Limits

- **Concurrency:** at most `--max-concurrent` tool subprocesses run at once (default **16**, `0` selects the default, validated `1..256`). A call arriving when the cap is full gets a clean in-band MCP error — `mcp-commands is at capacity (16 concurrent tool executions); please retry shortly` — that the client can retry. Lower it on small hosts; raise it for bursty clients.
- **Request size:** bodies larger than **10 MiB** are rejected with `413` (never buffered past the cap, so an oversized body cannot exhaust server memory).
- **Request time:** a body taking more than **30 s** in total to arrive is rejected with `400` (stops a slow-drip client from pinning a connection).

Both limits are fixed, not flags — generous for the real workload (small JSON tool calls), there to bound resource use.

### Logging

All diagnostics go to **stderr** as one-line slog records (`<LEVEL>@HH:mm:ss message key=value …`); stdout is reserved for program output (the MCP protocol in stdio mode, the tool list / `--call-tool` result in diagnostics). `--log-level <level>` (or `LOG_LEVEL`) sets the minimum level — accepted in every mode: `trace`, `debug`, `info` (default), `warn`, `error`.

- **`info`** — startup/shutdown milestones, watch setup, warnings, errors.
- **`debug`** — discovery summary, file events, rescans that change the tool set, every completed tool call (bounded to 64 KiB of params).
- **`trace`** — full verbatim stdout/stderr of every tool call (capped 1 MiB each) plus raw request params.

Record format rules, the failed-call record pair, the failure `reason` taxonomy, and client-identity fields: [docs/reference/logging.md](docs/reference/logging.md).

## Troubleshooting

### Diagnostics

Two self-contained diagnostic modes reuse the exact discovery, frontmatter parsing and validation of the server — and never start a server.

**List tools** — what the MCP server would register, in terminal form:

```console
$ mcp-commands --dir . --scripts ./scripts --list-tools

build(profile:str)
     Build the project with the given profile. (timeout: 5m0s)

run([args:str])
     Run the project. (timeout: none)

status()
     Status of the demo app. (timeout: 10s)
```

- Names without parentheses declare no parameters; parameters in brackets are optional, unbracketed ones required.
- The effective per-tool timeout is shown (`none` = no deadline).
- Descriptions are word-wrapped at the terminal width when stdout is a TTY (re-queried on every print, so resizes are honored), falling back to a fixed 160-rune width when piped.
- Add `--watch` for a live list — re-prints only on actual changes, runs until `Ctrl-C`.

**Call one tool** — run it once, bypassing the MCP protocol:

```console
$ mcp-commands --dir . --scripts ./scripts --call-tool build --params '{"profile":"release"}'

<stdout>
building with release
</stdout>

$ mcp-commands --dir . --scripts ./scripts --call-tool build
invalid tool arguments: validating root: missing properties: 'profile'
```

- `--params` is a JSON object (default `{}`); an explicitly empty value and a JSON `null` are accepted as `{}`, anything non-object is a startup error. Required-parameter validation applies exactly as in server mode; arguments are translated with the [rules above](#argument-translation).
- Output is printed verbatim under `<stdout>`/`<stderr>` markers (markers appear only for streams that produced output).
- Timeout precedence is the same as server mode (`Timeout:` frontmatter > `--timeout`; `NONE` = no deadline).
- `--call-tool` is mutually exclusive with `--list-tools`; an explicitly empty `--call-tool=` is a startup error.

| Outcome | Exit code | Output |
| --- | --- | --- |
| Tool succeeds | 0 | result on stdout |
| Missing required param, non-zero script exit, or timeout | 1 | tool's result on stdout |
| Unknown tool, invalid `--params`, unstartable script, other operational failure | 1 | reason on stderr, script never started |

Server-mode flags (`--host`, `--port`, `--api-key`, `--api-key-file`, `--tls-cert`/`--tls-key`, `--insecure-no-auth`, `--max-concurrent`, and the CORS flags) are ignored in both diagnostic modes, as is `--watch` with `--call-tool`; if you pass any of them explicitly, a single notice is printed to stderr. `--watch` *is* honored with `--list-tools` (the live-list mode above).

### Common Problems

- **Script not discovered.** Needs the executable bit — `chmod +x <script>`. Verify with `--list-tools`.
- **Frontmatter warnings on stderr.** An invalid `Param:` or `Timeout:` line in the first 30 lines is skipped with a warning. For `Description:` and `Timeout:` the *first occurrence* wins — if the first `Timeout:` is invalid it warns and the global timeout applies, and any later lines are ignored; put a single valid `Timeout:` line first. Discovery and hot reload are not broken.
- **Duplicate tool names.** `a.sh` and `a.py` both register as `a`; rename one to expose both.
- **CORS failures.** Do not set `MCPGODEBUG=enableoriginverification=1` — see [docs/reference/cors.md](docs/reference/cors.md).

## Reference

### Flags

Essentials and modes:

| Flag | Default | Meaning |
|---|---|---|
| `--dir <dir>` | _(required; or in the config file)_ | Working directory where tool scripts are executed. |
| `--scripts <dir>` | _(required; or in the config file)_ | Directory scanned for executable scripts. |
| `--config <path>` | _(none)_ | JSON/JSONC file with recurring settings — format, keys, and precedence in the [Config File section](#config-file). |
| `--watch` | off | Hot-reload in server mode; live re-print in `--list-tools` mode (ignored with `--call-tool`). |
| `--list-tools` | off | Print the tools the server would register, then exit; no server starts. |
| `--call-tool <name>` | _(none)_ | Run one discovered tool once and exit (debug mode; no server starts). |
| `--params <json>` | `{}` | JSON object of named arguments for `--call-tool`. |
| `--version` | off | Print the version and exit. |
| `--log-level <level>` | `info` | Minimum log level: `trace`, `debug`, `info`, `warn`, or `error` (or `LOG_LEVEL` when the flag is absent). |

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
| `--timeout <duration>` | 5m default | Global per-tool timeout (see [Timeouts](#timeouts)). |
| `--no-timeout` | off | Disable the global timeout (mutually exclusive with `--timeout`). |

CORS: `--allowed-origins`, `--allow-all-origins`, `--disable-localhost-protection` — see [CORS](#cors-browser-clients). Config file (`--config <path>`) — format, keys, and precedence in the [Config File section](#config-file).

### Config File

Recurring operational settings can be declared once in a JSON or JSONC file and passed with `--config <path>` instead of being re-typed as flags or environment variables on every invocation:

```bash
mcp-commands --config ./mcp-commands.jsonc
```

The file is read and validated at startup, before the server starts; an invalid file (bad JSON, wrong extension, unknown key, out-of-range or invalid value) is a startup error — the server never starts partially configured. `--version` skips the config file entirely.

**Format:**

- **`.json`** (strict JSON) and **`.jsonc`** (JSON plus `//` line and `/* block */` comments; comment sequences inside string literals are untouched). The format is selected by the file extension (case-insensitive); any other extension is a startup error, and comments in a `.json` file are a parse error.
- **Single JSON document** — content after the closing `}` is rejected.
- **1 MiB size cap** — a larger file fails fast as a likely wrong path.
- **All keys are optional, kebab-case; unknown keys are rejected.** The file is validated against the JSON Schema shipped at [`config.schema.json`](https://github.com/MKuckert/mcp-commands/blob/main/config.schema.json) in the repository root (and embedded into the binary); point your editor's JSON validation at it for inline errors and autocompletion. The `$schema` property is accepted for editor support (e.g. `"https://raw.githubusercontent.com/MKuckert/mcp-commands/main/config.schema.json"`) and ignored by the server.

**Precedence:** per setting, **CLI flag > environment variable > config file > built-in default**. A *present* key counts as explicitly set — `false`, `0`, or `[]` are deliberate values, exactly like an explicitly-set flag. `--config` is a CLI-only flag and never a key in the file.

Top-level keys:

| Key | Type | Flag / env equivalent | Meaning |
|---|---|---|---|
| `dir` | string, non-empty | `--dir` | Working directory where tool scripts are executed. |
| `scripts` | string, non-empty | `--scripts` | Directory scanned for executable scripts. |
| `watch` | boolean | `--watch` | Hot-reload in server mode; live re-print with `--list-tools` (ignored with `--call-tool`). An explicit `--watch=false` still wins over the file. |
| `log-level` | string: `trace`, `debug`, `info`, `warn`, or `error` | `--log-level` / `LOG_LEVEL` | Minimum log level; the env var is consulted before the file. |
| `timeout` | string: a duration (`5m`, `1h 30m 5s`) or `NONE` | `--timeout` | Global per-tool timeout ([Timeouts](#timeouts) for the format); `NONE` or `0s` = no deadline. An explicit `--timeout` or `--no-timeout` wins over the file. |

The `server` object carries the HTTP server settings. In diagnostic modes (`--list-tools` / `--call-tool`) it is **silently ignored** — no notice, unlike server *flags*, which keep their existing ignore report.

| Key | Type | Flag / env equivalent | Meaning |
|---|---|---|---|
| `host` | string, non-empty | `--host` | Bind address for HTTP mode. |
| `port` | integer: `0` (stdio) or `1..65535` | `--port` | A non-zero value switches to HTTP mode. |
| `api-key` | string, non-empty | — | Bearer token; the lowest-precedence link of the chain `--api-key` > `--api-key-file` > `MCP_COMMANDS_API_KEY` > `server.api-key` > `server.api-key-file`. Inline tokens are allowed; for a real secret prefer one of the file-based links. |
| `api-key-file` | string, non-empty | — | Read the token from a file — content trimmed (trailing newline ok), max **8 KiB**, must be non-empty; same rules as `--api-key-file`. |
| `tls-cert` / `tls-key` | string, non-empty | `--tls-cert` / `--tls-key` | Serve HTTPS directly (PEM files, required together). A non-empty flag value wins per file; the pairing and startup readability checks apply to the merged pair across sources. |
| `insecure-no-auth` | boolean | `--insecure-no-auth` | Escape hatch: start an unauthenticated server on a non-loopback host (loudly warned). |
| `max-concurrent` | integer: `0` (default) or `1..256` | `--max-concurrent` | Cap on simultaneous tool executions. |
| `allowed-origins` | array of strings | `--allowed-origins` / `MCP_COMMANDS_ALLOWED_ORIGINS` | Exact origin allowlist for CORS ([CORS](#cors-browser-clients)). A *present* key — even `[]` — is an explicit choice that suppresses the env-var fallback, exactly like `--allowed-origins=`. |
| `allow-all-origins` | boolean | `--allow-all-origins` / `MCP_COMMANDS_ALLOW_ALL_ORIGINS` | Echo any Origin (dev convenience). A *present* key — even `false` — suppresses the env var, exactly like `--allow-all-origins=false`. A non-empty `allowed-origins` (from any source) combined with an effective `allow-all-origins: true` is a startup error. |
| `disable-localhost-protection` | boolean | `--disable-localhost-protection` | Disables the SDK's DNS-rebinding protection for loopback servers (no env fallback, deliberate). |

**Examples.** Top-level only — stdio server with hot reload and a longer tool timeout:

```jsonc
{
  "dir": "/path/to/workdir",
  "scripts": "/path/to/scripts",
  "watch": true,
  "timeout": "10m"
}
```

HTTP server with auth and CORS — everything the flag examples above would pass, in one file:

```jsonc
{
  "dir": "/path/to/workdir",
  "scripts": "/path/to/scripts",
  "server": {
    "host": "0.0.0.0",
    "port": 8443,
    // Inline token — fine for dev; prefer api-key-file for real secrets.
    "api-key": "my-secret-token",
    "tls-cert": "/path/to/cert.pem",
    "tls-key": "/path/to/key.pem",
    "allowed-origins": ["https://app.example.com"]
  }
}
```

`mcp-commands --config ./mcp-commands.jsonc` starts the authenticated HTTPS server with the origin allowlist; adding `--port 9000` on the command line overrides only the port.

### Environment Variables

Binary (each is consulted only when its flag is not set):

| Variable | Equivalent flag |
|---|---|
| `MCP_COMMANDS_API_KEY` | `--api-key` |
| `MCP_COMMANDS_ALLOWED_ORIGINS` | `--allowed-origins` |
| `MCP_COMMANDS_ALLOW_ALL_ORIGINS` | `--allow-all-origins` (`1`/`true`/`yes`) |
| `LOG_LEVEL` | `--log-level` |

Container (the included `Containerfile` wraps a prebuilt release binary; configuration is via environment variables):

| Variable | Default | Meaning |
|---|---|---|
| `MCP_COMMANDS_HOST` | `0.0.0.0` | Bind address |
| `MCP_COMMANDS_PORT` | `8080` | HTTP port |
| `MCP_COMMANDS_DIR` | `/work` | Tool working directory |
| `MCP_COMMANDS_SCRIPTS` | `/scripts` | Scripts directory |
| `MCP_COMMANDS_TIMEOUT` | binary's 5m | Per-tool timeout, e.g. `5m` |
| `MCP_COMMANDS_WATCH` | off | `1`/`true`/`yes` = hot-reload scripts |
| `MCP_COMMANDS_API_KEY`, `MCP_COMMANDS_ALLOWED_ORIGINS`, `MCP_COMMANDS_ALLOW_ALL_ORIGINS`, `LOG_LEVEL` | — | The binary's native env vars, passed through |

```bash
docker build -t mcp-commands .   # pin a release with --build-arg MCP_COMMANDS_VERSION=<version>, a platform with --platform linux/<amd64|arm64>
docker run -p 8080:8080 -v ./my-scripts:/scripts -e MCP_COMMANDS_API_KEY=... mcp-commands
```

The container only sees what you mount — for bridging the host toolchain into a sandbox, run the bare binary on the host over HTTP instead.

### Deeper Documentation

- [Hot reloading behavior](docs/reference/watch.md) — debouncing, tool diffing, symlink staleness, inotify limits
- [CORS reference](docs/reference/cors.md) — origin matching, stateless transport, protocol negotiation, client example
- [Logging reference](docs/reference/logging.md) — record format, levels, failure taxonomy, client identity
- [Shell completion](docs/reference/shell-completion.md) — bash/zsh/fish activation for brew and release installs

### Version

`mcp-commands --version` prints the server version and exits. It works without `--dir`/`--scripts` and skips all other validation, including any `--config` file. Release binaries report their release tag (injected at build time); local source builds without injected ldflags report `commit-local`.

## AI Usage

The implementation of `mcp-commands` is completely done by an AI. The idea and guidance for the plan is mine, the plan writing and code is the AI. It wrote the entire server, including argument parsing, script discovery, and MCP protocol handling, I did the review.

## License

MIT License
