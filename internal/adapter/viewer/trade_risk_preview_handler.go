package viewer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	applicationpreview "github.com/Nyukimin/RenCrow_CORE/internal/application/traderiskpreview"
	domainpolicy "github.com/Nyukimin/RenCrow_CORE/internal/domain/policydecision"
	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

const tradeRiskPreviewDiagnosticContractVersion = "trade-risk-preview-diagnostic/v1"
const maxTradeRiskPreviewRequestBytes = 1 << 20

type TradeRiskPreviewRunner interface {
	Evaluate(ctx context.Context, request applicationpreview.Request) (applicationpreview.Result, error)
}

type TradeRiskPreviewOptions struct {
	Enabled bool
	Runner  TradeRiskPreviewRunner
}

type tradeRiskPreviewRequest struct {
	RequestID      string                      `json:"request_id"`
	RequestAllowed *bool                       `json:"request_allowed"`
	Plan           moduletrade.RiskPreviewPlan `json:"plan"`
}

type tradeRiskPreviewProjection struct {
	ContractVersion     string                           `json:"contract_version"`
	Status              string                           `json:"status"`
	AuthorizesExecution bool                             `json:"authorizes_execution"`
	MutatesPortfolio    bool                             `json:"mutates_portfolio"`
	PolicyDecision      moduletrade.PolicyDecision       `json:"policy_decision,omitempty"`
	PolicyEvidence      *domainpolicy.Record             `json:"policy_evidence,omitempty"`
	PortfolioID         string                           `json:"portfolio_id,omitempty"`
	PortfolioEventCount int64                            `json:"portfolio_event_count,omitempty"`
	PortfolioLatestHash string                           `json:"portfolio_latest_event_hash,omitempty"`
	Decision            *moduletrade.RiskPreviewDecision `json:"decision,omitempty"`
	ReasonCode          string                           `json:"reason_code,omitempty"`
}

func HandleTradeRiskPreview(options TradeRiskPreviewOptions) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			writeTradeRiskPreview(writer, http.StatusMethodNotAllowed, tradeRiskPreviewProjection{Status: "rejected", ReasonCode: "METHOD_NOT_ALLOWED"})
			return
		}
		if !options.Enabled {
			writeTradeRiskPreview(writer, http.StatusServiceUnavailable, tradeRiskPreviewProjection{Status: "unavailable", ReasonCode: "TRADE_NOT_CONFIGURED"})
			return
		}
		if options.Runner == nil {
			writeTradeRiskPreview(writer, http.StatusServiceUnavailable, tradeRiskPreviewProjection{Status: "unavailable", ReasonCode: "TRADE_RISK_PREVIEW_UNAVAILABLE"})
			return
		}
		input, err := decodeTradeRiskPreviewRequest(writer, request)
		if err != nil || input.RequestAllowed == nil {
			writeTradeRiskPreview(writer, http.StatusBadRequest, tradeRiskPreviewProjection{Status: "rejected", ReasonCode: "INVALID_RISK_PREVIEW"})
			return
		}
		result, err := options.Runner.Evaluate(request.Context(), applicationpreview.Request{
			RequestID:      input.RequestID,
			TraceID:        strings.TrimSpace(request.Header.Get("X-Trace-ID")),
			Requester:      "viewer-trade-risk-preview",
			RequestAllowed: *input.RequestAllowed,
			Plan:           input.Plan,
		})
		projection := tradeRiskPreviewProjection{
			Status:         "evaluated",
			PolicyDecision: result.PolicyDecision,
		}
		if result.PolicyEvidence.DecisionID != "" {
			projection.PolicyEvidence = &result.PolicyEvidence
		}
		if result.Preview != nil {
			projection.PortfolioID = result.Preview.PortfolioID
			projection.PortfolioEventCount = result.Preview.PortfolioEventCount
			projection.PortfolioLatestHash = result.Preview.PortfolioLatestEventHash
			projection.Decision = &result.Preview.Decision
		}
		if err == nil {
			writeTradeRiskPreview(writer, http.StatusOK, projection)
			return
		}
		switch {
		case errors.Is(err, applicationpreview.ErrInvalidRequest):
			projection.Status = "rejected"
			projection.ReasonCode = "INVALID_RISK_PREVIEW"
			writeTradeRiskPreview(writer, http.StatusBadRequest, projection)
		case errors.Is(err, applicationpreview.ErrPolicyBlocked):
			projection.Status = "blocked"
			projection.ReasonCode = "POLICY_BLOCKED"
			writeTradeRiskPreview(writer, http.StatusOK, projection)
		case errors.Is(err, applicationpreview.ErrStalePolicyRevision):
			projection.Status = "blocked"
			projection.ReasonCode = "STALE_POLICY_REVISION"
			writeTradeRiskPreview(writer, http.StatusConflict, projection)
		case errors.Is(err, applicationpreview.ErrPolicyUnavailable):
			projection.Status = "unavailable"
			projection.ReasonCode = "TRADE_POLICY_UNAVAILABLE"
			writeTradeRiskPreview(writer, http.StatusServiceUnavailable, projection)
		default:
			projection.Status = "unavailable"
			projection.ReasonCode = "TRADE_RISK_PREVIEW_UNAVAILABLE"
			writeTradeRiskPreview(writer, http.StatusServiceUnavailable, projection)
		}
	}
}

func decodeTradeRiskPreviewRequest(writer http.ResponseWriter, request *http.Request) (tradeRiskPreviewRequest, error) {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return tradeRiskPreviewRequest{}, fmt.Errorf("content type must be application/json")
	}
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, maxTradeRiskPreviewRequestBytes))
	decoder.DisallowUnknownFields()
	var value tradeRiskPreviewRequest
	if err := decoder.Decode(&value); err != nil {
		return tradeRiskPreviewRequest{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return tradeRiskPreviewRequest{}, fmt.Errorf("multiple JSON values are not allowed")
		}
		return tradeRiskPreviewRequest{}, err
	}
	return value, nil
}

func writeTradeRiskPreview(writer http.ResponseWriter, status int, projection tradeRiskPreviewProjection) {
	projection.ContractVersion = tradeRiskPreviewDiagnosticContractVersion
	projection.AuthorizesExecution = false
	projection.MutatesPortfolio = false
	writeJSON(writer, status, projection)
}
