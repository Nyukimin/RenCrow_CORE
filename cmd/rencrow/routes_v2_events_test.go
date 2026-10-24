package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	domainsession "github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestViewerGlobalEventsFiltersPinnedV2SessionAndPreservesLegacy(t *testing.T) {
	store, err := eventstore.NewSQLiteStore(filepath.Join(t.TempDir(), "global-events-filter.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessionID, turnID := modulecore.NewSessionID(), modulecore.NewTurnID()
	input := modulecore.NewEventEnvelope(modulecore.NewTraceID(), "", nil, "orchestrator", "conversation.user_message", testRouteEventTime(), map[string]any{
		"from": "user", "source_actor_id": "viewer-user", "to": "mio", "text": "input", "content": "input", "visibility": "public",
	})
	input.SessionID, input.TurnID, input.MessageID = sessionID, turnID, modulecore.NewMessageID()
	if _, err := store.AcceptTurn(context.Background(), eventstore.AcceptInput{
		OperationID: "global-filter-v2-pin", OwnerID: "ren", RuntimeVersion: "v2", ClientSubmissionID: "global-filter-submission",
		ActorID: "viewer-user", TriggerKind: "user_message", GenerationEpoch: 1, CreateNewSession: true, InputEvent: input,
		InputMessage: eventstore.MessageIndex{MessageID: input.MessageID, EventID: input.EventID, SessionID: sessionID, TurnID: turnID, FromActor: "viewer-user", ToKind: "agent", ToActor: "mio", Kind: "user_message", Visibility: "public"},
	}); err != nil {
		t.Fatalf("pin v2 session: %v", err)
	}
	legacySessionID, legacyTurnID := modulecore.NewSessionID(), modulecore.NewTurnID()
	legacyInput := modulecore.NewEventEnvelope(modulecore.NewTraceID(), "", nil, "orchestrator", "conversation.user_message", testRouteEventTime(), map[string]any{
		"from": "user", "source_actor_id": "viewer-user", "to": "mio", "text": "legacy input", "content": "legacy input", "visibility": "public",
	})
	legacyInput.SessionID, legacyInput.TurnID, legacyInput.MessageID = legacySessionID, legacyTurnID, modulecore.NewMessageID()
	if _, err := store.AcceptTurn(context.Background(), eventstore.AcceptInput{
		OperationID: "global-filter-v1-pin", OwnerID: "ren", RuntimeVersion: "v1", ClientSubmissionID: "global-filter-v1-submission",
		ActorID: "viewer-user", TriggerKind: "user_message", GenerationEpoch: 1, CreateNewSession: true, InputEvent: legacyInput,
		InputMessage: eventstore.MessageIndex{MessageID: legacyInput.MessageID, EventID: legacyInput.EventID, SessionID: legacySessionID, TurnID: legacyTurnID, FromActor: "viewer-user", ToKind: "agent", ToActor: "mio", Kind: "user_message", Visibility: "public"},
	}); err != nil {
		t.Fatalf("pin v1 session: %v", err)
	}
	v2Session := routeTestSessionWithID(t, sessionID, "", false)
	legacySession := routeTestSessionWithID(t, legacySessionID, "", false)

	hub := viewer.NewEventHub(10)
	v2 := orchestrator.NewEvent("entry.stage", "mio", "system", "v2 event must stay scoped", "CHAT", "", string(sessionID), "viewer", "viewer-user")
	v2.EventSeq = 1
	legacy := orchestrator.NewEvent("entry.stage", "chrome", "system", "legacy stream remains", "CHAT", "", "", "local", "chrome")
	legacy.EventSeq = 2
	legacyV1Response := orchestrator.NewEvent("agent.response", "mio", "user", "legacy v1 response remains", "CHAT", "", string(legacySessionID), "viewer", "viewer-user")
	legacyV1Response.EventSeq = 3
	legacyV1Response.SessionID, legacyV1Response.MessageID, legacyV1Response.TurnID = legacySessionID, modulecore.NewMessageID(), legacyTurnID
	legacyV1Response.PublicationIncarnation = legacySession.PublicationIncarnation()
	hub.OnEvent(v2)
	hub.OnEvent(legacy)
	hub.OnEvent(legacyV1Response)
	deps := &Dependencies{
		eventHub: hub,
		sessionRepository: routeTestSessionRepository{sessions: map[string]*domainsession.Session{
			string(sessionID):       v2Session,
			string(legacySessionID): legacySession,
		}},
		canonicalEventStore: store,
	}
	cfg := &config.Config{}
	cfg.LocalAgentOps.UserID = "ren"

	request := httptest.NewRequest(http.MethodGet, "/viewer/events", nil)
	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	recorder := &cancelRouteSSEWriter{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
	viewerGlobalEventsHandler(cfg, deps).ServeHTTP(recorder, request.WithContext(ctx))
	body := recorder.Body.String()
	if strings.Contains(body, "v2 event must stay scoped") {
		t.Fatalf("global legacy route exposed an event from a pinned v2 session: %s", body)
	}
	if strings.Contains(body, "legacy stream remains") {
		t.Fatalf("global legacy route exposed an ambiguous session-less conversation event: %s", body)
	}
	if !strings.Contains(body, "legacy v1 response remains") {
		t.Fatalf("global legacy route did not preserve a response from a v1-pinned session: %s", body)
	}
}

type cancelRouteSSEWriter struct {
	*httptest.ResponseRecorder
	cancel  context.CancelFunc
	flushed bool
}

func (w *cancelRouteSSEWriter) Flush() {
	w.ResponseRecorder.Flush()
	if !w.flushed {
		w.flushed = true
		w.cancel()
	}
}

func testRouteEventTime() time.Time {
	return time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
}
