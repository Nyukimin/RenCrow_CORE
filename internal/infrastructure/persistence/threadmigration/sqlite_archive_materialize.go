package threadmigration

// This file owns the bounded, archive-only half of Step 05. It accepts
// caller-owned database handles and materializes only the three legacy archive
// identity surfaces into a disposable destination clone. It never opens,
// closes, copies, renames, or deletes a file and it never writes to Source.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	SQLiteArchiveMaterializationReceiptSchemaVersion = "rencrow.threadmigration.sqlite_archive_materialization.v1"
	SQLiteArchiveMaterializationStatus               = "materialized_archive_not_runtime_ready"
)

// ArchiveSQLiteMaterializationInput contains caller-owned handles. Source is
// the exact legacy archive snapshot inventoried in Inventory; Destination is a
// separate disposable clone. Neither handle is closed by the operation.
type ArchiveSQLiteMaterializationInput struct {
	Source      *sql.DB
	Destination *sql.DB
	Inventory   SQLiteInventoryResult
}

// SQLiteArchiveMaterializationTableCount is one committed canonical archive
// row count. Receipt table counts are sorted lexicographically.
type SQLiteArchiveMaterializationTableCount struct {
	Table string `json:"table"`
	Rows  int64  `json:"rows"`
}

// SQLiteArchiveIdentityAudit is a bounded post-commit identity audit. Every
// archive row is either a positive canonical tuple or an allowed optional-zero
// tuple; LegacyNumericRows must remain zero.
type SQLiteArchiveIdentityAudit struct {
	CanonicalThreadRows int64 `json:"canonical_thread_rows"`
	OptionalZeroRows    int64 `json:"optional_zero_rows"`
	LegacyNumericRows   int64 `json:"legacy_numeric_rows"`
}

// SQLiteArchiveMaterializationReceipt is deterministic and bounded. It does
// not contain paths, SQL, row identifiers, payloads, or raw database errors.
// Owner indexes and other reconciliation work remain for the later owner-open
// step, so a successful receipt is deliberately not runtime-ready.
type SQLiteArchiveMaterializationReceipt struct {
	SchemaVersion                     string                                   `json:"schema_version"`
	Status                            string                                   `json:"status"`
	OwnerSchemaReconciliationRequired bool                                     `json:"owner_schema_reconciliation_required"`
	InventoryReceiptSHA256            string                                   `json:"inventory_receipt_sha256"`
	MappingSHA256                     string                                   `json:"mapping_sha256"`
	TableCounts                       []SQLiteArchiveMaterializationTableCount `json:"table_counts"`
	IdentityAudit                     SQLiteArchiveIdentityAudit               `json:"identity_audit"`
	ReceiptSHA256                     string                                   `json:"receipt_sha256"`
}

// ArchiveSQLiteMaterializationError is a bounded typed error. PostCommit is
// true after the destination transaction has committed; callers must then
// discard the destination rather than claiming a rollback occurred.
type ArchiveSQLiteMaterializationError struct {
	Code       string
	Phase      string
	PostCommit bool
	cause      error
}

func (err *ArchiveSQLiteMaterializationError) Error() string {
	if err == nil {
		return "<nil>"
	}
	if err.PostCommit {
		return fmt.Sprintf("archive SQLite materialization %s failed after commit; destination is unusable", err.Code)
	}
	return fmt.Sprintf("archive SQLite materialization %s failed during %s", err.Code, err.Phase)
}

func (err *ArchiveSQLiteMaterializationError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.cause
}

var canonicalArchiveMaterializationTables = []string{
	sessionThreadSurface,
	threadSummaryReceiptSurface,
	l1MemoryEventArchiveSurface,
}

var archiveMaterializationCopyOrder = []string{
	sessionThreadSurface,
	threadSummaryReceiptSurface,
	l1MemoryEventArchiveSurface,
}

// Summary and memory have no declared foreign key, but dropping dependents
// first keeps this order safe if the disposable clone carries owner-side
// references that are reconciled later.
var archiveMaterializationDropOrder = []string{
	threadSummaryReceiptSurface,
	l1MemoryEventArchiveSurface,
	sessionThreadSurface,
}

