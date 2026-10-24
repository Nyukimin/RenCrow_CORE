package session

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func newCanonicalRepositoryTestSession(t *testing.T) *session.Session {
	t.Helper()
	address, err := conversation.NewChannelAddress("line", "U123")
	if err != nil {
		t.Fatal(err)
	}
	value, err := session.NewCanonicalSession(modulecore.NewSessionID(), "2026-03-01", address, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func newRepositoryTurnInputForTest(t *testing.T, sess *session.Session, message string) conversation.TurnInput {
	t.Helper()
	input, err := conversation.NewTurnInput(modulecore.NewTaskID(), message, sess.ChannelAddress())
	if err != nil {
		t.Fatal(err)
	}
	return input.WithSessionID(sess.ID())
}

func newBlockedPersistenceQueue(t *testing.T, capacity int, byteCapacity int64) (*persistencequeue.Queue[persistencequeue.PersistenceCommand], func()) {
	t.Helper()
	queue, err := persistencequeue.NewPersistenceQueue(capacity, byteCapacity)
	if err != nil {
		t.Fatalf("NewPersistenceQueue: %v", err)
	}
	blocker, err := persistencequeue.Admit(queue, 1, 1)
	if err != nil {
		t.Fatalf("Admit blocker: %v", err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	if _, err := blocker.Enqueue(persistencequeue.PersistenceCommand{
		Owner:     persistencequeue.OwnerSession,
		Kind:      "test.block",
		SizeBytes: 1,
		Apply: func(context.Context) error {
			close(started)
			<-release
			return nil
		},
	}); err != nil {
		t.Fatalf("enqueue blocker: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("persistence worker did not enter blocker")
	}
	if status := queue.Status(); status.Failure != nil {
		t.Fatalf("persistence worker failed inside blocker: %v", status.Failure)
	}
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(func() {
		unblock()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = queue.Shutdown(ctx)
	})
	return queue, unblock
}

func admitSessionWrites(t *testing.T, queue *persistencequeue.Queue[persistencequeue.PersistenceCommand], count int, byteBudget int64) (context.Context, *persistencequeue.Admission) {
	t.Helper()
	admission, err := persistencequeue.Admit(queue, count, byteBudget)
	if err != nil {
		t.Fatalf("Admit session writes: %v", err)
	}
	return persistencequeue.WithAdmission(context.Background(), admission), admission
}

func newSessionPersistenceQueue(t *testing.T, capacity int, byteCapacity int64) *persistencequeue.Queue[persistencequeue.PersistenceCommand] {
	t.Helper()
	queue, err := persistencequeue.NewPersistenceQueue(capacity, byteCapacity)
	if err != nil {
		t.Fatalf("NewPersistenceQueue: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := queue.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown persistence queue: %v", err)
		}
	})
	return queue
}

func blockNextSessionWrite(t *testing.T, repo *JSONSessionRepository) (<-chan struct{}, func()) {
	t.Helper()
	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	repo.beforeWriteSessionBytes = func(context.Context, string, []byte) error {
		startedOnce.Do(func() {
			close(started)
			<-release
		})
		return nil
	}
	t.Cleanup(unblock)
	return started, unblock
}

func TestJSONSessionRepositoryAdmittedSessionAndTurnWritesDoNotWaitForBlockedDiskWrite(t *testing.T) {
	repo := NewJSONSessionRepository(t.TempDir())
	if err := repo.WarmCanonicalIndex(context.Background()); err != nil {
		t.Fatalf("WarmCanonicalIndex: %v", err)
	}
	writeStarted, unblock := blockNextSessionWrite(t, repo)
	defer unblock()
	queue := newSessionPersistenceQueue(t, 4, 4<<20)
	ctx, admission := admitSessionWrites(t, queue, 3, 3<<20)
	t.Cleanup(func() { _ = admission.Release() })

	firstAddress, err := conversation.NewChannelAddress("line", "blocked-writer-first")
	if err != nil {
		t.Fatal(err)
	}
	first, created, err := repo.LoadOrCreateCanonical(ctx, "2026-10-24", firstAddress, time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC))
	if err != nil || !created {
		t.Fatalf("first admitted LoadOrCreateCanonical=(%v,%t,%v), want a new session", first, created, err)
	}
	select {
	case <-writeStarted:
	case <-time.After(time.Second):
		t.Fatal("persistence worker did not enter the blocked filesystem write")
	}

	secondAddress, err := conversation.NewChannelAddress("line", "blocked-writer-second")
	if err != nil {
		t.Fatal(err)
	}
	type createResult struct {
		value   *session.Session
		created bool
		err     error
	}
	createDone := make(chan createResult, 1)
	go func() {
		value, created, createErr := repo.LoadOrCreateCanonical(ctx, "2026-10-24", secondAddress, time.Date(2026, 10, 24, 1, 0, 0, 0, time.UTC))
		createDone <- createResult{value: value, created: created, err: createErr}
	}()
	var second createResult
	select {
	case second = <-createDone:
	case <-time.After(time.Second):
		t.Fatal("admitted next session waited for the blocked filesystem write")
	}
	if second.err != nil || second.value == nil || !second.created {
		t.Fatalf("second admitted LoadOrCreateCanonical=(%v,%t,%v), want a new session", second.value, second.created, second.err)
	}

	second.value.AddTurnInput(newRepositoryTurnInputForTest(t, second.value, "accepted while the previous session is syncing"))
	saveDone := make(chan error, 1)
	go func() { saveDone <- repo.Save(ctx, second.value) }()
	select {
	case err := <-saveDone:
		if err != nil {
			t.Fatalf("admitted turn Save while disk write was blocked: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("admitted turn Save waited for the blocked filesystem write")
	}

	visible, err := repo.Load(context.Background(), second.value.ID())
	if err != nil || visible.HistoryCount() != 1 {
		t.Fatalf("RAM-visible accepted turn=(%v,%v), want one turn", visible, err)
	}
	unblock()
	barrierCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := admission.Barrier(barrierCtx); err != nil {
		t.Fatalf("Barrier after blocked filesystem write: %v", err)
	}
	for _, want := range []*session.Session{first, second.value} {
		if _, err := os.Stat(repo.getFilePath(want.ID())); err != nil {
			t.Fatalf("accepted session %q was not persisted: %v", want.ID(), err)
		}
	}
	persisted, err := repo.Load(context.Background(), second.value.ID())
	if err != nil || persisted.HistoryCount() != 1 {
		t.Fatalf("persisted accepted turn=(%v,%v), want one turn", persisted, err)
	}
	canonical, found, err := repo.LoadCanonical(context.Background(), "2026-10-24", secondAddress)
	if err != nil || !found || canonical.ID() != second.value.ID() {
		t.Fatalf("canonical lookup after accepted writes=(%v,%t,%v), want persisted second session", canonical, found, err)
	}
}

func TestJSONSessionRepositoryAdmittedWritesFailFastWhileSynchronousWriteHoldsLock(t *testing.T) {
	for _, operation := range []string{"Save", "LoadOrCreateCanonical"} {
		t.Run(operation, func(t *testing.T) {
			repo := NewJSONSessionRepository(t.TempDir())
			writeStarted, unblock := blockNextSessionWrite(t, repo)
			defer unblock()
			queue := newSessionPersistenceQueue(t, 2, 2<<20)
			ctx, admission := admitSessionWrites(t, queue, 1, 1<<20)
			t.Cleanup(func() { _ = admission.Release() })

			synchronousDone := make(chan error, 1)
			synchronousSession := newCanonicalRepositoryTestSession(t)
			admittedSession := newCanonicalRepositoryTestSession(t)
			go func() { synchronousDone <- repo.Save(context.Background(), synchronousSession) }()
			select {
			case <-writeStarted:
			case <-time.After(time.Second):
				t.Fatal("synchronous Save did not enter the blocked filesystem write")
			}

			admittedDone := make(chan error, 1)
			go func() {
				if operation == "Save" {
					admittedDone <- repo.Save(ctx, admittedSession)
					return
				}
				address, err := conversation.NewChannelAddress("line", "blocked-sync-admission")
				if err != nil {
					admittedDone <- err
					return
				}
				_, _, err = repo.LoadOrCreateCanonical(ctx, "2026-10-24", address, time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC))
				admittedDone <- err
			}()
			select {
			case err := <-admittedDone:
				if err == nil {
					t.Fatal("admitted write succeeded while a synchronous writer held the repository lock")
				}
			case <-time.After(time.Second):
				unblock()
				select {
				case <-synchronousDone:
				case <-time.After(3 * time.Second):
					t.Fatal("synchronous Save did not finish after releasing the blocked write")
				}
				select {
				case <-admittedDone:
				case <-time.After(3 * time.Second):
					t.Fatal("admitted write did not finish after releasing the synchronous write")
				}
				t.Fatal("admitted write waited for the blocked synchronous filesystem write")
			}
			unblock()
			if err := <-synchronousDone; err != nil {
				t.Fatalf("synchronous Save after release: %v", err)
			}
		})
	}
}

func TestJSONSessionRepositorySynchronousSaveWaitsForEarlierAdmittedSameSessionWrite(t *testing.T) {
	repo := NewJSONSessionRepository(t.TempDir())
	queue, unblockQueue := newBlockedPersistenceQueue(t, 3, 3<<20)
	defer unblockQueue()
	ctx, admission := admitSessionWrites(t, queue, 2, 2<<20)
	t.Cleanup(func() { _ = admission.Release() })
	laterWriteStarted := make(chan struct{})
	releaseLaterWrite := make(chan struct{})
	var releaseLaterWriteOnce sync.Once
	allowLaterWrite := func() { releaseLaterWriteOnce.Do(func() { close(releaseLaterWrite) }) }
	t.Cleanup(allowLaterWrite)
	syncWriteStarted := make(chan struct{})
	releaseSyncWrite := make(chan struct{})
	var syncWriteStartedOnce, releaseSyncWriteOnce sync.Once
	allowSyncWrite := func() { releaseSyncWriteOnce.Do(func() { close(releaseSyncWrite) }) }
	t.Cleanup(allowSyncWrite)
	repo.beforeWriteSessionBytes = func(_ context.Context, _ string, data []byte) error {
		var snapshot struct {
			History []json.RawMessage `json:"history"`
		}
		if err := json.Unmarshal(data, &snapshot); err != nil {
			return err
		}
		if len(snapshot.History) == 2 {
			syncWriteStartedOnce.Do(func() { close(syncWriteStarted) })
			<-releaseSyncWrite
		}
		return nil
	}

	value := newCanonicalRepositoryTestSession(t)
	value.AddTurnInput(newRepositoryTurnInputForTest(t, value, "admitted snapshot A"))
	if err := repo.Save(ctx, value); err != nil {
		t.Fatalf("admitted Save(A): %v", err)
	}
	if _, err := admission.Enqueue(persistencequeue.PersistenceCommand{
		Owner:     persistencequeue.OwnerTask,
		Kind:      "test.later-unrelated-write",
		SizeBytes: 1,
		Apply: func(context.Context) error {
			close(laterWriteStarted)
			<-releaseLaterWrite
			return nil
		},
	}); err != nil {
		t.Fatalf("enqueue later unrelated write: %v", err)
	}
	value.AddTurnInput(newRepositoryTurnInputForTest(t, value, "synchronous snapshot B"))

	syncSaveDone := make(chan error, 1)
	go func() { syncSaveDone <- repo.Save(context.Background(), value) }()
	var syncSaveErr error
	syncSaveFinished := false
	returnedBeforeQueueReleased := false
	select {
	case <-syncWriteStarted:
		returnedBeforeQueueReleased = true
	case syncSaveErr = <-syncSaveDone:
		syncSaveFinished = true
		returnedBeforeQueueReleased = true
	case <-time.After(100 * time.Millisecond):
	}
	if returnedBeforeQueueReleased {
		allowSyncWrite()
		if !syncSaveFinished {
			select {
			case syncSaveErr = <-syncSaveDone:
				syncSaveFinished = true
			case <-time.After(time.Second):
				t.Fatal("synchronous Save(B) did not finish after releasing its write")
			}
		}
	}
	unblockQueue()
	select {
	case <-laterWriteStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("later unrelated queue command did not start after admitted Save(A)")
	}
	if !returnedBeforeQueueReleased {
		select {
		case <-syncWriteStarted:
			allowSyncWrite()
		case syncSaveErr = <-syncSaveDone:
			syncSaveFinished = true
			allowSyncWrite()
		case <-time.After(3 * time.Second):
			t.Fatal("synchronous Save(B) did not continue after admitted Save(A) became durable")
		}
	}
	if !syncSaveFinished {
		select {
		case syncSaveErr = <-syncSaveDone:
			syncSaveFinished = true
		case <-time.After(3 * time.Second):
			t.Fatal("synchronous Save(B) did not finish after its write was released")
		}
	}
	allowLaterWrite()
	if syncSaveErr != nil {
		t.Fatalf("synchronous Save(B): %v", syncSaveErr)
	}

	barrierCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := admission.Barrier(barrierCtx); err != nil {
		t.Fatalf("Barrier after synchronous Save(B): %v", err)
	}
	visible, err := repo.Load(context.Background(), value.ID())
	if err != nil || visible.HistoryCount() != 2 {
		t.Errorf("repository Load after queue drain=(%v,%v), want synchronous snapshot B with two turns", visible, err)
	}
	fresh := NewJSONSessionRepository(repo.baseDir)
	persisted, err := fresh.Load(context.Background(), value.ID())
	if err != nil || persisted.HistoryCount() != 2 {
		t.Errorf("fresh repository Load after queue drain=(%v,%v), want synchronous snapshot B with two turns", persisted, err)
	}
	if returnedBeforeQueueReleased {
		t.Error("synchronous Save(B) returned before the earlier admitted Save(A) became durable")
	}
}

func TestJSONSessionRepositoryAdmittedCanonicalCreateIsVisibleBeforePersistence(t *testing.T) {
	dir := t.TempDir()
	repo := NewJSONSessionRepository(dir)
	queue, unblock := newBlockedPersistenceQueue(t, 4, 2<<20)
	ctx, admission := admitSessionWrites(t, queue, 1, 1<<20)
	address, err := conversation.NewChannelAddress("line", "accepted-user")
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	type createResult struct {
		value   *session.Session
		created bool
		err     error
	}
	resultCh := make(chan createResult, 1)
	go func() {
		value, created, createErr := repo.LoadOrCreateCanonical(ctx, "2026-10-20", address, createdAt)
		resultCh <- createResult{value: value, created: created, err: createErr}
	}()
	var result createResult
	select {
	case result = <-resultCh:
	case <-time.After(time.Second):
		t.Fatal("admitted canonical create waited for the blocked persistence worker")
	}
	if result.err != nil || result.value == nil || !result.created {
		t.Fatalf("admitted LoadOrCreateCanonical=(%v,%t,%v), want created session", result.value, result.created, result.err)
	}
	if _, err := os.Stat(repo.getFilePath(result.value.ID())); !os.IsNotExist(err) {
		t.Fatalf("session file before worker release: stat err=%v, want not-exist", err)
	}

	loaded, err := repo.Load(context.Background(), result.value.ID())
	if err != nil || loaded.ID() != result.value.ID() {
		t.Fatalf("Load accepted canonical session=(%v,%v), want %q", loaded, err, result.value.ID())
	}
	exists, err := repo.Exists(context.Background(), result.value.ID())
	if err != nil || !exists {
		t.Fatalf("Exists accepted canonical session=(%t,%v), want true", exists, err)
	}
	canonical, intent, found, err := repo.LoadCanonicalWithCreationIntent(context.Background(), "2026-10-20", address)
	if err != nil || !found || canonical.ID() != result.value.ID() || intent != nil {
		t.Fatalf("LoadCanonicalWithCreationIntent=(%v,%+v,%t,%v), want accepted legacy session", canonical, intent, found, err)
	}
	second, created, err := repo.LoadOrCreateCanonical(context.Background(), "2026-10-20", address, createdAt.Add(time.Hour))
	if err != nil || created || second.ID() != result.value.ID() {
		t.Fatalf("repeated admitted canonical lookup=(%v,%t,%v), want same session with created=false", second, created, err)
	}
	if _, _, err := repo.ReserveCanonicalV2(context.Background(), "2026-10-20", address, "owner", createdAt); err == nil {
		t.Fatal("ReserveCanonicalV2 promoted an admitted legacy session that was only in the RAM overlay")
	}

	unblock()
	if err := admission.Barrier(context.Background()); err != nil {
		t.Fatalf("Barrier after admitted create: %v", err)
	}
	if _, err := os.Stat(repo.getFilePath(result.value.ID())); err != nil {
		t.Fatalf("session file after worker release: %v", err)
	}
}

func TestJSONSessionRepositoryAdmittedConcurrentSavesKeepLatestSnapshot(t *testing.T) {
	dir := t.TempDir()
	repo := NewJSONSessionRepository(dir)
	queue, unblock := newBlockedPersistenceQueue(t, 5, 2<<20)
	ctx, admission := admitSessionWrites(t, queue, 2, 1<<20)
	base := newCanonicalRepositoryTestSession(t)
	first, err := session.NewCanonicalSession(modulecore.SessionID(base.ID()), base.LogicalDate(), base.ChannelAddress(), base.CreatedAt())
	if err != nil {
		t.Fatal(err)
	}
	first.SetMemory("version", "first")
	second, err := session.NewCanonicalSession(modulecore.SessionID(base.ID()), base.LogicalDate(), base.ChannelAddress(), base.CreatedAt())
	if err != nil {
		t.Fatal(err)
	}
	second.SetMemory("version", "second")
	values := []*session.Session{first, second}
	errCh := make(chan error, len(values))
	for _, value := range values {
		value := value
		go func() {
			errCh <- repo.Save(ctx, value)
		}()
	}
	for range values {
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("concurrent admitted Save: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("admitted Save waited for the blocked persistence worker")
		}
	}
	first.SetMemory("version", "mutated after Save returned")
	second.SetMemory("version", "mutated after Save returned")
	if _, err := os.Stat(repo.getFilePath(base.ID())); !os.IsNotExist(err) {
		t.Fatalf("session file before worker release: stat err=%v, want not-exist", err)
	}
	visible, err := repo.Load(context.Background(), base.ID())
	if err != nil {
		t.Fatalf("Load latest accepted Save: %v", err)
	}
	wantVersion, ok := visible.GetMemory("version")
	if !ok || (wantVersion != "first" && wantVersion != "second") {
		t.Fatalf("visible version=%v, want one accepted snapshot", wantVersion)
	}

	unblock()
	if err := admission.Barrier(context.Background()); err != nil {
		t.Fatalf("Barrier after concurrent Saves: %v", err)
	}
	persisted, err := repo.Load(context.Background(), base.ID())
	if err != nil {
		t.Fatalf("Load persisted latest Save: %v", err)
	}
	gotVersion, ok := persisted.GetMemory("version")
	if !ok || gotVersion != wantVersion {
		t.Fatalf("persisted version=%v, want latest accepted version %v", gotVersion, wantVersion)
	}
}

func TestJSONSessionRepositoryAdmissionBudgetFailureDoesNotExposeMutation(t *testing.T) {
	dir := t.TempDir()
	repo := NewJSONSessionRepository(dir)
	queue, err := persistencequeue.NewPersistenceQueue(2, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = queue.Shutdown(ctx)
	})
	ctx, admission := admitSessionWrites(t, queue, 2, 1)
	sess := newCanonicalRepositoryTestSession(t)
	if err := repo.Save(ctx, sess); err == nil {
		t.Fatal("Save accepted a command larger than the reserved queue byte budget")
	}
	exists, err := repo.Exists(context.Background(), sess.ID())
	if err != nil || exists {
		t.Fatalf("Exists after rejected Save=(%t,%v), want false", exists, err)
	}
	if _, err := repo.Load(context.Background(), sess.ID()); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("Load after rejected Save error=%v, want session not found", err)
	}
	address := sess.ChannelAddress()
	if _, created, err := repo.LoadOrCreateCanonical(ctx, sess.LogicalDate(), address, sess.CreatedAt()); err == nil || created {
		t.Fatalf("LoadOrCreateCanonical after rejected enqueue=(created %t, err %v), want error without creation", created, err)
	}
	if _, found, err := repo.LoadCanonical(context.Background(), sess.LogicalDate(), address); err != nil || found {
		t.Fatalf("LoadCanonical after rejected create=(found %t, err %v), want no visible session", found, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("files after rejected admissions=(%d,%v), want none", len(entries), err)
	}
	if err := admission.Release(); err != nil {
		t.Fatalf("Release unused admission: %v", err)
	}
}

func TestJSONSessionRepositoryFailedAdmittedWriteRemainsReadableAndQueueRejects(t *testing.T) {
	dir := t.TempDir()
	repo := NewJSONSessionRepository(dir)
	queue, unblock := newBlockedPersistenceQueue(t, 4, 2<<20)
	ctx, admission := admitSessionWrites(t, queue, 1, 1<<20)
	address, err := conversation.NewChannelAddress("line", "failed-write-user")
	if err != nil {
		t.Fatal(err)
	}
	value, created, err := repo.LoadOrCreateCanonical(ctx, "2026-10-21", address, time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC))
	if err != nil || !created {
		t.Fatalf("admitted LoadOrCreateCanonical=(%v,%t,%v)", value, created, err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	unblock()
	if err := admission.Barrier(context.Background()); !errors.Is(err, persistencequeue.ErrFailed) {
		t.Fatalf("Barrier after persistence failure=%v, want ErrFailed", err)
	}
	loaded, err := repo.Load(context.Background(), value.ID())
	if err != nil || loaded.ID() != value.ID() {
		t.Fatalf("Load failed pending write=(%v,%v), want accepted session", loaded, err)
	}
	exists, err := repo.Exists(context.Background(), value.ID())
	if err != nil || !exists {
		t.Fatalf("Exists failed pending write=(%t,%v), want true", exists, err)
	}
	if _, found, err := repo.LoadCanonical(context.Background(), "2026-10-21", address); err == nil || found {
		t.Fatalf("LoadCanonical with unreadable session directory=(found %t, err %v), want fail-closed error", found, err)
	}
	if _, err := persistencequeue.Admit(queue, 1, 1); !errors.Is(err, persistencequeue.ErrFailed) {
		t.Fatalf("next persistence admission error=%v, want ErrFailed", err)
	}
}

func TestJSONSessionRepositoryDeleteInvalidatesPendingAdmittedSave(t *testing.T) {
	dir := t.TempDir()
	repo := NewJSONSessionRepository(dir)
	queue, unblock := newBlockedPersistenceQueue(t, 4, 2<<20)
	ctx, admission := admitSessionWrites(t, queue, 1, 1<<20)
	sess := newCanonicalRepositoryTestSession(t)
	if err := repo.Save(ctx, sess); err != nil {
		t.Fatalf("admitted Save: %v", err)
	}
	if err := repo.Delete(context.Background(), sess.ID()); err != nil {
		t.Fatalf("Delete before queued Save runs: %v", err)
	}
	if exists, err := repo.Exists(context.Background(), sess.ID()); err != nil || exists {
		t.Fatalf("Exists after Delete=(%t,%v), want false", exists, err)
	}
	if _, err := repo.Load(context.Background(), sess.ID()); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("Load after Delete error=%v, want not found", err)
	}
	unblock()
	if err := admission.Barrier(context.Background()); err != nil {
		t.Fatalf("Barrier after invalidated Save: %v", err)
	}
	if _, err := os.Stat(repo.getFilePath(sess.ID())); !os.IsNotExist(err) {
		t.Fatalf("queued Save recreated deleted session: stat err=%v", err)
	}
}

func TestJSONSessionRepositoryV2ReservationRemainsSynchronousWithAdmissionContext(t *testing.T) {
	dir := t.TempDir()
	repo := NewJSONSessionRepository(dir)
	queue, _ := newBlockedPersistenceQueue(t, 4, 2<<20)
	ctx, admission := admitSessionWrites(t, queue, 1, 1<<20)
	defer admission.Release()
	address, err := conversation.NewChannelAddress("viewer", "v2-sync-user")
	if err != nil {
		t.Fatal(err)
	}
	value, created, err := repo.ReserveCanonicalV2(ctx, "2026-10-22", address, "owner", time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC))
	if err != nil || !created {
		t.Fatalf("ReserveCanonicalV2 with admission=(%v,%t,%v)", value, created, err)
	}
	if _, err := os.Stat(repo.getFilePath(value.ID())); err != nil {
		t.Fatalf("v2 reservation was not durable on return: %v", err)
	}
	if err := repo.Save(ctx, value); err == nil {
		t.Fatal("admitted legacy Save modified a response-first v2 reservation")
	}
	if status := queue.Status(); !status.InFlight || status.Pending != 1 || status.Reserved != 1 || status.Failure != nil {
		t.Fatalf("queue status after v2 reservation=%+v, want only blocker in flight and unused admission reservation", status)
	}
}

func TestJSONSessionRepositoryLoadOrCreateCanonicalUsesExplicitLookupAttributes(t *testing.T) {
	repo := NewJSONSessionRepository(t.TempDir())
	address, err := conversation.NewChannelAddress("line", "U123")
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	first, firstCreated, err := repo.LoadOrCreateCanonical(context.Background(), "2026-09-03", address, createdAt)
	if err != nil {
		t.Fatalf("first LoadOrCreateCanonical: %v", err)
	}
	if !firstCreated {
		t.Fatal("first LoadOrCreateCanonical created=false, want true")
	}
	second, secondCreated, err := repo.LoadOrCreateCanonical(context.Background(), "2026-09-03", address, createdAt.Add(time.Hour))
	if err != nil {
		t.Fatalf("second LoadOrCreateCanonical: %v", err)
	}
	if secondCreated {
		t.Fatal("second LoadOrCreateCanonical created=true, want false")
	}
	if first.ID() != second.ID() {
		t.Fatalf("same lookup attributes produced %q and %q", first.ID(), second.ID())
	}
	if err := modulecore.SessionID(first.ID()).Validate(); err != nil {
		t.Fatalf("created SessionID: %v", err)
	}

	nextDate, nextDateCreated, err := repo.LoadOrCreateCanonical(context.Background(), "2026-09-04", address, createdAt.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("date boundary LoadOrCreateCanonical: %v", err)
	}
	if !nextDateCreated {
		t.Fatal("date boundary LoadOrCreateCanonical created=false, want true")
	}
	if nextDate.ID() == first.ID() {
		t.Fatal("date boundary reused the prior SessionID")
	}
}

func TestJSONSessionRepositoryLoadOrCreateCanonicalIsConcurrentSafe(t *testing.T) {
	dir := t.TempDir()
	repo := NewJSONSessionRepository(dir)
	address, err := conversation.NewChannelAddress("viewer", "ren")
	if err != nil {
		t.Fatal(err)
	}
	const workers = 32
	type result struct {
		id      string
		created bool
	}
	results := make(chan result, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			item, created, loadErr := repo.LoadOrCreateCanonical(context.Background(), "2026-09-03", address, time.Now().UTC())
			if loadErr != nil {
				errs <- loadErr
				return
			}
			results <- result{id: item.ID(), created: created}
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("LoadOrCreateCanonical: %v", err)
	}
	var want string
	createdCount := 0
	for item := range results {
		if want == "" {
			want = item.id
		}
		if item.id != want {
			t.Fatalf("concurrent IDs include %q and %q", want, item.id)
		}
		if item.created {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Fatalf("concurrent created provenance count=%d, want exactly one", createdCount)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("session files = %d, want 1", len(entries))
	}
}

func TestJSONSessionRepositoryReserveCanonicalV2IsAtomicReusableAndExclusive(t *testing.T) {
	dir := t.TempDir()
	address, err := conversation.NewChannelAddress("viewer", "viewer-user")
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	repo := NewJSONSessionRepository(dir)
	first, created, err := repo.ReserveCanonicalV2(context.Background(), "2026-10-03", address, "ren", createdAt)
	if err != nil {
		t.Fatalf("ReserveCanonicalV2: %v", err)
	}
	if !created {
		t.Fatal("first reservation created=false, want true")
	}
	second, created, err := NewJSONSessionRepository(dir).ReserveCanonicalV2(context.Background(), "2026-10-03", address, "ren", createdAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("retry ReserveCanonicalV2 after repository restart: %v", err)
	}
	if created || first.ID() != second.ID() {
		t.Fatalf("retry reservation=(%q,%t), want same ID %q and created=false", second.ID(), created, first.ID())
	}
	reserved, err := repo.IsCanonicalV2Reserved(context.Background(), first.ID(), "ren")
	if err != nil || !reserved {
		t.Fatalf("IsCanonicalV2Reserved(same owner)=(%t,%v), want true", reserved, err)
	}
	reserved, err = repo.IsCanonicalV2Reserved(context.Background(), first.ID(), "another-owner")
	if err != nil || reserved {
		t.Fatalf("IsCanonicalV2Reserved(other owner)=(%t,%v), want false", reserved, err)
	}
	if _, _, err := repo.ReserveCanonicalV2(context.Background(), "2026-10-03", address, "another-owner", createdAt); err == nil {
		t.Fatal("ReserveCanonicalV2 reused a reservation owned by another owner")
	}
	if _, _, err := repo.LoadOrCreateCanonical(context.Background(), "2026-10-03", address, createdAt); err == nil {
		t.Fatal("legacy LoadOrCreateCanonical reused a v2 reservation")
	}
	if err := repo.Save(context.Background(), second); err == nil {
		t.Fatal("Save modified a v2-reserved session")
	}

	raw, err := os.ReadFile(filepath.Join(dir, first.ID()+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]json.RawMessage
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	var intent struct {
		Runtime string `json:"runtime"`
		OwnerID string `json:"owner_id"`
	}
	if err := json.Unmarshal(record["creation_intent"], &intent); err != nil {
		t.Fatalf("creation_intent: %v", err)
	}
	if intent.Runtime != "v2" || intent.OwnerID != "ren" {
		t.Fatalf("creation_intent=%+v, want runtime v2 / owner ren", intent)
	}
}

func TestJSONSessionRepositoryReserveCanonicalV2NeverPromotesLegacyJSON(t *testing.T) {
	dir := t.TempDir()
	repo := NewJSONSessionRepository(dir)
	address, err := conversation.NewChannelAddress("viewer", "viewer-user")
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	legacy, created, err := repo.LoadOrCreateCanonical(context.Background(), "2026-10-04", address, createdAt)
	if err != nil || !created {
		t.Fatalf("create legacy canonical session=(%v,%t,%v)", legacy, created, err)
	}
	if _, _, err := repo.ReserveCanonicalV2(context.Background(), "2026-10-04", address, "ren", createdAt); err == nil {
		t.Fatal("ReserveCanonicalV2 promoted an existing empty legacy JSON session")
	}
	reserved, err := repo.IsCanonicalV2Reserved(context.Background(), legacy.ID(), "ren")
	if err != nil || reserved {
		t.Fatalf("legacy session reservation=(%t,%v), want false", reserved, err)
	}
	loaded, err := repo.Load(context.Background(), legacy.ID())
	if err != nil {
		t.Fatalf("legacy JSON should remain loadable after refused promotion: %v", err)
	}
	if loaded.ID() != legacy.ID() {
		t.Fatalf("legacy session identity changed: got %q want %q", loaded.ID(), legacy.ID())
	}
}

func TestJSONSessionRepositoryReserveCanonicalV2ConcurrentCreationProvenance(t *testing.T) {
	dir := t.TempDir()
	repo := NewJSONSessionRepository(dir)
	address, err := conversation.NewChannelAddress("viewer", "viewer-user")
	if err != nil {
		t.Fatal(err)
	}
	const workers = 32
	type result struct {
		id      string
		created bool
	}
	results := make(chan result, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, created, reserveErr := repo.ReserveCanonicalV2(context.Background(), "2026-10-05", address, "ren", time.Now().UTC())
			if reserveErr != nil {
				errs <- reserveErr
				return
			}
			results <- result{id: value.ID(), created: created}
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("ReserveCanonicalV2: %v", err)
	}
	wantID := ""
	createdCount := 0
	for item := range results {
		if wantID == "" {
			wantID = item.id
		}
		if item.id != wantID {
			t.Fatalf("concurrent reservation IDs include %q and %q", wantID, item.id)
		}
		if item.created {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Fatalf("concurrent created provenance count=%d, want exactly one", createdCount)
	}
}

func TestJSONSessionRepositoryLegacyCreateAndV2ReserveCannotBothAdmit(t *testing.T) {
	repo := NewJSONSessionRepository(t.TempDir())
	address, err := conversation.NewChannelAddress("viewer", "viewer-user")
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	type outcome struct {
		created bool
		err     error
	}
	legacyDone := make(chan outcome, 1)
	v2Done := make(chan outcome, 1)
	createdAt := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	go func() {
		<-start
		_, created, createErr := repo.LoadOrCreateCanonical(context.Background(), "2026-10-06", address, createdAt)
		legacyDone <- outcome{created: created, err: createErr}
	}()
	go func() {
		<-start
		_, created, reserveErr := repo.ReserveCanonicalV2(context.Background(), "2026-10-06", address, "ren", createdAt)
		v2Done <- outcome{created: created, err: reserveErr}
	}()
	close(start)
	legacy := <-legacyDone
	v2 := <-v2Done
	if legacy.err == nil && v2.err == nil {
		t.Fatal("legacy creation and v2 reservation both succeeded for one natural key")
	}
	if legacy.err != nil && v2.err != nil {
		t.Fatalf("both session admissions failed: legacy=%v v2=%v", legacy.err, v2.err)
	}
	value, intent, found, err := repo.LoadCanonicalWithCreationIntent(context.Background(), "2026-10-06", address)
	if err != nil || !found || value == nil {
		t.Fatalf("canonical session after admission race=(%v,%+v,%t,%v), want exactly one session", value, intent, found, err)
	}
	if legacy.err == nil && intent != nil {
		t.Fatalf("legacy creation won but session has a v2 intent: %+v", intent)
	}
	if v2.err == nil && (intent == nil || intent.Runtime != "v2" || intent.OwnerID != "ren") {
		t.Fatalf("v2 reservation won but intent is %+v", intent)
	}
}

func TestJSONSessionRepositoryLoadCanonicalIsReadOnlyAndResolvesNaturalKey(t *testing.T) {
	dir := t.TempDir()
	repo := NewJSONSessionRepository(dir)
	address, err := conversation.NewChannelAddress("viewer", "viewer-user")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	missing, found, err := repo.LoadCanonical(ctx, "2026-09-03", address)
	if err != nil || found || missing != nil {
		t.Fatalf("LoadCanonical before creation=(%v,%t,%v), want (nil,false,nil)", missing, found, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("read-only lookup created %d files, want none", len(entries))
	}

	want, created, err := repo.LoadOrCreateCanonical(ctx, "2026-09-03", address, time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC))
	if err != nil || !created {
		t.Fatalf("LoadOrCreateCanonical=(%v,%t,%v), want newly-created session", want, created, err)
	}
	got, found, err := repo.LoadCanonical(ctx, "2026-09-03", address)
	if err != nil || !found || got == nil || got.ID() != want.ID() {
		t.Fatalf("LoadCanonical after creation=(%v,%t,%v), want session %q", got, found, err, want.ID())
	}
}

func TestJSONSessionRepositoryWarmCanonicalIndexScansOnceAndServesLookupsFromRAM(t *testing.T) {
	dir := t.TempDir()
	seed := NewJSONSessionRepository(dir)
	want := newCanonicalRepositoryTestSession(t)
	if err := seed.Save(context.Background(), want); err != nil {
		t.Fatalf("seed Save: %v", err)
	}

	repo := NewJSONSessionRepository(dir)
	directoryReads := 0
	identityFileReads := 0
	repo.readCanonicalDirectory = func(path string) ([]os.DirEntry, error) {
		directoryReads++
		return os.ReadDir(path)
	}
	repo.readCanonicalIdentityFile = func(path string) ([]byte, error) {
		identityFileReads++
		return os.ReadFile(path)
	}
	if repo.CanonicalIndexWarmed() {
		t.Fatal("new repository canonical index is warmed before startup scan")
	}
	if err := repo.WarmCanonicalIndex(context.Background()); err != nil {
		t.Fatalf("WarmCanonicalIndex: %v", err)
	}
	if !repo.CanonicalIndexWarmed() {
		t.Fatal("CanonicalIndexWarmed=false after successful warm")
	}
	if directoryReads != 1 || identityFileReads != 1 {
		t.Fatalf("warm reads=(directory %d, files %d), want (1,1)", directoryReads, identityFileReads)
	}

	gotID, err := repo.findCanonicalSessionID(context.Background(), want.LogicalDate(), want.ChannelAddress())
	if err != nil || gotID != want.ID() {
		t.Fatalf("indexed canonical lookup=(%q,%v), want %q", gotID, err, want.ID())
	}
	loaded, found, err := repo.LoadCanonical(context.Background(), want.LogicalDate(), want.ChannelAddress())
	if err != nil || !found || loaded == nil || loaded.ID() != want.ID() {
		t.Fatalf("LoadCanonical after warm=(%v,%t,%v), want session %q", loaded, found, err, want.ID())
	}
	if err := repo.WarmCanonicalIndex(context.Background()); err != nil {
		t.Fatalf("idempotent WarmCanonicalIndex: %v", err)
	}
	if directoryReads != 1 || identityFileReads != 1 {
		t.Fatalf("post-warm scan reads=(directory %d, files %d), want unchanged (1,1)", directoryReads, identityFileReads)
	}
}

func TestJSONSessionRepositoryWarmCanonicalIndexTracksOverlayAndDelete(t *testing.T) {
	repo := NewJSONSessionRepository(t.TempDir())
	directoryReads := 0
	repo.readCanonicalDirectory = func(path string) ([]os.DirEntry, error) {
		directoryReads++
		return os.ReadDir(path)
	}
	if err := repo.WarmCanonicalIndex(context.Background()); err != nil {
		t.Fatalf("WarmCanonicalIndex: %v", err)
	}
	queue, unblock := newBlockedPersistenceQueue(t, 5, 4<<20)
	ctx, admission := admitSessionWrites(t, queue, 2, 2<<20)
	defer admission.Release()
	address, err := conversation.NewChannelAddress("line", "indexed-overlay-user")
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	created, wasCreated, err := repo.LoadOrCreateCanonical(ctx, "2026-10-23", address, createdAt)
	if err != nil || !wasCreated {
		t.Fatalf("admitted LoadOrCreateCanonical=(%v,%t,%v), want new overlay", created, wasCreated, err)
	}
	rekeyed, err := session.NewCanonicalSession(modulecore.SessionID(created.ID()), "2026-10-24", address, createdAt)
	if err != nil {
		t.Fatalf("NewCanonicalSession for admitted rekey: %v", err)
	}
	if err := repo.Save(ctx, rekeyed); err != nil {
		t.Fatalf("admitted Save rekey: %v", err)
	}
	if id, err := repo.findCanonicalSessionID(context.Background(), "2026-10-23", address); err != nil || id != "" {
		t.Fatalf("old canonical lookup after admitted rekey=(%q,%v), want no match", id, err)
	}
	loaded, found, err := repo.LoadCanonical(context.Background(), "2026-10-24", address)
	if err != nil || !found || loaded == nil || loaded.ID() != created.ID() {
		t.Fatalf("LoadCanonical for overlay=(%v,%t,%v), want %q", loaded, found, err, created.ID())
	}
	if directoryReads != 1 {
		t.Fatalf("directory reads after overlay lookup=%d, want only startup scan", directoryReads)
	}
	if err := repo.Delete(context.Background(), created.ID()); err != nil {
		t.Fatalf("Delete overlay: %v", err)
	}
	if id, err := repo.findCanonicalSessionID(context.Background(), "2026-10-24", address); err != nil || id != "" {
		t.Fatalf("canonical lookup after Delete=(%q,%v), want no match", id, err)
	}
	if directoryReads != 1 {
		t.Fatalf("directory reads after Delete=%d, want only startup scan", directoryReads)
	}
	unblock()
	if err := admission.Barrier(context.Background()); err != nil {
		t.Fatalf("Barrier after Delete: %v", err)
	}
}

func TestJSONSessionRepositoryWarmCanonicalIndexTracksSyncSaveAndV2Reservation(t *testing.T) {
	repo := NewJSONSessionRepository(t.TempDir())
	if err := repo.WarmCanonicalIndex(context.Background()); err != nil {
		t.Fatalf("WarmCanonicalIndex: %v", err)
	}
	legacy := newCanonicalRepositoryTestSession(t)
	if err := repo.Save(context.Background(), legacy); err != nil {
		t.Fatalf("Save after warm: %v", err)
	}
	if id, err := repo.findCanonicalSessionID(context.Background(), legacy.LogicalDate(), legacy.ChannelAddress()); err != nil || id != legacy.ID() {
		t.Fatalf("canonical lookup after sync Save=(%q,%v), want %q", id, err, legacy.ID())
	}
	rekeyed, err := session.NewCanonicalSession(modulecore.SessionID(legacy.ID()), "2026-03-02", legacy.ChannelAddress(), legacy.CreatedAt())
	if err != nil {
		t.Fatalf("NewCanonicalSession for sync rekey: %v", err)
	}
	if err := repo.Save(context.Background(), rekeyed); err != nil {
		t.Fatalf("Save rekeyed session: %v", err)
	}
	if id, err := repo.findCanonicalSessionID(context.Background(), legacy.LogicalDate(), legacy.ChannelAddress()); err != nil || id != "" {
		t.Fatalf("old canonical lookup after sync rekey=(%q,%v), want no match", id, err)
	}
	if id, err := repo.findCanonicalSessionID(context.Background(), rekeyed.LogicalDate(), rekeyed.ChannelAddress()); err != nil || id != rekeyed.ID() {
		t.Fatalf("new canonical lookup after sync rekey=(%q,%v), want %q", id, err, rekeyed.ID())
	}
	if err := repo.Delete(context.Background(), legacy.ID()); err != nil {
		t.Fatalf("Delete sync session: %v", err)
	}
	if id, err := repo.findCanonicalSessionID(context.Background(), rekeyed.LogicalDate(), rekeyed.ChannelAddress()); err != nil || id != "" {
		t.Fatalf("canonical lookup after sync Delete=(%q,%v), want no match", id, err)
	}

	address, err := conversation.NewChannelAddress("viewer", "indexed-v2-user")
	if err != nil {
		t.Fatal(err)
	}
	reserved, created, err := repo.ReserveCanonicalV2(context.Background(), "2026-10-24", address, "owner", time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC))
	if err != nil || !created {
		t.Fatalf("ReserveCanonicalV2 after warm=(%v,%t,%v), want new reservation", reserved, created, err)
	}
	if id, err := repo.findCanonicalSessionID(context.Background(), "2026-10-24", address); err != nil || id != reserved.ID() {
		t.Fatalf("canonical lookup after v2 reservation=(%q,%v), want %q", id, err, reserved.ID())
	}
}

func TestJSONSessionRepositoryWarmCanonicalIndexRejectsDuplicateOrCorruptIdentity(t *testing.T) {
	t.Run("duplicate natural key", func(t *testing.T) {
		repo := NewJSONSessionRepository(t.TempDir())
		first := newCanonicalRepositoryTestSession(t)
		second := newCanonicalRepositoryTestSession(t)
		if err := repo.Save(context.Background(), first); err != nil {
			t.Fatalf("first Save: %v", err)
		}
		if err := repo.Save(context.Background(), second); err != nil {
			t.Fatalf("second Save before warm: %v", err)
		}
		if err := repo.WarmCanonicalIndex(context.Background()); err == nil {
			t.Fatal("WarmCanonicalIndex accepted duplicate natural keys")
		}
		if repo.CanonicalIndexWarmed() {
			t.Fatal("duplicate natural key left canonical index marked warm")
		}
	})

	t.Run("incomplete identity", func(t *testing.T) {
		dir := t.TempDir()
		repo := NewJSONSessionRepository(dir)
		id := modulecore.NewSessionID()
		raw := []byte(`{"id":"` + string(id) + `","logical_date":"2026-10-25"}`)
		if err := os.WriteFile(filepath.Join(dir, string(id)+".json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		if err := repo.WarmCanonicalIndex(context.Background()); err == nil {
			t.Fatal("WarmCanonicalIndex accepted incomplete canonical identity")
		}
		if repo.CanonicalIndexWarmed() {
			t.Fatal("corrupt canonical identity left canonical index marked warm")
		}
	})

	t.Run("disk read error", func(t *testing.T) {
		repo := NewJSONSessionRepository(t.TempDir())
		if err := repo.Save(context.Background(), newCanonicalRepositoryTestSession(t)); err != nil {
			t.Fatalf("seed Save: %v", err)
		}
		repo.readCanonicalIdentityFile = func(string) ([]byte, error) {
			return nil, os.ErrPermission
		}
		if err := repo.WarmCanonicalIndex(context.Background()); err == nil {
			t.Fatal("WarmCanonicalIndex hid identity file read error")
		}
		if repo.CanonicalIndexWarmed() {
			t.Fatal("read error left canonical index marked warm")
		}
	})
}

func TestJSONSessionRepositoryCanonicalIdentityRoundTrip(t *testing.T) {
	dir := t.TempDir()
	repo := NewJSONSessionRepository(dir)
	address, err := conversation.NewChannelAddress("line", "U123")
	if err != nil {
		t.Fatal(err)
	}
	id := modulecore.NewSessionID()
	sess, err := session.NewCanonicalSession(id, "2026-09-02", address, time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	input := newRepositoryTurnInputForTest(t, sess, "hello").
		WithAttachments([]attachment.Attachment{{
			ID:                  "att-1",
			Kind:                attachment.KindDocument,
			Filename:            "notes.txt",
			ContentType:         "text/plain",
			SizeBytes:           5,
			Path:                "attachments/att-1",
			SHA256:              "sha256-notes",
			ExtractedText:       "hello",
			ExtractionTruncated: true,
			SecurityWarnings:    []string{"warning"},
			Data:                []byte("bytes must not persist"),
		}}).
		WithViewerRecipient("shiro").
		WithForcedRoute(routing.RouteCODE3).
		WithRoute(routing.RouteCHAT)
	sess.AddTurnInput(input)
	sess.SetMemory("key", "value")
	if err := repo.Save(context.Background(), sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, string(id)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["logical_date"]; !ok {
		t.Fatal("logical_date is absent")
	}
	if _, ok := fields["channel_address"]; !ok {
		t.Fatal("channel_address is absent")
	}
	if _, ok := fields["channel"]; ok {
		t.Fatal("legacy channel must not be written for a canonical session")
	}
	if _, ok := fields["chat_id"]; ok {
		t.Fatal("legacy chat_id must not be written for a canonical session")
	}
	var channelAddress map[string]json.RawMessage
	if err := json.Unmarshal(fields["channel_address"], &channelAddress); err != nil {
		t.Fatalf("channel_address: %v", err)
	}
	if len(channelAddress) != 2 {
		t.Fatalf("channel_address keys = %v, want exactly channel_type and external_conversation_id", channelAddress)
	}
	for _, key := range []string{"channel_type", "external_conversation_id"} {
		if _, ok := channelAddress[key]; !ok {
			t.Fatalf("channel_address.%s is absent", key)
		}
	}
	for _, key := range []string{"channel", "address"} {
		if _, ok := channelAddress[key]; ok {
			t.Fatalf("legacy channel_address.%s must not be written", key)
		}
	}
	var history []map[string]json.RawMessage
	if err := json.Unmarshal(fields["history"], &history); err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history length=%d, want 1", len(history))
	}
	for _, key := range []string{"root_task_id", "turn_id", "trace_id", "user_message_id", "agent_message_id", "message_text", "channel_address", "attachments"} {
		if _, ok := history[0][key]; !ok {
			t.Fatalf("history.%s is absent", key)
		}
	}
	for _, key := range []string{"job_id", "user_message", "channel", "chat_id", "session_id"} {
		if _, ok := history[0][key]; ok {
			t.Fatalf("legacy history.%s must not be written", key)
		}
	}
	var storedAttachments []map[string]json.RawMessage
	if err := json.Unmarshal(history[0]["attachments"], &storedAttachments); err != nil {
		t.Fatalf("attachments: %v", err)
	}
	if len(storedAttachments) != 1 {
		t.Fatalf("stored attachments=%d, want 1", len(storedAttachments))
	}
	if _, ok := storedAttachments[0]["data"]; ok {
		t.Fatal("attachment data must remain non-persisted")
	}

	loaded, err := repo.Load(context.Background(), string(id))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.ID() != string(id) || loaded.LogicalDate() != "2026-09-02" || loaded.ChannelAddress() != address {
		t.Fatalf("loaded identity = id:%q date:%q address:%#v", loaded.ID(), loaded.LogicalDate(), loaded.ChannelAddress())
	}
	if !loaded.CreatedAt().Equal(sess.CreatedAt()) || !loaded.UpdatedAt().Equal(sess.UpdatedAt()) {
		t.Fatalf("loaded timestamps = %s/%s, want %s/%s", loaded.CreatedAt(), loaded.UpdatedAt(), sess.CreatedAt(), sess.UpdatedAt())
	}
	loadedInput := loaded.GetHistory()[0]
	if loadedInput.RootTaskID() != input.RootTaskID() || loadedInput.TurnID() != input.TurnID() || loadedInput.TraceID() != input.TraceID() || loadedInput.UserMessageID() != input.UserMessageID() || loadedInput.AgentMessageID() != input.AgentMessageID() {
		t.Fatalf("loaded canonical identities changed: got=%#v want=%#v", loadedInput, input)
	}
	if loadedInput.MessageText() != input.MessageText() || loadedInput.SessionID() != sess.ID() || loadedInput.ChannelAddress() != address || loadedInput.ViewerRecipient() != "shiro" || loadedInput.ForcedRoute() != routing.RouteCODE3 || loadedInput.Route() != routing.RouteCHAT {
		t.Fatalf("loaded input metadata changed: got=%#v", loadedInput)
	}
	wantAttachment := input.Attachments()[0]
	wantAttachment.Data = nil
	if !reflect.DeepEqual(loadedInput.Attachments(), []attachment.Attachment{wantAttachment}) {
		t.Fatalf("loaded attachment metadata changed: got=%#v want=%#v", loadedInput.Attachments(), []attachment.Attachment{wantAttachment})
	}
}

func TestJSONSessionRepositoryRejectsLegacySessionContract(t *testing.T) {
	dir := t.TempDir()
	legacy := []byte(`{"id":"20260301-line-U123","channel":"line","chat_id":"U123","history":[],"memory":{},"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`)
	if err := os.WriteFile(filepath.Join(dir, "20260301-line-U123.json"), legacy, 0600); err != nil {
		t.Fatal(err)
	}
	repo := NewJSONSessionRepository(dir)
	if _, err := repo.Load(context.Background(), "20260301-line-U123"); err == nil {
		t.Fatal("legacy Session was accepted by canonical repository Load")
	}
	address, err := conversation.NewChannelAddress("line", "U123")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.LoadOrCreateCanonical(context.Background(), "2026-03-01", address, time.Now().UTC()); err == nil {
		t.Fatal("legacy Session was skipped by canonical lookup")
	}
	if _, _, err := repo.LoadCanonical(context.Background(), "2026-03-01", address); err == nil {
		t.Fatal("legacy Session was skipped by read-only canonical lookup")
	}
}

func TestJSONSessionRepositoryRejectsCanonicalRecordWithLegacyIdentityFields(t *testing.T) {
	dir := t.TempDir()
	id := modulecore.NewSessionID()
	mixed := []byte(`{"id":"` + string(id) + `","logical_date":"2026-03-01","channel_address":{"channel":"line","address":"U123"},"history":[],"memory":{},"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`)
	if err := os.WriteFile(filepath.Join(dir, string(id)+".json"), mixed, 0600); err != nil {
		t.Fatal(err)
	}
	repo := NewJSONSessionRepository(dir)
	if _, err := repo.Load(context.Background(), string(id)); err == nil {
		t.Fatal("canonical Session with legacy identity fields was accepted")
	}
	address, err := conversation.NewChannelAddress("line", "U123")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.LoadOrCreateCanonical(context.Background(), "2026-03-01", address, time.Now().UTC()); err == nil {
		t.Fatal("canonical lookup accepted legacy nested channel_address fields")
	}
}

func TestJSONSessionRepositoryRejectsLegacyHistoryFields(t *testing.T) {
	dir := t.TempDir()
	id := modulecore.NewSessionID()
	legacy := []byte(`{"id":"` + string(id) + `","logical_date":"2026-03-01","channel_address":{"channel_type":"line","external_conversation_id":"U123"},"history":[{"job_id":"job-1","user_message":"hello","channel":"line","chat_id":"U123"}],"memory":{},"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`)
	if err := os.WriteFile(filepath.Join(dir, string(id)+".json"), legacy, 0600); err != nil {
		t.Fatal(err)
	}
	repo := NewJSONSessionRepository(dir)
	if _, err := repo.Load(context.Background(), string(id)); err == nil {
		t.Fatal("legacy history DTO was accepted by canonical repository Load")
	}
}

func TestJSONSessionRepositoryRejectsHistorySessionOrAddressMismatchBeforeWrite(t *testing.T) {
	repo := NewJSONSessionRepository(t.TempDir())
	tests := []struct {
		name  string
		build func(*testing.T, *session.Session) conversation.TurnInput
	}{
		{
			name: "session mismatch",
			build: func(t *testing.T, sess *session.Session) conversation.TurnInput {
				return newRepositoryTurnInputForTest(t, sess, "wrong session").WithSessionID(string(modulecore.NewSessionID()))
			},
		},
		{
			name: "address mismatch",
			build: func(t *testing.T, sess *session.Session) conversation.TurnInput {
				otherAddress, err := conversation.NewChannelAddress("viewer", "other-user")
				if err != nil {
					t.Fatal(err)
				}
				input, err := conversation.NewTurnInput(modulecore.NewTaskID(), "wrong address", otherAddress)
				if err != nil {
					t.Fatal(err)
				}
				return input.WithSessionID(sess.ID())
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sess := newCanonicalRepositoryTestSession(t)
			sess.AddTurnInput(tt.build(t, sess))
			if err := repo.Save(context.Background(), sess); err == nil {
				t.Fatal("Save accepted history boundary mismatch")
			}
			exists, err := repo.Exists(context.Background(), sess.ID())
			if err != nil {
				t.Fatal(err)
			}
			if exists {
				t.Fatal("mismatched session was written")
			}
		})
	}
}

func TestNewJSONSessionRepository(t *testing.T) {
	tmpDir := t.TempDir()
	repo := NewJSONSessionRepository(tmpDir)

	if repo == nil {
		t.Fatal("NewJSONSessionRepository should not return nil")
	}
}

func TestJSONSessionRepository_SaveAndLoad(t *testing.T) {
	tmpDir := t.TempDir()
	repo := NewJSONSessionRepository(tmpDir)

	// セッション作成
	sess := newCanonicalRepositoryTestSession(t)
	testTask := newRepositoryTurnInputForTest(t, sess, "テストメッセージ")
	sess.AddTurnInput(testTask)
	sess.SetMemory("key1", "value1")

	// 保存
	err := repo.Save(context.Background(), sess)
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// ロード
	loaded, err := repo.Load(context.Background(), sess.ID())
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if loaded.ID() != sess.ID() {
		t.Errorf("Expected ID '%s', got '%s'", sess.ID(), loaded.ID())
	}

	if loaded.ChannelAddress() != sess.ChannelAddress() {
		t.Errorf("Expected ChannelAddress %#v, got %#v", sess.ChannelAddress(), loaded.ChannelAddress())
	}

	if loaded.HistoryCount() != 1 {
		t.Errorf("Expected 1 task in history, got %d", loaded.HistoryCount())
	}
	history := loaded.GetHistory()
	if len(history) != 1 || history[0].SessionID() != sess.ID() {
		t.Fatalf("loaded task SessionID=%q, want canonical session %q", history[0].SessionID(), sess.ID())
	}

	value, ok := loaded.GetMemory("key1")
	if !ok {
		t.Error("Expected key1 to exist in memory")
	}
	if value != "value1" {
		t.Errorf("Expected memory value 'value1', got '%v'", value)
	}
}

func TestJSONSessionRepository_LoadNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	repo := NewJSONSessionRepository(tmpDir)

	_, err := repo.Load(context.Background(), string(modulecore.NewSessionID()))
	if err == nil {
		t.Error("Expected error when loading non-existent session")
	}
}

func TestJSONSessionRepository_Exists(t *testing.T) {
	tmpDir := t.TempDir()
	repo := NewJSONSessionRepository(tmpDir)

	sess := newCanonicalRepositoryTestSession(t)
	if err := repo.Save(context.Background(), sess); err != nil {
		t.Fatal(err)
	}

	exists, err := repo.Exists(context.Background(), sess.ID())
	if err != nil {
		t.Fatalf("Exists failed: %v", err)
	}

	if !exists {
		t.Error("Session should exist")
	}

	exists, err = repo.Exists(context.Background(), string(modulecore.NewSessionID()))
	if err != nil {
		t.Fatalf("Exists failed: %v", err)
	}

	if exists {
		t.Error("Session should not exist")
	}
}

func TestJSONSessionRepository_Delete(t *testing.T) {
	tmpDir := t.TempDir()
	repo := NewJSONSessionRepository(tmpDir)

	sess := newCanonicalRepositoryTestSession(t)
	if err := repo.Save(context.Background(), sess); err != nil {
		t.Fatal(err)
	}

	// 削除前に存在確認
	exists, _ := repo.Exists(context.Background(), sess.ID())
	if !exists {
		t.Error("Session should exist before deletion")
	}

	// 削除
	err := repo.Delete(context.Background(), sess.ID())
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// 削除後に存在確認
	exists, _ = repo.Exists(context.Background(), sess.ID())
	if exists {
		t.Error("Session should not exist after deletion")
	}
}

func TestJSONSessionRepository_FileStructure(t *testing.T) {
	tmpDir := t.TempDir()
	repo := NewJSONSessionRepository(tmpDir)

	sess := newCanonicalRepositoryTestSession(t)
	if err := repo.Save(context.Background(), sess); err != nil {
		t.Fatal(err)
	}

	// ファイルが正しい場所に作成されているか確認
	expectedPath := filepath.Join(tmpDir, sess.ID()+".json")
	if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
		t.Errorf("Expected file to exist at %s", expectedPath)
	}

	// ファイルの内容がJSONとして読めるか確認
	data, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}

	if len(data) == 0 {
		t.Error("File should not be empty")
	}
}

func TestJSONSessionRepository_MultipleHistoryItems(t *testing.T) {
	tmpDir := t.TempDir()
	repo := NewJSONSessionRepository(tmpDir)

	sess := newCanonicalRepositoryTestSession(t)

	// 複数のタスクを追加
	for i := 0; i < 5; i++ {
		sess.AddTurnInput(newRepositoryTurnInputForTest(t, sess, "Message "+string(rune('A'+i))))
	}

	// 保存してロード
	if err := repo.Save(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	loaded, err := repo.Load(context.Background(), sess.ID())
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if loaded.HistoryCount() != 5 {
		t.Errorf("Expected 5 tasks in history, got %d", loaded.HistoryCount())
	}

	history := loaded.GetHistory()
	if history[0].MessageText() != "Message A" {
		t.Errorf("Expected first message 'Message A', got '%s'", history[0].MessageText())
	}
}

func TestJSONSessionRepository_MemoryPreservation(t *testing.T) {
	tmpDir := t.TempDir()
	repo := NewJSONSessionRepository(tmpDir)

	sess := newCanonicalRepositoryTestSession(t)
	sess.SetMemory("string", "value")
	sess.SetMemory("number", 42)
	sess.SetMemory("bool", true)

	if err := repo.Save(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	loaded, err := repo.Load(context.Background(), sess.ID())
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// メモリが正しく保存・復元されているか確認
	if val, ok := loaded.GetMemory("string"); !ok || val != "value" {
		t.Errorf("Expected string memory 'value', got '%v'", val)
	}

	if val, ok := loaded.GetMemory("number"); !ok || val.(float64) != 42 { // JSONは数値をfloat64にする
		t.Errorf("Expected number memory 42, got '%v'", val)
	}

	if val, ok := loaded.GetMemory("bool"); !ok || val != true {
		t.Errorf("Expected bool memory true, got '%v'", val)
	}
}
