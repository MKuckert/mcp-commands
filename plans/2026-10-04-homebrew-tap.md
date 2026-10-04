# PLAN — Homebrew tap for mcp-commands (v0.9.0)

**Branch:** `feat/homebrew-tap` (from `main` @ `36f1d41`)
**Target version:** v0.9.0
**New repo:** `MKuckert/homebrew-tap` (created as part of this plan)
Status: Approved

## Scope

- [/] U1: versioned release archive names (breaking URL change) + version bump to 0.9.0
- [ ] U2: `release.yml` — dispatch `release-bumped` to the tap repo after a successful release
- [ ] U3: create `MKuckert/homebrew-tap` — formula `mcp-commands.rb` @ v0.9.0, tap README, `bump-formula.yml` workflow
- [ ] U4: main README — brew install section + updated download instructions
- [ ] U5: cut v0.9.0 release, end-to-end verification (dispatch → formula bump → audit)

## Design decisions

1. **Archive naming (breaking):** GoReleaser `name_template` becomes
   `{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}` →
   `mcp-commands_0.9.0_darwin_arm64.tar.gz` etc. Existing v0.8.3 assets keep their
   old names (assets are not renamed retroactively); only new releases use the
   new layout. This lets the formula omit an explicit `version` (Homebrew
   infers it from the URL) and is the standard Go/GoReleaser layout.
   Windows `zip` inherits the same template (→ `mcp-commands_0.9.0_windows_amd64.zip`).
   **Pinned archive layout (R-1):** `wrap_in_directory: false` is set explicitly
   (binary at archive root) — the formula's `bin.install "mcp-commands"` path
   must not depend on an implicit GoReleaser default. U1's dry-run verifies the
   inner path with `tar -tzf`/`unzip -l` (R-1).
2. **Formula shape:** class `McpCommands`, `license :mit`, no `version` line
   in steady state (version inferred from the new-name URL). **Exception
   (N-1):** the one-time v0.8.3 seed (Decision 5) carries an explicit
   `version "0.8.3"` line — old-name URLs encode no version for Homebrew to
   infer — and Decision 3's script deletes any `version "…"` line on the first
   bump, so it never goes stale.
   `on_arm` / `on_intel` / `on_linux` blocks each carry `url` + `sha256`
   (per-arch asset ⇒ per-arch checksum). No `glibc` dependency — the release
   build is `CGO_ENABLED=0` (static). Install: `bin.install "mcp-commands"`
   (archive layout pinned per Decision 1). `livecheck` block on the
   GitHub `releases/latest` redirect, `regex v?(\d+(?:\.\d+)+)`.
   `test do assert_match /\d+\.\d+\.\d+/, shell_output("#{bin}/mcp-commands --version") end` —
   `--version` skips all validation (flags.go:159) and prints the *bare*
   version string, so the test matches the semver, not the name (R-2).
   No bottle workflow (nothing is compiled).
3. **Bump automation (Pattern A):** `bump-formula.yml` in the tap repo,
   `on: repository_dispatch: types: [release-bumped]` with
   `client_payload.tag` (e.g. `v0.9.0`), **`permissions: contents: write`** —
   the job's own `GITHUB_TOKEN` performs the commit/push; the PAT only
   authorizes the dispatch call from the main repo (R-4). Script: strip the
   leading `v` from the tag (`v0.9.0` → `0.9.0` — GoReleaser's `{{ .Version }}`
   is bare, so filenames/URLs use the bare form, R-5); delete any
   `version "…"` line (present only in the v0.8.3 seed, N-1); rewrite the
   `url` (tag + version in the filename) and `sha256` of each of the three
   `on_*` blocks wholesale from the tag-derived template; then fetch
   `checksums.txt` for the new sums, run
   `brew audit --formula Formula/mcp-commands.rb` as a gate, commit as
   `github-actions[bot]` with message `mcp-commands <ver>`, push to `main`.
   Idempotent: re-dispatch on an unchanged version is a no-op
   (`git diff --quiet` → exit 0, no commit).
