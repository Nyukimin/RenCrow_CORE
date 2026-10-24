package conversation

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

const preparedRecallRuntimeTestOwner = "owner-a"

type preparedRecallRuntimeReadFunc func(context.Context, string, int) ([]domainmemory.UserMemory, string, error)

type preparedRecallRuntimeFakeSource struct {
	mu       sync.Mutex
	calls    int
	started  chan int
	finished chan int
	read     preparedRecallRuntimeReadFunc
}

func (s *preparedRecallRuntimeFakeSource) ReadPreparedRecallUserMemories(ctx context.Context, ownerID string) ([]domainmemory.UserMemory, string, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.mu.Unlock()
	if s.started != nil {
		select {
		case s.started <- call:
		default:
		}
	}
	memories, coverage, err := s.read(ctx, ownerID, call)
	if s.finished != nil {
		select {
		case s.finished <- call:
		default:
		}
	}
	return clonePreparedRecallRuntimeMemories(memories), coverage, err
}

func (s *preparedRecallRuntimeFakeSource) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func TestPreparedRecallRuntimeWarmPublishesOwnerBoundUserMemoryOnly(t *testing.T) {
	source := &preparedRecallRuntimeFakeSource{read: func(_ context.Context, ownerID string, _ int) ([]domainmemory.UserMemory, string, error) {
		if ownerID != preparedRecallRuntimeTestOwner {
			return nil, "", fmt.Errorf("unexpected source owner %q", ownerID)
		}
		return []domainmemory.UserMemory{preparedRecallRuntimeMemory("warm", "owner-a prefers green tea")}, domainconversation.PreparedRecallCoverageFull, nil
	}}
	runtime := newPreparedRecallRuntimeForTest(t, source)
	if err := runtime.Warm(context.Background()); err != nil {
		t.Fatalf("Warm() error = %v", err)
	}

	capability, ok := runtime.CurrentCapability()
	if !ok {
		t.Fatal("CurrentCapability() unavailable after successful Warm")
	}
	if capability.OwnerID() != preparedRecallRuntimeTestOwner || capability.UserScope() != "user:owner-a" || capability.SourceVersion() == "" {
		t.Fatalf("current capability = owner %q scope %q version %q", capability.OwnerID(), capability.UserScope(), capability.SourceVersion())
	}
	readyOption, ok := capability.ReadyOption(nil)
	if !ok || readyOption.ProjectionOwner != runtime.projection || readyOption.ExpectedSourceVersion != capability.SourceVersion() {
		t.Fatalf("ReadyOption() = (%+v, %t), want current read-only capability binding", readyOption, ok)
	}
	selection := selectPreparedRecallRuntime(t, runtime, capability, "green tea")
	if selection.PreparationStatus != domainconversation.PreparedRecallPreparationReady || selection.Coverage.UserMemory != domainconversation.PreparedRecallCoverageFull || selection.Coverage.Knowledge != domainconversation.PreparedRecallCoverageNone {
		t.Fatalf("prepared selection status/coverage = %q/%+v", selection.PreparationStatus, selection.Coverage)
	}
	if len(selection.UserMemories) != 1 || len(selection.Knowledge) != 0 {
		t.Fatalf("prepared recall injected user memories=%d knowledge=%d, want one user memory and no knowledge", len(selection.UserMemories), len(selection.Knowledge))
	}
	if got := source.callCount(); got != 1 {
		t.Fatalf("source reads = %d, want one startup snapshot", got)
	}
}

func TestPreparedRecallRuntimeWarmRejectsUnboundedSnapshot(t *testing.T) {
	memories := make([]domainmemory.UserMemory, domainconversation.PreparedRecallMaxRecordsPerSource+1)
	source := &preparedRecallRuntimeFakeSource{read: func(context.Context, string, int) ([]domainmemory.UserMemory, string, error) {
		return memories, domainconversation.PreparedRecallCoveragePartial, nil
	}}
	runtime := newPreparedRecallRuntimeForTest(t, source)
	if err := runtime.Warm(context.Background()); err == nil {
		t.Fatal("Warm() accepted a source snapshot above the per-source bound")
	}
	if _, ok := runtime.CurrentCapability(); ok {
		t.Fatal("CurrentCapability() available after an unbounded source snapshot")
	}
}

