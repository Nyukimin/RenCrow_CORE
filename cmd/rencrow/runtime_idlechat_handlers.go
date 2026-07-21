package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
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
		if !d.idleChatOrch.IsChatActive() {
			resetIdleChatTTSQueue()
		}
		if !d.idleChatOrch.IsManualMode() && !d.prepareIdleChatStart(w, r) {
			return
		}
		if err := d.idleChatOrch.StartManualMode(); err != nil {
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
		d.idleChatOrch.StopManualMode()
		resetIdleChatTTSQueue()
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
		if !d.idleChatOrch.IsChatActive() && !d.prepareIdleChatStart(w, r) {
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
		if !d.idleChatOrch.IsChatActive() && !d.prepareIdleChatStart(w, r) {
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
		if !d.idleChatOrch.IsChatActive() && !d.prepareIdleChatStart(w, r) {
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

func (d *Dependencies) prepareIdleChatStart(w http.ResponseWriter, r *http.Request) bool {
	if d.idleChatStartGate == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 650*time.Second)
	defer cancel()
	if err := d.idleChatStartGate.PrepareIdleChatStart(ctx); err != nil {
		var busy *viewer.LLMOpsIdleChatBusyError
		if errors.As(err, &busy) {
			http.Error(w, err.Error(), http.StatusConflict)
			return false
		}
		log.Printf("[IdleChat] llm ops prepare failed: %v", err)
		http.Error(w, "idlechat llm runtime prepare failed", http.StatusBadGateway)
		return false
	}
	return true
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
