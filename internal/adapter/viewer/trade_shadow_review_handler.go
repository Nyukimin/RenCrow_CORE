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

	applicationreview "github.com/Nyukimin/RenCrow_CORE/internal/application/tradeshadowreview"
	domainpolicy "github.com/Nyukimin/RenCrow_CORE/internal/domain/policydecision"
	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

const tradeShadowReviewProjectionVersion = "trade-shadow-review-projection/v1"

type TradeShadowReviewRunner interface {
	Record(context.Context, applicationreview.Request) (applicationreview.Result, error)
}
type TradeShadowReviewOptions struct {
	Enabled bool
	Runner  TradeShadowReviewRunner
}
type tradeShadowReviewRequest struct {
	RequestID   string                        `json:"request_id"`
	AllowRecord *bool                         `json:"allow_record"`
	Review      moduletrade.ShadowReviewInput `json:"review"`
}
type tradeShadowReviewProjection struct {
	ContractVersion             string                           `json:"contract_version"`
	Status                      string                           `json:"status"`
	Environment                 string                           `json:"environment"`
	AuthorizesExternalExecution bool                             `json:"authorizes_external_execution"`
	PortfolioMutated            bool                             `json:"portfolio_mutated"`
	KnowledgePromoted           bool                             `json:"knowledge_promoted"`
	PolicyDecision              moduletrade.PolicyDecision       `json:"policy_decision,omitempty"`
	PolicyEvidence              *domainpolicy.Record             `json:"policy_evidence,omitempty"`
	Record                      *moduletrade.PrivateShadowReview `json:"record,omitempty"`
	ReasonCode                  string                           `json:"reason_code,omitempty"`
}

func HandleTradeShadowReview(options TradeShadowReviewOptions) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			writeTradeShadowReview(writer, http.StatusMethodNotAllowed, tradeShadowReviewProjection{Status: "rejected", ReasonCode: "METHOD_NOT_ALLOWED"})
			return
		}
		if !options.Enabled || options.Runner == nil {
			writeTradeShadowReview(writer, http.StatusServiceUnavailable, tradeShadowReviewProjection{Status: "unavailable", ReasonCode: "TRADE_SHADOW_REVIEW_UNAVAILABLE"})
			return
		}
		input, err := decodeTradeShadowReviewRequest(writer, request)
		if err != nil || input.AllowRecord == nil || !*input.AllowRecord {
			writeTradeShadowReview(writer, http.StatusBadRequest, tradeShadowReviewProjection{Status: "rejected", ReasonCode: "EXPLICIT_RECORD_REQUIRED"})
			return
		}
		result, err := options.Runner.Record(request.Context(), applicationreview.Request{RequestID: input.RequestID, TraceID: strings.TrimSpace(request.Header.Get("X-Trace-ID")), Requester: "viewer-trade-shadow-review", RequestAllowed: *input.AllowRecord, Review: input.Review})
		projection := tradeShadowReviewProjection{Status: "recorded", PolicyDecision: result.PolicyDecision, Record: result.Record}
		if result.PolicyEvidence.DecisionID != "" {
			projection.PolicyEvidence = &result.PolicyEvidence
		}
		if err == nil {
			writeTradeShadowReview(writer, http.StatusOK, projection)
			return
		}
		switch {
		case errors.Is(err, applicationreview.ErrInvalidRequest):
			projection.Status, projection.ReasonCode = "rejected", "INVALID_OR_CONFLICTING_SHADOW_REVIEW"
			writeTradeShadowReview(writer, http.StatusConflict, projection)
		case errors.Is(err, applicationreview.ErrPolicyBlocked):
			projection.Status, projection.ReasonCode = "blocked", "POLICY_BLOCKED"
			writeTradeShadowReview(writer, http.StatusForbidden, projection)
		default:
			projection.Status, projection.ReasonCode = "unavailable", "TRADE_SHADOW_REVIEW_UNAVAILABLE"
			writeTradeShadowReview(writer, http.StatusServiceUnavailable, projection)
		}
	}
}

func decodeTradeShadowReviewRequest(writer http.ResponseWriter, request *http.Request) (tradeShadowReviewRequest, error) {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return tradeShadowReviewRequest{}, fmt.Errorf("content type must be application/json")
	}
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, maxTradeRiskPreviewRequestBytes))
	decoder.DisallowUnknownFields()
	var value tradeShadowReviewRequest
	if err := decoder.Decode(&value); err != nil {
		return tradeShadowReviewRequest{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return tradeShadowReviewRequest{}, fmt.Errorf("multiple JSON values are not allowed")
	}
	return value, nil
}

func writeTradeShadowReview(writer http.ResponseWriter, status int, projection tradeShadowReviewProjection) {
	projection.ContractVersion = tradeShadowReviewProjectionVersion
	projection.Environment = "SHADOW"
	projection.AuthorizesExternalExecution = false
	projection.PortfolioMutated = false
	projection.KnowledgePromoted = false
	writeJSON(writer, status, projection)
}
