package eventstore

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func newChatAdmissionStore(t *testing.T, capacity int) (*SQLiteStore, *persistencequeue.Queue[persistencequeue.PersistenceCommand], *PersistenceQueueWriteQueue, func()) {
	t.Helper()
	queue, err := persistencequeue.NewPersistenceQueue(capacity, 1<<20)
	if err != nil {
		t.Fatalf("NewPersistenceQueue: %v", err)
	}
	writeQueue, err := NewPersistenceQueueWriteQueue(queue)
	if err != nil {
		t.Fatalf("NewPersistenceQueueWriteQueue: %v", err)
	}
	store, err := NewSQLiteStoreWithWriteQueue(filepath.Join(t.TempDir(), "chat-admission.db"), writeQueue)
	if err != nil {
		t.Fatalf("NewSQLiteStoreWithWriteQueue: %v", err)
	}
	expectedQueueFailure := false
	allowExpectedQueueFailure := func() { expectedQueueFailure = true }
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := queue.Shutdown(ctx); err != nil && !(expectedQueueFailure && errors.Is(err, persistencequeue.ErrFailed)) {
			t.Errorf("persistence queue shutdown: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Errorf("event store close: %v", err)
		}
	})
	return store, queue, writeQueue, allowExpectedQueueFailure
}

func newChatAdmissionContext(t *testing.T, queue *persistencequeue.Queue[persistencequeue.PersistenceCommand], count int) (context.Context, *persistencequeue.Admission) {
	t.Helper()
	admission, err := persistencequeue.Admit(queue, count, 1<<19)
	if err != nil {
		t.Fatalf("persistencequeue.Admit: %v", err)
	}
	ctx := persistencequeue.WithAdmission(context.Background(), admission)
	t.Cleanup(func() {
		if err := admission.Release(); err != nil {
			t.Errorf("release persistence admission: %v", err)
		}
	})
	return ctx, admission
}

