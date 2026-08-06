package trade

import (
	"strings"
	"testing"
)

func validDisabledStatus() PrivateStatus {
	return PrivateStatus{
		ContractVersion: PrivateContractVersion,
		ServiceStatus:   "ready",
		CorrelationID:   "trade-1",
		ExecutionMode:   "DISABLED",
		KillSwitch:      "ON",
		Dependencies:    DependencyStatuses{Broker: "disabled", Ledger: "unconfigured", MarketData: "unavailable"},
		Policy: PolicyStatus{
			ExecutionMode:          "DISABLED",
			KillSwitch:             "ON",
			BrokerAdapter:          "none",
			ModulePolicyRevision:   "sha256:module",
			BinaryContractRevision: "trade-binary/v1",
			Capabilities:           map[string]bool{"live_order": false},
		},
		Portfolio: PortfolioProjection{Status: "unconfigured"},
	}
}

func TestPrivateShadowObservationValidatesNoExecutionMutationOrPromotion(t *testing.T) {
	request := ShadowObservationRequest{
		ContractVersion: ShadowObservationContractVersion,
		RequestID:       "shadow-request-1",
		Observation: ShadowObservationInput{
			IdempotencyKey: "shadow-key-1", StudyID: "study-1", DecisionID: "decision-1", ActorID: "mio", InstrumentID: "JP-TEST",
			DecisionKind: "select", MarketObservedAt: "2026-08-06T12:00:00Z", ContextSnapshotSHA256: strings.Repeat("a", 64),
			OutcomeLabelContractSHA256: strings.Repeat("b", 64), ReasonCodes: []string{"ELIGIBLE"}, EvidenceRefs: []string{"source/official-1"},
		},
		Policy: PolicyEvaluationRequest{
			ContractVersion: PolicyEvaluationContractVersion, Capability: "shadow_observation_record",
			GlobalPolicy: GlobalPolicyInput{ContractRevision: "global-policy/v1", BundleRevision: "2026-08-06.1", ContentSHA256: strings.Repeat("d", 64), Allowed: true},
			Deployment:   PolicyLayerInput{Revision: "deployment-1", Allowed: true}, RequestScope: PolicyLayerInput{Allowed: true},
		},
	}
	request.Policy.RequestID = request.RequestID
	request.Policy.RequestScope.Revision = "shadow-observation/sha256:" + request.Observation.ContextSnapshotSHA256
	response := PrivateShadowObservation{
		ContractVersion: PrivateContractVersion, ServiceStatus: "ready", CorrelationID: "trace-1", ExecutionMode: "DISABLED", LearningMode: "OFFLINE_AVAILABLE",
		RequestID: request.RequestID, Environment: "SHADOW", PolicyDecision: PolicyDecision{Capability: "shadow_observation_record", Status: "allowed"},
		Event: ShadowObservationEvent{EventVersion: 1, EventID: "shadow-event/sha256:" + strings.Repeat("c", 64), Sequence: 1, RecordedAt: "2026-08-06T12:01:00Z", Type: "shadow_observation_recorded", ShadowObservationInput: request.Observation, EventHash: "sha256:" + strings.Repeat("c", 64)},
	}
	if err := response.Validate(request); err != nil {
		t.Fatal(err)
	}
	response.KnowledgePromoted = true
	if err := response.Validate(request); err == nil {
		t.Fatal("knowledge promotion must invalidate Shadow response")
	}
}

func TestPrivateStatusValidateDisabledFoundation(t *testing.T) {
	status := validDisabledStatus()
	if err := status.ValidateDisabledFoundation(); err != nil {
		t.Fatal(err)
	}
	status.Policy.Capabilities["live_order"] = true
	if err := status.ValidateDisabledFoundation(); err == nil {
		t.Fatal("expected unauthorized capability rejection")
	}
}

