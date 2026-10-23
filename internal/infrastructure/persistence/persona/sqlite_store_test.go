package persona

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	domainpersona "github.com/Nyukimin/RenCrow_CORE/internal/domain/persona"
)

func TestSQLiteStoreConfiguresSerializedBusyTimeout(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "persona.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	if got := store.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", got)
	}
	var busyTimeout int
	if err := store.db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("busy_timeout query failed: %v", err)
	}
	if busyTimeout != sqliteBusyTimeoutMilliseconds {
		t.Fatalf("busy_timeout = %d, want %d", busyTimeout, sqliteBusyTimeoutMilliseconds)
	}
}

func TestSQLiteStoreConfiguresWALPragmasForEveryConnection(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "persona.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	if got := store.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", got)
	}
	store.db.SetMaxIdleConns(0)
	for connection := 0; connection < 2; connection++ {
		var walAutoCheckpoint int
		if err := store.db.QueryRow(`PRAGMA wal_autocheckpoint`).Scan(&walAutoCheckpoint); err != nil {
			t.Fatalf("connection %d wal_autocheckpoint: %v", connection, err)
		}
		if walAutoCheckpoint != 0 {
			t.Fatalf("connection %d wal_autocheckpoint = %d, want 0 to keep checkpoints off the append path", connection, walAutoCheckpoint)
		}
		var journalMode string
		if err := store.db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
			t.Fatalf("connection %d journal_mode: %v", connection, err)
		}
		if !strings.EqualFold(journalMode, "wal") {
			t.Fatalf("connection %d journal_mode = %q, want wal", connection, journalMode)
		}
		var synchronous int
		if err := store.db.QueryRow(`PRAGMA synchronous`).Scan(&synchronous); err != nil {
			t.Fatalf("connection %d synchronous: %v", connection, err)
		}
		if synchronous != 1 {
			t.Fatalf("connection %d synchronous = %d, want NORMAL (1)", connection, synchronous)
		}
	}
}

func TestSQLiteStoreConcurrentObservationWrites(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "persona.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	const workers = 8
	errs := make(chan error, workers+4)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- store.SaveObservationLog(context.Background(), domainpersona.ObservationLog{
				EventID:         fmt.Sprintf("concurrent-observation-%d", i),
				ObserverID:      "lumina",
				TargetID:        "ren",
				ObservationType: "daily",
				Sensitivity:     "normal",
				ReviewStatus:    "pending",
				CreatedAt:       time.Now().UTC(),
			})
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := store.checkpointWorker.Checkpoint(context.Background()); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent SaveObservationLog failed: %v", err)
		}
	}
	items, err := store.ListObservationLogs(context.Background(), workers)
	if err != nil || len(items) != workers {
		t.Fatalf("concurrent observation count = %d, err=%v; want %d", len(items), err, workers)
	}
}

func TestSQLiteStoreReopensSavedPersonaDataAfterFinalCheckpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "persona-reopen.db")
	first, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	item := domainpersona.ObservationLog{
		EventID:         "reopen-observation",
		ObserverID:      "lumina",
		TargetID:        "ren",
		ObservationType: "daily",
		Summary:         "persist through checkpoint and close",
		Sensitivity:     "normal",
		ReviewStatus:    "pending",
		CreatedAt:       time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC),
	}
	if err := first.SaveObservationLog(context.Background(), item); err != nil {
		_ = first.Close()
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("repeated Close() = %v, want nil", err)
	}
	status := first.CheckpointStatus()
	if status.LastSuccessAt.IsZero() || !status.CheckpointAttempted {
		t.Fatalf("closed checkpoint status = %+v, want final success", status)
	}

	second, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	items, err := second.ListObservationLogs(context.Background(), 10)
	if err != nil || len(items) != 1 || items[0].EventID != item.EventID {
		t.Fatalf("reopened observations=%#v err=%v, want saved observation", items, err)
	}
}

