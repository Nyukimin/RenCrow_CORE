package worldoutbox

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	appprojection "github.com/Nyukimin/RenCrow_CORE/internal/application/worldprojection"
	domainprojection "github.com/Nyukimin/RenCrow_CORE/internal/domain/worldprojection"
)

func testTarget() domainprojection.Target {
	return domainprojection.Target{
		WorldID:          "world-house",
		TimelineID:       "timeline-main",
		TimelineEpoch:    "epoch-1",
		ActorID:          "mio",
		ActorInstanceID:  "instance-mio",
		ActorTransportID: "mio",
	}
}

func testRecord(id string, revision uint64, digest string) appprojection.OutboxRecord {
	if digest == "" {
		sum := sha256.Sum256([]byte(id))
		digest = hex.EncodeToString(sum[:])
	}
	return appprojection.OutboxRecord{
		ID:             id,
		Target:         testTarget(),
		Kind:           domainprojection.KindWorkSet,
		Envelope:       []byte(`{"record":"` + id + `"}`),
		Digest:         digest,
		SourceEpoch:    "core-epoch-1",
		SourceRevision: revision,
	}
}

func newTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "world-outbox.sqlite"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestOutboxEnqueueIsIdempotentForSameRevisionAndDigest(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	record := testRecord("one", 1, "")
	if err := store.Enqueue(context.Background(), record, now); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	record.Envelope = []byte(`{"changed":true}`) // digest is still the authority
	if err := store.Enqueue(context.Background(), record, now.Add(time.Minute)); err != nil {
		t.Fatalf("Enqueue() retry error = %v", err)
	}
	got, err := store.Get(context.Background(), "one")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Status != appprojection.OutboxPending || string(got.Envelope) != `{"record":"one"}` {
		t.Fatalf("record = %+v", got)
	}
}

func TestOutboxRejectsSameRevisionDifferentDigest(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	if err := store.Enqueue(context.Background(), testRecord("one", 1, strings.Repeat("a", 64)), now); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	err := store.Enqueue(context.Background(), testRecord("two", 1, strings.Repeat("b", 64)), now)
	if !errors.Is(err, appprojection.ErrRevisionConflict) {
		t.Fatalf("Enqueue() error = %v, want ErrRevisionConflict", err)
	}
}

func TestOutboxRejectsOlderRevision(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	if err := store.Enqueue(context.Background(), testRecord("two", 2, ""), now); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	err := store.Enqueue(context.Background(), testRecord("one", 1, ""), now)
	if !errors.Is(err, appprojection.ErrStaleRevision) {
		t.Fatalf("Enqueue() error = %v, want ErrStaleRevision", err)
	}
}

func TestOutboxEpochChangeRequiresReconcileAndSupersedesOldPending(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	if err := store.Enqueue(context.Background(), testRecord("old", 3, ""), now); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	next := testRecord("new", 1, "")
	next.SourceEpoch = "core-epoch-2"
	err := store.Enqueue(context.Background(), next, now)
	if !errors.Is(err, appprojection.ErrEpochChangeRequiresReconcile) {
		t.Fatalf("Enqueue() error = %v, want ErrEpochChangeRequiresReconcile", err)
	}
	next.Reconcile = true
	if err := store.Enqueue(context.Background(), next, now); err != nil {
		t.Fatalf("Enqueue(reconcile) error = %v", err)
	}
	old, err := store.Get(context.Background(), "old")
	if err != nil {
		t.Fatalf("Get(old) error = %v", err)
	}
	if old.Status != appprojection.OutboxSuperseded {
		t.Fatalf("old status = %q", old.Status)
	}
	claimed, err := store.Claim(context.Background(), now.Add(time.Second), 10)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if len(claimed) != 1 || claimed[0].ID != "new" {
		t.Fatalf("claimed = %+v", claimed)
	}
}

func TestSourceRevisionIsMonotonicAndPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "world-outbox.sqlite")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	for want := uint64(1); want <= 3; want++ {
		got, err := store.NextSourceRevision(context.Background(), testTarget(), domainprojection.KindWorkSet, "core-epoch-1", now)
		if err != nil {
			t.Fatalf("NextSourceRevision() error = %v", err)
		}
		if got != want {
			t.Fatalf("revision = %d, want %d", got, want)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("reopen NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	got, err := store.NextSourceRevision(context.Background(), testTarget(), domainprojection.KindWorkSet, "core-epoch-1", now)
	if err != nil {
		t.Fatalf("NextSourceRevision() after reopen error = %v", err)
	}
	if got != 4 {
		t.Fatalf("revision after reopen = %d, want 4", got)
	}
	err = func() error {
		_, err := store.NextSourceRevision(context.Background(), testTarget(), domainprojection.KindWorkSet, "core-epoch-2", now)
		return err
	}()
	if !errors.Is(err, appprojection.ErrEpochChangeRequiresReconcile) {
		t.Fatalf("epoch change error = %v, want ErrEpochChangeRequiresReconcile", err)
	}
}

func TestSourceRevisionRecoversFromLatestOutboxWhenCounterIsMissing(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	first, err := store.NextSourceRevision(context.Background(), testTarget(), domainprojection.KindWorkSet, "core-epoch-1", now)
	if err != nil || first != 1 {
		t.Fatalf("first revision = %d, err = %v", first, err)
	}
	if err := store.Enqueue(context.Background(), testRecord("one", first, ""), now); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	if _, err := store.db.Exec(`DELETE FROM world_projection_source_revision
		WHERE world_id = ? AND timeline_id = ? AND timeline_epoch = ? AND actor_id = ? AND kind = ?`,
		testTarget().WorldID, testTarget().TimelineID, testTarget().TimelineEpoch,
		testTarget().ActorID, domainprojection.KindWorkSet); err != nil {
		t.Fatalf("delete counter row: %v", err)
	}
	if _, err := store.NextSourceRevision(context.Background(), testTarget(), domainprojection.KindWorkSet, "core-epoch-2", now); !errors.Is(err, appprojection.ErrEpochChangeRequiresReconcile) {
		t.Fatalf("epoch change after recovery error = %v", err)
	}
	next, err := store.NextSourceRevision(context.Background(), testTarget(), domainprojection.KindWorkSet, "core-epoch-1", now)
	if err != nil {
		t.Fatalf("NextSourceRevision() after delete error = %v", err)
	}
	if next != 2 {
		t.Fatalf("revision after delete = %d, want 2", next)
	}
}

func TestOutboxLatestReturnsNewestRecord(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	if err := store.Enqueue(context.Background(), testRecord("one", 1, ""), now); err != nil {
		t.Fatalf("Enqueue(one) error = %v", err)
	}
	if err := store.Enqueue(context.Background(), testRecord("two", 2, ""), now); err != nil {
		t.Fatalf("Enqueue(two) error = %v", err)
	}
	record, ok, err := store.Latest(context.Background(), testTarget(), domainprojection.KindWorkSet)
	if err != nil {
		t.Fatalf("Latest() error = %v", err)
	}
	if !ok || record.ID != "two" || record.SourceRevision != 2 {
		t.Fatalf("Latest() = %+v, ok = %v", record, ok)
	}
	if _, ok, err := store.Latest(context.Background(), testTarget(), domainprojection.KindLifePolicy); err != nil || ok {
		t.Fatalf("Latest(LifePolicy) ok = %v, err = %v", ok, err)
	}
}

func TestSourceRevisionMigratesFromV1Outbox(t *testing.T) {
	path := filepath.Join(t.TempDir(), "world-outbox.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE world_projection_outbox (
		id TEXT PRIMARY KEY NOT NULL, world_id TEXT NOT NULL, timeline_id TEXT NOT NULL,
		timeline_epoch TEXT NOT NULL, actor_id TEXT NOT NULL, actor_instance_id TEXT NOT NULL,
		actor_transport_id TEXT NOT NULL, kind TEXT NOT NULL, source_epoch TEXT NOT NULL,
		source_revision INTEGER NOT NULL, digest TEXT NOT NULL, envelope BLOB NOT NULL,
		reconcile INTEGER NOT NULL, status TEXT NOT NULL, attempts INTEGER NOT NULL DEFAULT 0,
		next_attempt_at TEXT NOT NULL DEFAULT '', last_error TEXT NOT NULL DEFAULT '',
		ack_status TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
		sent_at TEXT NOT NULL DEFAULT '',
		UNIQUE(world_id, timeline_id, timeline_epoch, actor_id, kind, source_epoch, source_revision))`); err != nil {
		t.Fatalf("create v1 outbox: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO world_projection_outbox
		(id, world_id, timeline_id, timeline_epoch, actor_id, actor_instance_id, actor_transport_id,
		 kind, source_epoch, source_revision, digest, envelope, reconcile, status, created_at, updated_at)
		VALUES ('old', 'world-house', 'timeline-main', 'epoch-1', 'mio', 'instance-mio', 'mio',
		 'full_actor_work_set', 'core-epoch-1', 5, 'digest', '{}', 0, 'sent',
		 '2026-10-20T00:00:00.000000000Z', '2026-10-20T00:00:00.000000000Z')`); err != nil {
		t.Fatalf("insert v1 row: %v", err)
	}
	if _, err := db.Exec("PRAGMA user_version = 1"); err != nil {
		t.Fatalf("set v1 version: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(v1) error = %v", err)
	}
	defer store.Close()
	record, ok, err := store.Latest(context.Background(), testTarget(), domainprojection.KindWorkSet)
	if err != nil || !ok || record.ID != "old" {
		t.Fatalf("Latest() = %+v, ok = %v, err = %v", record, ok, err)
	}
	got, err := store.NextSourceRevision(context.Background(), testTarget(), domainprojection.KindWorkSet, "core-epoch-1", time.Now().UTC())
	if err != nil {
		t.Fatalf("NextSourceRevision() error = %v", err)
	}
	if got != 6 {
		t.Fatalf("revision = %d, want 6", got)
	}
}

func TestOutboxClaimCompleteAndPersistAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "world-outbox.sqlite")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	if err := store.Enqueue(context.Background(), testRecord("one", 1, ""), now); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	claimed, err := store.Claim(context.Background(), now, 10)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if len(claimed) != 1 || claimed[0].Status != appprojection.OutboxInFlight {
		t.Fatalf("claimed = %+v", claimed)
	}
	if again, err := store.Claim(context.Background(), now, 10); err != nil || len(again) != 0 {
		t.Fatalf("second Claim() = %+v, %v", again, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer store.Close()
	if err := store.Complete(context.Background(), "one", appprojection.PublishResult{Status: "accepted"}, now.Add(time.Second)); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	got, err := store.Get(context.Background(), "one")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Status != appprojection.OutboxSent || got.AckStatus != "accepted" || got.SentAt.IsZero() {
		t.Fatalf("record = %+v", got)
	}
}

func TestOutboxReclaimsStaleInFlightRecord(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	if err := store.Enqueue(context.Background(), testRecord("stale", 1, ""), now); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	first, err := store.Claim(context.Background(), now, 10)
	if err != nil || len(first) != 1 {
		t.Fatalf("first Claim() = %+v, %v", first, err)
	}
	if second, err := store.Claim(context.Background(), now.Add(time.Minute), 10); err != nil || len(second) != 0 {
		t.Fatalf("early Claim() = %+v, %v", second, err)
	}
	reclaimed, err := store.Claim(context.Background(), now.Add(10*time.Minute), 10)
	if err != nil {
		t.Fatalf("stale Claim() error = %v", err)
	}
	if len(reclaimed) != 1 || reclaimed[0].ID != "stale" || reclaimed[0].Status != appprojection.OutboxInFlight {
		t.Fatalf("reclaimed = %+v", reclaimed)
	}
}

func TestOutboxFailSchedulesRetryOrTerminal(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	if err := store.Enqueue(context.Background(), testRecord("retry", 1, ""), now); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	if _, err := store.Claim(context.Background(), now, 10); err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	retryAt := now.Add(2 * time.Minute)
	if err := store.Fail(context.Background(), "retry", retryAt, false, "temporary", now); err != nil {
		t.Fatalf("Fail() error = %v", err)
	}
	got, err := store.Get(context.Background(), "retry")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Status != appprojection.OutboxPending || got.Attempts != 1 || !got.NextAttemptAt.Equal(retryAt) || got.LastError != "temporary" {
		t.Fatalf("record = %+v", got)
	}
	if err := store.Enqueue(context.Background(), testRecord("terminal", 2, ""), now); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	if _, err := store.Claim(context.Background(), now, 10); err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if err := store.Fail(context.Background(), "terminal", time.Time{}, true, "conflict", now); err != nil {
		t.Fatalf("Fail(terminal) error = %v", err)
	}
	got, err = store.Get(context.Background(), "terminal")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Status != appprojection.OutboxFailed || got.Attempts != 1 {
		t.Fatalf("record = %+v", got)
	}
}

func TestOutboxRejectsInvalidRecord(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	for name, mutate := range map[string]func(*appprojection.OutboxRecord){
		"missing id":      func(r *appprojection.OutboxRecord) { r.ID = "" },
		"missing kind":    func(r *appprojection.OutboxRecord) { r.Kind = "" },
		"missing epoch":   func(r *appprojection.OutboxRecord) { r.SourceEpoch = "" },
		"missing digest":  func(r *appprojection.OutboxRecord) { r.Digest = "" },
		"bad revision":    func(r *appprojection.OutboxRecord) { r.SourceRevision = 0 },
		"missing payload": func(r *appprojection.OutboxRecord) { r.Envelope = nil },
	} {
		record := testRecord("bad", 1, "")
		mutate(&record)
		if err := store.Enqueue(context.Background(), record, now); err == nil {
			t.Fatalf("%s: Enqueue() error = nil", name)
		}
	}
}

func TestWorkerPublishesDurableOutboxToHTTPWorld(t *testing.T) {
	var (
		mu       sync.Mutex
		received []byte
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		received = append([]byte(nil), body...)
		mu.Unlock()
		if r.Method != http.MethodPut {
			t.Errorf("method = %s", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema_version":1,"projection":{"status":"accepted"}}`))
	}))
	defer server.Close()

	target := testTarget()
	workSet, err := domainprojection.NewWorkSet("world-house", "mio", "core-epoch-1", 1,
		time.Date(2026, 10, 20, 13, 0, 0, 0, time.UTC), []domainprojection.WorkItem{})
	if err != nil {
		t.Fatalf("NewWorkSet() error = %v", err)
	}
	env, err := domainprojection.NewEnvelope(target, domainprojection.KindWorkSet, workSet)
	if err != nil {
		t.Fatalf("NewEnvelope() error = %v", err)
	}
	raw, err := env.Marshal()
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	store := newTestStore(t)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	record := appprojection.OutboxRecord{
		ID:             "delivery",
		Target:         target,
		Kind:           domainprojection.KindWorkSet,
		Envelope:       raw,
		Digest:         env.Digest,
		SourceEpoch:    "core-epoch-1",
		SourceRevision: 1,
	}
	if err := store.Enqueue(context.Background(), record, now); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	publisher, err := appprojection.NewHTTPPublisher(server.URL, "secret", 5*time.Second)
	if err != nil {
		t.Fatalf("NewHTTPPublisher() error = %v", err)
	}
	worker := &appprojection.Worker{Outbox: store, Publisher: publisher, Now: func() time.Time { return now }, Limit: 10}
	result, err := worker.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result.Sent != 1 || result.Retried != 0 || result.Failed != 0 {
		t.Fatalf("result = %+v", result)
	}
	mu.Lock()
	got := string(received)
	mu.Unlock()
	if got != string(raw) {
		t.Fatalf("received = %s, want %s", got, raw)
	}
	stored, err := store.Get(context.Background(), "delivery")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if stored.Status != appprojection.OutboxSent || stored.AckStatus != "accepted" {
		t.Fatalf("stored = %+v", stored)
	}
}
