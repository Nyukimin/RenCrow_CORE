package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/conversationruntime"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	sessionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/session"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type viewerBridgeCountProcessor struct {
	calls    int
	done     chan struct{}
	doneOnce sync.Once
}

type responseFirstFailOnceAcceptStore struct {
	*eventstore.SQLiteStore
	failNext bool
}

func (store *responseFirstFailOnceAcceptStore) AcceptTurn(ctx context.Context, input eventstore.AcceptInput) (eventstore.AcceptedTurn, error) {
	if store.failNext {
		store.failNext = false
		return eventstore.AcceptedTurn{}, errors.New("injected EventStore Accept failure")
	}
	return store.SQLiteStore.AcceptTurn(ctx, input)
}

func openViewerBridgeAdmissionStore(t *testing.T) *eventstore.SQLiteStore {
	t.Helper()
	store, err := eventstore.NewSQLiteStore(filepath.Join(t.TempDir(), "event-store.sqlite"))
	if err != nil {
		t.Fatalf("open EventStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

type responseFirstTestReservationReader struct{ ownerID string }

func (reader responseFirstTestReservationReader) IsCanonicalV2Reserved(context.Context, string, string) (bool, error) {
	return reader.ownerID == responseFirstTestOwnerID, nil
}

func allowResponseFirstTestReservations(runtime *responseFirstConversationRuntime) {
	runtime.reservations = responseFirstTestReservationReader{ownerID: responseFirstTestOwnerID}
}

func (p *viewerBridgeCountProcessor) ProcessMessage(_ context.Context, req orchestrator.ProcessMessageRequest) (orchestrator.ProcessMessageResponse, error) {
	p.calls++
	if p.done != nil {
		p.doneOnce.Do(func() { close(p.done) })
	}
	return orchestrator.ProcessMessageResponse{SessionID: req.SessionID, Response: "legacy"}, nil
}

func TestViewerBridgeExplicitV2FailsClosedWhenResponseFirstIsDisabled(t *testing.T) {
	processor := &viewerBridgeCountProcessor{}
	factories := buildViewerBridgeHandlers(
		&config.Config{}, &Dependencies{}, "", ttsEntryRuntime{},
		sessionpersistence.NewJSONSessionRepository(t.TempDir()),
	)
	handler := factories.ViewerSendFromOrch(processor)
	request := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(
		`{"protocol_version":2,"message":"hello","client_submission_id":"submit-1","to":"mio"}`,
	))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s, want %d while v2 is disabled", response.Code, response.Body.String(), http.StatusServiceUnavailable)
	}
	if processor.calls != 0 {
		t.Fatalf("legacy processor calls=%d, want 0 for explicit v2", processor.calls)
	}
}

func TestViewerV2ReservationSurvivesAcceptFailureRestartAndSameSubmissionRetry(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sessions")
	if err := os.MkdirAll(sessionDir, 0700); err != nil {
		t.Fatal(err)
	}
	eventStorePath := filepath.Join(root, "events.sqlite")
	logicalDate := time.Now().UTC().Format("2006-01-02")
	firstStore, err := eventstore.NewSQLiteStore(eventStorePath)
	if err != nil {
		t.Fatalf("open initial EventStore: %v", err)
	}
	firstRepo := sessionpersistence.NewJSONSessionRepository(sessionDir)
	failingStore := &responseFirstFailOnceAcceptStore{SQLiteStore: firstStore, failNext: true}
	firstRuntime, err := newResponseFirstConversationRuntime(failingStore, agent.NewMioAgent(nil, nil, nil, nil, nil, nil), &responseFirstTestProvider{}, newResponseFirstTestPublisher(), responseFirstTestOwnerID)
	if err != nil {
		t.Fatalf("build first response-first runtime: %v", err)
	}
	firstRuntime.reservations = firstRepo
	firstConfig := &config.Config{}
	firstConfig.ConversationRuntime.Version = "v2"
	firstHandler := buildViewerBridgeHandlers(firstConfig, &Dependencies{responseFirst: firstRuntime}, "", ttsEntryRuntime{}, firstRepo).ViewerSendFromOrch(&viewerBridgeCountProcessor{})
	failed := postViewerV2(firstHandler, "reservation-restart-submit", "resume after Accept failure")
	if failed.Code != http.StatusServiceUnavailable {
		t.Fatalf("first POST status=%d body=%s, want temporary failure before EventStore acceptance", failed.Code, failed.Body.String())
	}
	address, err := conversation.NewChannelAddress("viewer", "viewer-user")
	if err != nil {
		t.Fatal(err)
	}
	reserved, intent, found, err := firstRepo.LoadCanonicalWithCreationIntent(context.Background(), logicalDate, address)
	if err != nil || !found || reserved == nil || intent == nil || intent.Runtime != "v2" || intent.OwnerID != responseFirstTestOwnerID {
		t.Fatalf("owner reservation after failed POST=(%v,%+v,%t,%v), want durable same-owner intent", reserved, intent, found, err)
	}
	if err := firstStore.Close(); err != nil {
		t.Fatalf("close initial EventStore: %v", err)
	}

	restartedStore, err := eventstore.NewSQLiteStore(eventStorePath)
	if err != nil {
		t.Fatalf("reopen EventStore: %v", err)
	}
	defer restartedStore.Close()
	restartedRepo := sessionpersistence.NewJSONSessionRepository(sessionDir)
	restartedRuntime, err := newResponseFirstConversationRuntime(restartedStore, agent.NewMioAgent(nil, nil, nil, nil, nil, nil), &responseFirstTestProvider{}, newResponseFirstTestPublisher(), responseFirstTestOwnerID)
	if err != nil {
		t.Fatalf("build restarted response-first runtime: %v", err)
	}
	restartedRuntime.reservations = restartedRepo
	restartedConfig := &config.Config{}
	restartedConfig.ConversationRuntime.Version = "v2"
	restartedHandler := buildViewerBridgeHandlers(restartedConfig, &Dependencies{responseFirst: restartedRuntime}, "", ttsEntryRuntime{}, restartedRepo).ViewerSendFromOrch(&viewerBridgeCountProcessor{})
	accepted := postViewerV2(restartedHandler, "reservation-restart-submit", "resume after Accept failure")
	if accepted.Code != http.StatusAccepted {
		t.Fatalf("retry POST status=%d body=%s, want 202 from the recovered reservation", accepted.Code, accepted.Body.String())
	}
	var firstReceipt struct {
		TurnID string `json:"turn_id"`
	}
	if err := json.Unmarshal(accepted.Body.Bytes(), &firstReceipt); err != nil || firstReceipt.TurnID == "" {
		t.Fatalf("accepted retry receipt=%+v err=%v, want a durable turn id", firstReceipt, err)
	}
	replayed := postViewerV2(restartedHandler, "reservation-restart-submit", "resume after Accept failure")
	if replayed.Code != http.StatusAccepted {
		t.Fatalf("same-submission retry status=%d body=%s, want idempotent 202", replayed.Code, replayed.Body.String())
	}
	var replayReceipt struct {
		TurnID string `json:"turn_id"`
	}
	if err := json.Unmarshal(replayed.Body.Bytes(), &replayReceipt); err != nil || replayReceipt.TurnID != firstReceipt.TurnID {
		t.Fatalf("same-submission receipt=%+v err=%v, want original turn %q", replayReceipt, err, firstReceipt.TurnID)
	}
}

func TestViewerBridgeV1RemainsLegacyWhenResponseFirstIsEnabled(t *testing.T) {
	processor := &viewerBridgeCountProcessor{done: make(chan struct{})}
	cfg := &config.Config{}
	cfg.ConversationRuntime.Version = "v2"
	store, err := eventstore.NewSQLiteStore(filepath.Join(t.TempDir(), "event-store.sqlite"))
	if err != nil {
		t.Fatalf("open EventStore: %v", err)
	}
	defer store.Close()
	deps := &Dependencies{canonicalEventStore: store}
	factories := buildViewerBridgeHandlers(cfg, deps, "", ttsEntryRuntime{}, sessionpersistence.NewJSONSessionRepository(t.TempDir()))
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(
		`{"protocol_version":1,"message":"legacy hello","to":"mio"}`,
	))
	request.Header.Set("Content-Type", "application/json")
	factories.ViewerSendFromOrch(processor).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want legacy 200 response", response.Code, response.Body.String())
	}
	select {
	case <-processor.done:
	case <-time.After(3 * time.Second):
		t.Fatal("protocol v1 did not reach the legacy processor")
	}
	if processor.calls != 1 {
		t.Fatalf("legacy processor calls=%d, want exactly one", processor.calls)
	}
}

