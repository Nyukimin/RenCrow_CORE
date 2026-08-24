package l1sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

const (
	chatGPTProfilePromotionBindingTable            = "l1_chatgpt_profile_promotion_binding"
	chatGPTProfilePromotionBindingMigrationName    = "conversation_l1_chatgpt_profile_promotion_binding"
	chatGPTProfilePromotionBindingMigrationVersion = 1
)

// applyChatGPTProfilePromotionBindingSchema adds the immutable membership
// evidence used to scope ProfilePromotion jobs to one authenticated export.
// It runs inside the import-ledger schema transaction so a store cannot expose
// a ledger without the binding contract.
func applyChatGPTProfilePromotionBindingSchema(ctx context.Context, tx *sql.Tx) error {
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS l1_chatgpt_profile_promotion_binding (
			owner_id TEXT NOT NULL CHECK(length(owner_id) > 0),
			export_id TEXT NOT NULL CHECK(length(export_id) > 0),
			evidence_event_id TEXT NOT NULL CHECK(length(evidence_event_id) > 0),
			created_at TIMESTAMP NOT NULL,
			PRIMARY KEY(owner_id, export_id, evidence_event_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_l1_chatgpt_profile_promotion_binding_owner_export
			ON l1_chatgpt_profile_promotion_binding(owner_id, export_id, evidence_event_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_l1_chatgpt_profile_promotion_binding_evidence_unique
			ON l1_chatgpt_profile_promotion_binding(evidence_event_id)`,
		`CREATE TRIGGER IF NOT EXISTS trg_l1_chatgpt_profile_promotion_binding_immutable_update
			BEFORE UPDATE ON l1_chatgpt_profile_promotion_binding
			BEGIN SELECT RAISE(ABORT, 'l1 ChatGPT profile promotion binding is immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS trg_l1_chatgpt_profile_promotion_binding_immutable_delete
			BEFORE DELETE ON l1_chatgpt_profile_promotion_binding
			BEGIN SELECT RAISE(ABORT, 'l1 ChatGPT profile promotion binding is immutable'); END`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return chatGPTImportInternalError("ChatGPT profile promotion binding schema update failed")
		}
	}
	if err := verifyChatGPTProfilePromotionBindingSchema(ctx, tx); err != nil {
		return err
	}
	var appliedVersion int
	markerErr := tx.QueryRowContext(ctx, `
SELECT version FROM l1_schema_migrations WHERE migration_name = ?
`, chatGPTProfilePromotionBindingMigrationName).Scan(&appliedVersion)
	switch {
	case markerErr == nil:
		if appliedVersion != chatGPTProfilePromotionBindingMigrationVersion {
			return chatGPTImportInternalError("ChatGPT profile promotion binding migration version is incompatible")
		}
	case errors.Is(markerErr, sql.ErrNoRows):
		if err := backfillChatGPTProfilePromotionBindings(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO l1_schema_migrations (migration_name, version, applied_at)
VALUES (?, ?, ?)
`, chatGPTProfilePromotionBindingMigrationName, chatGPTProfilePromotionBindingMigrationVersion, time.Now().UTC()); err != nil {
			return chatGPTImportInternalError("ChatGPT profile promotion binding migration marker could not be recorded")
		}
	default:
		return chatGPTImportInternalError("ChatGPT profile promotion binding migration marker could not be read")
	}
	return nil
}

func verifyChatGPTProfilePromotionBindingSchema(ctx context.Context, tx *sql.Tx) error {
	for _, object := range []struct {
		kind string
		name string
	}{
		{kind: "table", name: chatGPTProfilePromotionBindingTable},
		{kind: "index", name: "idx_l1_chatgpt_profile_promotion_binding_owner_export"},
		{kind: "index", name: "idx_l1_chatgpt_profile_promotion_binding_evidence_unique"},
		{kind: "trigger", name: "trg_l1_chatgpt_profile_promotion_binding_immutable_update"},
		{kind: "trigger", name: "trg_l1_chatgpt_profile_promotion_binding_immutable_delete"},
	} {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = ? AND name = ?`, object.kind, object.name).Scan(&count); err != nil {
			return chatGPTImportInternalError("ChatGPT profile promotion binding schema verification failed")
		}
		if count != 1 {
			return chatGPTImportInternalError("ChatGPT profile promotion binding schema is incomplete")
		}
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(l1_chatgpt_profile_promotion_binding)`)
	if err != nil {
		return chatGPTImportInternalError("ChatGPT profile promotion binding columns cannot be read")
	}
	columns := make(map[string]int, 4)
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return chatGPTImportInternalError("ChatGPT profile promotion binding columns cannot be read")
		}
		if notNull != 1 {
			return chatGPTImportInternalError("ChatGPT profile promotion binding column is nullable")
		}
		columns[name] = primaryKey
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return chatGPTImportInternalError("ChatGPT profile promotion binding columns cannot be read")
	}
	if err := rows.Close(); err != nil {
		return chatGPTImportInternalError("ChatGPT profile promotion binding columns cannot be read")
	}
	for _, name := range []string{"owner_id", "export_id", "evidence_event_id", "created_at"} {
		if _, ok := columns[name]; !ok {
			return chatGPTImportInternalError("ChatGPT profile promotion binding schema is incomplete")
		}
	}
	if columns["owner_id"] != 1 || columns["export_id"] != 2 || columns["evidence_event_id"] != 3 || columns["created_at"] != 0 {
		return chatGPTImportInternalError("ChatGPT profile promotion binding primary key is incomplete")
	}
	if err := verifyChatGPTProfilePromotionBindingIndex(ctx, tx, "idx_l1_chatgpt_profile_promotion_binding_owner_export", false, []string{"owner_id", "export_id", "evidence_event_id"}); err != nil {
		return err
	}
	if err := verifyChatGPTProfilePromotionBindingIndex(ctx, tx, "idx_l1_chatgpt_profile_promotion_binding_evidence_unique", true, []string{"evidence_event_id"}); err != nil {
		return err
	}
	return nil
}

