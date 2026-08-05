package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	idlechat "github.com/Nyukimin/RenCrow_CORE/internal/application/idlechat"
)

func (d *Dependencies) handleIdleChatStart() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if d.idleChatOrch == nil {
			http.Error(w, "idlechat not enabled", http.StatusNotFound)
			return
		}
		var err error
		if d.idleChatSurfacePresence != nil {
			err = d.idleChatSurfacePresence.StartExplicit()
		} else {
			if !d.idleChatOrch.IsChatActive() {
				resetIdleChatTTSQueue()
			}
			err = d.idleChatOrch.StartManualMode()
		}
		if err != nil {
			if errors.Is(err, errChatSurfacePresent) {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{
			"ok":            true,
			"mode":          d.idleChatOrch.CurrentMode(),
			"manual_mode":   d.idleChatOrch.IsManualMode(),
			"disabled":      d.idleChatOrch.IsDisabled(),
			"chat_active":   d.idleChatOrch.IsChatActive(),
			"current_topic": d.idleChatOrch.CurrentTopic(),
		})
	}
}

func (d *Dependencies) handleIdleChatStop() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if d.idleChatOrch == nil {
			http.Error(w, "idlechat not enabled", http.StatusNotFound)
			return
		}
		if d.idleChatSurfacePresence != nil {
			d.idleChatSurfacePresence.StopExplicit()
		} else {
			d.idleChatOrch.StopManualMode()
			resetIdleChatTTSQueue()
		}
		writeJSON(w, map[string]any{
			"ok":            true,
			"mode":          d.idleChatOrch.CurrentMode(),
			"manual_mode":   d.idleChatOrch.IsManualMode(),
			"disabled":      d.idleChatOrch.IsDisabled(),
			"chat_active":   d.idleChatOrch.IsChatActive(),
			"current_topic": d.idleChatOrch.CurrentTopic(),
		})
	}
}

type surfacePresenceRequest struct {
	ViewerClientID string `json:"viewer_client_id"`
	Surface        string `json:"surface"`
	Action         string `json:"action"`
}

func (d *Dependencies) handleSurfacePresence() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if strings.TrimSpace(r.Header.Get("X-RenCrow-Client")) != "RenCrow_PORTAL" {
			http.Error(w, "surface presence requires RenCrow_PORTAL", http.StatusForbidden)
			return
		}
		profile := strings.ToLower(strings.TrimSpace(r.Header.Get(interactionProfileHeader)))
		if profile != "portal-chat" && profile != "portal-idlechat" {
			http.Error(w, "surface presence profile is not allowed", http.StatusForbidden)
			return
		}
		if d.idleChatSurfacePresence == nil {
			http.Error(w, "idlechat surface presence is unavailable", http.StatusServiceUnavailable)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		var req surfacePresenceRequest
		decoder := json.NewDecoder(r.Body)
		if err := decoder.Decode(&req); err != nil {
			http.Error(w, "invalid surface presence request: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			http.Error(w, "invalid surface presence request", http.StatusBadRequest)
			return
		}
		req.ViewerClientID = strings.TrimSpace(req.ViewerClientID)
		req.Surface = strings.ToLower(strings.TrimSpace(req.Surface))
		req.Action = strings.ToLower(strings.TrimSpace(req.Action))
		if !validSurfacePresenceViewerClientID(req.ViewerClientID) {
			http.Error(w, "viewer_client_id is required and must be at most 256 bytes", http.StatusBadRequest)
			return
		}
		if req.Surface != "chat" && req.Surface != "idlechat" {
			http.Error(w, "surface must be chat or idlechat", http.StatusBadRequest)
			return
		}
		if req.Action != "claim" && req.Action != "heartbeat" && req.Action != "release" {
			http.Error(w, "action must be claim, heartbeat, or release", http.StatusBadRequest)
			return
		}
		expectedSurface := strings.TrimPrefix(profile, "portal-")
		if req.Surface != expectedSurface {
			http.Error(w, "interaction profile does not match surface", http.StatusForbidden)
			return
		}

		snapshot, err := d.idleChatSurfacePresence.Update(req.ViewerClientID, req.Surface, req.Action)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{
			"ok":                      true,
			"surface":                 req.Surface,
			"action":                  req.Action,
			"effective_mode":          snapshot.EffectiveMode,
			"idlechat_active":         snapshot.IdleChatActive,
			"chat_presence_count":     snapshot.ChatPresenceCount,
			"idlechat_presence_count": snapshot.IdleChatPresenceCount,
			"lease_expires_at":        snapshot.LeaseExpiresAt,
		})
	}
}

