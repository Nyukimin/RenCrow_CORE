package persistencequeue

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type batchTestStore struct {
	started   chan struct{}
	release   chan struct{}
	err       error
	mu        sync.Mutex
	writes    [][]map[string][]byte
	startOnce sync.Once
}

func (s *batchTestStore) WriteJSONLBatch(_ context.Context, writes []map[string][]byte) error {
	s.startOnce.Do(func() {
		if s.started != nil {
			close(s.started)
		}
	})
	if s.release != nil {
		<-s.release
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	copyWrites := make([]map[string][]byte, len(writes))
	for index, write := range writes {
		copyWrites[index] = make(map[string][]byte, len(write))
		for filename, payload := range write {
			copyWrites[index][filename] = append([]byte(nil), payload...)
		}
	}
	s.writes = append(s.writes, copyWrites)
	return s.err
}

func (s *batchTestStore) snapshot() [][]map[string][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]map[string][]byte(nil), s.writes...)
}

func newBatchCommand(token *JSONLBatchToken, payload []byte, applied *int) PersistenceCommand {
	ownedPayload := append([]byte(nil), payload...)
	return PersistenceCommand{
		Owner: OwnerTransport, Kind: KindTransportReceiptTransaction, SizeBytes: int64(len(ownedPayload)),
		JSONLBatch: &JSONLWriteBatch{Token: token, Payloads: map[string][]byte{"transport_request.jsonl": ownedPayload}},
		Apply: func(context.Context) error {
			if applied != nil {
				*applied++
			}
			return nil
		},
	}
}

func enqueueQueueBlocker(t *testing.T, queue *Queue[PersistenceCommand]) func() {
	t.Helper()
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	if _, err := queue.Enqueue(PersistenceCommand{
		Owner: OwnerSession, Kind: "session.blocker", SizeBytes: 1,
		Apply: func(context.Context) error { close(started); <-release; return nil },
	}, 1); err != nil {
		t.Fatalf("enqueue queue blocker: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("queue blocker did not start")
	}
	return func() { releaseOnce.Do(func() { close(release) }) }
}

func TestTransportJSONLBatchTicketsWaitForOneDurableCommit(t *testing.T) {
	store := &batchTestStore{started: make(chan struct{}), release: make(chan struct{})}
	var releaseBatchOnce sync.Once
	releaseBatch := func() { releaseBatchOnce.Do(func() { close(store.release) }) }
	queue, err := NewPersistenceQueue(8, 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Shutdown(context.Background())
	defer releaseBatch()
	releaseHead := enqueueQueueBlocker(t, queue)
	defer releaseHead()
	token := NewJSONLBatchToken(store)
	var applied int
	var last uint64
	for _, value := range []string{"one\n", "two\n", "three\n"} {
		last, err = queue.Enqueue(newBatchCommand(token, []byte(value), &applied), int64(len(value)))
		if err != nil {
			t.Fatalf("enqueue transport payload: %v", err)
		}
	}
	releaseHead()
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("typed JSONL batch writer did not start")
	}
	if status := queue.Status(); status.Pending != 3 || !status.InFlight {
		t.Fatalf("status while batch commit is blocked = %+v, want all three commands pending and in-flight", status)
	}
	shortCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := queue.Wait(shortCtx, last); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait before JSONL commit = %v, want deadline", err)
	}
	releaseBatch()
	if err := queue.Wait(context.Background(), last); err != nil {
		t.Fatalf("Wait after JSONL commit: %v", err)
	}
	if status := queue.Status(); status.BatchWrites != 1 || status.BatchedCommands != 3 {
		t.Fatalf("successful batch counters = writes %d commands %d, want 1 and 3", status.BatchWrites, status.BatchedCommands)
	}
	if applied != 0 {
		t.Fatalf("individual Apply calls = %d, want 0 for grouped writes", applied)
	}
	writes := store.snapshot()
	if len(writes) != 1 || len(writes[0]) != 3 {
		t.Fatalf("durable write groups = %d; first group size = %d, want one group of three", len(writes), len(writes[0]))
	}
	for index, want := range []string{"one\n", "two\n", "three\n"} {
		if got := string(writes[0][index]["transport_request.jsonl"]); got != want {
			t.Fatalf("payload[%d] = %q, want %q", index, got, want)
		}
	}
}

