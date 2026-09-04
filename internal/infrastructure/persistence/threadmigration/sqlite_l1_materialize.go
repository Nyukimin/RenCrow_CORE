package threadmigration

// This file owns the bounded, L1-only Step 05 materializer.  It deliberately
// accepts database handles rather than filesystem paths: the caller must have
// produced a disposable byte-for-byte clone before invoking this operation.
// The operation never opens, closes, copies, renames, or deletes a file and it
// never writes to the source handle.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	SQLiteL1MaterializationReceiptSchemaVersion = "rencrow.threadmigration.sqlite_l1_materialization.v1"
	SQLiteL1MaterializationStatus               = "materialized_l1_not_runtime_ready"
)

// L1SQLiteMaterializationInput contains caller-owned handles. Source is the
// exact legacy L1 snapshot used by InventorySQLite. Destination is a separate
// disposable clone. Neither handle is closed by MaterializeL1SQLite.
//
// Source and Destination are only distinguishable here by handle identity.
// A later CLI must additionally bind the source/destination file identity and
// clone receipt before calling this function; this package intentionally does
// not inspect filesystem paths.
type L1SQLiteMaterializationInput struct {
	Source      *sql.DB
	Destination *sql.DB
	Inventory   SQLiteInventoryResult
}

// SQLiteL1MaterializationTableCount is the committed row count for one
// rebuilt L1 table. Tables are sorted lexicographically in the receipt.
type SQLiteL1MaterializationTableCount struct {
	Table string `json:"table"`
	Rows  int64  `json:"rows"`
}

// SQLiteL1IdentityAudit is the bounded identity audit emitted after the
// destination transaction commits. LegacyNumericRows must remain zero.
type SQLiteL1IdentityAudit struct {
	CanonicalThreadRows       int64 `json:"canonical_thread_rows"`
	OptionalZeroRows          int64 `json:"optional_zero_rows"`
	CanonicalClosedThreadRows int64 `json:"canonical_closed_thread_rows"`
	CanonicalJSONRows         int64 `json:"canonical_json_rows"`
	LegacyNumericRows         int64 `json:"legacy_numeric_rows"`
}

// SQLiteL1MaterializationReceipt is a deterministic bounded receipt. It does
// not include paths, SQL, row identifiers, payloads, or raw database errors.
// The status intentionally says not_runtime_ready because owner indexes,
// triggers, projections, and post-open verification still belong to the later
// owner-schema reconciliation step.
type SQLiteL1MaterializationReceipt struct {
	SchemaVersion                     string                              `json:"schema_version"`
	Status                            string                              `json:"status"`
	OwnerSchemaReconciliationRequired bool                                `json:"owner_schema_reconciliation_required"`
	InventoryReceiptSHA256            string                              `json:"inventory_receipt_sha256"`
	MappingSHA256                     string                              `json:"mapping_sha256"`
	TableCounts                       []SQLiteL1MaterializationTableCount `json:"table_counts"`
	IdentityAudit                     SQLiteL1IdentityAudit               `json:"identity_audit"`
	ReceiptSHA256                     string                              `json:"receipt_sha256"`
}

// L1SQLiteMaterializationError is a bounded typed error. PostCommit is true
// when the destination transaction has committed and a subsequent validation
// failed; callers must then treat the disposable destination as unusable and
// must not pretend that a rollback occurred.
type L1SQLiteMaterializationError struct {
	Code       string
	Phase      string
	PostCommit bool
	cause      error
}

func (err *L1SQLiteMaterializationError) Error() string {
	if err == nil {
		return "<nil>"
	}
	if err.PostCommit {
		return fmt.Sprintf("L1 SQLite materialization %s failed after commit; destination is unusable", err.Code)
	}
	return fmt.Sprintf("L1 SQLite materialization %s failed during %s", err.Code, err.Phase)
}

func (err *L1SQLiteMaterializationError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.cause
}

var canonicalL1MaterializationTables = []string{
	activeThreadSurface,
	l1EventLogSurface,
	l1MemoryEventSurface,
	l1ProfilePromotionSurface,
	turnOutboxSurface,
	turnReceiptSurface,
}

var l1MaterializationCopyOrder = []string{
	l1MemoryEventSurface,
	l1EventLogSurface,
	l1ProfilePromotionSurface,
	activeThreadSurface,
	turnReceiptSurface,
	turnOutboxSurface,
}

var l1MaterializationDropOrder = []string{
	turnOutboxSurface,
	turnReceiptSurface,
	l1ProfilePromotionSurface,
	activeThreadSurface,
	l1EventLogSurface,
	l1MemoryEventSurface,
}

var canonicalL1MaterializationDescriptors = []legacyTableDescriptor{
	{Database: "l1", Name: l1MemoryEventSurface, Columns: []legacyColumnDescriptor{
		{Name: "id", Type: "TEXT", PrimaryKey: 1},
		{Name: "namespace", Type: "TEXT", NotNull: 1},
		{Name: "session_id", Type: "TEXT", NotNull: 1},
		{Name: "thread_id", Type: "TEXT", NotNull: 1, Default: stringPointer("''")},
		{Name: "thread_seq", Type: "INTEGER", NotNull: 1, Default: stringPointer("0")},
		{Name: "thread_kind", Type: "TEXT", NotNull: 1, Default: stringPointer("''")},
		{Name: "speaker", Type: "TEXT", NotNull: 1},
		{Name: "message", Type: "TEXT", NotNull: 1},
		{Name: "meta_json", Type: "TEXT", NotNull: 1, Default: stringPointer("'{}'")},
		{Name: "memory_state", Type: "TEXT", NotNull: 1},
		{Name: "layer", Type: "TEXT", NotNull: 1},
		{Name: "source", Type: "TEXT", NotNull: 1, Default: stringPointer("''")},
		{Name: "created_at", Type: "TIMESTAMP", NotNull: 1},
		{Name: "updated_at", Type: "TIMESTAMP", NotNull: 1},
	}},
	{Database: "l1", Name: l1EventLogSurface, Columns: []legacyColumnDescriptor{
		{Name: "id", Type: "TEXT", PrimaryKey: 1},
		{Name: "event_type", Type: "TEXT", NotNull: 1},
		{Name: "namespace", Type: "TEXT", NotNull: 1},
		{Name: "session_id", Type: "TEXT", NotNull: 1, Default: stringPointer("''")},
		{Name: "thread_id", Type: "TEXT", NotNull: 1, Default: stringPointer("''")},
		{Name: "thread_seq", Type: "INTEGER", NotNull: 1, Default: stringPointer("0")},
		{Name: "thread_kind", Type: "TEXT", NotNull: 1, Default: stringPointer("''")},
		{Name: "payload_json", Type: "TEXT", NotNull: 1, Default: stringPointer("'{}'")},
		{Name: "source", Type: "TEXT", NotNull: 1, Default: stringPointer("''")},
		{Name: "created_at", Type: "TIMESTAMP", NotNull: 1},
	}},
	{Database: "l1", Name: l1ProfilePromotionSurface, Columns: []legacyColumnDescriptor{
		{Name: "evidence_event_id", Type: "TEXT", PrimaryKey: 1},
		{Name: "session_id", Type: "TEXT", NotNull: 1},
		{Name: "thread_id", Type: "TEXT", NotNull: 1 /* no default in owner schema */},
		{Name: "thread_seq", Type: "INTEGER", NotNull: 1},
		{Name: "thread_kind", Type: "TEXT", NotNull: 1},
		{Name: "state", Type: "TEXT", NotNull: 1},
		{Name: "attempt_count", Type: "INTEGER", NotNull: 1, Default: stringPointer("0")},
		{Name: "lease_token", Type: "TEXT", NotNull: 1, Default: stringPointer("''")},
		{Name: "lease_expires_at", Type: "TIMESTAMP"},
		{Name: "next_attempt_at", Type: "TIMESTAMP"},
		{Name: "last_error", Type: "TEXT", NotNull: 1, Default: stringPointer("''")},
		{Name: "created_at", Type: "TIMESTAMP", NotNull: 1},
		{Name: "updated_at", Type: "TIMESTAMP", NotNull: 1},
	}},
	{Database: "l1", Name: activeThreadSurface, Columns: []legacyColumnDescriptor{
		{Name: "session_id", Type: "TEXT", PrimaryKey: 1},
		{Name: "thread_id", Type: "TEXT", NotNull: 1},
		{Name: "thread_seq", Type: "INTEGER", NotNull: 1},
		{Name: "thread_kind", Type: "TEXT", NotNull: 1},
		{Name: "domain", Type: "TEXT", NotNull: 1},
		{Name: "message_count", Type: "INTEGER", NotNull: 1},
		{Name: "updated_at", Type: "TIMESTAMP", NotNull: 1},
	}},
	{Database: "l1", Name: turnReceiptSurface, Columns: []legacyColumnDescriptor{
		{Name: "turn_id", Type: "TEXT", PrimaryKey: 1},
		{Name: "payload_sha256", Type: "TEXT", NotNull: 1},
		{Name: "session_id", Type: "TEXT", NotNull: 1},
		{Name: "trace_id", Type: "TEXT", NotNull: 1},
		{Name: "thread_id", Type: "TEXT", NotNull: 1},
		{Name: "thread_seq", Type: "INTEGER", NotNull: 1},
		{Name: "thread_kind", Type: "TEXT", NotNull: 1},
		{Name: "closed_thread_id", Type: "TEXT", NotNull: 1, Default: stringPointer("''")},
		{Name: "closed_thread_seq", Type: "INTEGER", NotNull: 1, Default: stringPointer("0")},
		{Name: "closed_thread_kind", Type: "TEXT", NotNull: 1, Default: stringPointer("''")},
		{Name: "user_message_id", Type: "TEXT", NotNull: 1},
		{Name: "agent_message_id", Type: "TEXT", NotNull: 1},
		{Name: "status", Type: "TEXT", NotNull: 1},
		{Name: "result_json", Type: "TEXT", NotNull: 1},
		{Name: "created_at", Type: "TIMESTAMP", NotNull: 1},
		{Name: "updated_at", Type: "TIMESTAMP", NotNull: 1},
	}},
	{Database: "l1", Name: turnOutboxSurface, Columns: []legacyColumnDescriptor{
		{Name: "turn_id", Type: "TEXT", NotNull: 1, PrimaryKey: 1},
		{Name: "target", Type: "TEXT", NotNull: 1, PrimaryKey: 2},
		{Name: "session_id", Type: "TEXT", NotNull: 1},
		{Name: "thread_id", Type: "TEXT", NotNull: 1},
		{Name: "thread_seq", Type: "INTEGER", NotNull: 1},
		{Name: "thread_kind", Type: "TEXT", NotNull: 1},
		{Name: "closed_thread_id", Type: "TEXT", NotNull: 1, Default: stringPointer("''")},
		{Name: "closed_thread_seq", Type: "INTEGER", NotNull: 1, Default: stringPointer("0")},
		{Name: "closed_thread_kind", Type: "TEXT", NotNull: 1, Default: stringPointer("''")},
		{Name: "payload_sha256", Type: "TEXT", NotNull: 1},
		{Name: "payload_json", Type: "TEXT", NotNull: 1},
		{Name: "status", Type: "TEXT", NotNull: 1},
		{Name: "lease_token", Type: "TEXT", NotNull: 1, Default: stringPointer("''")},
		{Name: "lease_expires_at", Type: "TIMESTAMP"},
		{Name: "attempts", Type: "INTEGER", NotNull: 1, Default: stringPointer("0")},
		{Name: "last_error", Type: "TEXT", NotNull: 1, Default: stringPointer("''")},
		{Name: "created_at", Type: "TIMESTAMP", NotNull: 1},
		{Name: "updated_at", Type: "TIMESTAMP", NotNull: 1},
	}},
}