func TestSQLiteStorePersonaLogs(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "persona.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	if err := store.SaveDiscomfortLog(ctx, domainpersona.DiscomfortLog{
		EventID:     "evt_discomfort_1",
		CharacterID: "mio",
		Discomfort:  "期待より軽すぎた",
		Status:      "candidate",
		CreatedAt:   now,
	}); err != nil {
		t.Fatalf("SaveDiscomfortLog() error = %v", err)
	}
	if err := store.SaveTriggerLog(ctx, domainpersona.TriggerLog{
		EventID:     "evt_trigger_1",
		CharacterID: "kuro",
		TriggerID:   "danger",
		Activated:   true,
		Confidence:  0.7,
		CreatedAt:   now,
	}); err != nil {
		t.Fatalf("SaveTriggerLog() error = %v", err)
	}
	if err := store.SaveCanonicalResponseLog(ctx, domainpersona.CanonicalResponseLog{
		EventID:     "evt_canonical_1",
		CharacterID: "kuro",
		ResponseKey: "block_destructive",
		Used:        true,
		CreatedAt:   now,
	}); err != nil {
		t.Fatalf("SaveCanonicalResponseLog() error = %v", err)
	}
	if err := store.SaveObservationLog(ctx, domainpersona.ObservationLog{
		EventID:         "evt_observation_1",
		ObserverID:      "lumina",
		TargetID:        "ren",
		ObservationType: "daily",
		Summary:         "観測候補",
		Sensitivity:     "normal",
		ReviewStatus:    "pending",
		CreatedAt:       now,
	}); err != nil {
		t.Fatalf("SaveObservationLog() error = %v", err)
	}
	if err := store.SaveMetaProfileUpdate(ctx, domainpersona.MetaProfileUpdate{
		UpdateID:        "meta_upd_1",
		ObserverID:      "lumina",
		TargetID:        "ren",
		Section:         "Risk Signs",
		ProposedContent: "疲労時は判断を急がない方がよい",
		Sensitivity:     "health",
		ReviewStatus:    "pending",
		CreatedAt:       now,
	}); err != nil {
		t.Fatalf("SaveMetaProfileUpdate() error = %v", err)
	}
	if err := store.SaveInterfaceSession(ctx, domainpersona.InterfaceSession{
		SessionID:     "persona_session_1",
		CharacterID:   "mio",
		InterfaceType: "web",
		SessionKey:    "web:viewer",
		CreatedAt:     now,
	}); err != nil {
		t.Fatalf("SaveInterfaceSession() error = %v", err)
	}

	discomforts, err := store.ListDiscomfortLogs(ctx, 10)
	if err != nil || len(discomforts) != 1 || discomforts[0].EventID != "evt_discomfort_1" {
		t.Fatalf("discomforts=%#v err=%v", discomforts, err)
	}
	triggers, err := store.ListTriggerLogs(ctx, 10)
	if err != nil || len(triggers) != 1 || triggers[0].TriggerID != "danger" {
		t.Fatalf("triggers=%#v err=%v", triggers, err)
	}
	canonicals, err := store.ListCanonicalResponseLogs(ctx, 10)
	if err != nil || len(canonicals) != 1 || canonicals[0].ResponseKey != "block_destructive" {
		t.Fatalf("canonicals=%#v err=%v", canonicals, err)
	}
	observations, err := store.ListObservationLogs(ctx, 10)
	if err != nil || len(observations) != 1 || observations[0].ReviewStatus != "pending" {
		t.Fatalf("observations=%#v err=%v", observations, err)
	}
	metaUpdates, err := store.ListMetaProfileUpdates(ctx, 10)
	if err != nil || len(metaUpdates) != 1 || metaUpdates[0].UpdateID != "meta_upd_1" {
		t.Fatalf("metaUpdates=%#v err=%v", metaUpdates, err)
	}
	sessions, err := store.ListInterfaceSessions(ctx, 10)
	if err != nil || len(sessions) != 1 || sessions[0].SessionKey != "web:viewer" {
		t.Fatalf("sessions=%#v err=%v", sessions, err)
	}
}

func TestSQLiteStoreRejectsSensitiveAutoAdoptedObservation(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "persona.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	err = store.SaveObservationLog(context.Background(), domainpersona.ObservationLog{
		EventID:         "evt_observation_1",
		ObserverID:      "lumina",
		TargetID:        "ren",
		ObservationType: "daily",
		Sensitivity:     "health",
		ReviewStatus:    "adopted",
		CreatedAt:       time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("expected sensitive auto-adopted observation to fail")
	}
}

func TestSQLiteStoreFindObservationLogByIDUsesExactPrimaryKey(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "persona.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	item := domainpersona.ObservationLog{
		EventID:         "event-exact",
		ObserverID:      "lumina",
		TargetID:        "ren",
		ObservationType: "daily",
		Summary:         "stored",
		Sensitivity:     "normal",
		ReviewStatus:    "pending",
		CreatedAt:       time.Date(2026, 8, 14, 1, 2, 3, 0, time.UTC),
	}
	if err := store.SaveObservationLog(ctx, item); err != nil {
		t.Fatalf("SaveObservationLog() failed: %v", err)
	}

	got, found, err := store.FindObservationLogByID(ctx, item.EventID)
	if err != nil || !found || !reflect.DeepEqual(got, item) {
		t.Fatalf("FindObservationLogByID() = %#v, found=%v, err=%v", got, found, err)
	}
	if got, found, err := store.FindObservationLogByID(ctx, "event-exact-suffix"); err != nil || found || !reflect.DeepEqual(got, domainpersona.ObservationLog{}) {
		t.Fatalf("suffix FindObservationLogByID() = %#v, found=%v, err=%v", got, found, err)
	}
}

func TestSQLiteStoreFindObservationLogByIDRejectsMalformedPayload(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "persona.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	item := domainpersona.ObservationLog{
		EventID:         "event-malformed",
		ObserverID:      "lumina",
		TargetID:        "ren",
		ObservationType: "daily",
		Sensitivity:     "normal",
		ReviewStatus:    "pending",
		CreatedAt:       time.Date(2026, 8, 14, 1, 2, 3, 0, time.UTC),
	}
	if err := store.SaveObservationLog(ctx, item); err != nil {
		t.Fatalf("SaveObservationLog() failed: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE observation_log SET payload = ? WHERE event_id = ?`, "{malformed}", item.EventID); err != nil {
		t.Fatalf("corrupt observation payload: %v", err)
	}
	if _, found, err := store.FindObservationLogByID(ctx, item.EventID); err == nil || found {
		t.Fatalf("expected malformed observation payload error, found=%v err=%v", found, err)
	}
}
