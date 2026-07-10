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

type hobbyDomainGraphStoreStub struct {
	itemTotal     int
	itemItems     []l1sqlite.L1DomainGraphAssertion
	relationTotal int
	relationItems []l1sqlite.L1DomainGraphAssertion
	queries       []l1sqlite.DomainGraphAssertionQuery
	err           error
}

func (s *hobbyDomainGraphStoreStub) DomainGraphAssertions(ctx context.Context, q l1sqlite.DomainGraphAssertionQuery) (int, []l1sqlite.L1DomainGraphAssertion, error) {
	s.queries = append(s.queries, q)
	if s.err != nil {
		return 0, nil, s.err
	}
	if q.EntityType == "work_relation" {
		return s.relationTotal, s.relationItems, nil
	}
	return s.itemTotal, s.itemItems, nil
}

func TestHandleHobbyDomainGraphSyncUpsertsWorkItems(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "hobby_graph.sqlite")
	now := time.Now().UTC()
	store := &hobbyDomainGraphStoreStub{
		itemTotal: 2,
		itemItems: []l1sqlite.L1DomainGraphAssertion{
			{
				ID:               "dg:manga:work:1",
				Domain:           "manga",
				EntityType:       "work",
				EntityID:         "manga:dungeon-meshi",
				SourceURL:        "https://example.com/dungeon-meshi",
				Summary:          "迷宮と食事を扱う漫画。",
				ValidationStatus: l1sqlite.L1StagingStatusValidated,
				Evidence: map[string]interface{}{
					"title": "ダンジョン飯",
				},
				CreatedAt: now,
				UpdatedAt: now,
			},
			{
				ID:               "dg:movie:skip",
				Domain:           "movie",
				EntityType:       "work",
				EntityID:         "movie:57573",
				SourceURL:        "https://eiga.com/movie/57573/",
				Summary:          "Movie should skip.",
				ValidationStatus: l1sqlite.L1StagingStatusValidated,
				CreatedAt:        now,
				UpdatedAt:        now,
			},
		},
	}
	h := HandleHobbyDomainGraphSync(HobbyGraphOptions{DBPath: dbPath}, store)

	req := httptest.NewRequest(http.MethodPost, "/viewer/hobby-graph/domain-graph-sync?domain=manga&limit=10", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(store.queries) != 2 {
		t.Fatalf("expected work and relation queries, got %+v", store.queries)
	}
	if store.queries[0].Domain != "manga" || store.queries[0].EntityType != "work" || store.queries[0].ValidationStatus != l1sqlite.L1StagingStatusValidated || store.queries[0].Limit != 10 {
		t.Fatalf("unexpected work query: %+v", store.queries[0])
	}
	if store.queries[1].Domain != "manga" || store.queries[1].EntityType != "work_relation" || store.queries[1].ValidationStatus != l1sqlite.L1StagingStatusValidated || store.queries[1].Limit != 10 {
		t.Fatalf("unexpected relation query: %+v", store.queries[1])
	}
	var out hobbyDomainGraphSyncResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !out.Available || out.DBPath != dbPath || out.Domain != "manga" || out.EntityType != "work" {
		t.Fatalf("unexpected response identity: %+v", out)
	}
	if out.Checked != 2 || out.Upserted != 1 || out.Skipped != 1 || len(out.ItemIDs) != 1 {
		t.Fatalf("unexpected counts: %+v", out)
	}
	if out.SkipReasons["movie_domain"] != 1 {
		t.Fatalf("unexpected skip reasons: %+v", out.SkipReasons)
	}

	db, err := sql.Open("sqlite", dbPath+"?_time_format=sqlite")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	var category, itemType, title, normalizedTitle, externalIDsJSON, metadataJSON string
	if err := db.QueryRow(`
SELECT category, item_type, title, normalized_title, external_ids_json, metadata_json
FROM hobby_items
WHERE item_id = ?`, out.ItemIDs[0]).Scan(&category, &itemType, &title, &normalizedTitle, &externalIDsJSON, &metadataJSON); err != nil {
		t.Fatalf("query hobby item: %v", err)
	}
	if category != "manga" || itemType != "work" || title != "ダンジョン飯" || normalizedTitle != "ダンジョン飯" {
		t.Fatalf("unexpected hobby item category=%q type=%q title=%q normalized=%q", category, itemType, title, normalizedTitle)
	}
	if !strings.Contains(externalIDsJSON, "manga:dungeon-meshi") || !strings.Contains(metadataJSON, "dg:manga:work:1") {
		t.Fatalf("unexpected item metadata external=%q metadata=%q", externalIDsJSON, metadataJSON)
	}
}

