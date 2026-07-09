package dci

import (
	"context"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/conversation/l1sqlite"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domconv "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/conversation"
	domaindci "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/dci"
)

func TestL1SourceCandidateStoreStagesDCIEvidenceAsPendingSearchResult(t *testing.T) {
	ctx := context.Background()
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.sqlite"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer l1.Close()
	store := NewL1SourceCandidateStore(l1, "kb:dci").WithNow(func() time.Time {
		return time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	})
	result := domaindci.SearchResult{
		Pack: domaindci.EvidencePack{
			EventID:      "evt_dci_1",
			Query:        "DCI evidence",
			DerivedTerms: []string{"DCI"},
			Evidence: []domaindci.Evidence{{
				EvidenceID: "evt_dci_1_ev_001",
				FilePath:   "docs/spec.md",
				LineStart:  12,
				LineEnd:    12,
				Snippet:    "DCI evidence line",
				Reason:     "query term matched",
				Confidence: 0.7,
			}},
		},
		Trace: domaindci.SearchTrace{
			EventID:   "evt_dci_1",
			StartedAt: time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
			EndedAt:   time.Date(2026, 5, 18, 12, 0, 1, 0, time.UTC),
		},
	}

	if err := store.SaveDCISourceCandidates(ctx, result); err != nil {
		t.Fatalf("SaveDCISourceCandidates failed: %v", err)
	}
	items, err := l1.RecentStagingItems(ctx, l1sqlite.L1StagingStatusPending, 10)
	if err != nil {
		t.Fatalf("RecentStagingItems failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one staging item, got %#v", items)
	}
	item := items[0]
	if item.Kind != l1sqlite.L1StagingKindSearchResult {
		t.Fatalf("kind = %s", item.Kind)
	}
	if item.SourceID == "dci:evt_dci_1" || item.SourceURL == "" {
		t.Fatalf("unexpected source fields: %+v", item)
	}
	if item.Meta["source_kind"] != "dci" || item.Meta["review_required"] != true {
		t.Fatalf("missing dci review metadata: %#v", item.Meta)
	}
	if item.Meta["file_path"] != "docs/spec.md" || item.RawText != "DCI evidence line" {
		t.Fatalf("unexpected staged evidence: %+v", item)
	}
	entries, err := l1.ListSourceRegistryEntries(ctx, false)
	if err != nil {
		t.Fatalf("ListSourceRegistryEntries failed: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one source registry candidate, got %#v", entries)
	}
	entry := entries[0]
	if entry.SourceID != item.SourceID || entry.URL != item.SourceURL {
		t.Fatalf("source registry candidate does not match staging source: entry=%+v item=%+v", entry, item)
	}
	if entry.Kind != l1sqlite.L1SourceKindSearchFallback || entry.Enabled {
		t.Fatalf("unexpected source registry candidate state: %+v", entry)
	}
	if entry.Meta["source_kind"] != "dci" || entry.Meta["auto_fetch"] != false || entry.Meta["review_required"] != true {
		t.Fatalf("missing source registry dci metadata: %#v", entry.Meta)
	}
}

func TestL1SourceCandidateStoreWithoutSourceRegistrySupportStillStages(t *testing.T) {
	ctx := context.Background()
	store := &stagingOnlyStore{}
	candidates := NewL1SourceCandidateStore(store, "kb:dci").WithNow(func() time.Time {
		return time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	})
	result := domaindci.SearchResult{
		Pack: domaindci.EvidencePack{
			EventID: "evt_dci_2",
			Query:   "DCI evidence",
			Evidence: []domaindci.Evidence{{
				EvidenceID: "evt_dci_2_ev_001",
				FilePath:   "docs/spec.md",
				Snippet:    "DCI evidence line",
			}},
		},
		Trace: domaindci.SearchTrace{EventID: "evt_dci_2"},
	}

	if err := candidates.SaveDCISourceCandidates(ctx, result); err != nil {
		t.Fatalf("SaveDCISourceCandidates failed: %v", err)
	}
	if len(store.items) != 1 || store.items[0].SourceURL == "" {
		t.Fatalf("expected staged DCI source candidate with synthetic URL, got %#v", store.items)
	}
}

