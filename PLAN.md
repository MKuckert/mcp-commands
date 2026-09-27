# Plan: Tool Diagnostics (`--list-tools` / `--call-tool`)

## Objective

Give users a way to inspect and exercise the tools **without an MCP client**:

1. **`--list-tools`** — one-shot: discover the scripts directory, print every
   discovered tool as `name(<signature>)` plus its full registered description
   (word-wrapped), then exit. The server is **not** started (no port, no stdio
   transport, no watcher). This shows exactly what the LLM sees — including the
   `(timeout: …)` suffix.
2. **`--call-tool=<name>`** with optional **`--params='<json object>'`** —
   one-shot: run a single discovered tool through the *same* execution path as
   the MCP handler (required-param validation, JSON→CLI-arg translation,
   timeout resolution), print the result text to stdout, and map the outcome to
   a process exit code. Debugging a misbehaving tool no longer needs a client.

Example output (excerpt — the source toolset has 9 executables; this is the
spec-correct rendering of the first two):

```
meridian_build([profile:str], [scope:str], [target:str], [timings:bool])
     Compile the Meridian workspace (or just meridian-app) on the remote host — the offload target; the long pole is the Bevy dep tree, built once then cached.
     Timeout is NONE (a full clean build of the Bevy dep tree can far exceed the 5-minute MCP default; re-call to see a slow build's result) — prefer scope=app
     for incremental work. (timeout: none)

meridian_fetch([commit:str])
     Check out the pinned commit of the Meridian repo on the remote host (idempotent) and report HEAD/branch, rustc, cargo and Cargo.lock info. Run first on a
     remote host before any other meridian tool. (timeout: 2m0s)
```

## Requirements & Decisions

- **Frameworks:** None new — single-file Go app (`main.go`), existing deps only
  (`modelcontextprotocol/go-sdk`, `fsnotify`); word wrapping is a small
  stdlib-only helper (rune-based). No new libraries.
- **Chosen Libraries:** n/a (stdlib only).

**`--list-tools` output format (spec):**

- One block per tool, in discovery order (already sorted by filename),
  separated by exactly one blank line.
- Line 1: `name(<decls>)` — one `<decl>` per parameter in declaration order:
  `key:shorttype` if required, `[key:shorttype]` if optional, joined by `, `.
  Short types: `string`→`str`, `number`→`num`, `boolean`→`bool`.
  A tool with no parameters renders as `name()`.
- Duplicate parameter names: dedup **last-wins for type/required**, rendered
  at the **first-occurrence position** (mirrors `buildInputSchema`, whose
  `required` array keeps first-occurrence order) — so the signature always
  matches the registered schema.
- Lines 2…n: the **registered description** (frontmatter `Description:` plus
  the `(timeout: <d>)` / `(timeout: none)` suffix) word-wrapped. The wrap
  contract, deterministic: width **160 runes total INCLUDING the 5-space
  indent** (so the content budget is 155); rune-based; greedy (fill each line
  as far as it fits); breaks at runs of whitespace, which collapse to a single
  space; never splits a word. A description that fits stays one line; a single
  unbreakable token longer than the budget is emitted whole on its own line
  (visible, never truncated).
- The per-tool timeout for the suffix resolves with the **same precedence as
  the registry** (per-tool `Timeout:` > global `--timeout`/`--no-timeout` >
  default): a shared helper `resolveToolTimeout(tool discoveredTool, global
  time.Duration) time.Duration` is used by *both* `replace` and
  `renderToolList` so the two call sites cannot drift.
- Printed to **stdout**; discovery warnings (invalid `Param:`/`Timeout:`)
  continue going to **stderr** exactly as today.
- The description+suffix assembly is extracted from `toolRegistry.replace`
  into a shared helper `registeredDescription(desc string, timeout
  time.Duration) string` (DRY): the registry and the list renderer must never
  drift apart.
