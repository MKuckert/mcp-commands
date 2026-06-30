# Plan: Code Review Fixes — src/main.go

## Objective

Address the six findings raised in the code review of `src/main.go`. Each task is scoped to a single fix, testable in isolation, and must leave all existing tests green.

## Requirements & Decisions

- **Frameworks:** Go stdlib + `github.com/modelcontextprotocol/go-sdk` v1.6.1 (already in use)
- **Chosen Libraries:** None — all fixes use stdlib only
- **Error Handling Strategy:** Fail loud, never fake. Errors must be surfaced (logged to stderr or returned) and never silently swallowed or substituted with placeholder strings.

## Implementation Steps

> Status Markers: [ ] Open, [/] In Progress, [x] Completed (set after accepted review only!)

- [x] **Task 1: Surface errors in `snapshotScriptsDir` instead of silent substitution**
  - **Description:** When `os.Stat` fails inside `snapshotScriptsDir` (e.g., a file is deleted after `ReadDir` returns — TOCTOU), the code silently appends the literal string `"error"` to the snapshot. This fakes state and violates the error-handling principle. The error must be returned to the caller. `watchTools` already logs `snapshotScriptsDir` errors to stderr and continues on the ticker path; this behaviour is acceptable and must be preserved for the new return path too.
  - **Review Criteria:**
    - `snapshotScriptsDir` returns `("", error)` when any `os.Stat` call inside it fails.
    - The literal string `"error"` is no longer used as a substitute for missing file metadata.
    - The watch-loop path in `watchTools` logs the returned error to stderr and calls `continue` (matching the existing pattern for other `snapshotScriptsDir` errors at line 268).
    - Unit test for the `os.Stat` failure path: use `t.Skip("requires non-root") if os.Getuid() == 0` guard, then create a temp file, remove read permission (`chmod 000` equivalent), and assert that `snapshotScriptsDir` returns a non-nil error. Document the root-guard limitation in the test's comment.

- [ ] **Task 2: Fix hot-reload snapshot to detect content changes**
  - **Description:** `snapshotScriptsDir` currently encodes only `Name` and `Mode` per entry. It must also include `ModTime` (nanosecond precision via `UnixNano()`) and `Size` so that edits to an existing script (e.g., updating the `Description:` comment) trigger a re-discovery. This task is done on the clean error-returning path established in Task 1.
  - **Review Criteria:**
    - `snapshotScriptsDir` includes `info.ModTime().UnixNano()` and `info.Size()` in the snapshot string for each entry.
    - Existing `TestWatchTools` still passes.
    - A new or updated sub-test verifies that overwriting a script's content (name and mode unchanged) produces a different snapshot string, and that `watchTools` subsequently calls `registry.replace`.

- [ ] **Task 3: Remove global `os.Chdir`; store `dirAbs` on `toolRegistry`**
  - **Description:** `run()` calls `os.Chdir(dirAbs)` to set a global working directory, which is thread-unsafe and an anti-pattern. The fix: store `dirAbs` on `toolRegistry` at construction time. Change `newToolRegistry(server *mcp.Server)` to `newToolRegistry(server *mcp.Server, dir string) *toolRegistry`, storing `dir` as a field `dirAbs string`. Inside `replace()`, the closure passed to `mcp.Server.AddTool` captures `r.dirAbs` and sets `cmd.Dir = r.dirAbs` in `executeTool`. `executeTool` gains a `dir string` parameter for this. Remove `os.Chdir` from `run()` and remove `cmd.Dir = "."` from `executeTool`. The `replace()` and `watchTools()` signatures remain unchanged.
  - **Review Criteria:**
    - `os.Chdir` is absent from the codebase.
    - `toolRegistry` has a `dirAbs string` field set in `newToolRegistry`.
    - `executeTool` accepts a `dir string` parameter and sets `cmd.Dir = dir`.
    - The closure in `replace()` passes `r.dirAbs` to `executeTool`.
    - `replace()` and `watchTools()` signatures are unchanged.
    - Existing tests pass; update any `newToolRegistry` call in `main_test.go` to supply an empty string or `t.TempDir()` as the dir argument.
    - A new sub-test confirms that the executed script's working directory matches the `dirAbs` supplied to `newToolRegistry`.

