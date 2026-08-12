package viewer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	personrelatedcatalog "github.com/Nyukimin/RenCrow_CORE/internal/application/personrelatedcatalog"
)

func TestHandlePersonRelatedCatalogRequiresExactQueryAndBoundedLimit(t *testing.T) {
	called := false
	handler := HandlePersonRelatedCatalog(func(context.Context, string, string, int) (personrelatedcatalog.LookupResult, error) {
		called = true
		return personrelatedcatalog.LookupResult{}, nil
	})

	for _, rawURL := range []string{
		"/viewer/movie-catalog/person-related",
		"/viewer/movie-catalog/person-related?person_id=p1",
		"/viewer/movie-catalog/person-related?person_id=p1&category=unknown",
		"/viewer/movie-catalog/person-related?person_id=p1&category=movie&limit=0",
		"/viewer/movie-catalog/person-related?person_id=p1&category=movie&limit=51",
	} {
		t.Run(rawURL, func(t *testing.T) {
			called = false
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, rawURL, nil))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if called {
				t.Fatal("provider must not be called for invalid query")
			}
		})
	}
}

func TestHandlePersonRelatedCatalogProjectsExactIDSummaryAndCoverage(t *testing.T) {
	var gotID, gotCategory string
	var gotLimit int
	handler := HandlePersonRelatedCatalog(func(_ context.Context, personID, category string, limit int) (personrelatedcatalog.LookupResult, error) {
		gotID, gotCategory, gotLimit = personID, category, limit
		return personrelatedcatalog.LookupResult{
			Items: []personrelatedcatalog.RelatedCatalogItem{
				{
					ItemID:       "work-1",
					Category:     category,
					DisplayName:  "<script>alert(1)</script>",
					NameOriginal: "Original & Name",
					NameJA:       "日本語作品",
					SummaryJA:    "日本語の説明",
					SummaryState: "translated_summary",
				},
			},
			SummaryCoverage: personrelatedcatalog.SummaryCoverage{Ready: 1, Total: 1},
		}, nil
	})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/viewer/movie-catalog/person-related?person_id=person-7&category=manga&limit=17", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if gotID != "person-7" || gotCategory != "manga" || gotLimit != 17 {
		t.Fatalf("provider args=(%q,%q,%d), want=(person-7,manga,17)", gotID, gotCategory, gotLimit)
	}
	var response struct {
		Available       bool                                      `json:"available"`
		Status          string                                    `json:"status"`
		PersonID        string                                    `json:"person_id"`
		Category        string                                    `json:"category"`
		Items           []personrelatedcatalog.RelatedCatalogItem `json:"items"`
		SummaryCoverage personrelatedcatalog.SummaryCoverage      `json:"summary_coverage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.Available || response.Status != "available" || response.PersonID != "person-7" || response.Category != "manga" {
		t.Fatalf("unexpected response header: %+v", response)
	}
	if len(response.Items) != 1 || response.Items[0].DisplayName != "<script>alert(1)</script>" || response.Items[0].NameOriginal != "Original & Name" {
		t.Fatalf("dynamic names were not preserved: %+v", response.Items)
	}
	if !reflect.DeepEqual(response.SummaryCoverage, personrelatedcatalog.SummaryCoverage{Ready: 1, Total: 1}) {
		t.Fatalf("coverage=%+v", response.SummaryCoverage)
	}
	if strings.Contains(rec.Body.String(), "<script>") {
		t.Fatalf("JSON response must not emit raw HTML markup: %s", rec.Body.String())
	}
}

func TestHandlePersonRelatedCatalogFailsClosedWithoutInternalError(t *testing.T) {
	handler := HandlePersonRelatedCatalog(func(context.Context, string, string, int) (personrelatedcatalog.LookupResult, error) {
		return personrelatedcatalog.LookupResult{}, errors.New("sqlite path=/srv/private/hobby.sqlite token=secret")
	})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/viewer/movie-catalog/person-related?person_id=p1&category=movie&limit=1", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d", rec.Code, http.StatusOK)
	}
	var response struct {
		Available bool                                      `json:"available"`
		Status    string                                    `json:"status"`
		PersonID  string                                    `json:"person_id"`
		Category  string                                    `json:"category"`
		Items     []personrelatedcatalog.RelatedCatalogItem `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Available || response.Status != "unavailable" || response.PersonID != "p1" || response.Category != "movie" || response.Items == nil || len(response.Items) != 0 {
		t.Fatalf("unexpected unavailable response: %+v", response)
	}
	if strings.Contains(rec.Body.String(), "private") || strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("internal error leaked: %s", rec.Body.String())
	}
}

func TestHandlePersonRelatedCatalogRejectsNonGET(t *testing.T) {
	handler := HandlePersonRelatedCatalog(nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/viewer/movie-catalog/person-related", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want=%d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandlePersonRelatedCatalogPeopleProjectsOnlyProviderFields(t *testing.T) {
	handler := HandlePersonRelatedCatalogPeople(func(_ context.Context, limit int) ([]personrelatedcatalog.EligiblePerson, error) {
		if limit != 17 {
			t.Fatalf("limit=%d", limit)
		}
		return []personrelatedcatalog.EligiblePerson{{MovieCatalogPersonID: "p1", Name: "役所広司", Familiarity: "known", Sentiment: "like"}}, nil
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/viewer/movie-catalog/person-related/people?limit=17", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"movie_catalog_person_id":"p1"`) || strings.Contains(rec.Body.String(), "db_path") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlePersonRelatedCatalogPeopleAllowsBoundedFullEvaluatedList(t *testing.T) {
	handler := HandlePersonRelatedCatalogPeople(func(_ context.Context, limit int) ([]personrelatedcatalog.EligiblePerson, error) {
		if limit != 1000 {
			t.Fatalf("limit=%d, want 1000", limit)
		}
		return []personrelatedcatalog.EligiblePerson{{MovieCatalogPersonID: "35188", Name: "新海誠", Familiarity: "known", Sentiment: "like"}}, nil
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/viewer/movie-catalog/person-related/people?limit=1000", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"新海誠"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlePersonRelatedCatalogPeopleFailsClosed(t *testing.T) {
	rec := httptest.NewRecorder()
	HandlePersonRelatedCatalogPeople(nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/viewer/movie-catalog/person-related/people", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"available":false`) || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
