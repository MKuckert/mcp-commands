# PLAN — Dedicated `--config` file (JSON/JSONC) for recurring operational settings

**Branch:** `feature/config-file` (from `main` @ `b318797`, worktree `/workspace/mcp-commands-config-file`)
**Relay:** `config-file` (packet: `.pi-web/relays/config-file/`)
Status: in progress — U2 complete (2026-11-06, relay leg 2)

## Scope

- [x] U1: `--config <path>` flag + file loading (extension dispatch `.json`/`.jsonc`, size
      cap, JSONC comments) + embedded-JSON-Schema validation + merge of the five top-level
      shared keys (`dir`, `scripts`, `watch`, `log-level`, `timeout`) with
      flag > env > file precedence; completions updated
- [x] U2: `server` section keys (`host`, `port`, `api-key`, `api-key-file`, `tls-cert`,
      `tls-key`, `insecure-no-auth`, `max-concurrent`, `allowed-origins`,
      `allow-all-origins`, `disable-localhost-protection`) with cross-source validation
      (TLS pairing, CORS mutual exclusion across flag+file) and silent-ignore in
      diagnostic modes
- [ ] U3: README documentation (format, keys, precedence) + schema finalization

## Design decisions

1. **Schema-first validation with the existing `santhosh-tekuri/jsonschema` dependency.**
   The shipped `config.schema.json` (repo root, `go:embed`'d into the binary) defines the
   key set structurally: types, `additionalProperties: false` (unknown keys rejected),
   `minLength: 1` where an empty value is meaningless (`dir`, `scripts`, `timeout`).
   Value *semantics* (duration format, log-level set) stay in the existing resolvers
   (`parseTimeoutDuration`, `resolveLogLevel`) so one error format covers flag, env, and
   file sources; the resolvers receive a source label for error text.
2. **Key presence = explicit set.** The file decodes into `fileConfig` with pointer
   fields; a present-but-false/empty-meaning key (e.g. `watch: false`) is distinguishable
   from an absent key and participates in precedence exactly like an explicitly-set flag.
   Precedence chain per key: CLI flag (visit-tracked, so `--watch=false` counts) > env var
   (where one exists: `LOG_LEVEL`) > config file > built-in default. Where explicit-empty
   is meaningful (`allowed-origins`, `allow-all-origins`), a *present* file key — even with
   a zero value (`[]`, `false`) — is an explicit choice that suppresses the env-var
   fallback, mirroring an explicitly-set flag; the effective chain there is
   flag > file (present) > env.
3. **Extension dispatch:** `.json` strict, `.jsonc` comment-stripped (case-insensitive
   extension); any other extension is a startup error. The JSONC stripper replaces comment
   bytes with spaces (newlines kept) so byte offsets in later decode errors still point at
   the original file; comment sequences inside string literals are untouched.
4. **Size cap:** 1 MiB via the same `LimitReader(cap+1)` pattern as `--api-key-file`;
   over-size is a startup error ("wrong file?").
5. **Strict single-document decode:** a second `Decode` must hit EOF, so trailing garbage
   is rejected (mirrors `parseToolArguments`).
6. **`--version` skips the config load entirely** (works without dir/scripts and without a
   readable config file). `--config` is honored in diagnostic modes for the shared keys;
   the server section is silently ignored there (D2, landed with U2).
7. ~~Leg-1 schema deliberately omits `server`~~ (superseded by U2): a file containing it
   now validates against the full schema; unknown keys *inside* `server` are rejected.

## Verification (per leg)

- Focused: `go build ./...`, `go vet ./...`, `gofmt -l .`, `go test ./...`
- Full (before whole-work review): + coverage profile, CI gate ≥80% excluding `prod.go`
- Built-binary smoke: `--config` with valid/invalid files through real invocations

## Progress log

- 2026-11-06 (leg 1): U1 implemented — `config.go` (loader, JSONC stripper, embedded
  schema), `config.schema.json`, `flags.go` (`--config` flag, five-key merge into
  resolution), `timeout.go` (`resolveTimeout` file tier), completion scripts, tests
  (`config_test.go`, `timeout_test.go` file-tier cases).
- 2026-11-06 (leg 2): U2 implemented — `config.schema.json` (`server` object,
  kebab-case, additionalProperties: false), `config.go` (`fileServerConfig`, pointer
  fields for presence), `http.go` (`resolveAPIKey` config tier with new
  `apiKeySourceConfig`, shared `readAPIKeyFile`; `resolveCORS` file tier with
  presence-suppresses-env semantics), `flags.go` (server-section merge in server mode:
  host/port/max-concurrent/insecure-no-auth/disable-localhost-protection with
  visit-tracked flag precedence, source-labeled range checks, cross-source TLS pairing
  + fail-fast file check; diagnostic modes return before the merge, so the section is
  silently ignored per D2). Tests: `config_test.go` (server-section decode, schema
  rejections, `TestParseCLIServerSection` merge/precedence/validation/silent-ignore),
  `http_test.go` (resolveAPIKey/resolveCORS file-tier cases, source String). Built-binary
  smoke: `.jsonc` server config starts an authenticated HTTP server with file CORS;
  fail-fast on bad origin, out-of-range port, unpaired TLS. Quality pass (subagent,
  report-only) found no blockers; four non-blocking findings fixed: CORS
  mutual-exclusion error now names the effective sources (e.g. "config file
  server.allowed-origins and --allow-all-origins"), config-tier api-key-file empty hint
  no longer promises an env fallback that cannot happen, startup refusal/warning
  messages mention `server.api-key`, and the untested chain links got direct coverage
  (env > file api-key-file, flag api-key-file > file api-key, explicit-empty flag +
  file origins, `--call-tool` silent-ignore, file-only tls-key).
