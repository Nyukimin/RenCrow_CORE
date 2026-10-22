package main

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/conversationruntime"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	"github.com/Nyukimin/RenCrow_CORE/internal/turntiming"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type responseFirstProjectionProbePublisher struct {
	store       *eventstore.SQLiteStore
	claimOnCall int
}

func (p *responseFirstProjectionProbePublisher) PublishAcceptedInput(context.Context, modulecore.EventEnvelope) error {
	return nil
}

func (p *responseFirstProjectionProbePublisher) PublishCommitted(ctx context.Context, _ conversationruntime.CommittedBundle) error {
	claimed, err := p.store.ClaimWork(ctx, eventstore.WorkClaimInput{
		Kind: responseFirstProjectionWorkKind, Limit: 1, LeaseDuration: time.Minute, Now: time.Now().UTC(),
	})
	if err != nil {
		return err
	}
	p.claimOnCall = len(claimed)
	if len(claimed) != 0 {
		return p.store.RetryWork(ctx, eventstore.WorkRetryInput{WorkLeaseInput: eventstore.WorkLeaseInput{
			WorkID: claimed[0].WorkID, LeaseToken: claimed[0].LeaseToken, Version: claimed[0].Version, Now: time.Now().UTC(),
		}})
	}
	return nil
}

type responseFirstProjectionCompleteFailStore struct {
	*eventstore.SQLiteStore
	failNext atomic.Bool
}

func (s *responseFirstProjectionCompleteFailStore) CompleteConversationProjection(ctx context.Context, input eventstore.CompleteConversationProjectionInput) error {
	if s.failNext.Swap(false) {
		return errors.New("injected projection completion failure")
	}
	return s.SQLiteStore.CompleteConversationProjection(ctx, input)
}

type responseFirstProjectionCaptureProvider struct {
	requests []llm.GenerateRequest
}

type responseFirstProjectionFairnessStore struct {
	responseFirstProjectionPersistence
	pending       eventstore.PendingClarificationReplyProjection
	markedWork    eventstore.LeasedWorkItem
	retryCalls    int
	retryErr      error
	markCalls     int
	claimCalls    int
	loadCalls     int
	completeCalls int
}

func (s *responseFirstProjectionFairnessStore) ListUnmarkedClarificationReplyProjections(context.Context, string, int) ([]eventstore.PendingClarificationReplyProjection, error) {
	return []eventstore.PendingClarificationReplyProjection{s.pending}, nil
}

func (s *responseFirstProjectionFairnessStore) RetryUnmarkedClarificationReplyProjection(context.Context, string, string, modulecore.EventID, time.Time, time.Duration) error {
	s.retryCalls++
	return s.retryErr
}

func (s *responseFirstProjectionFairnessStore) MarkConversationProjectionDeliveryAttempt(context.Context, eventstore.ConversationProjectionDeliveryAttemptInput) error {
	s.markCalls++
	return nil
}

func (s *responseFirstProjectionFairnessStore) ClaimWork(context.Context, eventstore.WorkClaimInput) ([]eventstore.LeasedWorkItem, error) {
	s.claimCalls++
	return []eventstore.LeasedWorkItem{s.markedWork}, nil
}

func (s *responseFirstProjectionFairnessStore) LoadLeasedConversationProjection(_ context.Context, input eventstore.LoadConversationProjectionInput) (eventstore.LeasedConversationProjection, error) {
	s.loadCalls++
	item := domainconversation.CanonicalConversationProjection{
		ConversationProjectionSource: domainconversation.ConversationProjectionSource{
			SessionID: s.markedWork.SessionID, TurnID: modulecore.NewTurnID(), MessageID: modulecore.NewMessageID(),
			EventID: s.markedWork.SourceEventID, EventSeq: 8, ActorID: responseFirstActorID,
			Speaker: "mio", Text: "another session reply", OccurredAt: input.Lease.Now, Visibility: "public",
		},
		ThreadID: modulecore.NewThreadID(), ThreadSeq: 1, ThreadKind: modulecore.ThreadKindUserConversation,
	}
	return eventstore.LeasedConversationProjection{
		Work: s.markedWork, OwnerID: responseFirstTestOwnerID, SessionID: s.markedWork.SessionID,
		ExpectedWatermark: 0, AppliedThrough: 8, Items: []domainconversation.CanonicalConversationProjection{item},
	}, nil
}

