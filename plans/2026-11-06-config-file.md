# PLAN — Dedicated `--config` file (JSON/JSONC) for recurring operational settings

**Branch:** `feature/config-file` (from `main` @ `b318797`, worktree `/workspace/mcp-commands-config-file`)
**Relay:** `config-file` (packet: `.pi-web/relays/config-file/`)
Status: complete — delivered as PR #42 (2026-11-07, relay legs 8–9)

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
- [x] U3: README documentation (format, keys, precedence) + schema finalization

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
- 2026-11-06 (leg 3): U3 done — README `Config File` section (format rules,
  top-level + server key reference tables, precedence with
  presence/explicit-empty semantics, API-key chain, diagnostic-mode
  silent-ignore, two worked examples), consolidated-summary and intro
  pointers; `config.schema.json` finalized — key set verified against the
  documented reference (five top-level + eleven server keys,
  additionalProperties: false throughout) and the `$schema` meta-key added
  for editor support (accepted, ignored by the server; coupled test in
  `config_test.go`); consistency pass between README statements and the
  error strings/behavior in `flags.go`, `http.go`, `config.go`.
- 2026-11-07 (leg 9, Copilot review remediation on PR #42): all four
  comments triaged as real concerns and fixed — (1) duplicate `server` keys:
  the struct is now decoded from the *validated* document (the map decode
  collapses duplicate keys, last wins), so the applied config can never
  retain a value absent from the validated document (an earlier duplicate's
  `insecure-no-auth` could previously survive); (2) `server.allowed-origins`
  file array: each element is validated as-is as an exact origin — no CSV
  comma splitting or empty-entry filtering, so `"a,b"` in one element and
  `""` now fail fast instead of being silently split/filtered (flag/env CSV
  semantics unchanged; explicit `[]` still means "no origins"); (3) test
  fixtures JSON-escape filesystem paths (Windows temp-dir backslashes are
  invalid JSON escapes); (4) api-key tests isolate an inherited
  `MCP_COMMANDS_API_KEY` via `t.Setenv`. Regression tests:
  `duplicate_server_keys_last_object_wins`, `file_element_with_comma_rejected`,
  `file_empty_element_rejected`, `file_multi_element`.
