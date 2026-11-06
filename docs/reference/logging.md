# Logging — Deep Dive

`mcp-commands` logs all diagnostics to **stderr** as [slog](https://pkg.go.dev/log/slog) records, one line each:

```
<LEVEL>@HH:mm:ss message key=value …
```

## Record format

- The message is unquoted; when the record carries attributes, a ` | `
  separator joins it to the first `key=value` (a record with no attributes
  ends right after the message).
- Attribute values containing whitespace or a control character are
  double-quoted; control characters — including terminal escape sequences
  that can ride in client-controlled values such as the client identity —
  render as Go-style escapes (`\x1b`), so a remote client cannot corrupt the
  one-line format or drive the operator's terminal.

```
WARN@18:02:11 ignoring invalid Timeout | file=/path/to/scripts/render.sh error="invalid timeout ..."
```

- **stdout is reserved for program output only:** in stdio mode it carries the
  MCP protocol itself; in diagnostics it carries the tool list, `--call-tool`
  result text, `-h` help, and `--version`. Routing logs to stdout would
  corrupt the protocol, so every record goes to stderr in all modes. In HTTP
  mode stdout is not used at all.
- Flag-parse and validation failures occur before the logger exists and are
  printed as raw `Error: …` lines — slog formatting (and `--log-level`) do
  not apply to startup configuration errors.

## Levels

`--log-level <level>` (or `LOG_LEVEL` when the flag is absent) sets the
minimum level emitted: `trace`, `debug`, `info` (default), `warn`, `error`.

- **`info` (default)** — operational milestones: `Starting HTTP server` /
  `Starting stdio server`, `shutting down` on a termination signal,
  `server stopped` on clean exit, watch setup, and all warnings and errors.
- **`debug`** — operational detail: the discovery summary, each file event the
  watch loop sees (`path`, `op`, `valid`), each rescan that changes the tool
  set (the `added`/`removed`/`changed` names), watch reattach attempts, every
  completed tool call (tool, path, command, exit code, output byte counts,
  duration, `truncated`, the client's arguments JSON — bounded to **64 KiB**
  with a `…` marker, and — when the request carries it — the calling client's
  name and version), and allowed CORS preflights.
- **`trace`** — the raw material: for every tool call, the full stdout and
  stderr of the script, capped at **1 MiB each** with a `truncated` marker in
  the record — everything a client could have seen, verbatim — plus the raw
  request params (uncapped; the `WARN`/`DEBUG` copies are bounded). Note a
  *failed* call writes that output in two records (the `WARN` and its `TRACE`
  pair), so expect up to ~4 MiB of log per failed call at this level.

## Failed tool calls

The record pair for a failing call (`WARN` at the default level, `TRACE` at
`trace`) — the `TRACE` record carries the same fields, with `params` raw (the
`WARN` copy is bounded to 1 KiB):

```
WARN@18:02:11 tool call failed | tool=render path=/scripts/render.sh command="/scripts/render.sh" exitCode=1 stdoutBytes=0 stderrBytes=5 duration=1.2s truncated=false params={"size":"large"} reason=nonzero-exit stdout= stderr="boom\n"
TRACE@18:02:11 tool call failed | tool=render path=/scripts/render.sh command="/scripts/render.sh" exitCode=1 stdoutBytes=0 stderrBytes=5 duration=1.2s truncated=false params={"size":"large"} reason=nonzero-exit stdout= stderr="boom\n"
```

For a large payload the two lines differ in exactly one place: `params` is raw on the `TRACE` line, bounded to 1 KiB (with a `…` marker) on the `WARN` line.

Failed calls carry a `reason` from a fixed taxonomy: `timeout` (the script
outlived its deadline), `nonzero-exit`, `canceled` (the client aborted the
request), or `start-failed` (the process never started, e.g. the script is
missing).

## Client identity

Tool-call records additionally carry the calling client's identity when the
request provides it — `clientName` and `clientVersion`, sourced from the SDK's
per-request `ClientInfo()`. Clients on the 2026-07-28 protocol send
`clientInfo` in every request's `_meta`, so their identity is always present;
legacy-protocol clients carry it from the `initialize` handshake over stdio,
and a stateless HTTP session without one omits the fields entirely rather than
logging "unknown". Each field is cut at 256 bytes with an explicit truncation
marker, so a hostile client cannot inflate the record.
