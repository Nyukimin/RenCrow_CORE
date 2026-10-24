package aiworkflow

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func newAIWorkflowTestQueue(
	t *testing.T,
	capacity int,
	byteCapacity int64,
	writer func(persistencequeue.PersistenceCommand) error,
) *persistencequeue.Queue[persistencequeue.PersistenceCommand] {
	t.Helper()
	queue, err := persistencequeue.New[persistencequeue.PersistenceCommand](capacity, byteCapacity, func(command persistencequeue.Command[persistencequeue.PersistenceCommand]) error {
		return writer(command.Value)
	})
	if err != nil {
		t.Fatalf("create persistence queue: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = queue.Shutdown(ctx)
	})
	return queue
}

func admitAIWorkflowWrites(t *testing.T, queue *persistencequeue.Queue[persistencequeue.PersistenceCommand], count int) *persistencequeue.Admission {
	t.Helper()
	admission, err := persistencequeue.Admit(queue, count, 16<<10)
	if err != nil {
		t.Fatalf("admit AI Workflow writes: %v", err)
	}
	t.Cleanup(func() { _ = admission.Release() })
	return admission
}

func TestSQLiteStoreAdmittedContextUsageDoesNotWaitForWriterAndSnapshotsValue(t *testing.T) {
	writerStarted := make(chan struct{})
	queue := newAIWorkflowTestQueue(t, 2, 32<<10, func(command persistencequeue.PersistenceCommand) error {
		select {
		case <-writerStarted:
		default:
			close(writerStarted)
		}
		return command.Apply(context.Background())
	})
	path := filepath.Join(t.TempDir(), "ai_workflow.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	unlockDatabase := func() {}
	defer func() {
		unlockDatabase()
		if err := store.Close(); err != nil {
			t.Errorf("close SQLite store: %v", err)
		}
	}()
	if err := store.SetPersistenceQueue(queue); err != nil {
		t.Fatalf("SetPersistenceQueue() error = %v", err)
	}
	if !store.MatchesPersistenceQueue(queue) {
		t.Fatal("MatchesPersistenceQueue() = false after queue configuration")
	}
	lockDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	lockDB.SetMaxOpenConns(1)
	lockConn, err := lockDB.Conn(context.Background())
	if err != nil {
		_ = lockDB.Close()
		t.Fatal(err)
	}
	if _, err := lockConn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		_ = lockConn.Close()
		_ = lockDB.Close()
		t.Fatal(err)
	}
	var unlockOnce sync.Once
	unlockDatabase = func() {
		unlockOnce.Do(func() {
			_, _ = lockConn.ExecContext(context.Background(), "ROLLBACK")
			_ = lockConn.Close()
			_ = lockDB.Close()
		})
	}

	admission := admitAIWorkflowWrites(t, queue, 1)
	ctx := persistencequeue.WithAdmission(context.Background(), admission)
	item := domainai.ContextUsage{EventID: "before", Agent: "Mio", CreatedAt: time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)}
	writeDone := make(chan error, 1)
	go func() { writeDone <- store.SaveContextUsage(ctx, item) }()
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("SaveContextUsage() error = %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("SaveContextUsage() waited for the locked SQLite writer")
	}
	select {
	case <-writerStarted:
	case <-time.After(time.Second):
		t.Fatal("persistence writer did not start")
	}
	if !queue.Status().InFlight {
		t.Fatal("persistence queue is not blocked in its writer while the SQLite lock is held")
	}
	item.EventID = "after"
	unlockDatabase()
	barrierCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := admission.Barrier(barrierCtx); err != nil {
		t.Fatalf("Admission.Barrier() error = %v", err)
	}
	items, err := store.ListContextUsages(context.Background(), 10)
	if err != nil || len(items) != 1 || items[0].EventID != "before" {
		t.Fatalf("saved context usages = %#v, error = %v; want immutable pre-mutation value", items, err)
	}
}

