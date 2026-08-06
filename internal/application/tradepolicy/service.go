package tradepolicy

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	domainbundle "github.com/Nyukimin/RenCrow_CORE/internal/domain/policybundle"
	domaindecision "github.com/Nyukimin/RenCrow_CORE/internal/domain/policydecision"
	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

var lowerSHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

var (
	ErrInvalidRequest          = errors.New("invalid TRADE policy evaluation request")
	ErrGlobalPolicyUnavailable = errors.New("Global Policy is unavailable")
	ErrTradePolicyUnavailable  = errors.New("TRADE policy evaluator is unavailable")
	ErrEvidenceUnavailable     = errors.New("policy decision evidence is unavailable")
)

type SnapshotProvider interface {
	Snapshot() (domainbundle.Snapshot, bool)
}

type ModuleEvaluator interface {
	Evaluate(ctx context.Context, correlationID string, request moduletrade.PolicyEvaluationRequest) (moduletrade.PrivatePolicyEvaluation, error)
}

type DecisionStore interface {
	Save(ctx context.Context, record domaindecision.Record) error
}

type Options struct {
	Snapshots SnapshotProvider
	Evaluator ModuleEvaluator
	Decisions DecisionStore
	Now       func() time.Time
	NewID     func() (string, error)
}

type Service struct {
	snapshots SnapshotProvider
	evaluator ModuleEvaluator
	decisions DecisionStore
	now       func() time.Time
	newID     func() (string, error)
}

type Request struct {
	RequestID            string
	TraceID              string
	Requester            string
	Capability           string
	RequestScopeRevision string
	RequestAllowed       bool
}

type Result struct {
	AuthorizesExecution bool                                `json:"authorizes_execution"`
	Decision            moduletrade.PolicyDecision          `json:"decision,omitempty"`
	Evidence            domaindecision.Record               `json:"evidence"`
	EvaluationInput     moduletrade.PolicyEvaluationRequest `json:"-"`
}

func NewService(options Options) (*Service, error) {
	if options.Snapshots == nil || options.Evaluator == nil || options.Decisions == nil {
		return nil, fmt.Errorf("TRADE policy service dependencies are required")
	}
	if options.Now == nil {
		options.Now = func() time.Time { return time.Now().UTC() }
	}
	if options.NewID == nil {
		options.NewID = newDecisionID
	}
	return &Service{
		snapshots: options.Snapshots,
		evaluator: options.Evaluator,
		decisions: options.Decisions,
		now:       options.Now,
		newID:     options.NewID,
	}, nil
}

func (service *Service) Evaluate(ctx context.Context, request Request) (Result, error) {
	if err := validateRequest(request); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	snapshot, ok := service.snapshots.Snapshot()
	if !ok {
		record, err := service.saveUnavailable(ctx, request, "unavailable", "unavailable", "unavailable", "unavailable", "GLOBAL_POLICY_UNAVAILABLE", request)
		if err != nil {
			return Result{}, err
		}
		return Result{Evidence: record}, ErrGlobalPolicyUnavailable
	}
	if err := validateSnapshot(snapshot); err != nil {
		record, saveErr := service.saveUnavailable(ctx, request, snapshot.BundleRevision, snapshot.BundleRevision+"#deployment", "unavailable", "unavailable", "GLOBAL_POLICY_INVALID", request)
		if saveErr != nil {
			return Result{}, saveErr
		}
		return Result{Evidence: record}, fmt.Errorf("%w: %v", ErrGlobalPolicyUnavailable, err)
	}
	globalAllowed := snapshot.Capabilities[request.Capability]
	deploymentAllowed := !snapshot.ProductionDisabled[request.Capability]
	moduleRequest := moduletrade.PolicyEvaluationRequest{
		ContractVersion: moduletrade.PolicyEvaluationContractVersion,
		RequestID:       request.RequestID,
		Capability:      request.Capability,
		GlobalPolicy: moduletrade.GlobalPolicyInput{
			ContractRevision: domainbundle.ContractRevision,
			BundleRevision:   snapshot.BundleRevision,
			ContentSHA256:    snapshot.ContentSHA256,
			Allowed:          globalAllowed,
		},
		Deployment: moduletrade.PolicyLayerInput{
			Revision: snapshot.BundleRevision + "#deployment",
			Allowed:  deploymentAllowed,
		},
		RequestScope: moduletrade.PolicyLayerInput{
			Revision: request.RequestScopeRevision,
			Allowed:  request.RequestAllowed,
		},
	}
	inputHash, err := hashJSON(moduleRequest)
	if err != nil {
		return Result{}, fmt.Errorf("%w: hash evaluation input: %v", ErrEvidenceUnavailable, err)
	}
	response, err := service.evaluator.Evaluate(ctx, request.RequestID, moduleRequest)
	if err != nil {
		record, saveErr := service.saveUnavailable(ctx, request, snapshot.BundleRevision, moduleRequest.Deployment.Revision, "unavailable", "unavailable", "TRADE_POLICY_UNAVAILABLE", moduleRequest)
		if saveErr != nil {
			return Result{}, saveErr
		}
		return Result{Evidence: record}, fmt.Errorf("%w: %v", ErrTradePolicyUnavailable, err)
	}
	if err := response.Validate(moduleRequest); err != nil {
		record, saveErr := service.saveUnavailable(ctx, request, snapshot.BundleRevision, moduleRequest.Deployment.Revision, "unavailable", "unavailable", "TRADE_POLICY_CONTRACT_INVALID", moduleRequest)
		if saveErr != nil {
			return Result{}, saveErr
		}
		return Result{Evidence: record}, fmt.Errorf("%w: %v", ErrTradePolicyUnavailable, err)
	}
	outcome := domaindecision.OutcomeBlocked
	if response.Decision.Status == "allowed" {
		outcome = domaindecision.OutcomeAllowed
	}
	record, err := service.newRecord(request, snapshot.BundleRevision, moduleRequest.Deployment.Revision, response.Decision.BinaryContractRevision, response.Decision.ModulePolicyRevision, outcome, []string{response.Decision.ReasonCode + ": " + response.Decision.Reason}, inputHash)
	if err != nil {
		return Result{}, err
	}
	record.MatchedPolicyIDs = []string{snapshot.BundleID, response.Decision.PolicyID}
	if err := service.decisions.Save(ctx, record); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrEvidenceUnavailable, err)
	}
	return Result{Decision: response.Decision, Evidence: record, EvaluationInput: moduleRequest}, nil
}

