package task

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func newExecutionFenceTestTask(title string) domaintask.Task {
	now := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	return domaintask.Task{
		TaskID:          modulecore.NewTaskID(),
		Title:           title,
		Route:           domaintask.RouteGeneral,
		Status:          domaintask.StatusQueued,
		Priority:        domaintask.PriorityNormal,
		InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

func waitForExecutionFenceStart(t *testing.T, started <-chan struct{}, done <-chan error) {
	t.Helper()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("execution fence returned before callback started: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("execution fence callback did not start")
	}
}

func TestJSONLStoreExecutionFenceAllowsUnrelatedTaskTransaction(t *testing.T) {
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	taskA := newExecutionFenceTestTask("effect task")
	taskB := newExecutionFenceTestTask("unrelated task")
	if err := store.SaveTask(ctx, taskA); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveTask(ctx, taskB); err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	effectDone := make(chan error, 1)
	go func() {
		effectDone <- store.WithTaskExecutionFence(ctx, taskA.TaskID, func() error {
			close(started)
			<-release
			return nil
		})
	}()
	waitForExecutionFenceStart(t, started, effectDone)

	taskB.UpdatedAt = taskB.UpdatedAt.Add(time.Minute)
	bDone := make(chan error, 1)
	go func() {
		bDone <- store.TaskTransaction(ctx, taskB.TaskID, func(tx domaintask.Store) error {
			return tx.SaveTask(ctx, taskB)
		})
	}()
	select {
	case err := <-bDone:
		if err != nil {
			t.Fatalf("unrelated Task transaction failed while effect was blocked: %v", err)
		}
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("unrelated Task transaction remained blocked by external effect")
	}

	close(release)
	if err := <-effectDone; err != nil {
		t.Fatalf("execution fence: %v", err)
	}
	got, err := store.GetTask(ctx, taskB.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.UpdatedAt.Equal(taskB.UpdatedAt) {
		t.Fatalf("unrelated Task update was not committed: got=%s want=%s", got.UpdatedAt, taskB.UpdatedAt)
	}
}

func TestJSONLStoreRawTransactionRejectsActiveExecutionFenceTask(t *testing.T) {
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	task := newExecutionFenceTestTask("fenced raw transaction")
	if err := store.SaveTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	effectDone := make(chan error, 1)
	go func() {
		effectDone <- store.WithTaskExecutionFence(ctx, task.TaskID, func() error {
			close(started)
			<-release
			return nil
		})
	}()
	waitForExecutionFenceStart(t, started, effectDone)

	updated := task
	updated.UpdatedAt = updated.UpdatedAt.Add(time.Minute)
	rawErr := store.Transaction(ctx, func(tx domaintask.Store) error {
		return tx.SaveTask(ctx, updated)
	})
	if rawErr == nil || !strings.Contains(rawErr.Error(), "active execution fence") {
		close(release)
		<-effectDone
		t.Fatalf("raw transaction error = %v, want active-fence rejection", rawErr)
	}
	close(release)
	if err := <-effectDone; err != nil {
		t.Fatalf("execution fence: %v", err)
	}
	got, err := store.GetTask(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.UpdatedAt.Equal(task.UpdatedAt) {
		t.Fatalf("raw transaction bypassed active fence: got updated_at=%s want=%s", got.UpdatedAt, task.UpdatedAt)
	}
}

func TestJSONLStoreExecutionFenceAllowsRawTransactionWithoutActiveFence(t *testing.T) {
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	task := newExecutionFenceTestTask("raw transaction without fence")
	if err := store.Transaction(ctx, func(tx domaintask.Store) error {
		return tx.SaveTask(ctx, task)
	}); err != nil {
		t.Fatalf("raw transaction without an active fence: %v", err)
	}
	if _, err := store.GetTask(ctx, task.TaskID); err != nil {
		t.Fatalf("raw transaction did not commit: %v", err)
	}
}

func TestJSONLStoreScopedTaskTransactionRejectsWrongTaskWrite(t *testing.T) {
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	owner := newExecutionFenceTestTask("scoped owner")
	other := newExecutionFenceTestTask("wrong scoped task")
	if err := store.SaveTask(ctx, owner); err != nil {
		t.Fatal(err)
	}

	err = store.TaskTransaction(ctx, owner.TaskID, func(tx domaintask.Store) error {
		return tx.SaveTask(ctx, other)
	})
	if !errors.Is(err, errTaskExecutionFenceScope) {
		t.Fatalf("scoped transaction wrong-Task write error = %v, want scope mismatch", err)
	}
	if _, err := store.GetTask(ctx, other.TaskID); !errors.Is(err, domaintask.ErrNotFound) {
		t.Fatalf("wrong Task was written despite scoped rejection: %v", err)
	}
}

func TestJSONLStoreExecutionFenceRejectsReadOnlyNestedTaskTransaction(t *testing.T) {
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	taskID := modulecore.NewTaskID()
	called := false
	err = store.ReadTransaction(context.Background(), func(tx domaintask.Store) error {
		return tx.TaskTransaction(context.Background(), taskID, func(domaintask.Store) error {
			called = true
			return nil
		})
	})
	if !errors.Is(err, errReadOnlyTransaction) {
		t.Fatalf("read-only nested TaskTransaction error = %v, want read-only error", err)
	}
	if called {
		t.Fatal("read-only nested TaskTransaction invoked its mutation callback")
	}
}

func TestJSONLStoreSameTaskFenceWaiterHonorsCancellation(t *testing.T) {
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	task := newExecutionFenceTestTask("same task fence")
	if err := store.SaveTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	effectDone := make(chan error, 1)
	go func() {
		effectDone <- store.WithTaskExecutionFence(ctx, task.TaskID, func() error {
			close(started)
			<-release
			return nil
		})
	}()
	waitForExecutionFenceStart(t, started, effectDone)

	waitCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	entered := make(chan struct{}, 1)
	waitErr := store.TaskTransaction(waitCtx, task.TaskID, func(domaintask.Store) error {
		entered <- struct{}{}
		return errors.New("same Task transaction entered while execution fence was held")
	})
	select {
	case <-entered:
		close(release)
		<-effectDone
		t.Fatal("same Task transaction entered while execution fence was held")
	default:
	}
	if !errors.Is(waitErr, context.DeadlineExceeded) {
		t.Fatalf("same Task waiter error = %v, want context deadline", waitErr)
	}
	close(release)
	if err := <-effectDone; err != nil {
		t.Fatalf("execution fence: %v", err)
	}
}

func TestJSONLStoreSameTaskExecutionFencesSerialize(t *testing.T) {
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	taskID := modulecore.NewTaskID()
	firstStarted := make(chan struct{})
	firstRelease := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- store.WithTaskExecutionFence(context.Background(), taskID, func() error {
			close(firstStarted)
			<-firstRelease
			return nil
		})
	}()
	waitForExecutionFenceStart(t, firstStarted, firstDone)

	secondStarted := make(chan struct{})
	secondRelease := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- store.WithTaskExecutionFence(context.Background(), taskID, func() error {
			close(secondStarted)
			<-secondRelease
			return nil
		})
	}()
	select {
	case <-secondStarted:
		close(firstRelease)
		close(secondRelease)
		<-firstDone
		<-secondDone
		t.Fatal("same Task execution fences overlapped")
	case <-time.After(100 * time.Millisecond):
	}
	close(firstRelease)
	select {
	case <-secondStarted:
	case err := <-secondDone:
		close(secondRelease)
		<-firstDone
		t.Fatalf("second execution fence returned before callback started: %v", err)
	case <-time.After(2 * time.Second):
		close(secondRelease)
		<-firstDone
		<-secondDone
		t.Fatal("second same Task execution fence did not start after first released")
	}
	close(secondRelease)
	if err := <-firstDone; err != nil {
		t.Fatalf("first execution fence: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second execution fence: %v", err)
	}
}

func TestJSONLStoreCloseWaitsForActiveExecutionFence(t *testing.T) {
	root := t.TempDir()
	store, err := NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	task := newExecutionFenceTestTask("close fence")
	if err := store.SaveTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	effectDone := make(chan error, 1)
	go func() {
		effectDone <- store.WithTaskExecutionFence(ctx, task.TaskID, func() error {
			close(started)
			<-release
			return nil
		})
	}()
	waitForExecutionFenceStart(t, started, effectDone)

	closeDone := make(chan error, 1)
	go func() { closeDone <- store.Close() }()
	select {
	case err := <-closeDone:
		close(release)
		<-effectDone
		t.Fatalf("Close returned while execution fence was active: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	newWriter, newWriterErr := NewJSONLStore(root)
	if newWriter != nil {
		_ = newWriter.Close()
	}
	if !errors.Is(newWriterErr, ErrTaskWriterBusy) {
		close(release)
		<-effectDone
		<-closeDone
		t.Fatalf("new writer admission while Close was draining = %v, want ErrTaskWriterBusy", newWriterErr)
	}
	close(release)
	if err := <-effectDone; err != nil {
		t.Fatalf("execution fence: %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := store.SaveTask(ctx, task); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("write after Close error = %v, want os.ErrClosed", err)
	}
}
