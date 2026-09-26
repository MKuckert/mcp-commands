# Plan: Configurable & Per-Tool Timeout Handling

## Objective

Make tool execution timeouts configurable at three levels, in precedence order
(per-tool > CLI > default):

1. **Default:** 5 minutes (current behavior, unchanged).
2. **Global:** new `--timeout <duration>` CLI flag (formatted duration string)
   and `--no-timeout` boolean to disable the global timeout.
3. **Per-tool:** new `Timeout: <duration>` line in a script's frontmatter
   (first `scanHeaderLines` = 30 lines), using the same format string as the
   flag, with the literal `NONE` meaning no timeout.

A script whose frontmatter declares `Timeout:` always wins over the global;
a script without one inherits the global. `Timeout: 0s` and `Timeout: NONE`
are equivalent (zero duration = no deadline).

## Requirements & Decisions

- **Frameworks:** None new — single-file Go app (`main.go`), existing deps only
  (`modelcontextprotocol/go-sdk`, `fsnotify`). No new libraries; duration
  parsing is a small stdlib-only helper (stdlib `time.ParseDuration` is rejected:
  it accepts negatives, decimals, and space-less compounds only — see parser spec).
- **Chosen Libraries:** n/a (stdlib only).
- **Error Handling Strategy:**
  - `--timeout` is parsed **at startup, before the server starts** (same fail-fast
    pattern as `resolveCORS`). Invalid value → stderr error, exit 1. The server
    never boots with a broken config.
  - An invalid per-tool `Timeout:` value **does not** fail discovery or
    registration. It logs `Warning: ignoring invalid Timeout in <file>: <reason>`
    to stderr and falls back to the global timeout (visible degradation, same
    behavior as invalid `Param:` annotations today). This keeps hot-reload robust:
    a user editing a script's timeout mid-flight never breaks the tool.
  - Timeout expiry at execution: unchanged — kill via `exec.CommandContext`,
    return `IsError: true` result `"tool timed out after <d>\n<partial output>"`.

**Format spec (shared by flag and frontmatter):**

- Whitespace-separated list of tokens, each exactly `^<digits><unit>$` with unit
  one of `ns`, `us`, `µs`, `ms`, `s`, `m`, `h`. At least one token required.
  Digits-only integers (no decimals, no `+`/`-` signs, no bare unit).
  Examples: `5m`, `60s`, `1h 30m 5s`. Whitespace between tokens is optional.
- `NONE` (case-insensitive, leading/trailing whitespace trimmed) → no timeout.
- Result of `0` (e.g. `0s`) → no timeout, identical to `NONE`.

**Precedence decisions (recorded):**

- `--no-timeout` **beats** `--timeout` when both are passed (explicit boolean
  flag precedence — the pattern established in the CORS feature review).
- Per-tool `Timeout:` **always** overrides the global, even if the global is
  `--no-timeout`: an author who pinned `Timeout: 30s` gets 30s; authors get
  predictable per-script behavior.
- `Timeout:` uses **first occurrence wins** (same rule as `Description:`);
  multiple lines are allowed, extras are silently ignored.
- No env-var override for the global (YAGNI; flag + frontmatter cover the need).
- When no timeout applies, the exec context is the request context *itself*
  (client cancellation/abort still kills the script — "no timeout" means
  "no deadline", never "uninterruptible").

## Implementation Steps

> Status Markers: [ ] Open, [/] In Progress, [x] Completed (set after accepted review only!)

- [/] **Task 1: Duration parser + flag plumbing**
  - **Description:** Add `parseTimeoutDuration(raw string) (time.Duration, error)`
    in `main.go` implementing the format spec above (returns `0` for `NONE`;
    rejects negatives/decimals/invalid tokens with a descriptive error). Add CLI
    flags `--timeout` (string, default `""` → `5 * time.Minute`) and `--no-timeout`
    (bool). Resolve in `main()` before `run()` (fail-fast on parse error, like
    CORS): `--no-timeout` ⇒ 0; `--timeout` given ⇒ parsed; else 5m. Thread the
    resolved `time.Duration` through `run(...)` into `newToolRegistry(server, dir, globalTimeout)`
    (new field on `toolRegistry`).
  - **Review Criteria:** Table tests cover: `5m`, `60s`, `1h 30m 5s` (sums
    correctly), `NONE`/`none`/` NONE `, `0s` → 0; rejects `-5m`, `+5m`, `1.5h`,
    `m5`, `h`, `""`, `5m 5m` is accepted (10m). `--no-timeout` + `--timeout 5s`
    ⇒ 0. Startup with `--timeout bogus` exits 1 with a clear message. `go vet`
    clean, no new deps in `go.mod`; the three existing `newToolRegistry(server, dir)`
    call sites in main_test.go are updated to the new signature in the same commit
    (reviewer advisory, round 1).
- [/] **Task 2: Per-tool `Timeout:` frontmatter**
  - **Description:** Add `scanTimeoutPrefix = "Timeout:"` const and
    `extractTimeout(filePath string) (time.Duration, bool)` mirroring
    `extractDescription` (first match within `scanHeaderLines`; returns
    `(0, false)` when absent, `(0, true)` for `NONE`/`0`, `(d, true)` when
    valid). Invalid value → stderr warning, returns `(0, false)` so the global
    applies. Extend `discoveredTool` with `Timeout time.Duration` and
    `TimeoutSet bool`; populate both in `discoverTools`.
  - **Review Criteria:** Tests: present/absent, `NONE` vs `0s` vs `30s`, value
    beyond line 30 ignored, invalid value logs warning + `TimeoutSet == false`,
    first-of-multiple wins. Hot reload of a script whose `Timeout:` changed
    re-registers with the new value (covered by existing watch test pattern).
