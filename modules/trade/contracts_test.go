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
		ValidationState: "owner_validated", CompletedAt: "2026-08-14T00:00:00.123456789Z", CorrelationID: requestID,
	}
}

func validDisabledStatus() PrivateStatus {
	return PrivateStatus{
		ContractVersion: PrivateContractVersion,
		ServiceStatus:   "ready",
		CorrelationID:   "trade-1",
		ExecutionMode:   "DISABLED",
		KillSwitch:      "ON",
		Dependencies:    DependencyStatuses{Broker: "disabled", Ledger: "unconfigured", MarketData: "unavailable", MemoryOwner: "unavailable"},
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
	if status.Dependencies.MemoryOwnerReady() {
		t.Fatal("unavailable memory owner must not be reported ready")
	}
	status.Dependencies.MemoryOwner = "ready"
	if err := status.ValidateDisabledFoundation(); err != nil || !status.Dependencies.MemoryOwnerReady() {
		t.Fatalf("ready memory owner must be accepted: %v", err)
	}
	status.Dependencies.MemoryOwner = "broken"
	if err := status.ValidateDisabledFoundation(); err == nil {
		t.Fatal("invalid memory owner dependency must be rejected")
	}
	status.Dependencies.MemoryOwner = "ready"
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

func validMemorySourceRecordForTest() SourceRecord {
	return SourceRecord{
		RecordVersion: 1, SourceRecordID: "source-record-1", CaptureNonce: "0123456789abcdef", SourceDefinitionID: "source-definition-1",
		SourceDefinitionHash: "sha256:" + strings.Repeat("a", 64), Title: "Official source", Publisher: "Publisher", Jurisdiction: "JP",
		Category: "market", Language: "ja", SourceURL: "https://example.com/source", FinalURL: "https://example.com/final",
		TermsReference: "terms-1", LicenseStatus: "review_required", Status: "quarantined", ObservedAt: "2026-08-14T00:00:00Z",
		PointInTimeAvailableAt: "2026-08-14T00:00:00Z", HTTPStatus: 200, MediaType: "text/html", ByteSize: 12,
		ContentHash: "sha256:" + strings.Repeat("b", 64), Tags: []string{"official"},
	}
}

func validMemoryCandidateForTest() LearningCandidateRecord {
	return LearningCandidateRecord{
		RecordVersion: 1, CandidateRecordID: "candidate-record-1", CandidateDefinitionID: "candidate-definition-1", Status: "candidate",
		Title: "Candidate", Statement: "A bounded statement.", BoundSources: []BoundSource{{
			SourceDefinitionID: "source-definition-1", SourceRecordID: "source-record-1", ContentHash: "sha256:" + strings.Repeat("b", 64),
			ObservedAt: "2026-08-14T00:00:00Z", Locator: "section-1",
		}}, Applicability: []string{"research"}, Limitations: []string{"not advice"}, InvalidationConditions: []string{"source changed"},
		Tags: []string{"candidate"}, ContentHash: "sha256:" + strings.Repeat("c", 64),
	}
}

func validMemoryMarketSnapshotForTest() MarketSnapshot {
	return MarketSnapshot{
		SnapshotID: "snapshot-1", SchemaVersion: 1, InstrumentID: "instrument-1", Symbol: "TEST", Name: "Test instrument",
		AssetType: "equity", Venue: "TSE", Currency: "JPY", TradeDate: "2026-08-14", AvailableAt: "2026-08-14T00:00:00Z",
		Open: 100, High: 110, Low: 90, Close: 105, AdjClose: 105, Volume: 1000, SourceName: "official",
		RunID: "run-1", PlanID: "plan-1", PlanHash: "sha256:" + strings.Repeat("a", 64), DatasetID: "dataset-1",
		DatasetHash: "sha256:" + strings.Repeat("b", 64), DatasetSourceRef: "dataset-source-1", CodeRevision: "revision-1",
		ContentHash: "sha256:" + strings.Repeat("c", 64),
	}
}

func validMemoryReplayDecisionForTest() ReplayDecision {
	return ReplayDecision{
		DecisionID: "decision-1", SchemaVersion: 1, SnapshotID: "snapshot-1", RunID: "run-1", InstrumentID: "instrument-1",
		TradeDate: "2026-08-14", Action: MemoryActionSelect, ContentHash: "sha256:" + strings.Repeat("d", 64),
	}
}

func validPortfolioSnapshotForTest() PortfolioSnapshot {
	nav := int64(1_000_000)
	unrealized := int64(0)
	return PortfolioSnapshot{
		SchemaVersion: 1, PortfolioID: "main-sim", Mode: "SIMULATION", Guaranteed: false, InitialCashJPY: 1_000_000, CashJPY: 1_000_000,
		ValuationStatus: "complete", UnrealizedPnLJPY: &unrealized, NAVJPY: &nav, Positions: []PortfolioPosition{}, EventCount: 1,
		LatestEventHash: "sha256:" + strings.Repeat("e", 64),
	}
}

func validMemoryEvidenceForTest(purpose, domain, operation, requestID string) OwnerEvidence {
	evidence := validOwnerEvidenceForTest("shiro", "worker", purpose, domain, operation, requestID, requestID)
	evidence.RequestTime = "2026-08-14T00:00:00Z"
	switch purpose {
	case "source_memory_read":
		evidence.FreshnessState = "source_observed_at"
		evidence.ValidationState = "source_record_integrity_verified"
	case "learning_memory_read":
		evidence.FreshnessState = "learning_candidate_observed_at"
		evidence.ValidationState = "learning_candidate_integrity_verified"
	case "market_memory_read":
		evidence.FreshnessState = "market_snapshot_available_at_read"
		evidence.ValidationState = "market_snapshot_integrity_verified"
	case "replay_memory_read":
		evidence.FreshnessState = "replay_snapshot_bound_at_read"
		evidence.ValidationState = "replay_decision_integrity_verified"
	}
	return evidence
}

func TestMemoryOwnerContractsValidateTypedRecordsAndIdentityBindings(t *testing.T) {
	requestID := "memory-request-1"
	source := validMemorySourceRecordForTest()
	candidate := validMemoryCandidateForTest()
	snapshot := validMemoryMarketSnapshotForTest()
	decision := validMemoryReplayDecisionForTest()

	readCases := []struct {
		name     string
		validate func() error
	}{
		{"source", func() error {
			return (SourceRecordReadResponse{ContractVersion: PrivateContractVersion, ServiceStatus: "ready", CorrelationID: requestID, ExecutionMode: "DISABLED", LearningMode: "OFFLINE_AVAILABLE", MemorySource: MemoryOwnerSource, Record: source, OwnerEvidence: validMemoryEvidenceForTest("source_memory_read", "source", "source_record", requestID)}).Validate(requestID, requestID)
		}},
		{"learning", func() error {
			return (LearningCandidateReadResponse{ContractVersion: PrivateContractVersion, ServiceStatus: "ready", CorrelationID: requestID, ExecutionMode: "DISABLED", LearningMode: "OFFLINE_AVAILABLE", MemorySource: MemoryOwnerSource, Record: candidate, OwnerEvidence: validMemoryEvidenceForTest("learning_memory_read", "learning", "learning_candidate", requestID)}).Validate(requestID, requestID)
		}},
		{"market", func() error {
			return (MarketSnapshotReadResponse{ContractVersion: PrivateContractVersion, ServiceStatus: "ready", CorrelationID: requestID, ExecutionMode: "DISABLED", LearningMode: "OFFLINE_AVAILABLE", MemorySource: MemoryOwnerSource, Record: snapshot, OwnerEvidence: validMemoryEvidenceForTest("market_memory_read", "market", "market_snapshot", requestID)}).Validate(requestID, requestID)
		}},
		{"replay", func() error {
			return (ReplayDecisionReadResponse{ContractVersion: PrivateContractVersion, ServiceStatus: "ready", CorrelationID: requestID, ExecutionMode: "DISABLED", LearningMode: "OFFLINE_AVAILABLE", MemorySource: MemoryOwnerSource, Record: decision, OwnerEvidence: validMemoryEvidenceForTest("replay_memory_read", "replay", "replay_decision", requestID)}).Validate(requestID, requestID)
		}},
	}
	for _, test := range readCases {
		t.Run(test.name, func(t *testing.T) {
			if err := test.validate(); err != nil {
				t.Fatal(err)
			}
		})
	}

	writeCases := []struct {
		name     string
		validate func() error
	}{
		{"source", func() error {
			receipt := validOwnerReceiptForTest("shiro", "worker", "source_memory_write", "source", "collect_source", requestID, false)
			receipt.AuditRef = source.SourceRecordID
			return (SourceRecordWriteResponse{ContractVersion: PrivateContractVersion, ServiceStatus: "ready", CorrelationID: requestID, ExecutionMode: "DISABLED", LearningMode: "OFFLINE_AVAILABLE", MemorySource: MemoryOwnerSource, Record: source, OwnerReceipt: receipt}).Validate(requestID)
		}},
		{"learning", func() error {
			receipt := validOwnerReceiptForTest("shiro", "worker", "learning_memory_write", "learning", "import_learning_candidate", requestID, false)
			receipt.AuditRef = candidate.CandidateRecordID
			return (LearningCandidateWriteResponse{ContractVersion: PrivateContractVersion, ServiceStatus: "ready", CorrelationID: requestID, ExecutionMode: "DISABLED", LearningMode: "OFFLINE_AVAILABLE", MemorySource: MemoryOwnerSource, Record: candidate, OwnerReceipt: receipt}).Validate(requestID)
		}},
		{"market", func() error {
			receipt := validOwnerReceiptForTest("shiro", "worker", "market_memory_write", "market", "import_market_snapshot", requestID, false)
			receipt.AuditRef = snapshot.SnapshotID
			return (MarketSnapshotWriteResponse{ContractVersion: PrivateContractVersion, ServiceStatus: "ready", CorrelationID: requestID, ExecutionMode: "DISABLED", LearningMode: "OFFLINE_AVAILABLE", MemorySource: MemoryOwnerSource, Record: snapshot, OwnerReceipt: receipt}).Validate(requestID)
		}},
		{"replay", func() error {
			receipt := validOwnerReceiptForTest("shiro", "worker", "replay_memory_write", "replay", "record_replay_decision", requestID, false)
			receipt.AuditRef = decision.DecisionID
			return (ReplayDecisionWriteResponse{ContractVersion: PrivateContractVersion, ServiceStatus: "ready", CorrelationID: requestID, ExecutionMode: "DISABLED", LearningMode: "OFFLINE_AVAILABLE", MemorySource: MemoryOwnerSource, Record: decision, OwnerReceipt: receipt}).Validate(requestID)
		}},
	}
	for _, test := range writeCases {
		t.Run(test.name, func(t *testing.T) {
			if err := test.validate(); err != nil {
				t.Fatal(err)
			}
		})
	}

	badSource := source
	badSource.Status = "active"
	if err := badSource.Validate(); err == nil {
		t.Fatal("source status must be quarantined")
	}
	badCandidate := candidate
	badCandidate.BoundSources[0].ContentHash = "not-a-hash"
	if err := badCandidate.Validate(); err == nil {
		t.Fatal("candidate source hash must be validated")
	}
	badSnapshot := snapshot
	badSnapshot.TradeDate = "2026-8-14"
	if err := badSnapshot.Validate(); err == nil {
		t.Fatal("market trade date must be exact")
	}
	badDecision := decision
	badDecision.Action = "buy"
	if err := badDecision.Validate(); err == nil {
		t.Fatal("replay action must be bounded")
	}

	receipt := validOwnerReceiptForTest("shiro", "worker", "source_memory_write", "source", "collect_source", requestID, false)
	receipt.AuditRef = source.SourceRecordID
	receipt.CorrelationID = "different-correlation"
	badResponse := SourceRecordWriteResponse{ContractVersion: PrivateContractVersion, ServiceStatus: "ready", CorrelationID: requestID, ExecutionMode: "DISABLED", LearningMode: "OFFLINE_AVAILABLE", MemorySource: MemoryOwnerSource, Record: source, OwnerReceipt: receipt}
	if err := badResponse.Validate(requestID); err == nil {
		t.Fatal("receipt correlation must bind to response correlation")
	}
}

func TestMemoryOwnerReadEvidenceUsesRouteSpecificStates(t *testing.T) {
	cases := []struct {
		name       string
		route      ownerRouteContract
		purpose    string
		domain     string
		operation  string
		freshness  string
		validation string
	}{
		{name: "source", route: ownerSourceRecordRoute, purpose: "source_memory_read", domain: "source", operation: "source_record", freshness: "source_observed_at", validation: "source_record_integrity_verified"},
		{name: "learning", route: ownerLearningCandidateRoute, purpose: "learning_memory_read", domain: "learning", operation: "learning_candidate", freshness: "learning_candidate_observed_at", validation: "learning_candidate_integrity_verified"},
		{name: "market", route: ownerMarketSnapshotRoute, purpose: "market_memory_read", domain: "market", operation: "market_snapshot", freshness: "market_snapshot_available_at_read", validation: "market_snapshot_integrity_verified"},
		{name: "replay", route: ownerReplayDecisionRoute, purpose: "replay_memory_read", domain: "replay", operation: "replay_decision", freshness: "replay_snapshot_bound_at_read", validation: "replay_decision_integrity_verified"},
		{name: "portfolio generic", route: ownerPortfolioSnapshotRoute, purpose: "portfolio_memory_read", domain: "portfolio", operation: "portfolio_snapshot", freshness: "observed_at_read", validation: "owner_route_succeeded"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			evidence := validOwnerEvidenceForTest("shiro", "worker", test.purpose, test.domain, test.operation, "request-1", "request-1")
			evidence.FreshnessState = test.freshness
			evidence.ValidationState = test.validation
			if err := validateOwnerEvidence(evidence, test.route, "request-1", "request-1"); err != nil {
				t.Fatalf("canonical evidence rejected: %v", err)
			}

			wrongFreshness := evidence
			wrongFreshness.FreshnessState = "observed_at_read"
			if test.freshness == "observed_at_read" {
				wrongFreshness.FreshnessState = "source_observed_at"
			}
			if err := validateOwnerEvidence(wrongFreshness, test.route, "request-1", "request-1"); err == nil {
				t.Fatal("generic freshness state unexpectedly accepted")
			}
			wrongValidation := evidence
			wrongValidation.ValidationState = "owner_route_succeeded"
			if test.validation == "owner_route_succeeded" {
				wrongValidation.ValidationState = "source_record_integrity_verified"
			}
			if err := validateOwnerEvidence(wrongValidation, test.route, "request-1", "request-1"); err == nil {
				t.Fatal("generic validation state unexpectedly accepted")
			}
		})
	}
}

func TestPortfolioMemoryOwnerContractsValidateSimulationSnapshotAndBinding(t *testing.T) {
	requestID := "portfolio-request-1"
	snapshot := validPortfolioSnapshotForTest()
	read := PortfolioSnapshotReadResponse{
		ContractVersion: PrivateContractVersion, ServiceStatus: "ready", CorrelationID: requestID, ExecutionMode: "DISABLED", LearningMode: "OFFLINE_AVAILABLE",
		MemorySource: MemoryOwnerSource, Snapshot: snapshot, OwnerEvidence: validMemoryEvidenceForTest("portfolio_memory_read", "portfolio", "portfolio_snapshot", requestID),
	}
	if err := read.Validate(requestID, requestID); err != nil {
		t.Fatal(err)
	}
	receipt := validOwnerReceiptForTest("shiro", "worker", "portfolio_memory_write", "portfolio", "ensure_initialized", requestID, false)
	receipt.AuditRef = snapshot.LatestEventHash
	write := PortfolioSnapshotWriteResponse{
		ContractVersion: PrivateContractVersion, ServiceStatus: "ready", CorrelationID: requestID, ExecutionMode: "DISABLED", LearningMode: "OFFLINE_AVAILABLE",
		MemorySource: MemoryOwnerSource, Snapshot: snapshot, OwnerReceipt: receipt,
	}
	if err := write.Validate(requestID); err != nil {
		t.Fatal(err)
	}

	for name, mutate := range map[string]func(*PortfolioSnapshot){
		"mode":         func(value *PortfolioSnapshot) { value.Mode = "LIVE" },
		"initial cash": func(value *PortfolioSnapshot) { value.InitialCashJPY = 1 },
		"guaranteed":   func(value *PortfolioSnapshot) { value.Guaranteed = true },
		"event hash":   func(value *PortfolioSnapshot) { value.LatestEventHash = "not-a-hash" },
		"event count":  func(value *PortfolioSnapshot) { value.EventCount = 0 },
		"portfolio id": func(value *PortfolioSnapshot) { value.PortfolioID = "../portfolio" },
		"position value": func(value *PortfolioSnapshot) {
			value.Positions = []PortfolioPosition{{InstrumentID: "instrument-1", Quantity: 1, CostBasisJPY: -1}}
		},
	} {
		value := snapshot
		mutate(&value)
		if err := value.ValidateMemoryOwner(); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}

	write.OwnerReceipt.AuditRef = "sha256:" + strings.Repeat("f", 64)
	if err := write.Validate(requestID); err == nil {
		t.Fatal("portfolio receipt audit must bind to snapshot latest event hash")
	}
	write.OwnerReceipt.AuditRef = snapshot.LatestEventHash
	write.OwnerReceipt.CorrelationID = "other-correlation"
	if err := write.Validate(requestID); err == nil {
		t.Fatal("portfolio receipt correlation must bind to response correlation")
	}
}

func TestMemoryOwnerRequestsValidateStrictInputs(t *testing.T) {
	validCollect := CollectSourceRequest{ContractVersion: MemoryOwnerContractVersion, RequestID: "request-1", SourceDefinitionID: "source-definition-1"}
	if err := validCollect.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*CollectSourceRequest){
		"contract":   func(value *CollectSourceRequest) { value.ContractVersion = "other/v1" },
		"request id": func(value *CollectSourceRequest) { value.RequestID = "../request" },
		"source id":  func(value *CollectSourceRequest) { value.SourceDefinitionID = "source/definition" },
	} {
		value := validCollect
		mutate(&value)
		if err := value.Validate(); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
	if err := (ImportLearningCandidateRequest{ContractVersion: MemoryOwnerContractVersion, RequestID: "request-1", CandidateDefinitionID: "candidate-1"}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (ImportMarketSnapshotRequest{ContractVersion: MemoryOwnerContractVersion, RequestID: "request-1", RunID: "run-1", InstrumentID: "instrument-1", TradeDate: "2026-08-14"}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (RecordReplayDecisionRequest{ContractVersion: MemoryOwnerContractVersion, RequestID: "request-1", RunID: "run-1", InstrumentID: "instrument-1", TradeDate: "2026-08-14", Action: MemoryActionAvoid}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (EnsurePortfolioInitializedRequest{ContractVersion: MemoryOwnerContractVersion, RequestID: "request-1"}).Validate(); err != nil {
		t.Fatal(err)
	}
	invalidPortfolioRequest := EnsurePortfolioInitializedRequest{ContractVersion: MemoryOwnerContractVersion, RequestID: "request/1"}
	if err := invalidPortfolioRequest.Validate(); err == nil {
		t.Fatal("portfolio initialization request identity must be safe")
	}
	invalidReplay := RecordReplayDecisionRequest{ContractVersion: MemoryOwnerContractVersion, RequestID: "request-1", RunID: "run-1", InstrumentID: "instrument-1", TradeDate: "2026-08-14", Action: "buy"}
	if err := invalidReplay.Validate(); err == nil {
		t.Fatal("replay request action must be bounded")
	}
}
