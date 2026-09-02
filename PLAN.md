# Plan: API Token Authentication for the HTTP Transport (Issue #3)

## Objective

Protect the HTTP transport with an **optional** static API token. If the
operator supplies a token — via a new `--api-key` flag or the
`MCP_COMMANDS_API_KEY` environment variable (predefined key) — every request
to the HTTP server must carry it in an `Authorization: Bearer <token>`
header; anything else is rejected with `401 Unauthorized`. If no token is
supplied, the server starts unauthenticated, exactly as it does today. The
stdio transport is untouched — it is local and has no network surface.

Resolves: https://github.com/mkuckert/mcp-commands/issues/3

---

## Requirements & Decisions

- **Frameworks:** Go standard library only (`net/http`, `crypto/subtle`,
  `strings`). No new dependencies.
- **Token sources (in precedence order):**
  1. `--api-key <token>` CLI flag
  2. `MCP_COMMANDS_API_KEY` environment variable
- **Scope:** Applies **only** in HTTP mode (`--port > 0`). Stdio mode ignores
  the token entirely (no warning, no behavior change).
- **Optional feature:** HTTP mode with **no** token from either source starts
  the server **without authentication** — identical behavior to today. No
  error, no warning, no auto-generated token (the issue allows "either
  predefined or generated"; we implement the predefined path per scope).
- **Auth check (only when a token is configured):** Middleware wrapping the
  SDK's `*mcp.StreamableHTTPHandler` (which implements `http.Handler`, so no
  SDK changes are needed):
  - Header must be exactly `Bearer <token>`: scheme compared
    case-insensitively per RFC 7235, separated by a single space, non-empty
    token. Anything else (missing header, `Basic`, extra spaces, empty token)
    → reject.
  - Token compared with `crypto/subtle.ConstantTimeCompare` (timing-attack
    safe). Length mismatch is also a constant-time failure path.
  - Rejection response: `401 Unauthorized` with body
    `unauthorized` and header `WWW-Authenticate: Bearer` (RFC 6750).
  - Enforced for **all** HTTP methods (GET, POST, DELETE, others) — the
    middleware runs before the SDK handler's own method dispatch.
- **Logging:** When a token is configured, log
  `Starting HTTP server on <addr> (API key auth enabled)`; otherwise keep the
  existing `Starting HTTP server on <addr>` line. The token value is **never**
  logged, in any log line or error.
- **Usage string:** `main()` usage/help output gains `[--api-key <token>]`.
- **Version:** `serverVersion` bumps `0.3.0` → `0.4.0` (new feature release).

---

## Implementation Steps

> Status Markers: [ ] Open, [/] In Progress, [x] Completed (set after accepted review only!)

- [x] **Task 1: Token resolution (`resolveAPIKey`)**
  - **Description:** Add to `main.go`:
    ```go
    const apiKeyEnvVar = "MCP_COMMANDS_API_KEY"

    // resolveAPIKey returns the token from the flag value if non-empty,
    // otherwise from MCP_COMMANDS_API_KEY. Returns "" if neither is set.
    func resolveAPIKey(flagValue string) string {
        if flagValue != "" {
            return flagValue
        }
        return os.Getenv(apiKeyEnvVar)
    }
    ```
  - **Review Criteria:**
    - Flag non-empty → flag wins, env var ignored (even if set).
    - Flag empty, env set → env value returned.
    - Neither set → `""`.

