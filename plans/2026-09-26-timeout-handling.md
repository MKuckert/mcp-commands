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
  one of `s`, `m`, `h` (sub-second units are not meaningful for tool timeouts
  and are rejected). At least one token required. Digits-only integers (no
  decimals, no `+`/`-` signs, no bare unit). Examples: `5m`, `60s`,
  `1h 30m 5s`. Whitespace between tokens is optional.
- `NONE` (case-insensitive, leading/trailing whitespace trimmed) → no timeout.
- Result of `0` (e.g. `0s`) → no timeout, identical to `NONE`.
- Overflow guard: each term is checked for multiplication overflow
  (`n > maxDuration/multiplier`) and the running sum for addition overflow
  (`total > maxDuration-term`); a violating value is rejected with a clear
  `overflows` error before any wraparound arithmetic can produce a positive
  duration.

**Precedence decisions (recorded):**

- `--no-timeout` and `--timeout` are **mutually exclusive** (Round 2; the
  earlier "no-timeout beats --timeout" decision is retired): passing both is a
  startup error, mirroring the CORS `--allowed-origins` + `--allow-all-origins`
  contradiction pattern. The `--timeout` flag visit is tracked via `flag.Visit`
  so an explicitly empty `--timeout=` is distinguishable from the `""` default
  and fails fast on parse (empty → error); the default 5m applies only when the
  flag was omitted entirely.
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

- [x] **Task 1: Duration parser + flag plumbing**
  - **Description:** Add `parseTimeoutDuration(raw string) (time.Duration, error)`
    in `main.go` implementing the format spec above (returns `0` for `NONE`;
    rejects negatives/decimals/invalid tokens with a descriptive error). Add CLI
    flags `--timeout` (string, default `""` → `5 * time.Minute`) and `--no-timeout`
    (bool). Resolve in `main()` before `run()` (fail-fast on parse error, like
    CORS): `--timeout` + `--no-timeout` ⇒ mutual-exclusion error; `--no-timeout`
    ⇒ 0; `--timeout` visited ⇒ parsed value (explicit empty ⇒ parse error);
    else 5m. The `--timeout` visit is tracked with `flag.Visit` (pattern shared
    with `--allow-all-origins`). The parser enforces the overflow guard above.
    Thread the resolved `time.Duration` through `run(...)` into
    `newToolRegistry(server, dir, globalTimeout)`
    (new field on `toolRegistry`).
  - **Review Criteria:** Table tests cover: `5m`, `60s`, `1h 30m 5s` (sums
    correctly), `NONE`/`none`/` NONE `, `0s` → 0; accepts `5m 5m` (10m); rejects
    sub-second `250ms`/`250us`/`250µs`/`1000ns`, `-5m`, `+5m`, `1.5h`, `m5`,
    `h`, `""`, unknown units. Overflow: three ~max terms whose sum exceeds
    `math.MaxInt64` (`876000h × 3`) and a single term whose seconds→nanoseconds
    multiply overflows (`9223372037s`) are rejected with an `overflows` error.
    `resolveTimeout`: `--no-timeout` + `--timeout` ⇒ mutual-exclusion error;
    explicit empty `--timeout=` ⇒ parse error; `--no-timeout` alone ⇒ 0;
    omitted ⇒ 5m. Startup with `--timeout bogus` exits 1 with a clear message.
    `go vet` clean, no new deps in `go.mod`; the three existing
    `newToolRegistry(server, dir)` call sites in main_test.go are updated to the
    new signature in the same commit (reviewer advisory, round 1).
- [x] **Task 2: Per-tool `Timeout:` frontmatter**
  - **Description:** Add `scanTimeoutPrefix = "Timeout:"` const and timeout
    extraction (first match within `scanHeaderLines`; `nil` when absent,
    `&0` for `NONE`/`0`, `&d` when valid; invalid value → stderr warning,
    `nil` so the global applies). Round 2: merged with `extractDescription`
    and `extractParams` into a single-pass `extractFrontmatter(path)
    (desc, params, timeout *time.Duration)` — one `os.Open` + scanner, first
    `Description:`, all valid `Param:` lines, first `Timeout:`. `discoveredTool`
    carries one `Timeout *time.Duration` (nil = undeclared, `&0` = NONE);
    populated in `discoverTools`.
  - **Review Criteria:** Tests assert via `extractFrontmatter`: present/absent
    (pointer nil vs non-nil), `NONE` vs `0s` vs `30s`, value beyond line 30
    ignored, first-of-multiple wins. The invalid-value case captures stderr via
    a pipe (restoring `os.Stderr`) and asserts the warning names the script file
    AND states the invalid value's reason. Hot reload of a script whose `Timeout:`
    changed re-registers with the new value (covered by existing watch test
    pattern).
