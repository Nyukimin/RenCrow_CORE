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

func TestAdmittedTransportWritesProceedWhileSynchronousWriteWaitsForOSLock(t *testing.T) {
	ctx, actions, actionStore, _, attempt := transportTestActionWithStore(t)
	queue := transportTestQueue(t, 16)
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
	admission, err := persistencequeue.Admit(queue, 4, 1<<16)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = admission.Release()
		_ = queue.Shutdown(context.Background())
	})
	requestCtx := persistencequeue.WithAdmission(ctx, admission)

	releaseLock, blockerDone := holdJSONLBatchLock(t, storeRoot)
	type beginResult struct {
		request domaintransport.Request
		err     error
	}
	synchronousDone := make(chan beginResult, 1)
	go func() {
		request, callErr := manager.BeginRequest(ctx, "llm.chat")
		synchronousDone <- beginResult{request: request, err: callErr}
	}()
	waitTransportQueueInFlight(t, queue)

	// The non-admitted write is now waiting for its own FIFO ticket while the
	// worker is blocked on the filesystem lock. RAM admission must stay usable.
	foregroundDone := make(chan struct {
		request  domaintransport.Request
		response domaintransport.Response
		err      error
	}, 1)
	go func() {
		request, callErr := manager.BeginRequest(requestCtx, "llm.chat")
		if callErr != nil {
			foregroundDone <- struct {
				request  domaintransport.Request
				response domaintransport.Response
				err      error
			}{err: callErr}
			return
		}
		response, callErr := manager.CompleteResponse(requestCtx, request.RequestID, "provider-response")
		foregroundDone <- struct {
			request  domaintransport.Request
			response domaintransport.Response
			err      error
		}{request: request, response: response, err: callErr}
	}()
	var foreground struct {
		request  domaintransport.Request
		response domaintransport.Response
		err      error
	}
	select {
	case foreground = <-foregroundDone:
	case <-time.After(time.Second):
		t.Fatal("admitted transport write waited behind the synchronous filesystem write")
	}
	if foreground.err != nil {
		t.Fatalf("admitted BeginRequest/CompleteResponse() error = %v", foreground.err)
	}
	if foreground.request.Sequence != 2 || foreground.response.RequestID != foreground.request.RequestID || foreground.response.AttemptID != attempt.AttemptID {
		t.Fatalf("foreground transport lineage/sequence = request %#v response %#v", foreground.request, foreground.response)
	}
	requests, err := manager.ListRequests(requestCtx, domaintransport.RequestFilter{AttemptID: attempt.AttemptID})
	if err != nil || len(requests) != 2 || requests[0].Sequence != 1 || requests[1].Sequence != 2 || requests[1].Status != domaintransport.RequestStatusResponded {
		t.Fatalf("RAM read-your-writes requests=%#v err=%v", requests, err)
	}
	select {
	case got := <-synchronousDone:
		t.Fatalf("synchronous write returned before its FIFO ticket became durable: %#v", got)
	default:
	}

	releaseLock()
	if err := <-blockerDone; err != nil {
		t.Fatal(err)
	}
	background := <-synchronousDone
	if background.err != nil || background.request.Sequence != 1 {
		t.Fatalf("synchronous write after filesystem unlock = %#v", background)
	}
	if err := admission.Barrier(context.Background()); err != nil {
		t.Fatalf("accepted transport writes did not drain: %v", err)
	}
	reopened, err := transportpersistence.NewJSONLStore(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := reopened.ListRequests(context.Background(), domaintransport.RequestFilter{AttemptID: attempt.AttemptID})
	if err != nil || len(restored) != 2 || restored[0].Sequence != 1 || restored[0].Status != domaintransport.RequestStatusSent || restored[1].Sequence != 2 || restored[1].Status != domaintransport.RequestStatusResponded {
		t.Fatalf("durable requests after FIFO drain=%#v err=%v", restored, err)
	}
	responses, err := reopened.ListResponses(context.Background(), domaintransport.ResponseFilter{AttemptID: attempt.AttemptID})
	if err != nil || len(responses) != 1 || responses[0].ResponseID != foreground.response.ResponseID {
		t.Fatalf("durable response after FIFO drain=%#v err=%v", responses, err)
	}
}

