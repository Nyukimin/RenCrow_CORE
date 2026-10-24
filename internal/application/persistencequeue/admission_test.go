package persistencequeue

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestAdmissionRejectsBeforeOwnerEffectWhenReservationIsFull(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		capacity     int
		byteCapacity int64
		heldCount    int
		heldBytes    int64
		attemptCount int
		attemptBytes int64
	}{
		{name: "count", capacity: 1, byteCapacity: 16, heldCount: 1, heldBytes: 1, attemptCount: 1, attemptBytes: 1},
		{name: "bytes", capacity: 2, byteCapacity: 16, heldCount: 1, heldBytes: 16, attemptCount: 1, attemptBytes: 1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			queue := newAdmissionTestQueue(t, testCase.capacity, testCase.byteCapacity)
			first, err := Admit(queue, testCase.heldCount, testCase.heldBytes)
			if err != nil {
				t.Fatal(err)
			}
			defer first.Release()

			ownerWrites := 0
			attemptTurn := func() error {
				admission, err := Admit(queue, testCase.attemptCount, testCase.attemptBytes)
				if err != nil {
					return err
				}
				defer admission.Release()
				ownerWrites++
				return nil
			}
			if err := attemptTurn(); !errors.Is(err, ErrFull) {
				t.Fatalf("turn admission at full %s capacity = %v, want ErrFull", testCase.name, err)
			}
			if ownerWrites != 0 {
				t.Fatalf("owner writes after rejected admission = %d, want 0", ownerWrites)
			}
			if status := queue.Status(); status.Reserved != testCase.heldCount || status.ReservedBytes != testCase.heldBytes {
				t.Fatalf("queue status after rejected admission = %+v, want first reservation unchanged", status)
			}
		})
	}
}

func TestAcceptedCallbackRunsOffCallerWithIndependentContext(t *testing.T) {
	queue := newAdmissionTestQueue(t, 1, 64)
	admission, err := Admit(queue, 1, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Release()

	started := make(chan struct{})
	continueApply := make(chan struct{})
	var continueOnce sync.Once
	releaseApply := func() { continueOnce.Do(func() { close(continueApply) }) }
	t.Cleanup(releaseApply)
	callbackContextErr := make(chan error, 1)
	requestContext, cancelRequest := context.WithCancel(context.Background())
	defer cancelRequest()

	requestContext = WithAdmission(requestContext, admission)
	fromContext, ok := AdmissionFromContext(requestContext)
	if !ok || fromContext != admission {
		t.Fatal("context did not return its explicit admission capability")
	}

	enqueueResult := make(chan error, 1)
	go func() {
		_, err := fromContext.Enqueue(testPersistenceCommand("session", "write", 8, func(ctx context.Context) error {
			close(started)
			<-continueApply
			callbackContextErr <- ctx.Err()
			return nil
		}))
		enqueueResult <- err
	}()
	select {
	case err := <-enqueueResult:
		if err != nil {
			t.Fatalf("enqueue accepted command: %v", err)
		}
	case <-time.After(time.Second):
		releaseApply()
		t.Fatal("Enqueue waited for the persistence callback instead of returning to the caller")
	}

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("persistence callback did not start")
	}
	cancelRequest()
	releaseApply()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := admission.Barrier(ctx); err != nil {
		t.Fatalf("barrier after callback: %v", err)
	}
	if err := <-callbackContextErr; err != nil {
		t.Fatalf("accepted callback context was canceled with request: %v", err)
	}
}

