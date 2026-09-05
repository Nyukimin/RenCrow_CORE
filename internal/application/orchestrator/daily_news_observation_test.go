package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainllm "github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domainnews "github.com/Nyukimin/RenCrow_CORE/internal/domain/newsbrief"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestDailyNewsFallbackRetargetsCollectorAndKeepsMioAtRoot(t *testing.T) {
	rootTaskID := modulecore.NewTaskID()
	childTaskID := modulecore.NewTaskID()
	traceID := modulecore.NewTraceID()
	rootSessionID := "daily-observation-session"
	rootCtx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	rootCtx = withOrchestrationLLMObservation(rootCtx, rootTaskID, traceID, rootSessionID, "orchestrator.message")
	rootObservation, _ := domainllm.ExecutionObservationFromContext(rootCtx)

	var collectorObservation, mioObservation domainllm.ExecutionObservation
	var mioContext context.Context
	collector := &dailyNewsObservationCollector{observe: func(ctx context.Context) {
		collectorObservation, _ = domainllm.ExecutionObservationFromContext(ctx)
	}}
	mio := &mockMioAgent{chatFunc: func(ctx context.Context, _ domainconversation.TurnInput) (string, error) {
		mioContext = ctx
		mioObservation, _ = domainllm.ExecutionObservationFromContext(ctx)
		return "Mio response", nil
	}}
	activation := func(ctx context.Context) (modulecore.TaskID, error) {
		observation, ok := domainllm.ExecutionObservationFromContext(ctx)
		if !ok || observation.TaskID != rootTaskID {
			t.Fatalf("Shiro activation did not receive root observation: %+v", observation)
		}
		return childTaskID, nil
	}

	_, handled, err := respondWithDailyNewsBrief(
		rootCtx,
		ProcessMessageRequest{SessionID: rootSessionID, UserMessage: "今朝のニュース"},
		nil,
		dailyNewsObservationTurnInput(t, rootTaskID, traceID, rootSessionID),
		rootTaskID,
		time.Now(),
		domainnews.DailyNewsBrief{},
		errors.New("brief unavailable"),
		collector,
		mio,
		nil,
		func(string, string, string, string, string, string, string, string, string) {},
		func(context.Context, *session.Session, domainconversation.TurnInput) error { return nil },
		func(response string, decision routing.Decision, taskID modulecore.TaskID) ProcessMessageResponse {
			return ProcessMessageResponse{Response: response, Route: decision.Route, TaskID: taskID.String()}
		},
		activation,
	)
	if err != nil || !handled {
		t.Fatalf("daily news fallback = handled %v err %v", handled, err)
	}
	assertDailyNewsObservation(t, collectorObservation, childTaskID, rootObservation)
	assertDailyNewsObservation(t, mioObservation, rootTaskID, rootObservation)
	assertDailyNewsContextDeadline(t, rootCtx, collector.context, mioContext)
}

