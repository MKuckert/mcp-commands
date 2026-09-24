# Plan: Optional CORS Support for the HTTP Transport (Browser Clients)

## Objective

Let browser-based MCP clients (web chat UIs, in-browser agents) talk to the
streamable HTTP transport, which the Go SDK's `NewStreamableHTTPHandler` does
not CORS-enable on its own. CORS support is **opt-in**: new flags
`--allowed-origins` / `--allow-all-origins` configure a CORS middleware that
wraps the handler, and `--disable-localhost-protection` opts out of the
SDK's default DNS-rebinding 403. In addition, the streamable handler is
**always constructed with `Stateless: true`** (user decision 2026-09-24):
this app keeps no per-session state — the tool registry is process-global —
so protocol sessions are vestigial. With no CORS flags set, the CORS surface
is byte-identical to today (zero CORS headers, no OPTIONS change); the
stateless switch is a user-visible 0.5.0 change and is documented (see
Requirements). The stdio transport is untouched.

Research: `/workspace/research/mcp-commands-cors.md` (verified against
`modelcontextprotocol/go-sdk v1.6.1` — the exact version in `go.mod`).
Key findings relied on: the SDK has no CORS/OPTIONS handling (its only origin
machinery is the deopted `CrossOriginProtection` and the default-on DNS-rebinding
localhost protection); JSON POSTs are non-simple, so browsers always preflight;
`Mcp-Session-Id` is unreadable in JS without `Access-Control-Expose-Headers`;
raw `EventSource` cannot work in *either* SDK mode, so the supported browser
path is fetch-based (e.g. the official MCP TS SDK).

---

## Requirements & Decisions

- **Frameworks:** Go standard library only (`net/http`, `strings`, `os`,
  `net/url`). No new dependencies — the app stays single-file with one direct
  dep beyond the SDK. (Third-party middleware like `rs/cors` explicitly
  rejected: ~50 lines of stdlib suffice.)
- **Configuration surface** (flag wins over env, like `--api-key`):

  | Flag | Env | Type | Meaning |
  |---|---|---|---|
  | `--allowed-origins` | `MCP_COMMANDS_ALLOWED_ORIGINS` | comma-separated string | Exact origin allowlist (`https://app.example.com`) |
  | `--allow-all-origins` | `MCP_COMMANDS_ALLOW_ALL_ORIGINS` | bool (`1`/`true`/`yes`, case-insensitive) | Echo any `Origin` (dev convenience) |
  | `--disable-localhost-protection` | *(none, deliberate)* | bool | `StreamableHTTPOptions{DisableLocalhostProtection: true}` — disables the SDK's default DNS-rebinding 403 on loopback servers; needed when a page on a tunnel/LAN hostname talks to a `127.0.0.1` server. |

  `--disable-localhost-protection` deliberately has **no** env fallback (a
  mode choice, not a secret) — the departure from the api-key pattern is
  documented in the README.
- **Always stateless (behavior change, user decision):** `buildHTTPHandler`
  always passes `Stateless: true` to `NewStreamableHTTPHandler`. Rationale:
  the app keeps no per-session state, so the SDK's session ID is issued,
  validated, and then ignored by us — pure overhead. Consequences, all
  documented in the README as 0.5.0 changes:
  - `Mcp-Session-Id` is vestigial. **Verified (Builder, 2026-09-24):**
    go-sdk v1.6.1 still *issues* the header in `initialize` responses even in
    stateless mode (streamable.go sets it unconditionally), but ignores it on
    all later requests — clients that store and resend it keep working.
  - Per spec `2025-11-25`, stateless servers should receive
    `Mcp-Protocol-Version` per request. **Builder: verify what go-sdk v1.6.1
    does when the header is absent in stateless mode** (expected: defaults to
    the latest supported version) and record the actual behavior in the
    README; if it 400s, that is a spec-conformance gap for the SDK, not a
    bug in this feature — clients conforming to the spec are unaffected.
  - GET is 405 in stateless mode (it already required a session before); the
    browser path remains fetch-POST based.
