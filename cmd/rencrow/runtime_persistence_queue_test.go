package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	domainagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainllm "github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestRuntimePersistenceQueueIsFiniteAndAdmissionReservationIsBounded(t *testing.T) {
	queue, err := newRuntimePersistenceQueue()
	if err != nil {
		t.Fatal(err)
	}
	var admissions []*persistencequeue.Admission
	t.Cleanup(func() {
		for _, admission := range admissions {
			if err := admission.Release(); err != nil {
				t.Errorf("release reservation: %v", err)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := queue.Shutdown(ctx); err != nil {
			t.Errorf("queue shutdown: %v", err)
		}
	})

	maxTurnReservations := runtimePersistenceQueueByteCapacity / runtimeViewerChatAdmissionBytes
	for i := int64(0); i < maxTurnReservations; i++ {
		admission, err := persistencequeue.Admit(queue, runtimeViewerChatAdmissionCommands, runtimeViewerChatAdmissionBytes)
		if err != nil {
			t.Fatalf("admit reservation %d: %v", i, err)
		}
		admissions = append(admissions, admission)
	}
	if _, err := persistencequeue.Admit(queue, runtimeViewerChatAdmissionCommands, runtimeViewerChatAdmissionBytes); !errors.Is(err, persistencequeue.ErrFull) {
		t.Fatalf("admit beyond bounded reservations = %v, want ErrFull", err)
	}
	status := queue.Status()
	wantReserved := int(maxTurnReservations) * runtimeViewerChatAdmissionCommands
	if status.Reserved != wantReserved || status.ReservedBytes != runtimePersistenceQueueByteCapacity {
		t.Fatalf("queue reservation status=(%d,%d), want (%d,%d)", status.Reserved, status.ReservedBytes, wantReserved, runtimePersistenceQueueByteCapacity)
	}
}

func TestRuntimePersistenceQueueHealthAndShutdownRejectNewAdmission(t *testing.T) {
	queue, err := newRuntimePersistenceQueue()
	if err != nil {
		t.Fatal(err)
	}
	workerBlocked := make(chan struct{})
	workerStarted := make(chan struct{})
	if _, err := queue.Enqueue(persistencequeue.PersistenceCommand{
		Owner: persistencequeue.OwnerTask, Kind: "test.block", SizeBytes: 1,
		Apply: func(context.Context) error {
			close(workerStarted)
			<-workerBlocked
			return nil
		},
	}, 1); err != nil {
		t.Fatal(err)
	}
	select {
	case <-workerStarted:
	case <-time.After(time.Second):
		t.Fatal("queue worker did not enter the test blocker")
	}

	relay := &runtimeChatBinderStub{durableSeq: 42}
	result := newRuntimePersistenceQueueCheck(queue, relay).Run(context.Background())
	if result.Name != "runtime_persistence_queue" || result.Status != "ok" {
		t.Fatalf("queue health = %+v", result)
	}
	for key, want := range map[string]any{
		"accepting": true, "pending_commands": 1, "in_flight": true,
		"durable_event_seq": modulecore.EventSeq(42), "failed": false,
	} {
		if got := result.Details[key]; got != want {
			t.Fatalf("health detail %q=%v want %v", key, got, want)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := queue.Shutdown(shutdownCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown with blocked writer = %v, want deadline exceeded", err)
	}
	if _, err := persistencequeue.Admit(queue, 1, 1); !errors.Is(err, persistencequeue.ErrClosed) {
		t.Fatalf("admit after shutdown = %v, want ErrClosed", err)
	}
	close(workerBlocked)
	shutdownCtx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if err := queue.Shutdown(shutdownCtx2); err != nil {
		t.Fatalf("drain queue after releasing worker: %v", err)
	}
}

func TestRuntimeTaskOwnerRequiresLocalIndexedStoreForRAMAdmission(t *testing.T) {
	for _, tc := range []struct {
		name  string
		index bool
		want  bool
	}{
		{name: "indexed", index: true, want: true},
		{name: "unindexed", index: false, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := taskpersistence.NewJSONLStoreWithOptions(t.TempDir(), taskpersistence.OpenOptions{Index: tc.index})
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if got := runtimeTaskStoreSupportsRAMAdmission(store); got != tc.want {
				t.Fatalf("runtimeTaskStoreSupportsRAMAdmission()=%t want=%t", got, tc.want)
			}
		})
	}
	if runtimeTaskStoreSupportsRAMAdmission(nil) {
		t.Fatal("nil/remote Task owner unexpectedly supports local RAM admission")
	}
}

func TestRuntimeCombinedChatAdmissionRequiresTheActualOneGenerationRuntime(t *testing.T) {
	cfg := &config.Config{
		TaskStore:   config.TaskStoreConfig{Index: config.TaskStoreIndexConfig{Enabled: true}},
		ActionStore: config.ActionStoreConfig{Index: config.ActionStoreIndexConfig{Enabled: true}},
	}
	engine := runtimeCombinedTurnEngineStub{}
	classifier := runtimeRouteReplyClassifierStub{}
	for _, tc := range []struct {
		name          string
		cfg           *config.Config
		classifier    any
		engine        any
		chatProvider  bool
		distributed   bool
		wantAvailable bool
	}{
		{name: "configured local runtime", cfg: cfg, classifier: classifier, engine: engine, chatProvider: true, wantAvailable: true},
		{name: "missing provider", cfg: cfg, classifier: classifier, engine: engine, wantAvailable: false},
		{name: "legacy classifier", cfg: cfg, classifier: struct{}{}, engine: engine, chatProvider: true, wantAvailable: false},
		{name: "legacy conversation engine", cfg: cfg, classifier: classifier, engine: struct{}{}, chatProvider: true, wantAvailable: false},
		{name: "speech plan enabled", cfg: &config.Config{TTS: config.TTSConfig{SpeechPlanEnabled: true}}, classifier: classifier, engine: engine, chatProvider: true, wantAvailable: false},
		{name: "distributed route", cfg: cfg, classifier: classifier, engine: engine, chatProvider: true, distributed: true, wantAvailable: false},
		{name: "Task index disabled", cfg: &config.Config{ActionStore: cfg.ActionStore}, classifier: classifier, engine: engine, chatProvider: true, wantAvailable: false},
		{name: "Action index disabled", cfg: &config.Config{TaskStore: cfg.TaskStore}, classifier: classifier, engine: engine, chatProvider: true, wantAvailable: false},
		{name: "missing config", classifier: classifier, engine: engine, chatProvider: true, wantAvailable: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := runtimeCombinedChatAdmissionEnabled(tc.cfg, tc.classifier, tc.engine, tc.chatProvider, tc.distributed)
			if got != tc.wantAvailable {
				t.Fatalf("runtimeCombinedChatAdmissionEnabled()=%t want=%t", got, tc.wantAvailable)
			}
		})
	}
}

func TestRuntimeConfiguredOwnerAdmissionRequiresAQueueBoundStore(t *testing.T) {
	queue, err := newRuntimePersistenceQueue()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := queue.Shutdown(ctx); err != nil {
			t.Errorf("queue shutdown: %v", err)
		}
	})
	otherQueue, err := newRuntimePersistenceQueue()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := otherQueue.Shutdown(ctx); err != nil {
			t.Errorf("other queue shutdown: %v", err)
		}
	})
	for _, tc := range []struct {
		name    string
		enabled bool
		owner   any
		queue   *persistencequeue.Queue[persistencequeue.PersistenceCommand]
		want    bool
	}{
		{name: "disabled owner", enabled: false, want: true},
		{name: "enabled owner without queue capability", enabled: true, owner: struct{}{}, queue: queue, want: false},
		{name: "enabled owner without queue", enabled: true, owner: runtimeQueueBindingStub{queue: queue}, want: false},
		{name: "enabled owner bound to another queue", enabled: true, owner: &runtimeQueueBindingStub{queue: otherQueue}, queue: queue, want: false},
		{name: "enabled owner bound to shared queue", enabled: true, owner: &runtimeQueueBindingStub{queue: queue}, queue: queue, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := runtimeConfiguredOwnerMatchesQueue(tc.enabled, tc.owner, tc.queue); got != tc.want {
				t.Fatalf("runtimeConfiguredOwnerMatchesQueue()=%t want=%t", got, tc.want)
			}
		})
	}
}

