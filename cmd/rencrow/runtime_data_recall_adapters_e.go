package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	capdomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

// registerRuntimeDataRecallToolRegistry exposes only bounded metadata and
// exact receipt reads. It never reads or executes a registered script.
func registerRuntimeDataRecallToolRegistry(r *runtimeDataRecallRegistry, runtimeToolRegistry capdomain.ToolRegistry) error {
	if r == nil || runtimeToolRegistry == nil {
		return fmt.Errorf("tool registry data recall unavailable")
	}
	owner, ok := runtimeToolRegistry.(capdomain.ToolRegistryReceiptOwner)
	if !ok || owner == nil {
		return fmt.Errorf("tool registry receipt owner unavailable")
	}
	if err := r.Register("tool_registry", "list_tools", dataRecallAccessInternal, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		entries, err := runtimeToolRegistry.ListForPlatform(ctx, q.Query)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		records := make([]map[string]any, 0, minRuntimeToolRegistryRecallLimit(len(entries), q.Limit))
		for _, entry := range entries {
			if !runtimeToolRegistryHasPlatform(entry, q.Query) {
				continue
			}
			records = append(records, runtimeToolRegistryProjection(entry))
			if len(records) >= q.Limit {
				break
			}
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, records), nil
	}); err != nil {
		return err
	}
	if err := r.Register("tool_registry", "tool", dataRecallAccessInternal, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		entry, err := runtimeToolRegistry.Get(ctx, q.Query)
		if errors.Is(err, capdomain.ErrToolRegistryEntryNotFound) {
			return newRuntimeDataRecallResult(q.Store, q.Operation, []map[string]any{}), nil
		}
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, []map[string]any{runtimeToolRegistryProjection(entry)}), nil
	}); err != nil {
		return err
	}
	return r.Register("tool_registry", "requests", dataRecallAccessInternal, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		receipt, found, err := owner.FindRequestReceipt(ctx, q.Query)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		if !found {
			return newRuntimeDataRecallResult(q.Store, q.Operation, []map[string]any{}), nil
		}
		record := map[string]any{
			"request_id":   receipt.RequestID,
			"actor_id":     receipt.ActorID,
			"payload_hash": receipt.PayloadHash,
			"tool_name":    receipt.ToolName,
			"created_at":   receipt.CreatedAt,
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, []map[string]any{record}), nil
	})
}

func runtimeToolRegistryHasPlatform(entry capdomain.ToolEntry, platform string) bool {
	platform = strings.TrimSpace(platform)
	for _, value := range entry.Platforms {
		if strings.TrimSpace(value) == platform {
			return true
		}
	}
	return false
}

func runtimeToolRegistryProjection(entry capdomain.ToolEntry) map[string]any {
	return map[string]any{
		"name":        entry.Name,
		"description": entry.Description,
		"schema_json": entry.SchemaJSON,
		"platforms":   append([]string(nil), entry.Platforms...),
		"source":      string(entry.Source),
		"created_at":  entry.CreatedAt,
		"created_by":  entry.CreatedBy,
	}
}

func minRuntimeToolRegistryRecallLimit(count, limit int) int {
	if count < limit {
		return count
	}
	return limit
}
