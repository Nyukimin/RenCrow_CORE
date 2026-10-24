package eventstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	_ "modernc.org/sqlite"
)

const (
	defaultListLimit              = 50
	maxListLimit                  = 1000
	sqliteBusyTimeoutMilliseconds = 5000
	maxEventSeqValue              = int64(1<<63 - 1)
)

var errStoreClosed = errors.New("event store is closed")
var errStep09MigrationRequired = errors.New("Step09 migration required: existing event_envelope schema is incompatible with storage-owned event_seq; refusing automatic migration")

// SQLiteStore is the append-only SQLite implementation of the canonical event
// envelope store.
type SQLiteStore struct {
	db                             *sql.DB
	sessionStateDB                 *sql.DB
	checkpointWorker               *persistence.SQLiteWALCheckpointWorker
	sequence                       *eventSeqAllocator
	sequenceWriteMu                *sync.Mutex
	sequenceAdmissionMu            *sync.Mutex
	writeQueue                     WriteQueue
	committedOrchestratorHighWater atomic.Int64
	pendingSyncSequenceWrites      int
	waitingSyncSequenceWrites      int
	chatLease                      *ChatLease
	releaseWriter                  func() error
	closeOnce                      sync.Once
	closeErr                       error
}

// NewSQLiteStore opens a SQLite canonical event store at the configured path.
// It never falls back to a repository or runtime-home location.
func NewSQLiteStore(path string) (*SQLiteStore, error) {
	return newSQLiteStore(path, nil)
}

// NewSQLiteStoreWithWriteQueue opens a canonical event store whose EventSeq
// writing commands are admitted to the injected shared persistence queue.
func NewSQLiteStoreWithWriteQueue(path string, queue WriteQueue) (*SQLiteStore, error) {
	if queue == nil {
		return nil, errors.New("event store write queue is required")
	}
	return newSQLiteStore(path, queue)
}

func newSQLiteStore(path string, queue WriteQueue) (*SQLiteStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("canonical event store path is required")
	}
	if err := ensureEventStorePath(path); err != nil {
		return nil, err
	}
	_, pathState, releaseWriter, err := acquireEventStoreWriter(path)
	if err != nil {
		return nil, err
	}
	locked := true
	defer func() {
		if locked {
			_ = releaseWriter()
		}
	}()

	db, _, err := persistence.OpenSQLiteWALWriter(path, sqliteBusyTimeoutMilliseconds, "foreign_keys(ON)")
	if err != nil {
		return nil, err
	}
	if pathState.databasePath != "" {
		if err := rejectHardlinkedEventStoreFile(pathState.databasePath); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	store := &SQLiteStore{
		db: db, sequence: &pathState.sequence, sequenceWriteMu: &pathState.sequenceWriteMu,
		sequenceAdmissionMu: &pathState.sequenceAdmissionMu,
		writeQueue:          queue,
		releaseWriter:       releaseWriter,
	}
	if err := store.enableForeignKeys(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	maxSequence, err := currentMaxEventSeq(context.Background(), db)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("seed event_seq allocator after SQLite recovery: %w", err)
	}
	if err := store.sequence.seed(maxSequence); err != nil {
		_ = db.Close()
		return nil, err
	}
	var orchestratorHighWater int64
	if err := db.QueryRowContext(context.Background(), `SELECT COALESCE(MAX(event_seq), 0) FROM event_envelope WHERE component_id = ?`, canonicalOrchestratorComponent).Scan(&orchestratorHighWater); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("seed committed orchestrator high-water after SQLite recovery: %w", err)
	}
	if orchestratorHighWater < 0 {
		_ = db.Close()
		return nil, errors.New("orchestrator event_seq contains a negative persisted value")
	}
	store.committedOrchestratorHighWater.Store(orchestratorHighWater)
	if pathState.databasePath != "" {
		store.sessionStateDB, err = openSessionStateReadPool(pathState.databasePath)
		if err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	store.checkpointWorker, err = persistence.NewSQLiteWALCheckpointWorker("event_store", path, sqliteBusyTimeoutMilliseconds)
	if err != nil {
		if store.sessionStateDB != nil {
			_ = store.sessionStateDB.Close()
		}
		_ = db.Close()
		return nil, err
	}
	if err := store.checkpointWorker.Start(); err != nil {
		_ = store.checkpointWorker.Close()
		if store.sessionStateDB != nil {
			_ = store.sessionStateDB.Close()
		}
		_ = db.Close()
		return nil, err
	}
	locked = false
	return store, nil
}

