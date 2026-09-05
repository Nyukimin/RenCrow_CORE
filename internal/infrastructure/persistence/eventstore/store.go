package eventstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	db *sql.DB
}

// NewSQLiteStore opens a SQLite canonical event store at the configured path.
// It never falls back to a repository or runtime-home location.
func NewSQLiteStore(path string) (*SQLiteStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("canonical event store path is required")
	}
	if !isSQLiteURI(path) {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
	}

	dsn := path
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	dsn += fmt.Sprintf("%s_pragma=busy_timeout%%3d%d&_time_format=sqlite", separator, sqliteBusyTimeoutMilliseconds)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &SQLiteStore{db: db}
	if err := store.enableForeignKeys(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// Close releases the underlying SQLite connection. It is safe to call on a
// nil or already uninitialized store.
func (s *SQLiteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
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
	if err := s.ensureOpen(); err != nil {
		return modulecore.EventEnvelope{}, err
	}
	if err := modulecore.ValidateEventEnvelope(event); err != nil {
		return modulecore.EventEnvelope{}, fmt.Errorf("validate event envelope: %w", err)
	}
	if event.EventSeq != 0 {
		return modulecore.EventEnvelope{}, errors.New("event_seq must be zero for live append; positive sequences are reserved for AppendBatch")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return modulecore.EventEnvelope{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	event.EventSeq, err = nextEventSeq(ctx, tx)
	if err != nil {
		return modulecore.EventEnvelope{}, err
	}
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	maxSequence, err := currentMaxEventSeq(ctx, tx)
	if err != nil {
		return err
	}
	assigned := append([]modulecore.EventEnvelope(nil), events...)
	payloads := make([][]byte, len(assigned))
	seenSequences := make(map[modulecore.EventSeq]struct{}, len(assigned))
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
		seenSequences[event.EventSeq] = struct{}{}
	}
	for index := range assigned {
		event := &assigned[index]
		if event.EventSeq == 0 {
			for {
				if maxSequence == maxEventSeqValue {
					return errors.New("event_seq exhausted")
				}
				maxSequence++
				candidate := modulecore.EventSeq(maxSequence)
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
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
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
	return nil
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

func currentMaxEventSeq(ctx context.Context, tx *sql.Tx) (int64, error) {
	var maxSequence int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(event_seq), 0) FROM event_envelope`).Scan(&maxSequence); err != nil {
		return 0, err
	}
	if maxSequence < 0 {
		return 0, errors.New("event_seq contains a negative persisted value")
	}
	return maxSequence, nil
}

func nextEventSeq(ctx context.Context, tx *sql.Tx) (modulecore.EventSeq, error) {
	maxSequence, err := currentMaxEventSeq(ctx, tx)
	if err != nil {
		return 0, err
	}
	if maxSequence >= maxEventSeqValue {
		return 0, errors.New("event_seq exhausted")
	}
	return modulecore.EventSeq(maxSequence + 1), nil
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
