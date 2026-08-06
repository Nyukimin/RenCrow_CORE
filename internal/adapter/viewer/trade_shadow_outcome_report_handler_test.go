package viewer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	applicationreport "github.com/Nyukimin/RenCrow_CORE/internal/application/tradeshadowoutcomereport"
	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

type reportRunnerStub struct {
	result applicationreport.Result
	err    error
}

func (stub *reportRunnerStub) Report(_ context.Context, request applicationreport.Request) (applicationreport.Result, error) {
	if request.StudyID != "study-1" {
		return applicationreport.Result{}, applicationreport.ErrInvalidRequest
	}
	return stub.result, stub.err
}

func TestTradeShadowOutcomeReportIsReadOnly(t *testing.T) {
	report := &moduletrade.PrivateShadowOutcomeReport{
		ContractVersion: "trade-private/v1", ExecutionMode: "DISABLED", Environment: "SHADOW",
		Report: moduletrade.ShadowOutcomeReport{SchemaVersion: 1, ContractVersion: moduletrade.ShadowOutcomeReportContractVersion, StudyID: "study-1", Environment: "SHADOW", ObservationCount: 1, PendingOutcomeCount: 1, LabelCounts: map[string]int64{"success": 0, "failure": 0, "neutral": 0, "inconclusive": 0}, ReviewState: "pending_outcomes"},
	}
	handler := HandleTradeShadowOutcomeReport(TradeShadowOutcomeReportOptions{Enabled: true, Runner: &reportRunnerStub{result: applicationreport.Result{Report: report}}})
	request := httptest.NewRequest(http.MethodGet, "/viewer/trade/shadow/outcomes/report?study_id=study-1", nil)
	request.Header.Set("X-Request-ID", "request-1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"portfolio_mutated":false`) || !strings.Contains(response.Body.String(), `"review_state":"pending_outcomes"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestTradeShadowOutcomeReportRequiresSingleStudyID(t *testing.T) {
	handler := HandleTradeShadowOutcomeReport(TradeShadowOutcomeReportOptions{Enabled: true, Runner: &reportRunnerStub{}})
	for _, path := range []string{"/viewer/trade/shadow/outcomes/report", "/viewer/trade/shadow/outcomes/report?study_id=study-1&study_id=study-2"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("path=%s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}
