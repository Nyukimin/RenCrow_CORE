package transportmanager_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/transportmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	actionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
	transportpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestManagerOwnsMultipleTransportRequestsWithinOneAttempt(t *testing.T) {
	ctx, actions, action, attempt := transportTestAction(t)
	storeRoot := filepath.Join(t.TempDir(), "transport")
	store, err := transportpersistence.NewJSONLStore(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	manager := transportmanager.New(store, actions)

	first, err := manager.BeginRequest(ctx, "llm.chat")
	if err != nil {
		t.Fatal(err)
	}
	firstResponse, err := manager.CompleteResponse(ctx, first.RequestID, "provider-response-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.BeginRequest(ctx, "llm.chat")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.FailWithoutResponse(ctx, second.RequestID, "dial failed"); err != nil {
		t.Fatal(err)
	}

	if first.RequestID == second.RequestID || first.Sequence != 1 || second.Sequence != 2 {
		t.Fatalf("requests do not preserve ordered unique identity: first=%#v second=%#v", first, second)
	}
	if first.AttemptID != attempt.AttemptID || second.AttemptID != attempt.AttemptID || first.ActionID != action.ActionID || second.ActionID != action.ActionID {
		t.Fatalf("request lineage escaped one Action Attempt: first=%#v second=%#v", first, second)
	}
	if firstResponse.RequestID != first.RequestID || firstResponse.ResponseID == "" || firstResponse.ExternalRef != "provider-response-1" {
		t.Fatalf("response lineage=%#v", firstResponse)
	}
	requests, err := manager.ListRequests(context.Background(), domaintransport.RequestFilter{AttemptID: attempt.AttemptID})
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || requests[0].Status != domaintransport.RequestStatusResponded || requests[1].Status != domaintransport.RequestStatusFailed {
		t.Fatalf("requests=%#v", requests)
	}
	responses, err := manager.ListResponses(context.Background(), domaintransport.ResponseFilter{AttemptID: attempt.AttemptID})
	if err != nil {
		t.Fatal(err)
	}
	if len(responses) != 1 || responses[0].RequestID != first.RequestID {
		t.Fatalf("responses=%#v", responses)
	}
	reopened, err := transportpersistence.NewJSONLStore(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	restarted := transportmanager.New(reopened, actions)
	restartedRequests, err := restarted.ListRequests(context.Background(), domaintransport.RequestFilter{AttemptID: attempt.AttemptID})
	if err != nil || len(restartedRequests) != 2 || restartedRequests[0].RequestID != first.RequestID || restartedRequests[1].RequestID != second.RequestID {
		t.Fatalf("restart receipts=%#v err=%v", restartedRequests, err)
	}
}

func TestManagerRejectsUnboundAndForeignAttemptBeforePersistence(t *testing.T) {
	ctx, actions, _, _ := transportTestAction(t)
	store, err := transportpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "transport"))
	if err != nil {
		t.Fatal(err)
	}
	manager := transportmanager.New(store, actions)

	if _, err := manager.BeginRequest(context.Background(), "llm.chat"); err == nil {
		t.Fatal("unbound transport request was accepted")
	}
	identity, err := domainexecution.IdentityFromContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := domainexecution.WithIdentity(context.Background(), identity.TaskID, identity.RunID, identity.TraceID)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err = domainexecution.WithChildBoundActionAttempt(foreign, modulecore.NewActionID(), modulecore.NewAttemptID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.BeginRequest(foreign, "llm.chat"); err == nil {
		t.Fatal("foreign Action Attempt was accepted")
	}
	requests, err := manager.ListRequests(context.Background(), domaintransport.RequestFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 0 {
		t.Fatalf("rejected requests persisted=%#v", requests)
	}
}

func TestManagerRestartRecoveryFailsSentRequestWithoutResponse(t *testing.T) {
	ctx, actions, action, attempt := transportTestAction(t)
	storeRoot := filepath.Join(t.TempDir(), "transport")
	store, err := transportpersistence.NewJSONLStore(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	manager := transportmanager.New(store, actions)
	request, err := manager.BeginRequest(ctx, "llm.generate")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := actions.CompleteAttempt(ctx, action.ActionID, attempt.AttemptID, domainaction.AttemptStatusCancelled, domainaction.StatusCancelled, "process restart"); err != nil {
		t.Fatal(err)
	}
	reopened, err := transportpersistence.NewJSONLStore(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	restarted := transportmanager.New(reopened, actions)
	recovered, err := restarted.RecoverAfterRestart(context.Background())
	if err != nil || recovered != 1 {
		t.Fatalf("RecoverAfterRestart()=%d err=%v", recovered, err)
	}
	requests, _ := restarted.ListRequests(context.Background(), domaintransport.RequestFilter{})
	responses, _ := restarted.ListResponses(context.Background(), domaintransport.ResponseFilter{})
	if len(requests) != 1 || requests[0].RequestID != request.RequestID || requests[0].Status != domaintransport.RequestStatusFailed || len(responses) != 0 {
		t.Fatalf("restart receipts requests=%#v responses=%#v", requests, responses)
	}
}

func TestAdmittedBeginAndCompleteResponseDoNotWaitForOSLockAndSyncWriterFences(t *testing.T) {
	ctx, actions, actionStore, _, attempt := transportTestActionWithStore(t)
	queue := transportTestQueue(t, 8)
	if err := actionStore.SetPersistenceQueue(queue); err != nil {
		t.Fatal(err)
	}
	storeRoot := filepath.Join(t.TempDir(), "transport")
	store, err := transportpersistence.NewJSONLStore(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetPersistenceQueue(queue); err != nil {
		t.Fatal(err)
	}
	manager := transportmanager.New(store, actions)
	admission, err := persistencequeue.Admit(queue, 2, 1<<16)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = admission.Release()
		_ = queue.Shutdown(context.Background())
	})
	requestCtx := persistencequeue.WithAdmission(ctx, admission)

	releaseLock, blockerDone := holdJSONLBatchLock(t, storeRoot)
	requestStarted := time.Now()
	request, err := manager.BeginRequest(requestCtx, "llm.chat")
	if err != nil {
		releaseLock()
		t.Fatalf("admitted BeginRequest() error = %v", err)
	}
	if elapsed := time.Since(requestStarted); elapsed > time.Second {
		releaseLock()
		t.Fatalf("admitted BeginRequest() waited %v behind the OS lock", elapsed)
	}
	responseStarted := time.Now()
	response, err := manager.CompleteResponse(requestCtx, request.RequestID, "provider-response")
	if err != nil {
		releaseLock()
		t.Fatalf("admitted CompleteResponse() error = %v", err)
	}
	if elapsed := time.Since(responseStarted); elapsed > time.Second {
		releaseLock()
		t.Fatalf("admitted CompleteResponse() waited %v behind the OS lock", elapsed)
	}
	if response.RequestID != request.RequestID || response.AttemptID != attempt.AttemptID {
		releaseLock()
		t.Fatalf("admitted response lineage = %#v, request=%#v", response, request)
	}
	requests, err := manager.ListRequests(requestCtx, domaintransport.RequestFilter{AttemptID: attempt.AttemptID})
	if err != nil || len(requests) != 1 || requests[0].Status != domaintransport.RequestStatusResponded {
		releaseLock()
		t.Fatalf("read-your-writes requests=%#v err=%v", requests, err)
	}
	responses, err := manager.ListResponses(requestCtx, domaintransport.ResponseFilter{RequestID: request.RequestID})
	if err != nil || len(responses) != 1 || responses[0].ResponseID != response.ResponseID {
		releaseLock()
		t.Fatalf("read-your-writes responses=%#v err=%v", responses, err)
	}
	waitTransportQueueInFlight(t, queue)

	// A legacy synchronous call must wait for this owner's accepted writes and
	// assign its sequence from the resulting durable view.
	syncDone := make(chan struct {
		request domaintransport.Request
		err     error
	}, 1)
	go func() {
		created, callErr := manager.BeginRequest(ctx, "llm.chat")
		syncDone <- struct {
			request domaintransport.Request
			err     error
		}{created, callErr}
	}()
	select {
	case got := <-syncDone:
		releaseLock()
		t.Fatalf("synchronous BeginRequest returned before the accepted FIFO drained: request=%#v err=%v", got.request, got.err)
	case <-time.After(25 * time.Millisecond):
	}
	releaseLock()
	if err := <-blockerDone; err != nil {
		t.Fatal(err)
	}
	got := <-syncDone
	if got.err != nil || got.request.Sequence != 2 {
		t.Fatalf("synchronous request after drain = %#v err=%v, want sequence 2", got.request, got.err)
	}
	if err := admission.Barrier(context.Background()); err != nil {
		t.Fatalf("accepted transport writes did not drain: %v", err)
	}
	reopened, err := transportpersistence.NewJSONLStore(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := reopened.ListRequests(context.Background(), domaintransport.RequestFilter{AttemptID: attempt.AttemptID})
	if err != nil || len(restored) != 2 || restored[0].Status != domaintransport.RequestStatusResponded || restored[1].Sequence != 2 {
		t.Fatalf("durable requests after drain=%#v err=%v", restored, err)
	}
}

func TestAdmittedTransportSerializesConcurrentRAMOverlay(t *testing.T) {
	ctx, _, _, attempt := transportTestAction(t)
	queue := transportTestQueue(t, 32)
	store, err := transportpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "transport"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetPersistenceQueue(queue); err != nil {
		t.Fatal(err)
	}
	admission, err := persistencequeue.Admit(queue, 24, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Shutdown(context.Background())
	defer admission.Release()
	requestCtx := persistencequeue.WithAdmission(ctx, admission)
	identity, err := domainexecution.IdentityFromContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	const count = 24
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := store.Transaction(requestCtx, func(tx domaintransport.Store) error {
				requests, err := tx.ListRequests(requestCtx, domaintransport.RequestFilter{AttemptID: attempt.AttemptID})
				if err != nil {
					return err
				}
				request := domaintransport.Request{
					RequestID: modulecore.NewRequestID(), TaskID: identity.TaskID, RunID: identity.RunID,
					ActionID: attempt.ActionID, AttemptID: attempt.AttemptID, TraceID: identity.TraceID,
					Operation: "llm.chat", Sequence: len(requests) + 1,
					Status: domaintransport.RequestStatusSent, SentAt: time.Now().UTC(),
				}
				return tx.SaveRequest(requestCtx, request)
			})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent admitted transaction error = %v", err)
		}
	}
	requests, err := store.ListRequests(requestCtx, domaintransport.RequestFilter{AttemptID: attempt.AttemptID})
	if err != nil || len(requests) != count {
		t.Fatalf("accepted overlay count=%d err=%v, want %d", len(requests), err, count)
	}
	seen := make(map[int]bool, count)
	for _, request := range requests {
		if seen[request.Sequence] {
			t.Fatalf("duplicate concurrent request sequence %d", request.Sequence)
		}
		seen[request.Sequence] = true
	}
	for sequence := 1; sequence <= count; sequence++ {
		if !seen[sequence] {
			t.Fatalf("concurrent overlay is missing sequence %d", sequence)
		}
	}
	if err := admission.Barrier(context.Background()); err != nil {
		t.Fatalf("concurrent accepted commands did not drain: %v", err)
	}
}

func TestTransportAdmissionRejectsMismatchReservationExhaustionAndHeadFailure(t *testing.T) {
	t.Run("queue mismatch", func(t *testing.T) {
		ctx, actions, actionStore, _, _ := transportTestActionWithStore(t)
		queue := transportTestQueue(t, 4)
		otherQueue := transportTestQueue(t, 4)
		if err := actionStore.SetPersistenceQueue(queue); err != nil {
			t.Fatal(err)
		}
		store, err := transportpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "transport"))
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SetPersistenceQueue(otherQueue); err != nil {
			t.Fatal(err)
		}
		admission, err := persistencequeue.Admit(queue, 1, 1<<16)
		if err != nil {
			t.Fatal(err)
		}
		defer otherQueue.Shutdown(context.Background())
		defer queue.Shutdown(context.Background())
		defer admission.Release()
		manager := transportmanager.New(store, actions)
		if _, err := manager.BeginRequest(persistencequeue.WithAdmission(ctx, admission), "llm.chat"); !errors.Is(err, transportpersistence.ErrAdmissionQueueMismatch) {
			t.Fatalf("BeginRequest mismatch error=%v", err)
		}
	})

	t.Run("reservation exhausted", func(t *testing.T) {
		ctx, actions, actionStore, _, _ := transportTestActionWithStore(t)
		queue := transportTestQueue(t, 4)
		if err := actionStore.SetPersistenceQueue(queue); err != nil {
			t.Fatal(err)
		}
		store, err := transportpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "transport"))
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SetPersistenceQueue(queue); err != nil {
			t.Fatal(err)
		}
		admission, err := persistencequeue.Admit(queue, 1, 1<<16)
		if err != nil {
			t.Fatal(err)
		}
		defer queue.Shutdown(context.Background())
		defer admission.Release()
		manager := transportmanager.New(store, actions)
		requestCtx := persistencequeue.WithAdmission(ctx, admission)
		request, err := manager.BeginRequest(requestCtx, "llm.chat")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := manager.CompleteResponse(requestCtx, request.RequestID, "provider-response"); !errors.Is(err, persistencequeue.ErrInvalidReservation) {
			t.Fatalf("CompleteResponse after reservation exhaustion error=%v", err)
		}
		requests, err := manager.ListRequests(requestCtx, domaintransport.RequestFilter{})
		if err != nil || len(requests) != 1 || requests[0].Status != domaintransport.RequestStatusSent {
			t.Fatalf("reservation exhaustion changed accepted request: requests=%#v err=%v", requests, err)
		}
	})

	t.Run("head failure poisons RAM view", func(t *testing.T) {
		ctx, actions, actionStore, _, _ := transportTestActionWithStore(t)
		queue := transportTestQueue(t, 4)
		if err := actionStore.SetPersistenceQueue(queue); err != nil {
			t.Fatal(err)
		}
		storeRoot := filepath.Join(t.TempDir(), "transport")
		store, err := transportpersistence.NewJSONLStore(storeRoot)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SetPersistenceQueue(queue); err != nil {
			t.Fatal(err)
		}
		admission, err := persistencequeue.Admit(queue, 2, 1<<16)
		if err != nil {
			t.Fatal(err)
		}
		defer queue.Shutdown(context.Background())
		defer admission.Release()
		manager := transportmanager.New(store, actions)
		releaseLock, blockerDone := holdJSONLBatchLock(t, storeRoot)
		request, err := manager.BeginRequest(persistencequeue.WithAdmission(ctx, admission), "llm.chat")
		if err != nil {
			releaseLock()
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(storeRoot, ".jsonlbatch.wal"), []byte("{broken\n"), 0o600); err != nil {
			releaseLock()
			t.Fatal(err)
		}
		releaseLock()
		if err := <-blockerDone; err != nil {
			t.Fatal(err)
		}
		if err := admission.Barrier(context.Background()); !errors.Is(err, persistencequeue.ErrFailed) {
			t.Fatalf("Barrier after poisoned WAL error=%v", err)
		}
		if _, err := manager.ListRequests(persistencequeue.WithAdmission(ctx, admission), domaintransport.RequestFilter{}); !errors.Is(err, persistencequeue.ErrFailed) {
			t.Fatalf("read after head failure error=%v, request=%s", err, request.RequestID)
		}
	})
}

