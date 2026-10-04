package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	domainglossary "github.com/Nyukimin/RenCrow_CORE/internal/glossary/domain/entity"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/storagehost"
)

type runtimeGlossaryDatabaseViewerItem struct {
	ID          string `json:"id"`
	Term        string `json:"term"`
	Explanation string `json:"explanation"`
	Source      string `json:"source"`
	Category    string `json:"category"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type runtimeGlossaryDatabaseViewerResponse struct {
	Available bool                                `json:"available"`
	Total     int                                 `json:"total"`
	Items     []runtimeGlossaryDatabaseViewerItem `json:"items"`
}

func runtimeGlossaryDatabaseViewerHandler(options viewer.DatabaseViewerOptions, selected *storagehost.GlossaryStoreClient) http.HandlerFunc {
	if selected != nil {
		return handleRemoteGlossaryDatabaseViewer(selected)
	}
	return viewer.HandleGlossaryDatabase(options)
}

func handleRemoteGlossaryDatabaseViewer(owner *storagehost.GlossaryStoreClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		limit := 50
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value <= 0 {
				http.Error(w, "invalid limit", http.StatusBadRequest)
				return
			}
			if value > 200 {
				value = 200
			}
			limit = value
		}
		total, page, err := owner.FindViewerPage(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load glossary", http.StatusInternalServerError)
			return
		}
		items := make([]runtimeGlossaryDatabaseViewerItem, 0, len(page))
		for _, item := range page {
			items = append(items, runtimeGlossaryDatabaseViewerItemFromEntity(item))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(runtimeGlossaryDatabaseViewerResponse{Available: true, Total: total, Items: items})
	}
}

func runtimeGlossaryDatabaseViewerItemFromEntity(item *domainglossary.GlossaryItem) runtimeGlossaryDatabaseViewerItem {
	if item == nil {
		return runtimeGlossaryDatabaseViewerItem{}
	}
	return runtimeGlossaryDatabaseViewerItem{
		ID: item.ID, Term: item.Term, Explanation: item.Explanation, Source: item.Source,
		Category: item.Category, CreatedAt: item.CreatedAt.Format(time.RFC3339Nano), UpdatedAt: item.UpdatedAt.Format(time.RFC3339Nano),
	}
}