// The SQL is intentionally kept here, rather than using the owner open path,
// because the owner path also creates projections and triggers. Step 05 must
// first swap only these six tables; later reconciliation opens the database
// through the owner and recreates its indexes/triggers.
var l1MaterializationCreateStatements = []string{
	`CREATE TABLE "l1_memory_event_s5_new" (
		id TEXT PRIMARY KEY,
		namespace TEXT NOT NULL,
		session_id TEXT NOT NULL,
		thread_id TEXT NOT NULL DEFAULT '',
		thread_seq INTEGER NOT NULL DEFAULT 0,
		thread_kind TEXT NOT NULL DEFAULT '',
		speaker TEXT NOT NULL,
		message TEXT NOT NULL,
		meta_json TEXT NOT NULL DEFAULT '{}',
		memory_state TEXT NOT NULL,
		layer TEXT NOT NULL,
		source TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL,
		CHECK ((thread_id = '' AND thread_seq = 0 AND thread_kind = '') OR (thread_id <> '' AND thread_seq > 0 AND thread_kind IN ('user_conversation', 'agent_discussion', 'idlechat', 'document', 'system')))
	)`,
	`CREATE TABLE "l1_event_log_s5_new" (
		id TEXT PRIMARY KEY,
		event_type TEXT NOT NULL,
		namespace TEXT NOT NULL,
		session_id TEXT NOT NULL DEFAULT '',
		thread_id TEXT NOT NULL DEFAULT '',
		thread_seq INTEGER NOT NULL DEFAULT 0,
		thread_kind TEXT NOT NULL DEFAULT '',
		payload_json TEXT NOT NULL DEFAULT '{}',
		source TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMP NOT NULL,
		CHECK ((thread_id = '' AND thread_seq = 0 AND thread_kind = '') OR (thread_id <> '' AND thread_seq > 0 AND thread_kind IN ('user_conversation', 'agent_discussion', 'idlechat', 'document', 'system')))
	)`,
	`CREATE TABLE "l1_profile_promotion_job_s5_new" (
		evidence_event_id TEXT PRIMARY KEY,
		session_id TEXT NOT NULL,
		thread_id TEXT NOT NULL CHECK(length(thread_id) > 0),
		thread_seq INTEGER NOT NULL CHECK(thread_seq > 0),
		thread_kind TEXT NOT NULL CHECK(thread_kind IN ('user_conversation', 'agent_discussion', 'idlechat', 'document', 'system')),
		state TEXT NOT NULL,
		attempt_count INTEGER NOT NULL DEFAULT 0,
		lease_token TEXT NOT NULL DEFAULT '',
		lease_expires_at TIMESTAMP,
		next_attempt_at TIMESTAMP,
		last_error TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	)`,
	`CREATE TABLE "conversation_active_thread_s5_new" (
		session_id TEXT PRIMARY KEY CHECK(length(session_id) > 0),
		thread_id TEXT NOT NULL CHECK(length(thread_id) > 0),
		thread_seq INTEGER NOT NULL CHECK(thread_seq > 0),
		thread_kind TEXT NOT NULL CHECK(thread_kind IN ('user_conversation', 'agent_discussion', 'idlechat', 'document', 'system')),
		domain TEXT NOT NULL CHECK(length(domain) > 0 AND length(domain) <= 1024),
		message_count INTEGER NOT NULL CHECK(message_count >= 0 AND message_count <= 12),
		updated_at TIMESTAMP NOT NULL,
		UNIQUE(thread_id),
		UNIQUE(session_id, thread_seq)
	)`,
	`CREATE TABLE "conversation_turn_receipt_s5_new" (
		turn_id TEXT PRIMARY KEY CHECK(length(turn_id) > 0),
		payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256) = 64 AND lower(payload_sha256) = payload_sha256 AND payload_sha256 NOT GLOB '*[^0-9a-f]*'),
		session_id TEXT NOT NULL CHECK(length(session_id) > 0),
		trace_id TEXT NOT NULL CHECK(length(trace_id) > 0),
		thread_id TEXT NOT NULL CHECK(length(thread_id) > 0),
		thread_seq INTEGER NOT NULL CHECK(thread_seq > 0),
		thread_kind TEXT NOT NULL CHECK(thread_kind IN ('user_conversation', 'agent_discussion', 'idlechat', 'document', 'system')),
		closed_thread_id TEXT NOT NULL DEFAULT '',
		closed_thread_seq INTEGER NOT NULL DEFAULT 0,
		closed_thread_kind TEXT NOT NULL DEFAULT '',
		user_message_id TEXT NOT NULL CHECK(length(user_message_id) > 0),
		agent_message_id TEXT NOT NULL CHECK(length(agent_message_id) > 0),
		status TEXT NOT NULL CHECK(status IN ('completed', 'partial', 'failed')),
		result_json TEXT NOT NULL CHECK(length(result_json) <= 65536),
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL,
		CHECK ((closed_thread_id = '' AND closed_thread_seq = 0 AND closed_thread_kind = '') OR (closed_thread_id <> '' AND closed_thread_seq > 0 AND closed_thread_kind IN ('user_conversation', 'agent_discussion', 'idlechat', 'document', 'system')))
	)`,
	`CREATE TABLE "conversation_turn_outbox_s5_new" (
		turn_id TEXT NOT NULL CHECK(length(turn_id) > 0),
		target TEXT NOT NULL CHECK(target IN ('redis_projection', 'thread_followers')),
		session_id TEXT NOT NULL CHECK(length(session_id) > 0),
		thread_id TEXT NOT NULL CHECK(length(thread_id) > 0),
		thread_seq INTEGER NOT NULL CHECK(thread_seq > 0),
		thread_kind TEXT NOT NULL CHECK(thread_kind IN ('user_conversation', 'agent_discussion', 'idlechat', 'document', 'system')),
		closed_thread_id TEXT NOT NULL DEFAULT '',
		closed_thread_seq INTEGER NOT NULL DEFAULT 0,
		closed_thread_kind TEXT NOT NULL DEFAULT '',
		payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256) = 64 AND lower(payload_sha256) = payload_sha256 AND payload_sha256 NOT GLOB '*[^0-9a-f]*'),
		payload_json TEXT NOT NULL CHECK(length(payload_json) <= 8192),
		status TEXT NOT NULL CHECK(status IN ('pending', 'running', 'completed', 'failed')),
		lease_token TEXT NOT NULL DEFAULT '',
		lease_expires_at TIMESTAMP,
		attempts INTEGER NOT NULL DEFAULT 0 CHECK(attempts >= 0),
		last_error TEXT NOT NULL DEFAULT '' CHECK(last_error IN ('', 'invalid', 'conflict', 'unavailable', 'internal')),
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL,
		PRIMARY KEY(turn_id, target),
		FOREIGN KEY(turn_id) REFERENCES conversation_turn_receipt_s5_new(turn_id),
		CHECK ((closed_thread_id = '' AND closed_thread_seq = 0 AND closed_thread_kind = '') OR (closed_thread_id <> '' AND closed_thread_seq > 0 AND closed_thread_kind IN ('user_conversation', 'agent_discussion', 'idlechat', 'document', 'system')))
	)`,
}