- When server-mode flags (`--watch`, `--port`, `--api-key`, CORS) are
  explicitly passed in diagnostic mode they are ignored, and the process prints
  a single stderr notice naming the ignored flags (disclosed, per fail-loud —
  never silent).

**`--call-tool` semantics:**

- `--params` is a string flag, default `{}`; an explicitly empty `--params=` is
  accepted as `{}` (lenient). Anything non-empty must be a JSON object —
  parsed by the existing `parseToolArguments` (rejects non-objects,
  double-encoded JSON). Parse failure → stderr message, **exit 1, the script
  is never started**.
- The tool name is looked up exactly among discovered tools. Unknown name →
  stderr `Error: unknown tool "x"` listing the available names, **exit 1**.
- Execution reuses the MCP handler body verbatim: `validateRequiredParams` →
  `argumentsToCLIArgs` → `executeTool(ctx, path, args, resolvedTimeout,
  dirAbs)`. Timeout resolution is identical to the registry: per-tool
  `Timeout:` > global `--timeout`/`--no-timeout` > 5m default — so the debug
  call honors the exact production budget, including `Timeout: NONE` ⇒ **no
  deadline** (a long build can be re-called to completion; intentional and
  documented).
- Result reporting: every `TextContent` is written to **stdout** (newline-
  separated). `IsError: true` (validation failure, non-zero script exit,
  timeout) → content still printed to stdout, **exit 1**. Hard execution
  errors (script unstartable) → stderr, **exit 1**. Success → **exit 0**.
- Inherited quirk (document, do not change): `validateRequiredParams` iterates
  *all* declarations, so a duplicate name declared `required` then `optional`
  is still enforced by `--call-tool` although the schema marks it optional
  (last-wins) — identical to the production handler.

**Mode dispatch & flag rules:**

- `--list-tools` and `--call-tool` are **mutually exclusive** — passing both is
  a startup error (fail loud, mirrors the `--timeout`/`--no-timeout` pattern).
- In diagnostic mode the process exits after the diagnostic runs. `--watch`,
  `--port`, `--api-key`, and the CORS flags are server-mode options and are
  **ignored** in diagnostic mode (with the stderr notice above, and documented
  in the usage text and README); they are still fail-fast-validated so a typo
  never masquerades as a silent success. `--dir` and `--scripts` remain
  required in all modes (`--dir` is only consumed by `--call-tool`). The
  diagnostic branch resolves paths itself — the `filepath.Abs` + `os.Stat`
  pair is extracted from `run()` into a shared helper so the resolution
  behavior and error text stay identical in both modes.
- Because `--call-tool` must resolve the tool timeout, `resolveTimeout` runs
  (fail-fast, as today) before the diagnostic branch; `resolveCORS` likewise.
  The MCP server, registry, and watcher are constructed only in server mode.
- `serverVersion` bumps `0.6.0` → **`0.7.0`** (new user-facing capability).

**Error handling strategy:** fail-loud-fail-fast, per the global directive.
Invalid flags/values exit 1 at startup; a bad `--params` payload exits 1 before
spawning anything; a failing tool run exits 1 with the full tool output on
stdout (so the failure is inspectable, not hidden). No fallbacks, no silent
degradation: every non-success path prints a human-readable reason to stderr
and exits non-zero.

## Implementation Steps

> Status Markers: [ ] Open, [/] In Progress, [x] Completed (set after accepted review only!)

