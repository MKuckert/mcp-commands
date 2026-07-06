# Plan: Typed Parameter Declarations for Script-Based MCP Tools

## Objective

Allow scripts to declare their own parameter interface via inline comment annotations
(mirroring the existing `Description:` pattern). The MCP server parses these annotations
and builds a proper JSON Schema `properties` + `required` block per tool instead of
the current generic `additionalProperties: { type: string }`. The server also validates
required parameters before executing the script, failing loud on missing values.

---

## Requirements & Decisions

- **Frameworks:** Go standard library, `github.com/modelcontextprotocol/go-sdk`, `fsnotify`
- **Chosen Libraries:** none — no new dependencies
- **Supported types:** `string`, `number`, `boolean`
- **Annotation syntax:**
  ```
  # Param: <name> <type> <required|optional> "<description>"
  ```
  Example:
  ```bash
  #!/bin/bash
  # Description: Convert a file to PDF
  # Param: path string required "Path to the input file"
  # Param: dpi number optional "DPI for rasterisation (default 150)"
  # Param: verbose boolean optional "Print progress"
  ```
- **No-param behaviour:** A script with zero `Param:` lines emits an empty schema
  (`{ "type": "object", "properties": {} }`). No generic fallback.
- **Scan window:** Expanded from 10 to **30 lines** (new constant `scanHeaderLines = 30`).
  The old `scanDescriptionLines` constant is renamed/replaced.
- **Required enforcement:** Server-side. Go validates that every required parameter is
  present in the call before exec. Returns `IsError: true` with a descriptive message.
- **Error Handling Strategy:**
  - Malformed `Param:` line (wrong field count, unknown type, bad name) → **log a warning**
    at startup/reload and **skip that annotation** (the parameter is not added to the
    schema). Do not crash the server.
  - Missing required parameter at call time → return `IsError: true` with message:
    `"missing required parameter: <name>"`. Do not exec the script.
  - Unknown/extra parameters passed by the agent are silently forwarded (JSON Schema
    does not set `additionalProperties: false`; the script decides what to do with them).

---

## Implementation Steps

> Status Markers: [ ] Open, [/] In Progress, [x] Completed (set after accepted review only!)

- [x] **Task 1: Define `paramSpec` type and parse `Param:` annotations**
  - **Description:** Introduce a `paramSpec` struct:
    ```go
    type paramSpec struct {
        Name        string // validated against argumentKeyPattern
        Type        string // "string" | "number" | "boolean"
        Required    bool
        Description string
    }
    ```
    Add function `extractParams(filePath string) []paramSpec` that:
    1. Opens the file, scans up to `scanHeaderLines` (30) lines.
    2. For each line, checks `strings.Contains(line, "Param:")`.
    3. Splits on `"Param:"` and trims. Remainder must match:
       `<name> <type> <required|optional> "<description>"` — parse with a small
       regex or manual split (split on spaces, last field is everything inside quotes).
    4. Validates `name` against `argumentKeyPattern`, `type` against the allowed set,
       and `required/optional` token. On any validation failure bail out with a clear warning
       indicating the parse error: `fmt.Fprintf(os.Stderr, "Warning: skipping invalid Param
       annotation in %s because %s: %q\n", filePath, reason, rawLine)`.
    5. Returns the valid `[]paramSpec` (empty slice if none).
  - **Review Criteria:**
    - Parses all valid combinations of the three types and both required/optional tokens.
    - Descriptions with internal spaces (quoted) are extracted verbatim.
    - Invalid lines produce a stderr warning and are skipped without panic.
    - An empty file or a file with no `Param:` lines returns `[]paramSpec{}`.
    - A `Param:` line with an unquoted description (no leading `"`) is treated as
      malformed, emits a stderr warning, and is skipped. There's no escaping mechanism for
      quotes inside the description; authors must avoid them.

- [x] **Task 2: Extend `discoveredTool` with `Params`**
  - **Description:** Add `Params []paramSpec` to `discoveredTool`. In `discoverTools`,
    call `extractParams(resolvedPath)` and populate `discoveredTool.Params`. Update
    `scanDescriptionLines` constant: rename to `scanHeaderLines = 30` and use it in
    both `extractDescription` and `extractParams` (share one file-scan pass if
    feasible, or make two passes — correctness over micro-optimisation).
  - **Review Criteria:**
    - `discoveredTool.Params` is correctly populated in discovery.
    - `scanHeaderLines = 30` is used everywhere the old `scanDescriptionLines` was used.
    - No regression in `extractDescription` behaviour.