var canonicalArchiveMaterializationDescriptors = []legacyTableDescriptor{
	{Database: "archive", Name: sessionThreadSurface, Columns: []legacyColumnDescriptor{
		{Name: "thread_id", Type: "TEXT", NotNull: 1, PrimaryKey: 1},
		{Name: "thread_seq", Type: "INTEGER", NotNull: 1},
		{Name: "thread_kind", Type: "TEXT", NotNull: 1},
		{Name: "session_id", Type: "VARCHAR", NotNull: 1},
		{Name: "ts_start", Type: "TIMESTAMP", NotNull: 1},
		{Name: "ts_end", Type: "TIMESTAMP"},
		{Name: "domain", Type: "VARCHAR"},
		{Name: "summary", Type: "TEXT"},
		{Name: "keywords", Type: "TEXT"},
		{Name: "embedding", Type: "TEXT"},
		{Name: "is_novel", Type: "BOOLEAN"},
		{Name: "created_at", Type: "TIMESTAMP", Default: stringPointer("CURRENT_TIMESTAMP")},
	}},
	{Database: "archive", Name: threadSummaryReceiptSurface, Columns: []legacyColumnDescriptor{
		{Name: "thread_id", Type: "TEXT", NotNull: 1, PrimaryKey: 1},
		{Name: "schema_version", Type: "TEXT", NotNull: 1},
		{Name: "generation_mode", Type: "TEXT", NotNull: 1},
		{Name: "provider", Type: "TEXT", NotNull: 1},
		{Name: "failure_code", Type: "TEXT", NotNull: 1},
		{Name: "evidence_sha256", Type: "TEXT", NotNull: 1},
		{Name: "source_turn_count", Type: "INTEGER", NotNull: 1},
		{Name: "roles_json", Type: "TEXT", NotNull: 1},
		{Name: "created_at", Type: "TIMESTAMP", NotNull: 1},
	}},
	{Database: "archive", Name: l1MemoryEventArchiveSurface, Columns: []legacyColumnDescriptor{
		{Name: "id", Type: "VARCHAR", PrimaryKey: 1},
		{Name: "namespace", Type: "VARCHAR", NotNull: 1},
		{Name: "session_id", Type: "VARCHAR", NotNull: 1},
		{Name: "thread_id", Type: "TEXT", NotNull: 1},
		{Name: "thread_seq", Type: "INTEGER", NotNull: 1},
		{Name: "thread_kind", Type: "TEXT", NotNull: 1},
		{Name: "speaker", Type: "VARCHAR", NotNull: 1},
		{Name: "message", Type: "TEXT", NotNull: 1},
		{Name: "meta_json", Type: "TEXT", NotNull: 1},
		{Name: "memory_state", Type: "VARCHAR", NotNull: 1},
		{Name: "layer", Type: "VARCHAR", NotNull: 1},
		{Name: "source", Type: "VARCHAR", NotNull: 1},
		{Name: "created_at", Type: "TIMESTAMP", NotNull: 1},
		{Name: "updated_at", Type: "TIMESTAMP", NotNull: 1},
	}},
}

var archiveMaterializationCreateStatements = []string{
	`CREATE TABLE "session_thread_s5_archive_new" (
		thread_id TEXT PRIMARY KEY NOT NULL,
		thread_seq INTEGER NOT NULL CHECK (thread_seq > 0),
		thread_kind TEXT NOT NULL CHECK (thread_kind IN ('user_conversation', 'agent_discussion', 'idlechat', 'document', 'system')),
		session_id VARCHAR NOT NULL,
		ts_start TIMESTAMP NOT NULL,
		ts_end TIMESTAMP,
		domain VARCHAR,
		summary TEXT,
		keywords TEXT,
		embedding TEXT,
		is_novel BOOLEAN,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		UNIQUE (session_id, thread_seq)
	)`,
	`CREATE TABLE "conversation_thread_summary_receipt_s5_archive_new" (
		thread_id TEXT PRIMARY KEY NOT NULL,
		schema_version TEXT NOT NULL,
		generation_mode TEXT NOT NULL,
		provider TEXT NOT NULL,
		failure_code TEXT NOT NULL,
		evidence_sha256 TEXT NOT NULL,
		source_turn_count INTEGER NOT NULL,
		roles_json TEXT NOT NULL,
		created_at TIMESTAMP NOT NULL
	)`,
	`CREATE TABLE "l1_memory_event_archive_s5_archive_new" (
		id VARCHAR PRIMARY KEY,
		namespace VARCHAR NOT NULL,
		session_id VARCHAR NOT NULL,
		thread_id TEXT NOT NULL,
		thread_seq INTEGER NOT NULL,
		thread_kind TEXT NOT NULL,
		speaker VARCHAR NOT NULL,
		message TEXT NOT NULL,
		meta_json TEXT NOT NULL,
		memory_state VARCHAR NOT NULL,
		layer VARCHAR NOT NULL,
		source VARCHAR NOT NULL,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL,
		CHECK (
			(thread_id = '' AND thread_seq = 0 AND thread_kind = '') OR
			(thread_id <> '' AND thread_seq > 0 AND thread_kind IN ('user_conversation', 'agent_discussion', 'idlechat', 'document', 'system'))
		)
	)`,
}

