package browsertrace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The two sentinels are deliberately distinct: the first is what the Artifact write
// itself reported, the second is the rollback that was supposed to undo it. Only the
// second is under test control, and the faulty rollback never runs ROLLBACK at all,
// so the transaction the helper opened is still dirty when it returns.
var (
	errArtifactWriteSentinel    = errors.New("test injected artifact transaction write failure")
	errArtifactRollbackSentinel = errors.New("test injected artifact rollback failure")
)

// TestSQLiteStoreWithArtifactTransactionReportsRollbackFailure covers the Stage 4C2
// cleanup of a browser trace Artifact transaction whose rollback fails: on a real
// temporary SQLite file, through the real helper, a real write is made and then the
// caller reports a failure, so the helper has to roll back. When that rollback itself
// fails the caller has to be told about both facts, because a swallowed rollback error
// leaves a caller unable to tell whether its write was undone, and the transaction may
// still be open on the connection that is about to leave the helper.
//
// The second half is what that disposal is for: the write has to be gone for every
// other reader, another instance has to be able to write, and the original store has to
// be able to start a fresh owner transaction. A dirty connection that had been returned
// to the pool would instead surface as a transaction already in progress.
//
// This is a helper-boundary test of the transaction cleanup, not end-to-end route
// evidence: the failing write is issued on the reserved connection inside the callback
// rather than through SaveAPIArtifact's own checks, and a cancellation that arrives
// after BEGIN is covered by the owner-method tests in sqlite_store_test.go.
func TestSQLiteStoreWithArtifactTransactionReportsRollbackFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "browser_trace.sqlite")
	store := newSupersedeStoreAt(t, path)
	fixture := supersedeFixtureArtifact("a", supersedeContentA, supersedeHashA)
	other := supersedeFixtureArtifact("b", supersedeContentB, supersedeHashB)
	mustSaveSupersedeArtifacts(t, store, fixture, other)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	before := snapshotAPIArtifactRows(t, store)

	// The fault seam is private to this store instance, so no other store and no
	// unrelated test gets a rollback that fails. Production always installs
	// realArtifactRollback and there is no setter.
	store.rollbackArtifact = func(context.Context, *sql.Conn) error {
		return errArtifactRollbackSentinel
	}

	var affected int64
	err := store.withArtifactTransaction(ctx, "artifact save",
		func(ctx context.Context, conn *sql.Conn) error {
			t.Helper()
			// A legitimate in-place update of the stored row: same identity and scope,
			// so apart from the injected failure that follows it nothing here is
			// invalid, and a rejection can only come from the cleanup under test.
			updated := fixture
			updated.Title = "updated inside the transaction"
			payload, err := json.Marshal(updated)
			if err != nil {
				return err
			}
			res, err := conn.ExecContext(ctx,
				`UPDATE api_artifact SET payload = ? WHERE artifact_id = ?`,
				string(payload), string(fixture.ArtifactID))
			if err != nil {
				return err
			}
			if affected, err = res.RowsAffected(); err != nil {
				return err
			}
			return errArtifactWriteSentinel
		})
	if affected != 1 {
		t.Fatalf("dirty write rows affected = %d, want 1: error = %v", affected, err)
	}
	if !errors.Is(err, errArtifactWriteSentinel) {
		t.Fatalf("withArtifactTransaction() error = %v, want the original write sentinel", err)
	}
	if !errors.Is(err, errArtifactRollbackSentinel) {
		t.Fatalf("withArtifactTransaction() error = %v, want it to also report the failed rollback %v",
			err, errArtifactRollbackSentinel)
	}

	// The rollback never ran, so what proves the cleanup is the state left behind. Read
	// through an independent instance: the uncommitted write has to be gone, which also
	// proves the connection that carried it was disposed of rather than reused.
	reader := newSupersedeStoreAt(t, path)
	if got := snapshotAPIArtifactRows(t, reader); !reflect.DeepEqual(got, before) {
		t.Errorf("api_artifact after the failed rollback = %q, want the unchanged rows %q", got, before)
	}

	// The write lock has to be free for that other instance as well: an ordinary owner
	// update of the sibling row goes through.
	update := other
	update.Title = "written by the second instance"
	if err := reader.SaveAPIArtifact(ctx, update); err != nil {
		t.Fatalf("SaveAPIArtifact on the second instance after the failed rollback: %v", err)
	}

	// And the original store has to be usable again, which it would not be if the
	// connection whose rollback failed had been handed back to its pool with the
	// transaction still open. The store is reused as it is, not closed and reopened.
	store.rollbackArtifact = realArtifactRollback
	reused := fixture
	reused.Title = "written by the original store afterwards"
	if err := store.SaveAPIArtifact(ctx, reused); err != nil {
		t.Fatalf("SaveAPIArtifact on the original store after the failed rollback: %v", err)
	}

	if stored := mustFindSupersedeArtifact(t, store, fixture.ArtifactID); !reflect.DeepEqual(stored, reused) {
		t.Errorf("artifact from the original store = %#v, want the afterwards written %#v", stored, reused)
	}
	if stored := mustFindSupersedeArtifact(t, reader, fixture.ArtifactID); !reflect.DeepEqual(stored, reused) {
		t.Errorf("artifact seen by the second instance = %#v, want the same written %#v", stored, reused)
	}
	if stored := mustFindSupersedeArtifact(t, reader, other.ArtifactID); !reflect.DeepEqual(stored, update) {
		t.Errorf("artifact %s = %#v, want the second instance write %#v", other.ArtifactID, stored, update)
	}
}

