package entry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	channelapp "github.com/Nyukimin/picoclaw_multiLLM/internal/application/channel"
)

// Request is a unified cross-platform entry payload.
type Request struct {
	Platform  string `json:"platform"`
	Channel   string `json:"channel"`
	UserID    string `json:"user_id"`
	SessionID string `json:"session_id,omitempty"`
	Message   string `json:"message"`
}

// Result is a normalized response from message processor.
type Result struct {
	SessionID   string `json:"session_id"`
	Route       string `json:"route"`
	JobID       string `json:"job_id"`
	Response    string `json:"response"`
	EvidenceRef string `json:"evidence_ref,omitempty"`
}

// Processor processes unified requests.
type Processor func(ctx context.Context, req Request) (Result, error)

// Stage represents unified entry lifecycle stages.
type Stage string

const (
	StageReceived  Stage = "received"
	StagePlanning  Stage = "planning"
	StageApplying  Stage = "applying"
	StageVerifying Stage = "verifying"
	StageCompleted Stage = "completed"
	StageFailed    Stage = "failed"
)

// Observer is notified on stage transitions.
type Observer func(ctx context.Context, stage Stage, req Request, result *Result, err error)

func Handle(process Processor) http.HandlerFunc {
	return HandleWithObserver(process, nil)
}

func HandleWithObserver(process Processor, observer Observer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, 8192))
		if err != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}
		defer r.Body.Close()

		var req Request
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		req.Message = strings.TrimSpace(req.Message)
		if req.Message == "" {
			http.Error(w, "message is required", http.StatusBadRequest)
			return
		}
		req.Platform, req.Channel = channelapp.NormalizeEntryPlatformChannel(req.Platform, req.Channel)
		if req.UserID == "" {
			req.UserID = "anonymous"
		}
		if req.SessionID == "" {
			req.SessionID = channelapp.BuildSessionID(time.Now().UTC(), req.Channel, req.UserID)
		}
		if observer != nil {
			observer(r.Context(), StageReceived, req, nil, nil)
			observer(r.Context(), StagePlanning, req, nil, nil)
			observer(r.Context(), StageApplying, req, nil, nil)
		}

		result, err := process(r.Context(), req)
		if err != nil {
			if observer != nil {
				observer(r.Context(), StageFailed, req, nil, err)
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if observer != nil {
			observer(r.Context(), StageVerifying, req, &result, nil)
			observer(r.Context(), StageCompleted, req, &result, nil)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":           true,
			"session_id":   result.SessionID,
			"route":        result.Route,
			"job_id":       result.JobID,
			"response":     result.Response,
			"evidence_ref": result.EvidenceRef,
		})
	}
}