- [x] **Task 2: Bearer-auth middleware (`newBearerAuthHandler`)**
  - **Description:** Add to `main.go`:
    ```go
    // newBearerAuthHandler wraps next, requiring an
    // "Authorization: Bearer <token>" header that matches token.
    // Mismatches get 401 with a WWW-Authenticate: Bearer header.
    func newBearerAuthHandler(next http.Handler, token string) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            scheme, got, ok := strings.Cut(r.Header.Get("Authorization"), " ")
            if !ok || !strings.EqualFold(scheme, "Bearer") || got == "" {
                reject(w)
                return
            }
            if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
                reject(w)
                return
            }
            next.ServeHTTP(w, r)
        })
    }

    // reject writes a 401 with the WWW-Authenticate header per RFC 6750.
    func reject(w http.ResponseWriter) {
        w.Header().Set("WWW-Authenticate", "Bearer")
        w.WriteHeader(http.StatusUnauthorized)
        _, _ = w.Write([]byte("unauthorized"))
    }
    ```
    `strings.Cut` splits on the **first** single space: no space (or header
    shorter than scheme) → `ok == false` → 401; multiple spaces leave them in
    `got`, which then fails the token comparison.
  - **Review Criteria:**
    - No header → 401. Header without space / empty token (`"Bearer"`,
      `"Bearer "`) → 401. Wrong scheme (`"Basic xyz"`) → 401.
    - `"bearer <token>"` (lowercase scheme) → 200 (RFC 7235 case-insensitivity).
    - Wrong token → 401; correct token → request reaches `next`.
    - `WWW-Authenticate: Bearer` present on every 401.
    - Comparison uses `subtle.ConstantTimeCompare`.

