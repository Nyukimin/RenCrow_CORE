package persona

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	domainpersona "github.com/Nyukimin/RenCrow_CORE/internal/domain/persona"
)

type personaQueueBinding interface {
	SetPersistenceQueue(*persistencequeue.Queue[persistencequeue.PersistenceCommand]) error
	MatchesPersistenceQueue(*persistencequeue.Queue[persistencequeue.PersistenceCommand]) bool
}

func TestPersonaStoresRequireExactSharedQueueForAdmission(t *testing.T) {
	queue, err := persistencequeue.NewPersistenceQueue(4, 1<<20)
	if err != nil {
		t.Fatalf("NewPersistenceQueue(): %v", err)
	}
	otherQueue, err := persistencequeue.NewPersistenceQueue(4, 1<<20)
	if err != nil {
		t.Fatalf("NewPersistenceQueue() for mismatched owner: %v", err)
	}
	t.Cleanup(func() { shutdownPersonaPersistenceQueue(t, queue) })
	t.Cleanup(func() { shutdownPersonaPersistenceQueue(t, otherQueue) })

	sqliteStore, err := NewSQLiteStore(filepath.Join(t.TempDir(), "persona.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore(): %v", err)
	}
	t.Cleanup(func() {
		if err := sqliteStore.Close(); err != nil {
			t.Errorf("close SQLite store: %v", err)
		}
	})
	jsonlStore := NewJSONLStore(t.TempDir())

	for name, store := range map[string]personaQueueBinding{"SQLite": sqliteStore, "JSONL": jsonlStore} {
		t.Run(name, func(t *testing.T) {
			if store.MatchesPersistenceQueue(queue) || store.MatchesPersistenceQueue(nil) {
				t.Fatal("unconfigured store reported a persistence queue match")
			}
			if err := store.SetPersistenceQueue(nil); !errors.Is(err, ErrPersonaAdmissionUnavailable) {
				t.Fatalf("SetPersistenceQueue(nil) = %v, want unavailable", err)
			}

			admission, err := persistencequeue.Admit(otherQueue, 1, 4096)
			if err != nil {
				t.Fatalf("Admit(mismatched queue): %v", err)
			}
			defer admission.Release()
			ctx := persistencequeue.WithAdmission(context.Background(), admission)
			item := domainpersona.TriggerLog{EventID: "persona-mismatch", CharacterID: "mio", TriggerID: "safe", Activated: true, CreatedAt: time.Now().UTC()}
			if err := store.(interface {
				SaveTriggerLog(context.Context, domainpersona.TriggerLog) error
			}).SaveTriggerLog(ctx, item); !errors.Is(err, ErrPersonaAdmissionUnavailable) {
				t.Fatalf("unconfigured admitted SaveTriggerLog() = %v, want unavailable", err)
			}

			if err := store.SetPersistenceQueue(queue); err != nil {
				t.Fatalf("SetPersistenceQueue(): %v", err)
			}
			if !store.MatchesPersistenceQueue(queue) || store.MatchesPersistenceQueue(otherQueue) || store.MatchesPersistenceQueue(nil) {
				t.Fatal("configured store did not report only its exact queue")
			}
			if err := store.SetPersistenceQueue(queue); err != nil {
				t.Fatalf("idempotent SetPersistenceQueue(): %v", err)
			}
			if err := store.SetPersistenceQueue(otherQueue); !errors.Is(err, ErrPersonaAdmissionQueueMismatch) {
				t.Fatalf("SetPersistenceQueue(other queue) = %v, want mismatch", err)
			}
			if err := store.(interface {
				SaveTriggerLog(context.Context, domainpersona.TriggerLog) error
			}).SaveTriggerLog(ctx, item); !errors.Is(err, ErrPersonaAdmissionQueueMismatch) {
				t.Fatalf("mismatched admitted SaveTriggerLog() = %v, want mismatch", err)
			}
		})
	}
}