func (d *Dependencies) handleIdleChatInterrupt() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if d.idleChatOrch == nil {
			http.Error(w, "idlechat not enabled", http.StatusNotFound)
			return
		}
		reason := "viewer_interrupt"
		var req struct {
			Reason           string `json:"reason"`
			Source           string `json:"source"`
			ClientGeneration string `json:"client_generation"`
		}
		if strings.Contains(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
			_ = json.NewDecoder(r.Body).Decode(&req)
		}
		if strings.TrimSpace(req.Reason) != "" {
			reason = strings.TrimSpace(req.Reason)
		}
		d.idleChatOrch.Interrupt(reason)
		resetIdleChatTTSQueue()
		writeJSON(w, map[string]any{
			"ok":            true,
			"interrupted":   true,
			"mode":          d.idleChatOrch.CurrentMode(),
			"manual_mode":   d.idleChatOrch.IsManualMode(),
			"disabled":      d.idleChatOrch.IsDisabled(),
			"chat_active":   d.idleChatOrch.IsChatActive(),
			"current_topic": d.idleChatOrch.CurrentTopic(),
		})
	}
}

func (d *Dependencies) handleIdleChatStatus() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if d.idleChatOrch == nil {
			http.Error(w, "idlechat not enabled", http.StatusNotFound)
			return
		}
		if !d.idleChatOrch.IsChatActive() {
			clearTTSPublicSequenceStateIfNoRoutes()
		}
		activeSessionID, activeTranscript := d.idleChatOrch.ActiveSessionTranscript(100)
		writeJSON(w, map[string]any{
			"ok":                   true,
			"mode":                 d.idleChatOrch.CurrentMode(),
			"manual_mode":          d.idleChatOrch.IsManualMode(),
			"disabled":             d.idleChatOrch.IsDisabled(),
			"chat_active":          d.idleChatOrch.IsChatActive(),
			"current_topic":        d.idleChatOrch.CurrentTopic(),
			"topic_stock_playback": d.idleChatOrch.TopicStockPlaybackSnapshot(),
			"active_session_id":    activeSessionID,
			"active_transcript":    activeTranscript,
			"watchdog":             d.idleChatOrch.WatchdogSnapshot(time.Now().UTC()),
			"word_topic_stock":     d.idleChatOrch.WordTopicStockSnapshot(),
			"forecast_stock":       d.idleChatOrch.ForecastTopicStockSnapshot(),
			"episode_stock":        d.idleChatOrch.StoryEpisodeStockSnapshot(),
			"llm_busy":             d.snapshotLLMBusy(),
			"tts_pending":          snapshotIdleChatTTSPending(),
			"tts_public":           snapshotTTSPublicSessions(),
		})
	}
}

type idleChatPlaybackRequest struct {
	Action string `json:"action"`
	ItemID string `json:"item_id"`
}

func (d *Dependencies) handleIdleChatPlayback() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if d.idleChatOrch == nil {
			http.Error(w, "idlechat not enabled", http.StatusNotFound)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		var req idleChatPlaybackRequest
		decoder := json.NewDecoder(r.Body)
		if err := decoder.Decode(&req); err != nil {
			http.Error(w, "invalid playback request: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			http.Error(w, "invalid playback request", http.StatusBadRequest)
			return
		}
		action := strings.ToLower(strings.TrimSpace(req.Action))
		if action != "play" && action != "next" && action != "previous" {
			http.Error(w, "action must be play, next, or previous", http.StatusBadRequest)
			return
		}
		resetIdleChatTTSQueue()
		snapshot, err := d.idleChatOrch.StartTopicStockPlayback(action, strings.TrimSpace(req.ItemID))
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, idlechat.ErrTopicStockEmpty) || errors.Is(err, idlechat.ErrTopicStockNotFound) || errors.Is(err, idlechat.ErrTopicStockNoPrevious) {
				status = http.StatusConflict
			}
			http.Error(w, err.Error(), status)
			return
		}
		writeJSON(w, map[string]any{
			"ok": true, "playback": snapshot,
			"mode": d.idleChatOrch.CurrentMode(), "chat_active": d.idleChatOrch.IsChatActive(),
			"current_topic": d.idleChatOrch.CurrentTopic(),
		})
	}
}

