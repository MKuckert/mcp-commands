#!/bin/sh
# Entrypoint for the mcp-commands container image.
#
# Maps MCP_COMMANDS_* environment variables onto CLI flags, then execs the
# binary with those flags plus any extra args passed to the container
# (e.g. `docker run ... --disable-localhost-protection`).
#
# Auth, CORS, and logging are handled natively by the binary through its
# own env vars — no flags needed here:
#   MCP_COMMANDS_API_KEY, MCP_COMMANDS_ALLOWED_ORIGINS,
#   MCP_COMMANDS_ALLOW_ALL_ORIGINS, LOG_LEVEL
set -eu

host="${MCP_COMMANDS_HOST:-0.0.0.0}"
port="${MCP_COMMANDS_PORT:-8080}"
dir="${MCP_COMMANDS_DIR:-/work}"
scripts="${MCP_COMMANDS_SCRIPTS:-/scripts}"

set -- --host "$host" --port "$port" --dir "$dir" --scripts "$scripts" "$@"

if [ -n "${MCP_COMMANDS_TIMEOUT:-}" ]; then
	set -- "$@" --timeout "$MCP_COMMANDS_TIMEOUT"
fi

if [ "${MCP_COMMANDS_WATCH:-false}" = "true" ]; then
	set -- "$@" --watch
fi

exec /usr/local/bin/mcp-commands "$@"
