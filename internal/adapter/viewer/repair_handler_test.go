package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
)

type repairTestListener struct {
	events []orchestrator.OrchestratorEvent
}

func (l *repairTestListener) OnEvent(ev orchestrator.OrchestratorEvent) {
	l.events = append(l.events, ev)
}

type repairTestRunner struct {
	calls []RepairJobRequest
}

func (r *repairTestRunner) StartRepairJob(_ context.Context, req RepairJobRequest) error {
	r.calls = append(r.calls, req)
	return nil
}

func TestHandleRepairRunEmitsRepairEvents(t *testing.T) {
	listener := &repairTestListener{}
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
	if !resp.OK || resp.JobID == "" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if len(listener.events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(listener.events))
	}
	if listener.events[0].Type != "repair.requested" || listener.events[0].JobID != resp.JobID {
		t.Fatalf("unexpected repair event: %+v", listener.events[0])
	}
	if listener.events[1].Type != "job.notification" || listener.events[1].Route != "OPS" {
		t.Fatalf("unexpected notification event: %+v", listener.events[1])
	}
}

func TestHandleRepairRunStartsRepairJobRunner(t *testing.T) {
	listener := &repairTestListener{}
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
	if call.JobID != resp.JobID {
		t.Fatalf("runner job id = %q, response job id = %q", call.JobID, resp.JobID)
	}
	if call.Instruction != "ログを見て修復" || call.TargetRoute != "CHAT" || call.TargetAgent != "mio" || call.Source != "viewer" {
		t.Fatalf("unexpected runner request: %+v", call)
	}
}
