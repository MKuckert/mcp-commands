# PLAN — U1: Discovery file filter (REVIEW_REPORT.md findings H1, M3 + H5 predicate)

**Branch:** `fix/u1-discovery-file-filter` (from `main` @ 479ab81)
**Target release:** v0.9.1
**Source of truth:** `REVIEW_REPORT.md` §U1, §H1, §M3, §H5 (predicate leg only — the Windows CI leg belongs to U5).
Status: **Implemented, pending Code Reviewer**

## Scope

Files: `discover.go`, `discover_test.go`, `main.go` + `Makefile` (version bump only).

- [/] H1: regular-file predicate — require `fileInfo.Mode().IsRegular()` before the exec-bit check and before `os.Open`; an executable FIFO (or symlink-to-FIFO) must be skipped, never opened.
- [/] H5 predicate: OS-aware executable detection — Unix permission bits; on Windows, executable extensions (`.exe`, `.com`, `.bat`, `.cmd`) since normal file modes never set `0111`.
- [/] M3a: scanner `Err()` handling in `extractFrontmatter` — an oversized (>64 KiB) frontmatter line must log a stderr warning naming the file; the tool registers with the metadata collected so far (disclosed, never silent).
- [/] M3b: 30-line boundary — read exactly `scanHeaderLines` lines; the current `for scanner.Scan() && lineCount < n` calls `Scan()` first and consumes line 31.
- [/] Version bump `serverVersion`/`VERSION` → `0.9.1`.
- [/] Tests: FIFO (skipped), symlink-to-FIFO (skipped, bounded by a test deadline), >64 KiB frontmatter line (warning + partial metadata), line-30-included/line-31-excluded boundary, predicate table (regular/non-regular, exec-bit, Windows extensions).

## Design decisions

1. Single predicate `isToolFile(fileInfo, name)` in `discover.go`: regular-file check first (FIFO/device/symlink-to-FIFO all fail it and are skipped before any open), then OS-specific: Unix `mode&0111`, Windows extension set. Kept in `discover.go` to honor U1's file scope; pure `isWindowsExecutable(name)` split out for cross-OS testability.
2. `extractFrontmatter` loop restructured to `for lineCount < scanHeaderLines { if !scanner.Scan() { break }; lineCount++ }` — reads at most 30 lines; after the loop, `scanner.Err()` is checked and reported as a stderr warning (incomplete frontmatter) — never silently registered.
3. Windows extension set is intentionally narrow (`.exe`, `.com`, `.bat`, `.cmd`): what `os/exec` runs directly or via `cmd.exe`; script languages (`.ps1`, `.js`) do not self-execute on Windows and stay out of scope.
4. The pre-existing symlink-to-directory guard (`fileInfo.IsDir()`) is subsumed by `IsRegular()` (a directory is non-regular) — one check replaces both.
5. `-race` cannot run in this sandbox (ThreadSanitizer VMA); no new shared state is introduced, so no new race surface.

## Verification

- `gofmt -l .` clean; `go vet ./...`; `go test ./... -count=1` green.
- FIFO regression proven end-to-end: built binary + executable FIFO + `--list-tools` returns promptly (previously exited 124 under `timeout 3`).

## Out of scope (other units)

U2 (input contract), U3 (watch lifecycle), U4 (config/HTTP hardening), U5 (CI/docs, incl. the Windows CI leg of H5).

## Review Log

(empty — Code Reviewer appends)
