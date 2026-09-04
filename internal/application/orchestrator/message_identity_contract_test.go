package orchestrator

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestProcessMessageRejectsMalformedIdentityBeforeSessionState(t *testing.T) {
	sessionErr := errors.New("session repository must not be called")
	request := ProcessMessageRequest{
		SessionID:   "session-that-would-load",
		TraceID:     "wrong-trace",
		Channel:     "viewer",
		ChatID:      "viewer-user",
		UserMessage: "hello",
	}
	tests := []struct {
		name string
		run  func(SessionRepository) error
	}{
		{
			name: "local",
			run: func(repo SessionRepository) error {
				orch := NewMessageOrchestrator(repo, &mockMioAgent{}, &mockShiroAgent{}, nil, nil, nil, nil, nil)
				_, err := orch.ProcessMessage(context.Background(), request)
				return err
			},
		},
		{
			name: "distributed",
			run: func(repo SessionRepository) error {
				orch := NewDistributedOrchestrator(repo, &mockMioAgent{}, nil, nil, nil)
				_, err := orch.ProcessMessage(context.Background(), request)
				return err
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := newMockSessionRepository()
			repo.loadErr = sessionErr
			err := tc.run(repo)
			if err == nil {
				t.Fatal("malformed identity was accepted")
			}
			if errors.Is(err, sessionErr) {
				t.Fatalf("session repository ran before identity rejection: %v", err)
			}
		})
	}
}

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

	ingressTurnID := string(modulecore.NewTurnID())
	ingressTraceID := string(modulecore.NewTraceID())
	ingressRootTaskID := string(modulecore.NewTaskID())
	ingressMessageID := string(modulecore.NewMessageID())
	ingressAgentMessageID := string(modulecore.NewMessageID())
	resp, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		JobID: "job-fixed", TurnID: ingressTurnID, TraceID: ingressTraceID, RootTaskID: ingressRootTaskID,
		MessageID: ingressMessageID, AgentMessageID: ingressAgentMessageID,
		SessionID: "session-1", Channel: "viewer", ChatID: "viewer-user", UserMessage: "hello",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if resp.TurnID != ingressTurnID || resp.TraceID != ingressTraceID || resp.RootTaskID != ingressRootTaskID || resp.MessageID != ingressAgentMessageID {
		t.Fatalf("response identity = %+v", resp)
	}

	receivedIndex := indexOfEvent(events.events, "message.received", "user", "mio", "")
	responseIndex := indexOfEvent(events.events, "agent.response", "mio", "user", "CHAT")
	if receivedIndex < 0 || responseIndex < 0 {
		t.Fatalf("missing conversation events: %#v", events.events)
	}
	if events.events[receivedIndex].MessageID != ingressMessageID ||
		events.events[receivedIndex].TraceID != resp.TraceID {
		t.Fatalf("ingress event identity drifted: %+v", events.events[receivedIndex])
	}
	if events.events[responseIndex].MessageID != resp.MessageID ||
		events.events[responseIndex].TraceID != resp.TraceID {
		t.Fatalf("response event identity drifted: response=%+v event=%+v", resp, events.events[responseIndex])
	}

	if len(turns.turns) != 2 {
		t.Fatalf("session turns = %#v", turns.turns)
	}
	if turns.turns[0].messageID != ingressMessageID || turns.turns[0].traceID != resp.TraceID {
		t.Fatalf("user session log identity drifted: %+v", turns.turns[0])
	}
	if turns.turns[1].messageID != resp.MessageID || turns.turns[1].traceID != resp.TraceID ||
		turns.turns[1].jobID != resp.JobID {
		t.Fatalf("assistant session log identity drifted: %+v", turns.turns[1])
	}
}

func TestEnsureProcessRequestIdentityRejectsMalformedIngressWithoutRepair(t *testing.T) {
	req := ProcessMessageRequest{MessageID: string(modulecore.NewMessageID()), TraceID: "not-a-canonical-trace"}
	original := req

	if err := ensureProcessRequestIdentity(&req); err == nil {
		t.Fatal("malformed trace_id was accepted")
	}
	if !reflect.DeepEqual(req, original) {
		t.Fatalf("rejected request was partially repaired: got=%+v want=%+v", req, original)
	}
}

