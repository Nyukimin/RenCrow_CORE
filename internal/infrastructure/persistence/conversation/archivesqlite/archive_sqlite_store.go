package archivesqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	_ "modernc.org/sqlite"
)

// ArchiveSQLiteStore はSQLite（pure Go, modernc.org/sqlite）を使ったL2会話アーカイブである。
type ArchiveSQLiteStore struct {
	db *sql.DB
	mu sync.Mutex
}

var _ l1sqlite.OwnerArchiveStore = (*ArchiveSQLiteStore)(nil)
var _ l1sqlite.L1RawLifecycleArchiveStore = (*ArchiveSQLiteStore)(nil)

// ArchiveRequestReceipt binds one trusted data.write request to the exact L1
// memory event archived by CORE. It is deliberately separate from any model
// payload or generated summary.
type ArchiveRequestReceipt = l1sqlite.OwnerArchiveRequest

// ConversationArchiveRequestReceipt is the descriptive alias used by runtime
// owner adapters and callers that want the storage boundary named explicitly.
type ConversationArchiveRequestReceipt = ArchiveRequestReceipt

const (
	L1ArchiveMemory    = "memory"
	L1ArchiveNews      = "news"
	L1ArchiveKnowledge = "knowledge"
	L1ArchiveStaging   = "staging"
)

