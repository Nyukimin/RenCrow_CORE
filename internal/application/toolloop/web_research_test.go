package toolloop

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
)

type webResearchRunnerStub struct {
	metadata []tool.ToolMetadata
	calls    []string
	args     []map[string]any
	err      error
	results  map[string]*tool.ToolResponse
}

func (r *webResearchRunnerStub) ListTools(context.Context) ([]tool.ToolMetadata, error) {
	return r.metadata, nil
}

func (r *webResearchRunnerStub) ExecuteV2(_ context.Context, name string, args map[string]any) (*tool.ToolResponse, error) {
	r.calls = append(r.calls, name)
	r.args = append(r.args, args)
	if r.err != nil {
		return nil, r.err
	}
	if result, ok := r.results[name]; ok {
		return result, nil
	}
	return tool.NewSuccess(map[string]any{"url": "https://example.org/source", "text": "actual evidence"}), nil
}

func TestWebResearchFetchesSearchSourceEvenWhenModelStopsAtSearch(t *testing.T) {
	for _, name := range []string{"web_gather.search", "web_search"} {
		t.Run(name, func(t *testing.T) {
			search := tool.NewSuccess(modulewebgather.SearchResponse{Results: []modulewebgather.SearchResult{{URL: "https://example.org/source", Title: "actual source"}}})
			if name == "web_search" {
				search = tool.NewSuccess("search snippet")
				search.Metadata = map[string]any{"search_items": []map[string]any{{"link": "https://example.org/source"}}}
			}
			runner := &webResearchRunnerStub{metadata: webResearchMetadata(name, "web_gather.fetch"), results: map[string]*tool.ToolResponse{
				name:               search,
				"web_gather.fetch": tool.NewSuccess(modulewebgather.FetchResponse{URL: "https://example.org/source", Status: "ok", HTTPStatus: 200, ExtractedChars: 300, TextPreview: "body actually fetched"}),
			}}
			provider := &mockToolCallingProvider{responses: []llm.ChatResponse{researchCall("search", name, map[string]any{"query": "source"}), {Message: llm.ChatMessage{Role: "assistant", Content: "done"}}}}
			evidence, err := NewWebResearcher(runner, nil).Gather(webResearchContext(t), provider, llm.GenerateRequest{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(runner.calls) != 2 || runner.calls[1] != "web_gather.fetch" || runner.args[1]["url"] != "https://example.org/source" || runner.args[1]["store_staging"] != false {
				t.Fatalf("search did not reach actual fetch: calls=%v args=%v", runner.calls, runner.args)
			}
			if !strings.Contains(evidence[0].Content, "body actually fetched") {
				t.Fatal("body result missing from final evidence")
			}
		})
	}
}

func TestWebResearchFetchFallbackHonorsGrantEmptyResultsAndFailure(t *testing.T) {
	for _, tc := range []struct {
		name       string
		urls       []string
		allowed    []string
		fetch      *tool.ToolResponse
		wantCalls  int
		wantStatus string
	}{
		{"private-search-only", []string{"https://example.org/source"}, []string{"web_gather.search"}, nil, 1, "not_attempted"},
		{"empty", nil, nil, nil, 1, "not_attempted"},
		{"unsafe-sources", []string{"file:///etc/passwd", "https://user:password@example.org/", "javascript:alert(1)", "http://127.0.0.1/private", "http://192.168.1.1/private"}, nil, nil, 1, "not_attempted"},
		{"upstream-failure", []string{"https://example.org/source"}, nil, tool.NewError(tool.ErrNotFound, "HTTP 451", nil), 2, "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var items []modulewebgather.SearchResult
			for _, u := range tc.urls {
				items = append(items, modulewebgather.SearchResult{URL: u})
			}
			runner := &webResearchRunnerStub{metadata: webResearchMetadata("web_gather.search", "web_gather.fetch"), results: map[string]*tool.ToolResponse{"web_gather.search": tool.NewSuccess(modulewebgather.SearchResponse{Results: items}), "web_gather.fetch": tc.fetch}}
			provider := &mockToolCallingProvider{responses: []llm.ChatResponse{researchCall("search", "web_gather.search", map[string]any{"query": "source"}), {Message: llm.ChatMessage{Role: "assistant", Content: "done"}}}}
			evidence, err := NewWebResearcher(runner, nil).Gather(webResearchContext(t), provider, llm.GenerateRequest{}, tc.allowed)
			if err != nil || len(runner.calls) != tc.wantCalls {
				t.Fatalf("calls=%v err=%v", runner.calls, err)
			}
			if !strings.Contains(evidence[0].Content, `"status":"`+tc.wantStatus+`"`) {
				t.Fatalf("wrong body state: %s", evidence[0].Content)
			}
		})
	}
}

func TestWebResearchReservesLastCallForSourceFetch(t *testing.T) {
	runner := &webResearchRunnerStub{metadata: webResearchMetadata("web_gather.search", "web_gather.fetch"), results: map[string]*tool.ToolResponse{
		"web_gather.search": tool.NewSuccess(modulewebgather.SearchResponse{Results: []modulewebgather.SearchResult{{URL: "https://example.org/source"}}}),
		"web_gather.fetch":  tool.NewSuccess(modulewebgather.FetchResponse{URL: "https://example.org/source", Status: "ok", HTTPStatus: 200, ExtractedChars: 10, TextPreview: "body"}),
	}}
	var responses []llm.ChatResponse
	for _, query := range []string{"one", "two", "three", "four", "five", "six"} {
		responses = append(responses, researchCall(query, "web_gather.search", map[string]any{"query": query}))
	}
	responses = append(responses, llm.ChatResponse{Message: llm.ChatMessage{Role: "assistant", Content: "done"}})
	provider := &mockToolCallingProvider{responses: responses}
	if _, err := NewWebResearcher(runner, nil).Gather(webResearchContext(t), provider, llm.GenerateRequest{}, nil); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 6 || runner.calls[5] != "web_gather.fetch" {
		t.Fatalf("call budget did not include fetch: %v", runner.calls)
	}
}

func webResearchContext(t *testing.T) context.Context {
	t.Helper()
	scope, err := tool.NewToolExecutionScope("research-test", tool.ActorKindAgent, "midori", "", []string{tool.DataScopePublic}, tool.AuthenticationSourceAgentOrchestrator)
	if err != nil {
		t.Fatal(err)
	}
	return tool.WithToolExecutionScope(context.Background(), scope)
}

func webResearchMetadata(names ...string) []tool.ToolMetadata {
	var metadata []tool.ToolMetadata
	for _, name := range names {
		metadata = append(metadata, tool.ToolMetadata{ToolID: name, Category: "query", Description: name, Parameters: map[string]any{"type": "object"}})
	}
	return metadata
}

func researchCall(id, name string, args map[string]any) llm.ChatResponse {
	return llm.ChatResponse{Message: llm.ChatMessage{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: id, Function: llm.ToolCallFunction{Name: name, Arguments: args}}}}, FinishReason: "tool_calls"}
}