func TestPrivatePolicyEvaluationValidate(t *testing.T) {
	request := PolicyEvaluationRequest{
		ContractVersion: PolicyEvaluationContractVersion,
		RequestID:       "core-policy-1",
		Capability:      "live_order",
		GlobalPolicy: GlobalPolicyInput{
			ContractRevision: "global-policy/v1",
			BundleRevision:   "2026-08-06.1",
			ContentSHA256:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Allowed:          true,
		},
		Deployment:   PolicyLayerInput{Revision: "deployment-1", Allowed: true},
		RequestScope: PolicyLayerInput{Revision: "scope-1", Allowed: true},
	}
	response := PrivatePolicyEvaluation{
		ContractVersion: PrivateContractVersion,
		ServiceStatus:   "ready",
		CorrelationID:   "core-policy-1",
		ExecutionMode:   "DISABLED",
		LearningMode:    "OFFLINE_AVAILABLE",
		Decision: PolicyDecision{
			Capability:             "live_order",
			Status:                 "blocked",
			ReasonCode:             "BINARY_HARD_LIMIT_BLOCKED",
			Reason:                 "binary hard limit blocks capability",
			BinaryContractRevision: "trade-binary/v1",
			ModulePolicyRevision:   "sha256:module",
			PolicyID:               "trade-disabled",
			GlobalBundleRevision:   "2026-08-06.1",
			DeploymentRevision:     "deployment-1",
			RequestScopeRevision:   "scope-1",
		},
	}
	if err := response.Validate(request); err != nil {
		t.Fatal(err)
	}
	response.Decision.Status = "allowed"
	if err := response.Validate(request); err == nil {
		t.Fatal("expected binary-blocked capability rejection")
	}
	response.Decision.Status = "blocked"
	response.Decision.ReasonCode = "GLOBAL_POLICY_BLOCKED"
	if err := response.Validate(request); err == nil {
		t.Fatal("expected missing binary hard-limit reason rejection")
	}
}

func TestSimulationCommitContractRejectsExternalAuthorityAndAcceptsSimulationMutation(t *testing.T) {
	request := SimulationCommitRequest{
		ContractVersion: SimulationCommitContractVersion, RequestID: "sim-1", IdempotencyKey: "key-1",
		ExpectedPortfolioEventCount:      1,
		ExpectedPortfolioLatestEventHash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ExpectedInputSnapshotSHA256:      "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Plan:                             RiskPreviewPlan{ContractVersion: RiskPreviewPlanContractVersion, PlanID: "plan-1", PolicyRevision: "2026-08-06.1", AsOf: "2026-08-06T00:00:00Z", Selection: RiskPreviewSelection{InstrumentID: "JP-TEST"}, Proposal: RiskPreviewBuyProposal{InstrumentID: "JP-TEST"}, ExitContract: RiskPreviewExitContract{ContractID: "exit-1", InstrumentID: "JP-TEST"}},
		Policy:                           PolicyEvaluationRequest{ContractVersion: PolicyEvaluationContractVersion, RequestID: "sim-1", Capability: "portfolio_simulation_commit", GlobalPolicy: GlobalPolicyInput{ContractRevision: "global-policy/v1", BundleRevision: "2026-08-06.1", ContentSHA256: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", Allowed: true}, Deployment: PolicyLayerInput{Revision: "deployment-1", Allowed: true}, RequestScope: PolicyLayerInput{Revision: "simulation-commit/sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Allowed: true}},
	}
	response := PrivateSimulationCommit{
		ContractVersion: PrivateContractVersion, ServiceStatus: "ready", CorrelationID: "sim-1", ExecutionMode: "DISABLED", RequestID: "sim-1",
		PortfolioID: "main-sim", Mode: "SIMULATION", PortfolioMutated: true,
		PreviousPortfolioEventCount: 1, PreviousPortfolioLatestHash: request.ExpectedPortfolioLatestEventHash,
		PolicyDecision: PolicyDecision{Capability: "portfolio_simulation_commit", Status: "allowed"},
		RiskDecision:   &RiskPreviewDecision{Status: "pass"},
		Snapshot:       PortfolioSnapshot{PortfolioID: "main-sim", Mode: "SIMULATION", EventCount: 2},
	}
	if err := response.Validate(request); err != nil {
		t.Fatal(err)
	}
	response.AuthorizesExternalExecution = true
	if err := response.Validate(request); err == nil {
		t.Fatal("expected external execution authority rejection")
	}
}
