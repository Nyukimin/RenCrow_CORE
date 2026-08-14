package knowledgememory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	domainkm "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgememory"
	_ "modernc.org/sqlite"
)

type SQLiteStore struct {
	db *sql.DB
	mu sync.Mutex
}

// ErrKnowledgeMemoryRequestConflict means a request ID or candidate ID is
// already bound to different owner-controlled data. Candidate writes are
// insert-only and never silently replace an existing row.
var ErrKnowledgeMemoryRequestConflict = errors.New("knowledge memory request conflict")

// KnowledgeMemoryRequestReceipt is the durable owner binding for one
// knowledge-memory data.write request. Payload contents are represented only
// by their hash; model-owned raw payload and database details never leave this
// persistence boundary.
type KnowledgeMemoryRequestReceipt struct {
	RequestID   string    `json:"request_id"`
	UserID      string    `json:"user_id"`
	ActorID     string    `json:"actor_id"`
	PayloadHash string    `json:"payload_hash"`
	ItemID      string    `json:"item_id"`
	CreatedAt   time.Time `json:"created_at"`
}

func NewSQLiteStore(path string) (*SQLiteStore, error) {
	if path == "" {
		path = "workspace/logs/knowledge_memory.db"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout%3d5000&_time_format=sqlite")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &SQLiteStore{db: db}
	if err := store.migrate(); err != nil {
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

// EnsureOwnerRouteSchema creates only the durable receipt objects required by
// the owner-controlled data.write route. OpenSQLiteStoreWritable deliberately
// does not run the full migration, so runtime wiring must call this method
// after opening an existing writable database and before registering the route.
func (s *SQLiteStore) EnsureOwnerRouteSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("knowledge memory sqlite store is closed")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin knowledge memory owner schema transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS knowledge_memory_request_receipts (
			request_id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			actor_id TEXT NOT NULL,
			payload_hash TEXT NOT NULL,
			item_id TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_knowledge_memory_request_receipts_user_created
			ON knowledge_memory_request_receipts(user_id, created_at DESC)`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("ensure knowledge memory owner schema: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit knowledge memory owner schema: %w", err)
	}
	return nil
}

func (s *SQLiteStore) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS personal_archive (
			entry_id TEXT PRIMARY KEY,
			user_id TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS creative_knowledge (
			item_id TEXT PRIMARY KEY,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS knowledge_memory_request_receipts (
			request_id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			actor_id TEXT NOT NULL,
			payload_hash TEXT NOT NULL,
			item_id TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_knowledge_memory_request_receipts_user_created
			ON knowledge_memory_request_receipts(user_id, created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS news_knowledge (
			item_id TEXT PRIMARY KEY,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS daily_intake_rule (
			rule_id TEXT PRIMARY KEY,
			user_id TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS temporal_memory_marker (
			marker_id TEXT PRIMARY KEY,
			user_id TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS dream_consolidation_run (
			run_id TEXT PRIMARY KEY,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS knowledge_memory_search_documents (
			record_type TEXT NOT NULL,
			record_id TEXT NOT NULL,
			scope TEXT NOT NULL,
			user_id TEXT NOT NULL DEFAULT '',
			title TEXT NOT NULL,
			summary TEXT NOT NULL DEFAULT '',
			visibility TEXT NOT NULL,
			source_updated_at TEXT NOT NULL,
			indexed_at TEXT NOT NULL,
			content_sha256 TEXT NOT NULL,
			PRIMARY KEY (record_type, record_id)
		)`,
		`CREATE TABLE IF NOT EXISTS knowledge_memory_search_terms (
			scope TEXT NOT NULL,
			user_id TEXT NOT NULL DEFAULT '',
			token TEXT NOT NULL,
			record_type TEXT NOT NULL,
			record_id TEXT NOT NULL,
			PRIMARY KEY (scope, user_id, token, record_type, record_id),
			FOREIGN KEY (record_type, record_id)
				REFERENCES knowledge_memory_search_documents(record_type, record_id)
				ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_knowledge_memory_search_documents_lookup
			ON knowledge_memory_search_documents(scope, user_id, record_type, record_id)`,
		`CREATE INDEX IF NOT EXISTS idx_knowledge_memory_search_terms_lookup
			ON knowledge_memory_search_terms(scope, user_id, token, record_type, record_id)`,
		`CREATE TABLE IF NOT EXISTS knowledge_memory_index_cursor (
			record_type TEXT PRIMARY KEY,
			last_record_id TEXT NOT NULL DEFAULT '',
			eligible_count INTEGER NOT NULL DEFAULT 0,
			indexed_count INTEGER NOT NULL DEFAULT 0,
			state TEXT NOT NULL DEFAULT 'indexing',
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS knowledge_memory_import_manifest (
			manifest_id TEXT PRIMARY KEY,
			source_count INTEGER NOT NULL,
			imported_count INTEGER NOT NULL,
			source_hash TEXT NOT NULL,
			imported_hash TEXT NOT NULL,
			coverage_state TEXT NOT NULL,
			eligible_count INTEGER NOT NULL,
			indexed_count INTEGER NOT NULL,
			updated_at TEXT NOT NULL
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteStore) SavePersonalArchiveEntry(ctx context.Context, item domainkm.PersonalArchiveEntry) error {
	if err := domainkm.ValidatePersonalArchiveEntry(item); err != nil {
		return err
	}
	return s.save(ctx, "personal_archive", "entry_id", item.EntryID, "user_id", item.UserID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListPersonalArchiveEntries(ctx context.Context, limit int) ([]domainkm.PersonalArchiveEntry, error) {
	return listSQLiteItems[domainkm.PersonalArchiveEntry](ctx, s, "personal_archive", limit)
}

func (s *SQLiteStore) SaveCreativeKnowledgeItem(ctx context.Context, item domainkm.CreativeKnowledgeItem) error {
	if err := domainkm.ValidateCreativeKnowledgeItem(item); err != nil {
		return err
	}
	return s.saveCreativeKnowledgeItem(ctx, item)
}

// SaveCreativeCandidateWithReceipt inserts one private creative candidate and
// its request receipt in the same transaction. A matching request binding is
// an idempotent replay; every other request or item collision is a conflict.
// This method intentionally does not update the indexed search projection.
func (s *SQLiteStore) SaveCreativeCandidateWithReceipt(ctx context.Context, item domainkm.CreativeKnowledgeItem, receipt KnowledgeMemoryRequestReceipt) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("knowledge memory sqlite store is closed")
	}
	item.ItemID = strings.TrimSpace(item.ItemID)
	item.UserID = strings.TrimSpace(item.UserID)
	item.Title = strings.TrimSpace(item.Title)
	item.WorkType = strings.TrimSpace(item.WorkType)
	item.CreatorNames = trimKnowledgeMemoryStrings(item.CreatorNames)
	item.RelatedWorks = trimKnowledgeMemoryStrings(item.RelatedWorks)
	item.ContentHints = trimKnowledgeMemoryStrings(item.ContentHints)
	receipt.RequestID = strings.TrimSpace(receipt.RequestID)
	receipt.UserID = strings.TrimSpace(receipt.UserID)
	receipt.ActorID = strings.TrimSpace(receipt.ActorID)
	receipt.PayloadHash = strings.TrimSpace(receipt.PayloadHash)
	receipt.ItemID = strings.TrimSpace(receipt.ItemID)
	if receipt.CreatedAt.IsZero() {
		receipt.CreatedAt = time.Now().UTC()
	} else {
		receipt.CreatedAt = receipt.CreatedAt.UTC()
	}
	if err := domainkm.ValidateCreativeCandidate(item); err != nil {
		return false, err
	}
	if receipt.RequestID == "" || receipt.UserID == "" || receipt.ActorID == "" || receipt.PayloadHash == "" || receipt.ItemID == "" {
		return false, fmt.Errorf("knowledge memory request receipt fields are required")
	}
	if receipt.UserID != item.UserID || receipt.ItemID != item.ItemID {
		return false, fmt.Errorf("knowledge memory request receipt binding does not match candidate")
	}

	payload, err := marshalKnowledgeItem(item)
	if err != nil {
		return false, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin knowledge memory candidate transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	existingReceipt, found, err := findKnowledgeMemoryRequestReceipt(ctx, tx, receipt.RequestID, "")
	if err != nil {
		return false, err
	}
	if found {
		if !knowledgeMemoryReceiptBindingEqual(existingReceipt, receipt) {
			return false, fmt.Errorf("%w: request_id %q", ErrKnowledgeMemoryRequestConflict, receipt.RequestID)
		}
		existingItem, itemFound, err := findCreativeCandidateByID(ctx, tx, receipt.ItemID)
		if err != nil {
			return false, err
		}
		if !itemFound || existingItem.UserID != item.UserID || !creativeCandidateEqual(existingItem, item) {
			return false, fmt.Errorf("%w: request_id %q candidate binding", ErrKnowledgeMemoryRequestConflict, receipt.RequestID)
		}
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("commit knowledge memory candidate replay: %w", err)
		}
		return true, nil
	}

	if _, itemFound, err := findCreativeCandidateByID(ctx, tx, receipt.ItemID); err != nil {
		return false, err
	} else if itemFound {
		return false, fmt.Errorf("%w: item_id %q", ErrKnowledgeMemoryRequestConflict, receipt.ItemID)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO creative_knowledge (item_id, created_at, payload) VALUES (?, ?, ?)`, item.ItemID, item.CreatedAt.UTC().Format(timeFormatRFC3339Nano), payload); err != nil {
		return false, fmt.Errorf("insert creative candidate: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO knowledge_memory_request_receipts
		(request_id, user_id, actor_id, payload_hash, item_id, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		receipt.RequestID, receipt.UserID, receipt.ActorID, receipt.PayloadHash, receipt.ItemID,
		receipt.CreatedAt.Format(timeFormatRFC3339Nano)); err != nil {
		return false, fmt.Errorf("insert knowledge memory request receipt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit knowledge memory candidate: %w", err)
	}
	return false, nil
}

// FindCreativeCandidateByID performs a strict user-and-ID lookup and validates
// the stored private candidate before returning it. A candidate owned by a
// different user is indistinguishable from an absent row.
func (s *SQLiteStore) FindCreativeCandidateByID(ctx context.Context, userID, itemID string) (domainkm.CreativeKnowledgeItem, bool, error) {
	if s == nil || s.db == nil {
		return domainkm.CreativeKnowledgeItem{}, false, fmt.Errorf("knowledge memory sqlite store is closed")
	}
	userID = strings.TrimSpace(userID)
	itemID = strings.TrimSpace(itemID)
	if userID == "" || itemID == "" {
		return domainkm.CreativeKnowledgeItem{}, false, fmt.Errorf("knowledge memory candidate user_id and item_id are required")
	}
	item, found, err := findCreativeCandidateByID(ctx, s.db, itemID)
	if err != nil || !found {
		return item, found, err
	}
	if item.UserID != userID {
		return domainkm.CreativeKnowledgeItem{}, false, nil
	}
	return item, true, nil
}

// FindKnowledgeMemoryRequestReceipt performs an exact request-and-user lookup
// without exposing another user's receipt.
func (s *SQLiteStore) FindKnowledgeMemoryRequestReceipt(ctx context.Context, userID, requestID string) (KnowledgeMemoryRequestReceipt, bool, error) {
	if s == nil || s.db == nil {
		return KnowledgeMemoryRequestReceipt{}, false, fmt.Errorf("knowledge memory sqlite store is closed")
	}
	userID = strings.TrimSpace(userID)
	requestID = strings.TrimSpace(requestID)
	if userID == "" || requestID == "" {
		return KnowledgeMemoryRequestReceipt{}, false, fmt.Errorf("knowledge memory request user_id and request_id are required")
	}
	return findKnowledgeMemoryRequestReceipt(ctx, s.db, requestID, userID)
}

func (s *SQLiteStore) ListCreativeKnowledgeItems(ctx context.Context, limit int) ([]domainkm.CreativeKnowledgeItem, error) {
	return listSQLiteItems[domainkm.CreativeKnowledgeItem](ctx, s, "creative_knowledge", limit)
}

func (s *SQLiteStore) SaveNewsKnowledgeItem(ctx context.Context, item domainkm.NewsKnowledgeItem) error {
	if err := domainkm.ValidateNewsKnowledgeItem(item); err != nil {
		return err
	}
	return s.saveNewsKnowledgeItem(ctx, item)
}

func (s *SQLiteStore) ListNewsKnowledgeItems(ctx context.Context, limit int) ([]domainkm.NewsKnowledgeItem, error) {
	return listSQLiteItems[domainkm.NewsKnowledgeItem](ctx, s, "news_knowledge", limit)
}

func (s *SQLiteStore) SaveDailyIntakeRule(ctx context.Context, item domainkm.DailyIntakeRule) error {
	if err := domainkm.ValidateDailyIntakeRule(item); err != nil {
		return err
	}
	return s.save(ctx, "daily_intake_rule", "rule_id", item.RuleID, "user_id", item.UserID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListDailyIntakeRules(ctx context.Context, limit int) ([]domainkm.DailyIntakeRule, error) {
	return listSQLiteItems[domainkm.DailyIntakeRule](ctx, s, "daily_intake_rule", limit)
}

func (s *SQLiteStore) SaveTemporalMemoryMarker(ctx context.Context, item domainkm.TemporalMemoryMarker) error {
	if err := domainkm.ValidateTemporalMemoryMarker(item); err != nil {
		return err
	}
	return s.save(ctx, "temporal_memory_marker", "marker_id", item.MarkerID, "user_id", item.UserID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListTemporalMemoryMarkers(ctx context.Context, limit int) ([]domainkm.TemporalMemoryMarker, error) {
	return listSQLiteItems[domainkm.TemporalMemoryMarker](ctx, s, "temporal_memory_marker", limit)
}

func (s *SQLiteStore) SaveDreamConsolidationRun(ctx context.Context, item domainkm.DreamConsolidationRun) error {
	if err := domainkm.ValidateDreamConsolidationRun(item); err != nil {
		return err
	}
	return s.save(ctx, "dream_consolidation_run", "run_id", item.RunID, "", "", item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListDreamConsolidationRuns(ctx context.Context, limit int) ([]domainkm.DreamConsolidationRun, error) {
	return listSQLiteItems[domainkm.DreamConsolidationRun](ctx, s, "dream_consolidation_run", limit)
}

func (s *SQLiteStore) save(ctx context.Context, table string, idColumn string, id string, secondaryColumn string, secondaryValue string, createdAt string, item any) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("knowledge memory sqlite store is closed")
	}
	payload, err := json.Marshal(item)
	if err != nil {
		return err
	}
	if secondaryColumn == "" {
		query := fmt.Sprintf(`INSERT OR REPLACE INTO %s (%s, created_at, payload) VALUES (?, ?, ?)`, table, idColumn)
		_, err = s.db.ExecContext(ctx, query, id, createdAt, string(payload))
		return err
	}
	query := fmt.Sprintf(`INSERT OR REPLACE INTO %s (%s, %s, created_at, payload) VALUES (?, ?, ?, ?)`, table, idColumn, secondaryColumn)
	_, err = s.db.ExecContext(ctx, query, id, secondaryValue, createdAt, string(payload))
	return err
}

func listSQLiteItems[T any](ctx context.Context, s *SQLiteStore, table string, limit int) ([]T, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("knowledge memory sqlite store is closed")
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT payload FROM %s ORDER BY rowid DESC LIMIT ?`, table), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []T{}
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var item T
		if err := json.Unmarshal([]byte(payload), &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

type knowledgeMemoryQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func findCreativeCandidateByID(ctx context.Context, queryer knowledgeMemoryQueryer, itemID string) (domainkm.CreativeKnowledgeItem, bool, error) {
	var payload string
	err := queryer.QueryRowContext(ctx, `SELECT payload FROM creative_knowledge WHERE item_id = ?`, itemID).Scan(&payload)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domainkm.CreativeKnowledgeItem{}, false, nil
		}
		return domainkm.CreativeKnowledgeItem{}, false, fmt.Errorf("find creative candidate %q: %w", itemID, err)
	}
	var item domainkm.CreativeKnowledgeItem
	if err := json.Unmarshal([]byte(payload), &item); err != nil {
		return domainkm.CreativeKnowledgeItem{}, false, fmt.Errorf("decode stored creative candidate %q: %w", itemID, err)
	}
	if item.ItemID != itemID {
		return domainkm.CreativeKnowledgeItem{}, false, fmt.Errorf("stored creative candidate item_id mismatch")
	}
	if err := domainkm.ValidateCreativeCandidate(item); err != nil {
		return domainkm.CreativeKnowledgeItem{}, false, fmt.Errorf("stored creative candidate is invalid: %w", err)
	}
	return item, true, nil
}

func findKnowledgeMemoryRequestReceipt(ctx context.Context, queryer knowledgeMemoryQueryer, requestID, userID string) (KnowledgeMemoryRequestReceipt, bool, error) {
	query := `SELECT request_id, user_id, actor_id, payload_hash, item_id, created_at
		FROM knowledge_memory_request_receipts WHERE request_id = ?`
	args := []any{requestID}
	if userID != "" {
		query += ` AND user_id = ?`
		args = append(args, userID)
	}
	var receipt KnowledgeMemoryRequestReceipt
	var createdAt string
	if err := queryer.QueryRowContext(ctx, query, args...).Scan(
		&receipt.RequestID, &receipt.UserID, &receipt.ActorID, &receipt.PayloadHash, &receipt.ItemID, &createdAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return KnowledgeMemoryRequestReceipt{}, false, nil
		}
		return KnowledgeMemoryRequestReceipt{}, false, fmt.Errorf("find knowledge memory request receipt: %w", err)
	}
	parsed, err := time.Parse(timeFormatRFC3339Nano, createdAt)
	if err != nil {
		return KnowledgeMemoryRequestReceipt{}, false, fmt.Errorf("parse knowledge memory request receipt timestamp: %w", err)
	}
	receipt.CreatedAt = parsed.UTC()
	return receipt, true, nil
}

func knowledgeMemoryReceiptBindingEqual(left, right KnowledgeMemoryRequestReceipt) bool {
	return left.RequestID == right.RequestID && left.UserID == right.UserID && left.ActorID == right.ActorID &&
		left.PayloadHash == right.PayloadHash && left.ItemID == right.ItemID
}

func creativeCandidateEqual(left, right domainkm.CreativeKnowledgeItem) bool {
	left.CreatedAt = time.Time{}
	right.CreatedAt = time.Time{}
	return reflect.DeepEqual(left, right)
}

func trimKnowledgeMemoryStrings(values []string) []string {
	if values == nil {
		return nil
	}
	trimmed := make([]string, len(values))
	for i, value := range values {
		trimmed[i] = strings.TrimSpace(value)
	}
	return trimmed
}

const timeFormatRFC3339Nano = "2006-01-02T15:04:05.999999999Z07:00"
