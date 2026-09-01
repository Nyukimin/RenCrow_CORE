package dci

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

	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"

	_ "modernc.org/sqlite"
)

const dciSchemaVersion = 2

const (
	dciTraceTraceIDIndex      = "dci_search_trace_trace_id_unique"
	dciTraceIdempotencyIndex  = "dci_search_trace_idempotency_unique"
	dciStepActionStepIndex    = "dci_search_step_action_step_unique"
	dciStepEventIDIndex       = "dci_search_step_event_id_unique"
	dciEvidenceCreatedByIndex = "dci_evidence_created_by_event_unique"
)

var dciSchemaTables = []string{
	"dci_search_trace",
	"dci_search_step",
	"dci_evidence",
	"dci_query_terms",
}

var dciSchemaColumns = map[string][]string{
	"dci_search_trace": {
		"action_id", "trace_id", "actor_attribution", "actor_kind", "actor_id", "idempotency_key",
		"started_at", "ended_at", "mode", "user_query", "corpus_scope", "status",
		"final_evidence_count", "error_message", "pack_intent", "pack_confidence", "pack_limitations",
	},
	"dci_search_step": {
		"id", "action_id", "step_no", "event_id", "event_type", "tool", "command_text",
		"file_path", "result_count", "status", "error_message", "created_at",
	},
	"dci_evidence": {
		"evidence_id", "action_id", "created_by_event_id", "source_id", "file_path", "heading",
		"line_start", "line_end", "snippet", "reason", "confidence", "created_at",
	},
	"dci_query_terms": {
		"id", "action_id", "term", "term_type", "parent_term", "created_at",
	},
}

var dciSchemaTypes = map[string]map[string]string{
	"dci_search_trace": {
		"action_id": "TEXT", "trace_id": "TEXT", "actor_attribution": "TEXT", "actor_kind": "TEXT",
		"actor_id": "TEXT", "idempotency_key": "TEXT", "started_at": "TEXT", "ended_at": "TEXT",
		"mode": "TEXT", "user_query": "TEXT", "corpus_scope": "TEXT", "status": "TEXT",
		"final_evidence_count": "INTEGER", "error_message": "TEXT", "pack_intent": "TEXT",
		"pack_confidence": "REAL", "pack_limitations": "TEXT",
	},
	"dci_search_step": {
		"id": "INTEGER", "action_id": "TEXT", "step_no": "INTEGER", "event_id": "TEXT",
		"event_type": "TEXT", "tool": "TEXT", "command_text": "TEXT", "file_path": "TEXT",
		"result_count": "INTEGER", "status": "TEXT", "error_message": "TEXT", "created_at": "TEXT",
	},
	"dci_evidence": {
		"evidence_id": "TEXT", "action_id": "TEXT", "created_by_event_id": "TEXT", "source_id": "TEXT",
		"file_path": "TEXT", "heading": "TEXT", "line_start": "INTEGER", "line_end": "INTEGER",
		"snippet": "TEXT", "reason": "TEXT", "confidence": "REAL", "created_at": "TEXT",
	},
	"dci_query_terms": {
		"id": "INTEGER", "action_id": "TEXT", "term": "TEXT", "term_type": "TEXT",
		"parent_term": "TEXT", "created_at": "TEXT",
	},
}

var dciSchemaPrimaryKeys = map[string]string{
	"dci_search_trace": "action_id",
	"dci_search_step":  "id",
	"dci_evidence":     "evidence_id",
	"dci_query_terms":  "id",
}

var dciSchemaNotNull = map[string]map[string]bool{
	"dci_search_trace": {
		"action_id": true, "trace_id": true, "actor_attribution": true, "actor_kind": true,
		"actor_id": true, "idempotency_key": true, "started_at": true, "mode": true,
		"user_query": true, "corpus_scope": true, "status": true, "final_evidence_count": true,
		"error_message": true, "pack_intent": true, "pack_confidence": true, "pack_limitations": true,
	},
	"dci_search_step": {
		"id": true, "action_id": true, "step_no": true, "event_id": true, "event_type": true,
		"tool": true, "result_count": true, "status": true, "created_at": true,
	},
	"dci_evidence": {
		"evidence_id": true, "action_id": true, "created_by_event_id": true, "file_path": true,
		"line_start": true, "line_end": true, "snippet": true, "confidence": true, "created_at": true,
	},
	"dci_query_terms": {
		"id": true, "action_id": true, "term": true, "created_at": true,
	},
}

type SQLiteStore struct {
	db *sql.DB
}

// MigrationRecord is one validated historical DCI result and the exact
// evidence creation time for each evidence row. The map is migration input
// only; it is not persisted as a second source of truth.
type MigrationRecord struct {
	Result            domaindci.SearchResult
	EvidenceCreatedAt map[modulecore.EvidenceID]time.Time
}

// syncMigrationSnapshotForMigration is kept as a narrow package-local seam so
// cleanup after a post-create failure can be verified without weakening the
// production sync operation.
var syncMigrationSnapshotForMigration = syncMigrationSnapshot

