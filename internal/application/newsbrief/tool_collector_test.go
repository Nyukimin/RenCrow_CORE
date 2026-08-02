package newsbrief

import (
	"context"
	"testing"
	"time"

	domainnews "github.com/Nyukimin/RenCrow_CORE/internal/domain/newsbrief"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
)

func TestToolCollectorPrefersSearchAndFetchAndKeepsSourceSeparate(t *testing.T) {
	runner := &fakeToolRunner{
		tools: []tool.ToolMetadata{{ToolID: "web_gather.search_and_fetch"}, {ToolID: "web_search"}},
		response: tool.NewSuccess(modulewebgather.SearchAndFetchResponse{
			Query: "今日のニュース 日本",
			Items: []modulewebgather.SearchAndFetchItem{{
				SearchResult: modulewebgather.SearchResult{Title: "記事タイトル", URL: "https://example.com/news", Snippet: "候補"},
				Fetch:        modulewebgather.FetchResponse{Status: "ok", FinalURL: "https://example.com/news", Title: "記事タイトル", TextPreview: "本文の抜粋"},
			}},
		}),
	}
	collector := NewToolCollector(runner)
	now := time.Date(2026, 8, 2, 8, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	brief, err := collector.Collect(context.Background(), "今日のニュース", now)
	if err != nil {
		t.Fatalf("Collect failed: %v", err)
	}
	if runner.called != "web_gather.search_and_fetch" {
		t.Fatalf("called tool = %q", runner.called)
	}
	if brief.Source != domainnews.SourceLiveSearch || brief.Source == domainnews.SourceScheduled {
		t.Fatalf("source = %q, live result must not look scheduled", brief.Source)
	}
	if brief.EnrichmentStatus != "ready" || len(brief.Items) != 1 || brief.Items[0].SourceReadStatus != "fetched" {
		t.Fatalf("brief = %+v", brief)
	}
}

func TestToolCollectorFallsBackToWebSearchMetadata(t *testing.T) {
	runner := &fakeToolRunner{
		tools: []tool.ToolMetadata{{ToolID: "web_search"}},
		response: func() *tool.ToolResponse {
			resp := tool.NewSuccess("formatted")
			resp.Metadata = map[string]any{"search_items": []map[string]any{{
				"title": "検索記事", "link": "https://example.com/search", "snippet": "概要",
			}}}
			return resp
		}(),
	}
	brief, err := NewToolCollector(runner).Collect(context.Background(), "今日のニュース", time.Now())
	if err != nil {
		t.Fatalf("Collect failed: %v", err)
	}
	if runner.called != "web_search" || len(brief.Items) != 1 || brief.Items[0].SourceReadStatus != "search_result_only" {
		t.Fatalf("called=%q brief=%+v", runner.called, brief)
	}
	if brief.Status != "partial" {
		t.Fatalf("status = %q, want partial for snippet-only result", brief.Status)
	}
}

type fakeToolRunner struct {
	tools    []tool.ToolMetadata
	response *tool.ToolResponse
	called   string
}

func (r *fakeToolRunner) ExecuteV2(_ context.Context, name string, _ map[string]any) (*tool.ToolResponse, error) {
	r.called = name
	return r.response, nil
}

func (r *fakeToolRunner) ListTools(context.Context) ([]tool.ToolMetadata, error) {
	return r.tools, nil
}
