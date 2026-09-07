package tools

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

type mcpCallerStub struct {
	result     string
	err        error
	name       string
	args       map[string]any
	calls      int
	generation uint64
}

func (s *mcpCallerStub) CallTool(_ context.Context, name string, args map[string]any) (string, error) {
	s.calls++
	s.name = name
	s.args = args
	return s.result, s.err
}

func TestMCPToolCatalogIsDeterministicAndKeepsRemoteNamesPrivate(t *testing.T) {
	caller := &mcpCallerStub{}
	catalog := NewMCPToolCatalog("serena", caller, mcpFixtureDefinitions([]string{
		"replace_symbol",
		"find.symbol",
		"find symbol",
		"find.symbol",
		"../path",
		"bad\nname",
	}), 1)

	entries := catalog.Entries()
	if got, want := len(entries), 3; got != want {
		t.Fatalf("catalog entries=%d, want %d: %#v", got, want, entries)
	}
	want := []MCPToolEntry{
		{ToolID: "mcp.serena.find_symbol", RemoteName: "find symbol", InputSchema: map[string]any{"type": "object"}},
		{ToolID: "mcp.serena.find_symbol_2", RemoteName: "find.symbol", InputSchema: map[string]any{"type": "object"}},
		{ToolID: "mcp.serena.replace_symbol", RemoteName: "replace_symbol", InputSchema: map[string]any{"type": "object"}},
	}
	if !reflect.DeepEqual(entries, want) {
		t.Fatalf("entries=%#v, want %#v", entries, want)
	}
	entries[0].RemoteName = "mutated"
	if catalog.Entries()[0].RemoteName == "mutated" {
		t.Fatal("Entries must return a copy of the immutable catalog")
	}
}

func TestMCPToolRunnerForwardsExactRemoteNameAndReturnsStructuredResponse(t *testing.T) {
	caller := &mcpCallerStub{result: "found"}
	catalog := NewMCPToolCatalog("serena", caller, mcpFixtureDefinitions([]string{"find.symbol"}), 1)
	runner := NewToolRunner(ToolRunnerConfig{MCPToolCatalog: catalog})

	metas, err := runner.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}
	var foundMetadata *tool.ToolMetadata
	for i := range metas {
		if metas[i].ToolID == "mcp.serena.find_symbol" {
			foundMetadata = &metas[i]
			break
		}
	}
	if foundMetadata == nil {
		t.Fatalf("MCP metadata missing: %#v", metas)
	}
	if foundMetadata.Origin != tool.OriginCoreRuntime || foundMetadata.Category != "mutation" {
		t.Fatalf("unexpected MCP metadata: %#v", *foundMetadata)
	}
	definitionFound := false
	for _, definition := range runner.ToolDefinitions() {
		if definition.Function.Name == "mcp.serena.find_symbol" {
			definitionFound = true
			break
		}
	}
	if !definitionFound {
		t.Fatal("MCP tool missing from ToolDefinitions")
	}

	args := map[string]any{"name_path": "Foo"}
	resp, err := runner.ExecuteV2(context.Background(), "mcp.serena.find_symbol", args)
	if err != nil || resp == nil || resp.IsError() || resp.String() != "found" {
		t.Fatalf("MCP execution failed: resp=%#v response_error=%v err=%v", resp, resp.Error, err)
	}
	if caller.calls != 1 || caller.name != "find.symbol" || !reflect.DeepEqual(caller.args, args) {
		t.Fatalf("remote call mismatch: calls=%d name=%q args=%#v", caller.calls, caller.name, caller.args)
	}

	caller.err = errors.New("remote failure")
	failed, err := runner.ExecuteV2(context.Background(), "mcp.serena.find_symbol", nil)
	if err != nil || failed == nil || !failed.IsError() || failed.Error.Code != tool.ErrInternalError {
		t.Fatalf("MCP error should be structured: resp=%#v err=%v", failed, err)
	}
	if failed.Error.Message == "remote failure" {
		t.Fatal("remote error detail must not become an unbounded prompt result")
	}
}

func TestMCPToolCatalogIsWorkerOnlyAndEmptyCatalogIsAbsent(t *testing.T) {
	caller := &mcpCallerStub{result: "ok"}
	worker := NewToolRunner(ToolRunnerConfig{MCPToolCatalog: NewMCPToolCatalog("serena", caller, mcpFixtureDefinitions([]string{"search"}), 1)})
	chat := NewToolRunner(ToolRunnerConfig{})
	empty := NewToolRunner(ToolRunnerConfig{MCPToolCatalog: NewMCPToolCatalog("serena", caller, nil, 1)})

	if !hasMCPMetadata(mustListMCPMetadata(t, worker), "mcp.serena.search") {
		t.Fatal("Worker should expose observed MCP tool")
	}
	if hasMCPMetadata(mustListMCPMetadata(t, chat), "mcp.serena.search") {
		t.Fatal("Chat must not expose Serena MCP execution")
	}
	if hasMCPMetadata(mustListMCPMetadata(t, empty), "mcp.serena.search") {
		t.Fatal("empty observed set must not expose MCP execution")
	}
}

