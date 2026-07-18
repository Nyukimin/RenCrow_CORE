package toolregistry

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
	_ "modernc.org/sqlite"
)

// SQLiteToolRegistryStore はSQLite（pure Go, modernc.org/sqlite）を使った ToolRegistry 実装。
type SQLiteToolRegistryStore struct {
	db *sql.DB
}

// NewSQLiteToolRegistryStore は新しい SQLiteToolRegistryStore を作成する。
// dbPath が空の場合はインメモリ DB（":memory:"）を使用する。
func NewSQLiteToolRegistryStore(dbPath string) (*SQLiteToolRegistryStore, error) {
	if dbPath == "" {
		dbPath = ":memory:"
	}
	db, err := sql.Open("sqlite", dbPath+"?_time_format=sqlite")
	if err != nil {
		return nil, fmt.Errorf("failed to open tool registry sqlite: %w", err)
	}

	store := &SQLiteToolRegistryStore{db: db}
	if err := store.initTables(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize tool_registry table: %w", err)
	}
	return store, nil
}

// Close はデータベース接続を閉じる
func (s *SQLiteToolRegistryStore) Close() error {
	return s.db.Close()
}

// initTables は tool_registry テーブルを初期化する
func (s *SQLiteToolRegistryStore) initTables(ctx context.Context) error {
	schema := `
	PRAGMA journal_mode=WAL;
	CREATE TABLE IF NOT EXISTS tool_registry (
		name         TEXT PRIMARY KEY,
		description  TEXT NOT NULL,
		schema_json  TEXT NOT NULL,
		platforms    TEXT NOT NULL,
		source       TEXT NOT NULL,
		created_at   TIMESTAMP NOT NULL,
		created_by   TEXT NOT NULL
	);
	`
	_, err := s.db.ExecContext(ctx, schema)
	return err
}

// Register はツールを登録または更新する（name が同じ場合は上書き）
func (s *SQLiteToolRegistryStore) Register(ctx context.Context, entry capability.ToolEntry) error {
	platformsJSON, err := json.Marshal(entry.Platforms)
	if err != nil {
		return fmt.Errorf("marshal platforms: %w", err)
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now()
	}

	query := `
	INSERT INTO tool_registry (name, description, schema_json, platforms, source, created_at, created_by)
	VALUES (?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT (name) DO UPDATE SET
		description = excluded.description,
		schema_json = excluded.schema_json,
		platforms   = excluded.platforms,
		source      = excluded.source,
		created_by  = excluded.created_by
	`
	_, err = s.db.ExecContext(ctx, query,
		entry.Name,
		entry.Description,
		entry.SchemaJSON,
		string(platformsJSON),
		string(entry.Source),
		entry.CreatedAt,
		entry.CreatedBy,
	)
	if err != nil {
		return fmt.Errorf("register tool %q: %w", entry.Name, err)
	}
	return nil
}

// ListForPlatform は指定プラットフォームに対応するツールを返す
func (s *SQLiteToolRegistryStore) ListForPlatform(ctx context.Context, platform string) ([]capability.ToolEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT name, description, schema_json, platforms, source, created_at, created_by
		FROM tool_registry
		WHERE platforms LIKE ?
		ORDER BY name
	`, "%\""+platform+"\"%")
	if err != nil {
		return nil, fmt.Errorf("list tools for platform %q: %w", platform, err)
	}
	defer rows.Close()
	return scanEntries(rows)
}

// Get は名前でツールを取得する
func (s *SQLiteToolRegistryStore) Get(ctx context.Context, name string) (capability.ToolEntry, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT name, description, schema_json, platforms, source, created_at, created_by
		FROM tool_registry WHERE name = ?
	`, name)

	var e capability.ToolEntry
	var platformsJSON, source string
	var createdAt time.Time

	if err := row.Scan(
		&e.Name, &e.Description, &e.SchemaJSON,
		&platformsJSON, &source, &createdAt, &e.CreatedBy,
	); err == sql.ErrNoRows {
		return capability.ToolEntry{}, fmt.Errorf("tool %q not found", name)
	} else if err != nil {
		return capability.ToolEntry{}, fmt.Errorf("get tool %q: %w", name, err)
	}

	if err := json.Unmarshal([]byte(platformsJSON), &e.Platforms); err != nil {
		e.Platforms = []string{}
	}
	e.Source = capability.ToolSource(source)
	e.CreatedAt = createdAt
	return e, nil
}

// scanEntries は *sql.Rows から ToolEntry スライスを読み取る
func scanEntries(rows *sql.Rows) ([]capability.ToolEntry, error) {
	var entries []capability.ToolEntry
	for rows.Next() {
		var e capability.ToolEntry
		var platformsJSON, source string
		var createdAt time.Time

		if err := rows.Scan(
			&e.Name, &e.Description, &e.SchemaJSON,
			&platformsJSON, &source, &createdAt, &e.CreatedBy,
		); err != nil {
			return nil, err
		}

		if err := json.Unmarshal([]byte(platformsJSON), &e.Platforms); err != nil {
			e.Platforms = []string{}
		}
		e.Source = capability.ToolSource(source)
		e.CreatedAt = createdAt
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// コンパイル時インターフェース適合チェック
var _ capability.ToolRegistry = (*SQLiteToolRegistryStore)(nil)