func transportTestQueue(t *testing.T, capacity int) *persistencequeue.Queue[persistencequeue.PersistenceCommand] {
	t.Helper()
	queue, err := persistencequeue.NewPersistenceQueue(capacity, 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	return queue
}

func holdJSONLBatchLock(t *testing.T, root string) (func(), <-chan error) {
	t.Helper()
	blocker, err := jsonlbatch.New(root, []string{"transport_request.jsonl", "transport_response.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- blocker.Read(context.Background(), func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	var once sync.Once
	releaseOnce := func() { once.Do(func() { close(release) }) }
	t.Cleanup(releaseOnce)
	return releaseOnce, done
}

func waitTransportQueueInFlight(t *testing.T, queue *persistencequeue.Queue[persistencequeue.PersistenceCommand]) {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for !queue.Status().InFlight {
		select {
		case <-ticker.C:
		case <-timer.C:
			t.Fatal("transport FIFO writer did not reach the blocked OS lock")
		}
	}
}

func transportTestAction(t *testing.T) (context.Context, *actionmanager.Manager, domainaction.Action, domainaction.Attempt) {
	ctx, actions, _, action, attempt := transportTestActionWithStore(t)
	return ctx, actions, action, attempt
}

func transportTestActionWithStore(t *testing.T) (context.Context, *actionmanager.Manager, *actionpersistence.JSONLStore, domainaction.Action, domainaction.Attempt) {
	t.Helper()
	actionStore, err := actionpersistence.NewJSONLStoreWithOptions(filepath.Join(t.TempDir(), "actions"), actionpersistence.OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	actions := actionmanager.New(actionStore)
	taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()
	ctx, err := domainexecution.WithIdentity(context.Background(), taskID, runID, modulecore.NewTraceID())
	if err != nil {
		t.Fatal(err)
	}
	action, attempt, err := actions.CreateAction(ctx, actionmanager.CreateInput{
		TaskID: taskID,
		RunID:  runID,
		Kind:   domainaction.KindLLM,
		Name:   "llm.chat",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, err = domainexecution.WithBoundActionAttempt(ctx, action.ActionID, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, actions, actionStore, action, attempt
}