- [ ] **Task 1: Shared description helper + list rendering**
  - **Description:** Extract `registeredDescription(desc string, timeout
    time.Duration) string` (frontmatter desc + ` ` + `timeoutSuffix`; empty
    desc → suffix alone) from `toolRegistry.replace`, and
    `resolveToolTimeout(tool discoveredTool, global time.Duration)
    time.Duration` (per-tool wins over global); update `replace` to call both
    (registered descriptions byte-identical to today). Add `listParamDecl(p
    paramSpec) string` (`str`/`num`/`bool`, brackets for optional) with
    dedup: last-wins for type/required, **first-occurrence position** in the
    joined signature. Add `renderToolList(tools []discoveredTool, globalTimeout
    time.Duration) string`: per tool, the signature line, the word-wrapped
    description (160-rune total width incl. 5-space indent, `listWrapWidth`),
    and a trailing blank line between blocks. Add `wordWrap(s string, indent
    string, width int) []string` per the contract above (rune-based, greedy,
    whitespace runs collapse to one break, no word splitting, overlong tokens
    pass through whole).
  - **Review Criteria:** Table tests: `renderToolList` on a fixture covering
    no-params (`name()`), required-only, optional-only, mixed (`(a:str,
    [b:num], [c:bool])`), duplicate param names (last wins); `wordWrap` on
    short text (single line, no indent change), text forcing 3+ lines at the
    160-col width, a >160-rune single token (one line, unsplit), and
    multi-byte text (em-dashes — must never tear a rune mid-line).
    `registeredDescription` covers empty desc (suffix alone) and both timeout
    values; `resolveToolTimeout` covers per-tool-wins/global-fallback/nil.
    Existing watch/registry tests that assert exact description strings
    (e.g. `"beta updated (timeout: …)"`) stay green unchanged.
- [ ] **Task 2: `--list-tools` flag & dispatch**
  - **Description:** Add `--list-tools` bool flag. In `main()`, after the
    existing required-flag check and the fail-fast `resolveCORS` /
    `resolveTimeout`, branch: if `--list-tools`, discover (`discoverTools` on
    `scriptsAbs`), print `renderToolList(...)` to stdout, `os.Exit(0)` (the
    discovery-failure error → stderr, exit 1). `run(...)` is untouched by
    this mode. Update the `Usage:` line to show `[--list-tools] |
    [--call-tool <name> --params <json>] | <server mode>`.
  - **Review Criteria:** `go vet` clean. Tests exercise the dispatch via a
    seam (extract the branch into e.g. `diagnose(...)` returning an exit
    code, or capture the writer passed to `renderToolList`): zero tools →
    empty stdout, exit 0, existing stderr warning; discovery error (unreadable
    dir) → stderr message, exit 1; server-mode flags (`--port`, `--watch`,
    `--api-key`, CORS) present alongside `--list-tools` → ignored, no
    `ListenAndServe`, process exits after printing.
- [ ] **Task 3: `--call-tool` + `--params` debug invocation**
  - **Description:** Add `--call-tool` (string) and `--params` (string,
    default `{}`) flags and the mutual-exclusion check against `--list-tools`
    (both set → startup error). Implement `runCallTool(scriptsAbs, dirAbs
    string, globalTimeout time.Duration, name, paramsRaw string) (exitCode
    int, err error)`, taking already-resolved absolute paths (the branch uses
    the shared Abs+Stat helper): discover; look up exact name (unknown → stderr listing
    available names, code 1); `parseToolArguments([]byte(paramsRaw))` (failure
    → stderr, code 1, nothing executed); same timeout resolution as
    `toolRegistry.replace`; `validateRequiredParams` (failure → content to
    stdout, code 1); `executeTool(...)`; print all `TextContent` to stdout;
    `IsError` → code 1, else 0; hard `cmd.Start`-style error → stderr, code 1.
    Wire into `main()` (branch after timeout/CORS resolution) and the
    `Usage:` line.
  - **Review Criteria:** Registry-pattern tests: unknown tool (error names the
    tool and lists available names); `--params` `42`/`"x"`/double-encoded →
    code 1 and the fixture script is **not** executed (marker file absent);
    success run → stdout carries the script's `<stdout>`-tagged output, code
    0; script `exit 3` → output printed, code 1; missing required param →
    validation message, code 1, script not executed; a `sleep 5` script under
    per-tool `Timeout: 1s` → timeout message, code 1, returns within ~2s;
    per-tool `Timeout: NONE` under a short global → completes (per-tool
    wins); explicitly empty `--params=` ≡ `{}`.
