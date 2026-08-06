package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	applicationtradepolicy "github.com/Nyukimin/RenCrow_CORE/internal/application/tradepolicy"
	domainpolicy "github.com/Nyukimin/RenCrow_CORE/internal/domain/policydecision"
	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

type tradePolicyRunnerStub struct {
	request applicationtradepolicy.Request
	result  applicationtradepolicy.Result
	err     error
}

func (stub *tradePolicyRunnerStub) Evaluate(_ context.Context, request applicationtradepolicy.Request) (applicationtradepolicy.Result, error) {
	stub.request = request
	return stub.result, stub.err
}

func TestHandleTradePolicyEvaluationIsDiagnosticAndNeverAuthorizesExecution(t *testing.T) {
	evidence := domainpolicy.Record{
		RecordVersion: 1, DecisionID: "decision-1", Requester: "viewer-trade-policy-diagnostic", Module: "RenCrow_TRADE", Action: "live_order",
		BinaryContractRevision: "trade-binary/v1", GlobalBundleRevision: "2026-08-06.1", ModulePolicyRevision: "sha256:module", DeploymentRevision: "2026-08-06.1#deployment",
		Outcome: domainpolicy.OutcomeBlocked, Reasons: []string{"BINARY_HARD_LIMIT_BLOCKED"}, CreatedAt: time.Now().UTC(),
	}
	runner := &tradePolicyRunnerStub{result: applicationtradepolicy.Result{
		Decision: moduletrade.PolicyDecision{Capability: "live_order", Status: "blocked", ReasonCode: "BINARY_HARD_LIMIT_BLOCKED"},
		Evidence: evidence,
	}}
	body := []byte(`{"request_id":"request-1","capability":"live_order","request_scope_revision":"diagnostic/v1","request_allowed":true}`)
	request := httptest.NewRequest(http.MethodPost, "/viewer/trade/policy/evaluate", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Trace-ID", "trace-1")
	response := httptest.NewRecorder()
	HandleTradePolicyEvaluation(TradePolicyEvaluationOptions{Enabled: true, Runner: runner})(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var payload tradePolicyEvaluationProjection
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.AuthorizesExecution || payload.Status != "evaluated" || payload.Decision.Status != "blocked" {
		t.Fatalf("payload=%+v", payload)
	}
	if runner.request.Requester != "viewer-trade-policy-diagnostic" || runner.request.TraceID != "trace-1" || !runner.request.RequestAllowed {
		t.Fatalf("request=%+v", runner.request)
	}
}

func TestHandleTradePolicyEvaluationRequiresExplicitStrictInput(t *testing.T) {
	runner := &tradePolicyRunnerStub{}
	for _, body := range []string{
		`{"request_id":"request-1","capability":"source_collect","request_scope_revision":"diagnostic/v1"}`,
		`{"request_id":"request-1","capability":"source_collect","request_scope_revision":"diagnostic/v1","request_allowed":false,"extra":true}`,
	} {
		request := httptest.NewRequest(http.MethodPost, "/viewer/trade/policy/evaluate", bytes.NewBufferString(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		HandleTradePolicyEvaluation(TradePolicyEvaluationOptions{Enabled: true, Runner: runner})(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body=%s status=%d response=%s", body, response.Code, response.Body.String())
		}
	}
}
