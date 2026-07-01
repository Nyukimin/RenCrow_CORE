package chrome

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	entryadapter "github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/entry"
	channelapp "github.com/Nyukimin/picoclaw_multiLLM/internal/application/channel"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/orchestrator"
)

type BridgeRequest struct {
	RequestID string `json:"request_id,omitempty"`
	UserID    string `json:"user_id"`
	SessionID string `json:"session_id,omitempty"`
	Message   string `json:"message"`
}

func HandleBridge(process entryadapter.Processor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req BridgeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		req.Message = strings.TrimSpace(req.Message)
		if req.Message == "" {
			http.Error(w, "message is required", http.StatusBadRequest)
			return
		}
		userID := strings.TrimSpace(req.UserID)
		if userID == "" {
			userID = "anonymous"
		}
		sessionID := strings.TrimSpace(req.SessionID)
		if sessionID == "" {
			sessionID = channelapp.BuildSessionID(time.Now().UTC(), "local", userID)
		}
		requestID := strings.TrimSpace(req.RequestID)
		if requestID == "" {
			requestID = fmt.Sprintf("req-%d", time.Now().UTC().UnixNano())
		}
		acceptedAt := time.Now().UTC().Format(time.RFC3339)
		result, err := process(r.Context(), entryadapter.Request{
			Platform:  "chrome",
			Channel:   "local",
			UserID:    userID,
			SessionID: sessionID,
			Message:   req.Message,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":           true,
			"request_id":   requestID,
			"accepted_at":  acceptedAt,
			"session_id":   result.SessionID,
			"route":        result.Route,
			"job_id":       result.JobID,
			"response":     result.Response,
			"evidence_ref": result.EvidenceRef,
		})
	}
}

func HandleBridgeStatus(history func() []orchestrator.OrchestratorEvent, now func() time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
		if sessionID == "" {
			http.Error(w, "session_id is required", http.StatusBadRequest)
			return
		}
		events := history()
		stage := "unknown"
		route := ""
		jobID := ""
		for i := len(events) - 1; i >= 0; i-- {
			ev := events[i]
			if ev.SessionID != sessionID || ev.Type != "entry.stage" {
				continue
			}
			stage = strings.TrimSpace(ev.Content)
			route = ev.Route
			jobID = ev.JobID
			break
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":         true,
			"timestamp":  now().Format(time.RFC3339),
			"component":  "chrome.bridge",
			"session_id": sessionID,
			"stage":      stage,
			"route":      route,
			"job_id":     jobID,
		})
	}
}

type eventStream interface {
	History() []orchestrator.OrchestratorEvent
	Subscribe() chan []byte
	Unsubscribe(ch chan []byte)
}

func HandleBridgeEvents(stream eventStream) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
		if sessionID == "" {
			http.Error(w, "session_id is required", http.StatusBadRequest)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		lastSeen := parseLastEventID(r.Header.Get("Last-Event-ID"))

		for _, ev := range stream.History() {
			if ev.SessionID != sessionID {
				continue
			}
			if ev.Seq > 0 && ev.Seq <= lastSeen {
				continue
			}
			data, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			if ev.Seq > 0 {
				fmt.Fprintf(w, "id: %d\n", ev.Seq)
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
		}
		flusher.Flush()

		ch := stream.Subscribe()
		defer stream.Unsubscribe(ch)
		for {
			select {
			case <-r.Context().Done():
				return
			case data := <-ch:
				var ev orchestrator.OrchestratorEvent
				if err := json.Unmarshal(bytes.TrimSpace(data), &ev); err != nil {
					continue
				}
				if ev.SessionID != sessionID {
					continue
				}
				if ev.Seq > 0 {
					fmt.Fprintf(w, "id: %d\n", ev.Seq)
				}
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
			}
		}
	}
}

func parseLastEventID(v string) int64 {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
