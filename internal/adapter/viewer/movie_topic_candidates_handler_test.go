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

func TestHandleMovieTopicCandidatesGenerateCreatesWatchedFollowup(t *testing.T) {
	dbPath := seedMovieCatalogTestDB(t)
	db, err := sql.Open("sqlite", dbPath+"?_time_format=sqlite")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO movies(movie_id,title,url,synopsis)
VALUES('103262','爆弾','https://eiga.com/movie/103262/','未視聴候補。');
INSERT INTO movie_people(movie_id,person_id,role,source,movie_title,person_name,movie_url,person_url)
VALUES('103262','30003','出演','person_filmography','爆弾','ケビン・スペイシー','https://eiga.com/movie/103262/','https://eiga.com/person/30003/');
CREATE TABLE movie_watch_events(
  event_id TEXT PRIMARY KEY,
  movie_id TEXT,
  original_title TEXT NOT NULL,
  watched_at TEXT,
  source TEXT NOT NULL,
  source_batch_id TEXT,
  note TEXT,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO movie_watch_events(event_id,movie_id,original_title,watched_at,source,source_batch_id,note)
VALUES('watch_1','57573','マージン・コール','2026-06-03','user_list','batch_today','');
`); err != nil {
		db.Close()
		t.Fatalf("seed topic candidate inputs: %v", err)
	}
	db.Close()

	h := HandleMovieTopicCandidatesGenerate(MovieCatalogOptions{DBPath: dbPath})
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/viewer/movie-catalog/topic-candidates/generate?limit=20", nil)
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("run %d expected 200, got %d: %s", i+1, rec.Code, rec.Body.String())
		}
		var out movieTopicCandidatesGenerateResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("run %d invalid json: %v", i+1, err)
		}
		if !out.Available || out.DBPath != dbPath || out.Generated != 1 || len(out.CandidateIDs) != 1 {
			t.Fatalf("run %d unexpected response: %+v", i+1, out)
		}
	}

	db, err = sql.Open("sqlite", dbPath+"?_time_format=sqlite")
	if err != nil {
		t.Fatalf("reopen sqlite: %v", err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM movie_topic_candidates").Scan(&count); err != nil {
		t.Fatalf("count candidates: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one upserted candidate, got %d", count)
	}
	var topicType, targetMovieID, targetPersonID, title, reason, evidenceJSON, status, generatedBy string
	if err := db.QueryRow(`
SELECT topic_type, target_movie_id, target_person_id, title, reason, evidence_json, status, generated_by
FROM movie_topic_candidates
LIMIT 1`).Scan(&topicType, &targetMovieID, &targetPersonID, &title, &reason, &evidenceJSON, &status, &generatedBy); err != nil {
		t.Fatalf("query candidate: %v", err)
	}
	if topicType != "watched_followup" || targetMovieID != "103262" || targetPersonID != "30003" || status != "candidate" || generatedBy != "movie_topic_candidate_generator" {
		t.Fatalf("unexpected candidate identity: topicType=%q targetMovieID=%q targetPersonID=%q status=%q generatedBy=%q", topicType, targetMovieID, targetPersonID, status, generatedBy)
	}
	if !strings.Contains(title, "ケビン・スペイシー") || !strings.Contains(title, "爆弾") {
		t.Fatalf("unexpected candidate title: %q", title)
	}
	if !strings.Contains(reason, "マージン・コール") || !strings.Contains(reason, "出演") {
		t.Fatalf("unexpected reason: %q", reason)
	}
	var evidence map[string]interface{}
	if err := json.Unmarshal([]byte(evidenceJSON), &evidence); err != nil {
		t.Fatalf("invalid evidence json: %v", err)
	}
	if evidence["watched_movie_id"] != "57573" || evidence["target_movie_id"] != "103262" || evidence["person_id"] != "30003" {
		t.Fatalf("unexpected evidence: %+v", evidence)
	}
}

func TestHandleMovieTopicCandidatesGenerateMissingDBIsSoftUnavailable(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "missing.sqlite")
	h := HandleMovieTopicCandidatesGenerate(MovieCatalogOptions{DBPath: missingPath})
	req := httptest.NewRequest(http.MethodPost, "/viewer/movie-catalog/topic-candidates/generate", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out movieTopicCandidatesGenerateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if out.Available || out.Error != "movie catalog database not found" {
		t.Fatalf("unexpected unavailable response: %+v", out)
	}
}

func TestHandleMovieTopicCandidatesGenerateRejectsInvalidMethod(t *testing.T) {
	h := HandleMovieTopicCandidatesGenerate(MovieCatalogOptions{DBPath: filepath.Join(t.TempDir(), "missing.sqlite")})
	req := httptest.NewRequest(http.MethodGet, "/viewer/movie-catalog/topic-candidates/generate", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d: %s", rec.Code, rec.Body.String())
	}
}