func TestViewerBridgeV1RejectsPersistedV2SessionAfterRestart(t *testing.T) {
	eventStorePath := filepath.Join(t.TempDir(), "event-store.sqlite")
	sessionRepo := sessionpersistence.NewJSONSessionRepository(t.TempDir())
	address, err := conversation.NewChannelAddress("viewer", "viewer-user")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	session, _, err := sessionRepo.ReserveCanonicalV2(context.Background(), now.Format("2006-01-02"), address, responseFirstTestOwnerID, time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("reserve canonical Viewer session: %v", err)
	}
	sessionID := modulecore.SessionID(session.ID())

	store, err := eventstore.NewSQLiteStore(eventStorePath)
	if err != nil {
		t.Fatalf("open EventStore: %v", err)
	}
	runtime, err := newResponseFirstConversationRuntime(store, agent.NewMioAgent(nil, nil, nil, nil, nil, nil), &responseFirstTestProvider{}, newResponseFirstTestPublisher(), responseFirstTestOwnerID)
	if err != nil {
		_ = store.Close()
		t.Fatalf("build response-first runtime: %v", err)
	}
	runtime.reservations = sessionRepo
	if _, err := runtime.Accept(context.Background(), responseFirstRequest(sessionID, "persist-v2-pin", "pin this session")); err != nil {
		_ = store.Close()
		t.Fatalf("persist v2 session pin: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close EventStore before restart: %v", err)
	}

	restartedStore, err := eventstore.NewSQLiteStore(eventStorePath)
	if err != nil {
		t.Fatalf("reopen EventStore after restart: %v", err)
	}
	defer restartedStore.Close()
	processor := &viewerBridgeCountProcessor{done: make(chan struct{})}
	cfg := &config.Config{}
	cfg.ConversationRuntime.Version = "legacy"
	deps := &Dependencies{canonicalEventStore: restartedStore}
	handler := buildViewerBridgeHandlers(cfg, deps, "", ttsEntryRuntime{}, sessionRepo).ViewerSendFromOrch(processor)
	request := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{"protocol_version":1,"message":"must remain on pinned v2"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code == http.StatusOK {
		select {
		case <-processor.done:
		case <-time.After(3 * time.Second):
			t.Fatal("unexpectedly accepted v1 processor did not complete")
		}
	}
	if response.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s, want v1 rejected by persisted v2 session pin", response.Code, response.Body.String())
	}
	if processor.calls != 0 {
		t.Fatalf("legacy processor calls=%d, want 0 for a persisted v2 session", processor.calls)
	}
}

