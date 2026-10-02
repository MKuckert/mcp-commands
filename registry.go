package main

import (
	"fmt"
	"time"

	"context"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"reflect"
	"sync"
)

const defaultMaxConcurrentTools = 16

type toolRegistry struct {
	server        *mcp.Server
	dirAbs        string
	globalTimeout time.Duration // applied to tools without a per-tool Timeout:
	slot          *execSlot     // bounds concurrent tool executions
	mu            sync.Mutex
	names         []string
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

// replace unregisters all currently tracked tools and registers a new set of tools.
// It defines the InputSchema dynamically based on each tool's Param declarations,
// allowing tools to accept typed parameters with proper schema validation.

func (r *toolRegistry) replace(tools []discoveredTool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.names) > 0 {
		r.server.RemoveTools(r.names...)
	}

	r.names = make([]string, 0, len(tools))
	r.current = tools
	for _, tool := range tools {
		// A per-tool Timeout: always wins over the global, even --no-timeout.
		toolTimeout := resolveToolTimeout(tool, r.globalTimeout)

		description := registeredDescription(tool.Description, toolTimeout)

		handlerFunc := mcp.ToolHandler(func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			parsedArgs, err := parseToolArguments(req.Params.Arguments)
			if err != nil {
				return nil, err
			}
			if err := validateRequiredParams(parsedArgs, tool.Params); err != nil {
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
			InputSchema: buildInputSchema(tool.Params),
		}, handlerFunc)

		r.names = append(r.names, tool.Name)
	}
}

// replaceIfChanged re-registers only when the discovered set differs from
// the currently registered one: an unchanged set (e.g. a touched file whose
// frontmatter did not change) triggers no RemoveTools/AddTool churn and no
// tools/list_changed notifications. It returns false when the set
// was identical and the replace was skipped.

func (r *toolRegistry) replaceIfChanged(tools []discoveredTool) bool {
	r.mu.Lock()
	unchanged := toolsEqual(r.current, tools)
	r.mu.Unlock()
	if unchanged {
		return false
	}
	r.replace(tools)
	return true
}

// toolsEqual compares two discovered tool sets element-wise (order included;
// discoverTools yields ReadDir order, i.e. stable by filename). The Timeout
// pointer is compared by value, the Params slice structurally.

func toolsEqual(a, b []discoveredTool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || a[i].Path != b[i].Path || a[i].Description != b[i].Description {
			return false
		}
		ta, tb := a[i].Timeout, b[i].Timeout
		if (ta == nil) != (tb == nil) {
			return false
		}
		if ta != nil && *ta != *tb {
			return false
		}
		if !reflect.DeepEqual(a[i].Params, b[i].Params) {
			return false
		}
	}
	return true
}

// resolveToolTimeout resolves a tool's effective timeout with the registry's
// precedence: a per-tool Timeout: always wins over the global, even
// --no-timeout. Shared by the registry and the --list-tools renderer so the
// two call sites cannot drift.

func registeredDescription(desc string, timeout time.Duration) string {
	suffix := timeoutSuffix(timeout)
	if desc == "" {
		return suffix
	}
	return desc + " " + suffix
}

// timeoutSuffix renders the resolved timeout for the registered tool
// description so the LLM knows its budget: "(timeout: 30s)" or
// "(timeout: none)" when no deadline applies.