func (d *Dependencies) handleIdleChatCollection() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if d.idleChatOrch == nil {
			http.Error(w, "idlechat not enabled", http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]any{
			"ok":         true,
			"collection": d.idleChatOrch.DailySeedCollectionSnapshot(time.Now()),
		})
	}
}

func (d *Dependencies) snapshotLLMBusy() llmBusySnapshot {
	if d == nil || d.llmBusyTracker == nil {
		return llmBusySnapshot{}
	}
	return d.llmBusyTracker.Snapshot()
}

func (d *Dependencies) handleIdleChatForecast() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if d.idleChatOrch == nil {
			http.Error(w, "idlechat not enabled", http.StatusNotFound)
			return
		}
		if err := d.idleChatOrch.StartForecastMode(); err != nil {
			status := http.StatusBadRequest
			if strings.Contains(err.Error(), "already active") {
				status = http.StatusConflict
			}
			http.Error(w, err.Error(), status)
			return
		}
		go d.idleChatOrch.RunForecastSession()
		writeJSON(w, map[string]any{
			"ok":            true,
			"mode":          d.idleChatOrch.CurrentMode(),
			"manual_mode":   d.idleChatOrch.IsManualMode(),
			"disabled":      d.idleChatOrch.IsDisabled(),
			"chat_active":   d.idleChatOrch.IsChatActive(),
			"current_topic": d.idleChatOrch.CurrentTopic(),
		})
	}
}

func (d *Dependencies) handleIdleChatStory() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if d.idleChatOrch == nil {
			http.Error(w, "idlechat not enabled", http.StatusNotFound)
			return
		}
		if stock := d.idleChatOrch.StoryEpisodeStockSnapshot(); stock.Enabled && stock.Ready == 0 {
			d.idleChatOrch.RefillStoryEpisodesAsync("viewer_story_request")
			writeJSONStatus(w, http.StatusAccepted, map[string]any{"ok": true, "state": "preparing", "episode_stock": stock})
			return
		}
		if err := d.idleChatOrch.StartStoryMode(); err != nil {
			status := http.StatusBadRequest
			if strings.Contains(err.Error(), "already active") {
				status = http.StatusConflict
			}
			http.Error(w, err.Error(), status)
			return
		}
		go d.idleChatOrch.RunSimpleStorySession()
		writeJSON(w, map[string]any{
			"ok":            true,
			"mode":          d.idleChatOrch.CurrentMode(),
			"manual_mode":   d.idleChatOrch.IsManualMode(),
			"disabled":      d.idleChatOrch.IsDisabled(),
			"chat_active":   d.idleChatOrch.IsChatActive(),
			"current_topic": d.idleChatOrch.CurrentTopic(),
		})
	}
}

func (d *Dependencies) handleIdleChatStorySimple() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if d.idleChatOrch == nil {
			http.Error(w, "idlechat not enabled", http.StatusNotFound)
			return
		}
		if stock := d.idleChatOrch.StoryEpisodeStockSnapshot(); stock.Enabled && stock.Ready == 0 {
			d.idleChatOrch.RefillStoryEpisodesAsync("viewer_story_simple_request")
			writeJSONStatus(w, http.StatusAccepted, map[string]any{"ok": true, "state": "preparing", "episode_stock": stock})
			return
		}
		if err := d.idleChatOrch.StartSimpleStoryMode(); err != nil {
			status := http.StatusBadRequest
			if strings.Contains(err.Error(), "already active") {
				status = http.StatusConflict
			}
			http.Error(w, err.Error(), status)
			return
		}
		go d.idleChatOrch.RunSimpleStorySession()
		writeJSON(w, map[string]any{
			"ok":          true,
			"mode":        d.idleChatOrch.CurrentMode(),
			"disabled":    d.idleChatOrch.IsDisabled(),
			"chat_active": d.idleChatOrch.IsChatActive(),
		})
	}
}

