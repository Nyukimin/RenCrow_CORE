package tradepolicy

import (
	"context"
	"errors"
	"testing"
	"time"

	domainbundle "github.com/Nyukimin/RenCrow_CORE/internal/domain/policybundle"
	domaindecision "github.com/Nyukimin/RenCrow_CORE/internal/domain/policydecision"
	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

type snapshotStub struct {
	snapshot domainbundle.Snapshot
	ok       bool
}

func (stub snapshotStub) Snapshot() (domainbundle.Snapshot, bool) { return stub.snapshot, stub.ok }

type evaluatorStub struct {
	response moduletrade.PrivatePolicyEvaluation
	err      error
	request  moduletrade.PolicyEvaluationRequest
}

func (stub *evaluatorStub) Evaluate(_ context.Context, _ string, request moduletrade.PolicyEvaluationRequest) (moduletrade.PrivatePolicyEvaluation, error) {
	stub.request = request
	return stub.response, stub.err
}

type decisionStoreStub struct {
	records []domaindecision.Record
	err     error
}

func (stub *decisionStoreStub) Save(_ context.Context, record domaindecision.Record) error {
	if stub.err != nil {
		return stub.err
	}
	stub.records = append(stub.records, record)
	return nil
}

func TestServiceEvaluatesGlobalDeploymentAndRequestLayersAndSavesEvidence(t *testing.T) {
	snapshot := domainbundle.Snapshot{
		BundleID:       "rencrow-default",
		BundleRevision: "2026-08-06.1",
		ContentSHA256:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Capabilities:   map[string]bool{"learning_replay": true},
		ProductionDisabled: map[string]bool{
			"learning_replay": true,
		},
	}
	evaluator := &evaluatorStub{response: moduletrade.PrivatePolicyEvaluation{
		ContractVersion: moduletrade.PrivateContractVersion,
		ServiceStatus:   "ready",
		CorrelationID:   "request-1",
		ExecutionMode:   "DISABLED",
		LearningMode:    "OFFLINE_AVAILABLE",
		Decision: moduletrade.PolicyDecision{
			Capability:             "learning_replay",
			Status:                 "blocked",
			ReasonCode:             "DEPLOYMENT_BLOCKED",
			Reason:                 "deployment blocks capability",
			BinaryContractRevision: "trade-binary/v1",
			ModulePolicyRevision:   "sha256:module",
			PolicyID:               "trade-disabled",
			GlobalBundleRevision:   "2026-08-06.1",
			DeploymentRevision:     "2026-08-06.1#deployment",
			RequestScopeRevision:   "diagnostic/v1",
		},
	}}
	store := &decisionStoreStub{}
	service, err := NewService(Options{
		Snapshots: snapshotStub{snapshot: snapshot, ok: true},
		Evaluator: evaluator,
		Decisions: store,
		Now:       func() time.Time { return time.Date(2026, 8, 6, 1, 2, 3, 0, time.UTC) },
		NewID:     func() (string, error) { return "trade-policy-decision-1", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Evaluate(context.Background(), Request{
		RequestID:            "request-1",
		TraceID:              "trace-1",
		Requester:            "viewer-trade-policy-diagnostic",
		Capability:           "learning_replay",
		RequestScopeRevision: "diagnostic/v1",
		RequestAllowed:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.AuthorizesExecution || evaluator.request.Deployment.Allowed || !evaluator.request.GlobalPolicy.Allowed {
		t.Fatalf("result=%+v request=%+v", result, evaluator.request)
	}
	if len(store.records) != 1 || store.records[0].Outcome != domaindecision.OutcomeBlocked || store.records[0].ExecutionResult != "not_executed" || store.records[0].InputSnapshotSHA256 == "" {
		t.Fatalf("records=%+v", store.records)
	}
}

func TestServiceFailsClosedAndRecordsUnavailableGlobalPolicy(t *testing.T) {
	evaluator := &evaluatorStub{}
	store := &decisionStoreStub{}
	service, err := NewService(Options{
		Snapshots: snapshotStub{},
		Evaluator: evaluator,
		Decisions: store,
		NewID:     func() (string, error) { return "trade-policy-decision-2", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Evaluate(context.Background(), Request{
		RequestID: "request-2", Requester: "viewer-trade-policy-diagnostic", Capability: "source_collect", RequestScopeRevision: "diagnostic/v1",
	})
	if !errors.Is(err, ErrGlobalPolicyUnavailable) || result.AuthorizesExecution {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if evaluator.request.RequestID != "" {
		t.Fatal("TRADE must not be called without an active Global Policy")
	}
	if len(store.records) != 1 || store.records[0].Outcome != domaindecision.OutcomeUnavailable {
		t.Fatalf("records=%+v", store.records)
	}
}