func TestJSONLStoreAdmittedWritesStayInRAMUntilWorkerAndOverlayReads(t *testing.T) {
	root := t.TempDir()
	store := NewJSONLStore(root)
	queue, err := persistencequeue.NewPersistenceQueue(8, 1<<20)
	if err != nil {
		t.Fatalf("NewPersistenceQueue(): %v", err)
	}
	t.Cleanup(func() { shutdownPersonaPersistenceQueue(t, queue) })
	releaseWorker := blockPersonaPersistenceQueueWorker(t, queue)
	if err := store.SetPersistenceQueue(queue); err != nil {
		t.Fatalf("SetPersistenceQueue(): %v", err)
	}

	admission, err := persistencequeue.Admit(queue, 2, 16<<10)
	if err != nil {
		t.Fatalf("Admit(): %v", err)
	}
	defer admission.Release()
	ctx := persistencequeue.WithAdmission(context.Background(), admission)
	item := domainpersona.ObservationLog{
		EventID: "evt_jsonl_ram_overlay", ObserverID: "mio", TargetID: "ren", ObservationType: "chat_message",
		Summary: "pending observation", Sensitivity: "normal", ReviewStatus: "pending", CreatedAt: time.Date(2026, 10, 24, 15, 0, 0, 0, time.UTC),
	}
	canonical := domainpersona.CanonicalResponseLog{
		EventID: "evt_jsonl_ram_canonical", CharacterID: "mio", ResponseKey: "danger", MessageID: "ram-canonical", Used: true, CreatedAt: item.CreatedAt,
	}
	started := make(chan error, 1)
	go func() { started <- store.SaveObservationLog(ctx, item) }()
	select {
	case err := <-started:
		if err != nil {
			t.Fatalf("SaveObservationLog(): %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		releaseWorker()
		if err := <-started; err != nil {
			t.Fatalf("SaveObservationLog() after release: %v", err)
		}
		t.Fatal("admitted JSONL save waited for the shared queue worker")
	}
	if err := store.SaveCanonicalResponseLog(ctx, canonical); err != nil {
		t.Fatalf("SaveCanonicalResponseLog(): %v", err)
	}

	path := filepath.Join(root, "observation_log.jsonl")
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("JSONL file before worker release exists or stat failed: %v", err)
	}
	if got, found, err := store.FindObservationLogByID(ctx, item.EventID); err != nil || !found || got.EventID != item.EventID {
		t.Fatalf("admission observation read = %#v, found=%v, err=%v; want RAM overlay", got, found, err)
	}
	blockedDirectory := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blockedDirectory, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("create test path blocker: %v", err)
	}
	canonicalPath := store.canonicalPath
	store.canonicalPath = filepath.Join(blockedDirectory, "canonical.jsonl")
	if got, err := store.ListCanonicalResponseLogs(ctx, 50); err != nil || len(got) != 1 || got[0].EventID != canonical.EventID {
		store.canonicalPath = canonicalPath
		t.Fatalf("admission canonical list = %#v, err=%v; want RAM snapshot plus overlay without file access", got, err)
	}
	store.canonicalPath = canonicalPath
	if _, found, err := store.FindObservationLogByID(context.Background(), item.EventID); err != nil || found {
		t.Fatalf("unadmitted observation read found=%v err=%v; want no pending row", found, err)
	}
	if err := store.SaveTriggerLog(ctx, domainpersona.TriggerLog{EventID: "persona-over-budget", CharacterID: "mio", TriggerID: "trigger", Activated: true, CreatedAt: item.CreatedAt}); !errors.Is(err, persistencequeue.ErrInvalidReservation) {
		t.Fatalf("save beyond reserved count = %v, want reservation rejection", err)
	}

	releaseWorker()
	if err := admission.Barrier(context.Background()); err != nil {
		t.Fatalf("Barrier(): %v", err)
	}
	rows, err := store.ListObservationLogs(context.Background(), 10)
	if err != nil || len(rows) != 1 || rows[0].EventID != item.EventID {
		t.Fatalf("durable JSONL rows = %#v, err=%v; want accepted row", rows, err)
	}
	canonicals, err := store.ListCanonicalResponseLogs(context.Background(), 10)
	if err != nil || len(canonicals) != 1 || canonicals[0].EventID != canonical.EventID {
		t.Fatalf("durable JSONL canonical rows = %#v, err=%v; want exact accepted row", canonicals, err)
	}
}

