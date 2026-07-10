package viewer

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
)

const (
	defaultHobbyTopicCandidateLimit = 20
	maxHobbyTopicCandidateLimit     = 100
)

type hobbyTopicCandidatesGenerateResponse struct {
	Available    bool     `json:"available"`
	DBPath       string   `json:"db_path"`
	Generated    int      `json:"generated,omitempty"`
	Skipped      int      `json:"skipped,omitempty"`
	CandidateIDs []string `json:"candidate_ids,omitempty"`
	Error        string   `json:"error,omitempty"`
}

type hobbyTopicCandidateUpsert struct {
	CandidateID  string
	Category     string
	TopicType    string
	TargetItemID string
	Title        string
	Reason       string
	Evidence     map[string]interface{}
}

func HandleHobbyTopicCandidatesGenerate(opts HobbyGraphOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		limit, err := hobbyTopicCandidateLimit(r)
		if err != nil {
			http.Error(w, "invalid hobby topic candidates request", http.StatusBadRequest)
			return
		}
		dbPath := resolveHobbyGraphDBPath(opts.DBPath)
		if dbPath == "" {
			writeHobbyTopicCandidatesGenerateJSON(w, hobbyTopicCandidatesGenerateResponse{
				Available: false,
				DBPath:    strings.TrimSpace(opts.DBPath),
				Error:     "hobby graph database not found",
			})
			return
		}
		db, err := sql.Open("sqlite", dbPath+"?_time_format=sqlite")
		if err != nil {
			http.Error(w, "failed to open hobby graph", http.StatusInternalServerError)
			return
		}
		defer db.Close()
		resp, err := generateHobbyTopicCandidates(r.Context(), db, limit)
		if err != nil {
			log.Printf("[HobbyGraph] topic candidate generation failed: %v", err)
			http.Error(w, "failed to generate hobby topic candidates", http.StatusInternalServerError)
			return
		}
		resp.Available = true
		resp.DBPath = dbPath
		writeHobbyTopicCandidatesGenerateJSON(w, resp)
	}
}

func hobbyTopicCandidateLimit(r *http.Request) (int, error) {
	limit := defaultHobbyTopicCandidateLimit
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid limit")
		}
		if n > maxHobbyTopicCandidateLimit {
			n = maxHobbyTopicCandidateLimit
		}
		limit = n
	}
	return limit, nil
}

func generateHobbyTopicCandidates(ctx context.Context, db *sql.DB, limit int) (hobbyTopicCandidatesGenerateResponse, error) {
	resp := hobbyTopicCandidatesGenerateResponse{CandidateIDs: []string{}}
	if err := ensureHobbyGraphTables(ctx, db); err != nil {
		return resp, err
	}
	rows, err := db.QueryContext(ctx, `
SELECT i.interaction_id,
       i.item_id,
       i.category,
       i.interaction_type,
       i.original_title,
       source.item_type,
       source.title,
       r.relation_id,
       r.relation_type,
       r.source,
       COALESCE(r.evidence_url, '') AS evidence_url,
       related.item_id,
       related.item_type,
       related.title
FROM hobby_interactions i
JOIN hobby_items source ON source.item_id = i.item_id
JOIN hobby_relations r ON r.from_item_id = i.item_id
JOIN hobby_items related ON related.item_id = r.to_item_id
WHERE COALESCE(i.item_id, '') != ''
  AND i.interaction_type IN ('watched', 'read', 'listened', 'played', 'cleared', 'attended', 'owned', 'liked', 'interested')
GROUP BY 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14
ORDER BY MAX(i.created_at) DESC, source.title, related.title
LIMIT ?`, limit)
	if err != nil {
		return resp, err
	}
	defer rows.Close()
	candidates := []hobbyTopicCandidateUpsert{}
	for rows.Next() {
		var interactionID, sourceItemID, category, interactionType, originalTitle, sourceItemType, sourceTitle string
		var relationID, relationType, relationSource, evidenceURL, targetItemID, targetItemType, targetTitle string
		if err := rows.Scan(&interactionID, &sourceItemID, &category, &interactionType, &originalTitle, &sourceItemType, &sourceTitle, &relationID, &relationType, &relationSource, &evidenceURL, &targetItemID, &targetItemType, &targetTitle); err != nil {
			return resp, err
		}
		candidates = append(candidates, buildFollowupRelationHobbyTopicCandidate(interactionID, sourceItemID, category, interactionType, originalTitle, sourceItemType, sourceTitle, relationID, relationType, relationSource, evidenceURL, targetItemID, targetItemType, targetTitle))
	}
	if err := rows.Err(); err != nil {
		return resp, err
	}
	for _, candidate := range candidates {
		if err := upsertHobbyTopicCandidate(ctx, db, candidate); err != nil {
			return resp, err
		}
		resp.Generated++
		resp.CandidateIDs = append(resp.CandidateIDs, candidate.CandidateID)
	}
	return resp, nil
}

func buildFollowupRelationHobbyTopicCandidate(interactionID string, sourceItemID string, category string, interactionType string, originalTitle string, sourceItemType string, sourceTitle string, relationID string, relationType string, relationSource string, evidenceURL string, targetItemID string, targetItemType string, targetTitle string) hobbyTopicCandidateUpsert {
	topicType := "followup_relation"
	candidateID := hobbyGraphStableID("hobby_topic", topicType, interactionID, relationID, targetItemID)
	title := fmt.Sprintf("「%s」から%s「%s」を話題にする", sourceTitle, relationType, targetTitle)
	reason := fmt.Sprintf("%sした「%s」と%sで関係している", interactionType, originalTitle, relationType)
	return hobbyTopicCandidateUpsert{
		CandidateID:  candidateID,
		Category:     category,
		TopicType:    topicType,
		TargetItemID: targetItemID,
		Title:        title,
		Reason:       reason,
		Evidence: map[string]interface{}{
			"interaction_id":   interactionID,
			"interaction_type": interactionType,
			"source_item_id":   sourceItemID,
			"source_item_type": sourceItemType,
			"source_title":     sourceTitle,
			"original_title":   originalTitle,
			"relation_id":      relationID,
			"relation_type":    relationType,
			"relation_source":  relationSource,
			"evidence_url":     evidenceURL,
			"target_item_id":   targetItemID,
			"target_item_type": targetItemType,
			"target_title":     targetTitle,
			"source":           "hobby_graph",
		},
	}
}

func upsertHobbyTopicCandidate(ctx context.Context, db *sql.DB, candidate hobbyTopicCandidateUpsert) error {
	evidenceJSON, err := json.Marshal(candidate.Evidence)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
INSERT INTO hobby_topic_candidates(candidate_id, category, topic_type, target_item_id, title, reason, evidence_json, status, generated_by, generated_at)
VALUES(?, ?, ?, ?, ?, ?, ?, 'candidate', 'hobby_topic_candidate_generator', CURRENT_TIMESTAMP)
ON CONFLICT(candidate_id) DO UPDATE SET
	category = excluded.category,
	topic_type = excluded.topic_type,
	target_item_id = excluded.target_item_id,
	title = excluded.title,
	reason = excluded.reason,
	evidence_json = excluded.evidence_json,
	status = excluded.status,
	generated_by = excluded.generated_by,
	generated_at = excluded.generated_at
`, candidate.CandidateID, candidate.Category, candidate.TopicType, candidate.TargetItemID, candidate.Title, candidate.Reason, string(evidenceJSON))
	return err
}

func writeHobbyTopicCandidatesGenerateJSON(w http.ResponseWriter, payload hobbyTopicCandidatesGenerateResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}
