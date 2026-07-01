package knowledgememory

import (
	"context"
	"testing"
	"time"

	domainkm "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/knowledgememory"
)

type memoryDreamStore struct {
	personal []domainkm.PersonalArchiveEntry
	creative []domainkm.CreativeKnowledgeItem
	news     []domainkm.NewsKnowledgeItem
	temporal []domainkm.TemporalMemoryMarker
	dreams   []domainkm.DreamConsolidationRun
}

func (s *memoryDreamStore) ListPersonalArchiveEntries(_ context.Context, _ int) ([]domainkm.PersonalArchiveEntry, error) {
	return s.personal, nil
}
func (s *memoryDreamStore) ListCreativeKnowledgeItems(_ context.Context, _ int) ([]domainkm.CreativeKnowledgeItem, error) {
	return s.creative, nil
}
func (s *memoryDreamStore) ListNewsKnowledgeItems(_ context.Context, _ int) ([]domainkm.NewsKnowledgeItem, error) {
	return s.news, nil
}
func (s *memoryDreamStore) ListTemporalMemoryMarkers(_ context.Context, _ int) ([]domainkm.TemporalMemoryMarker, error) {
	return s.temporal, nil
}
func (s *memoryDreamStore) SaveDreamConsolidationRun(_ context.Context, item domainkm.DreamConsolidationRun) error {
	if err := domainkm.ValidateDreamConsolidationRun(item); err != nil {
		return err
	}
	s.dreams = append(s.dreams, item)
	return nil
}

func TestBuildDreamConsolidationProposalCreatesPendingReviewSeeds(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	store := &memoryDreamStore{
		personal: []domainkm.PersonalArchiveEntry{{EntryID: "pa_1", UserID: "ren", OriginalText: "protected original\nsecond line", Protected: true}},
		creative: []domainkm.CreativeKnowledgeItem{{ItemID: "ck_1", Title: "Movie Title", Status: "candidate"}},
		news:     []domainkm.NewsKnowledgeItem{{ItemID: "news_1", Source: "example", Topic: "Tech", Summary: "AI update", Status: "candidate"}},
		temporal: []domainkm.TemporalMemoryMarker{{MarkerID: "tm_1", Layer: "week", ReferenceID: "pa_1", Summary: "weekly pattern"}},
	}

	run, err := BuildDreamConsolidationProposal(context.Background(), store, DreamProposalInput{Now: now})
	if err != nil {
		t.Fatalf("BuildDreamConsolidationProposal failed: %v", err)
	}
	if run.RunID != "dream_20260518_120000" || run.Status != "proposal" || run.ReviewStatus != "pending" {
		t.Fatalf("run = %#v", run)
	}
	if len(run.IdeaSeeds) != 4 {
		t.Fatalf("idea seeds = %#v", run.IdeaSeeds)
	}
	if len(store.dreams) != 1 || store.dreams[0].ReviewStatus != "pending" {
		t.Fatalf("saved dreams = %#v", store.dreams)
	}
}

func TestBuildDreamConsolidationProposalNeverAutoApprovesEmptySources(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	store := &memoryDreamStore{}

	run, err := BuildDreamConsolidationProposal(context.Background(), store, DreamProposalInput{Now: now, Scope: []string{"news_knowledge"}})
	if err != nil {
		t.Fatalf("BuildDreamConsolidationProposal failed: %v", err)
	}
	if run.ReviewStatus != "pending" || run.Status != "proposal" {
		t.Fatalf("run = %#v", run)
	}
	if len(run.IdeaSeeds) != 1 || run.IdeaSeeds[0] == "" {
		t.Fatalf("idea seeds = %#v", run.IdeaSeeds)
	}
}
