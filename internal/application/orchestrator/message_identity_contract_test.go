package orchestrator

import (
	"context"
	"errors"
	"reflect"
	"testing"

	domainagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	infrarouting "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/routing"
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

func TestProcessMessageKeepsSynchronousSessionLogErrorsBestEffort(t *testing.T) {
	sessionID := newMockCanonicalSessionID("cmd", "cmd-user")
	logger := &contextAwareRecordingTurnLogger{
		userErr:      errors.New("ignored synchronous user log error"),
		assistantErr: errors.New("ignored synchronous assistant log error"),
	}
	orch := NewMessageOrchestrator(
		newMockSessionRepository(),
		&mockMioAgent{
			decision: routing.NewDecision(routing.RouteCHAT, 1, "chat"),
			response: "hello back",
		},
		&mockShiroAgent{},
		nil, nil, nil, nil, nil,
	)
	attachCanonicalTestTaskOwner(t, orch)
	orch.SetSessionTurnLogger(logger)

	if _, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID: sessionID, Channel: "cmd", ChatID: "cmd-user", UserMessage: "hello",
	}); err != nil {
		t.Fatalf("ProcessMessage() error = %v, want synchronous log errors to stay best effort", err)
	}
	if len(logger.contexts) != 0 {
		t.Fatalf("context-aware session logger calls = %d, want none without RAM admission", len(logger.contexts))
	}
	if len(logger.turns) != 2 || logger.turns[0].role != "user" || logger.turns[1].role != "assistant" {
		t.Fatalf("synchronous session turns = %#v, want user + assistant legacy writes", logger.turns)
	}
}

type recordedCorrelatedTurn struct {
	role      string
	messageID string
	traceID   string
	taskID    string
	content   string
}

type recordingCorrelatedTurnLogger struct {
	turns []recordedCorrelatedTurn
}

func (l *recordingCorrelatedTurnLogger) WriteUser(sessionID, channel, content string) {
	l.turns = append(l.turns, recordedCorrelatedTurn{role: "user", content: content})
}

func (l *recordingCorrelatedTurnLogger) WriteAssistant(sessionID, channel, route, taskID, content string) {
	l.turns = append(l.turns, recordedCorrelatedTurn{role: "assistant", taskID: taskID, content: content})
}

func (l *recordingCorrelatedTurnLogger) WriteUserWithIdentity(sessionID, channel, messageID, traceID, content string) {
	l.turns = append(l.turns, recordedCorrelatedTurn{
		role: "user", messageID: messageID, traceID: traceID, content: content,
	})
}

func (l *recordingCorrelatedTurnLogger) WriteAssistantWithIdentity(sessionID, channel, route, taskID, messageID, traceID, content string) {
	l.turns = append(l.turns, recordedCorrelatedTurn{
		role: "assistant", messageID: messageID, traceID: traceID, taskID: taskID, content: content,
	})
}

type sessionLogContextKey struct{}

type contextAwareRecordingTurnLogger struct {
	recordingCorrelatedTurnLogger
	userErr      error
	assistantErr error
	contexts     []any
}

func (l *contextAwareRecordingTurnLogger) WriteUserWithIdentityContext(ctx context.Context, sessionID, channel, messageID, traceID, content string) error {
	l.contexts = append(l.contexts, ctx.Value(sessionLogContextKey{}))
	l.WriteUserWithIdentity(sessionID, channel, messageID, traceID, content)
	return l.userErr
}

func (l *contextAwareRecordingTurnLogger) WriteAssistantWithIdentityContext(ctx context.Context, sessionID, channel, route, taskID, messageID, traceID, content string) error {
	l.contexts = append(l.contexts, ctx.Value(sessionLogContextKey{}))
	l.WriteAssistantWithIdentity(sessionID, channel, route, taskID, messageID, traceID, content)
	return l.assistantErr
}

