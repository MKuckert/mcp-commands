# PLAN — Shell completions shipped with the release (bash + zsh + fish)

**Branch:** `feat/shell-completions` (from `main` @ `7c4122f`, worktree `/workspace/mcp-commands-shell-completions`)
**Target version:** v0.11.1
**Repos touched:** `MKuckert/mcp-commands` (main) + `MKuckert/homebrew-tap` (formula, manual PR)
Status: Approved — Plan Reviewer, round 1 (2026-10-07); see Review Log

## Scope

- [x] U1: three static completion scripts (`completion/`) + GoReleaser `archives.files` (main repo)
- [x] U2: CI lint (shellcheck / `zsh -n` / `fish -n`) + flag-drift check (main repo)
- [x] U3: version bump 0.11.0 → 0.11.1 (main repo)
- [x] U4: README — Shell completion section (main repo)
- [ ] U5: tap formula — completion installs + test block (tap repo, **after** the v0.11.1 release)
- [ ] U6: cut v0.11.1, end-to-end verification (dispatch → formula bump → `brew audit` → install/test)

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
     exactly one dedicated block — bash: a single `_mcp_commands_flags="..."` assignment line;
     zsh: a single `_mcp_commands_flags=( ... )` array line; fish: a single
     `set -l _mcp_commands_flags ...` line (namespaced per the Copilot review, CR-1 — the
     names must not collide with user variables in the sourced shell). The drift check
     (Decision 4) extracts from that one block per script. The Builder keeps the value-class
     `case`/`-n` conditionals separate from the list block. All three blocks also carry
     `--help` (CR-2): the stdlib `flag` package accepts it even though `PrintDefaults`
     never lists it, so the drift check adds `help` to the binary-side set.
