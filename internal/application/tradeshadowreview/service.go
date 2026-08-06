package tradeshadowreview

import (
	"context"
	"errors"
	"fmt"
	"strings"

	applicationtradepolicy "github.com/Nyukimin/RenCrow_CORE/internal/application/tradepolicy"
	domainpolicy "github.com/Nyukimin/RenCrow_CORE/internal/domain/policydecision"
	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

const Capability = "shadow_outcome_review_record"

var (
	ErrInvalidRequest    = errors.New("invalid TRADE Shadow review request")
	ErrPolicyBlocked     = errors.New("TRADE Shadow review policy blocked")
	ErrPolicyUnavailable = errors.New("TRADE Shadow review policy unavailable")
	ErrRecordUnavailable = errors.New("TRADE Shadow review unavailable")
)

type PolicyEvaluator interface {
	Evaluate(context.Context, applicationtradepolicy.Request) (applicationtradepolicy.Result, error)
}

type ModuleRecorder interface {
	RecordShadowReview(context.Context, string, moduletrade.ShadowReviewRequest) (moduletrade.PrivateShadowReview, error)
}

type Service struct {
	policy PolicyEvaluator
	record ModuleRecorder
}

type Request struct {
	RequestID      string
	TraceID        string
	Requester      string
	RequestAllowed bool
	Review         moduletrade.ShadowReviewInput
}

type Result struct {
	AuthorizesExternalExecution bool                             `json:"authorizes_external_execution"`
	PolicyDecision              moduletrade.PolicyDecision       `json:"policy_decision,omitempty"`
	PolicyEvidence              domainpolicy.Record              `json:"policy_evidence"`
	Record                      *moduletrade.PrivateShadowReview `json:"record,omitempty"`
}

func NewService(policy PolicyEvaluator, record ModuleRecorder) (*Service, error) {
	if policy == nil || record == nil {
		return nil, fmt.Errorf("TRADE Shadow review dependencies are required")
	}
	return &Service{policy: policy, record: record}, nil
}

func (service *Service) Record(ctx context.Context, request Request) (Result, error) {
	if strings.TrimSpace(request.Requester) == "" || strings.TrimSpace(request.RequestID) == "" {
		return Result{}, ErrInvalidRequest
	}
	policyResult, err := service.policy.Evaluate(ctx, applicationtradepolicy.Request{
		RequestID: request.RequestID, TraceID: request.TraceID, Requester: request.Requester,
		Capability: Capability, RequestScopeRevision: "shadow-review/sha256:" + request.Review.OutcomeReportSHA256,
		RequestAllowed: request.RequestAllowed,
	})
	result := Result{PolicyDecision: policyResult.Decision, PolicyEvidence: policyResult.Evidence}
	if err != nil {
		return result, fmt.Errorf("%w: %v", ErrPolicyUnavailable, err)
	}
	if policyResult.Decision.Status != "allowed" {
		return result, ErrPolicyBlocked
	}
	moduleRequest := moduletrade.ShadowReviewRequest{ContractVersion: moduletrade.ShadowReviewContractVersion, RequestID: request.RequestID, Review: request.Review, Policy: policyResult.EvaluationInput}
	if err := moduleRequest.Validate(); err != nil {
		return result, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	record, err := service.record.RecordShadowReview(ctx, request.RequestID, moduleRequest)
	if err != nil {
		var statusError interface{ HTTPStatus() int }
		if errors.As(err, &statusError) && (statusError.HTTPStatus() == 400 || statusError.HTTPStatus() == 409) {
			return result, fmt.Errorf("%w: TRADE rejected invalid or conflicting Shadow review", ErrInvalidRequest)
		}
		return result, fmt.Errorf("%w: %v", ErrRecordUnavailable, err)
	}
	result.Record = &record
	return result, nil
}
