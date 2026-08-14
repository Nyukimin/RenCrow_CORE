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

func TestSQLiteStoreSaveSearchTraceMaintainsTraceContract(t *testing.T) {
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

func TestSQLiteStoreFindSearchTraceByIDReturnsExactTraceAndMissing(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "dci.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	first := domaindci.SearchTrace{
		EventID:            "evt_1",
		StartedAt:          now,
		EndedAt:            now.Add(time.Second),
		Actor:              "Worker",
		Mode:               "dci",
		UserQuery:          "first",
		FinalEvidenceCount: 1,
		Status:             "completed",
		Steps: []domaindci.SearchStep{{
			StepNo:      1,
			Tool:        "read_file",
			FilePath:    "docs/first.md",
			ResultCount: 1,
			Status:      "ok",
			CreatedAt:   now,
		}},
	}
	latest := first
	latest.EndedAt = now.Add(2 * time.Second)
	latest.UserQuery = "latest"
	latest.Steps = []domaindci.SearchStep{{
		StepNo:      1,
		Tool:        "rg",
		FilePath:    "docs/latest.md",
		ResultCount: 2,
		Status:      "completed",
		CreatedAt:   now.Add(time.Second),
	}}
	prefix := latest
	prefix.EventID = "evt_10"

	for _, trace := range []domaindci.SearchTrace{first, latest, prefix} {
		if err := store.SaveSearchTrace(ctx, trace); err != nil {
			t.Fatalf("SaveSearchTrace(%s): %v", trace.EventID, err)
		}
	}

	got, found, err := store.FindSearchTraceByID(ctx, "evt_1")
	if err != nil {
		t.Fatalf("FindSearchTraceByID: %v", err)
	}
	if !found || got.EventID != "evt_1" || got.UserQuery != "latest" || len(got.Steps) != 1 || got.Steps[0].Tool != "rg" {
		t.Fatalf("found=%v trace=%#v", found, got)
	}

	got, found, err = store.FindSearchTraceByID(ctx, "evt_1x")
	if err != nil {
		t.Fatalf("FindSearchTraceByID(prefix): %v", err)
	}
	if found || got.EventID != "" {
		t.Fatalf("prefix lookup found=%v trace=%#v", found, got)
	}

	got, found, err = store.FindSearchTraceByID(ctx, "missing")
	if err != nil {
		t.Fatalf("FindSearchTraceByID(missing): %v", err)
	}
	if found || got.EventID != "" {
		t.Fatalf("missing lookup found=%v trace=%#v", found, got)
	}
}

func TestSQLiteStoreFindSearchTraceByIDRejectsInvalidRowAndStep(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "dci.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	trace := domaindci.SearchTrace{
		EventID:   "evt_invalid",
		StartedAt: now,
		EndedAt:   now.Add(time.Second),
		Actor:     "Worker",
		Mode:      "dci",
		UserQuery: "invalid row test",
		Status:    "completed",
		Steps: []domaindci.SearchStep{{
			StepNo:    1,
			Tool:      "read_file",
			Status:    "ok",
			CreatedAt: now,
		}},
	}
	if err := store.SaveSearchTrace(ctx, trace); err != nil {
		t.Fatalf("SaveSearchTrace: %v", err)
	}

	if _, err := store.db.Exec("UPDATE dci_search_trace SET corpus_scope = ? WHERE event_id = ?", "{", trace.EventID); err != nil {
		t.Fatalf("corrupt trace row: %v", err)
	}
	_, found, err := store.FindSearchTraceByID(ctx, trace.EventID)
	if err == nil || found {
		t.Fatalf("FindSearchTraceByID invalid row: found=%v err=%v", found, err)
	}

	if _, err := store.db.Exec("UPDATE dci_search_trace SET corpus_scope = ? WHERE event_id = ?", "[]", trace.EventID); err != nil {
		t.Fatalf("restore trace row: %v", err)
	}
	if _, err := store.db.Exec("UPDATE dci_search_step SET created_at = ? WHERE event_id = ?", "not-a-time", trace.EventID); err != nil {
		t.Fatalf("corrupt trace step: %v", err)
	}
	_, found, err = store.FindSearchTraceByID(ctx, trace.EventID)
	if err == nil || found {
		t.Fatalf("FindSearchTraceByID invalid step: found=%v err=%v", found, err)
	}

	if _, err := store.db.Exec("UPDATE dci_search_step SET created_at = ? WHERE event_id = ?", formatTime(now), trace.EventID); err != nil {
		t.Fatalf("restore trace step: %v", err)
	}
	if _, err := store.db.Exec("UPDATE dci_search_trace SET status = ? WHERE event_id = ?", "unknown", trace.EventID); err != nil {
		t.Fatalf("corrupt trace status: %v", err)
	}
	_, found, err = store.FindSearchTraceByID(ctx, trace.EventID)
	if err == nil || found {
		t.Fatalf("FindSearchTraceByID semantically invalid row: found=%v err=%v", found, err)
	}
}
