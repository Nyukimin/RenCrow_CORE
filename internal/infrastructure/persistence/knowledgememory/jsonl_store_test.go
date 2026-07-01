package knowledgememory

import (
	"context"
	"testing"
	"time"

	domainkm "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/knowledgememory"
)

func TestJSONLStoreSavesKnowledgeMemoryRecords(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	if err := store.SavePersonalArchiveEntry(context.Background(), domainkm.PersonalArchiveEntry{
		EntryID:      "pa_1",
		UserID:       "ren",
		OriginalText: "bio",
		Protected:    true,
		CreatedAt:    now,
	}); err != nil {
		t.Fatalf("SavePersonalArchiveEntry() error = %v", err)
	}
	if err := store.SaveDailyIntakeRule(context.Background(), domainkm.DailyIntakeRule{
		RuleID:    "rule_1",
		UserID:    "ren",
		Topic:     "AI",
		Cadence:   "daily",
		Status:    "active",
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("SaveDailyIntakeRule() error = %v", err)
	}
	if err := store.SaveTemporalMemoryMarker(context.Background(), domainkm.TemporalMemoryMarker{
		MarkerID:    "tm_1",
		Layer:       "today",
		ReferenceID: "pa_1",
		Summary:     "bio",
		CreatedAt:   now,
	}); err != nil {
		t.Fatalf("SaveTemporalMemoryMarker() error = %v", err)
	}
	personal, err := store.ListPersonalArchiveEntries(context.Background(), 10)
	if err != nil || len(personal) != 1 {
		t.Fatalf("ListPersonalArchiveEntries() = %#v, %v", personal, err)
	}
	rules, err := store.ListDailyIntakeRules(context.Background(), 10)
	if err != nil || len(rules) != 1 {
		t.Fatalf("ListDailyIntakeRules() = %#v, %v", rules, err)
	}
	markers, err := store.ListTemporalMemoryMarkers(context.Background(), 10)
	if err != nil || len(markers) != 1 {
		t.Fatalf("ListTemporalMemoryMarkers() = %#v, %v", markers, err)
	}
}

func TestJSONLStoreListsLatestKnowledgeMemoryStatePerID(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	if err := store.SaveNewsKnowledgeItem(context.Background(), domainkm.NewsKnowledgeItem{
		ItemID:    "news_1",
		Source:    "source",
		Topic:     "topic",
		Status:    "candidate",
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("SaveNewsKnowledgeItem(candidate) error = %v", err)
	}
	if err := store.SaveNewsKnowledgeItem(context.Background(), domainkm.NewsKnowledgeItem{
		ItemID:    "news_1",
		Source:    "source",
		Topic:     "topic",
		Status:    "promoted",
		CreatedAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("SaveNewsKnowledgeItem(promoted) error = %v", err)
	}
	if err := store.SaveDailyIntakeRule(context.Background(), domainkm.DailyIntakeRule{
		RuleID:    "rule_1",
		UserID:    "ren",
		Topic:     "AI",
		Cadence:   "daily",
		Status:    "candidate",
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("SaveDailyIntakeRule(candidate) error = %v", err)
	}
	if err := store.SaveDailyIntakeRule(context.Background(), domainkm.DailyIntakeRule{
		RuleID:    "rule_1",
		UserID:    "ren",
		Topic:     "AI",
		Cadence:   "daily",
		Status:    "enabled",
		CreatedAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("SaveDailyIntakeRule(enabled) error = %v", err)
	}

	news, err := store.ListNewsKnowledgeItems(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListNewsKnowledgeItems() error = %v", err)
	}
	if len(news) != 1 || news[0].ItemID != "news_1" || news[0].Status != "promoted" {
		t.Fatalf("news current view=%#v", news)
	}
	rules, err := store.ListDailyIntakeRules(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListDailyIntakeRules() error = %v", err)
	}
	if len(rules) != 1 || rules[0].RuleID != "rule_1" || rules[0].Status != "enabled" {
		t.Fatalf("rules current view=%#v", rules)
	}
}
