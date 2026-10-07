# PLAN — U1: Discovery file filter (REVIEW_REPORT.md (removed after merge) findings H1, M3 + H5 predicate)

**Branch:** `fix/u1-discovery-file-filter` (from `main` @ 479ab81)
**Target release:** v0.9.1
**Source of truth:** `REVIEW_REPORT.md` (removed after merge) §U1, §H1, §M3, §H5 (predicate leg only — the Windows CI leg belongs to U5).
Status: **Done** — Code Reviewer approved 2026-10-04; Copilot PR review feedback addressed 2026-10-04 (see Review Log).

## Scope

Files: `discover.go`, `discover_test.go`, `main.go` + `Makefile` (version bump only).

- [x] H1: regular-file predicate — require `fileInfo.Mode().IsRegular()` before the exec-bit check and before `os.Open`; an executable FIFO (or symlink-to-FIFO) must be skipped, never opened.
- [x] H5 predicate: OS-aware executable detection — Unix permission bits; on Windows, executable extensions (`.exe`, `.com`) since normal file modes never set `0111`. Checked against the **resolved target** name, so symlinks are classified by the file that will actually be executed.
- [x] M3a: scanner `Err()` handling in `extractFrontmatter` — an oversized (>64 KiB) frontmatter line must log a stderr warning naming the file; the tool registers with the metadata collected so far (disclosed, never silent).
- [x] M3b: 30-line boundary — read exactly `scanHeaderLines` lines; the current `for scanner.Scan() && lineCount < n` calls `Scan()` first and consumes line 31.
- [x] Version bump `serverVersion`/`VERSION` → `0.9.1`.
- [x] Tests: FIFO (skipped), symlink-to-FIFO (skipped, bounded by a test deadline), >64 KiB frontmatter line (warning + partial metadata), line-30-included/line-31-excluded boundary, predicate table (regular/non-regular, exec-bit, Windows extensions).

## Design decisions

1. Single predicate `isToolFile(fileInfo, name)` in `discover.go`: regular-file check first (FIFO/device/symlink-to-FIFO all fail it and are skipped before any open), then OS-specific: Unix `mode&0111`, Windows extension set. Kept in `discover.go` to honor U1's file scope; pure `isWindowsExecutable(name)` split out for cross-OS testability.
2. `extractFrontmatter` loop restructured to `for lineCount < scanHeaderLines { if !scanner.Scan() { break }; lineCount++ }` — reads at most 30 lines; after the loop, `scanner.Err()` is checked and reported as a stderr warning (incomplete frontmatter) — never silently registered.
3. Windows extension set is intentionally narrow (`.exe`, `.com` only): PE binaries are what the exec path starts directly via `CreateProcess`. `.bat`/`.cmd` were dropped after Copilot review — the exec path passes the path straight to `exec.CommandContext` with no `cmd.exe /C` wrapper, so registering them would advertise tools whose calls fail or mangle arguments (a loud-fail violation); a quoted `cmd.exe` wrapper belongs to a future exec-path unit, not U1. Script languages (`.ps1`, `.js`) do not self-execute there. The extension is checked on the resolved target (`fileInfo.Name()`), not the link name: `alias.exe -> notes.txt` must be skipped, `alias -> tool.exe` must register.
4. The pre-existing symlink-to-directory guard (`fileInfo.IsDir()`) is subsumed by `IsRegular()` (a directory is non-regular) — one check replaces both.
5. `-race` cannot run in this sandbox (ThreadSanitizer VMA); no new shared state is introduced, so no new race surface.

## Verification

- `gofmt -l .` clean; `go vet ./...`; `go test ./... -count=1` green.
- FIFO regression proven end-to-end: built binary + executable FIFO + `--list-tools` returns promptly (previously exited 124 under `timeout 3`).

## Out of scope (other units)

U2 (input contract), U3 (watch lifecycle), U4 (config/HTTP hardening), U5 (CI/docs, incl. the Windows CI leg of H5).

## Review Log

**Code Reviewer — 2026-10-04: Approved, no blocking findings.** All plan tasks verified implemented and closed (H1, M3a/b, H5 predicate, version 0.9.1); `go test -count=1` / `go vet` / `gofmt` green, `GOOS=windows go vet` clean, new tests stable over 3 runs; end-to-end FIFO proof re-executed (12 ms, exit 0, pre-fix 124 gone); regression check: Unix predicate extensionally identical for regular executable files.

- N-1 (nit, fixed): `discover.go` comment "exceeding" → "at or over" the 64 KiB scanner buffer (`bufio.Scanner` rejects `>=` max token size).
- N-2 (forward pointer, U5): `README.md:46` install example pins the `0.9.0` archive name — move to `0.9.1` in the release PR.

**Copilot PR review — 2026-10-04: 3 findings, all addressed.**

- C-1 (medium, fixed): the Windows extension check used the directory-entry (link) name while the resolved target is what executes — `alias.exe -> notes.txt` would register, `alias -> tool.exe` would be skipped. Fixed: `isToolFile` takes only the resolved `fileInfo` and checks the target name.
- C-2 (medium, fixed): `.bat`/`.cmd` dropped from `windowsExecutableExtensions` (exec path has no `cmd.exe` wrapper; registering unlaunchable tools violates fail-loud). Decision 3 updated.
- C-3 (low, fixed): header status reconciled with the review log / final state.

Status: **Done** — all tasks landed; plan boxes ticked by reviewer approval.