func (d *Dependencies) handleIdleChatEpisodes() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if d.idleChatOrch == nil {
			http.Error(w, "idlechat not enabled", http.StatusNotFound)
			return
		}
		if episodeID := strings.TrimSpace(r.URL.Query().Get("episode_id")); episodeID != "" {
			episode, ok := d.idleChatOrch.StoryEpisode(episodeID)
			if !ok {
				http.Error(w, "episode not found", http.StatusNotFound)
				return
			}
			writeJSON(w, map[string]any{"ok": true, "episode": episode})
			return
		}
		snapshot := d.idleChatOrch.StoryEpisodeStockSnapshot()
		episodes := make([]map[string]any, 0, len(snapshot.Episodes))
		for _, episode := range snapshot.Episodes {
			episodes = append(episodes, map[string]any{
				"episode_id": episode.EpisodeID, "revision": episode.Revision,
				"episode_kind": episode.EpisodeKind, "generation_id": episode.GenerationID,
				"story_title":                episode.StoryTitle,
				"replacement_for_episode_id": episode.ReplacementForEpisodeID,
				"source":                     episode.Source, "reader": episode.Reader, "listener": episode.Listener,
				"story_contract": episode.Contract, "production_status": episode.ProductionStatus,
				"validation": episode.Validation, "utterance_count": len(episode.Turns),
				"fixed_prefix_length": episode.FixedPrefixLength, "repair_from_turn": episode.RepairFromTurn,
				"suffix_regenerations": episode.SuffixRegenerations,
				"play_count":           episode.PlayCount, "last_played_at": episode.LastPlayedAt,
				"created_at": episode.CreatedAt, "updated_at": episode.UpdatedAt,
			})
		}
		writeJSON(w, map[string]any{
			"ok": true, "ready": snapshot.Ready, "target": snapshot.Target, "missing": snapshot.Missing,
			"needs_repair": snapshot.NeedsRepair, "failed": snapshot.Failed, "untitled_ready": snapshot.UntitledReady, "filling": snapshot.Filling,
			"generation_attempts": snapshot.GenerationAttempts, "last_failure_phase": snapshot.LastFailurePhase,
			"last_error": snapshot.LastError, "episodes": episodes,
		})
	}
}

func (d *Dependencies) handleIdleChatEpisodesPrepare() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if d.idleChatOrch == nil {
			http.Error(w, "idlechat not enabled", http.StatusNotFound)
			return
		}
		var request struct {
			Count int `json:"count"`
		}
		if r.Body != nil && r.ContentLength != 0 {
			if err := json.NewDecoder(io.LimitReader(r.Body, 16*1024)).Decode(&request); err != nil {
				http.Error(w, "invalid JSON", http.StatusBadRequest)
				return
			}
		}
		if request.Count < 0 || request.Count > 10 {
			http.Error(w, "count must be between 1 and 10", http.StatusBadRequest)
			return
		}
		jobID := fmt.Sprintf("story-prepare-%d", time.Now().UTC().UnixNano())
		d.idleChatOrch.PrepareStoryEpisodeCountAsync(request.Count, jobID)
		writeJSONStatus(w, http.StatusAccepted, map[string]any{"ok": true, "job_id": jobID, "state": "queued", "episode_stock": d.idleChatOrch.StoryEpisodeStockSnapshot()})
	}
}

func (d *Dependencies) handleIdleChatEpisodesValidate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if d.idleChatOrch == nil {
			http.Error(w, "idlechat not enabled", http.StatusNotFound)
			return
		}
		var request struct {
			EpisodeID string `json:"episode_id"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 16*1024)).Decode(&request); err != nil || strings.TrimSpace(request.EpisodeID) == "" {
			http.Error(w, "episode_id is required", http.StatusBadRequest)
			return
		}
		episode, ok := d.idleChatOrch.StoryEpisode(request.EpisodeID)
		if !ok {
			http.Error(w, "episode not found", http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]any{
			"ok": true, "episode_id": episode.EpisodeID, "valid": episode.Validation.Valid,
			"first_invalid_turn": episode.Validation.FirstInvalidTurn, "errors": episode.Validation.Errors,
			"repair_required": !episode.Validation.Valid, "replacement_requested": !episode.Validation.Valid,
		})
	}
}

func (d *Dependencies) handleIdleChatLogs() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if d.idleChatOrch == nil {
			http.Error(w, "idlechat not enabled", http.StatusNotFound)
			return
		}
		limit := 20
		if s := r.URL.Query().Get("limit"); s != "" {
			if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 200 {
				limit = n
			}
		}
		activeSessionID, activeTranscript := d.idleChatOrch.ActiveSessionTranscript(100)
		writeJSON(w, map[string]any{
			"ok":                true,
			"mode":              d.idleChatOrch.CurrentMode(),
			"manual_mode":       d.idleChatOrch.IsManualMode(),
			"chat_active":       d.idleChatOrch.IsChatActive(),
			"current_topic":     d.idleChatOrch.CurrentTopic(),
			"active_session_id": activeSessionID,
			"active_transcript": activeTranscript,
			"history":           d.idleChatOrch.GetHistory(limit),
		})
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
