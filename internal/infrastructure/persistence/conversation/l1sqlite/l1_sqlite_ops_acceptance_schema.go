package l1sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const (
	opsAcceptanceMigrationName    = "conversation_l1_ops_input_acceptance"
	opsAcceptanceMigrationVersion = 1
	opsAcceptanceTable            = "conversation_ops_input_acceptance"
)

func (s *L1SQLiteStore) applyOPSInputAcceptanceSchemaMigration(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("ops input acceptance schema store is unavailable")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin ops input acceptance schema migration: %w", err)
	}
	rollback := func(cause error) error {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && rollbackErr != sql.ErrTxDone {
			return fmt.Errorf("%w; rollback ops input acceptance schema migration: %v", cause, rollbackErr)
		}
		return cause
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS l1_schema_migrations (
		migration_name TEXT PRIMARY KEY,
		version INTEGER NOT NULL,
		applied_at TIMESTAMP NOT NULL
	)`); err != nil {
		return rollback(fmt.Errorf("create ops input acceptance migration marker: %w", err))
	}

	var appliedVersion int
	markerErr := tx.QueryRowContext(ctx, `SELECT version FROM l1_schema_migrations WHERE migration_name = ?`, opsAcceptanceMigrationName).Scan(&appliedVersion)
	if markerErr == nil {
		if appliedVersion != opsAcceptanceMigrationVersion {
			return rollback(fmt.Errorf("ops input acceptance schema version %d is incompatible with %d", appliedVersion, opsAcceptanceMigrationVersion))
		}
		if err := verifyOPSInputAcceptanceSchema(ctx, tx); err != nil {
			return rollback(err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit existing ops input acceptance schema marker: %w", err)
		}
		return nil
	}
	if markerErr != sql.ErrNoRows {
		return rollback(fmt.Errorf("read ops input acceptance migration marker: %w", markerErr))
	}

	// The FOREIGN KEY clauses below document the receipt references. L1 SQLite
	// currently leaves PRAGMA foreign_keys disabled on shared connections, so
	// the acceptance transaction and exact reader enforce reference integrity.
	statements := []string{
		`CREATE TABLE IF NOT EXISTS conversation_ops_input_acceptance (
			acceptance_sequence INTEGER PRIMARY KEY AUTOINCREMENT CHECK(acceptance_sequence > 0),
			owner_id TEXT NOT NULL CHECK(length(CAST(owner_id AS BLOB)) BETWEEN 1 AND 255),
			actor_id TEXT NOT NULL CHECK(actor_id = owner_id),
			request_id TEXT NOT NULL CHECK(length(CAST(request_id AS BLOB)) BETWEEN 1 AND 128),
			payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256) = 64 AND lower(payload_sha256) = payload_sha256 AND payload_sha256 NOT GLOB '*[^0-9a-f]*'),
			declared_origin TEXT NOT NULL CHECK(declared_origin IN ('automation','human')),
			session_id TEXT NOT NULL CHECK(length(session_id) > 0),
			thread_id TEXT NOT NULL CHECK(length(thread_id) > 0),
			thread_seq INTEGER NOT NULL CHECK(thread_seq = 1),
			thread_kind TEXT NOT NULL CHECK(thread_kind = 'user_conversation'),
			task_id TEXT NOT NULL CHECK(length(task_id) > 0),
			turn_id TEXT NOT NULL CHECK(length(turn_id) > 0),
			trace_id TEXT NOT NULL CHECK(length(trace_id) > 0),
			user_message_id TEXT NOT NULL CHECK(length(user_message_id) > 0),
			agent_message_id TEXT NOT NULL CHECK(length(agent_message_id) > 0 AND agent_message_id <> user_message_id),
			raw_record_id TEXT NOT NULL,
			manifest_id TEXT NOT NULL,
			raw_sha256 TEXT NOT NULL CHECK(length(raw_sha256) = 64 AND lower(raw_sha256) = raw_sha256 AND raw_sha256 NOT GLOB '*[^0-9a-f]*'),
			manifest_sha256 TEXT NOT NULL CHECK(length(manifest_sha256) = 64 AND lower(manifest_sha256) = manifest_sha256 AND manifest_sha256 NOT GLOB '*[^0-9a-f]*'),
			accepted_at TIMESTAMP NOT NULL,
			UNIQUE(owner_id, request_id),
			UNIQUE(session_id),
			UNIQUE(thread_id),
			UNIQUE(turn_id),
			UNIQUE(user_message_id),
			UNIQUE(agent_message_id),
			FOREIGN KEY(raw_record_id) REFERENCES l1_raw_record(raw_record_id),
			FOREIGN KEY(manifest_id) REFERENCES l1_raw_source_manifest(manifest_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_ops_input_acceptance_owner_sequence ON conversation_ops_input_acceptance(owner_id, acceptance_sequence DESC)`,
		`CREATE TRIGGER IF NOT EXISTS trg_ops_input_acceptance_immutable_update BEFORE UPDATE ON conversation_ops_input_acceptance BEGIN SELECT RAISE(ABORT, 'ops input acceptance is immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS trg_ops_input_acceptance_immutable_delete BEFORE DELETE ON conversation_ops_input_acceptance BEGIN SELECT RAISE(ABORT, 'ops input acceptance is immutable'); END`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return rollback(fmt.Errorf("apply ops input acceptance schema statement: %w", err))
		}
	}
	if err := verifyOPSInputAcceptanceSchema(ctx, tx); err != nil {
		return rollback(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO l1_schema_migrations (migration_name, version, applied_at) VALUES (?, ?, ?)`, opsAcceptanceMigrationName, opsAcceptanceMigrationVersion, time.Now().UTC()); err != nil {
		return rollback(fmt.Errorf("record ops input acceptance schema migration: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit ops input acceptance schema migration: %w", err)
	}
	return nil
}

func verifyOPSInputAcceptanceSchema(ctx context.Context, tx *sql.Tx) error {
	for _, object := range []struct {
		kind string
		name string
	}{
		{kind: "table", name: opsAcceptanceTable},
		{kind: "index", name: "idx_ops_input_acceptance_owner_sequence"},
		{kind: "trigger", name: "trg_ops_input_acceptance_immutable_update"},
		{kind: "trigger", name: "trg_ops_input_acceptance_immutable_delete"},
	} {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = ? AND name = ?`, object.kind, object.name).Scan(&count); err != nil {
			return fmt.Errorf("verify ops input acceptance %s %s: %w", object.kind, object.name, err)
		}
		if count != 1 {
			return fmt.Errorf("ops input acceptance marker exists but %s %s is missing", object.kind, object.name)
		}
	}
	for _, trigger := range []string{
		"trg_ops_input_acceptance_immutable_update",
		"trg_ops_input_acceptance_immutable_delete",
	} {
		var definition string
		if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type = 'trigger' AND name = ?`, trigger).Scan(&definition); err != nil {
			return fmt.Errorf("read ops input acceptance trigger %s: %w", trigger, err)
		}
		definition = strings.ToLower(definition)
		if !strings.Contains(definition, "before ") || !strings.Contains(definition, "raise(abort") {
			return fmt.Errorf("ops input acceptance trigger %s is not immutable", trigger)
		}
	}
	var createSQL string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, opsAcceptanceTable).Scan(&createSQL); err != nil {
		return fmt.Errorf("read ops input acceptance table definition: %w", err)
	}
	for _, required := range []string{
		"acceptance_sequence INTEGER PRIMARY KEY AUTOINCREMENT",
		"UNIQUE(owner_id, request_id)",
		"UNIQUE(session_id)",
		"CHECK(declared_origin IN ('automation','human'))",
		"FOREIGN KEY(raw_record_id) REFERENCES l1_raw_record",
		"FOREIGN KEY(manifest_id) REFERENCES l1_raw_source_manifest",
	} {
		if !strings.Contains(strings.ToLower(createSQL), strings.ToLower(required)) {
			return fmt.Errorf("ops input acceptance table is missing required constraint %q", required)
		}
	}
	requiredColumns := []string{
		"acceptance_sequence", "owner_id", "actor_id", "request_id", "payload_sha256", "declared_origin",
		"session_id", "thread_id", "thread_seq", "thread_kind", "task_id", "turn_id", "trace_id",
		"user_message_id", "agent_message_id", "raw_record_id", "manifest_id", "raw_sha256",
		"manifest_sha256", "accepted_at",
	}
	present := make(map[string]struct{}, len(requiredColumns))
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(conversation_ops_input_acceptance)`)
	if err != nil {
		return fmt.Errorf("inspect ops input acceptance columns: %w", err)
	}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan ops input acceptance columns: %w", err)
		}
		present[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate ops input acceptance columns: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close ops input acceptance columns: %w", err)
	}
	for _, column := range requiredColumns {
		if _, ok := present[column]; !ok {
			return fmt.Errorf("ops input acceptance schema is missing column %s", column)
		}
	}
	if len(present) != len(requiredColumns) {
		return fmt.Errorf("ops input acceptance schema has unexpected columns")
	}
	if _, hasRawText := present["raw_message"]; hasRawText {
		return fmt.Errorf("ops input acceptance receipt must not store raw message text")
	}
	return nil
}