- [x] **Task 3: Apply resolved timeout at execution**
  - **Description:** In `toolRegistry.replace`, handler resolves
    `t := r.globalTimeout; if tool.Timeout != nil { t = *tool.Timeout }` and
    passes `t` to `executeTool` (replacing the `defaultToolTimeout` argument).
    In
    `executeTool`, only call `context.WithTimeout(ctx, timeout)` when
    `timeout > 0`; otherwise use `ctx` directly (cancellation preserved). Append
    a short suffix to the registered tool description, e.g.
    `(timeout: 30s)` / `(timeout: none)`, so the LLM knows the budget.
  - **Review Criteria:** Registry-level test (pattern:
    `TestRequiredParamValidationViaRegistry`): tool with `Timeout: &1s` running a
    `sleep 5` script returns `IsError` + "timed out after 1s" within ~1–2s;
    same script via a tool *without* `Timeout:` under `--timeout`-style global
    of e.g. 2s gets the global; `Timeout: NONE` under a short global still
    completes (short script). Zero/`NONE` path uses request ctx: cancelling the
    caller ctx kills the script. Description suffix present and correct for
    all three cases (set, none, inherited).
- [x] **Task 4: Docs, usage text, README**
  - **Description:** Update the `Usage:` line in `main.go` with `[--timeout <duration>] | [--no-timeout]`.
    README: extend the Safety First bullet, the Usage section with both flags,
    and add a frontmatter subsection documenting `Timeout: <duration>` / `NONE`
    with the format spec, precedence rules, and a worked example script header.
  - **Review Criteria:** README examples copy-pasteable; every documented
    behavior matches implemented behavior (precedence, `NONE` case-insensitivity,
    first-match rule, warning-on-invalid); usage text and `flag` help strings
    consistent with README.

- [x] **Task 5: Commit & branch hygiene (cross-cutting)**
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

- `--no-timeout` and `--timeout` both passed → startup error, mutually exclusive (documented).
- Explicitly empty `--timeout=` → startup error (parse of empty fails; `flag.Visit` distinguishes it from omitted).
- `--timeout 0s` → valid, means no timeout (documented; consistent with `NONE`).
- Sub-second units (`ms`/`us`/`ns`) → rejected (not meaningful for tool timeouts).
- Duration sum/multiply overflow → rejected with an `overflows` error before wraparound can yield a positive duration.
- Negative/decimal/malformed `--timeout` → exit 1 at startup, server never starts.
- Invalid per-tool `Timeout:` → visible stderr warning + global fallback; never blocks discovery/hot-reload.
- `Timeout:` on line > 30 → ignored, tool uses global.
- No deadline (`Timeout: NONE`/`0s`, or `--no-timeout`/`--timeout 0s`) → no timeout; client abort still kills the script (request ctx).
- Zero timeout ⇒ `context.WithTimeout(ctx, 0)` is NOT used — code path picks raw `ctx` (equivalent, but explicit).
- Timeout expiry mid-output → partial stdout/stderr returned with the timeout message (existing behavior, unchanged).
- Hot reload edits only `Timeout:` → tool re-registered with new value, no restart.
- Symlinked scripts: `Timeout:` read from resolved target (same as `Description:` via `resolvedPath`).
- Output cap (1 MB) unchanged and independent of timeout.

## Review Log (Plan Review)

- **Round 1:** Verified every claim against the current code: `defaultToolTimeout = 5 * time.Minute` (~line 30), `replace` hardcoding `defaultToolTimeout` (~line 470), `executeTool`'s unconditional `context.WithTimeout(ctx, timeout)` (~line 594), the `resolveCORS` fail-fast pattern in `main()`, the `extractDescription`/`extractParams` mirror targets, and the registry/watch test patterns (`TestRequiredParamValidationViaRegistry`, `TestWatchToolsDetectsContentChanges`). Precedence rules are self-consistent (per-tool > global; `--no-timeout` > `--timeout`; first-match-wins matches `Description:` semantics), error strategy follows fail-loud-fail-fast, format spec is stdlib-achievable, edge checklist covers expiry-mid-output, hot-reload, symlinks, and cancellation-with-no-deadline. Advisory notes (non-blocking): (1) three existing `newToolRegistry(server, dir)` call sites in main_test.go plus the exact-equality assertion `res.Tools[0].Description == "beta updated"` in `TestWatchToolsDetectsContentChanges` (line 352) will need updates for the new constructor param and the registered-description suffix — Task 5's all-green-at-every-commit gate catches this; (2) define the suffix for empty frontmatter descriptions explicitly (e.g. description becomes `(timeout: 30s)` alone). Status: Approved

