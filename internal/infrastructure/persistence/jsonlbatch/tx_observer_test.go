package jsonlbatch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

// txCollector records every observation delivered to a Store.
type txCollector struct {
	mu    sync.Mutex
	items []TxObservation
	ctxs  []context.Context
}

func (c *txCollector) observe(ctx context.Context, tx TxObservation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = append(c.items, tx)
	c.ctxs = append(c.ctxs, ctx)
}

func (c *txCollector) snapshot() []TxObservation {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]TxObservation(nil), c.items...)
}

func (c *txCollector) only(t *testing.T) TxObservation {
	t.Helper()
	items := c.snapshot()
	if len(items) != 1 {
		t.Fatalf("observations = %d, want exactly 1: %+v", len(items), items)
	}
	return items[0]
}

func TestTxObserverRecordsWritePhases(t *testing.T) {
	const pause = 40 * time.Millisecond
	root := t.TempDir()
	store, err := New(root, []string{"state.jsonl", "run.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	// Make the data-file fsync measurable so the commit phase is distinguishable
	// from the callback phase.
	store.syncFn = func(file *os.File) error {
		if filepath.Base(file.Name()) == "state.jsonl" {
			time.Sleep(pause)
		}
		return file.Sync()
	}
	var collector txCollector
	store.SetTxObserver(collector.observe)

	if err := store.Write(context.Background(), func() (map[string][]byte, error) {
		time.Sleep(pause)
		return map[string][]byte{
			"state.jsonl": []byte("state\n"),
			"run.jsonl":   []byte("run\n"),
		}, nil
	}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	got := collector.only(t)
	if got.Kind != TxKindWrite {
		t.Fatalf("Kind = %q, want %q", got.Kind, TxKindWrite)
	}
	if !got.Acquired {
		t.Fatal("Acquired = false, want true")
	}
	if got.Err != nil {
		t.Fatalf("Err = %v, want nil", got.Err)
	}
	if got.Exec < pause {
		t.Fatalf("Exec = %v, want >= %v (callback sleep)", got.Exec, pause)
	}
	if got.Commit < pause {
		t.Fatalf("Commit = %v, want >= %v (fsync sleep)", got.Commit, pause)
	}
	if got.LockWait >= pause {
		t.Fatalf("LockWait = %v, want < %v for an uncontended lock", got.LockWait, pause)
	}
	if got.Settle >= pause {
		t.Fatalf("Settle = %v, want < %v for a settled journal", got.Settle, pause)
	}
	if want := []string{"run.jsonl", "state.jsonl"}; !reflect.DeepEqual(got.Files, want) {
		t.Fatalf("Files = %v, want %v", got.Files, want)
	}
}

func TestTxObserverRecordsReadPhases(t *testing.T) {
	const pause = 30 * time.Millisecond
	store, err := New(t.TempDir(), []string{"state.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	var collector txCollector
	store.SetTxObserver(collector.observe)

	if err := store.Read(context.Background(), func() error {
		time.Sleep(pause)
		return nil
	}); err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	got := collector.only(t)
	if got.Kind != TxKindRead || !got.Acquired || got.Err != nil {
		t.Fatalf("observation = %+v, want an acquired read without error", got)
	}
	if got.Exec < pause {
		t.Fatalf("Exec = %v, want >= %v", got.Exec, pause)
	}
	if got.Commit != 0 || len(got.Files) != 0 {
		t.Fatalf("read Commit/Files = %v/%v, want zero", got.Commit, got.Files)
	}
}

func TestTxObserverLockWaitGrowsWhileAnotherHolderHoldsTheLock(t *testing.T) {
	const hold = 150 * time.Millisecond
	root := t.TempDir()
	holder, err := New(root, []string{"state.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	waiter, err := New(root, []string{"state.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	var collector txCollector
	waiter.SetTxObserver(collector.observe)

	entered := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- holder.Read(context.Background(), func() error {
			close(entered)
			time.Sleep(hold)
			return nil
		})
	}()
	<-entered
	if err := waiter.Write(context.Background(), func() (map[string][]byte, error) {
		return map[string][]byte{"state.jsonl": []byte("after-wait\n")}, nil
	}); err != nil {
		t.Fatalf("waiter Write() error = %v", err)
	}
	if err := <-holderDone; err != nil {
		t.Fatalf("holder Read() error = %v", err)
	}

	got := collector.only(t)
	if !got.Acquired {
		t.Fatal("Acquired = false, want true after the holder released")
	}
	// The holder had ~150ms left when the waiter started, so most of it must be
	// attributed to lock_wait and none to exec.
	if got.LockWait < hold/2 {
		t.Fatalf("LockWait = %v, want >= %v while another holder owned the lock", got.LockWait, hold/2)
	}
	if got.Exec >= hold/2 {
		t.Fatalf("Exec = %v, want the wait not to be counted as exec", got.Exec)
	}
}

func TestTxObserverReportsLockNotAcquired(t *testing.T) {
	root := t.TempDir()
	holder, err := New(root, []string{"state.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	waiter, err := New(root, []string{"state.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	var collector txCollector
	waiter.SetTxObserver(collector.observe)

	entered := make(chan struct{})
	release := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- holder.Read(context.Background(), func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	called := false
	err = waiter.Write(ctx, func() (map[string][]byte, error) {
		called = true
		return nil, nil
	})
	close(release)
	if err := <-holderDone; err != nil {
		t.Fatalf("holder Read() error = %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) || called {
		t.Fatalf("Write() err = %v, callback called = %v, want deadline exceeded without callback", err, called)
	}

	got := collector.only(t)
	if got.Acquired {
		t.Fatal("Acquired = true, want false when the context ended while waiting")
	}
	if !errors.Is(got.Err, context.DeadlineExceeded) {
		t.Fatalf("Err = %v, want deadline exceeded", got.Err)
	}
	if got.LockWait < 40*time.Millisecond {
		t.Fatalf("LockWait = %v, want the time spent waiting before giving up", got.LockWait)
	}
	if got.Settle != 0 || got.Exec != 0 || got.Commit != 0 || len(got.Files) != 0 {
		t.Fatalf("observation = %+v, want no phase after the lock for an unacquired lock", got)
	}
}

func TestTxObserverReportsCallbackErrorWithoutCommit(t *testing.T) {
	store, err := New(t.TempDir(), []string{"state.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	var collector txCollector
	store.SetTxObserver(collector.observe)
	want := errors.New("validation failed")

	if err := store.Write(context.Background(), func() (map[string][]byte, error) {
		time.Sleep(10 * time.Millisecond)
		return nil, want
	}); !errors.Is(err, want) {
		t.Fatalf("Write() error = %v, want %v", err, want)
	}

	got := collector.only(t)
	if !got.Acquired || !errors.Is(got.Err, want) {
		t.Fatalf("observation = %+v, want an acquired tx carrying the callback error", got)
	}
	if got.Exec < 10*time.Millisecond {
		t.Fatalf("Exec = %v, want the failed callback to be timed", got.Exec)
	}
	if len(got.Files) != 0 {
		t.Fatalf("Files = %v, want none when nothing was committed", got.Files)
	}
}

func TestTxObserverReportsEmptyWriteWithoutFiles(t *testing.T) {
	store, err := New(t.TempDir(), []string{"state.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	var collector txCollector
	store.SetTxObserver(collector.observe)

	if err := store.Write(context.Background(), func() (map[string][]byte, error) { return nil, nil }); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	got := collector.only(t)
	if got.Kind != TxKindWrite || !got.Acquired || len(got.Files) != 0 {
		t.Fatalf("observation = %+v, want an acquired write that appended nothing", got)
	}
}

func TestTxObserverRunsAfterTheLockIsReleased(t *testing.T) {
	root := t.TempDir()
	store, err := New(root, []string{"state.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := New(root, []string{"state.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	var probeErrs []error
	store.SetTxObserver(func(context.Context, TxObservation) {
		// A second handle must be able to take the lock from inside the observer;
		// otherwise observer work (stat, logging) would extend the lock hold time.
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		probeErrs = append(probeErrs, other.Read(ctx, func() error { return nil }))
	})

	if err := store.Write(context.Background(), func() (map[string][]byte, error) {
		return map[string][]byte{"state.jsonl": []byte("x\n")}, nil
	}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := store.Read(context.Background(), func() error { return nil }); err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if len(probeErrs) != 2 {
		t.Fatalf("observer calls = %d, want exactly one per transaction", len(probeErrs))
	}
	for i, probeErr := range probeErrs {
		if probeErr != nil {
			t.Fatalf("lock probe %d from observer error = %v, want the lock to be free", i, probeErr)
		}
	}
}

func TestTxObserverReceivesTheCallerContext(t *testing.T) {
	type key struct{}
	store, err := New(t.TempDir(), []string{"state.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	var collector txCollector
	store.SetTxObserver(collector.observe)

	ctx := context.WithValue(context.Background(), key{}, "label")
	if err := store.Read(ctx, func() error { return nil }); err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	collector.only(t)
	if got := collector.ctxs[0].Value(key{}); got != "label" {
		t.Fatalf("observer context value = %v, want the caller context", got)
	}
}

func TestTxObserverNilAndClearedDoNothing(t *testing.T) {
	root := t.TempDir()
	store, err := New(root, []string{"state.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	// No observer installed: behavior is exactly the pre-observer behavior.
	if err := store.Write(context.Background(), func() (map[string][]byte, error) {
		return map[string][]byte{"state.jsonl": []byte("a\n")}, nil
	}); err != nil {
		t.Fatalf("Write() without observer error = %v", err)
	}
	if err := store.Read(context.Background(), func() error { return nil }); err != nil {
		t.Fatalf("Read() without observer error = %v", err)
	}

	var collector txCollector
	store.SetTxObserver(collector.observe)
	store.SetTxObserver(nil)
	if err := store.Write(context.Background(), func() (map[string][]byte, error) {
		return map[string][]byte{"state.jsonl": []byte("b\n")}, nil
	}); err != nil {
		t.Fatalf("Write() after clearing the observer error = %v", err)
	}
	if got := collector.snapshot(); len(got) != 0 {
		t.Fatalf("observations after SetTxObserver(nil) = %+v, want none", got)
	}
	data, err := os.ReadFile(filepath.Join(root, "state.jsonl"))
	if err != nil || string(data) != "a\nb\n" {
		t.Fatalf("state data = %q, err = %v, want both appends", data, err)
	}
}

func TestTxObserverDoesNotSeeRecoverOrReaderRejection(t *testing.T) {
	root := t.TempDir()
	store, err := New(root, []string{"state.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	var collector txCollector
	store.SetTxObserver(collector.observe)
	if err := store.Recover(context.Background()); err != nil {
		t.Fatalf("Recover() error = %v", err)
	}

	reader, err := OpenReader(root, []string{"state.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	var readerCollector txCollector
	reader.SetTxObserver(readerCollector.observe)
	if err := reader.Write(context.Background(), func() (map[string][]byte, error) {
		return map[string][]byte{"state.jsonl": []byte("x\n")}, nil
	}); err == nil {
		t.Fatal("reader Write() unexpectedly succeeded")
	}
	if got := collector.snapshot(); len(got) != 0 {
		t.Fatalf("Recover() produced observations %+v, want none (not a transaction)", got)
	}
	if got := readerCollector.snapshot(); len(got) != 0 {
		t.Fatalf("rejected reader Write() produced observations %+v, want none", got)
	}
}
