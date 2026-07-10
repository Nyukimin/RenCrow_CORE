package viewer

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type movieDomainGraphStoreStub struct {
	total         int
	items         []l1sqlite.L1DomainGraphAssertion
	relationTotal int
	relationItems []l1sqlite.L1DomainGraphAssertion
	query         l1sqlite.DomainGraphAssertionQuery
	queries       []l1sqlite.DomainGraphAssertionQuery
	err           error
}

func (s *movieDomainGraphStoreStub) DomainGraphAssertions(ctx context.Context, q l1sqlite.DomainGraphAssertionQuery) (int, []l1sqlite.L1DomainGraphAssertion, error) {
	s.query = q
	s.queries = append(s.queries, q)
	if s.err != nil {
		return 0, nil, s.err
	}
	if q.EntityType == "work_person" {
		return s.relationTotal, s.relationItems, nil
	}
	return s.total, s.items, nil
}

func TestHandleMovieDomainGraphSyncUpsertsMovieWorks(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "eiga_catalog.sqlite")
	now := time.Now().UTC()
	store := &movieDomainGraphStoreStub{
		total: 2,
		items: []l1sqlite.L1DomainGraphAssertion{
			{
				ID:               "dg:movie:1",
				Domain:           "movie",
				EntityType:       "work",
				EntityID:         "movie:1",
				SourceURL:        "https://example.com/movie/1",
				Summary:          "Movie summary",
				ValidationStatus: l1sqlite.L1StagingStatusValidated,
				Evidence: map[string]interface{}{
					"title": "Evidence Title",
				},
				CreatedAt: now,
				UpdatedAt: now,
			},
			{
				ID:               "dg:movie:skip",
				Domain:           "movie",
				EntityType:       "work",
				SourceURL:        "https://example.com/movie/skip",
				Summary:          "Skip summary",
				ValidationStatus: l1sqlite.L1StagingStatusValidated,
				CreatedAt:        now,
				UpdatedAt:        now,
			},
		},
	}
	h := HandleMovieDomainGraphSync(MovieCatalogOptions{DBPath: dbPath}, store)

	req := httptest.NewRequest(http.MethodPost, "/viewer/movie-catalog/domain-graph-sync?limit=10", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(store.queries) != 2 {
		t.Fatalf("expected work and relation queries, got %+v", store.queries)
	}
	if store.queries[0].Domain != "movie" || store.queries[0].EntityType != "work" || store.queries[0].ValidationStatus != l1sqlite.L1StagingStatusValidated || store.queries[0].Limit != 10 {
		t.Fatalf("unexpected work query: %+v", store.queries[0])
	}
	if store.queries[1].Domain != "movie" || store.queries[1].EntityType != "work_person" || store.queries[1].ValidationStatus != l1sqlite.L1StagingStatusValidated || store.queries[1].Limit != 10 {
		t.Fatalf("unexpected relation query: %+v", store.queries[1])
	}
	var out movieDomainGraphSyncResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !out.Available || out.DBPath != dbPath || out.Domain != "movie" || out.EntityType != "work" {
		t.Fatalf("unexpected response identity: %+v", out)
	}
	if out.Checked != 2 || out.Upserted != 1 || out.Skipped != 1 {
		t.Fatalf("unexpected counts: %+v", out)
	}
	if len(out.MovieIDs) != 1 || out.MovieIDs[0] != "movie:1" {
		t.Fatalf("unexpected movie ids: %+v", out.MovieIDs)
	}
	if out.SkipReasons["missing_entity_id"] != 1 {
		t.Fatalf("unexpected skip reasons: %+v", out.SkipReasons)
	}

	db, err := sql.Open("sqlite", dbPath+"?_time_format=sqlite")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	var title, url, synopsis string
	if err := db.QueryRow("SELECT title, url, COALESCE(synopsis, '') FROM movies WHERE movie_id = ?", "movie:1").Scan(&title, &url, &synopsis); err != nil {
		t.Fatalf("query synced movie: %v", err)
	}
	if title != "Evidence Title" || url != "https://example.com/movie/1" || synopsis != "Movie summary" {
		t.Fatalf("unexpected synced movie: title=%q url=%q synopsis=%q", title, url, synopsis)
	}
}

