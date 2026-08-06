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

	applicationcommit "github.com/Nyukimin/RenCrow_CORE/internal/application/tradesimulationcommit"
	domainpolicy "github.com/Nyukimin/RenCrow_CORE/internal/domain/policydecision"
	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

const tradeSimulationCommitProjectionVersion = "trade-simulation-commit-projection/v1"

type TradeSimulationCommitRunner interface {
	Commit(ctx context.Context, request applicationcommit.Request) (applicationcommit.Result, error)
}

type TradeSimulationCommitOptions struct {
	Enabled bool
	Runner  TradeSimulationCommitRunner
}

type tradeSimulationCommitRequest struct {
	RequestID                        string                      `json:"request_id"`
	AllowCommit                      *bool                       `json:"allow_commit"`
	IdempotencyKey                   string                      `json:"idempotency_key"`
	ExpectedPortfolioEventCount      int64                       `json:"expected_portfolio_event_count"`
	ExpectedPortfolioLatestEventHash string                      `json:"expected_portfolio_latest_event_hash"`
	ExpectedInputSnapshotSHA256      string                      `json:"expected_input_snapshot_sha256"`
	Plan                             moduletrade.RiskPreviewPlan `json:"plan"`
}

type tradeSimulationCommitProjection struct {
	ContractVersion             string                               `json:"contract_version"`
	Status                      string                               `json:"status"`
	AuthorizesExternalExecution bool                                 `json:"authorizes_external_execution"`
	PolicyDecision              moduletrade.PolicyDecision           `json:"policy_decision,omitempty"`
	PolicyEvidence              *domainpolicy.Record                 `json:"policy_evidence,omitempty"`
	Commit                      *moduletrade.PrivateSimulationCommit `json:"commit,omitempty"`
	ReasonCode                  string                               `json:"reason_code,omitempty"`
}

func HandleTradeSimulationCommit(options TradeSimulationCommitOptions) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			writeTradeSimulationCommit(writer, http.StatusMethodNotAllowed, tradeSimulationCommitProjection{Status: "rejected", ReasonCode: "METHOD_NOT_ALLOWED"})
			return
		}
		if !options.Enabled || options.Runner == nil {
			writeTradeSimulationCommit(writer, http.StatusServiceUnavailable, tradeSimulationCommitProjection{Status: "unavailable", ReasonCode: "TRADE_SIMULATION_COMMIT_UNAVAILABLE"})
			return
		}
		input, err := decodeTradeSimulationCommitRequest(writer, request)
		if err != nil || input.AllowCommit == nil || !*input.AllowCommit {
			writeTradeSimulationCommit(writer, http.StatusBadRequest, tradeSimulationCommitProjection{Status: "rejected", ReasonCode: "EXPLICIT_COMMIT_REQUIRED"})
			return
		}
		result, err := options.Runner.Commit(request.Context(), applicationcommit.Request{
			RequestID: input.RequestID, TraceID: strings.TrimSpace(request.Header.Get("X-Trace-ID")), Requester: "viewer-trade-simulation-commit",
			RequestAllowed: *input.AllowCommit, IdempotencyKey: input.IdempotencyKey,
			ExpectedPortfolioEventCount: input.ExpectedPortfolioEventCount, ExpectedPortfolioLatestEventHash: input.ExpectedPortfolioLatestEventHash,
			ExpectedInputSnapshotSHA256: input.ExpectedInputSnapshotSHA256, Plan: input.Plan,
		})
		projection := tradeSimulationCommitProjection{Status: "committed", PolicyDecision: result.PolicyDecision, Commit: result.Commit}
		if result.PolicyEvidence.DecisionID != "" {
			projection.PolicyEvidence = &result.PolicyEvidence
		}
		if err == nil {
			writeTradeSimulationCommit(writer, http.StatusOK, projection)
			return
		}
		switch {
		case errors.Is(err, applicationcommit.ErrInvalidRequest), errors.Is(err, applicationcommit.ErrStalePolicyRevision):
			projection.Status, projection.ReasonCode = "rejected", "STALE_OR_INVALID_SIMULATION_COMMIT"
			writeTradeSimulationCommit(writer, http.StatusConflict, projection)
		case errors.Is(err, applicationcommit.ErrPolicyBlocked):
			projection.Status, projection.ReasonCode = "blocked", "POLICY_BLOCKED"
			writeTradeSimulationCommit(writer, http.StatusForbidden, projection)
		default:
			projection.Status, projection.ReasonCode = "unavailable", "TRADE_SIMULATION_COMMIT_UNAVAILABLE"
			writeTradeSimulationCommit(writer, http.StatusServiceUnavailable, projection)
		}
	}
}

func decodeTradeSimulationCommitRequest(writer http.ResponseWriter, request *http.Request) (tradeSimulationCommitRequest, error) {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return tradeSimulationCommitRequest{}, fmt.Errorf("content type must be application/json")
	}
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, maxTradeRiskPreviewRequestBytes))
	decoder.DisallowUnknownFields()
	var value tradeSimulationCommitRequest
	if err := decoder.Decode(&value); err != nil {
		return tradeSimulationCommitRequest{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return tradeSimulationCommitRequest{}, fmt.Errorf("multiple JSON values are not allowed")
	}
	return value, nil
}

func writeTradeSimulationCommit(writer http.ResponseWriter, status int, projection tradeSimulationCommitProjection) {
	projection.ContractVersion = tradeSimulationCommitProjectionVersion
	projection.AuthorizesExternalExecution = false
	writeJSON(writer, status, projection)
}