func TestDailyNewsFallbackRetargetsShiroWhenMioFails(t *testing.T) {
	rootTaskID := modulecore.NewTaskID()
	childTaskID := modulecore.NewTaskID()
	traceID := modulecore.NewTraceID()
	rootSessionID := "daily-shiro-observation-session"
	rootCtx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	rootCtx = withOrchestrationLLMObservation(rootCtx, rootTaskID, traceID, rootSessionID, "orchestrator.message")
	rootObservation, _ := domainllm.ExecutionObservationFromContext(rootCtx)

	var collectorObservation, shiroObservation domainllm.ExecutionObservation
	var shiroContext context.Context
	collector := &dailyNewsObservationCollector{observe: func(ctx context.Context) {
		collectorObservation, _ = domainllm.ExecutionObservationFromContext(ctx)
	}}
	mio := &mockMioAgent{chatFunc: func(context.Context, domainconversation.TurnInput) (string, error) {
		return "", errors.New("mio unavailable")
	}}
	shiro := &mockMioAgent{chatFunc: func(ctx context.Context, _ domainconversation.TurnInput) (string, error) {
		shiroContext = ctx
		shiroObservation, _ = domainllm.ExecutionObservationFromContext(ctx)
		return "Shiro response", nil
	}}
	activation := func(ctx context.Context) (modulecore.TaskID, error) {
		observation, ok := domainllm.ExecutionObservationFromContext(ctx)
		if !ok || observation.TaskID != rootTaskID {
			t.Fatalf("Shiro activation did not receive root observation: %+v", observation)
		}
		return childTaskID, nil
	}

	response, handled, err := respondWithDailyNewsBrief(
		rootCtx,
		ProcessMessageRequest{SessionID: rootSessionID, UserMessage: "今朝のニュース"},
		nil,
		dailyNewsObservationTurnInput(t, rootTaskID, traceID, rootSessionID),
		rootTaskID,
		time.Now(),
		domainnews.DailyNewsBrief{},
		errors.New("brief unavailable"),
		collector,
		mio,
		shiro,
		func(string, string, string, string, string, string, string, string, string) {},
		func(context.Context, *session.Session, domainconversation.TurnInput) error { return nil },
		func(response string, decision routing.Decision, taskID modulecore.TaskID) ProcessMessageResponse {
			return ProcessMessageResponse{Response: response, Route: decision.Route, TaskID: taskID.String()}
		},
		activation,
	)
	if err != nil || !handled || response.Response != "Shiro response" {
		t.Fatalf("daily news Shiro fallback = response %+v handled %v err %v", response, handled, err)
	}
	assertDailyNewsObservation(t, collectorObservation, childTaskID, rootObservation)
	assertDailyNewsObservation(t, shiroObservation, childTaskID, rootObservation)
	assertDailyNewsContextDeadline(t, rootCtx, collector.context, shiroContext)
}

type dailyNewsObservationCollector struct {
	observe func(context.Context)
	context context.Context
}

func (c *dailyNewsObservationCollector) Collect(ctx context.Context, _ string, _ time.Time) (domainnews.DailyNewsBrief, error) {
	c.context = ctx
	if c.observe != nil {
		c.observe(ctx)
	}
	return domainnews.DailyNewsBrief{
		Source:           domainnews.SourceLiveSearch,
		Status:           domainnews.StatusReady,
		EnrichmentStatus: domainnews.EnrichmentReady,
		Items:            []domainnews.Item{{ID: "live-1", Title: "記事", Source: "source"}},
	}, nil
}

func dailyNewsObservationTurnInput(t *testing.T, rootTaskID modulecore.TaskID, traceID modulecore.TraceID, sessionID string) domainconversation.TurnInput {
	t.Helper()
	address, err := domainconversation.NewChannelAddress("viewer", "daily-news")
	if err != nil {
		t.Fatalf("build daily news address: %v", err)
	}
	input, err := domainconversation.ReconstructTurnInput(
		rootTaskID,
		modulecore.NewTurnID(),
		traceID,
		modulecore.NewMessageID(),
		modulecore.NewMessageID(),
		"今朝のニュース",
		address,
	)
	if err != nil {
		t.Fatalf("build daily news input: %v", err)
	}
	return input.WithSessionID(sessionID)
}

func assertDailyNewsObservation(t *testing.T, got domainllm.ExecutionObservation, wantTaskID modulecore.TaskID, root domainllm.ExecutionObservation) {
	t.Helper()
	if got.TaskID != wantTaskID || got.TraceID != root.TraceID || got.SessionID != root.SessionID {
		t.Fatalf("daily news observation = %+v, want task=%s trace=%s session=%s", got, wantTaskID, root.TraceID, root.SessionID)
	}
	if got.RequestID == "" || got.RequestID != root.RequestID {
		t.Fatalf("daily news request ID changed: root=%q got=%q", root.RequestID, got.RequestID)
	}
}

func assertDailyNewsContextDeadline(t *testing.T, root context.Context, contexts ...context.Context) {
	t.Helper()
	rootDeadline, ok := root.Deadline()
	if !ok {
		t.Fatal("root context has no deadline")
	}
	for _, ctx := range contexts {
		deadline, ok := ctx.Deadline()
		if !ok || !deadline.Equal(rootDeadline) {
			t.Fatalf("derived context deadline = %v (present=%v), want %v", deadline, ok, rootDeadline)
		}
	}
}
