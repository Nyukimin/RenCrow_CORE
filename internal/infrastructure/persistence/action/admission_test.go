package action

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func admissionTestQueue(t *testing.T) *persistencequeue.Queue[persistencequeue.PersistenceCommand] {
	t.Helper()
	queue, err := persistencequeue.NewPersistenceQueue(8, 1<<20)
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	t.Cleanup(func() {
		if queue.Status().Failure != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := queue.Shutdown(ctx); err != nil {
			t.Errorf("shutdown queue: %v", err)
		}
	})
	return queue
}

func openAdmissionStore(t *testing.T, root string, queue *persistencequeue.Queue[persistencequeue.PersistenceCommand], index bool) *JSONLStore {
	t.Helper()
	store, err := NewJSONLStoreWithOptions(root, OpenOptions{Index: index})
	if err != nil {
		t.Fatalf("open Action store: %v", err)
	}
	if err := store.SetPersistenceQueue(queue); err != nil {
		t.Fatalf("attach persistence queue: %v", err)
	}
	return store
}

func holdActionStoreOSLock(t *testing.T, store *JSONLStore) func() {
	t.Helper()
	holder, err := jsonlbatch.New(store.root, logFilenames[:])
	if err != nil {
		t.Fatalf("open lock holder: %v", err)
	}
	locked := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- holder.Read(context.Background(), func() error {
			close(locked)
			<-release
			return nil
		})
	}()
	select {
	case <-locked:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("test lock holder did not acquire the Action store OS lock")
	}
	var released bool
	return func() {
		if released {
			return
		}
		released = true
		close(release)
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("release OS lock: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("OS lock holder did not finish")
		}
	}
}

func waitForQueueInFlight(t *testing.T, queue *persistencequeue.Queue[persistencequeue.PersistenceCommand]) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		status := queue.Status()
		if status.Failure != nil {
			t.Fatalf("persistence queue failed before entering the OS lock wait: %v", status.Failure)
		}
		if status.InFlight {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("persistence worker did not start the accepted Action write")
}

func TestAdmittedActionAndAttemptWritesAndReadsDoNotWaitForOSLock(t *testing.T) {
	root := filepath.Join(t.TempDir(), "actions")
	queue := admissionTestQueue(t)
	store := openAdmissionStore(t, root, queue, true)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	admission, err := persistencequeue.Admit(queue, 2, 256<<10)
	if err != nil {
		t.Fatalf("admit Action writes: %v", err)
	}
	defer admission.Release()
	ctx = persistencequeue.WithAdmission(ctx, admission)
	manager := actionmanager.New(store)
	unlock := holdActionStoreOSLock(t, store)
	defer unlock()

	type pairResult struct {
		action  domainaction.Action
		attempt domainaction.Attempt
		err     error
	}
	created := make(chan pairResult, 1)
	go func() {
		action, attempt, err := manager.CreateAction(ctx, actionmanager.CreateInput{
			TaskID: modulecore.NewTaskID(), RunID: modulecore.NewRunID(), Kind: domainaction.KindTool, Name: "llm.generate",
		})
		created <- pairResult{action: action, attempt: attempt, err: err}
	}()
	var pair pairResult
	select {
	case pair = <-created:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("CreateAction waited for the Action store OS lock")
	}
	if pair.err != nil {
		t.Fatalf("CreateAction: %v", pair.err)
	}
	waitForQueueInFlight(t, queue)
	time.Sleep(25 * time.Millisecond)
	if status := queue.Status(); !status.InFlight || status.Pending == 0 {
		t.Fatalf("Action writer did not remain blocked behind the held OS lock: %+v", status)
	}

	closed := make(chan error, 1)
	go func() {
		_, _, err := manager.CompleteAttempt(ctx, pair.action.ActionID, pair.attempt.AttemptID,
			domainaction.AttemptStatusSucceeded, domainaction.StatusSucceeded, "LLM call complete")
		closed <- err
	}()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("CompleteAttempt: %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("CompleteAttempt waited for the Action store OS lock")
	}

	gotAction, err := store.GetAction(ctx, pair.action.ActionID)
	if err != nil {
		t.Fatalf("admitted GetAction: %v", err)
	}
	if gotAction.Status != domainaction.StatusSucceeded {
		t.Fatalf("admitted Action status = %q, want succeeded", gotAction.Status)
	}
	gotAttempt, err := store.GetAttempt(ctx, pair.attempt.AttemptID)
	if err != nil {
		t.Fatalf("admitted GetAttempt: %v", err)
	}
	if gotAttempt.Status != domainaction.AttemptStatusSucceeded {
		t.Fatalf("admitted Attempt status = %q, want succeeded", gotAttempt.Status)
	}

	unlock()
	if err := admission.Barrier(context.Background()); err != nil {
		t.Fatalf("wait for admitted Action persistence: %v", err)
	}
	if _, err := store.GetAction(context.Background(), pair.action.ActionID); err != nil {
		t.Fatalf("persisted Action read: %v", err)
	}
}

