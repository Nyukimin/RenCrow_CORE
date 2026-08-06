package viewer

import (
	"context"
	"errors"
	"net/http"
	"strings"

	applicationreport "github.com/Nyukimin/RenCrow_CORE/internal/application/tradeshadowoutcomereport"
	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

const tradeShadowOutcomeReportProjectionVersion = "trade-shadow-outcome-report-projection/v1"

type TradeShadowOutcomeReportRunner interface {
	Report(ctx context.Context, request applicationreport.Request) (applicationreport.Result, error)
}

type TradeShadowOutcomeReportOptions struct {
	Enabled bool
	Runner  TradeShadowOutcomeReportRunner
}

type tradeShadowOutcomeReportProjection struct {
	ContractVersion             string                                  `json:"contract_version"`
	Status                      string                                  `json:"status"`
	Environment                 string                                  `json:"environment"`
	AuthorizesExternalExecution bool                                    `json:"authorizes_external_execution"`
	PortfolioMutated            bool                                    `json:"portfolio_mutated"`
	KnowledgePromoted           bool                                    `json:"knowledge_promoted"`
	Report                      *moduletrade.PrivateShadowOutcomeReport `json:"report,omitempty"`
	ReasonCode                  string                                  `json:"reason_code,omitempty"`
}

func HandleTradeShadowOutcomeReport(options TradeShadowOutcomeReportOptions) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writeTradeShadowOutcomeReport(writer, http.StatusMethodNotAllowed, tradeShadowOutcomeReportProjection{Status: "rejected", ReasonCode: "METHOD_NOT_ALLOWED"})
			return
		}
		if !options.Enabled || options.Runner == nil {
			writeTradeShadowOutcomeReport(writer, http.StatusServiceUnavailable, tradeShadowOutcomeReportProjection{Status: "unavailable", ReasonCode: "TRADE_SHADOW_OUTCOME_REPORT_UNAVAILABLE"})
			return
		}
		values := request.URL.Query()
		studyIDs, ok := values["study_id"]
		if !ok || len(studyIDs) != 1 || len(values) != 1 || strings.TrimSpace(studyIDs[0]) == "" {
			writeTradeShadowOutcomeReport(writer, http.StatusBadRequest, tradeShadowOutcomeReportProjection{Status: "rejected", ReasonCode: "STUDY_ID_REQUIRED"})
			return
		}
		result, err := options.Runner.Report(request.Context(), applicationreport.Request{
			RequestID: strings.TrimSpace(request.Header.Get("X-Request-ID")), StudyID: studyIDs[0],
		})
		projection := tradeShadowOutcomeReportProjection{Status: "ready", Report: result.Report}
		if err == nil {
			writeTradeShadowOutcomeReport(writer, http.StatusOK, projection)
			return
		}
		if errors.Is(err, applicationreport.ErrInvalidRequest) {
			projection.Status, projection.ReasonCode = "rejected", "INVALID_SHADOW_OUTCOME_REPORT"
			writeTradeShadowOutcomeReport(writer, http.StatusBadRequest, projection)
			return
		}
		projection.Status, projection.ReasonCode = "unavailable", "TRADE_SHADOW_OUTCOME_REPORT_UNAVAILABLE"
		writeTradeShadowOutcomeReport(writer, http.StatusServiceUnavailable, projection)
	}
}

func writeTradeShadowOutcomeReport(writer http.ResponseWriter, status int, projection tradeShadowOutcomeReportProjection) {
	projection.ContractVersion = tradeShadowOutcomeReportProjectionVersion
	projection.Environment = "SHADOW"
	projection.AuthorizesExternalExecution = false
	projection.PortfolioMutated = false
	projection.KnowledgePromoted = false
	writeJSON(writer, status, projection)
}
