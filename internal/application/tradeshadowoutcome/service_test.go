package tradeshadowoutcome

import (
	"context"
	"errors"
	"strings"
	"testing"

	applicationtradepolicy "github.com/Nyukimin/RenCrow_CORE/internal/application/tradepolicy"
	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

type policyStub struct {
	result applicationtradepolicy.Result
	got    applicationtradepolicy.Request
}

func (stub *policyStub) Evaluate(_ context.Context, request applicationtradepolicy.Request) (applicationtradepolicy.Result, error) {
	stub.got = request
	return stub.result, nil
}

type recordStub struct {
	got moduletrade.ShadowOutcomeRequest
}

func (stub *recordStub) RecordShadowOutcome(_ context.Context, _ string, request moduletrade.ShadowOutcomeRequest) (moduletrade.PrivateShadowOutcome, error) {
	stub.got = request
	return moduletrade.PrivateShadowOutcome{Environment: "SHADOW"}, nil
}

func TestRecordBindsOutcomeSnapshotToPolicyScope(t *testing.T) {
	input := validOutcome()
	scope := "shadow-outcome/sha256:" + input.OutcomeSnapshotSHA256
	policyInput := moduletrade.PolicyEvaluationRequest{
		ContractVersion: moduletrade.PolicyEvaluationContractVersion, RequestID: "outcome-1", Capability: Capability,
		GlobalPolicy: moduletrade.GlobalPolicyInput{ContractRevision: "global-policy/v1", BundleRevision: "2026-08-06.1", ContentSHA256: strings.Repeat("d", 64), Allowed: true},
		Deployment:   moduletrade.PolicyLayerInput{Revision: "deployment-1", Allowed: true}, RequestScope: moduletrade.PolicyLayerInput{Revision: scope, Allowed: true},
	}
	policy := &policyStub{result: applicationtradepolicy.Result{Decision: moduletrade.PolicyDecision{Status: "allowed"}, EvaluationInput: policyInput}}
	recorder := &recordStub{}
	service, err := NewService(policy, recorder)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Record(context.Background(), Request{RequestID: "outcome-1", Requester: "test", RequestAllowed: true, Outcome: input})
	if err != nil || result.Record == nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if policy.got.RequestScopeRevision != scope || recorder.got.Policy.RequestScope.Revision != scope {
		t.Fatalf("policy=%+v record=%+v", policy.got, recorder.got)
	}
}

func TestRecordStopsBeforeModuleWhenPolicyBlocks(t *testing.T) {
	policy := &policyStub{result: applicationtradepolicy.Result{Decision: moduletrade.PolicyDecision{Status: "blocked"}}}
	recorder := &recordStub{}
	service, _ := NewService(policy, recorder)
	_, err := service.Record(context.Background(), Request{RequestID: "outcome-1", Requester: "test", Outcome: validOutcome()})
	if !errors.Is(err, ErrPolicyBlocked) || recorder.got.RequestID != "" {
		t.Fatalf("err=%v record=%+v", err, recorder.got)
	}
}

func validOutcome() moduletrade.ShadowOutcomeInput {
	return moduletrade.ShadowOutcomeInput{
		IdempotencyKey: "outcome-key-1", StudyID: "study-1", DecisionID: "decision-1", MarketObservedAt: "2026-08-06T12:00:00Z",
		OutcomeLabel: "success", OutcomeObservedAt: "2026-08-07T12:00:00Z", OutcomeSnapshotSHA256: strings.Repeat("c", 64),
		OutcomeReasonCodes: []string{"THESIS_CONFIRMED"}, OutcomeEvidenceRefs: []string{"source/outcome-1"}, OutcomeLabelContractSHA256: strings.Repeat("b", 64),
	}
}