func (s *responseFirstProjectionFairnessStore) CompleteConversationProjection(context.Context, eventstore.CompleteConversationProjectionInput) error {
	s.completeCalls++
	return nil
}

func (s *responseFirstProjectionFairnessStore) RetryWork(context.Context, eventstore.WorkRetryInput) error {
	return nil
}

type responseFirstProjectionFairnessL1 struct{ calls int }

func (l *responseFirstProjectionFairnessL1) ApplyCanonicalConversationProjection(_ context.Context, items []domainconversation.CanonicalConversationProjection) (domainconversation.ConversationProjectionApplyReceipt, error) {
	l.calls++
	if len(items) == 0 {
		return domainconversation.ConversationProjectionApplyReceipt{}, errors.New("empty fairness projection")
	}
	return domainconversation.ConversationProjectionApplyReceipt{SessionID: items[0].SessionID, AppliedThrough: items[len(items)-1].EventSeq}, nil
}

type responseFirstProjectionFailingAcceptedPublisher struct {
	err   error
	calls int
}

func (p *responseFirstProjectionFailingAcceptedPublisher) PublishAcceptedInput(context.Context, modulecore.EventEnvelope) error {
	p.calls++
	return p.err
}

func (p *responseFirstProjectionFailingAcceptedPublisher) PublishCommitted(context.Context, conversationruntime.CommittedBundle) error {
	return nil
}

func (p *responseFirstProjectionCaptureProvider) GenerateConversation(_ context.Context, _ domainconversation.ConversationScope, request llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.requests = append(p.requests, request)
	return llm.GenerateResponse{
		Content:   `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"user","text":"response-first reply"}}`,
		RequestID: modulecore.NewRequestID(), ResponseID: modulecore.NewResponseID(), TokensUsed: 12,
	}, nil
}

func (p *responseFirstProjectionCaptureProvider) Name() string { return "projection-capture" }

func newResponseFirstProjectionRuntime(t *testing.T, store responseFirstJournalStore, l1 *l1sqlite.L1SQLiteStore, publisher responseFirstEventPublisher, provider interface {
	GenerateConversation(context.Context, domainconversation.ConversationScope, llm.GenerateRequest) (llm.GenerateResponse, error)
}) *responseFirstConversationRuntime {
	t.Helper()
	runtime, err := newResponseFirstConversationRuntimeWithProjection(
		store, agent.NewMioAgent(nil, nil, nil, nil, nil, nil), provider, publisher, responseFirstTestOwnerID, l1,
	)
	if err != nil {
		t.Fatalf("build response-first projection runtime: %v", err)
	}
	allowResponseFirstTestReservations(runtime)
	return runtime
}

func openProjectionTestStores(t *testing.T, root string) (*eventstore.SQLiteStore, *l1sqlite.L1SQLiteStore) {
	t.Helper()
	store, err := eventstore.NewSQLiteStore(filepath.Join(root, "event-store.sqlite"))
	if err != nil {
		t.Fatalf("open EventStore: %v", err)
	}
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(root, "l1.sqlite"))
	if err != nil {
		_ = store.Close()
		t.Fatalf("open L1 owner: %v", err)
	}
	t.Cleanup(func() {
		_ = l1.Close()
		_ = store.Close()
	})
	return store, l1
}

func commitProjectionTestTurn(t *testing.T, runtime *responseFirstConversationRuntime, ctx context.Context, request viewer.V2SendRequest) viewer.V2AcceptedTurn {
	t.Helper()
	accepted, err := runtime.Accept(ctx, request)
	if err != nil {
		t.Fatalf("Accept(): %v", err)
	}
	if err := runtime.Process(ctx, accepted); err != nil {
		t.Fatalf("Process(): %v", err)
	}
	return accepted
}

func TestResponseFirstProjectionDeliveryMarkerPrecedesPublishCommitted(t *testing.T) {
	store, l1 := openProjectionTestStores(t, t.TempDir())
	probe := &responseFirstProjectionProbePublisher{store: store}
	runtime := newResponseFirstProjectionRuntime(t, store, l1, probe, &responseFirstTestProvider{})
	request := responseFirstRequest(modulecore.NewSessionID(), "projection-marker-first", "hello")
	commitProjectionTestTurn(t, runtime, context.Background(), request)
	if probe.claimOnCall != 1 {
		t.Fatalf("projection work visible to PublishCommitted=%d, want 1 after marker", probe.claimOnCall)
	}
}

