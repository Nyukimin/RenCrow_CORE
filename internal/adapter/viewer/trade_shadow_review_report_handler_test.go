package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	applicationreport "github.com/Nyukimin/RenCrow_CORE/internal/application/tradeshadowreviewreport"
	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

type shadowReviewReportRunnerStub struct{ got applicationreport.Request }

func (stub *shadowReviewReportRunnerStub) Report(_ context.Context, request applicationreport.Request) (applicationreport.Result, error) {
	stub.got = request
	return applicationreport.Result{Report: &moduletrade.PrivateShadowReviewReport{Report: moduletrade.ShadowReviewReport{SchemaVersion: 1, ContractVersion: moduletrade.ShadowReviewReportContractVersion, StudyID: request.StudyID, Environment: "SHADOW", ReviewState: "pending_review"}}}, nil
}

func TestTradeShadowReviewReportIsReadOnly(t *testing.T) {
	runner := &shadowReviewReportRunnerStub{}
	handler := HandleTradeShadowReviewReport(TradeShadowReviewReportOptions{Enabled: true, Runner: runner})
	request := httptest.NewRequest(http.MethodGet, "/viewer/trade/shadow/outcomes/reviews/report?study_id=study-1", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || runner.got.StudyID != "study-1" {
		t.Fatalf("status=%d request=%+v body=%s", response.Code, runner.got, response.Body.String())
	}
	var projection tradeShadowReviewReportProjection
	if err := json.Unmarshal(response.Body.Bytes(), &projection); err != nil {
		t.Fatal(err)
	}
	if projection.Environment != "SHADOW" || projection.AuthorizesExternalExecution || projection.PortfolioMutated || projection.KnowledgePromoted {
		t.Fatalf("unsafe projection: %+v", projection)
	}
}

func TestTradeShadowReviewReportRequiresSingleStudyID(t *testing.T) {
	handler := HandleTradeShadowReviewReport(TradeShadowReviewReportOptions{Enabled: true, Runner: &shadowReviewReportRunnerStub{}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/viewer/trade/shadow/outcomes/reviews/report", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", response.Code)
	}
}