type runtimeQueueBindingStub struct {
	queue *persistencequeue.Queue[persistencequeue.PersistenceCommand]
}

func (s *runtimeQueueBindingStub) SetPersistenceQueue(queue *persistencequeue.Queue[persistencequeue.PersistenceCommand]) error {
	if s == nil || queue == nil {
		return persistencequeue.ErrNilPersistenceQueue
	}
	if s.queue != nil && s.queue != queue {
		return errors.New("queue binding mismatch")
	}
	s.queue = queue
	return nil
}

func (s *runtimeQueueBindingStub) MatchesPersistenceQueue(queue *persistencequeue.Queue[persistencequeue.PersistenceCommand]) bool {
	return s.queue != nil && s.queue == queue
}

func TestRuntimeOwnerQueueBindingUsesSetterAndVerifiesIdentity(t *testing.T) {
	queue, err := newRuntimePersistenceQueue()
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownRuntimeQueue(t, queue)
	otherQueue, err := newRuntimePersistenceQueue()
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownRuntimeQueue(t, otherQueue)
	owner := &runtimeQueueBindingStub{}
	if err := bindRuntimeOwnerPersistenceQueue(owner, queue); err != nil {
		t.Fatalf("bindRuntimeOwnerPersistenceQueue(): %v", err)
	}
	if !runtimeConfiguredOwnerMatchesQueue(true, owner, queue) {
		t.Fatal("runtime owner did not confirm the shared queue after binding")
	}
	foreign := &runtimeQueueBindingStub{queue: otherQueue}
	if err := bindRuntimeOwnerPersistenceQueue(foreign, queue); err == nil {
		t.Fatal("binding a foreign queue was accepted")
	}
	if err := bindRuntimeOwnerPersistenceQueue(struct{}{}, queue); err == nil {
		t.Fatal("owner without a queue API was accepted")
	}
}

