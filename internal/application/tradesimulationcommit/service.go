package tradesimulationcommit

import (
	"context"
	"errors"
	"fmt"
	"strings"

	applicationtradepolicy "github.com/Nyukimin/RenCrow_CORE/internal/application/tradepolicy"
	domainpolicy "github.com/Nyukimin/RenCrow_CORE/internal/domain/policydecision"
	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

const Capability = "portfolio_simulation_commit"

var (
	ErrInvalidRequest      = errors.New("invalid TRADE simulation commit request")
	ErrPolicyBlocked       = errors.New("TRADE simulation commit policy blocked")
	ErrPolicyUnavailable   = errors.New("TRADE simulation commit policy unavailable")
	ErrStalePolicyRevision = errors.New("TRADE simulation commit policy revision is stale")
	ErrCommitUnavailable   = errors.New("TRADE simulation commit unavailable")
)

type PolicyEvaluator interface {
	Evaluate(ctx context.Context, request applicationtradepolicy.Request) (applicationtradepolicy.Result, error)
}

type ModuleCommitter interface {
	CommitSimulation(ctx context.Context, correlationID string, request moduletrade.SimulationCommitRequest) (moduletrade.PrivateSimulationCommit, error)
}

type Service struct {
	policy PolicyEvaluator
	commit ModuleCommitter
}

type Request struct {
	RequestID                        string
	TraceID                          string
	Requester                        string
	RequestAllowed                   bool
	IdempotencyKey                   string
	ExpectedPortfolioEventCount      int64
	ExpectedPortfolioLatestEventHash string
	ExpectedInputSnapshotSHA256      string
	Plan                             moduletrade.RiskPreviewPlan
}

type Result struct {
	AuthorizesExternalExecution bool                                 `json:"authorizes_external_execution"`
	PolicyDecision              moduletrade.PolicyDecision           `json:"policy_decision,omitempty"`
	PolicyEvidence              domainpolicy.Record                  `json:"policy_evidence"`
	Commit                      *moduletrade.PrivateSimulationCommit `json:"commit,omitempty"`
}

func NewService(policy PolicyEvaluator, commit ModuleCommitter) (*Service, error) {
	if policy == nil || commit == nil {
		return nil, fmt.Errorf("TRADE simulation commit dependencies are required")
	}
	return &Service{policy: policy, commit: commit}, nil
}

func (service *Service) Commit(ctx context.Context, request Request) (Result, error) {
	if strings.TrimSpace(request.Requester) == "" || strings.TrimSpace(request.IdempotencyKey) == "" {
		return Result{}, ErrInvalidRequest
	}
	scopeRevision := "simulation-commit/sha256:" + request.ExpectedInputSnapshotSHA256
	policyResult, err := service.policy.Evaluate(ctx, applicationtradepolicy.Request{
		RequestID: request.RequestID, TraceID: request.TraceID, Requester: request.Requester,
		Capability: Capability, RequestScopeRevision: scopeRevision, RequestAllowed: request.RequestAllowed,
	})
	result := Result{PolicyDecision: policyResult.Decision, PolicyEvidence: policyResult.Evidence}
	if err != nil {
		return result, fmt.Errorf("%w: %v", ErrPolicyUnavailable, err)
	}
	if policyResult.Decision.Status != "allowed" {
		return result, ErrPolicyBlocked
	}
	if request.Plan.PolicyRevision != policyResult.Decision.GlobalBundleRevision {
		return result, ErrStalePolicyRevision
	}
	moduleRequest := moduletrade.SimulationCommitRequest{
		ContractVersion: moduletrade.SimulationCommitContractVersion,
		RequestID:       request.RequestID, IdempotencyKey: request.IdempotencyKey,
		ExpectedPortfolioEventCount:      request.ExpectedPortfolioEventCount,
		ExpectedPortfolioLatestEventHash: request.ExpectedPortfolioLatestEventHash,
		ExpectedInputSnapshotSHA256:      request.ExpectedInputSnapshotSHA256,
		Plan:                             request.Plan, Policy: policyResult.EvaluationInput,
	}
	if err := moduleRequest.Validate(); err != nil {
		return result, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	commit, err := service.commit.CommitSimulation(ctx, request.RequestID, moduleRequest)
	if err != nil {
		var statusError interface{ HTTPStatus() int }
		if errors.As(err, &statusError) && (statusError.HTTPStatus() == 400 || statusError.HTTPStatus() == 409) {
			return result, fmt.Errorf("%w: TRADE rejected stale or invalid simulation commit", ErrInvalidRequest)
		}
		return result, fmt.Errorf("%w: %v", ErrCommitUnavailable, err)
	}
	result.Commit = &commit
	return result, nil
}