func openSessionStateReadPool(path string) (*sql.DB, error) {
	fileURI, err := sqliteFileURI(path)
	if err != nil {
		return nil, fmt.Errorf("build event store session-state reader URI: %w", err)
	}
	query := url.Values{}
	query.Set("mode", "ro")
	query.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", sqliteBusyTimeoutMilliseconds))
	query.Add("_pragma", "query_only(1)")
	query.Set("_time_format", "sqlite")
	db, err := sql.Open("sqlite", fileURI+"?"+query.Encode())
	if err != nil {
		return nil, fmt.Errorf("open event store session-state reader: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize event store session-state reader: %w", err)
	}
	var queryOnly int
	if err := db.QueryRowContext(context.Background(), `PRAGMA query_only`).Scan(&queryOnly); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("verify event store session-state reader is query-only: %w", err)
	}
	if queryOnly != 1 {
		_ = db.Close()
		return nil, fmt.Errorf("event store session-state reader query_only = %d, want 1", queryOnly)
	}
	return db, nil
}

func sqliteFileURI(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("sqlite file path is required")
	}
	uriPath := filepath.ToSlash(path)
	fileURL := url.URL{Scheme: "file"}
	if runtime.GOOS == "windows" {
		if strings.HasPrefix(uriPath, "//") {
			uncPath := strings.TrimPrefix(uriPath, "//")
			host, sharePath, found := strings.Cut(uncPath, "/")
			if !found || host == "" || sharePath == "" {
				return "", errors.New("invalid absolute Windows UNC sqlite path")
			}
			fileURL.Host = host
			fileURL.Path = "/" + sharePath
		} else {
			if len(uriPath) >= 2 && uriPath[1] == ':' {
				uriPath = "/" + uriPath
			}
			fileURL.Path = uriPath
		}
	} else {
		fileURL.Path = uriPath
	}
	return fileURL.String(), nil
}

const canonicalOrchestratorComponent = "orchestrator"

// OrchestratorCommittedHighWater returns the latest committed canonical
// orchestrator EventSeq from RAM. It is updated only after transaction commit.
func (s *SQLiteStore) OrchestratorCommittedHighWater() modulecore.EventSeq {
	if s == nil {
		return 0
	}
	return modulecore.EventSeq(s.committedOrchestratorHighWater.Load())
}

func (s *SQLiteStore) observeCommittedOrchestratorEvent(event modulecore.EventEnvelope) {
	if s == nil || event.ComponentID != canonicalOrchestratorComponent || event.EventSeq <= 0 {
		return
	}
	sequence := int64(event.EventSeq)
	for {
		current := s.committedOrchestratorHighWater.Load()
		if current >= sequence || s.committedOrchestratorHighWater.CompareAndSwap(current, sequence) {
			return
		}
	}
}

func (s *SQLiteStore) observeCommittedOrchestratorEvents(events []modulecore.EventEnvelope) {
	for _, event := range events {
		s.observeCommittedOrchestratorEvent(event)
	}
}

// beginSequenceTx serializes sequence reservation and its transaction commit
// against every other sequence-writing path for this database.
func (s *SQLiteStore) beginSequenceTx(ctx context.Context) (*sql.Tx, func(), error) {
	if s == nil || s.sequenceWriteMu == nil {
		return nil, func() {}, errors.New("event store sequence writer is unavailable")
	}
	if s.writeQueue != nil && !s.isQueueWorkerContext(ctx) {
		return nil, func() {}, errors.New("event store sequence writes must use the ordered write queue")
	}
	s.sequenceWriteMu.Lock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.sequenceWriteMu.Unlock()
		return nil, func() {}, err
	}
	return tx, s.sequenceWriteMu.Unlock, nil
}

// Close releases the underlying SQLite connection. It is safe to call on a
// nil or already uninitialized store.
func (s *SQLiteStore) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		defer func() {
			if s.releaseWriter != nil {
				s.closeErr = errors.Join(s.closeErr, s.releaseWriter())
			}
		}()
		var sessionStateErr error
		if s.sessionStateDB != nil {
			sessionStateErr = s.sessionStateDB.Close()
		}
		var writerErr error
		if s.db != nil {
			// Close and drain the writer pool before the final checkpoint.
			writerErr = persistence.CloseSQLiteWALWriter(s.db)
		}
		var checkpointErr error
		if s.checkpointWorker != nil {
			checkpointErr = s.checkpointWorker.Close()
		}
		s.closeErr = errors.Join(sessionStateErr, writerErr, checkpointErr)
	})
	return s.closeErr
}

