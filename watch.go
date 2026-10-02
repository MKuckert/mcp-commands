package main

import (
	"context"
	"fmt"
	"time"

	"github.com/fsnotify/fsnotify"
)

const watchDebounceDelay = 100 * time.Millisecond

// prodWatcherErrors is the watcher-error source for watchChanges. Production
// returns the watcher's own channel; tests inject a synthetic one (a real
// fsnotify error is not deterministically reproducible — chmod on an already-
// watched inode does not produce one).

func prodWatcherErrors(w *fsnotify.Watcher) <-chan error { return w.Errors }

// watchChanges watches dir with fsnotify and invokes onChange once per
// debounced burst of Create/Write/Remove/Rename events. Watcher errors are
// logged to env.stderr but do not stop the loop, ensuring robust operation
// even if the watched directory is deleted or permissions change. It returns
// when ctx is done or the watcher channels close.

func watchChanges(ctx context.Context, env liveEnv, dir string, onChange func()) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create watcher: %w", err)
	}
	defer watcher.Close()

	if err := watcher.Add(dir); err != nil {
		return fmt.Errorf("failed to watch directory: %w", err)
	}

	debounceTimer := time.NewTimer(watchDebounceDelay)
	// NewTimer arms the clock immediately; disarm it right away so onChange
	// only fires after a file event Resets the timer. (Go >= 1.23 Stop drains
	// the channel, so no stale tick can be in flight.)
	debounceTimer.Stop()
	defer debounceTimer.Stop()
	debounceActive := false

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case event, ok := <-watcher.Events:
			if !ok {
				return fmt.Errorf("watcher channel closed unexpectedly")
			}

			// Check if the event is for a file in the scripts directory
			// We look for Create, Write, Remove, and Rename operations
			if event.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Remove|fsnotify.Rename) != 0 {
				if !debounceActive {
					debounceActive = true
					debounceTimer.Reset(watchDebounceDelay)
				}
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
			onChange()
		}
	}
}

// watchTools watches the scripts directory and re-discovers + re-registers
// the tools on every debounced change (built on watchChanges). initialTools
// is the set the caller already discovered and registered at startup: the
// initial registration is re-asserted from it (no second directory scan,
// which used to duplicate the caller's scan and re-emit identical tool
// registrations, N list_changed notifications, at boot) and skipped
// entirely when the registry already matches it.

func watchTools(ctx context.Context, env liveEnv, scriptsDir string, registry *toolRegistry, initialTools []discoveredTool) error {
	registry.replaceIfChanged(initialTools)

	return watchChanges(ctx, env, scriptsDir, func() {
		// After debounce delay, rediscover tools. The diff-skip avoids
		// the remove/re-add churn and N list_changed notifications for a
		// no-op rescan (e.g. a touched file with unchanged frontmatter).
		tools, err := discoverTools(scriptsDir)
		if err != nil {
			fmt.Fprintf(env.stderr, "Warning: failed to rediscover tools: %v\n", err)
			return
		}

		registry.replaceIfChanged(tools)
	})
}