- **Origin matching:** exact, case-sensitive string match. No wildcard /
  pattern matching in v1 (YAGNI). Origins are validated at startup and
  garbage fails fast (`net/url` parse + require non-empty scheme, `http` or
  `https` scheme, non-empty host, no userinfo/query/fragment) — consistent
  with the existing fail-fast flag handling. Validation runs in **all**
  modes (a bad flag is an error even in stdio mode); only *application* of
  CORS is HTTP-mode-only, exactly like `--api-key`.
- **`--allowed-origins` + `--allow-all-origins` together:** startup error
  (contradictory configuration) — fail loud, never silently pick one.
- **CORS disabled** = neither origins nor allow-all set → no middleware
  attached, no `Vary: Origin`, no OPTIONS change. Default: off.
- **Middleware behavior** (`newCORSHandler`):
  1. No `Origin` header → non-browser caller; pass through untouched, add
     nothing.
  2. `Origin` not allowed (not in list, and not allow-all) → pass through
     **without** CORS headers; no status change (auth/MCP answer for itself).
  3. `Origin` allowed → set *before* `next.ServeHTTP` so the headers ride
     along on every downstream response, including 401s, SDK JSON, and SSE
     streams: `Access-Control-Allow-Origin: <echoed origin>` (never `*` —
     echo + `Vary: Origin` keeps shared caches and the credentials gotcha
     safe) and `Access-Control-Expose-Headers: Mcp-Session-Id, Last-Event-ID`
     (the session ID is never issued in stateless mode, so exposing it is a
     no-op; `Last-Event-ID` is harmless until an `EventStore` exists).
  4. Preflight `OPTIONS` (allowed origin) → add
     `Access-Control-Allow-Methods: POST, OPTIONS` (the transport is always
     stateless; the SDK 405s GET/DELETE),
     `Access-Control-Allow-Headers: <echo of Access-Control-Request-Headers>`
     (echo beats hardcoding: the MCP header set evolves; still fully
     controlled because only allowed origins see it — set only when the
     request carried a non-empty value), `Access-Control-Max-Age: 900`;
     respond **204** and never call `next`.
  5. No `Access-Control-Allow-Credentials` — there is no cookie auth (token
     is a header).
- **Middleware ordering (critical):** CORS is **outermost**:
  `http.Server → CORS → bearer auth (if token) → mcp.NewStreamableHTTPHandler`.
  - Browsers never send `Authorization` on preflight → preflight must not be
    authenticated; outermost placement guarantees the 204.
  - Headers set before `next.ServeHTTP` guarantee 401s from the auth wrapper
    carry CORS (else the browser masks the 401 as a CORS error — the most
    confusing failure mode).
- **Stdlib `http.NewCrossOriginProtection()` is deliberately not added**
  (Go 1.24+ is available in `go 1.25.0`): it would 403 the legitimate
  external-page → loopback-server setup and conflicts with the allowlist;
  the SDK's own DNS-rebinding/localhost protection (on by default) already
  covers the public-host threat and its 403s inherit CORS headers from the
  outer middleware.
- **Security posture (documented in README):** this server executes local
  scripts, so CORS is not a security boundary (it gates only which page's JS
  *reads* responses). Default off; `--allow-all-origins` is safe only with
  `--api-key` + TLS; explicit origin list is the production recommendation.