func TestResponseFirstProjectionReplyPublicationFailureDoesNotStarveMarkedWork(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	answerEvent := modulecore.NewRootEventEnvelope("orchestrator", "conversation.user_message", now, map[string]any{
		"from": "user", "source_actor_id": responseFirstActorID, "to": "mio", "content": "answer", "text": "answer",
		"visibility": "public", "reply_to_message_id": string(modulecore.NewMessageID()),
	})
	answerEvent.SessionID, answerEvent.TurnID, answerEvent.MessageID, answerEvent.EventSeq =
		modulecore.NewSessionID(), modulecore.NewTurnID(), modulecore.NewMessageID(), 4
	otherSession := modulecore.NewSessionID()
	otherEvent := modulecore.NewRootEventEnvelope("orchestrator", "agent.response", now, map[string]any{
		"from": "mio", "source_actor_id": "mio", "to": "user", "content": "another session reply", "text": "another session reply", "visibility": "public",
	})
	otherEvent.SessionID, otherEvent.TurnID, otherEvent.MessageID, otherEvent.EventSeq = otherSession, modulecore.NewTurnID(), modulecore.NewMessageID(), 8
	store := &responseFirstProjectionFairnessStore{
		pending: eventstore.PendingClarificationReplyProjection{
			WorkID: eventstore.ConversationProjectionWorkID(answerEvent.EventID), SourceEventID: answerEvent.EventID,
			SessionID: answerEvent.SessionID, Event: answerEvent,
		},
		markedWork: eventstore.LeasedWorkItem{
			WorkItem: eventstore.WorkItem{
				WorkID: "projection:other-session", DedupeKey: "projection:other-session", Kind: responseFirstProjectionWorkKind,
				SourceEventID: otherEvent.EventID, SessionID: otherSession, Priority: 10,
			},
			Attempt:    1,
			LeaseToken: "lease-token", Version: 1,
		},
	}
	publisher := &responseFirstProjectionFailingAcceptedPublisher{err: errors.New("injected accepted-input publication failure")}
	l1 := &responseFirstProjectionFairnessL1{}
	projection := newResponseFirstConversationProjection(store, l1, responseFirstTestOwnerID, nil, publisher)
	projection.now = func() time.Time { return now }
	progressed, err := projection.drainOne(context.Background())
	if !progressed || err == nil || !strings.Contains(err.Error(), "accepted-input publication failure") {
		t.Fatalf("drainOne()=(%t,%v), want marked work progress plus the reply publication error", progressed, err)
	}
	if publisher.calls != 1 || store.retryCalls != 1 || store.markCalls != 0 || store.claimCalls != 1 || store.loadCalls != 1 || l1.calls != 1 || store.completeCalls != 1 {
		t.Fatalf("reply failure fairness calls publisher=%d retry=%d mark=%d claim=%d load=%d l1=%d complete=%d; want retry plus ordinary marked projection completion",
			publisher.calls, store.retryCalls, store.markCalls, store.claimCalls, store.loadCalls, l1.calls, store.completeCalls)
	}
}