func verifyChatGPTProfilePromotionBindingIndex(ctx context.Context, tx *sql.Tx, indexName string, wantUnique bool, wantColumns []string) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA index_list(l1_chatgpt_profile_promotion_binding)`)
	if err != nil {
		return chatGPTImportInternalError("ChatGPT profile promotion binding indexes cannot be read")
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var sequence, unique, partial int
		var origin string
		var name string
		if err := rows.Scan(&sequence, &name, &unique, &origin, &partial); err != nil {
			return chatGPTImportInternalError("ChatGPT profile promotion binding indexes cannot be read")
		}
		if name == indexName {
			found = unique == boolInt(wantUnique)
			break
		}
	}
	if err := rows.Err(); err != nil {
		return chatGPTImportInternalError("ChatGPT profile promotion binding indexes cannot be read")
	}
	if !found {
		return chatGPTImportInternalError("ChatGPT profile promotion binding index uniqueness is incomplete")
	}

	indexRows, err := tx.QueryContext(ctx, `PRAGMA index_info(idx_l1_chatgpt_profile_promotion_binding_owner_export)`)
	if indexName == "idx_l1_chatgpt_profile_promotion_binding_evidence_unique" {
		indexRows, err = tx.QueryContext(ctx, `PRAGMA index_info(idx_l1_chatgpt_profile_promotion_binding_evidence_unique)`)
	}
	if err != nil {
		return chatGPTImportInternalError("ChatGPT profile promotion binding index columns cannot be read")
	}
	defer indexRows.Close()
	columns := make([]string, 0, len(wantColumns))
	for indexRows.Next() {
		var sequence, columnID int
		var name string
		if err := indexRows.Scan(&sequence, &columnID, &name); err != nil {
			return chatGPTImportInternalError("ChatGPT profile promotion binding index columns cannot be read")
		}
		columns = append(columns, name)
	}
	if err := indexRows.Err(); err != nil {
		return chatGPTImportInternalError("ChatGPT profile promotion binding index columns cannot be read")
	}
	if len(columns) != len(wantColumns) {
		return chatGPTImportInternalError("ChatGPT profile promotion binding index columns are incomplete")
	}
	for i := range wantColumns {
		if columns[i] != wantColumns[i] {
			return chatGPTImportInternalError("ChatGPT profile promotion binding index columns are incomplete")
		}
	}
	return nil
}

// backfillChatGPTProfilePromotionBindings is deliberately SQL-only and
// idempotent. Invalid metadata is filtered with json_valid and every JSON
// extraction is guarded by CASE, so malformed legacy rows remain unbound and
// cannot prevent schema startup. Progress/finalize subsequently fail closed
// when the immutable event count cannot be reconciled with these bindings.
func backfillChatGPTProfilePromotionBindings(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO l1_chatgpt_profile_promotion_binding (
	owner_id, export_id, evidence_event_id, created_at
)
SELECT r.owner_id, r.source_identity, j.evidence_event_id, CURRENT_TIMESTAMP
FROM l1_raw_record r
JOIN l1_memory_event e
	ON e.id = r.source_record_id
	AND e.speaker = 'user'
	AND e.source = 'chatgpt_export'
	AND e.layer = 'L3'
JOIN l1_profile_promotion_job j
	ON j.evidence_event_id = e.id
	AND j.session_id = e.session_id
	AND j.thread_id = e.thread_id
WHERE r.source_type = 'chatgpt_export'
	AND r.scope = 'user:' || r.owner_id
	AND r.role = 'user'
	AND json_valid(e.meta_json) = 1
	AND CASE WHEN json_valid(e.meta_json) = 1 THEN json_extract(e.meta_json, '$.external_source') ELSE NULL END = 'chatgpt_export'
	AND CASE WHEN json_valid(e.meta_json) = 1 THEN json_extract(e.meta_json, '$.export_id') ELSE NULL END = r.source_identity
	AND CASE WHEN json_valid(e.meta_json) = 1 THEN json_extract(e.meta_json, '$.original_role') ELSE NULL END = 'user'
	AND CASE WHEN json_valid(e.meta_json) = 1 THEN json_extract(e.meta_json, '$.on_current_branch') ELSE NULL END = 1
	AND j.state IN ('pending', 'running', 'retry_wait', 'failed', 'completed')
ORDER BY r.owner_id, r.source_identity, j.evidence_event_id
`)
	if err != nil {
		return chatGPTImportInternalError("ChatGPT profile promotion binding backfill failed")
	}
	return nil
}

