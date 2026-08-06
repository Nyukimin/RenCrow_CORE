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

	applicationtradepolicy "github.com/Nyukimin/RenCrow_CORE/internal/application/tradepolicy"
	domainpolicy "github.com/Nyukimin/RenCrow_CORE/internal/domain/policydecision"
	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

const tradePolicyDiagnosticContractVersion = "trade-policy-diagnostic/v1"
const maxTradePolicyDiagnosticRequestBytes = 64 << 10

type TradePolicyEvaluationRunner interface {
	Evaluate(ctx context.Context, request applicationtradepolicy.Request) (applicationtradepolicy.Result, error)
}

type TradePolicyEvaluationOptions struct {
	Enabled bool
	Runner  TradePolicyEvaluationRunner
}

type tradePolicyEvaluationRequest struct {
	RequestID            string `json:"request_id"`
	Capability           string `json:"capability"`
	RequestScopeRevision string `json:"request_scope_revision"`
	RequestAllowed       *bool  `json:"request_allowed"`
}

type tradePolicyEvaluationProjection struct {
	ContractVersion     string                     `json:"contract_version"`
	Status              string                     `json:"status"`
	AuthorizesExecution bool                       `json:"authorizes_execution"`
	Decision            moduletrade.PolicyDecision `json:"decision,omitempty"`
	Evidence            *domainpolicy.Record       `json:"evidence,omitempty"`
	ReasonCode          string                     `json:"reason_code,omitempty"`
}

func HandleTradePolicyEvaluation(options TradePolicyEvaluationOptions) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			writeJSON(writer, http.StatusMethodNotAllowed, tradePolicyEvaluationProjection{
				ContractVersion: tradePolicyDiagnosticContractVersion, Status: "rejected", ReasonCode: "METHOD_NOT_ALLOWED",
			})
			return
		}
		if !options.Enabled {
			writeJSON(writer, http.StatusServiceUnavailable, tradePolicyEvaluationProjection{
				ContractVersion: tradePolicyDiagnosticContractVersion, Status: "unavailable", ReasonCode: "TRADE_NOT_CONFIGURED",
			})
			return
		}
		if options.Runner == nil {
			writeJSON(writer, http.StatusServiceUnavailable, tradePolicyEvaluationProjection{
				ContractVersion: tradePolicyDiagnosticContractVersion, Status: "unavailable", ReasonCode: "TRADE_POLICY_EVALUATOR_UNAVAILABLE",
			})
			return
		}
		input, err := decodeTradePolicyEvaluationRequest(writer, request)
		if err != nil || input.RequestAllowed == nil {
			writeJSON(writer, http.StatusBadRequest, tradePolicyEvaluationProjection{
				ContractVersion: tradePolicyDiagnosticContractVersion, Status: "rejected", ReasonCode: "INVALID_POLICY_EVALUATION",
			})
			return
		}
		result, err := options.Runner.Evaluate(request.Context(), applicationtradepolicy.Request{
			RequestID:            input.RequestID,
			TraceID:              strings.TrimSpace(request.Header.Get("X-Trace-ID")),
			Requester:            "viewer-trade-policy-diagnostic",
			Capability:           input.Capability,
			RequestScopeRevision: input.RequestScopeRevision,
			RequestAllowed:       *input.RequestAllowed,
		})
		if err != nil {
			status := http.StatusServiceUnavailable
			reasonCode := "TRADE_POLICY_EVALUATION_UNAVAILABLE"
			if errors.Is(err, applicationtradepolicy.ErrInvalidRequest) {
				status = http.StatusBadRequest
				reasonCode = "INVALID_POLICY_EVALUATION"
			} else if errors.Is(err, applicationtradepolicy.ErrGlobalPolicyUnavailable) {
				reasonCode = "GLOBAL_POLICY_UNAVAILABLE"
			} else if errors.Is(err, applicationtradepolicy.ErrEvidenceUnavailable) {
				reasonCode = "POLICY_EVIDENCE_UNAVAILABLE"
			}
			projection := tradePolicyEvaluationProjection{
				ContractVersion: tradePolicyDiagnosticContractVersion,
				Status:          "unavailable",
				Decision:        result.Decision,
				ReasonCode:      reasonCode,
			}
			if result.Evidence.DecisionID != "" {
				projection.Evidence = &result.Evidence
			}
			writeJSON(writer, status, projection)
			return
		}
		writeJSON(writer, http.StatusOK, tradePolicyEvaluationProjection{
			ContractVersion: tradePolicyDiagnosticContractVersion,
			Status:          "evaluated",
			Decision:        result.Decision,
			Evidence:        &result.Evidence,
		})
	}
}

func decodeTradePolicyEvaluationRequest(writer http.ResponseWriter, request *http.Request) (tradePolicyEvaluationRequest, error) {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return tradePolicyEvaluationRequest{}, fmt.Errorf("content type must be application/json")
	}
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, maxTradePolicyDiagnosticRequestBytes))
	decoder.DisallowUnknownFields()
	var value tradePolicyEvaluationRequest
	if err := decoder.Decode(&value); err != nil {
		return tradePolicyEvaluationRequest{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return tradePolicyEvaluationRequest{}, fmt.Errorf("multiple JSON values are not allowed")
		}
		return tradePolicyEvaluationRequest{}, err
	}
	return value, nil
}
