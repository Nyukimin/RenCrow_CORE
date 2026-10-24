package superagent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestSQLiteStoreAdmittedLeadRecordsReturnBeforePersistenceAndCaptureValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "superagent.db")
	queue, err := persistencequeue.NewPersistenceQueue(4, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewSQLiteStoreWithWriteQueue(path, 3000, queue)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	admission, releaseWriter := blockSuperAgentQueue(t, queue, 3, 1<<20)
	defer func() {
		releaseWriter()
		_ = admission.Release()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = queue.Shutdown(ctx)
	}()
	ctx := persistencequeue.WithAdmission(context.Background(), admission)
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	run := domainsuperagent.AgentRun{RunID: modulecore.NewRunID(), TaskID: modulecore.NewTaskID(), ActorID: "mio", Status: "running", StartedAt: now}
	if err := store.SaveAgentRun(ctx, run); err != nil {
		t.Fatalf("SaveAgentRun() rejected matching admission: %v", err)
	}
	pack := contextPackFixture(modulecore.NewArtifactID(), run.TaskID, run.RunID, modulecore.NewArtifactID(), now)
	if err := store.SaveContextPack(ctx, pack); err != nil {
		t.Fatalf("SaveContextPack() rejected matching admission: %v", err)
	}
	pack.Summary = "mutated after admission"
	pack.IncludedSources[0] = "mutated after admission"

	if _, found, err := store.FindAgentRunByID(context.Background(), string(run.RunID)); err != nil || found {
		t.Fatalf("agent run became durable before the FIFO writer was released: found=%v err=%v", found, err)
	}
	if packs, err := store.ListContextPacks(context.Background(), 10); err != nil || len(packs) != 0 {
		t.Fatalf("context pack became durable before the FIFO writer was released: packs=%+v err=%v", packs, err)
	}

	releaseWriter()
	if err := admission.Barrier(context.Background()); err != nil {
		t.Fatalf("wait for admitted lead records: %v", err)
	}
	storedRun, found, err := store.FindAgentRunByID(context.Background(), string(run.RunID))
	if err != nil || !found || storedRun.Status != "running" {
		t.Fatalf("stored run = %+v found=%v err=%v", storedRun, found, err)
	}
	packs, err := store.ListContextPacks(context.Background(), 10)
	if err != nil || len(packs) != 1 {
		t.Fatalf("ListContextPacks() = %+v, %v", packs, err)
	}
	if packs[0].Summary != "summary" || len(packs[0].IncludedSources) != 1 || packs[0].IncludedSources[0] != "session:s1" {
		t.Fatalf("queued context pack changed after admission: %+v", packs[0])
	}
}

func TestJSONLStoreAdmittedLeadRecordsReturnBeforePersistenceAndCaptureValues(t *testing.T) {
	root := t.TempDir()
	queue, err := persistencequeue.NewPersistenceQueue(4, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewJSONLStoreWithWriteQueue(root, 3000, queue)
	if err != nil {
		t.Fatal(err)
	}

	admission, releaseWriter := blockSuperAgentQueue(t, queue, 3, 1<<20)
	defer func() {
		releaseWriter()
		_ = admission.Release()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = queue.Shutdown(ctx)
	}()
	ctx := persistencequeue.WithAdmission(context.Background(), admission)
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	run := domainsuperagent.AgentRun{RunID: modulecore.NewRunID(), TaskID: modulecore.NewTaskID(), ActorID: "mio", Status: "running", StartedAt: now}
	if err := store.SaveAgentRun(ctx, run); err != nil {
		t.Fatalf("SaveAgentRun() rejected matching admission: %v", err)
	}
	pack := contextPackFixture(modulecore.NewArtifactID(), run.TaskID, run.RunID, modulecore.NewArtifactID(), now)
	if err := store.SaveContextPack(ctx, pack); err != nil {
		t.Fatalf("SaveContextPack() rejected matching admission: %v", err)
	}
	pack.Summary = "mutated after admission"
	pack.IncludedSources[0] = "mutated after admission"

	for _, path := range []string{store.agentRunPath, store.contextPackPath} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("admitted save wrote %s before the FIFO writer was released: stat error=%v", path, err)
		}
	}

	releaseWriter()
	if err := admission.Barrier(context.Background()); err != nil {
		t.Fatalf("wait for admitted lead records: %v", err)
	}
	runs, err := store.ListAgentRuns(context.Background(), 10)
	if err != nil || len(runs) != 1 || runs[0].RunID != run.RunID {
		t.Fatalf("ListAgentRuns() = %+v, %v", runs, err)
	}
	packs, err := store.ListContextPacks(context.Background(), 10)
	if err != nil || len(packs) != 1 {
		t.Fatalf("ListContextPacks() = %+v, %v", packs, err)
	}
	if packs[0].Summary != "summary" || len(packs[0].IncludedSources) != 1 || packs[0].IncludedSources[0] != "session:s1" {
		t.Fatalf("queued context pack changed after admission: %+v", packs[0])
	}
}