func TestRuntimeLegacyRAMAdmissionReservesBeforeBindingWithoutWaitingForWorker(t *testing.T) {
	queue, err := newRuntimePersistenceQueue()
	if err != nil {
		t.Fatal(err)
	}
	workerBlocked := make(chan struct{})
	workerStarted := make(chan struct{})
	if _, err := queue.Enqueue(persistencequeue.PersistenceCommand{
		Owner: persistencequeue.OwnerTask, Kind: "test.block", SizeBytes: 1,
		Apply: func(context.Context) error {
			close(workerStarted)
			<-workerBlocked
			return nil
		},
	}, 1); err != nil {
		t.Fatal(err)
	}
	select {
	case <-workerStarted:
	case <-time.After(time.Second):
		t.Fatal("queue worker did not enter the test blocker")
	}

	store, err := taskpersistence.NewJSONLStoreWithOptions(t.TempDir(), taskpersistence.OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	relay := &runtimeChatBinderStub{}
	identity := viewer.LegacyPreSessionAdmissionIdentity{
		TraceID: modulecore.NewTraceID(), Recipient: "mio", OperationSource: "RenCrow_CMD",
	}
	type result struct {
		ctx     context.Context
		release func()
		err     error
	}
	completed := make(chan result, 1)
	go func() {
		ctx, release, err := admitRuntimeLegacyViewerChat(context.Background(), queue, store, true, relay, identity)
		completed <- result{ctx: ctx, release: release, err: err}
	}()
	var admitted result
	select {
	case admitted = <-completed:
	case <-time.After(time.Second):
		t.Fatal("RAM admission waited for the blocked persistence worker")
	}
	if admitted.err != nil {
		t.Fatalf("admitRuntimeLegacyViewerChat() error = %v", admitted.err)
	}
	if !relay.called || relay.traceID != identity.TraceID {
		t.Fatalf("relay BindChat called=%t trace=%q, want true and %q", relay.called, relay.traceID, identity.TraceID)
	}
	if got, ok := persistencequeue.AdmissionFromContext(admitted.ctx); !ok || !got.MatchesQueue(queue) {
		t.Fatal("returned context did not carry an Admission from the shared queue")
	}
	if admitted.release == nil {
		t.Fatal("admission release was nil")
	}
	admitted.release()
	if admission, ok := persistencequeue.AdmissionFromContext(admitted.ctx); ok {
		if err := admission.Release(); err != nil {
			t.Fatalf("release unused admission capacity: %v", err)
		}
	}
	close(workerBlocked)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := queue.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("queue shutdown: %v", err)
	}
}

