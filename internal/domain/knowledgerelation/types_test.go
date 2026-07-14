package knowledgerelation

import "testing"

func TestBuildRelationsKeepsOnlyScoreAtOrAboveThreshold(t *testing.T) {
	items := []ItemMetadata{
		{ItemID: "qiita-1", Entities: []string{"MLX"}, Topics: []string{"local_llm"}},
		{ItemID: "github-1", Entities: []string{"MLX"}, Topics: []string{"release"}},
		{ItemID: "note-1", Entities: []string{"CUDA"}, Topics: []string{"local_llm"}},
	}
	relations := BuildRelations(items, DefaultScoringConfig())
	if len(relations) != 0 {
		t.Fatalf("default threshold should drop single entity/topic matches, got %#v", relations)
	}
	cfg := DefaultScoringConfig()
	cfg.MinimumScore = 3
	relations = BuildRelations(items, cfg)
	if len(relations) == 0 {
		t.Fatal("expected relations when threshold is lowered")
	}
	if relations[0].RelationType != RelationSameEntity || relations[0].Score != 3 {
		t.Fatalf("unexpected top relation: %#v", relations[0])
	}
}

func TestBuildPairRelationsSameProjectPassesDefaultThreshold(t *testing.T) {
	src := ItemMetadata{ItemID: "x-1", Projects: []string{"RenCrow_LLM"}, Entities: []string{"MLX"}}
	dst := ItemMetadata{ItemID: "note-1", Projects: []string{"RenCrow_LLM"}, Entities: []string{"Ollama"}}
	cfg := DefaultScoringConfig()
	cfg.MinimumScore = 3
	relations := BuildPairRelations(src, dst, cfg)
	if len(relations) != 1 {
		t.Fatalf("len(relations) = %d, want 1: %#v", len(relations), relations)
	}
	if relations[0].RelationType != RelationSameProject {
		t.Fatalf("relation type = %q", relations[0].RelationType)
	}
}
