package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

const boardAgentWorkPath = "/viewer/board/agent-work"

type BoardAgentWorkReader interface {
	ReadBoardAgentWork(context.Context) (BoardAgentWorkSnapshot, error)
}

type BoardAgentWorkSnapshot struct {
	Actors []BoardAgentWorkActor `json:"actors"`
}

type BoardAgentWorkAvailability struct {
	Heartbeat string `json:"heartbeat"`
	Tasks     string `json:"tasks"`
	RunQueue  string `json:"run_queue"`
}

type BoardAgentWorkActor struct {
	ActorID        string                     `json:"actor_id"`
	Availability   BoardAgentWorkAvailability `json:"availability"`
	HeartbeatState string                     `json:"heartbeat_state"`
	ActivityKind   string                     `json:"activity_kind,omitempty"`
	ActivityTitle  string                     `json:"activity_title,omitempty"`
	CurrentTasks   []BoardAgentWorkTask       `json:"current_tasks"`
	WaitingQueue   []BoardAgentWorkTask       `json:"waiting_queue"`
}

// BoardAgentWorkTask deliberately projects only task identity, title, state,
// and scheduler metadata. It never serializes Task context, summary, evidence,
// waiting reason, or user-provided prompt content.
type BoardAgentWorkTask struct {
	TaskID         string     `json:"task_id"`
	Title          string     `json:"title"`
	Status         string     `json:"status"`
	Priority       string     `json:"priority"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	QueueClass     string     `json:"queue_class"`
	RunQueueStatus string     `json:"run_queue_status,omitempty"`
	NotBefore      *time.Time `json:"not_before,omitempty"`
}

type boardAgentWorkResponse struct {
	ObservedAt string                `json:"observed_at"`
	Actors     []BoardAgentWorkActor `json:"actors"`
}

func NewBoardAgentWorkHandler(reader BoardAgentWorkReader, token []byte) http.HandlerFunc {
	return (&boardAgentWorkHandler{reader: reader, token: append([]byte(nil), token...)}).ServeHTTP
}

type boardAgentWorkHandler struct {
	reader BoardAgentWorkReader
	token  []byte
}

func (h *boardAgentWorkHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !memoryOwnerDirectLocalRequest(r) {
		writeMemoryOwnerError(w, http.StatusNotFound, "not_found")
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeMemoryOwnerError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if r.URL == nil || r.URL.Path != boardAgentWorkPath {
		writeMemoryOwnerError(w, http.StatusNotFound, "not_found")
		return
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if len(h.token) == 0 {
		writeMemoryOwnerError(w, http.StatusServiceUnavailable, "owner_auth_unavailable")
		return
	}
	if !memoryOwnerBearerAuthorized(r, h.token) {
		writeMemoryOwnerError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !boardStudioOwnerProfileAllowed(r) {
		writeMemoryOwnerError(w, http.StatusForbidden, "forbidden")
		return
	}
	if h.reader == nil {
		writeMemoryOwnerError(w, http.StatusServiceUnavailable, "work_unavailable")
		return
	}
	snapshot, err := h.reader.ReadBoardAgentWork(r.Context())
	if err != nil {
		writeMemoryOwnerError(w, http.StatusServiceUnavailable, "work_unavailable")
		return
	}
	if len(snapshot.Actors) == 0 {
		writeMemoryOwnerError(w, http.StatusServiceUnavailable, "actor_snapshot_unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(boardAgentWorkResponse{
		ObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Actors:     snapshot.Actors,
	})
}

func boardStudioOwnerProfileAllowed(r *http.Request) bool {
	client, clientOK := exactlyOneMemoryOwnerHeader(r.Header, "X-RenCrow-Client")
	profile, profileOK := exactlyOneMemoryOwnerHeader(r.Header, "X-RenCrow-Interaction-Profile")
	return clientOK && profileOK && client == "RenCrow_BOARD" && profile == "board-studio-readonly"
}
