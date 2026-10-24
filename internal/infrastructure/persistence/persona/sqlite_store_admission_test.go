package persona

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	domainpersona "github.com/Nyukimin/RenCrow_CORE/internal/domain/persona"
)

func TestSQLiteStoreAdmittedPersonaWritesAvoidLockedDiskAndOverlaySameAdmission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "persona-admitted.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(): %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close Persona SQLite store: %v", err)
		}
	})

	queue, err := persistencequeue.NewPersistenceQueue(8, 2<<20)
	if err != nil {
		t.Fatalf("NewPersistenceQueue(): %v", err)
	}
	if err := store.SetPersistenceQueue(queue); err != nil {
		t.Fatalf("SetPersistenceQueue(): %v", err)
	}
	t.Cleanup(func() { shutdownPersonaPersistenceQueue(t, queue) })
	releaseWorker := blockPersonaPersistenceQueueWorker(t, queue)

	lockDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open disk-lock connection: %v", err)
	}
	lockDB.SetMaxOpenConns(1)
	lockConn, err := lockDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("get disk-lock connection: %v", err)
	}
	if _, err := lockConn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("begin disk-lock transaction: %v", err)
	}
	var unlockOnce sync.Once
	unlockDisk := func() {
		unlockOnce.Do(func() {
			_, _ = lockConn.ExecContext(context.Background(), "ROLLBACK")
			_ = lockConn.Close()
			_ = lockDB.Close()
		})
	}
	t.Cleanup(unlockDisk)

	admission, err := persistencequeue.Admit(queue, 6, 1<<20)
	if err != nil {
		t.Fatalf("Admit(): %v", err)
	}
	defer admission.Release()
	ctx := persistencequeue.WithAdmission(context.Background(), admission)
	now := time.Date(2026, 10, 24, 12, 0, 0, 123, time.UTC)
	items := personaAdmissionTestItems(now)

	finished := make(chan error, 1)
	go func() {
		for _, save := range items.saves {
			if err := save(store, ctx); err != nil {
				finished <- err
				return
			}
		}
		finished <- nil
	}()

	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("admitted Save methods: %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		unlockDisk()
		releaseWorker()
		if err := <-finished; err != nil {
			t.Fatalf("Save methods after unlocking test database: %v", err)
		}
		t.Fatal("admitted Persona saves waited for the SQLite writer lock")
	}

	if got, found, err := store.FindObservationLogByID(context.Background(), items.observation.EventID); err != nil || found || !reflect.DeepEqual(got, domainpersona.ObservationLog{}) {
		t.Fatalf("unadmitted observation read before worker starts = %#v, found=%v, err=%v; want no pending overlay", got, found, err)
	}
	store.db.SetMaxOpenConns(1)
	releaseWorker()
	deadline := time.Now().Add(time.Second)
	for store.db.Stats().InUse == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if store.db.Stats().InUse == 0 {
		unlockDisk()
		t.Fatal("queue worker did not enter its SQLite write while the writer lock was held")
	}
	observationRead := make(chan struct {
		item  domainpersona.ObservationLog
		found bool
		err   error
	}, 1)
	go func() {
		item, found, err := store.FindObservationLogByID(ctx, items.observation.EventID)
		observationRead <- struct {
			item  domainpersona.ObservationLog
			found bool
			err   error
		}{item: item, found: found, err: err}
	}()
	select {
	case result := <-observationRead:
		if result.err != nil || !result.found || !reflect.DeepEqual(result.item, items.observation) {
			t.Fatalf("same-admission observation read = %#v, found=%v, err=%v; want immutable RAM overlay", result.item, result.found, result.err)
		}
	case <-time.After(250 * time.Millisecond):
		unlockDisk()
		if err := admission.Barrier(context.Background()); err != nil {
			t.Fatalf("drain worker after blocked observation read: %v", err)
		}
		<-observationRead
		t.Fatal("admission observation overlay waited for the SQLite writer lock")
	}
	nextAdmission, err := persistencequeue.Admit(queue, 1, 1024)
	if err != nil {
		t.Fatalf("Admit() for next CHAT turn: %v", err)
	}
	defer nextAdmission.Release()
	nextTurnContext := persistencequeue.WithAdmission(context.Background(), nextAdmission)
	canonicalRead := make(chan struct {
		items []domainpersona.CanonicalResponseLog
		err   error
	}, 1)
	go func() {
		got, err := store.ListCanonicalResponseLogs(nextTurnContext, 50)
		canonicalRead <- struct {
			items []domainpersona.CanonicalResponseLog
			err   error
		}{items: got, err: err}
	}()
	select {
	case result := <-canonicalRead:
		if result.err != nil || len(result.items) != 1 || !reflect.DeepEqual(result.items[0], items.canonical) {
			t.Fatalf("next-admission canonical response list = %#v, err=%v; want prior accepted CHAT response", result.items, result.err)
		}
	case <-time.After(250 * time.Millisecond):
		unlockDisk()
		if err := admission.Barrier(context.Background()); err != nil {
			t.Fatalf("drain worker after blocked canonical read: %v", err)
		}
		<-canonicalRead
		t.Fatal("admission canonical snapshot waited for the SQLite writer connection")
	}
	if got, found, err := store.FindObservationLogByID(nextTurnContext, items.observation.EventID); err != nil || !found || !reflect.DeepEqual(got, items.observation) {
		t.Fatalf("next-admission observation read = %#v, found=%v, err=%v; want prior accepted CHAT observation", got, found, err)
	}
	unlockDisk()
	if err := admission.Barrier(context.Background()); err != nil {
		t.Fatalf("wait for admitted Persona writes: %v", err)
	}
	assertPersonaAdmissionItems(t, store, ctx, items)
	assertPersonaAdmissionItems(t, store, context.Background(), items)

	if err := admission.Release(); err != nil {
		t.Fatalf("release admission: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close Persona SQLite store: %v", err)
	}
	reopened, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("reopen Persona SQLite store: %v", err)
	}
	defer reopened.Close()
	assertPersonaAdmissionItems(t, reopened, context.Background(), items)
}

