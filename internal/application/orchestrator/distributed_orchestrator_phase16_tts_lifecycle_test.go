package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/turntiming"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

func TestPhase16DistributedTTSLifecycleUsesUpdatedTTSBridge(t *testing.T) {
	bridge := &mockTTSBridge{}
	lifecycle := newDistributedTTSLifecycle(nil, nil, func(eventType, from, to, content, route, taskID, sessionID, channel, chatID string) {})
	lifecycle.SetTTSBridge(bridge)

	req := ProcessMessageRequest{
		SessionID:   "sess-1",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "実行して",
	}
	taskID := modulecore.NewTaskID()
	decision := routing.NewDecision(routing.RouteOPS, 0.9, "ops")

	ttsSessionID := lifecycle.StartSessionForRoute(context.Background(), req, taskID, decision)
	wantSessionID := "sess-1-" + taskID.String()
	if ttsSessionID != wantSessionID {
		t.Fatalf("expected session %s, got %s", wantSessionID, ttsSessionID)
	}
	if len(bridge.startReqs) != 1 {
		t.Fatalf("expected one TTS start request, got %d", len(bridge.startReqs))
	}
	if bridge.startReqs[0].SessionID != wantSessionID {
		t.Fatalf("expected start request session %s, got %s", wantSessionID, bridge.startReqs[0].SessionID)
	}

	lifecycle.Push(context.Background(), ttsSessionID, routing.RouteOPS, "shiro", "agent.response", "完了しました")
	if len(bridge.pushes) == 0 {
		t.Fatal("expected TTS push after bridge update")
	}

	lifecycle.EndSession(context.Background(), ttsSessionID)
	if len(bridge.ended) != 1 || bridge.ended[0] != ttsSessionID {
		t.Fatalf("expected TTS end for %s, got %#v", ttsSessionID, bridge.ended)
	}
}

func TestPhase16DistributedTTSLifecyclePassesRequestTraceToTTSSession(t *testing.T) {
	bridge := &mockTTSBridge{}
	lifecycle := newDistributedTTSLifecycle(bridge, nil, func(eventType, from, to, content, route, taskID, sessionID, channel, chatID string) {})
	traceID := modulecore.NewTraceID()

	lifecycle.StartSessionForRoute(context.Background(), ProcessMessageRequest{
		TraceID: string(traceID), SessionID: "sess-trace", Channel: "viewer", ChatID: "viewer-user",
	}, modulecore.NewTaskID(), routing.NewDecision(routing.RouteCHAT, 0.9, "chat"))

	if len(bridge.startReqs) != 1 {
		t.Fatalf("expected one TTS start request, got %d", len(bridge.startReqs))
	}
	if bridge.startReqs[0].TraceID != string(traceID) {
		t.Fatalf("TTS start trace_id = %q, want parent %q", bridge.startReqs[0].TraceID, traceID)
	}
}

func TestPhase16DistributedTTSLifecycleStartFailureClearsSession(t *testing.T) {
	bridge := &mockTTSBridge{startErr: errors.New("tts down")}
	lifecycle := newDistributedTTSLifecycle(bridge, nil, func(eventType, from, to, content, route, taskID, sessionID, channel, chatID string) {})

	ttsSessionID := lifecycle.StartSessionForRoute(context.Background(), ProcessMessageRequest{
		SessionID:   "sess-1",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "実行して",
	}, modulecore.NewTaskID(), routing.NewDecision(routing.RouteCHAT, 0.9, "chat"))

	if ttsSessionID != "" {
		t.Fatalf("expected empty TTS session after start failure, got %s", ttsSessionID)
	}
	lifecycle.EndSession(context.Background(), ttsSessionID)
	if len(bridge.ended) != 0 {
		t.Fatalf("expected no EndSession after empty session, got %#v", bridge.ended)
	}
}