func TestPreparedRecallRuntimeRetriesTransientInitialWarmOnWorker(t *testing.T) {
	readStarted := make(chan int, 4)
	source := &preparedRecallRuntimeFakeSource{
		started: readStarted,
		read: func(ctx context.Context, _ string, call int) ([]domainmemory.UserMemory, string, error) {
			if call == 1 {
				<-ctx.Done()
				// L1 wraps SQL context failures with %v, so the underlying
				// DeadlineExceeded is not available through errors.Is.
				return nil, "", fmt.Errorf("failed to read prepared recall snapshot: %v", ctx.Err())
			}
			return []domainmemory.UserMemory{preparedRecallRuntimeMemory("recovered", "green tea")}, domainconversation.PreparedRecallCoverageFull, nil
		},
	}
	runtime := newPreparedRecallRuntimeForTest(t, source)

	warmCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	warmErr := runtime.Warm(warmCtx)
	if warmErr == nil {
		t.Fatal("Warm() error = nil, want initial source deadline")
	}
	if _, ok := runtime.CurrentCapability(); ok {
		t.Fatal("CurrentCapability() published after the failed initial warm")
	}

	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		if source.callCount() >= 2 {
			break
		}
		select {
		case <-readStarted:
		case <-timer.C:
			t.Fatalf("source reads = %d, want an asynchronous worker retry after Warm returned", source.callCount())
		}
	}

	capability := waitForPreparedRecallRuntimeCapability(t, runtime)
	if capability.OwnerID() != preparedRecallRuntimeTestOwner {
		t.Fatalf("recovered capability owner = %q, want %q", capability.OwnerID(), preparedRecallRuntimeTestOwner)
	}
}

func TestPreparedRecallRuntimeInitialWarmDoesNotRetryPermanentFailures(t *testing.T) {
	largeSnapshot := make([]domainmemory.UserMemory, domainconversation.PreparedRecallMaxRecordsPerSource+1)
	tests := []struct {
		name string
		read preparedRecallRuntimeReadFunc
	}{
		{
			name: "invalid owner",
			read: func(context.Context, string, int) ([]domainmemory.UserMemory, string, error) {
				return nil, "", domainmemory.ErrUserMemoryOwnerInvalid
			},
		},
		{
			name: "owner validation",
			read: func(context.Context, string, int) ([]domainmemory.UserMemory, string, error) {
				return []domainmemory.UserMemory{{
					ID: "foreign", Namespace: "user:owner-b", UserID: "owner-b",
				}}, domainconversation.PreparedRecallCoverageFull, nil
			},
		},
		{
			name: "capacity",
			read: func(context.Context, string, int) ([]domainmemory.UserMemory, string, error) {
				return largeSnapshot, domainconversation.PreparedRecallCoveragePartial, nil
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			readStarted := make(chan int, 4)
			source := &preparedRecallRuntimeFakeSource{started: readStarted, read: tt.read}
			runtime := newPreparedRecallRuntimeForTest(t, source)
			if err := runtime.Warm(context.Background()); err == nil {
				t.Fatal("Warm() error = nil, want permanent source validation failure")
			}
			select {
			case got := <-readStarted:
				if got != 1 {
					t.Fatalf("initial source read = %d, want 1", got)
				}
			case <-time.After(time.Second):
				t.Fatal("initial source read was not observed")
			}

			timer := time.NewTimer(preparedRecallInitialWarmRetryBackoff + 100*time.Millisecond)
			defer timer.Stop()
			select {
			case got := <-readStarted:
				t.Fatalf("source read %d retried a permanent failure", got)
			case <-timer.C:
				if got := source.callCount(); got != 1 {
					t.Fatalf("source reads = %d, want one attempt for permanent failure", got)
				}
			}
		})
	}
}