// MaterializeL1SQLite rebuilds only the six L1 identity-bearing tables inside
// a single destination transaction. The source is queried in deterministic
// primary-key order and never receives an Exec call. A successful result is
// intentionally not runtime-ready until the owner schema reconciliation pass.
func MaterializeL1SQLite(ctx context.Context, input L1SQLiteMaterializationInput) (SQLiteL1MaterializationReceipt, error) {
	if ctx == nil {
		return SQLiteL1MaterializationReceipt{}, newL1MaterializationError("invalid_input", "preflight", false, errors.New("nil context"))
	}
	if err := ctx.Err(); err != nil {
		return SQLiteL1MaterializationReceipt{}, newL1MaterializationError("canceled", "preflight", false, err)
	}
	if input.Source == nil || input.Destination == nil || input.Source == input.Destination {
		return SQLiteL1MaterializationReceipt{}, newL1MaterializationError("invalid_input", "preflight", false, errors.New("source and destination handles must be distinct"))
	}
	if err := input.Inventory.Validate(); err != nil {
		return SQLiteL1MaterializationReceipt{}, newL1MaterializationError("invalid_inventory", "preflight", false, err)
	}
	index, expectedCounts, err := preflightL1Materialization(ctx, input)
	if err != nil {
		return SQLiteL1MaterializationReceipt{}, newL1MaterializationError("preflight", "preflight", false, err)
	}

	destinationConn, err := input.Destination.Conn(ctx)
	if err != nil {
		return SQLiteL1MaterializationReceipt{}, newL1MaterializationError("destination_connection", "preflight", false, err)
	}
	defer destinationConn.Close()
	if err := enableDestinationForeignKeys(ctx, destinationConn); err != nil {
		return SQLiteL1MaterializationReceipt{}, newL1MaterializationError("foreign_keys", "preflight", false, err)
	}
	if err := rejectStageCollisions(ctx, destinationConn); err != nil {
		return SQLiteL1MaterializationReceipt{}, newL1MaterializationError("stage_collision", "preflight", false, err)
	}

	tx, err := destinationConn.BeginTx(ctx, nil)
	if err != nil {
		return SQLiteL1MaterializationReceipt{}, newL1MaterializationError("begin_transaction", "prepare", false, err)
	}
	rollback := func(code, phase string, cause error) (SQLiteL1MaterializationReceipt, error) {
		_ = tx.Rollback()
		return SQLiteL1MaterializationReceipt{}, newL1MaterializationError(code, phase, false, cause)
	}
	for _, statement := range l1MaterializationCreateStatements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return rollback("create_stage", "prepare", err)
		}
	}

	for _, surface := range l1MaterializationCopyOrder {
		var copied int64
		switch surface {
		case l1MemoryEventSurface:
			copied, err = copyL1MemoryEvents(ctx, input.Source, tx, index)
		case l1EventLogSurface:
			copied, err = copyL1EventLog(ctx, input.Source, tx, index)
		case l1ProfilePromotionSurface:
			copied, err = copyL1ProfilePromotionJobs(ctx, input.Source, tx, index)
		case activeThreadSurface:
			copied, err = copyL1ActiveThreads(ctx, input.Source, tx, index)
		case turnReceiptSurface:
			copied, err = copyL1TurnReceipts(ctx, input.Source, tx, index)
		case turnOutboxSurface:
			copied, err = copyL1TurnOutbox(ctx, input.Source, tx, index)
		default:
			err = errors.New("unknown L1 materialization surface")
		}
		if err != nil {
			return rollback("copy_rows", "copy", err)
		}
		if copied != expectedCounts[surface] {
			return rollback("copy_count", "copy", fmt.Errorf("staged row count mismatch"))
		}
	}
	if err := verifyStagedCounts(ctx, tx, expectedCounts); err != nil {
		return rollback("staged_count", "copy", err)
	}
	if err := ctx.Err(); err != nil {
		return rollback("canceled", "copy", err)
	}
	dependentTriggers, err := snapshotDependentL1Triggers(ctx, tx)
	if err != nil {
		return rollback("snapshot_dependent_triggers", "swap", err)
	}
	for _, trigger := range dependentTriggers {
		if _, err := tx.ExecContext(ctx, `DROP TRIGGER `+quoteSQLiteIdentifier(trigger.Name)); err != nil {
			return rollback("drop_dependent_trigger", "swap", err)
		}
	}
	for _, surface := range l1MaterializationDropOrder {
		if _, err := tx.ExecContext(ctx, `DROP TABLE `+quoteSQLiteIdentifier(surface)); err != nil {
			return rollback("drop_legacy", "swap", err)
		}
	}
	for _, surface := range l1MaterializationCopyOrder {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE `+quoteSQLiteIdentifier(stageTableName(surface))+` RENAME TO `+quoteSQLiteIdentifier(surface)); err != nil {
			return rollback("rename_stage", "swap", err)
		}
	}
	for _, trigger := range dependentTriggers {
		if _, err := tx.ExecContext(ctx, trigger.SQL); err != nil {
			return rollback("recreate_dependent_trigger", "swap", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return rollback("canceled", "swap", err)
	}
	if err := tx.Commit(); err != nil {
		return SQLiteL1MaterializationReceipt{}, newL1MaterializationError("commit", "commit", true, err)
	}

	counts, audit, err := validateCommittedL1Materialization(ctx, destinationConn, expectedCounts)
	if err != nil {
		return SQLiteL1MaterializationReceipt{}, newL1MaterializationError("post_commit_validation", "post_commit", true, err)
	}
	receipt := SQLiteL1MaterializationReceipt{
		SchemaVersion:                     SQLiteL1MaterializationReceiptSchemaVersion,
		Status:                            SQLiteL1MaterializationStatus,
		OwnerSchemaReconciliationRequired: true,
		InventoryReceiptSHA256:            input.Inventory.Receipt.ReceiptSHA256,
		MappingSHA256:                     input.Inventory.Plan.MappingSHA256,
		TableCounts:                       counts,
		IdentityAudit:                     audit,
	}
	receiptHash, err := receipt.ComputeSHA256()
	if err != nil {
		return SQLiteL1MaterializationReceipt{}, newL1MaterializationError("receipt_hash", "post_commit", true, err)
	}
	receipt.ReceiptSHA256 = receiptHash
	if err := receipt.Validate(); err != nil {
		return SQLiteL1MaterializationReceipt{}, newL1MaterializationError("receipt_validation", "post_commit", true, err)
	}
	return receipt, nil
}

func newL1MaterializationError(code, phase string, postCommit bool, cause error) error {
	return &L1SQLiteMaterializationError{Code: code, Phase: phase, PostCommit: postCommit, cause: cause}
}

func preflightL1Materialization(ctx context.Context, input L1SQLiteMaterializationInput) (sqliteTransformIndex, map[string]int64, error) {
	index, err := newSQLiteTransformIndex(input.Inventory.Plan)
	if err != nil {
		return sqliteTransformIndex{}, nil, err
	}
	for _, descriptor := range legacyL1Tables {
		observed, err := inspectLegacySchema(ctx, input.Source, descriptor)
		if err != nil {
			return sqliteTransformIndex{}, nil, err
		}
		expected, ok := inventorySchemaFingerprint(input.Inventory.Receipt, descriptor.Database, descriptor.Name)
		if !ok || observed.SHA256 != expected.SHA256 {
			return sqliteTransformIndex{}, nil, fmt.Errorf("source schema fingerprint does not match inventory")
		}
		if _, err := inspectLegacySchema(ctx, input.Destination, descriptor); err != nil {
			return sqliteTransformIndex{}, nil, fmt.Errorf("destination legacy schema mismatch: %w", err)
		}
	}
	if err := rejectStageCollisions(ctx, input.Destination); err != nil {
		return sqliteTransformIndex{}, nil, err
	}
	expectedCounts := make(map[string]int64, len(legacyL1Tables))
	for _, descriptor := range legacyL1Tables {
		count, err := countSQLiteTable(ctx, input.Source, descriptor.Name)
		if err != nil {
			return sqliteTransformIndex{}, nil, fmt.Errorf("read source row count: %w", err)
		}
		receiptCount, ok := input.Inventory.Receipt.SurfaceCount(descriptor.Name)
		if !ok || count != receiptCount.Rows {
			return sqliteTransformIndex{}, nil, fmt.Errorf("source row count does not match inventory")
		}
		destinationCount, err := countSQLiteTable(ctx, input.Destination, descriptor.Name)
		if err != nil {
			return sqliteTransformIndex{}, nil, fmt.Errorf("read destination row count: %w", err)
		}
		if destinationCount != receiptCount.Rows {
			return sqliteTransformIndex{}, nil, fmt.Errorf("destination row count does not match inventory")
		}
		expectedCounts[descriptor.Name] = count
	}
	return index, expectedCounts, nil
}

func inventorySchemaFingerprint(receipt SQLiteInventoryReceipt, database, table string) (SQLiteInventorySchemaFingerprint, bool) {
	for _, fingerprint := range receipt.SourceSchemaFingerprints {
		if fingerprint.Database == database && fingerprint.Table == table {
			return fingerprint, true
		}
	}
	return SQLiteInventorySchemaFingerprint{}, false
}

func countSQLiteTable(ctx context.Context, db *sql.DB, table string) (int64, error) {
	var count int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+quoteSQLiteIdentifier(table)).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func stageTableName(surface string) string { return surface + "_s5_new" }

type l1DependentTrigger struct {
	Name  string
	Table string
	SQL   string
}

func snapshotDependentL1Triggers(ctx context.Context, tx *sql.Tx) ([]l1DependentTrigger, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name, tbl_name, sql FROM sqlite_master WHERE type = 'trigger' AND sql IS NOT NULL ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	triggers := make([]l1DependentTrigger, 0)
	for rows.Next() {
		var trigger l1DependentTrigger
		if err := rows.Scan(&trigger.Name, &trigger.Table, &trigger.SQL); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if isCanonicalL1MaterializationTable(trigger.Table) || !triggerReferencesCanonicalL1Table(trigger.SQL) {
			continue
		}
		triggers = append(triggers, trigger)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return triggers, nil
}

func isCanonicalL1MaterializationTable(table string) bool {
	for _, surface := range canonicalL1MaterializationTables {
		if strings.EqualFold(table, surface) {
			return true
		}
	}
	return false
}

func triggerReferencesCanonicalL1Table(triggerSQL string) bool {
	lowerSQL := strings.ToLower(triggerSQL)
	for _, surface := range canonicalL1MaterializationTables {
		if strings.Contains(lowerSQL, strings.ToLower(surface)) {
			return true
		}
	}
	return false
}

func rejectStageCollisions(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}) error {
	for _, surface := range canonicalL1MaterializationTables {
		var count int64
		if err := queryer.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, stageTableName(surface)).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return errors.New("destination contains a reserved materialization stage object")
		}
	}
	return nil
}

func enableDestinationForeignKeys(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		return err
	}
	var enabled int64
	if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&enabled); err != nil {
		return err
	}
	if enabled != 1 {
		return errors.New("destination foreign keys could not be enabled")
	}
	return nil
}

func verifyStagedCounts(ctx context.Context, tx *sql.Tx, expected map[string]int64) error {
	for _, surface := range canonicalL1MaterializationTables {
		var count int64
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+quoteSQLiteIdentifier(stageTableName(surface))).Scan(&count); err != nil {
			return err
		}
		if count != expected[surface] {
			return errors.New("staged table count mismatch")
		}
	}
	return nil
}

func copyL1MemoryEvents(ctx context.Context, source *sql.DB, tx *sql.Tx, index sqliteTransformIndex) (int64, error) {
	rows, err := source.QueryContext(ctx, `SELECT id, namespace, session_id, thread_id, speaker, message, meta_json, memory_state, layer, source, created_at, updated_at FROM l1_memory_event ORDER BY id ASC`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO "l1_memory_event_s5_new" (id, namespace, session_id, thread_id, thread_seq, thread_kind, speaker, message, meta_json, memory_state, layer, source, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	var count int64
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return count, err
		}
		var id, namespace, sessionID, speaker, message, metaJSON, memoryState, layer, sourceValue string
		var createdAt, updatedAt interface{}
		var threadID int64
		if err := rows.Scan(&id, &namespace, &sessionID, &threadID, &speaker, &message, &metaJSON, &memoryState, &layer, &sourceValue, &createdAt, &updatedAt); err != nil {
			return count, err
		}
		tuple, err := resolveSQLiteOptionalThreadTuple(index, sessionID, threadID)
		if err != nil {
			return count, err
		}
		namespace, err = rewriteL1LegacyNamespace(index, sessionID, threadID, namespace)
		if err != nil {
			return count, err
		}
		if _, err := stmt.ExecContext(ctx, id, namespace, string(tuple.SessionID), string(tuple.ThreadID), int64(tuple.ThreadSeq), string(tuple.ThreadKind), speaker, message, metaJSON, memoryState, layer, sourceValue, createdAt, updatedAt); err != nil {
			return count, err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return count, err
	}
	return count, rows.Close()
}

func copyL1EventLog(ctx context.Context, source *sql.DB, tx *sql.Tx, index sqliteTransformIndex) (int64, error) {
	rows, err := source.QueryContext(ctx, `SELECT id, event_type, namespace, session_id, thread_id, payload_json, source, created_at FROM l1_event_log ORDER BY id ASC`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO "l1_event_log_s5_new" (id, event_type, namespace, session_id, thread_id, thread_seq, thread_kind, payload_json, source, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	var count int64
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return count, err
		}
		var id, eventType, namespace, sessionID, payloadJSON, sourceValue string
		var createdAt interface{}
		var threadID int64
		if err := rows.Scan(&id, &eventType, &namespace, &sessionID, &threadID, &payloadJSON, &sourceValue, &createdAt); err != nil {
			return count, err
		}
		tuple, err := resolveSQLiteOptionalThreadTuple(index, sessionID, threadID)
		if err != nil {
			return count, err
		}
		namespace, err = rewriteL1LegacyNamespace(index, sessionID, threadID, namespace)
		if err != nil {
			return count, err
		}
		if _, err := stmt.ExecContext(ctx, id, eventType, namespace, string(tuple.SessionID), string(tuple.ThreadID), int64(tuple.ThreadSeq), string(tuple.ThreadKind), payloadJSON, sourceValue, createdAt); err != nil {
			return count, err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return count, err
	}
	return count, rows.Close()
}

func copyL1ProfilePromotionJobs(ctx context.Context, source *sql.DB, tx *sql.Tx, index sqliteTransformIndex) (int64, error) {
	rows, err := source.QueryContext(ctx, `SELECT evidence_event_id, session_id, thread_id, state, attempt_count, lease_token, lease_expires_at, next_attempt_at, last_error, created_at, updated_at FROM l1_profile_promotion_job ORDER BY evidence_event_id ASC`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO "l1_profile_promotion_job_s5_new" (evidence_event_id, session_id, thread_id, thread_seq, thread_kind, state, attempt_count, lease_token, lease_expires_at, next_attempt_at, last_error, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	var count int64
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return count, err
		}
		var evidenceID, sessionID, state, leaseToken, lastError string
		var threadID, attempts int64
		var leaseExpires, nextAttempt, createdAt, updatedAt interface{}
		if err := rows.Scan(&evidenceID, &sessionID, &threadID, &state, &attempts, &leaseToken, &leaseExpires, &nextAttempt, &lastError, &createdAt, &updatedAt); err != nil {
			return count, err
		}
		tuple, err := resolveSQLiteThreadTuple(index, sessionID, threadID)
		if err != nil {
			return count, err
		}
		if _, err := stmt.ExecContext(ctx, evidenceID, string(tuple.SessionID), string(tuple.ThreadID), int64(tuple.ThreadSeq), string(tuple.ThreadKind), state, attempts, leaseToken, leaseExpires, nextAttempt, lastError, createdAt, updatedAt); err != nil {
			return count, err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return count, err
	}
	return count, rows.Close()
}

func copyL1ActiveThreads(ctx context.Context, source *sql.DB, tx *sql.Tx, index sqliteTransformIndex) (int64, error) {
	rows, err := source.QueryContext(ctx, `SELECT session_id, thread_id, domain, message_count, updated_at FROM conversation_active_thread ORDER BY session_id ASC`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO "conversation_active_thread_s5_new" (session_id, thread_id, thread_seq, thread_kind, domain, message_count, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	var count int64
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return count, err
		}
		var sessionID string
		var threadID, messageCount int64
		var domain string
		var updatedAt interface{}
		if err := rows.Scan(&sessionID, &threadID, &domain, &messageCount, &updatedAt); err != nil {
			return count, err
		}
		tuple, err := resolveSQLiteThreadTuple(index, sessionID, threadID)
		if err != nil {
			return count, err
		}
		if _, err := stmt.ExecContext(ctx, string(tuple.SessionID), string(tuple.ThreadID), int64(tuple.ThreadSeq), string(tuple.ThreadKind), domain, messageCount, updatedAt); err != nil {
			return count, err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return count, err
	}
	return count, rows.Close()
}

func copyL1TurnReceipts(ctx context.Context, source *sql.DB, tx *sql.Tx, index sqliteTransformIndex) (int64, error) {
	rows, err := source.QueryContext(ctx, `SELECT turn_id, payload_sha256, session_id, trace_id, thread_id, closed_thread_id, user_message_id, agent_message_id, status, result_json, created_at, updated_at FROM conversation_turn_receipt ORDER BY turn_id ASC`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO "conversation_turn_receipt_s5_new" (turn_id, payload_sha256, session_id, trace_id, thread_id, thread_seq, thread_kind, closed_thread_id, closed_thread_seq, closed_thread_kind, user_message_id, agent_message_id, status, result_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	var count int64
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return count, err
		}
		var row legacyReceiptRow
		var closedID sql.NullInt64
		var resultJSON string
		var createdAt, updatedAt interface{}
		if err := rows.Scan(&row.turnID, &row.payloadHash, &row.sessionID, &row.traceID, &row.threadID, &closedID, &row.userMessage, &row.agentMessage, &row.status, &resultJSON, &createdAt, &updatedAt); err != nil {
			return count, err
		}
		row.closed = closedID.Valid
		row.closedID = closedID.Int64
		canonicalJSON, err := transformLegacyTurnResult(index, row, resultJSON)
		if err != nil {
			return count, err
		}
		tuple, err := resolveSQLiteThreadTuple(index, row.sessionID, row.threadID)
		if err != nil {
			return count, err
		}
		closedTuple := sqliteCanonicalThreadTuple{}
		if row.closed {
			closedTuple, err = resolveSQLiteThreadTuple(index, row.sessionID, row.closedID)
			if err != nil {
				return count, err
			}
		}
		if _, err := stmt.ExecContext(ctx, row.turnID, row.payloadHash, string(tuple.SessionID), row.traceID, string(tuple.ThreadID), int64(tuple.ThreadSeq), string(tuple.ThreadKind), string(closedTuple.ThreadID), int64(closedTuple.ThreadSeq), string(closedTuple.ThreadKind), row.userMessage, row.agentMessage, row.status, string(canonicalJSON), createdAt, updatedAt); err != nil {
			return count, err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return count, err
	}
	return count, rows.Close()
}

func copyL1TurnOutbox(ctx context.Context, source *sql.DB, tx *sql.Tx, index sqliteTransformIndex) (int64, error) {
	rows, err := source.QueryContext(ctx, `SELECT o.turn_id, o.target, o.session_id, o.thread_id, o.closed_thread_id, o.payload_sha256, o.payload_json, o.status, o.lease_token, o.lease_expires_at, o.attempts, o.last_error, o.created_at, o.updated_at, r.payload_sha256, r.session_id, r.trace_id, r.thread_id, r.closed_thread_id, r.user_message_id, r.agent_message_id, r.status FROM conversation_turn_outbox AS o JOIN conversation_turn_receipt AS r ON r.turn_id = o.turn_id ORDER BY o.turn_id ASC, o.target ASC`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO "conversation_turn_outbox_s5_new" (turn_id, target, session_id, thread_id, thread_seq, thread_kind, closed_thread_id, closed_thread_seq, closed_thread_kind, payload_sha256, payload_json, status, lease_token, lease_expires_at, attempts, last_error, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	var count int64
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return count, err
		}
		var row sqliteLegacyOutboxRow
		var closedID, receiptClosedID sql.NullInt64
		var leaseExpires interface{}
		var attempts int64
		var createdAt, updatedAt interface{}
		var outboxStatus, leaseToken, lastError string
		var receiptPayloadHash, receiptSessionID, receiptTraceID, receiptStatus, receiptUserMessage, receiptAgentMessage string
		var receiptThreadID int64
		if err := rows.Scan(&row.TurnID, &row.Target, &row.SessionID, &row.ThreadID, &closedID, &row.PayloadHash, &row.PayloadJSON, &outboxStatus, &leaseToken, &leaseExpires, &attempts, &lastError, &createdAt, &updatedAt, &receiptPayloadHash, &receiptSessionID, &receiptTraceID, &receiptThreadID, &receiptClosedID, &receiptUserMessage, &receiptAgentMessage, &receiptStatus); err != nil {
			return count, err
		}
		row.Receipt = legacyReceiptRow{
			turnID: row.TurnID, payloadHash: receiptPayloadHash, sessionID: receiptSessionID,
			traceID: receiptTraceID, threadID: receiptThreadID, userMessage: receiptUserMessage,
			agentMessage: receiptAgentMessage, status: receiptStatus,
		}
		row.ClosedID = legacyOptionalInt64{Value: closedID.Int64, Valid: closedID.Valid}
		row.Receipt.closed = receiptClosedID.Valid
		row.Receipt.closedID = receiptClosedID.Int64
		canonicalJSON, err := transformLegacyOutboxPayload(index, row)
		if err != nil {
			return count, err
		}
		tuple, err := resolveSQLiteThreadTuple(index, row.SessionID, row.ThreadID)
		if err != nil {
			return count, err
		}
		closedTuple := sqliteCanonicalThreadTuple{}
		if row.ClosedID.Valid {
			closedTuple, err = resolveSQLiteThreadTuple(index, row.SessionID, row.ClosedID.Value)
			if err != nil {
				return count, err
			}
		}
		if _, err := stmt.ExecContext(ctx, row.TurnID, row.Target, string(tuple.SessionID), string(tuple.ThreadID), int64(tuple.ThreadSeq), string(tuple.ThreadKind), string(closedTuple.ThreadID), int64(closedTuple.ThreadSeq), string(closedTuple.ThreadKind), row.PayloadHash, string(canonicalJSON), outboxStatus, leaseToken, leaseExpires, attempts, lastError, createdAt, updatedAt); err != nil {
			return count, err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return count, err
	}
	return count, rows.Close()
}

// rewriteL1LegacyNamespace changes only exact legacy identity namespaces. A
// custom conv:* namespace, user:* namespace, session:* namespace, and all
// operational namespaces remain byte-exact.
func rewriteL1LegacyNamespace(index sqliteTransformIndex, sourceSessionID string, legacyThreadID int64, namespace string) (string, error) {
	if legacyThreadID <= 0 {
		return namespace, nil
	}
	if mapping, ok := index.chatGPT[legacyTuple{sessionID: sourceSessionID, threadID: legacyThreadID}]; ok {
		if namespace == "conv:"+mapping.ChatGPTConversationID {
			return "conv:" + string(mapping.ThreadID), nil
		}
		return namespace, nil
	}
	canonicalSessionID, err := canonicalGenericSessionID(sourceSessionID)
	if err != nil {
		return "", err
	}
	mapping, ok := index.generic[genericGroupKey{sessionID: canonicalSessionID, legacyThreadID: legacyThreadID}]
	if !ok {
		return "", errors.New("legacy namespace row has no thread mapping")
	}
	if namespace == "conv:"+strconv.FormatInt(legacyThreadID, 10) {
		return "conv:" + string(mapping.ThreadID), nil
	}
	return namespace, nil
}

func validateCommittedL1Materialization(ctx context.Context, conn *sql.Conn, expected map[string]int64) ([]SQLiteL1MaterializationTableCount, SQLiteL1IdentityAudit, error) {
	var enabled int64
	if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&enabled); err != nil {
		return nil, SQLiteL1IdentityAudit{}, err
	}
	if enabled != 1 {
		return nil, SQLiteL1IdentityAudit{}, errors.New("destination foreign keys are disabled after commit")
	}
	foreignKeys, err := conn.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return nil, SQLiteL1IdentityAudit{}, err
	}
	for foreignKeys.Next() {
		_ = foreignKeys.Close()
		return nil, SQLiteL1IdentityAudit{}, errors.New("destination foreign key check failed")
	}
	if err := foreignKeys.Err(); err != nil {
		_ = foreignKeys.Close()
		return nil, SQLiteL1IdentityAudit{}, err
	}
	if err := foreignKeys.Close(); err != nil {
		return nil, SQLiteL1IdentityAudit{}, err
	}

	for _, descriptor := range canonicalL1MaterializationDescriptors {
		if _, err := inspectLegacySchemaOnConn(ctx, conn, descriptor); err != nil {
			return nil, SQLiteL1IdentityAudit{}, err
		}
	}
	counts := make([]SQLiteL1MaterializationTableCount, 0, len(canonicalL1MaterializationTables))
	for _, surface := range canonicalL1MaterializationTables {
		var count int64
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+quoteSQLiteIdentifier(surface)).Scan(&count); err != nil {
			return nil, SQLiteL1IdentityAudit{}, err
		}
		if count != expected[surface] {
			return nil, SQLiteL1IdentityAudit{}, errors.New("committed row count mismatch")
		}
		counts = append(counts, SQLiteL1MaterializationTableCount{Table: surface, Rows: count})
	}
	sort.Slice(counts, func(left, right int) bool { return counts[left].Table < counts[right].Table })
	audit := SQLiteL1IdentityAudit{}
	if err := auditCanonicalL1Memory(ctx, conn, l1MemoryEventSurface, &audit); err != nil {
		return nil, SQLiteL1IdentityAudit{}, err
	}
	if err := auditCanonicalL1Memory(ctx, conn, l1EventLogSurface, &audit); err != nil {
		return nil, SQLiteL1IdentityAudit{}, err
	}
	if err := auditCanonicalL1Profile(ctx, conn, &audit); err != nil {
		return nil, SQLiteL1IdentityAudit{}, err
	}
	if err := auditCanonicalL1Active(ctx, conn, &audit); err != nil {
		return nil, SQLiteL1IdentityAudit{}, err
	}
	if err := auditCanonicalL1Receipts(ctx, conn, &audit); err != nil {
		return nil, SQLiteL1IdentityAudit{}, err
	}
	if err := auditCanonicalL1Outbox(ctx, conn, &audit); err != nil {
		return nil, SQLiteL1IdentityAudit{}, err
	}
	if audit.LegacyNumericRows != 0 {
		return nil, SQLiteL1IdentityAudit{}, errors.New("committed destination retains numeric identity")
	}
	return counts, audit, nil
}

// inspectLegacySchemaOnConn is the connection-bound equivalent of the
// inventory schema inspector. It reuses the exact descriptor semantics while
// keeping post-commit validation on the transaction's single destination
// connection.
func inspectLegacySchemaOnConn(ctx context.Context, conn *sql.Conn, descriptor legacyTableDescriptor) (SQLiteInventorySchemaFingerprint, error) {
	var objectType string
	if err := conn.QueryRowContext(ctx, `SELECT type FROM sqlite_master WHERE name = ?`, descriptor.Name).Scan(&objectType); err != nil {
		return SQLiteInventorySchemaFingerprint{}, err
	}
	if objectType != "table" {
		return SQLiteInventorySchemaFingerprint{}, errors.New("committed L1 schema object is not a table")
	}
	rows, err := conn.QueryContext(ctx, `PRAGMA table_info(`+quoteSQLiteIdentifier(descriptor.Name)+`)`)
	if err != nil {
		return SQLiteInventorySchemaFingerprint{}, err
	}
	defer rows.Close()
	columns := make([]legacyTableColumn, 0, len(descriptor.Columns))
	for rows.Next() {
		var cid, notNull, primaryKey int64
		var name, declaredType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &declaredType, &notNull, &defaultValue, &primaryKey); err != nil {
			return SQLiteInventorySchemaFingerprint{}, err
		}
		if cid != int64(len(columns)) {
			return SQLiteInventorySchemaFingerprint{}, errors.New("committed L1 schema column order mismatch")
		}
		columns = append(columns, legacyTableColumn{Name: name, Type: declaredType, NotNull: notNull, Default: defaultValue, PrimaryKey: primaryKey})
	}
	if err := rows.Err(); err != nil {
		return SQLiteInventorySchemaFingerprint{}, err
	}
	if err := rows.Close(); err != nil {
		return SQLiteInventorySchemaFingerprint{}, err
	}
	if len(columns) != len(descriptor.Columns) {
		return SQLiteInventorySchemaFingerprint{}, errors.New("committed L1 schema column count mismatch")
	}
	for index, expected := range descriptor.Columns {
		actual := columns[index]
		if actual.Name != expected.Name || normalizeDeclaredType(actual.Type) != normalizeDeclaredType(expected.Type) || actual.NotNull != expected.NotNull || actual.PrimaryKey != expected.PrimaryKey || !sameDefaultValue(actual.Default, expected.Default) {
			return SQLiteInventorySchemaFingerprint{}, errors.New("committed L1 schema column mismatch")
		}
	}
	if descriptor.Name == turnOutboxSurface {
		rows, err := conn.QueryContext(ctx, `PRAGMA foreign_key_list("conversation_turn_outbox")`)
		if err != nil {
			return SQLiteInventorySchemaFingerprint{}, err
		}
		defer rows.Close()
		var id, sequence int64
		var table, from, to, onUpdate, onDelete, match string
		if !rows.Next() || rows.Scan(&id, &sequence, &table, &from, &to, &onUpdate, &onDelete, &match) != nil || table != turnReceiptSurface || from != "turn_id" || to != "turn_id" || onUpdate != "NO ACTION" || onDelete != "NO ACTION" || rows.Next() {
			return SQLiteInventorySchemaFingerprint{}, errors.New("committed L1 outbox foreign key mismatch")
		}
		if err := rows.Err(); err != nil {
			return SQLiteInventorySchemaFingerprint{}, err
		}
	}
	return SQLiteInventorySchemaFingerprint{Database: descriptor.Database, Table: descriptor.Name}, nil
}

func auditCanonicalL1Memory(ctx context.Context, conn *sql.Conn, table string, audit *SQLiteL1IdentityAudit) error {
	query := `SELECT session_id, typeof(session_id), thread_id, typeof(thread_id), thread_seq, typeof(thread_seq), thread_kind, typeof(thread_kind) FROM ` + quoteSQLiteIdentifier(table) + ` ORDER BY id ASC`
	rows, err := conn.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var sessionID, sessionType, threadID, threadType, threadKind, kindType, seqType string
		var threadSeq int64
		if err := rows.Scan(&sessionID, &sessionType, &threadID, &threadType, &threadSeq, &seqType, &threadKind, &kindType); err != nil {
			return err
		}
		optional, err := auditCanonicalTuple(sessionID, sessionType, threadID, threadType, threadSeq, seqType, threadKind, kindType, true, audit)
		if err != nil {
			return err
		}
		if optional {
			audit.OptionalZeroRows++
		}
	}
	return rows.Err()
}

func auditCanonicalL1Profile(ctx context.Context, conn *sql.Conn, audit *SQLiteL1IdentityAudit) error {
	rows, err := conn.QueryContext(ctx, `SELECT session_id, typeof(session_id), thread_id, typeof(thread_id), thread_seq, typeof(thread_seq), thread_kind, typeof(thread_kind) FROM l1_profile_promotion_job ORDER BY evidence_event_id ASC`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var sessionID, sessionType, threadID, threadType, threadKind, kindType, seqType string
		var threadSeq int64
		if err := rows.Scan(&sessionID, &sessionType, &threadID, &threadType, &threadSeq, &seqType, &threadKind, &kindType); err != nil {
			return err
		}
		if _, err := auditCanonicalTuple(sessionID, sessionType, threadID, threadType, threadSeq, seqType, threadKind, kindType, false, audit); err != nil {
			return err
		}
	}
	return rows.Err()
}

func auditCanonicalL1Active(ctx context.Context, conn *sql.Conn, audit *SQLiteL1IdentityAudit) error {
	rows, err := conn.QueryContext(ctx, `SELECT session_id, typeof(session_id), thread_id, typeof(thread_id), thread_seq, typeof(thread_seq), thread_kind, typeof(thread_kind) FROM conversation_active_thread ORDER BY session_id ASC`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var sessionID, sessionType, threadID, threadType, threadKind, kindType, seqType string
		var threadSeq int64
		if err := rows.Scan(&sessionID, &sessionType, &threadID, &threadType, &threadSeq, &seqType, &threadKind, &kindType); err != nil {
			return err
		}
		if _, err := auditCanonicalTuple(sessionID, sessionType, threadID, threadType, threadSeq, seqType, threadKind, kindType, false, audit); err != nil {
			return err
		}
	}
	return rows.Err()
}

func auditCanonicalTuple(sessionID, sessionType, threadID, threadType string, threadSeq int64, seqType, threadKind, kindType string, allowZero bool, audit *SQLiteL1IdentityAudit) (bool, error) {
	if sessionType != "text" || threadType != "text" || seqType != "integer" || kindType != "text" {
		return false, errors.New("canonical identity storage class mismatch")
	}
	if threadID == "" || threadSeq == 0 || threadKind == "" {
		if !allowZero || threadID != "" || threadSeq != 0 || threadKind != "" {
			return false, errors.New("canonical identity tuple is incomplete")
		}
		if sessionID != "" {
			if err := modulecore.SessionID(sessionID).Validate(); err != nil {
				return false, err
			}
		}
		return true, nil
	}
	if err := modulecore.SessionID(sessionID).Validate(); err != nil {
		return false, err
	}
	if err := modulecore.ThreadID(threadID).Validate(); err != nil {
		return false, err
	}
	if err := modulecore.ThreadSeq(threadSeq).Validate(); err != nil {
		return false, err
	}
	if err := modulecore.ThreadKind(threadKind).Validate(); err != nil {
		return false, err
	}
	audit.CanonicalThreadRows++
	return false, nil
}

func auditCanonicalL1Receipts(ctx context.Context, conn *sql.Conn, audit *SQLiteL1IdentityAudit) error {
	rows, err := conn.QueryContext(ctx, `SELECT session_id, typeof(session_id), thread_id, typeof(thread_id), thread_seq, typeof(thread_seq), thread_kind, typeof(thread_kind), closed_thread_id, typeof(closed_thread_id), closed_thread_seq, typeof(closed_thread_seq), closed_thread_kind, typeof(closed_thread_kind), result_json FROM conversation_turn_receipt ORDER BY turn_id ASC`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var sessionID, sessionType, threadID, threadType, threadKind, kindType, seqType string
		var closedID, closedType, closedSeqType, closedKind, closedKindType, resultJSON string
		var threadSeq, closedSeq int64
		if err := rows.Scan(&sessionID, &sessionType, &threadID, &threadType, &threadSeq, &seqType, &threadKind, &kindType, &closedID, &closedType, &closedSeq, &closedSeqType, &closedKind, &closedKindType, &resultJSON); err != nil {
			return err
		}
		if _, err := auditCanonicalTuple(sessionID, sessionType, threadID, threadType, threadSeq, seqType, threadKind, kindType, false, audit); err != nil {
			return err
		}
		if err := auditCanonicalClosedTuple(closedID, closedType, closedSeq, closedSeqType, closedKind, closedKindType, audit); err != nil {
			return err
		}
		if err := auditCanonicalTurnJSON(resultJSON, canonicalAuditTuple(sessionID, threadID, threadSeq, threadKind), canonicalAuditTuple("", closedID, closedSeq, closedKind)); err != nil {
			return err
		}
		audit.CanonicalJSONRows++
	}
	return rows.Err()
}

func auditCanonicalL1Outbox(ctx context.Context, conn *sql.Conn, audit *SQLiteL1IdentityAudit) error {
	rows, err := conn.QueryContext(ctx, `SELECT session_id, typeof(session_id), thread_id, typeof(thread_id), thread_seq, typeof(thread_seq), thread_kind, typeof(thread_kind), closed_thread_id, typeof(closed_thread_id), closed_thread_seq, typeof(closed_thread_seq), closed_thread_kind, typeof(closed_thread_kind), payload_json FROM conversation_turn_outbox ORDER BY turn_id ASC, target ASC`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var sessionID, sessionType, threadID, threadType, threadKind, kindType, seqType string
		var closedID, closedType, closedSeqType, closedKind, closedKindType, payloadJSON string
		var threadSeq, closedSeq int64
		if err := rows.Scan(&sessionID, &sessionType, &threadID, &threadType, &threadSeq, &seqType, &threadKind, &kindType, &closedID, &closedType, &closedSeq, &closedSeqType, &closedKind, &closedKindType, &payloadJSON); err != nil {
			return err
		}
		if _, err := auditCanonicalTuple(sessionID, sessionType, threadID, threadType, threadSeq, seqType, threadKind, kindType, false, audit); err != nil {
			return err
		}
		if err := auditCanonicalClosedTuple(closedID, closedType, closedSeq, closedSeqType, closedKind, closedKindType, audit); err != nil {
			return err
		}
		if err := auditCanonicalTurnJSON(payloadJSON, canonicalAuditTuple(sessionID, threadID, threadSeq, threadKind), canonicalAuditTuple("", closedID, closedSeq, closedKind)); err != nil {
			return err
		}
		audit.CanonicalJSONRows++
	}
	return rows.Err()
}

func auditCanonicalClosedTuple(threadID, threadType string, threadSeq int64, seqType, threadKind, kindType string, audit *SQLiteL1IdentityAudit) error {
	if threadID == "" && threadSeq == 0 && threadKind == "" {
		if threadType != "text" || seqType != "integer" || kindType != "text" {
			return errors.New("canonical closed identity storage class mismatch")
		}
		return nil
	}
	if threadType != "text" || seqType != "integer" || kindType != "text" || threadID == "" || threadKind == "" || threadSeq == 0 {
		return errors.New("canonical closed identity tuple is incomplete")
	}
	if err := modulecore.ThreadID(threadID).Validate(); err != nil {
		return err
	}
	if err := modulecore.ThreadSeq(threadSeq).Validate(); err != nil {
		return err
	}
	if err := modulecore.ThreadKind(threadKind).Validate(); err != nil {
		return err
	}
	audit.CanonicalClosedThreadRows++
	return nil
}

func canonicalAuditTuple(sessionID, threadID string, threadSeq int64, threadKind string) sqliteCanonicalThreadTuple {
	return sqliteCanonicalThreadTuple{
		SessionID:  modulecore.SessionID(sessionID),
		ThreadID:   modulecore.ThreadID(threadID),
		ThreadSeq:  modulecore.ThreadSeq(threadSeq),
		ThreadKind: modulecore.ThreadKind(threadKind),
	}
}

func auditCanonicalTurnJSON(encoded string, expected, expectedClosed sqliteCanonicalThreadTuple) error {
	receipt, err := AuditJSONIdentity([]byte(encoded))
	if err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(receipt.Occurrences))
	for _, occurrence := range receipt.Occurrences {
		if occurrence.Classification == JSONIdentityClassificationLegacyNumeric {
			return errors.New("canonical JSON retains a legacy numeric identity")
		}
		if occurrence.Pointer != "/"+occurrence.Key {
			return errors.New("canonical JSON identity is not at the root")
		}
		switch occurrence.Key {
		case JSONIdentityKeyThreadID, JSONIdentityKeyClosedThreadID:
			if occurrence.ValueKind != JSONIdentityValueString || occurrence.Classification != JSONIdentityClassificationCanonicalThread {
				return errors.New("canonical JSON thread ID is not a canonical string")
			}
		default:
			// AuditJSONIdentity intentionally reports only exact legacy ID
			// keys. thread_seq/thread_kind are checked by the strict typed
			// decode below, so the generic auditor remains unchanged.
		}
		seen[occurrence.Key] = struct{}{}
	}
	var projection struct {
		TurnID           string                `json:"turn_id"`
		SessionID        string                `json:"session_id"`
		ThreadID         modulecore.ThreadID   `json:"thread_id"`
		ThreadSeq        modulecore.ThreadSeq  `json:"thread_seq"`
		ThreadKind       modulecore.ThreadKind `json:"thread_kind"`
		ClosedThreadID   modulecore.ThreadID   `json:"closed_thread_id,omitempty"`
		ClosedThreadSeq  modulecore.ThreadSeq  `json:"closed_thread_seq,omitempty"`
		ClosedThreadKind modulecore.ThreadKind `json:"closed_thread_kind,omitempty"`
	}
	if err := json.Unmarshal([]byte(encoded), &projection); err != nil {
		return errors.New("canonical JSON typed identity decode failed")
	}
	if err := modulecore.SessionID(projection.SessionID).Validate(); err != nil {
		return errors.New("canonical JSON session ID is invalid")
	}
	if err := projection.ThreadID.Validate(); err != nil {
		return errors.New("canonical JSON thread ID is invalid")
	}
	if err := projection.ThreadSeq.Validate(); err != nil {
		return errors.New("canonical JSON thread sequence is invalid")
	}
	if err := projection.ThreadKind.Validate(); err != nil {
		return errors.New("canonical JSON thread kind is invalid")
	}
	if expected.ThreadID == "" || expected.ThreadSeq == 0 || expected.ThreadKind == "" || projection.SessionID == "" {
		return errors.New("SQL identity tuple for canonical JSON is incomplete")
	}
	if err := modulecore.SessionID(expected.SessionID).Validate(); err != nil {
		return errors.New("SQL session identity tuple is invalid")
	}
	if err := expected.ThreadID.Validate(); err != nil {
		return errors.New("SQL thread identity tuple is invalid")
	}
	if err := expected.ThreadSeq.Validate(); err != nil {
		return errors.New("SQL thread sequence tuple is invalid")
	}
	if err := expected.ThreadKind.Validate(); err != nil {
		return errors.New("SQL thread kind tuple is invalid")
	}
	if projection.SessionID != string(expected.SessionID) || projection.ThreadID != expected.ThreadID || projection.ThreadSeq != expected.ThreadSeq || projection.ThreadKind != expected.ThreadKind {
		return errors.New("canonical JSON identity does not match SQL tuple")
	}
	if _, ok := seen[JSONIdentityKeyThreadID]; !ok {
		return errors.New("canonical JSON is missing a required thread identity field")
	}
	closedExpected := expectedClosed.ThreadID != "" || expectedClosed.ThreadSeq != 0 || expectedClosed.ThreadKind != "" || expectedClosed.SessionID != ""
	if !closedExpected {
		if projection.ClosedThreadID != "" || projection.ClosedThreadSeq != 0 || projection.ClosedThreadKind != "" {
			return errors.New("canonical JSON closed identity does not match SQL tuple")
		}
		if projection.ClosedThreadSeq != 0 || projection.ClosedThreadKind != "" {
			return errors.New("canonical JSON closed identity tuple is incomplete")
		}
		if _, ok := seen[JSONIdentityKeyClosedThreadID]; ok {
			return errors.New("canonical JSON contains an empty closed thread ID")
		}
	} else {
		if expectedClosed.ThreadID == "" || expectedClosed.ThreadSeq == 0 || expectedClosed.ThreadKind == "" {
			return errors.New("SQL closed identity tuple is incomplete")
		}
		if err := expectedClosed.ThreadID.Validate(); err != nil {
			return errors.New("SQL closed thread identity tuple is invalid")
		}
		if err := expectedClosed.ThreadSeq.Validate(); err != nil {
			return errors.New("SQL closed thread sequence tuple is invalid")
		}
		if err := expectedClosed.ThreadKind.Validate(); err != nil {
			return errors.New("SQL closed thread kind tuple is invalid")
		}
		if err := projection.ClosedThreadID.Validate(); err != nil {
			return errors.New("canonical JSON closed thread ID is invalid")
		}
		if err := projection.ClosedThreadSeq.Validate(); err != nil {
			return errors.New("canonical JSON closed thread sequence is invalid")
		}
		if err := projection.ClosedThreadKind.Validate(); err != nil {
			return errors.New("canonical JSON closed thread kind is invalid")
		}
		if _, ok := seen[JSONIdentityKeyClosedThreadID]; !ok {
			return errors.New("canonical JSON is missing a closed thread ID")
		}
		if projection.ClosedThreadID != expectedClosed.ThreadID || projection.ClosedThreadSeq != expectedClosed.ThreadSeq || projection.ClosedThreadKind != expectedClosed.ThreadKind {
			return errors.New("canonical JSON closed identity does not match SQL tuple")
		}
	}
	return nil
}

func (receipt SQLiteL1MaterializationReceipt) CanonicalJSON() ([]byte, error) {
	type canonicalReceipt struct {
		SchemaVersion                     string                              `json:"schema_version"`
		Status                            string                              `json:"status"`
		OwnerSchemaReconciliationRequired bool                                `json:"owner_schema_reconciliation_required"`
		InventoryReceiptSHA256            string                              `json:"inventory_receipt_sha256"`
		MappingSHA256                     string                              `json:"mapping_sha256"`
		TableCounts                       []SQLiteL1MaterializationTableCount `json:"table_counts"`
		IdentityAudit                     SQLiteL1IdentityAudit               `json:"identity_audit"`
	}
	counts := append([]SQLiteL1MaterializationTableCount(nil), receipt.TableCounts...)
	sort.Slice(counts, func(left, right int) bool { return counts[left].Table < counts[right].Table })
	if counts == nil {
		counts = []SQLiteL1MaterializationTableCount{}
	}
	return json.Marshal(canonicalReceipt{
		SchemaVersion:                     receipt.SchemaVersion,
		Status:                            receipt.Status,
		OwnerSchemaReconciliationRequired: receipt.OwnerSchemaReconciliationRequired,
		InventoryReceiptSHA256:            receipt.InventoryReceiptSHA256,
		MappingSHA256:                     receipt.MappingSHA256,
		TableCounts:                       counts,
		IdentityAudit:                     receipt.IdentityAudit,
	})
}

func (receipt SQLiteL1MaterializationReceipt) ComputeSHA256() (string, error) {
	encoded, err := receipt.CanonicalJSON()
	if err != nil {
		return "", err
	}
	return sha256HexJSONIdentity(encoded), nil
}

func (receipt SQLiteL1MaterializationReceipt) Validate() error {
	if receipt.SchemaVersion != SQLiteL1MaterializationReceiptSchemaVersion || receipt.Status != SQLiteL1MaterializationStatus {
		return errors.New("invalid L1 materialization receipt schema or status")
	}
	if !receipt.OwnerSchemaReconciliationRequired {
		return errors.New("owner schema reconciliation requirement is missing")
	}
	if err := validateSHA256Hex(receipt.InventoryReceiptSHA256, "inventory receipt SHA256"); err != nil {
		return err
	}
	if err := validateSHA256Hex(receipt.MappingSHA256, "mapping SHA256"); err != nil {
		return err
	}
	if len(receipt.TableCounts) != len(canonicalL1MaterializationTables) {
		return errors.New("L1 materialization table count coverage is incomplete")
	}
	seen := make(map[string]struct{}, len(receipt.TableCounts))
	for index, count := range receipt.TableCounts {
		if count.Rows < 0 || strings.TrimSpace(count.Table) == "" {
			return errors.New("invalid L1 materialization table count")
		}
		if _, ok := seen[count.Table]; ok {
			return errors.New("duplicate L1 materialization table count")
		}
		seen[count.Table] = struct{}{}
		if index > 0 && receipt.TableCounts[index-1].Table >= count.Table {
			return errors.New("L1 materialization table counts are not sorted")
		}
	}
	for _, table := range canonicalL1MaterializationTables {
		if _, ok := seen[table]; !ok {
			return errors.New("L1 materialization table count is missing")
		}
	}
	if receipt.IdentityAudit.CanonicalThreadRows < 0 || receipt.IdentityAudit.OptionalZeroRows < 0 || receipt.IdentityAudit.CanonicalClosedThreadRows < 0 || receipt.IdentityAudit.CanonicalJSONRows < 0 || receipt.IdentityAudit.LegacyNumericRows < 0 || receipt.IdentityAudit.LegacyNumericRows != 0 {
		return errors.New("invalid L1 materialization identity audit")
	}
	var totalRows int64
	for _, count := range receipt.TableCounts {
		totalRows += count.Rows
	}
	if receipt.IdentityAudit.CanonicalThreadRows+receipt.IdentityAudit.OptionalZeroRows != totalRows {
		return errors.New("L1 materialization identity row counts do not reconcile")
	}
	var receiptRows, outboxRows int64
	for _, count := range receipt.TableCounts {
		switch count.Table {
		case turnReceiptSurface:
			receiptRows = count.Rows
		case turnOutboxSurface:
			outboxRows = count.Rows
		}
	}
	if receipt.IdentityAudit.CanonicalJSONRows != receiptRows+outboxRows {
		return errors.New("L1 materialization JSON row counts do not reconcile")
	}
	if receipt.IdentityAudit.CanonicalClosedThreadRows > receipt.IdentityAudit.CanonicalJSONRows {
		return errors.New("L1 materialization closed identity count exceeds JSON rows")
	}
	if err := validateSHA256Hex(receipt.ReceiptSHA256, "L1 materialization receipt SHA256"); err != nil {
		return err
	}
	computed, err := receipt.ComputeSHA256()
	if err != nil {
		return err
	}
	if computed != receipt.ReceiptSHA256 {
		return errors.New("L1 materialization receipt SHA256 does not match canonical JSON")
	}
	return nil
}
