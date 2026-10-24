package orchestrator

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/turntiming"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

func TestPhase10TTSLifecycleUsesUpdatedTTSBridge(t *testing.T) {
	bridge := &mockTTSBridge{}
	lifecycle := newMessageTTSLifecycle(nil, nil, func(eventType, from, to, content, route, taskID, sessionID, channel, chatID string) {})
	lifecycle.SetTTSBridge(bridge)

	req := ProcessMessageRequest{
		SessionID:   "sess-1",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "実行して",
	}
	taskID := modulecore.NewTaskID()
	decision := routing.NewDecision(routing.RouteOPS, 0.9, "ops")

	lifecycle.StartSessionForRoute(context.Background(), req, taskID, decision, "tts-1")
	if len(bridge.startReqs) != 1 {
		t.Fatalf("expected one TTS start request, got %d", len(bridge.startReqs))
	}
	if bridge.startReqs[0].SessionID != "tts-1" {
		t.Fatalf("expected session tts-1, got %s", bridge.startReqs[0].SessionID)
	}
	if bridge.startReqs[0].CanonicalSessionID != req.SessionID {
		t.Fatalf("canonical Session ID = %q, want resolved request Session ID %q", bridge.startReqs[0].CanonicalSessionID, req.SessionID)
	}

	lifecycle.Push(context.Background(), "tts-1", routing.RouteOPS, "shiro", "agent.response", "完了しました")
	if len(bridge.pushes) == 0 {
		t.Fatal("expected TTS push after bridge update")
	}

	lifecycle.EndSession(context.Background(), "tts-1")
	if len(bridge.ended) != 1 || bridge.ended[0] != "tts-1" {
		t.Fatalf("expected TTS end for tts-1, got %#v", bridge.ended)
	}
}

func TestPhase10TTSLifecyclePassesRequestTraceToTTSSession(t *testing.T) {
	bridge := &mockTTSBridge{}
	lifecycle := newMessageTTSLifecycle(bridge, nil, func(eventType, from, to, content, route, taskID, sessionID, channel, chatID string) {})
	traceID := modulecore.NewTraceID()

	lifecycle.StartSessionForRoute(context.Background(), ProcessMessageRequest{
		TraceID: string(traceID), SessionID: "sess-trace", Channel: "viewer", ChatID: "viewer-user",
	}, modulecore.NewTaskID(), routing.NewDecision(routing.RouteCHAT, 0.9, "chat"), "tts-trace")

	if len(bridge.startReqs) != 1 {
		t.Fatalf("expected one TTS start request, got %d", len(bridge.startReqs))
	}
	if bridge.startReqs[0].TraceID != string(traceID) {
		t.Fatalf("TTS start trace_id = %q, want parent %q", bridge.startReqs[0].TraceID, traceID)
	}
}

func TestPhase10TTSLifecycleStreamHooksEmitFirstTokenLatencyMetric(t *testing.T) {
	var metrics []OrchestratorEvent
	lifecycle := newMessageTTSLifecycle(nil, nil, func(eventType, from, to, content, route, taskID, sessionID, channel, chatID string) {
		metrics = append(metrics, NewEvent(eventType, from, to, content, route, taskID, sessionID, channel, chatID))
	})

	ctx := contextWithLatencyTrace(context.Background(), time.Now())
	streamCtx, _ := lifecycle.WithStreamHooks(ctx, routing.RouteCHAT, "mio", "tsk_00000000-0000-5000-8000-000000000021", "sess-1", "line", "U123", "")
	callback := llm.StreamCallbackFromContext(streamCtx)
	if callback == nil {
		t.Fatal("expected stream callback")
	}
	callback("tok")
	callback("en")

	count := 0
	for _, ev := range metrics {
		if ev.Type != "metrics.latency" {
			continue
		}
		var payload struct {
			Kind  string `json:"kind"`
			Point string `json:"point"`
		}
		if err := json.Unmarshal([]byte(ev.Content), &payload); err != nil {
			t.Fatalf("latency metric content should be JSON: %v", err)
		}
		if payload.Kind == "llm" && payload.Point == "first_token" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly one llm first_token metric, got %d events=%#v", count, metrics)
	}
}

func TestPhase10TTSLifecycleStreamHooksEmitBackendThroughputMetric(t *testing.T) {
	var events []OrchestratorEvent
	lifecycle := newMessageTTSLifecycle(nil, nil, func(eventType, from, to, content, route, taskID, sessionID, channel, chatID string) {
		events = append(events, NewEvent(eventType, from, to, content, route, taskID, sessionID, channel, chatID))
	})

	streamCtx, _ := lifecycle.WithStreamHooks(context.Background(), routing.RouteCHAT, "mio", "tsk_00000000-0000-5000-8000-000000000022", "sess-1", "line", "U123", "")
	callback := llm.GenerationMetricsCallbackFromContext(streamCtx)
	if callback == nil {
		t.Fatal("expected generation metrics callback")
	}
	callback(llm.GenerationMetrics{CompletionTokens: 7, TokensPerSecond: 51.1808})

	for _, ev := range events {
		if ev.Type != "metrics.latency" {
			continue
		}
		var payload latencyMetricPayload
		if err := json.Unmarshal([]byte(ev.Content), &payload); err != nil {
			t.Fatalf("metric content should be JSON: %v", err)
		}
		if payload.Kind == "llm" && payload.Point == "throughput" && payload.CompletionTokens == 7 && payload.TokensPerSecond == 51.1808 {
			return
		}
	}
	t.Fatalf("backend throughput metric not emitted: %#v", events)
}

