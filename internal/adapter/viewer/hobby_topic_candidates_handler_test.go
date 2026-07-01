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

func TestHandleHobbyTopicCandidatesGenerateCreatesFollowupRelation(t *testing.T) {
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
		"source":"manual",
		"evidence_url":"https://example.com/dungeon-meshi"
	}`)))
	if relationRec.Code != http.StatusOK {
		t.Fatalf("seed relation expected 200, got %d: %s", relationRec.Code, relationRec.Body.String())
	}

	h := HandleHobbyTopicCandidatesGenerate(HobbyGraphOptions{DBPath: dbPath})
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodPost, "/viewer/hobby-graph/topic-candidates/generate?limit=20", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("run %d expected 200, got %d: %s", i+1, rec.Code, rec.Body.String())
		}
		var out hobbyTopicCandidatesGenerateResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("run %d invalid json: %v", i+1, err)
		}
		if !out.Available || out.DBPath != dbPath || out.Generated != 1 || len(out.CandidateIDs) != 1 {
			t.Fatalf("run %d unexpected response: %+v", i+1, out)
		}
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM hobby_topic_candidates").Scan(&count); err != nil {
		t.Fatalf("count candidates: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one upserted candidate, got %d", count)
	}
	var category, topicType, targetItemID, title, reason, evidenceJSON, status, generatedBy string
	if err := db.QueryRow(`
SELECT category, topic_type, target_item_id, title, reason, evidence_json, status, generated_by
FROM hobby_topic_candidates
LIMIT 1`).Scan(&category, &topicType, &targetItemID, &title, &reason, &evidenceJSON, &status, &generatedBy); err != nil {
		t.Fatalf("query candidate: %v", err)
	}
	if category != "manga" || topicType != "followup_relation" || targetItemID != creatorID || status != "candidate" || generatedBy != "hobby_topic_candidate_generator" {
		t.Fatalf("unexpected candidate identity: category=%q topicType=%q targetItemID=%q status=%q generatedBy=%q", category, topicType, targetItemID, status, generatedBy)
	}
	if !strings.Contains(title, "ダンジョン飯") || !strings.Contains(title, "九井諒子") {
		t.Fatalf("unexpected candidate title: %q", title)
	}
	if !strings.Contains(reason, "read") || !strings.Contains(reason, "created_by") {
		t.Fatalf("unexpected reason: %q", reason)
	}
	var evidence map[string]interface{}
	if err := json.Unmarshal([]byte(evidenceJSON), &evidence); err != nil {
		t.Fatalf("invalid evidence json: %v", err)
	}
	if evidence["source_item_id"] != workID || evidence["target_item_id"] != creatorID || evidence["relation_type"] != "created_by" || evidence["interaction_type"] != "read" {
		t.Fatalf("unexpected evidence: %+v", evidence)
	}
}

func TestHandleHobbyTopicCandidatesGenerateMissingDBIsSoftUnavailable(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "missing.sqlite")
	h := HandleHobbyTopicCandidatesGenerate(HobbyGraphOptions{DBPath: missingPath})
	req := httptest.NewRequest(http.MethodPost, "/viewer/hobby-graph/topic-candidates/generate", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out hobbyTopicCandidatesGenerateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if out.Available || out.DBPath != missingPath || out.Error != "hobby graph database not found" {
		t.Fatalf("unexpected unavailable response: %+v", out)
	}
}

func TestHandleHobbyTopicCandidatesGenerateRejectsInvalidRequest(t *testing.T) {
	h := HandleHobbyTopicCandidatesGenerate(HobbyGraphOptions{DBPath: filepath.Join(t.TempDir(), "missing.sqlite")})
	req := httptest.NewRequest(http.MethodPost, "/viewer/hobby-graph/topic-candidates/generate?limit=0", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid hobby topic candidates request") {
		t.Fatalf("unexpected body: %q", rec.Body.String())
	}
}

func TestHandleHobbyTopicCandidatesGenerateRejectsInvalidMethod(t *testing.T) {
	h := HandleHobbyTopicCandidatesGenerate(HobbyGraphOptions{DBPath: filepath.Join(t.TempDir(), "missing.sqlite")})
	req := httptest.NewRequest(http.MethodGet, "/viewer/hobby-graph/topic-candidates/generate", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d: %s", rec.Code, rec.Body.String())
	}
}