func TestJSONLAdmissionFailureIsLatchedAndDirectMetaApplyFailsClosed(t *testing.T) {
	root := t.TempDir()
	metaRoot := filepath.Join(t.TempDir(), "meta")
	store := NewJSONLStoreWithMetaRoot(root, metaRoot)
	queue, err := persistencequeue.NewPersistenceQueue(4, 1<<20)
	if err != nil {
		t.Fatalf("NewPersistenceQueue(): %v", err)
	}
	t.Cleanup(func() { shutdownPersonaPersistenceQueue(t, queue) })
	if err := store.SetPersistenceQueue(queue); err != nil {
		t.Fatalf("SetPersistenceQueue(): %v", err)
	}
	blockedDirectory := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blockedDirectory, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("create invalid JSONL parent: %v", err)
	}
	store.observationPath = filepath.Join(blockedDirectory, "observation.jsonl")
	releaseWorker := blockPersonaPersistenceQueueWorker(t, queue)
	admission, err := persistencequeue.Admit(queue, 2, 8192)
	if err != nil {
		t.Fatalf("Admit(): %v", err)
	}
	defer admission.Release()
	ctx := persistencequeue.WithAdmission(context.Background(), admission)
	item := domainpersona.ObservationLog{
		EventID: "evt_jsonl_failure", ObserverID: "mio", TargetID: "ren", ObservationType: "chat_message",
		Summary: "this JSONL append will fail in the worker", Sensitivity: "normal", ReviewStatus: "pending", CreatedAt: time.Now().UTC(),
	}
	if err := store.SaveObservationLog(ctx, item); err != nil {
		t.Fatalf("SaveObservationLog() should accept RAM command before worker failure: %v", err)
	}
	if _, found, err := store.FindObservationLogByID(ctx, item.EventID); err != nil || !found {
		t.Fatalf("pending observation read found=%v err=%v; want accepted overlay", found, err)
	}
	if _, err := store.ApplyMetaProfileUpdate(ctx, domainpersona.MetaProfileUpdate{
		UpdateID: "meta_jsonl_admitted", ObserverID: "mio", TargetID: "ren", Section: "flow", ProposedContent: "must not write synchronously",
		Sensitivity: "normal", ReviewStatus: "adopted", CreatedAt: time.Now().UTC(),
	}); !errors.Is(err, ErrPersonaAdmissionUnsupportedOperation) {
		t.Fatalf("admitted ApplyMetaProfileUpdate() = %v, want explicit unsupported result", err)
	}
	releaseWorker()
	if err := admission.Barrier(context.Background()); !errors.Is(err, persistencequeue.ErrFailed) {
		t.Fatalf("Barrier() = %v, want latched JSONL file failure", err)
	}
	if err := store.SaveTriggerLog(ctx, domainpersona.TriggerLog{EventID: "evt_jsonl_after_failure", CharacterID: "mio", TriggerID: "safe", Activated: true, CreatedAt: time.Now().UTC()}); !errors.Is(err, persistencequeue.ErrFailed) {
		t.Fatalf("SaveTriggerLog() after queue failure = %v, want immediate queue failure", err)
	}
}

func TestSQLiteAdmissionRejectsWritePastReservedCommandCount(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "persona-reservation.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore(): %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close SQLite store: %v", err)
		}
	})
	queue, err := persistencequeue.NewPersistenceQueue(4, 1<<20)
	if err != nil {
		t.Fatalf("NewPersistenceQueue(): %v", err)
	}
	t.Cleanup(func() { shutdownPersonaPersistenceQueue(t, queue) })
	if err := store.SetPersistenceQueue(queue); err != nil {
		t.Fatalf("SetPersistenceQueue(): %v", err)
	}
	admission, err := persistencequeue.Admit(queue, 1, 8192)
	if err != nil {
		t.Fatalf("Admit(): %v", err)
	}
	defer admission.Release()
	ctx := persistencequeue.WithAdmission(context.Background(), admission)
	now := time.Now().UTC()
	if err := store.SaveTriggerLog(ctx, domainpersona.TriggerLog{EventID: "evt_sqlite_reserved", CharacterID: "mio", TriggerID: "safe", Activated: true, CreatedAt: now}); err != nil {
		t.Fatalf("first reserved save: %v", err)
	}
	if err := store.SaveObservationLog(ctx, domainpersona.ObservationLog{
		EventID: "evt_sqlite_unreserved", ObserverID: "mio", TargetID: "ren", ObservationType: "chat_message", Summary: "must be rejected", Sensitivity: "normal", ReviewStatus: "pending", CreatedAt: now,
	}); !errors.Is(err, persistencequeue.ErrInvalidReservation) {
		t.Fatalf("save beyond reserved command count = %v, want reservation rejection", err)
	}
	if err := admission.Barrier(context.Background()); err != nil {
		t.Fatalf("Barrier(): %v", err)
	}
}

