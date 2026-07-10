package viewer

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

type HobbyGraphOptions struct {
	DBPath string
}

type hobbyGraphResponse struct {
	Available bool           `json:"available"`
	DBPath    string         `json:"db_path"`
	Action    string         `json:"action"`
	Stats     map[string]int `json:"stats,omitempty"`
	Error     string         `json:"error,omitempty"`
}

type hobbyGraphOverviewResponse struct {
	Available       bool                          `json:"available"`
	DBPath          string                        `json:"db_path"`
	Action          string                        `json:"action"`
	Stats           map[string]int                `json:"stats,omitempty"`
	Items           []hobbyGraphOverviewItemDTO   `json:"items,omitempty"`
	Relations       []hobbyGraphOverviewRelation  `json:"relations,omitempty"`
	Interactions    []hobbyGraphOverviewEventDTO  `json:"interactions,omitempty"`
	TopicCandidates []hobbyGraphTopicCandidateDTO `json:"topic_candidates,omitempty"`
	Error           string                        `json:"error,omitempty"`
}

type hobbyGraphOverviewItemDTO struct {
	ItemID          string `json:"item_id"`
	Category        string `json:"category"`
	ItemType        string `json:"item_type"`
	Title           string `json:"title"`
	NormalizedTitle string `json:"normalized_title"`
	UpdatedAt       string `json:"updated_at"`
}

type hobbyGraphOverviewRelation struct {
	RelationID   string `json:"relation_id"`
	FromItemID   string `json:"from_item_id"`
	FromTitle    string `json:"from_title"`
	ToItemID     string `json:"to_item_id"`
	ToTitle      string `json:"to_title"`
	RelationType string `json:"relation_type"`
	Source       string `json:"source"`
	CreatedAt    string `json:"created_at"`
}

type hobbyGraphOverviewEventDTO struct {
	InteractionID   string `json:"interaction_id"`
	ItemID          string `json:"item_id"`
	Title           string `json:"title"`
	Category        string `json:"category"`
	InteractionType string `json:"interaction_type"`
	Source          string `json:"source"`
	CreatedAt       string `json:"created_at"`
}

type hobbyGraphTopicCandidateDTO struct {
	CandidateID  string `json:"candidate_id"`
	Category     string `json:"category"`
	TopicType    string `json:"topic_type"`
	TargetItemID string `json:"target_item_id"`
	TargetTitle  string `json:"target_title"`
	Title        string `json:"title"`
	Reason       string `json:"reason"`
	Status       string `json:"status"`
	GeneratedBy  string `json:"generated_by"`
	GeneratedAt  string `json:"generated_at"`
}

type hobbyGraphInteractionRequest struct {
	Category        string   `json:"category"`
	ItemType        string   `json:"item_type"`
	Title           string   `json:"title"`
	InteractionType string   `json:"interaction_type"`
	OccurredAt      string   `json:"occurred_at"`
	Source          string   `json:"source"`
	SourceBatchID   string   `json:"source_batch_id"`
	Rating          *float64 `json:"rating"`
	Note            string   `json:"note"`
}

type hobbyGraphInteractionResponse struct {
	Available   bool                     `json:"available"`
	DBPath      string                   `json:"db_path"`
	Item        hobbyGraphItemDTO        `json:"item"`
	Interaction hobbyGraphInteractionDTO `json:"interaction"`
	Observation hobbyGraphObservationDTO `json:"observation"`
}

type hobbyGraphItemDTO struct {
	ItemID          string `json:"item_id"`
	Category        string `json:"category"`
	ItemType        string `json:"item_type"`
	Title           string `json:"title"`
	NormalizedTitle string `json:"normalized_title"`
}

type hobbyGraphInteractionDTO struct {
	InteractionID   string   `json:"interaction_id"`
	ItemID          string   `json:"item_id"`
	Category        string   `json:"category"`
	InteractionType string   `json:"interaction_type"`
	OriginalTitle   string   `json:"original_title"`
	OccurredAt      string   `json:"occurred_at,omitempty"`
	Source          string   `json:"source"`
	SourceBatchID   string   `json:"source_batch_id,omitempty"`
	Rating          *float64 `json:"rating,omitempty"`
	Note            string   `json:"note,omitempty"`
}