func TestSQLiteStoreAdmittedWriteFailureIsLatchedAndNotOverlaid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "persona-admitted-failure.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(): %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close Persona SQLite store: %v", err)
		}
	})
	if _, err := store.db.Exec(`CREATE TRIGGER reject_admitted_observation BEFORE INSERT ON observation_log BEGIN SELECT RAISE(ABORT, 'injected Persona write failure'); END`); err != nil {
		t.Fatalf("install failure trigger: %v", err)
	}

	queue, err := persistencequeue.NewPersistenceQueue(4, 2<<20)
	if err != nil {
		t.Fatalf("NewPersistenceQueue(): %v", err)
	}
	if err := store.SetPersistenceQueue(queue); err != nil {
		t.Fatalf("SetPersistenceQueue(): %v", err)
	}
	t.Cleanup(func() { shutdownPersonaPersistenceQueue(t, queue) })
	releaseWorker := blockPersonaPersistenceQueueWorker(t, queue)

	admission, err := persistencequeue.Admit(queue, 2, 1<<20)
	if err != nil {
		t.Fatalf("Admit(): %v", err)
	}
	defer admission.Release()
	ctx := persistencequeue.WithAdmission(context.Background(), admission)
	item := domainpersona.ObservationLog{
		EventID:         "evt_persona_admitted_failure",
		ObserverID:      "mio",
		TargetID:        "ren",
		ObservationType: "chat_message",
		Summary:         "queue write should fail",
		Sensitivity:     "normal",
		ReviewStatus:    "pending",
		CreatedAt:       time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC),
	}
	if err := store.SaveObservationLog(ctx, item); err != nil {
		t.Fatalf("SaveObservationLog() should accept the reserved RAM command: %v", err)
	}
	if got, found, err := store.FindObservationLogByID(ctx, item.EventID); err != nil || !found || !reflect.DeepEqual(got, item) {
		t.Fatalf("pending observation read = %#v, found=%v, err=%v; want accepted write overlay", got, found, err)
	}

	releaseWorker()
	barrierErr := admission.Barrier(context.Background())
	if !errors.Is(barrierErr, persistencequeue.ErrFailed) || barrierErr == nil {
		t.Fatalf("admission barrier error = %v, want latched queue failure", barrierErr)
	}
	status := queue.Status()
	if status.Failure == nil || !errors.Is(status.Failure, persistencequeue.ErrFailed) {
		t.Fatalf("queue failure = %+v, want the barrier's latched write failure", status.Failure)
	}
	if err := store.SaveTriggerLog(ctx, domainpersona.TriggerLog{
		EventID: "evt_persona_after_latched_failure", CharacterID: "mio", TriggerID: "danger", Activated: true, CreatedAt: item.CreatedAt,
	}); !errors.Is(err, persistencequeue.ErrFailed) {
		t.Fatalf("save after latched queue failure = %v, want immediate queue failure", err)
	}
	if got, found, err := store.FindObservationLogByID(ctx, item.EventID); !errors.Is(err, persistencequeue.ErrFailed) || found || !reflect.DeepEqual(got, domainpersona.ObservationLog{}) {
		t.Fatalf("failed observation read = %#v, found=%v, err=%v; want immediate latched queue failure without failed-write overlay", got, found, err)
	}
}

