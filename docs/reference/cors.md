# CORS (Browser Clients) — Deep Dive

How to let browser-based MCP clients (web chat UIs, in-browser agents) talk to
the streamable HTTP transport.

## Configuration

CORS is **off by default** — with no flags set, the server sends no CORS
headers at all. To opt in:

```bash
mcp-commands --dir /path/to/workdir --scripts /path/to/scripts --port 8080 \
  --allowed-origins https://app.example.com,https://chat.example.com
```

| Flag | Env | Meaning |
|---|---|---|
| `--allowed-origins <o1,o2,...>` | `MCP_COMMANDS_ALLOWED_ORIGINS` | Comma-separated **exact** origin allowlist (`https://app.example.com`). Origins are validated at startup (must be `http`/`https` + host, no path/userinfo) and the flag fails fast even in stdio mode. Default ports are normalized on match, so `http://app.example.com:80` and `http://app.example.com` are equivalent — matching what browsers send in the `Origin` header (which omits default ports). Non-ASCII (IDN) hostnames are compared as given: browsers send punycode, so list the punycode form (`https://xn--bcher-kva.example`) for Unicode domains. An explicitly empty `--allowed-origins=` means “no origins” — it disables the allowlist and does **not** fall back to the env var. |
| `--allow-all-origins` | `MCP_COMMANDS_ALLOW_ALL_ORIGINS` (`1`/`true`/`yes`) | Echo any `Origin`. **Dev convenience only** — safe only with `--api-key` + TLS. The env var is consulted only when the flag is not set at all; an explicit `--allow-all-origins=false` suppresses it. |
| `--disable-localhost-protection` | *(none, deliberate)* | Disables the SDK's DNS-rebinding 403 for servers on loopback. For dev setups where the page is served from a tunnel/LAN hostname that resolves to `127.0.0.1`. This flag intentionally has no env fallback — it is a mode choice, not a secret. |

Each flag wins over its env var.

## Security

- This server executes local scripts, so CORS is **not** a security boundary —
  it only gates which page's JavaScript can *read* responses. Use the explicit
  `--allowed-origins` list in production; never `--allow-all-origins` on a
  public, unauthenticated server.
- `--allow-all-origins` combined with **no** `--api-key` prints a loud startup
  warning: any web page opened in a browser can then invoke tools against the
  server and read their output.
- **Production requires TLS:** put a TLS-terminating proxy (Caddy/nginx) in
  front — the proxy can also add CORS as an alternative to these flags — or
  serve HTTPS directly with `--tls-cert`/`--tls-key`.

## Protocol behavior

- **The HTTP transport is always stateless** (behavior change in 0.5.0): each
  request stands on its own. go-sdk v1.8.0 neither issues nor reads a
  `Mcp-Session-Id` header in stateless mode, so clients that stored and
  resend a session ID keep working (the SDK ignores it). A request missing the
  `Mcp-Protocol-Version` header defaults to `2025-03-26` (the oldest supported
  version). Non-`POST` methods (including the `GET` SSE stream) return 405.
- **Protocol `2026-07-28` is negotiated with modern clients** (go-sdk ≥
  v1.7.0): current-generation clients (new MCP TypeScript SDK, GitHub MCP)
  skip the `initialize` handshake and send the protocol version per request
  via `_meta`. Tool calls behave identically to the legacy protocol, and older
  clients (`2025-03-26` / `2025-11-25`) are unaffected — the SDK negotiates
  the highest mutually supported version.

## Client requirements

- **Use a fetch-based client**, e.g. the official MCP TypeScript SDK — raw
  `EventSource` cannot work in any mode. Send an explicit
  `Accept: application/json, text/event-stream` header on POST — that is the
  documented contract (the TS SDK sends both `Accept` values automatically);
  some clients that send only `*/*` happen to work, but do not rely on it.
- **Do not set `MCPGODEBUG=enableoriginverification=1`** to "fix" CORS
  failures: it makes the SDK 403 *all* cross-origin requests inside the
  handler, where the CORS middleware cannot recover.

## Example: MCP TypeScript SDK

```js
// The URL is the mcp-commands transport endpoint — a separate origin from
// the page. It is the *page's* origin that --allowed-origins must list.
const transport = new StreamableHTTPClientTransport(new URL("https://mcp.example.com"), {
  requestInit: { headers: { Authorization: `Bearer ${token}` } },
});
const client = new Client({ name: "web-client", version: "1.0.0" });
await client.connect(transport);
```