// MaterializeArchiveSQLite rebuilds only the three archive identity surfaces
// inside one destination transaction. Source rows are streamed in primary-key
// order, and a successful receipt still requires later owner-schema opening.
func MaterializeArchiveSQLite(ctx context.Context, input ArchiveSQLiteMaterializationInput) (SQLiteArchiveMaterializationReceipt, error) {
	if ctx == nil {
		return SQLiteArchiveMaterializationReceipt{}, newArchiveMaterializationError("invalid_input", "preflight", false, errors.New("nil context"))
	}
	if err := ctx.Err(); err != nil {
		return SQLiteArchiveMaterializationReceipt{}, newArchiveMaterializationError("canceled", "preflight", false, err)
	}
	if input.Source == nil || input.Destination == nil || input.Source == input.Destination {
		return SQLiteArchiveMaterializationReceipt{}, newArchiveMaterializationError("invalid_input", "preflight", false, errors.New("source and destination handles must be distinct"))
	}
	if err := input.Inventory.Validate(); err != nil {
		return SQLiteArchiveMaterializationReceipt{}, newArchiveMaterializationError("invalid_inventory", "preflight", false, err)
	}
	index, expectedCounts, err := preflightArchiveMaterialization(ctx, input)
	if err != nil {
		return SQLiteArchiveMaterializationReceipt{}, newArchiveMaterializationError("preflight", "preflight", false, err)
	}

	destinationConn, err := input.Destination.Conn(ctx)
	if err != nil {
		return SQLiteArchiveMaterializationReceipt{}, newArchiveMaterializationError("destination_connection", "preflight", false, err)
	}
	defer destinationConn.Close()
	if err := enableDestinationForeignKeys(ctx, destinationConn); err != nil {
		return SQLiteArchiveMaterializationReceipt{}, newArchiveMaterializationError("foreign_keys", "preflight", false, err)
	}
	if err := rejectArchiveStageCollisions(ctx, destinationConn); err != nil {
		return SQLiteArchiveMaterializationReceipt{}, newArchiveMaterializationError("stage_collision", "preflight", false, err)
	}

	tx, err := destinationConn.BeginTx(ctx, nil)
	if err != nil {
		return SQLiteArchiveMaterializationReceipt{}, newArchiveMaterializationError("begin_transaction", "prepare", false, err)
	}
	rollback := func(code, phase string, cause error) (SQLiteArchiveMaterializationReceipt, error) {
		_ = tx.Rollback()
		return SQLiteArchiveMaterializationReceipt{}, newArchiveMaterializationError(code, phase, false, cause)
	}
	for _, statement := range archiveMaterializationCreateStatements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return rollback("create_stage", "prepare", err)
		}
	}

	for _, surface := range archiveMaterializationCopyOrder {
		var copied int64
		switch surface {
		case sessionThreadSurface:
			copied, err = copyArchiveSessionThreads(ctx, input.Source, tx, index)
		case threadSummaryReceiptSurface:
			copied, err = copyArchiveSummaryReceipts(ctx, input.Source, tx, index)
		case l1MemoryEventArchiveSurface:
			copied, err = copyArchiveMemoryEvents(ctx, input.Source, tx, index)
		default:
			err = errors.New("unknown archive materialization surface")
		}
		if err != nil {
			return rollback("copy_rows", "copy", err)
		}
		if copied != expectedCounts[surface] {
			return rollback("copy_count", "copy", errors.New("staged row count mismatch"))
		}
	}
	if err := verifyArchiveStagedCounts(ctx, tx, expectedCounts); err != nil {
		return rollback("staged_count", "copy", err)
	}
	if err := ctx.Err(); err != nil {
		return rollback("canceled", "copy", err)
	}
	for _, surface := range archiveMaterializationDropOrder {
		if _, err := tx.ExecContext(ctx, `DROP TABLE `+quoteSQLiteIdentifier(surface)); err != nil {
			return rollback("drop_legacy", "swap", err)
		}
	}
	for _, surface := range archiveMaterializationCopyOrder {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE `+quoteSQLiteIdentifier(archiveStageTableName(surface))+` RENAME TO `+quoteSQLiteIdentifier(surface)); err != nil {
			return rollback("rename_stage", "swap", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return rollback("canceled", "swap", err)
	}
	if err := tx.Commit(); err != nil {
		return SQLiteArchiveMaterializationReceipt{}, newArchiveMaterializationError("commit", "commit", true, err)
	}

	counts, audit, err := validateCommittedArchiveMaterialization(ctx, destinationConn, expectedCounts)
	if err != nil {
		return SQLiteArchiveMaterializationReceipt{}, newArchiveMaterializationError("post_commit_validation", "post_commit", true, err)
	}
	receipt := SQLiteArchiveMaterializationReceipt{
		SchemaVersion:                     SQLiteArchiveMaterializationReceiptSchemaVersion,
		Status:                            SQLiteArchiveMaterializationStatus,
		OwnerSchemaReconciliationRequired: true,
		InventoryReceiptSHA256:            input.Inventory.Receipt.ReceiptSHA256,
		MappingSHA256:                     input.Inventory.Plan.MappingSHA256,
		TableCounts:                       counts,
		IdentityAudit:                     audit,
	}
	receiptHash, err := receipt.ComputeSHA256()
	if err != nil {
		return SQLiteArchiveMaterializationReceipt{}, newArchiveMaterializationError("receipt_hash", "post_commit", true, err)
	}
	receipt.ReceiptSHA256 = receiptHash
	if err := receipt.Validate(); err != nil {
		return SQLiteArchiveMaterializationReceipt{}, newArchiveMaterializationError("receipt_validation", "post_commit", true, err)
	}
	return receipt, nil
}

func newArchiveMaterializationError(code, phase string, postCommit bool, cause error) error {
	return &ArchiveSQLiteMaterializationError{Code: code, Phase: phase, PostCommit: postCommit, cause: cause}
}

func preflightArchiveMaterialization(ctx context.Context, input ArchiveSQLiteMaterializationInput) (sqliteTransformIndex, map[string]int64, error) {
	index, err := newSQLiteTransformIndex(input.Inventory.Plan)
	if err != nil {
		return sqliteTransformIndex{}, nil, err
	}
	for _, descriptor := range legacyArchiveTables {
		observed, err := inspectLegacySchema(ctx, input.Source, descriptor)
		if err != nil {
			return sqliteTransformIndex{}, nil, err
		}
		expected, ok := inventorySchemaFingerprint(input.Inventory.Receipt, descriptor.Database, descriptor.Name)
		if !ok || observed.SHA256 != expected.SHA256 {
			return sqliteTransformIndex{}, nil, errors.New("source archive schema fingerprint does not match inventory")
		}
		if _, err := inspectLegacySchema(ctx, input.Destination, descriptor); err != nil {
			return sqliteTransformIndex{}, nil, fmt.Errorf("destination archive legacy schema mismatch: %w", err)
		}
	}
	if err := rejectArchiveStageCollisions(ctx, input.Destination); err != nil {
		return sqliteTransformIndex{}, nil, err
	}
	expectedCounts := make(map[string]int64, len(legacyArchiveTables))
	for _, descriptor := range legacyArchiveTables {
		count, err := countArchiveSQLiteTable(ctx, input.Source, descriptor.Name)
		if err != nil {
			return sqliteTransformIndex{}, nil, fmt.Errorf("read source archive row count: %w", err)
		}
		receiptCount, ok := input.Inventory.Receipt.SurfaceCount(descriptor.Name)
		if !ok || count != receiptCount.Rows {
			return sqliteTransformIndex{}, nil, errors.New("source archive row count does not match inventory")
		}
		destinationCount, err := countArchiveSQLiteTable(ctx, input.Destination, descriptor.Name)
		if err != nil {
			return sqliteTransformIndex{}, nil, fmt.Errorf("read destination archive row count: %w", err)
		}
		if destinationCount != receiptCount.Rows {
			return sqliteTransformIndex{}, nil, errors.New("destination archive row count does not match inventory")
		}
		expectedCounts[descriptor.Name] = count
	}
	return index, expectedCounts, nil
}

func countArchiveSQLiteTable(ctx context.Context, db *sql.DB, table string) (int64, error) {
	var count int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+quoteSQLiteIdentifier(table)).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func archiveStageTableName(surface string) string { return surface + "_s5_archive_new" }

func rejectArchiveStageCollisions(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}) error {
	for _, surface := range canonicalArchiveMaterializationTables {
		var count int64
		if err := queryer.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, archiveStageTableName(surface)).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return errors.New("destination contains a reserved archive materialization stage object")
		}
	}
	return nil
}

func verifyArchiveStagedCounts(ctx context.Context, tx *sql.Tx, expected map[string]int64) error {
	for _, surface := range canonicalArchiveMaterializationTables {
		var count int64
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+quoteSQLiteIdentifier(archiveStageTableName(surface))).Scan(&count); err != nil {
			return err
		}
		if count != expected[surface] {
			return errors.New("staged archive table count mismatch")
		}
	}
	return nil
}

func copyArchiveSessionThreads(ctx context.Context, source *sql.DB, tx *sql.Tx, index sqliteTransformIndex) (int64, error) {
	rows, err := source.QueryContext(ctx, `SELECT thread_id, session_id, ts_start, ts_end, domain, summary, keywords, embedding, is_novel, created_at FROM session_thread ORDER BY thread_id ASC`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO "session_thread_s5_archive_new" (thread_id, thread_seq, thread_kind, session_id, ts_start, ts_end, domain, summary, keywords, embedding, is_novel, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	var count int64
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return count, err
		}
		var legacyThreadID int64
		var legacySessionID string
		var tsStart, tsEnd, domain, summary, keywords, embedding, isNovel, createdAt interface{}
		if err := rows.Scan(&legacyThreadID, &legacySessionID, &tsStart, &tsEnd, &domain, &summary, &keywords, &embedding, &isNovel, &createdAt); err != nil {
			return count, err
		}
		tuple, err := resolveSQLiteThreadTuple(index, legacySessionID, legacyThreadID)
		if err != nil {
			return count, err
		}
		if _, err := stmt.ExecContext(ctx, string(tuple.ThreadID), int64(tuple.ThreadSeq), string(tuple.ThreadKind), string(tuple.SessionID), tsStart, tsEnd, domain, summary, keywords, embedding, isNovel, createdAt); err != nil {
			return count, err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return count, err
	}
	return count, rows.Close()
}

func copyArchiveSummaryReceipts(ctx context.Context, source *sql.DB, tx *sql.Tx, index sqliteTransformIndex) (int64, error) {
	rows, err := source.QueryContext(ctx, `SELECT r.thread_id, s.session_id, r.schema_version, r.generation_mode, r.provider, r.failure_code, r.evidence_sha256, r.source_turn_count, r.roles_json, r.created_at FROM conversation_thread_summary_receipt AS r JOIN session_thread AS s ON s.thread_id = r.thread_id ORDER BY r.thread_id ASC`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO "conversation_thread_summary_receipt_s5_archive_new" (thread_id, schema_version, generation_mode, provider, failure_code, evidence_sha256, source_turn_count, roles_json, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	var count int64
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return count, err
		}
		var legacyThreadID int64
		var legacySessionID, schemaVersion, generationMode, provider, failureCode, evidenceSHA256, rolesJSON string
		var sourceTurnCount int64
		var createdAt interface{}
		if err := rows.Scan(&legacyThreadID, &legacySessionID, &schemaVersion, &generationMode, &provider, &failureCode, &evidenceSHA256, &sourceTurnCount, &rolesJSON, &createdAt); err != nil {
			return count, err
		}
		tuple, err := resolveSQLiteThreadTuple(index, legacySessionID, legacyThreadID)
		if err != nil {
			return count, err
		}
		if _, err := stmt.ExecContext(ctx, string(tuple.ThreadID), schemaVersion, generationMode, provider, failureCode, evidenceSHA256, sourceTurnCount, rolesJSON, createdAt); err != nil {
			return count, err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return count, err
	}
	return count, rows.Close()
}

func copyArchiveMemoryEvents(ctx context.Context, source *sql.DB, tx *sql.Tx, index sqliteTransformIndex) (int64, error) {
	rows, err := source.QueryContext(ctx, `SELECT id, namespace, session_id, thread_id, speaker, message, meta_json, memory_state, layer, source, created_at, updated_at FROM l1_memory_event_archive ORDER BY id ASC`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO "l1_memory_event_archive_s5_archive_new" (id, namespace, session_id, thread_id, thread_seq, thread_kind, speaker, message, meta_json, memory_state, layer, source, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	var count int64
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return count, err
		}
		var id, namespace, legacySessionID, speaker, message, metaJSON, memoryState, layer, sourceValue string
		var legacyThreadID int64
		var createdAt, updatedAt interface{}
		if err := rows.Scan(&id, &namespace, &legacySessionID, &legacyThreadID, &speaker, &message, &metaJSON, &memoryState, &layer, &sourceValue, &createdAt, &updatedAt); err != nil {
			return count, err
		}
		tuple, err := resolveSQLiteOptionalThreadTuple(index, legacySessionID, legacyThreadID)
		if err != nil {
			return count, err
		}
		namespace, err = rewriteL1LegacyNamespace(index, legacySessionID, legacyThreadID, namespace)
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

func validateCommittedArchiveMaterialization(ctx context.Context, conn *sql.Conn, expected map[string]int64) ([]SQLiteArchiveMaterializationTableCount, SQLiteArchiveIdentityAudit, error) {
	var enabled int64
	if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&enabled); err != nil {
		return nil, SQLiteArchiveIdentityAudit{}, err
	}
	if enabled != 1 {
		return nil, SQLiteArchiveIdentityAudit{}, errors.New("destination foreign keys are disabled after archive commit")
	}
	foreignKeys, err := conn.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return nil, SQLiteArchiveIdentityAudit{}, err
	}
	for foreignKeys.Next() {
		_ = foreignKeys.Close()
		return nil, SQLiteArchiveIdentityAudit{}, errors.New("destination archive foreign key check failed")
	}
	if err := foreignKeys.Err(); err != nil {
		_ = foreignKeys.Close()
		return nil, SQLiteArchiveIdentityAudit{}, err
	}
	if err := foreignKeys.Close(); err != nil {
		return nil, SQLiteArchiveIdentityAudit{}, err
	}

	for _, descriptor := range canonicalArchiveMaterializationDescriptors {
		if _, err := inspectLegacySchemaOnConn(ctx, conn, descriptor); err != nil {
			return nil, SQLiteArchiveIdentityAudit{}, err
		}
	}
	if err := validateArchiveCanonicalConstraints(ctx, conn); err != nil {
		return nil, SQLiteArchiveIdentityAudit{}, err
	}

	counts := make([]SQLiteArchiveMaterializationTableCount, 0, len(canonicalArchiveMaterializationTables))
	for _, surface := range canonicalArchiveMaterializationTables {
		var count int64
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+quoteSQLiteIdentifier(surface)).Scan(&count); err != nil {
			return nil, SQLiteArchiveIdentityAudit{}, err
		}
		if count != expected[surface] {
			return nil, SQLiteArchiveIdentityAudit{}, errors.New("committed archive row count mismatch")
		}
		counts = append(counts, SQLiteArchiveMaterializationTableCount{Table: surface, Rows: count})
	}
	sort.Slice(counts, func(left, right int) bool { return counts[left].Table < counts[right].Table })

	audit := SQLiteArchiveIdentityAudit{}
	count, err := auditArchiveSessionThreads(ctx, conn, &audit)
	if err != nil || count != expected[sessionThreadSurface] {
		if err != nil {
			return nil, SQLiteArchiveIdentityAudit{}, err
		}
		return nil, SQLiteArchiveIdentityAudit{}, errors.New("committed session_thread audit count mismatch")
	}
	count, err = auditArchiveSummaryReceipts(ctx, conn, &audit)
	if err != nil || count != expected[threadSummaryReceiptSurface] {
		if err != nil {
			return nil, SQLiteArchiveIdentityAudit{}, err
		}
		return nil, SQLiteArchiveIdentityAudit{}, errors.New("committed summary receipt audit count mismatch")
	}
	count, err = auditArchiveMemoryEvents(ctx, conn, &audit)
	if err != nil || count != expected[l1MemoryEventArchiveSurface] {
		if err != nil {
			return nil, SQLiteArchiveIdentityAudit{}, err
		}
		return nil, SQLiteArchiveIdentityAudit{}, errors.New("committed archive memory audit count mismatch")
	}
	if audit.LegacyNumericRows != 0 {
		return nil, SQLiteArchiveIdentityAudit{}, errors.New("committed archive destination retains numeric identity")
	}
	return counts, audit, nil
}

func validateArchiveCanonicalConstraints(ctx context.Context, conn *sql.Conn) error {
	for _, requirement := range []struct {
		table     string
		fragments []string
	}{
		{table: sessionThreadSurface, fragments: []string{"check (thread_seq > 0)", "check (thread_kind in ('user_conversation', 'agent_discussion', 'idlechat', 'document', 'system'))"}},
		{table: l1MemoryEventArchiveSurface, fragments: []string{"thread_id = '' and thread_seq = 0 and thread_kind = ''", "thread_id <> '' and thread_seq > 0 and thread_kind in ('user_conversation', 'agent_discussion', 'idlechat', 'document', 'system')"}},
	} {
		var schema sql.NullString
		if err := conn.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE name = ?`, requirement.table).Scan(&schema); err != nil {
			return err
		}
		if !schema.Valid {
			return errors.New("canonical archive table SQL is missing")
		}
		normalized := strings.ToLower(strings.Join(strings.Fields(schema.String), " "))
		for _, fragment := range requirement.fragments {
			if !strings.Contains(normalized, fragment) {
				return errors.New("canonical archive CHECK contract is missing")
			}
		}
	}
	return validateArchiveSessionThreadUnique(ctx, conn)
}