type responseFirstTestProvider struct{ calls int }

func (p *responseFirstTestProvider) GenerateConversation(_ context.Context, _ conversation.ConversationScope, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.calls++
	return llm.GenerateResponse{
		Content:   `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"user","text":"response-first reply"}}`,
		RequestID: modulecore.NewRequestID(), ResponseID: modulecore.NewResponseID(), TokensUsed: 12,
	}, nil
}

func (p *responseFirstTestProvider) Name() string { return "test-conversation" }

type responseFirstCancelOnceProvider struct {
	calls   atomic.Int32
	entered chan struct{}
}

func (p *responseFirstCancelOnceProvider) GenerateConversation(ctx context.Context, _ conversation.ConversationScope, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
	if p.calls.Add(1) == 1 {
		close(p.entered)
		<-ctx.Done()
		return llm.GenerateResponse{}, ctx.Err()
	}
	return llm.GenerateResponse{
		Content:   `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"user","text":"resumed response"}}`,
		RequestID: modulecore.NewRequestID(), ResponseID: modulecore.NewResponseID(), TokensUsed: 12,
	}, nil
}

func (p *responseFirstCancelOnceProvider) Name() string { return "cancel-once-conversation" }

type responseFirstTestPublisher struct {
	accepted  chan modulecore.EventEnvelope
	committed chan conversationruntime.CommittedBundle
}