func TestSQLiteStorageHostMutationDoesNotPretendToUseAsyncAdmission(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "persona-storagehost-admission.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore(): %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close SQLite store: %v", err)
		}
	})
	queue, err := persistencequeue.NewPersistenceQueue(2, 1<<20)
	if err != nil {
		t.Fatalf("NewPersistenceQueue(): %v", err)
	}
	t.Cleanup(func() { shutdownPersonaPersistenceQueue(t, queue) })
	if err := store.SetPersistenceQueue(queue); err != nil {
		t.Fatalf("SetPersistenceQueue(): %v", err)
	}
	admission, err := persistencequeue.Admit(queue, 1, 8192)
	if err != nil {
		t.Fatalf("Admit(): %v", err)
	}
	defer admission.Release()
	ctx := persistencequeue.WithAdmission(context.Background(), admission)
	item := domainpersona.CanonicalResponseLog{
		EventID: "evt_persona_storagehost_admission", CharacterID: "mio", ResponseKey: "danger", MessageID: "msg", Used: true,
		CreatedAt: time.Date(2026, 10, 24, 15, 20, 0, 0, time.UTC),
	}
	identity := personaReceiptTestIdentity("persona-admission-storagehost", PersonaSaveCanonicalResponseLogStorageHostOperation, item, 1)
	if _, err := store.SaveCanonicalResponseLogForStorageHostOperation(ctx, identity, item); !errors.Is(err, ErrPersonaAdmissionUnsupportedOperation) {
		t.Fatalf("storage-host save with Admission = %v, want explicit unsupported error", err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM canonical_response_log WHERE event_id = ?`, item.EventID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("canonical row count=%d err=%v; admitted storage-host call must not write synchronously", count, err)
	}
}

func TestSQLiteCanonicalAdmissionReadUsesWarmSnapshotWhileQueuedWriteHoldsWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "persona-warm-snapshot.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(): %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close SQLite store: %v", err)
		}
	})
	baseline := domainpersona.CanonicalResponseLog{
		EventID: "evt_persona_canonical_baseline", CharacterID: "mio", ResponseKey: "greeting", MessageID: "baseline-msg", Used: true,
		CreatedAt: time.Date(2026, 10, 24, 15, 10, 0, 0, time.UTC),
	}
	if err := store.SaveCanonicalResponseLog(context.Background(), baseline); err != nil {
		t.Fatalf("save durable baseline: %v", err)
	}

	queue, err := persistencequeue.NewPersistenceQueue(4, 1<<20)
	if err != nil {
		t.Fatalf("NewPersistenceQueue(): %v", err)
	}
	t.Cleanup(func() { shutdownPersonaPersistenceQueue(t, queue) })
	if err := store.SetPersistenceQueue(queue); err != nil {
		t.Fatalf("SetPersistenceQueue(): %v", err)
	}
	releaseWorker := blockPersonaPersistenceQueueWorker(t, queue)

	lockDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open lock connection: %v", err)
	}
	lockDB.SetMaxOpenConns(1)
	lockConn, err := lockDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("get lock connection: %v", err)
	}
	if _, err := lockConn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("begin writer lock: %v", err)
	}
	unlockOnce := sync.Once{}
	unlockDisk := func() {
		unlockOnce.Do(func() {
			_, _ = lockConn.ExecContext(context.Background(), "ROLLBACK")
			_ = lockConn.Close()
			_ = lockDB.Close()
		})
	}
	t.Cleanup(unlockDisk)

	admission, err := persistencequeue.Admit(queue, 1, 8192)
	if err != nil {
		t.Fatalf("Admit(): %v", err)
	}
	defer admission.Release()
	ctx := persistencequeue.WithAdmission(context.Background(), admission)
	pending := domainpersona.CanonicalResponseLog{
		EventID: "evt_persona_canonical_pending", CharacterID: "mio", ResponseKey: "danger", MessageID: "pending-msg", Used: true,
		CreatedAt: time.Date(2026, 10, 24, 15, 11, 0, 0, time.UTC),
	}
	if err := store.SaveCanonicalResponseLog(ctx, pending); err != nil {
		t.Fatalf("admitted canonical save: %v", err)
	}
	store.db.SetMaxOpenConns(1)
	releaseWorker()
	deadline := time.Now().Add(time.Second)
	for store.db.Stats().InUse == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if store.db.Stats().InUse == 0 {
		unlockDisk()
		t.Fatal("queue worker did not enter SQLite write while external writer lock was held")
	}

	read := make(chan struct {
		items []domainpersona.CanonicalResponseLog
		err   error
	}, 1)
	go func() {
		items, err := store.ListCanonicalResponseLogs(ctx, 50)
		read <- struct {
			items []domainpersona.CanonicalResponseLog
			err   error
		}{items: items, err: err}
	}()
	select {
	case result := <-read:
		if result.err != nil || len(result.items) != 2 || result.items[0].EventID != pending.EventID || result.items[1].EventID != baseline.EventID {
			t.Fatalf("canonical read = %#v, err=%v; want pending overlay then durable warm snapshot", result.items, result.err)
		}
	case <-time.After(250 * time.Millisecond):
		unlockDisk()
		if err := admission.Barrier(context.Background()); err != nil {
			t.Fatalf("drain after blocked canonical read: %v", err)
		}
		<-read
		t.Fatal("admitted canonical list waited for the in-flight SQLite writer")
	}

	unlockDisk()
	if err := admission.Barrier(context.Background()); err != nil {
		t.Fatalf("Barrier(): %v", err)
	}
	durable, err := store.ListCanonicalResponseLogs(context.Background(), 50)
	if err != nil || len(durable) != 2 || durable[0].EventID != pending.EventID || durable[1].EventID != baseline.EventID {
		t.Fatalf("durable canonical list = %#v, err=%v; want exact persisted rows", durable, err)
	}
}

func TestPersonaSynchronousSaveWaitsForAcceptedAdmissionWrite(t *testing.T) {
	for _, kind := range []string{"SQLite", "JSONL"} {
		t.Run(kind, func(t *testing.T) {
			queue, err := persistencequeue.NewPersistenceQueue(4, 1<<20)
			if err != nil {
				t.Fatalf("NewPersistenceQueue(): %v", err)
			}
			t.Cleanup(func() { shutdownPersonaPersistenceQueue(t, queue) })
			releaseWorker := blockPersonaPersistenceQueueWorker(t, queue)

			root := t.TempDir()
			var save func(context.Context, domainpersona.ObservationLog) error
			var closeStore func() error
			switch kind {
			case "SQLite":
				store, err := NewSQLiteStore(filepath.Join(root, "persona.db"))
				if err != nil {
					t.Fatalf("NewSQLiteStore(): %v", err)
				}
				closeStore = store.Close
				save = store.SaveObservationLog
				if err := store.SetPersistenceQueue(queue); err != nil {
					t.Fatalf("SetPersistenceQueue(): %v", err)
				}
			case "JSONL":
				store := NewJSONLStore(root)
				closeStore = func() error { return nil }
				save = store.SaveObservationLog
				if err := store.SetPersistenceQueue(queue); err != nil {
					t.Fatalf("SetPersistenceQueue(): %v", err)
				}
			}
			t.Cleanup(func() {
				if err := closeStore(); err != nil {
					t.Errorf("close store: %v", err)
				}
			})

			admission, err := persistencequeue.Admit(queue, 1, 4096)
			if err != nil {
				t.Fatalf("Admit(): %v", err)
			}
			defer admission.Release()
			first := domainpersona.ObservationLog{EventID: "evt_persona_async_first", ObserverID: "mio", TargetID: "ren", ObservationType: "chat_message", Summary: "first", Sensitivity: "normal", ReviewStatus: "pending", CreatedAt: time.Now().UTC()}
			second := domainpersona.ObservationLog{EventID: "evt_persona_sync_second", ObserverID: "mio", TargetID: "ren", ObservationType: "chat_message", Summary: "second", Sensitivity: "normal", ReviewStatus: "pending", CreatedAt: time.Now().UTC().Add(time.Second)}
			if err := save(persistencequeue.WithAdmission(context.Background(), admission), first); err != nil {
				t.Fatalf("admitted save: %v", err)
			}
			finished := make(chan error, 1)
			go func() { finished <- save(context.Background(), second) }()
			select {
			case err := <-finished:
				releaseWorker()
				if err != nil {
					t.Fatalf("synchronous save: %v", err)
				}
				t.Fatal("synchronous save overtook a queued Persona write")
			case <-time.After(50 * time.Millisecond):
			}
			releaseWorker()
			select {
			case err := <-finished:
				if err != nil {
					t.Fatalf("synchronous save after queue drain: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("synchronous save did not resume after queue drain")
			}
			if err := admission.Barrier(context.Background()); err != nil {
				t.Fatalf("Barrier(): %v", err)
			}
		})
	}
}