- [ ] **Task 4: Docs, usage text, version**
  - **Description:** README: new **Diagnostics** section in Usage —
    `--list-tools` (with the rendered example from the Objective) and
    `--call-tool` (with the `--params` JSON form, exit-code table: 0
    success / 1 any failure, output streams), the note that diagnostic mode
    ignores server-mode flags and honors the full timeout precedence
    (`Timeout: NONE` ⇒ the debug call has no deadline), a one-line note that
    this prints exactly what the LLM sees, the ignored-server-flags stderr
    notice, and the duplicate-`Param:` `validateRequiredParams` quirk note
    (documented, inherited from the handler). Flag help strings, `Usage:`
    text, and README kept consistent. `serverVersion` → `0.7.0`.
  - **Review Criteria:** README examples copy-pasteable and match actual
    output (run the built binary against the example scripts and paste the
    real output); every documented behavior (mutual exclusion, ignored
    server flags, exit codes, timeout precedence, 160-col wrap) matches the
    code; `--help` output and README consistent.
- [ ] **Task 5: Commit & branch hygiene (cross-cutting)**
  - **Description:** Work lands on branch `feat/tool-diagnostics` off `main`.
    Conventional commits in order: `feat: render tool list output`
    (includes PLAN.md + archive), `feat: --list-tools flag`, `feat:
    --call-tool and --params debug invocation`, `docs: diagnostics and version
    0.7.0`. `go build ./...`, `go vet ./...`, `go test ./...` green at every
    commit. Final manual smoke: build, then run `--list-tools` and
    `--call-tool` against the meridian remote toolset
    (`/workspace/games/experiment-settler2/tooling/remote` with `--dir` set to
    the settler2 checkout) and diff the listing against the example in the
    Objective (PLAN.md + archive already landed on `main` in the planner
    commit; the branch commits touch code/docs only).
  - **Review Criteria:** `git log main..feat/tool-diagnostics` shows exactly
    the four commits, all green; smoke output matches the documented format
    byte-for-byte (modulo the 160-col wrap); PR ready to open.

## Edge Case & Safety Checklist

- No executable scripts → `--list-tools` prints empty stdout, exit 0, the
  existing `Warning: No executable scripts found in <dir>` on stderr.
- Unreadable scripts directory → stderr error, exit 1 (both diagnostics).
- Tool with empty frontmatter description → rendered line is the timeout
  suffix alone (same rule the registry uses today).
- Duplicate `Param:` names → single signature entry, last declaration wins,
  first-occurrence position (matches `buildInputSchema`).
- Word longer than 160 columns (long path/URL in a description) → emitted
  whole on its own line; never split, never truncated.
- Multi-byte text (em-dashes, accented characters) → wrap counts **runes**,
  never tears a character.
- `--params` non-object (`42`, `["x"]`, `"str"`, double-encoded) → exit 1
  before any script starts.
- `--params` explicitly empty (`--params=`) → treated as `{}`, valid.
- Unknown `--call-tool` name → exit 1, message lists available tool names.
- Declared-required param missing → validation message to stdout, exit 1,
  script not started (identical to the MCP handler result).
- Script exits non-zero → its `<stdout>`/`<stderr>` content printed, exit 1.
- Script unstartable (vanished file, exec bit removed) → stderr, exit 1.
- Timeout expiry → the standard `tool timed out after <d>` result, exit 1.
- `Timeout: NONE` / `--no-timeout` → the debug call runs with **no deadline**
  (matches production; documented so a re-call of a slow build can complete).
- Duplicate name declared `required` then `optional` → `--call-tool` still
  enforces it (`validateRequiredParams` iterates all declarations) — inherited
  handler quirk, documented, unchanged.
- `--list-tools` and `--call-tool` both passed → startup error (mutually
  exclusive).
- Server-mode flags (`--watch`, `--port`, `--api-key`, CORS) in diagnostic
  mode → validated fail-fast, then ignored with a single stderr notice naming
  the ignored flags; no port bound, no watcher, no stdio server.
