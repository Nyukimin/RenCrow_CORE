package chrome

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	entryadapter "github.com/Nyukimin/RenCrow_CORE/internal/adapter/entry"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
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
			"task_id":      result.TaskID,
			"response":     result.Response,
			"evidence_ref": result.EvidenceRef,
		})
	}
}

func HandleBridgeStatus(history func() []orchestrator.OrchestratorEvent, now func() time.Time) http.HandlerFunc {
	return HandleBridgeStatusWithSessionAccess(history, now, nil)
}

// SessionEventAccessFilter authorizes one event against its immutable issuance
// identity and the current canonical Session binding.
type SessionEventAccessFilter func(context.Context, orchestrator.OrchestratorEvent) (bool, error)

// SessionAccessFilter admits a requested Session selector before Chrome bridge
// handlers inspect the shared event stream.
type SessionAccessFilter func(context.Context, string) (bool, error)

// HandleBridgeStatusWithSessionAccess exposes status only after the trusted
// runtime confirms access to the canonical session bound to the request. It
// has no event-level publication proof, so it returns the existing unknown
// stage until a caller supplies one through HandleBridgeStatusWithSessionEventAccess.
func HandleBridgeStatusWithSessionAccess(history func() []orchestrator.OrchestratorEvent, now func() time.Time, sessionAccess SessionAccessFilter) http.HandlerFunc {
	return HandleBridgeStatusWithSessionEventAccess(history, now, sessionAccess, nil)
}

// HandleBridgeStatusWithSessionEventAccess checks every candidate against its
// event-time Session publication identity before projecting stage, route, or
// task details. A missing or failing event policy leaves status at "unknown".
func HandleBridgeStatusWithSessionEventAccess(history func() []orchestrator.OrchestratorEvent, now func() time.Time, sessionAccess SessionAccessFilter, eventAccess SessionEventAccessFilter) http.HandlerFunc {
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
		if sessionAccess == nil {
			http.Error(w, "session access unavailable", http.StatusServiceUnavailable)
			return
		}
		allowed, err := sessionAccess(r.Context(), sessionID)
		if err != nil {
			http.Error(w, "session access unavailable", http.StatusServiceUnavailable)
			return
		}
		if !allowed {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		events := history()
		stage := "unknown"
		route := ""
		taskID := ""
		for i := len(events) - 1; i >= 0; i-- {
			ev := events[i]
			if string(ev.SessionID) != sessionID || ev.Type != "entry.stage" {
				continue
			}
			allowed, _ := checkSessionEventAccess(r.Context(), eventAccess, ev)
			if !allowed {
				continue
			}
			stage = strings.TrimSpace(ev.Content)
			route = ev.Route
			taskID = ev.TaskID.String()
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
			"task_id":    taskID,
		})
	}
}

func HandleBridgeEvents(stream viewer.EventStream) http.HandlerFunc {
	return HandleBridgeEventsWithSessionAccess(stream, nil)
}

// HandleBridgeEventsWithSessionAccess requires a trusted runtime session
// policy before touching the global event stream. Without an event-level
// publication proof it emits no events; use
// HandleBridgeEventsWithSessionEventAccess for visible candidates.
func HandleBridgeEventsWithSessionAccess(stream viewer.EventStream, sessionAccess SessionAccessFilter) http.HandlerFunc {
	return HandleBridgeEventsWithSessionEventAccess(stream, sessionAccess, nil)
}

// HandleBridgeEventsWithSessionEventAccess rechecks selector admission and
// validates each matching replayed or live event immediately before writing.
// Event identities without a trusted current-binding decision fail closed.
func HandleBridgeEventsWithSessionEventAccess(stream viewer.EventStream, sessionAccess SessionAccessFilter, eventAccess SessionEventAccessFilter) http.HandlerFunc {
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
		if sessionAccess == nil {
			http.Error(w, "session access unavailable", http.StatusServiceUnavailable)
			return
		}
		allowed, err := sessionAccess(r.Context(), sessionID)
		if err != nil {
			http.Error(w, "session access unavailable", http.StatusServiceUnavailable)
			return
		}
		if !allowed {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		lastSeen, err := parseLastEventID(r.Header.Get("Last-Event-ID"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
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
		ch, history := stream.Subscribe()
		defer stream.Unsubscribe(ch)
		clearReplayDeadline := viewer.SetReplayWriteDeadline(w)
		defer clearReplayDeadline()

		replayWrote := false
		replayAccessFailed := false
		replayCursor, err := stream.Replay(r.Context(), lastSeen, history, func(ev orchestrator.OrchestratorEvent) error {
			if string(ev.SessionID) != sessionID {
				return nil
			}
			allowed, accessErr := sessionAccess(r.Context(), sessionID)
			if accessErr != nil {
				replayAccessFailed = true
				return accessErr
			}
			if !allowed {
				return nil
			}
			allowed, accessErr = checkSessionEventAccess(r.Context(), eventAccess, ev)
			if accessErr != nil {
				replayAccessFailed = true
				return accessErr
			}
			if !allowed || viewer.IsTransientReplayEvent(ev) {
				return nil
			}
			data, err := json.Marshal(ev)
			if err != nil {
				return err
			}
			replayWrote = true
			if ev.EventSeq > 0 {
				if _, err := fmt.Fprintf(w, "id: %d\n", ev.EventSeq); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				return err
			}
			return nil
		})
		if err != nil {
			if r.Context().Err() != nil {
				return
			}
			if replayAccessFailed {
				if !replayWrote {
					http.Error(w, "session access unavailable", http.StatusServiceUnavailable)
				}
				return
			}
			log.Printf("[Chrome] bridge SSE replay failed: %v", err)
			if !replayWrote {
				http.Error(w, "event replay unavailable", http.StatusConflict)
			}
			return
		}
		flusher.Flush()
		clearReplayDeadline()

		for {
			select {
			case <-r.Context().Done():
				return
			case data, ok := <-ch:
				if !ok {
					return
				}
				var ev orchestrator.OrchestratorEvent
				if err := json.Unmarshal(bytes.TrimSpace(data), &ev); err != nil {
					continue
				}
				if string(ev.SessionID) != sessionID {
					continue
				}
				if ev.EventSeq > 0 && ev.EventSeq <= replayCursor {
					continue
				}
				allowed, accessErr := sessionAccess(r.Context(), sessionID)
				if accessErr != nil {
					return
				}
				if !allowed {
					continue
				}
				allowed, accessErr = checkSessionEventAccess(r.Context(), eventAccess, ev)
				if accessErr != nil {
					return
				}
				if !allowed {
					continue
				}
				if ev.EventSeq > 0 {
					if _, err := fmt.Fprintf(w, "id: %d\n", ev.EventSeq); err != nil {
						return
					}
				}
				if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	}
}

func checkSessionEventAccess(ctx context.Context, access SessionEventAccessFilter, event orchestrator.OrchestratorEvent) (bool, error) {
	if access == nil {
		return false, nil
	}
	return access(ctx, event)
}

func parseLastEventID(v string) (modulecore.EventSeq, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("Last-Event-ID must be a non-negative EventSeq")
	}
	return modulecore.EventSeq(n), nil
}
