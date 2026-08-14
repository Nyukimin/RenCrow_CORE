package toolregistry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
	_ "modernc.org/sqlite"
)

// SQLiteToolRegistryStore はSQLite（pure Go, modernc.org/sqlite）を使った ToolRegistry 実装。
type SQLiteToolRegistryStore struct {
	db *sql.DB
	mu sync.Mutex
}

var (
	// ErrToolRegistryRequestConflict indicates that a request ID was already
	// used with a different actor or payload.
	ErrToolRegistryRequestConflict = errors.New("tool registry request conflict")
	// ErrToolRegistryEntryConflict indicates that a new request attempted to
	// replace an existing ToolEntry with different content.
	ErrToolRegistryEntryConflict = errors.New("tool registry entry conflict")
	// ErrToolRegistryRequestNotFound indicates that an exact receipt is absent.
	ErrToolRegistryRequestNotFound = errors.New("tool registry request receipt not found")
)

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
	// Keep the in-process SQLite connection serialized. This also preserves
	// schema visibility for :memory: tests while receipt writes use a single
	// transaction as their atomic boundary.
	db.SetMaxOpenConns(1)

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
	CREATE TABLE IF NOT EXISTS tool_registry_request_receipts (
		request_id   TEXT PRIMARY KEY,
		actor_id     TEXT NOT NULL,
		payload_hash TEXT NOT NULL,
		tool_name    TEXT NOT NULL,
		created_at   TIMESTAMP NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_tool_registry_request_receipts_tool_name
		ON tool_registry_request_receipts(tool_name);
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

// RegisterWithReceipt is the receipt-aware Owner write path. It never
// overwrites an existing ToolEntry: an identical entry receives a semantic
// dedupe receipt, while different content is rejected. The entry and receipt
// are inserted in one SQLite transaction.
func (s *SQLiteToolRegistryStore) RegisterWithReceipt(ctx context.Context, entry capability.ToolEntry, requestID, actorID, payloadHash string) (capability.ToolRegistryRegistrationResult, error) {
	if s == nil || s.db == nil {
		return capability.ToolRegistryRegistrationResult{}, fmt.Errorf("tool registry sqlite store is closed")
	}
	requestID = strings.TrimSpace(requestID)
	actorID = strings.TrimSpace(actorID)
	payloadHash = strings.TrimSpace(payloadHash)
	entry.Name = strings.TrimSpace(entry.Name)
	if requestID == "" || actorID == "" || payloadHash == "" || entry.Name == "" {
		return capability.ToolRegistryRegistrationResult{}, fmt.Errorf("tool registry receipt fields are required")
	}

	// The Owner boundary controls timestamps. A caller-provided CreatedAt is
	// deliberately ignored so model payloads cannot forge durable time.
	entry.CreatedAt = time.Now().UTC()
	platformsJSON, err := json.Marshal(entry.Platforms)
	if err != nil {
		return capability.ToolRegistryRegistrationResult{}, fmt.Errorf("marshal tool platforms: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return capability.ToolRegistryRegistrationResult{}, fmt.Errorf("begin tool registry receipt transaction: %w", err)
	}
	defer tx.Rollback()

	var receipt capability.ToolRegistryRequestReceipt
	var receiptCreatedAt time.Time
	err = tx.QueryRowContext(ctx, `
		SELECT request_id, actor_id, payload_hash, tool_name, created_at
		FROM tool_registry_request_receipts WHERE request_id = ?
	`, requestID).Scan(
		&receipt.RequestID, &receipt.ActorID, &receipt.PayloadHash, &receipt.ToolName, &receiptCreatedAt,
	)
	if err == nil {
		receipt.CreatedAt = receiptCreatedAt
		if receipt.ActorID != actorID || receipt.PayloadHash != payloadHash || receipt.ToolName != entry.Name {
			return capability.ToolRegistryRegistrationResult{}, fmt.Errorf("%w: request_id %q", ErrToolRegistryRequestConflict, requestID)
		}
		if err := tx.Commit(); err != nil {
			return capability.ToolRegistryRegistrationResult{}, fmt.Errorf("commit tool registry request replay: %w", err)
		}
		return capability.ToolRegistryRegistrationResult{Receipt: receipt, RequestReplay: true}, nil
	}
	if err != sql.ErrNoRows {
		return capability.ToolRegistryRegistrationResult{}, fmt.Errorf("read tool registry request receipt: %w", err)
	}

	rowEntry, found, err := scanToolEntry(tx.QueryRowContext(ctx, `
		SELECT name, description, schema_json, platforms, source, created_at, created_by
		FROM tool_registry WHERE name = ?
	`, entry.Name))
	if err != nil {
		return capability.ToolRegistryRegistrationResult{}, fmt.Errorf("read existing tool %q: %w", entry.Name, err)
	}
	semanticDedupe := false
	if found {
		if !toolEntriesEquivalent(rowEntry, entry) {
			return capability.ToolRegistryRegistrationResult{}, fmt.Errorf("%w: tool %q", ErrToolRegistryEntryConflict, entry.Name)
		}
		semanticDedupe = true
	} else if _, err := tx.ExecContext(ctx, `
		INSERT INTO tool_registry (name, description, schema_json, platforms, source, created_at, created_by)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, entry.Name, entry.Description, entry.SchemaJSON, string(platformsJSON), string(entry.Source), entry.CreatedAt, entry.CreatedBy); err != nil {
		return capability.ToolRegistryRegistrationResult{}, fmt.Errorf("insert tool %q: %w", entry.Name, err)
	}

	receipt = capability.ToolRegistryRequestReceipt{
		RequestID: requestID, ActorID: actorID, PayloadHash: payloadHash,
		ToolName: entry.Name, CreatedAt: entry.CreatedAt,
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO tool_registry_request_receipts (request_id, actor_id, payload_hash, tool_name, created_at)
		VALUES (?, ?, ?, ?, ?)
	`, receipt.RequestID, receipt.ActorID, receipt.PayloadHash, receipt.ToolName, receipt.CreatedAt); err != nil {
		return capability.ToolRegistryRegistrationResult{}, fmt.Errorf("insert tool registry request receipt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return capability.ToolRegistryRegistrationResult{}, fmt.Errorf("commit tool registry registration: %w", err)
	}
	return capability.ToolRegistryRegistrationResult{Receipt: receipt, SemanticDedupe: semanticDedupe}, nil
}

// FindRequestReceipt returns the exact durable receipt for requestID.
func (s *SQLiteToolRegistryStore) FindRequestReceipt(ctx context.Context, requestID string) (capability.ToolRegistryRequestReceipt, bool, error) {
	if s == nil || s.db == nil {
		return capability.ToolRegistryRequestReceipt{}, false, fmt.Errorf("tool registry sqlite store is closed")
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return capability.ToolRegistryRequestReceipt{}, false, nil
	}
	var receipt capability.ToolRegistryRequestReceipt
	var createdAt time.Time
	err := s.db.QueryRowContext(ctx, `
		SELECT request_id, actor_id, payload_hash, tool_name, created_at
		FROM tool_registry_request_receipts WHERE request_id = ?
	`, requestID).Scan(&receipt.RequestID, &receipt.ActorID, &receipt.PayloadHash, &receipt.ToolName, &createdAt)
	if err == sql.ErrNoRows {
		return capability.ToolRegistryRequestReceipt{}, false, nil
	}
	if err != nil {
		return capability.ToolRegistryRequestReceipt{}, false, fmt.Errorf("find tool registry request receipt %q: %w", requestID, err)
	}
	receipt.CreatedAt = createdAt
	return receipt, true, nil
}

// GetRequestReceipt is the strict form of FindRequestReceipt for direct
// persistence callers that want an error when the request is absent.
func (s *SQLiteToolRegistryStore) GetRequestReceipt(ctx context.Context, requestID string) (capability.ToolRegistryRequestReceipt, error) {
	receipt, found, err := s.FindRequestReceipt(ctx, requestID)
	if err != nil {
		return capability.ToolRegistryRequestReceipt{}, err
	}
	if !found {
		return capability.ToolRegistryRequestReceipt{}, fmt.Errorf("%w: %q", ErrToolRegistryRequestNotFound, strings.TrimSpace(requestID))
	}
	return receipt, nil
}

func scanToolEntry(scanner interface{ Scan(...any) error }) (capability.ToolEntry, bool, error) {
	var entry capability.ToolEntry
	var platformsJSON, source string
	var createdAt time.Time
	if err := scanner.Scan(
		&entry.Name, &entry.Description, &entry.SchemaJSON,
		&platformsJSON, &source, &createdAt, &entry.CreatedBy,
	); err == sql.ErrNoRows {
		return capability.ToolEntry{}, false, nil
	} else if err != nil {
		return capability.ToolEntry{}, false, err
	}
	if err := json.Unmarshal([]byte(platformsJSON), &entry.Platforms); err != nil {
		return capability.ToolEntry{}, false, fmt.Errorf("decode tool platforms: %w", err)
	}
	entry.Source = capability.ToolSource(source)
	entry.CreatedAt = createdAt
	return entry, true, nil
}

func toolEntriesEquivalent(left, right capability.ToolEntry) bool {
	// CreatedBy is trusted request metadata and may differ for a new semantic
	// request; it must not turn identical tool content into an overwrite.
	if left.Name != right.Name || left.Description != right.Description || left.SchemaJSON != right.SchemaJSON || left.Source != right.Source {
		return false
	}
	leftPlatforms := append([]string(nil), left.Platforms...)
	rightPlatforms := append([]string(nil), right.Platforms...)
	sort.Strings(leftPlatforms)
	sort.Strings(rightPlatforms)
	if len(leftPlatforms) != len(rightPlatforms) {
		return false
	}
	for i := range leftPlatforms {
		if strings.TrimSpace(leftPlatforms[i]) != strings.TrimSpace(rightPlatforms[i]) {
			return false
		}
	}
	return true
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
		return capability.ToolEntry{}, fmt.Errorf("%w: %q", capability.ErrToolRegistryEntryNotFound, name)
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
var _ capability.ToolRegistryReceiptOwner = (*SQLiteToolRegistryStore)(nil)