func TestJSONLStoreQueuesAllWriteMethodsForMatchingAdmission(t *testing.T) {
	queue := newAIWorkflowTestQueue(t, 4, 64<<10, func(command persistencequeue.PersistenceCommand) error {
		return command.Apply(context.Background())
	})
	store := NewJSONLStore(t.TempDir())
	if err := store.SetPersistenceQueue(queue); err != nil {
		t.Fatalf("SetPersistenceQueue() error = %v", err)
	}
	if !store.MatchesPersistenceQueue(queue) {
		t.Fatal("MatchesPersistenceQueue() = false after queue configuration")
	}
	admission := admitAIWorkflowWrites(t, queue, 4)
	ctx := persistencequeue.WithAdmission(context.Background(), admission)
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	if err := store.SaveProjectMemoryIndex(ctx, domainai.ProjectMemoryIndex{MemoryID: modulecore.NewMemoryID(), Repo: "repo", FilePath: ".ai/PROJECT_MEMORY.md", MemoryType: "project", UpdatedAt: now}); err != nil {
		t.Fatalf("SaveProjectMemoryIndex() error = %v", err)
	}
	if err := store.SaveWorktreeRegistry(ctx, domainai.WorktreeRegistry{WorktreeID: "wt_1", Repo: "repo", Path: "worktree", Branch: "feature", Status: "active", CreatedAt: now}); err != nil {
		t.Fatalf("SaveWorktreeRegistry() error = %v", err)
	}
	if err := store.SaveCommandRegistry(ctx, domainai.CommandRegistry{CommandName: "/review", FilePath: "commands/review.md", UpdatedAt: now}); err != nil {
		t.Fatalf("SaveCommandRegistry() error = %v", err)
	}
	if err := store.SaveContextUsage(ctx, domainai.ContextUsage{EventID: "ctx_1", Agent: "Mio", CreatedAt: now}); err != nil {
		t.Fatalf("SaveContextUsage() error = %v", err)
	}
	barrierCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := admission.Barrier(barrierCtx); err != nil {
		t.Fatalf("Admission.Barrier() error = %v", err)
	}
	if items, err := store.ListProjectMemoryIndexes(context.Background(), 10); err != nil || len(items) != 1 || items[0].Repo != "repo" {
		t.Fatalf("project memory indexes = %#v, error = %v", items, err)
	}
	if items, err := store.ListWorktreeRegistries(context.Background(), 10); err != nil || len(items) != 1 || items[0].WorktreeID != "wt_1" {
		t.Fatalf("worktree registries = %#v, error = %v", items, err)
	}
	if items, err := store.ListCommandRegistries(context.Background(), 10); err != nil || len(items) != 1 || items[0].CommandName != "/review" {
		t.Fatalf("command registries = %#v, error = %v", items, err)
	}
	if items, err := store.ListContextUsages(context.Background(), 10); err != nil || len(items) != 1 || items[0].EventID != "ctx_1" {
		t.Fatalf("context usages = %#v, error = %v", items, err)
	}
}

func TestJSONLStoreAdmittedContextUsagePreservesFIFOOrder(t *testing.T) {
	queue := newAIWorkflowTestQueue(t, 2, 32<<10, func(command persistencequeue.PersistenceCommand) error {
		return command.Apply(context.Background())
	})
	store := NewJSONLStore(t.TempDir())
	if err := store.SetPersistenceQueue(queue); err != nil {
		t.Fatal(err)
	}
	admission := admitAIWorkflowWrites(t, queue, 2)
	ctx := persistencequeue.WithAdmission(context.Background(), admission)
	now := time.Now().UTC()
	for _, eventID := range []string{"pre", "post"} {
		if err := store.SaveContextUsage(ctx, domainai.ContextUsage{EventID: eventID, Agent: "Mio", CreatedAt: now}); err != nil {
			t.Fatalf("SaveContextUsage(%q) error = %v", eventID, err)
		}
	}
	barrierCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := admission.Barrier(barrierCtx); err != nil {
		t.Fatalf("Admission.Barrier() error = %v", err)
	}
	contents, err := os.ReadFile(store.contextUsagePath)
	if err != nil {
		t.Fatal(err)
	}
	preIndex, postIndex := strings.Index(string(contents), `"event_id":"pre"`), strings.Index(string(contents), `"event_id":"post"`)
	if preIndex < 0 || postIndex < 0 || preIndex >= postIndex {
		t.Fatalf("persisted JSONL order = %s; want pre before post", contents)
	}
}