func TestHandleHobbyDomainGraphSyncUpsertsWorkRelations(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "hobby_graph.sqlite")
	now := time.Now().UTC()
	store := &hobbyDomainGraphStoreStub{
		relationTotal: 2,
		relationItems: []l1sqlite.L1DomainGraphAssertion{
			{
				ID:               "dg:manga:relation:1",
				Domain:           "manga",
				EntityType:       "work_relation",
				EntityID:         "manga:dungeon-meshi",
				RelationType:     "created_by",
				SourceURL:        "https://example.com/dungeon-meshi",
				Summary:          "ダンジョン飯の作者情報。",
				ValidationStatus: l1sqlite.L1StagingStatusValidated,
				Evidence: map[string]interface{}{
					"work_id":      "manga:dungeon-meshi",
					"work_title":   "ダンジョン飯",
					"creator_id":   "creator:kui-ryoko",
					"creator_name": "九井諒子",
				},
				CreatedAt: now,
				UpdatedAt: now,
			},
			{
				ID:               "dg:manga:relation:skip",
				Domain:           "manga",
				EntityType:       "work_relation",
				EntityID:         "manga:missing-target",
				RelationType:     "created_by",
				ValidationStatus: l1sqlite.L1StagingStatusValidated,
				Evidence: map[string]interface{}{
					"work_id": "manga:missing-target",
				},
				CreatedAt: now,
				UpdatedAt: now,
			},
		},
	}
	h := HandleHobbyDomainGraphSync(HobbyGraphOptions{DBPath: dbPath}, store)

	req := httptest.NewRequest(http.MethodPost, "/viewer/hobby-graph/domain-graph-sync?limit=25", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(store.queries) != 2 || store.queries[1].EntityType != "work_relation" || store.queries[1].Limit != 25 {
		t.Fatalf("unexpected queries: %+v", store.queries)
	}
	var out hobbyDomainGraphSyncResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if out.RelationChecked != 2 || out.RelationUpserted != 1 || out.RelationSkipped != 1 {
		t.Fatalf("unexpected relation counts: %+v", out)
	}
	if out.RelationSkipReasons["missing_target_entity_id"] != 1 {
		t.Fatalf("unexpected relation skip reasons: %+v", out.RelationSkipReasons)
	}

	db, err := sql.Open("sqlite", dbPath+"?_time_format=sqlite")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	var itemCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM hobby_items").Scan(&itemCount); err != nil {
		t.Fatalf("count hobby_items: %v", err)
	}
	if itemCount != 2 {
		t.Fatalf("expected source and target hobby items, got %d", itemCount)
	}
	var relationID, fromItemID, toItemID, relationType, source, evidenceURL, evidenceJSON string
	if err := db.QueryRow(`
SELECT relation_id, from_item_id, to_item_id, relation_type, source, COALESCE(evidence_url, ''), evidence_json
FROM hobby_relations
LIMIT 1`).Scan(&relationID, &fromItemID, &toItemID, &relationType, &source, &evidenceURL, &evidenceJSON); err != nil {
		t.Fatalf("query hobby relation: %v", err)
	}
	if relationID == "" || fromItemID == "" || toItemID == "" || relationType != "created_by" || source != "domain_graph" || evidenceURL != "https://example.com/dungeon-meshi" {
		t.Fatalf("unexpected relation id=%q from=%q to=%q type=%q source=%q url=%q", relationID, fromItemID, toItemID, relationType, source, evidenceURL)
	}
	if !strings.Contains(evidenceJSON, "dg:manga:relation:1") || !strings.Contains(evidenceJSON, "creator:kui-ryoko") {
		t.Fatalf("unexpected relation evidence: %q", evidenceJSON)
	}
}

func TestHandleHobbyDomainGraphSyncUnavailable(t *testing.T) {
	h := HandleHobbyDomainGraphSync(HobbyGraphOptions{DBPath: filepath.Join(t.TempDir(), "hobby_graph.sqlite")}, nil)
	req := httptest.NewRequest(http.MethodPost, "/viewer/hobby-graph/domain-graph-sync", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "hobby domain graph sync unavailable") {
		t.Fatalf("unexpected body: %q", rec.Body.String())
	}
}

func TestHandleHobbyDomainGraphSyncRejectsInvalidMethod(t *testing.T) {
	h := HandleHobbyDomainGraphSync(HobbyGraphOptions{DBPath: filepath.Join(t.TempDir(), "hobby_graph.sqlite")}, &hobbyDomainGraphStoreStub{})
	req := httptest.NewRequest(http.MethodGet, "/viewer/hobby-graph/domain-graph-sync", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleHobbyDomainGraphSyncRejectsInvalidLimit(t *testing.T) {
	h := HandleHobbyDomainGraphSync(HobbyGraphOptions{DBPath: filepath.Join(t.TempDir(), "hobby_graph.sqlite")}, &hobbyDomainGraphStoreStub{})
	req := httptest.NewRequest(http.MethodPost, "/viewer/hobby-graph/domain-graph-sync?limit=-1", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid hobby domain graph sync request") {
		t.Fatalf("unexpected body: %q", rec.Body.String())
	}
}
