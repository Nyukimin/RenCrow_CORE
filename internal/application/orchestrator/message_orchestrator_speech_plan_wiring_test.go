package orchestrator

import (
	"context"
	"strings"
	"testing"

	domainagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/speech"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

type speechPlanWiringBridge struct {
	startCalls    int
	startIdentity domainexecution.Identity
	startErr      error
	pushIdentity  domainexecution.Identity
	pushErr       error
	ttsSessionID  string
	sessionID     string
	turnID        string
	speakerID     string
	plan          speech.SpeechPlan
}

func (b *speechPlanWiringBridge) StartSession(ctx context.Context, _ TTSSessionStart) error {
	b.startCalls++
	b.startIdentity, b.startErr = domainexecution.IdentityFromContext(ctx)
	return nil
}
func (*speechPlanWiringBridge) PushText(context.Context, string, string, *moduletts.EmotionState) error {
	return nil
}
func (*speechPlanWiringBridge) EndSession(context.Context, string) error { return nil }
func (b *speechPlanWiringBridge) PushSpeechPlan(ctx context.Context, ttsSessionID, sessionID, turnID, speakerID string, plan speech.SpeechPlan) error {
	b.pushIdentity, b.pushErr = domainexecution.IdentityFromContext(ctx)
	b.ttsSessionID = ttsSessionID
	b.sessionID = sessionID
	b.turnID = turnID
	b.speakerID = speakerID
	b.plan = plan
	return nil
}

func TestMessageOrchestratorStartsSpeechPlanWithCanonicalExecutionIdentity(t *testing.T) {
	rootTaskID := modulecore.NewTaskID()
	traceID := modulecore.NewTraceID()
	manager := newRecordingTaskLifecycleManager()
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCHAT, 1, "chat"),
		chatFunc: func(ctx context.Context, _ conversation.TurnInput) (string, error) {
			callback := domainagent.SpeechPlanCallbackFromContext(ctx)
			if callback == nil {
				t.Fatal("CHAT route did not install SpeechPlan callback")
			}
			callback(speech.Result{
				Valid:       true,
				DisplayText: "canonical speech identity",
				Plan: speech.SpeechPlan{
					SchemaVersion: speech.SchemaVersion,
					Utterances:    []speech.Utterance{{Segments: []speech.Segment{{Text: "canonical speech identity"}}}},
				},
			})
			return "canonical speech identity", nil
		},
	}
	bridge := &speechPlanWiringBridge{}
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	orch.SetTaskLifecycleManager(manager)
	orch.SetTTSBridge(bridge)
	orch.SetSpeechPlanEnabled(true)

	_, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		RootTaskID:  rootTaskID.String(),
		TraceID:     string(traceID),
		SessionID:   string(modulecore.NewSessionID()),
		Channel:     "viewer",
		ChatID:      "viewer-user",
		To:          "mio",
		UserMessage: "speak with canonical identity",
	})
	if err != nil {
		t.Fatalf("ProcessMessage() error = %v", err)
	}
	if bridge.startCalls != 1 {
		t.Fatalf("TTS StartSession calls = %d, want 1", bridge.startCalls)
	}
	if bridge.startErr != nil || bridge.startIdentity.TaskID != rootTaskID || bridge.startIdentity.TraceID != traceID {
		t.Fatalf("TTS StartSession identity = %#v, error=%v; want task=%s trace=%s", bridge.startIdentity, bridge.startErr, rootTaskID, traceID)
	}
	if err := bridge.startIdentity.RunID.Validate(); err != nil {
		t.Fatalf("TTS StartSession run identity %q is invalid: %v", bridge.startIdentity.RunID, err)
	}
	if bridge.pushErr != nil || bridge.pushIdentity != bridge.startIdentity {
		t.Fatalf("SpeechPlan push identity = %#v, error=%v; want startup identity %#v", bridge.pushIdentity, bridge.pushErr, bridge.startIdentity)
	}
	run, ok := manager.runs[bridge.startIdentity.RunID]
	if !ok || run.TaskID != rootTaskID || run.Status != domaintask.RunStatusSucceeded {
		t.Fatalf("canonical TTS run = %#v, exists=%v; want completed run for task %s", run, ok, rootTaskID)
	}
}

func TestMessageOrchestratorRejectsPreboundForeignIdentityBeforeTTSStart(t *testing.T) {
	rootTaskID := modulecore.NewTaskID()
	traceID := modulecore.NewTraceID()
	foreignCtx, err := domainexecution.WithIdentity(context.Background(), modulecore.NewTaskID(), modulecore.NewRunID(), modulecore.NewTraceID())
	if err != nil {
		t.Fatal(err)
	}
	bridge := &speechPlanWiringBridge{}
	mio := &mockMioAgent{decision: routing.NewDecision(routing.RouteCHAT, 1, "chat"), response: "unused"}
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	orch.SetTaskLifecycleManager(newRecordingTaskLifecycleManager())
	orch.SetTTSBridge(bridge)

	_, err = orch.ProcessMessage(foreignCtx, ProcessMessageRequest{
		RootTaskID:  rootTaskID.String(),
		TraceID:     string(traceID),
		SessionID:   string(modulecore.NewSessionID()),
		Channel:     "viewer",
		ChatID:      "viewer-user",
		To:          "mio",
		UserMessage: "reject foreign identity",
	})
	if err == nil || !strings.Contains(err.Error(), "execution identity is already bound") {
		t.Fatalf("ProcessMessage() error = %v, want prebound identity mismatch", err)
	}
	if bridge.startCalls != 0 {
		t.Fatalf("TTS StartSession calls = %d, want 0 after identity mismatch", bridge.startCalls)
	}
}

func TestMessageOrchestratorWiresSpeechPlanPusherThroughLifecycle(t *testing.T) {
	orch := NewMessageOrchestrator(nil, nil, nil, nil, nil, nil, nil, nil)
	bridge := &speechPlanWiringBridge{}
	orch.SetTTSBridge(bridge)
	pusher := orch.routeDispatcher.pushSpeechPlan
	if pusher == nil {
		t.Fatal("MessageOrchestrator did not wire its SpeechPlan pusher")
	}
	plan := speech.SpeechPlan{SchemaVersion: speech.SchemaVersion, Utterances: []speech.Utterance{{Segments: []speech.Segment{{Text: "経路確認。"}}}}}
	sessionID := string(modulecore.NewSessionID())
	turnID := string(modulecore.NewTurnID())
	if err := pusher(context.Background(), "tts-session", sessionID, turnID, "mio", plan); err != nil {
		t.Fatalf("SpeechPlan pusher error = %v", err)
	}
	if bridge.ttsSessionID != "tts-session" || bridge.sessionID != sessionID || bridge.turnID != turnID || bridge.speakerID != "mio" {
		t.Fatalf("SpeechPlan identity = tts=%q session=%q turn=%q speaker=%q", bridge.ttsSessionID, bridge.sessionID, bridge.turnID, bridge.speakerID)
	}
	if len(bridge.plan.Utterances) != 1 || bridge.plan.Utterances[0].Segments[0].Text != "経路確認。" {
		t.Fatalf("SpeechPlan = %#v", bridge.plan)
	}
}
