package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
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
			"ok":                true,
			"mode":              d.idleChatOrch.CurrentMode(),
			"manual_mode":       d.idleChatOrch.IsManualMode(),
			"disabled":          d.idleChatOrch.IsDisabled(),
			"chat_active":       d.idleChatOrch.IsChatActive(),
			"current_topic":     d.idleChatOrch.CurrentTopic(),
			"active_session_id": activeSessionID,
			"active_transcript": activeTranscript,
			"watchdog":          d.idleChatOrch.WatchdogSnapshot(time.Now().UTC()),
			"forecast_stock":    d.idleChatOrch.ForecastTopicStockSnapshot(),
			"llm_busy":          d.snapshotLLMBusy(),
			"tts_pending":       snapshotIdleChatTTSPending(),
			"tts_public":        snapshotTTSPublicSessions(),
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
