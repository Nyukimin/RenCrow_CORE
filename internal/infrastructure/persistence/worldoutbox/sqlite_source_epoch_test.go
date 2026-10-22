package worldoutbox

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	appprojection "github.com/Nyukimin/RenCrow_CORE/internal/application/worldprojection"
	domainprojection "github.com/Nyukimin/RenCrow_CORE/internal/domain/worldprojection"
)

// D-06: CORE's source epoch switch must be durable. The projector reads the
// current durable epoch before allocating a revision, so an authorized
// reconcile enqueue has to move the same counter NextSourceRevision uses.
func TestCurrentSourceRevisionReportsDurableEpoch(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()

	_, _, found, err := store.CurrentSourceRevision(context.Background(), testTarget(), domainprojection.KindWorkSet)
	if err != nil {
		t.Fatalf("CurrentSourceRevision() error = %v", err)
	}
	if found {
		t.Fatal("CurrentSourceRevision() found = true for an unused destination")
	}

	revision, err := store.NextSourceRevision(context.Background(), testTarget(), domainprojection.KindWorkSet, "core-epoch-1", now)
	if err != nil {
		t.Fatalf("NextSourceRevision() error = %v", err)
	}
	epoch, current, found, err := store.CurrentSourceRevision(context.Background(), testTarget(), domainprojection.KindWorkSet)
	if err != nil {
		t.Fatalf("CurrentSourceRevision() error = %v", err)
	}
	if !found || epoch != "core-epoch-1" || current != revision {
		t.Fatalf("CurrentSourceRevision() = (%q, %d, %t), want (core-epoch-1, %d, true)", epoch, current, found, revision)
	}
}

func TestCurrentSourceRevisionRecoversFromLatestOutboxWhenCounterIsMissing(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	record := testRecord("one", 7, "")
	record.SourceEpoch = "core-epoch-1"
	if err := store.Enqueue(context.Background(), record, now); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	if _, err := store.db.Exec(`DELETE FROM world_projection_source_revision
		WHERE world_id = ? AND timeline_id = ? AND timeline_epoch = ? AND actor_id = ? AND kind = ?`,
		"world-house", "timeline-main", "epoch-1", "mio", domainprojection.KindWorkSet); err != nil {
		t.Fatalf("delete counter: %v", err)
	}

	epoch, revision, found, err := store.CurrentSourceRevision(context.Background(), testTarget(), domainprojection.KindWorkSet)
	if err != nil {
		t.Fatalf("CurrentSourceRevision() error = %v", err)
	}
	if !found || epoch != "core-epoch-1" || revision != 7 {
		t.Fatalf("CurrentSourceRevision() = (%q, %d, %t), want (core-epoch-1, 7, true)", epoch, revision, found)
	}
}

func TestReconcileEnqueueAdvancesDurableSourceEpoch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "world-outbox.sqlite")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	now := time.Now().UTC()
	if _, err := store.NextSourceRevision(context.Background(), testTarget(), domainprojection.KindWorkSet, "core-epoch-1", now); err != nil {
		t.Fatalf("NextSourceRevision() error = %v", err)
	}
	reconcile := testRecord("switch", 1, "")
	reconcile.SourceEpoch = "core-epoch-2"
	reconcile.Reconcile = true
	if err := store.Enqueue(context.Background(), reconcile, now); err != nil {
		t.Fatalf("Enqueue(reconcile) error = %v", err)
	}

	epoch, revision, found, err := store.CurrentSourceRevision(context.Background(), testTarget(), domainprojection.KindWorkSet)
	if err != nil {
		t.Fatalf("CurrentSourceRevision() error = %v", err)
	}
	if !found || epoch != "core-epoch-2" || revision != 1 {
		t.Fatalf("CurrentSourceRevision() = (%q, %d, %t), want (core-epoch-2, 1, true)", epoch, revision, found)
	}

	next, err := store.NextSourceRevision(context.Background(), testTarget(), domainprojection.KindWorkSet, "core-epoch-2", now)
	if err != nil {
		t.Fatalf("NextSourceRevision() after reconcile error = %v", err)
	}
	if next != 2 {
		t.Fatalf("NextSourceRevision() = %d, want 2", next)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	reopened, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer reopened.Close()
	epoch, revision, found, err = reopened.CurrentSourceRevision(context.Background(), testTarget(), domainprojection.KindWorkSet)
	if err != nil {
		t.Fatalf("CurrentSourceRevision() after reopen error = %v", err)
	}
	if !found || epoch != "core-epoch-2" || revision != 2 {
		t.Fatalf("CurrentSourceRevision() after reopen = (%q, %d, %t), want (core-epoch-2, 2, true)", epoch, revision, found)
	}
}

func TestEnqueueWithoutReconcileKeepsDurableSourceEpoch(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	if _, err := store.NextSourceRevision(context.Background(), testTarget(), domainprojection.KindWorkSet, "core-epoch-1", now); err != nil {
		t.Fatalf("NextSourceRevision() error = %v", err)
	}
	stale := testRecord("stale", 1, "")
	stale.SourceEpoch = "core-epoch-2"
	err := store.Enqueue(context.Background(), stale, now)
	if !errors.Is(err, appprojection.ErrEpochChangeRequiresReconcile) {
		t.Fatalf("Enqueue() error = %v, want ErrEpochChangeRequiresReconcile", err)
	}
	epoch, revision, found, err := store.CurrentSourceRevision(context.Background(), testTarget(), domainprojection.KindWorkSet)
	if err != nil {
		t.Fatalf("CurrentSourceRevision() error = %v", err)
	}
	if !found || epoch != "core-epoch-1" || revision != 1 {
		t.Fatalf("CurrentSourceRevision() = (%q, %d, %t), want (core-epoch-1, 1, true)", epoch, revision, found)
	}
}

func TestCurrentSourceRevisionValidatesInput(t *testing.T) {
	store := newTestStore(t)
	if _, _, _, err := store.CurrentSourceRevision(context.Background(), domainprojection.Target{}, domainprojection.KindWorkSet); err == nil {
		t.Fatal("CurrentSourceRevision() error = nil for an invalid target")
	}
	if _, _, _, err := store.CurrentSourceRevision(context.Background(), testTarget(), "unsupported"); err == nil {
		t.Fatal("CurrentSourceRevision() error = nil for an unsupported kind")
	}
}
