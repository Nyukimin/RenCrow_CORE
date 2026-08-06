package tradeshadowobservation

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
	got moduletrade.ShadowObservationRequest
}

func (stub *recordStub) RecordShadowObservation(_ context.Context, _ string, request moduletrade.ShadowObservationRequest) (moduletrade.PrivateShadowObservation, error) {
	stub.got = request
	return moduletrade.PrivateShadowObservation{Environment: "SHADOW"}, nil
}

func TestRecordBindsPolicyToContextBeforeModuleWrite(t *testing.T) {
	observation := validObservation()
	scope := "shadow-observation/sha256:" + observation.ContextSnapshotSHA256
	policyInput := moduletrade.PolicyEvaluationRequest{
		ContractVersion: moduletrade.PolicyEvaluationContractVersion, RequestID: "shadow-1", Capability: Capability,
		GlobalPolicy: moduletrade.GlobalPolicyInput{ContractRevision: "global-policy/v1", BundleRevision: "2026-08-06.1", ContentSHA256: strings.Repeat("c", 64), Allowed: true},
		Deployment:   moduletrade.PolicyLayerInput{Revision: "deployment-1", Allowed: true}, RequestScope: moduletrade.PolicyLayerInput{Revision: scope, Allowed: true},
	}
	policy := &policyStub{result: applicationtradepolicy.Result{Decision: moduletrade.PolicyDecision{Status: "allowed"}, EvaluationInput: policyInput}}
	recorder := &recordStub{}
	service, err := NewService(policy, recorder)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Record(context.Background(), Request{RequestID: "shadow-1", Requester: "test", RequestAllowed: true, Observation: observation})
	if err != nil || result.Record == nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if policy.got.RequestScopeRevision != scope || recorder.got.Policy.RequestScope.Revision != scope || recorder.got.Observation.ActorID != "mio" {
		t.Fatalf("policy=%+v record=%+v", policy.got, recorder.got)
	}
}

func TestRecordStopsBeforeModuleWhenPolicyBlocks(t *testing.T) {
	policy := &policyStub{result: applicationtradepolicy.Result{Decision: moduletrade.PolicyDecision{Status: "blocked"}}}
	recorder := &recordStub{}
	service, _ := NewService(policy, recorder)
	_, err := service.Record(context.Background(), Request{RequestID: "shadow-1", Requester: "test", Observation: validObservation()})
	if !errors.Is(err, ErrPolicyBlocked) || recorder.got.RequestID != "" {
		t.Fatalf("err=%v record=%+v", err, recorder.got)
	}
}

func validObservation() moduletrade.ShadowObservationInput {
	return moduletrade.ShadowObservationInput{
		IdempotencyKey: "shadow-key-1", StudyID: "study-1", DecisionID: "decision-1", ActorID: "mio", InstrumentID: "JP-TEST",
		DecisionKind: "select", MarketObservedAt: "2026-08-06T12:00:00Z", ContextSnapshotSHA256: strings.Repeat("a", 64),
		OutcomeLabelContractSHA256: strings.Repeat("b", 64), ReasonCodes: []string{"ELIGIBLE"}, EvidenceRefs: []string{"source/official-1"},
	}
}
