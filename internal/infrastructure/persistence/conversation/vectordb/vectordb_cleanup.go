package vectordb

import (
	"context"
	"fmt"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"strings"

	"github.com/qdrant/go-client/qdrant"
)

func (v *VectorDBStore) CleanupMemoryVectors(ctx context.Context, items []l1sqlite.L1VectorCleanupItem) (*l1sqlite.L1VectorCleanupResult, error) {
	if v == nil || v.client == nil || len(items) == 0 {
		return &l1sqlite.L1VectorCleanupResult{}, nil
	}
	ids := cleanupMemoryIDs(items)
	if len(ids) == 0 {
		return &l1sqlite.L1VectorCleanupResult{}, nil
	}
	collections := []string{v.collectionName}
	if domains, err := v.GetKBCollections(ctx); err == nil {
		for _, domain := range domains {
			collections = append(collections, v.getKBCollectionName(domain))
		}
	}
	deleted := 0
	for _, collection := range collections {
		if strings.TrimSpace(collection) == "" {
			continue
		}
		exists, err := v.client.CollectionExists(ctx, collection)
		if err != nil {
			return nil, fmt.Errorf("failed to check vector cleanup collection %s: %w", collection, err)
		}
		if !exists {
			continue
		}
		if err := v.deleteMemoryIDsFromCollection(ctx, collection, ids); err != nil {
			return nil, err
		}
		deleted += len(ids)
	}
	return &l1sqlite.L1VectorCleanupResult{Deleted: deleted}, nil
}

func cleanupMemoryIDs(items []l1sqlite.L1VectorCleanupItem) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		id := strings.TrimSpace(item.MemoryID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func (v *VectorDBStore) deleteMemoryIDsFromCollection(ctx context.Context, collection string, ids []string) error {
	conditions := make([]*qdrant.Condition, 0, len(ids)*2)
	for _, id := range ids {
		conditions = append(conditions,
			qdrantKeywordCondition("id", id),
			qdrantKeywordCondition("memory_id", id),
		)
	}
	if len(conditions) == 0 {
		return nil
	}
	waitTrue := true
	_, err := v.client.Delete(ctx, &qdrant.DeletePoints{
		CollectionName: collection,
		Wait:           &waitTrue,
		Points: &qdrant.PointsSelector{
			PointsSelectorOneOf: &qdrant.PointsSelector_Filter{
				Filter: &qdrant.Filter{Should: conditions},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to delete memory vectors from %s: %w", collection, err)
	}
	return nil
}

func qdrantKeywordCondition(key string, value string) *qdrant.Condition {
	return &qdrant.Condition{
		ConditionOneOf: &qdrant.Condition_Field{
			Field: &qdrant.FieldCondition{
				Key: key,
				Match: &qdrant.Match{
					MatchValue: &qdrant.Match_Keyword{Keyword: value},
				},
			},
		},
	}
}
