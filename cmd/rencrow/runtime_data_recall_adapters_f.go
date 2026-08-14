package main

import (
	"context"
	"fmt"
	"strings"

	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/archivesqlite"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

type runtimeConversationArchiveRecallStore interface {
	FindUserMemoryArchive(context.Context, string, string) (l1sqlite.L1MemoryEvent, bool, error)
	FindArchiveRequestReceipt(context.Context, string, string) (archivesqlite.ArchiveRequestReceipt, bool, error)
}

// registerRuntimeDataRecallConversationArchive exposes only exact, user-owned
// archive records and request receipts. It does not expose session routes or
// any model-generated summary/content field.
func registerRuntimeDataRecallConversationArchive(r *runtimeDataRecallRegistry, store runtimeConversationArchiveRecallStore) error {
	if err := registerRuntimeDataRecallConversationArchiveUserMemory(r, store); err != nil {
		return err
	}
	return registerRuntimeDataRecallConversationArchiveRequests(r, store)
}

func registerRuntimeDataRecallConversationArchiveUserMemory(r *runtimeDataRecallRegistry, store runtimeConversationArchiveRecallStore) error {
	if r == nil || store == nil {
		return fmt.Errorf("conversation archive user memory recall unavailable")
	}
	return r.Register("conversation_archive", "user_memory", dataRecallAccessUser, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		userID, err := runtimeConversationArchiveRecallScope(ctx)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		event, found, err := store.FindUserMemoryArchive(ctx, userID, q.Query)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		records := []map[string]any{}
		if found && event.Namespace == "user:"+userID && event.ID == q.Query {
			records = append(records, runtimeConversationArchiveMemoryProjection(event))
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, records), nil
	})
}

func registerRuntimeDataRecallConversationArchiveRequests(r *runtimeDataRecallRegistry, store runtimeConversationArchiveRecallStore) error {
	if r == nil || store == nil {
		return fmt.Errorf("conversation archive request recall unavailable")
	}
	return r.Register("conversation_archive", "requests", dataRecallAccessUser, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		userID, err := runtimeConversationArchiveRecallScope(ctx)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		receipt, found, err := store.FindArchiveRequestReceipt(ctx, userID, q.Query)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		records := []map[string]any{}
		if found && receipt.UserID == userID && receipt.RequestID == q.Query {
			records = append(records, map[string]any{
				"request_id": receipt.RequestID, "actor_id": receipt.ActorID, "payload_hash": receipt.PayloadHash,
				"memory_id": receipt.MemoryID, "created_at": receipt.CreatedAt,
			})
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, records), nil
	})
}

func runtimeConversationArchiveRecallScope(ctx context.Context) (string, error) {
	scope, found := domaintool.ToolExecutionScopeFromContext(ctx)
	if !found || scope.Validate() != nil || strings.TrimSpace(scope.AuthenticatedUserID) == "" || !scope.Allows(domaintool.DataScopeUser) {
		return "", fmt.Errorf("conversation archive recall requires authenticated user scope")
	}
	return strings.TrimSpace(scope.AuthenticatedUserID), nil
}

func runtimeConversationArchiveMemoryProjection(event l1sqlite.L1MemoryEvent) map[string]any {
	return map[string]any{
		"memory_id": event.ID, "message": event.Message, "memory_state": event.MemoryState,
		"layer": event.Layer, "created_at": event.CreatedAt, "updated_at": event.UpdatedAt,
	}
}
