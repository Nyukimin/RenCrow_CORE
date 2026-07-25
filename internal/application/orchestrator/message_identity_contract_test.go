package orchestrator

import (
	"context"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
)

type recordedCorrelatedTurn struct {
	role      string
	messageID string
	traceID   string
	jobID     string
	content   string
}

type recordingCorrelatedTurnLogger struct {
	turns []recordedCorrelatedTurn
}

func (l *recordingCorrelatedTurnLogger) WriteUser(sessionID, channel, content string) {
	l.turns = append(l.turns, recordedCorrelatedTurn{role: "user", content: content})
}

func (l *recordingCorrelatedTurnLogger) WriteAssistant(sessionID, channel, route, jobID, content string) {
	l.turns = append(l.turns, recordedCorrelatedTurn{role: "assistant", jobID: jobID, content: content})
}

func (l *recordingCorrelatedTurnLogger) WriteUserWithIdentity(sessionID, channel, messageID, traceID, content string) {
	l.turns = append(l.turns, recordedCorrelatedTurn{
		role: "user", messageID: messageID, traceID: traceID, content: content,
	})
}

func (l *recordingCorrelatedTurnLogger) WriteAssistantWithIdentity(sessionID, channel, route, jobID, messageID, traceID, content string) {
	l.turns = append(l.turns, recordedCorrelatedTurn{
		role: "assistant", messageID: messageID, traceID: traceID, jobID: jobID, content: content,
	})
}

func TestProcessMessagePreservesIdentityAcrossResponseEventsAndSessionLog(t *testing.T) {
	orch := NewMessageOrchestrator(
		newMockSessionRepository(),
		&mockMioAgent{
			decision: routing.NewDecision(routing.RouteCHAT, 1, "chat"),
			response: "hello back",
		},
		&mockShiroAgent{},
		nil, nil, nil, nil, nil,
	)
	events := &recordingEventListener{}
	turns := &recordingCorrelatedTurnLogger{}
	orch.SetEventListener(events)
	orch.SetSessionTurnLogger(turns)

	resp, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		JobID:       "job-fixed",
		MessageID:   "msg_ingress_fixed",
		TraceID:     "discarded-non-root-trace",
		SessionID:   "session-1",
		Channel:     "viewer",
		ChatID:      "viewer-user",
		UserMessage: "hello",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if resp.TraceID != "job-fixed" || resp.MessageID == "" {
		t.Fatalf("response identity = %+v", resp)
	}

	receivedIndex := indexOfEvent(events.events, "message.received", "user", "mio", "")
	responseIndex := indexOfEvent(events.events, "agent.response", "mio", "user", "CHAT")
	if receivedIndex < 0 || responseIndex < 0 {
		t.Fatalf("missing conversation events: %#v", events.events)
	}
	if events.events[receivedIndex].MessageID != "msg_ingress_fixed" ||
		events.events[receivedIndex].TraceID != "job-fixed" {
		t.Fatalf("ingress event identity drifted: %+v", events.events[receivedIndex])
	}
	if events.events[responseIndex].MessageID != resp.MessageID ||
		events.events[responseIndex].TraceID != resp.TraceID {
		t.Fatalf("response event identity drifted: response=%+v event=%+v", resp, events.events[responseIndex])
	}

	if len(turns.turns) != 2 {
		t.Fatalf("session turns = %#v", turns.turns)
	}
	if turns.turns[0].messageID != "msg_ingress_fixed" || turns.turns[0].traceID != "job-fixed" {
		t.Fatalf("user session log identity drifted: %+v", turns.turns[0])
	}
	if turns.turns[1].messageID != resp.MessageID || turns.turns[1].traceID != resp.TraceID ||
		turns.turns[1].jobID != resp.JobID {
		t.Fatalf("assistant session log identity drifted: %+v", turns.turns[1])
	}
}