func TestSQLiteStoreCloseDrainsAcceptedPersonaWritesBeforeClosingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "persona-admitted-close.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(): %v", err)
	}
	var closeOnce sync.Once
	closeStore := func() error {
		var closeErr error
		closeOnce.Do(func() { closeErr = store.Close() })
		return closeErr
	}
	t.Cleanup(func() {
		if err := closeStore(); err != nil {
			t.Errorf("close Persona SQLite store: %v", err)
		}
	})

	queue, err := persistencequeue.NewPersistenceQueue(2, 2<<20)
	if err != nil {
		t.Fatalf("NewPersistenceQueue(): %v", err)
	}
	if err := store.SetPersistenceQueue(queue); err != nil {
		t.Fatalf("SetPersistenceQueue(): %v", err)
	}
	t.Cleanup(func() { shutdownPersonaPersistenceQueue(t, queue) })
	releaseWorker := blockPersonaPersistenceQueueWorker(t, queue)
	admission, err := persistencequeue.Admit(queue, 1, 1<<20)
	if err != nil {
		t.Fatalf("Admit(): %v", err)
	}
	defer admission.Release()
	ctx := persistencequeue.WithAdmission(context.Background(), admission)
	item := domainpersona.ObservationLog{
		EventID:         "evt_persona_admitted_close",
		ObserverID:      "mio",
		TargetID:        "ren",
		ObservationType: "chat_message",
		Summary:         "must survive owner close",
		Sensitivity:     "normal",
		ReviewStatus:    "pending",
		CreatedAt:       time.Date(2026, 10, 24, 13, 0, 0, 0, time.UTC),
	}
	if err := store.SaveObservationLog(ctx, item); err != nil {
		t.Fatalf("SaveObservationLog(): %v", err)
	}
	closeResult := make(chan error, 1)
	go func() { closeResult <- closeStore() }()

	closedEarly := false
	select {
	case err := <-closeResult:
		closedEarly = true
		if err != nil {
			t.Fatalf("Close() before queue release: %v", err)
		}
	case <-time.After(50 * time.Millisecond):
	}
	releaseWorker()
	if !closedEarly {
		select {
		case err := <-closeResult:
			if err != nil {
				t.Fatalf("Close() after accepted write drained: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Close() did not finish after the queued Persona write completed")
		}
	} else {
		t.Fatal("Close() returned while an accepted Persona write was still queued")
	}
	if err := admission.Barrier(context.Background()); err != nil {
		t.Fatalf("admission barrier after store close: %v", err)
	}

	reopened, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("reopen Persona SQLite store: %v", err)
	}
	defer reopened.Close()
	if got, found, err := reopened.FindObservationLogByID(context.Background(), item.EventID); err != nil || !found || !reflect.DeepEqual(got, item) {
		t.Fatalf("reopened observation = %#v, found=%v, err=%v; want accepted write", got, found, err)
	}
}

type personaAdmissionTestSet struct {
	discomfort  domainpersona.DiscomfortLog
	trigger     domainpersona.TriggerLog
	canonical   domainpersona.CanonicalResponseLog
	observation domainpersona.ObservationLog
	meta        domainpersona.MetaProfileUpdate
	session     domainpersona.InterfaceSession
	saves       []func(*SQLiteStore, context.Context) error
}

