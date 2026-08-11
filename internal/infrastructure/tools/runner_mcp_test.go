package tools

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

type mcpCallerStub struct {
	result string
	err    error
	name   string
	args   map[string]any
	calls  int
}

func (s *mcpCallerStub) CallTool(_ context.Context, name string, args map[string]any) (string, error) {
	s.calls++
	s.name = name
	s.args = args
	return s.result, s.err
}

func TestMCPToolCatalogIsDeterministicAndKeepsRemoteNamesPrivate(t *testing.T) {
	caller := &mcpCallerStub{}
	catalog := NewMCPToolCatalog("serena", caller, []string{
		"replace_symbol",
		"find.symbol",
		"find symbol",
		"find.symbol",
		"../path",
		"bad\nname",
	})

	entries := catalog.Entries()
	if got, want := len(entries), 3; got != want {
		t.Fatalf("catalog entries=%d, want %d: %#v", got, want, entries)
	}
	want := []MCPToolEntry{
		{ToolID: "mcp.serena.find_symbol", RemoteName: "find symbol"},
		{ToolID: "mcp.serena.find_symbol_2", RemoteName: "find.symbol"},
		{ToolID: "mcp.serena.replace_symbol", RemoteName: "replace_symbol"},
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
	catalog := NewMCPToolCatalog("serena", caller, []string{"find.symbol"})
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
	if foundMetadata.Origin != tool.OriginCoreRuntime || foundMetadata.Category != "query" {
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
	worker := NewToolRunner(ToolRunnerConfig{MCPToolCatalog: NewMCPToolCatalog("serena", caller, []string{"search"})})
	chat := NewToolRunner(ToolRunnerConfig{})
	empty := NewToolRunner(ToolRunnerConfig{MCPToolCatalog: NewMCPToolCatalog("serena", caller, nil)})

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
