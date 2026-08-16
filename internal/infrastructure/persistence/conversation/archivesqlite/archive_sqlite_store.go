package archivesqlite

import (
	"context"
	"database/sql"
	"fmt"
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
		thread_id BIGINT PRIMARY KEY,
		session_id VARCHAR NOT NULL,
		ts_start TIMESTAMP NOT NULL,
		ts_end TIMESTAMP,
		domain VARCHAR,
		summary TEXT,
		keywords TEXT,
		embedding TEXT,
		is_novel BOOLEAN,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	-- 単一カラムインデックス（互換性維持）
	CREATE INDEX IF NOT EXISTS idx_session_thread_session_id ON session_thread(session_id);
	CREATE INDEX IF NOT EXISTS idx_session_thread_domain ON session_thread(domain);
	CREATE INDEX IF NOT EXISTS idx_session_thread_ts_start ON session_thread(ts_start);

	-- 複合インデックス（パフォーマンス最適化）
	CREATE INDEX IF NOT EXISTS idx_session_thread_session_ts ON session_thread(session_id, ts_start DESC);
	CREATE INDEX IF NOT EXISTS idx_session_thread_domain_ts ON session_thread(domain, ts_start DESC);

	CREATE TABLE IF NOT EXISTS conversation_thread_summary_receipt (
		thread_id BIGINT PRIMARY KEY,
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
		thread_id BIGINT NOT NULL,
		speaker VARCHAR NOT NULL,
		message TEXT NOT NULL,
		meta_json TEXT NOT NULL,
		memory_state VARCHAR NOT NULL,
		layer VARCHAR NOT NULL,
		source VARCHAR NOT NULL,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
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
	`

	if _, err := d.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("failed to create tables: %w", err)
	}

	return nil
}
