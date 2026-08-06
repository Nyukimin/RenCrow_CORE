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

	applicationrecord "github.com/Nyukimin/RenCrow_CORE/internal/application/tradeshadowobservation"
	domainpolicy "github.com/Nyukimin/RenCrow_CORE/internal/domain/policydecision"
	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

const tradeShadowObservationProjectionVersion = "trade-shadow-observation-projection/v1"

type TradeShadowObservationRunner interface {
	Record(ctx context.Context, request applicationrecord.Request) (applicationrecord.Result, error)
}

type TradeShadowObservationOptions struct {
	Enabled bool
	Runner  TradeShadowObservationRunner
}

type tradeShadowObservationRequest struct {
	RequestID   string                             `json:"request_id"`
	AllowRecord *bool                              `json:"allow_record"`
	Observation moduletrade.ShadowObservationInput `json:"observation"`
}

type tradeShadowObservationProjection struct {
	ContractVersion             string                                `json:"contract_version"`
	Status                      string                                `json:"status"`
	Environment                 string                                `json:"environment"`
	AuthorizesExternalExecution bool                                  `json:"authorizes_external_execution"`
	PortfolioMutated            bool                                  `json:"portfolio_mutated"`
	KnowledgePromoted           bool                                  `json:"knowledge_promoted"`
	PolicyDecision              moduletrade.PolicyDecision            `json:"policy_decision,omitempty"`
	PolicyEvidence              *domainpolicy.Record                  `json:"policy_evidence,omitempty"`
	Record                      *moduletrade.PrivateShadowObservation `json:"record,omitempty"`
	ReasonCode                  string                                `json:"reason_code,omitempty"`
}

func HandleTradeShadowObservation(options TradeShadowObservationOptions) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			writeTradeShadowObservation(writer, http.StatusMethodNotAllowed, tradeShadowObservationProjection{Status: "rejected", ReasonCode: "METHOD_NOT_ALLOWED"})
			return
		}
		if !options.Enabled || options.Runner == nil {
			writeTradeShadowObservation(writer, http.StatusServiceUnavailable, tradeShadowObservationProjection{Status: "unavailable", ReasonCode: "TRADE_SHADOW_OBSERVATION_UNAVAILABLE"})
			return
		}
		input, err := decodeTradeShadowObservationRequest(writer, request)
		if err != nil || input.AllowRecord == nil || !*input.AllowRecord {
			writeTradeShadowObservation(writer, http.StatusBadRequest, tradeShadowObservationProjection{Status: "rejected", ReasonCode: "EXPLICIT_RECORD_REQUIRED"})
			return
		}
		result, err := options.Runner.Record(request.Context(), applicationrecord.Request{
			RequestID: input.RequestID, TraceID: strings.TrimSpace(request.Header.Get("X-Trace-ID")), Requester: "viewer-trade-shadow-observation",
			RequestAllowed: *input.AllowRecord, Observation: input.Observation,
		})
		projection := tradeShadowObservationProjection{Status: "recorded", PolicyDecision: result.PolicyDecision, Record: result.Record}
		if result.PolicyEvidence.DecisionID != "" {
			projection.PolicyEvidence = &result.PolicyEvidence
		}
		if err == nil {
			writeTradeShadowObservation(writer, http.StatusOK, projection)
			return
		}
		switch {
		case errors.Is(err, applicationrecord.ErrInvalidRequest):
			projection.Status, projection.ReasonCode = "rejected", "INVALID_OR_CONFLICTING_SHADOW_OBSERVATION"
			writeTradeShadowObservation(writer, http.StatusConflict, projection)
		case errors.Is(err, applicationrecord.ErrPolicyBlocked):
			projection.Status, projection.ReasonCode = "blocked", "POLICY_BLOCKED"
			writeTradeShadowObservation(writer, http.StatusForbidden, projection)
		default:
			projection.Status, projection.ReasonCode = "unavailable", "TRADE_SHADOW_OBSERVATION_UNAVAILABLE"
			writeTradeShadowObservation(writer, http.StatusServiceUnavailable, projection)
		}
	}
}

func decodeTradeShadowObservationRequest(writer http.ResponseWriter, request *http.Request) (tradeShadowObservationRequest, error) {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return tradeShadowObservationRequest{}, fmt.Errorf("content type must be application/json")
	}
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, maxTradeRiskPreviewRequestBytes))
	decoder.DisallowUnknownFields()
	var value tradeShadowObservationRequest
	if err := decoder.Decode(&value); err != nil {
		return tradeShadowObservationRequest{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return tradeShadowObservationRequest{}, fmt.Errorf("multiple JSON values are not allowed")
	}
	return value, nil
}

func writeTradeShadowObservation(writer http.ResponseWriter, status int, projection tradeShadowObservationProjection) {
	projection.ContractVersion = tradeShadowObservationProjectionVersion
	projection.Environment = "SHADOW"
	projection.AuthorizesExternalExecution = false
	projection.PortfolioMutated = false
	projection.KnowledgePromoted = false
	writeJSON(writer, status, projection)
}
