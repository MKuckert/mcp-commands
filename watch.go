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
// file write arrives named by the child, not by the directory), while file
// targets match events named with their own path. passive marks the parent
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
// onRescan performs the rescan (discover + apply) and returns the external
// symlink-target paths the current tool set resolves to (may be nil).
// watchChanges keeps watches on those targets in sync: each watched target
// and its parent is added when it appears in the returned set (a target edit
// outside the scripts directory then fires a rescan) and removed when it
// leaves it. Setup failures (watcher creation, scripts-directory Add) are
// returned as errors — the caller decides the process-level consequence;
// target Add/Remove is best effort at all times.
//
// Replacement recovery: every watched path's parent is watched, and an
// event *on* a watched path that removes, renames, or recreates it triggers
// a best-effort re-Add of the path (with a warning on failure, never fatal
// at runtime) plus a rescan. Deleting and recreating the scripts directory
// therefore restores watching of the new inode; a permanent deletion degrades
// to the last known tool set with repeated rescan warnings. Transient
// fsnotify error-channel entries are logged and swallowed. It returns when
// ctx is done (context.Canceled) or a channel closes (error).
func watchChanges(ctx context.Context, env liveEnv, scriptsDir string, onRescan func() []string) error {
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
	protected := map[string]bool{scriptsWP.path: true, scriptsWP.parent: true}
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
	targets := onRescan()
	addTargets(watcher, &paths, env, protected, targets)

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
		if err := watcher.Add(p); err != nil {
			fmt.Fprintf(env.stderr, "Warning: failed to reattach watch on %s: %v\n", p, err)
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
				if event.Op&watchSpecEvents != 0 {
					if !debounceActive {
						debounceActive = true
						debounceTimer.Reset(watchDebounceDelay)
					}
				}
				// Only events named with a watched path itself replace its
				// inode; child events never do. A recreated directory and a
				// recreated/replaced target must be re-Added to be visible
				// again (inotify watches inodes, not paths).
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
			fmt.Fprintf(env.stderr, "Warning: file watcher error: %v\n", err)

		case <-debounceTimer.C:
			debounceActive = false
			targets = onRescan()
			addTargets(watcher, &paths, env, protected, targets)
		}
	}
}

// addTargets syncs the watched external-target set to want: new targets (and
// their parents) get watches, vanished ones lose theirs. Best effort — a
// target that disappears between the rescan and the Add/Remove logs a
// warning and never aborts the loop. A nil/empty want is valid (no targets).
// Protected paths (the scripts directory and its parent) are never unwatched:
// a vanished target whose parent is shared with the scripts directory must
// not drop the scripts-directory replacement-recovery watch.
func addTargets(watcher *fsnotify.Watcher, paths *[]watchPath, env liveEnv, protected map[string]bool, want []string) {
	current := make(map[string]bool, len(*paths))
	for _, wp := range *paths {
		current[wp.path] = true
	}
	for _, p := range want {
		if current[p] {
			continue
		}
		wp := watchPathFor(p)
		if err := watcher.Add(wp.path); err != nil {
			fmt.Fprintf(env.stderr, "Warning: failed to watch %s: %v\n", p, err)
			continue
		}
		if wp.parent != "" {
			if err := watcher.Add(wp.parent); err != nil {
				fmt.Fprintf(env.stderr, "Warning: failed to watch %s: %v\n", wp.parent, err)
				pp := watchPathFor(wp.parent)
				pp.passive = true
				*paths = append(*paths, pp)
			}
		}
		*paths = append(*paths, wp)
		current[p] = true
	}
	keep := make(map[string]bool, len(want))
	for _, p := range want {
		keep[p] = true
	}
	kept := make([]watchPath, 0, len(*paths))
	removePath := make(map[string]bool)
	removeParent := make(map[string]bool)
	for _, wp := range *paths {
		if protected[wp.path] || keep[wp.path] {
			kept = append(kept, wp)
			continue
		}
		if wp.passive {
			if !protected[wp.path] {
				removeParent[wp.path] = true
			}
			continue
		}
		if !protected[wp.path] {
			removePath[wp.path] = true
		}
		if wp.parent != "" {
			removeParent[wp.parent] = true
		}
	}
	for p := range removePath {
		if err := watcher.Remove(p); err != nil {
			fmt.Fprintf(env.stderr, "Warning: failed to unwatch %s: %v\n", p, err)
		}
	}
	// Unwatch a parent only when no surviving entry (as a path or as a
	// parent) still references it: several targets in one external directory
	// share the parent watch, and dropping it would silently disable
	// delete/recreate recovery for the ones that stay.
	survivors := make(map[string]bool)
	for _, wp := range kept {
		survivors[wp.path] = true
		if wp.parent != "" {
			survivors[wp.parent] = true
		}
	}
	for p := range removeParent {
		if protected[p] || survivors[p] {
			continue
		}
		if err := watcher.Remove(p); err != nil {
			fmt.Fprintf(env.stderr, "Warning: failed to unwatch %s: %v\n", p, err)
		}
	}
	*paths = kept
}

// externalTargets returns the deduped, discovery-ordered resolved tool paths
// that live outside scriptsDir: symlinks whose targets the scripts-directory
// watch cannot see. A normal file, or a symlink resolving inside the
// directory, is not external.
func externalTargets(tools []discoveredTool, scriptsDir string) []string {
	seen := make(map[string]bool, len(tools))
	var out []string
	for _, tool := range tools {
		if filepath.Dir(tool.Path) == scriptsDir || seen[tool.Path] {
			continue
		}
		seen[tool.Path] = true
		out = append(out, tool.Path)
	}
	return out
}

// watchTools watches the scripts directory and re-discovers + re-registers
// the tools on every debounced change. initialTools is the set the caller
// already discovered and registered at startup: the initial registration is
// re-asserted from it (no second directory scan before the watcher is ready,
// which used to duplicate the caller's scan and re-emit identical tool
// registrations, N list_changed notifications, at boot) and the guaranteed
// post-readiness rescan skips it entirely when the registry already matches.
// Symlinked tools whose targets live outside the directory are watched
// directly, so editing the target refreshes the registered tool.
// Setup failures and channel closures are returned as errors; only
// context.Canceled signals a clean shutdown.
func watchTools(ctx context.Context, env liveEnv, scriptsDir string, registry *toolRegistry, initialTools []discoveredTool) error {
	registry.replaceIfChanged(initialTools)

	var lastTargets []string
	return watchChanges(ctx, env, scriptsDir, func() []string {
		// After debounce delay, rediscover tools. The diff-skip avoids
		// the remove/re-add churn and N list_changed notifications for a
		// no-op rescan (e.g. a touched file with unchanged frontmatter).
		tools, err := discoverTools(scriptsDir, env.stderr)
		if err != nil {
			// Keep the previous target watches: the registry keeps the last
			// known tool set, so its symlink targets are still live.
			fmt.Fprintf(env.stderr, "Warning: failed to rediscover tools: %v\n", err)
			return lastTargets
		}

		registry.replaceIfChanged(tools)
		lastTargets = externalTargets(tools, scriptsDir)
		return lastTargets
	})
}
