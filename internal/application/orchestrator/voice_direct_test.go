package orchestrator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

func TestProcessVoiceDirect_EmitsRoutingDecisionAndAgentResponse(t *testing.T) {
	mio := &mockMioAgent{
		decideFunc: func(context.Context, task.Task) (routing.Decision, error) {
			t.Fatal("DecideAction must not be called for voice direct")
			return routing.Decision{}, nil
		},
		chatFunc: func(context.Context, task.Task) (string, error) {
			t.Fatal("Chat must not be called for voice direct")
			return "", nil
		},
	}
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	rec := &recordingEventListener{}
	orch.SetEventListener(rec)

	resp, err := orch.ProcessVoiceDirect(context.Background(), ProcessVoiceDirectRequest{
		UtteranceID: "utt-1",
		SessionID:   "viewer-session",
		Channel:     "viewer",
		ChatID:      "viewer-user",
		FinalText:   "おはよう",
		StartedAt:   time.Now(),
	})
	if err != nil {
		t.Fatalf("ProcessVoiceDirect failed: %v", err)
	}
	if resp.Route != routing.RouteCHAT || resp.Response != "おはよう" || resp.JobID == "" {
		t.Fatalf("unexpected response: %+v", resp)
	}

	decisionIdx := indexOfEvent(rec.events, "routing.decision", "mio", "", "CHAT")
	responseIdx := indexOfEvent(rec.events, "agent.response", "mio", "user", "CHAT")
	if decisionIdx < 0 || responseIdx < 0 {
		t.Fatalf("missing voice direct events: %#v", rec.events)
	}
	if decisionIdx >= responseIdx {
		t.Fatalf("routing.decision should precede agent.response: decision=%d response=%d", decisionIdx, responseIdx)
	}
	if !strings.Contains(rec.events[decisionIdx].Content, "surface=voice_chat") {
		t.Fatalf("routing.decision should mention voice_chat surface: %#v", rec.events[decisionIdx])
	}
	if !strings.Contains(rec.events[decisionIdx].Content, "evidence=voice_direct") {
		t.Fatalf("routing.decision should preserve voice_direct evidence: %#v", rec.events[decisionIdx])
	}
}

