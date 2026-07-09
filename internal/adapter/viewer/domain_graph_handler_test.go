package viewer

import (
	"context"
	"encoding/json"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/conversation/l1sqlite"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHandleDomainGraphAssertionsReturnsCurrentView(t *testing.T) {
	ctx := context.Background()
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	assertion := saveViewerDomainGraphAssertion(t, ctx, store)
	h := HandleDomainGraphAssertions(store)

	req := httptest.NewRequest(http.MethodGet, "/viewer/domain-graph/assertions?domain=movie&limit=10", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "viewer domain graph raw text") {
		t.Fatalf("response should not expose raw text: %s", rec.Body.String())
	}
	var out domainGraphAssertionsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if out.Total != 1 || out.Limit != 10 || out.Offset != 0 || len(out.Items) != 1 {
		t.Fatalf("unexpected response: %+v", out)
	}
	got := out.Items[0]
	if got.ID != assertion.ID || got.StagingID != assertion.StagingID || got.Domain != "movie" || got.EntityType != "work" {
		t.Fatalf("unexpected assertion dto: %+v", got)
	}
	if got.SourceID != assertion.SourceID || got.RawHash != assertion.RawHash || got.ValidationStatus != l1sqlite.L1StagingStatusValidated {
		t.Fatalf("unexpected source fields: %+v", got)
	}
	if got.Evidence["staging_id"] != assertion.StagingID {
		t.Fatalf("expected evidence in response: %+v", got.Evidence)
	}
	if got.CreatedAt == "" || got.UpdatedAt == "" {
		t.Fatalf("expected timestamps: %+v", got)
	}
}

func TestHandleDomainGraphAssertionsUnavailable(t *testing.T) {
	h := HandleDomainGraphAssertions(nil)
	req := httptest.NewRequest(http.MethodGet, "/viewer/domain-graph/assertions", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "domain graph unavailable") {
		t.Fatalf("unexpected body: %q", rec.Body.String())
	}
}

func TestHandleDomainGraphAssertionsRejectsInvalidQuery(t *testing.T) {
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	h := HandleDomainGraphAssertions(store)
	for _, path := range []string{
		"/viewer/domain-graph/assertions?offset=-1",
		"/viewer/domain-graph/assertions?validation_status=approved",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d: %s", path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "invalid domain graph assertion query") {
			t.Fatalf("%s: unexpected body: %q", path, rec.Body.String())
		}
	}
}

func saveViewerDomainGraphAssertion(t *testing.T, ctx context.Context, store *l1sqlite.L1SQLiteStore) *l1sqlite.L1DomainGraphAssertion {
	t.Helper()
	item, err := store.SaveStagingItem(ctx, l1sqlite.L1StagingItem{
		Kind:         l1sqlite.L1StagingKindExternalFetch,
		Namespace:    "kb:movie",
		EventID:      "viewer-domain-graph",
		SourceID:     "web:eiga",
		SourceURL:    "https://example.com/movie/1",
		FetchedAt:    time.Date(2026, 6, 6, 10, 0, 0, 0, time.UTC),
		RawText:      "viewer domain graph raw text",
		SummaryDraft: "viewer domain graph summary",
		Keywords:     []string{"movie"},
		LicenseNote:  "public catalog; review before promotion",
		Meta:         map[string]interface{}{"title": "viewer-domain-graph"},
	})
	if err != nil {
		t.Fatalf("SaveStagingItem failed: %v", err)
	}
	if _, err := store.ValidateStagingItem(ctx, item.ID, l1sqlite.L1StagingValidationPolicy{
		SourceTrustScores: map[string]float64{"web:eiga": 0.9},
		MinimumTrustScore: 0.5,
		Now:               time.Date(2026, 6, 6, 10, 5, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("ValidateStagingItem failed: %v", err)
	}
	assertion, err := store.PromoteValidatedStagingItemToDomainGraph(ctx, item.ID, "movie", "work", "movie:1", "catalog_fact", 0.7)
	if err != nil {
		t.Fatalf("PromoteValidatedStagingItemToDomainGraph failed: %v", err)
	}
	return assertion
}