func validateArchiveSessionThreadUnique(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, `PRAGMA index_list("session_thread")`)
	if err != nil {
		return err
	}
	type indexDescriptor struct {
		name   string
		unique int64
	}
	indexes := make([]indexDescriptor, 0, 2)
	for rows.Next() {
		var sequence, unique, partial int64
		var name, origin string
		if err := rows.Scan(&sequence, &name, &unique, &origin, &partial); err != nil {
			_ = rows.Close()
			return err
		}
		if unique == 1 {
			indexes = append(indexes, indexDescriptor{name: name, unique: unique})
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	matching := 0
	for _, index := range indexes {
		infoRows, err := conn.QueryContext(ctx, `PRAGMA index_info(`+quoteSQLiteIdentifier(index.name)+`)`)
		if err != nil {
			return err
		}
		columns := make([]string, 0, 2)
		for infoRows.Next() {
			var sequence, columnID int64
			var columnName string
			if err := infoRows.Scan(&sequence, &columnID, &columnName); err != nil {
				_ = infoRows.Close()
				return err
			}
			columns = append(columns, columnName)
		}
		if err := infoRows.Err(); err != nil {
			_ = infoRows.Close()
			return err
		}
		if err := infoRows.Close(); err != nil {
			return err
		}
		if len(columns) == 2 && columns[0] == "session_id" && columns[1] == "thread_seq" {
			matching++
		}
	}
	if matching != 1 {
		return errors.New("canonical session_thread unique identity contract is missing")
	}
	return nil
}

func auditArchiveSessionThreads(ctx context.Context, conn *sql.Conn, audit *SQLiteArchiveIdentityAudit) (int64, error) {
	rows, err := conn.QueryContext(ctx, `SELECT session_id, typeof(session_id), thread_id, typeof(thread_id), thread_seq, typeof(thread_seq), thread_kind, typeof(thread_kind) FROM session_thread ORDER BY thread_id ASC`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var count int64
	for rows.Next() {
		var sessionID, sessionType, threadID, threadType, threadKind, kindType, seqType string
		var threadSeq int64
		if err := rows.Scan(&sessionID, &sessionType, &threadID, &threadType, &threadSeq, &seqType, &threadKind, &kindType); err != nil {
			return count, err
		}
		if _, err := auditArchiveTuple(sessionID, sessionType, threadID, threadType, threadSeq, seqType, threadKind, kindType, false, audit); err != nil {
			return count, err
		}
		count++
	}
	return count, rows.Err()
}

func auditArchiveSummaryReceipts(ctx context.Context, conn *sql.Conn, audit *SQLiteArchiveIdentityAudit) (int64, error) {
	rows, err := conn.QueryContext(ctx, `SELECT s.session_id, typeof(s.session_id), r.thread_id, typeof(r.thread_id), s.thread_seq, typeof(s.thread_seq), s.thread_kind, typeof(s.thread_kind) FROM conversation_thread_summary_receipt AS r JOIN session_thread AS s ON s.thread_id = r.thread_id ORDER BY r.thread_id ASC`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var count int64
	for rows.Next() {
		var sessionID, sessionType, threadID, threadType, threadKind, kindType, seqType string
		var threadSeq int64
		if err := rows.Scan(&sessionID, &sessionType, &threadID, &threadType, &threadSeq, &seqType, &threadKind, &kindType); err != nil {
			return count, err
		}
		if _, err := auditArchiveTuple(sessionID, sessionType, threadID, threadType, threadSeq, seqType, threadKind, kindType, false, audit); err != nil {
			return count, err
		}
		count++
	}
	return count, rows.Err()
}

func auditArchiveMemoryEvents(ctx context.Context, conn *sql.Conn, audit *SQLiteArchiveIdentityAudit) (int64, error) {
	rows, err := conn.QueryContext(ctx, `SELECT session_id, typeof(session_id), thread_id, typeof(thread_id), thread_seq, typeof(thread_seq), thread_kind, typeof(thread_kind) FROM l1_memory_event_archive ORDER BY id ASC`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var count int64
	for rows.Next() {
		var sessionID, sessionType, threadID, threadType, threadKind, kindType, seqType string
		var threadSeq int64
		if err := rows.Scan(&sessionID, &sessionType, &threadID, &threadType, &threadSeq, &seqType, &threadKind, &kindType); err != nil {
			return count, err
		}
		optional, err := auditArchiveTuple(sessionID, sessionType, threadID, threadType, threadSeq, seqType, threadKind, kindType, true, audit)
		if err != nil {
			return count, err
		}
		if optional {
			audit.OptionalZeroRows++
		}
		count++
	}
	return count, rows.Err()
}

func auditArchiveTuple(sessionID, sessionType, threadID, threadType string, threadSeq int64, seqType, threadKind, kindType string, allowZero bool, audit *SQLiteArchiveIdentityAudit) (bool, error) {
	if sessionType != "text" || threadType != "text" || seqType != "integer" || kindType != "text" {
		return false, errors.New("canonical archive identity storage class mismatch")
	}
	if threadID == "" || threadSeq == 0 || threadKind == "" {
		if !allowZero || threadID != "" || threadSeq != 0 || threadKind != "" {
			return false, errors.New("canonical archive identity tuple is incomplete")
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

func (receipt SQLiteArchiveMaterializationReceipt) CanonicalJSON() ([]byte, error) {
	type canonicalReceipt struct {
		SchemaVersion                     string                                   `json:"schema_version"`
		Status                            string                                   `json:"status"`
		OwnerSchemaReconciliationRequired bool                                     `json:"owner_schema_reconciliation_required"`
		InventoryReceiptSHA256            string                                   `json:"inventory_receipt_sha256"`
		MappingSHA256                     string                                   `json:"mapping_sha256"`
		TableCounts                       []SQLiteArchiveMaterializationTableCount `json:"table_counts"`
		IdentityAudit                     SQLiteArchiveIdentityAudit               `json:"identity_audit"`
	}
	counts := append([]SQLiteArchiveMaterializationTableCount(nil), receipt.TableCounts...)
	sort.Slice(counts, func(left, right int) bool { return counts[left].Table < counts[right].Table })
	if counts == nil {
		counts = []SQLiteArchiveMaterializationTableCount{}
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

func (receipt SQLiteArchiveMaterializationReceipt) ComputeSHA256() (string, error) {
	encoded, err := receipt.CanonicalJSON()
	if err != nil {
		return "", err
	}
	return sha256HexJSONIdentity(encoded), nil
}

func (receipt SQLiteArchiveMaterializationReceipt) Validate() error {
	if receipt.SchemaVersion != SQLiteArchiveMaterializationReceiptSchemaVersion || receipt.Status != SQLiteArchiveMaterializationStatus {
		return errors.New("invalid archive materialization receipt schema or status")
	}
	if !receipt.OwnerSchemaReconciliationRequired {
		return errors.New("archive owner schema reconciliation requirement is missing")
	}
	if err := validateSHA256Hex(receipt.InventoryReceiptSHA256, "archive inventory receipt SHA256"); err != nil {
		return err
	}
	if err := validateSHA256Hex(receipt.MappingSHA256, "archive mapping SHA256"); err != nil {
		return err
	}
	if len(receipt.TableCounts) != len(canonicalArchiveMaterializationTables) {
		return errors.New("archive materialization table count coverage is incomplete")
	}
	seen := make(map[string]struct{}, len(receipt.TableCounts))
	for index, count := range receipt.TableCounts {
		if count.Rows < 0 || strings.TrimSpace(count.Table) == "" {
			return errors.New("invalid archive materialization table count")
		}
		if _, ok := seen[count.Table]; ok {
			return errors.New("duplicate archive materialization table count")
		}
		seen[count.Table] = struct{}{}
		if index > 0 && receipt.TableCounts[index-1].Table >= count.Table {
			return errors.New("archive materialization table counts are not sorted")
		}
	}
	for _, table := range canonicalArchiveMaterializationTables {
		if _, ok := seen[table]; !ok {
			return errors.New("archive materialization table count is missing")
		}
	}
	if receipt.IdentityAudit.CanonicalThreadRows < 0 || receipt.IdentityAudit.OptionalZeroRows < 0 || receipt.IdentityAudit.LegacyNumericRows < 0 || receipt.IdentityAudit.LegacyNumericRows != 0 {
		return errors.New("invalid archive materialization identity audit")
	}
	var totalRows int64
	for _, count := range receipt.TableCounts {
		totalRows += count.Rows
	}
	if receipt.IdentityAudit.CanonicalThreadRows+receipt.IdentityAudit.OptionalZeroRows != totalRows {
		return errors.New("archive materialization identity row counts do not reconcile")
	}
	if err := validateSHA256Hex(receipt.ReceiptSHA256, "archive materialization receipt SHA256"); err != nil {
		return err
	}
	computed, err := receipt.ComputeSHA256()
	if err != nil {
		return err
	}
	if computed != receipt.ReceiptSHA256 {
		return errors.New("archive materialization receipt SHA256 does not match canonical JSON")
	}
	return nil
}
