package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/archivesqlite"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

const runtimeConversationArchivePolicyRevision = "core-conversation-archive-owner/v1"

type runtimeConversationArchiveL1MemoryFinder interface {
	FindUserMemoryEventByID(context.Context, string, string) (l1sqlite.L1MemoryEvent, bool, error)
}

type runtimeConversationArchiveWriter interface {
	ArchiveUserMemoryWithReceipt(context.Context, l1sqlite.L1MemoryEvent, archivesqlite.ArchiveRequestReceipt) (bool, error)
}

type runtimeConversationArchiveUserMemoryWritePayload struct {
	MemoryID string `json:"memory_id"`
}

type runtimeConversationArchiveUserMemoryWriter struct {
	mu      sync.Mutex
	l1      runtimeConversationArchiveL1MemoryFinder
	archive runtimeConversationArchiveWriter
}

// registerRuntimeDataWriteConversationArchive installs the CORE-owned route
// that copies one confirmed or pinned L1 event into the conversation archive.
// The model supplies only the exact memory ID; identity, actor, request and
// payload receipt bindings come from the trusted ToolExecutionScope.
func registerRuntimeDataWriteConversationArchive(
	r *runtimeDataWriteRegistry,
	l1 runtimeConversationArchiveL1MemoryFinder,
	archive runtimeConversationArchiveWriter,
) error {
	if r == nil || l1 == nil || archive == nil {
		return fmt.Errorf("conversation archive data write unavailable")
	}
	writer := &runtimeConversationArchiveUserMemoryWriter{l1: l1, archive: archive}
	return r.RegisterWithContract("conversation_archive", "archive_user_memory", dataRecallAccessUser, runtimeDataWriteContract{
		RequiredPayloadFields: []string{"memory_id"},
	}, writer.write)
}

// registerRuntimeDataWriteConversationArchiveUserMemory is the explicit route
// name retained for startup code that spells out the operation owner.
func registerRuntimeDataWriteConversationArchiveUserMemory(
	r *runtimeDataWriteRegistry,
	l1 runtimeConversationArchiveL1MemoryFinder,
	archive runtimeConversationArchiveWriter,
) error {
	return registerRuntimeDataWriteConversationArchive(r, l1, archive)
}

func (w *runtimeConversationArchiveUserMemoryWriter) write(ctx context.Context, request tools.DataWriteRequest) (runtimeDataWriteOwnerResult, error) {
	if w == nil || w.l1 == nil || w.archive == nil {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("conversation archive owner is unavailable")
	}
	scope, err := runtimeDataWriteOwnerScope(ctx)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	userID := strings.TrimSpace(scope.AuthenticatedUserID)
	if userID == "" || !scope.Allows(domaintool.DataScopeUser) {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("authenticated user scope is required")
	}
	payload, err := decodeRuntimeConversationArchiveUserMemoryWritePayload(request.Payload)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	payloadHash, err := hashRuntimeConversationArchiveUserMemoryPayload(payload)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	event, found, err := w.l1.FindUserMemoryEventByID(ctx, userID, payload.MemoryID)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if !found {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("conversation archive user memory is unavailable")
	}
	if event.Namespace != "user:"+userID || (event.MemoryState != l1sqlite.MemoryStateConfirmed && event.MemoryState != l1sqlite.MemoryStatePinned) {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("conversation archive user memory is outside the authenticated confirmed scope")
	}
	replay, err := w.archive.ArchiveUserMemoryWithReceipt(ctx, event, archivesqlite.ArchiveRequestReceipt{
		RequestID: scope.RequestID, UserID: userID, ActorID: strings.TrimSpace(scope.ActorID), PayloadHash: payloadHash, MemoryID: event.ID,
	})
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	return runtimeDataWriteOwnerResult{
		SchemaVersion:    "conversation-archive-user-memory/v1",
		MigrationState:   "embedded_current",
		ValidationState:  "owner_validated",
		AuditRef:         event.ID,
		IdempotencyKey:   scope.RequestID,
		IdempotentReplay: replay,
		PolicyRevision:   runtimeConversationArchivePolicyRevision,
	}, nil
}

func decodeRuntimeConversationArchiveUserMemoryWritePayload(payload map[string]any) (runtimeConversationArchiveUserMemoryWritePayload, error) {
	if err := validateRuntimeDataWritePayloadKeys(payload, map[string]struct{}{"memory_id": {}}); err != nil {
		return runtimeConversationArchiveUserMemoryWritePayload{}, err
	}
	var decoded runtimeConversationArchiveUserMemoryWritePayload
	if err := decodeRuntimeDataWritePayload(payload, &decoded); err != nil {
		return runtimeConversationArchiveUserMemoryWritePayload{}, err
	}
	decoded.MemoryID = strings.TrimSpace(decoded.MemoryID)
	if decoded.MemoryID == "" {
		return runtimeConversationArchiveUserMemoryWritePayload{}, fmt.Errorf("memory_id is required")
	}
	return decoded, nil
}

func hashRuntimeConversationArchiveUserMemoryPayload(payload runtimeConversationArchiveUserMemoryWritePayload) (string, error) {
	encoded, err := json.Marshal(struct {
		MemoryID string `json:"memory_id"`
	}{MemoryID: strings.TrimSpace(payload.MemoryID)})
	if err != nil {
		return "", fmt.Errorf("conversation archive payload hash: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
