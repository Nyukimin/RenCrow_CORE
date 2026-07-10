package viewer

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
)

const (
	defaultMovieTopicCandidateLimit = 20
	maxMovieTopicCandidateLimit     = 100
)

type movieTopicCandidatesGenerateResponse struct {
	Available    bool     `json:"available"`
	DBPath       string   `json:"db_path"`
	Generated    int      `json:"generated,omitempty"`
	Skipped      int      `json:"skipped,omitempty"`
	CandidateIDs []string `json:"candidate_ids,omitempty"`
	Error        string   `json:"error,omitempty"`
}

type movieTopicCandidateUpsert struct {
	CandidateID    string
	TopicType      string
	TargetMovieID  string
	TargetPersonID string
	Title          string
	Reason         string
	Evidence       map[string]interface{}
}

func HandleMovieTopicCandidatesGenerate(opts MovieCatalogOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		limit, err := movieTopicCandidateLimit(r)
		if err != nil {
			http.Error(w, "invalid movie topic candidates request", http.StatusBadRequest)
			return
		}
		dbPath := resolveMovieCatalogDBPath(opts.DBPath)
		if dbPath == "" {
			writeMovieTopicCandidatesGenerateJSON(w, movieTopicCandidatesGenerateResponse{
				Available: false,
				DBPath:    strings.TrimSpace(opts.DBPath),
				Error:     "movie catalog database not found",
			})
			return
		}
		db, err := sql.Open("sqlite", dbPath+"?_time_format=sqlite")
		if err != nil {
			http.Error(w, "failed to open movie catalog", http.StatusInternalServerError)
			return
		}
		defer db.Close()
		resp, err := generateMovieTopicCandidates(r.Context(), db, limit)
		if err != nil {
			log.Printf("[MovieCatalog] topic candidate generation failed: %v", err)
			http.Error(w, "failed to generate movie topic candidates", http.StatusInternalServerError)
			return
		}
		resp.Available = true
		resp.DBPath = dbPath
		writeMovieTopicCandidatesGenerateJSON(w, resp)
	}
}

func movieTopicCandidateLimit(r *http.Request) (int, error) {
	limit := defaultMovieTopicCandidateLimit
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid limit")
		}
		if n > maxMovieTopicCandidateLimit {
			n = maxMovieTopicCandidateLimit
		}
		limit = n
	}
	return limit, nil
}

func generateMovieTopicCandidates(ctx context.Context, db *sql.DB, limit int) (movieTopicCandidatesGenerateResponse, error) {
	resp := movieTopicCandidatesGenerateResponse{CandidateIDs: []string{}}
	if err := ensureMovieTopicCandidateTables(ctx, db); err != nil {
		return resp, err
	}
	if !movieCatalogTableExists(db, "movie_watch_events") || !movieCatalogTableExists(db, "movie_people") {
		return resp, nil
	}
	rows, err := db.QueryContext(ctx, `
SELECT we.movie_id,
       COALESCE(NULLIF(wm.title, ''), NULLIF(wmp.movie_title, ''), we.original_title) AS watched_title,
       cmp.movie_id,
       COALESCE(NULLIF(cm.title, ''), NULLIF(cmp.movie_title, ''), cmp.movie_id) AS candidate_title,
       cmp.person_id,
       COALESCE(NULLIF(cmp.person_name, ''), NULLIF(wmp.person_name, ''), cmp.person_id) AS person_name,
       COALESCE(NULLIF(cmp.role, ''), NULLIF(wmp.role, ''), '関係') AS role
FROM movie_watch_events we
JOIN movie_people wmp ON wmp.movie_id = we.movie_id
JOIN movie_people cmp ON cmp.person_id = wmp.person_id AND cmp.movie_id != we.movie_id
LEFT JOIN movies wm ON wm.movie_id = we.movie_id
LEFT JOIN movies cm ON cm.movie_id = cmp.movie_id
WHERE COALESCE(we.movie_id, '') != ''
  AND COALESCE(cmp.movie_id, '') != ''
  AND COALESCE(cmp.person_id, '') != ''
  AND NOT EXISTS (SELECT 1 FROM movie_watch_events wx WHERE wx.movie_id = cmp.movie_id)
GROUP BY 1, 2, 3, 4, 5, 6, 7
ORDER BY MAX(we.created_at) DESC, 4, 6
LIMIT ?`, limit)
	if err != nil {
		return resp, err
	}
	candidates := []movieTopicCandidateUpsert{}
	for rows.Next() {
		var watchedMovieID, watchedTitle, targetMovieID, targetTitle, personID, personName, role string
		if err := rows.Scan(&watchedMovieID, &watchedTitle, &targetMovieID, &targetTitle, &personID, &personName, &role); err != nil {
			return resp, err
		}
		candidates = append(candidates, buildWatchedFollowupMovieTopicCandidate(watchedMovieID, watchedTitle, targetMovieID, targetTitle, personID, personName, role))
	}
	if err := rows.Err(); err != nil {
		return resp, err
	}
	if err := rows.Close(); err != nil {
		return resp, err
	}
	for _, candidate := range candidates {
		if err := upsertMovieTopicCandidate(ctx, db, candidate); err != nil {
			return resp, err
		}
		resp.Generated++
		resp.CandidateIDs = append(resp.CandidateIDs, candidate.CandidateID)
	}
	return resp, nil
}