func enqueueChatAdmissionBlocker(t *testing.T, queue *persistencequeue.Queue[persistencequeue.PersistenceCommand]) func() {
	t.Helper()
	started := make(chan struct{})
	release := make(chan struct{})
	if _, err := queue.Enqueue(persistencequeue.PersistenceCommand{
		Owner: persistencequeue.OwnerEvent, Kind: "eventstore.test-block", SizeBytes: 1,
		Apply: func(context.Context) error {
			close(started)
			<-release
			return nil
		},
	}, 1); err != nil {
		t.Fatalf("enqueue blocking head: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("blocking queue head did not start")
	}
	var releaseOnce sync.Once
	finish := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(finish)
	return finish
}

func TestAdmitSequencedReturnsBeforeBlockedWriterAndPersistsAfterRequestCancellation(t *testing.T) {
	store, queue, _, _ := newChatAdmissionStore(t, 6)
	ctx, admission := newChatAdmissionContext(t, queue, 2)
	requestCtx, cancelRequest := context.WithCancel(ctx)
	defer cancelRequest()
	releaseBlocker := enqueueChatAdmissionBlocker(t, queue)
	lease, err := store.TryBeginChat(requestCtx)
	if err != nil {
		t.Fatalf("TryBeginChat with idle EventSeq writer: %v", err)
	}
	defer lease.EndChat()

	event := eventFixture(modulecore.NewTraceID(), "chat.initial", "orchestrator")
	type result struct {
		event  modulecore.EventEnvelope
		ticket uint64
		err    error
	}
	accepted := make(chan result, 1)
	go func() {
		stored, ticket, err := store.AdmitSequenced(requestCtx, event)
		accepted <- result{event: stored, ticket: ticket, err: err}
	}()
	var got result
	select {
	case got = <-accepted:
	case <-time.After(time.Second):
		t.Fatal("AdmitSequenced waited for the blocked writer")
	}
	if got.err != nil {
		t.Fatalf("AdmitSequenced: %v", got.err)
	}
	if got.event.EventSeq != 1 || got.event.EventID != event.EventID || got.ticket != 2 {
		t.Fatalf("AdmitSequenced result = (seq %d, id %q, ticket %d), want (1, %q, 2)", got.event.EventSeq, got.event.EventID, got.ticket, event.EventID)
	}
	if status := queue.Status(); status.Pending != 2 || status.OldestSequence != 1 {
		t.Fatalf("queue status after RAM admission = %#v, want blocker and admitted event pending", status)
	}

	// Request cancellation after acceptance must not cancel the queue-owned write.
	cancelRequest()
	if err := admission.Barrier(requestCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled admission barrier = %v, want context.Canceled", err)
	}
	releaseBlocker()
	if err := queue.Wait(context.Background(), got.ticket); err != nil {
		t.Fatalf("wait for accepted command after request cancellation: %v", err)
	}
	persisted, found, err := store.GetByID(context.Background(), got.event.EventID)
	if err != nil || !found {
		t.Fatalf("GetByID after queue drain: found=%v err=%v", found, err)
	}
	if persisted.EventSeq != got.event.EventSeq || persisted.EventID != got.event.EventID {
		t.Fatalf("persisted event = (seq %d, id %q), admitted (seq %d, id %q)", persisted.EventSeq, persisted.EventID, got.event.EventSeq, got.event.EventID)
	}
}

func TestTryBeginChatRejectsEarlierPendingWriterAndSerializesChatLease(t *testing.T) {
	store, queue, _, _ := newChatAdmissionStore(t, 6)
	releaseBlocker := enqueueChatAdmissionBlocker(t, queue)
	firstWrite := eventFixture(modulecore.NewTraceID(), "writer.before-chat", "orchestrator")
	writeResult := make(chan error, 1)
	go func() {
		_, err := store.AppendSequenced(context.Background(), firstWrite)
		writeResult <- err
	}()
	waitForQueuePendingCount(t, queue, 2)

	ctx, _ := newChatAdmissionContext(t, queue, 2)
	leaseResult := make(chan error, 1)
	go func() {
		_, err := store.TryBeginChat(ctx)
		leaseResult <- err
	}()
	select {
	case err := <-leaseResult:
		if !errors.Is(err, ErrEventSeqWritePending) {
			t.Fatalf("TryBeginChat with an earlier pending EventSeq write = %v, want ErrEventSeqWritePending", err)
		}
	case <-time.After(time.Second):
		t.Fatal("TryBeginChat waited for an earlier pending EventSeq write")
	}
	releaseBlocker()
	select {
	case err := <-writeResult:
		if err != nil {
			t.Fatalf("earlier AppendSequenced: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("earlier AppendSequenced did not finish after writer release")
	}

	lease, err := store.TryBeginChat(ctx)
	if err != nil {
		t.Fatalf("TryBeginChat after prior write completed: %v", err)
	}
	if _, err := store.TryBeginChat(ctx); !errors.Is(err, ErrChatLeaseActive) {
		t.Fatalf("second TryBeginChat = %v, want ErrChatLeaseActive", err)
	}
	if _, err := store.AppendSequenced(ctx, eventFixture(modulecore.NewTraceID(), "writer.same-chat-admission", "orchestrator")); !errors.Is(err, ErrChatLeaseActive) {
		t.Fatalf("synchronous write carrying the active CHAT admission = %v, want immediate ErrChatLeaseActive", err)
	}
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancelledWrite := make(chan error, 1)
	go func() {
		_, err := store.AppendSequenced(cancelCtx, eventFixture(modulecore.NewTraceID(), "writer.cancelled-during-chat", "orchestrator"))
		cancelledWrite <- err
	}()
	waitForWaitingSyncSequenceWrites(t, store, 1)
	cancel()
	select {
	case err := <-cancelledWrite:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("synchronous write cancelled during CHAT lease = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled synchronous writer stayed blocked on the CHAT lease")
	}
	blockedWrite := make(chan error, 1)
	go func() {
		_, err := store.AppendSequenced(context.Background(), eventFixture(modulecore.NewTraceID(), "writer.during-chat", "orchestrator"))
		blockedWrite <- err
	}()
	waitForWaitingSyncSequenceWrites(t, store, 1)
	select {
	case err := <-blockedWrite:
		t.Fatalf("non-CHAT writer completed before EndChat: %v", err)
	case <-time.After(time.Second):
		// Ordinary synchronous writers remain context-aware waiters until lease end.
	}
	if err := lease.EndChat(); err != nil {
		t.Fatalf("EndChat: %v", err)
	}
	if err := lease.EndChat(); err != nil {
		t.Fatalf("idempotent EndChat: %v", err)
	}
	select {
	case err := <-blockedWrite:
		if err != nil {
			t.Fatalf("non-CHAT EventSeq writer after EndChat: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("non-CHAT writer did not resume after EndChat")
	}
}

func TestAdmittedChatAndSynchronousEventWriterKeepFIFOWithoutDeadlock(t *testing.T) {
	store, queue, _, _ := newChatAdmissionStore(t, 6)
	ctx, _ := newChatAdmissionContext(t, queue, 3)
	releaseBlocker := enqueueChatAdmissionBlocker(t, queue)
	lease, err := store.TryBeginChat(ctx)
	if err != nil {
		t.Fatalf("TryBeginChat: %v", err)
	}
	chatEvent := eventFixture(modulecore.NewTraceID(), "chat.accepted", "orchestrator")
	admitted, ticket, err := store.AdmitSequenced(ctx, chatEvent)
	if err != nil {
		t.Fatalf("AdmitSequenced: %v", err)
	}
	if err := lease.EndChat(); err != nil {
		t.Fatalf("EndChat: %v", err)
	}
	secondLease, err := store.TryBeginChat(ctx)
	if err != nil {
		t.Fatalf("TryBeginChat after publication while prior CHAT append is queued: %v", err)
	}
	secondChatEvent := eventFixture(modulecore.NewTraceID(), "chat.accepted.next", "orchestrator")
	secondAdmitted, secondTicket, err := store.AdmitSequenced(ctx, secondChatEvent)
	if err != nil {
		t.Fatalf("AdmitSequenced during next CHAT lease: %v", err)
	}
	if err := secondLease.EndChat(); err != nil {
		t.Fatalf("EndChat for second CHAT: %v", err)
	}

	nonChatEvent := eventFixture(modulecore.NewTraceID(), "writer.after-chat", "orchestrator")
	writeResult := make(chan resultEvent, 1)
	go func() {
		stored, err := store.AppendSequenced(context.Background(), nonChatEvent)
		writeResult <- resultEvent{event: stored, err: err}
	}()
	waitForQueuePendingCount(t, queue, 4)
	select {
	case got := <-writeResult:
		t.Fatalf("synchronous AppendSequenced returned before FIFO head drained: seq=%d err=%v", got.event.EventSeq, got.err)
	default:
	}
	releaseBlocker()
	if err := queue.Wait(context.Background(), ticket); err != nil {
		t.Fatalf("wait for CHAT EventSeq ticket: %v", err)
	}
	if err := queue.Wait(context.Background(), secondTicket); err != nil {
		t.Fatalf("wait for next CHAT EventSeq ticket: %v", err)
	}
	var written resultEvent
	select {
	case written = <-writeResult:
	case <-time.After(time.Second):
		t.Fatal("synchronous EventSeq writer deadlocked behind admitted CHAT write")
	}
	if written.err != nil {
		t.Fatalf("synchronous AppendSequenced: %v", written.err)
	}
	if admitted.EventSeq != 1 || secondAdmitted.EventSeq != 2 || written.event.EventSeq != 3 {
		t.Fatalf("mixed writer sequences = (%d, %d, %d), want (1, 2, 3)", admitted.EventSeq, secondAdmitted.EventSeq, written.event.EventSeq)
	}
}

func TestAdmitSequencedRequiresAdmissionBoundToConfiguredQueueAndActiveChatLease(t *testing.T) {
	store, _, _, _ := newChatAdmissionStore(t, 6)
	otherQueue, err := persistencequeue.NewPersistenceQueue(4, 1<<20)
	if err != nil {
		t.Fatalf("NewPersistenceQueue for mismatched capability: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := otherQueue.Shutdown(ctx); err != nil {
			t.Errorf("mismatched queue shutdown: %v", err)
		}
	})
	wrongCtx, _ := newChatAdmissionContext(t, otherQueue, 1)
	if _, err := store.TryBeginChat(context.Background()); !errors.Is(err, ErrPersistenceAdmissionRequired) {
		t.Fatalf("TryBeginChat without explicit admission = %v, want ErrPersistenceAdmissionRequired", err)
	}
	if _, err := store.TryBeginChat(wrongCtx); !errors.Is(err, ErrPersistenceAdmissionQueueMismatch) {
		t.Fatalf("TryBeginChat with mismatched queue = %v, want ErrPersistenceAdmissionQueueMismatch", err)
	}
	if _, _, err := store.AdmitSequenced(wrongCtx, eventFixture(modulecore.NewTraceID(), "chat.unleased", "orchestrator")); !errors.Is(err, ErrPersistenceAdmissionQueueMismatch) {
		t.Fatalf("AdmitSequenced with mismatched queue = %v, want ErrPersistenceAdmissionQueueMismatch", err)
	}
	if _, _, err := store.AdmitSequenced(context.Background(), eventFixture(modulecore.NewTraceID(), "chat.no-admission", "orchestrator")); !errors.Is(err, ErrPersistenceAdmissionRequired) {
		t.Fatalf("AdmitSequenced without explicit admission = %v, want ErrPersistenceAdmissionRequired", err)
	}
	if status := store.writeQueue.(*PersistenceQueueWriteQueue).queue.Status(); status.Pending != 0 {
		t.Fatalf("configured queue status after rejected admissions = %#v, want no commands", status)
	}
}

func TestAdmittedAppendFailurePoisonsItsFIFOHead(t *testing.T) {
	store, queue, _, allowExpectedFailure := newChatAdmissionStore(t, 6)
	ctx, _ := newChatAdmissionContext(t, queue, 3)
	releaseBlocker := enqueueChatAdmissionBlocker(t, queue)
	lease, err := store.TryBeginChat(ctx)
	if err != nil {
		t.Fatalf("TryBeginChat: %v", err)
	}
	defer lease.EndChat()
	duplicate := eventFixture(modulecore.NewTraceID(), "chat.duplicate", "orchestrator")
	first, firstTicket, err := store.AdmitSequenced(ctx, duplicate)
	if err != nil {
		t.Fatalf("AdmitSequenced(first): %v", err)
	}
	second, secondTicket, err := store.AdmitSequenced(ctx, duplicate)
	if err != nil {
		t.Fatalf("AdmitSequenced(duplicate): %v", err)
	}
	if secondTicket != firstTicket+1 || second.EventSeq != first.EventSeq+1 {
		t.Fatalf("duplicate admissions = (seq %d,ticket %d), (seq %d,ticket %d), want consecutive values", first.EventSeq, firstTicket, second.EventSeq, secondTicket)
	}
	laterEvent := eventFixture(modulecore.NewTraceID(), "chat.after-conflict", "orchestrator")
	_, laterTicket, err := store.AdmitSequenced(ctx, laterEvent)
	if err != nil {
		t.Fatalf("AdmitSequenced(after conflict): %v", err)
	}
	releaseBlocker()
	if err := queue.Wait(context.Background(), secondTicket); err == nil || !errors.Is(err, persistencequeue.ErrFailed) {
		t.Fatalf("queue.Wait(duplicate admission) = %v, want poisoned queue head", err)
	}
	status := queue.Status()
	if status.Failure == nil || status.Failure.Sequence != secondTicket {
		t.Fatalf("queue failure = %#v, want sequence %d", status.Failure, secondTicket)
	}
	if status.Pending != 2 || status.OldestSequence != secondTicket {
		t.Fatalf("queue pending after failed append = %#v, want failed head and later record retained", status)
	}
	persisted, found, err := store.GetByID(context.Background(), first.EventID)
	if err != nil || !found || persisted.EventSeq != first.EventSeq {
		t.Fatalf("first accepted event after poison: found=%v seq=%d err=%v", found, persisted.EventSeq, err)
	}
	if _, found, err := store.GetByID(context.Background(), laterEvent.EventID); err != nil || found {
		t.Fatalf("later admitted event after poison: found=%v err=%v, want not persisted", found, err)
	}
	if err := queue.Wait(context.Background(), laterTicket); err == nil || !errors.Is(err, persistencequeue.ErrFailed) {
		t.Fatalf("queue.Wait(later event) = %v, want same poisoned head", err)
	}
	allowExpectedFailure()
}

type resultEvent struct {
	event modulecore.EventEnvelope
	err   error
}

func waitForQueuePendingCount(t *testing.T, queue *persistencequeue.Queue[persistencequeue.PersistenceCommand], count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if queue.Status().Pending == count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("queue pending = %d, want %d", queue.Status().Pending, count)
}

func waitForWaitingSyncSequenceWrites(t *testing.T, store *SQLiteStore, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		store.sequenceAdmissionMu.Lock()
		waiting := store.waitingSyncSequenceWrites
		store.sequenceAdmissionMu.Unlock()
		if waiting == count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	store.sequenceAdmissionMu.Lock()
	waiting := store.waitingSyncSequenceWrites
	store.sequenceAdmissionMu.Unlock()
	t.Fatalf("waiting synchronous EventSeq writes = %d, want %d", waiting, count)
}

func TestPersistenceQueueWriteQueueRejectsMismatchedAdmission(t *testing.T) {
	first, err := persistencequeue.NewPersistenceQueue(2, 1024)
	if err != nil {
		t.Fatal(err)
	}
	second, err := persistencequeue.NewPersistenceQueue(2, 1024)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = first.Shutdown(ctx)
		_ = second.Shutdown(ctx)
	})
	adapter, err := NewPersistenceQueueWriteQueue(first)
	if err != nil {
		t.Fatal(err)
	}
	admission, err := persistencequeue.Admit(second, 1, 32)
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Release()
	if _, err := adapter.EnqueueAdmission(context.Background(), admission, 1, func(context.Context) error { return nil }); !errors.Is(err, ErrPersistenceAdmissionQueueMismatch) {
		t.Fatalf("EnqueueAdmission mismatched queue = %v, want explicit queue mismatch", err)
	}
}

func TestTryBeginChatRejectsCanceledContextAndIsBounded(t *testing.T) {
	store, queue, _, _ := newChatAdmissionStore(t, 4)
	ctx, _ := newChatAdmissionContext(t, queue, 1)
	lease, err := store.TryBeginChat(ctx)
	if err != nil {
		t.Fatalf("TryBeginChat: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	cancelledResult := make(chan error, 1)
	go func() {
		_, err := store.TryBeginChat(cancelled)
		cancelledResult <- err
	}()
	select {
	case err := <-cancelledResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("TryBeginChat with canceled context = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("TryBeginChat waited for a canceled request")
	}
	if err := lease.EndChat(); err != nil {
		t.Fatal(err)
	}
}

func TestTryBeginChatRejectsQueueFailureAfterAdmissionReservation(t *testing.T) {
	store, queue, _, allowExpectedFailure := newChatAdmissionStore(t, 4)
	ctx, _ := newChatAdmissionContext(t, queue, 1)
	failure := errors.New("earlier shared persistence write failed")
	ticket, err := queue.Enqueue(persistencequeue.PersistenceCommand{
		Owner: persistencequeue.OwnerSession, Kind: "eventstore.test-prior-failure", SizeBytes: 1,
		Apply: func(context.Context) error { return failure },
	}, 1)
	if err != nil {
		t.Fatalf("enqueue failing prior persistence command: %v", err)
	}
	if err := queue.Wait(context.Background(), ticket); err == nil || !errors.Is(err, persistencequeue.ErrFailed) {
		t.Fatalf("wait for failing prior persistence command = %v, want queue failure", err)
	}
	if _, err := store.TryBeginChat(ctx); err == nil || !errors.Is(err, persistencequeue.ErrFailed) {
		t.Fatalf("TryBeginChat after shared queue failure = %v, want queue failure", err)
	}
	allowExpectedFailure()
}

func TestAdmittedChatQueueDoesNotWaitOnSequenceAdmissionMutexDuringWorkerWrite(t *testing.T) {
	store, queue, _, _ := newChatAdmissionStore(t, 6)
	ctx, _ := newChatAdmissionContext(t, queue, 2)
	releaseBlocker := enqueueChatAdmissionBlocker(t, queue)
	lease, err := store.TryBeginChat(ctx)
	if err != nil {
		t.Fatalf("TryBeginChat: %v", err)
	}
	defer lease.EndChat()
	if _, _, err := store.AdmitSequenced(ctx, eventFixture(modulecore.NewTraceID(), "chat.mutex", "orchestrator")); err != nil {
		t.Fatal(err)
	}
	if got := queue.Status().Pending; got != 2 {
		t.Fatalf("queue pending = %d, want 2 while blocker holds worker", got)
	}
	if err := lease.EndChat(); err != nil {
		t.Fatal(err)
	}
	// A synchronous writer must be able to enqueue behind the admitted CHAT
	// command without being held out by a mutex kept across the LLM lifetime.
	writeResult := make(chan error, 1)
	go func() {
		_, err := store.AppendSequenced(context.Background(), eventFixture(modulecore.NewTraceID(), "chat.mutex.follow-up", "orchestrator"))
		writeResult <- err
	}()
	waitForQueuePendingCount(t, queue, 3)
	releaseBlocker()
	select {
	case err := <-writeResult:
		if err != nil {
			t.Fatalf("follow-up AppendSequenced: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("follow-up synchronous writer deadlocked")
	}
}
