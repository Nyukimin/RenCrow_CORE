package tradesimulationcommit

import (
	"context"
	"errors"
	"testing"

	applicationtradepolicy "github.com/Nyukimin/RenCrow_CORE/internal/application/tradepolicy"
	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

type policyStub struct {
	result applicationtradepolicy.Result
	err    error
	got    applicationtradepolicy.Request
}

func (stub *policyStub) Evaluate(_ context.Context, request applicationtradepolicy.Request) (applicationtradepolicy.Result, error) {
	stub.got = request
	return stub.result, stub.err
}

type commitStub struct {
	got moduletrade.SimulationCommitRequest
}

func (stub *commitStub) CommitSimulation(_ context.Context, _ string, request moduletrade.SimulationCommitRequest) (moduletrade.PrivateSimulationCommit, error) {
	stub.got = request
	return moduletrade.PrivateSimulationCommit{PortfolioID: "main-sim", Mode: "SIMULATION"}, nil
}

func TestCommitBindsActivePolicyInputToPreviewEvidence(t *testing.T) {
	inputHash := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	policyInput := moduletrade.PolicyEvaluationRequest{
		ContractVersion: moduletrade.PolicyEvaluationContractVersion, RequestID: "sim-1", Capability: Capability,
		GlobalPolicy: moduletrade.GlobalPolicyInput{ContractRevision: "global-policy/v1", BundleRevision: "2026-08-06.1", ContentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Allowed: true},
		Deployment:   moduletrade.PolicyLayerInput{Revision: "deployment-1", Allowed: true}, RequestScope: moduletrade.PolicyLayerInput{Revision: "simulation-commit/sha256:" + inputHash, Allowed: true},
	}
	policy := &policyStub{result: applicationtradepolicy.Result{Decision: moduletrade.PolicyDecision{Status: "allowed", GlobalBundleRevision: "2026-08-06.1"}, EvaluationInput: policyInput}}
	committer := &commitStub{}
	service, err := NewService(policy, committer)
	if err != nil {
		t.Fatal(err)
	}
	request := Request{RequestID: "sim-1", Requester: "test", RequestAllowed: true, IdempotencyKey: "key-1", ExpectedPortfolioEventCount: 1, ExpectedPortfolioLatestEventHash: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", ExpectedInputSnapshotSHA256: inputHash, Plan: moduletrade.RiskPreviewPlan{ContractVersion: moduletrade.RiskPreviewPlanContractVersion, PlanID: "plan-1", PolicyRevision: "2026-08-06.1", AsOf: "2026-08-06T00:00:00Z", Selection: moduletrade.RiskPreviewSelection{InstrumentID: "JP-TEST"}, Proposal: moduletrade.RiskPreviewBuyProposal{InstrumentID: "JP-TEST"}, ExitContract: moduletrade.RiskPreviewExitContract{ContractID: "exit-1", InstrumentID: "JP-TEST"}}}
	result, err := service.Commit(context.Background(), request)
	if err != nil || result.Commit == nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if policy.got.Capability != Capability || policy.got.RequestScopeRevision != "simulation-commit/sha256:"+inputHash || committer.got.Policy.RequestScope.Revision != policy.got.RequestScopeRevision {
		t.Fatalf("policy=%+v commit=%+v", policy.got, committer.got)
	}
}

func TestCommitStopsBeforeModuleWhenPolicyBlocks(t *testing.T) {
	policy := &policyStub{result: applicationtradepolicy.Result{Decision: moduletrade.PolicyDecision{Status: "blocked"}}}
	committer := &commitStub{}
	service, _ := NewService(policy, committer)
	_, err := service.Commit(context.Background(), Request{RequestID: "sim-1", Requester: "test", IdempotencyKey: "key-1"})
	if !errors.Is(err, ErrPolicyBlocked) || committer.got.RequestID != "" {
		t.Fatalf("err=%v commit=%+v", err, committer.got)
	}
}
