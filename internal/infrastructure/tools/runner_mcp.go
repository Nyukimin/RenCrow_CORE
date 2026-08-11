package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

// MCPToolCaller is the smallest capability needed by a Worker MCP adapter.
// The caller is injected after startup observation; the runner never starts a
// server or discovers tools at execution time.
type MCPToolCaller interface {
	CallTool(ctx context.Context, toolName string, args map[string]any) (string, error)
}

// MCPToolEntry binds a stable Worker-facing tool ID to the exact remote MCP
// name observed during startup.
type MCPToolEntry struct {
	ToolID     string
	RemoteName string
}

// MCPToolCatalog is an immutable, startup-observed MCP tool set.
// It contains no filesystem or process lifecycle behavior.
type MCPToolCatalog struct {
	caller  MCPToolCaller
	entries []MCPToolEntry
}

// NewMCPToolCatalog creates a deterministic catalog from one successful MCP
// tools/list result. Invalid names are excluded before registration; remote
// names are retained only in memory for the eventual CallTool request.
func NewMCPToolCatalog(namespace string, caller MCPToolCaller, remoteNames []string) *MCPToolCatalog {
	catalog := &MCPToolCatalog{}
	if caller == nil {
		return catalog
	}
	namespace = sanitizeMCPNamespace(namespace)
	if namespace == "" {
		return catalog
	}
	catalog.caller = caller

	names := make([]string, 0, len(remoteNames))
	seenRemote := make(map[string]struct{}, len(remoteNames))
	for _, raw := range remoteNames {
		name := raw
		if strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name {
			continue
		}
		if _, seen := seenRemote[name]; seen {
			continue
		}
		if !validMCPRemoteName(name) {
			continue
		}
		seenRemote[name] = struct{}{}
		names = append(names, name)
	}
	sort.Strings(names)

	usedIDs := make(map[string]struct{}, len(names))
	for _, remoteName := range names {
		segment := sanitizeMCPToolSegment(remoteName)
		if segment == "" {
			continue
		}
		baseID := "mcp." + namespace + "." + segment
		toolID := baseID
		for suffix := 2; ; suffix++ {
			if _, exists := usedIDs[toolID]; !exists {
				break
			}
			toolID = fmt.Sprintf("%s_%d", baseID, suffix)
		}
		usedIDs[toolID] = struct{}{}
		catalog.entries = append(catalog.entries, MCPToolEntry{
			ToolID:     toolID,
			RemoteName: remoteName,
		})
	}
	return catalog
}

// Len reports how many observed MCP tools are eligible for Worker registration.
// The Worker policy remains the execution gate.
func (c *MCPToolCatalog) Len() int {
	if c == nil {
		return 0
	}
	return len(c.entries)
}

// Entries returns a copy in deterministic order for registration and
// capability projection. Callers cannot mutate the catalog's internal set.
func (c *MCPToolCatalog) Entries() []MCPToolEntry {
	if c == nil || len(c.entries) == 0 {
		return nil
	}
	entries := make([]MCPToolEntry, len(c.entries))
	copy(entries, c.entries)
	return entries
}

func sanitizeMCPNamespace(namespace string) string {
	namespace = strings.TrimSpace(strings.ToLower(namespace))
	if namespace == "" || !validMCPRemoteName(namespace) {
		return ""
	}
	var b strings.Builder
	for _, r := range namespace {
		if isMCPNameRune(r) {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('_')
	}
	return strings.ToLower(strings.Trim(b.String(), "_-"))
}

func validMCPRemoteName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) || r == '/' || r == '\\' || r == ':' {
			return false
		}
	}
	return true
}

func sanitizeMCPToolSegment(name string) string {
	var b strings.Builder
	for _, r := range name {
		if isMCPNameRune(r) {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('_')
	}
	return strings.ToLower(strings.Trim(b.String(), "_-"))
}

func isMCPNameRune(r rune) bool {
	return r >= 'a' && r <= 'z' ||
		r >= 'A' && r <= 'Z' ||
		r >= '0' && r <= '9' ||
		r == '_' || r == '-'
}

func (r *ToolRunner) registerMCPTools() {
	if r.config.MCPToolCatalog == nil || r.config.MCPToolCatalog.Len() == 0 {
		return
	}
	for _, entry := range r.config.MCPToolCatalog.Entries() {
		entry := entry
		r.toolsV2[entry.ToolID] = func(ctx context.Context, args map[string]any) (*tool.ToolResponse, error) {
			return r.executeMCPToolV2(ctx, entry, args)
		}
	}
}

func (r *ToolRunner) executeMCPToolV2(ctx context.Context, entry MCPToolEntry, args map[string]any) (*tool.ToolResponse, error) {
	if r.config.MCPToolCatalog == nil || r.config.MCPToolCatalog.caller == nil {
		return tool.NewError(tool.ErrNotFound, "MCP tool is not connected", nil), nil
	}
	if args == nil {
		args = map[string]any{}
	}
	result, err := r.config.MCPToolCatalog.caller.CallTool(ctx, entry.RemoteName, args)
	if err != nil {
		return tool.NewError(tool.ErrInternalError, "MCP tool execution failed", nil), nil
	}
	return tool.NewSuccess(result), nil
}
