package main

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

const watchDebounceDelay = 100 * time.Millisecond

// prodNewWatcher creates the real fsnotify watcher. Tests inject a failing
// one through liveEnv to exercise the setup-failure path deterministically.
func prodNewWatcher() (*fsnotify.Watcher, error) { return fsnotify.NewWatcher() }

// prodWatcherErrors is the watcher-error source for watchChanges. Production
// returns the watcher's own channel; tests inject a synthetic one (a real
// fsnotify error is not deterministically reproducible — chmod on an already-
// watched inode does not produce one).
func prodWatcherErrors(w *fsnotify.Watcher) <-chan error { return w.Errors }

// watchPath is one watched path plus the parent directory whose events
// observe the path's deletion and recreation. parent is "" when the path has
// no distinct parent (it is the filesystem root) — the path watch alone then
// receives every relevant event. dirScope is set for the scripts directory
// itself: it is a directory whose *children's* events are relevant (a script
// file write arrives named by the child, not by the directory), while a plain
// file path matches events named with itself. passive marks the parent
// observer entries: they exist only so the kernel keeps reporting events on
// the primary path after its inode is replaced — sibling activity in a
// parent directory must never arm the debounce.
type watchPath struct {
	path     string
	parent   string
	dirScope bool
	passive  bool
}

func watchPathFor(path string) watchPath {
	parent := filepath.Dir(path)
	if parent == path {
		parent = ""
	}
	return watchPath{path: path, parent: parent}
}

// watchSpecEvents is the operation mask that makes an event file-relevant:
// create, write, delete, rename (on linux, moves arrive as Rename).
var watchSpecEvents = fsnotify.Create | fsnotify.Write | fsnotify.Remove | fsnotify.Rename