func TestPreparedRecallRuntimeInitialWarmRetriesAreBounded(t *testing.T) {
	readStarted := make(chan int, preparedRecallInitialWarmRetryAttempts+1)
	source := &preparedRecallRuntimeFakeSource{
		started: readStarted,
		read: func(context.Context, string, int) ([]domainmemory.UserMemory, string, error) {
			return nil, "", context.DeadlineExceeded
		},
	}
	runtime := newPreparedRecallRuntimeForTest(t, source)
	if err := runtime.Warm(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Warm() error = %v, want initial deadline", err)
	}

	startTimer := time.NewTimer(10 * time.Second)
	defer startTimer.Stop()
	for want := 1; want <= preparedRecallInitialWarmRetryAttempts+1; want++ {
		select {
		case got := <-readStarted:
			if got != want {
				t.Fatalf("source read start = %d, want ordered attempt %d", got, want)
			}
		case <-startTimer.C:
			t.Fatalf("timed out waiting for bounded retry attempt %d", want)
		}
	}
	timer := time.NewTimer(preparedRecallInitialWarmMaxBackoff + 100*time.Millisecond)
	defer timer.Stop()
	for {
		if got := source.callCount(); got != preparedRecallInitialWarmRetryAttempts+1 {
			t.Fatalf("source reads after retry exhaustion = %d, want %d", got, preparedRecallInitialWarmRetryAttempts+1)
		}
		select {
		case <-readStarted:
			t.Fatalf("source started an extra retry after the configured limit: reads=%d", source.callCount())
		case <-timer.C:
			if _, ok := runtime.CurrentCapability(); ok {
				t.Fatal("CurrentCapability() available after all initial warm retries failed")
			}
			return
		}
	}
}

func TestPreparedRecallRuntimeCloseCancelsInitialWarmRetry(t *testing.T) {
	readStarted := make(chan int, 4)
	readFinished := make(chan int, 4)
	source := &preparedRecallRuntimeFakeSource{
		started:  readStarted,
		finished: readFinished,
		read: func(ctx context.Context, _ string, _ int) ([]domainmemory.UserMemory, string, error) {
			<-ctx.Done()
			return nil, "", fmt.Errorf("prepared recall read interrupted: %v", ctx.Err())
		},
	}
	runtime := newPreparedRecallRuntimeForTest(t, source)
	warmCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	if err := runtime.Warm(warmCtx); err == nil {
		cancel()
		t.Fatal("Warm() error = nil, want initial source deadline")
	}
	cancel()
	waitForPreparedRecallRuntimeRead(t, readStarted, 2)

	closed := make(chan error, 1)
	go func() { closed <- runtime.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close() did not cancel and join the initial warm retry")
	}
	waitForPreparedRecallRuntimeRead(t, readFinished, 2)
	if got := source.callCount(); got != 2 {
		t.Fatalf("source reads after Close() = %d, want no retry after cancellation", got)
	}
}

func TestPreparedRecallRuntimeDuplicateOutOfOrderOpaqueNoticesCoalesce(t *testing.T) {
	readStarted := make(chan int, 4)
	readFinished := make(chan int, 4)
	releaseSecondRead := make(chan struct{})
	source := &preparedRecallRuntimeFakeSource{
		started:  readStarted,
		finished: readFinished,
		read: func(ctx context.Context, ownerID string, call int) ([]domainmemory.UserMemory, string, error) {
			if ownerID != preparedRecallRuntimeTestOwner {
				return nil, "", fmt.Errorf("unexpected owner %q", ownerID)
			}
			switch call {
			case 1:
				return []domainmemory.UserMemory{preparedRecallRuntimeMemory("initial", "initial green tea")}, domainconversation.PreparedRecallCoverageFull, nil
			case 2:
				select {
				case <-releaseSecondRead:
					return []domainmemory.UserMemory{preparedRecallRuntimeMemory("stale", "stale green tea")}, domainconversation.PreparedRecallCoverageFull, nil
				case <-ctx.Done():
					return nil, "", ctx.Err()
				}
			default:
				return []domainmemory.UserMemory{preparedRecallRuntimeMemory("latest", "latest green tea")}, domainconversation.PreparedRecallCoverageFull, nil
			}
		},
	}
	runtime := newPreparedRecallRuntimeForTest(t, source)
	if err := runtime.Warm(context.Background()); err != nil {
		t.Fatalf("Warm() error = %v", err)
	}
	initial, ok := runtime.CurrentCapability()
	if !ok {
		t.Fatal("initial capability unavailable")
	}

	runtime.HandleUserMemoryMutation(l1sqlite.UserMemoryMutationNotice{UserID: preparedRecallRuntimeTestOwner, ChangeID: "opaque-z", MemoryIDs: []string{"first"}})
	waitForPreparedRecallRuntimeRead(t, readStarted, 2)
	if got, ok := runtime.CurrentCapability(); ok {
		t.Fatalf("capability survived first synchronous revoke: version %q", got.SourceVersion())
	}
	// ChangeID is opaque: duplicate and out-of-order values are all invalidations.
	runtime.HandleUserMemoryMutation(l1sqlite.UserMemoryMutationNotice{UserID: preparedRecallRuntimeTestOwner, ChangeID: "opaque-a", MemoryIDs: []string{"second"}})
	runtime.HandleUserMemoryMutation(l1sqlite.UserMemoryMutationNotice{UserID: preparedRecallRuntimeTestOwner, ChangeID: "opaque-z", MemoryIDs: []string{"first"}})
	runtime.HandleUserMemoryMutation(l1sqlite.UserMemoryMutationNotice{UserID: "another-owner", ChangeID: "opaque-0", MemoryIDs: []string{"other"}})
	if _, ok := runtime.CurrentCapability(); ok {
		t.Fatal("current capability remained readable after duplicate/out-of-order owner notices")
	}

	close(releaseSecondRead)
	waitForPreparedRecallRuntimeRead(t, readStarted, 3)
	waitForPreparedRecallRuntimeRead(t, readFinished, 3)
	capability := waitForPreparedRecallRuntimeCapability(t, runtime)
	if capability.SourceVersion() == initial.SourceVersion() {
		t.Fatalf("version did not advance after invalidation: %q", capability.SourceVersion())
	}
	selection := selectPreparedRecallRuntime(t, runtime, capability, "latest green tea")
	if len(selection.UserMemories) != 1 || selection.UserMemories[0].Decision.Item.ID != "latest" {
		t.Fatalf("coalesced rebuild selected stale source: %+v", selection.UserMemories)
	}
	if got := source.callCount(); got != 3 {
		t.Fatalf("source reads = %d, want startup + stale in-flight + one coalesced rebuild", got)
	}
}

