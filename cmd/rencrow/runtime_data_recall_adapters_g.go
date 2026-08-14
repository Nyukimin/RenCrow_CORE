package main

import (
	"context"
	"fmt"
	"strings"

	appkm "github.com/Nyukimin/RenCrow_CORE/internal/application/knowledgememory"
	domainkm "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgememory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	knowledgememorypersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/knowledgememory"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

const runtimeKnowledgeMemorySearchMaxLimit = 20

type runtimeKnowledgeMemoryCandidateFinder interface {
	FindCreativeCandidateByID(context.Context, string, string) (domainkm.CreativeKnowledgeItem, bool, error)
	FindKnowledgeMemoryRequestReceipt(context.Context, string, string) (knowledgememorypersistence.KnowledgeMemoryRequestReceipt, bool, error)
}

type runtimeKnowledgeMemoryIndexedSearcher interface {
	Search(context.Context, appkm.SearchRequest) ([]appkm.SearchResult, error)
}

func registerRuntimeDataRecallKnowledgeMemory(r *runtimeDataRecallRegistry, candidates runtimeKnowledgeMemoryCandidateFinder, searcher runtimeKnowledgeMemoryIndexedSearcher) error {
	if r == nil || candidates == nil || searcher == nil {
		return fmt.Errorf("knowledge memory recall unavailable")
	}
	if err := registerRuntimeDataRecallKnowledgeMemoryCandidate(r, candidates); err != nil {
		return err
	}
	if err := registerRuntimeDataRecallKnowledgeMemoryRequests(r, candidates); err != nil {
		return err
	}
	return registerRuntimeDataRecallKnowledgeMemorySearch(r, searcher)
}

func registerRuntimeDataRecallKnowledgeMemoryCandidate(r *runtimeDataRecallRegistry, candidates runtimeKnowledgeMemoryCandidateFinder) error {
	if r == nil || candidates == nil {
		return fmt.Errorf("knowledge memory candidate recall unavailable")
	}
	return r.Register("knowledge_memory", "creative_candidate", dataRecallAccessUser, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		userID, err := runtimeKnowledgeMemoryRecallUserID(ctx)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		candidate, found, err := candidates.FindCreativeCandidateByID(ctx, userID, q.Query)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		records := []map[string]any{}
		if found {
			if candidate.ItemID != q.Query || candidate.UserID != userID {
				return runtimeDataRecallResult{}, fmt.Errorf("knowledge memory candidate identity mismatch")
			}
			if err := domainkm.ValidateCreativeCandidate(candidate); err != nil {
				return runtimeDataRecallResult{}, fmt.Errorf("stored knowledge memory candidate is invalid: %w", err)
			}
			records = append(records, runtimeKnowledgeMemoryCandidateRecord(candidate))
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, records), nil
	})
}

func registerRuntimeDataRecallKnowledgeMemoryRequests(r *runtimeDataRecallRegistry, candidates runtimeKnowledgeMemoryCandidateFinder) error {
	if r == nil || candidates == nil {
		return fmt.Errorf("knowledge memory request recall unavailable")
	}
	return r.Register("knowledge_memory", "requests", dataRecallAccessUser, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		userID, err := runtimeKnowledgeMemoryRecallUserID(ctx)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		receipt, found, err := candidates.FindKnowledgeMemoryRequestReceipt(ctx, userID, q.Query)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		records := []map[string]any{}
		if found {
			if receipt.RequestID != q.Query || receipt.UserID != userID || strings.TrimSpace(receipt.ItemID) == "" || receipt.CreatedAt.IsZero() {
				return runtimeDataRecallResult{}, fmt.Errorf("knowledge memory request receipt identity mismatch")
			}
			records = append(records, map[string]any{
				"request_id": receipt.RequestID, "user_id": receipt.UserID, "actor_id": receipt.ActorID,
				"payload_hash": receipt.PayloadHash, "item_id": receipt.ItemID, "created_at": receipt.CreatedAt,
			})
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, records), nil
	})
}