3. **Archive layout.** `.goreleaser.yaml` `archives.files` lists the three scripts
   (`completion/mcp-commands.{bash,zsh,fish}`). **Amended by Builder (see Review Log, BF-1):**
   GoReleaser v2 archives listed files with their project-root-relative path — a snapshot
   build verified the entries land as `completion/mcp-commands.*`, **not** at the root with
   their basenames (there is no basename remap in v2 `archives.files`). The archive root
   thus holds `LICENSE`, `README.md`, `mcp-commands` + a `completion/` dir with the three
   scripts — 6 files total, same count as the plan's root layout; the formula install lines
   reference the `completion/` prefix (Decision 5, likewise amended). **CRITICAL:** GoReleaser applies its default globs (`license*`, `LICENSE*`, `readme*`,
   `README*`, `changelog*`, `CHANGELOG*`) **only when `files` is unset**
   (`internal/pipe/archive/archive.go`, `len(archive.Files) == 0`). The current v0.11.0 archive
   (verified: `LICENSE`, `README.md`, `mcp-commands`) relies on those defaults, so the explicit
   `files:` list **must re-enumerate the six default globs plus the three scripts**, or the next
   release silently drops LICENSE/README from every archive (the `changelog*`/`CHANGELOG*`
   globs match nothing — there is no `CHANGELOG.md` in the repo). The v0.11.0 tarball
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
   # completion files land in the archive under completion/ (see Decision 3, BF-1)
   bash_completion.install "completion/mcp-commands.bash" => "mcp-commands"   # etc/bash_completion.d
   zsh_completion.install    "completion/mcp-commands.zsh"  => "_mcp-commands" # share/zsh/site-functions
   fish_completion.install   "completion/mcp-commands.fish"                   # share/fish/vendor_completions.d
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
   whole install). Sequence: merge main-repo PR (U1–U4, incl. version bump) → **tag v0.11.1**
   (release archives now contain the scripts; dispatch bumps the formula's URLs/shas to v0.11.1)
   → **then** push the formula completion PR (U5) → U6 verification. In this order the only
   formula state that ever ships is either "no completion lines + old archive" or "completion
   lines + archive that has the files".
9. **Docs (main README, after the Homebrew section).** Per-shell activation:
   - bash: `brew install bash-completion` (macOS stock bash 3.2) **or** `bash-completion@2`
     (+ `brew install bash` for 4+); then in `~/.bashrc`:
     `[[ -r "$(brew --prefix)/etc/profile.d/bash_completion.sh" ]] && . "$(brew --prefix)/etc/profile.d/bash_completion.sh"`
     (both v1 and `@2` source `#{prefix}/etc/bash_completion.d` — one file serves both)
   - zsh: in `~/.zshrc`: `fpath=($(brew --prefix)/share/zsh/site-functions $fpath)` **before**
     the `compinit` call, plus `autoload -Uz compinit && compinit` if the user's `~/.zshrc`
     does not already run it (CR-5: without `compinit`, stock zsh never loads completions)
     (Homebrew's own zsh picks up the fpath entry automatically)
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
  → `tar -tzf`/`unzip -l` shows exactly 6 files: the binary, `LICENSE`, `README.md` at root
  plus the 3 scripts under `completion/` (BF-1; the v0.11.0 baseline {LICENSE, README.md} +
  the 3 scripts; no `CHANGELOG.md` exists, so the changelog globs contribute nothing)

**U2 — CI lint + drift check** (main repo)
- `ci.yml` `test` job: new step after Test, **carrying `if: matrix.os == 'ubuntu-latest'`**
  (the job is a matrix; without the guard the lint also runs on `windows-latest`, which has
  no `shellcheck`/`zsh`) —
  `shellcheck completion/mcp-commands.bash` (runner-preinstalled),
  `zsh -n completion/mcp-commands.zsh` (runner-preinstalled),
  `sudo apt-get update && sudo apt-get install -y fish && fish -n completion/mcp-commands.fish`
- Drift check per Decision 4 (build → `--help` vs. the three flag blocks, dash-normalized)
- Table-test: temporarily add a flag to a script copy / the binary in the PR to prove the
  check fails, then restore

**U3 — version bump 0.11.1** (main repo)
- `Makefile` `VERSION` 0.11.0 → 0.11.1. `main.go`'s `serverVersion = "commit-local"`
  development marker **stays as-is** (deliberate local-build marker, documented in
  `main.go:13-14` and README.md:276; release versions come from the tag via the
  `.goreleaser.yaml` ldflags — bumping it would contradict that documentation and change
  what local source builds report)
- README release-download examples → `mcp-commands_0.11.1_*` names
- `go test ./...` green (tag-injection assertion in release.yml covers the rest at tag time)

**U4 — README shell completion section** (main repo, per Decision 9)

**U5 — tap formula** (tap repo, direct push to tap main **after the `bump-formula` commit has
landed on the tap** — i.e. the formula already points at the v0.11.1 archives that contain the
scripts; "v0.11.1 published" alone is not enough — per Decision 8)
- Three install lines in each stage branch + test block + `:test` deps (Decision 5/6)
- Commit message: `mcp-commands add bash/zsh/fish completion installs`
- The tap's `bump-formula` audit gate runs on the next dispatch; run `brew audit` locally as well

**U6 — release + end-to-end verification**
- Tag `v0.11.1` → release workflow green (6 files per archive: binary, LICENSE, README.md at
  root + 3 scripts under `completion/`, BF-1) → dispatch → `bump-formula` commits `mcp-commands 0.11.1`
- Push U5 → `brew audit --formula mcp-commands` clean in tap CI
- Real machine (macOS): `brew trust --formula … && brew install … && brew test mcp-commands`
  (the three sourcing checks run), then a live tab test:
  `bash -ic 'complete -p mcp-commands'` shows the registration — **manual gate**
- Re-dispatch `v0.11.1` to confirm the bump stays a no-op (idempotency with the manual U5 commit
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

### Review (round 1, 2026-10-07)

**Verified against the code — no finding:**

- **Decision 2 flag inventory:** `flags.go` `parseCLI` defines exactly 21 flags; every value
  class matches (7 bools; `--dir`/`--scripts` strings; `--api-key-file`/`--tls-cert`/`--tls-key`
  file paths; `--log-level` → `logLevels` map `debug|info|warn|error`; 8 free-form incl.
  int-typed `--port`/`--max-concurrent` and string `--timeout`). ✔
- **Decision 3 globs:** GoReleaser v2.18.2 (current stable; `release.yml` floats
  `distribution: goreleaser`) `internal/pipe/archive/archive.go:83-90` applies exactly six
  defaults (`license*`, `LICENSE*`, `readme*`, `README*`, `changelog*`, `CHANGELOG*`) iff
  `len(archive.Files) == 0` — the "six re-enumerated globs" requirement is precisely right. ✔
- **Archive baseline:** the published v0.11.0 `darwin_arm64` tarball contains exactly
  `LICENSE`, `README.md`, `mcp-commands` — matches Decision 3. ✔
- **Decision 5 formula shape:** tap `Formula/mcp-commands.rb` has top-level `url`/`sha256`
  (darwin_arm64) + `resource "darwin_amd64"` + `resource "linux_amd64"` + one `stage` branch
  each carrying `bin.install` — the three-branch structure the U5 diff assumes. ✔
- **Decision 7 awk pass-through:** `bump-formula.yml`'s awk rewrites only `url`/
  `sha256` lines (plus seed `version`/comment deletion); every proposed new line
  (`*_completion.install`, `system` test lines, `:test` deps) falls to `{ print }`. ✔
- **U6 idempotency:** the workflow checks `git diff --quiet` *before* the audit and exits 0
  with no commit — a re-dispatch over the manual U5 commit is a no-op. ✔
- **U2 feasibility:** `ci.yml` `test` is a `[ubuntu-latest, windows-latest]` matrix; `shellcheck`
  and `zsh` are preinstalled on `ubuntu-latest`, `fish` is not (plan apt-installs it). ✔
  (Guard requirement → N-1.)
- **Decision 6 shells:** a `#compdef` file sources cleanly in plain `zsh -c` (`compdef`/
  `compadd` are zsh builtins — no completion framework); bash `complete` and fish `complete`
  are builtins available in non-interactive `-c` invocations; brew `:test` deps land on the
  test `PATH`. ✔
- **Naming collisions:** `mcp-commands.{bash,zsh,fish}` at the archive root collide with
  nothing (binary is `mcp-commands` / `mcp-commands.exe`). ✔
- **Decision 8 ordering:** the only formula states ever shipped are "v0.11.0 URLs + no
  completion lines" and "v0.11.1 URLs + completion lines"; no state references files the
  archive lacks — *provided* U5 waits for the bump commit (wording gap → N-2).

**Findings:**

R-1 (blocking, **corrected in this commit**): **Phantom `CHANGELOG.md`.** U1's verification
claimed the archive shows "… LICENSE + README.md + CHANGELOG.md at root" and U6 claimed
"9 files per archive: binary, 3 scripts, LICENSE, README, CHANGELOG" — but there is **no
`CHANGELOG.md` in the repo** (no `CHANGELOG*` file at the root; the v0.11.0 tarball
contains exactly 3 entries), so the `changelog*`/`CHANGELOG*` globs match nothing and the
v0.11.1 archive will contain **6** files. The "9" also contradicted its own six-item
list. U1/U6 now assert the true 6-file set.

R-2 (blocking, **corrected in this commit**): **U3 targeted the wrong constant.** It said
"`main.go` `serverVersion` → `0.11.1`", but `main.go:15` is
`serverVersion = "commit-local"` (not `0.11.0` — `0.11.0` exists only in `Makefile:4`
`VERSION ?= 0.11.0`), and the `commit-local` dev marker is *documented behavior*
(`main.go:13-14` comment, README.md:276). Bumping it would contradict the docs and change
what local source builds report. U3 now bumps only the Makefile.

N-1 (minor, **corrected in this commit**): **U2 lint steps need an explicit matrix guard.**
"new step after Test" in a matrix job runs on both runners; `windows-latest` has no
`shellcheck`/`zsh`. U2 now carries `if: matrix.os == 'ubuntu-latest'` on the new step, so
the Windows job stays untouched.

N-2 (minor, **corrected in this commit**): **U5's gate was one step too early.** "after
v0.11.1 is published" is not the right gate: in the window between the release publishing
and the `bump-formula` commit landing, the tap formula still points at the **v0.11.0**
archives (3 files, no scripts) — a completion-lines push there would make
`bash_completion.install` fail the whole `brew install` on a missing staged file. U5 now
says "after the `bump-formula` commit has landed on the tap", matching U6's sequence.

**Verdict: Approved** — all four findings (R-1, R-2, N-1, N-2) are corrected in this
commit; every other concrete claim (flag inventory, GoReleaser glob semantics, formula
structure, awk pass-through, idempotency, CI availability, shell-sourcing feasibility,
ordering window) was verified against the code and the live tap/release artifacts and
holds. No residual contradictions remain; the Builder may start at U1.

### Amendment (2026-10-07)

Target version changed v0.12.0 → v0.11.1 per user request (patch release); all version
references in the plan updated. Scope, design decisions, and review findings are otherwise
unchanged — the round-1 verdict carries over.

### Builder finding BF-1 (2026-10-07, U1)

Decision 3 claimed `files` entries "land at the archive root with their basenames". GoReleaser
v2 does not remap paths: the U1 snapshot build shows the scripts archived as
`completion/mcp-commands.{bash,zsh,fish}` (relative path preserved), so the archive root
holds the binary + LICENSE + README.md plus a `completion/` directory — still 6 files total.
Decision 3 and the Decision 5 formula install lines were amended in-place to reference the
`completion/` prefix; the 6-file archive-count assertion is unchanged. No other consequence:
brew `*_completion.install` accepts staging-relative paths, and the U5/U6 gates are unaffected.

### Review (round 2, U1)

**Verified against the code — no finding:**

- **Flag inventory (Decision 2):** extracted the flag block from each script and diffed against
  the 21 flags in `flags.go` `parseCLI` — all three lists are an exact 21/21 match (no missing,
  extra, or misspelled flags). One dedicated parseable block per script (`flags="…"`,
  `flags=( … )`, `set -l flags …`); value-class conditionals kept separate. ✔
- **bash 3.2 floor:** no bash-4+ features (no `mapfile`, associative arrays, `${var,,}`,
  extglob); the only construct of interest is process substitution (bash 2+) around
  `compgen`; `shellcheck` exits clean. ✔
- **Sourcing in plain shells:** all three source cleanly in `zsh -c` / `bash -c` / `fish -c`;
  the zsh `compdef` guard (`$+functions[compdef] || $+builtins[compdef]`) correctly skips
  registration in a plain `zsh -c`, so Decision 6's test-block sourcing holds. ✔
- **fish `-a`/argv pattern:** `__mcp_commands_flag_candidates` receives the flag list as
  `argv` and prints one per line via `printf '%s
'` — correct; avoids `complete` misparsing
  dash-prefixed positionals as its own options. `__fish_*` conditionals are stock fish.
  Function name is namespaced, no collision. ✔
- **zsh/guarded registration, bash `complete -F`:** names `_mcp-commands` / `_mcp-commands`
  collide with nothing in scope. ✔
- **`.goreleaser.yaml`:** `files:` = exactly the six default globs + the three scripts, with an
  accurate comment explaining why the re-enumeration is required. ✔
- **BF-1 amendment:** sound. GoReleaser v2 `archives.files` preserves project-root-relative
  paths (no basename remap); the `completion/` prefix in the amended Decision 3 and the
  Decision 5 install lines is consistent with brew's staging-relative `*_completion.install`,
  and the 6-file archive count is unchanged. Accept as written. ✔

**Findings:**

B-2 (blocking): **zsh value completion is dead code.** `completion/mcp-commands.zsh:21`
sets `prev="${words[1]}"` — in a zsh completion function `words[1]` is the **first** word of
the command line (the command name `mcp-commands`), not the previous word. So every
`case "$prev"` branch (`--log-level`, `--dir|--scripts`, `--api-key-file|--tls-cert|--tls-key`)
is unreachable: values never complete; only the bare `-` flag list works. This defeats the
value-class half of Decision 2's contract in zsh. Fix is one line: `prev="${words[CURRENT-1]}"`
(or use the `$1` argument the completion system passes).

N-3 (minor): **fish value entries complete nothing, and the comment says the opposite.** The
two entries `… --dir --scripts … -f -d 'Directory'` and `… --api-key-file --tls-cert
--tls-key … -f -d 'File'` use `-f` (no file completion) with no `-a`/`-F` candidates, so
tabbing after `--dir`/`--api-key-file` yields zero candidates — while the comment above them
claims "the directory flags complete directories, the file flags complete files". The comment
is the inverse of the behavior. Fix: drop `-f` from those two entries (fish then falls back
to file/dir completion) and correct the comment, or add an `-F` candidate function. Non-
blocking: safe behavior, no contract violation, pure QoL.

N-4 (minor): **zsh flag-list completion only fires on a bare `-`.** `[[ "${words[CURRENT]}"
== - ]]` matches only the literal dash; `--d<Tab>` completes nothing (the bash script handles
any `-*` prefix). One-character class of fix (`== -*`). Non-blocking QoL; also note neither
bash nor zsh handles the `--flag=value` equals form — accepted as a known limitation, not
required by the plan.

(Trivia, no action: the zsh file assigns `flags`/`log_levels` at global scope when autoloaded;
harmless but mildly generic names.

**Verdict: Changes requested** — one blocking finding (B-2: zsh `prev` reads the command
name, all value branches dead); two minor QoL findings (N-3 fish value entries + inverted
comment; N-4 zsh bare-dash-only prefix match). The Builder should fix B-2 (and, while in the
file, N-4) and resubmit; N-3 may ship as-is if explicitly accepted. U1 stays unticked.

### Review (round 3, U1 correction)

Correction `e2233f2` ("fix: zsh prev word index, dead value branches, fish value entries")
verified against the diff and the files:

- **B-2 — fixed.** `prev="${words[CURRENT-1]}"` is the canonical zsh completion idiom:
  `CURRENT` is the 1-based index of the word being completed in `words`, so
  `words[CURRENT-1]` is the previous word. `CURRENT` is always ≥ 2 inside a completion
  function (word 1 is the command name and never triggers the function), so there is no
  `words[0]` edge case. All three value branches (`--log-level`, `--dir|--scripts`,
  `--api-key-file|--tls-cert|--tls-key`) are now reachable. ✔
- **N-3 — resolved.** Both value entries dropped `-f` and keep `-d` documentation only. A
  matching fish spec without `-f` leaves file completion enabled, and with no `-a`/`-F`
  actions the candidate list stays empty, so the engine falls back to its default
  file+directory completion — `--dir`/`--api-key-file` now complete paths. The rewritten
  comment ("keep fish's default file+directory completion (the -d entries document the
  value kind)") is accurate. The pragmatic fallback is accepted: dirs-only completion in
  fish was never in the plan's contract, and default file+dir is the standard fish behavior
  for path flags; the `-x`/`-F` restriction the Builder cites need not be re-verified
  because the shipped code does not rely on it. ✔
- **N-4 — fixed.** `[[ "${words[CURRENT]}" == -* ]]` matches any dash-prefixed partial
  word, and `compadd` prefix-filters its candidates against the current word by default,
  so `--d<Tab>` yields `--dir` and `--disable-localhost-protection` only. ✔
- **No new bugs.** The diff is 6 lines across the two files; the zsh `case` structure (single
  fall-through `return 0`), the fish condition shapes, the 21/21 flag inventories, and the
  `#compdef`/guard registration are all untouched. `completion/mcp-commands.bash`,
  `.goreleaser.yaml`, and `flags.go` are unchanged since `6058d7f` (round-2 accepted). ✔

**Verdict: Approved** — all three round-2 findings (B-2, N-3, N-4) are resolved with no
residual findings; U1 is ticked.

### Review (round 4, U2)

`541817e` ("ci: lint shell completion scripts and check flag drift") verified against
`.github/workflows/ci.yml`, the three `completion/` scripts, `flags.go`, and `main.go`:

**Plan compliance — no finding:**

- **Lint step:** sits after Test, guarded `if: matrix.os == 'ubuntu-latest'`; runs
  `shellcheck` (runner-preinstalled) + `zsh -n` (runner-preinstalled) +
  `sudo apt-get update && sudo apt-get install -y fish && fish -n` (fish is not
  preinstalled) — exactly U2 as amended by N-1; the Windows job steps are untouched. ✔
- **Drift check:** matches Decision 4 — build → `--help` vs. the three flag blocks,
  dash-normalized, fail on any divergence; per-script as the plan requires. The U2
  table-test (synthetic flag → red, then restore) was run locally by the Builder and is
  correctly **not** in the commit (the diff touches only `ci.yml` + the plan). ✔

**Correctness of the drift-check shell logic — no finding:**

- **`--help` contract:** `main.go:44-47` — `flag.ErrHelp` prints the captured usage to
  **stdout** and exits **0**, so `help_flags=$(dist/mcp-commands --help | awk …)` is
  well-defined under `set -e`/`pipefail`. ✔
- **awk extraction:** stdlib `flag` `PrintDefaults` renders each flag as `  -name [type]`
  and each description line as `    \t…` (4 spaces + tab) — the third character is a
  space, never `-`, so `/^  -/` matches exactly the 21 flag lines and nothing else (not
  the `Usage of` header, not descriptions, not wrapped usage lines); `print $1` yields
  the `-name` token, `sed 's/^-//'` strips the single dash, `sort` canonicalizes the
  registration order. ✔
- **Per-script normalizations:** all three flag blocks are single anchored lines that the
  respective `grep -E` patterns match uniquely (the zsh `log_levels=(` line does not
  match `^flags=\(`; the fish file has no other `set -l` lines). `sed` prefix/suffix
  strips (`flags="`/`"`, `flags=( `/` )`, `set -l flags `) leave the pure space-separated
  list; `normalize` (`tr ' ' '\n'` + `sed 's/^--*//'` — one or more leading dashes — +
  `grep -v '^$'` + `sort -u`) handles double vs. single dashes, blank tokens, and
  ordering. Both sides of the `drift()` string compare are sorted, so the comparison is
  valid. ✔
- **`set -euo pipefail` semantics:** a missing flag block → `grep` empty → `normalize`'s
  `grep -v` exits 1 with no output → the substitution's status is discarded in argument
  context, so `drift()` receives `""` ≠ the 21-flag set → drift message + `exit 1`.
  A block reformatted across lines is captured only partially → set mismatch → red. Both
  failure modes fail loudly, never fake a match. `diff <(…) <(…) >&2 || true` correctly
  prevents `set -e` from aborting on diff's exit-1 before the intended `exit 1`; process
  substitution is fine because Actions `run:` uses bash. The lint step relies on the
  runner's default `set -eo pipefail` (an `apt` failure aborts) — acceptable. ✔
- **YAML:** both steps are `run: |` literal blocks; the `--` sequences inside sed/grep
  patterns are inert in a block scalar, quoting is sound; the Builder's YAML-parse
  check plus this reading is sufficient. ✔
- **Builder validation:** green baseline, red on a synthetic flag injected into each of
  the three scripts, green after restore — combined with the line-by-line reading above,
  the evidence is sufficient. (Trivial, no action: the drift check's `go build -o
  dist/mcp-commands .` creates a `dist/` dir in the CI workspace; harmless.)

**Verdict: Approved** — no residual findings; U2 is ticked.

### Review (round 5, U3)

`74d04a5` ("chore: bump version to 0.11.1") verified against the diff, `main.go`,
`README.md`, and `Containerfile`:

- **Makefile:** `VERSION ?= 0.11.0` → `VERSION ?= 0.11.1` (`Makefile:4`) — exactly U3's
  first item. `LDFLAGS` already injects `main.serverVersion=$(VERSION)`, so local
  `make build`s will report 0.11.1. ✔
- **README:** the release-download example at line ~46 now reads
  `tar -xzf mcp-commands_0.11.1_linux_amd64.tar.gz` — the only 0.11.0 asset name in the
  README; no other versioned asset names exist to update. ✔
- **main.go untouched (R-2):** the diff touches only `Makefile`, `README.md`, and the plan.
  `serverVersion = "commit-local"` and its dev-marker comment (`main.go:12-15`) are
  unchanged, and the README's Version section (line ~276) still documents `commit-local`
  for non-injected source builds — the comment, code, and README remain mutually
  consistent with the plan's R-2 rationale (release versions come from the goreleaser
  tag ldflags). ✔
- **Completeness:** a repo-wide grep for `0.11.0` over tracked files finds only
  (a) `Containerfile` (3 hits: a comment example, the build-arg example, and the
  `ARG MCP_COMMANDS_VERSION=0.11.0` default) and (b) the three historical `plans/*.md`
  documents. All are outside U3's declared scope (Makefile, README). ✔
- **Containerfile non-bump — acceptable.** The `MCP_COMMANDS_VERSION` ARG is a
  *build-time default for pulling a prebuilt release into the image*, not the project's
  own version constant; the plan's out-of-scope section excludes container changes, and
  `git log -- Containerfile` shows two commits, neither a per-release bump — the file has
  never been version-bumped per release, so leaving it is consistent with established
  project practice, not an oversight. (Trivia, no action: the comment example on line 9
  will slowly age as releases ship; a "e.g." phrasing makes that harmless.)
- **Tests:** Builder reports `go test ./...` green on this commit; the diff is two
  one-line value changes plus the plan, so no test can be affected — the claim is
  consistent with the diff.

**Verdict: Approved** — no residual findings; U3 is ticked.

### Review (round 6, U4)

`2255712` ("docs: README shell completion section") verified against the README diff,
Decision 9, the BF-1 amendment, and the three `completion/` script headers:

- **Placement (check 1) — satisfies the intent.** Decision 9 says "main README, after the
  Homebrew section"; the Builder placed a `### Shell completion` subsection at the **end**
  of `## Installation` (after the container paragraph, before `## Usage`). It is literally
  after the Homebrew section, and the alternative reading (insert *between* the Homebrew
  block and the Go block) would have split the Homebrew/Go/release/container install-method
  list — strictly worse. Placing it as the section's closing subsection keeps the method
  list intact, and the section opens by anchoring to the formula ("The Homebrew formula
  installs…") plus a non-brew paragraph, so both audiences are served from the
  Installation area. ✔
- **Content coverage (check 2) — all six Decision 9 items present:** brew `bash-completion`
  (macOS stock bash 3.2) **or** `bash-completion@2` + `brew install bash`; the
  `~/.bashrc` profile.d source line; the "both v1 and `@2` source `etc/bash_completion.d`"
  note; the `~/.zshrc` fpath line + Homebrew-zsh-automatically note; fish automatic +
  vendor-dir note; the non-brew paragraph (release archives ship the scripts, drop them in
  your shell's completion dir; `go install` carries none); the Linuxbrew
  `/usr/share/bash-completion/completions` note. Nothing from Decision 9 is missing. ✔
- **Technical accuracy (check 3) — README and script headers agree on every path:**
  `$(brew --prefix)/etc/profile.d/bash_completion.sh` (the canonical Homebrew
  bash-completion v1/@2 profile script) ✔; `$(brew --prefix)/etc/bash_completion.d` +
  `mcp-commands` ↔ `completion/mcp-commands.bash:4` ✔; `share/zsh/site-functions` +
  `_mcp-commands` rename ↔ `completion/mcp-commands.zsh:7` (and the `#compdef` autoload
  naming) ✔; `share/fish/vendor_completions.d` + `mcp-commands.fish` ↔
  `completion/mcp-commands.fish:4` ✔; the Linuxbrew `/usr/share/bash-completion/completions`
  path ↔ the same path in the bash header ✔. The unquoted `fpath=($(brew --prefix) …)`
  form is the Homebrew-documented recipe (fine for space-free default prefixes) and is the
  exact line Decision 9 prescribed. (Trivia, no action: the zsh snippet is fenced
  ```bash``` — cosmetic only.)
- **BF-1 consistency (check 4) — no contradiction.** The non-brew paragraph names the three
  scripts by **basename** ("`mcp-commands.bash`, `mcp-commands.zsh`, `mcp-commands.fish`")
  and gives no archive location, so it is compatible with the BF-1 `completion/` prefix
  (the `.goreleaser.yaml` `files:` entries confirm the scripts archive as
  `completion/mcp-commands.*`). No statement in the section claims a root location. ✔
- **Forward-looking formula claim — accepted, non-blocking.** "The Homebrew formula installs
  completion scripts…" is only true once U5 lands on the tap; in the window between the
  v0.11.1 release and the U5 push, the formula (now bumped to v0.11.1) ships the new README
  without the `*_completion.install` lines. This is an inherent consequence of the plan's
  approved U4-before-release / U5-after-bump ordering (Decisions 8/9) and a documentation
  inaccuracy, not an install failure — the formula remains fully functional. U5 is the
  immediately next step, so the window is minimal. Not a finding. (Trivia, no action:
  mentioning the `completion/` subdirectory in the non-brew paragraph would be marginally
  more helpful for `tar`/`unzip` users; basenames alone are unambiguous in practice.)

**Verdict: Approved** — all four checks pass with no residual findings; U4 is ticked.

### CI-1 (2026-10-07, post-push): ubuntu runner image has no zsh

The round-1 claim that `shellcheck` and `zsh` are preinstalled on `ubuntu-latest` was wrong
for the current image — the lint step failed on `zsh: command not found`. U2's lint step now
apt-installs `fish shellcheck zsh` up front, removing all image-content assumptions. U2's
verification criterion ("shellcheck / `zsh -n` / `fish -n` green") is unchanged; the
round-4 approval carries over to this correction (no drift-check impact).

### Copilot review (2026-10-08, PR #32) — 7 accepted, 1 objected

**Accepted and fixed** (one commit):
- **CR-1** bash/zsh: the generic global names `flags`/`log_levels` are sourced into the user's
  interactive shell and could clobber same-named user variables → namespaced to
  `_mcp_commands_flags`/`_mcp_commands_log_levels` in both scripts (fish's `set -l` renamed
  for consistency). The drift-check `grep`/`sed` anchors follow the renamed blocks (Decision 2
  amended).
- **CR-2** `--help` gap: the stdlib `flag` package accepts `--help`/`-h` (main.go handles
  `flag.ErrHelp`) but `PrintDefaults` never lists it, so none of the three scripts completed a
  supported option → `--help` added to all three flag blocks; the drift check adds `help` to
  the normalized binary set to keep the contract consistent.
- **CR-3** bash: path candidates now go through `complete -F … -o filenames` so readline
  applies filename quoting / directory-suffix handling (3.2-compatible).
- **CR-4** fish: `__fish_use_subcommand`/`__fish_seen_subcommand_from` are subcommand-oriented
  and misbehave on a flag-only CLI (flag candidates vanish after a positional; value
  completion sticks to every later token) → replaced with two namespaced condition functions
  (later re-based onto `commandline` per CR-7).
- **CR-7** (Copilot round 2, 2026-10-08) fish: the CR-4 condition functions read
  `$commandline_tokens`, which **does not exist in fish 3.6** (our floor; zero mentions in the
  3.6.0 language docs — the variable is newer), so the conditions silently never fired. fish's
  own bundled completions use the `commandline` builtin in conditions → both functions now use
  `commandline -ct` (current token, for the flag test) and `commandline -poc` (completed tokens
  before the cursor, last one = the preceding token). Re-verified via `fish 3.6.0 -c 'complete
  -C …'` (which *does* populate the transient `commandline`): `--` → all flags; `--watch --d` →
  filtered flags (no longer swallowed by a positional); `--dir ␣`/`--api-key-file ␣` →
  default file+dir; `--log-level d` → `debug` only; empty token → no flags, files only. The
  `complete -C` *variable* gap (fish issue #11993, fixed in 4.3) never affected this design.
  The earlier simulation-based verification (round 1) is superseded by this direct test.
- **CR-5** README zsh: extending `fpath` alone does not activate completions → the snippet now
  includes `autoload -Uz compinit && compinit` (with the "only if not already run" and
  ordering notes); Decision 9 amended.
- **CR-6** plan U1 verification still claimed all six files at the archive root, contradicting
  BF-1 → reworded to the actual layout (3 at root + `completion/` dir); U6 likewise.

**Objected (no change):** the zsh comment claiming the autoloaded body "never executes the
`case`/`compadd` logic, so the first Tab produces no candidates" is a misreading of zsh
autoload semantics. An autoload stub loads the file *and then calls the newly defined
function* — the `#compdef` + function-definition pattern is exactly how every stock zsh
completion file works; the first Tab invokes the body. Demonstrated in-sandbox: with the file
installed as `_mcp-commands` on `fpath`, `autoload -U _mcp_commands; _mcp_commands` executes
the body (the `compadd` call fires). No code change warranted.

All 8 threads were answered on the PR (7 accepted + the objection above).

**Copilot round 2 (2026-10-08).** One new comment (fish:13, the `commandline_tokens`
claim) — accepted and fixed as CR-7; the two other new comments concerned PR metadata / the
plan file and are out of Builder scope per the user. PR metadata comment 4216606659 and
plan comment 4216606661 were left for the user. The junk-reply caveat above still stands. Note: two earlier
reply attempts posted literal `@…` placeholder bodies (the app token's `gh -f body=@file`
expansion misbehaved in this shell); the token can create but not delete PR review comments
(DELETE → 404), so the 16 junk replies still sit in the threads alongside the 8 correct ones
— the user should delete them from the web UI (they are easily spotted: bodies reading
`@[4215…]` or `@b_*.txt`).