- [x] **Task 3: Build per-tool JSON Schema from `paramSpec`**
  - **Description:** Extract a helper `buildInputSchema(params []paramSpec) json.RawMessage`.
    - Iterate `params` in order. Use a `map[string]any` for `properties`; write each
      param entry keyed by `Name` (last write wins for duplicates — this is the
      **single deduplication point** for both `properties` and `required`).
    - After building `properties`, iterate `params` a second time to collect required
      names: use a `seen map[string]bool` to emit each name into the `required` slice
      at most once, and only if the *last-seen* spec for that name has `Required == true`.
      Concretely: build a `lastRequired map[string]bool` in the first pass (same
      iteration), then in the second pass emit names whose `lastRequired[name]` is `true`.
    - `"required"` key omitted entirely if the resulting slice is empty.
    - Always sets `"type": "object"`. Does NOT set `additionalProperties`.
    - Example output for zero params:
      ```json
      {"type":"object","properties":{}}
      ```
    - Example output with one required and one optional:
      ```json
      {
        "type": "object",
        "properties": {
          "path":    {"type":"string","description":"Path to the input file"},
          "verbose": {"type":"boolean","description":"Print progress"}
        },
        "required": ["path"]
      }
      ```
  - **Review Criteria:**
    - Helper is pure (no I/O, testable in isolation).
    - `"required"` key is absent when no params are required.
    - `"properties"` is always present (empty `{}` when no params).
    - Correct JSON Schema type strings for all three supported types.
    - Duplicate param names: `properties` contains only one entry (the last); `required`
      contains the name at most once, present only if the last declaration was `required`.

- [x] **Task 4: Wire schema into `toolRegistry.replace`**
  - **Description:** In `toolRegistry.replace`, replace the hardcoded `InputSchema`
    with `buildInputSchema(discoveredTool.Params)`. No other changes to the registration
    logic.
  - **Review Criteria:**
    - Tools with `Param:` annotations expose correct schema to MCP clients.
    - Tools without annotations expose `{"type":"object","properties":{}}`.
    - Hot-reload (`watchTools`) picks up schema changes because `replace` is called on
      every rediscovery.

- [x] **Task 5: Server-side required-parameter validation**
  - **Description:** Use the **closure-with-pre-parse** approach to avoid doubling
    `executeTool`'s responsibilities:

    1. Extract a pure helper:
       ```go
       func validateRequiredParams(tool string, args map[string]any, params []paramSpec) error
       ```
       Iterates `params`; for each `Required == true`, checks `args[name]` exists
       (key presence only — nil values count as absent). Returns the first missing
       param as an error: `fmt.Errorf("missing required parameter %s in tool call %s", name, tool)`.

    2. In `toolRegistry.replace`, the `AddTool` callback becomes:
       ```go
       func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
           parsedArgs, err := parseToolArguments(req.Params.Arguments)
           if err != nil {
               return nil, err
           }
           if err := validateRequiredParams(req.Params.Name, parsedArgs, toolParams); err != nil {
               return &mcp.CallToolResult{
                   Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
                   IsError: true,
               }, nil
           }
           return executeTool(ctx, toolPath, parsedArgs, defaultToolTimeout, r.dirAbs)
       }
       ```

    3. `executeTool` signature changes to accept **pre-parsed args**:
       ```go
       func executeTool(ctx context.Context, scriptPath string,
           args map[string]any, timeout time.Duration, dir string) (*mcp.CallToolResult, error)
       ```
       Remove the internal `parseToolArguments` call. All other logic unchanged.

    This means `parseToolArguments` is called exactly once per invocation (in the
    closure), validation runs on the parsed map, and `executeTool` receives clean data.

  - **Review Criteria:**
    - `executeTool` signature accepts `map[string]any` (pre-parsed), not `json.RawMessage`.
    - The closure in `toolRegistry.replace` calls `parseToolArguments` then
      `validateRequiredParams` then `executeTool` — in that order.
    - Calling a tool with a missing required param returns `IsError: true` with message
      containing `"missing required parameter: <name>"`.
    - All required params present → proceeds to exec normally.
    - Optional params may be absent without error.
    - `validateRequiredParams` is unit-tested independently.
    - Existing `TestExecuteToolRejectsInvalidArgumentKeys` and
      `TestExecuteToolWithWorkingDirectory` tests still pass (update their call sites
      to pass a `map[string]any` instead of `json.RawMessage`).

