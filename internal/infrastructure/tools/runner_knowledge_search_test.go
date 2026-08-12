package tools

import (
	"context"
	"strings"
	"testing"

	appkm "github.com/Nyukimin/RenCrow_CORE/internal/application/knowledgememory"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

type knowledgeSearchStub struct {
	requests []appkm.SearchRequest
	results  []appkm.SearchResult
}

func (s *knowledgeSearchStub) Search(_ context.Context, request appkm.SearchRequest) ([]appkm.SearchResult, error) {
	s.requests = append(s.requests, request)
	return append([]appkm.SearchResult(nil), s.results...), nil
}

func knowledgeSearchTestScope(t *testing.T, userID string, scopes ...string) context.Context {
	t.Helper()
	scope, err := tool.NewToolExecutionScope(
		"req-knowledge-1",
		tool.ActorKindAgent,
		"mio",
		userID,
		scopes,
		tool.AuthenticationSourceAgentOrchestrator,
	)
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	return tool.WithToolExecutionScope(context.Background(), scope)
}

func TestToolRunnerKnowledgeSearchRequiresReadyIndexedCapability(t *testing.T) {
	stub := &knowledgeSearchStub{}
	for _, ready := range []bool{false, true} {
		runner := NewToolRunner(ToolRunnerConfig{
			KnowledgeMemorySearcher:    stub,
			KnowledgeMemorySearchReady: ready,
			DisableToolHarness:         true,
		})
		metas, err := runner.ListTools(context.Background())
		if err != nil {
			t.Fatalf("ListTools() error = %v", err)
		}
		found := false
		for _, metadata := range metas {
			if metadata.ToolID == "knowledge.search" {
				found = true
				if !ready {
					t.Fatal("knowledge.search registered before readiness gate")
				}
				if metadata.Parameters["additionalProperties"] != false {
					t.Fatalf("metadata must reject additional properties: %#v", metadata.Parameters)
				}
			}
		}
		if found != ready {
			t.Fatalf("knowledge.search found=%t, want ready=%t", found, ready)
		}
	}
}

func TestToolRunnerKnowledgeSearchBlocksMissingOrInvalidScope(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{
		KnowledgeMemorySearcher:    &knowledgeSearchStub{},
		KnowledgeMemorySearchReady: true,
		DisableToolHarness:         true,
	})
	for name, ctx := range map[string]context.Context{
		"missing": context.Background(),
		"invalid": tool.WithToolExecutionScope(context.Background(), tool.ToolExecutionScope{RequestID: "req", ActorID: "mio"}),
	} {
		response, err := runner.ExecuteV2(ctx, "knowledge.search", map[string]any{
			"query":       "日本語",
			"record_type": "creative_knowledge",
		})
		if err != nil {
			t.Fatalf("%s ExecuteV2() error = %v", name, err)
		}
		if response == nil || response.Error == nil {
			t.Fatalf("%s response = %#v, want structured blocked error", name, response)
		}
		want := tool.ErrToolExecutionScopeMissing
		if name == "invalid" {
			want = tool.ErrToolExecutionScopeInvalid
		}
		if response.Error.Code != want {
			t.Fatalf("%s code = %s, want %s", name, response.Error.Code, want)
		}
	}
}

