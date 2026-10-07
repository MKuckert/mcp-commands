# mcp-commands container image
#
# The image downloads a prebuilt release binary (static Go build) — no Go
# toolchain needed. It is intended for *self-contained* script sets: mount
# your scripts at /scripts. For bridging the host toolchain into a sandbox,
# run the bare binary on the host instead (see the README).
#
# Build args:
#   MCP_COMMANDS_VERSION   release version to install, e.g. 0.11.0
#   TARGETARCH             amd64 | arm64 (set automatically by BuildKit;
#                          pass --platform to build for a specific one)
#
# Runtime env vars (all optional):
#   MCP_COMMANDS_HOST               bind address            (default: 0.0.0.0)
#   MCP_COMMANDS_PORT               HTTP port               (default: 8080)
#   MCP_COMMANDS_DIR                tool working directory  (default: /work)
#   MCP_COMMANDS_SCRIPTS            scripts directory       (default: /scripts)
#   MCP_COMMANDS_API_KEY            bearer token            (native binary env var)
#   MCP_COMMANDS_ALLOWED_ORIGINS    comma-separated CORS allowlist (native)
#   MCP_COMMANDS_ALLOW_ALL_ORIGINS  true = echo any Origin  (native)
#   MCP_COMMANDS_TIMEOUT            per-tool timeout, e.g. 5m (default: binary's 5m)
#   MCP_COMMANDS_WATCH              true = hot-reload scripts (default: false)
#   LOG_LEVEL                       debug|info|warn|error   (native)
#
# Build:
#   docker build -t mcp-commands --platform linux/amd64 .
#   docker build --build-arg MCP_COMMANDS_VERSION=0.11.0 -t mcp-commands:0.11.0 .
#
# Run:
#   docker run -p 8080:8080 -v ./my-scripts:/scripts \
#     -e MCP_COMMANDS_API_KEY=... mcp-commands

ARG MCP_COMMANDS_VERSION=0.11.0
ARG TARGETARCH

FROM alpine:3.21

RUN set -eux; \
	arch="${TARGETARCH:-amd64}"; \
	case "$arch" in \
		amd64 | arm64) ;; \
		*) echo "unsupported TARGETARCH: $arch" >&2; exit 1 ;; \
	esac; \
	curl -fsSL "https://github.com/MKuckert/mcp-commands/releases/download/v${MCP_COMMANDS_VERSION}/mcp-commands_${MCP_COMMANDS_VERSION}_linux_${arch}.tar.gz" | tar -xz; \
	install -m 0755 mcp-commands /usr/local/bin/mcp-commands; \
	rm -rf mcp-commands LICENSE README.md

COPY container/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod 0755 /usr/local/bin/entrypoint.sh

RUN adduser -D -u 10001 mcpuser
USER 10001

RUN mkdir -p /work /scripts
WORKDIR /work
VOLUME ["/work", "/scripts"]

EXPOSE 8080

# Liveness probe: discovery over the mounted directories must succeed.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
	CMD /usr/local/bin/mcp-commands --dir /work --scripts /scripts --list-tools >/dev/null

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
