package main

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const defaultMaxConcurrentTools = 16

// toolRegistry manages the dynamic registration and deregistration of tools
// within the MCP server. It ensures thread-safe updates via a mutex, allowing
// tools to be swapped out at runtime when changes are detected in the scripts directory.
type toolRegistry struct {
	server        *mcp.Server
	dirAbs        string
	globalTimeout time.Duration // applied to tools without a per-tool Timeout:
	slot          *execSlot     // bounds concurrent tool executions
	mu            sync.Mutex
	current       []discoveredTool // last registered set (for the change-diff in replaceIfChanged)
}

func newToolRegistry(server *mcp.Server, dir string, globalTimeout time.Duration, maxConcurrent int) *toolRegistry {
	return &toolRegistry{
		server:        server,
		dirAbs:        dir,
		globalTimeout: globalTimeout,
		slot:          newExecSlot(maxConcurrent),
	}
}

// execSlot bounds how many tool subprocesses may run at once. It is a
// fixed-size buffered channel: each running execution holds one slot.
type execSlot struct {
	sem   chan struct{}
	limit int
}

func newExecSlot(limit int) *execSlot {
	if limit <= 0 {
		limit = defaultMaxConcurrentTools
	}
	return &execSlot{sem: make(chan struct{}, limit), limit: limit}
}

// tryAcquire grabs a slot without blocking: false means the server is at
// capacity (the caller returns a clean "at capacity" tool result).
func (s *execSlot) tryAcquire() bool {
	select {
	case s.sem <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *execSlot) release() { <-s.sem }

// replace syncs the registered tool set to the given set via a per-tool diff
// against the last registered one: only removed names are unregistered and
// only added/changed tools are (re-)added. The SDK replaces an existing name
// in place on add, so the diff avoids the remove-then-add gap that made an
// unchanged tool invisible to a concurrent tools/list for the duration of
// the rescan, and avoids N list_changed notifications for an unchanged set.
// The InputSchema is defined dynamically from each tool's Param declarations,
// allowing tools to accept typed parameters with proper schema validation.
func (r *toolRegistry) replace(tools []discoveredTool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.replaceLocked(tools)
}

// replaceLocked applies the per-tool diff; the caller must hold r.mu.
func (r *toolRegistry) replaceLocked(tools []discoveredTool) {
	previous := make(map[string]discoveredTool, len(r.current))
	for _, tool := range r.current {
		previous[tool.Name] = tool
	}
	want := make(map[string]discoveredTool, len(tools))
	for _, tool := range tools {
		want[tool.Name] = tool
	}

	// Only names that left the set are unregistered. Removals are
	// deterministic (name-sorted) so the resulting list_changed stream is
	// stable.
	var removed []string
	for name := range previous {
		if _, ok := want[name]; !ok {
			removed = append(removed, name)
		}
	}
	sort.Strings(removed)
	if len(removed) > 0 {
		r.server.RemoveTools(removed...)
	}

	for _, tool := range tools {
		if prev, existed := previous[tool.Name]; existed && toolEqual(prev, tool) {
			continue
		}
		r.addToolLocked(tool)
	}
	// Copy: the registry keeps this as its diff baseline and must not alias
	// the caller's slice.
	r.current = append([]discoveredTool(nil), tools...)
}

// addToolLocked registers one tool (add is a replace-in-place in the SDK,
// so calling it for a changed tool updates the existing registration without
// a removal gap); the caller must hold r.mu.
func (r *toolRegistry) addToolLocked(tool discoveredTool) {
	// A per-tool Timeout: always wins over the global, even --no-timeout.
	toolTimeout := resolveToolTimeout(tool, r.globalTimeout)

	description := registeredDescription(tool.Description, toolTimeout)

	inputSchema := buildInputSchema(tool.Params)
	validator, err := resolveInputSchema(inputSchema)
	if err != nil {
		panic(fmt.Errorf("invalid input schema for tool %q: %w", tool.Name, err))
	}

	handlerFunc := mcp.ToolHandler(func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		parsedArgs, err := parseToolArguments(req.Params.Arguments)
		if err != nil {
			return nil, err
		}
		if err := validateToolArguments(parsedArgs, validator); err != nil {
			return textResult(err.Error(), true), nil
		}
		// Capacity check after validation: malformed calls must not
		// consume a slot. At capacity, fail cleanly so clients retry
		// rather than piling up pinned subprocesses.
		if !r.slot.tryAcquire() {
			msg := fmt.Sprintf("mcp-commands is at capacity (%d concurrent tool executions); please retry shortly", r.slot.limit)
			return textResult(msg, true), nil
		}
		defer r.slot.release()
		return executeTool(ctx, tool.Path, parsedArgs, toolTimeout, r.dirAbs)
	})

	r.server.AddTool(&mcp.Tool{
		Name:        tool.Name,
		Description: description,
		InputSchema: inputSchema,
	}, handlerFunc)
}

// replaceIfChanged re-registers only when the discovered set differs from
// the currently registered one — the diff and the skip decision under a
// single lock acquisition, so a replace that starts concurrently cannot
// land between the check and the diff (the check-and-act pair is atomic).
// An unchanged set (e.g. a touched file whose frontmatter did not change)
// triggers no RemoveTools/AddTool churn and no list_changed notifications.
// It returns false when the set was identical and the replace was skipped.
func (r *toolRegistry) replaceIfChanged(tools []discoveredTool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if toolsEqual(r.current, tools) {
		return false
	}
	r.replaceLocked(tools)
	return true
}

// stringSlicesEqual reports whether two string slices are elementwise equal.
func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// toolEqual reports whether two discovered tools would register identically:
// name, path, description (the Timeout pointer compared by value, the Params
// slice structurally). Name is implicit — the callers index by it.
func toolEqual(a, b discoveredTool) bool {
	if a.Path != b.Path || a.Description != b.Description {
		return false
	}
	ta, tb := a.Timeout, b.Timeout
	if (ta == nil) != (tb == nil) {
		return false
	}
	if ta != nil && *ta != *tb {
		return false
	}
	return reflect.DeepEqual(a.Params, b.Params)
}

// toolsEqual compares two discovered tool sets element-wise (order included;
// discoverTools yields ReadDir order, i.e. stable by filename).
func toolsEqual(a, b []discoveredTool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || !toolEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

// registeredDescription assembles the registered tool description: the
// frontmatter description plus the " (timeout: …)" suffix (the suffix alone
// when the description is empty). Shared by the registry and the
// --list-tools renderer so the two never drift apart.
func registeredDescription(desc string, timeout time.Duration) string {
	suffix := timeoutSuffix(timeout)
	if desc == "" {
		return suffix
	}
	return desc + " " + suffix
}
