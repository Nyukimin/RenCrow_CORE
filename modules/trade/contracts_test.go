package trade

import (
	"strings"
	"testing"
)

func validOwnerEvidenceForTest(agentID, role, purpose, domain, operation, requestID, correlationID string) OwnerEvidence {
	return OwnerEvidence{
		AgentID: agentID, Role: role, Purpose: purpose, DataScope: "internal", RequestID: requestID,
		OwnerModule: "RenCrow_TRADE", Domain: domain, Operation: operation, CorrelationID: correlationID,
		ProvenanceRef: "portfolio/genesis", RetrievedAt: "2026-08-14T00:00:00.123456789Z",
		FreshnessState: "observed_at_read", ValidationState: "owner_route_succeeded", BudgetLimit: 1, ReturnedCount: 1,
	}
}

func validOwnerReceiptForTest(agentID, role, purpose, domain, operation, requestID string, replay bool) OwnerReceipt {
	return OwnerReceipt{
		ReceiptID: "receipt-1", RequestID: requestID, AgentID: agentID, Role: role, Purpose: purpose, DataScope: "internal",
		OwnerModule: "RenCrow_TRADE", Domain: domain, Operation: operation, Status: "completed", IdempotentReplay: replay,
		SchemaVersion: 1, AuditRef: "audit-1", PolicyRevision: "policy-1", MigrationState: "embedded_current",
		ValidationState: "owner_validated", CompletedAt: "2026-08-14T00:00:00.123456789Z",
	}
}

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
		ContractVersion: PrivateContractVersion, ServiceStatus: "ready", CorrelationID: request.RequestID, ExecutionMode: "DISABLED", LearningMode: "OFFLINE_AVAILABLE",
		RequestID: request.RequestID, Environment: "SHADOW", PolicyDecision: PolicyDecision{Capability: "shadow_observation_record", Status: "allowed", ModulePolicyRevision: "policy-1"},
		Event:        ShadowObservationEvent{EventVersion: 1, EventID: "shadow-event/sha256:" + strings.Repeat("c", 64), Sequence: 1, RecordedAt: "2026-08-06T12:01:00Z", Type: "shadow_observation_recorded", ShadowObservationInput: request.Observation, EventHash: "sha256:" + strings.Repeat("c", 64)},
		OwnerReceipt: validOwnerReceiptForTest("shiro", "worker", "ledger_memory_write", "ledger", "shadow_observation", request.RequestID, false),
	}
	response.OwnerReceipt.AuditRef = response.Event.EventID
	response.OwnerReceipt.PolicyRevision = response.PolicyDecision.ModulePolicyRevision
	if err := response.Validate(request); err != nil {
		t.Fatal(err)
	}
	response.KnowledgePromoted = true
	if err := response.Validate(request); err == nil {
		t.Fatal("knowledge promotion must invalidate Shadow response")
	}
}

func TestPrivateShadowOutcomeValidatesFixedLabelContractAndSafety(t *testing.T) {
	request := ShadowOutcomeRequest{
		ContractVersion: ShadowOutcomeContractVersion, RequestID: "outcome-1",
		Outcome: ShadowOutcomeInput{
			IdempotencyKey: "outcome-key-1", StudyID: "study-1", DecisionID: "decision-1", MarketObservedAt: "2026-08-06T12:00:00Z",
			OutcomeLabel: "success", OutcomeObservedAt: "2026-08-07T12:00:00Z", OutcomeSnapshotSHA256: strings.Repeat("c", 64),
			OutcomeReasonCodes: []string{"THESIS_CONFIRMED"}, OutcomeEvidenceRefs: []string{"source/outcome-1"}, OutcomeLabelContractSHA256: strings.Repeat("b", 64),
		},
		Policy: PolicyEvaluationRequest{
			ContractVersion: PolicyEvaluationContractVersion, RequestID: "outcome-1", Capability: "shadow_outcome_record",
			GlobalPolicy: GlobalPolicyInput{ContractRevision: "global-policy/v1", BundleRevision: "2026-08-06.1", ContentSHA256: strings.Repeat("d", 64), Allowed: true},
			Deployment:   PolicyLayerInput{Revision: "deployment-1", Allowed: true}, RequestScope: PolicyLayerInput{Revision: "shadow-outcome/sha256:" + strings.Repeat("c", 64), Allowed: true},
		},
	}
	response := PrivateShadowOutcome{
		ContractVersion: PrivateContractVersion, ServiceStatus: "ready", CorrelationID: "outcome-1", ExecutionMode: "DISABLED", LearningMode: "OFFLINE_AVAILABLE",
		RequestID: "outcome-1", Environment: "SHADOW", PolicyDecision: PolicyDecision{Capability: "shadow_outcome_record", Status: "allowed", ModulePolicyRevision: "policy-1"},
		Event:        ShadowOutcomeEvent{EventVersion: 1, EventID: "shadow-event/sha256:" + strings.Repeat("e", 64), Sequence: 2, RecordedAt: "2026-08-07T12:01:00Z", Type: "shadow_outcome_recorded", IdempotencyKey: "outcome-key-1", StudyID: "study-1", DecisionID: "decision-1", MarketObservedAt: "2026-08-06T12:00:00Z", OutcomeLabel: "success", OutcomeObservedAt: "2026-08-07T12:00:00Z", OutcomeSnapshotSHA256: strings.Repeat("c", 64), OutcomeReasonCodes: []string{"THESIS_CONFIRMED"}, OutcomeEvidenceRefs: []string{"source/outcome-1"}, OutcomeLabelContractSHA256: strings.Repeat("b", 64), EventHash: "sha256:" + strings.Repeat("e", 64)},
		OwnerReceipt: validOwnerReceiptForTest("shiro", "worker", "ledger_memory_write", "ledger", "shadow_outcome", "outcome-1", false),
	}
	response.OwnerReceipt.AuditRef = response.Event.EventID
	response.OwnerReceipt.PolicyRevision = response.PolicyDecision.ModulePolicyRevision
	if err := response.Validate(request); err != nil {
		t.Fatal(err)
	}
	response.PortfolioMutated = true
	if err := response.Validate(request); err == nil {
		t.Fatal("portfolio mutation must invalidate Shadow outcome response")
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
		PolicyDecision: PolicyDecision{Capability: "portfolio_simulation_commit", Status: "allowed", ModulePolicyRevision: "policy-1"},
		RiskDecision:   &RiskPreviewDecision{Status: "pass"},
		Snapshot:       PortfolioSnapshot{PortfolioID: "main-sim", Mode: "SIMULATION", EventCount: 2, LatestEventHash: "audit-1"},
		OwnerReceipt:   validOwnerReceiptForTest("shiro", "worker", "portfolio_memory_write", "portfolio", "simulation_commit", "sim-1", false),
	}
	if err := response.Validate(request); err != nil {
		t.Fatal(err)
	}
	response.AuthorizesExternalExecution = true
	if err := response.Validate(request); err == nil {
		t.Fatal("expected external execution authority rejection")
	}
}

