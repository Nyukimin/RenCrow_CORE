package knowledgememory

import (
	"context"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"path/filepath"
	"testing"
	"time"

	kmapp "github.com/Nyukimin/RenCrow_CORE/internal/application/knowledgememory"
	domainkm "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgememory"
)

type fakeL1KnowledgeMemoryStore struct {
	staging  []l1sqlite.L1StagingItem
	registry []l1sqlite.L1SourceRegistryEntry
}

func (s *fakeL1KnowledgeMemoryStore) SaveStagingItem(_ context.Context, item l1sqlite.L1StagingItem) (*l1sqlite.L1StagingItem, error) {
	s.staging = append(s.staging, item)
	return &item, nil
}

func (s *fakeL1KnowledgeMemoryStore) SaveSourceRegistryEntry(_ context.Context, entry l1sqlite.L1SourceRegistryEntry) (*l1sqlite.L1SourceRegistryEntry, error) {
	s.registry = append(s.registry, entry)
	return &entry, nil
}

func TestDailyIntakeRegistryAdapterConvertsSourceRegistryEntry(t *testing.T) {
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	t.Cleanup(func() { _ = l1.Close() })
	adapter := NewDailyIntakeRegistryAdapter(l1)
	saved, err := adapter.SaveSourceRegistryEntry(context.Background(), kmapp.SourceRegistryEntry{
		SourceID:      "knowledge_memory:daily_intake_rule:rule_1",
		URL:           "https://example.com/feed.xml",
		Kind:          kmapp.SourceKindSearchFallback,
		TrustScore:    0.55,
		FetchInterval: 24 * time.Hour,
		LicenseNote:   "daily intake rule reviewed source; fetch to staging before promote",
		Enabled:       true,
		Meta:          map[string]interface{}{"daily_intake_enabled": true},
	})
	if err != nil {
		t.Fatalf("SaveSourceRegistryEntry failed: %v", err)
	}
	if saved == nil || saved.SourceID != "knowledge_memory:daily_intake_rule:rule_1" || !saved.Enabled {
		t.Fatalf("saved entry = %#v", saved)
	}
	entries, err := l1.ListSourceRegistryEntries(context.Background(), true)
	if err != nil {
		t.Fatalf("ListSourceRegistryEntries failed: %v", err)
	}
	if len(entries) != 1 || entries[0].Kind != l1sqlite.L1SourceKindSearchFallback || entries[0].Meta["daily_intake_enabled"] != true {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestWithL1ConnectionIgnoresTypedNilL1Store(t *testing.T) {
	base := NewJSONLStore(t.TempDir())
	var l1 *fakeL1KnowledgeMemoryStore
	store := WithL1Connection(base, l1)
	if store != base {
		t.Fatalf("typed nil L1 store should not wrap base store")
	}
	err := store.SaveNewsKnowledgeItem(context.Background(), domainkm.NewsKnowledgeItem{
		ItemID:    "news_1",
		Source:    "example",
		Topic:     "AI policy",
		Status:    "candidate",
		CreatedAt: fixedKnowledgeMemoryTime(),
	})
	if err != nil {
		t.Fatalf("SaveNewsKnowledgeItem() error = %v", err)
	}
}

func TestL1ConnectedStoreStagesPersonalArchiveWithoutPromote(t *testing.T) {
	base := NewJSONLStore(t.TempDir())
	l1 := &fakeL1KnowledgeMemoryStore{}
	store := WithL1Connection(base, l1)

	err := store.SavePersonalArchiveEntry(context.Background(), domainkm.PersonalArchiveEntry{
		EntryID:      "pa_1",
		UserID:       "ren",
		OriginalText: "protected original text",
		Protected:    true,
		CreatedAt:    fixedKnowledgeMemoryTime(),
	})
	if err != nil {
		t.Fatalf("SavePersonalArchiveEntry() error = %v", err)
	}
	if len(l1.staging) != 1 {
		t.Fatalf("expected one staging item, got %#v", l1.staging)
	}
	item := l1.staging[0]
	if item.Kind != l1sqlite.L1StagingKindMemoryCandidate || item.Namespace != "user:ren" {
		t.Fatalf("unexpected staging route: %#v", item)
	}
	if item.ValidationStatus != l1sqlite.L1StagingStatusPending {
		t.Fatalf("expected pending status, got %s", item.ValidationStatus)
	}
	if item.Meta["protected_original"] != true || item.Meta["auto_promote"] != false || item.Meta["review_required"] != true {
		t.Fatalf("expected protected review metadata, got %#v", item.Meta)
	}
}

func TestL1ConnectedStoreStagesNewsAndDisabledSourceRegistryCandidate(t *testing.T) {
	base := NewJSONLStore(t.TempDir())
	l1 := &fakeL1KnowledgeMemoryStore{}
	store := WithL1Connection(base, l1)

	err := store.SaveNewsKnowledgeItem(context.Background(), domainkm.NewsKnowledgeItem{
		ItemID:    "news_1",
		Source:    "example",
		Topic:     "AI policy",
		URL:       "https://example.com/news/1",
		Summary:   "summary",
		Durable:   false,
		Status:    "candidate",
		CreatedAt: fixedKnowledgeMemoryTime(),
	})
	if err != nil {
		t.Fatalf("SaveNewsKnowledgeItem() error = %v", err)
	}
	if len(l1.staging) != 1 {
		t.Fatalf("expected one staging item, got %#v", l1.staging)
	}
	if l1.staging[0].Kind != l1sqlite.L1StagingKindExternalFetch || l1.staging[0].Namespace != "kb:news" {
		t.Fatalf("unexpected news staging item: %#v", l1.staging[0])
	}
	if len(l1.registry) != 1 {
		t.Fatalf("expected one source registry candidate, got %#v", l1.registry)
	}
	source := l1.registry[0]
	if source.Enabled {
		t.Fatalf("source registry candidate must be disabled before review: %#v", source)
	}
	if source.URL != "https://example.com/news/1" || source.Kind != l1sqlite.L1SourceKindSearchFallback {
		t.Fatalf("unexpected source registry candidate: %#v", source)
	}
	if source.Meta["review_required"] != true || source.Meta["auto_fetch"] != false {
		t.Fatalf("expected review metadata, got %#v", source.Meta)
	}
}

func TestL1ConnectedStoreDailyIntakeURLCreatesDisabledSourceRegistryCandidate(t *testing.T) {
	base := NewJSONLStore(t.TempDir())
	l1 := &fakeL1KnowledgeMemoryStore{}
	store := WithL1Connection(base, l1)

	err := store.SaveDailyIntakeRule(context.Background(), domainkm.DailyIntakeRule{
		RuleID:     "rule_1",
		UserID:     "ren",
		Topic:      "AI news",
		SourceHint: "https://example.com/feed.xml",
		Cadence:    "daily",
		Status:     "active",
		CreatedAt:  fixedKnowledgeMemoryTime(),
	})
	if err != nil {
		t.Fatalf("SaveDailyIntakeRule() error = %v", err)
	}
	if len(l1.staging) != 1 {
		t.Fatalf("expected one staging item, got %#v", l1.staging)
	}
	if l1.staging[0].Kind != l1sqlite.L1StagingKindMemoryCandidate || l1.staging[0].Namespace != "user:ren" {
		t.Fatalf("unexpected daily intake staging item: %#v", l1.staging[0])
	}
	if len(l1.registry) != 1 {
		t.Fatalf("expected one source registry candidate, got %#v", l1.registry)
	}
	if l1.registry[0].Enabled {
		t.Fatalf("daily intake source candidate must require review before enabling: %#v", l1.registry[0])
	}
	if l1.registry[0].URL != "https://example.com/feed.xml" || l1.registry[0].Meta["source_type"] != "daily_intake_rule" {
		t.Fatalf("unexpected daily intake source candidate: %#v", l1.registry[0])
	}
}

func fixedKnowledgeMemoryTime() time.Time {
	return time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
}
