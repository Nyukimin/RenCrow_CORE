package viewer

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandleHobbyGraphBootstrapCreatesCommonTables(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "hobby_graph.sqlite")
	bootstrap := HandleHobbyGraphBootstrap(HobbyGraphOptions{DBPath: dbPath})

	req := httptest.NewRequest(http.MethodPost, "/viewer/hobby-graph/bootstrap", nil)
	rec := httptest.NewRecorder()
	bootstrap(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out hobbyGraphResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !out.Available || out.DBPath != dbPath || out.Action != "bootstrap" {
		t.Fatalf("unexpected bootstrap response: %+v", out)
	}
	for _, table := range hobbyGraphTables {
		if out.Stats[table] != 0 {
			t.Fatalf("expected empty table %s, stats=%+v", table, out.Stats)
		}
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	for _, table := range hobbyGraphTables {
		if !hobbyGraphTableExists(db, table) {
			t.Fatalf("expected table %s to exist", table)
		}
	}

	status := HandleHobbyGraph(HobbyGraphOptions{DBPath: dbPath})
	statusRec := httptest.NewRecorder()
	status(statusRec, httptest.NewRequest(http.MethodGet, "/viewer/hobby-graph?action=stats", nil))
	if statusRec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", statusRec.Code, statusRec.Body.String())
	}
	var statusOut hobbyGraphResponse
	if err := json.Unmarshal(statusRec.Body.Bytes(), &statusOut); err != nil {
		t.Fatalf("invalid status json: %v", err)
	}
	if !statusOut.Available || statusOut.Action != "stats" || len(statusOut.Stats) != len(hobbyGraphTables) {
		t.Fatalf("unexpected status response: %+v", statusOut)
	}
}

func TestHandleHobbyGraphMissingDBIsSoftUnavailable(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "missing.sqlite")
	h := HandleHobbyGraph(HobbyGraphOptions{DBPath: dbPath})

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/viewer/hobby-graph?action=stats", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out hobbyGraphResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if out.Available || out.DBPath != dbPath || out.Action != "stats" || out.Error != "hobby graph database not found" {
		t.Fatalf("unexpected unavailable response: %+v", out)
	}
}

func TestHandleHobbyGraphRejectsUnsupportedAction(t *testing.T) {
	h := HandleHobbyGraph(HobbyGraphOptions{DBPath: filepath.Join(t.TempDir(), "missing.sqlite")})

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/viewer/hobby-graph?action=items", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "unsupported action") {
		t.Fatalf("unexpected body: %q", rec.Body.String())
	}
}