func TestPreparedRecallRuntimeMutationDuringRebuildPreventsOldPublication(t *testing.T) {
	readStarted := make(chan int, 4)
	readFinished := make(chan int, 4)
	source := &preparedRecallRuntimeFakeSource{
		started:  readStarted,
		finished: readFinished,
		read: func(_ context.Context, _ string, call int) ([]domainmemory.UserMemory, string, error) {
			switch call {
			case 1:
				return []domainmemory.UserMemory{preparedRecallRuntimeMemory("initial", "initial green tea")}, domainconversation.PreparedRecallCoverageFull, nil
			case 2:
				return []domainmemory.UserMemory{preparedRecallRuntimeMemory("old", "old green tea")}, domainconversation.PreparedRecallCoverageFull, nil
			default:
				return []domainmemory.UserMemory{preparedRecallRuntimeMemory("latest", "latest green tea")}, domainconversation.PreparedRecallCoverageFull, nil
			}
		},
	}
	runtime := newPreparedRecallRuntimeForTest(t, source)
	if err := runtime.Warm(context.Background()); err != nil {
		t.Fatalf("Warm() error = %v", err)
	}
	runtime.projection.publishMu.Lock()
	publicationBlocked := true
	defer func() {
		if publicationBlocked {
			runtime.projection.publishMu.Unlock()
		}
	}()
	runtime.HandleUserMemoryMutation(l1sqlite.UserMemoryMutationNotice{UserID: preparedRecallRuntimeTestOwner, ChangeID: "first", MemoryIDs: []string{"memory"}})
	waitForPreparedRecallRuntimeRead(t, readStarted, 2)
	waitForPreparedRecallRuntimeRead(t, readFinished, 2)
	waitForPreparedRecallRuntimeCandidate(t, runtime)
	runtime.HandleUserMemoryMutation(l1sqlite.UserMemoryMutationNotice{UserID: preparedRecallRuntimeTestOwner, ChangeID: "second", MemoryIDs: []string{"memory"}})
	runtime.projection.mu.Lock()
	remainingCandidates := len(runtime.projection.candidates)
	runtime.projection.mu.Unlock()
	if remainingCandidates != 0 {
		t.Fatalf("stale candidates after synchronous second revoke = %d, want none", remainingCandidates)
	}
	if _, ok := runtime.CurrentCapability(); ok {
		t.Fatal("current capability remained available after second mutation")
	}
	runtime.projection.publishMu.Unlock()
	publicationBlocked = false
	waitForPreparedRecallRuntimeRead(t, readStarted, 3)
	waitForPreparedRecallRuntimeRead(t, readFinished, 3)
	capability := waitForPreparedRecallRuntimeCapability(t, runtime)
	selection := selectPreparedRecallRuntime(t, runtime, capability, "latest green tea")
	if len(selection.UserMemories) != 1 || selection.UserMemories[0].Decision.Item.ID != "latest" {
		t.Fatalf("published memory = %+v, want latest source snapshot", selection.UserMemories)
	}
}

