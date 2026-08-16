package archivesqlite

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

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

type parquetReceiptRow struct {
	RequestID      string
	UserID         string
	ActorID        string
	PayloadHash    string
	ManifestSHA256 string
	RunRelPath     string
	ResultJSON     string
	CreatedAt      time.Time
}

var _ l1sqlite.OwnerParquetArchiveStore = (*ArchiveSQLiteStore)(nil)

func (d *ArchiveSQLiteStore) ExportConversationArchiveParquet(ctx context.Context, req l1sqlite.OwnerParquetExportRequest, configuredRoot string) (domainmemory.ConversationArchiveParquetExportResult, error) {
	if d == nil || d.db == nil {
		return domainmemory.ConversationArchiveParquetExportResult{}, parquetUnavailable("archive sqlite store is closed")
	}
	if err := validateParquetExportRequest(req); err != nil {
		return domainmemory.ConversationArchiveParquetExportResult{}, err
	}
	root, err := cleanParquetRoot(configuredRoot)
	if err != nil {
		return domainmemory.ConversationArchiveParquetExportResult{}, parquetUnavailable(err.Error())
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if stored, found, readErr := findParquetReceipt(ctx, d.db, req.RequestID); readErr != nil {
		return domainmemory.ConversationArchiveParquetExportResult{}, parquetUnavailable("read parquet export receipt", readErr)
	} else if found {
		if !parquetReceiptBindingEqual(stored, req) {
			return domainmemory.ConversationArchiveParquetExportResult{}, domainmemory.ErrUserMemoryOwnerConflict
		}
		var replay domainmemory.ConversationArchiveParquetExportResult
		if err := json.Unmarshal([]byte(stored.ResultJSON), &replay); err != nil {
			return domainmemory.ConversationArchiveParquetExportResult{}, parquetUnavailable("decode parquet export receipt", err)
		}
		manifest, files, err := verifyStoredParquetArtifacts(root, stored, req.RequestID)
		if err != nil {
			return domainmemory.ConversationArchiveParquetExportResult{}, domainmemory.ErrUserMemoryOwnerConflict
		}
		if err := validateParquetReplayResult(replay, req, stored, manifest, files); err != nil {
			return domainmemory.ConversationArchiveParquetExportResult{}, domainmemory.ErrUserMemoryOwnerConflict
		}
		replay.Receipt.IdempotentReplay = true
		return replay, nil
	}

	snapshot, err := d.readArchiveParquetSnapshot(ctx)
	if err != nil {
		return domainmemory.ConversationArchiveParquetExportResult{}, parquetUnavailable("read archive snapshot", err)
	}
	createdAt := req.CreatedAt.UTC()
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	paths, err := prepareParquetExportPaths(root, req.RequestID)
	if err != nil {
		return domainmemory.ConversationArchiveParquetExportResult{}, parquetUnavailable("prepare parquet export paths", err)
	}
	cleanupStaging := true
	defer func() {
		if cleanupStaging {
			_ = removeDerivedEntry(paths.staging)
			_ = removeEmptyDir(paths.stagingParent)
		}
	}()

	files, totalRows, err := writeArchiveParquetSnapshot(paths.staging, snapshot)
	if err != nil {
		return domainmemory.ConversationArchiveParquetExportResult{}, parquetUnavailable("write parquet export", err)
	}
	manifest := archiveParquetManifest{
		Format:    domainmemory.ConversationArchiveParquetFormat,
		ExportID:  req.RequestID,
		CreatedAt: createdAt.Format(time.RFC3339Nano),
		TotalRows: totalRows,
		Files:     files,
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return domainmemory.ConversationArchiveParquetExportResult{}, parquetUnavailable("encode parquet manifest", err)
	}
	manifestPath := filepath.Join(paths.staging, parquetManifestName)
	if err := writePrivateFile(manifestPath, manifestBytes); err != nil {
		return domainmemory.ConversationArchiveParquetExportResult{}, parquetUnavailable("write parquet manifest", err)
	}
	manifestSHA256 := sha256Bytes(manifestBytes)
	runRelPath := filepath.ToSlash(filepath.Join(parquetRunDir, req.RequestID))
	manifestRelPath := filepath.ToSlash(filepath.Join(runRelPath, parquetManifestName))
	result := domainmemory.ConversationArchiveParquetExportResult{
		ExportID:        req.RequestID,
		CreatedAt:       createdAt,
		TotalRows:       totalRows,
		RunRelPath:      runRelPath,
		ManifestRelPath: manifestRelPath,
		ManifestSHA256:  manifestSHA256,
		Files:           manifestFilesToDomain(files),
		Receipt: domainmemory.UserMemoryOwnerReceipt{
			RequestID:        req.RequestID,
			Operation:        domainmemory.UserMemoryOwnerOperationParquetExport,
			Status:           "completed",
			OwnerRoute:       "conversation_archive/parquet/export",
			PolicyRevision:   domainmemory.ConversationArchiveParquetPolicyRevision,
			IdempotencyKey:   req.RequestID,
			IdempotentReplay: false,
			InputCount:       int(totalRows),
			OutputCount:      5,
			Warnings:         []string{},
			AuditReference:   req.RequestID,
			CompletedAt:      createdAt,
		},
	}
	if err := ensurePrivateDir(paths.runsDir); err != nil {
		return domainmemory.ConversationArchiveParquetExportResult{}, parquetUnavailable("prepare parquet runs directory", err)
	}
	if _, err := os.Lstat(paths.finalDir); err == nil {
		return domainmemory.ConversationArchiveParquetExportResult{}, domainmemory.ErrUserMemoryOwnerConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return domainmemory.ConversationArchiveParquetExportResult{}, parquetUnavailable("inspect existing parquet run", err)
	}
	if err := os.Rename(paths.staging, paths.finalDir); err != nil {
		return domainmemory.ConversationArchiveParquetExportResult{}, parquetUnavailable("finalize parquet export", err)
	}
	cleanupStaging = false
	if err := insertParquetReceipt(ctx, d.db, req, result); err != nil {
		quarantineErr := quarantineParquetRun(root, paths.finalDir, req.RequestID)
		if quarantineErr != nil {
			return domainmemory.ConversationArchiveParquetExportResult{}, parquetUnavailable("persist parquet receipt and remove derived run", errors.Join(err, quarantineErr))
		}
		return domainmemory.ConversationArchiveParquetExportResult{}, parquetUnavailable("persist parquet receipt", err)
	}
	_ = removeEmptyDir(paths.stagingParent)
	return result, nil
}

func (d *ArchiveSQLiteStore) VerifyConversationArchiveParquet(ctx context.Context, req l1sqlite.OwnerParquetVerifyRequest, configuredRoot string) (domainmemory.ConversationArchiveParquetVerifyResult, error) {
	if d == nil || d.db == nil {
		return domainmemory.ConversationArchiveParquetVerifyResult{}, parquetUnavailable("archive sqlite store is closed")
	}
	if err := validateParquetVerifyRequest(req); err != nil {
		return domainmemory.ConversationArchiveParquetVerifyResult{}, err
	}
	root, err := cleanParquetRoot(configuredRoot)
	if err != nil {
		return domainmemory.ConversationArchiveParquetVerifyResult{}, parquetUnavailable(err.Error())
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	stored, found, err := findParquetReceipt(ctx, d.db, req.TargetExportRequestID)
	if err != nil {
		return domainmemory.ConversationArchiveParquetVerifyResult{}, parquetUnavailable("read target parquet receipt", err)
	}
	if !found || stored.UserID != req.UserID {
		return domainmemory.ConversationArchiveParquetVerifyResult{}, domainmemory.ErrUserMemoryOwnerNotFound
	}
	wantRunRelPath := filepath.ToSlash(filepath.Join(parquetRunDir, req.TargetExportRequestID))
	if stored.RunRelPath != wantRunRelPath {
		return domainmemory.ConversationArchiveParquetVerifyResult{}, domainmemory.ErrUserMemoryOwnerConflict
	}
	manifest, verifiedFiles, err := verifyStoredParquetArtifacts(root, stored, req.TargetExportRequestID)
	if err != nil {
		return domainmemory.ConversationArchiveParquetVerifyResult{}, domainmemory.ErrUserMemoryOwnerConflict
	}
	createdAt, err := time.Parse(time.RFC3339Nano, manifest.CreatedAt)
	if err != nil {
		return domainmemory.ConversationArchiveParquetVerifyResult{}, domainmemory.ErrUserMemoryOwnerConflict
	}
	return domainmemory.ConversationArchiveParquetVerifyResult{
		ExportID:        manifest.ExportID,
		CreatedAt:       createdAt.UTC(),
		TotalRows:       manifest.TotalRows,
		RunRelPath:      stored.RunRelPath,
		ManifestRelPath: filepath.ToSlash(filepath.Join(stored.RunRelPath, parquetManifestName)),
		ManifestSHA256:  strings.ToLower(stored.ManifestSHA256),
		Files:           verifiedFiles,
		Receipt: domainmemory.UserMemoryOwnerReceipt{
			RequestID:      req.RequestID,
			Operation:      domainmemory.UserMemoryOwnerOperationParquetVerify,
			Status:         "completed",
			OwnerRoute:     "conversation_archive/parquet/verify",
			PolicyRevision: domainmemory.ConversationArchiveParquetPolicyRevision,
			IdempotencyKey: req.RequestID,
			InputCount:     5,
			OutputCount:    5,
			Warnings:       []string{},
			AuditReference: req.TargetExportRequestID,
			CompletedAt:    time.Now().UTC(),
		},
	}, nil
}

func validateParquetExportRequest(req l1sqlite.OwnerParquetExportRequest) error {
	if !isSafeOpaqueID(req.RequestID) || strings.TrimSpace(req.UserID) == "" || strings.TrimSpace(req.ActorID) == "" || strings.TrimSpace(req.PayloadHash) == "" {
		return domainmemory.ErrUserMemoryOwnerInvalid
	}
	return nil
}

func validateParquetVerifyRequest(req l1sqlite.OwnerParquetVerifyRequest) error {
	if !isSafeOpaqueID(req.RequestID) || !isSafeOpaqueID(req.TargetExportRequestID) || strings.TrimSpace(req.UserID) == "" || strings.TrimSpace(req.ActorID) == "" || strings.TrimSpace(req.PayloadHash) == "" {
		return domainmemory.ErrUserMemoryOwnerInvalid
	}
	return nil
}

func isSafeOpaqueID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || value == "." || value == ".." || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func insertParquetReceipt(ctx context.Context, db *sql.DB, req l1sqlite.OwnerParquetExportRequest, result domainmemory.ConversationArchiveParquetExportResult) error {
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO conversation_archive_parquet_receipt (
	request_id, user_id, actor_id, payload_hash, manifest_sha256, run_relpath, result_json, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
`, req.RequestID, req.UserID, req.ActorID, req.PayloadHash, result.ManifestSHA256, result.RunRelPath, string(resultJSON), result.CreatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

func findParquetReceipt(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}, requestID string) (parquetReceiptRow, bool, error) {
	var row parquetReceiptRow
	err := queryer.QueryRowContext(ctx, `
SELECT request_id, user_id, actor_id, payload_hash, manifest_sha256, run_relpath, result_json, created_at
FROM conversation_archive_parquet_receipt WHERE request_id = ?`, requestID).Scan(
		&row.RequestID, &row.UserID, &row.ActorID, &row.PayloadHash, &row.ManifestSHA256, &row.RunRelPath, &row.ResultJSON, &row.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return parquetReceiptRow{}, false, nil
	}
	if err != nil {
		return parquetReceiptRow{}, false, err
	}
	return row, true, nil
}

func parquetReceiptBindingEqual(row parquetReceiptRow, req l1sqlite.OwnerParquetExportRequest) bool {
	return row.RequestID == req.RequestID && row.UserID == req.UserID && row.ActorID == req.ActorID && row.PayloadHash == req.PayloadHash
}

func parquetUnavailable(message string, args ...interface{}) error {
	if len(args) > 0 {
		return fmt.Errorf("%w: %s: %v", domainmemory.ErrUserMemoryOwnerUnavailable, message, errors.Join(argsToErrors(args)...))
	}
	return fmt.Errorf("%w: %s", domainmemory.ErrUserMemoryOwnerUnavailable, message)
}

func argsToErrors(args []interface{}) []error {
	result := make([]error, 0, len(args))
	for _, arg := range args {
		if err, ok := arg.(error); ok {
			result = append(result, err)
		} else {
			result = append(result, fmt.Errorf("%v", arg))
		}
	}
	return result
}