// CheckpointStatus reports the latest checkpoint and its effective WAL settings.
func (s *SQLiteStore) CheckpointStatus() persistence.SQLiteWALCheckpointStatus {
	if s == nil || s.checkpointWorker == nil {
		return persistence.SQLiteWALCheckpointStatus{}
	}
	return s.checkpointWorker.CheckpointStatus()
}

// Append validates and atomically appends one canonical event envelope and
// its causation/dependency edges. Every referenced event must already exist in
// the same trace, so a normal append cannot introduce a cycle.
func (s *SQLiteStore) Append(ctx context.Context, event modulecore.EventEnvelope) error {
	_, err := s.AppendSequenced(ctx, event)
	return err
}

// AppendSequenced assigns the next storage-owned sequence to an unassigned live
// envelope and returns the envelope exactly as persisted. Positive sequences
// are reserved for the explicit migration batch path.
func (s *SQLiteStore) AppendSequenced(ctx context.Context, event modulecore.EventEnvelope) (modulecore.EventEnvelope, error) {
	if s.shouldQueueWrite(ctx) {
		if err := s.ensureOpen(); err != nil {
			return modulecore.EventEnvelope{}, err
		}
		if err := modulecore.ValidateEventEnvelope(event); err != nil {
			return modulecore.EventEnvelope{}, fmt.Errorf("validate event envelope: %w", err)
		}
		if event.EventSeq != 0 {
			return modulecore.EventEnvelope{}, errors.New("event_seq must be zero for live append; positive sequences are reserved for AppendBatch")
		}
		queuedEvent := cloneEventEnvelope(event)
		sizeBytes, err := queuedWriteSize(queuedEvent)
		if err != nil {
			return modulecore.EventEnvelope{}, fmt.Errorf("size queued event envelope: %w", err)
		}
		var stored modulecore.EventEnvelope
		err = s.enqueueOrderedWrite(ctx, 1, sizeBytes, func(commandCtx context.Context) error {
			var appendErr error
			stored, appendErr = s.appendSequencedInline(commandCtx, queuedEvent)
			return appendErr
		})
		if err != nil {
			return modulecore.EventEnvelope{}, err
		}
		return stored, nil
	}
	return s.appendSequencedInline(ctx, event)
}