type hobbyGraphObservationDTO struct {
	ObservationID   string `json:"observation_id"`
	Category        string `json:"category"`
	OriginalTitle   string `json:"original_title"`
	NormalizedTitle string `json:"normalized_title"`
	Status          string `json:"status"`
	ResolvedItemID  string `json:"resolved_item_id"`
}

type hobbyGraphRelationRequest struct {
	FromItemID   string                 `json:"from_item_id"`
	ToItemID     string                 `json:"to_item_id"`
	RelationType string                 `json:"relation_type"`
	Source       string                 `json:"source"`
	EvidenceURL  string                 `json:"evidence_url"`
	Evidence     map[string]interface{} `json:"evidence"`
}

type hobbyGraphRelationResponse struct {
	Available bool                  `json:"available"`
	DBPath    string                `json:"db_path"`
	Relation  hobbyGraphRelationDTO `json:"relation"`
}

type hobbyGraphRelationDTO struct {
	RelationID   string                 `json:"relation_id"`
	FromItemID   string                 `json:"from_item_id"`
	ToItemID     string                 `json:"to_item_id"`
	RelationType string                 `json:"relation_type"`
	Source       string                 `json:"source"`
	EvidenceURL  string                 `json:"evidence_url,omitempty"`
	Evidence     map[string]interface{} `json:"evidence,omitempty"`
}

var hobbyGraphTables = []string{
	"hobby_items",
	"hobby_relations",
	"hobby_interactions",
	"hobby_title_observations",
	"hobby_preference_signals",
	"hobby_topic_candidates",
	"hobby_collection_runs",
	"hobby_collection_targets",
}

func HandleHobbyGraph(opts HobbyGraphOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		action := strings.TrimSpace(r.URL.Query().Get("action"))
		if action == "" {
			action = "stats"
		}
		if action != "stats" && action != "overview" {
			http.Error(w, "unsupported action", http.StatusBadRequest)
			return
		}
		dbPath := resolveHobbyGraphDBPath(opts.DBPath)
		if dbPath == "" {
			if action == "overview" {
				writeHobbyGraphOverviewJSON(w, hobbyGraphOverviewResponse{
					Available: false,
					DBPath:    strings.TrimSpace(opts.DBPath),
					Action:    action,
					Error:     "hobby graph database not found",
				})
				return
			}
			writeHobbyGraphJSON(w, hobbyGraphResponse{
				Available: false,
				DBPath:    strings.TrimSpace(opts.DBPath),
				Action:    action,
				Error:     "hobby graph database not found",
			})
			return
		}
		db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_time_format=sqlite")
		if err != nil {
			http.Error(w, "failed to open hobby graph", http.StatusInternalServerError)
			return
		}
		defer db.Close()
		if action == "overview" {
			limit, err := hobbyGraphOverviewLimit(r)
			if err != nil {
				http.Error(w, "invalid hobby graph overview request", http.StatusBadRequest)
				return
			}
			overview, err := hobbyGraphOverview(db, limit)
			if err != nil {
				http.Error(w, "failed to load hobby graph", http.StatusInternalServerError)
				return
			}
			overview.Available = true
			overview.DBPath = dbPath
			overview.Action = action
			writeHobbyGraphOverviewJSON(w, overview)
			return
		}
		stats, err := hobbyGraphStats(db)
		if err != nil {
			http.Error(w, "failed to load hobby graph", http.StatusInternalServerError)
			return
		}
		writeHobbyGraphJSON(w, hobbyGraphResponse{
			Available: true,
			DBPath:    dbPath,
			Action:    action,
			Stats:     stats,
		})
	}
}

func HandleHobbyGraphBootstrap(opts HobbyGraphOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		dbPath := resolveHobbyGraphWritableDBPath(opts.DBPath)
		if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
			http.Error(w, "failed to create hobby graph directory", http.StatusInternalServerError)
			return
		}
		db, err := sql.Open("sqlite", dbPath+"?_time_format=sqlite")
		if err != nil {
			http.Error(w, "failed to open hobby graph", http.StatusInternalServerError)
			return
		}
		defer db.Close()
		if err := ensureHobbyGraphTables(r.Context(), db); err != nil {
			http.Error(w, "failed to bootstrap hobby graph", http.StatusInternalServerError)
			return
		}
		stats, err := hobbyGraphStats(db)
		if err != nil {
			http.Error(w, "failed to load hobby graph", http.StatusInternalServerError)
			return
		}
		writeHobbyGraphJSON(w, hobbyGraphResponse{
			Available: true,
			DBPath:    dbPath,
			Action:    "bootstrap",
			Stats:     stats,
		})
	}
}

