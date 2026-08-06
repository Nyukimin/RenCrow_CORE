package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	applicationpreview "github.com/Nyukimin/RenCrow_CORE/internal/application/traderiskpreview"
	domainpolicy "github.com/Nyukimin/RenCrow_CORE/internal/domain/policydecision"
	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

type tradeRiskPreviewRunnerStub struct {
	request applicationpreview.Request
	result  applicationpreview.Result
	err     error
}

func (stub *tradeRiskPreviewRunnerStub) Evaluate(_ context.Context, request applicationpreview.Request) (applicationpreview.Result, error) {
	stub.request = request
	return stub.result, stub.err
}

func riskPreviewRequestBody(t *testing.T) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"request_id":      "request-1",
		"request_allowed": true,
		"plan": moduletrade.RiskPreviewPlan{
			ContractVersion: moduletrade.RiskPreviewPlanContractVersion,
			PlanID:          "plan-1", PolicyRevision: "2026-08-06.1", AsOf: "2026-08-06T00:00:00Z",
			Selection:    moduletrade.RiskPreviewSelection{InstrumentID: "JP-TEST", Status: "selected", EvidenceRefs: []string{"source:1"}, KnownMissingData: []string{}},
			Proposal:     moduletrade.RiskPreviewBuyProposal{InstrumentID: "JP-TEST", Side: "buy", Quantity: 100, EntryPriceJPY: 1000, GrossJPY: 100000},
			ExitContract: moduletrade.RiskPreviewExitContract{ContractID: "exit-1", Revision: "v1", InstrumentID: "JP-TEST"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestHandleTradeRiskPreviewReturnsEvidenceWithoutExecutionAuthority(t *testing.T) {
	preview := moduletrade.PrivateRiskPreview{
		PortfolioID: "main-sim", PortfolioEventCount: 1,
		PortfolioLatestEventHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Decision:                 moduletrade.RiskPreviewDecision{Status: "pass"},
	}
	runner := &tradeRiskPreviewRunnerStub{result: applicationpreview.Result{
		PolicyDecision: moduletrade.PolicyDecision{Capability: applicationpreview.Capability, Status: "allowed"},
		PolicyEvidence: domainpolicy.Record{DecisionID: "policy-1"}, Preview: &preview,
	}}
	request := httptest.NewRequest(http.MethodPost, "/viewer/trade/risk-preview", bytes.NewReader(riskPreviewRequestBody(t)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Trace-ID", "trace-1")
	response := httptest.NewRecorder()
	HandleTradeRiskPreview(TradeRiskPreviewOptions{Enabled: true, Runner: runner})(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var result tradeRiskPreviewProjection
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "evaluated" || result.AuthorizesExecution || result.MutatesPortfolio || result.Decision == nil || result.Decision.Status != "pass" {
		t.Fatalf("result=%+v", result)
	}
	if runner.request.Requester != "viewer-trade-risk-preview" || runner.request.TraceID != "trace-1" || !runner.request.RequestAllowed {
		t.Fatalf("request=%+v", runner.request)
	}
}

func TestHandleTradeRiskPreviewRequiresExplicitStrictInput(t *testing.T) {
	runner := &tradeRiskPreviewRunnerStub{}
	for _, body := range []string{
		`{"request_id":"request-1","plan":{}}`,
		`{"request_id":"request-1","request_allowed":true,"plan":{},"data_root":"/tmp/forbidden"}`,
	} {
		request := httptest.NewRequest(http.MethodPost, "/viewer/trade/risk-preview", bytes.NewBufferString(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		HandleTradeRiskPreview(TradeRiskPreviewOptions{Enabled: true, Runner: runner})(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body=%s status=%d response=%s", body, response.Code, response.Body.String())
		}
	}
}
