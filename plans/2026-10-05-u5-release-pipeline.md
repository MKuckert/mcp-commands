# PLAN — U5: Release pipeline and documentation

**Branch:** `fix/u5-release-pipeline` from `main` (v0.9.4). **Target:** v0.9.5.
**Source:** `REVIEW_REPORT.md` §U5 (M10, H5's CI leg, M9, L1, L2).
**Status:** Draft.

## Tasks

- [/] M10: Release pipeline gate — `release.yml` runs `go test ./...` and `go vet ./...` in a job that the goreleaser job `needs:`; a version-verification step builds with the tag injected via ldflags and asserts `--version` prints the tag version. `.goreleaser.yaml` injects `main.serverVersion` from the tag (`ldflags: -X main.serverVersion={{.Version}}`) so tagged releases can never claim the hardcoded dev version.
- [/] M10: Bump the development version to 0.9.5 in `main.go` and `Makefile`; update the pinned release-download archive name in `README.md` and `docs/index.html` from `0.9.0` to `0.9.5`.
- [/] H5 CI leg: new `ci.yml` workflow (push + pull_request to `main`) running `go vet ./...` and `go test ./...` on `ubuntu-latest` and `windows-latest`; a Windows-gated discovery/invocation smoke test in `discover_test.go`/`execute_test.go` so the `windows-latest` job natively exercises `.exe` discovery and execution.
- [/] M9: Website examples in `docs/index.html` made runnable — the `render.sh` demo parses the translated `--source` flag instead of using an unset `${source}`; the "Run it" HTTP command includes the required `--dir`/`--scripts` and no longer labels plain HTTP "TLS"; the client config JSON uses absolute paths instead of a literal `~/proj`; the release-install tab targets `0.9.5`.
- [/] L1: README trust-boundary and safety language — qualify "the sandbox stays the security boundary" (name the scripts directory as the server's own trust boundary), remove the obsolete "Adjust package path" install aside and the non-operational "AI Usage" section, separate the browser page origin from the MCP endpoint in the CORS client example, and document/fix the `--allowed-origins` env-fallback so the flag always wins (an explicit `--allowed-origins=` means "no origins, ignore env" — the flag *value* being empty no longer re-consults `MCP_COMMANDS_ALLOWED_ORIGINS`). Website: replace "Reliably safe." and "injection-proof" with claims the code actually makes.
- [/] L2: README `--list-tools` claims — describe the output as a human-readable rendering of the registered tools (names, signatures, descriptions, effective timeouts), not "exactly what the LLM sees"; soften the Accept-header claim to a recommendation without the absolute "always 400" assertion.
- [ ] Final: `gofmt`, `go test ./count=1`, `go vet ./...` green; plan status updated; commit.

## Design

- **M10 (version):** the source constant `main.serverVersion` stays as the *development* version (bumped per release commit, as in v0.9.3/v0.9.4). Goreleaser already derives `{{.Version}}` from the `vX.Y.Z` tag; injecting it via `builds.ldflags` makes the *shipped* binary tag-derived, so a mismatch between the constant and the tag can no longer produce a release claiming the wrong version. The release workflow verifies this explicitly: after goreleaser, a step builds with `-X main.serverVersion=v$GITHUB_REF_NAME` and asserts `--version` equals the tag. The test/vet gate is a separate `test` job so a failing test blocks the release without touching the goreleaser step.
- **H5 CI leg:** the OS-aware predicate (`isToolFile`'s Windows extension branch) already shipped with U1; what was missing is *running it on Windows*. A `ci.yml` with a `windows-latest` job doing `go test ./...` exercises discovery through the real code path natively. The smoke test is GOOS-gated (`runtime.GOOS == "windows"`): it writes an executable-named `tool.exe`, asserts discovery registers it, and invokes it through the real exec path. No Unix runner can exercise the Windows branch of `isToolFile`, so the gate is required, not decorative.
- **M9:** all three defective website examples (demo script, run-it HTTP command, client config) are corrected to be copy-runnable as written. The demo script parses `--source` from argv the way the argument translation actually emits it. The run-it HTTP example carries `--dir`/`--scripts` and names TLS as an additional explicit step (`--tls-cert`/`--tls-key` or a TLS-terminating proxy), not a property of the shown command.
- **L1 (empty flag precedence):** `resolveCORS` currently treats an empty `--allowed-origins` value as "flag not given" and falls back to the env var, contradicting the documented "each flag wins over its env var" rule. Fix (chosen over doc-only): `parseCLI` detects explicit `--allowed-origins` via the existing `fs.Visit` and passes `allowedOriginsSet` to `resolveCORS`; a set-but-empty flag disables CORS origins and does not consult the env var — mirroring the `allowAllSet` pattern already in place for the boolean flag. Table-tested.
- **L1/L2 (language):** the README and site get the actual contracts: the trust boundary is write access to `--scripts` (code execution as the server user); `--list-tools` is a human-readable rendering, not the JSON schema; the Accept header is *recommended* explicit, not absolutely required. "Untrusted Tool Output" already states the real boundary — the overpromising lines are aligned with it, not removed.
- **Draft move (L2):** `docs/posts/` no longer exists in the tree (the draft note was removed before this branch); the Pages workflow deploys `docs/` as-is, so nothing further is required — the task list notes this.

## Review log

_(empty — pending review)_
