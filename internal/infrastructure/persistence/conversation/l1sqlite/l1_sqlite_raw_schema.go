package l1sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const (
	commonRawSchemaMigrationName    = "conversation_l1_common_raw"
	commonRawSchemaMigrationVersion = 1
)

// applyCommonRawSchemaMigration installs the Common Raw contract in the
// existing conversation_l1 database. The marker is deliberately separate from
// the four logical Raw tables so reopening an existing database is a no-op.
func (s *L1SQLiteStore) applyCommonRawSchemaMigration(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin common raw schema migration: %w", err)
	}
	rollback := func(cause error) error {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && rollbackErr != sql.ErrTxDone {
			return fmt.Errorf("%w; rollback common raw schema migration: %v", cause, rollbackErr)
		}
		return cause
	}

	if _, err := tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS l1_schema_migrations (
		migration_name TEXT PRIMARY KEY,
		version INTEGER NOT NULL,
		applied_at TIMESTAMP NOT NULL
)`); err != nil {
		return rollback(fmt.Errorf("create l1 schema migration marker: %w", err))
	}

	var appliedVersion int
	markerErr := tx.QueryRowContext(ctx, `SELECT version FROM l1_schema_migrations WHERE migration_name = ?`, commonRawSchemaMigrationName).Scan(&appliedVersion)
	if markerErr == nil {
		if appliedVersion != commonRawSchemaMigrationVersion {
			return rollback(fmt.Errorf("common raw schema migration version %d is incompatible with %d", appliedVersion, commonRawSchemaMigrationVersion))
		}
		if err := verifyCommonRawSchemaObjects(ctx, tx); err != nil {
			return rollback(err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit existing common raw schema migration marker: %w", err)
		}
		return nil
	}
	if markerErr != sql.ErrNoRows {
		return rollback(fmt.Errorf("read common raw schema migration marker: %w", markerErr))
	}

	statements := []string{
		`CREATE TABLE IF NOT EXISTS l1_raw_source_manifest (
			manifest_id TEXT PRIMARY KEY,
			contract_version TEXT NOT NULL,
			source_type TEXT NOT NULL,
			source_identity TEXT NOT NULL,
			manifest_sha256 TEXT NOT NULL CHECK(length(manifest_sha256) = 64),
			source_count INTEGER NOT NULL CHECK(source_count >= 0),
			asset_count INTEGER NOT NULL CHECK(asset_count >= 0),
			schema_version TEXT NOT NULL,
			converter_version TEXT NOT NULL,
			owner_id TEXT NOT NULL,
			scope TEXT NOT NULL,
			sensitivity TEXT NOT NULL CHECK(sensitivity = 'private'),
			rights TEXT NOT NULL,
			license TEXT NOT NULL,
			provenance TEXT NOT NULL,
			allow_empty INTEGER NOT NULL CHECK(allow_empty IN (0, 1)),
			request_id TEXT NOT NULL,
			actor_id TEXT NOT NULL,
			intake_status TEXT NOT NULL CHECK(intake_status IN ('validating', 'committing', 'completed', 'rejected', 'blocked')),
			checkpoint_json TEXT NOT NULL,
			receipt_json TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			UNIQUE(owner_id, scope, source_type, source_identity, manifest_sha256)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_l1_raw_manifest_owner_created ON l1_raw_source_manifest(owner_id, created_at DESC, manifest_id)`,
		`CREATE INDEX IF NOT EXISTS idx_l1_raw_manifest_source ON l1_raw_source_manifest(source_type, source_identity, created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS l1_raw_record (
			raw_record_id TEXT PRIMARY KEY,
			manifest_id TEXT NOT NULL,
			contract_version TEXT NOT NULL,
			source_type TEXT NOT NULL,
			source_identity TEXT NOT NULL,
			source_record_id TEXT NOT NULL,
			parent_id TEXT NOT NULL DEFAULT '',
			thread_id TEXT NOT NULL DEFAULT '',
			owner_id TEXT NOT NULL,
			scope TEXT NOT NULL,
			sensitivity TEXT NOT NULL CHECK(sensitivity = 'private'),
			role TEXT NOT NULL,
			content_type TEXT NOT NULL,
			occurred_at TIMESTAMP NOT NULL,
			ingested_at TIMESTAMP NOT NULL,
			storage_kind TEXT NOT NULL CHECK(storage_kind IN ('inline', 'object')),
			inline_payload BLOB,
			object_ref TEXT NOT NULL DEFAULT '',
			content_sha256 TEXT NOT NULL CHECK(length(content_sha256) = 64),
			content_size INTEGER NOT NULL CHECK(content_size >= 0),
			asset_refs_json TEXT NOT NULL DEFAULT '[]',
			provenance TEXT NOT NULL,
			rights TEXT NOT NULL,
			license TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL,
			FOREIGN KEY(manifest_id) REFERENCES l1_raw_source_manifest(manifest_id),
			UNIQUE(owner_id, scope, source_type, source_identity, source_record_id),
			CHECK((storage_kind = 'inline' AND inline_payload IS NOT NULL AND object_ref = '') OR
				(storage_kind = 'object' AND inline_payload IS NULL AND length(object_ref) > 0))
		)`,
		`CREATE INDEX IF NOT EXISTS idx_l1_raw_record_manifest ON l1_raw_record(manifest_id, source_record_id)`,
		`CREATE INDEX IF NOT EXISTS idx_l1_raw_record_source ON l1_raw_record(owner_id, scope, source_type, source_identity, source_record_id)`,
		`CREATE INDEX IF NOT EXISTS idx_l1_raw_record_hash ON l1_raw_record(content_sha256)`,
		`CREATE TABLE IF NOT EXISTS l1_raw_state_event (
			state_event_id TEXT PRIMARY KEY,
			raw_record_id TEXT NOT NULL,
			manifest_id TEXT NOT NULL,
			event_type TEXT NOT NULL CHECK(event_type IN ('ingested', 'correction', 'forget', 'delete_applied', 'delete_blocked', 'supersede', 'reject', 'resume')),
			event_hash TEXT NOT NULL CHECK(length(event_hash) = 64),
			owner_id TEXT NOT NULL,
			scope TEXT NOT NULL,
			request_id TEXT NOT NULL,
			actor_id TEXT NOT NULL,
			reason_code TEXT NOT NULL DEFAULT '',
			payload_json TEXT NOT NULL DEFAULT '{}',
			created_at TIMESTAMP NOT NULL,
			FOREIGN KEY(raw_record_id) REFERENCES l1_raw_record(raw_record_id),
			FOREIGN KEY(manifest_id) REFERENCES l1_raw_source_manifest(manifest_id),
			UNIQUE(raw_record_id, event_type, event_hash)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_l1_raw_state_record_created ON l1_raw_state_event(raw_record_id, created_at ASC, state_event_id)`,
		`CREATE TABLE IF NOT EXISTS l1_raw_projection_receipt (
			projection_receipt_id TEXT PRIMARY KEY,
			projection_type TEXT NOT NULL,
			output_store TEXT NOT NULL,
			output_record_id TEXT NOT NULL,
			raw_record_ids_json TEXT NOT NULL,
			revision TEXT NOT NULL,
			input_sha256 TEXT NOT NULL CHECK(length(input_sha256) = 64),
			output_sha256 TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			failure_reason TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_l1_raw_projection_record_revision ON l1_raw_projection_receipt(revision, projection_type, created_at)`,
		`CREATE TRIGGER IF NOT EXISTS trg_l1_raw_manifest_immutable_update BEFORE UPDATE ON l1_raw_source_manifest BEGIN SELECT RAISE(ABORT, 'l1 raw source manifest is immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS trg_l1_raw_manifest_immutable_delete BEFORE DELETE ON l1_raw_source_manifest BEGIN SELECT RAISE(ABORT, 'l1 raw source manifest is immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS trg_l1_raw_record_immutable_update BEFORE UPDATE ON l1_raw_record BEGIN SELECT RAISE(ABORT, 'l1 raw record is immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS trg_l1_raw_record_immutable_delete BEFORE DELETE ON l1_raw_record BEGIN SELECT RAISE(ABORT, 'l1 raw record is immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS trg_l1_raw_state_immutable_update BEFORE UPDATE ON l1_raw_state_event BEGIN SELECT RAISE(ABORT, 'l1 raw state event is immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS trg_l1_raw_state_immutable_delete BEFORE DELETE ON l1_raw_state_event BEGIN SELECT RAISE(ABORT, 'l1 raw state event is immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS trg_l1_raw_projection_immutable_update BEFORE UPDATE ON l1_raw_projection_receipt BEGIN SELECT RAISE(ABORT, 'l1 raw projection receipt is immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS trg_l1_raw_projection_immutable_delete BEFORE DELETE ON l1_raw_projection_receipt BEGIN SELECT RAISE(ABORT, 'l1 raw projection receipt is immutable'); END`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return rollback(fmt.Errorf("apply common raw schema statement: %w", err))
		}
	}
	if err := verifyCommonRawSchemaObjects(ctx, tx); err != nil {
		return rollback(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO l1_schema_migrations (migration_name, version, applied_at) VALUES (?, ?, ?)`, commonRawSchemaMigrationName, commonRawSchemaMigrationVersion, time.Now().UTC()); err != nil {
		return rollback(fmt.Errorf("record common raw schema migration marker: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit common raw schema migration: %w", err)
	}
	return nil
}

func verifyCommonRawSchemaObjects(ctx context.Context, tx *sql.Tx) error {
	objects := []struct {
		kind string
		name string
	}{
		{kind: "table", name: "l1_raw_source_manifest"},
		{kind: "table", name: "l1_raw_record"},
		{kind: "table", name: "l1_raw_state_event"},
		{kind: "table", name: "l1_raw_projection_receipt"},
		{kind: "index", name: "idx_l1_raw_manifest_owner_created"},
		{kind: "index", name: "idx_l1_raw_manifest_source"},
		{kind: "index", name: "idx_l1_raw_record_manifest"},
		{kind: "index", name: "idx_l1_raw_record_source"},
		{kind: "index", name: "idx_l1_raw_record_hash"},
		{kind: "index", name: "idx_l1_raw_state_record_created"},
		{kind: "index", name: "idx_l1_raw_projection_record_revision"},
		{kind: "trigger", name: "trg_l1_raw_manifest_immutable_update"},
		{kind: "trigger", name: "trg_l1_raw_manifest_immutable_delete"},
		{kind: "trigger", name: "trg_l1_raw_record_immutable_update"},
		{kind: "trigger", name: "trg_l1_raw_record_immutable_delete"},
		{kind: "trigger", name: "trg_l1_raw_state_immutable_update"},
		{kind: "trigger", name: "trg_l1_raw_state_immutable_delete"},
		{kind: "trigger", name: "trg_l1_raw_projection_immutable_update"},
		{kind: "trigger", name: "trg_l1_raw_projection_immutable_delete"},
	}
	for _, object := range objects {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = ? AND name = ?`, object.kind, object.name).Scan(&count); err != nil {
			return fmt.Errorf("verify common raw schema %s %s: %w", object.kind, object.name, err)
		}
		if count != 1 {
			return fmt.Errorf("common raw schema marker exists but %s %s is missing", object.kind, object.name)
		}
	}
	requiredColumns := []struct {
		table   string
		columns []string
	}{
		{table: "l1_raw_source_manifest", columns: []string{
			"manifest_id", "contract_version", "source_type", "source_identity", "manifest_sha256",
			"source_count", "asset_count", "schema_version", "converter_version", "owner_id", "scope",
			"sensitivity", "rights", "license", "provenance", "allow_empty", "request_id", "actor_id",
			"intake_status", "checkpoint_json", "receipt_json", "created_at", "updated_at",
		}},
		{table: "l1_raw_record", columns: []string{
			"raw_record_id", "manifest_id", "contract_version", "source_type", "source_identity", "source_record_id",
			"parent_id", "thread_id", "owner_id", "scope", "sensitivity", "role", "content_type", "occurred_at",
			"ingested_at", "storage_kind", "inline_payload", "object_ref", "content_sha256", "content_size",
			"asset_refs_json", "provenance", "rights", "license", "created_at",
		}},
		{table: "l1_raw_state_event", columns: []string{
			"state_event_id", "raw_record_id", "manifest_id", "event_type", "event_hash", "owner_id", "scope",
			"request_id", "actor_id", "reason_code", "payload_json", "created_at",
		}},
		{table: "l1_raw_projection_receipt", columns: []string{
			"projection_receipt_id", "projection_type", "output_store", "output_record_id", "raw_record_ids_json",
			"revision", "input_sha256", "output_sha256", "status", "created_at", "updated_at", "failure_reason",
		}},
	}
	for _, tableSpec := range requiredColumns {
		table := tableSpec.table
		columns := tableSpec.columns
		present := make(map[string]struct{}, len(columns))
		rows, err := tx.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", table))
		if err != nil {
			return fmt.Errorf("verify common raw schema columns for %s: %w", table, err)
		}
		for rows.Next() {
			var cid int
			var name, columnType string
			var notNull, primaryKey int
			var defaultValue sql.NullString
			if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan common raw schema columns for %s: %w", table, err)
			}
			present[name] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("iterate common raw schema columns for %s: %w", table, err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close common raw schema columns for %s: %w", table, err)
		}
		for _, column := range columns {
			if _, ok := present[column]; !ok {
				return fmt.Errorf("common raw schema table %s is missing required column %s", table, column)
			}
		}
	}
	return nil
}