func TestResponseFirstTerminalClarificationReplyDeliveryReleasesChildAsIncomplete(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	answerEvent := modulecore.NewRootEventEnvelope("orchestrator", "conversation.user_message", now, map[string]any{
		"from": "user", "source_actor_id": responseFirstActorID, "to": "mio", "content": "answer", "text": "answer",
		"visibility": "public", "reply_to_message_id": string(modulecore.NewMessageID()),
	})
	answerEvent.SessionID, answerEvent.TurnID, answerEvent.MessageID, answerEvent.EventSeq =
		modulecore.NewSessionID(), modulecore.NewTurnID(), modulecore.NewMessageID(), 4
	otherSession := modulecore.NewSessionID()
	otherEvent := modulecore.NewRootEventEnvelope("orchestrator", "agent.response", now, map[string]any{
		"from": "mio", "source_actor_id": "mio", "to": "user", "content": "another session reply", "text": "another session reply", "visibility": "public",
	})
	otherEvent.SessionID, otherEvent.TurnID, otherEvent.MessageID, otherEvent.EventSeq = otherSession, modulecore.NewTurnID(), modulecore.NewMessageID(), 8
	workID := eventstore.ConversationProjectionWorkID(answerEvent.EventID)
	store := &responseFirstProjectionFairnessStore{
		pending: eventstore.PendingClarificationReplyProjection{
			WorkID: workID, SourceEventID: answerEvent.EventID, SessionID: answerEvent.SessionID, Event: answerEvent,
		},
		markedWork: eventstore.LeasedWorkItem{
			WorkItem: eventstore.WorkItem{
				WorkID: "projection:terminal-other-session", DedupeKey: "projection:terminal-other-session", Kind: responseFirstProjectionWorkKind,
				SourceEventID: otherEvent.EventID, SessionID: otherSession, Priority: 10,
			},
			Attempt:    1,
			LeaseToken: "lease-token", Version: 1,
		},
		retryErr: eventstore.ErrConversationProjectionTerminalAttention,
	}
	children := newResponseFirstProjectionChildren()
	batch, err := turntiming.NewBatchRecorder()
	if err != nil {
		t.Fatalf("NewBatchRecorder(): %v", err)
	}
	turn, _, err := batch.FindOrBeginTurn(string(answerEvent.TurnID), string(answerEvent.TraceID))
	if err != nil {
		t.Fatalf("FindOrBeginTurn(): %v", err)
	}
	if !turn.FinishForeground() {
		t.Fatal("FinishForeground() failed")
	}
	children.registerBeforeCommit(turntiming.WithBatchTurnRecorder(context.Background(), turn), workID)
	projection := newResponseFirstConversationProjection(store, &responseFirstProjectionFairnessL1{}, responseFirstTestOwnerID, children,
		&responseFirstProjectionFailingAcceptedPublisher{err: errors.New("persistent accepted-input failure")})
	projection.now = func() time.Time { return now }
	progressed, err := projection.drainOne(context.Background())
	if !progressed || !errors.Is(err, eventstore.ErrConversationProjectionTerminalAttention) {
		t.Fatalf("drainOne()=(%t,%v), want terminal attention while marked work progresses", progressed, err)
	}
	if len(children.entries) != 0 || store.completeCalls != 1 {
		t.Fatalf("terminal child entries=%d marked completions=%d; want child released as failed and other session completed", len(children.entries), store.completeCalls)
	}
	if err := batch.Freeze(); err != nil {
		t.Fatalf("Freeze() after terminal failed child: %v", err)
	}
	snapshot, err := batch.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot(): %v", err)
	}
	turnSnapshot, found := snapshot.Turn(0)
	if !found || !snapshot.IsIncomplete() || !turnSnapshot.WorkTrackingIncomplete() || turnSnapshot.PendingChildren() != 0 {
		t.Fatalf("terminal reply diagnostics incomplete=%t found=%t turn_incomplete=%t pending_children=%d; want failed terminal visible and no false successful drain",
			snapshot.IsIncomplete(), found, turnSnapshot.WorkTrackingIncomplete(), turnSnapshot.PendingChildren())
	}
}

func TestResponseFirstProjectionRuntimeFailsClosedWithoutSelectedL1ProjectionPort(t *testing.T) {
	store, _ := openProjectionTestStores(t, t.TempDir())
	_, err := newResponseFirstConversationRuntimeWithProjection(
		store, agent.NewMioAgent(nil, nil, nil, nil, nil, nil), &responseFirstTestProvider{},
		newResponseFirstTestPublisher(), responseFirstTestOwnerID, struct{}{},
	)
	if err == nil || !strings.Contains(err.Error(), "selected EventStore and Chat L1 owner projection ports") {
		t.Fatalf("runtime without selected L1 projection port error=%v, want fail-closed configuration error", err)
	}
}

