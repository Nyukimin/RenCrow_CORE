package dci

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
)

func TestSQLiteStoreSaveSearchResultStoresTraceStepsEvidenceAndTerms(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "dci.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	result := domaindci.SearchResult{
		Pack: domaindci.EvidencePack{
			EventID:     "evt_dci_1",
			Query:       "DCI Source Registry",
			CorpusScope: []string{"docs/"},
			Evidence: []domaindci.Evidence{{
				EvidenceID: "ev_1",
				SourceID:   "src_1",
				FilePath:   "docs/10_新仕様/19_DCI_直接コーパス探索仕様.md",
				LineStart:  10,
				LineEnd:    12,
				Snippet:    "DCI evidence",
				Reason:     "test evidence",
				Confidence: 0.8,
			}},
			DerivedTerms: []string{"Source Registry"},
		},
		Trace: domaindci.SearchTrace{
			EventID:            "evt_dci_1",
			StartedAt:          now,
			EndedAt:            now.Add(time.Second),
			Actor:              "Worker",
			Mode:               "dci",
			UserQuery:          "DCI Source Registry",
			CorpusScope:        []string{"docs/"},
			FinalEvidenceCount: 1,
			Status:             "completed",
			Steps: []domaindci.SearchStep{{
				StepNo:      1,
				Tool:        "read_file",
				FilePath:    "docs/spec.md",
				ResultCount: 1,
				Status:      "ok",
				CreatedAt:   now,
			}},
		},
	}
	if err := store.SaveSearchResult(context.Background(), result); err != nil {
		t.Fatalf("SaveSearchResult: %v", err)
	}

	recent, err := store.ListRecent(1)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(recent) != 1 {
		t.Fatalf("recent count = %d", len(recent))
	}
	if recent[0].EventID != "evt_dci_1" || recent[0].FinalEvidenceCount != 1 {
		t.Fatalf("recent trace = %#v", recent[0])
	}
	if len(recent[0].Steps) != 1 || recent[0].Steps[0].Tool != "read_file" {
		t.Fatalf("recent steps = %#v", recent[0].Steps)
	}

	var evidenceCount int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM dci_evidence WHERE event_id = ?", "evt_dci_1").Scan(&evidenceCount); err != nil {
		t.Fatalf("query evidence count: %v", err)
	}
	if evidenceCount != 1 {
		t.Fatalf("evidence count = %d", evidenceCount)
	}
	var termCount int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM dci_query_terms WHERE event_id = ?", "evt_dci_1").Scan(&termCount); err != nil {
		t.Fatalf("query term count: %v", err)
	}
	if termCount != 1 {
		t.Fatalf("term count = %d", termCount)
	}
}

func TestSQLiteStoreSaveSearchTraceMaintainsLegacyTraceContract(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "dci.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	if err := store.SaveSearchTrace(context.Background(), domaindci.SearchTrace{
		EventID:   "evt_trace_only",
		StartedAt: now,
		EndedAt:   now.Add(time.Second),
		Actor:     "Worker",
		Mode:      "dci",
		UserQuery: "trace only",
		Status:    "completed",
	}); err != nil {
		t.Fatalf("SaveSearchTrace: %v", err)
	}
	recent, err := store.ListRecent(10)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(recent) != 1 || recent[0].EventID != "evt_trace_only" {
		t.Fatalf("recent = %#v", recent)
	}
}