func TestTransportJSONLBatchStopsAtOwnerTokenAndOrdinaryCommand(t *testing.T) {
	firstStore := &batchTestStore{}
	secondStore := &batchTestStore{}
	queue, err := NewPersistenceQueue(16, 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Shutdown(context.Background())
	releaseHead := enqueueQueueBlocker(t, queue)
	defer releaseHead()
	firstToken := NewJSONLBatchToken(firstStore)
	secondToken := NewJSONLBatchToken(secondStore)
	var applied int
	commands := []PersistenceCommand{
		newBatchCommand(firstToken, []byte("a1\n"), &applied),
		newBatchCommand(firstToken, []byte("a2\n"), &applied),
		{Owner: OwnerTransport, Kind: KindTransportReceiptTransaction, SizeBytes: 1, Apply: func(context.Context) error { applied++; return nil }},
		newBatchCommand(firstToken, []byte("a3\n"), &applied),
		newBatchCommand(firstToken, []byte("a4\n"), &applied),
		newBatchCommand(secondToken, []byte("b1\n"), &applied),
		newBatchCommand(secondToken, []byte("b2\n"), &applied),
	}
	var last uint64
	for _, command := range commands {
		last, err = queue.Enqueue(command, command.SizeBytes)
		if err != nil {
			t.Fatalf("enqueue boundary command: %v", err)
		}
	}
	releaseHead()
	if err := queue.Wait(context.Background(), last); err != nil {
		t.Fatalf("wait for boundary commands: %v", err)
	}
	if got := len(firstStore.snapshot()); got != 2 {
		t.Fatalf("first owner transactions = %d, want 2 groups separated by ordinary command", got)
	}
	if got := len(secondStore.snapshot()); got != 1 {
		t.Fatalf("second owner transactions = %d, want one store-specific group", got)
	}
	if applied != 1 {
		t.Fatalf("ordinary Apply calls = %d, want 1", applied)
	}
	if status := queue.Status(); status.BatchWrites != 3 || status.BatchedCommands != 6 {
		t.Fatalf("store-boundary counters = %+v, want three 2-command batches", status)
	}
}

func TestTransportJSONLBatchCapsCommandsAndBytes(t *testing.T) {
	t.Run("command cap", func(t *testing.T) {
		store := &batchTestStore{}
		queue, err := NewPersistenceQueue(66, 1024)
		if err != nil {
			t.Fatal(err)
		}
		defer queue.Shutdown(context.Background())
		releaseHead := enqueueQueueBlocker(t, queue)
		defer releaseHead()
		token := NewJSONLBatchToken(store)
		var applied int
		var last uint64
		for index := 0; index < 65; index++ {
			payload := []byte(fmt.Sprintf("%d", index))
			last, err = queue.Enqueue(newBatchCommand(token, payload, &applied), int64(len(payload)))
			if err != nil {
				t.Fatalf("enqueue command %d: %v", index, err)
			}
		}
		releaseHead()
		if err := queue.Wait(context.Background(), last); err != nil {
			t.Fatalf("wait for capped batch: %v", err)
		}
		if got := len(store.snapshot()); got != 1 {
			t.Fatalf("batch commits = %d, want one group of 64 and one normal single write", got)
		}
		if applied != 1 {
			t.Fatalf("single fallback Apply calls = %d, want 1", applied)
		}
		if status := queue.Status(); status.BatchWrites != 1 || status.BatchedCommands != 64 {
			t.Fatalf("command-cap counters = %+v, want one 64-command batch", status)
		}
	})

	t.Run("byte cap", func(t *testing.T) {
		store := &batchTestStore{}
		queue, err := NewPersistenceQueue(4, 3<<20)
		if err != nil {
			t.Fatal(err)
		}
		defer queue.Shutdown(context.Background())
		releaseHead := enqueueQueueBlocker(t, queue)
		defer releaseHead()
		token := NewJSONLBatchToken(store)
		var applied int
		payload := make([]byte, 600<<10)
		oversized := make([]byte, int(MaxJSONLBatchBytes+1))
		var last uint64
		for index, nextPayload := range [][]byte{oversized, payload, payload} {
			last, err = queue.Enqueue(newBatchCommand(token, nextPayload, &applied), int64(len(nextPayload)))
			if err != nil {
				t.Fatalf("enqueue large command %d: %v", index, err)
			}
		}
		releaseHead()
		if err := queue.Wait(context.Background(), last); err != nil {
			t.Fatalf("wait for byte-capped commands: %v", err)
		}
		if got := len(store.snapshot()); got != 0 {
			t.Fatalf("group commits = %d, want none when the two commands exceed 1 MiB", got)
		}
		if applied != 3 {
			t.Fatalf("single fallback Apply calls = %d, want 3", applied)
		}
		if status := queue.Status(); status.BatchWrites != 0 || status.BatchedCommands != 0 {
			t.Fatalf("byte-cap counters = %+v, want no batch", status)
		}
	})
}

func TestTransportJSONLBatchFailureBlocksGroupAndLaterTickets(t *testing.T) {
	wantErr := errors.New("injected batch commit failure")
	store := &batchTestStore{err: wantErr}
	queue, err := NewPersistenceQueue(4, 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Shutdown(context.Background())
	releaseHead := enqueueQueueBlocker(t, queue)
	defer releaseHead()
	token := NewJSONLBatchToken(store)
	first, err := queue.Enqueue(newBatchCommand(token, []byte("first\n"), nil), 6)
	if err != nil {
		t.Fatal(err)
	}
	second, err := queue.Enqueue(newBatchCommand(token, []byte("second\n"), nil), 7)
	if err != nil {
		t.Fatal(err)
	}
	later, err := queue.Enqueue(PersistenceCommand{Owner: OwnerSession, Kind: "session.later", SizeBytes: 1, Apply: func(context.Context) error { return nil }}, 1)
	if err != nil {
		t.Fatal(err)
	}
	releaseHead()
	err = queue.Wait(context.Background(), second)
	var failure *Failure
	if !errors.Is(err, ErrFailed) || !errors.As(err, &failure) || !errors.Is(err, wantErr) {
		t.Fatalf("Wait(group ticket) = %v, want queue failure wrapping batch commit error", err)
	}
	if failure.Sequence != first {
		t.Fatalf("failure sequence = %d, want first batch sequence %d", failure.Sequence, first)
	}
	if err := queue.Wait(context.Background(), later); !errors.Is(err, ErrFailed) {
		t.Fatalf("Wait(later ticket) = %v, want same failed queue", err)
	}
	if status := queue.Status(); status.Pending != 3 || status.Failure == nil {
		t.Fatalf("status after batch failure = %+v, want all commands retained and queue failed", status)
	}
	if status := queue.Status(); status.BatchWrites != 0 || status.BatchedCommands != 0 {
		t.Fatalf("failed batch counters = writes %d commands %d, want 0 and 0", status.BatchWrites, status.BatchedCommands)
	}
}

func TestMalformedTransportBatchIsFIFOFailureBoundary(t *testing.T) {
	store := &batchTestStore{}
	queue, err := NewPersistenceQueue(4, 128)
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Shutdown(context.Background())
	releaseHead := enqueueQueueBlocker(t, queue)
	defer releaseHead()
	token := NewJSONLBatchToken(store)
	var applied int
	validTicket, err := queue.Enqueue(newBatchCommand(token, []byte("valid\n"), &applied), int64(len("valid\n")))
	if err != nil {
		t.Fatal(err)
	}
	malformed := newBatchCommand(token, []byte("bad\n"), &applied)
	malformed.SizeBytes++
	invalidTicket, err := queue.Enqueue(malformed, malformed.SizeBytes)
	if err != nil {
		t.Fatal(err)
	}
	laterTicket, err := queue.Enqueue(newBatchCommand(token, []byte("later\n"), &applied), int64(len("later\n")))
	if err != nil {
		t.Fatal(err)
	}
	releaseHead()
	if err := queue.Wait(context.Background(), validTicket); err != nil {
		t.Fatalf("valid command before malformed boundary: %v", err)
	}
	err = queue.Wait(context.Background(), invalidTicket)
	if !errors.Is(err, ErrFailed) || !errors.Is(err, ErrInvalidPersistenceCommand) {
		t.Fatalf("malformed descriptor error = %v, want queue failure wrapping validation error", err)
	}
	if err := queue.Wait(context.Background(), laterTicket); !errors.Is(err, ErrFailed) {
		t.Fatalf("later ticket error = %v, want same failed queue", err)
	}
	if applied != 1 {
		t.Fatalf("individual Apply calls before failure = %d, want only the valid head", applied)
	}
	if got := len(store.snapshot()); got != 0 {
		t.Fatalf("typed batch writes = %d, want malformed descriptor to prevent group commit", got)
	}
}
