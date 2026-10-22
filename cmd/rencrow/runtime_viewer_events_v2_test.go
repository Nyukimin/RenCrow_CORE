package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	sessionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/session"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestViewerV2EventsResolveOnlyTheCanonicalOwnerSession(t *testing.T) {
	ctx := context.Background()
	repo := sessionpersistence.NewJSONSessionRepository(t.TempDir())
	address, err := domainconversation.NewChannelAddress("viewer", "viewer-user")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	session, _, err := repo.ReserveCanonicalV2(ctx, now.Format("2006-01-02"), address, responseFirstTestOwnerID, time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("reserve canonical Viewer session: %v", err)
	}
	sessionID := modulecore.SessionID(session.ID())
	store, err := eventstore.NewSQLiteStore(filepath.Join(t.TempDir(), "v2-events.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime, err := newResponseFirstConversationRuntime(store, agent.NewMioAgent(nil, nil, nil, nil, nil, nil), &responseFirstTestProvider{}, newResponseFirstTestPublisher(), responseFirstTestOwnerID)
	if err != nil {
		t.Fatalf("create response-first runtime: %v", err)
	}
	runtime.reservations = repo
	archive, err := viewer.NewCanonicalEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	hub := viewer.NewEventHub(10)
	hub.SetSessionPublicReplayReader(archive)
	deps := &Dependencies{responseFirst: runtime, eventHub: hub}
	cfg := &config.Config{}
	cfg.ConversationRuntime.Version = "v2"
	handler := buildViewerBridgeHandlers(cfg, deps, "", ttsEntryRuntime{}, repo).V2ConversationEvents

	request := httptest.NewRequest(http.MethodGet, "/viewer/conversation/events?session_id=attacker-session&owner_id=attacker-owner", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("un-pinned route status=%d, want temporary 503", response.Code)
	}

	if _, err := runtime.Accept(ctx, responseFirstRequest(sessionID, "events-route-submission", "canonical session replay")); err != nil {
		t.Fatalf("accept v2 turn: %v", err)
	}
	streamContext, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	request = httptest.NewRequest(http.MethodGet, "/viewer/conversation/events?session_id=attacker-session&owner_id=attacker-owner", nil).WithContext(streamContext)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "canonical session replay") {
		t.Fatalf("owner-scoped route status=%d body=%s, want canonical session replay", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "attacker-session") || strings.Contains(response.Body.String(), "attacker-owner") {
		t.Fatalf("client query values influenced the trusted session scope: %s", response.Body.String())
	}
}

func TestViewerV2EventsGetBeforePostDoesNotCreateSession(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sessions")
	if err := os.MkdirAll(sessionDir, 0700); err != nil {
		t.Fatal(err)
	}
	sessionRepo := sessionpersistence.NewJSONSessionRepository(sessionDir)
	storePath := filepath.Join(root, "events.sqlite")
	store, err := eventstore.NewSQLiteStore(storePath)
	if err != nil {
		t.Fatalf("open EventStore: %v", err)
	}
	factories, publisher := newViewerV2BridgeTestFactories(t, sessionRepo, store)
	events := factories.V2ConversationEvents
	post := factories.ViewerSendFromOrch(&viewerBridgeCountProcessor{})

	response := getViewerV2Events(events)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("initial GET status=%d body=%s, want temporary 503 before POST", response.Code, response.Body.String())
	}
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("initial GET created %d session files, want none", len(entries))
	}

	accepted := postViewerV2(post, "get-before-post", "first v2 turn")
	if accepted.Code != http.StatusAccepted {
		t.Fatalf("POST status=%d body=%s, want 202 after the read-only GET", accepted.Code, accepted.Body.String())
	}
	waitForViewerV2Commit(t, publisher)

	response = getViewerV2Events(events)
	if response.Code != http.StatusOK {
		t.Fatalf("GET after POST status=%d body=%s, want 200 for the pinned v2 session", response.Code, response.Body.String())
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close EventStore: %v", err)
	}
}