func HandleHobbyGraphInteraction(opts HobbyGraphOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req hobbyGraphInteractionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid hobby graph interaction request", http.StatusBadRequest)
			return
		}
		if err := normalizeHobbyGraphInteractionRequest(&req); err != nil {
			http.Error(w, "invalid hobby graph interaction request", http.StatusBadRequest)
			return
		}
		dbPath := resolveHobbyGraphWritableDBPath(opts.DBPath)
		if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
			http.Error(w, "failed to create hobby graph directory", http.StatusInternalServerError)
			return
		}
		db, err := sql.Open("sqlite", dbPath+"?_time_format=sqlite")
		if err != nil {
			http.Error(w, "failed to open hobby graph", http.StatusInternalServerError)
			return
		}
		defer db.Close()
		if err := ensureHobbyGraphTables(r.Context(), db); err != nil {
			http.Error(w, "failed to bootstrap hobby graph", http.StatusInternalServerError)
			return
		}
		resp, err := saveHobbyGraphInteraction(r.Context(), db, req)
		if err != nil {
			http.Error(w, "failed to save hobby graph interaction", http.StatusInternalServerError)
			return
		}
		resp.Available = true
		resp.DBPath = dbPath
		writeHobbyGraphInteractionJSON(w, resp)
	}
}

func HandleHobbyGraphRelation(opts HobbyGraphOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req hobbyGraphRelationRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid hobby graph relation request", http.StatusBadRequest)
			return
		}
		if err := normalizeHobbyGraphRelationRequest(&req); err != nil {
			http.Error(w, "invalid hobby graph relation request", http.StatusBadRequest)
			return
		}
		dbPath := resolveHobbyGraphWritableDBPath(opts.DBPath)
		if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
			http.Error(w, "failed to create hobby graph directory", http.StatusInternalServerError)
			return
		}
		db, err := sql.Open("sqlite", dbPath+"?_time_format=sqlite")
		if err != nil {
			http.Error(w, "failed to open hobby graph", http.StatusInternalServerError)
			return
		}
		defer db.Close()
		if err := ensureHobbyGraphTables(r.Context(), db); err != nil {
			http.Error(w, "failed to bootstrap hobby graph", http.StatusInternalServerError)
			return
		}
		resp, notFound, err := saveHobbyGraphRelation(r.Context(), db, req)
		if err != nil {
			http.Error(w, "failed to save hobby graph relation", http.StatusInternalServerError)
			return
		}
		if notFound {
			http.Error(w, "hobby graph relation item not found", http.StatusNotFound)
			return
		}
		resp.Available = true
		resp.DBPath = dbPath
		writeHobbyGraphRelationJSON(w, resp)
	}
}

func normalizeHobbyGraphInteractionRequest(req *hobbyGraphInteractionRequest) error {
	req.Category = normalizeHobbyGraphToken(req.Category)
	req.ItemType = normalizeHobbyGraphToken(req.ItemType)
	req.Title = strings.TrimSpace(req.Title)
	req.InteractionType = normalizeHobbyGraphToken(req.InteractionType)
	req.OccurredAt = strings.TrimSpace(req.OccurredAt)
	req.Source = normalizeHobbyGraphToken(req.Source)
	if req.Source == "" {
		req.Source = "manual"
	}
	req.SourceBatchID = strings.TrimSpace(req.SourceBatchID)
	req.Note = strings.TrimSpace(req.Note)
	if req.Category == "" || req.ItemType == "" || req.Title == "" || req.InteractionType == "" {
		return fmt.Errorf("required field missing")
	}
	if req.Rating != nil && (*req.Rating < 0 || *req.Rating > 5) {
		return fmt.Errorf("invalid rating")
	}
	return nil
}

