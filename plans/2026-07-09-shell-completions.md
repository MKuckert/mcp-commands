# PLAN — Shell completions shipped with the release (bash + zsh + fish)

**Branch:** `feat/shell-completions` (from `main` @ `7c4122f`, worktree `/workspace/mcp-commands-shell-completions`)
**Target version:** v0.12.0
**Repos touched:** `MKuckert/mcp-commands` (main) + `MKuckert/homebrew-tap` (formula, manual PR)
Status: Draft — awaiting Plan Reviewer

## Scope

- [ ] U1: three static completion scripts (`completion/`) + GoReleaser `archives.files` (main repo)
- [ ] U2: CI lint (shellcheck / `zsh -n` / `fish -n`) + flag-drift check (main repo)
- [ ] U3: version bump 0.11.0 → 0.12.0 (main repo)
- [ ] U4: README — Shell completion section (main repo)
- [ ] U5: tap formula — completion installs + test block (tap repo, **after** the v0.12.0 release)
- [ ] U6: cut v0.12.0, end-to-end verification (dispatch → formula bump → `brew audit` → install/test)

## Design decisions

1. **Static scripts, three shells, one source of truth per flag.** The CLI is a fixed 21-flag
   stdlib-`flag` surface (no subcommands, no cobra). We write three hand-maintained scripts in
   a new `completion/` directory: `mcp-commands.bash`, `mcp-commands.zsh`, `mcp-commands.fish`.
   No `--help` parsing at tab time (spawn per tab + fragile parser), no new hidden CLI command
   (would inflate the server binary's surface), no GoReleaser `completion` pipe (removed from
   current GoReleaser v2; the cask-only `generate_completions_from_executable` needs a
   binary-side command we don't have). Maintenance cost is bounded by Decision 4 (drift check).
2. **Flag inventory (the contract all three scripts implement)** — from `flags.go`:
   - *Booleans (no value):* `--watch` `--insecure-no-auth` `--allow-all-origins`
     `--disable-localhost-protection` `--version` `--no-timeout` `--list-tools`
   - *Directory values:* `--dir` `--scripts`
   - *File values:* `--api-key-file` `--tls-cert` `--tls-key`
   - *Enum value:* `--log-level` → `debug info warn error`
   - *Free-form / no completion:* `--host` `--port` `--api-key` (secret — never file-complete
     a token) `--max-concurrent` `--allowed-origins` `--timeout` `--call-tool` `--params`
   - **bash 3.2 floor** (macOS stock bash, `bash-completion` v1): the `.bash` script is limited
     to `complete`/`compgen`, plain arrays, `[[ ]]` — no bash-4+ features. zsh/fish scripts have
     no such floor.
   - **Parseable flag block (Decision 4 dependency):** each script carries its full flag list in
     exactly one dedicated block — bash: a single `flags="..."` assignment line; zsh: a single
     `flags=( ... )` array line; fish: a single `set -l flags ...` line. The drift check
     (Decision 4) extracts from that one block per script. The Builder keeps the value-class
     `case`/`-n` conditionals separate from the list block.
3. **Archive layout.** `.goreleaser.yaml` `archives.files` lists the three scripts
   (`completion/mcp-commands.{bash,zsh,fish}`) — non-glob sources land at the archive root with
   their basenames, alongside the binary (consistent with `wrap_in_directory: false`).
   **CRITICAL:** GoReleaser applies its default globs (`license*`, `LICENSE*`, `readme*`,
   `README*`, `changelog*`, `CHANGELOG*`) **only when `files` is unset**
   (`internal/pipe/archive/archive.go`, `len(archive.Files) == 0`). The current v0.11.0 archive
   (verified: `LICENSE`, `README.md`, `mcp-commands`) relies on those defaults, so the explicit
   `files:` list **must re-enumerate the six default globs plus the three scripts**, or the next
   release silently drops LICENSE/README/CHANGELOG from every archive. The v0.11.0 tarball
   contents are the regression baseline; U1's dry-run asserts the new set.
4. **Drift check (CI, ubuntu job only).** After building, extract flag names from
   `mcp-commands --help` and from each script's flag block, normalize leading dashes (stdlib
   `flag` prints `-dir`, scripts complete `--dir`), and diff the sets — fail CI on any
   divergence in any of the three scripts. This makes "forgot to update completions when
   adding a flag" a red build, not a stale tab. Table-tested in U2 with a synthetic flag.