4. **Dispatch (main repo `release.yml`):** new step after the GoReleaser job
   succeeds: `gh api -X POST repos/MKuckert/homebrew-tap/dispatches` with
   `event=release-bumped`, `client_payload=tag=$GITHUB_REF_NAME`,
   `GH_TOKEN: ${{ secrets.BUMP_TAP_TOKEN }}`. Secret = a PAT with
   `contents:write` + `contents:read` on `homebrew-tap` only. The dispatch
   step must **not** fail the release on its own error (release artifacts
   are already uploaded): the step runs `if: success()` on the goreleaser
   job (a failed release publishes no `checksums.txt` — dispatching would
   spawn spurious failing bumps, N-2), with `continue-on-error: true` on the
   dispatch itself.
5. **Order of operations (R-3, sequence (a) chosen):** U1+U2 merge on `main`
   **first**. Then U3 pushes the tap repo **before** the tag: the formula is
   seeded with the *real, already-published* v0.8.3 values (old-name layout —
   the formula is installable from the moment of push; the bump script
   rewrites the `url` lines wholesale from a tag-derived template, so the
   layout change rides along in the first genuine bump). Finally U5 tags
   `v0.9.0` → release dispatch performs the *real* v0.8.3→v0.9.0 bump, so the
   automated path is exercised end-to-end (no silent first dispatch, no
   placeholder checksums ever shipped). The only unverifiable-in-sandbox
   part stays the macOS `brew install` gate.
6. **Trust model in docs:** current Homebrew requires explicit trust for
   non-core taps. README documents **both** lines:
   `brew trust --formula MKuckert/homebrew-tap/mcp-commands` then
   `brew install MKuckert/homebrew-tap/mcp-commands`.
7. **Makefile `buildall` is untouched** — it is the local `dist/` layout, not
   release assets; GoReleaser alone governs published names.

## Units (one commit each, in order)

**U1 — versioned archives + 0.9.0 bump** (main repo)
- `.goreleaser.yaml`: `name_template` → `{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}`
- `main.go` `serverVersion` → `0.9.0`; `Makefile` `VERSION` → `0.9.0`
- README "Installing a release" section: new asset names (`mcp-commands_0.9.0_<os>_<arch>.tar.gz`)
- Tests: `go test ./...` green (no code behavior change)

**U2 — dispatch step** (main repo)
- `release.yml`: post-goreleaser `Trigger tap bump` step (Decision 4)
- Document the required `BUMP_TAP_TOKEN` secret in the PR description
  (the secret itself is created manually; nothing to commit for it)

**U3 — tap repo** (new `MKuckert/homebrew-tap`)
- `Formula/mcp-commands.rb` per Decision 2, **seeded with the published v0.8.3 values** (real sha256s from the v0.8.3 release, old-name URLs) **plus an explicit `version "0.8.3"` line** (N-1 — old-name URLs encode no inferable version; the first bump deletes it) — pushed *before* the v0.9.0 tag per Decision 5, so the formula is valid at first push and the release dispatch performs a genuine first bump
- `bump-formula.yml` per Decision 3
- `README.md` (what the tap is, the two-line install incl. `brew trust`, maintenance = automatic via dispatch)
- `.gitignore` (`.DS_Store`)
- No `tap-new` bottle workflow, no `Casks/`

**U4 — README brew section** (main repo)
- Installation: add Homebrew as the first option (two-line trust + install),
  keep `go install` and release download as alternatives; note the new
  archive naming for the download path (already done in U1 — U4 is the brew part)

