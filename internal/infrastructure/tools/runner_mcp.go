package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"unicode"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// MCPToolCaller is the smallest capability needed by a Worker MCP adapter.
// The caller is injected after startup observation; the runner never starts a
// server or discovers tools at execution time.
type MCPToolCaller interface {
	ConnectionGeneration() uint64
	CallToolAtGeneration(ctx context.Context, generation uint64, toolName string, args map[string]any) (string, error)
}

// MCPToolEntry binds a stable Worker-facing tool ID to the exact remote MCP
// name observed during startup.
type MCPToolEntry struct {
	ToolID      string
	RemoteName  string
	Description string
	InputSchema map[string]any
}

// MCPToolCatalog is an immutable, startup-observed MCP tool set.
// It contains no filesystem or process lifecycle behavior.
type MCPToolCatalog struct {
	generation uint64
	caller     MCPToolCaller
	entries    []MCPToolEntry
	schemas    map[string]*jsonschema.Schema
}

// NewMCPToolCatalog creates a deterministic catalog from one successful MCP
// tools/list result. Invalid names are excluded before registration; remote
// names are retained only in memory for the eventual CallTool request.
func NewMCPToolCatalog(namespace string, caller MCPToolCaller, definitions []tool.MCPToolDefinition, generation uint64) *MCPToolCatalog {
	catalog := &MCPToolCatalog{}
	if caller == nil || generation == 0 || caller.ConnectionGeneration() != generation {
		return catalog
	}
	namespace = sanitizeMCPNamespace(namespace)
	if namespace == "" {
		return catalog
	}
	catalog.caller = caller
	catalog.generation = generation
	catalog.schemas = make(map[string]*jsonschema.Schema)

	observed := make(map[string]tool.MCPToolDefinition, len(definitions))
	ambiguous := make(map[string]bool)
	for _, definition := range definitions {
		name := definition.Name
		if strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name || !validMCPRemoteName(name) || ambiguous[name] {
			continue
		}
		schema, ok := copyMCPSchema(definition.InputSchema)
		if !ok {
			ambiguous[name] = true
			delete(observed, name)
			continue
		}
		definition.InputSchema = schema
		if previous, exists := observed[name]; exists {
			if !reflect.DeepEqual(previous, definition) {
				ambiguous[name] = true
				delete(observed, name)
			}
			continue
		}
		observed[name] = definition
	}
	names := make([]string, 0, len(observed))
	for name := range observed {
		names = append(names, name)
	}
	sort.Strings(names)

	usedIDs := make(map[string]struct{}, len(names))
	for _, remoteName := range names {
		compiler := jsonschema.NewCompiler()
		compiler.DefaultDraft(jsonschema.Draft2020)
		compiler.UseLoader(mcpSchemaLoader{})
		const resource = "https://rencrow.invalid/observed-input-schema"
		if err := compiler.AddResource(resource, observed[remoteName].InputSchema); err != nil {
			continue
		}
		compiled, err := compiler.Compile(resource)
		if err != nil {
			continue
		}
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
		catalog.schemas[toolID] = compiled
		catalog.entries = append(catalog.entries, MCPToolEntry{
			ToolID:      toolID,
			RemoteName:  remoteName,
			Description: observed[remoteName].Description,
			InputSchema: observed[remoteName].InputSchema,
		})
	}
	return catalog
}

// Len reports how many observed MCP tools are eligible for Worker registration.
// The Worker policy remains the execution gate.
func (c *MCPToolCatalog) Len() int {
	if !c.available() {
		return 0
	}
	return len(c.entries)
}

// Entries returns a copy in deterministic order for registration and
// capability projection. Callers cannot mutate the catalog's internal set.
func (c *MCPToolCatalog) Entries() []MCPToolEntry {
	if !c.available() || len(c.entries) == 0 {
		return nil
	}
	entries := make([]MCPToolEntry, len(c.entries))
	for i, entry := range c.entries {
		schema, ok := copyMCPSchema(entry.InputSchema)
		if !ok {
			return nil
		}
		entry.InputSchema = schema
		entries[i] = entry
	}
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
	if !r.config.MCPToolCatalog.available() {
		return tool.NewError(tool.ErrNotFound, "MCP tool is not connected", nil), nil
	}
	if args == nil {
		args = map[string]any{}
	}
	// Validate the same private JSON value that is sent. Public metadata and
	// caller-owned maps must not replace or mutate the catalog contract.
	schema := r.config.MCPToolCatalog.schemas[entry.ToolID]
	raw, err := json.Marshal(args)
	if err != nil || schema == nil {
		return tool.NewError(tool.ErrValidationFailed, "Invalid MCP arguments", nil), nil
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil || schema.Validate(value) != nil {
		return tool.NewError(tool.ErrValidationFailed, "Invalid MCP arguments", nil), nil
	}
	validated, ok := value.(map[string]any)
	if !ok {
		return tool.NewError(tool.ErrValidationFailed, "Invalid MCP arguments", nil), nil
	}
	result, err := r.config.MCPToolCatalog.caller.CallToolAtGeneration(ctx, r.config.MCPToolCatalog.generation, entry.RemoteName, validated)
	if err != nil {
		return tool.NewError(tool.ErrInternalError, "MCP tool execution failed", nil), nil
	}
	return tool.NewSuccess(result), nil
}

func copyMCPSchema(schema map[string]any) (map[string]any, bool) {
	if schema == nil || schema["type"] != "object" {
		return nil, false
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, false
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, false
	}
	copied, ok := value.(map[string]any)
	return copied, ok
}

// Observed schemas may resolve their own local references, never fetch files
// or network resources. Standard dialects are bundled by the compiler.
type mcpSchemaLoader struct{}

func (mcpSchemaLoader) Load(string) (any, error) {
	return nil, errors.New("external MCP schema resources are unavailable")
}

// available binds discovery to the same live generation as execution. It does
// not rediscover tools or transfer an old observation to a replacement client.
func (c *MCPToolCatalog) available() bool {
	return c != nil && c.caller != nil && c.generation != 0 && c.caller.ConnectionGeneration() == c.generation
}

func (r *ToolRunner) unavailableMCPTool(id string) bool {
	catalog := r.config.MCPToolCatalog
	if catalog == nil {
		return false
	}
	_, observed := catalog.schemas[id]
	return observed && !catalog.available()
}