// TestSQLiteStoreWithArtifactTransactionCancelledAfterBeginRollsBackOnALiveContext
// is the deterministic cancellation case for the same cleanup owner: BEGIN succeeded,
// a real update was written, and the caller context was cancelled inside the callback,
// so the commit is attempted with a context that is already done.
//
// It is labelled a helper-boundary test: the write is issued on the reserved connection
// inside the callback rather than through SaveAPIArtifact's own checks, and the
// cancellation arrives at a chosen point rather than from a runtime route, so it is not
// end-to-end route evidence. The owner-method tests in sqlite_store_test.go cover the
// cancellations that arrive while a real statement is in flight.
//
// What has to hold is that the cancelled caller context is reported, that the rollback
// still ran on a live context of its own rather than on the cancelled one, that the
// uncommitted write is gone for every reader, and that neither the original store nor a
// second instance is left without a usable connection.
func TestSQLiteStoreWithArtifactTransactionCancelledAfterBeginRollsBackOnALiveContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "browser_trace.sqlite")
	store := newSupersedeStoreAt(t, path)
	fixture := supersedeFixtureArtifact("a", supersedeContentA, supersedeHashA)
	other := supersedeFixtureArtifact("b", supersedeContentB, supersedeHashB)
	mustSaveSupersedeArtifacts(t, store, fixture, other)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	before := snapshotAPIArtifactRows(t, store)

	// The seam is observed, not replaced with a fake rollback: it records the context
	// the cleanup handed over and then delegates to the production rollback, so the
	// statement that runs is still a real ROLLBACK on the reserved connection.
	var rollbackRan, rollbackCtxLive, rollbackCtxBounded bool
	store.rollbackArtifact = func(rollbackCtx context.Context, conn *sql.Conn) error {
		rollbackRan = true
		rollbackCtxLive = rollbackCtx.Err() == nil
		_, rollbackCtxBounded = rollbackCtx.Deadline()
		return realArtifactRollback(rollbackCtx, conn)
	}
	t.Cleanup(func() { store.rollbackArtifact = realArtifactRollback })

	var affected int64
	err := store.withArtifactTransaction(ctx, "supersede",
		func(ctx context.Context, conn *sql.Conn) error {
			t.Helper()
			updated := fixture
			updated.Title = "written and then cancelled before commit"
			payload, err := json.Marshal(updated)
			if err != nil {
				return err
			}
			res, err := conn.ExecContext(ctx,
				`UPDATE api_artifact SET payload = ? WHERE artifact_id = ?`,
				string(payload), string(fixture.ArtifactID))
			if err != nil {
				return err
			}
			if affected, err = res.RowsAffected(); err != nil {
				return err
			}
			// The write is real and the transaction is open; the caller gives up here,
			// so the commit that follows is attempted with a done context.
			cancel()
			return nil
		})
	if affected != 1 {
		t.Fatalf("write before the cancellation rows affected = %d, want 1: error = %v", affected, err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("withArtifactTransaction() error = %v, want the cancelled caller context", err)
	}
	if !rollbackRan {
		t.Fatalf("the cancelled transaction was rolled back without the owner rollback running: %v", err)
	}
	if !rollbackCtxLive {
		t.Fatalf("the owner rollback was handed the caller context that was already done: %v", err)
	}
	if !rollbackCtxBounded {
		t.Fatalf("the owner rollback was handed a context with no deadline, so releasing the write lock could hang forever: %v", err)
	}
	if strings.Contains(err.Error(), "rollback browser trace") {
		t.Errorf("withArtifactTransaction() error = %v, want only the commit failure: the rollback on a live context was not reported as a failure", err)
	}

	// The write that the cancelled transaction made has to be gone for another reader.
	reader := newSupersedeStoreAt(t, path)
	if got := snapshotAPIArtifactRows(t, reader); !reflect.DeepEqual(got, before) {
		t.Errorf("api_artifact after the cancelled transaction = %q, want the unchanged rows %q", got, before)
	}

	// The original store is reused as it stands, not closed and reopened: it has to be
	// able to run a fresh owner transaction, and so does the second instance.
	store.rollbackArtifact = realArtifactRollback
	followUpCtx, cancelFollowUp := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelFollowUp()
	reused := fixture
	reused.Title = "written by the original store after the cancellation"
	if err := store.SaveAPIArtifact(followUpCtx, reused); err != nil {
		t.Fatalf("SaveAPIArtifact on the original store after the cancelled transaction: %v", err)
	}
	update := other
	update.Title = "written by the second instance after the cancellation"
	if err := reader.SaveAPIArtifact(followUpCtx, update); err != nil {
		t.Fatalf("SaveAPIArtifact on the second instance after the cancelled transaction: %v", err)
	}

	if stored := mustFindSupersedeArtifact(t, store, fixture.ArtifactID); !reflect.DeepEqual(stored, reused) {
		t.Errorf("artifact from the original store = %#v, want the fresh write %#v", stored, reused)
	}
	if stored := mustFindSupersedeArtifact(t, reader, fixture.ArtifactID); !reflect.DeepEqual(stored, reused) {
		t.Errorf("artifact seen by the second instance = %#v, want the same fresh write %#v", stored, reused)
	}
	if stored := mustFindSupersedeArtifact(t, reader, other.ArtifactID); !reflect.DeepEqual(stored, update) {
		t.Errorf("artifact %s = %#v, want the second instance write %#v", other.ArtifactID, stored, update)
	}
}