// ensureChatGPTProfilePromotionBindingTx inserts the membership evidence in
// the same transaction that creates or validates the existing job. It never
// stores job state and rejects an evidence ID already assigned to another
// owner/export instead of silently moving it.
func ensureChatGPTProfilePromotionBindingTx(ctx context.Context, tx *sql.Tx, ownerID, exportID, evidenceEventID string) error {
	var storedOwner, storedExport string
	err := tx.QueryRowContext(ctx, `
SELECT owner_id, export_id
FROM l1_chatgpt_profile_promotion_binding
WHERE evidence_event_id = ?
ORDER BY owner_id, export_id
LIMIT 1`, evidenceEventID).Scan(&storedOwner, &storedExport)
	if err == nil {
		if storedOwner != ownerID || storedExport != exportID {
			return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "ChatGPT profile promotion binding belongs to another owner or export")
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read ChatGPT profile promotion binding: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO l1_chatgpt_profile_promotion_binding (owner_id, export_id, evidence_event_id, created_at)
VALUES (?, ?, ?, CURRENT_TIMESTAMP)`, ownerID, exportID, evidenceEventID); err != nil {
		return fmt.Errorf("insert ChatGPT profile promotion binding: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `
SELECT owner_id, export_id
FROM l1_chatgpt_profile_promotion_binding
WHERE evidence_event_id = ?
ORDER BY owner_id, export_id
LIMIT 1`, evidenceEventID).Scan(&storedOwner, &storedExport); err != nil {
		return fmt.Errorf("verify ChatGPT profile promotion binding: %w", err)
	}
	if storedOwner != ownerID || storedExport != exportID {
		return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "ChatGPT profile promotion binding belongs to another owner or export")
	}
	return nil
}