- [x] **Task 6: Update tests**
  - **Description:** Add/update tests in `main_test.go`:
    1. **`TestExtractParams`** — table-driven:
       - Happy path: all three types, required + optional, quoted description with spaces.
       - Malformed line (wrong token count) → skipped, warning to stderr.
       - Unknown type → skipped.
       - Bad param name (digit-leading) → skipped.
       - No `Param:` lines → empty slice.
    2. **`TestBuildInputSchema`** — table-driven:
       - Zero params → `{"type":"object","properties":{}}`.
       - One required string param → `required` array present.
       - Mix of required/optional → only required names in array.
    3. **`TestValidateRequiredParams`** — table-driven:
       - All required present → nil error.
       - One missing → error with param name.
       - No required params → nil error.
    4. **`TestDiscoverTools`** — extend existing test: script with `Param:` annotations
       should populate `tool.Params` correctly.
     5. **Integration smoke test** (`TestRequiredParamValidationViaRegistry`) — Concretely:
        - Extract the validation closure into a **named `handlerFunc` variable** inside
          `toolRegistry.replace` (the same function literal that is passed to `AddTool`).
          Store the last-registered handler in a new unexported field
          `lastHandler mcp.ToolHandlerFunc` on `toolRegistry` (set inside `replace` after
          each `AddTool` call; only needed in tests — acceptable for testability).
        - In the test: call `registry.replace([]discoveredTool{...})`, then call
          `registry.lastHandler(ctx, req)` directly with a `*mcp.CallToolRequest` whose
          `Params.Arguments` is an empty JSON object `{}`.
        - Assert `result.IsError == true` and `result.Content[0].(*mcp.TextContent).Text`
          contains `"missing required parameter: path"`.
        - Do **not** spin up a transport or HTTP server for this test.
  - **Review Criteria:**
    - `go test ./...` passes with zero failures.
    - New tests cover all edge cases listed above.
    - No existing tests broken.

---

## Edge Case & Safety Checklist

- **Duplicate param names in the same script:** Last declaration wins. Deduplication
  happens in `buildInputSchema`: the `properties` map naturally keeps the last value for
  a key; a `lastRequired map[string]bool` pass ensures the `required` array contains the
  name at most once, reflecting only the last declaration's `Required` field. No warning
  is emitted — authors may refine annotations during development.
- **Param name collides with Go/CLI reserved words:** No special handling needed; the
  existing `argumentKeyPattern` regex already enforces safe names.
- **Description contains `Param:` substring:** Parser splits on the *first* occurrence of
  `Param:` per line; any occurrence inside the quoted description string is after the split
  point and is part of the description value — no collision.
- **Script with only `Param:` but no `Description:`:** Fully supported; description remains
  empty string as today.
- **Very long quoted descriptions (> 200 chars):** No truncation; passed verbatim to
  JSON Schema. MCP clients display it as-is.
- **Hot-reload race:** `toolRegistry.replace` holds the mutex for the whole swap; schema
  changes from a rewritten script are atomic from the client's perspective.
- **Number type and CLI args:** `number` maps to JSON Schema `"type":"number"` which allows
  integers and floats. `argumentsToCLIArgs` already handles numeric Go values via
  `fmt.Sprint`. MCP clients may pass numbers as JSON numbers; `parseToolArguments`
  unmarshals them to `float64` which `fmt.Sprint` formats correctly.
- **Boolean params and the existing CLI logic:** Already handled by `argumentsToCLIArgs`
  (true → `--flag`, false → omitted). No change needed.
- **Empty scripts directory after reload:** `replace([])` with no tools still calls
  `RemoveTools` on prior names — existing behaviour, unchanged.

---

## Review Log (Plan Review)

- **Round 1:** NOT APPROVED — 4 issues raised (2 blocking). See archived feedback in git
  history. All four issues resolved in plan revision before Round 2 submission.
- **Round 2:** CONDITIONAL APPROVAL (one blocking gap in Task 6 item 5). Resolved in
  plan revision: Task 6 item 5 now specifies storing the handler in `registry.lastHandler`
  and calling it directly in the test — no transport, no server dependency. All four
  Round 1 issues and this Round 2 gap are now closed. **APPROVED.**
- **Round 3:** N/A

## Final Status (Code Review)

- **Round 1:** APPROVED. Tasks 5 and 6 satisfy all review criteria. Implementation is
  clean, test coverage is thorough, and no existing tests are broken.
  Advisory note (non-blocking): `validateRequiredParams` treats `{"path": null}` as
  "present" (key exists), while the plan text says "nil values count as absent". Consider
  adding `|| v == nil` to the check if this edge case matters in practice.
- **Round 2:** N/A
- **Round 3:** N/A
