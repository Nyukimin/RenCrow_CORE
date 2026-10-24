package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainsession "github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const privateEventsOwnerToken = "private-events-owner-token-0123456789abcdef"

func TestViewerEventsAnonymousExcludesOwnerBoundAndAmbiguousSessionEvents(t *testing.T) {
	store, err := eventstore.NewSQLiteStore(filepath.Join(t.TempDir(), "events.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ownerSessionID, ownerSession := routeTestSession(t, "owner-a", true)
	legacySessionID, legacySession := routeTestSession(t, "legacy", false)
	hub := viewer.NewEventHub(10)
	events := []orchestrator.OrchestratorEvent{
		routeTestEvent("agent.response", "owner-bound private answer", ownerSessionID, ownerSession.PublicationIncarnation()),
		routeTestEvent("agent.response", "legacy public answer", legacySessionID, legacySession.PublicationIncarnation()),
		orchestrator.NewEvent("agent.response", "mio", "user", "ambiguous reply without session", "CHAT", "", "", "viewer", "viewer-user"),
		orchestrator.NewEvent("viewer.active_control", "viewer", "viewer", `{"action":"stop","kind":"audio"}`, "VIEWER", "", "", "", ""),
	}
	for _, event := range events {
		hub.OnEvent(event)
	}
	deps := &Dependencies{
		eventHub: hub,
		sessionRepository: routeTestSessionRepository{sessions: map[string]*domainsession.Session{
			string(ownerSessionID):  ownerSession,
			string(legacySessionID): legacySession,
		}},
		canonicalEventStore: store,
	}
	cfg := &config.Config{LocalAgentOps: config.LocalAgentOpsConfig{UserID: "owner-a"}}
	request := httptest.NewRequest(http.MethodGet, "/viewer/events", nil)
	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	response := &cancelRouteSSEWriter{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
	viewerGlobalEventsHandler(cfg, deps).ServeHTTP(response, request.WithContext(ctx))

	body := response.Body.String()
	for _, private := range []string{"owner-bound private answer", "ambiguous reply without session"} {
		if strings.Contains(body, private) {
			t.Fatalf("anonymous Viewer event stream exposed %q: %s", private, body)
		}
	}
	for _, public := range []string{"legacy public answer", "viewer.active_control", `\"action\":\"stop\"`} {
		if !strings.Contains(body, public) {
			t.Fatalf("anonymous Viewer event stream omitted allowed public event content %q: %s", public, body)
		}
	}
}

func TestViewerEventsAuthenticatedCMDIncludesOnlyMatchingOwnerSession(t *testing.T) {
	store, err := eventstore.NewSQLiteStore(filepath.Join(t.TempDir(), "events.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ownerSessionID, ownerSession := routeTestSession(t, "owner-a", true)
	otherSessionID, otherSession := routeTestSession(t, "owner-b", true)
	publicSessionID, publicSession := routeTestSession(t, "public", false)
	hub := viewer.NewEventHub(10)
	for _, event := range []orchestrator.OrchestratorEvent{
		routeTestEvent("agent.response", "owner-a private answer", ownerSessionID, ownerSession.PublicationIncarnation()),
		routeTestEvent("agent.response", "owner-b private answer", otherSessionID, otherSession.PublicationIncarnation()),
		routeTestEvent("agent.response", "public session answer", publicSessionID, publicSession.PublicationIncarnation()),
		orchestrator.NewEvent("viewer.active_control", "viewer", "viewer", "global control", "VIEWER", "", "", "", ""),
	} {
		hub.OnEvent(event)
	}
	cfg := routeTestOwnerConfig(t)
	deps := &Dependencies{
		eventHub: hub,
		sessionRepository: routeTestSessionRepository{sessions: map[string]*domainsession.Session{
			string(ownerSessionID):  ownerSession,
			string(otherSessionID):  otherSession,
			string(publicSessionID): publicSession,
		}},
		canonicalEventStore: store,
	}
	request := routeTestOwnerRequest("/viewer/events?to=mio", privateEventsOwnerToken)
	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	response := &cancelRouteSSEWriter{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
	viewerGlobalEventsHandler(cfg, deps).ServeHTTP(response, request.WithContext(ctx))

	body := response.Body.String()
	if !strings.Contains(body, "owner-a private answer") {
		t.Fatalf("authenticated CMD stream omitted the matching owner's answer: %s", body)
	}
	for _, private := range []string{"owner-b private answer", "public session answer", "global control"} {
		if strings.Contains(body, private) {
			t.Fatalf("authenticated CMD stream exposed out-of-scope event %q: %s", private, body)
		}
	}
}

func TestViewerEventsAuthenticatedCursorFromOtherOwnerRestartsWithinOwnerWindow(t *testing.T) {
	store, err := eventstore.NewSQLiteStore(filepath.Join(t.TempDir(), "events.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	archive, err := viewer.NewCanonicalEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	ownerSessionID, ownerSession := routeTestSession(t, "owner-a", true)
	otherSessionID, otherSession := routeTestSession(t, "owner-b", true)
	first, err := archive.AppendSequenced(routeTestEvent("agent.response", "owner-a replay answer", ownerSessionID, ownerSession.PublicationIncarnation()))
	if err != nil {
		t.Fatal(err)
	}
	otherCursor, err := archive.AppendSequenced(routeTestEvent("agent.response", "owner-b cursor answer", otherSessionID, otherSession.PublicationIncarnation()))
	if err != nil {
		t.Fatal(err)
	}
	if first.EventSeq >= otherCursor.EventSeq {
		t.Fatalf("seed event order = %d then %d, want increasing", first.EventSeq, otherCursor.EventSeq)
	}

	hub := viewer.NewEventHub(10)
	hub.SetReplayReader(archive)
	cfg := routeTestOwnerConfig(t)
	deps := &Dependencies{
		eventHub: hub,
		sessionRepository: routeTestSessionRepository{sessions: map[string]*domainsession.Session{
			string(ownerSessionID): ownerSession,
			string(otherSessionID): otherSession,
		}},
		canonicalEventStore: store,
	}
	request := routeTestOwnerRequest("/viewer/events?to=mio", privateEventsOwnerToken)
	request.Header.Set("Last-Event-ID", string(otherCursor.EventID))
	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	response := &cancelRouteSSEWriter{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
	viewerGlobalEventsHandler(cfg, deps).ServeHTTP(response, request.WithContext(ctx))

	body := response.Body.String()
	if !strings.Contains(body, "owner-a replay answer") {
		t.Fatalf("foreign cursor skipped the authorized owner window instead of resetting: %s", body)
	}
	if strings.Contains(body, "owner-b cursor answer") {
		t.Fatalf("authenticated CMD replay exposed the foreign cursor event: %s", body)
	}
	if !strings.Contains(body, "id: reset") {
		t.Fatalf("foreign cursor did not receive an authorized-window reset: %s", body)
	}
}

func TestViewerEventsAuthenticatedCMDFiltersLiveEvents(t *testing.T) {
	store, err := eventstore.NewSQLiteStore(filepath.Join(t.TempDir(), "events.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ownerSessionID, ownerSession := routeTestSession(t, "owner-a", true)
	otherSessionID, otherSession := routeTestSession(t, "owner-b", true)
	publicSessionID, publicSession := routeTestSession(t, "public", false)
	hub := viewer.NewEventHub(10)
	cfg := routeTestOwnerConfig(t)
	deps := &Dependencies{
		eventHub: hub,
		sessionRepository: routeTestSessionRepository{sessions: map[string]*domainsession.Session{
			string(ownerSessionID):  ownerSession,
			string(otherSessionID):  otherSession,
			string(publicSessionID): publicSession,
		}},
		canonicalEventStore: store,
	}
	request := routeTestOwnerRequest("/viewer/events?from=now&to=mio", privateEventsOwnerToken)
	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	response := &liveRouteSSEWriter{
		ResponseRecorder: httptest.NewRecorder(),
		cancel:           cancel,
		publish: func() {
			hub.OnEvent(routeTestEvent("agent.response", "owner-b live answer", otherSessionID, otherSession.PublicationIncarnation()))
			hub.OnEvent(routeTestEvent("agent.response", "public live answer", publicSessionID, publicSession.PublicationIncarnation()))
			hub.OnEvent(routeTestEvent("agent.response", "owner-a live answer", ownerSessionID, ownerSession.PublicationIncarnation()))
		},
	}
	viewerGlobalEventsHandler(cfg, deps).ServeHTTP(response, request.WithContext(ctx))

	body := response.Body.String()
	if !strings.Contains(body, "owner-a live answer") {
		t.Fatalf("authenticated CMD stream omitted the matching live event: %s", body)
	}
	for _, private := range []string{"owner-b live answer", "public live answer"} {
		if strings.Contains(body, private) {
			t.Fatalf("authenticated CMD live stream exposed %q: %s", private, body)
		}
	}
}

func TestViewerEventsRequiresLocalCMDChatOwnerBearer(t *testing.T) {
	for _, tc := range []struct {
		name       string
		client     string
		profile    string
		authority  []string
		remoteAddr string
		wantStatus int
	}{
		{name: "missing bearer", client: "RenCrow_CMD", profile: "cmd-chat", remoteAddr: "127.0.0.1:1234", wantStatus: http.StatusUnauthorized},
		{name: "invalid bearer", client: "RenCrow_CMD", profile: "cmd-chat", authority: []string{"Bearer invalid"}, remoteAddr: "127.0.0.1:1234", wantStatus: http.StatusUnauthorized},
		{name: "duplicate bearer", client: "RenCrow_CMD", profile: "cmd-chat", authority: []string{"Bearer " + privateEventsOwnerToken, "Bearer " + privateEventsOwnerToken}, remoteAddr: "127.0.0.1:1234", wantStatus: http.StatusUnauthorized},
		{name: "wrong profile", client: "RenCrow_CMD", profile: "cmd-diagnostics", authority: []string{"Bearer " + privateEventsOwnerToken}, remoteAddr: "127.0.0.1:1234", wantStatus: http.StatusForbidden},
		{name: "remote address", client: "RenCrow_CMD", profile: "cmd-chat", authority: []string{"Bearer " + privateEventsOwnerToken}, remoteAddr: "192.0.2.10:1234", wantStatus: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hub := viewer.NewEventHub(1)
			cfg := routeTestOwnerConfig(t)
			deps := &Dependencies{eventHub: hub}
			request := httptest.NewRequest(http.MethodGet, "/viewer/events?to=mio", nil)
			request.RemoteAddr = tc.remoteAddr
			if tc.remoteAddr == "127.0.0.1:1234" {
				request.Host = "127.0.0.1:18790"
			} else {
				request.Host = "core.example.test"
			}
			if tc.client != "" {
				request.Header.Set("X-RenCrow-Client", tc.client)
			}
			if tc.profile != "" {
				request.Header.Set("X-RenCrow-Interaction-Profile", tc.profile)
			}
			for _, value := range tc.authority {
				request.Header.Add("Authorization", value)
			}
			response := httptest.NewRecorder()
			viewerGlobalEventsHandler(cfg, deps).ServeHTTP(response, request)
			if response.Code != tc.wantStatus {
				t.Fatalf("status=%d body=%q want=%d", response.Code, response.Body.String(), tc.wantStatus)
			}
			if strings.Contains(response.Body.String(), privateEventsOwnerToken) {
				t.Fatal("event authorization response leaked the configured token")
			}
		})
	}
}

func TestViewerEventsSessionLookupFailureFailsClosed(t *testing.T) {
	store, err := eventstore.NewSQLiteStore(filepath.Join(t.TempDir(), "events.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessionID, session := routeTestSession(t, "owner-a", true)
	hub := viewer.NewEventHub(10)
	hub.OnEvent(routeTestEvent("agent.response", "must not leak after lookup failure", sessionID, session.PublicationIncarnation()))
	deps := &Dependencies{
		eventHub: hub,
		sessionRepository: routeTestSessionRepository{
			loadErr: errors.New("session owner lookup unavailable"),
		},
		canonicalEventStore: store,
	}
	request := httptest.NewRequest(http.MethodGet, "/viewer/events", nil)
	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	response := &cancelRouteSSEWriter{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
	viewerGlobalEventsHandler(&config.Config{}, deps).ServeHTTP(response, request.WithContext(ctx))
	if strings.Contains(response.Body.String(), "must not leak") {
		t.Fatalf("event stream exposed content after Session lookup failure: %s", response.Body.String())
	}
	if response.Code == http.StatusOK {
		t.Fatalf("event stream returned success after Session lookup failure: %s", response.Body.String())
	}
}

func TestViewerEventsCMDNonMioRecipientKeepsPublicSubscription(t *testing.T) {
	store, err := eventstore.NewSQLiteStore(filepath.Join(t.TempDir(), "events.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	publicSessionID, publicSession := routeTestSession(t, "legacy", false)
	privateSessionID, privateSession := routeTestSession(t, "owner-a", true)
	hub := viewer.NewEventHub(10)
	deps := &Dependencies{
		eventHub: hub,
		sessionRepository: routeTestSessionRepository{sessions: map[string]*domainsession.Session{
			string(publicSessionID):  publicSession,
			string(privateSessionID): privateSession,
		}},
		canonicalEventStore: store,
	}
	request := httptest.NewRequest(http.MethodGet, "/viewer/events?from=now&to=shiro", nil)
	request.RemoteAddr = "127.0.0.1:1234"
	request.Host = "127.0.0.1:18790"
	request.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
	request.Header.Set("X-RenCrow-Interaction-Profile", "cmd-chat")
	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	response := &liveRouteSSEWriter{
		ResponseRecorder: httptest.NewRecorder(),
		cancel:           cancel,
		publish: func() {
			hub.OnEvent(routeTestEvent("agent.response", "private Mio answer", privateSessionID, privateSession.PublicationIncarnation()))
			hub.OnEvent(routeTestEvent("agent.response", "public Shiro answer", publicSessionID, publicSession.PublicationIncarnation()))
		},
	}
	viewerGlobalEventsHandler(routeTestOwnerConfig(t), deps).ServeHTTP(response, request.WithContext(ctx))

	body := response.Body.String()
	if !strings.Contains(body, "public Shiro answer") {
		t.Fatalf("non-Mio CMD public subscription lost the legacy answer: %s", body)
	}
	if strings.Contains(body, "private Mio answer") {
		t.Fatalf("non-Mio CMD public subscription exposed an owner-bound answer: %s", body)
	}
}

func TestViewerEventsPrivateMioFinalUsesOwnerIncarnationAndVersionPolicy(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version string
		found   bool
	}{
		{name: "v2", version: "v2", found: true},
		{name: "v1", version: "v1", found: true},
		{name: "legacy", version: "legacy", found: true},
		{name: "unversioned legacy", found: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := eventstore.NewSQLiteStore(filepath.Join(t.TempDir(), "events.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			archive, err := viewer.NewCanonicalEventLog(store)
			if err != nil {
				t.Fatal(err)
			}

			sessionID := modulecore.NewSessionID()
			address, err := domainconversation.NewChannelAddress("viewer", "cmd:owner-a")
			if err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
			currentSession, err := domainsession.NewCanonicalSessionWithOwner(sessionID, "owner-a", "2026-10-24", address, now)
			if err != nil {
				t.Fatal(err)
			}
			oldSession, err := domainsession.NewCanonicalSessionWithOwner(sessionID, "owner-a", "2026-10-24", address, now)
			if err != nil {
				t.Fatal(err)
			}

			oldEvent, err := archive.AppendSequenced(routeTestPrivateMioFinal(sessionID, "owner-a", oldSession.PublicationIncarnation(), "old private final"))
			if err != nil {
				t.Fatal(err)
			}
			currentEvent, err := archive.AppendSequenced(routeTestPrivateMioFinal(sessionID, "owner-a", currentSession.PublicationIncarnation(), "current private final"))
			if err != nil {
				t.Fatal(err)
			}

			runtimeStore := &routeTestRuntimeVersionStore{
				SQLiteStore: store, sessionID: sessionID, ownerID: "owner-a", version: tc.version, found: tc.found,
			}
			cfg := routeTestOwnerConfig(t)
			deps := &Dependencies{
				eventHub: viewer.NewEventHub(10),
				sessionRepository: routeTestSessionRepository{sessions: map[string]*domainsession.Session{
					string(sessionID): currentSession,
				}},
				canonicalEventStore: runtimeStore,
			}
			deps.eventHub.SetReplayReader(archive)
			request := routeTestOwnerRequest("/viewer/events?to=mio", privateEventsOwnerToken)
			request.Header.Set("Last-Event-ID", string(modulecore.NewEventID()))
			ctx, cancel := context.WithCancel(request.Context())
			defer cancel()
			replayResponse := &cancelRouteSSEWriter{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
			viewerGlobalEventsHandler(cfg, deps).ServeHTTP(replayResponse, request.WithContext(ctx))
			if strings.Contains(replayResponse.Body.String(), "old private final") || !strings.Contains(replayResponse.Body.String(), "current private final") {
				t.Fatalf("private replay for %s did not enforce current incarnation: %s", tc.name, replayResponse.Body.String())
			}

			liveHub := viewer.NewEventHub(10)
			liveHub.SetReplayReader(archive)
			liveDeps := &Dependencies{
				eventHub: liveHub,
				sessionRepository: routeTestSessionRepository{sessions: map[string]*domainsession.Session{
					string(sessionID): currentSession,
				}},
				canonicalEventStore: runtimeStore,
			}
			liveRequest := routeTestOwnerRequest("/viewer/events?from=now&to=mio", privateEventsOwnerToken)
			liveCtx, liveCancel := context.WithCancel(liveRequest.Context())
			defer liveCancel()
			liveResponse := &liveRouteSSEWriter{
				ResponseRecorder: httptest.NewRecorder(),
				cancel:           liveCancel,
				publish: func() {
					if _, err := liveHub.PublishLive(oldEvent); err != nil {
						t.Errorf("publish stale private final: %v", err)
					}
					if _, err := liveHub.PublishLive(currentEvent); err != nil {
						t.Errorf("publish current private final: %v", err)
					}
				},
			}
			viewerGlobalEventsHandler(cfg, liveDeps).ServeHTTP(liveResponse, liveRequest.WithContext(liveCtx))
			if strings.Contains(liveResponse.Body.String(), "old private final") || !strings.Contains(liveResponse.Body.String(), "current private final") {
				t.Fatalf("private live stream for %s did not enforce current incarnation: %s", tc.name, liveResponse.Body.String())
			}
		})
	}
}

func TestRuntimeSessionVisibilityRejectsOldIncarnationAndLegacyEvents(t *testing.T) {
	sessionID, original := routeTestSession(t, "owner-a", true)
	repository := &countingRouteTestSessionRepository{
		routeTestSessionRepository: routeTestSessionRepository{sessions: map[string]*domainsession.Session{
			string(sessionID): original,
		}},
	}
	visibility := newRuntimeSessionVisibility(repository)
	ctx := context.Background()
	oldEvent := routeTestEvent("agent.response", "old publication", sessionID, original.PublicationIncarnation())
	oldBinding, allowed, err := visibility.ResolveEvent(ctx, oldEvent, "owner-a")
	if err != nil || !allowed {
		t.Fatalf("current event binding allowed=%t err=%v, want allowed", allowed, err)
	}
	if current, err := visibility.IsCurrent(ctx, oldBinding); err != nil || !current {
		t.Fatalf("initial binding current=%t err=%v, want current", current, err)
	}

	recreated := routeTestSessionWithID(t, sessionID, "owner-a", true)
	repository.sessions[string(sessionID)] = recreated
	if recreated.PublicationIncarnation() == original.PublicationIncarnation() {
		t.Fatal("same-ID replacement reused the previous publication incarnation")
	}
	if _, allowed, err := visibility.ResolveEvent(ctx, oldEvent, "owner-a"); err != nil || allowed {
		t.Fatalf("old incarnation event allowed=%t err=%v, want denied", allowed, err)
	}
	if current, err := visibility.IsCurrent(ctx, oldBinding); err != nil || current {
		t.Fatalf("old binding current=%t err=%v, want stale", current, err)
	}
	newEvent := routeTestEvent("agent.response", "new publication", sessionID, recreated.PublicationIncarnation())
	if _, allowed, err := visibility.ResolveEvent(ctx, newEvent, "owner-a"); err != nil || !allowed {
		t.Fatalf("new incarnation event allowed=%t err=%v, want allowed", allowed, err)
	}
	legacyEvent := routeTestEvent("agent.response", "legacy event without incarnation", sessionID)
	if _, allowed, err := visibility.ResolveEvent(ctx, legacyEvent, "owner-a"); err != nil || allowed {
		t.Fatalf("legacy event allowed=%t err=%v, want fail-closed denial", allowed, err)
	}
	if got := repository.publicationReads.Load(); got != 3 {
		t.Fatalf("canonical publication reads=%d, want 3 reads with no SessionID cache", got)
	}
}

func TestRuntimeSessionVisibilityFailsClosedWhenCanonicalProjectionUnavailable(t *testing.T) {
	sessionID, sess := routeTestSession(t, "owner-a", true)
	unknownRepository := &countingRouteTestSessionRepository{
		routeTestSessionRepository: routeTestSessionRepository{loadErr: errors.New("session source unavailable")},
	}
	visibility := newRuntimeSessionVisibility(unknownRepository)
	event := routeTestEvent("agent.response", "not visible", sessionID, sess.PublicationIncarnation())
	for range 2 {
		if _, allowed, err := visibility.ResolveEvent(context.Background(), event, "owner-a"); err == nil || allowed {
			t.Fatalf("unknown session result allowed=%t err=%v, want fail-closed lookup error", allowed, err)
		}
	}
	if got := unknownRepository.publicationReads.Load(); got != 2 {
		t.Fatalf("failed canonical publication reads=%d, want 2 uncached errors", got)
	}
}

type routeTestSessionRepository struct {
	sessions map[string]*domainsession.Session
	loadErr  error
}

type countingRouteTestSessionRepository struct {
	routeTestSessionRepository
	publicationReads atomic.Int32
}

func (r *countingRouteTestSessionRepository) Load(ctx context.Context, id string) (*domainsession.Session, error) {
	r.publicationReads.Add(1)
	return r.routeTestSessionRepository.Load(ctx, id)
}

func (r *countingRouteTestSessionRepository) CurrentPublicationBinding(ctx context.Context, id string) (domainsession.CanonicalSessionPublicationBinding, error) {
	r.publicationReads.Add(1)
	return r.routeTestSessionRepository.CurrentPublicationBinding(ctx, id)
}

func (r routeTestSessionRepository) Save(context.Context, *domainsession.Session) error { return nil }

func (r routeTestSessionRepository) Load(_ context.Context, id string) (*domainsession.Session, error) {
	if r.loadErr != nil {
		return nil, r.loadErr
	}
	session, ok := r.sessions[id]
	if !ok {
		return nil, domainsession.ErrSessionNotFound
	}
	if session == nil {
		return nil, nil
	}
	return session, nil
}

func (r routeTestSessionRepository) CurrentPublicationBinding(_ context.Context, id string) (domainsession.CanonicalSessionPublicationBinding, error) {
	if r.loadErr != nil {
		return domainsession.CanonicalSessionPublicationBinding{}, r.loadErr
	}
	sess, ok := r.sessions[id]
	if !ok || sess == nil {
		return domainsession.CanonicalSessionPublicationBinding{}, domainsession.ErrSessionNotFound
	}
	return domainsession.NewCanonicalSessionPublicationBinding(sess)
}

func (r routeTestSessionRepository) UsePublicationBinding(_ context.Context, expected domainsession.CanonicalSessionPublicationBinding, claim func() error) error {
	if claim == nil {
		return errors.New("claim callback required")
	}
	sess, ok := r.sessions[string(expected.SessionID())]
	if !ok || sess == nil {
		return domainsession.ErrSessionNotFound
	}
	current, err := domainsession.NewCanonicalSessionPublicationBinding(sess)
	if err != nil {
		return err
	}
	if !current.SameIdentity(expected) {
		return errors.New("publication binding stale")
	}
	return claim()
}

func (r routeTestSessionRepository) Exists(_ context.Context, id string) (bool, error) {
	_, ok := r.sessions[id]
	return ok, r.loadErr
}

func (r routeTestSessionRepository) Delete(context.Context, string) error { return nil }

func routeTestSession(t *testing.T, ownerID string, owned bool) (modulecore.SessionID, *domainsession.Session) {
	t.Helper()
	id := modulecore.NewSessionID()
	return id, routeTestSessionWithID(t, id, ownerID, owned)
}

func routeTestSessionWithID(t *testing.T, id modulecore.SessionID, ownerID string, owned bool) *domainsession.Session {
	t.Helper()
	address, err := domainconversation.NewChannelAddress("viewer", "viewer-user")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	var sess *domainsession.Session
	if owned {
		sess, err = domainsession.NewCanonicalSessionWithOwner(id, ownerID, "2026-10-24", address, now)
	} else {
		sess, err = domainsession.NewCanonicalSession(id, "2026-10-24", address, now)
	}
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

func routeTestEvent(eventType, content string, sessionID modulecore.SessionID, publicationIncarnation ...string) orchestrator.OrchestratorEvent {
	event := orchestrator.NewEvent(eventType, "mio", "user", content, "CHAT", "", string(sessionID), "viewer", "viewer-user")
	event.SessionID = sessionID
	if len(publicationIncarnation) > 0 {
		event.PublicationIncarnation = publicationIncarnation[0]
	}
	return event
}

func routeTestPrivateMioFinal(sessionID modulecore.SessionID, ownerID, publicationIncarnation, content string) orchestrator.OrchestratorEvent {
	event := orchestrator.NewEvent("agent.response", "mio", "user", content, "CHAT", string(modulecore.NewTaskID()), string(sessionID), "viewer", "cmd:"+ownerID)
	event.Visibility = "private"
	event.PublicationIncarnation = publicationIncarnation
	event.MessageID = modulecore.NewMessageID()
	event.TurnID = modulecore.NewTurnID()
	event.RunID = modulecore.NewRunID()
	event.ActorKind = "agent"
	event.ActorID = "mio"
	return event
}

func routeTestOwnerConfig(t *testing.T) *config.Config {
	t.Helper()
	tokenPath := filepath.Join(t.TempDir(), "owner.token")
	if err := os.WriteFile(tokenPath, []byte(privateEventsOwnerToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &config.Config{LocalAgentOps: config.LocalAgentOpsConfig{
		Enabled:       true,
		AuthTokenFile: tokenPath,
		UserID:        "owner-a",
	}}
}

func routeTestOwnerRequest(target, token string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.RemoteAddr = "127.0.0.1:1234"
	request.Host = "127.0.0.1:18790"
	request.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
	request.Header.Set("X-RenCrow-Interaction-Profile", "cmd-chat")
	request.Header.Set("Authorization", "Bearer "+token)
	return request
}

type liveRouteSSEWriter struct {
	*httptest.ResponseRecorder
	cancel  context.CancelFunc
	flushed bool
	publish func()
}

type routeTestRuntimeVersionStore struct {
	*eventstore.SQLiteStore
	sessionID    modulecore.SessionID
	ownerID      string
	version      string
	found        bool
	beforeReturn func()
}

func (s *routeTestRuntimeVersionStore) ReadSessionState(_ context.Context, sessionID modulecore.SessionID, ownerID string) (eventstore.SessionState, bool, error) {
	if s.beforeReturn != nil {
		s.beforeReturn()
	}
	if !s.found || sessionID != s.sessionID || ownerID != s.ownerID {
		return eventstore.SessionState{}, false, nil
	}
	return eventstore.SessionState{SessionID: sessionID, OwnerID: ownerID, RuntimeVersion: s.version}, true, nil
}

func (w *liveRouteSSEWriter) Flush() {
	w.ResponseRecorder.Flush()
	if !w.flushed {
		w.flushed = true
		w.publish()
		return
	}
	w.cancel()
}
