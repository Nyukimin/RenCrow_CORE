package durablestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	domain "github.com/Nyukimin/RenCrow_CORE/internal/domain/durablestore"
	_ "modernc.org/sqlite"
)

const (
	schemaVersion = 2
	timeFormat    = "2006-01-02T15:04:05.999999999Z07:00"
)

type SQLiteStore struct{ db *sql.DB }

func NewSQLiteStore(path string) (*SQLiteStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("durable store workflow sqlite path is required")
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("durable store workflow sqlite path must be absolute")
	}
	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("durable store workflow parent directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("durable store workflow parent is not a directory")
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout%3d5000&_time_format=sqlite")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &SQLiteStore{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *SQLiteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *SQLiteStore) migrate() error {
	if s == nil || s.db == nil {
		return fmt.Errorf("durable store workflow sqlite store is closed")
	}
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version < 0 || version > schemaVersion {
		return fmt.Errorf("durable store workflow schema version %d is unsupported (current %d)", version, schemaVersion)
	}
	if version == schemaVersion {
		return nil
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	switch version {
	case 0:
		if _, err := tx.Exec(`CREATE TABLE durable_store_workflow (
			requirement_id TEXT PRIMARY KEY,
			dedupe_key TEXT NOT NULL UNIQUE,
			status TEXT NOT NULL,
			lifecycle TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			payload TEXT NOT NULL
		)`); err != nil {
			return err
		}
		if err := createReceiptTable(tx); err != nil {
			return err
		}
	case 1:
		if err := createReceiptTable(tx); err != nil {
			return err
		}
		if err := migrateLegacyReceipts(tx); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

func createReceiptTable(tx *sql.Tx) error {
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS durable_store_workflow_receipt (
		request_id TEXT PRIMARY KEY,
		user_scope TEXT NOT NULL,
		payload_hash TEXT NOT NULL,
		requirement_id TEXT NOT NULL,
		created_at TEXT NOT NULL
	)`); err != nil {
		return err
	}
	_, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_durable_store_workflow_receipt_requirement
		ON durable_store_workflow_receipt(requirement_id)`)
	return err
}

func migrateLegacyReceipts(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT requirement_id, created_at, payload FROM durable_store_workflow ORDER BY requirement_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var rowRequirementID, rowCreatedAt, payload string
		if err := rows.Scan(&rowRequirementID, &rowCreatedAt, &payload); err != nil {
			return err
		}
		result, err := decodeWorkflowPayload(rowRequirementID, payload)
		if err != nil {
			return fmt.Errorf("migrate durable workflow %q: %w", rowRequirementID, err)
		}
		requestID := strings.TrimSpace(result.Requirement.RequestID)
		if requestID == "" {
			return fmt.Errorf("migrate durable workflow %q: request_id is required", rowRequirementID)
		}
		createdAt, err := time.Parse(timeFormat, rowCreatedAt)
		if err != nil || createdAt.IsZero() {
			return fmt.Errorf("migrate durable workflow %q: created_at is invalid", rowRequirementID)
		}
		receipt := domain.RequestReceipt{
			RequestID: requestID, UserScope: strings.TrimSpace(result.Requirement.UserScope),
			PayloadHash: domain.HashStorageRequirement(result.Requirement), RequirementID: rowRequirementID, CreatedAt: createdAt,
		}
		if err := domain.ValidateRequestReceipt(receipt); err != nil {
			return fmt.Errorf("migrate durable workflow %q receipt: %w", rowRequirementID, err)
		}
		if _, err := tx.Exec(`INSERT INTO durable_store_workflow_receipt (request_id, user_scope, payload_hash, requirement_id, created_at) VALUES (?, ?, ?, ?, ?)`, receipt.RequestID, receipt.UserScope, receipt.PayloadHash, receipt.RequirementID, receipt.CreatedAt.UTC().Format(timeFormat)); err != nil {
			return fmt.Errorf("migrate durable workflow %q receipt: %w", rowRequirementID, err)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return nil
}

// Save is retained for direct persistence callers. New Owner writes use
// SaveWithReceipt so the canonical result and request receipt share one tx.
func (s *SQLiteStore) Save(ctx context.Context, result domain.WorkflowResult) error {
	requestID := strings.TrimSpace(result.Requirement.RequestID)
	if requestID == "" {
		requestID = "legacy/" + legacyDigest(result.Requirement.RequirementID, result.Requirement.DedupeKey)
	}
	receipt := domain.RequestReceipt{
		RequestID: requestID, UserScope: strings.TrimSpace(result.Requirement.UserScope),
		PayloadHash: domain.HashStorageRequirement(result.Requirement), RequirementID: result.Requirement.RequirementID, CreatedAt: result.CreatedAt,
	}
	return s.SaveWithReceipt(ctx, &result, receipt)
}

func (s *SQLiteStore) SaveWithReceipt(ctx context.Context, result *domain.WorkflowResult, receipt domain.RequestReceipt) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("durable store workflow sqlite store is closed")
	}
	receipt.RequestID = strings.TrimSpace(receipt.RequestID)
	receipt.UserScope = strings.TrimSpace(receipt.UserScope)
	receipt.PayloadHash = strings.TrimSpace(receipt.PayloadHash)
	receipt.RequirementID = strings.TrimSpace(receipt.RequirementID)
	if err := domain.ValidateRequestReceipt(receipt); err != nil {
		return err
	}
	var payload string
	if result != nil {
		if strings.TrimSpace(result.Requirement.RequirementID) == "" || strings.TrimSpace(result.Requirement.DedupeKey) == "" {
			return fmt.Errorf("requirement_id and dedupe_key are required")
		}
		if receipt.RequirementID != result.Requirement.RequirementID {
			return fmt.Errorf("receipt requirement_id does not match workflow result")
		}
		if receipt.PayloadHash != domain.HashStorageRequirement(result.Requirement) {
			return fmt.Errorf("receipt payload_hash does not match workflow result")
		}
		payloadBytes, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			return marshalErr
		}
		payload = string(payloadBytes)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if result != nil {
		if _, err := tx.ExecContext(ctx, `INSERT INTO durable_store_workflow (requirement_id, dedupe_key, status, lifecycle, created_at, updated_at, payload)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, result.Requirement.RequirementID, result.Requirement.DedupeKey, result.Status, result.Lifecycle, result.CreatedAt.UTC().Format(timeFormat), result.UpdatedAt.UTC().Format(timeFormat), payload); err != nil {
			return err
		}
	} else {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM durable_store_workflow WHERE requirement_id = ?`, receipt.RequirementID).Scan(&exists); err != nil {
			if err == sql.ErrNoRows {
				return fmt.Errorf("receipt requirement %q is not persisted", receipt.RequirementID)
			}
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO durable_store_workflow_receipt (request_id, user_scope, payload_hash, requirement_id, created_at) VALUES (?, ?, ?, ?, ?)`, receipt.RequestID, receipt.UserScope, receipt.PayloadHash, receipt.RequirementID, receipt.CreatedAt.UTC().Format(timeFormat)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) FindByDedupeKey(ctx context.Context, key string) (*domain.WorkflowResult, error) {
	return s.findWorkflow(ctx, `SELECT requirement_id, payload FROM durable_store_workflow WHERE dedupe_key = ?`, key)
}

func (s *SQLiteStore) FindByRequirementID(ctx context.Context, requirementID string) (*domain.WorkflowResult, error) {
	return s.findWorkflow(ctx, `SELECT requirement_id, payload FROM durable_store_workflow WHERE requirement_id = ?`, requirementID)
}

func (s *SQLiteStore) FindByRequestID(ctx context.Context, requestID string) (*domain.RequestReceipt, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("durable store workflow sqlite store is closed")
	}
	var receipt domain.RequestReceipt
	var createdAt string
	err := s.db.QueryRowContext(ctx, `SELECT request_id, user_scope, payload_hash, requirement_id, created_at FROM durable_store_workflow_receipt WHERE request_id = ?`, requestID).Scan(&receipt.RequestID, &receipt.UserScope, &receipt.PayloadHash, &receipt.RequirementID, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	receipt.CreatedAt, err = time.Parse(timeFormat, createdAt)
	if err != nil {
		return nil, err
	}
	if err := domain.ValidateRequestReceipt(receipt); err != nil {
		return nil, err
	}
	return &receipt, nil
}

func (s *SQLiteStore) findWorkflow(ctx context.Context, query, key string) (*domain.WorkflowResult, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("durable store workflow sqlite store is closed")
	}
	var requirementID, payload string
	err := s.db.QueryRowContext(ctx, query, key).Scan(&requirementID, &payload)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result, err := decodeWorkflowPayload(requirementID, payload)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func decodeWorkflowPayload(rowRequirementID, payload string) (domain.WorkflowResult, error) {
	var result domain.WorkflowResult
	if err := json.Unmarshal([]byte(payload), &result); err != nil {
		return domain.WorkflowResult{}, err
	}
	if strings.TrimSpace(rowRequirementID) == "" || result.Requirement.RequirementID != rowRequirementID {
		return domain.WorkflowResult{}, fmt.Errorf("payload requirement_id does not match row")
	}
	if strings.TrimSpace(result.Requirement.DedupeKey) == "" {
		return domain.WorkflowResult{}, fmt.Errorf("payload dedupe_key is required")
	}
	return result, nil
}

func legacyDigest(requirementID, dedupeKey string) string {
	return domain.HashStorageRequirement(domain.StorageRequirement{RequirementID: requirementID, DedupeKey: dedupeKey})
}