**U5 — release + verification** (sequence per Decision 5)
- Create secret `BUMP_TAP_TOKEN` in the main repo settings
- Merge U1+U4 → push tap repo (U3, seeded v0.8.3) → **then** tag `v0.9.0`
- Verify:
  - release dispatch fires → `bump-formula` commits `mcp-commands 0.9.0`
    (genuine bump from the seeded v0.8.3 — this is the end-to-end proof;
    a failure here is a plan defect, not an acceptable no-op)
  - `brew audit --new --formula Formula/mcp-commands.rb` (run locally where
    Homebrew exists; the workflow's audit gate covers CI)
  - `--version` test exits 0 and prints the version string
  - On a macOS machine: `brew trust --formula … && brew install
    MKuckert/homebrew-tap/mcp-commands && brew test mcp-commands` —
    **requires a real machine; flag as the final manual gate** (sandbox is
    Linux, no guarantee `brew` is available — if Linuxbrew works here, use it
    as a secondary check)
- Re-dispatch `v0.9.0` once more to prove idempotency (no second commit)

## Out of scope

- Linuxbrew eligibility review beyond the `on_linux` block (add block; if
  Linuxbrew rejects, `depends_on :macos` is the one-line fallback)
- Submitting to `Homebrew/homebrew-core` (upstream tap is the goal; core
  submission is a later, separate discussion)
- Re-naming existing v0.8.x release assets

## Verification

- `gofmt -l .` clean, `go vet ./...` + `go test ./...` green (U1)
- GoReleaser dry run: `goreleaser release --snapshot --clean` (or `goreleaser
  check`) shows `mcp-commands_0.9.0_darwin_arm64.tar.gz`-style names, and
  `tar -tzf`/`unzip -l` confirms the pinned inner path (R-1) (U1)
- Bump script table-tested: run it against the real v0.9.0 `checksums.txt`
  locally before push; diff shows only url/sha256 lines change (U3/U5)
- `brew audit --formula` green (U5, where brew exists)

## Review Log

R-1: **Archive internal layout is unpinned.** Decision 2 asserts "GoReleaser places the binary at archive root", which is only the *implicit* default of GoReleaser ≥ v2.8 (`wrap_in_directory: false`); earlier v2.x used `directory`, and the workflow floats the version (`distribution: goreleaser`). U1 must pin the layout explicitly in `.goreleaser.yaml` (recommend `wrap_in_directory: true` with a *version-less* fixed name such as `mcp-commands`, so the formula's `bin.install` path is stable across bumps), and the dry-run verification must assert the inner path via `tar -tzf`/`unzip -l`, not just the file name.

R-2: **Formula test assertion is wrong.** Decision 2 says the test asserts output contains `mcp-commands` — but `main.go:57` prints the *bare* version string (`0.9.0`); the assertion would fail. Assert the version (or a `\d+\.\d+\.\d+` match) instead.

R-3: **Ordering contradiction between Decision 5, U3, and U5.** Decision 5 says the formula is pushed *after* the release and "the first dispatch (from the real release) populates the correct values" — impossible: the U2 dispatch fires at tag time, before the tap repo exists, and a `repository_dispatch` to a missing repo/workflow is silently dropped (HTTP 204, no signal). Decision 3's script also *rewrites* existing `on_*` blocks, so the formula must exist before the first dispatch. Pick one explicit sequence and state it: (a) push the tap (bump workflow + formula seeded with real v0.8.3 values — the script builds the full URL from the tag, so the old-name layout seeds fine) **before** the tag, so the release dispatch performs a genuine bump and the auto path is exercised end-to-end; or (b) accept the post-release push and document that the release's dispatch is a silent no-op and the auto path is proven only by manual dispatch. As written, U5's "dispatch fires → commit (or no-op)" hides which case actually occurs.

R-4: **Bump workflow needs `permissions: contents: write`.** The PAT (`BUMP_TAP_TOKEN`) only authorizes the dispatch *call* from the main repo; the tap's `bump-formula.yml` runs with its own `GITHUB_TOKEN`, which is `contents: read` by default and will fail on the commit/push. U3 must declare `permissions: contents: write` (and `id-token: write` is not needed, don't add it).

R-5: **Bump script must strip the leading `v`.** `client_payload.tag` is `v0.9.0` (`$GITHUB_REF_NAME`), but GoReleaser's `{{ .Version }}` in the filename is the bare `0.9.0`; a URL built from the raw tag (`…/v0.9.0/…` with `mcp-commands_v0.9.0_…`) 404s. Spell out the `v`-strip in Decision 3's script. Minor: the Verification section's example name `mcp-commands_0.9.0-darwin-arm64.tar.gz` uses dashes — the chosen `name_template` produces underscores; fix the example to match.

## Re-Review (round 2)

**Verdict: Rejected** — corrections R-1…R-5 are all present and verified against the repo (`.goreleaser.yaml`, `release.yml`, `main.go`/`flags.go`); no leftover contradicting orderings remain (Decision 5 / U3 / U5 now uniformly sequence (a)). Two new gaps introduced by the corrections: **N-1 blocks** (formula shape / bump script completeness), **N-2 is minor** (dispatch gating).

**R-corrections verified:**
- R-1: Decision 1 pins `wrap_in_directory: false` explicitly; U1 + Verification assert the inner path via `tar -tzf`/`unzip -l`. `false` matches the published v0.8.3 layout, so no regression. ✔
- R-2: Decision 2 test is `assert_match /\d+\.\d+\.\d+/, shell_output(… --version)`. Confirmed: flags.go:159 `--version` skips all validation; main.go:57 prints the bare version. ✔
- R-3: Decision 5 is now unambiguous sequence (a): U1+U2 merged → tap pushed with real published v0.8.3 values → tag → genuine first bump; U3 and U5 restate the same order; "bump failure = plan defect" is explicit. No old phrasing remains. ✔
- R-4: Decision 3 carries `permissions: contents: write`, PAT correctly scoped to the dispatch call only. ✔
- R-5: `v`-strip spelled out in Decision 3; Verification example uses underscores. ✔

**New findings:**

N-1 (blocking): **Seeded v0.8.3 formula needs a `version` line the bump script must remove.** The v0.8.3 assets use the *old* name layout (`mcp-commands_darwin_arm64.tar.gz` — no version in the URL; confirmed against `.goreleaser.yaml`'s current `name_template`), so the seeded formula **must** carry an explicit `version "0.8.3"` line or it is invalid at push time (Homebrew cannot infer a version from that URL). But Decision 2 mandates *no* `version` line (valid only for the new, versioned-URL layout), and Decision 3's script only rewrites `url` + `sha256` — nothing removes the `version` line on the first bump. A builder following the plan literally leaves a formula with `version "0.8.3"` + 0.9.0 URLs, which `brew audit` fails → the `bump-formula` job fails the U5 end-to-end proof. Fix: state in U3 that the seed includes `version "0.8.3"`, and in Decision 3 that the script removes the `version` line once the URL carries the version (or rewrites it each run from the tag — pick one).

N-2 (minor): **Decision 4's dispatch gating is self-contradictory.** It says `if: always()` *"on the goreleaser success path"* — `always()` also fires when the goreleaser job *fails*, producing a dispatch for a release whose `checksums.txt` was never published, i.e. a spurious failing `bump-formula` run on every botched release. Use `if: success()` on the goreleaser job (the dispatch step needs no `always()` at all) and keep `continue-on-error: true` for the dispatch's own failure mode.

## Re-Review (round 3)

**Verdict: Approved — final.** No findings. Correction loop closed.

- **N-1 verified:** Decision 2 carries the exception (one-time v0.8.3 seed = explicit `version "0.8.3"`; the script deletes any `version "…"` line on first bump); Decision 3's script includes the deletion step, annotated "present only in the v0.8.3 seed"; U3's seed bullet spells out `version "0.8.3"`. Deleting a non-existent line is a no-op, so the script stays idempotent in steady state (Decision 2's "no `version` line" remains true for all post-seed formulas). The three statements agree; no leftover "no version line, no exceptions" text anywhere. ✔
- **N-2 verified:** Decision 4 now reads `if: success()` on the goreleaser job + `continue-on-error: true` on the dispatch. No `always()` remains anywhere in the plan. U2 ("post-goreleaser `Trigger tap bump` step") restates Decision 4 without contradiction. ✔
- **Final pass** (order, feasibility, completeness): Decision 5 / U3 / U5 sequence remains uniformly (a); tap exists before the tag, so the release dispatch hits a live workflow with a valid seeded formula. Repo spot-checks: `.goreleaser.yaml`'s current `name_template` (no version) confirms the old-name v0.8.3 seed URLs and the rationale for the explicit version line; `checksum: name_template: "checksums.txt"` matches the bump script's sum source; `release.yml`'s single `goreleaser` job is the correct insertion point; `main.go` prints the bare `serverVersion` and `flags.go:159` skips validation, so the formula `test` regex holds. Scope, verification gates, and out-of-scope list are complete and consistent.
- Non-blocking clarification (not a finding): "new step after the goreleaser job" — if the Builder adds it as a step *inside* the existing `goreleaser` job, `if: success()` means "all prior steps in this job succeeded", which is exactly the intended gate. A separate job with `if: success()` (job-level) works identically. Either implementation satisfies Decision 4; no plan change required.