func TestHandleHobbyGraphOverviewReturnsRecentRows(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "hobby_graph.sqlite")
	interaction := HandleHobbyGraphInteraction(HobbyGraphOptions{DBPath: dbPath})
	workID := createHobbyGraphTestItem(t, interaction, `{
		"category":"manga",
		"item_type":"work",
		"title":"ダンジョン飯",
		"interaction_type":"read"
	}`)
	creatorID := createHobbyGraphTestItem(t, interaction, `{
		"category":"manga",
		"item_type":"creator",
		"title":"九井諒子",
		"interaction_type":"interested"
	}`)
	relation := HandleHobbyGraphRelation(HobbyGraphOptions{DBPath: dbPath})
	relationRec := httptest.NewRecorder()
	relation(relationRec, httptest.NewRequest(http.MethodPost, "/viewer/hobby-graph/relation", strings.NewReader(`{
		"from_item_id":"`+workID+`",
		"to_item_id":"`+creatorID+`",
		"relation_type":"created_by",
		"source":"manual"
	}`)))
	if relationRec.Code != http.StatusOK {
		t.Fatalf("seed relation expected 200, got %d: %s", relationRec.Code, relationRec.Body.String())
	}
	candidates := HandleHobbyTopicCandidatesGenerate(HobbyGraphOptions{DBPath: dbPath})
	candidateRec := httptest.NewRecorder()
	candidates(candidateRec, httptest.NewRequest(http.MethodPost, "/viewer/hobby-graph/topic-candidates/generate", nil))
	if candidateRec.Code != http.StatusOK {
		t.Fatalf("generate candidates expected 200, got %d: %s", candidateRec.Code, candidateRec.Body.String())
	}

	h := HandleHobbyGraph(HobbyGraphOptions{DBPath: dbPath})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/viewer/hobby-graph?action=overview&limit=5", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out hobbyGraphOverviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !out.Available || out.DBPath != dbPath || out.Action != "overview" {
		t.Fatalf("unexpected overview identity: %+v", out)
	}
	if out.Stats["hobby_items"] != 2 || out.Stats["hobby_relations"] != 1 || out.Stats["hobby_interactions"] != 2 || out.Stats["hobby_topic_candidates"] != 1 {
		t.Fatalf("unexpected stats: %+v", out.Stats)
	}
	if len(out.Items) != 2 || len(out.Relations) != 1 || len(out.Interactions) != 2 || len(out.TopicCandidates) != 1 {
		t.Fatalf("unexpected overview lengths items=%d relations=%d interactions=%d candidates=%d", len(out.Items), len(out.Relations), len(out.Interactions), len(out.TopicCandidates))
	}
	if out.Relations[0].FromTitle != "ダンジョン飯" || out.Relations[0].ToTitle != "九井諒子" || out.Relations[0].RelationType != "created_by" {
		t.Fatalf("unexpected relation overview: %+v", out.Relations[0])
	}
	if out.TopicCandidates[0].TargetTitle != "九井諒子" || out.TopicCandidates[0].Status != "candidate" {
		t.Fatalf("unexpected topic candidate overview: %+v", out.TopicCandidates[0])
	}
}

func TestHandleHobbyGraphOverviewMissingDBIsSoftUnavailable(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "missing.sqlite")
	h := HandleHobbyGraph(HobbyGraphOptions{DBPath: dbPath})

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/viewer/hobby-graph?action=overview", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out hobbyGraphOverviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if out.Available || out.DBPath != dbPath || out.Action != "overview" || out.Error != "hobby graph database not found" {
		t.Fatalf("unexpected unavailable overview: %+v", out)
	}
}

func TestHandleHobbyGraphOverviewRejectsInvalidLimit(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "hobby_graph.sqlite")
	bootstrap := HandleHobbyGraphBootstrap(HobbyGraphOptions{DBPath: dbPath})
	bootstrapRec := httptest.NewRecorder()
	bootstrap(bootstrapRec, httptest.NewRequest(http.MethodPost, "/viewer/hobby-graph/bootstrap", nil))
	if bootstrapRec.Code != http.StatusOK {
		t.Fatalf("bootstrap expected 200, got %d: %s", bootstrapRec.Code, bootstrapRec.Body.String())
	}
	h := HandleHobbyGraph(HobbyGraphOptions{DBPath: dbPath})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/viewer/hobby-graph?action=overview&limit=0", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid hobby graph overview request") {
		t.Fatalf("unexpected body: %q", rec.Body.String())
	}
}