func TestNonAdmittedReadFencesBehindAcceptedActionWrite(t *testing.T) {
	queue := admissionTestQueue(t)
	store := openAdmissionStore(t, filepath.Join(t.TempDir(), "actions"), queue, true)
	admission, err := persistencequeue.Admit(queue, 1, 128<<10)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	defer admission.Release()
	ctx := persistencequeue.WithAdmission(context.Background(), admission)
	unlock := holdActionStoreOSLock(t, store)
	defer unlock()

	action, _, err := actionmanager.New(store).CreateAction(ctx, actionmanager.CreateInput{
		TaskID: modulecore.NewTaskID(), RunID: modulecore.NewRunID(), Kind: domainaction.KindTool, Name: "llm.generate",
	})
	if err != nil {
		t.Fatalf("admitted CreateAction: %v", err)
	}
	readDone := make(chan error, 1)
	go func() {
		_, err := store.GetAction(context.Background(), action.ActionID)
		readDone <- err
	}()
	select {
	case err := <-readDone:
		t.Fatalf("non-admitted read returned before accepted write was durable: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	unlock()
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatalf("non-admitted read after queue drained: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("non-admitted read did not pass the persistence fence")
	}
}

func TestAdmittedActionReadFailsFastWhenSynchronousFenceIsActive(t *testing.T) {
	queue := admissionTestQueue(t)
	store := openAdmissionStore(t, filepath.Join(t.TempDir(), "actions"), queue, true)
	admission, err := persistencequeue.Admit(queue, 1, 128<<10)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	defer admission.Release()
	ctx := persistencequeue.WithAdmission(context.Background(), admission)

	store.admissionMu.Lock()
	completed := make(chan error, 1)
	go func() {
		_, err := store.GetAction(ctx, modulecore.NewActionID())
		completed <- err
	}()
	select {
	case err := <-completed:
		if !errors.Is(err, ErrActionAdmissionBusy) {
			t.Fatalf("admitted read while synchronous fence is held = %v, want ErrActionAdmissionBusy", err)
		}
	case <-time.After(250 * time.Millisecond):
		store.admissionMu.Unlock()
		t.Fatal("admitted read waited for the synchronous store fence")
	}
	store.admissionMu.Unlock()
}

func TestAdmittedActionTransactionFailsFastWhenSynchronousWriterFenceIsActive(t *testing.T) {
	queue := admissionTestQueue(t)
	store := openAdmissionStore(t, filepath.Join(t.TempDir(), "actions"), queue, true)
	admission, err := persistencequeue.Admit(queue, 1, 128<<10)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	defer admission.Release()
	ctx := persistencequeue.WithAdmission(context.Background(), admission)
	unlock := holdActionStoreOSLock(t, store)
	defer unlock()

	syncDone := make(chan error, 1)
	go func() {
		syncDone <- store.Transaction(context.Background(), func(domainaction.Store) error { return nil })
	}()
	deadline := time.Now().Add(time.Second)
	for store.admissionMu.TryLock() {
		store.admissionMu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("synchronous Transaction did not enter its writer fence")
		}
		time.Sleep(time.Millisecond)
	}

	completed := make(chan error, 1)
	go func() {
		completed <- store.Transaction(ctx, func(domainaction.Store) error { return nil })
	}()
	select {
	case err := <-completed:
		if !errors.Is(err, ErrActionAdmissionBusy) {
			t.Fatalf("admitted Transaction while synchronous writer is active = %v, want ErrActionAdmissionBusy", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("admitted Transaction waited for the synchronous writer fence")
	}

	unlock()
	select {
	case err := <-syncDone:
		if err != nil {
			t.Fatalf("synchronous Transaction after OS lock release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("synchronous Transaction did not finish after OS lock release")
	}
}

func TestActionQueueHeadFailurePoisonsAdmittedReads(t *testing.T) {
	queue := admissionTestQueue(t)
	store := openAdmissionStore(t, filepath.Join(t.TempDir(), "actions"), queue, true)
	admission, err := persistencequeue.Admit(queue, 1, 128<<10)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	defer admission.Release()
	ctx := persistencequeue.WithAdmission(context.Background(), admission)
	unlock := holdActionStoreOSLock(t, store)

	action, _, err := actionmanager.New(store).CreateAction(ctx, actionmanager.CreateInput{
		TaskID: modulecore.NewTaskID(), RunID: modulecore.NewRunID(), Kind: domainaction.KindTool, Name: "llm.generate",
	})
	if err != nil {
		unlock()
		t.Fatalf("admitted CreateAction before simulated persistence failure: %v", err)
	}
	if err := os.Remove(store.actionPath); err != nil {
		unlock()
		t.Fatalf("remove Action log to fail the queue head: %v", err)
	}
	unlock()

	if err := admission.Barrier(context.Background()); err == nil {
		t.Fatal("admission barrier succeeded after its Action queue head failed")
	}
	if _, err := store.GetAction(ctx, action.ActionID); err == nil {
		t.Fatal("admitted read returned overlay state after the Action queue failed")
	}
}

func TestActionAdmissionRejectsQueueMismatchAndDisabledIndex(t *testing.T) {
	queue := admissionTestQueue(t)
	otherQueue := admissionTestQueue(t)
	store := openAdmissionStore(t, filepath.Join(t.TempDir(), "actions"), queue, true)
	wrong, err := persistencequeue.Admit(otherQueue, 1, 128<<10)
	if err != nil {
		t.Fatalf("admit foreign queue: %v", err)
	}
	defer wrong.Release()
	called := false
	err = store.Transaction(persistencequeue.WithAdmission(context.Background(), wrong), func(tx domainaction.Store) error {
		called = true
		return nil
	})
	if !errors.Is(err, ErrActionAdmissionQueueMismatch) {
		t.Fatalf("transaction with foreign admission = %v, want queue mismatch", err)
	}
	if called {
		t.Fatal("transaction callback ran with a foreign Admission")
	}

	withoutIndex := openAdmissionStore(t, filepath.Join(t.TempDir(), "no-index"), queue, false)
	valid, err := persistencequeue.Admit(queue, 1, 128<<10)
	if err != nil {
		t.Fatalf("admit no-index request: %v", err)
	}
	defer valid.Release()
	err = withoutIndex.Transaction(persistencequeue.WithAdmission(context.Background(), valid), func(domainaction.Store) error {
		t.Fatal("transaction callback ran without an Action index")
		return nil
	})
	if !errors.Is(err, ErrActionAdmissionUnavailable) {
		t.Fatalf("transaction without index = %v, want admission unavailable", err)
	}
}