5. **Formula (tap repo).** Inside **each** of the three stage branches (top-level darwin_arm64,
   `resource "darwin_amd64"`, `resource "linux_amd64"`) — the completion files exist in all
   three archives, and `*.install` reads from the active staging dir, so the installs must live
   in the same `stage` blocks as `bin.install`:
   ```ruby
   bash_completion.install "mcp-commands.bash" => "mcp-commands"   # etc/bash_completion.d
   zsh_completion.install    "mcp-commands.zsh"  => "_mcp-commands" # share/zsh/site-functions
   fish_completion.install   "mcp-commands.fish"                   # share/fish/vendor_completions.d
   ```
   (zsh files must be named `_mcp-commands` to autoload; fish files keep the command name.)
   No `depends_on "bash-completion"`/`"zsh"`/`"fish"` — homebrew-core convention
   (`pidcat`, `stress-ng`, `tree-sitter-cli`) is to ship completions without framework
   dependencies; brew auto-prints per-shell "completion installed to …" caveats.
   `link_overwrite` not needed (no other formula ships these paths for `mcp-commands`).
6. **Formula test block** —
   ```ruby
   test do
     system bin/"mcp-commands", "--version"
     system "bash", "-c", ". #{bash_completion}/mcp-commands"
     system "zsh",  "-c", ". #{zsh_completion}/'_mcp-commands'"
     system "fish", "-c", "source #{fish_completion}/mcp-commands.fish"
   end
   ```
   Sourcing is a real functional check: `complete`/`compadd`/fish `complete` are builtins, so
   the scripts load without the completion frameworks (bash is always present; zsh and fish are
   added as `depends_on "zsh" => :test` / `depends_on "fish" => :test` so the test is
   deterministic on both macOS and Linuxbrew).
7. **Bump workflow — no change.** `bump-formula.yml` rewrites only `url`/`sha256` lines and
   passes everything else through (`{ print }`); the new lines are static, and
   `checksums.txt` already covers the changed archive contents. The formula edit is a manual
   PR/push to the tap, like the one-time v0.8.3 seed — never touched by the dispatch.