func TestSuperAgentStoreRejectsAdmissionFromDifferentQueue(t *testing.T) {
	queue, err := persistencequeue.NewPersistenceQueue(1, 4096)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownPersistenceQueue(t, queue)
	otherQueue, err := persistencequeue.NewPersistenceQueue(1, 4096)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownPersistenceQueue(t, otherQueue)
	store, err := NewSQLiteStoreWithWriteQueue(filepath.Join(t.TempDir(), "superagent.db"), 3000, queue)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	admission, err := persistencequeue.Admit(otherQueue, 1, 4096)
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Release()

	run := domainsuperagent.AgentRun{RunID: modulecore.NewRunID(), TaskID: modulecore.NewTaskID(), ActorID: "mio", Status: "running", StartedAt: time.Now().UTC()}
	err = store.SaveAgentRun(persistencequeue.WithAdmission(context.Background(), admission), run)
	if !errors.Is(err, ErrAdmissionQueueMismatch) {
		t.Fatalf("SaveAgentRun() with admission from a different queue = %v, want ErrAdmissionQueueMismatch", err)
	}
	if _, found, err := store.FindAgentRunByID(context.Background(), string(run.RunID)); err != nil || found {
		t.Fatalf("mismatched admission caused a direct store write: found=%v err=%v", found, err)
	}
}

func TestSuperAgentAdmittedWriteFailsWhenItsReservationCannotHoldPayload(t *testing.T) {
	queue, err := persistencequeue.NewPersistenceQueue(1, 4096)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownPersistenceQueue(t, queue)
	store, err := NewSQLiteStoreWithWriteQueue(filepath.Join(t.TempDir(), "superagent.db"), 3000, queue)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	admission, err := persistencequeue.Admit(queue, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Release()
	run := domainsuperagent.AgentRun{RunID: modulecore.NewRunID(), TaskID: modulecore.NewTaskID(), ActorID: "mio", Status: "running", StartedAt: time.Now().UTC()}
	err = store.SaveAgentRun(persistencequeue.WithAdmission(context.Background(), admission), run)
	if !errors.Is(err, persistencequeue.ErrReservationBytes) {
		t.Fatalf("SaveAgentRun() with too-small reservation = %v, want ErrReservationBytes", err)
	}
	if _, found, err := store.FindAgentRunByID(context.Background(), string(run.RunID)); err != nil || found {
		t.Fatalf("reservation failure caused a direct store write: found=%v err=%v", found, err)
	}
}

func blockSuperAgentQueue(t *testing.T, queue *persistencequeue.Queue[persistencequeue.PersistenceCommand], count int, byteBudget int64) (*persistencequeue.Admission, func()) {
	t.Helper()
	admission, err := persistencequeue.Admit(queue, count, byteBudget)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	continueWrite := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(continueWrite) }) }
	if _, err := admission.Enqueue(persistencequeue.PersistenceCommand{
		Owner:     persistencequeue.OwnerSuperAgent,
		Kind:      "test.blocker",
		SizeBytes: 1,
		Apply: func(context.Context) error {
			close(started)
			<-continueWrite
			return nil
		},
	}); err != nil {
		release()
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		release()
		t.Fatal("queue head did not start")
	}
	return admission, release
}

func shutdownPersistenceQueue(t *testing.T, queue *persistencequeue.Queue[persistencequeue.PersistenceCommand]) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := queue.Shutdown(ctx); err != nil {
		t.Errorf("shutdown persistence queue: %v", err)
	}
}
