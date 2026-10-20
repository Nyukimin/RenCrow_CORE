package orchestrator

import (
	"context"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/speech"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

type speechPlanWiringBridge struct {
	ttsSessionID string
	sessionID    string
	turnID       string
	speakerID    string
	plan         speech.SpeechPlan
}

func (*speechPlanWiringBridge) StartSession(context.Context, TTSSessionStart) error { return nil }
func (*speechPlanWiringBridge) PushText(context.Context, string, string, *moduletts.EmotionState) error {
	return nil
}
func (*speechPlanWiringBridge) EndSession(context.Context, string) error { return nil }
func (b *speechPlanWiringBridge) PushSpeechPlan(_ context.Context, ttsSessionID, sessionID, turnID, speakerID string, plan speech.SpeechPlan) error {
	b.ttsSessionID = ttsSessionID
	b.sessionID = sessionID
	b.turnID = turnID
	b.speakerID = speakerID
	b.plan = plan
	return nil
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