- [ ] **Task 4: Validate argument keys in `argumentsToCLIArgs`**
  - **Description:** JSON keys supplied by the caller are blindly prepended with `--` and forwarded to the script. An empty key produces `--` (POSIX end-of-flags sentinel); keys with spaces, `=`, or leading `-` corrupt the script's argument parser. Change the function signature to `argumentsToCLIArgs(args map[string]any) ([]string, error)`. Add a validation step that rejects any key not matching the regex `^[a-zA-Z][a-zA-Z0-9_-]*$`. Return a descriptive error for any invalid key. Update `executeTool` to handle this error and return an `IsError` `CallToolResult`.
  - **Review Criteria:**
    - `argumentsToCLIArgs` returns `([]string, error)`.
    - Invalid keys (empty string, `"--"`, keys with spaces/`=`, keys starting with `-` or a digit) produce a descriptive error.
    - Valid keys (`my-flag`, `foo_bar`, `v`, `verbose`) pass without error.
    - All callers (currently only `executeTool`) are updated to handle the new return signature.
    - Unit tests cover at least: empty key, digit-leading key, space-containing key, valid single-char key, valid multi-char key.
    - Code comment in `argumentsToCLIArgs` explains why digit-leading keys are rejected.

- [ ] **Task 5: Handle boolean and nil values correctly in `argumentsToCLIArgs`**
  - **Description:** This task modifies `argumentsToCLIArgs` after the signature change in Task 4. Current behaviour: `nil` emits `--key ""`; `bool` values are `fmt.Sprint`ed to `"true"`/`"false"`. Correct behaviour per POSIX/GNU conventions: `true` → emit `--key` with no value argument; `false` → omit the flag entirely; `nil` → omit the flag entirely. Implement via type-switch (`switch v := value.(type) { case bool: ... }`) — not via string comparison — so that string arguments whose value happens to be `"true"` are unaffected.
  - **Review Criteria:**
    - Boolean detection uses a type-switch on `bool`, not string comparison.
    - `true` value → `["--key"]` (flag only, no value appended).
    - `false` value → flag omitted from output.
    - `nil` value → flag omitted from output.
    - String `"true"` / `"false"` values → still treated as plain strings (`--key true`).
    - Existing argument-encoding unit tests are updated/extended to cover all four cases.
    - No regression for string, numeric, and slice value types.
    - Behaviour change is noted in a code comment: `nil` and `false` booleans are now silently dropped; scripts relying on `--key ""` for nil must be updated.

- [ ] **Task 6: Enforce strict JSON-object input in `parseToolArguments`**
  - **Description:** `parseToolArguments` contains a double-decode fallback: if the raw message is not a plain JSON object, it attempts to decode it as a JSON-encoded string containing JSON. This masks malformed inputs from callers. Remove the fallback. The function must succeed only when the raw message is a proper JSON object (or empty/null).
  - **Review Criteria:**
    - The fallback `json.Unmarshal`-of-a-string block is removed.
    - Passing a double-encoded JSON string as arguments returns an error matching `"arguments must be a JSON object"`.
    - All existing callers and tests remain green.
    - A unit test explicitly documents and asserts the rejection of double-encoded input.

## Edge Case & Safety Checklist

- Empty scripts directory: `discoverTools` returns `[]`, `run` logs a warning — no change required.
- Script deleted between snapshot and `discoverTools`: `EvalSymlinks` or `Stat` in `discoverTools` returns an error; the entry is skipped (existing behaviour, correct).
- `snapshotScriptsDir` stat race (file deleted after `ReadDir`): Task 1 ensures this now returns an error instead of faking state.
- Concurrent `replace()` calls: Already thread-safe via `toolRegistry.mu`; confirmed by Explorer. No change needed.
- `os.Chdir` removal (Task 3): Scripts that relied on implicit CWD must now receive it explicitly via `cmd.Dir = r.dirAbs`. Verify no other code path assumed a specific CWD after startup.
- Argument key regex (Task 4): Keys that are purely numeric (e.g., `"1"`) are rejected by `^[a-zA-Z]...`. This is intentional — numeric leading chars are uncommon for flags and could cause confusion. Document in code comment.
- Boolean `false` / nil omission (Task 5): Scripts expecting an explicit `--flag ""` for nil or `--flag false` for boolean false will no longer receive it. This is a behaviour change; document via a code comment in `argumentsToCLIArgs`.
- Stat-failure test portability: Task 1 prescribes a `t.Skip` root guard; CI environments running as root will skip the chmod-based sub-test.
- Timeout handling: unchanged — `context.DeadlineExceeded` path remains correct.

## Review Log (Plan Review)

- **Round 1:** NOT APPROVED — 3 blockers, 2 minors. See detail in prior revision. All items addressed in Round 2 revision.
- **Round 2:** APPROVED. All Round 1 blockers and minors resolved. Plan is internally consistent, execution order is explicit, review criteria are concrete and testable. No new issues introduced by the reshuffle. Builder may proceed in prescribed order (1→2→3→4→5→6).
- **Round 3:** [Feedback or "N/A"]

## Final Status (Code Review)

- **Round 1:** [Feedback or "N/A"]
- **Round 2:** [Feedback or "N/A"]
- **Round 3:** [Feedback or "N/A"]