func TestHandleHobbyGraphBootstrapRejectsInvalidMethod(t *testing.T) {
	h := HandleHobbyGraphBootstrap(HobbyGraphOptions{DBPath: filepath.Join(t.TempDir(), "hobby_graph.sqlite")})

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/viewer/hobby-graph/bootstrap", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleHobbyGraphInteractionCreatesItemInteractionAndObservation(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "hobby_graph.sqlite")
	h := HandleHobbyGraphInteraction(HobbyGraphOptions{DBPath: dbPath})
	body := `{
		"category":"manga",
		"item_type":"work",
		"title":"ダンジョン飯",
		"interaction_type":"read",
		"occurred_at":"2026-06-06",
		"source":"manual",
		"source_batch_id":"manual_20260606",
		"rating":5,
		"note":"アニメ版も気になる"
	}`
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodPost, "/viewer/hobby-graph/interaction", strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("run %d expected 200, got %d: %s", i+1, rec.Code, rec.Body.String())
		}
		var out hobbyGraphInteractionResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("run %d invalid json: %v", i+1, err)
		}
		if !out.Available || out.DBPath != dbPath {
			t.Fatalf("run %d unexpected response identity: %+v", i+1, out)
		}
		if out.Item.Category != "manga" || out.Item.ItemType != "work" || out.Item.Title != "ダンジョン飯" || out.Item.NormalizedTitle != "ダンジョン飯" {
			t.Fatalf("run %d unexpected item: %+v", i+1, out.Item)
		}
		if out.Interaction.ItemID != out.Item.ItemID || out.Interaction.InteractionType != "read" || out.Interaction.Rating == nil || *out.Interaction.Rating != 5 {
			t.Fatalf("run %d unexpected interaction: %+v", i+1, out.Interaction)
		}
		if out.Observation.Status != "resolved" || out.Observation.ResolvedItemID != out.Item.ItemID {
			t.Fatalf("run %d unexpected observation: %+v", i+1, out.Observation)
		}
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	for table, want := range map[string]int{
		"hobby_items":              1,
		"hobby_interactions":       1,
		"hobby_title_observations": 1,
	} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != want {
			t.Fatalf("table %s count=%d, want %d", table, count, want)
		}
	}
	var itemID, category, interactionType, originalTitle, status, resolvedItemID string
	if err := db.QueryRow(`
SELECT i.item_id, i.category, e.interaction_type, e.original_title, o.status, o.resolved_item_id
FROM hobby_items i
JOIN hobby_interactions e ON e.item_id = i.item_id
JOIN hobby_title_observations o ON o.resolved_item_id = i.item_id
LIMIT 1`).Scan(&itemID, &category, &interactionType, &originalTitle, &status, &resolvedItemID); err != nil {
		t.Fatalf("query saved graph rows: %v", err)
	}
	if itemID == "" || resolvedItemID != itemID || category != "manga" || interactionType != "read" || originalTitle != "ダンジョン飯" || status != "resolved" {
		t.Fatalf("unexpected saved rows item=%q category=%q interaction=%q title=%q status=%q resolved=%q", itemID, category, interactionType, originalTitle, status, resolvedItemID)
	}
}

func TestHandleHobbyGraphInteractionRejectsInvalidRequest(t *testing.T) {
	h := HandleHobbyGraphInteraction(HobbyGraphOptions{DBPath: filepath.Join(t.TempDir(), "hobby_graph.sqlite")})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/viewer/hobby-graph/interaction", strings.NewReader(`{"category":"manga","rating":9}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid hobby graph interaction request") {
		t.Fatalf("unexpected body: %q", rec.Body.String())
	}
}

