package l1sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const chatGPTImportConfirmReceiptTable = "l1_chatgpt_import_confirm_receipt"

// applyChatGPTImportConfirmSchema installs the append-only confirmation
// receipt contract on every store open. It is intentionally separate from the
// import ledger: a valid ledger must not imply that confirmation receipts are
// present or structurally sound.
func (s *L1SQLiteStore) applyChatGPTImportConfirmSchema(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin ChatGPT import confirmation schema: %w", err)
	}
	rollback := func(cause error) error {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return errors.Join(cause, fmt.Errorf("rollback ChatGPT import confirmation schema: %w", rollbackErr))
		}
		return cause
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS l1_chatgpt_import_confirm_receipt (
			receipt_id TEXT PRIMARY KEY CHECK(length(receipt_id) > 0),
			request_id TEXT NOT NULL CHECK(length(request_id) > 0),
			owner_id TEXT NOT NULL CHECK(length(owner_id) > 0),
			actor_id TEXT NOT NULL CHECK(length(actor_id) > 0),
			export_id TEXT NOT NULL CHECK(length(export_id) > 0),
			apply INTEGER NOT NULL CHECK(apply = 1),
			reason_hash TEXT NOT NULL CHECK(length(reason_hash) = 64 AND lower(reason_hash) = reason_hash AND reason_hash NOT GLOB '*[^0-9a-f]*'),
			payload_hash TEXT NOT NULL CHECK(length(payload_hash) = 64 AND lower(payload_hash) = payload_hash AND payload_hash NOT GLOB '*[^0-9a-f]*'),
			matched INTEGER NOT NULL CHECK(matched >= 0),
			confirmed INTEGER NOT NULL CHECK(confirmed >= 0),
			projection_pending INTEGER NOT NULL CHECK(projection_pending >= 0),
			projection_running INTEGER NOT NULL CHECK(projection_running >= 0),
			projection_retry_wait INTEGER NOT NULL CHECK(projection_retry_wait >= 0),
			projection_failed INTEGER NOT NULL CHECK(projection_failed >= 0),
			projection_completed INTEGER NOT NULL CHECK(projection_completed >= 0),
			idempotent_replay INTEGER NOT NULL CHECK(idempotent_replay = 0),
			audit_reference TEXT NOT NULL CHECK(length(audit_reference) > 0 AND length(audit_reference) <= 256),
			result_json TEXT NOT NULL CHECK(length(result_json) > 0 AND length(result_json) <= 16384),
			created_at TIMESTAMP NOT NULL,
			UNIQUE(owner_id, request_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_l1_chatgpt_confirm_receipt_owner_export ON l1_chatgpt_import_confirm_receipt(owner_id, export_id, created_at DESC, receipt_id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_l1_chatgpt_confirm_receipt_owner_request ON l1_chatgpt_import_confirm_receipt(owner_id, request_id)`,
		`CREATE TRIGGER IF NOT EXISTS trg_l1_chatgpt_confirm_receipt_immutable_update BEFORE UPDATE ON l1_chatgpt_import_confirm_receipt BEGIN SELECT RAISE(ABORT, 'l1 ChatGPT import confirmation receipt is immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS trg_l1_chatgpt_confirm_receipt_immutable_delete BEFORE DELETE ON l1_chatgpt_import_confirm_receipt BEGIN SELECT RAISE(ABORT, 'l1 ChatGPT import confirmation receipt is immutable'); END`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return rollback(fmt.Errorf("apply ChatGPT import confirmation schema: %w", err))
		}
	}
	if err := verifyChatGPTImportConfirmSchema(ctx, tx); err != nil {
		return rollback(err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit ChatGPT import confirmation schema: %w", err)
	}
	return nil
}

func verifyChatGPTImportConfirmSchema(ctx context.Context, tx *sql.Tx) error {
	objects := []struct {
		kind string
		name string
	}{
		{kind: "table", name: chatGPTImportConfirmReceiptTable},
		{kind: "index", name: "idx_l1_chatgpt_confirm_receipt_owner_export"},
		{kind: "index", name: "idx_l1_chatgpt_confirm_receipt_owner_request"},
		{kind: "trigger", name: "trg_l1_chatgpt_confirm_receipt_immutable_update"},
		{kind: "trigger", name: "trg_l1_chatgpt_confirm_receipt_immutable_delete"},
	}
	for _, object := range objects {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = ? AND name = ?`, object.kind, object.name).Scan(&count); err != nil {
			return fmt.Errorf("verify ChatGPT import confirmation schema %s %s: %w", object.kind, object.name, err)
		}
		if count != 1 {
			return fmt.Errorf("ChatGPT import confirmation schema %s %s is missing", object.kind, object.name)
		}
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(l1_chatgpt_import_confirm_receipt)`)
	if err != nil {
		return fmt.Errorf("read ChatGPT import confirmation receipt columns: %w", err)
	}
	defer rows.Close()
	columns := make(map[string]struct{})
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return fmt.Errorf("scan ChatGPT import confirmation receipt column: %w", err)
		}
		columns[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate ChatGPT import confirmation receipt columns: %w", err)
	}
	for _, name := range []string{
		"receipt_id", "request_id", "owner_id", "actor_id", "export_id", "apply", "reason_hash", "payload_hash",
		"matched", "confirmed", "projection_pending", "projection_running", "projection_retry_wait", "projection_failed",
		"projection_completed", "idempotent_replay", "audit_reference", "result_json", "created_at",
	} {
		if _, ok := columns[name]; !ok {
			return fmt.Errorf("ChatGPT import confirmation receipt column %s is missing", name)
		}
	}
	return nil
}