func TestAdmissionAcceptsSuperAgentPersistenceOwner(t *testing.T) {
	queue := newAdmissionTestQueue(t, 1, 16)
	admission, err := Admit(queue, 1, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Release()

	if _, err := admission.Enqueue(testPersistenceCommand("superagent", "agent_run.save", 1, func(context.Context) error {
		return nil
	})); err != nil {
		t.Fatalf("enqueue SuperAgent persistence command: %v", err)
	}
}

func TestAdmissionAcceptsActionPersistenceOwner(t *testing.T) {
	queue := newAdmissionTestQueue(t, 1, 16)
	admission, err := Admit(queue, 1, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Release()

	if _, err := admission.Enqueue(testPersistenceCommand(OwnerAction, "action.transaction", 1, func(context.Context) error {
		return nil
	})); err != nil {
		t.Fatalf("enqueue Action persistence command: %v", err)
	}
}

func TestAdmissionAcceptsTransportPersistenceOwner(t *testing.T) {
	queue := newAdmissionTestQueue(t, 1, 16)
	admission, err := Admit(queue, 1, 16)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admission.Enqueue(testPersistenceCommand(OwnerTransport, "transport.request", 1, func(context.Context) error { return nil })); err != nil {
		t.Fatalf("Enqueue(transport) error = %v", err)
	}
	if err := admission.Barrier(context.Background()); err != nil {
		t.Fatalf("Barrier() error = %v", err)
	}
}

func TestAdmissionAcceptsAIWorkflowPersistenceOwner(t *testing.T) {
	queue := newAdmissionTestQueue(t, 1, 16)
	admission, err := Admit(queue, 1, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Release()
	if _, err := admission.Enqueue(testPersistenceCommand(OwnerAIWorkflow, "aiworkflow.context_usage", 1, func(context.Context) error { return nil })); err != nil {
		t.Fatalf("Enqueue(AI Workflow) error = %v", err)
	}
}

func TestBarrierWaitsForAcceptedFIFOCommandsBeforeExternalEffect(t *testing.T) {
	queue := newAdmissionTestQueue(t, 2, 32)
	admission, err := Admit(queue, 2, 32)
	if err != nil {
		t.Fatal(err)
	}

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var releaseOnce sync.Once
	releaseBlockedWriter := func() { releaseOnce.Do(func() { close(releaseFirst) }) }
	t.Cleanup(releaseBlockedWriter)
	var startedOnce sync.Once
	var mu sync.Mutex
	var order []string
	appendOrder := func(value string) {
		mu.Lock()
		order = append(order, value)
		mu.Unlock()
	}

	if _, err := admission.Enqueue(testPersistenceCommand("event", "first", 4, func(context.Context) error {
		startedOnce.Do(func() { close(firstStarted) })
		<-releaseFirst
		appendOrder("first")
		return nil
	})); err != nil {
		t.Fatalf("enqueue first command: %v", err)
	}
	if _, err := admission.Enqueue(testPersistenceCommand("conversation", "second", 5, func(context.Context) error {
		appendOrder("second")
		return nil
	})); err != nil {
		t.Fatalf("enqueue second command: %v", err)
	}
	if err := admission.Release(); err != nil {
		t.Fatalf("release unused reservation: %v", err)
	}
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first persistence callback did not start")
	}

	externalEffectCalls := 0
	shortCtx, cancelShort := context.WithTimeout(context.Background(), 25*time.Millisecond)
	err = admission.Barrier(shortCtx)
	cancelShort()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("barrier with blocked head = %v, want deadline while prior FIFO work is pending", err)
	}
	if externalEffectCalls != 0 {
		t.Fatal("external effect ran before the last accepted ticket persisted")
	}

	releaseBlockedWriter()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := admission.Barrier(ctx); err != nil {
		t.Fatalf("barrier after FIFO commands completed: %v", err)
	}
	externalEffectCalls++
	if externalEffectCalls != 1 {
		t.Fatalf("external effect calls after barrier = %d, want 1", externalEffectCalls)
	}

	mu.Lock()
	gotOrder := append([]string(nil), order...)
	mu.Unlock()
	if want := []string{"first", "second"}; !reflect.DeepEqual(gotOrder, want) {
		t.Fatalf("callback order = %v, want FIFO %v", gotOrder, want)
	}
	if _, err := admission.Enqueue(testPersistenceCommand("task", "after-release", 1, func(context.Context) error { return nil })); !errors.Is(err, ErrAdmissionReleased) {
		t.Fatalf("enqueue after release = %v, want ErrAdmissionReleased", err)
	}
}

func TestReleaseCancelsUnusedReservationButKeepsBarrierTicket(t *testing.T) {
	queue := newAdmissionTestQueue(t, 2, 20)
	admission, err := Admit(queue, 2, 20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admission.Enqueue(testPersistenceCommand("session", "one", 3, func(context.Context) error { return nil })); err != nil {
		t.Fatal(err)
	}
	if err := admission.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := admission.Release(); err != nil {
		t.Fatalf("second release should be idempotent: %v", err)
	}
	if status := queue.Status(); status.Reserved != 0 || status.ReservedBytes != 0 {
		t.Fatalf("queue status after release = %+v, want no unused reservation", status)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := admission.Barrier(ctx); err != nil {
		t.Fatalf("barrier after release: %v", err)
	}

	second, err := Admit(queue, 1, 17)
	if err != nil {
		t.Fatalf("new admission after unused capacity was released: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestAcceptedPublicationFailureRemainsVisibleAtBarrier(t *testing.T) {
	writeErr := errors.New("store unavailable")
	queue, err := NewPersistenceQueue(1, 32)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = queue.Shutdown(ctx)
	})
	admission, err := Admit(queue, 1, 32)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admission.Enqueue(testPersistenceCommand("conversation", "publish", 7, func(context.Context) error {
		return writeErr
	})); err != nil {
		t.Fatalf("enqueue should publish before writer failure: %v", err)
	}
	if err := admission.Release(); err != nil {
		t.Fatalf("release after accepted publication: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := admission.Barrier(ctx); !errors.Is(err, ErrFailed) || !errors.Is(err, writeErr) {
		t.Fatalf("barrier error = %v, want queue failure wrapping %v", err, writeErr)
	}
	status := queue.Status()
	if status.Failure == nil || status.Failure.Sequence != 1 || !errors.Is(status.Failure, writeErr) {
		t.Fatalf("queue failure = %+v, want failed accepted ticket 1", status.Failure)
	}
}

func TestAdmissionContextRequiresExplicitCapability(t *testing.T) {
	queue := newAdmissionTestQueue(t, 1, 8)
	admission, err := Admit(queue, 1, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Release()

	if got, ok := AdmissionFromContext(WithAdmission(context.Background(), admission)); !ok || got != admission {
		t.Fatal("explicit admission capability was not found in context")
	}
	bareBool := context.WithValue(context.Background(), admissionContextKey{}, true)
	if got, ok := AdmissionFromContext(bareBool); ok || got != nil {
		t.Fatalf("bare bool was accepted as an admission capability: (%p, %v)", got, ok)
	}
}

func TestAdmissionAcceptsPersonaOwner(t *testing.T) {
	queue := newAdmissionTestQueue(t, 1, 16)
	admission, err := Admit(queue, 1, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Release()

	if _, err := admission.Enqueue(testPersistenceCommand("persona", "sqlite.record", 1, func(context.Context) error {
		return nil
	})); err != nil {
		t.Fatalf("enqueue persona-owned persistence command: %v", err)
	}
	if err := admission.Barrier(context.Background()); err != nil {
		t.Fatalf("wait for persona-owned persistence command: %v", err)
	}
}

func TestAdmissionMatchesOnlyItsSharedQueue(t *testing.T) {
	queue := newAdmissionTestQueue(t, 1, 16)
	otherQueue := newAdmissionTestQueue(t, 1, 16)
	admission, err := Admit(queue, 1, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Release()
	matcher, ok := any(admission).(interface {
		MatchesQueue(*Queue[PersistenceCommand]) bool
	})
	if !ok {
		t.Fatal("admission does not expose immutable queue identity")
	}

	if !matcher.MatchesQueue(queue) {
		t.Fatal("admission did not match the queue that created it")
	}
	if matcher.MatchesQueue(otherQueue) {
		t.Fatal("admission matched a different queue")
	}
	if matcher.MatchesQueue(nil) {
		t.Fatal("admission matched a nil queue")
	}
	var nilAdmission any = (*Admission)(nil)
	nilMatcher, ok := nilAdmission.(interface {
		MatchesQueue(*Queue[PersistenceCommand]) bool
	})
	if !ok {
		t.Fatal("nil admission does not expose immutable queue identity")
	}
	if nilMatcher.MatchesQueue(queue) {
		t.Fatal("nil admission matched a queue")
	}
}

func newAdmissionTestQueue(t *testing.T, count int, bytes int64) *Queue[PersistenceCommand] {
	t.Helper()
	queue, err := NewPersistenceQueue(count, bytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := queue.Shutdown(ctx); err != nil && !errors.Is(err, ErrFailed) {
			t.Errorf("shutdown persistence queue: %v", err)
		}
	})
	return queue
}

func testPersistenceCommand(owner, kind string, size int64, apply func(context.Context) error) PersistenceCommand {
	return PersistenceCommand{Owner: owner, Kind: kind, SizeBytes: size, Apply: apply}
}