func TestPreparedRecallRuntimeConcurrentRevokeAndRead(t *testing.T) {
	source := &preparedRecallRuntimeFakeSource{read: func(context.Context, string, int) ([]domainmemory.UserMemory, string, error) {
		return []domainmemory.UserMemory{preparedRecallRuntimeMemory("memory", "green tea")}, domainconversation.PreparedRecallCoverageFull, nil
	}}
	runtime := newPreparedRecallRuntimeForTest(t, source)
	if err := runtime.Warm(context.Background()); err != nil {
		t.Fatalf("Warm() error = %v", err)
	}
	oldCapability, ok := runtime.CurrentCapability()
	if !ok {
		t.Fatal("initial capability unavailable")
	}
	oldLease, _, err := runtime.projection.Acquire(oldCapability.OwnerID(), oldCapability.UserScope(), oldCapability.SourceVersion())
	if err != nil {
		t.Fatalf("Acquire initial generation: %v", err)
	}
	defer oldLease.Release()

	start := make(chan struct{})
	var readers sync.WaitGroup
	for i := 0; i < 32; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			<-start
			for n := 0; n < 50; n++ {
				capability, ok := runtime.CurrentCapability()
				if !ok {
					continue
				}
				lease, _, acquireErr := runtime.projection.Acquire(capability.OwnerID(), capability.UserScope(), capability.SourceVersion())
				if acquireErr == nil {
					_, _ = lease.Select(domainconversation.PreparedRecallSelectInput{
						AuthenticatedOwnerID: capability.OwnerID(), UserScope: capability.UserScope(),
						ExpectedSourceVersion: capability.SourceVersion(), Persona: "mio", Message: "green tea", Now: time.Now(),
					})
					lease.Release()
				}
			}
		}()
	}
	close(start)
	runtime.HandleUserMemoryMutation(l1sqlite.UserMemoryMutationNotice{UserID: preparedRecallRuntimeTestOwner, ChangeID: "opaque", MemoryIDs: []string{"memory"}})
	done := make(chan struct{})
	go func() { readers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("concurrent capability reads blocked behind revoke")
	}
	if _, err := oldLease.Select(domainconversation.PreparedRecallSelectInput{
		AuthenticatedOwnerID: oldCapability.OwnerID(), UserScope: oldCapability.UserScope(),
		ExpectedSourceVersion: oldCapability.SourceVersion(), Persona: "mio", Message: "green tea", Now: time.Now(),
	}); !errors.Is(err, ErrPreparedRecallProjectionStale) {
		t.Fatalf("old read lease Select() error = %v, want stale after synchronous revoke", err)
	}
}

func TestPreparedRecallRuntimeRebuildFailureStaysUnavailable(t *testing.T) {
	readStarted := make(chan int, 2)
	readFinished := make(chan int, 2)
	source := &preparedRecallRuntimeFakeSource{
		started:  readStarted,
		finished: readFinished,
		read: func(_ context.Context, _ string, call int) ([]domainmemory.UserMemory, string, error) {
			if call == 1 {
				return []domainmemory.UserMemory{preparedRecallRuntimeMemory("initial", "green tea")}, domainconversation.PreparedRecallCoverageFull, nil
			}
			return nil, "", errors.New("source unavailable")
		},
	}
	runtime := newPreparedRecallRuntimeForTest(t, source)
	if err := runtime.Warm(context.Background()); err != nil {
		t.Fatalf("Warm() error = %v", err)
	}
	runtime.HandleUserMemoryMutation(l1sqlite.UserMemoryMutationNotice{UserID: preparedRecallRuntimeTestOwner, ChangeID: "changed", MemoryIDs: []string{"memory"}})
	waitForPreparedRecallRuntimeRead(t, readStarted, 2)
	waitForPreparedRecallRuntimeRead(t, readFinished, 2)
	if _, ok := runtime.CurrentCapability(); ok {
		t.Fatal("current capability remained available after source rebuild failure")
	}
	if lease, _, err := runtime.projection.Acquire(preparedRecallRuntimeTestOwner, "user:owner-a", "unpublished"); lease != nil || !errors.Is(err, ErrPreparedRecallProjectionUnavailable) {
		t.Fatalf("projection Acquire after failed rebuild = (%v, %v), want unavailable", lease, err)
	}
}

