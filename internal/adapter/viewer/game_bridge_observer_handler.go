package viewer

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

func HandleGameBridgeSessions(store *GameBridgeStore, opts GameBridgeStatusOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		limit, err := parseGameBridgeObserverLimit(r, 20, 100)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		sessions, skipped, err := store.RecentGameBridgeSessions(r.Context(), limit)
		if err != nil {
			writeGameBridgeObserverError(w, err)
			return
		}
		for i := range sessions {
			if strings.TrimSpace(sessions[i].DecisionMode) == "" {
				sessions[i].DecisionMode = defaultGameBridgeDecisionMode(opts)
			}
			if strings.TrimSpace(sessions[i].ResultMode) == "" {
				sessions[i].ResultMode = defaultGameBridgeResultMode(opts)
			}
			if strings.TrimSpace(sessions[i].MemoryMode) == "" {
				sessions[i].MemoryMode = defaultGameBridgeMemoryMode(opts)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":            true,
			"source":        "candidate_log",
			"sessions":      sessions,
			"skipped_count": skipped,
		})
	}
}

func HandleGameBridgeEvents(store *GameBridgeStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		limit, err := parseGameBridgeObserverLimit(r, 50, 500)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		events, skipped, err := store.RecentGameBridgeEventViews(
			r.Context(),
			r.URL.Query().Get("game_id"),
			r.URL.Query().Get("session_id"),
			limit,
		)
		if err != nil {
			writeGameBridgeObserverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":            true,
			"source":        "candidate_log",
			"events":        events,
			"skipped_count": skipped,
		})
	}
}

func parseGameBridgeObserverLimit(r *http.Request, fallback int, max int) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return fallback, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 {
		return 0, fmt.Errorf("limit must be a positive integer")
	}
	if limit > max {
		return max, nil
	}
	return limit, nil
}

func writeGameBridgeObserverError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	message := "game bridge observer unavailable"
	code := "observer_unavailable"
	if errors.Is(err, ErrGameBridgeStoreUnavailable) {
		status = http.StatusServiceUnavailable
		message = "game bridge candidate log unavailable"
		code = "source_unavailable"
	}
	writeJSON(w, status, map[string]any{
		"ok":      false,
		"source":  "candidate_log",
		"error":   code,
		"message": message,
	})
}

func defaultGameBridgeDecisionMode(opts GameBridgeStatusOptions) string {
	if strings.TrimSpace(opts.DecisionMode) != "" {
		return opts.DecisionMode
	}
	return "deterministic_stub"
}

func defaultGameBridgeResultMode(opts GameBridgeStatusOptions) string {
	if strings.TrimSpace(opts.ResultMode) != "" {
		return opts.ResultMode
	}
	return "candidate_ack"
}

func defaultGameBridgeMemoryMode(opts GameBridgeStatusOptions) string {
	if strings.TrimSpace(opts.MemoryMode) != "" {
		return opts.MemoryMode
	}
	return "candidate_only"
}
