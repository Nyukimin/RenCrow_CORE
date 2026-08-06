package traderiskpreview

import (
	"context"
	"errors"
	"strings"
	"testing"

	applicationtradepolicy "github.com/Nyukimin/RenCrow_CORE/internal/application/tradepolicy"
	domainpolicy "github.com/Nyukimin/RenCrow_CORE/internal/domain/policydecision"
	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

type policyStub struct {
	request applicationtradepolicy.Request
	result  applicationtradepolicy.Result
	err     error
}

func (stub *policyStub) Evaluate(_ context.Context, request applicationtradepolicy.Request) (applicationtradepolicy.Result, error) {
	stub.request = request
	return stub.result, stub.err
}

type previewStub struct {
	request moduletrade.RiskPreviewRequest
	result  moduletrade.PrivateRiskPreview
	calls   int
	err     error
}

type previewHTTPError int

func (err previewHTTPError) Error() string   { return "preview HTTP error" }
func (err previewHTTPError) HTTPStatus() int { return int(err) }

func (stub *previewStub) PreviewRisk(_ context.Context, _ string, request moduletrade.RiskPreviewRequest) (moduletrade.PrivateRiskPreview, error) {
	stub.calls++
	stub.request = request
	return stub.result, stub.err
}

func validPlan() moduletrade.RiskPreviewPlan {
	return moduletrade.RiskPreviewPlan{
		ContractVersion: moduletrade.RiskPreviewPlanContractVersion,
		PlanID:          "plan-1", PolicyRevision: "2026-08-06.1", AsOf: "2026-08-06T00:00:00Z",
		Selection:    moduletrade.RiskPreviewSelection{InstrumentID: "JP-TEST", Status: "selected", EvidenceRefs: []string{"source:1"}, KnownMissingData: []string{}},
		Proposal:     moduletrade.RiskPreviewBuyProposal{InstrumentID: "JP-TEST", Side: "buy", Quantity: 100, EntryPriceJPY: 1000, GrossJPY: 100000},
		ExitContract: moduletrade.RiskPreviewExitContract{ContractID: "exit-1", Revision: "v1", InstrumentID: "JP-TEST"},
	}
}

func allowedPolicyResult() applicationtradepolicy.Result {
	return applicationtradepolicy.Result{
		Decision: moduletrade.PolicyDecision{Capability: Capability, Status: "allowed", GlobalBundleRevision: "2026-08-06.1"},
		Evidence: domainpolicy.Record{DecisionID: "policy-1", GlobalBundleRevision: "2026-08-06.1"},
	}
}

func TestServiceBindsPlanHashToPolicyBeforePreview(t *testing.T) {
	policy := &policyStub{result: allowedPolicyResult()}
	preview := &previewStub{result: moduletrade.PrivateRiskPreview{RequestID: "request-1"}}
	service, err := NewService(policy, preview)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Evaluate(context.Background(), Request{
		RequestID: "request-1", TraceID: "trace-1", Requester: "viewer-trade-risk-preview", RequestAllowed: true, Plan: validPlan(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.AuthorizesExecution || result.MutatesPortfolio || result.Preview == nil || preview.calls != 1 {
		t.Fatalf("result=%+v calls=%d", result, preview.calls)
	}
	if policy.request.Capability != Capability || !strings.HasPrefix(policy.request.RequestScopeRevision, "risk-preview-plan/sha256:") || len(strings.TrimPrefix(policy.request.RequestScopeRevision, "risk-preview-plan/sha256:")) != 64 {
		t.Fatalf("policy request=%+v", policy.request)
	}
}

func TestServiceDoesNotPreviewWhenPolicyBlocksOrPlanRevisionIsStale(t *testing.T) {
	blockedPolicy := allowedPolicyResult()
	blockedPolicy.Decision.Status = "blocked"
	preview := &previewStub{}
	service, _ := NewService(&policyStub{result: blockedPolicy}, preview)
	_, err := service.Evaluate(context.Background(), Request{RequestID: "request-1", Requester: "viewer", RequestAllowed: true, Plan: validPlan()})
	if !errors.Is(err, ErrPolicyBlocked) || preview.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, preview.calls)
	}

	stale := validPlan()
	stale.PolicyRevision = "2026-08-05.1"
	service, _ = NewService(&policyStub{result: allowedPolicyResult()}, preview)
	_, err = service.Evaluate(context.Background(), Request{RequestID: "request-2", Requester: "viewer", RequestAllowed: true, Plan: stale})
	if !errors.Is(err, ErrStalePolicyRevision) || preview.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, preview.calls)
	}
}

func TestServiceClassifiesTradePlanRejectionAsInvalidInput(t *testing.T) {
	preview := &previewStub{err: previewHTTPError(400)}
	service, _ := NewService(&policyStub{result: allowedPolicyResult()}, preview)
	_, err := service.Evaluate(context.Background(), Request{
		RequestID: "request-1", Requester: "viewer", RequestAllowed: true, Plan: validPlan(),
	})
	if !errors.Is(err, ErrInvalidRequest) || preview.calls != 1 {
		t.Fatalf("err=%v calls=%d", err, preview.calls)
	}
}
