package l1sqlite

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

// OwnerParquetExportRequest is the trusted request binding created by the L1
// owner boundary. It contains no caller-controlled filesystem path.
type OwnerParquetExportRequest struct {
	RequestID   string
	UserID      string
	ActorID     string
	PayloadHash string
	CreatedAt   time.Time
}

type OwnerParquetVerifyRequest struct {
	RequestID             string
	UserID                string
	ActorID               string
	TargetExportRequestID string
	PayloadHash           string
	CreatedAt             time.Time
}

// OwnerParquetArchiveStore is captured by WithArchiveStore. The output root
// is supplied only from this trusted L1 wiring field; owner callers never
// provide a path.
type OwnerParquetArchiveStore interface {
	ExportConversationArchiveParquet(context.Context, OwnerParquetExportRequest, string) (domainmemory.ConversationArchiveParquetExportResult, error)
	VerifyConversationArchiveParquet(context.Context, OwnerParquetVerifyRequest, string) (domainmemory.ConversationArchiveParquetVerifyResult, error)
}

// SetParquetExportRoot is the error-returning form for explicit wiring.
func (s *L1SQLiteStore) SetParquetExportRoot(root string) error {
	if s == nil {
		return fmt.Errorf("l1 sqlite store is nil")
	}
	root = strings.TrimSpace(root)
	if err := validateParquetExportRoot(root); err != nil {
		return err
	}
	s.parquetExportRoot = filepath.Clean(root)
	return nil
}

func validateParquetExportRoot(root string) error {
	root = strings.TrimSpace(root)
	if root == "" || !filepath.IsAbs(root) {
		return fmt.Errorf("parquet export root must be a non-empty absolute path")
	}
	clean := filepath.Clean(root)
	if clean == "." || clean == string(filepath.Separator) {
		return fmt.Errorf("parquet export root is invalid")
	}
	if info, err := os.Lstat(clean); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("parquet export root is not a regular directory")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

func validateOwnerParquetScope(ctx context.Context, requestID, ownerID, actorID string) error {
	scope, ok := domaintool.ToolExecutionScopeFromContext(ctx)
	if !ok || scope.Validate() != nil || scope.RequestID != strings.TrimSpace(requestID) ||
		scope.ActorKind != domaintool.ActorKindUser || scope.ActorID != strings.TrimSpace(actorID) ||
		scope.AuthenticatedUserID != strings.TrimSpace(ownerID) || !scope.Allows(domaintool.DataScopeUser) {
		return domainmemory.ErrUserMemoryOwnerForbidden
	}
	return nil
}

func (s *L1SQLiteStore) OwnerExportConversationArchiveParquet(ctx context.Context, requestID, ownerID, actorID string) (domainmemory.ConversationArchiveParquetExportResult, error) {
	requestID = strings.TrimSpace(requestID)
	ownerID = strings.TrimSpace(ownerID)
	actorID = strings.TrimSpace(actorID)
	if requestID == "" || ownerID == "" || actorID == "" {
		return domainmemory.ConversationArchiveParquetExportResult{}, domainmemory.ErrUserMemoryOwnerInvalid
	}
	if err := validateOwnerParquetScope(ctx, requestID, ownerID, actorID); err != nil {
		return domainmemory.ConversationArchiveParquetExportResult{}, err
	}
	if s == nil || s.db == nil || s.parquetArchiveStore == nil {
		return domainmemory.ConversationArchiveParquetExportResult{}, domainmemory.ErrUserMemoryOwnerUnavailable
	}
	if err := validateParquetExportRoot(s.parquetExportRoot); err != nil {
		return domainmemory.ConversationArchiveParquetExportResult{}, fmt.Errorf("%w: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	req := OwnerParquetExportRequest{
		RequestID:   requestID,
		UserID:      ownerID,
		ActorID:     actorID,
		PayloadHash: ownerMemoryPayloadHash(domainmemory.UserMemoryOwnerOperationParquetExport, ownerID, actorID, "", "", "", "", ""),
		CreatedAt:   time.Now().UTC(),
	}
	result, err := s.parquetArchiveStore.ExportConversationArchiveParquet(ctx, req, s.parquetExportRoot)
	if err != nil {
		return domainmemory.ConversationArchiveParquetExportResult{}, normalizeParquetOwnerError(err)
	}
	return result, nil
}

func (s *L1SQLiteStore) OwnerVerifyConversationArchiveParquet(ctx context.Context, requestID, ownerID, actorID, targetExportRequestID string) (domainmemory.ConversationArchiveParquetVerifyResult, error) {
	requestID = strings.TrimSpace(requestID)
	ownerID = strings.TrimSpace(ownerID)
	actorID = strings.TrimSpace(actorID)
	targetExportRequestID = strings.TrimSpace(targetExportRequestID)
	if requestID == "" || ownerID == "" || actorID == "" || targetExportRequestID == "" {
		return domainmemory.ConversationArchiveParquetVerifyResult{}, domainmemory.ErrUserMemoryOwnerInvalid
	}
	if err := validateOwnerParquetScope(ctx, requestID, ownerID, actorID); err != nil {
		return domainmemory.ConversationArchiveParquetVerifyResult{}, err
	}
	if s == nil || s.db == nil || s.parquetArchiveStore == nil {
		return domainmemory.ConversationArchiveParquetVerifyResult{}, domainmemory.ErrUserMemoryOwnerUnavailable
	}
	if err := validateParquetExportRoot(s.parquetExportRoot); err != nil {
		return domainmemory.ConversationArchiveParquetVerifyResult{}, fmt.Errorf("%w: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	req := OwnerParquetVerifyRequest{
		RequestID:             requestID,
		UserID:                ownerID,
		ActorID:               actorID,
		TargetExportRequestID: targetExportRequestID,
		PayloadHash:           ownerMemoryPayloadHash(domainmemory.UserMemoryOwnerOperationParquetVerify, ownerID, actorID, targetExportRequestID, "", "", "", ""),
		CreatedAt:             time.Now().UTC(),
	}
	result, err := s.parquetArchiveStore.VerifyConversationArchiveParquet(ctx, req, s.parquetExportRoot)
	if err != nil {
		return domainmemory.ConversationArchiveParquetVerifyResult{}, normalizeParquetOwnerError(err)
	}
	return result, nil
}

func normalizeParquetOwnerError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, domainmemory.ErrUserMemoryOwnerInvalid):
		return err
	case errors.Is(err, domainmemory.ErrUserMemoryOwnerNotFound):
		return err
	case errors.Is(err, domainmemory.ErrUserMemoryOwnerForbidden):
		return err
	case errors.Is(err, domainmemory.ErrUserMemoryOwnerConflict):
		return err
	case errors.Is(err, domainmemory.ErrUserMemoryOwnerUnavailable):
		return err
	default:
		return fmt.Errorf("%w: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
}
