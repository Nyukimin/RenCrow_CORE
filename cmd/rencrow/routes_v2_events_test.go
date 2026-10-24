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
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainsession "github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	sessionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/session"
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

func TestViewerGlobalEventsAllowsOnlyCurrentOwnerMioTerminalErrors(t *testing.T) {
	for _, runtimeVersion := range []string{"v1", "v2"} {
		t.Run(runtimeVersion, func(t *testing.T) {
			store, err := eventstore.NewSQLiteStore(filepath.Join(t.TempDir(), "terminal-errors.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()

			validID := modulecore.NewSessionID()
			validSession := routeTestPrivateErrorSession(t, validID, "owner-a", "cmd:owner-a", "2026-10-24")
			validBinding, err := domainsession.NewCanonicalSessionPublicationBinding(validSession)
			if err != nil {
				t.Fatal(err)
			}
			otherID := modulecore.NewSessionID()
			otherSession := routeTestPrivateErrorSession(t, otherID, "owner-b", "cmd:owner-b", "2026-10-24")
			publicID := modulecore.NewSessionID()
			publicSession, err := domainsession.NewCanonicalSession(publicID, "2026-10-24", routeTestViewerAddress(t, "viewer-user"), testRouteEventTime())
			if err != nil {
				t.Fatal(err)
			}
			staleID := modulecore.NewSessionID()
			staleIssued := routeTestPrivateErrorSession(t, staleID, "owner-a", "cmd:owner-a", "2026-10-24")
			staleCurrent, err := domainsession.NewCanonicalSessionWithOwner(staleID, "owner-a", "2026-10-24", routeTestViewerAddress(t, "cmd:owner-a"), testRouteEventTime().Add(time.Second))
			if err != nil {
				t.Fatal(err)
			}

			events := []orchestrator.OrchestratorEvent{
				routeTestPrivateMioTerminalError(validBinding, "current owner terminal error"),
				routeTestPrivateMioTerminalErrorFromSession(t, otherSession, "other owner terminal error"),
				routeTestPrivateMioTerminalErrorFromSession(t, publicSession, "public terminal error"),
				routeTestPrivateMioTerminalErrorFromSession(t, staleIssued, "old incarnation terminal error"),
			}
			nonTerminal := routeTestPrivateMioTerminalError(validBinding, "nonterminal event must stay closed")
			nonTerminal.Type = "agent.error"
			events = append(events, nonTerminal)
			for index := range events {
				events[index].EventSeq = modulecore.EventSeq(index + 1)
			}

			hub := viewer.NewEventHub(16)
			for _, event := range events {
				hub.OnEvent(event)
			}
			deps := &Dependencies{
				eventHub: hub,
				sessionRepository: routeTestSessionRepository{sessions: map[string]*domainsession.Session{
					string(validID):  validSession,
					string(otherID):  otherSession,
					string(publicID): publicSession,
					string(staleID):  staleCurrent,
				}},
				canonicalEventStore: &routeTestRuntimeVersionStore{
					SQLiteStore: store, sessionID: validID, ownerID: "owner-a", version: runtimeVersion, found: true,
				},
			}
			cfg := routeTestOwnerConfig(t)
			request := routeTestOwnerRequest("/viewer/events?to=mio", privateEventsOwnerToken)
			ctx, cancel := context.WithCancel(request.Context())
			defer cancel()
			response := &cancelRouteSSEWriter{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
			viewerGlobalEventsHandler(cfg, deps).ServeHTTP(response, request.WithContext(ctx))
			body := response.Body.String()
			if !strings.Contains(body, "current owner terminal error") {
				t.Fatalf("authenticated owner stream omitted bound %s terminal error: %s", runtimeVersion, body)
			}
			for _, denied := range []string{
				"other owner terminal error", "public terminal error", "old incarnation terminal error", "nonterminal event must stay closed",
			} {
				if strings.Contains(body, denied) {
					t.Fatalf("authenticated owner stream exposed %q: %s", denied, body)
				}
			}

			anonymous := httptest.NewRequest(http.MethodGet, "/viewer/events", nil)
			anonCtx, anonCancel := context.WithCancel(anonymous.Context())
			defer anonCancel()
			anonResponse := &cancelRouteSSEWriter{ResponseRecorder: httptest.NewRecorder(), cancel: anonCancel}
			viewerGlobalEventsHandler(cfg, deps).ServeHTTP(anonResponse, anonymous.WithContext(anonCtx))
			if strings.Contains(anonResponse.Body.String(), "current owner terminal error") {
				t.Fatalf("anonymous stream exposed private owner terminal error: %s", anonResponse.Body.String())
			}
		})
	}
}

func TestViewerGlobalEventsAllowsReservedV2OwnerTerminalError(t *testing.T) {
	store, err := eventstore.NewSQLiteStore(filepath.Join(t.TempDir(), "reserved-terminal-error.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	const ownerID = "owner-a"
	address := routeTestViewerAddress(t, "cmd:"+ownerID)
	repository := sessionpersistence.NewJSONSessionRepository(t.TempDir())
	if err := repository.WarmCanonicalIndex(context.Background()); err != nil {
		t.Fatalf("warm canonical Session index: %v", err)
	}
	reservedSession, _, err := repository.ReserveCanonicalV2(context.Background(), "2026-10-24", address, ownerID, testRouteEventTime())
	if err != nil {
		t.Fatalf("reserve canonical v2 Session: %v", err)
	}
	sessionID := modulecore.SessionID(reservedSession.ID())
	binding, err := domainsession.NewReservedV2ErrorPublicationBinding(reservedSession, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	event := routeTestPrivateMioTerminalError(binding, "reserved v2 terminal error")
	event.TaskID = "" // response-first acceptance has no execution TaskID before Mio issues one
	event.EventSeq = 1
	visibility := newRuntimeSessionVisibility(repository)
	resolved, allowed, reservedProof, err := visibility.ResolvePrivateTerminalErrorEvent(context.Background(), event, ownerID)
	if err != nil || !allowed || !reservedProof || !resolved.SameIdentity(binding) {
		t.Fatalf("resolve real reserved terminal binding=(%+v,%t,%t,%v), want exact reservation proof", resolved, allowed, reservedProof, err)
	}
	current, err := visibility.IsCurrentPrivateTerminalError(context.Background(), resolved)
	if err != nil || !current {
		t.Fatalf("real reserved terminal binding current=%t err=%v, want current", current, err)
	}
	hub := viewer.NewEventHub(8)
	hub.OnEvent(event)
	deps := &Dependencies{
		eventHub:          hub,
		sessionRepository: repository,
		canonicalEventStore: &routeTestRuntimeVersionStore{
			SQLiteStore: store, sessionID: sessionID, ownerID: ownerID, version: "v2", found: false,
		},
	}
	cfg := routeTestOwnerConfig(t)
	request := routeTestOwnerRequest("/viewer/events?to=mio", privateEventsOwnerToken)
	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	response := &cancelRouteSSEWriter{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
	viewerGlobalEventsHandler(cfg, deps).ServeHTTP(response, request.WithContext(ctx))
	if !strings.Contains(response.Body.String(), "reserved v2 terminal error") {
		t.Fatalf("authenticated owner stream omitted terminal error on the persisted v2 reservation: %s", response.Body.String())
	}
}

func TestViewerGlobalEventsRejectsReservedV2ErrorAfterSameIDRecreation(t *testing.T) {
	store, err := eventstore.NewSQLiteStore(filepath.Join(t.TempDir(), "reserved-terminal-error-recreate.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	const ownerID = "owner-a"
	address := routeTestViewerAddress(t, "cmd:"+ownerID)
	repository := sessionpersistence.NewJSONSessionRepository(t.TempDir())
	if err := repository.WarmCanonicalIndex(context.Background()); err != nil {
		t.Fatalf("warm canonical Session index: %v", err)
	}
	oldSession, _, err := repository.ReserveCanonicalV2(context.Background(), "2026-10-24", address, ownerID, testRouteEventTime())
	if err != nil {
		t.Fatalf("reserve canonical v2 Session: %v", err)
	}
	sessionID := modulecore.SessionID(oldSession.ID())
	binding, err := domainsession.NewReservedV2ErrorPublicationBinding(oldSession, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	event := routeTestPrivateMioTerminalError(binding, "stale reserved terminal error")
	event.TaskID = ""
	event.EventSeq = 1
	hub := viewer.NewEventHub(8)
	hub.OnEvent(event)
	versions := &routeTestRuntimeVersionStore{
		SQLiteStore: store, sessionID: sessionID, ownerID: ownerID, version: "v2", found: false,
		beforeReturn: func() {
			if err := repository.Delete(context.Background(), string(sessionID)); err != nil {
				t.Fatalf("delete reserved Session before final claim: %v", err)
			}
			recreated, err := domainsession.NewCanonicalSessionWithOwner(sessionID, ownerID, "2026-10-24", address, testRouteEventTime().Add(time.Second))
			if err != nil {
				t.Fatalf("create same-ID replacement Session: %v", err)
			}
			if err := repository.Save(context.Background(), recreated); err != nil {
				t.Fatalf("save same-ID replacement Session: %v", err)
			}
		},
	}
	deps := &Dependencies{eventHub: hub, sessionRepository: repository, canonicalEventStore: versions}
	ctx, cancel := context.WithCancel(routeTestOwnerRequest("/viewer/events?to=mio", privateEventsOwnerToken).Context())
	defer cancel()
	response := &cancelRouteSSEWriter{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
	viewerGlobalEventsHandler(routeTestOwnerConfig(t), deps).ServeHTTP(response, routeTestOwnerRequest("/viewer/events?to=mio", privateEventsOwnerToken).WithContext(ctx))
	if strings.Contains(response.Body.String(), "stale reserved terminal error") {
		t.Fatalf("authenticated owner stream exposed reserved error after same-ID recreation: %s", response.Body.String())
	}
}

func TestViewerGlobalEventsAllowsTasklessTerminalErrorOnlyForV2(t *testing.T) {
	for _, runtimeVersion := range []string{"v1", "v2"} {
		t.Run(runtimeVersion, func(t *testing.T) {
			store, err := eventstore.NewSQLiteStore(filepath.Join(t.TempDir(), "taskless-terminal-error.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()

			const ownerID = "owner-a"
			sessionID := modulecore.NewSessionID()
			session := routeTestPrivateErrorSession(t, sessionID, ownerID, "cmd:"+ownerID, "2026-10-24")
			binding, err := domainsession.NewCanonicalSessionPublicationBinding(session)
			if err != nil {
				t.Fatal(err)
			}
			event := routeTestPrivateMioTerminalError(binding, "taskless accepted v2 error")
			event.TaskID = ""
			event.EventSeq = 1
			hub := viewer.NewEventHub(4)
			hub.OnEvent(event)
			deps := &Dependencies{
				eventHub: hub,
				sessionRepository: routeTestSessionRepository{sessions: map[string]*domainsession.Session{
					string(sessionID): session,
				}},
				canonicalEventStore: &routeTestRuntimeVersionStore{
					SQLiteStore: store, sessionID: sessionID, ownerID: ownerID, version: runtimeVersion, found: true,
				},
			}
			request := routeTestOwnerRequest("/viewer/events?to=mio", privateEventsOwnerToken)
			ctx, cancel := context.WithCancel(request.Context())
			defer cancel()
			response := &cancelRouteSSEWriter{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
			viewerGlobalEventsHandler(routeTestOwnerConfig(t), deps).ServeHTTP(response, request.WithContext(ctx))
			if got := strings.Contains(response.Body.String(), "taskless accepted v2 error"); got != (runtimeVersion == "v2") {
				t.Fatalf("taskless error admitted=%t for runtime=%s, body=%s", got, runtimeVersion, response.Body.String())
			}
		})
	}
}

func routeTestViewerAddress(t *testing.T, chatID string) domainconversation.ChannelAddress {
	t.Helper()
	address, err := domainconversation.NewChannelAddress("viewer", chatID)
	if err != nil {
		t.Fatal(err)
	}
	return address
}

func routeTestPrivateErrorSession(t *testing.T, id modulecore.SessionID, ownerID, chatID, logicalDate string) *domainsession.Session {
	t.Helper()
	session, err := domainsession.NewCanonicalSessionWithOwner(id, ownerID, logicalDate, routeTestViewerAddress(t, chatID), testRouteEventTime())
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func routeTestPrivateMioTerminalError(binding domainsession.CanonicalSessionPublicationBinding, content string) orchestrator.OrchestratorEvent {
	address := binding.ChannelAddress()
	event := orchestrator.NewEventWithTraceID(modulecore.NewTraceID(), "viewer.error", "system", "viewer", content,
		"CHAT", "", string(binding.SessionID()), address.ChannelType(), address.ExternalConversationID())
	event.Visibility = "private"
	event.PublicationIncarnation = binding.PublicationIncarnation()
	event.TaskID = modulecore.NewTaskID()
	event.TurnID = modulecore.NewTurnID()
	event.MessageID = modulecore.NewMessageID()
	return event
}

func routeTestPrivateMioTerminalErrorFromSession(t *testing.T, session *domainsession.Session, content string) orchestrator.OrchestratorEvent {
	t.Helper()
	binding, err := domainsession.NewCanonicalSessionPublicationBinding(session)
	if err != nil {
		t.Fatal(err)
	}
	return routeTestPrivateMioTerminalError(binding, content)
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
