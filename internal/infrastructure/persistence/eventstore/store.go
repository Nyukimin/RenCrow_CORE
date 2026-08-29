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
)

var errStoreClosed = errors.New("event store is closed")

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
	if err := s.ensureOpen(); err != nil {
		return err
	}
	if err := modulecore.ValidateEventEnvelope(event); err != nil {
		return fmt.Errorf("validate event envelope: %w", err)
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event envelope: %w", err)
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

	var existing int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM event_envelope WHERE event_id = ? LIMIT 1`, string(event.EventID)).Scan(&existing)
	if err == nil {
		return fmt.Errorf("duplicate event_id %q", event.EventID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	if err := s.validateReferences(ctx, tx, event); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO event_envelope
			(event_id, trace_id, schema_version, event_type, component_id, occurred_at, envelope_json)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		string(event.EventID),
		string(event.TraceID),
		event.SchemaVersion,
		event.EventType,
		event.ComponentID,
		event.OccurredAt.Format(time.RFC3339Nano),
		string(payload),
	); err != nil {
		return err
	}

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
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
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
	for _, event := range events {
		var existing int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM event_envelope WHERE event_id = ? LIMIT 1`, string(event.EventID)).Scan(&existing)
		if err == nil {
			return fmt.Errorf("duplicate event_id %q", event.EventID)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		payload, err := json.Marshal(event)
		if err != nil {
			return fmt.Errorf("marshal event envelope: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO event_envelope
				(event_id, trace_id, schema_version, event_type, component_id, occurred_at, envelope_json)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			string(event.EventID), string(event.TraceID), event.SchemaVersion,
			event.EventType, event.ComponentID, event.OccurredAt.Format(time.RFC3339Nano), string(payload),
		); err != nil {
			return err
		}
	}
	for _, event := range events {
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
	err := s.db.QueryRowContext(ctx, `
		SELECT event_id, trace_id, schema_version, event_type, component_id, occurred_at, envelope_json
		FROM event_envelope
		WHERE event_id = ?`, string(eventID)).Scan(
		&storedEventID, &traceID, &schemaVersion, &eventType, &componentID, &occurredAt, &payload,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return modulecore.EventEnvelope{}, false, nil
	}
	if err != nil {
		return modulecore.EventEnvelope{}, false, err
	}
	event, err := decodeStoredEnvelope(storedEventID, traceID, schemaVersion, eventType, componentID, occurredAt, payload)
	if err != nil {
		return modulecore.EventEnvelope{}, false, err
	}
	return event, true, nil
}

// ListByComponent returns at most limit canonical envelopes for componentID,
// ordered newest first by occurrence time and then event ID. Non-positive limits use the
// default bound and larger limits are capped.
func (s *SQLiteStore) ListByComponent(ctx context.Context, componentID string, limit int) ([]modulecore.EventEnvelope, error) {
	if err := s.ensureOpen(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(componentID) == "" {
		return nil, fmt.Errorf("component_id is required")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT event_id, trace_id, schema_version, event_type, component_id, occurred_at, envelope_json
		FROM event_envelope
		WHERE component_id = ?
		ORDER BY occurred_at DESC, event_id DESC
		LIMIT ?`, componentID, boundedLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := make([]modulecore.EventEnvelope, 0)
	for rows.Next() {
		var storedEventID, traceID, schemaVersion, eventType, storedComponentID, occurredAt, payload string
		if err := rows.Scan(&storedEventID, &traceID, &schemaVersion, &eventType, &storedComponentID, &occurredAt, &payload); err != nil {
			return nil, err
		}
		event, err := decodeStoredEnvelope(storedEventID, traceID, schemaVersion, eventType, storedComponentID, occurredAt, payload)
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
	statements := []string{
		`CREATE TABLE IF NOT EXISTS event_envelope (
			event_id TEXT PRIMARY KEY NOT NULL,
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
			ON event_envelope (component_id, occurred_at, event_id)`,
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

func decodeStoredEnvelope(storedEventID, traceID, schemaVersion, eventType, componentID, occurredAt, payload string) (modulecore.EventEnvelope, error) {
	var event modulecore.EventEnvelope
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		return modulecore.EventEnvelope{}, fmt.Errorf("decode event envelope %q: %w", storedEventID, err)
	}
	if err := modulecore.ValidateEventEnvelope(event); err != nil {
		return modulecore.EventEnvelope{}, fmt.Errorf("stored event %q: %w", storedEventID, err)
	}
	if string(event.EventID) != storedEventID || string(event.TraceID) != traceID || event.SchemaVersion != schemaVersion || event.EventType != eventType || event.ComponentID != componentID || event.OccurredAt.Format(time.RFC3339Nano) != occurredAt {
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