- **Logging:** the startup line composes its parenthetical from enabled
  features: `Starting HTTP server on <addr> (API key auth enabled, CORS:
  3 origin(s))`, `(..., CORS: any origin — dev mode)`; parens omitted when
  neither is enabled (today's line).
- **Usage string** in `main()` gains the three new flags.
- **Version:** `serverVersion` bumps `0.4.0` → `0.5.0` (new feature release).

---

## Implementation Steps

> Status Markers: [ ] Open, [/] In Progress, [x] Completed (set after accepted review only!)

Housekeeping (already done by the Planner): the completed api-token-auth
plan was archived to `plans/2026-09-03-api-token-auth.md`; this file is the
live plan. The archive move is committed with Task 1.

- [ ] **Task 1: CORS configuration (`corsConfig` + `resolveCORS`)**
  - **Description:** Add to `main.go`:
    ```go
    const (
        allowedOriginsEnvVar = "MCP_COMMANDS_ALLOWED_ORIGINS"
        allowAllOriginsEnvVar = "MCP_COMMANDS_ALLOW_ALL_ORIGINS"
    )

    type corsConfig struct {
        origins                  []string // exact origin allowlist
        allowAll                 bool     // echo any Origin (dev)
        disableLocalhostProtection bool   // StreamableHTTPOptions.DisableLocalhostProtection
    }

    // disabled reports whether no CORS behavior is requested.
    func (c corsConfig) disabled() bool { return len(c.origins) == 0 && !c.allowAll }

    // originSet returns the allowlist as a set for O(1) lookup.
    func (c corsConfig) originSet() map[string]bool { ... }

    // summary is the startup-log fragment for enabled CORS.
    func (c corsConfig) summary() string {
        if c.allowAll { return "CORS: any origin — dev mode" }
        return fmt.Sprintf("CORS: %d origin(s)", len(c.origins))
    }

    // resolveCORS resolves flags (win) over env and validates origins.
    // Returns an error for malformed origins or a contradictory
    // --allowed-origins + --allow-all-origins combination.
    func resolveCORS(allowedOriginsFlag string, allowAllFlag, disableLocalhostProtection bool) (corsConfig, error) { ... }
    ```
    - Origins: split on comma, `strings.TrimSpace` each, drop empties.
    - Bool env: `parseBoolEnv` helper — `""` → false; `1`/`true`/`yes`
      (case-insensitive) → true; anything else → error (fail loud, no
      silent misparse).
    - Origin validation: `url.Parse`, reject parse errors, require scheme
      `http`/`https`, non-empty `u.Host`, no `u.User`/`u.RawQuery`/`u.Fragment`.
  - **Review Criteria:**
    - Flag non-empty → flag wins, env ignored; env only → env used; neither
      → empty.
    - `"https://a.example, https://b.example"` (spaces) → two clean origins.
    - `--allowed-origins x --allow-all-origins` → error.
    - `notaurl`, `https://`, `ftp://x.example`, `https://user@x.example`,
      `https://x.example/path` → startup errors; `https://x.example:8443`
      → accepted.
    - Bool env `"TRUE"`, `"0"`, `"yes"` handled; `"banana"` → error.

- [ ] **Task 2: CORS middleware (`newCORSHandler`)**
  - **Description:** Add to `main.go`:
    ```go
    const (
        corsAllowMaxAge    = "900" // 15 min; browsers cap at 7200s
        corsExposedHeaders = "Mcp-Session-Id, Last-Event-ID"
        // The transport is always stateless; the SDK 405s GET/DELETE.
        corsAllowedMethods = "POST, OPTIONS"
    )

    // newCORSHandler allows cross-origin browser requests from allowed
    // origins. Requests without an Origin header, and origins not on the
    // allowlist (and not covered by allowAll), pass through with no CORS
    // headers — the browser then blocks the response itself.
    func newCORSHandler(next http.Handler, cfg corsConfig) http.Handler {
        allowed := cfg.originSet()
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            origin := r.Header.Get("Origin")
            if origin == "" || (!cfg.allowAll && !allowed[origin]) {
                next.ServeHTTP(w, r)
                return
            }
            h := w.Header()
            h.Set("Access-Control-Allow-Origin", origin) // echo, never "*"
            h.Add("Vary", "Origin")
            h.Set("Access-Control-Expose-Headers", corsExposedHeaders)
            if r.Method == http.MethodOptions { // preflight
                h.Set("Access-Control-Allow-Methods", corsAllowedMethods)
                if achr := r.Header.Get("Access-Control-Request-Headers"); achr != "" {
                    h.Set("Access-Control-Allow-Headers", achr)
                }
                h.Set("Access-Control-Max-Age", corsAllowMaxAge)
                w.WriteHeader(http.StatusNoContent)
                return // never call next for preflight
            }
            next.ServeHTTP(w, r)
        })
    }
    ```
  - **Review Criteria:**
    - No `Origin` → zero header changes, request forwarded.
    - Disallowed `Origin` → no CORS headers, forwarded (status unchanged).
    - Allowed `Origin` → ACAO echoes it, `Vary` includes `Origin`, expose
      headers set, on every downstream response including 401s.
    - Preflight → 204, `Access-Control-Allow-Methods: POST, OPTIONS`,
      echoed request headers, max-age 900, empty body, `next` not invoked.

- [ ] **Task 3: Wire into `main()` / `run()` / `buildHTTPHandler`**
  - **Description:**
    - `buildHTTPHandler(server *mcp.Server, token string, cors corsConfig)` —
      new `cors` parameter (breaks the two existing test call sites; updated
      in Task 4):
      ```go
      func buildHTTPHandler(server *mcp.Server, token string, cors corsConfig) http.Handler {
          h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
              return server
          }, &mcp.StreamableHTTPOptions{
              // Always stateless (user decision): the app keeps no
              // per-session state, so protocol sessions are vestigial.
              Stateless:                  true,
              DisableLocalhostProtection: cors.disableLocalhostProtection,
          })
          if token != "" {
              h = newBearerAuthHandler(h, token)
          }
          if !cors.disabled() {
              h = newCORSHandler(h, cors) // outermost: preflight unauthenticated, 401s carry CORS
          }
          return h
      }
      ```
      (Builder: confirm the two `StreamableHTTPOptions` field names exist in
      go-sdk v1.6.1 — the research verified them; compile is the final proof.)
    - `main()`: add the three flags (`--allowed-origins`,
      `--allow-all-origins`, `--disable-localhost-protection`); update the
      usage string; call
      `resolveCORS(...)` and exit on error via the existing
      `fmt.Fprintf(os.Stderr, "Error: %v\n")` + `os.Exit(1)` pattern
      (matching the `--dir`/`--scripts` validation); pass the resolved
      `corsConfig` into `run` (validation therefore runs in all modes,
      before `run`).
    - `run(ctx, dir, scriptsDir string, watch bool, host string, port int,
      apiKey string, cors corsConfig)`: consumes the passed `corsConfig`
      (already resolved/validated in `main`) — no re-resolution here;
      `buildHTTPHandler(server, apiKey, cors)` in the HTTP branch; startup
      log composes its parenthetical from `apiKey != ""` and
      `!cors.disabled()` (see Requirements).
  - **Review Criteria:**
    - No flags → server starts, zero CORS behavior, log line identical to
      today.
    - `--port` + `--allowed-origins` → log mentions origin count;
      `--allow-all-origins` → "any origin — dev mode".
    - stdio mode: byte-identical behavior, flags ignored (like api-key).
    - `Stateless: true` and `DisableLocalhostProtection` take effect only
      via `StreamableHTTPOptions`; no other code paths touched.
    - A re-sent `Mcp-Session-Id` (issued or made-up) keeps being served
      (stateless ignores it) — no 401/404.
    - **Verified (Builder):** a stateless request lacking
      `Mcp-Protocol-Version` defaults to `2025-03-26` (the oldest supported
      version, streamable.go:391–393), not the latest; it never 400s on a
      missing header. Recorded in README per Requirements.

- [ ] **Task 4: Tests**
  - **Description:** Add to `main_test.go` (table-driven where noted, matching
    existing style; the two existing `buildHTTPHandler(server, ...)` call
    sites at the end of the file gain a zero `corsConfig` argument):
    1. `TestResolveCORS` — table: flag-only, env-only, both (flag wins),
       neither; origins comma+space parsing; allow-all env
       (set/clear `t.Setenv(allowAllOriginsEnvVar, ...)`); contradictory
       flags → error; malformed origins → error; bool env `"banana"` →
       error.
    2. `TestCORSHandlerPreflight` — asserts 204, ACAO echo,
       `Access-Control-Allow-Methods: POST, OPTIONS`, `Vary: Origin`,
       `Access-Control-Allow-Headers` echo (and absent when the request
       sent none), `Access-Control-Max-Age: 900`, empty body, and a
       sentinel `next` proves the request was **not** forwarded.
    3. `TestCORSHandlerNonPreflight` — table: no Origin / disallowed Origin /
       allowed Origin / allowAll mode → assert header presence/absence and
       pass-through to a recorder `next` on a normal (non-204) response.
    4. `TestBuildHTTPHandlerPreflightUnauthenticated` — serve
       `buildHTTPHandler(server, "s3cret", cors)` via `httptest.NewServer`;
       `OPTIONS` with an allowed `Origin` **and no Authorization header** →
       204 with CORS headers, not 401 (ordering guarantee).
    5. `TestBuildHTTPHandler401CarriesCORS` — POST with a bad token +
       allowed `Origin` → 401 **with** `Access-Control-Allow-Origin` and
       `Access-Control-Expose-Headers: Mcp-Session-Id, Last-Event-ID`.
    6. `TestBuildHTTPHandlerInitializeCORS` — full `initialize` round-trip
       (mirror `TestBuildHTTPHandlerEndToEnd`) with an allowed `Origin` →
       non-401, CORS headers present; then a follow-up request re-sending the
       session ID (or a made-up one) is still served — the always-stateless
       guarantee (verified: v1.6.1 *does* issue a vestigial
       `Mcp-Session-Id` header, so the test asserts it is *ignored*, not
       absent).
    7. `TestBuildHTTPHandlerCORSDisabled` — zero `corsConfig`, request
       without Origin → no CORS headers at all (regression guard for the
       default-off promise).
  - **Review Criteria:**
    - All seven tests present; `go test ./...` green; no sleeps/polling.
    - The pre-existing tests are unmodified except the two `buildHTTPHandler`
      call sites gaining the zero-value `corsConfig` argument.

- [ ] **Task 5: Documentation & release**
  - **Description:**
    - `README.md`: new `#### Browser Clients (CORS)` subsection after
      `#### Authentication (optional)`, under `### Starting the Server`
      (mirroring the auth subsection shape: env vars + flag precedence +
      client example), covering: CORS is off by default and opt-in;
      `--allowed-origins` (comma-separated, exact match, production
      recommendation); `--allow-all-origins` is a dev convenience — safe
      only with `--api-key` + TLS, because the server executes local scripts
      CORS gates only response *readability*, not reachability;
      **0.5.0 behavior change — the transport is always stateless:** each
      request stands on its own; go-sdk v1.6.1 still issues a vestigial
      `Mcp-Session-Id` header on `initialize` but ignores it on later
      requests, so clients that stored and send a session ID keep working;
      the SDK defaults a missing `Mcp-Protocol-Version` header to 2025-03-26
      (oldest supported); this is also why raw `EventSource` remains unusable
      in any mode — use a fetch-based client such as the MCP TS SDK; stateless
      clients per spec send `Mcp-Protocol-Version` per request, and fetch
      clients must send `Accept: application/json, text/event-stream` on POST
      (the SDK 400s otherwise; the TS SDK does both automatically);
      `--disable-localhost-protection` for dev setups where the page is
      served from a tunnel/LAN hostname and the server binds 127.0.0.1, with
      the DNS-rebinding trade-off warning; the flag intentionally has no env
      fallback; `MCPGODEBUG=enableoriginverification=1` (go-sdk v1.6.1)
      makes the SDK 403 *all* cross-origin requests internally and conflicts
      with this feature — do not set it to "fix" CORS failures; a short ops
      note that a TLS-terminating proxy (Caddy/nginx) is required for
      browser production use and is the alternative way to add CORS.
    - `serverVersion` → `"0.5.0"` (single location).
  - **Review Criteria:**
    - README examples copy-paste runnable; flag/env names match the code
      exactly.
    - Version string updated in exactly one place.

- [ ] **Task 6: Commits**
  - **Description:** Conventional commits, one per task unit; `PLAN.md` is
    included in each related commit (per AGENTS.md Committer convention).
    The first commit also carries the `plans/2026-09-03-api-token-auth.md`
    archive move:
    1. `feat: add CORS and streamable-HTTP mode configuration flags`
       (Task 1 + archive move)
    2. `feat: CORS middleware for browser clients on the HTTP transport`
       (Task 2)
    3. `feat: run the streamable HTTP transport stateless and wire in CORS`
       (Task 3)
    4. `test: cover CORS middleware, preflight ordering, and origin resolution`
       (Task 4)
    5. `docs: document browser client (CORS) support; bump version to 0.5.0`
       (Task 5)
  - **Review Criteria:** `git log` matches; no secrets in any commit (tests
    use placeholder tokens only).

---

## Edge Case & Safety Checklist

- **CORS disabled (default):** no middleware, no `Vary: Origin`, no OPTIONS
  change — existing curl/desktop clients see byte-identical CORS behavior.
  Deliberate opt-in; Task 4, test 7 (`TestBuildHTTPHandlerCORSDisabled`)
  guards it.
- **Always stateless (0.5.0 behavior change, verified):** the SDK v1.6.1
  still issues a `Mcp-Session-Id` header on `initialize` even in stateless
  mode, but ignores it on all later requests. Existing clients that store
  it and send it on later requests keep working — nothing breaks, the
  header is simply vestigial. Documented as a 0.5.0 change in the README.
  (GET was already unusable without a session; in stateless mode it is a
  clean 405.)
- **Stateless + missing `Mcp-Protocol-Version` header (verified):** the SDK
  v1.6.1 defaults to `2025-03-26` (the oldest supported version) and never
  400s on a missing header; recorded in the README. Spec-conforming clients
  send the header per request.
- **Preflight without `Origin`:** treated as non-browser, passed to `next`
  (SDK answers 400/405-ish) — mirrors curl behavior; no header injection.
- **`Origin: null`** (sandboxed iframes, `file://` pages): matched like any
  other string — echoed under `--allow-all-origins` only, never via the
  exact list. Do not special-case it.
- **Empty `Access-Control-Request-Headers` on preflight:** the
  `Allow-Headers` response header is simply omitted (never set empty).
- **Duplicate `Vary` values:** `h.Add("Vary", "Origin")` appends a second
  header line if downstream code also sets `Vary`; both lines are legal and
  clients combine them. Acceptable.
- **`--allow-all-origins` without `--api-key` on a non-loopback host:** any
  site on the internet can both invoke tools *and read* responses; the README
  requires the explicit origin list for production. The flag is accepted
  (operator choice) — README warning, not a startup error, matching the
  app's existing open-by-default posture when no token is set.
- **DNS rebinding / localhost protection:** on by default (`go-sdk`
  v1.6.1, spec `2025-11-25`); its 403 responses carry CORS headers because
  the middleware sets headers before `next`. `--disable-localhost-protection`
  is the escape hatch, documented with the trade-off.
- **`MCPGODEBUG=enableoriginverification=1` (v1.6.1 compat knob):** the SDK
  403s cross-origin requests *inside* the handler where the CORS middleware
  cannot recover — documented in the README as a non-fix.
- **Credentials:** `Access-Control-Allow-Credentials` is never emitted; the
  only auth mechanism is the `Authorization` header, which is not a
  credentialed request under the CORS spec.
- **Origin wildcard patterns (`https://*.example.com`):** out of scope for
  v1; exact list covers the realistic cases. Follow-up candidate.
- **SSE streams:** headers are written once at stream start (before the
  first SSE byte), which is exactly when the browser evaluates CORS — no
  `Flusher` special-casing needed.
- **Stdio mode:** zero behavior change; the CORS flags are parsed and
  validated (fail-fast) but never applied.
- **TLS:** the server is HTTP-only; browser production use requires a
  TLS-terminating proxy (documented).

---

## Review Log (Plan Review)

- **Round 1:** **APPROVED**

  Every plan claim was verified against `main.go`, `main_test.go`, and the
  go-sdk v1.6.1 source: the current `buildHTTPHandler(server, token)`
  signature and its two test call sites, the `run(...)` signature, the
  `apiKey`-based startup-log if/else, `reject()` (never clears pre-set
  headers, so CORS headers ride along on 401s), `serverVersion` at a single
  `var` location, and the existence of
  `StreamableHTTPOptions{Stateless, DisableLocalhostProtection}` in v1.6.1
  (a non-nil zero-value struct is behaviorally identical to today's `nil`).
  The SDK sets no CORS headers and no `Vary` today, so the default-off
  promise holds. Security design sound: echo-origin + `Vary: Origin` +
  never `*` is cache- and credentials-safe; echoing
  `Access-Control-Request-Headers` is acceptable because only allowed
  origins see the response; skipping stdlib
  `http.NewCrossOriginProtection()` is defensible and arguably required —
  its default-deny policy would 403 the exact external-page → loopback
  setups the feature exists to serve, while the SDK's default-on
  DNS-rebinding protection remains the real boundary (its 403s inherit CORS
  headers from the outer middleware) and the
  `MCPGODEBUG=enableoriginverification=1` conflict is documented.
  Completeness vs the research report: config table, all five middleware
  rules, ordering, security notes, and all eight research tests are covered
  (research test 8 folded into Task 4's preamble; tests 4–6 merged into
  plan tests 3/7; the `corsConfig` struct refines the research's four
  scalar parameters). House style matches
  `plans/2026-09-03-api-token-auth.md`.

  **Advisory (non-blocking), all folded into the plan text:**
  1. Edge Case checklist said "Task 7 regression test" — corrected to Task
     4, test 7 (`TestBuildHTTPHandlerCORSDisabled`).
  2. Task 3 implied double resolution of CORS (in `main()` *and* `run()`) —
     now explicit: `main()` resolves/validates, `run()` only consumes.
  3. Task 5's README anchor "HTTP Server Mode" is not a section heading —
     now pinned to `#### Browser Clients (CORS)` after
     `#### Authentication (optional)` under `### Starting the Server`.
  4. README bullet list gains the research's `Accept:
     application/json, text/event-stream` note for fetch-based clients.
  5. Edge Case checklist gains an `Origin: null` note (allow-all only,
     never via the exact list; do not special-case).
  No issue link applies (no GitHub issue for this feature); the prior
  plan's "Resolves:" line has no analog — accepted as-is.
- **Round 2:** **APPROVED (revised per user decision)**

  User decision 2026-09-24: **drop the `--stateless` flag and hardcode
  `Stateless: true`** in `buildHTTPHandler` (the app keeps no per-session
  state — the registry is process-global — so protocol sessions are
  vestigial; making it the only mode is a deliberate 0.5.0 behavior change,
  documented). `--disable-localhost-protection` stays as designed (opt-out;
  the DNS-rebinding protection remains the one real security boundary for a
  script-executing local server and must not be off by default). Plan text
  updated consistently: Objective, Requirements (config table, always-
  stateless decision with `Mcp-Session-Id` and `Mcp-Protocol-Version`
  consequences + Builder verification item), middleware rule 4 (constant
  `POST, OPTIONS`), Tasks 1–6 (struct, sketch, flags, usage string, test 2
  and 6, README bullets, commit 3 subject), and the Edge Case checklist
  (always-stateless bullet + protocol-version bullet; the `--stateless`
  bullet replaced). The always-stateless choice is defensible: it loses no
  app functionality, matches the spec's stateless-server expectations, and
  simplifies the CORS surface; the client-visible delta (no session ID
  issued) is called out for the README. No further correction required.
- **Round 3:** N/A

## Final Status (Code Review)

- **Round 1:** (pending)
- **Round 2:** N/A
- **Round 3:** N/A