func hasMCPMetadata(metas []tool.ToolMetadata, id string) bool {
	for _, metadata := range metas {
		if metadata.ToolID == id {
			return true
		}
	}
	return false
}

func mustListMCPMetadata(t *testing.T, runner *ToolRunner) []tool.ToolMetadata {
	t.Helper()
	metas, err := runner.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}
	return metas
}

func TestMCPMetadataQueryRequiresKnownSerenaOperation(t *testing.T) {
	for _, name := range []string{"find_symbol", "find_referencing_symbols", "get_symbols_overview", "read_file", "search_for_pattern", "list_dir", "replace_symbol", "unknown", "find.symbol", "Find_symbol"} {
		entry := MCPToolEntry{ToolID: "mcp.serena." + name, RemoteName: name}
		got := mcpToolMetadata(entry).Category
		want := "mutation"
		switch name {
		case "find_symbol", "find_referencing_symbols", "get_symbols_overview", "read_file", "search_for_pattern", "list_dir":
			want = "query"
		}
		if got != want {
			t.Errorf("%s category=%s want=%s", name, got, want)
		}
	}
	if got := mcpToolMetadata(MCPToolEntry{ToolID: "mcp.other.read_file", RemoteName: "read_file"}).Category; got != "mutation" {
		t.Errorf("foreign namespace classified %s", got)
	}
}

func (s *mcpCallerStub) ConnectionGeneration() uint64 {
	if s.generation == 0 {
		return 1
	}
	return s.generation
}
func (s *mcpCallerStub) CallToolAtGeneration(ctx context.Context, generation uint64, name string, args map[string]any) (string, error) {
	if generation == 0 || generation != s.ConnectionGeneration() {
		return "", errors.New("connection generation changed")
	}
	return s.CallTool(ctx, name, args)
}
func TestMCPToolCatalogRejectsReconnectedClient(t *testing.T) {
	caller := &mcpCallerStub{result: "new connection", generation: 1}
	catalog := NewMCPToolCatalog("serena", caller, mcpFixtureDefinitions([]string{"read_file"}), 1)
	runner := NewToolRunner(ToolRunnerConfig{MCPToolCatalog: catalog})
	caller.generation = 2
	response, err := runner.ExecuteV2(context.Background(), "mcp.serena.read_file", map[string]any{"relative_path": "file"})
	if err == nil && (response == nil || response.Error == nil) {
		t.Fatal("stale catalog accepted replacement connection")
	}
	if caller.calls != 0 {
		t.Fatalf("stale catalog called new connection %d times", caller.calls)
	}
}

func mcpFixtureDefinitions(names []string) []tool.MCPToolDefinition {
	definitions := make([]tool.MCPToolDefinition, 0, len(names))
	for _, name := range names {
		definitions = append(definitions, tool.MCPToolDefinition{Name: name, InputSchema: map[string]any{"type": "object"}})
	}
	return definitions
}

func TestMCPObservedSchemaSurvivesCatalogAndMetadata(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{"relative_path": map[string]any{"type": "string"}}, "required": []any{"relative_path"}, "additionalProperties": false}
	catalog := NewMCPToolCatalog("serena", &mcpCallerStub{}, []tool.MCPToolDefinition{{Name: "read_file", Description: "Read source", InputSchema: schema}}, 1)
	schema["properties"].(map[string]any)["relative_path"].(map[string]any)["type"] = "number"
	first := catalog.Entries()
	if len(first) != 1 {
		t.Fatalf("definition missing: %#v", first)
	}
	first[0].InputSchema["properties"].(map[string]any)["relative_path"].(map[string]any)["type"] = "boolean"
	runner := NewToolRunner(ToolRunnerConfig{MCPToolCatalog: catalog})
	metas, err := runner.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, meta := range metas {
		if meta.ToolID == "mcp.serena.read_file" {
			got := meta.Parameters
			if got["additionalProperties"] != false || got["properties"].(map[string]any)["relative_path"].(map[string]any)["type"] != "string" || meta.Description != "Read source" {
				t.Fatalf("schema/description lost or mutated: %#v", meta)
			}
			return
		}
	}
	t.Fatal("observed MCP metadata missing")
}
func TestMCPMissingAndConflictingDefinitionsAreUnavailable(t *testing.T) {
	definitions := []tool.MCPToolDefinition{
		{Name: "missing"},
		{Name: "wrong_type", InputSchema: map[string]any{"type": "array"}},
		{Name: "conflict", InputSchema: map[string]any{"type": "object"}},
		{Name: "conflict", InputSchema: map[string]any{"type": "object", "additionalProperties": false}},
		{Name: "valid", InputSchema: map[string]any{"type": "object"}},
	}
	catalog := NewMCPToolCatalog("serena", &mcpCallerStub{}, definitions, 1)
	entries := catalog.Entries()
	if len(entries) != 1 || entries[0].RemoteName != "valid" {
		t.Fatalf("ambiguous/absent schema admitted: %#v", entries)
	}
}

