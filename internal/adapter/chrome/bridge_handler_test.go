package chrome

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	entryadapter "github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/entry"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/orchestrator"
)

func TestHandleBridge_Success(t *testing.T) {
	h := HandleBridge(func(ctx context.Context, req entryadapter.Request) (entryadapter.Result, error) {
		if req.Platform != "chrome" || req.Channel != "local" {
			t.Fatalf("unexpected normalized req: %+v", req)
		}
		return entryadapter.Result{
			SessionID:   "sess-1",
			Route:       "CODE3",
			JobID:       "job-1",
			Response:    "done",
			EvidenceRef: "execution_report:job-1",
		}, nil
	})

	body := []byte(`{"user_id":"u1","message":"TTS実装して"}`)
	req := httptest.NewRequest(http.MethodPost, "/chrome/bridge", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if out["ok"] != true || out["session_id"] == "" || out["evidence_ref"] == "" {
		t.Fatalf("unexpected output: %+v", out)
	}
	if out["request_id"] == "" || out["accepted_at"] == "" {
		t.Fatalf("expected request_id/accepted_at in output: %+v", out)
	}
}

func TestHandleBridge_EchoesRequestID(t *testing.T) {
	h := HandleBridge(func(ctx context.Context, req entryadapter.Request) (entryadapter.Result, error) {
		return entryadapter.Result{SessionID: req.SessionID, Route: "CHAT", JobID: "j1", Response: "ok"}, nil
	})
	req := httptest.NewRequest(http.MethodPost, "/chrome/bridge", bytes.NewReader([]byte(`{"user_id":"u1","request_id":"req-123","message":"hello"}`)))
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if out["request_id"] != "req-123" {
		t.Fatalf("expected echoed request_id, got %+v", out)
	}
}

func TestHandleBridgeStatus_LatestStageBySession(t *testing.T) {
	history := []orchestrator.OrchestratorEvent{
		orchestrator.NewEvent("entry.stage", "chrome", "system", "received", "CODE3", "job-1", "sess-1", "local", "u1"),
		orchestrator.NewEvent("entry.stage", "chrome", "system", "planning", "CODE3", "job-1", "sess-1", "local", "u1"),
		orchestrator.NewEvent("entry.stage", "chrome", "system", "completed", "CODE3", "job-1", "sess-1", "local", "u1"),
	}
	h := HandleBridgeStatus(func() []orchestrator.OrchestratorEvent { return history }, func() time.Time {
		return time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	})

	req := httptest.NewRequest(http.MethodGet, "/chrome/bridge/status?session_id=sess-1", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var out struct {
		OK      bool   `json:"ok"`
		Session string `json:"session_id"`
		Stage   string `json:"stage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !out.OK || out.Session != "sess-1" || out.Stage != "completed" {
		t.Fatalf("unexpected output: %+v", out)
	}
}

type fakeEventStream struct {
	history []orchestrator.OrchestratorEvent
	ch      chan []byte
}

func (f *fakeEventStream) History() []orchestrator.OrchestratorEvent { return f.history }
func (f *fakeEventStream) Subscribe() chan []byte                    { return f.ch }
func (f *fakeEventStream) Unsubscribe(_ chan []byte)                 {}

func TestHandleBridgeEvents_FiltersBySession(t *testing.T) {
	src := &fakeEventStream{
		history: []orchestrator.OrchestratorEvent{
			{Type: "entry.stage", SessionID: "sess-1", Content: "received", Seq: 1},
			{Type: "entry.stage", SessionID: "sess-2", Content: "received", Seq: 2},
		},
		ch: make(chan []byte, 1),
	}
	h := HandleBridgeEvents(src)

	req := httptest.NewRequest(http.MethodGet, "/chrome/bridge/events?session_id=sess-1", nil)
	ctx, cancel := context.WithCancel(req.Context())
	cancel() // history送信後に即終了させる
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"session_id":"sess-1"`) {
		t.Fatalf("expected sess-1 event in stream, got: %s", body)
	}
	if strings.Contains(body, `"session_id":"sess-2"`) {
		t.Fatalf("did not expect sess-2 event in stream, got: %s", body)
	}
}

func TestHandleBridgeEvents_RespectsLastEventID(t *testing.T) {
	src := &fakeEventStream{
		history: []orchestrator.OrchestratorEvent{
			{Type: "entry.stage", SessionID: "sess-1", Content: "received", Seq: 1},
			{Type: "entry.stage", SessionID: "sess-1", Content: "planning", Seq: 2},
		},
		ch: make(chan []byte, 1),
	}
	h := HandleBridgeEvents(src)

	req := httptest.NewRequest(http.MethodGet, "/chrome/bridge/events?session_id=sess-1", nil)
	req.Header.Set("Last-Event-ID", "1")
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, `"seq":1`) {
		t.Fatalf("expected seq=1 to be skipped by Last-Event-ID, got: %s", body)
	}
	if !strings.Contains(body, `"seq":2`) {
		t.Fatalf("expected seq=2 to be replayed, got: %s", body)
	}
}