// watchChanges watches scriptsDir and — after the watcher is ready —
// performs one guaranteed rescan, then re-runs onRescan once per debounced
// burst of file events.
//
// onRescan performs the rescan (discover + apply). Only the scripts
// directory (and its parent, for replacement recovery) is watched: symlinked
// tools whose *targets* live outside the directory are intentionally not
// watched (the complexity was not justified by the minor finding they
// addressed) — editing such a target refreshes the tool's *execution* (the
// file is re-read on every call) but leaves its registered *metadata* stale
// until the link itself is touched (which fires a rescan) or the server
// restarts.
//
// Setup failures (watcher creation, scripts-directory Add) are returned as
// errors — the caller decides the process-level consequence.
//
// Replacement recovery: every watched path's parent is watched, and an
// event *on* a watched path that removes, renames, or recreates it triggers
// a best-effort re-Add of the path (with a warning on failure, never fatal
// at runtime) plus a rescan. Deleting and recreating the scripts directory
// therefore restores watching of the new inode; a permanent deletion degrades
// to the last known tool set with repeated rescan warnings. Transient
// fsnotify error-channel entries are logged and swallowed. It returns when
// ctx is done (context.Canceled) or a channel closes (error).
func watchChanges(ctx context.Context, env liveEnv, scriptsDir string, onRescan func()) error {
	watcher, err := env.newWatcher()
	if err != nil {
		return fmt.Errorf("failed to create watcher: %w", err)
	}
	defer watcher.Close()

	scriptsWP := watchPathFor(scriptsDir)
	scriptsWP.dirScope = true
	if err := watcher.Add(scriptsWP.path); err != nil {
		return fmt.Errorf("failed to watch %s: %w", scriptsDir, err)
	}
	paths := []watchPath{scriptsWP}
	if scriptsWP.parent != "" {
		if err := watcher.Add(scriptsWP.parent); err != nil {
			return fmt.Errorf("failed to watch %s: %w", scriptsWP.parent, err)
		}
		p := watchPathFor(scriptsWP.parent)
		p.passive = true
		paths = append(paths, p)
	}

	// Guaranteed rescan after watch readiness: a change landing between the
	// caller's initial scan and watcher.Add can no longer be lost — this
	// scan runs after Add and re-applies it (no-op when nothing changed).
	onRescan()

	debounceTimer := time.NewTimer(watchDebounceDelay)
	// NewTimer arms the clock immediately; disarm it right away so onChange
	// only fires after a file event Resets the timer. (Go >= 1.23 Stop drains
	// the channel, so no stale tick can be in flight.)
	debounceTimer.Stop()
	defer debounceTimer.Stop()
	debounceActive := false

	// reattach re-establishes a watch on a path whose inode was replaced
	// (deleted, moved, recreated). Best effort: the path may not exist yet;
	// the next event retries. Never fatal at runtime. A successful reattach
	// arms the debounce immediately: events that land between the recreation
	// and the re-Add are lost to inotify, so a rescan is warranted to pick
	// up whatever appeared in the replacement.
	reattach := func(p string) {
		env.log.Debug("reattaching watch", "path", p)
		if err := watcher.Add(p); err != nil {
			env.log.Warn("failed to reattach watch", "path", p, "error", err)
			return
		}
		if !debounceActive {
			debounceActive = true
			debounceTimer.Reset(watchDebounceDelay)
		}
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case event, ok := <-watcher.Events:
			if !ok {
				return fmt.Errorf("watcher channel closed unexpectedly")
			}
			for i := range paths {
				if paths[i].passive {
					continue
				}
				matched := event.Name == paths[i].path
				if !matched && paths[i].dirScope && filepath.Dir(event.Name) == paths[i].path {
					// A child event of the scripts directory (a script file
					// create/write/delete/rename) arrives named by the child.
					matched = true
				}
				if !matched {
					continue
				}
				relevant := event.Op&watchSpecEvents != 0
				// Per-file-event record (N11): the raw event stream feeding
				// the debounced rescan. valid marks the events that arm the
				// debounce (a create/write/delete/rename on a watched path);
				// other ops are recorded but inert. Whether the file then
				// yields a tool is decided by the rescan's own filter.
				valid := relevant
				env.log.Debug("watch event", "path", event.Name, "op", event.Op.String(), "valid", valid)
				if relevant {
					if !debounceActive {
						debounceActive = true
						debounceTimer.Reset(watchDebounceDelay)
					}
				}
				// Only events named with a watched path itself replace its
				// inode; child events never do. A recreated or replaced
				// watched path must be re-Added to be visible again (inotify
				// watches inodes, not paths).
				if event.Name == paths[i].path && event.Op&(fsnotify.Create|fsnotify.Remove|fsnotify.Rename) != 0 {
					reattach(paths[i].path)
				}
				break
			}

		case err, ok := <-env.watcherErrors(watcher):
			if !ok {
				return fmt.Errorf("watcher error channel closed unexpectedly")
			}
			// Log the error but don't crash the watcher
			// This handles cases like permission denied, file not found, etc.
			env.log.Warn("file watcher error", "error", err)

		case <-debounceTimer.C:
			debounceActive = false
			env.log.Debug("rescan fired")
			onRescan()
		}
	}
}

// watchTools watches the scripts directory and re-discovers + re-registers
// the tools on every debounced change. initialTools is the set the caller
// already discovered and registered at startup: the initial registration is
// re-asserted from it (no second directory scan before the watcher is ready,
// which used to duplicate the caller's scan and re-emit identical tool
// registrations, N list_changed notifications, at boot) and the guaranteed
// post-readiness rescan skips it entirely when the registry already matches.
// Symlinked tools whose targets live outside the directory are not watched
// directly (deliberate simplification, documented in the README): editing
// such a target keeps working at call time, and its registered metadata
// refreshes the next time the link itself changes or the server restarts.
// Setup failures and channel closures are returned as errors; only
// context.Canceled signals a clean shutdown.
func watchTools(ctx context.Context, env liveEnv, scriptsDir string, registry *toolRegistry, initialTools []discoveredTool) error {
	registry.replaceIfChanged(initialTools)
	return watchChanges(ctx, env, scriptsDir, func() {
		// After debounce delay, rediscover tools. The diff-skip avoids
		// the remove/re-add churn and N list_changed notifications for a
		// no-op rescan (e.g. a touched file with unchanged frontmatter).
		tools, err := discoverTools(scriptsDir, env.log)
		if err != nil {
			// The registry keeps the last known tool set.
			env.log.Warn("failed to rediscover tools", "error", err)
			return
		}
		registry.replaceIfChanged(tools)
	})
}