## Final Status (Code Review)

- **Round 1:** Verified against the diff and by execution: all 5 commits (65ff0d2…66d1493) build/vet/test green individually; parser, precedence (`--no-timeout` > `--timeout`; per-tool > global), first-match-wins, >30-line ignore, warning-on-invalid, description suffixes (incl. empty description → suffix alone, advisory #2), zero≡NONE raw-ctx path, call-site updates (advisory #1), README/usage consistency, and the full edge checklist all confirmed — tests include registry-level expiry, inheritance, NONE-under-short-global, and cancellation-kill. Only deviation: two plan-phase docs commits predate the 5 implementation commits (7 total on branch), which is acceptable and not a defect. Status: Approved
- **Round 2: PR feedback (Copilot + principal 2026-09-26)** — all 8 items addressed in 2347f08…8e44d82 (build/vet/test green at every commit): (1) units restricted to `s`/`m`/`h` in the shared parser, doc comments, error messages, README, and tests (250ms/us/µs/1000ns now rejected) — 2347f08; (2) overflow guard: multiplication (`n > maxDuration/multiplier`) and addition (`total > maxDuration-term`) checked before each term, `overflows` error; tests cover 3×876000h sum and single-term multiply overflow — 2347f08; (3) `--no-timeout` + `--timeout` is now a startup error (mutually exclusive, not "no-timeout wins") and an explicitly empty `--timeout=` fails fast via `flag.Visit`-tracked `timeoutSet` in `resolveTimeout`; the retired "no-timeout beats --timeout" decision is rewritten in Requirements, the edge checklist, and README — 5af1535; (4) `discoveredTool.TimeoutSet`/`Timeout` replaced by a single `Timeout *time.Duration` (nil = undeclared, &0 = NONE) in the `replace` handler, description suffix, and tests — 85428d3; (5) the no-op `cancel = func(){}` path removed from `executeTool` (`execCtx := ctx`; cancel created and deferred only in the `timeout > 0` branch) — 85428d3; (6) `extractDescription`/`extractParams`/`extractTimeout` merged into a single-pass `extractFrontmatter` (one `os.Open` + scanner) with all tests asserting via the merged function — 8e44d82; (7) the invalid-timeout test captures stderr via a pipe (restoring `os.Stderr`) and asserts the warning contains both the script filename and the invalid-value reason — 8e44d82; (8) README frontmatter section now documents `Param:` (syntax + example in the worked script), and README/PLAN reflect s/m/h-only units, flag mutual exclusion, and explicit-empty failing — bbac358. Verified against `git diff c3c0a13..HEAD` and by execution (build/vet/gofmt/test green at tip): (1) `timeoutUnits` is s/m/h-only in the shared parser, error text, and docs — 250ms/us/µs/1000ns rejected in tests; (2) overflow guards precede each multiply (`n > maxDuration/multiplier`) and add (`total > maxDuration-term`), wrap-to-positive impossible, sum- and single-term-overflow tests assert `overflows`; (3) `resolveTimeout` errors on `--timeout`+`--no-timeout`, explicit-empty `--timeout=` fails via `flag.Visit`-tracked `timeoutSet`, retired "no-timeout beats" decision consistently rewritten in Requirements, checklist, README, and flag help; (4) `Timeout *time.Duration` everywhere (nil/`&0`), no `TimeoutSet` residue; (5) `executeTool` has no no-op cancel — cancel created+deferred only in the `timeout > 0` branch, `execCtx := ctx` otherwise; (6) single-pass `extractFrontmatter` (one open+scan, first Description/Timeout, all valid Params, 30-line window preserved), old extractors deleted, all tests (incl. `TestExtractParams`) route through it; (7) invalid-timeout test captures stderr and asserts filename AND reason; (8) README documents `Param:`, s/m/h, mutual exclusion, and explicit-empty. Merged scanner semantics behavior-identical to the old three passes (the pre-existing scanner.Err discard in extractDescription was the only nuance, unobservable for text files). PLAN text matches the code. All task boxes already ticked in round 1 and remain satisfied. Status: Approved