// CreateMigrationSnapshot creates a new canonical v2 DCI database containing
// validated historical results. It never overwrites an existing target and
// removes the fresh target and SQLite sidecars if any post-create operation
// fails.
func CreateMigrationSnapshot(ctx context.Context, targetPath string, records []MigrationRecord) (err error) {
	if ctx == nil {
		return fmt.Errorf("dci migration snapshot context is required")
	}
	if strings.TrimSpace(targetPath) == "" {
		return fmt.Errorf("dci migration snapshot target path is required")
	}
	if err := validateMigrationRecords(ctx, records); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateFreshMigrationTarget(targetPath); err != nil {
		return err
	}

	file, err := os.OpenFile(targetPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create dci migration snapshot: %w", err)
	}
	created := true
	fileClosed := false
	var store *SQLiteStore
	defer func() {
		if !created || err == nil {
			return
		}
		var cleanupErr error
		if store != nil {
			cleanupErr = errors.Join(cleanupErr, store.Close())
		}
		if !fileClosed {
			cleanupErr = errors.Join(cleanupErr, file.Close())
		}
		cleanupErr = errors.Join(cleanupErr, removeMigrationSnapshotArtifacts(targetPath))
		if cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("clean up failed dci migration snapshot: %w", cleanupErr))
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("set dci migration snapshot permissions: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync dci migration snapshot placeholder: %w", err)
	}
	if closeErr := file.Close(); closeErr != nil {
		return fmt.Errorf("close dci migration snapshot placeholder: %w", closeErr)
	}
	fileClosed = true

	store, err = NewSQLiteStore(targetPath)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin dci migration snapshot transaction: %w", err)
	}
	defer tx.Rollback()
	for index, record := range records {
		if err := insertSearchResultTx(ctx, tx, record.Result, record.EvidenceCreatedAt); err != nil {
			return fmt.Errorf("insert dci migration snapshot record %d: %w", index, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit dci migration snapshot: %w", err)
	}
	if err := verifyMigrationSnapshot(ctx, store.db); err != nil {
		return err
	}
	if err := store.Close(); err != nil {
		return fmt.Errorf("close dci migration snapshot: %w", err)
	}
	store = nil
	if err := syncMigrationSnapshotForMigration(targetPath); err != nil {
		return err
	}
	if err := ensureNoMigrationSnapshotSidecars(targetPath); err != nil {
		return err
	}
	created = false
	return nil
}

func validateMigrationRecords(ctx context.Context, records []MigrationRecord) error {
	seenActions := make(map[modulecore.ActionID]struct{}, len(records))
	seenTraces := make(map[modulecore.TraceID]struct{}, len(records))
	seenStepEvents := make(map[modulecore.EventID]struct{})
	seenEvidenceIDs := make(map[modulecore.EvidenceID]struct{})
	seenCreatedEvents := make(map[modulecore.EventID]struct{})
	seenEventIDs := make(map[modulecore.EventID]struct{})
	for index, record := range records {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := domaindci.ValidateStoredSearchResult(record.Result); err != nil {
			return fmt.Errorf("dci migration record %d: %w", index, err)
		}
		if record.Result.Trace.IdempotencyKey != "" {
			return fmt.Errorf("dci migration record %d idempotency_key must be empty", index)
		}
		if _, exists := seenActions[record.Result.Trace.ActionID]; exists {
			return fmt.Errorf("dci migration duplicate action_id %q", record.Result.Trace.ActionID)
		}
		seenActions[record.Result.Trace.ActionID] = struct{}{}
		if _, exists := seenTraces[record.Result.Trace.TraceID]; exists {
			return fmt.Errorf("dci migration duplicate trace_id %q", record.Result.Trace.TraceID)
		}
		seenTraces[record.Result.Trace.TraceID] = struct{}{}
		for _, step := range record.Result.Trace.Steps {
			if _, exists := seenStepEvents[step.EventID]; exists {
				return fmt.Errorf("dci migration duplicate step event_id %q", step.EventID)
			}
			seenStepEvents[step.EventID] = struct{}{}
			if _, exists := seenEventIDs[step.EventID]; exists {
				return fmt.Errorf("dci migration duplicate event_id %q", step.EventID)
			}
			seenEventIDs[step.EventID] = struct{}{}
		}
		if len(record.EvidenceCreatedAt) != len(record.Result.Pack.Evidence) {
			return fmt.Errorf("dci migration record %d evidence created_at keys must exactly match evidence", index)
		}
		for _, evidence := range record.Result.Pack.Evidence {
			createdAt, ok := record.EvidenceCreatedAt[evidence.EvidenceID]
			if !ok {
				return fmt.Errorf("dci migration evidence %q created_at is missing", evidence.EvidenceID)
			}
			utc := createdAt.UTC()
			encoded := formatTime(utc)
			if createdAt.IsZero() || utc.IsZero() || encoded == "" {
				return fmt.Errorf("dci migration evidence %q created_at must be nonzero", evidence.EvidenceID)
			}
			if _, err := time.Parse(time.RFC3339Nano, encoded); err != nil {
				return fmt.Errorf("dci migration evidence %q created_at is not UTC-encodable", evidence.EvidenceID)
			}
			if _, exists := seenEvidenceIDs[evidence.EvidenceID]; exists {
				return fmt.Errorf("dci migration duplicate evidence_id %q", evidence.EvidenceID)
			}
			seenEvidenceIDs[evidence.EvidenceID] = struct{}{}
			if _, exists := seenCreatedEvents[evidence.CreatedByEventID]; exists {
				return fmt.Errorf("dci migration duplicate created_by_event_id %q", evidence.CreatedByEventID)
			}
			seenCreatedEvents[evidence.CreatedByEventID] = struct{}{}
			if _, exists := seenEventIDs[evidence.CreatedByEventID]; exists {
				return fmt.Errorf("dci migration duplicate event_id %q", evidence.CreatedByEventID)
			}
			seenEventIDs[evidence.CreatedByEventID] = struct{}{}
		}
	}
	return nil
}

func validateFreshMigrationTarget(targetPath string) error {
	if _, err := os.Lstat(targetPath); err == nil {
		return fmt.Errorf("dci migration snapshot target already exists")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect dci migration snapshot target: %w", err)
	}
	for _, suffix := range migrationSnapshotSidecarSuffixes() {
		if _, err := os.Lstat(targetPath + suffix); err == nil {
			return fmt.Errorf("dci migration snapshot sidecar already exists")
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect dci migration snapshot sidecar: %w", err)
		}
	}
	return nil
}

func verifyMigrationSnapshot(ctx context.Context, db *sql.DB) error {
	var quickCheck string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&quickCheck); err != nil {
		return fmt.Errorf("dci migration snapshot quick_check: %w", err)
	}
	if quickCheck != "ok" {
		return fmt.Errorf("dci migration snapshot quick_check returned %q", quickCheck)
	}
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("dci migration snapshot foreign_key_check: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var table string
		var rowID, parent, foreignKeyID any
		if err := rows.Scan(&table, &rowID, &parent, &foreignKeyID); err != nil {
			return fmt.Errorf("dci migration snapshot foreign_key_check row: %w", err)
		}
		return fmt.Errorf("dci migration snapshot foreign_key_check found violation in %q", table)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("dci migration snapshot foreign_key_check: %w", err)
	}
	return nil
}