func normalizeHobbyGraphRelationRequest(req *hobbyGraphRelationRequest) error {
	req.FromItemID = strings.TrimSpace(req.FromItemID)
	req.ToItemID = strings.TrimSpace(req.ToItemID)
	req.RelationType = normalizeHobbyGraphToken(req.RelationType)
	req.Source = normalizeHobbyGraphToken(req.Source)
	if req.Source == "" {
		req.Source = "manual"
	}
	req.EvidenceURL = strings.TrimSpace(req.EvidenceURL)
	if req.Evidence == nil {
		req.Evidence = map[string]interface{}{}
	}
	if req.FromItemID == "" || req.ToItemID == "" || req.RelationType == "" {
		return fmt.Errorf("required field missing")
	}
	return nil
}

func saveHobbyGraphInteraction(ctx context.Context, db *sql.DB, req hobbyGraphInteractionRequest) (hobbyGraphInteractionResponse, error) {
	normalizedTitle := normalizeHobbyGraphTitle(req.Title)
	itemID := hobbyGraphStableID("hobby_item", req.Category, req.ItemType, normalizedTitle)
	interactionID := hobbyGraphStableID("hobby_interaction", itemID, req.InteractionType, req.OccurredAt, req.Source, req.SourceBatchID, req.Note)
	observationID := hobbyGraphStableID("hobby_titleobs", req.Category, normalizedTitle, req.Source, req.SourceBatchID)
	if _, err := db.ExecContext(ctx, `
INSERT INTO hobby_items(item_id, category, item_type, title, normalized_title, external_ids_json, metadata_json, created_at, updated_at)
VALUES(?, ?, ?, ?, ?, '{}', '{}', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
ON CONFLICT(item_id) DO UPDATE SET
	category = excluded.category,
	item_type = excluded.item_type,
	title = excluded.title,
	normalized_title = excluded.normalized_title,
	updated_at = excluded.updated_at
`, itemID, req.Category, req.ItemType, req.Title, normalizedTitle); err != nil {
		return hobbyGraphInteractionResponse{}, err
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO hobby_interactions(interaction_id, item_id, category, interaction_type, original_title, occurred_at, source, source_batch_id, rating, note, created_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(interaction_id) DO UPDATE SET
	item_id = excluded.item_id,
	category = excluded.category,
	interaction_type = excluded.interaction_type,
	original_title = excluded.original_title,
	occurred_at = excluded.occurred_at,
	source = excluded.source,
	source_batch_id = excluded.source_batch_id,
	rating = excluded.rating,
	note = excluded.note
`, interactionID, itemID, req.Category, req.InteractionType, req.Title, nullableString(req.OccurredAt), req.Source, nullableString(req.SourceBatchID), req.Rating, nullableString(req.Note)); err != nil {
		return hobbyGraphInteractionResponse{}, err
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO hobby_title_observations(observation_id, category, original_title, normalized_title, source, source_batch_id, status, resolved_item_id, resolution_note, created_at, resolved_at)
VALUES(?, ?, ?, ?, ?, ?, 'resolved', ?, 'manual interaction registration', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
ON CONFLICT(observation_id) DO UPDATE SET
	original_title = excluded.original_title,
	normalized_title = excluded.normalized_title,
	status = excluded.status,
	resolved_item_id = excluded.resolved_item_id,
	resolution_note = excluded.resolution_note,
	resolved_at = excluded.resolved_at
`, observationID, req.Category, req.Title, normalizedTitle, req.Source, nullableString(req.SourceBatchID), itemID); err != nil {
		return hobbyGraphInteractionResponse{}, err
	}
	return hobbyGraphInteractionResponse{
		Item: hobbyGraphItemDTO{
			ItemID:          itemID,
			Category:        req.Category,
			ItemType:        req.ItemType,
			Title:           req.Title,
			NormalizedTitle: normalizedTitle,
		},
		Interaction: hobbyGraphInteractionDTO{
			InteractionID:   interactionID,
			ItemID:          itemID,
			Category:        req.Category,
			InteractionType: req.InteractionType,
			OriginalTitle:   req.Title,
			OccurredAt:      req.OccurredAt,
			Source:          req.Source,
			SourceBatchID:   req.SourceBatchID,
			Rating:          req.Rating,
			Note:            req.Note,
		},
		Observation: hobbyGraphObservationDTO{
			ObservationID:   observationID,
			Category:        req.Category,
			OriginalTitle:   req.Title,
			NormalizedTitle: normalizedTitle,
			Status:          "resolved",
			ResolvedItemID:  itemID,
		},
	}, nil
}

func saveHobbyGraphRelation(ctx context.Context, db *sql.DB, req hobbyGraphRelationRequest) (hobbyGraphRelationResponse, bool, error) {
	fromExists, err := hobbyGraphItemExists(ctx, db, req.FromItemID)
	if err != nil {
		return hobbyGraphRelationResponse{}, false, err
	}
	toExists, err := hobbyGraphItemExists(ctx, db, req.ToItemID)
	if err != nil {
		return hobbyGraphRelationResponse{}, false, err
	}
	if !fromExists || !toExists {
		return hobbyGraphRelationResponse{}, true, nil
	}
	relationID := hobbyGraphStableID("hobby_relation", req.FromItemID, req.ToItemID, req.RelationType, req.Source)
	evidenceJSON, err := json.Marshal(req.Evidence)
	if err != nil {
		return hobbyGraphRelationResponse{}, false, err
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO hobby_relations(relation_id, from_item_id, to_item_id, relation_type, source, evidence_url, evidence_json, created_at)
VALUES(?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(relation_id) DO UPDATE SET
	from_item_id = excluded.from_item_id,
	to_item_id = excluded.to_item_id,
	relation_type = excluded.relation_type,
	source = excluded.source,
	evidence_url = excluded.evidence_url,
	evidence_json = excluded.evidence_json
`, relationID, req.FromItemID, req.ToItemID, req.RelationType, req.Source, nullableString(req.EvidenceURL), string(evidenceJSON)); err != nil {
		return hobbyGraphRelationResponse{}, false, err
	}
	return hobbyGraphRelationResponse{
		Relation: hobbyGraphRelationDTO{
			RelationID:   relationID,
			FromItemID:   req.FromItemID,
			ToItemID:     req.ToItemID,
			RelationType: req.RelationType,
			Source:       req.Source,
			EvidenceURL:  req.EvidenceURL,
			Evidence:     req.Evidence,
		},
	}, false, nil
}

func hobbyGraphItemExists(ctx context.Context, db *sql.DB, itemID string) (bool, error) {
	var exists int
	err := db.QueryRowContext(ctx, "SELECT 1 FROM hobby_items WHERE item_id = ? LIMIT 1", itemID).Scan(&exists)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return false, err
}

func resolveHobbyGraphDBPath(configured string) string {
	candidates := []string{}
	if env := strings.TrimSpace(os.Getenv("RENCROW_HOBBY_GRAPH_DB")); env != "" {
		candidates = append(candidates, env)
	}
	if configured = strings.TrimSpace(configured); configured != "" {
		candidates = append(candidates, configured)
	}
	candidates = append(candidates, filepath.Join("tmp", "hobby_graph", "hobby_graph.sqlite"))
	for _, p := range candidates {
		if p == "" {
			continue
		}
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

func resolveHobbyGraphWritableDBPath(configured string) string {
	if resolved := resolveHobbyGraphDBPath(configured); resolved != "" {
		return resolved
	}
	if env := strings.TrimSpace(os.Getenv("RENCROW_HOBBY_GRAPH_DB")); env != "" {
		return env
	}
	if configured = strings.TrimSpace(configured); configured != "" {
		return configured
	}
	return filepath.Join("tmp", "hobby_graph", "hobby_graph.sqlite")
}

func ensureHobbyGraphTables(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS hobby_items (
  item_id TEXT PRIMARY KEY,
  category TEXT NOT NULL,
  item_type TEXT NOT NULL,
  title TEXT NOT NULL,
  normalized_title TEXT NOT NULL,
  subtitle TEXT,
  canonical_source TEXT,
  canonical_url TEXT,
  external_ids_json TEXT NOT NULL DEFAULT '{}',
  metadata_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS hobby_relations (
  relation_id TEXT PRIMARY KEY,
  from_item_id TEXT NOT NULL,
  to_item_id TEXT NOT NULL,
  relation_type TEXT NOT NULL,
  source TEXT NOT NULL,
  evidence_url TEXT,
  evidence_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS hobby_interactions (
  interaction_id TEXT PRIMARY KEY,
  item_id TEXT,
  category TEXT NOT NULL,
  interaction_type TEXT NOT NULL,
  original_title TEXT NOT NULL,
  occurred_at TEXT,
  source TEXT NOT NULL,
  source_batch_id TEXT,
  rating REAL,
  note TEXT,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS hobby_title_observations (
  observation_id TEXT PRIMARY KEY,
  category TEXT NOT NULL,
  original_title TEXT NOT NULL,
  normalized_title TEXT NOT NULL,
  source TEXT NOT NULL,
  source_batch_id TEXT,
  status TEXT NOT NULL DEFAULT 'unresolved',
  resolved_item_id TEXT,
  resolution_note TEXT,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  resolved_at TEXT
);
CREATE TABLE IF NOT EXISTS hobby_preference_signals (
  signal_id TEXT PRIMARY KEY,
  category TEXT,
  signal_type TEXT NOT NULL,
  target_item_id TEXT,
  target_label TEXT NOT NULL,
  weight REAL NOT NULL DEFAULT 1.0,
  evidence_json TEXT NOT NULL,
  generated_by TEXT NOT NULL,
  generated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS hobby_topic_candidates (
  candidate_id TEXT PRIMARY KEY,
  category TEXT,
  topic_type TEXT NOT NULL,
  target_item_id TEXT,
  title TEXT NOT NULL,
  reason TEXT NOT NULL,
  evidence_json TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'candidate',
  generated_by TEXT NOT NULL,
  generated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  used_at TEXT
);
CREATE TABLE IF NOT EXISTS hobby_collection_runs (
  run_id TEXT PRIMARY KEY,
  category TEXT,
  reason TEXT NOT NULL,
  trigger_source TEXT NOT NULL,
  trigger_id TEXT,
  started_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  finished_at TEXT,
  status TEXT NOT NULL DEFAULT 'running',
  summary TEXT
);
CREATE TABLE IF NOT EXISTS hobby_collection_targets (
  run_id TEXT NOT NULL,
  target_url TEXT NOT NULL,
  target_kind TEXT NOT NULL,
  target_id TEXT,
  reason TEXT NOT NULL,
  parent_kind TEXT,
  parent_id TEXT,
  status TEXT NOT NULL DEFAULT 'pending',
  fetched_at TEXT,
  error TEXT,
  PRIMARY KEY(run_id, target_url)
)`)
	return err
}

func hobbyGraphStats(db *sql.DB) (map[string]int, error) {
	out := map[string]int{}
	for _, table := range hobbyGraphTables {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			return nil, err
		}
		out[table] = n
	}
	return out, nil
}

func hobbyGraphOverviewLimit(r *http.Request) (int, error) {
	limit := 5
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid limit")
		}
		if n > 20 {
			n = 20
		}
		limit = n
	}
	return limit, nil
}