func TestProcessVoiceDirect_SplitsStructuredFinalIntoUserAndReply(t *testing.T) {
	orch := NewMessageOrchestrator(newMockSessionRepository(), &mockMioAgent{}, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	rec := &recordingEventListener{}
	orch.SetEventListener(rec)

	resp, err := orch.ProcessVoiceDirect(context.Background(), ProcessVoiceDirectRequest{
		UtteranceID: "utt-1",
		SessionID:   "viewer",
		Channel:     "viewer",
		ChatID:      "default",
		FinalText:   `{"user_text":"Mioさんいますか","reply":"はい、います。"}`,
	})
	if err != nil {
		t.Fatalf("ProcessVoiceDirect failed: %v", err)
	}
	if resp.Response != "はい、います。" {
		t.Fatalf("expected reply response, got %+v", resp)
	}
	userIdx := indexOfEvent(rec.events, "message.received", "user", "mio", "")
	responseIdx := indexOfEvent(rec.events, "agent.response", "mio", "user", "CHAT")
	if userIdx < 0 || responseIdx < 0 {
		t.Fatalf("missing split chat events: %#v", rec.events)
	}
	if rec.events[userIdx].Content != "Mioさんいますか" {
		t.Fatalf("expected user_text in message.received, got %#v", rec.events[userIdx])
	}
	if rec.events[responseIdx].Content != "はい、います。" {
		t.Fatalf("expected reply in agent.response, got %#v", rec.events[responseIdx])
	}
	for _, ev := range rec.events {
		if strings.Contains(ev.Content, `"user_text"`) {
			t.Fatalf("raw structured JSON leaked to chat event: %#v", ev)
		}
	}
}

func TestProcessVoiceDirect_EmitsLatencyMetrics(t *testing.T) {
	orch := NewMessageOrchestrator(newMockSessionRepository(), &mockMioAgent{}, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	rec := &recordingEventListener{}
	orch.SetEventListener(rec)

	startedAt := time.Now().Add(-120 * time.Millisecond)
	firstTokenAt := startedAt.Add(80 * time.Millisecond)
	_, err := orch.ProcessVoiceDirect(context.Background(), ProcessVoiceDirectRequest{
		UtteranceID:  "utt-1",
		SessionID:    "viewer-session",
		Channel:      "viewer",
		ChatID:       "viewer-user",
		FinalText:    "おはよう",
		StartedAt:    startedAt,
		FirstTokenAt: firstTokenAt,
	})
	if err != nil {
		t.Fatalf("ProcessVoiceDirect failed: %v", err)
	}
	for _, spec := range []struct {
		kind, point string
	}{
		{"network", "server_received"},
		{"llm", "route_decision"},
		{"llm", "dispatch_start"},
		{"llm", "first_token"},
		{"llm", "response_complete"},
	} {
		if !hasLatencyMetric(rec.events, spec.kind, spec.point) {
			t.Fatalf("missing latency metric kind=%s point=%s: %#v", spec.kind, spec.point, rec.events)
		}
	}
}

func TestProcessVoiceDirect_RejectsEmptyFinalText(t *testing.T) {
	orch := NewMessageOrchestrator(newMockSessionRepository(), &mockMioAgent{}, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	_, err := orch.ProcessVoiceDirect(context.Background(), ProcessVoiceDirectRequest{
		UtteranceID: "utt-1",
		Channel:     "viewer",
		FinalText:   " ",
	})
	if err == nil {
		t.Fatal("expected validation error for empty final text")
	}
}

func TestProcessVoiceDirect_RejectsNonViewerChannel(t *testing.T) {
	orch := NewMessageOrchestrator(newMockSessionRepository(), &mockMioAgent{}, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	_, err := orch.ProcessVoiceDirect(context.Background(), ProcessVoiceDirectRequest{
		UtteranceID: "utt-1",
		Channel:     "line",
		FinalText:   "hello",
	})
	if err == nil || !strings.Contains(err.Error(), "viewer channel") {
		t.Fatalf("expected viewer-only guard error, got %v", err)
	}
}

func TestNotifyVoiceDirectFirstToken_EmitsMetricOnce(t *testing.T) {
	orch := NewMessageOrchestrator(newMockSessionRepository(), &mockMioAgent{}, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	rec := &recordingEventListener{}
	orch.SetEventListener(rec)

	req := ProcessVoiceDirectRequest{
		UtteranceID: "utt-1",
		SessionID:   "viewer-session",
		Channel:     "viewer",
		ChatID:      "viewer-user",
		StartedAt:   time.Now().Add(-100 * time.Millisecond),
	}
	jobID := task.NewJobID()
	firstAt := req.StartedAt.Add(50 * time.Millisecond)
	orch.NotifyVoiceDirectFirstToken(context.Background(), req, jobID, firstAt)

	if !hasLatencyMetric(rec.events, "llm", "first_token") {
		t.Fatalf("missing first_token metric: %#v", rec.events)
	}
}

func TestProcessVoiceDirect_DoesNotRouteToIdleChat(t *testing.T) {
	idle := &phase8RecordingIdleNotifier{}
	orch := NewMessageOrchestrator(newMockSessionRepository(), &mockMioAgent{}, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	orch.SetIdleNotifier(idle)

	_, err := orch.ProcessVoiceDirect(context.Background(), ProcessVoiceDirectRequest{
		UtteranceID: "utt-1",
		Channel:     "viewer",
		FinalText:   "ok",
	})
	if err != nil {
		t.Fatalf("ProcessVoiceDirect failed: %v", err)
	}
	if len(idle.chatBusy) > 0 || len(idle.workerBusy) > 0 || idle.activities > 0 {
		t.Fatalf("ProcessVoiceDirect must not touch IdleChat notifier: %+v", idle)
	}
}
