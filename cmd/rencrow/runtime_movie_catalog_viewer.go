package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	moviecatalogapp "github.com/Nyukimin/RenCrow_CORE/internal/application/moviecatalog"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/storagehost"
)

const (
	runtimeMovieCatalogViewerDefaultLimit = 25
	runtimeMovieCatalogViewerMaxLimit     = 50
)

type runtimeMovieCatalogViewerResponse struct {
	Available bool   `json:"available"`
	DBPath    string `json:"db_path"`
	Action    string `json:"action"`
	Total     int    `json:"total,omitempty"`
	Limit     int    `json:"limit,omitempty"`
	Offset    int    `json:"offset,omitempty"`
	Items     any    `json:"items,omitempty"`
	Detail    any    `json:"detail,omitempty"`
	Stats     any    `json:"stats,omitempty"`
	Error     string `json:"error,omitempty"`
}

func runtimeMovieCatalogViewerHandler(options viewer.MovieCatalogOptions, selected *storagehost.MovieCatalogClient) http.HandlerFunc {
	if selected == nil {
		return viewer.HandleMovieCatalog(options)
	}
	return handleRemoteMovieCatalogViewer(selected)
}

func handleRemoteMovieCatalogViewer(owner *storagehost.MovieCatalogClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		action := strings.TrimSpace(r.URL.Query().Get("action"))
		if action == "" {
			action = "movies"
		}
		limit, offset, err := runtimeMovieCatalogViewerPage(r)
		if err != nil {
			http.Error(w, "invalid limit or offset", http.StatusBadRequest)
			return
		}
		response := runtimeMovieCatalogViewerResponse{Available: true, Action: action, Limit: limit, Offset: offset}
		query := moviecatalogapp.QueryParams{
			Query: r.URL.Query().Get("q"), Role: r.URL.Query().Get("role"), Source: r.URL.Query().Get("source"),
		}
		switch action {
		case "stats":
			response.Stats, err = owner.ViewerStats(r.Context())
		case "movies":
			response.Total, response.Items, err = owner.ViewerMovies(r.Context(), query, limit, offset)
		case "people":
			response.Total, response.Items, err = owner.ViewerPeople(r.Context(), query, limit, offset)
		case "cards":
			response.Total, response.Items, err = owner.ViewerCards(r.Context(), limit, offset)
		case "movie":
			response.Detail, err = owner.ViewerMovie(r.Context(), r.URL.Query().Get("id"))
		case "person":
			response.Detail, err = owner.ViewerPerson(r.Context(), r.URL.Query().Get("id"))
		default:
			http.Error(w, "unsupported action", http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, "failed to load movie catalog", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}
}

func runtimeMovieCatalogViewerPage(r *http.Request) (int, int, error) {
	limit := runtimeMovieCatalogViewerDefaultLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			return 0, 0, strconv.ErrSyntax
		}
		if value > runtimeMovieCatalogViewerMaxLimit {
			value = runtimeMovieCatalogViewerMaxLimit
		}
		limit = value
	}
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			return 0, 0, strconv.ErrSyntax
		}
		offset = value
	}
	return limit, offset, nil
}