func TestAIWorkflowAdmissionRejectsMismatchedQueueAndInsufficientReservation(t *testing.T) {
	queue := newAIWorkflowTestQueue(t, 2, 64<<10, func(command persistencequeue.PersistenceCommand) error {
		return command.Apply(context.Background())
	})
	otherQueue := newAIWorkflowTestQueue(t, 2, 64<<10, func(command persistencequeue.PersistenceCommand) error {
		return command.Apply(context.Background())
	})
	store := NewJSONLStore(t.TempDir())
	if err := store.SetPersistenceQueue(queue); err != nil {
		t.Fatal(err)
	}
	wrongAdmission := admitAIWorkflowWrites(t, otherQueue, 1)
	wrongCtx := persistencequeue.WithAdmission(context.Background(), wrongAdmission)
	item := domainai.ContextUsage{EventID: "mismatch", Agent: "Mio", CreatedAt: time.Now().UTC()}
	if err := store.SaveContextUsage(wrongCtx, item); !errors.Is(err, ErrAdmissionQueueMismatch) {
		t.Fatalf("mismatched SaveContextUsage() error = %v, want ErrAdmissionQueueMismatch", err)
	}

	smallAdmission, err := persistencequeue.Admit(queue, 1, 1)
	if err != nil {
		t.Fatalf("Admit(small reservation) error = %v", err)
	}
	defer smallAdmission.Release()
	ctx := persistencequeue.WithAdmission(context.Background(), smallAdmission)
	if err := store.SaveContextUsage(ctx, item); !errors.Is(err, persistencequeue.ErrReservationBytes) {
		t.Fatalf("under-sized SaveContextUsage() error = %v, want ErrReservationBytes", err)
	}
	if status := queue.Status(); status.Pending != 0 {
		t.Fatalf("queue pending after rejected writes = %d, want 0", status.Pending)
	}
}