func TestProcessMessagePropagatesContextAwareSessionLogErrors(t *testing.T) {
	userLogErr := errors.New("user session log admission failed")
	assistantLogErr := errors.New("assistant session log admission failed")
	marker := "request admission context"
	tests := []struct {
		name       string
		logger     *contextAwareRecordingTurnLogger
		wantErr    error
		wantRoles  []string
		wantValues int
	}{
		{
			name:       "user admission error stops processing",
			logger:     &contextAwareRecordingTurnLogger{userErr: userLogErr},
			wantErr:    userLogErr,
			wantRoles:  []string{"user"},
			wantValues: 1,
		},
		{
			name:       "assistant admission error reaches caller",
			logger:     &contextAwareRecordingTurnLogger{assistantErr: assistantLogErr},
			wantErr:    assistantLogErr,
			wantRoles:  []string{"user", "assistant"},
			wantValues: 2,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			provider := &sequentialChatReplyProvider{responses: []string{`{"schema_version":2,"route":"CHAT","outcome":{"kind":"answer","text":"hello back"}}`}}
			manager := newRecordingTaskLifecycleManager()
			engine := &chatReplyConversationEngine{manager: manager}
			mio := domainagent.NewMioAgent(
				provider,
				infrarouting.NewLLMClassifier(provider, "current Mio classifier route policy"),
				chatReplyRuleDictionary{}, nil, nil, engine,
			).WithSystemPrompt("canonical Mio persona")
			orch := NewMessageOrchestrator(
				newMockSessionRepository(),
				mio,
				&mockShiroAgent{},
				nil, nil, nil, nil, nil,
			)
			orch.SetTaskLifecycleManager(manager)
			orch.SetSessionTurnLogger(tc.logger)

			ctx, _, _, _ := newEmptyFallbackContext(t)
			ctx = context.WithValue(ctx, sessionLogContextKey{}, marker)
			sessionID := newMockCanonicalSessionID("cmd", "cmd-user")
			_, err := orch.ProcessMessage(ctx, ProcessMessageRequest{
				SessionID: sessionID, Channel: "cmd", ChatID: "cmd-user", UserMessage: "hello",
			})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ProcessMessage error = %v, want %v", err, tc.wantErr)
			}
			if len(tc.logger.contexts) != tc.wantValues {
				t.Fatalf("context-aware logger calls = %d, want %d; contexts=%#v", len(tc.logger.contexts), tc.wantValues, tc.logger.contexts)
			}
			for _, value := range tc.logger.contexts {
				if value != marker {
					t.Fatalf("session logger context value = %#v, want %#v", value, marker)
				}
			}
			if len(tc.logger.turns) != len(tc.wantRoles) {
				t.Fatalf("session turns = %#v, want roles %v", tc.logger.turns, tc.wantRoles)
			}
			for i, role := range tc.wantRoles {
				if tc.logger.turns[i].role != role {
					t.Fatalf("session turn %d role = %q, want %q", i, tc.logger.turns[i].role, role)
				}
			}
		})
	}
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
	attachCanonicalTestTaskOwner(t, orch)
	events := &recordingEventListener{}
	turns := &recordingCorrelatedTurnLogger{}
	orch.SetEventListener(events)
	orch.SetSessionTurnLogger(turns)

	ingressTurnID := string(modulecore.NewTurnID())
	ingressTraceID := string(modulecore.NewTraceID())
	ingressRootTaskID := string(modulecore.NewTaskID())
	ingressMessageID := string(modulecore.NewMessageID())
	ingressAgentMessageID := string(modulecore.NewMessageID())
	sessionID := newMockCanonicalSessionID("viewer", "viewer-user")
	resp, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		RootTaskID: ingressRootTaskID, TurnID: ingressTurnID, TraceID: ingressTraceID,
		MessageID: ingressMessageID, AgentMessageID: ingressAgentMessageID,
		SessionID: sessionID, Channel: "viewer", ChatID: "viewer-user", UserMessage: "hello",
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
	if string(events.events[receivedIndex].MessageID) != ingressMessageID ||
		string(events.events[receivedIndex].TraceID) != resp.TraceID {
		t.Fatalf("ingress event identity drifted: %+v", events.events[receivedIndex])
	}
	if string(events.events[responseIndex].MessageID) != resp.MessageID ||
		string(events.events[responseIndex].TraceID) != resp.TraceID ||
		string(events.events[responseIndex].TurnID) != resp.TurnID ||
		string(events.events[responseIndex].SessionID) != resp.SessionID {
		t.Fatalf("response event identity drifted: response=%+v event=%+v", resp, events.events[responseIndex])
	}

	if len(turns.turns) != 2 {
		t.Fatalf("session turns = %#v", turns.turns)
	}
	if turns.turns[0].messageID != ingressMessageID || turns.turns[0].traceID != resp.TraceID {
		t.Fatalf("user session log identity drifted: %+v", turns.turns[0])
	}
	if turns.turns[1].messageID != resp.MessageID || turns.turns[1].traceID != resp.TraceID ||
		turns.turns[1].taskID != resp.TaskID {
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
		RootTaskID:     string(modulecore.NewTaskID()),
		TurnID:         string(modulecore.NewTurnID()),
		TraceID:        string(modulecore.NewTraceID()),
		MessageID:      string(modulecore.NewMessageID()),
		AgentMessageID: string(modulecore.NewMessageID()),
	}
	tests := []struct {
		name   string
		mutate func(*ProcessMessageRequest)
	}{
		{name: "root_task_id", mutate: func(req *ProcessMessageRequest) { req.RootTaskID = string(modulecore.NewMessageID()) }},
		{name: "turn_id", mutate: func(req *ProcessMessageRequest) { req.TurnID = string(modulecore.NewTraceID()) }},
		{name: "trace_id", mutate: func(req *ProcessMessageRequest) { req.TraceID = string(modulecore.NewTurnID()) }},
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
	taskID := string(modulecore.NewTaskID())
	actualAgentMessageID := modulecore.NewMessageID()
	tracker.BindResponseMessageID(taskID, actualAgentMessageID)

	actual := OrchestratorEvent{Type: "agent.response", From: "heavy", To: "mio", TaskID: modulecore.TaskID(taskID), SessionID: "session"}
	tracker.Assign(&actual, "")
	forwarded := OrchestratorEvent{Type: "agent.response", From: "mio", To: "user", TaskID: modulecore.TaskID(taskID), SessionID: "session"}
	tracker.Assign(&forwarded, "")

	if actual.MessageID != actualAgentMessageID {
		t.Fatalf("actual Agent message_id=%q, want prebound %q", actual.MessageID, actualAgentMessageID)
	}
	if modulecore.MessageID(forwarded.MessageID).Validate() != nil || forwarded.MessageID == actual.MessageID {
		t.Fatalf("forwarded message_id=%q must be a distinct canonical ID", forwarded.MessageID)
	}
	if got := tracker.TakeResponseMessageID(taskID); got != string(forwarded.MessageID) {
		t.Fatalf("user-visible response message_id=%q, want %q", got, forwarded.MessageID)
	}
}