func TestPhase16DistributedTTSLifecycleSkipsRenCrowCMD(t *testing.T) {
	for _, tt := range []struct {
		name      string
		intent    AudioOutputIntent
		wantEmpty bool
	}{
		{name: "omitted", wantEmpty: true},
		{name: "explicit requested", intent: AudioOutputRequested, wantEmpty: false},
		{name: "explicit disabled", intent: AudioOutputDisabled, wantEmpty: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			bridge := &mockTTSBridge{}
			lifecycle := newDistributedTTSLifecycle(bridge, nil, func(eventType, from, to, content, route, taskID, sessionID, channel, chatID string) {})

			ttsSessionID := lifecycle.StartSessionForRoute(context.Background(), ProcessMessageRequest{
				SessionID: "viewer", Channel: "viewer", ChatID: "viewer-user", UserMessage: "おはようございます",
				OperationSource: "RenCrow_CMD", AudioOutput: tt.intent,
			}, modulecore.NewTaskID(), routing.NewDecision(routing.RouteCHAT, 0.98, "chat"))

			if (ttsSessionID == "") != tt.wantEmpty {
				t.Fatalf("ttsSessionID=%q wantEmpty=%t", ttsSessionID, tt.wantEmpty)
			}
			if (len(bridge.startReqs) == 0) != tt.wantEmpty {
				t.Fatalf("start requests=%d wantStarted=%t", len(bridge.startReqs), !tt.wantEmpty)
			}
		})
	}
}

func TestPhase16DistributedTTSLifecycleStreamHooksPreservePreviousCallbackWithoutPublicDeltas(t *testing.T) {
	var previous []string
	var emitted []string
	lifecycle := newDistributedTTSLifecycle(nil, nil, func(eventType, from, to, content, route, taskID, sessionID, channel, chatID string) {
		emitted = append(emitted, eventType+":"+content)
	})

	recorder := turntiming.New(time.Now())
	ctx := turntiming.WithRecorder(context.Background(), recorder)
	ctx = llm.ContextWithStreamCallback(ctx, func(token string) {
		previous = append(previous, token)
	})
	streamCtx, bundle := lifecycle.WithStreamHooks(ctx, routing.RouteCHAT, "mio", "tsk_00000000-0000-5000-8000-000000000024", "sess-1", "line", "U123", "")
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

	if len(previous) != 3 || previous[0] != "" || previous[1] != " \t" || previous[2] != "tok" {
		t.Fatalf("expected previous callback to receive token, got %#v", previous)
	}
	if len(emitted) != 0 {
		t.Fatalf("stream callbacks must not publish durable delta events, got %#v", emitted)
	}
	if _, ok := recorder.Snapshot().PointsMS[string(turntiming.PointFirstTextCallback)]; !ok {
		t.Fatal("first text callback was not timed")
	}
}

type distributedTTSContextKey struct{}

type distributedContextTTSBridge struct {
	pushed chan context.Context
}

func (b *distributedContextTTSBridge) StartSession(context.Context, TTSSessionStart) error {
	return nil
}

func (b *distributedContextTTSBridge) PushText(ctx context.Context, _ string, _ string, _ *moduletts.EmotionState) error {
	b.pushed <- ctx
	return nil
}

func (b *distributedContextTTSBridge) EndSession(context.Context, string) error {
	return nil
}

func TestPhase16DistributedTTSStreamPreservesRequestContext(t *testing.T) {
	bridge := &distributedContextTTSBridge{pushed: make(chan context.Context, 4)}
	lifecycle := newDistributedTTSLifecycle(bridge, nil, nil)
	ctx := context.WithValue(context.Background(), distributedTTSContextKey{}, "admission")
	_, bundle := lifecycle.WithStreamHooks(ctx, routing.RouteCHAT, "mio", "task-1", "session-1", "line", "U123", "tts-session-1")
	if bundle == nil {
		t.Fatal("expected stream bundle")
	}

	bundle.Finalize(context.Background(), "今日はいい天気ですね。少し歩いてから、温かいお茶を飲みましょう。")
	select {
	case pushCtx := <-bridge.pushed:
		if got := pushCtx.Value(distributedTTSContextKey{}); got != "admission" {
			t.Fatalf("TTS PushText context value = %#v, want preserved admission value", got)
		}
	case <-time.After(time.Second):
		t.Fatal("TTS PushText was not called")
	}
}