func ensureMovieTopicCandidateTables(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS movie_topic_candidates(
	candidate_id TEXT PRIMARY KEY,
	topic_type TEXT NOT NULL,
	target_movie_id TEXT,
	target_person_id TEXT,
	title TEXT NOT NULL,
	reason TEXT NOT NULL,
	evidence_json TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'candidate',
	generated_by TEXT NOT NULL,
	generated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	used_at TEXT
)`)
	return err
}

func buildWatchedFollowupMovieTopicCandidate(watchedMovieID string, watchedTitle string, targetMovieID string, targetTitle string, personID string, personName string, role string) movieTopicCandidateUpsert {
	topicType := "watched_followup"
	candidateID := movieTopicCandidateID(topicType, watchedMovieID, targetMovieID, personID, role)
	title := fmt.Sprintf("%sつながりで「%s」を話題にする", personName, targetTitle)
	reason := fmt.Sprintf("見た作品「%s」と同じ人物 %s が%sで関係している", watchedTitle, personName, role)
	return movieTopicCandidateUpsert{
		CandidateID:    candidateID,
		TopicType:      topicType,
		TargetMovieID:  targetMovieID,
		TargetPersonID: personID,
		Title:          title,
		Reason:         reason,
		Evidence: map[string]interface{}{
			"watched_movie_id":   watchedMovieID,
			"watched_title":      watchedTitle,
			"target_movie_id":    targetMovieID,
			"target_movie_title": targetTitle,
			"person_id":          personID,
			"person_name":        personName,
			"role":               role,
			"source":             "movie_catalog",
		},
	}
}

func movieTopicCandidateID(parts ...string) string {
	h := sha1.New()
	_, _ = h.Write([]byte(strings.Join(parts, "\x00")))
	return "movie_topic:" + hex.EncodeToString(h.Sum(nil))[:16]
}

func upsertMovieTopicCandidate(ctx context.Context, db *sql.DB, candidate movieTopicCandidateUpsert) error {
	evidenceJSON, err := json.Marshal(candidate.Evidence)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
INSERT INTO movie_topic_candidates(candidate_id, topic_type, target_movie_id, target_person_id, title, reason, evidence_json, status, generated_by, generated_at)
VALUES(?, ?, ?, ?, ?, ?, ?, 'candidate', 'movie_topic_candidate_generator', CURRENT_TIMESTAMP)
ON CONFLICT(candidate_id) DO UPDATE SET
	topic_type = excluded.topic_type,
	target_movie_id = excluded.target_movie_id,
	target_person_id = excluded.target_person_id,
	title = excluded.title,
	reason = excluded.reason,
	evidence_json = excluded.evidence_json,
	status = excluded.status,
	generated_by = excluded.generated_by,
	generated_at = excluded.generated_at
`, candidate.CandidateID, candidate.TopicType, candidate.TargetMovieID, candidate.TargetPersonID, candidate.Title, candidate.Reason, string(evidenceJSON))
	return err
}

func writeMovieTopicCandidatesGenerateJSON(w http.ResponseWriter, payload movieTopicCandidatesGenerateResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}