func TestEnsureProcessRequestIdentityRejectsEveryWrongCanonicalTypeWithoutRepair(t *testing.T) {
	valid := ProcessMessageRequest{
		TurnID:         string(modulecore.NewTurnID()),
		TraceID:        string(modulecore.NewTraceID()),
		RootTaskID:     string(modulecore.NewTaskID()),
		MessageID:      string(modulecore.NewMessageID()),
		AgentMessageID: string(modulecore.NewMessageID()),
	}
	tests := []struct {
		name   string
		mutate func(*ProcessMessageRequest)
	}{
		{name: "turn_id", mutate: func(req *ProcessMessageRequest) { req.TurnID = string(modulecore.NewTraceID()) }},
		{name: "trace_id", mutate: func(req *ProcessMessageRequest) { req.TraceID = string(modulecore.NewTurnID()) }},
		{name: "root_task_id", mutate: func(req *ProcessMessageRequest) { req.RootTaskID = string(modulecore.NewMessageID()) }},
		{name: "user_message_id", mutate: func(req *ProcessMessageRequest) { req.MessageID = string(modulecore.NewTaskID()) }},
		{name: "agent_message_id", mutate: func(req *ProcessMessageRequest) { req.AgentMessageID = string(modulecore.NewTraceID()) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := valid
			tc.mutate(&req)
			original := req
			if err := ensureProcessRequestIdentity(&req); err == nil {
				t.Fatalf("wrong canonical type for %s was accepted", tc.name)
			}
			if !reflect.DeepEqual(req, original) {
				t.Fatalf("rejected request was partially repaired: got=%+v want=%+v", req, original)
			}
		})
	}
}

func TestEnsureProcessRequestIdentityGeneratesFiveIndependentCanonicalIDs(t *testing.T) {
	var req ProcessMessageRequest
	if err := ensureProcessRequestIdentity(&req); err != nil {
		t.Fatalf("ensure identity: %v", err)
	}
	if modulecore.TurnID(req.TurnID).Validate() != nil || modulecore.TraceID(req.TraceID).Validate() != nil || modulecore.TaskID(req.RootTaskID).Validate() != nil || modulecore.MessageID(req.MessageID).Validate() != nil || modulecore.MessageID(req.AgentMessageID).Validate() != nil {
		t.Fatalf("generated identity is not canonical: %+v", req)
	}
	seen := map[string]struct{}{}
	for _, value := range []string{req.TurnID, req.TraceID, req.RootTaskID, req.MessageID, req.AgentMessageID} {
		if _, duplicate := seen[value]; duplicate {
			t.Fatalf("generated identity was reused: %+v", req)
		}
		seen[value] = struct{}{}
	}
}

func TestEnsureProcessRequestIdentityRejectsAliasedMessageIDsWithoutRepair(t *testing.T) {
	messageID := string(modulecore.NewMessageID())
	req := ProcessMessageRequest{
		TurnID:         string(modulecore.NewTurnID()),
		TraceID:        string(modulecore.NewTraceID()),
		RootTaskID:     string(modulecore.NewTaskID()),
		MessageID:      messageID,
		AgentMessageID: messageID,
	}
	original := req
	if err := ensureProcessRequestIdentity(&req); err == nil {
		t.Fatal("aliased user and agent message IDs were accepted")
	}
	if !reflect.DeepEqual(req, original) {
		t.Fatalf("rejected request was partially repaired: got=%+v want=%+v", req, original)
	}
}

func TestConversationIdentityTrackerBindsOnlyFirstActualAgentResponse(t *testing.T) {
	tracker := newConversationIdentityTracker()
	jobID := "job-multi-actor"
	actualAgentMessageID := modulecore.NewMessageID()
	tracker.BindResponseMessageID(jobID, actualAgentMessageID)

	actual := OrchestratorEvent{Type: "agent.response", From: "heavy", To: "mio", JobID: jobID, SessionID: "session"}
	tracker.Assign(&actual, "")
	forwarded := OrchestratorEvent{Type: "agent.response", From: "mio", To: "user", JobID: jobID, SessionID: "session"}
	tracker.Assign(&forwarded, "")

	if actual.MessageID != string(actualAgentMessageID) {
		t.Fatalf("actual Agent message_id=%q, want prebound %q", actual.MessageID, actualAgentMessageID)
	}
	if modulecore.MessageID(forwarded.MessageID).Validate() != nil || forwarded.MessageID == actual.MessageID {
		t.Fatalf("forwarded message_id=%q must be a distinct canonical ID", forwarded.MessageID)
	}
	if got := tracker.TakeResponseMessageID(jobID); got != forwarded.MessageID {
		t.Fatalf("user-visible response message_id=%q, want %q", got, forwarded.MessageID)
	}
}