func TestMCPArgumentSchemaExecutionBoundary(t *testing.T) {
	schema := map[string]any{"type": "object", "$defs": map[string]any{"query": map[string]any{"type": "string", "minLength": 2}}, "properties": map[string]any{"query": map[string]any{"$ref": "#/$defs/query"}}, "required": []any{"query"}, "additionalProperties": false}
	caller := &mcpCallerStub{result: "ok"}
	catalog := NewMCPToolCatalog("serena", caller, []tool.MCPToolDefinition{{Name: "search", InputSchema: schema}}, 1)
	runner := NewToolRunner(ToolRunnerConfig{MCPToolCatalog: catalog})
	for _, metadata := range mustListMCPMetadata(t, runner) {
		if metadata.ToolID == "mcp.serena.search" {
			metadata.Parameters["required"] = []any{}
			metadata.Parameters["additionalProperties"] = true
		}
	}
	for _, args := range []map[string]any{nil, {"query": 3}, {"query": "x"}, {"query": "valid", "extra": "secret"}, {"query": make(chan int)}} {
		response, err := runner.ExecuteV2(context.Background(), "mcp.serena.search", args)
		if err != nil || response == nil || !response.IsError() || response.Error.Code != tool.ErrValidationFailed {
			t.Fatalf("invalid input accepted: response=%#v err=%v", response, err)
		}
	}
	if caller.calls != 0 {
		t.Fatalf("invalid inputs reached caller: %d", caller.calls)
	}
	args := map[string]any{"query": "valid"}
	response, err := runner.ExecuteV2(context.Background(), "mcp.serena.search", args)
	if err != nil || response.IsError() || caller.calls != 1 {
		t.Fatalf("valid local ref rejected: %#v %v", response, err)
	}
	args["query"] = "changed"
	if caller.args["query"] != "valid" {
		t.Fatal("forwarded original mutable arguments")
	}
}

func TestMCPUncompileableSchemaUnavailable(t *testing.T) {
	for _, schema := range []map[string]any{
		{"type": "object", "required": "invalid"},
		{"type": "object", "$ref": "https://example.invalid/schema"},
		{"type": "object", "$ref": "file:///etc/passwd"},
		{"type": "object", "$schema": "https://example.invalid/dialect"},
	} {
		catalog := NewMCPToolCatalog("serena", &mcpCallerStub{}, []tool.MCPToolDefinition{{Name: "search", InputSchema: schema}}, 1)
		if catalog.Len() != 0 {
			t.Fatal("uncompileable schema exposed")
		}
	}
}

func TestMCPArgumentSchemaPreservesLargeIntegers(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{"n": map[string]any{"const": json.Number("9007199254740993")}}, "required": []any{"n"}}
	caller := &mcpCallerStub{result: "ok"}
	runner := NewToolRunner(ToolRunnerConfig{MCPToolCatalog: NewMCPToolCatalog("serena", caller, []tool.MCPToolDefinition{{Name: "search", InputSchema: schema}}, 1)})
	for _, number := range []string{"9007199254740992", "9007199254740993"} {
		response, err := runner.ExecuteV2(context.Background(), "mcp.serena.search", map[string]any{"n": json.Number(number)})
		if err != nil || response.IsError() != (number == "9007199254740992") {
			t.Fatalf("incorrect exact number validation: %s %#v %v", number, response, err)
		}
	}
	if caller.calls != 1 || caller.args["n"] != json.Number("9007199254740993") {
		t.Fatalf("number changed on wire: %#v", caller.args)
	}
}

func TestMCPStaleCatalogUnavailableToDiscovery(t *testing.T) {
	caller := &mcpCallerStub{generation: 1}
	catalog := NewMCPToolCatalog("serena", caller, mcpFixtureDefinitions([]string{"read_file"}), 1)
	runner := NewToolRunner(ToolRunnerConfig{MCPToolCatalog: catalog})
	caller.generation = 2
	if catalog.Len() != 0 || len(catalog.Entries()) != 0 {
		t.Fatal("stale catalog remains available")
	}
	for _, metadata := range mustListMCPMetadata(t, runner) {
		if metadata.ToolID == "mcp.serena.read_file" {
			t.Fatal("stale Worker metadata")
		}
	}
	for _, definition := range runner.ToolDefinitions() {
		if definition.Function.Name == "mcp.serena.read_file" {
			t.Fatal("stale model tool definition")
		}
	}
}