func TestPhase10TTSLifecycleStreamHooksPreservePreviousCallbackWithoutPublicDeltas(t *testing.T) {
	var previous []string
	var emitted []string
	lifecycle := newMessageTTSLifecycle(nil, nil, func(eventType, from, to, content, route, taskID, sessionID, channel, chatID string) {
		emitted = append(emitted, eventType+":"+content)
	})

	recorder := turntiming.New(time.Now())
	ctx := turntiming.WithRecorder(context.Background(), recorder)
	ctx = llm.ContextWithStreamCallback(ctx, func(token string) {
		previous = append(previous, token)
	})
	streamCtx, bundle := lifecycle.WithStreamHooks(ctx, routing.RouteCHAT, "mio", "tsk_00000000-0000-5000-8000-000000000023", "sess-1", "line", "U123", "")
	if bundle == nil {
		t.Fatal("expected stream bundle")
	}

	callback := llm.StreamCallbackFromContext(streamCtx)
	if callback == nil {
		t.Fatal("expected stream callback")
	}
	callback("")
	callback(" \t")
	if _, ok := recorder.Snapshot().PointsMS[string(turntiming.PointFirstTextCallback)]; ok {
		t.Fatal("empty or whitespace-only callback should not mark first visible text")
	}
	callback("tok")
	callback("en")

	if len(previous) != 4 || previous[0] != "" || previous[1] != " \t" || previous[2] != "tok" || previous[3] != "en" {
		t.Fatalf("expected previous callback to receive token, got %#v", previous)
	}
	if len(emitted) != 0 {
		t.Fatalf("stream callbacks must not publish durable delta events, got %#v", emitted)
	}
	if _, ok := recorder.Snapshot().PointsMS[string(turntiming.PointFirstTextCallback)]; !ok {
		t.Fatal("first non-empty text callback was not timed")
	}
}

type ttsStreamContextKey struct{}

type ttsStreamPushObservation struct {
	value       any
	err         error
	deadline    time.Time
	hasDeadline bool
}

type blockingContextTTSBridge struct {
	started chan ttsStreamPushObservation
	release chan struct{}
}

func (b *blockingContextTTSBridge) StartSession(context.Context, TTSSessionStart) error {
	return nil
}

func (b *blockingContextTTSBridge) PushText(ctx context.Context, _ string, _ string, _ *moduletts.EmotionState) error {
	deadline, hasDeadline := ctx.Deadline()
	b.started <- ttsStreamPushObservation{
		value:       ctx.Value(ttsStreamContextKey{}),
		err:         ctx.Err(),
		deadline:    deadline,
		hasDeadline: hasDeadline,
	}
	<-b.release
	return nil
}

func (b *blockingContextTTSBridge) EndSession(context.Context, string) error {
	return nil
}

func TestPhase10TTSStreamPreservesAdmissionContextAfterRequestCancellationAndFinalizeJoins(t *testing.T) {
	bridge := &blockingContextTTSBridge{
		started: make(chan ttsStreamPushObservation, 4),
		release: make(chan struct{}),
	}
	lifecycle := newMessageTTSLifecycle(bridge, nil, nil)
	parentCtx, cancel := context.WithCancel(context.WithValue(context.Background(), ttsStreamContextKey{}, "ram-admission"))
	_, bundle := lifecycle.WithStreamHooks(parentCtx, routing.RouteCHAT, "mio", "task-1", "session-1", "line", "U123", "tts-session-1")
	cancel()
	if bundle == nil {
		t.Fatal("expected stream bundle")
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(bridge.release) }) }
	defer release()

	finalized := make(chan struct{})
	go func() {
		bundle.Finalize(context.Background(), "今日はいい天気ですね。少し歩いてから、温かいお茶を飲みましょう。")
		close(finalized)
	}()

	var observation ttsStreamPushObservation
	select {
	case observation = <-bridge.started:
	case <-time.After(time.Second):
		t.Fatal("TTS PushText was not called after parent cancellation")
	}
	if observation.value != "ram-admission" {
		t.Fatalf("TTS PushText context value = %#v, want preserved admission value", observation.value)
	}
	if observation.err != nil {
		t.Fatalf("TTS PushText context should survive parent cancellation, got err %v", observation.err)
	}
	if !observation.hasDeadline {
		t.Fatal("TTS PushText context must have a finite timeout")
	}
	if remaining := time.Until(observation.deadline); remaining <= 0 || remaining > 20*time.Second {
		t.Fatalf("TTS PushText deadline remaining = %v, want (0, 20s]", remaining)
	}
	select {
	case <-finalized:
		t.Fatal("Finalize returned before TTS PushText completed")
	default:
	}

	release()
	select {
	case <-finalized:
	case <-time.After(time.Second):
		t.Fatal("Finalize did not return after TTS PushText completed")
	}
}