func TestWebResearchSearchFetchUseActualResultsAndCallerScope(t *testing.T) {
	provider := &mockToolCallingProvider{responses: []llm.ChatResponse{
		researchCall("search", "web_gather.search", map[string]any{"query": "official model"}),
		researchCall("fetch", "web_gather.fetch", map[string]any{"url": "https://example.org/source"}),
		{Message: llm.ChatMessage{Role: "assistant", Content: "not evidence: invented number 999999"}},
	}}
	runner := &webResearchRunnerStub{metadata: webResearchMetadata("web_gather.search", "web_gather.fetch", "shell")}
	request := llm.GenerateRequest{Messages: []llm.Message{{Role: "system", Content: "fixed prefix", Type: llm.PromptContextCharacter}, {Role: "user", Content: "検索して"}}}
	evidence, err := NewWebResearcher(runner, nil).Gather(webResearchContext(t), provider, request, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 2 || runner.calls[1] != "web_gather.fetch" || runner.args[1]["store_staging"] != false {
		t.Fatalf("actual calls: %v, args: %v", runner.calls, runner.args)
	}
	if len(evidence) != 1 || evidence[0].Role != "user" || evidence[0].Type != llm.PromptContextVariable || !strings.Contains(evidence[0].Content, "actual evidence") || strings.Contains(evidence[0].Content, "999999") {
		t.Fatalf("evidence must contain Tool results only: %#v", evidence)
	}
	for _, req := range provider.requests {
		if req.Messages[0].Content != "fixed prefix" || req.Messages[0].Type != llm.PromptContextCharacter {
			t.Fatal("character prefix changed")
		}
		for _, definition := range req.Tools {
			if definition.Function.Name == "shell" {
				t.Fatal("shell exposed to research")
			}
		}
	}
	if provider.requests[1].Messages[len(provider.requests[1].Messages)-1].ProviderToolCallID != "search" || provider.requests[2].Messages[len(provider.requests[2].Messages)-1].ProviderToolCallID != "fetch" {
		t.Fatal("Tool results lost provider call correlation")
	}
}

func TestWebResearchRejectsTextPretendingToHaveSearched(t *testing.T) {
	provider := &mockToolCallingProvider{responses: []llm.ChatResponse{{Message: llm.ChatMessage{Role: "assistant", Content: "I searched the web"}}}}
	runner := &webResearchRunnerStub{metadata: webResearchMetadata("web_search")}
	_, err := NewWebResearcher(runner, nil).Gather(webResearchContext(t), provider, llm.GenerateRequest{}, nil)
	if err == nil || !strings.Contains(err.Error(), "without executing") || len(runner.calls) != 0 {
		t.Fatalf("pretended search accepted: %v", err)
	}
}

func TestWebResearchOutputBudgetsAreIndependent(t *testing.T) {
	runner := &webResearchRunnerStub{metadata: webResearchMetadata("web_gather.fetch")}
	shared := NewWebResearcher(runner, nil)
	for _, tc := range []struct {
		name      string
		requested int
		want      int
	}{
		{"midori", 32768, 32768},
		{"shiro", 4096, 4096},
		{"aka-kin", 8192, 8192},
		{"short-chat", 512, 2048},
		{"unspecified", 0, 2048},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &mockToolCallingProvider{responses: []llm.ChatResponse{
				researchCall("fetch", "web_gather.fetch", map[string]any{"url": "https://example.org/source"}),
				{Message: llm.ChatMessage{Role: "assistant", Content: "done"}},
			}}
			if _, err := shared.Gather(webResearchContext(t), provider, llm.GenerateRequest{MaxTokens: tc.requested}, nil); err != nil {
				t.Fatal(err)
			}
			for _, request := range provider.requests {
				if request.MaxTokens != tc.want {
					t.Fatalf("output budget = %d, want %d", request.MaxTokens, tc.want)
				}
			}
		})
	}
}

