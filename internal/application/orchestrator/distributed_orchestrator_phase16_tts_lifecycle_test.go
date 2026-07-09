package orchestrator

import (
	"context"
	"errors"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

func TestPhase16DistributedTTSLifecycleUsesUpdatedTTSBridge(t *testing.T) {
	bridge := &mockTTSBridge{}
	lifecycle := newDistributedTTSLifecycle(nil, nil, func(eventType, from, to, content, route, jobID, sessionID, channel, chatID string) {})
	lifecycle.SetTTSBridge(bridge)

	req := ProcessMessageRequest{
		SessionID:   "sess-1",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "実行して",
	}
	jobID := task.NewJobID()
	decision := routing.NewDecision(routing.RouteOPS, 0.9, "ops")

	ttsSessionID := lifecycle.StartSessionForRoute(context.Background(), req, jobID, decision)
	wantSessionID := "sess-1-" + jobID.String()
	if ttsSessionID != wantSessionID {
		t.Fatalf("expected session %s, got %s", wantSessionID, ttsSessionID)
	}
	if len(bridge.startReqs) != 1 {
		t.Fatalf("expected one TTS start request, got %d", len(bridge.startReqs))
	}
	if bridge.startReqs[0].SessionID != wantSessionID {
		t.Fatalf("expected start request session %s, got %s", wantSessionID, bridge.startReqs[0].SessionID)
	}

	lifecycle.Push(context.Background(), ttsSessionID, routing.RouteOPS, "agent.response", "完了しました")
	if len(bridge.pushes) == 0 {
		t.Fatal("expected TTS push after bridge update")
	}

	lifecycle.EndSession(context.Background(), ttsSessionID)
	if len(bridge.ended) != 1 || bridge.ended[0] != ttsSessionID {
		t.Fatalf("expected TTS end for %s, got %#v", ttsSessionID, bridge.ended)
	}
}

func TestPhase16DistributedTTSLifecycleStartFailureClearsSession(t *testing.T) {
	bridge := &mockTTSBridge{startErr: errors.New("tts down")}
	lifecycle := newDistributedTTSLifecycle(bridge, nil, func(eventType, from, to, content, route, jobID, sessionID, channel, chatID string) {})

	ttsSessionID := lifecycle.StartSessionForRoute(context.Background(), ProcessMessageRequest{
		SessionID:   "sess-1",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "実行して",
	}, task.NewJobID(), routing.NewDecision(routing.RouteCHAT, 0.9, "chat"))

	if ttsSessionID != "" {
		t.Fatalf("expected empty TTS session after start failure, got %s", ttsSessionID)
	}
	lifecycle.EndSession(context.Background(), ttsSessionID)
	if len(bridge.ended) != 0 {
		t.Fatalf("expected no EndSession after empty session, got %#v", bridge.ended)
	}
}

func TestPhase16DistributedTTSLifecycleStreamHooksPreservePreviousCallbackAndEmitThinking(t *testing.T) {
	var previous []string
	var emitted []string
	lifecycle := newDistributedTTSLifecycle(nil, nil, func(eventType, from, to, content, route, jobID, sessionID, channel, chatID string) {
		emitted = append(emitted, eventType+":"+content)
	})

	ctx := llm.ContextWithStreamCallback(context.Background(), func(token string) {
		previous = append(previous, token)
	})
	streamCtx, bundle := lifecycle.WithStreamHooks(ctx, routing.RouteCHAT, "job-1", "sess-1", "line", "U123", "")
	if bundle == nil {
		t.Fatal("expected stream bundle")
	}

	callback := llm.StreamCallbackFromContext(streamCtx)
	if callback == nil {
		t.Fatal("expected stream callback")
	}
	callback("tok")

	if len(previous) != 1 || previous[0] != "tok" {
		t.Fatalf("expected previous callback to receive token, got %#v", previous)
	}
	if len(emitted) != 1 || emitted[0] != "agent.thinking:tok" {
		t.Fatalf("expected thinking event for token, got %#v", emitted)
	}
}
