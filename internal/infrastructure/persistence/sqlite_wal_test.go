package persistence

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenSQLiteWALWriterFailsClosedWhenWALIsUnavailable(t *testing.T) {
	db, _, err := OpenSQLiteWALWriter("file::memory:?cache=shared", 5000)
	if err == nil {
		_ = db.Close()
		t.Fatal("OpenSQLiteWALWriter() error = nil for an in-memory database that cannot enable WAL")
	}
}

func TestCloseSQLiteWALWriterReturnsErrorWhenTransactionDoesNotDrain(t *testing.T) {
	db, _, err := OpenSQLiteWALWriter(filepath.Join(t.TempDir(), "drain-timeout.db"), 5000)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := closeSQLiteWALWriter(db, 20*time.Millisecond); err == nil {
		t.Fatal("closeSQLiteWALWriter() error = nil with an active transaction")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback after timed-out close: %v", err)
	}
}

func TestSQLiteWALCheckpointIsIndependentOfWriterPool(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.db")
	writer, _, err := OpenSQLiteWALWriter(path, 5000)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.Exec(`CREATE TABLE checkpoint_test (value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec(`INSERT INTO checkpoint_test(value) VALUES ('seed')`); err != nil {
		t.Fatal(err)
	}
	worker, err := NewSQLiteWALCheckpointWorker("test_store", path, 5000)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()

	writerConn, err := writer.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer writerConn.Close()
	checkpointDone := make(chan error, 1)
	go func() { checkpointDone <- worker.Checkpoint(context.Background()) }()
	select {
	case err := <-checkpointDone:
		if err != nil {
			t.Fatalf("Checkpoint() while writer pool is occupied: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("checkpoint waited for the occupied writer pool")
	}
}

func TestSQLiteWALCheckpointReportsReaderPinnedPartialCheckpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pinned.db")
	writer, _, err := OpenSQLiteWALWriter(path, 5000)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.Exec(`CREATE TABLE checkpoint_test (value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec(`INSERT INTO checkpoint_test(value) VALUES ('before reader')`); err != nil {
		t.Fatal(err)
	}
	worker, err := NewSQLiteWALCheckpointWorker("test_store", path, 5000)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()

	reader, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	reader.SetMaxOpenConns(1)
	readerConn, err := reader.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer readerConn.Close()
	if _, err := readerConn.ExecContext(context.Background(), `BEGIN`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := readerConn.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM checkpoint_test`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec(`INSERT INTO checkpoint_test(value) VALUES ('after reader snapshot')`); err != nil {
		t.Fatal(err)
	}
	if err := worker.Checkpoint(context.Background()); err != nil {
		t.Fatalf("Checkpoint() returned SQL error for PASSIVE partial result: %v", err)
	}
	partial := worker.CheckpointStatus()
	if partial.LastErrorClass != "partial" && partial.LastErrorClass != "busy" {
		t.Fatalf("partial checkpoint error class = %q, want partial or busy; status=%+v", partial.LastErrorClass, partial)
	}
	if !partial.LastSuccessAt.IsZero() {
		t.Fatalf("partial checkpoint advanced last success time to %s", partial.LastSuccessAt)
	}
	if partial.Busy == 0 && partial.LogFrames == partial.CheckpointedFrames {
		t.Fatalf("reader-pinned checkpoint reported complete frames: %+v", partial)
	}
	if _, err := readerConn.ExecContext(context.Background(), `COMMIT`); err != nil {
		t.Fatal(err)
	}
	if err := worker.Checkpoint(context.Background()); err != nil {
		t.Fatalf("Checkpoint() after releasing reader: %v", err)
	}
	complete := worker.CheckpointStatus()
	if complete.LastErrorClass != "none" || complete.LastSuccessAt.IsZero() {
		t.Fatalf("complete checkpoint status = %+v, want successful timestamp and no error class", complete)
	}
	if complete.Busy != 0 || (complete.LogFrames != complete.CheckpointedFrames && !(complete.LogFrames == -1 && complete.CheckpointedFrames == -1)) {
		t.Fatalf("complete checkpoint frames = busy:%d log:%d checkpointed:%d", complete.Busy, complete.LogFrames, complete.CheckpointedFrames)
	}
}

func TestSQLiteWALCheckpointCapturesSQLFailureAndCloseFinalCheckpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "failure.db")
	worker, err := NewSQLiteWALCheckpointWorker("test_store", path, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	if err := worker.Close(); err != nil {
		t.Fatalf("Close() final checkpoint: %v", err)
	}
	status := worker.CheckpointStatus()
	if !status.CheckpointAttempted || status.LastSuccessAt.IsZero() || status.OpenedAt.IsZero() {
		t.Fatalf("Close() status = %+v, want final successful checkpoint", status)
	}
	if err := worker.Close(); err != nil {
		t.Fatalf("second Close() = %v, want nil", err)
	}
	if err := worker.Checkpoint(context.Background()); err == nil {
		t.Fatal("Checkpoint() after Close() error = nil")
	}

	failing, err := NewSQLiteWALCheckpointWorker("test_store", filepath.Join(t.TempDir(), "closed.db"), 5000)
	if err != nil {
		t.Fatal(err)
	}
	if err := failing.db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := failing.Checkpoint(context.Background()); err == nil {
		t.Fatal("Checkpoint() on closed SQLite pool error = nil")
	}
	failure := failing.CheckpointStatus()
	if !failure.CheckpointAttempted || failure.LastErrorClass != "sqlite" || !failure.LastSuccessAt.IsZero() {
		t.Fatalf("SQL failure status = %+v, want attempted sqlite failure and no success time", failure)
	}
	if err := failing.Close(); err == nil {
		t.Fatal("Close() after failed checkpoint error = nil")
	}
}