func TestOwnerEvidenceAndReceiptValidationIsFailClosed(t *testing.T) {
	evidence := validOwnerEvidenceForTest("shiro", "worker", "portfolio_memory_read", "portfolio", "risk_preview", "request-1", "request-1")
	if err := validateOwnerEvidence(evidence, ownerRiskPreviewRoute, "request-1", "request-1"); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*OwnerEvidence){
		"owner module":   func(value *OwnerEvidence) { value.OwnerModule = "CORE" },
		"domain":         func(value *OwnerEvidence) { value.Domain = "ledger" },
		"operation":      func(value *OwnerEvidence) { value.Operation = "simulation_commit" },
		"request id":     func(value *OwnerEvidence) { value.RequestID = "other" },
		"correlation id": func(value *OwnerEvidence) { value.CorrelationID = "other" },
		"provenance":     func(value *OwnerEvidence) { value.ProvenanceRef = "" },
		"freshness":      func(value *OwnerEvidence) { value.FreshnessState = "stale" },
		"validation":     func(value *OwnerEvidence) { value.ValidationState = "unverified" },
		"budget":         func(value *OwnerEvidence) { value.BudgetLimit = 2 },
		"returned":       func(value *OwnerEvidence) { value.ReturnedCount = 2 },
	} {
		value := evidence
		mutate(&value)
		if err := validateOwnerEvidence(value, ownerRiskPreviewRoute, "request-1", "request-1"); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}

	receipt := validOwnerReceiptForTest("shiro", "worker", "portfolio_memory_write", "portfolio", "simulation_commit", "request-1", false)
	if err := validateOwnerReceipt(receipt, ownerSimulationCommitRoute, "request-1", false); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*OwnerReceipt){
		"owner module":    func(value *OwnerReceipt) { value.OwnerModule = "CORE" },
		"domain":          func(value *OwnerReceipt) { value.Domain = "ledger" },
		"operation":       func(value *OwnerReceipt) { value.Operation = "shadow_outcome" },
		"request id":      func(value *OwnerReceipt) { value.RequestID = "other" },
		"status":          func(value *OwnerReceipt) { value.Status = "blocked" },
		"schema version":  func(value *OwnerReceipt) { value.SchemaVersion = 0 },
		"audit ref":       func(value *OwnerReceipt) { value.AuditRef = "" },
		"policy revision": func(value *OwnerReceipt) { value.PolicyRevision = "" },
		"migration":       func(value *OwnerReceipt) { value.MigrationState = "legacy" },
		"validation":      func(value *OwnerReceipt) { value.ValidationState = "unverified" },
		"completed at":    func(value *OwnerReceipt) { value.CompletedAt = "not-a-time" },
		"replay":          func(value *OwnerReceipt) { value.IdempotentReplay = true },
	} {
		value := receipt
		mutate(&value)
		if err := validateOwnerReceipt(value, ownerSimulationCommitRoute, "request-1", false); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
}
