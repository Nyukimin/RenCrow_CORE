package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const (
	SQLiteWALCheckpointInterval = time.Minute
	sqliteWALWriterDrainTimeout = 5 * time.Second
)

type SQLiteWALCheckpointErrorClass string

const (
	SQLiteWALCheckpointErrorNone    SQLiteWALCheckpointErrorClass = "none"
	SQLiteWALCheckpointErrorSQLite  SQLiteWALCheckpointErrorClass = "sqlite"
	SQLiteWALCheckpointErrorContext SQLiteWALCheckpointErrorClass = "context"
	SQLiteWALCheckpointErrorBusy    SQLiteWALCheckpointErrorClass = "busy"
	SQLiteWALCheckpointErrorPartial SQLiteWALCheckpointErrorClass = "partial"
)

// SQLiteWALCheckpointStatus is the bounded health snapshot for one SQLite WAL
// checkpoint worker. Synchronous is SQLite's numeric PRAGMA value (1 = NORMAL).
type SQLiteWALCheckpointStatus struct {
	OpenedAt            time.Time                     `json:"opened_at"`
	JournalMode         string                        `json:"journal_mode"`
	Synchronous         int                           `json:"synchronous"`
	WALAutoCheckpoint   int                           `json:"wal_autocheckpoint"`
	CheckpointAttempted bool                          `json:"checkpoint_attempted"`
	Busy                int                           `json:"busy"`
	LogFrames           int                           `json:"log_frames"`
	CheckpointedFrames  int                           `json:"checkpointed_frames"`
	Duration            time.Duration                 `json:"duration"`
	LastSuccessAt       time.Time                     `json:"last_success_at"`
	LastErrorClass      SQLiteWALCheckpointErrorClass `json:"last_error_class"`
}

// OpenSQLiteWALWriter opens the serialized writer pool with low-latency WAL
// settings on every connection. extraPragmas are store-specific settings that
// also need to survive connection replacement.
func OpenSQLiteWALWriter(path string, busyTimeoutMilliseconds int, extraPragmas ...string) (*sql.DB, SQLiteWALCheckpointStatus, error) {
	openedAt := time.Now().UTC()
	dsn := sqliteWALDSN(path, busyTimeoutMilliseconds, extraPragmas...)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, SQLiteWALCheckpointStatus{}, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	status, err := readSQLiteWALSettings(db, busyTimeoutMilliseconds, openedAt)
	if err != nil {
		_ = db.Close()
		return nil, SQLiteWALCheckpointStatus{}, err
	}
	return db, status, nil
}

// CloseSQLiteWALWriter prevents new queries and waits for transactions that
// still hold a writer connection before the caller performs its final checkpoint.
func CloseSQLiteWALWriter(db *sql.DB) error {
	return closeSQLiteWALWriter(db, sqliteWALWriterDrainTimeout)
}

func closeSQLiteWALWriter(db *sql.DB, timeout time.Duration) error {
	if db == nil {
		return nil
	}
	closeErr := db.Close()
	if timeout <= 0 {
		inUse := db.Stats().InUse
		if inUse != 0 {
			return errors.Join(closeErr, fmt.Errorf("sqlite writer pool still has %d in-use connection(s) after %s", inUse, timeout))
		}
		return closeErr
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		inUse := db.Stats().InUse
		if inUse == 0 {
			return closeErr
		}
		select {
		case <-deadline.C:
			return errors.Join(closeErr, fmt.Errorf("sqlite writer pool still has %d in-use connection(s) after %s", inUse, timeout))
		case <-ticker.C:
		}
	}
}

// SQLiteWALCheckpointWorker owns a separate SQLite pool for PASSIVE
// checkpoints. It never borrows the store's writer pool.
type SQLiteWALCheckpointWorker struct {
	db           *sql.DB
	databaseName string
	stop         chan struct{}
	done         chan struct{}
	interval     time.Duration

	stateMu sync.Mutex
	started bool
	closing bool
	closed  bool

	checkpointMu sync.Mutex
	statusMu     sync.RWMutex
	status       SQLiteWALCheckpointStatus

	closeOnce sync.Once
	closeErr  error
}