func TestToolRunnerKnowledgeSearchRejectsForgedScopeArgsAndUsesTrustedOwner(t *testing.T) {
	stub := &knowledgeSearchStub{results: []appkm.SearchResult{{RecordType: "creative_knowledge", RecordID: "a"}}}
	runner := NewToolRunner(ToolRunnerConfig{
		KnowledgeMemorySearcher:    stub,
		KnowledgeMemorySearchReady: true,
		DisableToolHarness:         true,
	})
	ctx := knowledgeSearchTestScope(t, "user-a", tool.DataScopeUser)

	for _, args := range []map[string]any{
		{"query": "日本語", "record_type": "creative_knowledge", "user_id": "user-b"},
		{"query": "日本語", "record_type": "creative_knowledge", "scope": "public"},
		{"query": "日本語", "record_type": "creative_knowledge", "sql": "SELECT *"},
	} {
		response, err := runner.ExecuteV2(ctx, "knowledge.search", args)
		if err != nil {
			t.Fatalf("forged args ExecuteV2() error = %v", err)
		}
		if response == nil || response.Error == nil || response.Error.Code != tool.ErrValidationFailed {
			t.Fatalf("forged args response = %#v, want validation error", response)
		}
	}

	response, err := runner.ExecuteV2(ctx, "knowledge.search", map[string]any{
		"query":       "日本語",
		"record_type": "creative_knowledge",
		"limit":       7,
	})
	if err != nil || response == nil || response.Error != nil {
		t.Fatalf("valid search response=%#v err=%v", response, err)
	}
	if len(stub.requests) != 1 {
		t.Fatalf("search calls = %d, want 1 after forged calls", len(stub.requests))
	}
	request := stub.requests[0]
	if request.Scope.Scope != appkm.SearchScopeUser || request.Scope.UserID != "user-a" {
		t.Fatalf("trusted scope = %#v, want user-a user scope", request.Scope)
	}
	if request.RecordType != "creative_knowledge" || request.Limit != 7 {
		t.Fatalf("bounded request = %#v", request)
	}
	if response.Metadata["execution_receipt"] == nil || strings.Contains(response.String(), "user-b") {
		t.Fatalf("receipt/result leaked forged body or missing receipt: %#v", response)
	}
}

func TestToolRunnerKnowledgeSearchUsesPublicOnlyForPublicScope(t *testing.T) {
	stub := &knowledgeSearchStub{}
	runner := NewToolRunner(ToolRunnerConfig{
		KnowledgeMemorySearcher:    stub,
		KnowledgeMemorySearchReady: true,
		DisableToolHarness:         true,
	})
	ctx := knowledgeSearchTestScope(t, "", tool.DataScopePublic)
	response, err := runner.ExecuteV2(ctx, "knowledge.search", map[string]any{
		"query":       "日本語",
		"record_type": "news_knowledge",
	})
	if err != nil || response == nil || response.Error != nil {
		t.Fatalf("public search response=%#v err=%v", response, err)
	}
	if len(stub.requests) != 1 || stub.requests[0].Scope != (appkm.SearchScope{Scope: appkm.SearchScopePublic}) {
		t.Fatalf("public scope request = %#v", stub.requests)
	}
}

func TestToolRunnerKnowledgeSearchPreservesTrustedScopeThroughWrappers(t *testing.T) {
	stub := &knowledgeSearchStub{}
	base := NewToolRunner(ToolRunnerConfig{
		KnowledgeMemorySearcher:    stub,
		KnowledgeMemorySearchReady: true,
		DisableToolHarness:         true,
	})
	wrapped := NewContextBudgetRunner(
		NewToolHarnessRunner(base, nil),
		ContextBudgetRunnerConfig{Agent: "Worker"},
	)
	ctx := knowledgeSearchTestScope(t, "user-a", tool.DataScopeUser)
	response, err := wrapped.ExecuteV2(ctx, "knowledge.search", map[string]any{
		"query": "日本語", "record_type": "creative_knowledge",
	})
	if err != nil || response == nil || response.Error != nil {
		t.Fatalf("wrapped search response=%#v err=%v", response, err)
	}
	if len(stub.requests) != 1 || stub.requests[0].Scope.UserID != "user-a" {
		t.Fatalf("trusted scope was not preserved through wrappers: %#v", stub.requests)
	}
}

func TestToolRunnerKnowledgeSearchRejectsBoundViolation(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{
		KnowledgeMemorySearcher:    &knowledgeSearchStub{},
		KnowledgeMemorySearchReady: true,
		DisableToolHarness:         true,
	})
	ctx := knowledgeSearchTestScope(t, "", tool.DataScopePublic)
	for _, args := range []map[string]any{
		{"query": "日本語", "record_type": "creative_knowledge", "limit": 21},
		{"query": "日本語", "record_type": "unknown"},
		{"query": "日本語", "record_type": " creative_knowledge"},
		{"query": "日本語", "record_type": "creative_knowledge", "limit": "7"},
		{"query": strings.Repeat("日本", 81), "record_type": "creative_knowledge"},
	} {
		response, err := runner.ExecuteV2(ctx, "knowledge.search", args)
		if err != nil || response == nil || response.Error == nil || response.Error.Code != tool.ErrValidationFailed {
			t.Fatalf("args=%#v response=%#v err=%v, want validation error", args, response, err)
		}
	}
}
