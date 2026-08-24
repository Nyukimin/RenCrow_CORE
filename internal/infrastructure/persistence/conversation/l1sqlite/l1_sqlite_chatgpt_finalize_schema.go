package l1sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

const chatGPTImportFinalizeReceiptTable = "l1_chatgpt_import_finalize_receipt"

// applyChatGPTImportFinalizeSchema installs the append-only machine
// finalization receipt. It is separate from the retired candidate-confirm
// receipt so the two semantics cannot be confused or replayed across routes.
func (s *L1SQLiteStore) applyChatGPTImportFinalizeSchema(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin ChatGPT import finalization schema: %w", err)
	}
	rollback := func(cause error) error {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && rollbackErr != sql.ErrTxDone {
			return fmt.Errorf("%w; rollback ChatGPT import finalization schema: %v", cause, rollbackErr)
		}
		return cause
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS l1_chatgpt_import_finalize_receipt (
			receipt_id TEXT PRIMARY KEY,
			owner_id TEXT NOT NULL,
			actor_id TEXT NOT NULL,
			export_id TEXT NOT NULL,
			apply INTEGER NOT NULL CHECK(apply IN (0, 1)),
			payload_hash TEXT NOT NULL CHECK(length(payload_hash) = 64),
			result_json TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL,
			UNIQUE(owner_id, export_id, apply)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_l1_chatgpt_finalize_receipt_owner_export ON l1_chatgpt_import_finalize_receipt(owner_id, export_id, apply)`,
		`CREATE TRIGGER IF NOT EXISTS trg_l1_chatgpt_finalize_receipt_immutable_update BEFORE UPDATE ON l1_chatgpt_import_finalize_receipt BEGIN SELECT RAISE(ABORT, 'l1 ChatGPT import finalization receipt is immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS trg_l1_chatgpt_finalize_receipt_immutable_delete BEFORE DELETE ON l1_chatgpt_import_finalize_receipt BEGIN SELECT RAISE(ABORT, 'l1 ChatGPT import finalization receipt is immutable'); END`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return rollback(fmt.Errorf("apply ChatGPT import finalization schema: %w", err))
		}
	}
	objects := []struct {
		kind string
		name string
	}{
		{kind: "table", name: chatGPTImportFinalizeReceiptTable},
		{kind: "index", name: "idx_l1_chatgpt_finalize_receipt_owner_export"},
		{kind: "trigger", name: "trg_l1_chatgpt_finalize_receipt_immutable_update"},
		{kind: "trigger", name: "trg_l1_chatgpt_finalize_receipt_immutable_delete"},
	}
	for _, object := range objects {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = ? AND name = ?`, object.kind, object.name).Scan(&count); err != nil {
			return rollback(fmt.Errorf("verify ChatGPT import finalization schema: %w", err))
		}
		if count != 1 {
			return rollback(fmt.Errorf("ChatGPT import finalization schema is incomplete"))
		}
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(l1_chatgpt_import_finalize_receipt)`)
	if err != nil {
		return rollback(fmt.Errorf("inspect ChatGPT import finalization receipt: %w", err))
	}
	columns := make(map[string]struct{}, 8)
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return rollback(fmt.Errorf("inspect ChatGPT import finalization receipt: %w", err))
		}
		columns[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return rollback(fmt.Errorf("inspect ChatGPT import finalization receipt: %w", err))
	}
	if err := rows.Close(); err != nil {
		return rollback(fmt.Errorf("close ChatGPT import finalization receipt inspection: %w", err))
	}
	for _, required := range []string{"receipt_id", "owner_id", "actor_id", "export_id", "apply", "payload_hash", "result_json", "created_at"} {
		if _, ok := columns[required]; !ok {
			return rollback(fmt.Errorf("ChatGPT import finalization receipt is missing required column"))
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit ChatGPT import finalization schema: %w", err)
	}
	return nil
}
