package tradeshadowreview

import (
	"context"
	"errors"
	"strings"
	"testing"

	applicationtradepolicy "github.com/Nyukimin/RenCrow_CORE/internal/application/tradepolicy"
	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

type reviewPolicyStub struct {
	result applicationtradepolicy.Result
	got    applicationtradepolicy.Request
}

func (stub *reviewPolicyStub) Evaluate(_ context.Context, request applicationtradepolicy.Request) (applicationtradepolicy.Result, error) {
	stub.got = request
	return stub.result, nil
}

type reviewRecorderStub struct {
	got moduletrade.ShadowReviewRequest
}

func (stub *reviewRecorderStub) RecordShadowReview(_ context.Context, _ string, request moduletrade.ShadowReviewRequest) (moduletrade.PrivateShadowReview, error) {
	stub.got = request
	return moduletrade.PrivateShadowReview{Environment: "SHADOW"}, nil
}

func TestRecordBindsReportHashToPolicyScope(t *testing.T) {
	input := validReview()
	scope := "shadow-review/sha256:" + input.OutcomeReportSHA256
	policyInput := moduletrade.PolicyEvaluationRequest{ContractVersion: moduletrade.PolicyEvaluationContractVersion, RequestID: "review-1", Capability: Capability, GlobalPolicy: moduletrade.GlobalPolicyInput{ContractRevision: "global-policy/v1", BundleRevision: "2026-08-06.1", ContentSHA256: strings.Repeat("d", 64), Allowed: true}, Deployment: moduletrade.PolicyLayerInput{Revision: "deployment-1", Allowed: true}, RequestScope: moduletrade.PolicyLayerInput{Revision: scope, Allowed: true}}
	policy := &reviewPolicyStub{result: applicationtradepolicy.Result{Decision: moduletrade.PolicyDecision{Status: "allowed"}, EvaluationInput: policyInput}}
	recorder := &reviewRecorderStub{}
	service, err := NewService(policy, recorder)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Record(context.Background(), Request{RequestID: "review-1", Requester: "reviewer", RequestAllowed: true, Review: input})
	if err != nil || result.Record == nil || policy.got.RequestScopeRevision != scope || recorder.got.Policy.RequestScope.Revision != scope {
		t.Fatalf("result=%+v policy=%+v record=%+v err=%v", result, policy.got, recorder.got, err)
	}
}

func TestRecordStopsBeforeModuleWhenPolicyBlocks(t *testing.T) {
	policy := &reviewPolicyStub{result: applicationtradepolicy.Result{Decision: moduletrade.PolicyDecision{Status: "blocked"}}}
	recorder := &reviewRecorderStub{}
	service, _ := NewService(policy, recorder)
	_, err := service.Record(context.Background(), Request{RequestID: "review-1", Requester: "reviewer", Review: validReview()})
	if !errors.Is(err, ErrPolicyBlocked) || recorder.got.RequestID != "" {
		t.Fatalf("err=%v record=%+v", err, recorder.got)
	}
}

func validReview() moduletrade.ShadowReviewInput {
	return moduletrade.ShadowReviewInput{IdempotencyKey: "review-key-1", StudyID: "study-1", OutcomeReportSHA256: strings.Repeat("a", 64), OutcomeReportLatestEventHash: "sha256:" + strings.Repeat("b", 64), ReviewerID: "reviewer-1", ReviewerType: "independent", ReviewDecision: "accept", ReviewedAt: "2026-08-08T12:00:00Z", ReviewReasonCodes: []string{"REPORT_VALIDATED"}, ReviewEvidenceRefs: []string{"review/record-1"}}
}