// NewSQLiteWALCheckpointWorker opens the dedicated checkpoint pool. Start it
// only after the owning store's migrations have completed.
func NewSQLiteWALCheckpointWorker(databaseName, path string, busyTimeoutMilliseconds int) (*SQLiteWALCheckpointWorker, error) {
	if strings.TrimSpace(databaseName) == "" {
		return nil, errors.New("sqlite WAL checkpoint database name is required")
	}
	db, status, err := OpenSQLiteWALWriter(path, busyTimeoutMilliseconds)
	if err != nil {
		return nil, fmt.Errorf("open dedicated sqlite WAL checkpoint connection: %w", err)
	}
	status.LastErrorClass = SQLiteWALCheckpointErrorNone
	return &SQLiteWALCheckpointWorker{
		db:           db,
		databaseName: databaseName,
		stop:         make(chan struct{}),
		done:         make(chan struct{}),
		interval:     SQLiteWALCheckpointInterval,
		status:       status,
	}, nil
}

// Start begins periodic checkpoints. It is safe to call more than once.
func (w *SQLiteWALCheckpointWorker) Start() error {
	if w == nil {
		return errors.New("sqlite WAL checkpoint worker is nil")
	}
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	if w.closed || w.closing {
		return errors.New("sqlite WAL checkpoint worker is closed")
	}
	if w.started {
		return nil
	}
	w.started = true
	go w.run()
	return nil
}

// CheckpointStatus returns the most recent checkpoint and connection settings.
func (w *SQLiteWALCheckpointWorker) CheckpointStatus() SQLiteWALCheckpointStatus {
	if w == nil {
		return SQLiteWALCheckpointStatus{}
	}
	w.statusMu.RLock()
	defer w.statusMu.RUnlock()
	return w.status
}

// Checkpoint performs one PASSIVE checkpoint on the dedicated connection.
func (w *SQLiteWALCheckpointWorker) Checkpoint(ctx context.Context) error {
	if w == nil {
		return errors.New("sqlite WAL checkpoint worker is nil")
	}
	if ctx == nil {
		return errors.New("sqlite WAL checkpoint context is required")
	}
	w.checkpointMu.Lock()
	defer w.checkpointMu.Unlock()
	w.stateMu.Lock()
	closed := w.closing || w.closed
	w.stateMu.Unlock()
	if closed {
		return errors.New("sqlite WAL checkpoint worker is closed")
	}
	return w.checkpointLocked(ctx)
}

// Close stops and joins periodic work, performs one final PASSIVE checkpoint,
// then closes the dedicated connection. Repeated calls return the first result.
func (w *SQLiteWALCheckpointWorker) Close() error {
	if w == nil {
		return nil
	}
	w.closeOnce.Do(func() {
		w.stateMu.Lock()
		w.closing = true
		started := w.started
		if started {
			close(w.stop)
		}
		w.stateMu.Unlock()
		if started {
			<-w.done
		}

		w.checkpointMu.Lock()
		defer w.checkpointMu.Unlock()
		checkpointErr := w.checkpointLocked(context.Background())
		w.stateMu.Lock()
		w.closed = true
		w.stateMu.Unlock()
		w.closeErr = errors.Join(checkpointErr, w.db.Close())
	})
	return w.closeErr
}

func (w *SQLiteWALCheckpointWorker) run() {
	defer close(w.done)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
			if err := w.Checkpoint(context.Background()); err != nil {
				slog.Error("SQLite WAL checkpoint failed", "database", w.databaseName, "error_class", w.CheckpointStatus().LastErrorClass)
			}
		}
	}
}