func TestHandleMovieDomainGraphSyncUnavailable(t *testing.T) {
	h := HandleMovieDomainGraphSync(MovieCatalogOptions{DBPath: filepath.Join(t.TempDir(), "eiga_catalog.sqlite")}, nil)
	req := httptest.NewRequest(http.MethodPost, "/viewer/movie-catalog/domain-graph-sync", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "movie domain graph sync unavailable") {
		t.Fatalf("unexpected body: %q", rec.Body.String())
	}
}

func TestHandleMovieDomainGraphSyncResolvesMoviePrefixedIDToExistingCatalogID(t *testing.T) {
	dbPath := seedMovieCatalogTestDB(t)
	now := time.Now().UTC()
	store := &movieDomainGraphStoreStub{
		total: 1,
		items: []l1sqlite.L1DomainGraphAssertion{{
			ID:               "dg:movie:57573",
			Domain:           "movie",
			EntityType:       "work",
			EntityID:         "movie:57573",
			SourceURL:        "https://eiga.com/movie/57573/",
			Summary:          "Domain Graph summary",
			ValidationStatus: l1sqlite.L1StagingStatusValidated,
			Evidence: map[string]interface{}{
				"title": "マージン・コール",
			},
			CreatedAt: now,
			UpdatedAt: now,
		}},
	}
	h := HandleMovieDomainGraphSync(MovieCatalogOptions{DBPath: dbPath}, store)

	req := httptest.NewRequest(http.MethodPost, "/viewer/movie-catalog/domain-graph-sync", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out movieDomainGraphSyncResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(out.MovieIDs) != 1 || out.MovieIDs[0] != "57573" {
		t.Fatalf("expected canonical movie id in response, got %+v", out.MovieIDs)
	}
	if out.ResolvedIDs["movie:57573"] != "57573" {
		t.Fatalf("expected resolved id mapping, got %+v", out.ResolvedIDs)
	}

	db, err := sql.Open("sqlite", dbPath+"?_time_format=sqlite")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	var duplicateCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM movies WHERE movie_id = ?", "movie:57573").Scan(&duplicateCount); err != nil {
		t.Fatalf("query duplicate movie: %v", err)
	}
	if duplicateCount != 0 {
		t.Fatalf("expected no movie:57573 duplicate row, got %d", duplicateCount)
	}
	var canonicalTitle string
	if err := db.QueryRow("SELECT title FROM movies WHERE movie_id = ?", "57573").Scan(&canonicalTitle); err != nil {
		t.Fatalf("query canonical movie: %v", err)
	}
	if canonicalTitle != "マージン・コール" {
		t.Fatalf("unexpected canonical title: %q", canonicalTitle)
	}
	var canonicalID, source string
	if err := db.QueryRow("SELECT canonical_movie_id, source FROM movie_id_aliases WHERE alias_id = ?", "movie:57573").Scan(&canonicalID, &source); err != nil {
		t.Fatalf("query alias: %v", err)
	}
	if canonicalID != "57573" || source != "domain_graph_sync" {
		t.Fatalf("unexpected alias row canonical=%q source=%q", canonicalID, source)
	}
}

