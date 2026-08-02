package orchestrator

import (
	"context"
	"testing"
	"time"

	domainnews "github.com/Nyukimin/RenCrow_CORE/internal/domain/newsbrief"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

func TestIsDailyNewsBriefRequest(t *testing.T) {
	tests := []struct {
		message string
		want    bool
	}{
		{message: "今朝のニュースを教えて", want: true},
		{message: "朝のニュースは？", want: true},
		{message: "今日のニュースを3件", want: true},
		{message: "2番を詳しく", want: true},
		{message: "今朝のニュースを検索して", want: false},
		{message: "最新のニュースを検索して", want: false},
		{message: "/chat 今朝のニュース", want: false},
	}
	for _, tt := range tests {
		if got := isDailyNewsBriefRequest(tt.message); got != tt.want {
			t.Errorf("isDailyNewsBriefRequest(%q) = %v, want %v", tt.message, got, tt.want)
		}
	}
}

func TestMessageOrchestratorDailyNewsBriefBypassesGenericRouting(t *testing.T) {
	repo := newMockSessionRepository()
	decideCalled := false
	mio := &mockMioAgent{
		decideFunc: func(context.Context, task.Task) (routing.Decision, error) {
			decideCalled = true
			return routing.NewDecision(routing.RouteRESEARCH, 0.5, "must not run"), nil
		},
		chatFunc: func(ctx context.Context, task task.Task) (string, error) {
			return "Mioの朝刊回答", nil
		},
	}
	orch := NewMessageOrchestrator(repo, mio, nil, nil, nil, nil, nil, nil)
	orch.SetDailyNewsBriefReader(domainnews.ReaderFunc(func(ctx context.Context, now time.Time) (domainnews.DailyNewsBrief, error) {
		return domainnews.DailyNewsBrief{
			Date:             domainnews.ExpectedMorningDate(now),
			Source:           domainnews.SourceScheduled,
			Status:           domainnews.StatusReady,
			EnrichmentStatus: domainnews.EnrichmentReady,
			Items:            []domainnews.Item{{ID: "news-1", Title: "朝刊記事", Source: "公式RSS"}},
		}, nil
	}))

	response, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "daily-news-test",
		Channel:     "viewer",
		ChatID:      "viewer",
		UserMessage: "今朝のニュースを教えて",
	})
	if err != nil {
		t.Fatalf("ProcessMessage returned error: %v", err)
	}
	if decideCalled {
		t.Fatal("daily_news_brief must bypass generic DecideAction")
	}
	if response.Route != routing.RouteCHAT || response.Response != "Mioの朝刊回答" {
		t.Fatalf("response = %+v", response)
	}
}

func TestMessageOrchestratorDailyNewsBriefFallbackEmitsRoleplayAndUsesCollector(t *testing.T) {
	repo := newMockSessionRepository()
	mio := &mockMioAgent{
		chatFunc: func(ctx context.Context, tk task.Task) (string, error) {
			return "Mioが収集結果を朗読", nil
		},
	}
	collector := &recordingDailyNewsCollector{brief: domainnews.DailyNewsBrief{
		Date:             "2026-08-02",
		Source:           domainnews.SourceLiveSearch,
		Status:           domainnews.StatusReady,
		EnrichmentStatus: domainnews.EnrichmentReady,
		Items:            []domainnews.Item{{ID: "live-1", Title: "最新記事", Source: "example.com"}},
	}}
	orch := NewMessageOrchestrator(repo, mio, nil, nil, nil, nil, nil, nil)
	orch.SetDailyNewsBriefReader(domainnews.ReaderFunc(func(context.Context, time.Time) (domainnews.DailyNewsBrief, error) {
		return domainnews.DailyNewsBrief{Status: domainnews.StatusEmpty, Items: []domainnews.Item{}}, nil
	}))
	orch.SetDailyNewsBriefCollector(collector)
	listener := &recordingEventListener{}
	orch.SetEventListener(listener)

	response, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "daily-news-fallback-test",
		Channel:     "viewer",
		ChatID:      "viewer",
		UserMessage: "今朝のニュースを教えて",
	})
	if err != nil {
		t.Fatalf("ProcessMessage returned error: %v", err)
	}
	if collector.calls != 1 {
		t.Fatalf("collector calls = %d, want 1", collector.calls)
	}
	if response.Response != "Mioが収集結果を朗読" {
		t.Fatalf("response = %q", response.Response)
	}
	if !hasEventRoute(listener.events, "agent.progress", "mio", "shiro") {
		t.Fatal("Mio -> Shiro news handoff progress event is missing")
	}
	if !hasEventRoute(listener.events, "agent.progress", "shiro", "mio") {
		t.Fatal("Shiro -> Mio news progress event is missing")
	}
}

type recordingDailyNewsCollector struct {
	calls int
	brief domainnews.DailyNewsBrief
}

func (c *recordingDailyNewsCollector) Collect(context.Context, string, time.Time) (domainnews.DailyNewsBrief, error) {
	c.calls++
	return c.brief, nil
}

func hasEventRoute(events []OrchestratorEvent, eventType, from, to string) bool {
	for _, event := range events {
		if event.Type == eventType && event.From == from && event.To == to {
			return true
		}
	}
	return false
}
