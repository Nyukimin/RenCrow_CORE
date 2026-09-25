package browsertrace

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"time"
)

// artifactTransactionCleanupTimeout bounds the rollback that runs after a context
// the caller already cancelled, so releasing the write lock cannot hang forever.
const artifactTransactionCleanupTimeout = 5 * time.Second

// artifactRollbackFunc ends the one transaction that withArtifactTransaction owns,
// on the connection that transaction reserved.
//
// It is a field of SQLiteStore rather than a package-level function so that a test
// can make the rollback itself fail on one store only: a package-wide override would
// change the behaviour of unrelated stores and unrelated tests running alongside it,
// which is wider than the fault being studied. Production builds leave it at
// realArtifactRollback, there is no setter, and the statement it runs is a plain
// ROLLBACK on the reserved connection.
type artifactRollbackFunc func(context.Context, *sql.Conn) error

// realArtifactRollback is that production rollback. Nothing here swaps the driver,
// the DSN or the schema: the database file and the statements stay real, and only the
// moment at which the rollback is observed to fail is under test control.
func realArtifactRollback(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(ctx, `ROLLBACK`)
	return err
}

// withArtifactTransaction owns one reserved connection for the lifetime of one
// browser trace Artifact write: reserve, BEGIN IMMEDIATE, the caller's reads, checks
// and write, then COMMIT. SaveAPIArtifact and SupersedeAPIArtifact both run through
// it, so the connection is never handed back to the pool between BEGIN and COMMIT and
// a second writer cannot interleave and observe a half-applied edge.
//
// A COMMIT that reports an error leaves the outcome unknown rather than undone: the
// write may or may not have reached the database, so the transaction is ended and the
// error is returned to the caller, who cannot read a failed COMMIT as proof that
// nothing was written. Only a path that never reached COMMIT claims an ended
// transaction whose write was rolled back, and even that claim is the rollback's own
// report rather than an assumption.
//
// label names the operation in the transaction errors, so a failure says which
// operation could not begin or commit.
func (s *SQLiteStore) withArtifactTransaction(ctx context.Context, label string, work func(context.Context, *sql.Conn) error) (err error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("reserve browser trace sqlite connection: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("begin browser trace %s transaction: %w", label, err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		err = s.cleanupArtifactTransaction(label, conn, err)
	}()
	if err := work(ctx, conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("commit browser trace %s transaction: %w", label, err)
	}
	committed = true
	return nil
}

// cleanupArtifactTransaction is the single owner of the cleanup path for a browser
// trace Artifact transaction whose commit was not confirmed, used by both SaveAPIArtifact and
// SupersedeAPIArtifact.
//
// The caller's context is often the thing that failed, so the rollback runs on its
// own fresh bounded context: releasing the write lock may not hang forever, and it
// may not be skipped either. The original error is always kept, and a rollback error
// is reported alongside it rather than swallowed, because "the write failed" and "the
// rollback could not be confirmed" are different facts and a caller cannot tell them apart
// from the original error alone.
//
// A failed rollback says the undo is unconfirmed, not that the row was lost: what the
// caller can rely on is only that the outcome is unknown and was reported. What it does
// prove is about the connection: the transaction may still be open on it, and
// database/sql does not reset an abandoned transaction when a connection goes back to
// the pool, so the next user of that pool would inherit it.
// Such a connection is therefore discarded through Conn.Raw by returning
// driver.ErrBadConn from the callback, which is how database/sql itself retires a bad
// connection: the driver object is never closed directly and then reused as a sql.Conn.
// Already-done and already-discarded sentinels mean the connection cannot be recycled
// either, so they are handled deliberately; any other cleanup failure is reported
// instead of hidden.
func (s *SQLiteStore) cleanupArtifactTransaction(label string, conn *sql.Conn, operationErr error) error {
	rollbackCtx, cancel := context.WithTimeout(context.Background(), artifactTransactionCleanupTimeout)
	defer cancel()
	rollbackErr := s.rollbackArtifact(rollbackCtx, conn)
	if rollbackErr == nil {
		return operationErr
	}
	failure := errors.Join(operationErr, fmt.Errorf("rollback browser trace %s transaction: %w", label, rollbackErr))
	if errors.Is(rollbackErr, sql.ErrConnDone) || errors.Is(rollbackErr, driver.ErrBadConn) {
		// That connection is already closed or discarded, so there is nothing left to
		// recycle: the open transaction cannot reach another operation. The rollback
		// is still reported, because it did not confirm that the write was undone.
		return failure
	}
	// The transaction is in an unknown state on a connection that is still open, so
	// it has to be disposed of rather than returned to the pool.
	if discardErr := conn.Raw(func(driverConn any) error { return driver.ErrBadConn }); discardErr != nil {
		if errors.Is(discardErr, sql.ErrConnDone) || errors.Is(discardErr, driver.ErrBadConn) {
			return failure
		}
		return errors.Join(failure, fmt.Errorf("discard browser trace %s sqlite connection after an unconfirmed rollback: %w", label, discardErr))
	}
	return failure
}
