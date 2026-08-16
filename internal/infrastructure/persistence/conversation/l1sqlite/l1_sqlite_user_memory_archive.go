package l1sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

// OwnerArchiveRequest is the trusted CORE binding passed to the
// Conversation Archive adapter. The request is created by the owner route;
// callers never supply it through the public JSON body.
type OwnerArchiveRequest struct {
	RequestID   string
	UserID      string
	ActorID     string
	PayloadHash string
	MemoryID    string
	CreatedAt   time.Time
}

// OwnerArchiveStore is intentionally defined by L1SQLiteStore. The archive
// adapter may implement it without introducing an import cycle back into L1.
type OwnerArchiveStore interface {
	ArchiveUserMemoryWithReceipt(context.Context, L1MemoryEvent, OwnerArchiveRequest) (bool, error)
	FindArchiveRequestReceipt(context.Context, string, string) (OwnerArchiveRequest, bool, error)
}

// OwnerArchiveUserMemory copies one exact, active confirmed/pinned UserMemory
// event to Conversation Archive. It never mutates the L1 source row, its
// metadata, or its recall eligibility. Archive row and request receipt are
// committed atomically by the archive adapter.
func (s *L1SQLiteStore) OwnerArchiveUserMemory(ctx context.Context, requestID, ownerID, actorID, memoryID, reason string) (domainmemory.UserMemoryOwnerResult, error) {
	requestID = strings.TrimSpace(requestID)
	ownerID = strings.TrimSpace(ownerID)
	actorID = strings.TrimSpace(actorID)
	memoryID = strings.TrimSpace(memoryID)
	reason = strings.TrimSpace(reason)
	if requestID == "" || ownerID == "" || actorID == "" || memoryID == "" || reason == "" {
		return domainmemory.UserMemoryOwnerResult{}, domainmemory.ErrUserMemoryOwnerInvalid
	}
	if err := validateOwnerArchiveScope(ctx, requestID, ownerID, actorID); err != nil {
		return domainmemory.UserMemoryOwnerResult{}, err
	}
	if s == nil || s.db == nil || s.ownerArchiveStore == nil {
		return domainmemory.UserMemoryOwnerResult{}, domainmemory.ErrUserMemoryOwnerUnavailable
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domainmemory.UserMemoryOwnerResult{}, fmt.Errorf("%w: begin l1 archive validation transaction: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	rollback := func(cause error) (domainmemory.UserMemoryOwnerResult, error) {
		return domainmemory.UserMemoryOwnerResult{}, rollbackL1Tx(tx, cause)
	}
	event, found, err := findL1MemoryEventByID(ctx, tx, memoryID)
	if err != nil {
		return rollback(fmt.Errorf("%w: read owner memory: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err))
	}
	if !found {
		return rollback(domainmemory.ErrUserMemoryOwnerNotFound)
	}
	item, err := strictUserMemoryFromEvent(event)
	if err != nil {
		// Invalid or non-user-memory rows are not an owner-visible item.
		return rollback(domainmemory.ErrUserMemoryOwnerNotFound)
	}
	if item.UserID != ownerID || event.Namespace != NamespaceKindUser+":"+ownerID {
		// Do not reveal that an exact ID belongs to another owner.
		return rollback(domainmemory.ErrUserMemoryOwnerNotFound)
	}
	if !item.Active || (item.State != domainmemory.MemoryStateConfirmed && item.State != domainmemory.MemoryStatePinned) {
		return rollback(domainmemory.ErrUserMemoryOwnerConflict)
	}

	payloadHash := ownerMemoryPayloadHash(domainmemory.UserMemoryOwnerOperationArchive, ownerID, actorID, memoryID, "", "", "", reason)
	archiveRequest := OwnerArchiveRequest{
		RequestID:   requestID,
		UserID:      ownerID,
		ActorID:     actorID,
		PayloadHash: payloadHash,
		MemoryID:    memoryID,
		CreatedAt:   time.Now().UTC(),
	}
	replay, err := s.ownerArchiveStore.ArchiveUserMemoryWithReceipt(ctx, event, archiveRequest)
	if err != nil {
		return rollback(normalizeOwnerArchiveError(err))
	}
	persistedReceipt, found, err := s.ownerArchiveStore.FindArchiveRequestReceipt(ctx, ownerID, requestID)
	if err != nil {
		return rollback(fmt.Errorf("%w: read archive request receipt: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err))
	}
	if !found {
		return rollback(fmt.Errorf("%w: archive request receipt is missing after archive", domainmemory.ErrUserMemoryOwnerUnavailable))
	}
	if !ownerArchiveRequestBindingEqual(persistedReceipt, archiveRequest) {
		return rollback(domainmemory.ErrUserMemoryOwnerConflict)
	}
	if err := tx.Commit(); err != nil {
		return domainmemory.UserMemoryOwnerResult{}, fmt.Errorf("%w: commit l1 archive validation transaction: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	return newOwnerMemoryArchiveResult(*item, requestID, memoryID, persistedReceipt.CreatedAt, replay), nil
}

func validateOwnerArchiveScope(ctx context.Context, requestID, ownerID, actorID string) error {
	scope, ok := domaintool.ToolExecutionScopeFromContext(ctx)
	if !ok || scope.Validate() != nil || scope.RequestID != requestID || scope.ActorKind != domaintool.ActorKindUser ||
		scope.ActorID != actorID || scope.AuthenticatedUserID != ownerID || !scope.Allows(domaintool.DataScopeUser) {
		return domainmemory.ErrUserMemoryOwnerForbidden
	}
	return nil
}

func normalizeOwnerArchiveError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, domainmemory.ErrUserMemoryOwnerInvalid),
		errors.Is(err, domainmemory.ErrUserMemoryOwnerNotFound),
		errors.Is(err, domainmemory.ErrUserMemoryOwnerForbidden),
		errors.Is(err, domainmemory.ErrUserMemoryOwnerConflict),
		errors.Is(err, domainmemory.ErrUserMemoryOwnerUnavailable):
		return err
	default:
		return fmt.Errorf("%w: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
}

func ownerArchiveRequestBindingEqual(left, right OwnerArchiveRequest) bool {
	return left.RequestID == right.RequestID && left.UserID == right.UserID && left.ActorID == right.ActorID && left.PayloadHash == right.PayloadHash && left.MemoryID == right.MemoryID
}

func newOwnerMemoryArchiveResult(item domainmemory.UserMemory, requestID, auditReference string, completedAt time.Time, replay bool) domainmemory.UserMemoryOwnerResult {
	if completedAt.IsZero() {
		completedAt = time.Now().UTC()
	}
	return domainmemory.UserMemoryOwnerResult{
		Item: domainmemory.UserMemoryOwnerViewFromMemory(item),
		Receipt: domainmemory.UserMemoryOwnerReceipt{
			RequestID:        requestID,
			Operation:        domainmemory.UserMemoryOwnerOperationArchive,
			Status:           "completed",
			OwnerRoute:       "conversation_archive/user_memory/archive",
			PolicyRevision:   domainmemory.UserMemoryOwnerPolicyRevision,
			IdempotencyKey:   requestID,
			IdempotentReplay: replay,
			InputCount:       1,
			OutputCount:      1,
			Warnings:         []string{},
			AuditReference:   auditReference,
			CompletedAt:      completedAt.UTC(),
		},
	}
}
