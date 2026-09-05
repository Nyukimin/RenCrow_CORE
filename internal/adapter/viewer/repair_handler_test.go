package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
)

type repairTestListener struct {
	events []orchestrator.OrchestratorEvent
	errAt  int
}

func (l *repairTestListener) OnEvent(ev orchestrator.OrchestratorEvent) error {
	if l.errAt >= 0 && len(l.events) == l.errAt {
		return errors.New("canonical append failed")
	}
	l.events = append(l.events, ev)
	return nil
}

type repairTestRunner struct {
	calls []RepairTaskRequest
}

func (r *repairTestRunner) StartRepairTask(_ context.Context, req RepairTaskRequest) error {
	r.calls = append(r.calls, req)
	return nil
}

func TestHandleRepairRunEmitsRepairEvents(t *testing.T) {
	listener := &repairTestListener{errAt: -1}
	body := bytes.NewBufferString(`{"reason":"echo-loop","instruction":"ログを見て修復","recent":50}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/repair/run", body)
	rec := httptest.NewRecorder()

	HandleRepairRun(listener)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp repairRunResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.OK || resp.TaskID == "" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if len(listener.events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(listener.events))
	}
	if listener.events[0].Type != "repair.requested" || listener.events[0].TaskID.String() != resp.TaskID {
		t.Fatalf("unexpected repair event: %+v", listener.events[0])
	}
	if listener.events[1].Type != "task.notification" || listener.events[1].Route != "OPS" {
		t.Fatalf("unexpected notification event: %+v", listener.events[1])
	}
}

func TestHandleRepairRunStartsRepairTaskRunner(t *testing.T) {
	listener := &repairTestListener{errAt: -1}
	runner := &repairTestRunner{}
	body := bytes.NewBufferString(`{"reason":"echo-loop","instruction":"ログを見て修復","recent":50,"target_route":"CHAT","target_agent":"mio"}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/repair/run", body)
	rec := httptest.NewRecorder()

	HandleRepairRunWithRunner(listener, runner)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp repairRunResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("expected runner to be called once, got %d", len(runner.calls))
	}
	call := runner.calls[0]
	if call.TaskID.String() != resp.TaskID {
		t.Fatalf("runner task ID = %q, response task ID = %q", call.TaskID, resp.TaskID)
	}
	if call.Instruction != "ログを見て修復" || call.TargetRoute != "CHAT" || call.TargetAgent != "mio" || call.Source != "viewer" {
		t.Fatalf("unexpected runner request: %+v", call)
	}
}

func TestHandleRepairRunPublicationFailureDoesNotStartRunner(t *testing.T) {
	listener := &repairTestListener{errAt: 0}
	runner := &repairTestRunner{}
	req := httptest.NewRequest(http.MethodPost, "/viewer/repair/run", bytes.NewBufferString(`{"reason":"archive-down"}`))
	rec := httptest.NewRecorder()

	HandleRepairRunWithRunner(listener, runner)(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s, want service unavailable", rec.Code, rec.Body.String())
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner calls=%d, want 0 after publication failure", len(runner.calls))
	}
}
