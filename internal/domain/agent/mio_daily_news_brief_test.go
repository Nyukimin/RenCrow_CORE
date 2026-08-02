package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domainnews "github.com/Nyukimin/RenCrow_CORE/internal/domain/newsbrief"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

func TestMioAgentChatUsesPreparedDailyNewsBriefWithoutSearch(t *testing.T) {
	searchCalled := false
	toolRunner := &mockToolRunner{
		executeV2Func: func(ctx context.Context, toolName string, args map[string]any) (*tool.ToolResponse, error) {
			if toolName == "web_search" {
				searchCalled = true
			}
			return tool.NewSuccess("unexpected search"), nil
		},
	}
	var captured []string
	provider := &mockLLMProvider{generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
		captured = make([]string, 0, len(req.Messages))
		for _, message := range req.Messages {
			captured = append(captured, message.Content)
		}
		return llm.GenerateResponse{Content: "prepared answer"}, nil
	}}
	mio := NewMioAgent(provider, &mockClassifier{}, &mockRuleDictionary{}, toolRunner, &mockMCPClient{}, nil)
	brief := domainnews.DailyNewsBrief{
		Date:             "2026-07-21",
		Source:           domainnews.SourceScheduled,
		Status:           domainnews.StatusReady,
		EnrichmentStatus: domainnews.EnrichmentReady,
		Items:            []domainnews.Item{{ID: "news-1", Title: "準備済み記事", Source: "公式RSS", Summary: "要約"}},
	}

	response, err := mio.Chat(WithDailyNewsBrief(context.Background(), brief), task.NewTask(task.NewJobID(), "今朝のニュースを教えて", "viewer", "viewer"))
	if err != nil || response != "prepared answer" {
		t.Fatalf("Chat response = %q, err=%v", response, err)
	}
	if searchCalled {
		t.Fatal("prepared brief must not trigger web_search")
	}
	joined := strings.Join(captured, "\n")
	if !strings.Contains(joined, "準備済み記事") || !strings.Contains(joined, "04:00 JST") {
		t.Fatalf("prepared brief prompt missing: %s", joined)
	}
}

func TestMioAgentChatDoesNotCollectMissingDailyNews(t *testing.T) {
	searchCalled := false
	toolRunner := &mockToolRunner{
		executeV2Func: func(ctx context.Context, toolName string, args map[string]any) (*tool.ToolResponse, error) {
			if toolName != "web_search" {
				t.Fatalf("unexpected tool: %s", toolName)
			}
			searchCalled = true
			return tool.NewSuccess("live news results"), nil
		},
	}
	mio := NewMioAgent(&mockLLMProvider{}, &mockClassifier{}, &mockRuleDictionary{}, toolRunner, &mockMCPClient{}, nil)
	_, err := mio.Chat(context.Background(), task.NewTask(task.NewJobID(), "今朝のニュースを教えて", "viewer", "viewer"))
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	if searchCalled {
		t.Fatal("Mio must not collect an implicit daily-news request")
	}
}
