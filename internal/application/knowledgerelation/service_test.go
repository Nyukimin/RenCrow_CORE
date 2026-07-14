package knowledgerelation

import (
	"context"
	"testing"
	"time"

	domainrelation "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgerelation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

type relationBuildStore struct {
	items     []l1sqlite.L1KnowledgeItem
	entities  []l1sqlite.L1KnowledgeEntity
	links     []l1sqlite.L1KnowledgeItemEntity
	relations []domainrelation.Relation
}

func (s *relationBuildStore) ListKnowledgeItemsForRelations(context.Context, string, int, time.Time) ([]l1sqlite.L1KnowledgeItem, error) {
	return append([]l1sqlite.L1KnowledgeItem(nil), s.items...), nil
}

func (s *relationBuildStore) SaveKnowledgeEntity(_ context.Context, item l1sqlite.L1KnowledgeEntity) error {
	s.entities = append(s.entities, item)
	return nil
}

func (s *relationBuildStore) SaveKnowledgeItemEntity(_ context.Context, item l1sqlite.L1KnowledgeItemEntity) error {
	s.links = append(s.links, item)
	return nil
}

func (s *relationBuildStore) SaveKnowledgeItemRelation(_ context.Context, item domainrelation.Relation) error {
	s.relations = append(s.relations, item)
	return nil
}

func TestMetadataExtractorDeterministicallyExtractsAndDeduplicates(t *testing.T) {
	extractor := NewMetadataExtractor(nil)
	metadata := extractor.ExtractFromL1KnowledgeItem(l1sqlite.L1KnowledgeItem{
		ID:       "kb:qiita:1",
		Domain:   "qiita",
		Title:    "RenCrow_CORE uses MLX and MLX",
		SourceID: "github:RenCrow_CORE",
		Keywords: []string{"local_llm", "MLX", "local_llm"},
	})
	if metadata.ItemID != "kb:qiita:1" || metadata.SourceType != "qiita" {
		t.Fatalf("metadata=%#v", metadata)
	}
	if countString(metadata.Entities, "mlx") != 1 {
		t.Fatalf("entities should contain canonical MLX once: %#v", metadata.Entities)
	}
	if countString(metadata.Topics, "local_llm") != 1 {
		t.Fatalf("topics should contain local_llm once: %#v", metadata.Topics)
	}
	if countString(metadata.Projects, "rencrow_core") != 1 {
		t.Fatalf("projects should contain rencrow_core once: %#v", metadata.Projects)
	}
}

func TestRelationBuildServiceDryRunReportsWithoutWriting(t *testing.T) {
	store := &relationBuildStore{items: []l1sqlite.L1KnowledgeItem{
		{ID: "a", Domain: "qiita", Title: "MLX local_llm", Keywords: []string{"MLX", "local_llm"}},
		{ID: "b", Domain: "github", Title: "MLX local_llm", Keywords: []string{"MLX", "local_llm"}},
	}}
	service := NewRelationBuildService(store, NewMetadataExtractor(nil), domainrelation.DefaultScoringConfig())
	report, err := service.BuildBatch(context.Background(), BatchQuery{Domain: "all", Limit: 100, DryRun: true})
	if err != nil {
		t.Fatalf("BuildBatch failed: %v", err)
	}
	if !report.DryRun || report.CheckedItems != 2 || report.RelationUpserts == 0 {
		t.Fatalf("report=%#v", report)
	}
	if len(store.entities) != 0 || len(store.links) != 0 || len(store.relations) != 0 {
		t.Fatalf("dry-run wrote data: %#v %#v %#v", store.entities, store.links, store.relations)
	}
}

func TestRelationBuildServiceBuildForItemPersistsMetadataAndBidirectionalRelations(t *testing.T) {
	store := &relationBuildStore{items: []l1sqlite.L1KnowledgeItem{
		{ID: "existing", Domain: "github", Title: "MLX runtime", Keywords: []string{"MLX", "local_llm"}},
	}}
	service := NewRelationBuildService(store, NewMetadataExtractor(nil), domainrelation.DefaultScoringConfig())
	report, err := service.BuildForItem(context.Background(), l1sqlite.L1KnowledgeItem{
		ID: "new", Domain: "qiita", Title: "MLX guide", Keywords: []string{"MLX", "local_llm"},
	})
	if err != nil {
		t.Fatalf("BuildForItem failed: %v", err)
	}
	if report.RelationUpserts != 4 || len(store.relations) != 4 {
		t.Fatalf("report=%#v relations=%#v", report, store.relations)
	}
	if len(store.entities) == 0 || len(store.links) == 0 {
		t.Fatalf("metadata was not persisted: entities=%#v links=%#v", store.entities, store.links)
	}
}

func TestRelationBuildServiceBlocksOversizedRunBeforeWriting(t *testing.T) {
	items := make([]l1sqlite.L1KnowledgeItem, 72)
	for i := range items {
		items[i] = l1sqlite.L1KnowledgeItem{ID: string(rune('a' + i)), Domain: "general", Title: "MLX", Keywords: []string{"MLX", "local_llm"}}
	}
	store := &relationBuildStore{items: items}
	service := NewRelationBuildService(store, NewMetadataExtractor(nil), domainrelation.DefaultScoringConfig())
	report, err := service.BuildBatch(context.Background(), BatchQuery{Domain: "all", Limit: 100, DryRun: false})
	if err != nil {
		t.Fatalf("BuildBatch failed: %v", err)
	}
	if report.Status != BuildStatusBlockedNeedsReview || report.RelationUpserts <= MaxRelationUpsertsPerRun {
		t.Fatalf("report=%#v", report)
	}
	if len(store.entities) != 0 || len(store.relations) != 0 {
		t.Fatal("blocked run must not write")
	}
}

func countString(values []string, want string) int {
	count := 0
	for _, value := range values {
		if value == want {
			count++
		}
	}
	return count
}