func TestSynchronousTransportReadPreservesNewerRAMOverlay(t *testing.T) {
	ctx, actions, actionStore, _, attempt := transportTestActionWithStore(t)
	queue := transportTestQueue(t, 16)
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
	admission, err := persistencequeue.Admit(queue, 4, 1<<16)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = admission.Release()
		_ = queue.Shutdown(context.Background())
	})
	requestCtx := persistencequeue.WithAdmission(ctx, admission)

	releaseLock, blockerDone := holdJSONLBatchLock(t, storeRoot)
	first, err := manager.BeginRequest(requestCtx, "llm.chat")
	if err != nil || first.Sequence != 1 {
		t.Fatalf("first admitted request=%#v err=%v", first, err)
	}

	gateStarted := make(chan struct{})
	gateRelease := make(chan struct{})
	var gateReleaseOnce sync.Once
	releaseGate := func() { gateReleaseOnce.Do(func() { close(gateRelease) }) }
	readCtx := &transportReadBarrierContext{
		Context:        context.Background(),
		ticketCaptured: make(chan struct{}),
		afterWait:      make(chan struct{}),
		resumeRead:     make(chan struct{}),
	}
	var resumeReadOnce sync.Once
	resumeRead := func() { resumeReadOnce.Do(func() { close(readCtx.resumeRead) }) }
	t.Cleanup(func() {
		releaseLock()
		releaseGate()
		resumeRead()
	})
	if _, err := queue.Enqueue(persistencequeue.PersistenceCommand{
		Owner:     persistencequeue.OwnerTransport,
		Kind:      "transport.test_read_gate",
		SizeBytes: 1,
		Apply: func(context.Context) error {
			close(gateStarted)
			<-gateRelease
			return nil
		},
	}, 1); err != nil {
		t.Fatalf("enqueue test FIFO gate: %v", err)
	}
	type readResult struct {
		requests []domaintransport.Request
		err      error
	}
	readDone := make(chan readResult, 1)
	go func() {
		var requests []domaintransport.Request
		readErr := store.ReadTransaction(readCtx, func(tx domaintransport.Store) error {
			var err error
			requests, err = tx.ListRequests(readCtx, domaintransport.RequestFilter{AttemptID: attempt.AttemptID})
			return err
		})
		readDone <- readResult{requests: requests, err: readErr}
	}()
	select {
	case <-readCtx.ticketCaptured:
	case <-time.After(time.Second):
		t.Fatal("synchronous read did not capture its transport ticket before waiting")
	}

	releaseLock()
	if err := <-blockerDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-gateStarted:
	case <-time.After(time.Second):
		t.Fatal("transport FIFO did not reach the test gate after the captured ticket")
	}
	select {
	case <-readCtx.afterWait:
	case <-time.After(time.Second):
		t.Fatal("synchronous read did not finish its captured FIFO barrier")
	}

	second, err := manager.BeginRequest(requestCtx, "llm.chat")
	if err != nil || second.Sequence != 2 {
		t.Fatalf("second admitted request=%#v err=%v", second, err)
	}
	resumeRead()
	select {
	case result := <-readDone:
		if result.err != nil || len(result.requests) != 1 || result.requests[0].RequestID != first.RequestID {
			t.Fatalf("synchronous read at captured ticket=%#v err=%v", result.requests, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("synchronous durable read did not finish after its ticket became durable")
	}
	requests, err := manager.ListRequests(requestCtx, domaintransport.RequestFilter{AttemptID: attempt.AttemptID})
	if err != nil || len(requests) != 2 || requests[0].RequestID != first.RequestID || requests[1].RequestID != second.RequestID {
		t.Fatalf("durable read overwrote a newer RAM overlay: requests=%#v err=%v", requests, err)
	}
	releaseGate()
	if err := admission.Barrier(context.Background()); err != nil {
		t.Fatalf("accepted transport writes did not drain: %v", err)
	}
}

func TestSynchronousTransportCancellationAndEmptyTransactionBarrier(t *testing.T) {
	ctx, actions, actionStore, _, attempt := transportTestActionWithStore(t)
	queue := transportTestQueue(t, 16)
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
	t.Cleanup(func() { _ = queue.Shutdown(context.Background()) })

	cancelledCtx, cancelBeforeEnqueue := context.WithCancel(ctx)
	cancelBeforeEnqueue()
	callbackCalled := false
	if err := store.Transaction(cancelledCtx, func(domaintransport.Store) error {
		callbackCalled = true
		return nil
	}); !errors.Is(err, context.Canceled) || callbackCalled {
		t.Fatalf("cancel before enqueue error=%v callbackCalled=%v", err, callbackCalled)
	}
	if status := queue.Status(); status.Pending != 0 {
		t.Fatalf("cancelled transaction enqueued work: status=%+v", status)
	}

	releaseLock, blockerDone := holdJSONLBatchLock(t, storeRoot)
	requestCtx, cancelAfterEnqueue := context.WithCancel(ctx)
	type beginResult struct {
		request domaintransport.Request
		err     error
	}
	backgroundDone := make(chan beginResult, 1)
	go func() {
		request, callErr := manager.BeginRequest(requestCtx, "llm.chat")
		backgroundDone <- beginResult{request: request, err: callErr}
	}()
	waitTransportQueueInFlight(t, queue)
	cancelAfterEnqueue()
	background := <-backgroundDone
	if !errors.Is(background.err, context.Canceled) {
		t.Fatalf("accepted transport write cancellation result=%#v", background)
	}

	emptyCallbackCalled := make(chan struct{})
	emptyDone := make(chan error, 1)
	go func() {
		emptyDone <- store.Transaction(context.Background(), func(domaintransport.Store) error {
			close(emptyCallbackCalled)
			return nil
		})
	}()
	select {
	case <-emptyCallbackCalled:
	case <-time.After(time.Second):
		t.Fatal("empty transaction callback did not run")
	}
	select {
	case err := <-emptyDone:
		t.Fatalf("empty transaction skipped the pending-ticket barrier: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	releaseLock()
	if err := <-blockerDone; err != nil {
		t.Fatal(err)
	}
	if err := <-emptyDone; err != nil {
		t.Fatalf("empty transaction after the accepted ticket drained: %v", err)
	}
	reopened, err := transportpersistence.NewJSONLStore(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	requests, err := reopened.ListRequests(context.Background(), domaintransport.RequestFilter{AttemptID: attempt.AttemptID})
	if err != nil || len(requests) != 1 || requests[0].Sequence != 1 || requests[0].Status != domaintransport.RequestStatusSent {
		t.Fatalf("accepted write did not continue after caller cancellation: requests=%#v err=%v", requests, err)
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

type transportReadBarrierContext struct {
	context.Context
	ticketCaptured chan struct{}
	afterWait      chan struct{}
	resumeRead     chan struct{}
	errCalls       int
	captureOnce    sync.Once
	afterWaitOnce  sync.Once
}

// Done signals that the read has passed its ticket snapshot and entered Queue.Wait.
// The third Err call is the store's cancellation check after Queue.Wait returns.
// Holding that check places a new RAM write after the read barrier but before
// the durable disk read, without coordinating through the timing of ctx.Err.
func (c *transportReadBarrierContext) Done() <-chan struct{} {
	c.captureOnce.Do(func() { close(c.ticketCaptured) })
	return c.Context.Done()
}

func (c *transportReadBarrierContext) Err() error {
	c.errCalls++
	if c.errCalls == 3 {
		c.afterWaitOnce.Do(func() {
			close(c.afterWait)
			<-c.resumeRead
		})
	}
	return c.Context.Err()
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