func registerRuntimeDataRecallKnowledgeMemorySearch(r *runtimeDataRecallRegistry, searcher runtimeKnowledgeMemoryIndexedSearcher) error {
	if r == nil || searcher == nil {
		return fmt.Errorf("knowledge memory indexed search unavailable")
	}
	registrations := []struct {
		operation  string
		access     dataRecallAccess
		scope      string
		recordType string
	}{
		{operation: "search_public_creative", access: dataRecallAccessPublic, scope: appkm.SearchScopePublic, recordType: "creative_knowledge"},
		{operation: "search_public_news", access: dataRecallAccessPublic, scope: appkm.SearchScopePublic, recordType: "news_knowledge"},
		{operation: "search_user_creative", access: dataRecallAccessUser, scope: appkm.SearchScopeUser, recordType: "creative_knowledge"},
		{operation: "search_user_news", access: dataRecallAccessUser, scope: appkm.SearchScopeUser, recordType: "news_knowledge"},
	}
	for _, registration := range registrations {
		registration := registration
		if err := r.Register("knowledge_memory", registration.operation, registration.access, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
			userID := ""
			if registration.scope == appkm.SearchScopeUser {
				var err error
				userID, err = runtimeKnowledgeMemoryRecallUserID(ctx)
				if err != nil {
					return runtimeDataRecallResult{}, err
				}
			}
			limit := q.Limit
			if limit > runtimeKnowledgeMemorySearchMaxLimit {
				limit = runtimeKnowledgeMemorySearchMaxLimit
			}
			results, err := searcher.Search(ctx, appkm.SearchRequest{
				Scope:      appkm.SearchScope{Scope: registration.scope, UserID: userID},
				Query:      q.Query,
				RecordType: registration.recordType,
				Limit:      limit,
			})
			if err != nil {
				return runtimeDataRecallResult{}, err
			}
			records, err := runtimeKnowledgeMemorySearchRecords(results, registration.scope, userID, registration.recordType, limit)
			if err != nil {
				return runtimeDataRecallResult{}, err
			}
			return newRuntimeDataRecallResult(q.Store, q.Operation, records), nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func runtimeKnowledgeMemoryRecallUserID(ctx context.Context) (string, error) {
	scope, found := domaintool.ToolExecutionScopeFromContext(ctx)
	if !found || scope.Validate() != nil || scope.ActorKind != domaintool.ActorKindAgent {
		return "", fmt.Errorf("trusted user recall scope is invalid")
	}
	userID := strings.TrimSpace(scope.AuthenticatedUserID)
	if userID == "" || !scope.Allows(domaintool.DataScopeUser) {
		return "", fmt.Errorf("authenticated user recall scope is required")
	}
	return userID, nil
}

func runtimeKnowledgeMemoryCandidateRecord(candidate domainkm.CreativeKnowledgeItem) map[string]any {
	return map[string]any{
		"item_id": candidate.ItemID, "user_id": candidate.UserID, "title": candidate.Title,
		"creator_names": candidate.CreatorNames, "work_type": candidate.WorkType,
		"related_works": candidate.RelatedWorks, "content_hints": candidate.ContentHints,
		"status": candidate.Status, "visibility": candidate.Visibility, "created_at": candidate.CreatedAt,
	}
}

func runtimeKnowledgeMemorySearchRecords(results []appkm.SearchResult, expectedScope, expectedUserID, expectedRecordType string, limit int) ([]map[string]any, error) {
	if limit < 1 || limit > runtimeKnowledgeMemorySearchMaxLimit {
		return nil, fmt.Errorf("knowledge memory search limit is invalid")
	}
	if len(results) > limit {
		return nil, fmt.Errorf("knowledge memory search exceeded requested limit")
	}
	records := make([]map[string]any, 0, len(results))
	for _, result := range results {
		if result.RecordType != expectedRecordType || result.Scope != expectedScope || result.UserID != expectedUserID {
			return nil, fmt.Errorf("knowledge memory search scope mismatch")
		}
		if expectedScope == appkm.SearchScopePublic && result.Visibility != "public" {
			return nil, fmt.Errorf("knowledge memory public search visibility mismatch")
		}
		records = append(records, map[string]any{
			"record_type": result.RecordType, "record_id": result.RecordID, "scope": result.Scope,
			"user_id": result.UserID, "title": result.Title, "summary": result.Summary,
			"visibility": result.Visibility, "source_updated_at": result.SourceUpdatedAt,
			"indexed_at": result.IndexedAt, "content_sha256": result.ContentSHA256,
		})
	}
	return records, nil
}

var _ runtimeKnowledgeMemoryIndexedSearcher = (*knowledgememorypersistence.SQLiteStore)(nil)