func newResponseFirstTestPublisher() *responseFirstTestPublisher {
	return &responseFirstTestPublisher{accepted: make(chan modulecore.EventEnvelope, 4), committed: make(chan conversationruntime.CommittedBundle, 4)}
}

func (p *responseFirstTestPublisher) PublishAcceptedInput(_ context.Context, event modulecore.EventEnvelope) error {
	p.accepted <- event
	return nil
}

func (p *responseFirstTestPublisher) PublishCommitted(_ context.Context, bundle conversationruntime.CommittedBundle) error {
	p.committed <- bundle
	return nil
}

func TestViewerBridgeV2AcceptsCommitsAndPublishesDirectReplyWithoutLegacyTask(t *testing.T) {
	store, processor, provider, publisher, handler, closeStore := newResponseFirstViewerHandler(t)
	defer closeStore()
	response := postViewerV2(handler, "submit-happy", "hello Mio")
	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s, want durable 202 acceptance", response.Code, response.Body.String())
	}
	var receipt struct {
		SessionID string `json:"session_id"`
		TurnID    string `json:"turn_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &receipt); err != nil {
		t.Fatalf("decode acceptance receipt: %v", err)
	}
	if receipt.SessionID == "" || receipt.TurnID == "" {
		t.Fatalf("acceptance receipt identity is incomplete: %+v", receipt)
	}
	if strings.Contains(response.Body.String(), "root_task_id") || strings.Contains(response.Body.String(), "task_id") {
		t.Fatalf("direct response-first acceptance leaked Task identity: %s", response.Body.String())
	}
	select {
	case input := <-publisher.accepted:
		if input.EventType != "conversation.user_message" || input.TurnID != modulecore.TurnID(receipt.TurnID) || input.Payload["source_actor_id"] != responseFirstActorID {
			t.Fatalf("published accepted input=%+v, want the accepted Viewer turn", input)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("accepted input was not passed to the event relay")
	}
	select {
	case bundle := <-publisher.committed:
		if bundle.TurnID != modulecore.TurnID(receipt.TurnID) || len(bundle.Events) == 0 || bundle.Events[0].EventType != "agent.response" {
			t.Fatalf("committed bundle=%+v, want a direct agent.response for the accepted turn", bundle)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("direct response was not committed and published")
	}
	if processor.calls != 0 {
		t.Fatalf("legacy processor calls=%d, want zero for protocol v2", processor.calls)
	}
	if provider.calls != 1 {
		t.Fatalf("conversation provider calls=%d, want exactly one", provider.calls)
	}
	if _, found, err := store.LoadCommittedOutcome(context.Background(), modulecore.TurnID(receipt.TurnID)); err != nil || !found {
		t.Fatalf("LoadCommittedOutcome(found=%t, err=%v), want durable response receipt", found, err)
	}
}

func TestViewerBridgeV2CompletedRetryRepublishesReceiptWithoutRegenerating(t *testing.T) {
	_, processor, provider, publisher, handler, closeStore := newResponseFirstViewerHandler(t)
	defer closeStore()
	first := postViewerV2(handler, "submit-retry", "retry me")
	if first.Code != http.StatusAccepted {
		t.Fatalf("first status=%d body=%s, want 202", first.Code, first.Body.String())
	}
	var firstReceipt struct {
		TurnID string `json:"turn_id"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstReceipt); err != nil {
		t.Fatal(err)
	}
	select {
	case <-publisher.committed:
	case <-time.After(3 * time.Second):
		t.Fatal("first response did not complete")
	}
	second := postViewerV2(handler, "submit-retry", "retry me")
	if second.Code != http.StatusAccepted {
		t.Fatalf("retry status=%d body=%s, want idempotent 202", second.Code, second.Body.String())
	}
	var secondReceipt struct {
		TurnID string `json:"turn_id"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &secondReceipt); err != nil {
		t.Fatal(err)
	}
	if secondReceipt.TurnID != firstReceipt.TurnID {
		t.Fatalf("retry turn id=%q, original=%q", secondReceipt.TurnID, firstReceipt.TurnID)
	}
	select {
	case <-publisher.committed:
	case <-time.After(3 * time.Second):
		t.Fatal("completed retry did not republish the committed receipt")
	}
	if processor.calls != 0 {
		t.Fatalf("legacy processor calls=%d, want zero", processor.calls)
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls=%d, want no regeneration on completed retry", provider.calls)
	}
}

func TestResponseFirstResumePendingAcceptedTurnRunsPerSession(t *testing.T) {
	store, err := eventstore.NewSQLiteStore(filepath.Join(t.TempDir(), "event-store.sqlite"))
	if err != nil {
		t.Fatalf("open event store: %v", err)
	}
	defer store.Close()
	provider := &responseFirstTestProvider{}
	publisher := newResponseFirstTestPublisher()
	runtime, err := newResponseFirstConversationRuntime(store, agent.NewMioAgent(nil, nil, nil, nil, nil, nil), provider, publisher, responseFirstTestOwnerID)
	if err != nil {
		t.Fatalf("build response-first runtime: %v", err)
	}
	allowResponseFirstTestReservations(runtime)
	sessionID := modulecore.NewSessionID()
	accepted, err := runtime.Accept(context.Background(), responseFirstRequest(sessionID, "resume-submit", "resume me"))
	if err != nil {
		t.Fatalf("Accept(): %v", err)
	}
	if accepted.IdempotentReplay {
		t.Fatal("new accepted turn was marked as a replay")
	}
	if err := runtime.ResumePendingAcceptedTurns(context.Background(), sessionID); err != nil {
		t.Fatalf("ResumePendingAcceptedTurns(): %v", err)
	}
	select {
	case bundle := <-publisher.committed:
		if bundle.TurnID != accepted.TurnID {
			t.Fatalf("resumed turn=%s, want %s", bundle.TurnID, accepted.TurnID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("pending accepted turn was not resumed")
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls=%d, want one resumed generation", provider.calls)
	}
}

func TestResponseFirstAcceptedRetrySchedulesExistingAcceptedTurn(t *testing.T) {
	store, err := eventstore.NewSQLiteStore(filepath.Join(t.TempDir(), "event-store.sqlite"))
	if err != nil {
		t.Fatalf("open event store: %v", err)
	}
	defer store.Close()
	provider := &responseFirstTestProvider{}
	publisher := newResponseFirstTestPublisher()
	runtime, err := newResponseFirstConversationRuntime(store, agent.NewMioAgent(nil, nil, nil, nil, nil, nil), provider, publisher, responseFirstTestOwnerID)
	if err != nil {
		t.Fatalf("build response-first runtime: %v", err)
	}
	allowResponseFirstTestReservations(runtime)
	sessionID := modulecore.NewSessionID()
	first, err := runtime.Accept(context.Background(), responseFirstRequest(sessionID, "accepted-retry", "resume the durable input"))
	if err != nil {
		t.Fatalf("initial Accept(): %v", err)
	}
	second, err := runtime.Accept(context.Background(), responseFirstRequest(sessionID, "accepted-retry", "resume the durable input"))
	if err != nil {
		t.Fatalf("retry Accept(): %v", err)
	}
	if second.TurnID != first.TurnID || second.IdempotentReplay {
		t.Fatalf("retry receipt=%+v, initial=%+v; accepted retry must schedule the same turn", second, first)
	}
	if err := runtime.Process(context.Background(), second); err != nil {
		t.Fatalf("Process(retried accepted turn): %v", err)
	}
	select {
	case bundle := <-publisher.committed:
		if bundle.TurnID != first.TurnID {
			t.Fatalf("processed turn=%s, want %s", bundle.TurnID, first.TurnID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("replayed accepted turn was not processed")
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls=%d, want one generation for the existing accepted turn", provider.calls)
	}
}

func TestResponseFirstCanceledForegroundLeavesDurableTurnResumable(t *testing.T) {
	store, err := eventstore.NewSQLiteStore(filepath.Join(t.TempDir(), "event-store.sqlite"))
	if err != nil {
		t.Fatalf("open event store: %v", err)
	}
	defer store.Close()
	provider := &responseFirstCancelOnceProvider{entered: make(chan struct{})}
	publisher := newResponseFirstTestPublisher()
	runtime, err := newResponseFirstConversationRuntime(store, agent.NewMioAgent(nil, nil, nil, nil, nil, nil), provider, publisher, responseFirstTestOwnerID)
	if err != nil {
		t.Fatalf("build response-first runtime: %v", err)
	}
	allowResponseFirstTestReservations(runtime)
	sessionID := modulecore.NewSessionID()
	accepted, err := runtime.Accept(context.Background(), responseFirstRequest(sessionID, "cancel-resume", "keep this accepted turn"))
	if err != nil {
		t.Fatalf("Accept(): %v", err)
	}

	processCtx, cancel := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() { firstDone <- runtime.Process(processCtx, accepted) }()
	select {
	case <-provider.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("provider generation did not start")
	}
	cancel()
	select {
	case err := <-firstDone:
		if err == nil {
			t.Fatal("canceled foreground processing unexpectedly succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled foreground processing did not return")
	}
	pending, err := store.ReadPendingAcceptedTurns(context.Background(), sessionID, responseFirstTestOwnerID)
	if err != nil {
		t.Fatalf("read accepted turn after cancellation: %v", err)
	}
	if len(pending) != 1 || pending[0].TurnID != accepted.TurnID || pending[0].State != "accepted" {
		t.Fatalf("canceled turn was not left durably resumable: %+v", pending)
	}

	if err := runtime.Process(context.Background(), accepted); err != nil {
		t.Fatalf("resume accepted turn after canceled foreground: %v", err)
	}
	select {
	case committed := <-publisher.committed:
		if committed.TurnID != accepted.TurnID {
			t.Fatalf("committed resumed turn=%s, want %s", committed.TurnID, accepted.TurnID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("resumed accepted turn was not committed and published")
	}
	if got := provider.calls.Load(); got != 2 {
		t.Fatalf("provider generation calls=%d, want canceled generation plus resumed generation", got)
	}
}

func TestResponseFirstAcceptRequiresMatchingOwnerReservationForUnpinnedSession(t *testing.T) {
	store := openViewerBridgeAdmissionStore(t)
	runtime, err := newResponseFirstConversationRuntime(store, agent.NewMioAgent(nil, nil, nil, nil, nil, nil), &responseFirstTestProvider{}, newResponseFirstTestPublisher(), responseFirstTestOwnerID)
	if err != nil {
		t.Fatalf("build response-first runtime: %v", err)
	}
	sessionID := modulecore.NewSessionID()
	if _, err := runtime.Accept(context.Background(), responseFirstRequest(sessionID, "unreserved-submit", "must not accept")); err == nil {
		t.Fatal("Accept() accepted an unpinned session without a reservation owner")
	}
	runtime.reservations = responseFirstTestReservationReader{ownerID: "different-owner"}
	if _, err := runtime.Accept(context.Background(), responseFirstRequest(sessionID, "wrong-owner-submit", "must not accept")); err == nil {
		t.Fatal("Accept() accepted an unpinned session with another owner's reservation")
	}
}

func TestResponseFirstRefusesExistingLegacySessionWithoutCutover(t *testing.T) {
	store, err := eventstore.NewSQLiteStore(filepath.Join(t.TempDir(), "event-store.sqlite"))
	if err != nil {
		t.Fatalf("open event store: %v", err)
	}
	defer store.Close()
	legacySession := modulecore.NewSessionID()
	legacy := modulecore.NewEventEnvelope(modulecore.NewTraceID(), "", nil, "orchestrator", "message.received", time.Now().UTC(), map[string]any{
		"from": "user", "source_actor_id": "ren", "to": "mio", "text": "legacy history", "content": "legacy history",
	})
	legacy.SessionID, legacy.TurnID, legacy.MessageID = legacySession, modulecore.NewTurnID(), modulecore.NewMessageID()
	legacy.ActorKind, legacy.ActorID = "user", "ren"
	if err := store.Append(context.Background(), legacy); err != nil {
		t.Fatalf("append legacy event: %v", err)
	}
	provider := &responseFirstTestProvider{}
	runtime, err := newResponseFirstConversationRuntime(store, agent.NewMioAgent(nil, nil, nil, nil, nil, nil), provider, newResponseFirstTestPublisher(), responseFirstTestOwnerID)
	if err != nil {
		t.Fatalf("build response-first runtime: %v", err)
	}
	if _, err := runtime.Accept(context.Background(), responseFirstRequest(legacySession, "legacy-submit", "must refuse")); err == nil || !strings.Contains(err.Error(), "refuses an existing legacy session") {
		t.Fatalf("Accept() error=%v, want explicit legacy-session refusal", err)
	}
	if provider.calls != 0 {
		t.Fatalf("provider calls=%d, want zero for refused v1 session", provider.calls)
	}
}

func TestViewerBridgeV2RefusesExistingLegacyViewerSession(t *testing.T) {
	store, err := eventstore.NewSQLiteStore(filepath.Join(t.TempDir(), "event-store.sqlite"))
	if err != nil {
		t.Fatalf("open event store: %v", err)
	}
	defer store.Close()
	repo := sessionpersistence.NewJSONSessionRepository(t.TempDir())
	address, err := conversation.NewChannelAddress("viewer", "viewer-user")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, _, err := repo.LoadOrCreateCanonical(context.Background(), now.Format("2006-01-02"), address, now); err != nil {
		t.Fatalf("create existing legacy Viewer session: %v", err)
	}
	provider := &responseFirstTestProvider{}
	publisher := newResponseFirstTestPublisher()
	runtime, err := newResponseFirstConversationRuntime(store, agent.NewMioAgent(nil, nil, nil, nil, nil, nil), provider, publisher, responseFirstTestOwnerID)
	if err != nil {
		t.Fatalf("build response-first runtime: %v", err)
	}
	processor := &viewerBridgeCountProcessor{}
	cfg := &config.Config{}
	cfg.ConversationRuntime.Version = "v2"
	factories := buildViewerBridgeHandlers(cfg, &Dependencies{responseFirst: runtime}, "", ttsEntryRuntime{}, repo)
	response := postViewerV2(factories.ViewerSendFromOrch(processor), "legacy-json-session", "must not cut over")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s, want fail-closed response for existing legacy session", response.Code, response.Body.String())
	}
	if processor.calls != 0 || provider.calls != 0 {
		t.Fatalf("legacy processor calls=%d provider calls=%d, want neither legacy processing nor generation", processor.calls, provider.calls)
	}
}

func responseFirstRequest(sessionID modulecore.SessionID, submissionID, message string) viewer.V2SendRequest {
	return viewer.V2SendRequest{
		SessionID: sessionID, Message: message, ClientSubmissionID: submissionID,
		To: "mio", TraceID: modulecore.NewTraceID(), TurnID: modulecore.NewTurnID(),
		InputMessageID: modulecore.NewMessageID(), ActorID: responseFirstActorID,
	}
}

const responseFirstTestOwnerID = "ren"

func newResponseFirstViewerHandler(t *testing.T) (*eventstore.SQLiteStore, *viewerBridgeCountProcessor, *responseFirstTestProvider, *responseFirstTestPublisher, http.HandlerFunc, func()) {
	t.Helper()
	store, err := eventstore.NewSQLiteStore(filepath.Join(t.TempDir(), "event-store.sqlite"))
	if err != nil {
		t.Fatalf("open event store: %v", err)
	}
	provider := &responseFirstTestProvider{}
	publisher := newResponseFirstTestPublisher()
	runtime, err := newResponseFirstConversationRuntime(store, agent.NewMioAgent(nil, nil, nil, nil, nil, nil), provider, publisher, responseFirstTestOwnerID)
	if err != nil {
		_ = store.Close()
		t.Fatalf("build response-first runtime: %v", err)
	}
	processor := &viewerBridgeCountProcessor{}
	deps := &Dependencies{responseFirst: runtime}
	cfg := &config.Config{}
	cfg.ConversationRuntime.Version = "v2"
	factories := buildViewerBridgeHandlers(cfg, deps, "", ttsEntryRuntime{}, sessionpersistence.NewJSONSessionRepository(t.TempDir()))
	return store, processor, provider, publisher, factories.ViewerSendFromOrch(processor), func() { _ = store.Close() }
}

func postViewerV2(handler http.HandlerFunc, submissionID, message string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]any{
		"protocol_version": 2, "message": message, "client_submission_id": submissionID, "to": "mio", "user_id": "untrusted-body-user",
	})
	request := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