func TestPreparedRecallRuntimeCloseCancelsWorkerAndJoins(t *testing.T) {
	readStarted := make(chan int, 2)
	readFinished := make(chan int, 2)
	source := &preparedRecallRuntimeFakeSource{
		started:  readStarted,
		finished: readFinished,
		read: func(ctx context.Context, _ string, call int) ([]domainmemory.UserMemory, string, error) {
			if call == 1 {
				return []domainmemory.UserMemory{preparedRecallRuntimeMemory("initial", "green tea")}, domainconversation.PreparedRecallCoverageFull, nil
			}
			<-ctx.Done()
			return nil, "", ctx.Err()
		},
	}
	runtime := newPreparedRecallRuntimeForTest(t, source)
	if err := runtime.Warm(context.Background()); err != nil {
		t.Fatalf("Warm() error = %v", err)
	}
	runtime.HandleUserMemoryMutation(l1sqlite.UserMemoryMutationNotice{UserID: preparedRecallRuntimeTestOwner, ChangeID: "changed", MemoryIDs: []string{"memory"}})
	waitForPreparedRecallRuntimeRead(t, readStarted, 2)
	closed := make(chan error, 1)
	go func() { closed <- runtime.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close() did not cancel source read and join worker")
	}
	select {
	case <-runtime.done:
	default:
		t.Fatal("Close() returned before worker exit")
	}
	if _, ok := runtime.CurrentCapability(); ok {
		t.Fatal("CurrentCapability() available after Close")
	}
}

func newPreparedRecallRuntimeForTest(t *testing.T, source PreparedRecallUserMemorySource) *PreparedRecallRuntime {
	t.Helper()
	runtime, err := NewPreparedRecallRuntime(preparedRecallRuntimeTestOwner, source)
	if err != nil {
		t.Fatalf("NewPreparedRecallRuntime() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime
}

func preparedRecallRuntimeMemory(id, statement string) domainmemory.UserMemory {
	return domainmemory.UserMemory{
		ID: id, Namespace: "user:owner-a", UserID: "owner-a", Type: domainmemory.UserMemoryTypePreference,
		Statement: statement, EvidenceEventIDs: []string{"event-" + id}, Confidence: 0.9, Sensitivity: "normal",
		State: domainmemory.MemoryStateConfirmed, Scope: "mio_only", Active: true,
	}
}

func clonePreparedRecallRuntimeMemories(memories []domainmemory.UserMemory) []domainmemory.UserMemory {
	cloned := make([]domainmemory.UserMemory, len(memories))
	copy(cloned, memories)
	for i := range cloned {
		cloned[i].EvidenceEventIDs = append([]string(nil), memories[i].EvidenceEventIDs...)
	}
	return cloned
}

func selectPreparedRecallRuntime(t *testing.T, runtime *PreparedRecallRuntime, capability PreparedRecallRuntimeCapability, message string) domainconversation.PreparedRecallSelection {
	t.Helper()
	lease, _, err := runtime.projection.Acquire(capability.OwnerID(), capability.UserScope(), capability.SourceVersion())
	if err != nil {
		t.Fatalf("Acquire current projection: %v", err)
	}
	defer lease.Release()
	selection, err := lease.Select(domainconversation.PreparedRecallSelectInput{
		AuthenticatedOwnerID: capability.OwnerID(), UserScope: capability.UserScope(),
		ExpectedSourceVersion: capability.SourceVersion(), Persona: "mio", Message: message, Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("Select current projection: %v", err)
	}
	return selection
}

func waitForPreparedRecallRuntimeRead(t *testing.T, reads <-chan int, want int) {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case got := <-reads:
			if got == want {
				return
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for source read %d", want)
		}
	}
}

func waitForPreparedRecallRuntimeCapability(t *testing.T, runtime *PreparedRecallRuntime) PreparedRecallRuntimeCapability {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if capability, ok := runtime.CurrentCapability(); ok {
			return capability
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			t.Fatal("timed out waiting for current prepared recall capability")
		}
	}
}

func waitForPreparedRecallRuntimeCandidate(t *testing.T, runtime *PreparedRecallRuntime) {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		runtime.projection.mu.Lock()
		staged := len(runtime.projection.candidates) > 0
		runtime.projection.mu.Unlock()
		if staged {
			return
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			t.Fatal("timed out waiting for a staged projection candidate")
		}
	}
}
