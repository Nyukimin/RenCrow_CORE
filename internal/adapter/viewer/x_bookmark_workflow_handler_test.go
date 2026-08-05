package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	domainworkflow "github.com/Nyukimin/RenCrow_CORE/internal/domain/xbookmarkworkflow"
)

type xBookmarkWorkflowServiceStub struct {
	request domainworkflow.RunRequest
	result  domainworkflow.Result
	values  []domainworkflow.Result
	err     error
}

func (s *xBookmarkWorkflowServiceStub) Run(_ context.Context, request domainworkflow.RunRequest) (domainworkflow.Result, error) {
	s.request = request
	return s.result, s.err
}

func (s *xBookmarkWorkflowServiceStub) List(_ context.Context, _ domainworkflow.ResultQuery) ([]domainworkflow.Result, error) {
	return s.values, s.err
}

func TestHandleXBookmarkWorkflowRunsOneExplicitRecord(t *testing.T) {
	service := &xBookmarkWorkflowServiceStub{result: domainworkflow.Result{
		SourceRecordID: "kb:source", Workflow: domainworkflow.WorkflowImagePromptDraw, Status: domainworkflow.StatusCompleted,
	}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/viewer/x-bookmarks/workflows/run", strings.NewReader(`{"workflow":"image_prompt_draw","source_record_id":"kb:source","idempotency_key":"key"}`))
	HandleXBookmarkWorkflow(service)(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if service.request.SourceRecordID != "kb:source" || service.request.Workflow != domainworkflow.WorkflowImagePromptDraw {
		t.Fatalf("request was not forwarded: %+v", service.request)
	}
	var payload map[string]domainworkflow.Result
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil || payload["result"].Status != domainworkflow.StatusCompleted {
		t.Fatalf("unexpected payload: %s err=%v", recorder.Body.String(), err)
	}
}

func TestHandleXBookmarkWorkflowListsDerivedResults(t *testing.T) {
	service := &xBookmarkWorkflowServiceStub{values: []domainworkflow.Result{{SourceRecordID: "kb:source", Workflow: domainworkflow.WorkflowAITipRenCrowEvaluation}}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/viewer/x-bookmarks/workflows?source_record_id=kb%3Asource&limit=20", nil)
	HandleXBookmarkWorkflow(service)(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"results"`) {
		t.Fatalf("unexpected list response: code=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