func (s *SQLiteStore) appendSequencedInline(ctx context.Context, event modulecore.EventEnvelope) (modulecore.EventEnvelope, error) {
	if err := s.ensureOpen(); err != nil {
		return modulecore.EventEnvelope{}, err
	}
	if err := modulecore.ValidateEventEnvelope(event); err != nil {
		return modulecore.EventEnvelope{}, fmt.Errorf("validate event envelope: %w", err)
	}
	if event.EventSeq != 0 && !s.isQueueWorkerContext(ctx) {
		return modulecore.EventEnvelope{}, errors.New("event_seq must be zero for live append; positive sequences are reserved for AppendBatch")
	}

	tx, unlockSequence, err := s.beginSequenceTx(ctx)
	if err != nil {
		return modulecore.EventEnvelope{}, err
	}
	defer unlockSequence()
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	sequence, err := s.nextSequence(ctx)
	if err != nil {
		return modulecore.EventEnvelope{}, err
	}
	if event.EventSeq != 0 && event.EventSeq != sequence {
		return modulecore.EventEnvelope{}, fmt.Errorf("admitted event_seq %d does not match its reserved sequence %d", event.EventSeq, sequence)
	}
	event.EventSeq = sequence
	payload, err := json.Marshal(event)
	if err != nil {
		return modulecore.EventEnvelope{}, fmt.Errorf("marshal event envelope: %w", err)
	}
	if err := ensureEventSeqAvailable(ctx, tx, event.EventSeq); err != nil {
		return modulecore.EventEnvelope{}, err
	}

	var existing int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM event_envelope WHERE event_id = ? LIMIT 1`, string(event.EventID)).Scan(&existing)
	if err == nil {
		return modulecore.EventEnvelope{}, fmt.Errorf("duplicate event_id %q", event.EventID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return modulecore.EventEnvelope{}, err
	}

	if err := s.validateReferences(ctx, tx, event); err != nil {
		return modulecore.EventEnvelope{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO event_envelope
			(event_id, event_seq, trace_id, schema_version, event_type, component_id, occurred_at, envelope_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		string(event.EventID),
		int64(event.EventSeq),
		string(event.TraceID),
		event.SchemaVersion,
		event.EventType,
		event.ComponentID,
		event.OccurredAt.Format(time.RFC3339Nano),
		string(payload),
	); err != nil {
		return modulecore.EventEnvelope{}, err
	}

	if event.CausationEventID != "" {
		if err := insertDependency(ctx, tx, event.EventID, event.CausationEventID, "causation"); err != nil {
			return modulecore.EventEnvelope{}, err
		}
	}
	for _, dependencyID := range event.DependencyEventIDs {
		if err := insertDependency(ctx, tx, event.EventID, dependencyID, "dependency"); err != nil {
			return modulecore.EventEnvelope{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return modulecore.EventEnvelope{}, err
	}
	committed = true
	s.observeCommittedOrchestratorEvent(event)
	return event, nil
}

// AppendBatch atomically appends one closed migration graph. Unlike the live
// Append path, input order does not need to place causes before effects.
func (s *SQLiteStore) AppendBatch(ctx context.Context, events []modulecore.EventEnvelope) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	if len(events) == 0 {
		return nil
	}
	if err := modulecore.ValidateEventEnvelopeGraph(events); err != nil {
		return fmt.Errorf("validate event envelope graph: %w", err)
	}
	if s.shouldQueueWrite(ctx) {
		queuedEvents := cloneQueuedValue(events)
		sizeBytes, err := queuedWriteSize(queuedEvents)
		if err != nil {
			return fmt.Errorf("size queued event batch: %w", err)
		}
		return s.enqueueOrderedBatchWrite(ctx, queuedEvents, sizeBytes, func(commandCtx context.Context) error {
			return s.AppendBatch(commandCtx, queuedEvents)
		})
	}
	tx, unlockSequence, err := s.beginSequenceTx(ctx)
	if err != nil {
		return err
	}
	defer unlockSequence()
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	assigned := append([]modulecore.EventEnvelope(nil), events...)
	payloads := make([][]byte, len(assigned))
	seenSequences := make(map[modulecore.EventSeq]struct{}, len(assigned))
	sequenceFloor := s.sequenceFloor(ctx)
	for index := range assigned {
		event := &assigned[index]
		if event.EventSeq == 0 {
			continue
		}
		if err := event.EventSeq.Validate(); err != nil {
			return fmt.Errorf("event_seq: %w", err)
		}
		if _, exists := seenSequences[event.EventSeq]; exists {
			return fmt.Errorf("duplicate event_seq %d", event.EventSeq)
		}
		if err := ensureEventSeqAvailable(ctx, tx, event.EventSeq); err != nil {
			return err
		}
		if int64(event.EventSeq) <= sequenceFloor {
			return fmt.Errorf("event_seq %d is not greater than current event_seq allocator %d", event.EventSeq, sequenceFloor)
		}
		seenSequences[event.EventSeq] = struct{}{}
	}
	for index := range assigned {
		event := &assigned[index]
		if event.EventSeq == 0 {
			for {
				sequence, err := s.nextSequence(ctx)
				if err != nil {
					return err
				}
				candidate := sequence
				if _, reserved := seenSequences[candidate]; reserved {
					continue
				}
				event.EventSeq = candidate
				seenSequences[candidate] = struct{}{}
				break
			}
		}
		payload, err := json.Marshal(event)
		if err != nil {
			return fmt.Errorf("marshal event envelope: %w", err)
		}
		payloads[index] = payload
	}
	for index, event := range assigned {
		var existing int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM event_envelope WHERE event_id = ? LIMIT 1`, string(event.EventID)).Scan(&existing)
		if err == nil {
			return fmt.Errorf("duplicate event_id %q", event.EventID)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO event_envelope
				(event_id, event_seq, trace_id, schema_version, event_type, component_id, occurred_at, envelope_json)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			string(event.EventID), int64(event.EventSeq), string(event.TraceID), event.SchemaVersion,
			event.EventType, event.ComponentID, event.OccurredAt.Format(time.RFC3339Nano), string(payloads[index]),
		); err != nil {
			return err
		}
	}
	for _, event := range assigned {
		if err := s.validateReferences(ctx, tx, event); err != nil {
			return err
		}
	}
	for _, event := range assigned {
		if event.CausationEventID != "" {
			if err := insertDependency(ctx, tx, event.EventID, event.CausationEventID, "causation"); err != nil {
				return err
			}
		}
		for _, dependencyID := range event.DependencyEventIDs {
			if err := insertDependency(ctx, tx, event.EventID, dependencyID, "dependency"); err != nil {
				return err
			}
		}
	}
	maxAssignedSequence := int64(0)
	for sequence := range seenSequences {
		if int64(sequence) > maxAssignedSequence {
			maxAssignedSequence = int64(sequence)
		}
	}
	s.sequence.advanceTo(maxAssignedSequence)
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	s.observeCommittedOrchestratorEvents(assigned)
	return nil
}