func syncMigrationSnapshot(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open dci migration snapshot for sync: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync dci migration snapshot: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close synced dci migration snapshot: %w", err)
	}
	return nil
}

func migrationSnapshotSidecarSuffixes() []string {
	return []string{"-wal", "-shm", "-journal"}
}

func ensureNoMigrationSnapshotSidecars(path string) error {
	for _, suffix := range migrationSnapshotSidecarSuffixes() {
		if _, err := os.Lstat(path + suffix); err == nil {
			return fmt.Errorf("dci migration snapshot sidecar remains")
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect dci migration snapshot sidecar: %w", err)
		}
	}
	return nil
}

func removeMigrationSnapshotArtifacts(path string) error {
	var cleanupErr error
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		cleanupErr = errors.Join(cleanupErr, err)
	}
	for _, suffix := range migrationSnapshotSidecarSuffixes() {
		if err := os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	return cleanupErr
}

func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout%3d5000&_time_format=sqlite")
	if err != nil {
		return nil, fmt.Errorf("failed to open dci sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &SQLiteStore{db: db}
	if err := store.ensureSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *SQLiteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *SQLiteStore) ensureSchema() error {
	if s == nil || s.db == nil {
		return fmt.Errorf("dci sqlite store is nil")
	}
	if _, err := s.db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		return fmt.Errorf("failed to enable dci sqlite foreign keys: %w", err)
	}
	var foreignKeys int
	if err := s.db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return fmt.Errorf("failed to read dci sqlite foreign keys: %w", err)
	}
	if foreignKeys != 1 {
		return fmt.Errorf("dci sqlite foreign keys are not enabled")
	}

	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("failed to read dci sqlite schema version: %w", err)
	}
	tables, err := listUserTables(s.db)
	if err != nil {
		return fmt.Errorf("failed to inspect dci sqlite tables: %w", err)
	}
	switch version {
	case 0:
		if len(tables) != 0 {
			return fmt.Errorf("dci sqlite nonempty schema has unknown version 0; migration is required")
		}
		if err := s.createSchemaV2(); err != nil {
			return err
		}
	case dciSchemaVersion:
		if err := validateSchemaV2(s.db, tables); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported dci sqlite schema version %d", version)
	}
	return nil
}

func (s *SQLiteStore) createSchemaV2() error {
	const schema = `
CREATE TABLE dci_search_trace (
  action_id TEXT PRIMARY KEY NOT NULL,
  trace_id TEXT NOT NULL,
  actor_attribution TEXT NOT NULL CHECK(actor_attribution IN ('authenticated', 'legacy_unattributed')),
  actor_kind TEXT NOT NULL DEFAULT '',
  actor_id TEXT NOT NULL DEFAULT '',
  idempotency_key TEXT NOT NULL DEFAULT '',
  started_at TEXT NOT NULL,
  ended_at TEXT,
  mode TEXT NOT NULL,
  user_query TEXT NOT NULL,
  corpus_scope TEXT NOT NULL,
  status TEXT NOT NULL,
  final_evidence_count INTEGER NOT NULL DEFAULT 0,
  error_message TEXT NOT NULL DEFAULT '',
  pack_intent TEXT NOT NULL DEFAULT '',
  pack_confidence REAL NOT NULL DEFAULT 0,
  pack_limitations TEXT NOT NULL DEFAULT '[]'
);

CREATE TABLE dci_search_step (
  id INTEGER PRIMARY KEY NOT NULL,
  action_id TEXT NOT NULL REFERENCES dci_search_trace(action_id) ON DELETE CASCADE,
  step_no INTEGER NOT NULL,
  event_id TEXT NOT NULL,
  event_type TEXT NOT NULL CHECK(event_type = 'dci.file.read'),
  tool TEXT NOT NULL,
  command_text TEXT,
  file_path TEXT,
  result_count INTEGER NOT NULL,
  status TEXT NOT NULL,
  error_message TEXT,
  created_at TEXT NOT NULL
);

CREATE TABLE dci_evidence (
  evidence_id TEXT PRIMARY KEY NOT NULL,
  action_id TEXT NOT NULL REFERENCES dci_search_trace(action_id) ON DELETE CASCADE,
  created_by_event_id TEXT NOT NULL,
  source_id TEXT,
  file_path TEXT NOT NULL,
  heading TEXT,
  line_start INTEGER NOT NULL,
  line_end INTEGER NOT NULL,
  snippet TEXT NOT NULL,
  reason TEXT,
  confidence REAL NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL
);

CREATE TABLE dci_query_terms (
  id INTEGER PRIMARY KEY NOT NULL,
  action_id TEXT NOT NULL REFERENCES dci_search_trace(action_id) ON DELETE CASCADE,
  term TEXT NOT NULL,
  term_type TEXT,
  parent_term TEXT,
  created_at TEXT NOT NULL
);

CREATE UNIQUE INDEX dci_search_trace_trace_id_unique
  ON dci_search_trace(trace_id);
CREATE UNIQUE INDEX dci_search_trace_idempotency_unique
  ON dci_search_trace(idempotency_key)
  WHERE idempotency_key <> '';
CREATE UNIQUE INDEX dci_search_step_action_step_unique
  ON dci_search_step(action_id, step_no);
CREATE UNIQUE INDEX dci_search_step_event_id_unique
  ON dci_search_step(event_id);
CREATE UNIQUE INDEX dci_evidence_created_by_event_unique
  ON dci_evidence(created_by_event_id);
`
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin dci sqlite schema creation: %w", err)
	}
	if _, err := tx.Exec(schema); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("failed to initialize dci sqlite schema: %w", err)
	}
	if _, err := tx.Exec("PRAGMA user_version = 2"); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("failed to set dci sqlite schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit dci sqlite schema: %w", err)
	}
	return validateSchemaV2(s.db, dciSchemaTables)
}