func hobbyGraphOverview(db *sql.DB, limit int) (hobbyGraphOverviewResponse, error) {
	stats, err := hobbyGraphStats(db)
	if err != nil {
		return hobbyGraphOverviewResponse{}, err
	}
	items, err := hobbyGraphOverviewItems(db, limit)
	if err != nil {
		return hobbyGraphOverviewResponse{}, err
	}
	relations, err := hobbyGraphOverviewRelations(db, limit)
	if err != nil {
		return hobbyGraphOverviewResponse{}, err
	}
	interactions, err := hobbyGraphOverviewInteractions(db, limit)
	if err != nil {
		return hobbyGraphOverviewResponse{}, err
	}
	candidates, err := hobbyGraphOverviewTopicCandidates(db, limit)
	if err != nil {
		return hobbyGraphOverviewResponse{}, err
	}
	return hobbyGraphOverviewResponse{
		Stats:           stats,
		Items:           items,
		Relations:       relations,
		Interactions:    interactions,
		TopicCandidates: candidates,
	}, nil
}

func hobbyGraphOverviewItems(db *sql.DB, limit int) ([]hobbyGraphOverviewItemDTO, error) {
	rows, err := db.Query(`
SELECT item_id, category, item_type, title, normalized_title, updated_at
FROM hobby_items
ORDER BY updated_at DESC, created_at DESC, title
LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []hobbyGraphOverviewItemDTO{}
	for rows.Next() {
		var item hobbyGraphOverviewItemDTO
		if err := rows.Scan(&item.ItemID, &item.Category, &item.ItemType, &item.Title, &item.NormalizedTitle, &item.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func hobbyGraphOverviewRelations(db *sql.DB, limit int) ([]hobbyGraphOverviewRelation, error) {
	rows, err := db.Query(`
SELECT r.relation_id,
       r.from_item_id,
       COALESCE(fi.title, r.from_item_id) AS from_title,
       r.to_item_id,
       COALESCE(ti.title, r.to_item_id) AS to_title,
       r.relation_type,
       r.source,
       r.created_at
FROM hobby_relations r
LEFT JOIN hobby_items fi ON fi.item_id = r.from_item_id
LEFT JOIN hobby_items ti ON ti.item_id = r.to_item_id
ORDER BY r.created_at DESC, r.relation_id
LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []hobbyGraphOverviewRelation{}
	for rows.Next() {
		var relation hobbyGraphOverviewRelation
		if err := rows.Scan(&relation.RelationID, &relation.FromItemID, &relation.FromTitle, &relation.ToItemID, &relation.ToTitle, &relation.RelationType, &relation.Source, &relation.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, relation)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func hobbyGraphOverviewInteractions(db *sql.DB, limit int) ([]hobbyGraphOverviewEventDTO, error) {
	rows, err := db.Query(`
SELECT i.interaction_id,
       COALESCE(i.item_id, '') AS item_id,
       COALESCE(h.title, i.original_title) AS title,
       i.category,
       i.interaction_type,
       i.source,
       i.created_at
FROM hobby_interactions i
LEFT JOIN hobby_items h ON h.item_id = i.item_id
ORDER BY i.created_at DESC, i.interaction_id
LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []hobbyGraphOverviewEventDTO{}
	for rows.Next() {
		var event hobbyGraphOverviewEventDTO
		if err := rows.Scan(&event.InteractionID, &event.ItemID, &event.Title, &event.Category, &event.InteractionType, &event.Source, &event.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func hobbyGraphOverviewTopicCandidates(db *sql.DB, limit int) ([]hobbyGraphTopicCandidateDTO, error) {
	rows, err := db.Query(`
SELECT c.candidate_id,
       COALESCE(c.category, '') AS category,
       c.topic_type,
       COALESCE(c.target_item_id, '') AS target_item_id,
       COALESCE(h.title, c.target_item_id, '') AS target_title,
       c.title,
       c.reason,
       c.status,
       c.generated_by,
       c.generated_at
FROM hobby_topic_candidates c
LEFT JOIN hobby_items h ON h.item_id = c.target_item_id
ORDER BY c.generated_at DESC, c.candidate_id
LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []hobbyGraphTopicCandidateDTO{}
	for rows.Next() {
		var candidate hobbyGraphTopicCandidateDTO
		if err := rows.Scan(&candidate.CandidateID, &candidate.Category, &candidate.TopicType, &candidate.TargetItemID, &candidate.TargetTitle, &candidate.Title, &candidate.Reason, &candidate.Status, &candidate.GeneratedBy, &candidate.GeneratedAt); err != nil {
			return nil, err
		}
		out = append(out, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func normalizeHobbyGraphToken(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.ReplaceAll(value, "-", "_")
	value = strings.ReplaceAll(value, " ", "_")
	return value
}

func normalizeHobbyGraphTitle(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "　", " ")
	value = strings.Join(strings.Fields(value), " ")
	return strings.ToLower(value)
}

func hobbyGraphStableID(prefix string, parts ...string) string {
	h := sha1.New()
	_, _ = h.Write([]byte(strings.Join(parts, "\x00")))
	return prefix + ":" + hex.EncodeToString(h.Sum(nil))[:16]
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func writeHobbyGraphJSON(w http.ResponseWriter, payload hobbyGraphResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func writeHobbyGraphOverviewJSON(w http.ResponseWriter, payload hobbyGraphOverviewResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func writeHobbyGraphInteractionJSON(w http.ResponseWriter, payload hobbyGraphInteractionResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func writeHobbyGraphRelationJSON(w http.ResponseWriter, payload hobbyGraphRelationResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}