func TestAIWorkflowAdmissionFailsWhenQueueCapacityIsFull(t *testing.T) {
	queue := newAIWorkflowTestQueue(t, 1, 32<<10, func(command persistencequeue.PersistenceCommand) error {
		return command.Apply(context.Background())
	})
	store := NewJSONLStore(t.TempDir())
	if err := store.SetPersistenceQueue(queue); err != nil {
		t.Fatal(err)
	}

	occupier, err := persistencequeue.Admit(queue, 1, 16<<10)
	if err != nil {
		t.Fatalf("reserve queue capacity: %v", err)
	}
	if _, err := persistencequeue.Admit(queue, 1, 16<<10); !errors.Is(err, persistencequeue.ErrFull) {
		t.Fatalf("admission with full queue capacity error = %v, want ErrFull", err)
	}
	if err := occupier.Release(); err != nil {
		t.Fatalf("release occupied queue capacity: %v", err)
	}

	admission := admitAIWorkflowWrites(t, queue, 1)
	ctx := persistencequeue.WithAdmission(context.Background(), admission)
	if err := store.SaveContextUsage(ctx, domainai.ContextUsage{EventID: "after_capacity", Agent: "Mio", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("SaveContextUsage() after queue capacity became available: %v", err)
	}
	barrierCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := admission.Barrier(barrierCtx); err != nil {
		t.Fatalf("Admission.Barrier() error = %v", err)
	}
	items, err := store.ListContextUsages(context.Background(), 10)
	if err != nil || len(items) != 1 || items[0].EventID != "after_capacity" {
		t.Fatalf("context usages after queue capacity became available = %#v, error = %v", items, err)
	}
}

func TestSQLiteStoreQueueFailureIsReturnedAfterAdmission(t *testing.T) {
	writeFailure := errors.New("disk unavailable")
	queue := newAIWorkflowTestQueue(t, 2, 32<<10, func(persistencequeue.PersistenceCommand) error {
		return writeFailure
	})
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "ai_workflow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SetPersistenceQueue(queue); err != nil {
		t.Fatal(err)
	}
	admission := admitAIWorkflowWrites(t, queue, 2)
	ctx := persistencequeue.WithAdmission(context.Background(), admission)
	if err := store.SaveContextUsage(ctx, domainai.ContextUsage{EventID: "accepted", Agent: "Mio", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("accepted SaveContextUsage() error = %v", err)
	}
	barrierCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := admission.Barrier(barrierCtx); !errors.Is(err, persistencequeue.ErrFailed) || !errors.Is(err, writeFailure) {
		t.Fatalf("Admission.Barrier() error = %v, want queue and writer failure", err)
	}
	if err := store.SaveContextUsage(ctx, domainai.ContextUsage{EventID: "after_failure", Agent: "Mio", CreatedAt: time.Now().UTC()}); !errors.Is(err, persistencequeue.ErrFailed) || !errors.Is(err, writeFailure) {
		t.Fatalf("SaveContextUsage() after queue failure = %v, want queue and writer failure", err)
	}
}

func TestSQLiteStoreSynchronousWriteWaitsOutsideAdmissionFence(t *testing.T) {
	writerStarted := make(chan struct{})
	releaseWriter := make(chan struct{})
	queue := newAIWorkflowTestQueue(t, 3, 64<<10, func(command persistencequeue.PersistenceCommand) error {
		if command.Kind == "sqlite.ai_context_usage.insert_or_replace" {
			select {
			case <-writerStarted:
			default:
				close(writerStarted)
			}
			<-releaseWriter
		}
		return command.Apply(context.Background())
	})
	defer func() {
		select {
		case <-releaseWriter:
		default:
			close(releaseWriter)
		}
	}()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "ai_workflow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SetPersistenceQueue(queue); err != nil {
		t.Fatal(err)
	}
	firstAdmission := admitAIWorkflowWrites(t, queue, 1)
	firstCtx := persistencequeue.WithAdmission(context.Background(), firstAdmission)
	now := time.Now().UTC()
	if err := store.SaveContextUsage(firstCtx, domainai.ContextUsage{EventID: "admitted_1", Agent: "Mio", CreatedAt: now}); err != nil {
		t.Fatalf("first admitted SaveContextUsage() error = %v", err)
	}
	select {
	case <-writerStarted:
	case <-time.After(time.Second):
		t.Fatal("queue writer did not start")
	}

	synchronousDone := make(chan error, 1)
	go func() {
		synchronousDone <- store.SaveContextUsage(context.Background(), domainai.ContextUsage{EventID: "synchronous", Agent: "Mio", CreatedAt: now})
	}()
	secondAdmission := admitAIWorkflowWrites(t, queue, 1)
	secondCtx := persistencequeue.WithAdmission(context.Background(), secondAdmission)
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- store.SaveContextUsage(secondCtx, domainai.ContextUsage{EventID: "admitted_2", Agent: "Mio", CreatedAt: now.Add(time.Second)})
	}()
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatalf("second admitted SaveContextUsage() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("admitted write waited while the synchronous caller was outside the admission fence")
	}
	select {
	case err := <-synchronousDone:
		t.Fatalf("synchronous write returned before earlier queued writes drained: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseWriter)
	barrierCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := firstAdmission.Barrier(barrierCtx); err != nil {
		t.Fatalf("first Admission.Barrier() error = %v", err)
	}
	if err := secondAdmission.Barrier(barrierCtx); err != nil {
		t.Fatalf("second Admission.Barrier() error = %v", err)
	}
	select {
	case err := <-synchronousDone:
		if err != nil {
			t.Fatalf("synchronous SaveContextUsage() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("synchronous write did not complete after queued writes drained")
	}
	var got string
	if err := store.db.QueryRow(`SELECT event_id FROM ai_context_usage ORDER BY rowid DESC LIMIT 1`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(got, "synchronous") {
		t.Fatalf("latest persisted event = %q, want synchronous write after queued writes", got)
	}
}