func TestL1SourceMetadataRankerRanksEnabledLocalPathCandidates(t *testing.T) {
	ctx := context.Background()
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.sqlite"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer l1.Close()
	target := filepath.Join("docs", "10_新仕様", "19_DCI_直接コーパス探索仕様.md")
	if _, err := l1.SaveSourceRegistryEntry(ctx, l1sqlite.L1SourceRegistryEntry{
		SourceID:      "src_dci_spec",
		URL:           "https://local.rencrow.invalid/dci/docs%2F10_%E6%96%B0%E4%BB%95%E6%A7%98%2F19_DCI_%E7%9B%B4%E6%8E%A5%E3%82%B3%E3%83%BC%E3%83%91%E3%82%B9%E6%8E%A2%E7%B4%A2%E4%BB%95%E6%A7%98.md",
		Kind:          l1sqlite.L1SourceKindSearchFallback,
		TrustScore:    0.90,
		FetchInterval: time.Hour,
		LicenseNote:   "local DCI spec metadata",
		Enabled:       true,
		Meta: map[string]interface{}{
			"local_path": target,
			"summary":    "DCI Source Registry metadata",
		},
	}); err != nil {
		t.Fatalf("SaveSourceRegistryEntry failed: %v", err)
	}
	if _, err := l1.SaveSourceRegistryEntry(ctx, l1sqlite.L1SourceRegistryEntry{
		SourceID:      "src_disabled_dci_spec",
		URL:           "https://local.rencrow.invalid/dci/docs%2Fdisabled.md",
		Kind:          l1sqlite.L1SourceKindSearchFallback,
		TrustScore:    1.0,
		FetchInterval: time.Hour,
		LicenseNote:   "disabled candidate",
		Enabled:       false,
		Meta: map[string]interface{}{
			"local_path": "docs/disabled.md",
		},
	}); err != nil {
		t.Fatalf("SaveSourceRegistryEntry disabled failed: %v", err)
	}

	ranker := NewL1SourceMetadataRanker(l1)
	ranks, err := ranker.RankDCICandidateFiles(ctx, []string{"docs/disabled.md", target}, []string{"DCI"})
	if err != nil {
		t.Fatalf("RankDCICandidateFiles failed: %v", err)
	}
	if len(ranks) != 1 {
		t.Fatalf("expected one enabled rank, got %#v", ranks)
	}
	if ranks[0].FilePath != target || ranks[0].SourceID != "src_dci_spec" {
		t.Fatalf("unexpected rank: %#v", ranks[0])
	}
	if ranks[0].Score <= 0.9 {
		t.Fatalf("expected trust and term score, got %#v", ranks[0])
	}
}