// GetByID returns the exact canonical envelope for eventID. The boolean is
// false when no event with that ID exists.
func (s *SQLiteStore) GetByID(ctx context.Context, eventID modulecore.EventID) (modulecore.EventEnvelope, bool, error) {
	if err := s.ensureOpen(); err != nil {
		return modulecore.EventEnvelope{}, false, err
	}
	if err := eventID.Validate(); err != nil {
		return modulecore.EventEnvelope{}, false, fmt.Errorf("event_id: %w", err)
	}

	var storedEventID, traceID, schemaVersion, eventType, componentID, occurredAt, payload string
	var eventSeq int64
	err := s.db.QueryRowContext(ctx, `
		SELECT event_id, event_seq, trace_id, schema_version, event_type, component_id, occurred_at, envelope_json
		FROM event_envelope
		WHERE event_id = ?`, string(eventID)).Scan(
		&storedEventID, &eventSeq, &traceID, &schemaVersion, &eventType, &componentID, &occurredAt, &payload,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return modulecore.EventEnvelope{}, false, nil
	}
	if err != nil {
		return modulecore.EventEnvelope{}, false, err
	}
	event, err := decodeStoredEnvelope(storedEventID, eventSeq, traceID, schemaVersion, eventType, componentID, occurredAt, payload)
	if err != nil {
		return modulecore.EventEnvelope{}, false, err
	}
	return event, true, nil
}

