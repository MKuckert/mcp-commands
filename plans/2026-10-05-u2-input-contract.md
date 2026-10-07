# PLAN — U2: Enforce the input contract

**Branch:** `fix/u2-input-contract` from `main` (v0.9.1). **Target:** v0.9.2.
**Source:** `REVIEW_REPORT.md` (removed after merge) §U2 (H2, M1, M2, L3).
**Status:** Done.

## Tasks

- [x] Publish closed JSON input schemas (`additionalProperties: false`) for declared and zero-param tools. Validate both MCP and diagnostic calls against precisely that schema before acquiring a slot or starting a subprocess. Invalid values return clear tool errors; malformed JSON remains an operational error. No implicit `null` acceptance for declared required values.
- [x] Normalize duplicate `Param:` declarations once (last declaration wins, first position retained) and use that representation for the schema, list signature, and runtime validation. Cover both declaration orders.
- [x] Decode JSON numbers with `UseNumber` so large integers and exponent spelling reach CLI unchanged; reject wrong scalar/array/object types. Preserve `false` as a valid required boolean which emits no CLI flag.
- [x] Fix ordered argv assertion; cover MCP and diagnostic calls with undeclared keys, invalid types, null, duplicate declarations, and precision; verify rejected calls never execute the script or consume a slot.
- [x] Bump `main.go` and `Makefile` to 0.9.2; run formatting, `go test ./... -count=1`, `go vet ./...`, and version smoke check.

## Design

Use `github.com/santhosh-tekuri/jsonschema/v6` for schema validation: the SDK's existing `github.com/google/jsonschema-go` incorrectly classifies `json.Number` as a JSON string during type validation (confirmed by integration tests), so it cannot validate numbers without losing precision. Compile/resolve a tool's *published* schema once at registration (and once for a diagnostic call), and reuse the resolved immutable validator per call. Keep parsing and validation as a shared helper. Preserve the existing empty/JSON-null top-level arguments normalization to `{}`. No broad lifecycle or discovery changes.

## Review log

Approved cf7ef20 with correction f509671. Reviewed correction diff and zero-param MCP/diagnostic rejection and success coverage; README example matches sorted argv. `go test ./... -count=1`, `go vet ./...`, `gofmt -l *.go`, `go run . --version` (0.9.2), and `git diff --check cf7ef20..f509671` passed. Only reviewer checks `[x]`.