func personaAdmissionTestItems(now time.Time) personaAdmissionTestSet {
	items := personaAdmissionTestSet{
		discomfort: domainpersona.DiscomfortLog{
			EventID: "evt_persona_admitted_discomfort", CharacterID: "mio", Discomfort: "期待より軽すぎた", Status: "candidate", CreatedAt: now,
		},
		trigger: domainpersona.TriggerLog{
			EventID: "evt_persona_admitted_trigger", CharacterID: "mio", TriggerID: "danger", TriggerCategory: "safety", Activated: true, Confidence: 0.9, CreatedAt: now,
		},
		canonical: domainpersona.CanonicalResponseLog{
			EventID: "evt_persona_admitted_canonical", CharacterID: "mio", ResponseKey: "danger", MessageID: "msg-persona-admitted", Used: true, CreatedAt: now,
		},
		observation: domainpersona.ObservationLog{
			EventID: "evt_persona_admitted_observation", ObserverID: "mio", TargetID: "ren", ObservationType: "chat_message", Summary: "review candidate", Sensitivity: "normal", ReviewStatus: "pending", CreatedAt: now,
		},
		meta: domainpersona.MetaProfileUpdate{
			UpdateID: "meta_persona_admitted", ObserverID: "mio", TargetID: "ren", Section: "flow_observation", ProposedContent: "review candidate", Sensitivity: "normal", ReviewStatus: "pending", CreatedAt: now,
		},
		session: domainpersona.InterfaceSession{
			SessionID: "persona_session_admitted", CharacterID: "mio", InterfaceType: "cmd", SessionKey: "cmd:admitted", CreatedAt: now, LastUsedAt: now,
		},
	}
	items.saves = []func(*SQLiteStore, context.Context) error{
		func(store *SQLiteStore, ctx context.Context) error {
			return store.SaveDiscomfortLog(ctx, items.discomfort)
		},
		func(store *SQLiteStore, ctx context.Context) error { return store.SaveTriggerLog(ctx, items.trigger) },
		func(store *SQLiteStore, ctx context.Context) error {
			return store.SaveCanonicalResponseLog(ctx, items.canonical)
		},
		func(store *SQLiteStore, ctx context.Context) error {
			return store.SaveObservationLog(ctx, items.observation)
		},
		func(store *SQLiteStore, ctx context.Context) error {
			return store.SaveMetaProfileUpdate(ctx, items.meta)
		},
		func(store *SQLiteStore, ctx context.Context) error {
			return store.SaveInterfaceSession(ctx, items.session)
		},
	}
	return items
}

func assertPersonaAdmissionItems(t *testing.T, store *SQLiteStore, ctx context.Context, items personaAdmissionTestSet) {
	t.Helper()
	if got, err := store.ListDiscomfortLogs(ctx, 10); err != nil || len(got) != 1 || !reflect.DeepEqual(got[0], items.discomfort) {
		t.Fatalf("discomfort list = %#v, err=%v; want exact record", got, err)
	}
	if got, err := store.ListTriggerLogs(ctx, 10); err != nil || len(got) != 1 || !reflect.DeepEqual(got[0], items.trigger) {
		t.Fatalf("trigger list = %#v, err=%v; want exact record", got, err)
	}
	if got, err := store.ListCanonicalResponseLogs(ctx, 10); err != nil || len(got) != 1 || !reflect.DeepEqual(got[0], items.canonical) {
		t.Fatalf("canonical response list = %#v, err=%v; want exact record", got, err)
	}
	if got, err := store.ListObservationLogs(ctx, 10); err != nil || len(got) != 1 || !reflect.DeepEqual(got[0], items.observation) {
		t.Fatalf("observation list = %#v, err=%v; want exact record", got, err)
	}
	if got, err := store.ListMetaProfileUpdates(ctx, 10); err != nil || len(got) != 1 || !reflect.DeepEqual(got[0], items.meta) {
		t.Fatalf("meta update list = %#v, err=%v; want exact record", got, err)
	}
	if got, err := store.ListInterfaceSessions(ctx, 10); err != nil || len(got) != 1 || !reflect.DeepEqual(got[0], items.session) {
		t.Fatalf("interface session list = %#v, err=%v; want exact record", got, err)
	}
}

func blockPersonaPersistenceQueueWorker(t *testing.T, queue *persistencequeue.Queue[persistencequeue.PersistenceCommand]) func() {
	t.Helper()
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	if _, err := queue.Enqueue(persistencequeue.PersistenceCommand{
		Owner: persistencequeue.OwnerEvent, Kind: "persona.test.blocker", SizeBytes: 1,
		Apply: func(context.Context) error {
			close(started)
			<-release
			return nil
		},
	}, 1); err != nil {
		t.Fatalf("enqueue blocking command: %v", err)
	}
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("queue worker did not reach the blocker")
	}
	return unblock
}

func shutdownPersonaPersistenceQueue(t *testing.T, queue *persistencequeue.Queue[persistencequeue.PersistenceCommand]) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := queue.Shutdown(ctx); err != nil && !errors.Is(err, persistencequeue.ErrFailed) {
		t.Errorf("shutdown Persona persistence queue: %v", err)
	}
}