func TestL1KnowledgeFTSCandidateProviderReturnsLocalPathCandidates(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(root, "l1.sqlite"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer l1.Close()
	target := filepath.Join(root, "docs", "dci.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir target dir: %v", err)
	}
	if err := os.WriteFile(target, []byte("DCI FTS evidence\n"), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	staged, err := l1.SaveStagingItem(ctx, l1sqlite.L1StagingItem{
		Kind:             l1sqlite.L1StagingKindSearchResult,
		Namespace:        "kb:general",
		EventID:          "evt_fts",
		SourceID:         "src_fts",
		SourceURL:        "https://local.rencrow.invalid/dci/docs%2Fdci.md",
		RawText:          "DCI FTS evidence",
		SummaryDraft:     "Direct Corpus Interaction",
		Keywords:         []string{"DCI"},
		ValidationStatus: l1sqlite.L1StagingStatusValidated,
		Meta: map[string]interface{}{
			"title":      "DCI FTS",
			"local_path": target,
		},
	})
	if err != nil {
		t.Fatalf("SaveStagingItem failed: %v", err)
	}
	if _, err := l1.PromoteValidatedStagingItemToKnowledge(ctx, staged.ID, "general"); err != nil {
		t.Fatalf("PromoteValidatedStagingItemToKnowledge failed: %v", err)
	}

	provider := NewL1KnowledgeFTSCandidateProvider(l1, []string{"general"})
	ranks, err := provider.CandidateFiles(ctx, "DCI", []string{"dci"}, []string{filepath.Join(root, "docs")}, 10)
	if err != nil {
		t.Fatalf("CandidateFiles failed: %v", err)
	}
	if len(ranks) != 1 {
		t.Fatalf("expected one candidate, got %#v", ranks)
	}
	if ranks[0].FilePath != target || ranks[0].SourceID != "src_fts" || !strings.Contains(ranks[0].Reason, "FTS") {
		t.Fatalf("unexpected rank: %#v", ranks[0])
	}
}

type vectorKBSearchStoreStub struct {
	docs    []*domconv.Document
	domains []string
	queries []string
	topKs   []int
	err     error
}

func (s *vectorKBSearchStoreStub) SearchKB(_ context.Context, domain string, query string, topK int) ([]*domconv.Document, error) {
	s.domains = append(s.domains, domain)
	s.queries = append(s.queries, query)
	s.topKs = append(s.topKs, topK)
	if s.err != nil {
		return nil, s.err
	}
	return append([]*domconv.Document(nil), s.docs...), nil
}

func TestVectorKBCandidateProviderReturnsSemanticLocalPathCandidates(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "docs", "semantic.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir target dir: %v", err)
	}
	store := &vectorKBSearchStoreStub{docs: []*domconv.Document{{
		ID:      "kb_vector_doc",
		Domain:  "general",
		Content: "Direct Corpus Interaction semantic evidence",
		Source:  "https://local.rencrow.invalid/dci/ignored",
		Score:   0.82,
		Meta: map[string]interface{}{
			"source_id":  "src_vector",
			"local_path": target,
			"keywords":   []interface{}{"DCI", "semantic"},
		},
	}}}
	provider := NewVectorKBCandidateProvider(store, []string{"general"})

	ranks, err := provider.CandidateFiles(context.Background(), "DCI semantic", []string{"dci", "semantic"}, []string{filepath.Join(root, "docs")}, 10)
	if err != nil {
		t.Fatalf("CandidateFiles failed: %v", err)
	}
	if len(store.domains) != 1 || store.domains[0] != "general" || store.queries[0] != "DCI semantic" || store.topKs[0] != 10 {
		t.Fatalf("store call mismatch domains=%#v queries=%#v topKs=%#v", store.domains, store.queries, store.topKs)
	}
	if len(ranks) != 1 {
		t.Fatalf("expected one candidate, got %#v", ranks)
	}
	if ranks[0].FilePath != target || ranks[0].SourceID != "src_vector" || !strings.Contains(ranks[0].Reason, "semantic") {
		t.Fatalf("unexpected rank: %#v", ranks[0])
	}
	if ranks[0].Score <= 2.0 {
		t.Fatalf("expected semantic score to include vector score and term score, got %#v", ranks[0])
	}
}

func TestVectorKBCandidateProviderSkipsOutsideAllowlist(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.md")
	store := &vectorKBSearchStoreStub{docs: []*domconv.Document{{
		ID:      "kb_vector_outside",
		Domain:  "general",
		Content: "DCI outside evidence",
		Score:   0.9,
		Meta: map[string]interface{}{
			"local_path": outside,
		},
	}}}
	provider := NewVectorKBCandidateProvider(store, []string{"general"})

	ranks, err := provider.CandidateFiles(context.Background(), "DCI", []string{"dci"}, []string{root}, 10)
	if err != nil {
		t.Fatalf("CandidateFiles failed: %v", err)
	}
	if len(ranks) != 0 {
		t.Fatalf("outside allowlist candidate leaked: %#v", ranks)
	}
}

type stagingOnlyStore struct {
	items []l1sqlite.L1StagingItem
}

func (s *stagingOnlyStore) SaveStagingItem(_ context.Context, item l1sqlite.L1StagingItem) (*l1sqlite.L1StagingItem, error) {
	s.items = append(s.items, item)
	return &item, nil
}