func TestHandleMovieDomainGraphSyncUpsertsMoviePeopleEdges(t *testing.T) {
	dbPath := seedMovieCatalogTestDB(t)
	now := time.Now().UTC()
	store := &movieDomainGraphStoreStub{
		relationTotal: 2,
		relationItems: []l1sqlite.L1DomainGraphAssertion{
			{
				ID:               "dg:movie:edge:1",
				Domain:           "movie",
				EntityType:       "work_person",
				EntityID:         "movie:57573",
				RelationType:     "actor",
				SourceURL:        "https://eiga.com/movie/57573/",
				Summary:          "Movie person relation",
				ValidationStatus: l1sqlite.L1StagingStatusValidated,
				Evidence: map[string]interface{}{
					"movie_id":    "movie:57573",
					"movie_title": "マージン・コール",
					"person_id":   "person:30003",
					"person_name": "ケビン・スペイシー",
					"person_url":  "https://eiga.com/person/30003/",
				},
				CreatedAt: now,
				UpdatedAt: now,
			},
			{
				ID:               "dg:movie:edge:skip",
				Domain:           "movie",
				EntityType:       "work_person",
				EntityID:         "movie:57573",
				RelationType:     "actor",
				SourceURL:        "https://eiga.com/movie/57573/",
				Summary:          "Missing person",
				ValidationStatus: l1sqlite.L1StagingStatusValidated,
				Evidence: map[string]interface{}{
					"movie_id": "movie:57573",
				},
				CreatedAt: now,
				UpdatedAt: now,
			},
		},
	}
	h := HandleMovieDomainGraphSync(MovieCatalogOptions{DBPath: dbPath}, store)

	req := httptest.NewRequest(http.MethodPost, "/viewer/movie-catalog/domain-graph-sync?limit=25", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(store.queries) != 2 || store.queries[1].EntityType != "work_person" || store.queries[1].Limit != 25 {
		t.Fatalf("unexpected queries: %+v", store.queries)
	}
	var out movieDomainGraphSyncResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if out.RelationChecked != 2 || out.RelationUpserted != 1 || out.RelationSkipped != 1 {
		t.Fatalf("unexpected relation counts: %+v", out)
	}
	if out.RelationSkipReasons["missing_person_id"] != 1 {
		t.Fatalf("unexpected relation skip reasons: %+v", out.RelationSkipReasons)
	}

	db, err := sql.Open("sqlite", dbPath+"?_time_format=sqlite")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	var movieID, personID, role, source, movieTitle, personName, movieURL, personURL string
	if err := db.QueryRow(`
SELECT movie_id, person_id, role, source, COALESCE(movie_title, ''), COALESCE(person_name, ''), COALESCE(movie_url, ''), COALESCE(person_url, '')
FROM movie_people
WHERE movie_id = ? AND person_id = ? AND source = ?`, "57573", "30003", "domain_graph").Scan(&movieID, &personID, &role, &source, &movieTitle, &personName, &movieURL, &personURL); err != nil {
		t.Fatalf("query movie_people edge: %v", err)
	}
	if movieID != "57573" || personID != "30003" || role != "出演" || source != "domain_graph" {
		t.Fatalf("unexpected edge identity: movie=%q person=%q role=%q source=%q", movieID, personID, role, source)
	}
	if movieTitle != "マージン・コール" || personName != "ケビン・スペイシー" || movieURL != "https://eiga.com/movie/57573/" || personURL != "https://eiga.com/person/30003/" {
		t.Fatalf("unexpected edge labels: movieTitle=%q personName=%q movieURL=%q personURL=%q", movieTitle, personName, movieURL, personURL)
	}
}

func TestHandleMovieDomainGraphSyncRejectsInvalidMethod(t *testing.T) {
	h := HandleMovieDomainGraphSync(MovieCatalogOptions{DBPath: filepath.Join(t.TempDir(), "eiga_catalog.sqlite")}, &movieDomainGraphStoreStub{})
	req := httptest.NewRequest(http.MethodGet, "/viewer/movie-catalog/domain-graph-sync", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleMovieDomainGraphSyncRejectsInvalidLimit(t *testing.T) {
	h := HandleMovieDomainGraphSync(MovieCatalogOptions{DBPath: filepath.Join(t.TempDir(), "eiga_catalog.sqlite")}, &movieDomainGraphStoreStub{})
	req := httptest.NewRequest(http.MethodPost, "/viewer/movie-catalog/domain-graph-sync?limit=-1", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid movie domain graph sync request") {
		t.Fatalf("unexpected body: %q", rec.Body.String())
	}
}