8. **Ordering (same discipline as the v0.8.3 tap seed).** The formula must never reference
   files the archive lacks (`bash_completion.install` on a missing staged file fails the
   whole install). Sequence: merge main-repo PR (U1–U4, incl. version bump) → **tag v0.12.0**
   (release archives now contain the scripts; dispatch bumps the formula's URLs/shas to v0.12.0)
   → **then** push the formula completion PR (U5) → U6 verification. In this order the only
   formula state that ever ships is either "no completion lines + old archive" or "completion
   lines + archive that has the files".
9. **Docs (main README, after the Homebrew section).** Per-shell activation:
   - bash: `brew install bash-completion` (macOS stock bash 3.2) **or** `bash-completion@2`
     (+ `brew install bash` for 4+); then in `~/.bashrc`:
     `[[ -r "$(brew --prefix)/etc/profile.d/bash_completion.sh" ]] && . "$(brew --prefix)/etc/profile.d/bash_completion.sh"`
     (both v1 and `@2` source `#{prefix}/etc/bash_completion.d` — one file serves both)
   - zsh: in `~/.zshrc`: `fpath=($(brew --prefix)/share/zsh/site-functions $fpath)`
     (Homebrew's own zsh picks it up automatically)
   - fish: automatic once brew's prefix is on fish's path; note the vendor dir location
   - One line for non-brew users: the scripts ship in every release archive
     (extract `mcp-commands.{bash,zsh,fish}` and drop them in your shell's completion dir);
     `go install` binaries carry no completion files.
   - Linuxbrew note: distro bash-completion reads `/usr/share/bash-completion/completions`,
     not the brew prefix — brew's `bash-completion@2` is required for the brew file to load.

## Units (one commit each, in order)

**U1 — completion scripts + archive files** (main repo)
- `completion/mcp-commands.bash` — bash 3.2-safe, per Decision 2/3 shapes
- `completion/mcp-commands.zsh` — `#compdef mcp-commands`, `_arguments`-style
- `completion/mcp-commands.fish` — `complete -c mcp-commands` entries
- `.goreleaser.yaml`: `archives.files` = six re-enumerated default globs + three script sources
- Verification: `goreleaser release --snapshot --clean` (or `goreleaser check` + local build)
  → `tar -tzf`/`unzip -l` shows binary + 3 scripts + LICENSE + README.md + CHANGELOG.md at root

**U2 — CI lint + drift check** (main repo)
- `ci.yml` ubuntu job: new step after Test —
  `shellcheck completion/mcp-commands.bash` (runner-preinstalled),
  `zsh -n completion/mcp-commands.zsh` (runner-preinstalled),
  `sudo apt-get update && sudo apt-get install -y fish && fish -n completion/mcp-commands.fish`
- Drift check per Decision 4 (build → `--help` vs. the three flag blocks, dash-normalized)
- Table-test: temporarily add a flag to a script copy / the binary in the PR to prove the
  check fails, then restore

**U3 — version bump 0.12.0** (main repo)
- `main.go` `serverVersion` → `0.12.0`; `Makefile` `VERSION` → `0.12.0`
- README release-download examples → `mcp-commands_0.12.0_*` names
- `go test ./...` green (tag-injection assertion in release.yml covers the rest at tag time)

**U4 — README shell completion section** (main repo, per Decision 9)

**U5 — tap formula** (tap repo, direct push to tap main after v0.12.0 is published, per Decision 8)
- Three install lines in each stage branch + test block + `:test` deps (Decision 5/6)
- Commit message: `mcp-commands add bash/zsh/fish completion installs`
- The tap's `bump-formula` audit gate runs on the next dispatch; run `brew audit` locally as well

**U6 — release + end-to-end verification**
- Tag `v0.12.0` → release workflow green (9 files per archive: binary, 3 scripts, LICENSE,
  README, CHANGELOG) → dispatch → `bump-formula` commits `mcp-commands 0.12.0`
- Push U5 → `brew audit --formula mcp-commands` clean in tap CI
- Real machine (macOS): `brew trust --formula … && brew install … && brew test mcp-commands`
  (the three sourcing checks run), then a live tab test:
  `bash -ic 'complete -p mcp-commands'` shows the registration — **manual gate**
- Re-dispatch `v0.12.0` to confirm the bump stays a no-op (idempotency with the manual U5 commit
  in place: dispatch rewrites identical url/sha lines → `git diff --quiet` → no commit)

## Out of scope

- Dynamic `--call-tool <name>` completion from `--list-tools` (needs `--dir`/`--scripts` at
  tab time; candidate for a later follow-up)
- PowerShell completions (Windows)
- `go install` delivery of completion files (archive-only for now)
- Project site (`docs/`) updates beyond the README
- Submitting the formula to `Homebrew/homebrew-core`
- Container image changes (server-side; shell completion is irrelevant in the container)

## Verification

- `gofmt -l .` clean, `go vet ./...` + `go test ./...` green (U1/U3)
- GoReleaser dry-run archive contents = baseline + 3 scripts (U1, Decision 3)
- shellcheck / `zsh -n` / `fish -n` green; drift check green, and proven red on a synthetic
  flag (U2)
- Formula: `brew audit --formula mcp-commands` green post-U5 (U6)
- `brew test` green on a real macOS machine (macOS is the primary target; Linuxbrew as
  secondary where available in the sandbox) (U6)
- Live completion smoke test in all three shells against the installed formula (U6)

## Review Log

— awaiting Plan Reviewer