- [/] **Task 3: Apply resolved timeout at execution**
  - **Description:** In `toolRegistry.replace`, handler resolves
    `t := r.globalTimeout; if tool.TimeoutSet { t = tool.Timeout }` and passes
    `t` to `executeTool` (replacing the `defaultToolTimeout` argument). In
    `executeTool`, only call `context.WithTimeout(ctx, timeout)` when
    `timeout > 0`; otherwise use `ctx` directly (cancellation preserved). Append
    a short suffix to the registered tool description, e.g.
    `(timeout: 30s)` / `(timeout: none)`, so the LLM knows the budget.
  - **Review Criteria:** Registry-level test (pattern:
    `TestRequiredParamValidationViaRegistry`): tool with `Timeout: 1s` running a
    `sleep 5` script returns `IsError` + "timed out after 1s" within ~1–2s;
    same script via a tool *without* `Timeout:` under `--timeout`-style global
    of e.g. 2s gets the global; `Timeout: NONE` under a short global still
    completes (short script). Zero/`NONE` path uses request ctx: cancelling the
    caller ctx kills the script. Description suffix present and correct for
    all three cases (set, none, inherited).
- [/] **Task 4: Docs, usage text, README**
  - **Description:** Update the `Usage:` line in `main.go` with `[--timeout <duration>] | [--no-timeout]`.
    README: extend the Safety First bullet, the Usage section with both flags,
    and add a frontmatter subsection documenting `Timeout: <duration>` / `NONE`
    with the format spec, precedence rules, and a worked example script header.
  - **Review Criteria:** README examples copy-pasteable; every documented
    behavior matches implemented behavior (precedence, `NONE` case-insensitivity,
    first-match rule, warning-on-invalid); usage text and `flag` help strings
    consistent with README.

- [/] **Task 5: Commit & branch hygiene (cross-cutting)**
  - **Description:** Work lands on branch `feat/timeout-handling` off `main`.
    One conventional commit per task in order: `feat: parse timeout duration
    strings` (includes PLAN.md), `feat: --timeout and --no-timeout flags`,
    `feat: per-tool Timeout: frontmatter`, `feat: apply resolved tool timeouts`,
    `docs: timeout configuration`. `go build ./...`, `go vet ./...`,
    `go test ./...` green at every commit; final manual smoke: stdio run with
    a `sleep 10` script under `Timeout: 1s` and under `--no-timeout`.
  - **Review Criteria:** `git log main..feat/timeout-handling` shows exactly the
    five commits, all building/testing green; PR ready to open.
    Reviewer advisory (round 1): the `newToolRegistry` signature change breaks
    three existing call sites in main_test.go, and the exact-equality assertion
    `res.Tools[0].Description == "beta updated"` in `TestWatchToolsDetectsContentChanges`
    (line ~352) must be updated for the suffix — update them in the same commit
    as Task 3 so every commit builds green.

## Edge Case & Safety Checklist

- `--no-timeout` and `--timeout` both passed → `--no-timeout` wins (documented).
- `--timeout 0s` → valid, means no timeout (documented; consistent with `NONE`).
- Negative/decimal/malformed `--timeout` → exit 1 at startup, server never starts.
- Invalid per-tool `Timeout:` → visible stderr warning + global fallback; never blocks discovery/hot-reload.
- `Timeout:` on line > 30 → ignored, tool uses global.
- `Timeout: NONE` + `--no-timeout` → no deadline; client abort still kills the script (request ctx).
- Zero timeout ⇒ `context.WithTimeout(ctx, 0)` is NOT used — code path picks raw `ctx` (equivalent, but explicit).
- Timeout expiry mid-output → partial stdout/stderr returned with the timeout message (existing behavior, unchanged).
- Hot reload edits only `Timeout:` → tool re-registered with new value, no restart.
- Symlinked scripts: `Timeout:` read from resolved target (same as `Description:` via `resolvedPath`).
- Output cap (1 MB) unchanged and independent of timeout.

## Review Log (Plan Review)

- **Round 1:** Verified every claim against the current code: `defaultToolTimeout = 5 * time.Minute` (~line 30), `replace` hardcoding `defaultToolTimeout` (~line 470), `executeTool`'s unconditional `context.WithTimeout(ctx, timeout)` (~line 594), the `resolveCORS` fail-fast pattern in `main()`, the `extractDescription`/`extractParams` mirror targets, and the registry/watch test patterns (`TestRequiredParamValidationViaRegistry`, `TestWatchToolsDetectsContentChanges`). Precedence rules are self-consistent (per-tool > global; `--no-timeout` > `--timeout`; first-match-wins matches `Description:` semantics), error strategy follows fail-loud-fail-fast, format spec is stdlib-achievable, edge checklist covers expiry-mid-output, hot-reload, symlinks, and cancellation-with-no-deadline. Advisory notes (non-blocking): (1) three existing `newToolRegistry(server, dir)` call sites in main_test.go plus the exact-equality assertion `res.Tools[0].Description == "beta updated"` in `TestWatchToolsDetectsContentChanges` (line 352) will need updates for the new constructor param and the registered-description suffix — Task 5's all-green-at-every-commit gate catches this; (2) define the suffix for empty frontmatter descriptions explicitly (e.g. description becomes `(timeout: 30s)` alone). Status: Approved

## Final Status (Code Review)

- **Round 1:** [N/A]