func TestViewerV2EventsGetRestartPostKeepsFirstSessionCreationOnPost(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sessions")
	if err := os.MkdirAll(sessionDir, 0700); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(root, "events.sqlite")
	firstStore, err := eventstore.NewSQLiteStore(storePath)
	if err != nil {
		t.Fatalf("open first EventStore: %v", err)
	}
	firstRepo := sessionpersistence.NewJSONSessionRepository(sessionDir)
	firstFactories, _ := newViewerV2BridgeTestFactories(t, firstRepo, firstStore)
	response := getViewerV2Events(firstFactories.V2ConversationEvents)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("initial GET status=%d body=%s, want temporary 503", response.Code, response.Body.String())
	}
	if err := firstStore.Close(); err != nil {
		t.Fatalf("close first EventStore: %v", err)
	}
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("initial GET created %d session files before restart, want none", len(entries))
	}

	// Reconstruct the owner and runtime around the same durable paths to model
	// the process restart between EventSource's initial request and POST.
	restartedRepo := sessionpersistence.NewJSONSessionRepository(sessionDir)
	restartedStore, err := eventstore.NewSQLiteStore(storePath)
	if err != nil {
		t.Fatalf("reopen EventStore after restart: %v", err)
	}
	restartedFactories, publisher := newViewerV2BridgeTestFactories(t, restartedRepo, restartedStore)
	accepted := postViewerV2(restartedFactories.ViewerSendFromOrch(&viewerBridgeCountProcessor{}), "get-restart-post", "first v2 turn after restart")
	if accepted.Code != http.StatusAccepted {
		t.Fatalf("POST after restart status=%d body=%s, want 202", accepted.Code, accepted.Body.String())
	}
	waitForViewerV2Commit(t, publisher)
	if err := restartedStore.Close(); err != nil {
		t.Fatalf("close restarted EventStore: %v", err)
	}
}

func TestViewerV2EventsKeepsExistingEmptyLegacyJSONUnavailable(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sessions")
	if err := os.MkdirAll(sessionDir, 0700); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(sessionDir, "20261022-viewer-viewer-user.json")
	legacy := []byte(`{"id":"20261022-viewer-viewer-user","channel":"viewer","chat_id":"viewer-user","history":[],"memory":{},"created_at":"2026-10-22T00:00:00Z","updated_at":"2026-10-22T00:00:00Z"}`)
	if err := os.WriteFile(legacyPath, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	repo := sessionpersistence.NewJSONSessionRepository(sessionDir)
	store, err := eventstore.NewSQLiteStore(filepath.Join(root, "events.sqlite"))
	if err != nil {
		t.Fatalf("open EventStore: %v", err)
	}
	factories, _ := newViewerV2BridgeTestFactories(t, repo, store)
	getResponse := getViewerV2Events(factories.V2ConversationEvents)
	if getResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET status=%d body=%s, want unavailable for an existing legacy JSON session", getResponse.Code, getResponse.Body.String())
	}
	postResponse := postViewerV2(factories.ViewerSendFromOrch(&viewerBridgeCountProcessor{}), "legacy-empty-json", "must not promote the legacy session")
	if postResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST status=%d body=%s, want strict legacy-session rejection", postResponse.Code, postResponse.Body.String())
	}
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(legacyPath) {
		t.Fatalf("legacy session directory entries=%v, want the original single file unchanged", entries)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close EventStore: %v", err)
	}
}

func newViewerV2BridgeTestFactories(t *testing.T, repo *sessionpersistence.JSONSessionRepository, store *eventstore.SQLiteStore) (viewerBridgeFactories, *responseFirstTestPublisher) {
	t.Helper()
	publisher := newResponseFirstTestPublisher()
	runtime, err := newResponseFirstConversationRuntime(store, agent.NewMioAgent(nil, nil, nil, nil, nil, nil), &responseFirstTestProvider{}, publisher, responseFirstTestOwnerID)
	if err != nil {
		t.Fatalf("create response-first runtime: %v", err)
	}
	archive, err := viewer.NewCanonicalEventLog(store)
	if err != nil {
		t.Fatalf("create canonical event log: %v", err)
	}
	hub := viewer.NewEventHub(10)
	hub.SetSessionPublicReplayReader(archive)
	deps := &Dependencies{responseFirst: runtime, eventHub: hub}
	cfg := &config.Config{}
	cfg.ConversationRuntime.Version = "v2"
	return buildViewerBridgeHandlers(cfg, deps, "", ttsEntryRuntime{}, repo), publisher
}

func getViewerV2Events(handler http.HandlerFunc) *httptest.ResponseRecorder {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/viewer/conversation/events", nil).WithContext(ctx)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func waitForViewerV2Commit(t *testing.T, publisher *responseFirstTestPublisher) {
	t.Helper()
	select {
	case <-publisher.committed:
	case <-time.After(3 * time.Second):
		t.Fatal("v2 turn did not commit before the test closed its owner store")
	}
}