func (w *SQLiteWALCheckpointWorker) checkpointLocked(ctx context.Context) error {
	startedAt := time.Now()
	slog.Info("SQLite WAL checkpoint started", "database", w.databaseName, "at", startedAt.UTC())
	var busy, logFrames, checkpointedFrames int
	err := w.db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(PASSIVE)`).Scan(&busy, &logFrames, &checkpointedFrames)
	duration := time.Since(startedAt)

	w.statusMu.Lock()
	w.status.CheckpointAttempted = true
	w.status.Duration = duration
	if err != nil {
		w.status.Busy = -1
		w.status.LogFrames = -1
		w.status.CheckpointedFrames = -1
		w.status.LastErrorClass = checkpointErrorClass(err)
		status := w.status
		w.statusMu.Unlock()
		slog.Info("SQLite WAL checkpoint ended", "database", w.databaseName, "duration", duration, "busy", status.Busy, "log_frames", status.LogFrames, "checkpointed_frames", status.CheckpointedFrames, "error_class", status.LastErrorClass)
		return err
	}
	w.status.Busy = busy
	w.status.LogFrames = logFrames
	w.status.CheckpointedFrames = checkpointedFrames
	switch {
	case busy != 0:
		w.status.LastErrorClass = SQLiteWALCheckpointErrorBusy
	case logFrames != checkpointedFrames && !(logFrames == -1 && checkpointedFrames == -1):
		w.status.LastErrorClass = SQLiteWALCheckpointErrorPartial
	default:
		w.status.LastSuccessAt = time.Now().UTC()
		w.status.LastErrorClass = SQLiteWALCheckpointErrorNone
	}
	status := w.status
	w.statusMu.Unlock()
	slog.Info("SQLite WAL checkpoint ended", "database", w.databaseName, "duration", duration, "busy", busy, "log_frames", logFrames, "checkpointed_frames", checkpointedFrames, "error_class", status.LastErrorClass)
	return nil
}

func checkpointErrorClass(err error) SQLiteWALCheckpointErrorClass {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return SQLiteWALCheckpointErrorContext
	}
	return SQLiteWALCheckpointErrorSQLite
}

func readSQLiteWALSettings(db *sql.DB, busyTimeoutMilliseconds int, openedAt time.Time) (SQLiteWALCheckpointStatus, error) {
	conn, err := db.Conn(context.Background())
	if err != nil {
		return SQLiteWALCheckpointStatus{}, fmt.Errorf("open sqlite WAL connection: %w", err)
	}
	defer conn.Close()
	status := SQLiteWALCheckpointStatus{OpenedAt: openedAt, LastErrorClass: SQLiteWALCheckpointErrorNone}
	if err := conn.QueryRowContext(context.Background(), `PRAGMA journal_mode`).Scan(&status.JournalMode); err != nil {
		return SQLiteWALCheckpointStatus{}, fmt.Errorf("read sqlite journal_mode: %w", err)
	}
	if !strings.EqualFold(status.JournalMode, "wal") {
		return SQLiteWALCheckpointStatus{}, fmt.Errorf("sqlite journal_mode = %q, want WAL", status.JournalMode)
	}
	if err := conn.QueryRowContext(context.Background(), `PRAGMA synchronous`).Scan(&status.Synchronous); err != nil {
		return SQLiteWALCheckpointStatus{}, fmt.Errorf("read sqlite synchronous: %w", err)
	}
	if status.Synchronous != 1 {
		return SQLiteWALCheckpointStatus{}, fmt.Errorf("sqlite synchronous = %d, want NORMAL (1)", status.Synchronous)
	}
	if err := conn.QueryRowContext(context.Background(), `PRAGMA wal_autocheckpoint`).Scan(&status.WALAutoCheckpoint); err != nil {
		return SQLiteWALCheckpointStatus{}, fmt.Errorf("read sqlite wal_autocheckpoint: %w", err)
	}
	if status.WALAutoCheckpoint != 0 {
		return SQLiteWALCheckpointStatus{}, fmt.Errorf("sqlite wal_autocheckpoint = %d, want 0", status.WALAutoCheckpoint)
	}
	var busyTimeout int
	if err := conn.QueryRowContext(context.Background(), `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		return SQLiteWALCheckpointStatus{}, fmt.Errorf("read sqlite busy_timeout: %w", err)
	}
	if busyTimeout != busyTimeoutMilliseconds {
		return SQLiteWALCheckpointStatus{}, fmt.Errorf("sqlite busy_timeout = %d, want %d", busyTimeout, busyTimeoutMilliseconds)
	}
	return status, nil
}

func sqliteWALDSN(path string, busyTimeoutMilliseconds int, extraPragmas ...string) string {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	pragmas := []string{
		fmt.Sprintf("_pragma=busy_timeout(%d)", busyTimeoutMilliseconds),
		"_pragma=journal_mode(WAL)",
		"_pragma=synchronous(NORMAL)",
		"_pragma=wal_autocheckpoint(0)",
	}
	for _, pragma := range extraPragmas {
		pragmas = append(pragmas, "_pragma="+url.QueryEscape(pragma))
	}
	pragmas = append(pragmas, "_time_format=sqlite")
	return path + separator + strings.Join(pragmas, "&")
}
