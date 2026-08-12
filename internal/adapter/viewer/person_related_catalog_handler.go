package viewer

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	personrelatedcatalog "github.com/Nyukimin/RenCrow_CORE/internal/application/personrelatedcatalog"
)

// PersonRelatedCatalogProvider is the narrow, read-only Viewer boundary for
// the startup person-related catalog lookup. It accepts the exact movie
// catalog person ID and never exposes database paths or Tool execution.
type PersonRelatedCatalogProvider func(context.Context, string, string, int) (personrelatedcatalog.LookupResult, error)
type PersonRelatedCatalogPeopleProvider func(context.Context, int) ([]personrelatedcatalog.EligiblePerson, error)

type personRelatedCatalogViewerResponse struct {
	Available       bool                                      `json:"available"`
	Status          string                                    `json:"status"`
	PersonID        string                                    `json:"person_id"`
	Category        string                                    `json:"category"`
	Items           []personrelatedcatalog.RelatedCatalogItem `json:"items"`
	SummaryCoverage personrelatedcatalog.SummaryCoverage      `json:"summary_coverage"`
}

func HandlePersonRelatedCatalogPeople(provider PersonRelatedCatalogPeopleProvider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireViewerMethod(w, r, http.MethodGet) {
			return
		}
		limit := 100
		if raw := r.URL.Query().Get("limit"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > 1000 {
				http.Error(w, "invalid limit", http.StatusBadRequest)
				return
			}
			limit = parsed
		}
		if provider == nil {
			writeJSON(w, http.StatusOK, map[string]any{"available": false, "status": "unavailable", "items": []personrelatedcatalog.EligiblePerson{}})
			return
		}
		items, err := provider(r.Context(), limit)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"available": false, "status": "unavailable", "items": []personrelatedcatalog.EligiblePerson{}})
			return
		}
		if items == nil {
			items = []personrelatedcatalog.EligiblePerson{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"available": true, "status": "available", "items": items})
	}
}

var personRelatedCatalogCategories = map[string]struct{}{
	personrelatedcatalog.CategoryMovie: {},
	personrelatedcatalog.CategoryDrama: {},
	personrelatedcatalog.CategoryAward: {},
	personrelatedcatalog.CategoryMusic: {},
	personrelatedcatalog.CategoryAnime: {},
	personrelatedcatalog.CategoryNovel: {},
	personrelatedcatalog.CategoryManga: {},
}

// HandlePersonRelatedCatalog serves the read-only category projection for an
// already selected movie-catalog person. Invalid requests are rejected with
// 400; an unavailable startup lookup is a generic HTTP 200 projection so no
// internal path or database error crosses the Viewer boundary.
func HandlePersonRelatedCatalog(provider PersonRelatedCatalogProvider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireViewerMethod(w, r, http.MethodGet) {
			return
		}

		personID := strings.TrimSpace(r.URL.Query().Get("person_id"))
		category := r.URL.Query().Get("category")
		if personID == "" {
			http.Error(w, "person_id is required", http.StatusBadRequest)
			return
		}
		if _, ok := personRelatedCatalogCategories[category]; !ok {
			http.Error(w, "invalid category", http.StatusBadRequest)
			return
		}
		limit := 20
		if rawLimit := r.URL.Query().Get("limit"); rawLimit != "" {
			parsed, err := strconv.Atoi(rawLimit)
			if err != nil || parsed < 1 || parsed > 50 {
				http.Error(w, "invalid limit", http.StatusBadRequest)
				return
			}
			limit = parsed
		}

		unavailable := func() {
			writeJSON(w, http.StatusOK, personRelatedCatalogViewerResponse{
				Available: false,
				Status:    "unavailable",
				PersonID:  personID,
				Category:  category,
				Items:     []personrelatedcatalog.RelatedCatalogItem{},
			})
		}
		if provider == nil {
			unavailable()
			return
		}
		result, err := provider(r.Context(), personID, category, limit)
		if err != nil {
			unavailable()
			return
		}
		if result.Items == nil {
			result.Items = []personrelatedcatalog.RelatedCatalogItem{}
		}
		writeJSON(w, http.StatusOK, personRelatedCatalogViewerResponse{
			Available:       true,
			Status:          "available",
			PersonID:        personID,
			Category:        category,
			Items:           result.Items,
			SummaryCoverage: result.SummaryCoverage,
		})
	}
}
