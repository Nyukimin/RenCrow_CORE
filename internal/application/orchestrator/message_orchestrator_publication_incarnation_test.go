package orchestrator

import (
	"context"
	"testing"
	"time"

	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	domainsession "github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestProcessMessageStampsGenericEventsWithResolvedSessionIncarnation(t *testing.T) {
	repository := newMockSessionRepository()
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCHAT, 1, "publication incarnation test"),
		response: "answer",
	}
	orch := NewMessageOrchestrator(repository, mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	attachCanonicalTestTaskOwner(t, orch)
	listener := &recordingEventListener{}
	orch.SetEventListener(listener)
	req := defaultReq()
	if _, err := orch.ProcessMessage(context.Background(), req); err != nil {
		t.Fatalf("ProcessMessage: %v", err)
	}
	sess := repository.sessions[req.SessionID]
	if sess == nil {
		t.Fatalf("ProcessMessage did not retain resolved Session %q", req.SessionID)
	}
	wantIncarnation := sess.PublicationIncarnation()
	wantTypes := map[string]bool{
		"message.received": false,
		"routing.decision": false,
		"agent.response":   false,
	}
	for _, event := range listener.events {
		if _, required := wantTypes[event.Type]; !required || event.SessionID != modulecore.SessionID(req.SessionID) {
			continue
		}
		if event.PublicationIncarnation != wantIncarnation {
			t.Errorf("%s incarnation=%q, want resolved Session incarnation %q", event.Type, event.PublicationIncarnation, wantIncarnation)
			continue
		}
		wantTypes[event.Type] = true
	}
	for eventType, found := range wantTypes {
		if !found {
			t.Errorf("no %s event carried the resolved Session incarnation", eventType)
		}
	}
}

func TestMessageEventPortStampsOnlyExactValidatedSessionBinding(t *testing.T) {
	for _, tc := range []struct {
		name     string
		ownerID  string
		audience domainconversation.SessionAudience
		chatID   string
	}{
		{name: "public", audience: domainconversation.SessionAudiencePublic, chatID: "viewer-user"},
		{name: "private", ownerID: "ren", audience: domainconversation.SessionAudiencePrivate, chatID: "cmd:ren"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			address, err := domainconversation.NewChannelAddress("viewer", tc.chatID)
			if err != nil {
				t.Fatal(err)
			}
			sessionID := modulecore.NewSessionID()
			createdAt := testPublicationIncarnationTime()
			var sess *domainsession.Session
			if tc.ownerID == "" {
				sess, err = domainsession.NewCanonicalSession(sessionID, "2026-10-24", address, createdAt)
			} else {
				sess, err = domainsession.NewCanonicalSessionWithOwner(sessionID, tc.ownerID, "2026-10-24", address, createdAt)
			}
			if err != nil {
				t.Fatalf("create canonical Session: %v", err)
			}
			binding, err := domainconversation.NewSessionBindingWithPublicationIncarnation(
				sessionID, address, tc.audience, tc.ownerID, domainconversation.BindingGenerationUnavailable, sess.PublicationIncarnation(),
			)
			if err != nil {
				t.Fatalf("NewSessionBindingWithPublicationIncarnation: %v", err)
			}
			listener := &recordingEventListener{}
			port := newMessageEventPort(listener)
			taskID, turnID, traceID := modulecore.NewTaskID(), modulecore.NewTurnID(), modulecore.NewTraceID()
			if err := port.BindConversationSessionBinding(taskID.String(), turnID, traceID, binding); err != nil {
				t.Fatalf("BindConversationSessionBinding: %v", err)
			}

			for _, eventType := range []string{"message.received", "routing.decision", "agent.response"} {
				port.Emit(eventType, "mio", "user", "test", "CHAT", taskID.String(), string(sessionID), "viewer", tc.chatID)
			}
			for i, event := range listener.events {
				if event.SessionID != sessionID || event.PublicationIncarnation != sess.PublicationIncarnation() {
					t.Errorf("generic event %q identity=(%s,%q), want (%s,%q)", event.Type, event.SessionID, event.PublicationIncarnation, sessionID, sess.PublicationIncarnation())
				}
				if i >= 3 {
					t.Fatalf("unexpected event count: %#v", listener.events)
				}
			}
			if len(listener.events) != 3 {
				t.Fatalf("generic event count=%d, want message.received/routing.decision/agent.response", len(listener.events))
			}

			stale := OrchestratorEvent{TaskID: taskID, SessionID: sessionID, PublicationIncarnation: "stale-incarnation"}
			port.traces.Apply(&stale)
			if stale.PublicationIncarnation != sess.PublicationIncarnation() {
				t.Fatalf("same-Session event incarnation=%q, want exact bound incarnation %q", stale.PublicationIncarnation, sess.PublicationIncarnation())
			}
			mismatched := OrchestratorEvent{TaskID: taskID, SessionID: modulecore.NewSessionID(), PublicationIncarnation: sess.PublicationIncarnation()}
			port.traces.Apply(&mismatched)
			if mismatched.PublicationIncarnation != "" {
				t.Fatalf("mismatched Session event retained incarnation %q", mismatched.PublicationIncarnation)
			}

			childTaskID := modulecore.NewTaskID()
			port.BindConversationIdentity(childTaskID.String(), turnID, traceID, sessionID)
			port.Emit("agent.response", "mio", "user", "child", "CHAT", childTaskID.String(), string(sessionID), "viewer", tc.chatID)
			if got := listener.events[len(listener.events)-1].PublicationIncarnation; got != sess.PublicationIncarnation() {
				t.Fatalf("same-turn child event incarnation=%q, want exact bound incarnation %q", got, sess.PublicationIncarnation())
			}
		})
	}

	listener := &recordingEventListener{}
	port := newMessageEventPort(listener)
	taskID, sessionID := modulecore.NewTaskID(), modulecore.NewSessionID()
	port.Emit("agent.response", "mio", "user", "unbound", "CHAT", taskID.String(), string(sessionID), "viewer", "viewer-user")
	if got := listener.events[0].PublicationIncarnation; got != "" {
		t.Fatalf("unbound event incarnation=%q, want fail-closed empty value", got)
	}
}

func testPublicationIncarnationTime() time.Time {
	return time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
}
