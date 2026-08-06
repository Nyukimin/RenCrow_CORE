package tradeshadowobservation

import (
	"context"
	"errors"
	"fmt"
	"strings"

	applicationtradepolicy "github.com/Nyukimin/RenCrow_CORE/internal/application/tradepolicy"
	domainpolicy "github.com/Nyukimin/RenCrow_CORE/internal/domain/policydecision"
	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

const Capability = "shadow_observation_record"

var (
	ErrInvalidRequest    = errors.New("invalid TRADE Shadow observation request")
	ErrPolicyBlocked     = errors.New("TRADE Shadow observation policy blocked")
	ErrPolicyUnavailable = errors.New("TRADE Shadow observation policy unavailable")
	ErrRecordUnavailable = errors.New("TRADE Shadow observation unavailable")
)

type PolicyEvaluator interface {
	Evaluate(ctx context.Context, request applicationtradepolicy.Request) (applicationtradepolicy.Result, error)
}

type ModuleRecorder interface {
	RecordShadowObservation(ctx context.Context, correlationID string, request moduletrade.ShadowObservationRequest) (moduletrade.PrivateShadowObservation, error)
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
	Observation    moduletrade.ShadowObservationInput
}

type Result struct {
	AuthorizesExternalExecution bool                                  `json:"authorizes_external_execution"`
	PolicyDecision              moduletrade.PolicyDecision            `json:"policy_decision,omitempty"`
	PolicyEvidence              domainpolicy.Record                   `json:"policy_evidence"`
	Record                      *moduletrade.PrivateShadowObservation `json:"record,omitempty"`
}

func NewService(policy PolicyEvaluator, record ModuleRecorder) (*Service, error) {
	if policy == nil || record == nil {
		return nil, fmt.Errorf("TRADE Shadow observation dependencies are required")
	}
	return &Service{policy: policy, record: record}, nil
}

func (service *Service) Record(ctx context.Context, request Request) (Result, error) {
	if strings.TrimSpace(request.Requester) == "" || strings.TrimSpace(request.RequestID) == "" {
		return Result{}, ErrInvalidRequest
	}
	scopeRevision := "shadow-observation/sha256:" + request.Observation.ContextSnapshotSHA256
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
	moduleRequest := moduletrade.ShadowObservationRequest{
		ContractVersion: moduletrade.ShadowObservationContractVersion,
		RequestID:       request.RequestID, Observation: request.Observation, Policy: policyResult.EvaluationInput,
	}
	if err := moduleRequest.Validate(); err != nil {
		return result, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	record, err := service.record.RecordShadowObservation(ctx, request.RequestID, moduleRequest)
	if err != nil {
		var statusError interface{ HTTPStatus() int }
		if errors.As(err, &statusError) && (statusError.HTTPStatus() == 400 || statusError.HTTPStatus() == 409) {
			return result, fmt.Errorf("%w: TRADE rejected invalid or conflicting Shadow observation", ErrInvalidRequest)
		}
		return result, fmt.Errorf("%w: %v", ErrRecordUnavailable, err)
	}
	result.Record = &record
	return result, nil
}
