package main

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/sourcefetcher"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

func TestMemoryLifecycleCancellationJoinsInFlightWorkWithoutFailureTask(t *testing.T) {
	runner := &blockingMemoryLifecycleRunner{started: make(chan struct{})}
	listener := &captureBackgroundJobEventListener{}
	owner := &captureBackgroundFailureTaskOwner{}
	ctx, cancel := context.WithCancel(context.Background())
	done := startMemoryLifecycleJobRunner(ctx, runner, memoryLifecycleJobConfig{
		Interval: time.Hour,
		Now:      func() time.Time { return time.Date(2026, 9, 14, 16, 0, 0, 0, time.UTC) },
		Label:    "shutdown-test",
	}, newBackgroundJobFailureReporter(listener, owner))
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("memory lifecycle runner did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("memory lifecycle runner did not join after cancellation")
	}
	if got := runner.calls.Load(); got != 1 {
		t.Fatalf("maintenance calls=%d, want one in-flight call and no next tick", got)
	}
	if events := listener.Events(); len(events) != 0 {
		t.Fatalf("cancellation produced failure events: %#v", events)
	}
	if len(owner.created) != 0 {
		t.Fatalf("cancellation created failure Tasks: %#v", owner.created)
	}
}

func TestSourceRegistryShutdownSkipsCanceledOwner(t *testing.T) {
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	listener := &captureBackgroundJobEventListener{}
	owner := &captureBackgroundFailureTaskOwner{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := startSourceRegistrySweeper(ctx, nil, store, newBackgroundJobFailureReporter(listener, owner))
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("source registry sweeper did not join after cancellation")
	}
	if events := listener.Events(); len(events) != 0 {
		t.Fatalf("cancellation produced source failure events: %#v", events)
	}
	if len(owner.created) != 0 {
		t.Fatalf("cancellation created source failure Tasks: %#v", owner.created)
	}
}

func TestSourceRegistryShutdownCancelsInFlightBeforeDueSweep(t *testing.T) {
	store := &sourceRegistryShutdownStore{listEntered: make(chan struct{})}
	listener := &captureBackgroundJobEventListener{}
	owner := &captureBackgroundFailureTaskOwner{}
	ctx, cancel := context.WithCancel(context.Background())
	done := startSourceRegistrySweeper(ctx, nil, store, newBackgroundJobFailureReporter(listener, owner))
	select {
	case <-store.listEntered:
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("source registry sweep did not enter the in-flight list operation")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("source registry sweep did not join after cancellation")
	}
	if got := store.dueCalls.Load(); got != 0 {
		t.Fatalf("due-stage calls=%d, want zero after in-flight cancellation", got)
	}
	if events := listener.Events(); len(events) != 0 {
		t.Fatalf("cancellation produced source failure events: %#v", events)
	}
	if len(owner.created) != 0 {
		t.Fatalf("cancellation created source failure Tasks: %#v", owner.created)
	}
}

func TestSourceRegistryShutdownReportsActiveError(t *testing.T) {
	store := &sourceRegistryShutdownStore{
		listEntered: make(chan struct{}),
		listErr:     errors.New("registry list failed"),
	}
	listener := &captureBackgroundJobEventListener{}
	owner := &captureBackgroundFailureTaskOwner{}
	ctx, cancel := context.WithCancel(context.Background())
	done := startSourceRegistrySweeper(ctx, nil, store, newBackgroundJobFailureReporter(listener, owner))
	select {
	case <-store.listEntered:
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("source registry sweep did not start")
	}
	if !listener.EventCountReachesWithin(2, time.Second) {
		cancel()
		<-done
		t.Fatalf("active source error events=%d, want failure and notification", len(listener.Events()))
	}
	if !countReachesWithin(&store.dueCalls, 1, time.Second) {
		cancel()
		<-done
		t.Fatal("active source error did not reach the due stage")
	}
	cancel()
	<-done
	if got := store.dueCalls.Load(); got != 1 {
		t.Fatalf("due-stage calls=%d, want one empty due sweep after active list error", got)
	}
	if got := len(owner.created); got != 1 {
		t.Fatalf("failure Tasks=%d, want one active-context failure Task", got)
	}
}

func TestDependenciesShutdownJoinsConversationBackgroundBeforeStores(t *testing.T) {
	var order []string
	deps := &Dependencies{
		conversationBackgroundStop: func() { order = append(order, "background_stop") },
		conversationArchiveCloser:  orderedConversationCloser{name: "archive_close", order: &order},
		conversationCloser:         orderedConversationCloser{name: "l1_close", order: &order},
	}
	deps.Shutdown()
	want := []string{"background_stop", "archive_close", "l1_close"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("shutdown order=%v, want %v", order, want)
	}
}

type blockingMemoryLifecycleRunner struct {
	started chan struct{}
	calls   atomic.Int64
}

func (r *blockingMemoryLifecycleRunner) RunMemoryLifecycleMaintenance(ctx context.Context, _ l1sqlite.MemoryLifecycleOptions) (*l1sqlite.MemoryLifecycleResult, error) {
	r.calls.Add(1)
	close(r.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

type orderedConversationCloser struct {
	name  string
	order *[]string
}

func (c orderedConversationCloser) Close() error {
	*c.order = append(*c.order, c.name)
	return nil
}

type sourceRegistryShutdownStore struct {
	listEntered chan struct{}
	listOnce    sync.Once
	listErr     error
	dueCalls    atomic.Int64
}

func (s *sourceRegistryShutdownStore) ListSourceRegistryEntries(ctx context.Context, _ bool) ([]l1sqlite.L1SourceRegistryEntry, error) {
	s.listOnce.Do(func() { close(s.listEntered) })
	if s.listErr != nil {
		return nil, s.listErr
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (s *sourceRegistryShutdownStore) DueSourceRegistryEntries(context.Context, time.Time) ([]l1sqlite.L1SourceRegistryEntry, error) {
	s.dueCalls.Add(1)
	return nil, nil
}

func (s *sourceRegistryShutdownStore) SourceTrustScores(context.Context) (map[string]float64, error) {
	return map[string]float64{}, nil
}

func (s *sourceRegistryShutdownStore) StageSourceRegistryFetch(context.Context, string, l1sqlite.L1SourceFetchPayload) (*l1sqlite.L1StagingItem, error) {
	return nil, nil
}

func (s *sourceRegistryShutdownStore) ValidateStagingItem(context.Context, string, l1sqlite.L1StagingValidationPolicy) (*l1sqlite.L1StagingValidationResult, error) {
	return nil, nil
}

func (s *sourceRegistryShutdownStore) PromoteValidatedStagingItemToNews(context.Context, string, string) (*l1sqlite.L1NewsItem, error) {
	return nil, nil
}

func (s *sourceRegistryShutdownStore) PromoteValidatedStagingItemToKnowledge(context.Context, string, string) (*l1sqlite.L1KnowledgeItem, error) {
	return nil, nil
}

func (s *sourceRegistryShutdownStore) MarkSourceRegistryFetched(context.Context, string, time.Time, string, string) error {
	return nil
}

var _ sourcefetcher.RegistryStore = (*sourceRegistryShutdownStore)(nil)
var _ sourcefetcher.RegistrySourceLister = (*sourceRegistryShutdownStore)(nil)