func listUserTables(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`
SELECT name
FROM sqlite_master
WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return nil, err
		}
		tables = append(tables, table)
	}
	return tables, rows.Err()
}

func validateSchemaV2(db *sql.DB, actualTables []string) error {
	if !sameStringSet(actualTables, dciSchemaTables) {
		return fmt.Errorf("dci sqlite schema tables do not match v2")
	}
	for table, columns := range dciSchemaColumns {
		if err := validateTableColumns(db, table, columns, dciSchemaTypes[table], dciSchemaPrimaryKeys[table], dciSchemaNotNull[table]); err != nil {
			return err
		}
	}
	if err := validateUniqueIndexes(db, "dci_search_trace", map[string]indexSpec{
		dciTraceTraceIDIndex:     {columns: []string{"trace_id"}},
		dciTraceIdempotencyIndex: {columns: []string{"idempotency_key"}, partial: true, where: "idempotency_key <> ''"},
	}); err != nil {
		return err
	}
	if err := validateUniqueIndexes(db, "dci_search_step", map[string]indexSpec{
		dciStepActionStepIndex: {columns: []string{"action_id", "step_no"}},
		dciStepEventIDIndex:    {columns: []string{"event_id"}},
	}); err != nil {
		return err
	}
	if err := validateUniqueIndexes(db, "dci_evidence", map[string]indexSpec{
		dciEvidenceCreatedByIndex: {columns: []string{"created_by_event_id"}},
	}); err != nil {
		return err
	}
	for _, table := range []string{"dci_search_step", "dci_evidence", "dci_query_terms"} {
		if err := validateActionForeignKey(db, table); err != nil {
			return err
		}
	}
	return nil
}

func sameStringSet(actual, expected []string) bool {
	if len(actual) != len(expected) {
		return false
	}
	expectedSet := make(map[string]struct{}, len(expected))
	for _, item := range expected {
		expectedSet[item] = struct{}{}
	}
	for _, item := range actual {
		if _, ok := expectedSet[item]; !ok {
			return false
		}
	}
	return true
}

func sameStringSlice(actual, expected []string) bool {
	if len(actual) != len(expected) {
		return false
	}
	for index := range expected {
		if actual[index] != expected[index] {
			return false
		}
	}
	return true
}

func validateTableColumns(db *sql.DB, table string, expected []string, expectedTypes map[string]string, primaryKey string, expectedNotNull map[string]bool) error {
	rows, err := db.Query("PRAGMA table_info('" + strings.ReplaceAll(table, "'", "''") + "')")
	if err != nil {
		return fmt.Errorf("failed to inspect dci sqlite table %s: %w", table, err)
	}
	defer rows.Close()
	actual := make(map[string]columnInfo, len(expected))
	for rows.Next() {
		var column columnInfo
		var defaultValue sql.NullString
		if err := rows.Scan(&column.cid, &column.name, &column.columnType, &column.notNull, &defaultValue, &column.primaryKey); err != nil {
			return fmt.Errorf("failed to read dci sqlite table %s: %w", table, err)
		}
		actual[column.name] = column
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("failed to iterate dci sqlite table %s: %w", table, err)
	}
	if len(actual) != len(expected) {
		return fmt.Errorf("dci sqlite table %s columns do not match v2", table)
	}
	for _, name := range expected {
		column, ok := actual[name]
		if !ok {
			return fmt.Errorf("dci sqlite table %s is missing column %s", table, name)
		}
		expectedType, ok := expectedTypes[name]
		if !ok || strings.ToUpper(strings.TrimSpace(column.columnType)) != strings.ToUpper(strings.TrimSpace(expectedType)) {
			return fmt.Errorf("dci sqlite table %s column %s has type %q, want %q", table, name, column.columnType, expectedType)
		}
		if column.cid != indexOfString(expected, name) {
			return fmt.Errorf("dci sqlite table %s column %s has unexpected order", table, name)
		}
		if expectedNotNull[name] && column.notNull != 1 && column.primaryKey != 1 {
			return fmt.Errorf("dci sqlite table %s column %s must be NOT NULL", table, name)
		}
	}
	for name, column := range actual {
		if name == primaryKey && column.primaryKey != 1 {
			return fmt.Errorf("dci sqlite table %s primary key must be %s", table, primaryKey)
		}
		if name != primaryKey && column.primaryKey != 0 {
			return fmt.Errorf("dci sqlite table %s has unexpected primary key column %s", table, name)
		}
	}
	return nil
}

func indexOfString(items []string, target string) int {
	for index, item := range items {
		if item == target {
			return index
		}
	}
	return -1
}

type columnInfo struct {
	cid        int
	name       string
	columnType string
	notNull    int
	primaryKey int
}

type indexSpec struct {
	columns []string
	partial bool
	where   string
}

type indexInfo struct {
	name    string
	unique  int
	partial int
}

func validateUniqueIndexes(db *sql.DB, table string, expected map[string]indexSpec) error {
	rows, err := db.Query("PRAGMA index_list('" + strings.ReplaceAll(table, "'", "''") + "')")
	if err != nil {
		return fmt.Errorf("failed to inspect dci sqlite indexes for %s: %w", table, err)
	}
	defer rows.Close()
	actual := make(map[string]indexInfo, len(expected))
	for rows.Next() {
		var seq int
		var index indexInfo
		var origin string
		if err := rows.Scan(&seq, &index.name, &index.unique, &origin, &index.partial); err != nil {
			return fmt.Errorf("failed to read dci sqlite indexes for %s: %w", table, err)
		}
		if strings.HasPrefix(index.name, "sqlite_autoindex_") {
			continue
		}
		actual[index.name] = index
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("failed to iterate dci sqlite indexes for %s: %w", table, err)
	}
	if len(actual) != len(expected) {
		return fmt.Errorf("dci sqlite indexes for %s do not match v2", table)
	}
	for name, spec := range expected {
		index, ok := actual[name]
		if !ok {
			return fmt.Errorf("dci sqlite table %s is missing index %s", table, name)
		}
		if index.unique != 1 || (index.partial == 1) != spec.partial {
			return fmt.Errorf("dci sqlite index %s has invalid uniqueness or partial state", name)
		}
		columns, err := sqliteIndexColumns(db, name)
		if err != nil {
			return err
		}
		if !sameStringSlice(columns, spec.columns) {
			return fmt.Errorf("dci sqlite index %s columns do not match v2", name)
		}
		if spec.where != "" {
			where, err := sqliteIndexWhere(db, name)
			if err != nil {
				return err
			}
			if normalizeSQLiteSQL(where) != normalizeSQLiteSQL(spec.where) {
				return fmt.Errorf("dci sqlite index %s predicate does not match v2", name)
			}
		}
	}
	return nil
}

func sqliteIndexWhere(db *sql.DB, name string) (string, error) {
	var definition sql.NullString
	if err := db.QueryRow("SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?", name).Scan(&definition); err != nil {
		return "", fmt.Errorf("failed to read dci sqlite index %s definition: %w", name, err)
	}
	if !definition.Valid || strings.TrimSpace(definition.String) == "" {
		return "", fmt.Errorf("dci sqlite index %s definition is missing", name)
	}
	definitionText := strings.TrimSpace(definition.String)
	whereIndex := strings.Index(strings.ToLower(definitionText), " where ")
	if whereIndex < 0 {
		return "", fmt.Errorf("dci sqlite index %s predicate is missing", name)
	}
	return strings.TrimSpace(strings.TrimSuffix(definitionText[whereIndex+len(" where "):], ";")), nil
}

func normalizeSQLiteSQL(sqlText string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(strings.TrimSuffix(sqlText, ";")))), " ")
}

func sqliteIndexColumns(db *sql.DB, name string) ([]string, error) {
	rows, err := db.Query("PRAGMA index_info('" + strings.ReplaceAll(name, "'", "''") + "')")
	if err != nil {
		return nil, fmt.Errorf("failed to inspect dci sqlite index %s: %w", name, err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var seq, cid int
		var column string
		if err := rows.Scan(&seq, &cid, &column); err != nil {
			return nil, fmt.Errorf("failed to read dci sqlite index %s: %w", name, err)
		}
		columns = append(columns, column)
	}
	return columns, rows.Err()
}

func validateActionForeignKey(db *sql.DB, table string) error {
	rows, err := db.Query("PRAGMA foreign_key_list('" + strings.ReplaceAll(table, "'", "''") + "')")
	if err != nil {
		return fmt.Errorf("failed to inspect dci sqlite foreign keys for %s: %w", table, err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id, seq int
		var referencedTable, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &referencedTable, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			return fmt.Errorf("failed to read dci sqlite foreign keys for %s: %w", table, err)
		}
		count++
		if id != 0 || seq != 0 || referencedTable != "dci_search_trace" || from != "action_id" || to != "action_id" || onUpdate != "NO ACTION" || onDelete != "CASCADE" || match != "NONE" {
			return fmt.Errorf("dci sqlite table %s has invalid action_id foreign key", table)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("failed to iterate dci sqlite foreign keys for %s: %w", table, err)
	}
	if count != 1 {
		return fmt.Errorf("dci sqlite table %s must have one action_id foreign key", table)
	}
	return nil
}

func (s *SQLiteStore) SaveSearchTrace(ctx context.Context, trace domaindci.SearchTrace) error {
	if trace.Status != "completed" || trace.FinalEvidenceCount != 0 {
		return fmt.Errorf("dci SaveSearchTrace requires a completed zero-evidence trace")
	}
	return s.SaveSearchResult(ctx, domaindci.SearchResult{
		Trace: trace,
		Pack: domaindci.EvidencePack{
			ActionID:    trace.ActionID,
			Query:       trace.UserQuery,
			CorpusScope: append([]string(nil), trace.CorpusScope...),
		},
	})
}

func (s *SQLiteStore) FindSearchTraceByActionID(ctx context.Context, actionID modulecore.ActionID) (domaindci.SearchTrace, bool, error) {
	if err := actionID.Validate(); err != nil {
		return domaindci.SearchTrace{}, false, fmt.Errorf("dci action lookup: %w", err)
	}
	trace, _, found, err := s.findStoredSearch(ctx, `WHERE action_id = ?`, string(actionID))
	return trace, found, err
}

func (s *SQLiteStore) FindSearchResultByActionID(ctx context.Context, actionID modulecore.ActionID) (domaindci.SearchResult, bool, error) {
	if err := actionID.Validate(); err != nil {
		return domaindci.SearchResult{}, false, fmt.Errorf("dci action lookup: %w", err)
	}
	trace, pack, found, err := s.findStoredSearch(ctx, `WHERE action_id = ?`, string(actionID))
	if err != nil || !found {
		return domaindci.SearchResult{}, found, err
	}
	pack.Evidence, err = s.listEvidenceExact(ctx, actionID)
	if err != nil {
		return domaindci.SearchResult{}, false, err
	}
	pack.DerivedTerms, err = s.listDerivedTermsExact(ctx, actionID, pack.Query)
	if err != nil {
		return domaindci.SearchResult{}, false, err
	}
	result := domaindci.SearchResult{Trace: trace, Pack: pack}
	if err := domaindci.ValidateStoredSearchResult(result); err != nil {
		return domaindci.SearchResult{}, false, err
	}
	return result, true, nil
}

func (s *SQLiteStore) FindSearchResultByIdempotencyKey(ctx context.Context, key string) (domaindci.SearchResult, bool, error) {
	if key == "" {
		return domaindci.SearchResult{}, false, fmt.Errorf("dci idempotency lookup key is required")
	}
	if strings.TrimSpace(key) != key {
		return domaindci.SearchResult{}, false, fmt.Errorf("dci idempotency lookup key must not have surrounding whitespace")
	}
	trace, pack, found, err := s.findStoredSearch(ctx, `WHERE idempotency_key = ?`, key)
	if err != nil || !found {
		return domaindci.SearchResult{}, found, err
	}
	pack.Evidence, err = s.listEvidenceExact(ctx, trace.ActionID)
	if err != nil {
		return domaindci.SearchResult{}, false, err
	}
	pack.DerivedTerms, err = s.listDerivedTermsExact(ctx, trace.ActionID, pack.Query)
	if err != nil {
		return domaindci.SearchResult{}, false, err
	}
	result := domaindci.SearchResult{Trace: trace, Pack: pack}
	if err := domaindci.ValidateStoredSearchResult(result); err != nil {
		return domaindci.SearchResult{}, false, err
	}
	return result, true, nil
}

func (s *SQLiteStore) FindSearchTraceByIdempotencyKey(ctx context.Context, key string) (domaindci.SearchTrace, bool, error) {
	if key == "" {
		return domaindci.SearchTrace{}, false, fmt.Errorf("dci idempotency lookup key is required")
	}
	if strings.TrimSpace(key) != key {
		return domaindci.SearchTrace{}, false, fmt.Errorf("dci idempotency lookup key must not have surrounding whitespace")
	}
	trace, _, found, err := s.findStoredSearch(ctx, `WHERE idempotency_key = ?`, key)
	return trace, found, err
}

func (s *SQLiteStore) findStoredSearch(ctx context.Context, predicate string, value string) (domaindci.SearchTrace, domaindci.EvidencePack, bool, error) {
	var actionID string
	var traceID string
	var attribution string
	var actorKind, actorID, idempotencyKey string
	var startedAt, mode, userQuery, traceScopeJSON, status, errorMessage string
	var endedAt sql.NullString
	var finalEvidenceCount int
	var packIntent, packLimitationsJSON string
	var packConfidence float64
	err := s.db.QueryRowContext(ctx, `
SELECT action_id, trace_id, actor_attribution, actor_kind, actor_id, idempotency_key,
       started_at, ended_at, mode, user_query, corpus_scope, status,
       final_evidence_count, error_message, pack_intent, pack_confidence, pack_limitations
FROM dci_search_trace `+predicate, value).Scan(
		&actionID,
		&traceID,
		&attribution,
		&actorKind,
		&actorID,
		&idempotencyKey,
		&startedAt,
		&endedAt,
		&mode,
		&userQuery,
		&traceScopeJSON,
		&status,
		&finalEvidenceCount,
		&errorMessage,
		&packIntent,
		&packConfidence,
		&packLimitationsJSON,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domaindci.SearchTrace{}, domaindci.EvidencePack{}, false, nil
	}
	if err != nil {
		return domaindci.SearchTrace{}, domaindci.EvidencePack{}, false, err
	}
	parsedActionID := modulecore.ActionID(actionID)
	if err := parsedActionID.Validate(); err != nil {
		return domaindci.SearchTrace{}, domaindci.EvidencePack{}, false, fmt.Errorf("dci stored action_id: %w", err)
	}
	parsedTraceID := modulecore.TraceID(traceID)
	if err := parsedTraceID.Validate(); err != nil {
		return domaindci.SearchTrace{}, domaindci.EvidencePack{}, false, fmt.Errorf("dci stored trace_id: %w", err)
	}
	started, err := parseRequiredStoredTime(startedAt, "started_at")
	if err != nil {
		return domaindci.SearchTrace{}, domaindci.EvidencePack{}, false, err
	}
	ended, err := parseOptionalStoredTime(endedAt.String, "ended_at")
	if err != nil {
		return domaindci.SearchTrace{}, domaindci.EvidencePack{}, false, err
	}
	traceScope, err := parseStoredStringSlice(traceScopeJSON, "corpus_scope")
	if err != nil {
		return domaindci.SearchTrace{}, domaindci.EvidencePack{}, false, err
	}
	packLimitations, err := parseStoredStringSlice(packLimitationsJSON, "pack_limitations")
	if err != nil {
		return domaindci.SearchTrace{}, domaindci.EvidencePack{}, false, err
	}
	trace := domaindci.SearchTrace{
		TraceID:            parsedTraceID,
		ActionID:           parsedActionID,
		StartedAt:          started,
		EndedAt:            ended,
		ActorAttribution:   domaindci.ActorAttribution(attribution),
		ActorKind:          actorKind,
		ActorID:            actorID,
		IdempotencyKey:     idempotencyKey,
		Mode:               mode,
		UserQuery:          userQuery,
		CorpusScope:        traceScope,
		FinalEvidenceCount: finalEvidenceCount,
		Status:             status,
		ErrorMessage:       errorMessage,
	}
	trace.Steps, err = s.listStepsExact(ctx, parsedActionID)
	if err != nil {
		return domaindci.SearchTrace{}, domaindci.EvidencePack{}, false, err
	}
	if err := domaindci.ValidateStoredSearchTrace(trace); err != nil {
		return domaindci.SearchTrace{}, domaindci.EvidencePack{}, false, err
	}
	pack := domaindci.EvidencePack{
		ActionID:    parsedActionID,
		Query:       userQuery,
		Intent:      packIntent,
		CorpusScope: append([]string(nil), traceScope...),
		Confidence:  packConfidence,
		Limitations: packLimitations,
	}
	return trace, pack, true, nil
}

func (s *SQLiteStore) SaveSearchResult(ctx context.Context, result domaindci.SearchResult) error {
	if ctx == nil {
		return fmt.Errorf("dci save context is required")
	}
	if err := domaindci.ValidateSearchResult(result); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertSearchResultTx(ctx, tx, result, nil); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func insertSearchResultTx(ctx context.Context, tx *sql.Tx, result domaindci.SearchResult, evidenceCreatedAt map[modulecore.EvidenceID]time.Time) error {
	trace := result.Trace
	pack := result.Pack
	traceScopeJSON, err := marshalStringSlice(trace.CorpusScope)
	if err != nil {
		return err
	}
	packLimitationsJSON, err := marshalStringSlice(pack.Limitations)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO dci_search_trace (
  action_id, trace_id, actor_attribution, actor_kind, actor_id, idempotency_key,
  started_at, ended_at, mode, user_query, corpus_scope, status,
  final_evidence_count, error_message, pack_intent, pack_confidence, pack_limitations
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(trace.ActionID),
		string(trace.TraceID),
		string(trace.ActorAttribution),
		trace.ActorKind,
		trace.ActorID,
		trace.IdempotencyKey,
		formatTime(trace.StartedAt),
		formatTime(trace.EndedAt),
		trace.Mode,
		trace.UserQuery,
		traceScopeJSON,
		trace.Status,
		trace.FinalEvidenceCount,
		trace.ErrorMessage,
		pack.Intent,
		pack.Confidence,
		packLimitationsJSON,
	)
	if err != nil {
		return fmt.Errorf("failed to save dci search trace: %w", err)
	}
	for _, step := range trace.Steps {
		_, err = tx.ExecContext(ctx, `
INSERT INTO dci_search_step (
  action_id, step_no, event_id, event_type, tool, command_text, file_path,
  result_count, status, error_message, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			string(trace.ActionID),
			step.StepNo,
			string(step.EventID),
			step.EventType,
			step.Tool,
			step.CommandText,
			step.FilePath,
			step.ResultCount,
			step.Status,
			step.ErrorMessage,
			formatTime(step.CreatedAt),
		)
		if err != nil {
			return fmt.Errorf("failed to save dci search step: %w", err)
		}
	}
	for _, evidence := range pack.Evidence {
		createdAt := trace.EndedAt
		if evidenceCreatedAt != nil {
			createdAt = evidenceCreatedAt[evidence.EvidenceID]
		}
		_, err = tx.ExecContext(ctx, `
INSERT INTO dci_evidence (
  evidence_id, action_id, created_by_event_id, source_id, file_path, heading,
  line_start, line_end, snippet, reason, confidence, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			string(evidence.EvidenceID),
			string(trace.ActionID),
			string(evidence.CreatedByEventID),
			evidence.SourceID,
			evidence.FilePath,
			evidence.Heading,
			evidence.LineStart,
			evidence.LineEnd,
			evidence.Snippet,
			evidence.Reason,
			evidence.Confidence,
			formatTime(createdAt),
		)
		if err != nil {
			return fmt.Errorf("failed to save dci evidence: %w", err)
		}
	}
	termsCreatedAt := trace.EndedAt
	for _, term := range pack.DerivedTerms {
		_, err = tx.ExecContext(ctx, `
INSERT INTO dci_query_terms (action_id, term, term_type, parent_term, created_at)
VALUES (?, ?, ?, ?, ?)`,
			string(trace.ActionID),
			term,
			"derived",
			pack.Query,
			formatTime(termsCreatedAt),
		)
		if err != nil {
			return fmt.Errorf("failed to save dci query term: %w", err)
		}
	}
	return nil
}

func (s *SQLiteStore) ListRecent(limit int) ([]domaindci.SearchTrace, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`
SELECT action_id
FROM dci_search_trace
ORDER BY started_at DESC, action_id DESC
LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	var actionIDs []modulecore.ActionID
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			_ = rows.Close()
			return nil, err
		}
		actionID := modulecore.ActionID(raw)
		if err := actionID.Validate(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("dci listed action_id: %w", err)
		}
		actionIDs = append(actionIDs, actionID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	traces := make([]domaindci.SearchTrace, 0, len(actionIDs))
	for _, actionID := range actionIDs {
		trace, found, err := s.FindSearchTraceByActionID(context.Background(), actionID)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("dci listed action_id %q disappeared", actionID)
		}
		traces = append(traces, trace)
	}
	return traces, nil
}

