package viewer

import (
	"context"
	"errors"
	"net/http"
	"strings"

	applicationreport "github.com/Nyukimin/RenCrow_CORE/internal/application/tradeshadowreviewreport"
	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

const tradeShadowReviewReportProjectionVersion = "trade-shadow-review-report-projection/v1"

type TradeShadowReviewReportRunner interface {
	Report(context.Context, applicationreport.Request) (applicationreport.Result, error)
}
type TradeShadowReviewReportOptions struct {
	Enabled bool
	Runner  TradeShadowReviewReportRunner
}
type tradeShadowReviewReportProjection struct {
	ContractVersion             string                                 `json:"contract_version"`
	Status                      string                                 `json:"status"`
	Environment                 string                                 `json:"environment"`
	AuthorizesExternalExecution bool                                   `json:"authorizes_external_execution"`
	PortfolioMutated            bool                                   `json:"portfolio_mutated"`
	KnowledgePromoted           bool                                   `json:"knowledge_promoted"`
	Report                      *moduletrade.PrivateShadowReviewReport `json:"report,omitempty"`
	ReasonCode                  string                                 `json:"reason_code,omitempty"`
}

func HandleTradeShadowReviewReport(options TradeShadowReviewReportOptions) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writeTradeShadowReviewReport(writer, http.StatusMethodNotAllowed, tradeShadowReviewReportProjection{Status: "rejected", ReasonCode: "METHOD_NOT_ALLOWED"})
			return
		}
		if !options.Enabled || options.Runner == nil {
			writeTradeShadowReviewReport(writer, http.StatusServiceUnavailable, tradeShadowReviewReportProjection{Status: "unavailable", ReasonCode: "TRADE_SHADOW_REVIEW_REPORT_UNAVAILABLE"})
			return
		}
		values := request.URL.Query()
		studyIDs, ok := values["study_id"]
		if !ok || len(studyIDs) != 1 || len(values) != 1 || strings.TrimSpace(studyIDs[0]) == "" {
			writeTradeShadowReviewReport(writer, http.StatusBadRequest, tradeShadowReviewReportProjection{Status: "rejected", ReasonCode: "STUDY_ID_REQUIRED"})
			return
		}
		result, err := options.Runner.Report(request.Context(), applicationreport.Request{RequestID: strings.TrimSpace(request.Header.Get("X-Request-ID")), StudyID: studyIDs[0]})
		projection := tradeShadowReviewReportProjection{Status: "ready", Report: result.Report}
		if err == nil {
			writeTradeShadowReviewReport(writer, http.StatusOK, projection)
			return
		}
		if errors.Is(err, applicationreport.ErrInvalidRequest) {
			projection.Status, projection.ReasonCode = "rejected", "INVALID_SHADOW_REVIEW_REPORT"
			writeTradeShadowReviewReport(writer, http.StatusBadRequest, projection)
			return
		}
		projection.Status, projection.ReasonCode = "unavailable", "TRADE_SHADOW_REVIEW_REPORT_UNAVAILABLE"
		writeTradeShadowReviewReport(writer, http.StatusServiceUnavailable, projection)
	}
}

func writeTradeShadowReviewReport(writer http.ResponseWriter, status int, projection tradeShadowReviewReportProjection) {
	projection.ContractVersion = tradeShadowReviewReportProjectionVersion
	projection.Environment = "SHADOW"
	projection.AuthorizesExternalExecution = false
	projection.PortfolioMutated = false
	projection.KnowledgePromoted = false
	writeJSON(writer, status, projection)
}
