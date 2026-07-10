package duckdb

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// DuckDBStore はSQLite（pure Go, modernc.org/sqlite）を使った会話記憶ストア（中期記憶warm、7日保持）。
// 型名・パッケージ名は互換性維持のため DuckDB 時代のまま残している。
type DuckDBStore struct {
	db *sql.DB
}

const (
	L1ArchiveMemory    = "memory"
	L1ArchiveNews      = "news"
	L1ArchiveKnowledge = "knowledge"
	L1ArchiveStaging   = "staging"
)

// NewDuckDBStore は新しいDuckDBStoreを生成
func NewDuckDBStore(dbPath string) (*DuckDBStore, error) {
	db, err := sql.Open("sqlite", dbPath+"?_time_format=sqlite")
	if err != nil {
		return nil, fmt.Errorf("failed to open duckdb: %w", err)
	}

	store := &DuckDBStore{db: db}

	// テーブル初期化
	if err := store.initTables(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize tables: %w", err)
	}

	return store, nil
}

// Close はDuckDB接続を閉じる
func (d *DuckDBStore) Close() error {
	return d.db.Close()
}

// initTables はテーブルを初期化
func (d *DuckDBStore) initTables(ctx context.Context) error {
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