- [x] **Task 3: Wire into `run()` / `main()`**
  - **Description:**
    - `main()`: add `apiKeyFlag := flag.String("api-key", "", "API token required by HTTP clients (or set MCP_COMMANDS_API_KEY)")`;
      update the usage string; pass `*apiKeyFlag` into `run(...)` (new last
      parameter).
    - `run(ctx, dir, scriptsDir, watch, host string, port int, apiKey string)`:
      resolve via `resolveAPIKey(apiKey)` **before** any server setup.
      The key is optional: `port > 0` with an empty key starts the server
      unauthenticated (today's behavior).
    - Extract handler construction so it is testable:
      ```go
      // buildHTTPHandler returns the streamable MCP handler. When token is
      // non-empty it is wrapped in bearer-token auth middleware; otherwise
      // the handler serves requests unauthenticated.
      func buildHTTPHandler(server *mcp.Server, token string) http.Handler {
          inner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
              return server
          }, nil)
          if token == "" {
              return inner
          }
          return newBearerAuthHandler(inner, token)
      }
      ```
      `run()` uses `buildHTTPHandler(server, apiKey)` as the `http.Server`
      handler. Startup log becomes
      `Starting HTTP server on <addr> (API key auth enabled)` **only** when a
      token is configured; the existing log line is kept otherwise.
  - **Review Criteria:**
    - `--port` without any key → server starts, requests are served
      unauthenticated (no behavior change).
    - `--port` + key → server starts; unauthenticated request gets 401;
      authenticated request is served.
    - stdio mode: identical behavior to before with or without a key set.
    - Token never appears in any log output.

- [x] **Task 4: Tests**
  - **Description:** Add to `main_test.go` (table-driven, matching existing style):
    1. `TestResolveAPIKey` — table: flag-only, env-only (set/clear
       `t.Setenv(apiKeyEnvVar, ...)`), both (flag wins), neither.
    2. `TestBearerAuthMiddleware` — table over header values: `""`, `"Bearer"`,
       `"Bearer "`, `"Basic abc"`, `"bearer <token>"` (accepted),
       `"Bearer <wrong>"`, `"Bearer <token>"`; assert status code and that an
       `httptest.ResponseRecorder`-captured `WWW-Authenticate` header is
       `Bearer` on 401; a downstream recorder handler proves pass-through.
    3. `TestBuildHTTPHandlerAuthDisabled` — serve
       `buildHTTPHandler(server, "")` via `httptest.NewServer`; assert a POST
       `/` initialize request **without** any Authorization header is served
       (non-401), i.e. an empty token leaves the handler unauthenticated.
    4. `TestBuildHTTPHandlerEndToEnd` — build a real `mcp.Server` with one
       trivial tool (reuse the registry or `AddTool` directly), serve it via
       `httptest.NewServer(buildHTTPHandler(server, "s3cret"))`; assert:
       POST `/` initialize request without header → 401; with
       `Authorization: Bearer s3cret` → non-401 (200/202).
  - **Review Criteria:**
    - All four tests present, table-driven where noted, no sleeps/polling.
    - `go test ./...` green; existing tests unmodified and passing.
    - Note (Plan Reviewer, Round 1): the Task 3 criteria "token never appears
      in any log output" and "stdio mode identical behavior" have no automated
      test here — verify them manually during code review.

- [x] **Task 5: Documentation & release**
  - **Description:**
    - `README.md`: extend the **HTTP Server Mode** section: authentication is
      optional; when enabled via `--api-key` or `MCP_COMMANDS_API_KEY`
      (flag precedence), all requests need the
      `Authorization: Bearer <token>` header or get 401; without a token the
      server is open as before. Include a client-side example (e.g. MCP client
      config with an `Authorization` header / `curl -H "Authorization: Bearer <token>" ...`).
      Note that stdio mode needs no token.
    - `serverVersion` → `"0.4.0"`.
  - **Review Criteria:**
    - README examples are copy-paste runnable and consistent with the flag/env
      names in code.
    - Version string updated in exactly one place (`serverVersion` const).

- [x] **Task 6: Commits**
  - **Description:** Conventional commits, one per task unit; `PLAN.md` is
    included in each related commit (per AGENTS.md Committer convention):
    1. `feat: resolve API key from --api-key flag or MCP_COMMANDS_API_KEY`
    2. `feat: optional Authorization Bearer auth for HTTP transport`
    3. `test: cover bearer auth middleware and API key resolution`
    4. `docs: document API token auth; bump version to 0.4.0`
  - **Review Criteria:** `git log` matches; no token values or secrets in any
    commit (tests use placeholder tokens only).

---

## Edge Case & Safety Checklist

- **No token set:** authentication is disabled; the server behaves exactly
  as it does today (open HTTP endpoint). This is a deliberate, documented
  choice — the feature is opt-in.
- **Empty token via flag (`--api-key ""`):** treated as unset → env var
  consulted; if that is empty too, auth stays disabled.
- **Token with spaces:** allowed; the client must send it verbatim after
  `Bearer `. We do not trim the token value (only validate header shape).
- **Header with multiple spaces (`"Bearer  x"`):** the single-space split
  yields token `" x"` (leading space) which will not match a stored token —
  rejected unless the token itself starts with a space. Acceptable; documented
  by the one-space rule in README.
- **Timing attacks:** `subtle.ConstantTimeCompare` on both sides; no early
  return on length mismatch that leaks token length (length equality is
  checked, which leaks only the *length* — standard and acceptable for static
  tokens).
- **CORS/OPTIONS:** no CORS is configured today; the middleware rejects
  unauthenticated preflights with 401. Consistent with "all requests require
  the token"; no special-casing (YAGNI).
- **Stdio mode:** zero behavior change; `resolveAPIKey` is simply not called
  in a way that affects execution.
- **Token leakage:** never logged, never in error messages, never in usage
  output. `--version` and help output unaffected.
- **Existing clients:** when the operator enables auth, current HTTP clients
  break until they send the token — intentional (that is the point of the
  feature); documented in README. When auth stays disabled, nothing changes.

---

## Review Log (Plan Review)

- **Round 1:** **APPROVED**

  The plan is complete, feasible against the actual code in `main.go`, and
  internally consistent. Every task maps to verifiable code changes: the
  `run(ctx, dir, scriptsDir, watch, host string, port int)` signature and the
  `mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server, nil)`
  construction in Task 3 match the current implementation exactly, so
  `buildHTTPHandler` is a drop-in extraction; `resolveAPIKey` and the
  middleware use only stdlib imports (`os`, `strings`, `crypto/subtle`,
  `net/http`) already or easily added to `main.go`. The optional-token
  behavior the user mandated (no token → unauthenticated, today's behavior;
  token set → Bearer enforced) is reflected consistently in Objective,
  Requirements & Decisions, Tasks 1–3, and the Edge Case checklist — no
  contradiction found. Security edge cases (timing-safe comparison with the
  length-leak explicitly acknowledged, empty token, header shape via
  `strings.Cut`, multi-space headers, CORS preflight rejection, stdio
  untouched, no token leakage in logs) are all covered. The README has an
  "HTTP Server Mode" section (line 53) for Task 5 to extend, and
  `serverVersion` is currently `0.3.0`, matching the planned bump. The four
  specified tests are implementable with `httptest` and the existing SDK API
  (`mcp.NewServer`, `AddTool`), matching the table-driven style of
  `main_test.go`. Three non-blocking advisory items follow; none require a
  correction loop.

  **Advisory (non-blocking):**

  1. *Task 6, commit 3:* message says "HTTP startup validation", but no task
     covers startup validation — Task 4's tests cover key resolution,
     middleware, and auth-disabled/with-token handler behavior. Align the
     commit subject with what is actually tested (e.g. "test: cover bearer
     auth middleware and API key resolution").
  2. *Task 3 vs Task 4:* two review criteria — "token never appears in any
     log output" and "stdio mode: identical behavior" — have no corresponding
     automated test in Task 4 (both are hard to assert without refactoring
     `run()`'s stderr output capture). Acceptable for code-review verification,
     but the Builder should be aware these criteria are checked manually.
  3. *Edge Case checklist, stdio:* `--api-key` set in stdio mode is silently
     ignored ("no warning"). This matches the user's explicit scope decision,
     so it stands; noted only because AGENTS.md's fail-loud philosophy would
     otherwise favor a one-line stderr note. No change required.
- **Round 2:** N/A
- **Round 3:** N/A

## Final Status (Code Review)

- **Round 1:** **APPROVED**

  The implementation in `main.go`, `main_test.go`, and `README.md` matches the approved plan exactly. Task 1: `resolveAPIKey` is implemented verbatim with the `apiKeyEnvVar` constant, and all three precedence criteria are covered by a four-case table in `TestResolveAPIKey` (flag-only, env-only, both/flag-wins, neither). Task 2: `newBearerAuthHandler` and `reject` match the plan's code; header-shape rejection via `strings.Cut`, case-insensitive scheme via `strings.EqualFold`, `subtle.ConstantTimeCompare` for the token, and `WWW-Authenticate: Bearer` on every 401 path were verified in code and exercised by the seven-case table in `TestBearerAuthMiddleware` (including lowercase-scheme acceptance, wrong scheme, empty token, and downstream pass-through). Task 3: the `--api-key` flag, usage string, `run(...)` signature with `apiKey` as last parameter, resolution before server setup, the `buildHTTPHandler` extraction, and the conditional startup log are all in place; stdio mode is byte-identical to before (the `port == 0` branch never touches `apiKey`). Task 4: all four specified tests exist, are table-driven where noted, use no sleeps/polling, and the existing tests are unmodified (the `main_test.go` diff is purely additive). Task 5: the README gains an optional-authentication subsection with flag/env precedence, a copy-paste `curl` example, a JSON client-config example, the single-space rule, and the stdio note; `serverVersion` is bumped to `0.4.0` in exactly one place. Task 6: the four commits (`21b00f1`, `7ee04d9`, `f47d936`, `4041b71`) have the plan's (advisory-corrected) subjects, sensible file membership, and no real tokens — only placeholders (`tok`, `s3cret`, `my-secret-token`). Stability: `gofmt -l .` clean, `go build ./...` OK, `TMPDIR=/root/gotmp go test -count=1 ./...` green (17/17 top-level tests pass). Plan Reviewer advisory items were resolved: (a) "token never appears in any log output" verified by reading all 17 print statements in `main.go` — none reference the key value; (b) "stdio mode identical behavior" verified as above. Non-blocking notes: `go test -race` is infeasible in this environment (Go race runtime FATALs with "Found 39 - Supported 48" identically on the base commit `41b426b`, so it is environmental, not a regression); `serverVersion` is declared as `var` though the plan text said "const" (pre-existing declaration, single location, no functional difference); PLAN.md appears in commit 1 only because it was unmodified by commits 2–4 (checkboxes are ticked post-review), which is consistent with the Committer convention; the multi-space header edge (`"Bearer  x"`) has no dedicated test case, but the plan's Task 4 spec did not require one and its behavior is documented in the README.
- **Round 2:** N/A
- **Round 3:** N/A