func (s *SQLiteStore) listStepsExact(ctx context.Context, actionID modulecore.ActionID) ([]domaindci.SearchStep, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT step_no, event_id, event_type, tool, command_text, file_path,
       result_count, status, error_message, created_at
FROM dci_search_step
WHERE action_id = ?
ORDER BY step_no ASC`, string(actionID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var steps []domaindci.SearchStep
	for rows.Next() {
		var step domaindci.SearchStep
		var eventID, eventType, createdAt string
		if err := rows.Scan(
			&step.StepNo,
			&eventID,
			&eventType,
			&step.Tool,
			&step.CommandText,
			&step.FilePath,
			&step.ResultCount,
			&step.Status,
			&step.ErrorMessage,
			&createdAt,
		); err != nil {
			return nil, err
		}
		step.EventID = modulecore.EventID(eventID)
		if err := step.EventID.Validate(); err != nil {
			return nil, fmt.Errorf("dci stored step event_id: %w", err)
		}
		step.EventType = eventType
		step.CreatedAt, err = parseRequiredStoredTime(createdAt, "step.created_at")
		if err != nil {
			return nil, err
		}
		steps = append(steps, step)
	}
	return steps, rows.Err()
}

func (s *SQLiteStore) listEvidenceExact(ctx context.Context, actionID modulecore.ActionID) ([]domaindci.Evidence, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT evidence_id, created_by_event_id, source_id, file_path, heading,
       line_start, line_end, snippet, reason, confidence
FROM dci_evidence
WHERE action_id = ?
ORDER BY rowid ASC`, string(actionID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var evidenceList []domaindci.Evidence
	for rows.Next() {
		var evidence domaindci.Evidence
		var evidenceID, createdByEventID string
		if err := rows.Scan(
			&evidenceID,
			&createdByEventID,
			&evidence.SourceID,
			&evidence.FilePath,
			&evidence.Heading,
			&evidence.LineStart,
			&evidence.LineEnd,
			&evidence.Snippet,
			&evidence.Reason,
			&evidence.Confidence,
		); err != nil {
			return nil, err
		}
		evidence.EvidenceID = modulecore.EvidenceID(evidenceID)
		if err := evidence.EvidenceID.Validate(); err != nil {
			return nil, fmt.Errorf("dci stored evidence evidence_id: %w", err)
		}
		evidence.CreatedByEventID = modulecore.EventID(createdByEventID)
		if err := evidence.CreatedByEventID.Validate(); err != nil {
			return nil, fmt.Errorf("dci stored evidence created_by_event_id: %w", err)
		}
		evidenceList = append(evidenceList, evidence)
	}
	return evidenceList, rows.Err()
}

func (s *SQLiteStore) listDerivedTermsExact(ctx context.Context, actionID modulecore.ActionID, query string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT term, term_type, parent_term
FROM dci_query_terms
WHERE action_id = ?
ORDER BY id ASC`, string(actionID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var terms []string
	for rows.Next() {
		var term, termType, parentTerm string
		if err := rows.Scan(&term, &termType, &parentTerm); err != nil {
			return nil, err
		}
		if termType != "derived" {
			return nil, fmt.Errorf("dci stored query term %q has invalid term_type %q", term, termType)
		}
		if parentTerm != query {
			return nil, fmt.Errorf("dci stored query term %q parent_term does not match query", term)
		}
		terms = append(terms, term)
	}
	return terms, rows.Err()
}

func marshalStringSlice(items []string) (string, error) {
	if items == nil {
		items = []string{}
	}
	b, err := json.Marshal(items)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func parseStoredStringSlice(raw, field string) ([]string, error) {
	if raw == "" {
		return nil, fmt.Errorf("dci stored %s is required", field)
	}
	var items []string
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil, fmt.Errorf("invalid dci stored %s: %w", field, err)
	}
	if items == nil {
		return nil, fmt.Errorf("dci stored %s must be an array", field)
	}
	return items, nil
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseRequiredStoredTime(raw, field string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, fmt.Errorf("dci search trace %s is required", field)
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid dci search trace %s: %w", field, err)
	}
	return t, nil
}

func parseOptionalStoredTime(raw, field string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid dci search trace %s: %w", field, err)
	}
	return t, nil
}
