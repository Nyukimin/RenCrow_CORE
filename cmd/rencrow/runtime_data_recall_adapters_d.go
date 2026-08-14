package main

import (
	"context"
	"fmt"
	"strings"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

type runtimeConversationL1UserMemoryRecallStore interface {
	ListUserMemories(context.Context, string, string, bool, int) ([]domainmemory.UserMemory, error)
	FindUserMemoryByID(context.Context, string) (domainmemory.UserMemory, bool, error)
}

func registerRuntimeDataRecallConversationL1(r *runtimeDataRecallRegistry, store runtimeConversationL1UserMemoryRecallStore) error {
	if err := registerRuntimeDataRecallConversationL1UserMemories(r, store); err != nil {
		return err
	}
	return registerRuntimeDataRecallConversationL1UserMemory(r, store)
}

func registerRuntimeDataRecallConversationL1UserMemories(r *runtimeDataRecallRegistry, store runtimeConversationL1UserMemoryRecallStore) error {
	if r == nil || store == nil {
		return fmt.Errorf("conversation l1 user memories recall unavailable")
	}
	return r.Register("conversation_l1", "user_memories", dataRecallAccessUser, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		userID, err := runtimeConversationL1UserMemoryRecallScope(ctx)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		items, err := store.ListUserMemories(ctx, userID, "", false, q.Limit)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		records := make([]map[string]any, 0, len(items))
		for _, item := range items {
			if item.UserID != userID || item.Namespace != "user:"+userID {
				continue
			}
			if strings.TrimSpace(q.Query) != "" && !dataRecallMatches(q.Query, item.ID, item.Type, item.Statement, item.State, item.Scope) {
				continue
			}
			records = append(records, runtimeConversationL1UserMemoryProjection(item))
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, records), nil
	})
}

func registerRuntimeDataRecallConversationL1UserMemory(r *runtimeDataRecallRegistry, store runtimeConversationL1UserMemoryRecallStore) error {
	if r == nil || store == nil {
		return fmt.Errorf("conversation l1 user memory recall unavailable")
	}
	return r.Register("conversation_l1", "user_memory", dataRecallAccessUser, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		userID, err := runtimeConversationL1UserMemoryRecallScope(ctx)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		item, found, err := store.FindUserMemoryByID(ctx, q.Query)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		records := []map[string]any{}
		if found && item.UserID == userID && item.Namespace == "user:"+userID {
			records = append(records, runtimeConversationL1UserMemoryProjection(item))
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, records), nil
	})
}

func runtimeConversationL1UserMemoryRecallScope(ctx context.Context) (string, error) {
	scope, found := domaintool.ToolExecutionScopeFromContext(ctx)
	if !found || scope.Validate() != nil {
		return "", fmt.Errorf("conversation l1 user memory recall scope is unavailable")
	}
	userID := strings.TrimSpace(scope.AuthenticatedUserID)
	if userID == "" {
		return "", fmt.Errorf("conversation l1 user memory recall requires authenticated user scope")
	}
	return userID, nil
}

func runtimeConversationL1UserMemoryProjection(item domainmemory.UserMemory) map[string]any {
	return map[string]any{
		"memory_id":          item.ID,
		"type":               item.Type,
		"statement":          item.Statement,
		"evidence_event_ids": append([]string(nil), item.EvidenceEventIDs...),
		"confidence":         item.Confidence,
		"sensitivity":        item.Sensitivity,
		"state":              item.State,
		"persona_scope":      item.Scope,
		"created_at":         item.CreatedAt,
		"updated_at":         item.UpdatedAt,
	}
}