- Hot-reload state irrelevant: diagnostics read the directory at call time;
  a concurrent `--watch` server is a separate process and unaffected.
- 1 MB output cap and arg-key injection guard apply unchanged (diagnostics
  reuse `executeTool` / `parseToolArguments` wholesale).

## Review Log (Plan Review)

- **Round 1:** Verified every claim against the current code. Confirmed: (1) `toolRegistry.replace` assembles the registered description exactly as the `registeredDescription` extraction assumes (`suffix := timeoutSuffix(toolTimeout); if description == "" { description = suffix } else { description += " " + suffix }`), so the extraction is behavior-identical and the existing exact-string tests (`"beta updated (timeout: 5m0s)"`, the `TestResolvedTimeoutViaRegistry` table, `"(timeout: 30s)"`) stay green; (2) `buildInputSchema` dedups param names last-wins (properties-map overwrite + `lastRequired`), so `listParamDecl`'s last-wins rule matches the registered schema; (3) `discoverTools` iterates `os.ReadDir` entries, i.e. sorted by filename; (4) `parseToolArguments` maps empty/`null` → `{}` and rejects non-objects and double-encoded JSON with `"arguments must be a JSON object"` (as `TestParseToolArgumentsRejectsDoubleEncodedJSON` documents), so the `--params` claims incl. lenient `--params=` are accurate; (5) `main()` is `--version` → required-flag check → `resolveCORS` → `resolveTimeout` → `run()`, so a dispatch branch after the two resolvers leaves every existing behavior — including `--version` priority — handled, and the `Usage:` rewrite is consistent. The proposed seams (pure helpers as table tests; `diagnose`/`runCallTool` returning exit codes) are all implementable in the existing single-file test setup, in the established registry-pattern style. The fail-loud-fail-fast strategy and the overall structure match the house style of the timeout-handling plan. **Defect (blocker, 1):** the Objective's example output is not what the spec produces against the real remote toolset — (a) the `meridian_build` wrap breaks ~8–10 runes early on every line (plan lines are 151/150/59 runes; a greedy 160-rune wrap with 5-space indent and space-only breaks yields 159/159/42: `cached.` fits on line 1, and line 2 ends `…prefer scope=app`, not the em-dash); (b) the `meridian_fetch` example omits the frontmatter sentence "Run first on a remote host before any other meridian tool." and renders one line, but the real registered description + ` (timeout: 2m0s)` wraps to two lines (158/53 runes). Because Task 4's criteria (README examples must match actual output) and Task 5's smoke gate (diff the listing against the Objective example) anchor acceptance on this block, it must be regenerated. **Corrections required before approval:** replace the Objective example with the spec-correct output below (generated by applying the plan's 160-col/5-space rune wrap to the actual frontmatter of `/workspace/games/experiment-settler2/tooling/remote`):

```
meridian_build([profile:str], [scope:str], [target:str], [timings:bool])
     Compile the Meridian workspace (or just meridian-app) on the remote host — the offload target; the long pole is the Bevy dep tree, built once then cached.
     Timeout is NONE (a full clean build of the Bevy dep tree can far exceed the 5-minute MCP default; re-call to see a slow build's result) — prefer scope=app
     for incremental work. (timeout: none)

meridian_fetch([commit:str])
     Check out the pinned commit of the Meridian repo on the remote host (idempotent) and report HEAD/branch, rustc, cargo and Cargo.lock info. Run first on a
     remote host before any other meridian tool. (timeout: 2m0s)
```

**Advisory (non-blocking, 7):** (1) Task 1 should state that `renderToolList` resolves each tool's timeout with the same precedence as `replace` (per-tool `Timeout:` > global) before calling `registeredDescription`; a shared `resolveToolTimeout(tool, global)` helper keeps the two call sites from drifting. (2) Fix the deduped entry's *position* in the signature: first-occurrence position with last-occurrence type/required (mirrors `buildInputSchema`'s first-occurrence `required`-array order) — the plan says last-wins dedup but not the position. (3) State the `wordWrap` contract deterministically: the 160 total **includes** the 5-space indent (content budget 155), and runs of whitespace collapse to one break — needed so the multi-line table tests are unambiguous. (4) A one-line README/edge-checklist note on an inherited quirk: `validateRequiredParams` iterates *all* declarations, so a duplicate name declared `required` then `optional` is still enforced by `--call-tool` although the schema marks it optional (last-wins) — it is consistent with the production handler, so document, don't change. (5) QoL: when server-mode flags are explicitly passed in diagnostic mode, print a single stderr notice (disclosed, per fail-loud) rather than silently ignoring them. (6) Label the Objective example "(excerpt)" — the remote toolset contains 9 executable `meridian_*` scripts (`common.sh` is non-executable, `history` a dir); two is fine as an illustration. (7) Task 3's `runCallTool(scriptsAbs, dirAbs, …)`: path resolution + `os.Stat` currently live only in `run()`; note that the diagnostic branch does its own `filepath.Abs`/`os.Stat` (or extracts a shared helper) so the signature is honest. Advisories 1–7 at the planner's discretion; only the example replacement is mandatory. **Status: Rejected**

- **Round 2:** Re-verified the `ca8ca2b` response. **(1) Mandatory correction — confirmed fixed:** re-derived the Objective example independently from the real frontmatter of `/workspace/games/experiment-settler2/tooling/remote` (greedy 155-rune content budget, 5-space indent, whitespace-run collapse, `time.Duration` String() suffix): byte-identical to the plan block (line rune lengths 159/159/42 and 158/64; line 2 correctly ends `…prefer scope=app` with the em-dash starting line 3). The "(excerpt — 9 executables)" label matches the directory (9 exec scripts; `common.sh` non-executable, `history` a dir) and the excerpt is the first two in discovery order. **(2) Advisories 1–7 — all incorporated into the plan body, not just acknowledged:** (1) `resolveToolTimeout(tool, global)` shared by `replace` and `renderToolList` — Requirements bullet + Task 1 description + review criteria (`per-tool-wins/global-fallback/nil`) + Task 3's "same timeout resolution as `toolRegistry.replace`"; consistent. (2) First-occurrence position / last-wins type/required — Requirements bullet + Task 1 description + edge checklist; three places agree. (3) Deterministic wrap contract (160 incl. indent, 155 budget, greedy, run-collapse, no word split, overlong whole) — Requirements + Task 1 `wordWrap` spec + table tests; edge checklist consistent. (4) `validateRequiredParams` duplicate quirk — Requirements bullet + Task 4 README note + edge checklist. (5) Ignored-server-flags stderr notice — Requirements bullet + mode dispatch ("the stderr notice above" now resolves — the earlier dangling reference is fixed) + edge checklist. (6) Excerpt label — done. (7) Shared Abs+Stat helper extracted from `run()` — mode dispatch bullet + Task 3 description ("the branch uses the shared Abs+Stat helper"); `run()` is untouched per Task 2. **(3) No regressions found:** Task 1's helper list (`registeredDescription`, `resolveToolTimeout`, `listParamDecl`, `renderToolList`, `wordWrap`) matches all usages in Tasks 2–4; `registeredDescription`'s "empty desc → suffix alone" still matches `replace`'s current code (re-confirmed); the Task 5 smoke gate (diff against Objective) is now sound because the Objective block is verified. **Advisory (non-blocking, 1):** Tasks 2/3 review criteria don't explicitly assert the ignored-flags stderr notice (Task 2's criteria say "ignored, no `ListenAndServe`" without the notice; Task 3's criteria omit it) — the behavior is mandated by the Requirements bullet and edge checklist, so a Builder is covered; adding the assertion to Task 2's criteria is a QoL improvement the Builder may include. **Status: Approved**

## Final Status (Code Review)

_(pending)_
