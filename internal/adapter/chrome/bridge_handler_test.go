package chrome

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	entryadapter "github.com/Nyukimin/RenCrow_CORE/internal/adapter/entry"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestHandleBridge_Success(t *testing.T) {
	h := HandleBridge(func(ctx context.Context, req entryadapter.Request) (entryadapter.Result, error) {
		if req.Platform != "chrome" || req.Channel != "local" {
			t.Fatalf("unexpected normalized req: %+v", req)
		}
		return entryadapter.Result{
			SessionID:   "sess-1",
			Route:       "CODE3",
			TaskID:      modulecore.NewTaskID().String(),
			Response:    "done",
			EvidenceRef: "execution_report:task",
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
		return entryadapter.Result{SessionID: req.SessionID, Route: "CHAT", TaskID: modulecore.NewTaskID().String(), Response: "ok"}, nil
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
	taskID := modulecore.NewTaskID().String()
	history := []orchestrator.OrchestratorEvent{
		orchestrator.NewEvent("entry.stage", "chrome", "system", "received", "CODE3", taskID, "sess-1", "local", "u1"),
		orchestrator.NewEvent("entry.stage", "chrome", "system", "planning", "CODE3", taskID, "sess-1", "local", "u1"),
		orchestrator.NewEvent("entry.stage", "chrome", "system", "completed", "CODE3", taskID, "sess-1", "local", "u1"),
	}
	h := HandleBridgeStatusWithSessionEventAccess(func() []orchestrator.OrchestratorEvent { return history }, func() time.Time {
		return time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	}, func(_ context.Context, sessionID string) (bool, error) { return sessionID == "sess-1", nil }, allowChromeBridgeEvent)

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

func TestHandleBridgeStatusSkipsCandidatesWithoutEventAccess(t *testing.T) {
	history := []orchestrator.OrchestratorEvent{
		{Type: "entry.stage", SessionID: "sess-1", Content: "trusted-stage", Route: "CHAT", TaskID: "trusted-task"},
		{Type: "entry.stage", SessionID: "sess-1", Content: "rejected-stage", Route: "PRIVATE", TaskID: "rejected-task"},
	}
	eventChecks := 0
	h := HandleBridgeStatusWithSessionEventAccess(
		func() []orchestrator.OrchestratorEvent { return history },
		time.Now,
		func(_ context.Context, sessionID string) (bool, error) { return sessionID == "sess-1", nil },
		func(_ context.Context, event orchestrator.OrchestratorEvent) (bool, error) {
			eventChecks++
			return event.Content == "trusted-stage", nil
		},
	)

	req := httptest.NewRequest(http.MethodGet, "/chrome/bridge/status?session_id=sess-1", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"stage":"trusted-stage"`) {
		t.Fatalf("status=%d body=%s, want the newest candidate that passed event access", rec.Code, rec.Body.String())
	}
	for _, secret := range []string{"rejected-stage", "PRIVATE", "rejected-task"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("status leaked rejected candidate %q: %s", secret, rec.Body.String())
		}
	}
	if eventChecks != 2 {
		t.Fatalf("event access checks=%d, want every matching status candidate checked", eventChecks)
	}

	eventChecks = 0
	h = HandleBridgeStatusWithSessionEventAccess(
		func() []orchestrator.OrchestratorEvent { return history },
		time.Now,
		func(_ context.Context, sessionID string) (bool, error) { return sessionID == "sess-1", nil },
		func(_ context.Context, _ orchestrator.OrchestratorEvent) (bool, error) {
			eventChecks++
			return false, nil
		},
	)
	rec = httptest.NewRecorder()
	h(rec, req)
	if !strings.Contains(rec.Body.String(), `"stage":"unknown"`) || strings.Contains(rec.Body.String(), "rejected-task") || strings.Contains(rec.Body.String(), "trusted-task") {
		t.Fatalf("status=%d body=%s, want empty unknown projection when no candidate is valid", rec.Code, rec.Body.String())
	}
}

type fakeEventStream struct {
	history          []orchestrator.OrchestratorEvent
	ch               chan []byte
	subscribeCalls   int
	unsubscribeCalls int
	replayCalls      int
}

func allowChromeBridgeEvent(_ context.Context, _ orchestrator.OrchestratorEvent) (bool, error) {
	return true, nil
}

func (f *fakeEventStream) Subscribe() (chan []byte, []orchestrator.OrchestratorEvent) {
	f.subscribeCalls++
	return f.ch, f.history
}
func (f *fakeEventStream) Unsubscribe(_ chan []byte) { f.unsubscribeCalls++ }
func (f *fakeEventStream) Replay(ctx context.Context, after modulecore.EventSeq, snapshot []orchestrator.OrchestratorEvent, emit func(orchestrator.OrchestratorEvent) error) (modulecore.EventSeq, error) {
	f.replayCalls++
	if err := ctx.Err(); err != nil {
		return after, err
	}
	cursor := after
	for _, event := range snapshot {
		if event.EventSeq > 0 && event.EventSeq <= after {
			continue
		}
		if err := emit(event); err != nil {
			return cursor, err
		}
		if event.EventSeq > cursor {
			cursor = event.EventSeq
		}
	}
	return cursor, nil
}

func TestHandleBridgeStatusFailsClosedWithoutSessionAccess(t *testing.T) {
	historyRead := false
	h := HandleBridgeStatus(func() []orchestrator.OrchestratorEvent {
		historyRead = true
		return []orchestrator.OrchestratorEvent{{
			Type: "entry.stage", SessionID: "private-session", Content: "private stage", Route: "private route", TaskID: "private-task",
		}}
	}, time.Now)

	req := httptest.NewRequest(http.MethodGet, "/chrome/bridge/status?session_id=private-session", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatalf("legacy status handler must fail closed without a session access policy, got 200: %s", rec.Body.String())
	}
	if historyRead {
		t.Fatal("expected session access to be required before reading global history")
	}
	for _, secret := range []string{"private stage", "private route", "private-task"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("response leaked %q: %s", secret, rec.Body.String())
		}
	}
}

func TestHandleBridgeStatusRejectsDeniedOrUnavailableSessionBeforeHistory(t *testing.T) {
	for _, tc := range []struct {
		name       string
		access     SessionAccessFilter
		wantStatus int
	}{
		{
			name: "owner-bound session denied",
			access: func(_ context.Context, sessionID string) (bool, error) {
				if sessionID != "private-session" {
					t.Fatalf("session access checked %q, want private-session", sessionID)
				}
				return false, nil
			},
			wantStatus: http.StatusForbidden,
		},
		{
			name: "lookup error",
			access: func(_ context.Context, _ string) (bool, error) {
				return false, errors.New("private owner lookup details")
			},
			wantStatus: http.StatusServiceUnavailable,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			historyRead := false
			h := HandleBridgeStatusWithSessionAccess(func() []orchestrator.OrchestratorEvent {
				historyRead = true
				return []orchestrator.OrchestratorEvent{{Type: "entry.stage", SessionID: "private-session", Content: "private stage", Route: "private route", TaskID: "private-task"}}
			}, time.Now, tc.access)

			req := httptest.NewRequest(http.MethodGet, "/chrome/bridge/status?session_id=private-session", nil)
			rec := httptest.NewRecorder()
			h(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if historyRead {
				t.Fatal("denied or unavailable session reached global history")
			}
			for _, secret := range []string{"private stage", "private route", "private-task", "private owner lookup details"} {
				if strings.Contains(rec.Body.String(), secret) {
					t.Fatalf("response leaked %q: %s", secret, rec.Body.String())
				}
			}
		})
	}
}

type bridgeCancelAfterFlushWriter struct {
	*httptest.ResponseRecorder
	cancelled bool
	cancel    context.CancelFunc
}

func (w *bridgeCancelAfterFlushWriter) Flush() {
	w.ResponseRecorder.Flush()
	if !w.cancelled {
		w.cancelled = true
		w.cancel()
	}
}

func TestHandleBridgeEvents_FiltersBySession(t *testing.T) {
	src := &fakeEventStream{
		history: []orchestrator.OrchestratorEvent{
			{Type: "entry.stage", SessionID: "sess-1", Content: "received", EventSeq: 1},
			{Type: "entry.stage", SessionID: "sess-2", Content: "received", EventSeq: 2},
		},
		ch: make(chan []byte, 1),
	}
	h := HandleBridgeEventsWithSessionEventAccess(src, func(_ context.Context, _ string) (bool, error) { return true, nil }, allowChromeBridgeEvent)

	req := httptest.NewRequest(http.MethodGet, "/chrome/bridge/events?session_id=sess-1", nil)
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	req = req.WithContext(ctx)
	rec := &bridgeCancelAfterFlushWriter{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
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
			{Type: "entry.stage", SessionID: "sess-1", Content: "received", EventSeq: 1},
			{Type: "entry.stage", SessionID: "sess-1", Content: "planning", EventSeq: 2},
		},
		ch: make(chan []byte, 1),
	}
	h := HandleBridgeEventsWithSessionEventAccess(src, func(_ context.Context, _ string) (bool, error) { return true, nil }, allowChromeBridgeEvent)

	req := httptest.NewRequest(http.MethodGet, "/chrome/bridge/events?session_id=sess-1", nil)
	req.Header.Set("Last-Event-ID", "1")
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	req = req.WithContext(ctx)
	rec := &bridgeCancelAfterFlushWriter{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
	h(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, `"event_seq":1`) {
		t.Fatalf("expected event_seq=1 to be skipped by Last-Event-ID, got: %s", body)
	}
	if !strings.Contains(body, `"event_seq":2`) {
		t.Fatalf("expected event_seq=2 to be replayed, got: %s", body)
	}
}

func TestHandleBridgeEventsRejectsInvalidLastEventID(t *testing.T) {
	src := &fakeEventStream{ch: make(chan []byte, 1)}
	h := HandleBridgeEventsWithSessionEventAccess(src, func(_ context.Context, _ string) (bool, error) { return true, nil }, allowChromeBridgeEvent)
	for _, value := range []string{"not-a-seq", "-1"} {
		t.Run(value, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/chrome/bridge/events?session_id=sess-1", nil)
			req.Header.Set("Last-Event-ID", value)
			rec := httptest.NewRecorder()
			h(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("Last-Event-ID %q status = %d, want 400", value, rec.Code)
			}
		})
	}
}

func TestHandleBridgeEventsFailsClosedWithoutSessionAccessBeforeSubscription(t *testing.T) {
	src := &fakeEventStream{
		history: []orchestrator.OrchestratorEvent{{Type: "entry.stage", SessionID: "private-session", Content: "private event", EventSeq: 1}},
		ch:      make(chan []byte, 1),
	}
	h := HandleBridgeEvents(src)
	req := httptest.NewRequest(http.MethodGet, "/chrome/bridge/events?session_id=private-session", nil)
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h(&bridgeCancelAfterFlushWriter{ResponseRecorder: rec, cancel: cancel}, req)

	if rec.Code == http.StatusOK {
		t.Fatalf("legacy event handler must fail closed without a session access policy, got 200: %s", rec.Body.String())
	}
	if src.subscribeCalls != 0 || src.replayCalls != 0 {
		t.Fatalf("unauthorized request reached global event stream: subscribe=%d replay=%d", src.subscribeCalls, src.replayCalls)
	}
	if strings.Contains(rec.Body.String(), "private event") || strings.Contains(rec.Body.String(), `"session_id":"private-session"`) {
		t.Fatalf("response leaked private event: %s", rec.Body.String())
	}
}

func TestHandleBridgeEventsRejectsDeniedOrUnavailableSessionBeforeSubscription(t *testing.T) {
	for _, tc := range []struct {
		name       string
		access     SessionAccessFilter
		wantStatus int
	}{
		{
			name: "owner-bound session denied",
			access: func(_ context.Context, sessionID string) (bool, error) {
				if sessionID != "private-session" {
					t.Fatalf("session access checked %q, want private-session", sessionID)
				}
				return false, nil
			},
			wantStatus: http.StatusForbidden,
		},
		{
			name: "lookup error",
			access: func(_ context.Context, _ string) (bool, error) {
				return false, errors.New("private owner lookup details")
			},
			wantStatus: http.StatusServiceUnavailable,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &fakeEventStream{
				history: []orchestrator.OrchestratorEvent{{Type: "entry.stage", SessionID: "private-session", Content: "private event", EventSeq: 1}},
				ch:      make(chan []byte, 1),
			}
			h := HandleBridgeEventsWithSessionEventAccess(src, tc.access, allowChromeBridgeEvent)
			req := httptest.NewRequest(http.MethodGet, "/chrome/bridge/events?session_id=private-session", nil)
			rec := httptest.NewRecorder()
			h(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if src.subscribeCalls != 0 || src.replayCalls != 0 {
				t.Fatalf("denied or unavailable session reached global stream: subscribe=%d replay=%d", src.subscribeCalls, src.replayCalls)
			}
			for _, secret := range []string{"private event", "private owner lookup details"} {
				if strings.Contains(rec.Body.String(), secret) {
					t.Fatalf("response leaked %q: %s", secret, rec.Body.String())
				}
			}
		})
	}
}

func TestHandleBridgeEventsRechecksReplayAndLiveEvents(t *testing.T) {
	t.Run("replay", func(t *testing.T) {
		src := &fakeEventStream{
			history: []orchestrator.OrchestratorEvent{{Type: "entry.stage", SessionID: "sess-1", Content: "private replay", EventSeq: 1}},
			ch:      make(chan []byte, 1),
		}
		accessCalls := 0
		h := HandleBridgeEventsWithSessionEventAccess(src, func(_ context.Context, _ string) (bool, error) {
			accessCalls++
			return accessCalls == 1, nil
		}, allowChromeBridgeEvent)

		req := httptest.NewRequest(http.MethodGet, "/chrome/bridge/events?session_id=sess-1", nil)
		ctx, cancel := context.WithCancel(req.Context())
		defer cancel()
		req = req.WithContext(ctx)
		rec := &bridgeCancelAfterFlushWriter{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
		h(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if accessCalls < 2 {
			t.Fatalf("session access checks = %d, want preflight plus replay event check", accessCalls)
		}
		if strings.Contains(rec.Body.String(), "private replay") {
			t.Fatalf("replay event was not rechecked before writing: %s", rec.Body.String())
		}
	})

	t.Run("live", func(t *testing.T) {
		event := orchestrator.OrchestratorEvent{Type: "entry.stage", SessionID: "sess-1", Content: "private live", EventSeq: 1}
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		src := &fakeEventStream{ch: make(chan []byte, 1)}
		src.ch <- data
		ctxReq := httptest.NewRequest(http.MethodGet, "/chrome/bridge/events?session_id=sess-1", nil)
		ctx, cancel := context.WithCancel(ctxReq.Context())
		defer cancel()
		accessCalls := 0
		h := HandleBridgeEventsWithSessionEventAccess(src, func(_ context.Context, _ string) (bool, error) {
			accessCalls++
			if accessCalls == 1 {
				return true, nil
			}
			cancel()
			return false, nil
		}, allowChromeBridgeEvent)
		req := ctxReq.WithContext(ctx)
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if accessCalls < 2 {
			t.Fatalf("session access checks = %d, want preflight plus live event check", accessCalls)
		}
		if strings.Contains(rec.Body.String(), "private live") {
			t.Fatalf("live event was not rechecked before writing: %s", rec.Body.String())
		}
	})
}

func TestHandleBridgeEventsRechecksEachReplayAndLiveEvent(t *testing.T) {
	t.Run("replay", func(t *testing.T) {
		src := &fakeEventStream{
			history: []orchestrator.OrchestratorEvent{{Type: "entry.stage", SessionID: "sess-1", Content: "stale replay", EventSeq: 1}},
			ch:      make(chan []byte, 1),
		}
		checks := 0
		h := HandleBridgeEventsWithSessionEventAccess(
			src,
			func(_ context.Context, _ string) (bool, error) { return true, nil },
			func(_ context.Context, _ orchestrator.OrchestratorEvent) (bool, error) { checks++; return false, nil },
		)
		req := httptest.NewRequest(http.MethodGet, "/chrome/bridge/events?session_id=sess-1", nil)
		ctx, cancel := context.WithCancel(req.Context())
		defer cancel()
		req = req.WithContext(ctx)
		rec := &bridgeCancelAfterFlushWriter{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
		h(rec, req)
		if rec.Code != http.StatusOK || checks != 1 || strings.Contains(rec.Body.String(), "stale replay") {
			t.Fatalf("status=%d checks=%d body=%s, want replay candidate denied before write", rec.Code, checks, rec.Body.String())
		}
	})

	t.Run("live", func(t *testing.T) {
		event := orchestrator.OrchestratorEvent{Type: "entry.stage", SessionID: "sess-1", Content: "stale live", EventSeq: 1}
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		src := &fakeEventStream{ch: make(chan []byte, 1)}
		src.ch <- data
		ctxReq := httptest.NewRequest(http.MethodGet, "/chrome/bridge/events?session_id=sess-1", nil)
		ctx, cancel := context.WithCancel(ctxReq.Context())
		defer cancel()
		checks := 0
		h := HandleBridgeEventsWithSessionEventAccess(
			src,
			func(_ context.Context, _ string) (bool, error) { return true, nil },
			func(_ context.Context, _ orchestrator.OrchestratorEvent) (bool, error) {
				checks++
				cancel()
				return false, nil
			},
		)
		rec := httptest.NewRecorder()
		h(rec, ctxReq.WithContext(ctx))
		if rec.Code != http.StatusOK || checks != 1 || strings.Contains(rec.Body.String(), "stale live") {
			t.Fatalf("status=%d checks=%d body=%s, want live candidate denied before write", rec.Code, checks, rec.Body.String())
		}
	})
}

func TestHandleBridgeEventsReplayLookupErrorDoesNotLeak(t *testing.T) {
	src := &fakeEventStream{
		history: []orchestrator.OrchestratorEvent{{Type: "entry.stage", SessionID: "sess-1", Content: "private replay", EventSeq: 1}},
		ch:      make(chan []byte, 1),
	}
	accessCalls := 0
	h := HandleBridgeEventsWithSessionEventAccess(src, func(_ context.Context, _ string) (bool, error) {
		accessCalls++
		if accessCalls == 1 {
			return true, nil
		}
		return false, errors.New("private owner lookup details")
	}, allowChromeBridgeEvent)
	req := httptest.NewRequest(http.MethodGet, "/chrome/bridge/events?session_id=sess-1", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
	for _, secret := range []string{"private replay", "private owner lookup details"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("response leaked %q: %s", secret, rec.Body.String())
		}
	}
}