// ListByComponent returns at most limit canonical envelopes for componentID,
// ordered newest first by the canonical event sequence. Non-positive limits
// use the default bound and larger limits are capped.
func (s *SQLiteStore) ListByComponent(ctx context.Context, componentID string, limit int) ([]modulecore.EventEnvelope, error) {
	if err := s.ensureOpen(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(componentID) == "" {
		return nil, fmt.Errorf("component_id is required")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT event_id, event_seq, trace_id, schema_version, event_type, component_id, occurred_at, envelope_json
		FROM event_envelope
		WHERE component_id = ?
		ORDER BY event_seq DESC
		LIMIT ?`, componentID, boundedLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := make([]modulecore.EventEnvelope, 0)
	for rows.Next() {
		var storedEventID, traceID, schemaVersion, eventType, storedComponentID, occurredAt, payload string
		var eventSeq int64
		if err := rows.Scan(&storedEventID, &eventSeq, &traceID, &schemaVersion, &eventType, &storedComponentID, &occurredAt, &payload); err != nil {
			return nil, err
		}
		event, err := decodeStoredEnvelope(storedEventID, eventSeq, traceID, schemaVersion, eventType, storedComponentID, occurredAt, payload)
		if err != nil {
			return nil, err
		}
		if event.ComponentID != componentID {
			return nil, fmt.Errorf("stored event %q component_id %q does not match indexed component_id %q", event.EventID, event.ComponentID, componentID)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

// ListByTraceID returns the exact canonical envelopes for one trace in
// deterministic event-sequence order. The caller supplies the hard bound;
// the query deliberately reads one additional row so an over-bound trace is
// rejected instead of silently truncated.
func (s *SQLiteStore) ListByTraceID(ctx context.Context, traceID modulecore.TraceID, max int) ([]modulecore.EventEnvelope, error) {
	if err := s.ensureOpen(); err != nil {
		return nil, err
	}
	if ctx == nil {
		return nil, errors.New("trace lookup context is required")
	}
	if err := traceID.Validate(); err != nil {
		return nil, fmt.Errorf("trace_id: %w", err)
	}
	if max <= 0 {
		return nil, errors.New("trace lookup maximum must be positive")
	}
	if max > maxListLimit {
		return nil, errors.New("trace lookup maximum exceeds the bound")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT event_id, event_seq, trace_id, schema_version, event_type, component_id, occurred_at, envelope_json
		FROM event_envelope
		WHERE trace_id = ?
		ORDER BY event_seq ASC
		LIMIT ?`, string(traceID), max+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := make([]modulecore.EventEnvelope, 0, max+1)
	for rows.Next() {
		var storedEventID, storedTraceID, schemaVersion, eventType, componentID, occurredAt, payload string
		var eventSeq int64
		if err := rows.Scan(&storedEventID, &eventSeq, &storedTraceID, &schemaVersion, &eventType, &componentID, &occurredAt, &payload); err != nil {
			return nil, err
		}
		event, err := decodeStoredEnvelope(storedEventID, eventSeq, storedTraceID, schemaVersion, eventType, componentID, occurredAt, payload)
		if err != nil {
			return nil, err
		}
		if event.TraceID != traceID {
			return nil, errors.New("stored trace lookup returned a mismatched trace")
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(events) > max {
		return nil, errors.New("trace lookup exceeded maximum")
	}
	return events, nil
}

// HasPublicConversationFinalResponse checks one exact, canonical public final
// identity without enumerating or truncating the surrounding trace.
func (s *SQLiteStore) HasPublicConversationFinalResponse(ctx context.Context, traceID modulecore.TraceID, turnID modulecore.TurnID, sessionID modulecore.SessionID) (bool, error) {
	if err := s.ensureOpen(); err != nil {
		return false, err
	}
	if ctx == nil {
		return false, errors.New("public final lookup context is required")
	}
	if err := traceID.Validate(); err != nil {
		return false, fmt.Errorf("trace_id: %w", err)
	}
	if err := turnID.Validate(); err != nil {
		return false, fmt.Errorf("turn_id: %w", err)
	}
	if err := sessionID.Validate(); err != nil {
		return false, fmt.Errorf("session_id: %w", err)
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT event_id, event_seq, trace_id, schema_version, event_type, component_id, occurred_at, envelope_json
		FROM event_envelope
		WHERE trace_id = ?
		  AND json_extract(envelope_json, '$.turn_id') = ?
		  AND json_extract(envelope_json, '$.session_id') = ?
		  AND event_type = 'agent.response'
		  AND component_id = 'orchestrator'
		  AND lower(trim(COALESCE(json_extract(envelope_json, '$.payload.to'), ''))) = 'user'
		LIMIT 1`, string(traceID), string(turnID), string(sessionID))
	var storedEventID, storedTraceID, schemaVersion, eventType, componentID, occurredAt, payload string
	var eventSeq int64
	if err := row.Scan(&storedEventID, &eventSeq, &storedTraceID, &schemaVersion, &eventType, &componentID, &occurredAt, &payload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	event, err := decodeStoredEnvelope(storedEventID, eventSeq, storedTraceID, schemaVersion, eventType, componentID, occurredAt, payload)
	if err != nil {
		return false, err
	}
	if event.TraceID != traceID || event.TurnID != turnID || event.SessionID != sessionID || event.EventType != "agent.response" || event.ComponentID != "orchestrator" || !publicRecipient(event.Payload["to"]) {
		return false, errors.New("stored public final lookup returned a mismatched envelope")
	}
	return true, nil
}

func publicRecipient(value any) bool {
	to, ok := value.(string)
	return ok && strings.EqualFold(strings.TrimSpace(to), "user")
}

func (s *SQLiteStore) ensureOpen() error {
	if s == nil || s.db == nil {
		return errStoreClosed
	}
	return nil
}

func (s *SQLiteStore) enableForeignKeys() error {
	if _, err := s.db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		return err
	}
	var enabled int
	if err := s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&enabled); err != nil {
		return err
	}
	if enabled != 1 {
		return fmt.Errorf("sqlite foreign_keys pragma is disabled")
	}
	return nil
}

func (s *SQLiteStore) migrate() error {
	if err := s.validateExistingEventEnvelopeSchema(); err != nil {
		return err
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS event_envelope (
			event_id TEXT PRIMARY KEY NOT NULL,
			event_seq INTEGER NOT NULL UNIQUE,
			trace_id TEXT NOT NULL,
			schema_version TEXT NOT NULL,
			event_type TEXT NOT NULL,
			component_id TEXT NOT NULL,
			occurred_at TEXT NOT NULL,
			envelope_json TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS event_dependency (
			event_id TEXT NOT NULL,
			dependency_event_id TEXT NOT NULL,
			relation_type TEXT NOT NULL CHECK (relation_type IN ('causation', 'dependency')),
			PRIMARY KEY (event_id, dependency_event_id),
			FOREIGN KEY (event_id) REFERENCES event_envelope(event_id) ON UPDATE RESTRICT ON DELETE RESTRICT,
			FOREIGN KEY (dependency_event_id) REFERENCES event_envelope(event_id) ON UPDATE RESTRICT ON DELETE RESTRICT
		)`,
		`CREATE INDEX IF NOT EXISTS event_envelope_component_idx
			ON event_envelope (component_id, event_seq)`,
		`CREATE INDEX IF NOT EXISTS event_envelope_trace_idx
			ON event_envelope (trace_id, event_seq)`,
		`CREATE INDEX IF NOT EXISTS event_envelope_seq_idx
			ON event_envelope (event_seq)`,
		`CREATE TRIGGER IF NOT EXISTS event_envelope_append_only_update
			BEFORE UPDATE ON event_envelope
			BEGIN
				SELECT RAISE(ABORT, 'event_envelope is append-only');
			END`,
		`CREATE TRIGGER IF NOT EXISTS event_envelope_append_only_delete
			BEFORE DELETE ON event_envelope
			BEGIN
				SELECT RAISE(ABORT, 'event_envelope is append-only');
			END`,
		`CREATE TRIGGER IF NOT EXISTS event_dependency_append_only_update
			BEFORE UPDATE ON event_dependency
			BEGIN
				SELECT RAISE(ABORT, 'event_dependency is append-only');
			END`,
		`CREATE TRIGGER IF NOT EXISTS event_dependency_append_only_delete
			BEFORE DELETE ON event_dependency
			BEGIN
				SELECT RAISE(ABORT, 'event_dependency is append-only');
			END`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			return err
		}
	}
	return s.migrateLiveJournal()
}

func (s *SQLiteStore) validateExistingEventEnvelopeSchema() error {
	var exists int
	if err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'event_envelope')`).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return nil
	}

	rows, err := s.db.Query(`PRAGMA table_info(event_envelope)`)
	if err != nil {
		return err
	}
	foundEventSeq := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		if name == "event_seq" {
			if !strings.EqualFold(columnType, "INTEGER") || notNull != 1 {
				return fmt.Errorf("%w: existing event_envelope.event_seq must be INTEGER NOT NULL", errStep09MigrationRequired)
			}
			foundEventSeq = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if !foundEventSeq {
		return fmt.Errorf("%w: existing event_envelope.event_seq is absent", errStep09MigrationRequired)
	}
	hasUniqueSequence, hasSequenceIndex, err := s.validateEventSeqIndexes()
	if err != nil {
		return err
	}
	if !hasUniqueSequence || !hasSequenceIndex {
		return fmt.Errorf("%w: existing event_envelope.event_seq must be UNIQUE with event_envelope_seq_idx", errStep09MigrationRequired)
	}
	return nil
}

func (s *SQLiteStore) validateEventSeqIndexes() (bool, bool, error) {
	rows, err := s.db.Query(`SELECT name, "unique" FROM pragma_index_list('event_envelope')`)
	if err != nil {
		return false, false, err
	}
	type indexDefinition struct {
		name     string
		isUnique int
	}
	indexes := make([]indexDefinition, 0)
	for rows.Next() {
		var index indexDefinition
		if err := rows.Scan(&index.name, &index.isUnique); err != nil {
			return false, false, err
		}
		indexes = append(indexes, index)
	}
	if err := rows.Err(); err != nil {
		return false, false, err
	}
	if err := rows.Close(); err != nil {
		return false, false, err
	}
	hasUniqueSequence := false
	hasSequenceIndex := false
	for _, index := range indexes {
		columns, err := s.indexColumns(index.name)
		if err != nil {
			return false, false, err
		}
		if len(columns) != 1 || columns[0] != "event_seq" {
			continue
		}
		if index.isUnique == 1 {
			hasUniqueSequence = true
		}
		if index.name == "event_envelope_seq_idx" {
			hasSequenceIndex = true
		}
	}
	return hasUniqueSequence, hasSequenceIndex, nil
}

func (s *SQLiteStore) indexColumns(name string) ([]string, error) {
	quotedName := strings.ReplaceAll(name, "'", "''")
	rows, err := s.db.Query("PRAGMA index_info('" + quotedName + "')")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := make([]string, 0, 1)
	for rows.Next() {
		var sequence, columnID int
		var columnName sql.NullString
		if err := rows.Scan(&sequence, &columnID, &columnName); err != nil {
			return nil, err
		}
		if columnName.Valid {
			columns = append(columns, columnName.String)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return columns, nil
}

type eventSeqQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func currentMaxEventSeq(ctx context.Context, queryer eventSeqQueryer) (int64, error) {
	var maxSequence int64
	if err := queryer.QueryRowContext(ctx, `SELECT COALESCE(MAX(event_seq), 0) FROM event_envelope`).Scan(&maxSequence); err != nil {
		return 0, err
	}
	if maxSequence < 0 {
		return 0, errors.New("event_seq contains a negative persisted value")
	}
	return maxSequence, nil
}

func ensureEventSeqAvailable(ctx context.Context, tx *sql.Tx, eventSeq modulecore.EventSeq) error {
	var existing int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM event_envelope WHERE event_seq = ? LIMIT 1`, int64(eventSeq)).Scan(&existing)
	if err == nil {
		return fmt.Errorf("duplicate event_seq %d", eventSeq)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}

func (s *SQLiteStore) validateReferences(ctx context.Context, tx *sql.Tx, event modulecore.EventEnvelope) error {
	if event.CausationEventID != "" {
		if err := validateReference(ctx, tx, event, event.CausationEventID, "causation"); err != nil {
			return err
		}
	}
	for _, dependencyID := range event.DependencyEventIDs {
		if err := validateReference(ctx, tx, event, dependencyID, "dependency"); err != nil {
			return err
		}
	}
	return nil
}

func validateReference(ctx context.Context, tx *sql.Tx, event modulecore.EventEnvelope, referenceID modulecore.EventID, relationType string) error {
	var referencedTraceID string
	err := tx.QueryRowContext(ctx, `SELECT trace_id FROM event_envelope WHERE event_id = ?`, string(referenceID)).Scan(&referencedTraceID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s event %q does not exist", relationType, referenceID)
	}
	if err != nil {
		return err
	}
	if modulecore.TraceID(referencedTraceID) != event.TraceID {
		return fmt.Errorf("%s event %q belongs to another trace", relationType, referenceID)
	}
	return nil
}

func insertDependency(ctx context.Context, tx *sql.Tx, eventID, dependencyID modulecore.EventID, relationType string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO event_dependency (event_id, dependency_event_id, relation_type)
		VALUES (?, ?, ?)`, string(eventID), string(dependencyID), relationType)
	return err
}

func decodeStoredEnvelope(storedEventID string, storedEventSeq int64, traceID, schemaVersion, eventType, componentID, occurredAt, payload string) (modulecore.EventEnvelope, error) {
	var event modulecore.EventEnvelope
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		return modulecore.EventEnvelope{}, fmt.Errorf("decode event envelope %q: %w", storedEventID, err)
	}
	if err := modulecore.ValidateEventEnvelope(event); err != nil {
		return modulecore.EventEnvelope{}, fmt.Errorf("stored event %q: %w", storedEventID, err)
	}
	if err := event.EventSeq.Validate(); err != nil {
		return modulecore.EventEnvelope{}, fmt.Errorf("stored event %q event_seq: %w", storedEventID, err)
	}
	if string(event.EventID) != storedEventID || int64(event.EventSeq) != storedEventSeq || string(event.TraceID) != traceID || event.SchemaVersion != schemaVersion || event.EventType != eventType || event.ComponentID != componentID || event.OccurredAt.Format(time.RFC3339Nano) != occurredAt {
		return modulecore.EventEnvelope{}, fmt.Errorf("stored event %q envelope does not match canonical columns", storedEventID)
	}
	return event, nil
}

func boundedLimit(limit int) int {
	if limit <= 0 {
		return defaultListLimit
	}
	if limit > maxListLimit {
		return maxListLimit
	}
	return limit
}

func isSQLiteURI(path string) bool {
	return path == ":memory:" || strings.HasPrefix(path, "file:")
}