func TestRuntimeLegacyRAMAdmissionReleasesReservationAfterEveryCompletedTurn(t *testing.T) {
	queue, err := newRuntimePersistenceQueue()
	if err != nil {
		t.Fatal(err)
	}
	var admissions []*persistencequeue.Admission
	defer func() {
		for _, admission := range admissions {
			_ = admission.Release()
		}
		shutdownRuntimeQueue(t, queue)
	}()
	store, err := taskpersistence.NewJSONLStoreWithOptions(t.TempDir(), taskpersistence.OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	relay := &runtimeChatBinderStub{}
	for turn := 0; turn < 16; turn++ {
		identity := viewer.LegacyPreSessionAdmissionIdentity{
			TraceID: modulecore.NewTraceID(), Recipient: "mio", OperationSource: "RenCrow_CMD",
		}
		ctx, release, err := admitRuntimeLegacyViewerChat(context.Background(), queue, store, true, relay, identity)
		if err != nil {
			t.Fatalf("turn %d admission: %v", turn, err)
		}
		admission, ok := persistencequeue.AdmissionFromContext(ctx)
		if !ok {
			t.Fatalf("turn %d returned no queue admission", turn)
		}
		admissions = append(admissions, admission)
		release()
		status := queue.Status()
		if status.Reserved != 0 || status.ReservedBytes != 0 {
			t.Fatalf("turn %d left queue capacity reserved: commands=%d bytes=%d", turn, status.Reserved, status.ReservedBytes)
		}
	}
}

func TestRuntimeLegacyRAMAdmissionRejectsInvalidScopeOrUnindexedTaskBeforeBinding(t *testing.T) {
	queue, err := newRuntimePersistenceQueue()
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownRuntimeQueue(t, queue)
	store, err := taskpersistence.NewJSONLStoreWithOptions(t.TempDir(), taskpersistence.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	relay := &runtimeChatBinderStub{}
	for _, identity := range []viewer.LegacyPreSessionAdmissionIdentity{
		{TraceID: modulecore.NewTraceID(), Recipient: "shiro", OperationSource: "RenCrow_CMD"},
		{TraceID: modulecore.NewTraceID(), Recipient: "mio", OperationSource: "RenCrow_VIEWER"},
		{TraceID: modulecore.NewTraceID(), Recipient: "mio", OperationSource: "RenCrow_CMD"},
	} {
		if _, _, err := admitRuntimeLegacyViewerChat(context.Background(), queue, store, true, relay, identity); err == nil {
			t.Fatalf("admit out-of-scope or unindexed identity %+v succeeded", identity)
		}
	}
	if relay.called {
		t.Fatal("relay BindChat ran for an invalid scope or unindexed Task store")
	}
	status := queue.Status()
	if status.Reserved != 0 || status.Pending != 0 {
		t.Fatalf("failed admission leaked queue capacity: %+v", status)
	}
}

func TestRuntimeLegacyRAMAdmissionFailsClosedWhenAnOwnerIsNotQueueBound(t *testing.T) {
	queue, err := newRuntimePersistenceQueue()
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownRuntimeQueue(t, queue)
	store, err := taskpersistence.NewJSONLStoreWithOptions(t.TempDir(), taskpersistence.OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	relay := &runtimeChatBinderStub{}
	identity := viewer.LegacyPreSessionAdmissionIdentity{
		TraceID: modulecore.NewTraceID(), Recipient: "mio", OperationSource: "RenCrow_CMD",
	}
	if _, _, err := admitRuntimeLegacyViewerChat(context.Background(), queue, store, false, relay, identity); err == nil {
		t.Fatal("admission succeeded while a configured persistence owner was not queue-bound")
	}
	if relay.called {
		t.Fatal("relay BindChat ran before queue-bound owner availability was confirmed")
	}
	status := queue.Status()
	if status.Reserved != 0 || status.Pending != 0 {
		t.Fatalf("failed admission leaked queue capacity: %+v", status)
	}
}

type runtimeChatBinderStub struct {
	called     bool
	traceID    modulecore.TraceID
	ctx        context.Context
	durableSeq modulecore.EventSeq
}

func (s *runtimeChatBinderStub) BindChat(traceID modulecore.TraceID, ctx context.Context) (func(), error) {
	s.called = true
	s.traceID = traceID
	s.ctx = ctx
	return func() {}, nil
}

func (s *runtimeChatBinderStub) MarkChatSynchronous(traceID modulecore.TraceID) error {
	if !s.called || s.traceID != traceID {
		return errors.New("trace ID does not match bound chat")
	}
	return nil
}

func (s *runtimeChatBinderStub) DurableEventSeq() modulecore.EventSeq { return s.durableSeq }

type runtimeRouteReplyClassifierStub struct{}

func (runtimeRouteReplyClassifierStub) ClassifyWithChatReply(context.Context, domainllm.GenerateRequest) (domainagent.RouteReplyClassification, error) {
	return domainagent.RouteReplyClassification{}, nil
}

type runtimeCombinedTurnEngineStub struct{}

func (runtimeCombinedTurnEngineStub) CommitConversationTurn(context.Context, domainconversation.ConversationTurnRequest) (domainconversation.ConversationTurnResult, error) {
	return domainconversation.ConversationTurnResult{}, nil
}

var _ domaintask.Store = (*taskpersistence.JSONLStore)(nil)

func shutdownRuntimeQueue(t *testing.T, queue *persistencequeue.Queue[persistencequeue.PersistenceCommand]) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := queue.Shutdown(ctx); err != nil {
		t.Fatalf("queue shutdown: %v", err)
	}
}