// NewArchiveSQLiteStore は新しいArchiveSQLiteStoreを生成
func NewArchiveSQLiteStore(dbPath string) (*ArchiveSQLiteStore, error) {
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout%3d5000&_time_format=sqlite")
	if err != nil {
		return nil, fmt.Errorf("failed to open archive sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	store := &ArchiveSQLiteStore{db: db}

	// テーブル初期化
	if err := store.initTables(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize tables: %w", err)
	}

	return store, nil
}

// Close はSQLite接続を閉じる。
func (d *ArchiveSQLiteStore) Close() error {
	if d == nil || d.db == nil {
		return nil
	}
	return d.db.Close()
}

// initTables はテーブルを初期化
func (d *ArchiveSQLiteStore) initTables(ctx context.Context) error {
	schema := `
	PRAGMA journal_mode=WAL;

	CREATE TABLE IF NOT EXISTS session_thread (
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
	);

	-- 単一カラムインデックス（互換性維持）
	CREATE INDEX IF NOT EXISTS idx_session_thread_session_id ON session_thread(session_id);
	CREATE INDEX IF NOT EXISTS idx_session_thread_domain ON session_thread(domain);
	CREATE INDEX IF NOT EXISTS idx_session_thread_ts_start ON session_thread(ts_start);

	-- 複合インデックス（パフォーマンス最適化）
	CREATE INDEX IF NOT EXISTS idx_session_thread_session_ts ON session_thread(session_id, ts_start DESC);
	CREATE INDEX IF NOT EXISTS idx_session_thread_domain_ts ON session_thread(domain, ts_start DESC);

	CREATE TABLE IF NOT EXISTS conversation_thread_summary_receipt (
		thread_id TEXT PRIMARY KEY NOT NULL,
		schema_version TEXT NOT NULL,
		generation_mode TEXT NOT NULL,
		provider TEXT NOT NULL,
		failure_code TEXT NOT NULL,
		evidence_sha256 TEXT NOT NULL,
		source_turn_count INTEGER NOT NULL,
		roles_json TEXT NOT NULL,
		created_at TIMESTAMP NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_thread_summary_receipt_created
		ON conversation_thread_summary_receipt(created_at DESC);

	CREATE TABLE IF NOT EXISTS l1_memory_event_archive (
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
	);
	CREATE INDEX IF NOT EXISTS idx_l1_memory_archive_namespace_created ON l1_memory_event_archive(namespace, created_at DESC);
	CREATE INDEX IF NOT EXISTS idx_l1_memory_archive_state_created ON l1_memory_event_archive(memory_state, created_at DESC);

	CREATE TABLE IF NOT EXISTS conversation_archive_request_receipt (
		request_id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL,
		actor_id TEXT NOT NULL,
		payload_hash TEXT NOT NULL,
		memory_id TEXT NOT NULL,
		created_at TIMESTAMP NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_conversation_archive_request_user_created
		ON conversation_archive_request_receipt(user_id, created_at DESC);
	CREATE INDEX IF NOT EXISTS idx_conversation_archive_request_memory
		ON conversation_archive_request_receipt(memory_id);

	CREATE TABLE IF NOT EXISTS conversation_archive_parquet_receipt (
		request_id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL,
		actor_id TEXT NOT NULL,
		payload_hash TEXT NOT NULL,
		manifest_sha256 TEXT NOT NULL,
		run_relpath TEXT NOT NULL,
		result_json TEXT NOT NULL,
		created_at TIMESTAMP NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_conversation_archive_parquet_receipt_user_created
		ON conversation_archive_parquet_receipt(user_id, created_at DESC);

	CREATE TABLE IF NOT EXISTS conversation_lifecycle_raw_archive_receipt (
		outbox_id TEXT PRIMARY KEY,
		event_id TEXT NOT NULL UNIQUE,
		event_sha256 TEXT NOT NULL,
		created_at TIMESTAMP NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_conversation_raw_archive_receipt_event
		ON conversation_lifecycle_raw_archive_receipt(event_id);

	CREATE TABLE IF NOT EXISTS l1_news_item_archive (
		id VARCHAR PRIMARY KEY,
		staging_id VARCHAR NOT NULL,
		category VARCHAR NOT NULL,
		source_id VARCHAR NOT NULL,
		source_url TEXT NOT NULL,
		published_at TIMESTAMP,
		fetched_at TIMESTAMP NOT NULL,
		raw_text TEXT NOT NULL,
		raw_hash VARCHAR NOT NULL,
		summary_draft TEXT NOT NULL,
		keywords_json TEXT NOT NULL,
		license_note TEXT NOT NULL,
		meta_json TEXT NOT NULL,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_l1_news_archive_category_published ON l1_news_item_archive(category, published_at DESC);
	CREATE INDEX IF NOT EXISTS idx_l1_news_archive_source_published ON l1_news_item_archive(source_id, published_at DESC);

	CREATE TABLE IF NOT EXISTS l1_knowledge_item_archive (
		id VARCHAR PRIMARY KEY,
		staging_id VARCHAR NOT NULL,
		domain VARCHAR NOT NULL,
		title TEXT NOT NULL,
		source_id VARCHAR NOT NULL,
		source_url TEXT NOT NULL,
		raw_text TEXT NOT NULL,
		raw_hash VARCHAR NOT NULL,
		summary_draft TEXT NOT NULL,
		keywords_json TEXT NOT NULL,
		license_note TEXT NOT NULL,
		meta_json TEXT NOT NULL,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_l1_knowledge_archive_domain_updated ON l1_knowledge_item_archive(domain, updated_at DESC);
	CREATE TABLE IF NOT EXISTS l1_knowledge_item_fts_archive (
		id VARCHAR PRIMARY KEY,
		domain VARCHAR NOT NULL,
		title TEXT NOT NULL,
		raw_text TEXT NOT NULL,
		summary_draft TEXT NOT NULL,
		keywords_text TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_l1_knowledge_fts_archive_domain ON l1_knowledge_item_fts_archive(domain);

	CREATE TABLE IF NOT EXISTS l1_staging_item_archive (
		id VARCHAR PRIMARY KEY,
		kind VARCHAR NOT NULL,
		namespace VARCHAR NOT NULL,
		event_id VARCHAR NOT NULL,
		source_id VARCHAR NOT NULL,
		source_url TEXT NOT NULL,
		fetched_at TIMESTAMP NOT NULL,
		published_at TIMESTAMP,
		raw_text TEXT NOT NULL,
		raw_hash VARCHAR NOT NULL,
		summary_draft TEXT NOT NULL,
		keywords_json TEXT NOT NULL,
		license_note TEXT NOT NULL,
		validation_status VARCHAR NOT NULL,
		meta_json TEXT NOT NULL,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_l1_staging_archive_status_created ON l1_staging_item_archive(validation_status, created_at DESC);
	CREATE INDEX IF NOT EXISTS idx_l1_staging_archive_namespace_created ON l1_staging_item_archive(namespace, created_at DESC);
	CREATE INDEX IF NOT EXISTS idx_l1_staging_archive_namespace_event ON l1_staging_item_archive(namespace, event_id);
	`

	if _, err := d.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("failed to create tables: %w", err)
	}
	if err := d.validateThreadIdentitySchema(ctx); err != nil {
		return err
	}

	return nil
}

// validateThreadIdentitySchema checks the columns whose representation is
// part of the canonical Thread identity contract. CREATE TABLE IF NOT EXISTS
// deliberately does not alter an existing table, so this check must run at
// open and fail closed for a pre-canonical database rather than deferring the
// failure until the first write.
func (d *ArchiveSQLiteStore) validateThreadIdentitySchema(ctx context.Context) error {
	type requiredColumn struct {
		name         string
		expectedType string
	}
	required := []struct {
		table   string
		columns []requiredColumn
	}{
		{
			table: "session_thread",
			columns: []requiredColumn{
				{name: "thread_id", expectedType: "TEXT"},
				{name: "thread_seq", expectedType: "INTEGER"},
				{name: "thread_kind", expectedType: "TEXT"},
			},
		},
		{
			table:   "conversation_thread_summary_receipt",
			columns: []requiredColumn{{name: "thread_id", expectedType: "TEXT"}},
		},
		{
			table: "l1_memory_event_archive",
			columns: []requiredColumn{
				{name: "thread_id", expectedType: "TEXT"},
				{name: "thread_seq", expectedType: "INTEGER"},
				{name: "thread_kind", expectedType: "TEXT"},
			},
		},
	}
	for _, table := range required {
		rows, err := d.db.QueryContext(ctx, "PRAGMA table_info("+table.table+")")
		if err != nil {
			return fmt.Errorf("failed to inspect archive sqlite schema for %s: %w", table.table, err)
		}
		actual := make(map[string]string)
		for rows.Next() {
			var cid, notNull, primaryKey int
			var name, columnType string
			var defaultValue sql.NullString
			if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
				_ = rows.Close()
				return fmt.Errorf("failed to inspect archive sqlite schema for %s: %w", table.table, err)
			}
			actual[name] = columnType
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("failed to inspect archive sqlite schema for %s: %w", table.table, err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("failed to close archive sqlite schema inspection for %s: %w", table.table, err)
		}
		for _, column := range table.columns {
			actualType, ok := actual[column.name]
			if !ok {
				return fmt.Errorf("archive sqlite schema requires writer-stopped migration: %s is missing %s", table.table, column.name)
			}
			if !strings.EqualFold(strings.TrimSpace(actualType), column.expectedType) {
				return fmt.Errorf("archive sqlite schema requires writer-stopped migration: %s.%s has type %q, want %s", table.table, column.name, actualType, column.expectedType)
			}
		}
	}
	return nil
}
