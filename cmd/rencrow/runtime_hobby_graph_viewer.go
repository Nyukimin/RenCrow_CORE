package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	musiccatalogapp "github.com/Nyukimin/RenCrow_CORE/internal/application/musiccatalog"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/storagehost"
)

const (
	runtimeHobbyGraphViewerDefaultLimit = 5
	runtimeHobbyGraphViewerMaxLimit     = 20
)

type runtimeHobbyGraphViewerResponse struct {
	Available       bool                                             `json:"available"`
	DBPath          string                                           `json:"db_path"`
	Action          string                                           `json:"action"`
	Stats           map[string]int                                   `json:"stats,omitempty"`
	Items           []musiccatalogapp.StorageHostOverviewItem        `json:"items,omitempty"`
	Relations       []musiccatalogapp.StorageHostOverviewRelation    `json:"relations,omitempty"`
	Interactions    []musiccatalogapp.StorageHostOverviewInteraction `json:"interactions,omitempty"`
	TopicCandidates []musiccatalogapp.StorageHostTopicCandidate      `json:"topic_candidates,omitempty"`
	Error           string                                           `json:"error,omitempty"`
}

func runtimeHobbyGraphViewerHandler(options viewer.HobbyGraphOptions, selected *storagehost.HobbyGraphClient) http.HandlerFunc {
	if selected == nil {
		return viewer.HandleHobbyGraph(options)
	}
	return handleRemoteHobbyGraphViewer(selected)
}

func handleRemoteHobbyGraphViewer(owner *storagehost.HobbyGraphClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		action := strings.TrimSpace(r.URL.Query().Get("action"))
		if action == "" {
			action = "stats"
		}
		response := runtimeHobbyGraphViewerResponse{Available: true, Action: action}
		switch action {
		case "stats":
			stats, err := owner.ViewerStats(r.Context())
			if err != nil {
				http.Error(w, "failed to load hobby graph", http.StatusInternalServerError)
				return
			}
			response.Stats = stats
		case "overview":
			limit, err := runtimeHobbyGraphViewerLimit(r)
			if err != nil {
				http.Error(w, "invalid hobby graph overview request", http.StatusBadRequest)
				return
			}
			overview, err := owner.ViewerOverview(r.Context(), limit)
			if err != nil {
				http.Error(w, "failed to load hobby graph", http.StatusInternalServerError)
				return
			}
			response.Stats = overview.Stats
			response.Items = overview.Items
			response.Relations = overview.Relations
			response.Interactions = overview.Interactions
			response.TopicCandidates = overview.TopicCandidates
		default:
			http.Error(w, "unsupported action", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}
}

func runtimeHobbyGraphViewerLimit(r *http.Request) (int, error) {
	limit := runtimeHobbyGraphViewerDefaultLimit
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			return 0, strconv.ErrSyntax
		}
		if value > runtimeHobbyGraphViewerMaxLimit {
			value = runtimeHobbyGraphViewerMaxLimit
		}
		limit = value
	}
	return limit, nil
}
