package verification

import (
	"context"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"testing"
	"time"

	appverification "github.com/Nyukimin/RenCrow_CORE/internal/application/verification"
	domainverification "github.com/Nyukimin/RenCrow_CORE/internal/domain/verification"
)

type stubL1EvidenceStore struct {
	kb       []l1sqlite.L1KnowledgeItem
	cache    *l1sqlite.L1SearchCacheEntry
	registry []l1sqlite.L1SourceRegistryEntry
}

func (s stubL1EvidenceStore) SearchKnowledgeItemsFTS(context.Context, string, string, int) ([]l1sqlite.L1KnowledgeItem, error) {
	return s.kb, nil
}

func (s stubL1EvidenceStore) GetSimilarFreshSearchCache(context.Context, string, string, time.Time, float64) (*l1sqlite.L1SearchCacheEntry, error) {
	return s.cache, nil
}

func (s stubL1EvidenceStore) ListSourceRegistryEntries(context.Context, bool) ([]l1sqlite.L1SourceRegistryEntry, error) {
	return s.registry, nil
}

func TestL1EvidenceReaderUsesRawKnowledgeAndSearchCache(t *testing.T) {
	now := time.Now().UTC()
	reader := NewL1EvidenceReader(stubL1EvidenceStore{
		kb: []l1sqlite.L1KnowledgeItem{{
			ID:           "kb-1",
			SourceURL:    "https://example.test/interstellar",
			RawText:      "インターステラーは2014年公開の宇宙映画です。",
			SummaryDraft: "要約は根拠補助であり raw_text を優先する。",
			UpdatedAt:    now,
		}},
		cache: &l1sqlite.L1SearchCacheEntry{
			QueryHash:   "hash-1",
			ResultsJSON: `{"title":"インターステラー","year":2014}`,
			RetrievedAt: now,
		},
	})

	evidence, err := reader.ReadEvidence(context.Background(), domainverification.Claim{
		ID:       "claim-1",
		Text:     "インターステラーは2014年公開です",
		Priority: domainverification.TriggerMedium,
	}, appverification.Request{})
	if err != nil {
		t.Fatalf("ReadEvidence failed: %v", err)
	}
	if len(evidence) < 2 {
		t.Fatalf("expected knowledge and cache evidence, got %+v", evidence)
	}
	if !evidence[0].Supports {
		t.Fatalf("expected raw knowledge evidence to support claim: %+v", evidence[0])
	}
	if evidence[0].SourceType != domainverification.EvidenceL1SQLite {
		t.Fatalf("unexpected source type: %s", evidence[0].SourceType)
	}
}

func TestL1EvidenceReaderDoesNotTreatSourceRegistryAsPromotedEvidence(t *testing.T) {
	reader := NewL1EvidenceReader(stubL1EvidenceStore{
		registry: []l1sqlite.L1SourceRegistryEntry{{
			SourceID:      "news:interstellar",
			URL:           "https://example.test/interstellar",
			Kind:          "feed",
			Enabled:       true,
			LastStatus:    "ok",
			LastFetchedAt: time.Now().UTC(),
		}},
	})

	evidence, err := reader.ReadEvidence(context.Background(), domainverification.Claim{
		ID:       "claim-1",
		Text:     "interstellar source registry",
		Priority: domainverification.TriggerHigh,
	}, appverification.Request{})
	if err != nil {
		t.Fatalf("ReadEvidence failed: %v", err)
	}
	if len(evidence) != 1 {
		t.Fatalf("expected one registry evidence ref, got %+v", evidence)
	}
	if evidence[0].Supports {
		t.Fatalf("source registry metadata must not be promoted evidence: %+v", evidence[0])
	}
	if evidence[0].SourceType != domainverification.EvidenceSourceRegistry {
		t.Fatalf("unexpected source type: %s", evidence[0].SourceType)
	}
}