func TestHandleHobbyGraphInteractionRejectsInvalidMethod(t *testing.T) {
	h := HandleHobbyGraphInteraction(HobbyGraphOptions{DBPath: filepath.Join(t.TempDir(), "hobby_graph.sqlite")})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/viewer/hobby-graph/interaction", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleHobbyGraphRelationCreatesRelationBetweenExistingItems(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "hobby_graph.sqlite")
	interaction := HandleHobbyGraphInteraction(HobbyGraphOptions{DBPath: dbPath})

	workID := createHobbyGraphTestItem(t, interaction, `{
		"category":"manga",
		"item_type":"work",
		"title":"ダンジョン飯",
		"interaction_type":"read"
	}`)
	creatorID := createHobbyGraphTestItem(t, interaction, `{
		"category":"manga",
		"item_type":"creator",
		"title":"九井諒子",
		"interaction_type":"interested"
	}`)

	body := `{
		"from_item_id":"` + workID + `",
		"to_item_id":"` + creatorID + `",
		"relation_type":"created-by",
		"source":"manual",
		"evidence_url":"https://example.com/source",
		"evidence":{"note":"manual relation"}
	}`
	h := HandleHobbyGraphRelation(HobbyGraphOptions{DBPath: dbPath})
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodPost, "/viewer/hobby-graph/relation", strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("run %d expected 200, got %d: %s", i+1, rec.Code, rec.Body.String())
		}
		var out hobbyGraphRelationResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("run %d invalid json: %v", i+1, err)
		}
		if !out.Available || out.DBPath != dbPath {
			t.Fatalf("run %d unexpected response identity: %+v", i+1, out)
		}
		if out.Relation.RelationID == "" || out.Relation.FromItemID != workID || out.Relation.ToItemID != creatorID || out.Relation.RelationType != "created_by" || out.Relation.Source != "manual" {
			t.Fatalf("run %d unexpected relation: %+v", i+1, out.Relation)
		}
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM hobby_relations").Scan(&count); err != nil {
		t.Fatalf("count hobby_relations: %v", err)
	}
	if count != 1 {
		t.Fatalf("hobby_relations count=%d, want 1", count)
	}
	var relationID, fromItemID, toItemID, relationType, source, evidenceURL, evidenceJSON string
	if err := db.QueryRow(`
SELECT relation_id, from_item_id, to_item_id, relation_type, source, evidence_url, evidence_json
FROM hobby_relations
LIMIT 1`).Scan(&relationID, &fromItemID, &toItemID, &relationType, &source, &evidenceURL, &evidenceJSON); err != nil {
		t.Fatalf("query relation row: %v", err)
	}
	if relationID == "" || fromItemID != workID || toItemID != creatorID || relationType != "created_by" || source != "manual" || evidenceURL != "https://example.com/source" {
		t.Fatalf("unexpected relation row id=%q from=%q to=%q type=%q source=%q url=%q", relationID, fromItemID, toItemID, relationType, source, evidenceURL)
	}
	if !strings.Contains(evidenceJSON, "manual relation") {
		t.Fatalf("unexpected evidence json: %q", evidenceJSON)
	}
}

func TestHandleHobbyGraphRelationRejectsMissingItem(t *testing.T) {
	h := HandleHobbyGraphRelation(HobbyGraphOptions{DBPath: filepath.Join(t.TempDir(), "hobby_graph.sqlite")})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/viewer/hobby-graph/relation", strings.NewReader(`{
		"from_item_id":"hobby_item:missing_from",
		"to_item_id":"hobby_item:missing_to",
		"relation_type":"created_by"
	}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "hobby graph relation item not found") {
		t.Fatalf("unexpected body: %q", rec.Body.String())
	}
}

func TestHandleHobbyGraphRelationRejectsInvalidRequest(t *testing.T) {
	h := HandleHobbyGraphRelation(HobbyGraphOptions{DBPath: filepath.Join(t.TempDir(), "hobby_graph.sqlite")})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/viewer/hobby-graph/relation", strings.NewReader(`{"from_item_id":"hobby_item:x"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid hobby graph relation request") {
		t.Fatalf("unexpected body: %q", rec.Body.String())
	}
}

func TestHandleHobbyGraphRelationRejectsInvalidMethod(t *testing.T) {
	h := HandleHobbyGraphRelation(HobbyGraphOptions{DBPath: filepath.Join(t.TempDir(), "hobby_graph.sqlite")})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/viewer/hobby-graph/relation", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d: %s", rec.Code, rec.Body.String())
	}
}

func createHobbyGraphTestItem(t *testing.T, h http.HandlerFunc, body string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/viewer/hobby-graph/interaction", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("create test item expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out hobbyGraphInteractionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("create test item invalid json: %v", err)
	}
	if out.Item.ItemID == "" {
		t.Fatalf("create test item missing item_id: %+v", out.Item)
	}
	return out.Item.ItemID
}

func hobbyGraphTableExists(db *sql.DB, name string) bool {
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&count)
	return err == nil && count > 0
}
