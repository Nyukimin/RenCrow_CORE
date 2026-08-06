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

	applicationrecord "github.com/Nyukimin/RenCrow_CORE/internal/application/tradeshadowoutcome"
	domainpolicy "github.com/Nyukimin/RenCrow_CORE/internal/domain/policydecision"
	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

const tradeShadowOutcomeProjectionVersion = "trade-shadow-outcome-projection/v1"

type TradeShadowOutcomeRunner interface {
	Record(ctx context.Context, request applicationrecord.Request) (applicationrecord.Result, error)
}

type TradeShadowOutcomeOptions struct {
	Enabled bool
	Runner  TradeShadowOutcomeRunner
}

type tradeShadowOutcomeRequest struct {
	RequestID   string                         `json:"request_id"`
	AllowRecord *bool                          `json:"allow_record"`
	Outcome     moduletrade.ShadowOutcomeInput `json:"outcome"`
}

type tradeShadowOutcomeProjection struct {
	ContractVersion             string                            `json:"contract_version"`
	Status                      string                            `json:"status"`
	Environment                 string                            `json:"environment"`
	AuthorizesExternalExecution bool                              `json:"authorizes_external_execution"`
	PortfolioMutated            bool                              `json:"portfolio_mutated"`
	KnowledgePromoted           bool                              `json:"knowledge_promoted"`
	PolicyDecision              moduletrade.PolicyDecision        `json:"policy_decision,omitempty"`
	PolicyEvidence              *domainpolicy.Record              `json:"policy_evidence,omitempty"`
	Record                      *moduletrade.PrivateShadowOutcome `json:"record,omitempty"`
	ReasonCode                  string                            `json:"reason_code,omitempty"`
}

func HandleTradeShadowOutcome(options TradeShadowOutcomeOptions) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			writeTradeShadowOutcome(writer, http.StatusMethodNotAllowed, tradeShadowOutcomeProjection{Status: "rejected", ReasonCode: "METHOD_NOT_ALLOWED"})
			return
		}
		if !options.Enabled || options.Runner == nil {
			writeTradeShadowOutcome(writer, http.StatusServiceUnavailable, tradeShadowOutcomeProjection{Status: "unavailable", ReasonCode: "TRADE_SHADOW_OUTCOME_UNAVAILABLE"})
			return
		}
		input, err := decodeTradeShadowOutcomeRequest(writer, request)
		if err != nil || input.AllowRecord == nil || !*input.AllowRecord {
			writeTradeShadowOutcome(writer, http.StatusBadRequest, tradeShadowOutcomeProjection{Status: "rejected", ReasonCode: "EXPLICIT_RECORD_REQUIRED"})
			return
		}
		result, err := options.Runner.Record(request.Context(), applicationrecord.Request{
			RequestID: input.RequestID, TraceID: strings.TrimSpace(request.Header.Get("X-Trace-ID")), Requester: "viewer-trade-shadow-outcome",
			RequestAllowed: *input.AllowRecord, Outcome: input.Outcome,
		})
		projection := tradeShadowOutcomeProjection{Status: "recorded", PolicyDecision: result.PolicyDecision, Record: result.Record}
		if result.PolicyEvidence.DecisionID != "" {
			projection.PolicyEvidence = &result.PolicyEvidence
		}
		if err == nil {
			writeTradeShadowOutcome(writer, http.StatusOK, projection)
			return
		}
		switch {
		case errors.Is(err, applicationrecord.ErrInvalidRequest):
			projection.Status, projection.ReasonCode = "rejected", "INVALID_OR_CONFLICTING_SHADOW_OUTCOME"
			writeTradeShadowOutcome(writer, http.StatusConflict, projection)
		case errors.Is(err, applicationrecord.ErrPolicyBlocked):
			projection.Status, projection.ReasonCode = "blocked", "POLICY_BLOCKED"
			writeTradeShadowOutcome(writer, http.StatusForbidden, projection)
		default:
			projection.Status, projection.ReasonCode = "unavailable", "TRADE_SHADOW_OUTCOME_UNAVAILABLE"
			writeTradeShadowOutcome(writer, http.StatusServiceUnavailable, projection)
		}
	}
}

func decodeTradeShadowOutcomeRequest(writer http.ResponseWriter, request *http.Request) (tradeShadowOutcomeRequest, error) {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return tradeShadowOutcomeRequest{}, fmt.Errorf("content type must be application/json")
	}
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, maxTradeRiskPreviewRequestBytes))
	decoder.DisallowUnknownFields()
	var value tradeShadowOutcomeRequest
	if err := decoder.Decode(&value); err != nil {
		return tradeShadowOutcomeRequest{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return tradeShadowOutcomeRequest{}, fmt.Errorf("multiple JSON values are not allowed")
	}
	return value, nil
}

func writeTradeShadowOutcome(writer http.ResponseWriter, status int, projection tradeShadowOutcomeProjection) {
	projection.ContractVersion = tradeShadowOutcomeProjectionVersion
	projection.Environment = "SHADOW"
	projection.AuthorizesExternalExecution = false
	projection.PortfolioMutated = false
	projection.KnowledgePromoted = false
	writeJSON(writer, status, projection)
}