func TestWebResearchEnforcesPrivateGrantAndRejectsUnadvertisedCalls(t *testing.T) {
	for _, allowed := range [][]string{{}, {"web_gather.fetch"}} {
		provider := &mockToolCallingProvider{responses: []llm.ChatResponse{researchCall("forbidden", "shell", nil), {Message: llm.ChatMessage{Role: "assistant", Content: "done"}}}}
		runner := &webResearchRunnerStub{metadata: webResearchMetadata("web_search", "web_gather.fetch", "shell")}
		_, err := NewWebResearcher(runner, nil).Gather(webResearchContext(t), provider, llm.GenerateRequest{}, allowed)
		if err == nil || len(runner.calls) != 0 {
			t.Fatalf("unauthorized tool executed: grant=%v calls=%v err=%v", allowed, runner.calls, err)
		}
	}
}

func TestWebResearchMissingScopeAndUnavailableRegistryDoNotCallModel(t *testing.T) {
	for _, ctx := range []context.Context{context.Background(), webResearchContext(t)} {
		provider := &mockToolCallingProvider{}
		_, err := NewWebResearcher(&webResearchRunnerStub{}, nil).Gather(ctx, provider, llm.GenerateRequest{}, nil)
		if err == nil || len(provider.requests) != 0 {
			t.Fatalf("unavailable research called model: %v", err)
		}
	}
}

func TestWebResearchRecordsFailureWithoutClaimingSuccess(t *testing.T) {
	provider := &mockToolCallingProvider{responses: []llm.ChatResponse{researchCall("fetch", "web_gather.fetch", nil), {Message: llm.ChatMessage{Role: "assistant", Content: "done"}}}}
	runner := &webResearchRunnerStub{metadata: webResearchMetadata("web_gather.fetch"), err: errors.New("upstream 403")}
	evidence, err := NewWebResearcher(runner, nil).Gather(webResearchContext(t), provider, llm.GenerateRequest{}, nil)
	if err != nil || len(evidence) != 1 || !strings.Contains(evidence[0].Content, "upstream 403") {
		t.Fatalf("failure evidence lost: %#v, %v", evidence, err)
	}
}

func TestWebResearchCachesIdenticalSuccessfulCallAndBlocksStaging(t *testing.T) {
	provider := &mockToolCallingProvider{responses: []llm.ChatResponse{
		researchCall("first", "web_gather.fetch", map[string]any{"url": "https://example.org"}),
		researchCall("same", "web_gather.fetch", map[string]any{"url": "https://example.org"}),
		{Message: llm.ChatMessage{Role: "assistant", Content: "done"}},
	}}
	runner := &webResearchRunnerStub{metadata: webResearchMetadata("web_gather.fetch")}
	if _, err := NewWebResearcher(runner, nil).Gather(webResearchContext(t), provider, llm.GenerateRequest{}, nil); err != nil || len(runner.calls) != 1 {
		t.Fatalf("duplicate fetch: calls=%v err=%v", runner.calls, err)
	}
	for _, value := range []any{true, "false", map[string]any{}} {
		provider := &mockToolCallingProvider{responses: []llm.ChatResponse{researchCall("write", "web_gather.fetch", map[string]any{"store_staging": value}), {Message: llm.ChatMessage{Role: "assistant", Content: "done"}}}}
		runner := &webResearchRunnerStub{metadata: webResearchMetadata("web_gather.fetch")}
		if _, err := NewWebResearcher(runner, nil).Gather(webResearchContext(t), provider, llm.GenerateRequest{}, nil); err == nil || len(runner.calls) != 0 {
			t.Fatalf("staging was not blocked: value=%v err=%v", value, err)
		}
	}
}