func TestResponseFirstProjectionCannotClaimBeforeDeliveryAttemptMarker(t *testing.T) {
	store, _ := openProjectionTestStores(t, t.TempDir())
	publisher := newResponseFirstTestPublisher()
	runtime, err := newResponseFirstConversationRuntime(
		store, agent.NewMioAgent(nil, nil, nil, nil, nil, nil), &responseFirstTestProvider{}, publisher, responseFirstTestOwnerID,
	)
	if err != nil {
		t.Fatalf("build response-first runtime: %v", err)
	}
	allowResponseFirstTestReservations(runtime)
	request := responseFirstRequest(modulecore.NewSessionID(), "projection-marker-gate", "hello")
	accepted := commitProjectionTestTurn(t, runtime, context.Background(), request)
	select {
	case bundle := <-publisher.committed:
		var publicOutput modulecore.EventEnvelope
		for _, event := range bundle.Events {
			if responseFirstPublicProjectionOutput(event) {
				publicOutput = event
				break
			}
		}
		if publicOutput.EventID == "" {
			t.Fatalf("committed bundle has no public projection output: %+v", bundle.Events)
		}
		if len(claimProjectionWork(t, store)) != 0 {
			t.Fatal("projection was claimable before its first delivery-attempt marker")
		}
		if err := store.MarkConversationProjectionDeliveryAttempt(context.Background(), eventstore.ConversationProjectionDeliveryAttemptInput{
			WorkID: "projection:" + string(accepted.TurnID), SourceEventID: publicOutput.EventID,
			OwnerID: responseFirstTestOwnerID, AttemptedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("MarkConversationProjectionDeliveryAttempt(): %v", err)
		}
		if len(claimProjectionWork(t, store)) != 1 {
			t.Fatal("projection did not become claimable after its delivery-attempt marker")
		}
	case <-time.After(time.Second):
		t.Fatal("committed output was not published")
	}
}

func claimProjectionWork(t *testing.T, store *eventstore.SQLiteStore) []eventstore.LeasedWorkItem {
	t.Helper()
	claimed, err := store.ClaimWork(context.Background(), eventstore.WorkClaimInput{
		Kind: responseFirstProjectionWorkKind, Limit: 1, LeaseDuration: time.Minute, Now: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("ClaimWork(conversation_projection): %v", err)
	}
	return claimed
}

func TestResponseFirstProjectionAppliesCommittedOutputToSelectedL1Owner(t *testing.T) {
	store, l1 := openProjectionTestStores(t, t.TempDir())
	runtime := newResponseFirstProjectionRuntime(t, store, l1, newResponseFirstTestPublisher(), &responseFirstTestProvider{})
	request := responseFirstRequest(modulecore.NewSessionID(), "projection-live-l1", "hello")
	commitProjectionTestTurn(t, runtime, context.Background(), request)
	progressed, err := runtime.projection.drainOne(context.Background())
	if err != nil || !progressed {
		t.Fatalf("drainOne()=(%t,%v), want projection success", progressed, err)
	}
	rows, err := l1.RecentBySession(context.Background(), string(request.SessionID), 10)
	if err != nil {
		t.Fatalf("read selected L1 owner: %v", err)
	}
	if len(rows) != 2 || rows[0].Source != "conversation_projection" || rows[1].Source != "conversation_projection" {
		t.Fatalf("L1 projection rows=%+v, want the committed public user/reply pair", rows)
	}
	if (rows[0].Message != "hello" && rows[1].Message != "hello") || (rows[0].Message != "response-first reply" && rows[1].Message != "response-first reply") {
		t.Fatalf("L1 projection messages=%q/%q, want the committed public pair", rows[0].Message, rows[1].Message)
	}
}

func TestResponseFirstProjectionPublisherFailsClosedOnCompletedWorkReplay(t *testing.T) {
	store, l1 := openProjectionTestStores(t, t.TempDir())
	basePublisher := newResponseFirstTestPublisher()
	runtime := newResponseFirstProjectionRuntime(t, store, l1, basePublisher, &responseFirstTestProvider{})
	request := responseFirstRequest(modulecore.NewSessionID(), "projection-completed-replay", "hello")
	commitProjectionTestTurn(t, runtime, context.Background(), request)
	var committed conversationruntime.CommittedBundle
	select {
	case committed = <-basePublisher.committed:
	case <-time.After(time.Second):
		t.Fatal("initial committed output was not published")
	}
	if progressed, err := runtime.projection.drainOne(context.Background()); !progressed || err != nil {
		t.Fatalf("drainOne()=(%t,%v), want completed projection", progressed, err)
	}
	if err := runtime.publisher.PublishCommitted(context.Background(), committed); err != nil {
		t.Fatalf("PublishCommitted(exact completed replay): %v; same-identity delivery markers are idempotent", err)
	}
	select {
	case replay := <-basePublisher.committed:
		if !reflect.DeepEqual(replay, committed) {
			t.Fatalf("completed work replay=%#v, want the exact committed receipt %#v", replay, committed)
		}
	case <-time.After(time.Second):
		t.Fatal("exact completed replay was not republished")
	}
}

func TestResponseFirstProjectionRetriesAfterL1ApplyAndCompletionFailure(t *testing.T) {
	baseStore, l1 := openProjectionTestStores(t, t.TempDir())
	store := &responseFirstProjectionCompleteFailStore{SQLiteStore: baseStore}
	store.failNext.Store(true)
	runtime := newResponseFirstProjectionRuntime(t, store, l1, newResponseFirstTestPublisher(), &responseFirstTestProvider{})
	projectionNow := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	runtime.projection.now = func() time.Time { return projectionNow }
	request := responseFirstRequest(modulecore.NewSessionID(), "projection-ack-retry", "hello")
	batch, err := turntiming.NewBatchRecorder()
	if err != nil {
		t.Fatalf("NewBatchRecorder(): %v", err)
	}
	turn, _, err := batch.FindOrBeginTurn(string(request.TurnID), string(request.TraceID))
	if err != nil {
		t.Fatalf("FindOrBeginTurn(): %v", err)
	}
	ctx := turntiming.WithBatchTurnRecorder(context.Background(), turn)
	commitProjectionTestTurn(t, runtime, ctx, request)
	if !turn.FinishForeground() {
		t.Fatal("FinishForeground() failed")
	}
	batch.CloseAdmission()

	if progressed, err := runtime.projection.drainOne(context.Background()); !progressed || err == nil || !strings.Contains(err.Error(), "completion failure") {
		t.Fatalf("first drainOne()=(%t,%v), want injected completion failure after L1 apply", progressed, err)
	}
	rows, err := l1.RecentBySession(context.Background(), string(request.SessionID), 10)
	if err != nil || len(rows) != 2 {
		t.Fatalf("L1 rows after first apply=%+v err=%v, want committed public pair", rows, err)
	}
	claimedBeforeRetry, err := baseStore.ClaimWork(context.Background(), eventstore.WorkClaimInput{
		Kind: responseFirstProjectionWorkKind, Limit: 1, LeaseDuration: time.Minute, Now: projectionNow.Add(time.Second - time.Millisecond),
	})
	if err != nil || len(claimedBeforeRetry) != 0 {
		t.Fatalf("claim before durable 1s not_before=%+v err=%v, want no eligible retry", claimedBeforeRetry, err)
	}
	projectionNow = projectionNow.Add(time.Second)
	if progressed, err := runtime.projection.drainOne(context.Background()); !progressed || err != nil {
		t.Fatalf("retry drainOne() at deterministic 1s not_before=(%t,%v), want idempotent L1 replay and owner completion", progressed, err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := batch.WaitDrained(waitCtx); err != nil {
		t.Fatalf("WaitDrained() after exact owner completion: %v", err)
	}
	if err := batch.Freeze(); err != nil {
		t.Fatalf("Freeze(): %v", err)
	}
	snapshot, err := batch.Snapshot()
	if err != nil || snapshot.IsIncomplete() {
		t.Fatalf("timing snapshot incomplete=%t err=%v, want clean in-memory child drain", snapshot.IsIncomplete(), err)
	}
	rows, err = l1.RecentBySession(context.Background(), string(request.SessionID), 10)
	if err != nil || len(rows) != 2 {
		t.Fatalf("L1 rows after retry=%+v err=%v, want idempotent public pair", rows, err)
	}
}

func TestResponseFirstProjectionChildRejectsStaleLeaseCompletion(t *testing.T) {
	batch, err := turntiming.NewBatchRecorder()
	if err != nil {
		t.Fatalf("NewBatchRecorder(): %v", err)
	}
	turn, err := batch.BeginTurn()
	if err != nil {
		t.Fatalf("BeginTurn(): %v", err)
	}
	children := newResponseFirstProjectionChildren()
	children.registerBeforeCommit(turntiming.WithBatchTurnRecorder(context.Background(), turn), "projection:turn-stale-lease")
	if !turn.FinishForeground() {
		t.Fatal("FinishForeground() failed")
	}
	batch.CloseAdmission()
	oldLease := eventstore.LeasedWorkItem{WorkItem: eventstore.WorkItem{WorkID: "projection:turn-stale-lease"}, LeaseToken: "old", Version: 1}
	newLease := eventstore.LeasedWorkItem{WorkItem: eventstore.WorkItem{WorkID: "projection:turn-stale-lease"}, LeaseToken: "new", Version: 2}
	oldGeneration, tracked := children.beginLease(oldLease)
	if !tracked {
		t.Fatal("first lease was not associated with its registered child")
	}
	newGeneration, tracked := children.beginLease(newLease)
	if !tracked || newGeneration <= oldGeneration {
		t.Fatalf("reclaimed lease generation=(%d,%t), previous=%d", newGeneration, tracked, oldGeneration)
	}
	if children.complete(oldLease.WorkID, oldGeneration, eventstore.WorkLeaseInput{LeaseToken: oldLease.LeaseToken, Version: oldLease.Version}) {
		t.Fatal("stale lease completion released the child")
	}
	if children.complete(newLease.WorkID, newGeneration, eventstore.WorkLeaseInput{LeaseToken: newLease.LeaseToken, Version: newLease.Version}) != true {
		t.Fatal("current owner completion did not release its registered child")
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := batch.WaitDrained(waitCtx); err != nil {
		t.Fatalf("WaitDrained() after current lease completion: %v", err)
	}
}

func TestResponseFirstNextReadViewUsesCanonicalTailWhileProjectionPaused(t *testing.T) {
	store, l1 := openProjectionTestStores(t, t.TempDir())
	provider := &responseFirstProjectionCaptureProvider{}
	runtime := newResponseFirstProjectionRuntime(t, store, l1, newResponseFirstTestPublisher(), provider)
	sessionID := modulecore.NewSessionID()
	first := responseFirstRequest(sessionID, "projection-paused-first", "first question")
	commitProjectionTestTurn(t, runtime, context.Background(), first)
	rows, err := l1.RecentBySession(context.Background(), string(sessionID), 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("L1 rows while projection lane paused=%+v err=%v, want empty derived view", rows, err)
	}
	second := responseFirstRequest(sessionID, "projection-paused-second", "second question")
	commitProjectionTestTurn(t, runtime, context.Background(), second)
	if len(provider.requests) != 2 {
		t.Fatalf("generation request count=%d, want 2", len(provider.requests))
	}
	seenPriorReply := false
	for _, message := range provider.requests[1].Messages {
		if message.Role == "assistant" && message.Content == "response-first reply" {
			seenPriorReply = true
			break
		}
	}
	if !seenPriorReply {
		t.Fatalf("second foreground readview omitted first committed source-tail reply: %+v", provider.requests[1].Messages)
	}
	rows, err = l1.RecentBySession(context.Background(), string(sessionID), 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("L1 rows after two turns with paused lane=%+v err=%v, want projection still paused", rows, err)
	}
}

func TestResponseFirstProjectionRecoversDurableWorkAfterRestartWithoutRAMChild(t *testing.T) {
	root := t.TempDir()
	store, l1 := openProjectionTestStores(t, root)
	request := responseFirstRequest(modulecore.NewSessionID(), "projection-restart", "hello")
	runtime := newResponseFirstProjectionRuntime(t, store, l1, newResponseFirstTestPublisher(), &responseFirstTestProvider{})
	commitProjectionTestTurn(t, runtime, context.Background(), request)
	if len(runtime.projection.children.entries) != 0 {
		t.Fatal("untimed foreground unexpectedly created a RAM timing child")
	}
	if err := l1.Close(); err != nil {
		t.Fatalf("close first L1 owner: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close first EventStore: %v", err)
	}

	restartedStore, err := eventstore.NewSQLiteStore(filepath.Join(root, "event-store.sqlite"))
	if err != nil {
		t.Fatalf("reopen EventStore: %v", err)
	}
	defer restartedStore.Close()
	restartedL1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(root, "l1.sqlite"))
	if err != nil {
		t.Fatalf("reopen selected L1 owner: %v", err)
	}
	defer restartedL1.Close()
	restarted := newResponseFirstProjectionRuntime(t, restartedStore, restartedL1, newResponseFirstTestPublisher(), &responseFirstTestProvider{})
	if progressed, err := restarted.projection.drainOne(context.Background()); !progressed || err != nil {
		t.Fatalf("restart drainOne()=(%t,%v), want durable projection recovery", progressed, err)
	}
	rows, err := restartedL1.RecentBySession(context.Background(), string(request.SessionID), 10)
	if err != nil || len(rows) != 2 {
		t.Fatalf("recovered L1 rows=%+v err=%v, want persisted public user/reply pair", rows, err)
	}
	foundReply := false
	for _, row := range rows {
		foundReply = foundReply || row.Message == "response-first reply"
	}
	if !foundReply {
		t.Fatalf("recovered L1 rows=%+v, missing persisted public reply", rows)
	}
}
