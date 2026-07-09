package browsertrace

import (
	"context"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"path/filepath"
	"testing"
	"time"

	domaintrace "github.com/Nyukimin/RenCrow_CORE/internal/domain/browsertrace"
)

func TestL1APICandidateStoreStagesBrowserTraceCandidatesAsPendingSearchResult(t *testing.T) {
	ctx := context.Background()
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.sqlite"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer l1.Close()
	store := NewL1APICandidateStore(l1, "kb:browser_trace_api").WithNow(func() time.Time {
		return time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	})
	now := time.Date(2026, 5, 18, 12, 1, 0, 0, time.UTC)
	result := domaintrace.DiscoveryResult{
		Run: domaintrace.TraceRun{
			TraceRunID: "trace_1",
			SiteID:     "example",
			TracePath:  "traces/trace_1",
			CapturedAt: now,
			CreatedAt:  now,
		},
		Candidates: []domaintrace.APICandidate{{
			CandidateID:          "api_cand_1",
			TraceRunID:           "trace_1",
			SiteID:               "example",
			Method:               "GET",
			ObservedURL:          "https://example.com/api/items?page=1",
			TemplatedURL:         "https://example.com/api/items?page={page}",
			PathTemplate:         "/api/items",
			AuthRequired:         true,
			ContainsPersonalData: "unknown",
			RiskLevel:            "medium",
			Status:               "candidate",
			Confidence:           0.8,
			CreatedAt:            now,
		}},
	}

	if err := store.SaveBrowserTraceAPICandidates(ctx, result); err != nil {
		t.Fatalf("SaveBrowserTraceAPICandidates failed: %v", err)
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
	if item.Namespace != "kb:browser_trace_api" || item.SourceID != "browser_trace:trace_1" {
		t.Fatalf("unexpected source fields: %+v", item)
	}
	if item.SourceURL != "https://example.com/api/items?page=1" {
		t.Fatalf("source_url = %q", item.SourceURL)
	}
	if item.Meta["source_kind"] != "browser_trace_api" || item.Meta["review_required"] != true {
		t.Fatalf("missing review metadata: %#v", item.Meta)
	}
	if item.Meta["promote_requires_validator"] != true || item.Meta["candidate_id"] != "api_cand_1" {
		t.Fatalf("missing validator metadata: %#v", item.Meta)
	}
	if item.Meta["auth_required"] != true || item.Meta["risk_level"] != "medium" {
		t.Fatalf("missing risk metadata: %#v", item.Meta)
	}
	if item.RawText == "" || item.SummaryDraft == "" {
		t.Fatalf("missing staged text: %+v", item)
	}
}
