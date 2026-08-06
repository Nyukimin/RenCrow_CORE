package traderiskpreview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	applicationtradepolicy "github.com/Nyukimin/RenCrow_CORE/internal/application/tradepolicy"
	domainpolicy "github.com/Nyukimin/RenCrow_CORE/internal/domain/policydecision"
	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

const Capability = "portfolio_risk_preview"

var (
	ErrInvalidRequest      = errors.New("invalid TRADE risk preview request")
	ErrPolicyBlocked       = errors.New("TRADE risk preview policy blocked")
	ErrPolicyUnavailable   = errors.New("TRADE risk preview policy unavailable")
	ErrStalePolicyRevision = errors.New("TRADE risk preview policy revision is stale")
	ErrPreviewUnavailable  = errors.New("TRADE risk preview unavailable")
)

type PolicyEvaluator interface {
	Evaluate(ctx context.Context, request applicationtradepolicy.Request) (applicationtradepolicy.Result, error)
}

type ModulePreviewer interface {
	PreviewRisk(ctx context.Context, correlationID string, request moduletrade.RiskPreviewRequest) (moduletrade.PrivateRiskPreview, error)
}

type Service struct {
	policy  PolicyEvaluator
	preview ModulePreviewer
}

type Request struct {
	RequestID      string
	TraceID        string
	Requester      string
	RequestAllowed bool
	Plan           moduletrade.RiskPreviewPlan
}

type Result struct {
	AuthorizesExecution bool                            `json:"authorizes_execution"`
	MutatesPortfolio    bool                            `json:"mutates_portfolio"`
	PolicyDecision      moduletrade.PolicyDecision      `json:"policy_decision,omitempty"`
	PolicyEvidence      domainpolicy.Record             `json:"policy_evidence"`
	Preview             *moduletrade.PrivateRiskPreview `json:"preview,omitempty"`
}

func NewService(policy PolicyEvaluator, preview ModulePreviewer) (*Service, error) {
	if policy == nil || preview == nil {
		return nil, fmt.Errorf("TRADE risk preview dependencies are required")
	}
	return &Service{policy: policy, preview: preview}, nil
}

func (service *Service) Evaluate(ctx context.Context, request Request) (Result, error) {
	moduleRequest := moduletrade.RiskPreviewRequest{
		ContractVersion: moduletrade.RiskPreviewRequestContractVersion,
		RequestID:       request.RequestID,
		Plan:            request.Plan,
	}
	if strings.TrimSpace(request.Requester) == "" {
		return Result{}, fmt.Errorf("%w: requester is required", ErrInvalidRequest)
	}
	if err := moduleRequest.Validate(); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	planHash, err := hashPlan(request.Plan)
	if err != nil {
		return Result{}, fmt.Errorf("%w: hash plan: %v", ErrInvalidRequest, err)
	}
	policyResult, err := service.policy.Evaluate(ctx, applicationtradepolicy.Request{
		RequestID:            request.RequestID,
		TraceID:              request.TraceID,
		Requester:            request.Requester,
		Capability:           Capability,
		RequestScopeRevision: "risk-preview-plan/sha256:" + planHash,
		RequestAllowed:       request.RequestAllowed,
	})
	result := Result{PolicyDecision: policyResult.Decision, PolicyEvidence: policyResult.Evidence}
	if err != nil {
		return result, fmt.Errorf("%w: %v", ErrPolicyUnavailable, err)
	}
	if policyResult.Decision.Status != "allowed" {
		return result, ErrPolicyBlocked
	}
	if request.Plan.PolicyRevision != policyResult.Decision.GlobalBundleRevision {
		return result, fmt.Errorf("%w: plan=%q active=%q", ErrStalePolicyRevision, request.Plan.PolicyRevision, policyResult.Decision.GlobalBundleRevision)
	}
	preview, err := service.preview.PreviewRisk(ctx, request.RequestID, moduleRequest)
	if err != nil {
		var statusError interface{ HTTPStatus() int }
		if errors.As(err, &statusError) && statusError.HTTPStatus() == 400 {
			return result, fmt.Errorf("%w: TRADE rejected the risk preview plan", ErrInvalidRequest)
		}
		return result, fmt.Errorf("%w: %v", ErrPreviewUnavailable, err)
	}
	result.Preview = &preview
	return result, nil
}

func hashPlan(plan moduletrade.RiskPreviewPlan) (string, error) {
	payload, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}
