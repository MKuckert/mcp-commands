# Hot Reloading (`--watch`) — Behavior Details

How the file watcher and registry behave in server mode and `--list-tools` live-list mode.

## Server mode

- Change events are debounced (100 ms) and then the scripts directory is
  rescanned. The registry applies a per-tool diff: tools that did not change
  are left alone, removed tools are unregistered, and added/changed tools are
  registered in place — so a `tools/list` from a client never sees a gap, and
  a no-op rescan emits no `tools/list_changed` notifications.
- Only the scripts directory (and its parent, for deletion/recreation
  recovery) is watched. A symlinked tool whose *target* lives outside the
  directory is therefore not watched directly: calling the tool always runs
  the current file, but its registered *metadata* (description, parameters,
  timeout) stays stale after an in-place target edit until the link itself
  is touched (replace or re-point the symlink — that fires a rescan) or the
  server restarts. Deliberate trade-off: watching every external target
  (and keeping recovery watches for deleted ones) added a large amount of
  complexity for a minor staleness window, so it was dropped.
- Deleting and recreating the scripts directory is recovered automatically
  (the watch re-attaches to the new directory); a permanent deletion degrades
  to the last known tool set with rescan warnings on stderr.
- One inherent inotify limit: if the scripts directory's *parent directory*
  is deleted, the parent watch dies and the parent's recreation is not
  observable — the registry then degrades to the last known tool set until
  the server restarts. Deleting and *recreating the scripts directory
  itself* is recovered automatically, since the surviving parent watch
  observes it.
- If the file watcher cannot be set up (e.g. `inotify` exhausted), the server
  fails to start with a visible error on stderr. `--watch` is an explicit
  request, so a fatal watcher failure at any later point also exits the
  process with a nonzero status (in `http` mode the server stops, too).

## Live list (`--list-tools --watch`)

- The list re-prints only when the tool set actually changed, with the screen
  cleared first only when stdout is a TTY (piped output simply accumulates) —
  an unchanged rescan stays completely silent; the process runs until `Ctrl-C`.

## Debug visibility

At `--log-level debug` the watch loop logs each file event (`path`, `op`,
`valid`), each rescan that changes the tool set (`added`/`removed`/`changed`
names), and watch reattach attempts — see [logging.md](logging.md).