func (service *Service) saveUnavailable(ctx context.Context, request Request, globalRevision, deploymentRevision, binaryRevision, moduleRevision, reason string, input any) (domaindecision.Record, error) {
	inputHash, err := hashJSON(input)
	if err != nil {
		return domaindecision.Record{}, fmt.Errorf("%w: hash unavailable input: %v", ErrEvidenceUnavailable, err)
	}
	record, err := service.newRecord(request, globalRevision, deploymentRevision, binaryRevision, moduleRevision, domaindecision.OutcomeUnavailable, []string{reason}, inputHash)
	if err != nil {
		return domaindecision.Record{}, err
	}
	if err := service.decisions.Save(ctx, record); err != nil {
		return domaindecision.Record{}, fmt.Errorf("%w: %v", ErrEvidenceUnavailable, err)
	}
	return record, nil
}

func (service *Service) newRecord(request Request, globalRevision, deploymentRevision, binaryRevision, moduleRevision string, outcome domaindecision.Outcome, reasons []string, inputHash string) (domaindecision.Record, error) {
	decisionID, err := service.newID()
	if err != nil {
		return domaindecision.Record{}, fmt.Errorf("%w: create decision ID: %v", ErrEvidenceUnavailable, err)
	}
	return domaindecision.Record{
		RecordVersion:          domaindecision.RecordVersion,
		DecisionID:             decisionID,
		TraceID:                request.TraceID,
		RequestID:              request.RequestID,
		Requester:              request.Requester,
		Module:                 "RenCrow_TRADE",
		Action:                 request.Capability,
		BinaryContractRevision: binaryRevision,
		GlobalBundleRevision:   globalRevision,
		ModulePolicyRevision:   moduleRevision,
		DeploymentRevision:     deploymentRevision,
		Outcome:                outcome,
		Reasons:                reasons,
		InputSnapshotSHA256:    inputHash,
		ExecutionResult:        "not_executed",
		CreatedAt:              service.now().UTC(),
	}, nil
}

func validateRequest(request Request) error {
	for name, value := range map[string]string{
		"request_id":             request.RequestID,
		"requester":              request.Requester,
		"capability":             request.Capability,
		"request_scope_revision": request.RequestScopeRevision,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if len(request.RequestID) > 128 {
		return fmt.Errorf("request_id must not exceed 128 bytes")
	}
	return nil
}

func validateSnapshot(snapshot domainbundle.Snapshot) error {
	if strings.TrimSpace(snapshot.BundleID) == "" || strings.TrimSpace(snapshot.BundleRevision) == "" {
		return fmt.Errorf("Global Policy identity is incomplete")
	}
	if !lowerSHA256Pattern.MatchString(snapshot.ContentSHA256) {
		return fmt.Errorf("Global Policy content hash is invalid")
	}
	if snapshot.Capabilities == nil || snapshot.ProductionDisabled == nil {
		return fmt.Errorf("Global Policy capability or deployment map is missing")
	}
	return nil
}

func hashJSON(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:]), nil
}

func newDecisionID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return "trade-policy-" + hex.EncodeToString(random[:]), nil
}
