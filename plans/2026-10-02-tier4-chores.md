# PLAN — Tier 4 fixes (REVIEW.md v0.7.0 findings F-24…F-31)

**Branch:** `fix/tier4-chores` (from `main` @ 710c65e, post-Tier-3)
**Target version:** v0.8.3
**Source of truth:** `REVIEW.md` §Tier 4. F-23 (Makefile) is already ✅; F-26/F-27 were removed from the queue.

## Tasks

- [x] **1. Plan + version bump.** Archive this plan; bump `serverVersion` (main.go) and `Makefile VERSION` to 0.8.3.
- [x] **2. F-24 — `.zed/debug.json`.** Point `program` and `build.cwd` at the repo root (drop the stale `src/` prefix); keep the user's `--dir`/`--scripts`/`--port` args as-is.
- [x] **3. F-25 — plan archival.** Move root `PLAN.md` (completed, Approved tool-diagnostics plan) to `plans/2026-09-27-tool-diagnostics.md` (its review rounds landed 2026-09-27); add one-line "superseded" banners to `plans/2026-06-30-initial.md` and any other superseded plans it contradicts (`--ip`, `src/`, `os.Chdir`, 10-line scan).
- [x] **4. F-28 — optional TLS.** `--tls-cert <path>` / `--tls-key <path>` flags → `serverConfig.tlsCert/tlsKey`; fail fast when only one is given, and when (in HTTP mode) a file is unreadable; `run()` uses `ListenAndServeTLS` and logs a `TLS` startup note. Flags join `serverModeFlagNames` (diagnostic ignored-flags notice). README: new TLS subsection + flags-table rows — the default transport is cleartext; terminate upstream (Caddy/nginx) or use the flags.
- [x] **5. F-29 — untrusted tool output.** README paragraph: tool output is untrusted model input; the `<stdout>`/`<stderr>` tags are advisory — a script can emit literal `</stdout>` (prompt-injection surface, SEC-9).
- [x] **6. F-30 — vuln scanning in CI.** Add a `govulncheck` step to `.github/workflows/release.yml` (after `setup-go`, before GoReleaser).
- [x] **7. F-31 — test nits.**
  - `argumentsToCLIArgs`: `[]byte` Uint8-exemption case (single `--key <fmt.Sprint value>`, not expanded byte-by-byte).
  - `buildInputSchema`: duplicate names keep **first-occurrence** position in `required` (assert exact array order).
  - bearer: `"Bearer  tok"` (double space) → 401.
  - CORS: preflight from a *disallowed* origin → no CORS headers, forwarded to next.
  - SDK handler: `GET`/`PUT`/`PATCH` → 405 + `Allow: POST`; stateless `DELETE` without `Mcp-Session-Id` → 400 (session precondition, not method allowlist).
  - `wordWrap`: width ≤ 0 → budget floors at 1, no crash (both no-indent and over-indent cases).
  - `TestDiscoverTools` nonexistent-dir case: replace hardcoded `/tmp/nonexistent_dir_12345` with a path under `t.TempDir()`.
  - Stderr-capture style: normalize the ad-hoc subprocess capture in the remaining exec-based tests (audit; touch only what is demonstrably inconsistent).
- [x] **8. REVIEW.md bookkeeping.** Mark F-24…F-31 `✅ <date> (commit <short-sha>)` with deviations noted; note the target version.

## Conventions

Conventional Commits, one commit per task, plan file rides along in the first commit; `go vet ./...` + `go test ./...` green after each unit (`-race` unavailable in this sandbox).
