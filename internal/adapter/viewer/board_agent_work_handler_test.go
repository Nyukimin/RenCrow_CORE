package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type boardAgentWorkReaderStub struct {
	snapshot BoardAgentWorkSnapshot
	err      error
}

func (s boardAgentWorkReaderStub) ReadBoardAgentWork(context.Context) (BoardAgentWorkSnapshot, error) {
	return s.snapshot, s.err
}

func boardAgentWorkOwnerRequest(method, remote string) *http.Request {
	req := httptest.NewRequest(method, "http://127.0.0.1:18790/viewer/board/agent-work", nil)
	req.RemoteAddr = remote
	req.Header.Set("Authorization", "Bearer owner-token")
	req.Header.Set("X-RenCrow-Client", "RenCrow_BOARD")
	req.Header.Set("X-RenCrow-Interaction-Profile", "board-studio-readonly")
	return req
}

func TestBoardAgentWorkHandlerRequiresDirectLocalOwnerAndBoardProfile(t *testing.T) {
	handler := NewBoardAgentWorkHandler(boardAgentWorkReaderStub{}, []byte("owner-token"))
	tests := []struct {
		name   string
		mutate func(*http.Request)
		want   int
	}{
		{name: "external request", mutate: func(r *http.Request) { r.RemoteAddr = "192.0.2.4:1234" }, want: http.StatusNotFound},
		{name: "missing bearer", mutate: func(r *http.Request) { r.Header.Del("Authorization") }, want: http.StatusUnauthorized},
		{name: "wrong bearer", mutate: func(r *http.Request) { r.Header.Set("Authorization", "Bearer wrong") }, want: http.StatusUnauthorized},
		{name: "duplicate bearer", mutate: func(r *http.Request) { r.Header.Add("Authorization", "Bearer owner-token") }, want: http.StatusUnauthorized},
		{name: "wrong client", mutate: func(r *http.Request) { r.Header.Set("X-RenCrow-Client", "RenCrow_CMD") }, want: http.StatusForbidden},
		{name: "wrong profile", mutate: func(r *http.Request) { r.Header.Set("X-RenCrow-Interaction-Profile", "cmd-diagnostics") }, want: http.StatusForbidden},
		{name: "query is rejected", mutate: func(r *http.Request) { r.URL.RawQuery = "actor=mio" }, want: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := boardAgentWorkOwnerRequest(http.MethodGet, "127.0.0.1:41000")
			tt.mutate(req)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tt.want, rec.Body.String())
			}
		})
	}

	req := boardAgentWorkOwnerRequest(http.MethodPost, "127.0.0.1:41000")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("POST response = %d Allow=%q, want 405 GET", rec.Code, rec.Header().Get("Allow"))
	}
}

func TestBoardAgentWorkHandlerReturnsAuthenticatedPrivateTaskProjection(t *testing.T) {
	createdAt := time.Date(2026, 11, 15, 1, 2, 3, 0, time.UTC)
	reader := boardAgentWorkReaderStub{snapshot: BoardAgentWorkSnapshot{Actors: []BoardAgentWorkActor{{
		ActorID:        "mio",
		Availability:   BoardAgentWorkAvailability{Heartbeat: "available", Tasks: "available", RunQueue: "available"},
		HeartbeatState: "waiting",
		CurrentTasks:   []BoardAgentWorkTask{},
		WaitingQueue: []BoardAgentWorkTask{{
			TaskID: "task_mio_1", Title: "private title", Status: "queued", Priority: "high",
			CreatedAt: createdAt, UpdatedAt: createdAt, QueueClass: "task_only",
		}},
	}}}}
	handler := NewBoardAgentWorkHandler(reader, []byte("owner-token"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, boardAgentWorkOwnerRequest(http.MethodGet, "127.0.0.1:41000"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", rec.Header().Get("Cache-Control"))
	}
	var got struct {
		ObservedAt string                `json:"observed_at"`
		Actors     []BoardAgentWorkActor `json:"actors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.ObservedAt == "" || len(got.Actors) != 1 || got.Actors[0].ActorID != "mio" {
		t.Fatalf("response identity = observed_at %q actors %#v", got.ObservedAt, got.Actors)
	}
	if len(got.Actors[0].WaitingQueue) != 1 || got.Actors[0].WaitingQueue[0].Title != "private title" {
		t.Fatalf("private task projection = %#v", got.Actors[0].WaitingQueue)
	}
	if strings.Contains(rec.Body.String(), "waiting_reason") || strings.Contains(rec.Body.String(), "summary") || strings.Contains(rec.Body.String(), "prompt") {
		t.Fatalf("projection leaked unapproved task content: %s", rec.Body.String())
	}
}

func TestBoardAgentWorkHandlerDoesNotTurnUnavailableIntoEmptyWork(t *testing.T) {
	handler := NewBoardAgentWorkHandler(nil, []byte("owner-token"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, boardAgentWorkOwnerRequest(http.MethodGet, "127.0.0.1:41000"))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "unavailable") {
		t.Fatalf("unconfigured source response = %d %s, want explicit 503 unavailable", rec.Code, rec.Body.String())
	}
}
