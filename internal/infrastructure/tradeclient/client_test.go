package tradeclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	toolcontext "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

const testToken = "0123456789abcdef0123456789abcdef"

func ownerContext(t *testing.T, requestID, agentID, role, purpose string) context.Context {
	t.Helper()
	scope := toolcontext.ToolExecutionScope{
		RequestID: requestID, ActorKind: toolcontext.ActorKindAgent, ActorID: agentID,
		AllowedDataScopes: []string{toolcontext.DataScopeInternal}, AuthenticationSource: toolcontext.AuthenticationSourceAgentOrchestrator,
		AgentRole: role, Purpose: purpose,
	}
	if err := scope.Validate(); err != nil {
		t.Fatal(err)
	}
	return toolcontext.WithToolExecutionScope(context.Background(), scope)
}

func assertOwnerHeaders(request *http.Request, agentID, role, purpose, requestID string) bool {
	requestTime, requestTimeErr := time.Parse(time.RFC3339Nano, request.Header.Get("X-RenCrow-Request-Time"))
	read := strings.HasSuffix(purpose, "_read")
	resultLimitOK := request.Header.Get("X-RenCrow-Result-Limit") == "1"
	if !read {
		resultLimitOK = request.Header.Get("X-RenCrow-Result-Limit") == ""
	}
	return requestTimeErr == nil && requestTime.Location() == time.UTC &&
		request.Header.Get("X-RenCrow-Agent-ID") == agentID &&
		request.Header.Get("X-RenCrow-Agent-Role") == role &&
		request.Header.Get("X-RenCrow-Request-Purpose") == purpose &&
		request.Header.Get("X-RenCrow-Data-Scope") == "internal" &&
		request.Header.Get("X-Request-ID") == requestID && resultLimitOK
}

func writeToken(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trade.token")
	if err := os.WriteFile(path, []byte(testToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func disabledStatus() moduletrade.PrivateStatus {
	return moduletrade.PrivateStatus{
		ContractVersion: moduletrade.PrivateContractVersion,
		ServiceStatus:   "ready",
		CorrelationID:   "core-1",
		ExecutionMode:   "DISABLED",
		LearningMode:    "OFFLINE_AVAILABLE",
		Ready:           true,
		KillSwitch:      "ON",
		Dependencies:    moduletrade.DependencyStatuses{Broker: "disabled", Ledger: "unconfigured", MarketData: "unavailable", MemoryOwner: "unavailable"},
		Policy: moduletrade.PolicyStatus{
			ExecutionMode:          "DISABLED",
			KillSwitch:             "ON",
			BrokerAdapter:          "none",
			ModulePolicyRevision:   "sha256:module",
			BinaryContractRevision: "trade-binary/v1",
			Capabilities:           map[string]bool{"broker_network": false, "paper_order": false, "live_order": false},
		},
		Portfolio: moduletrade.PortfolioProjection{Status: "unconfigured"},
	}
}

func clientMemoryHash(value byte) string {
	return "sha256:" + strings.Repeat(string(value), 64)
}

func clientSourceRecord() moduletrade.SourceRecord {
	return moduletrade.SourceRecord{
		RecordVersion: 1, SourceRecordID: "source-record-1", CaptureNonce: "0123456789abcdef", SourceDefinitionID: "source-definition-1",
		SourceDefinitionHash: clientMemoryHash('a'), Title: "Official source", Publisher: "Publisher", Jurisdiction: "JP", Category: "market", Language: "ja",
		SourceURL: "https://example.com/source", FinalURL: "https://example.com/final", TermsReference: "terms-1", LicenseStatus: "review_required",
		Status: "quarantined", ObservedAt: "2026-08-14T00:00:00Z", PointInTimeAvailableAt: "2026-08-14T00:00:00Z", HTTPStatus: 200,
		MediaType: "text/html", ByteSize: 12, ContentHash: clientMemoryHash('b'), Tags: []string{"official"},
	}
}

func clientLearningCandidate() moduletrade.LearningCandidateRecord {
	return moduletrade.LearningCandidateRecord{
		RecordVersion: 1, CandidateRecordID: "candidate-record-1", CandidateDefinitionID: "candidate-definition-1", Status: "candidate", Title: "Candidate", Statement: "A bounded statement.",
		BoundSources:  []moduletrade.BoundSource{{SourceDefinitionID: "source-definition-1", SourceRecordID: "source-record-1", ContentHash: clientMemoryHash('b'), ObservedAt: "2026-08-14T00:00:00Z", Locator: "section-1"}},
		Applicability: []string{"research"}, Limitations: []string{"not advice"}, InvalidationConditions: []string{"source changed"}, Tags: []string{"candidate"}, ContentHash: clientMemoryHash('c'),
	}
}

func clientMarketSnapshot() moduletrade.MarketSnapshot {
	return moduletrade.MarketSnapshot{
		SnapshotID: "snapshot-1", SchemaVersion: 1, InstrumentID: "instrument-1", Symbol: "TEST", Name: "Test instrument", AssetType: "equity", Venue: "TSE", Currency: "JPY",
		TradeDate: "2026-08-14", AvailableAt: "2026-08-14T00:00:00Z", Open: 100, High: 110, Low: 90, Close: 105, AdjClose: 105, Volume: 1000, SourceName: "official",
		RunID: "run-1", PlanID: "plan-1", PlanHash: clientMemoryHash('a'), DatasetID: "dataset-1", DatasetHash: clientMemoryHash('b'), DatasetSourceRef: "dataset-source-1", CodeRevision: "revision-1", ContentHash: clientMemoryHash('c'),
	}
}

func clientReplayDecision() moduletrade.ReplayDecision {
	return moduletrade.ReplayDecision{DecisionID: "decision-1", SchemaVersion: 1, SnapshotID: "snapshot-1", RunID: "run-1", InstrumentID: "instrument-1", TradeDate: "2026-08-14", Action: moduletrade.MemoryActionSelect, ContentHash: clientMemoryHash('d')}
}

func clientPortfolioSnapshot() moduletrade.PortfolioSnapshot {
	nav := int64(1_000_000)
	unrealized := int64(0)
	return moduletrade.PortfolioSnapshot{
		SchemaVersion: 1, PortfolioID: "main-sim", Mode: "SIMULATION", Guaranteed: false, InitialCashJPY: 1_000_000, CashJPY: 1_000_000,
		ValuationStatus: "complete", UnrealizedPnLJPY: &unrealized, NAVJPY: &nav, Positions: []moduletrade.PortfolioPosition{}, EventCount: 1, LatestEventHash: clientMemoryHash('e'),
	}
}

func clientMemoryEvidence(request *http.Request, purpose, domain, operation string) moduletrade.OwnerEvidence {
	requestID := request.Header.Get("X-Request-ID")
	freshnessState := "observed_at_read"
	validationState := "owner_route_succeeded"
	switch purpose {
	case "source_memory_read":
		freshnessState = "source_observed_at"
		validationState = "source_record_integrity_verified"
	case "learning_memory_read":
		freshnessState = "learning_candidate_observed_at"
		validationState = "learning_candidate_integrity_verified"
	case "market_memory_read":
		freshnessState = "market_snapshot_available_at_read"
		validationState = "market_snapshot_integrity_verified"
	case "replay_memory_read":
		freshnessState = "replay_snapshot_bound_at_read"
		validationState = "replay_decision_integrity_verified"
	}
	return moduletrade.OwnerEvidence{
		AgentID: request.Header.Get("X-RenCrow-Agent-ID"), Role: request.Header.Get("X-RenCrow-Agent-Role"), Purpose: purpose, DataScope: "internal", RequestID: requestID,
		OwnerModule: "RenCrow_TRADE", Domain: domain, Operation: operation, CorrelationID: request.Header.Get("X-Correlation-ID"), RequestTime: request.Header.Get("X-RenCrow-Request-Time"),
		ProvenanceRef: domain + "/record", RetrievedAt: "2026-08-14T00:00:00.123456789Z", FreshnessState: freshnessState, ValidationState: validationState, BudgetLimit: 1, ReturnedCount: 1,
	}
}

func clientMemoryReceipt(request *http.Request, purpose, domain, operation, auditRef string) moduletrade.OwnerReceipt {
	requestID := request.Header.Get("X-Request-ID")
	return moduletrade.OwnerReceipt{
		ReceiptID: "receipt-1", RequestID: requestID, AgentID: request.Header.Get("X-RenCrow-Agent-ID"), Role: request.Header.Get("X-RenCrow-Agent-Role"), Purpose: purpose, DataScope: "internal",
		OwnerModule: "RenCrow_TRADE", Domain: domain, Operation: operation, Status: "completed", IdempotentReplay: false, SchemaVersion: 1, AuditRef: auditRef, PolicyRevision: "policy-1",
		MigrationState: "embedded_current", ValidationState: "owner_validated", CompletedAt: "2026-08-14T00:00:00.123456789Z", CorrelationID: request.Header.Get("X-Correlation-ID"),
	}
}

func TestClientStatusUsesAuthenticatedPrivateRoute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/status" || request.Method != http.MethodGet {
			http.NotFound(writer, request)
			return
		}
		if request.Header.Get("Authorization") != "Bearer "+testToken || request.Header.Get("X-Correlation-ID") != "core-1" {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(disabledStatus())
	}))
	defer server.Close()

	client, err := NewClient(server.URL, writeToken(t), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.Status(context.Background(), "core-1")
	if err != nil {
		t.Fatal(err)
	}
	if status.Policy.ModulePolicyRevision != "sha256:module" || status.Dependencies.Broker != "disabled" {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestClientStatusRejectsExpandedTradingCapability(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		status := disabledStatus()
		status.Policy.Capabilities["live_order"] = true
		_ = json.NewEncoder(writer).Encode(status)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, writeToken(t), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Status(context.Background(), "core-1"); err == nil {
		t.Fatal("expected unauthorized capability rejection")
	}
}

func TestNewClientRejectsLooseTokenPermissions(t *testing.T) {
	path := writeToken(t)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewClient("http://127.0.0.1:8766", path, time.Second); err == nil {
		t.Fatal("expected token permissions rejection")
	}
}

func TestClientEvaluateUsesAuthenticatedPurePolicyRoute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/policy/evaluate" || request.Method != http.MethodPost {
			http.NotFound(writer, request)
			return
		}
		if request.Header.Get("Authorization") != "Bearer "+testToken || request.Header.Get("X-Correlation-ID") != "core-policy-1" {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		var input moduletrade.PolicyEvaluationRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(writer).Encode(moduletrade.PrivatePolicyEvaluation{
			ContractVersion: moduletrade.PrivateContractVersion,
			ServiceStatus:   "ready",
			CorrelationID:   "core-policy-1",
			ExecutionMode:   "DISABLED",
			LearningMode:    "OFFLINE_AVAILABLE",
			Decision: moduletrade.PolicyDecision{
				Capability:             input.Capability,
				Status:                 "blocked",
				ReasonCode:             "BINARY_HARD_LIMIT_BLOCKED",
				Reason:                 "binary hard limit blocks capability",
				BinaryContractRevision: "trade-binary/v1",
				ModulePolicyRevision:   "sha256:module",
				PolicyID:               "trade-disabled",
				GlobalBundleRevision:   input.GlobalPolicy.BundleRevision,
				DeploymentRevision:     input.Deployment.Revision,
				RequestScopeRevision:   input.RequestScope.Revision,
			},
		})
	}))
	defer server.Close()
	client, err := NewClient(server.URL, writeToken(t), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	request := moduletrade.PolicyEvaluationRequest{
		ContractVersion: moduletrade.PolicyEvaluationContractVersion,
		RequestID:       "core-policy-1",
		Capability:      "live_order",
		GlobalPolicy: moduletrade.GlobalPolicyInput{
			ContractRevision: "global-policy/v1",
			BundleRevision:   "2026-08-06.1",
			ContentSHA256:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Allowed:          true,
		},
		Deployment:   moduletrade.PolicyLayerInput{Revision: "deployment-1", Allowed: true},
		RequestScope: moduletrade.PolicyLayerInput{Revision: "scope-1", Allowed: true},
	}
	response, err := client.Evaluate(context.Background(), "core-policy-1", request)
	if err != nil {
		t.Fatal(err)
	}
	if response.Decision.ReasonCode != "BINARY_HARD_LIMIT_BLOCKED" {
		t.Fatalf("response=%+v", response)
	}
}

func TestClientPreviewRiskUsesAuthenticatedNonMutatingRoute(t *testing.T) {
	input := moduletrade.RiskPreviewRequest{
		ContractVersion: moduletrade.RiskPreviewRequestContractVersion,
		RequestID:       "core-risk-1",
		Plan: moduletrade.RiskPreviewPlan{
			ContractVersion: moduletrade.RiskPreviewPlanContractVersion,
			PlanID:          "plan-1", PolicyRevision: "2026-08-06.1", AsOf: "2026-08-06T00:00:00Z",
			Selection:    moduletrade.RiskPreviewSelection{InstrumentID: "JP-TEST", Status: "selected", EvidenceRefs: []string{"source:1"}, KnownMissingData: []string{}},
			Proposal:     moduletrade.RiskPreviewBuyProposal{InstrumentID: "JP-TEST", Side: "buy", Quantity: 100, EntryPriceJPY: 1000, GrossJPY: 100000},
			ExitContract: moduletrade.RiskPreviewExitContract{ContractID: "exit-1", Revision: "v1", InstrumentID: "JP-TEST"},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/portfolio/risk-preview" || request.Method != http.MethodPost {
			http.NotFound(writer, request)
			return
		}
		if request.Header.Get("Authorization") != "Bearer "+testToken || request.Header.Get("X-Correlation-ID") != input.RequestID || !assertOwnerHeaders(request, "shiro", "worker", "portfolio_memory_read", input.RequestID) {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		var got moduletrade.RiskPreviewRequest
		if err := json.NewDecoder(request.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if got.RequestID != input.RequestID || got.Plan.PlanID != input.Plan.PlanID {
			t.Fatalf("request=%+v", got)
		}
		_ = json.NewEncoder(writer).Encode(moduletrade.PrivateRiskPreview{
			ContractVersion: moduletrade.PrivateContractVersion,
			ServiceStatus:   "ready", CorrelationID: input.RequestID, ExecutionMode: "DISABLED", LearningMode: "OFFLINE_AVAILABLE",
			RequestID: input.RequestID, PortfolioID: "main-sim", PortfolioEventCount: 1,
			PortfolioLatestEventHash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Decision: moduletrade.RiskPreviewDecision{
				ContractVersion: moduletrade.RiskPreviewPlanContractVersion,
				PlanID:          input.Plan.PlanID, PolicyRevision: input.Plan.PolicyRevision, AsOf: input.Plan.AsOf, InstrumentID: input.Plan.Proposal.InstrumentID,
				Status: "pass", ReasonCodes: []string{}, InputSnapshotSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			},
			OwnerEvidence: moduletrade.OwnerEvidence{
				AgentID: "shiro", Role: "worker", Purpose: "portfolio_memory_read", DataScope: "internal", RequestID: input.RequestID,
				OwnerModule: "RenCrow_TRADE", Domain: "portfolio", Operation: "risk_preview", CorrelationID: input.RequestID,
				ProvenanceRef: "portfolio/genesis", RetrievedAt: "2026-08-14T00:00:00.123456789Z", FreshnessState: "observed_at_read", ValidationState: "owner_route_succeeded", BudgetLimit: 1, ReturnedCount: 1,
			},
		})
	}))
	defer server.Close()
	client, err := NewClient(server.URL, writeToken(t), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.PreviewRisk(ownerContext(t, input.RequestID, "shiro", "worker", "portfolio_memory_read"), input.RequestID, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision.Status != "pass" || result.Decision.AuthorizesExecution || result.Decision.MutatesPortfolio {
		t.Fatalf("result=%+v", result)
	}
}

func TestClientCommitSimulationUsesAuthenticatedPrivateRoute(t *testing.T) {
	inputHash := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	input := moduletrade.SimulationCommitRequest{
		ContractVersion: moduletrade.SimulationCommitContractVersion, RequestID: "sim-1", IdempotencyKey: "key-1",
		ExpectedPortfolioEventCount: 1, ExpectedPortfolioLatestEventHash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ExpectedInputSnapshotSHA256: inputHash,
		Plan:   moduletrade.RiskPreviewPlan{ContractVersion: moduletrade.RiskPreviewPlanContractVersion, PlanID: "plan-1", PolicyRevision: "2026-08-06.1", AsOf: "2026-08-06T00:00:00Z", Selection: moduletrade.RiskPreviewSelection{InstrumentID: "JP-TEST"}, Proposal: moduletrade.RiskPreviewBuyProposal{InstrumentID: "JP-TEST"}, ExitContract: moduletrade.RiskPreviewExitContract{ContractID: "exit-1", InstrumentID: "JP-TEST"}},
		Policy: moduletrade.PolicyEvaluationRequest{ContractVersion: moduletrade.PolicyEvaluationContractVersion, RequestID: "sim-1", Capability: "portfolio_simulation_commit", GlobalPolicy: moduletrade.GlobalPolicyInput{ContractRevision: "global-policy/v1", BundleRevision: "2026-08-06.1", ContentSHA256: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", Allowed: true}, Deployment: moduletrade.PolicyLayerInput{Revision: "deployment-1", Allowed: true}, RequestScope: moduletrade.PolicyLayerInput{Revision: "simulation-commit/sha256:" + inputHash, Allowed: true}},
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/portfolio/simulation-commit" || request.Header.Get("Authorization") != "Bearer "+testToken || !assertOwnerHeaders(request, "shiro", "worker", "portfolio_memory_write", "sim-1") {
			http.NotFound(writer, request)
			return
		}
		_ = json.NewEncoder(writer).Encode(moduletrade.PrivateSimulationCommit{
			ContractVersion: moduletrade.PrivateContractVersion, ServiceStatus: "ready", CorrelationID: "sim-1", ExecutionMode: "DISABLED", RequestID: "sim-1",
			PortfolioID: "main-sim", Mode: "SIMULATION", PortfolioMutated: true, PreviousPortfolioEventCount: 1, PreviousPortfolioLatestHash: input.ExpectedPortfolioLatestEventHash,
			PolicyDecision: moduletrade.PolicyDecision{Capability: "portfolio_simulation_commit", Status: "allowed", ModulePolicyRevision: "policy-1"}, RiskDecision: &moduletrade.RiskPreviewDecision{Status: "pass"}, Snapshot: moduletrade.PortfolioSnapshot{PortfolioID: "main-sim", Mode: "SIMULATION", EventCount: 2, LatestEventHash: "audit-1"},
			OwnerReceipt: moduletrade.OwnerReceipt{
				ReceiptID: "receipt-1", RequestID: "sim-1", AgentID: "shiro", Role: "worker", Purpose: "portfolio_memory_write", DataScope: "internal",
				OwnerModule: "RenCrow_TRADE", Domain: "portfolio", Operation: "simulation_commit", Status: "completed", IdempotentReplay: false, SchemaVersion: 1,
				AuditRef: "audit-1", PolicyRevision: "policy-1", MigrationState: "embedded_current", ValidationState: "owner_validated", CompletedAt: "2026-08-14T00:00:00.123456789Z",
			},
		})
	}))
	defer server.Close()
	client, err := NewClient(server.URL, writeToken(t), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.CommitSimulation(ownerContext(t, "sim-1", "shiro", "worker", "portfolio_memory_write"), "sim-1", input)
	if err != nil || !result.PortfolioMutated || result.AuthorizesExternalExecution {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestClientRecordShadowObservationUsesAuthenticatedPrivateRoute(t *testing.T) {
	contextHash := strings.Repeat("a", 64)
	input := moduletrade.ShadowObservationRequest{
		ContractVersion: moduletrade.ShadowObservationContractVersion, RequestID: "shadow-1",
		Observation: moduletrade.ShadowObservationInput{IdempotencyKey: "key-1", StudyID: "study-1", DecisionID: "decision-1", ActorID: "mio", InstrumentID: "JP-TEST", DecisionKind: "select", MarketObservedAt: "2026-08-06T12:00:00Z", ContextSnapshotSHA256: contextHash, OutcomeLabelContractSHA256: strings.Repeat("b", 64), ReasonCodes: []string{"ELIGIBLE"}, EvidenceRefs: []string{"source/official-1"}},
		Policy:      moduletrade.PolicyEvaluationRequest{ContractVersion: moduletrade.PolicyEvaluationContractVersion, RequestID: "shadow-1", Capability: "shadow_observation_record", GlobalPolicy: moduletrade.GlobalPolicyInput{ContractRevision: "global-policy/v1", BundleRevision: "2026-08-06.1", ContentSHA256: strings.Repeat("c", 64), Allowed: true}, Deployment: moduletrade.PolicyLayerInput{Revision: "deployment-1", Allowed: true}, RequestScope: moduletrade.PolicyLayerInput{Revision: "shadow-observation/sha256:" + contextHash, Allowed: true}},
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/shadow/observations" || request.Header.Get("Authorization") != "Bearer "+testToken || !assertOwnerHeaders(request, "shiro", "worker", "ledger_memory_write", "shadow-1") {
			http.NotFound(writer, request)
			return
		}
		_ = json.NewEncoder(writer).Encode(moduletrade.PrivateShadowObservation{
			ContractVersion: moduletrade.PrivateContractVersion, ServiceStatus: "ready", CorrelationID: "shadow-1", ExecutionMode: "DISABLED", RequestID: "shadow-1", Environment: "SHADOW",
			PolicyDecision: moduletrade.PolicyDecision{Capability: "shadow_observation_record", Status: "allowed", ModulePolicyRevision: "policy-1"},
			Event:          moduletrade.ShadowObservationEvent{EventVersion: 1, EventID: "shadow-event/sha256:" + strings.Repeat("d", 64), Sequence: 1, RecordedAt: "2026-08-06T12:01:00Z", Type: "shadow_observation_recorded", ShadowObservationInput: input.Observation, EventHash: "sha256:" + strings.Repeat("d", 64)},
			OwnerReceipt: moduletrade.OwnerReceipt{
				ReceiptID: "receipt-1", RequestID: "shadow-1", AgentID: "shiro", Role: "worker", Purpose: "ledger_memory_write", DataScope: "internal",
				OwnerModule: "RenCrow_TRADE", Domain: "ledger", Operation: "shadow_observation", Status: "completed", IdempotentReplay: false, SchemaVersion: 1,
				AuditRef: "shadow-event/sha256:" + strings.Repeat("d", 64), PolicyRevision: "policy-1", MigrationState: "embedded_current", ValidationState: "owner_validated", CompletedAt: "2026-08-14T00:00:00.123456789Z",
			},
		})
	}))
	defer server.Close()
	client, err := NewClient(server.URL, writeToken(t), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.RecordShadowObservation(ownerContext(t, "shadow-1", "shiro", "worker", "ledger_memory_write"), "shadow-1", input)
	if err != nil || result.Environment != "SHADOW" || result.AuthorizesExternalExecution || result.PortfolioMutated || result.KnowledgePromoted {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestClientRecordShadowOutcomeUsesAuthenticatedPrivateRoute(t *testing.T) {
	input := moduletrade.ShadowOutcomeRequest{
		ContractVersion: moduletrade.ShadowOutcomeContractVersion, RequestID: "outcome-1",
		Outcome: moduletrade.ShadowOutcomeInput{IdempotencyKey: "outcome-key-1", StudyID: "study-1", DecisionID: "decision-1", MarketObservedAt: "2026-08-06T12:00:00Z", OutcomeLabel: "success", OutcomeObservedAt: "2026-08-07T12:00:00Z", OutcomeSnapshotSHA256: strings.Repeat("c", 64), OutcomeReasonCodes: []string{"THESIS_CONFIRMED"}, OutcomeEvidenceRefs: []string{"source/outcome-1"}, OutcomeLabelContractSHA256: strings.Repeat("b", 64)},
		Policy:  moduletrade.PolicyEvaluationRequest{ContractVersion: moduletrade.PolicyEvaluationContractVersion, RequestID: "outcome-1", Capability: "shadow_outcome_record", GlobalPolicy: moduletrade.GlobalPolicyInput{ContractRevision: "global-policy/v1", BundleRevision: "2026-08-06.1", ContentSHA256: strings.Repeat("d", 64), Allowed: true}, Deployment: moduletrade.PolicyLayerInput{Revision: "deployment-1", Allowed: true}, RequestScope: moduletrade.PolicyLayerInput{Revision: "shadow-outcome/sha256:" + strings.Repeat("c", 64), Allowed: true}},
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/shadow/outcomes" || request.Header.Get("Authorization") != "Bearer "+testToken || !assertOwnerHeaders(request, "shiro", "worker", "ledger_memory_write", "outcome-1") {
			http.NotFound(writer, request)
			return
		}
		_ = json.NewEncoder(writer).Encode(moduletrade.PrivateShadowOutcome{
			ContractVersion: moduletrade.PrivateContractVersion, ServiceStatus: "ready", CorrelationID: "outcome-1", ExecutionMode: "DISABLED", RequestID: "outcome-1", Environment: "SHADOW",
			PolicyDecision: moduletrade.PolicyDecision{Capability: "shadow_outcome_record", Status: "allowed", ModulePolicyRevision: "policy-1"},
			Event:          moduletrade.ShadowOutcomeEvent{EventVersion: 1, EventID: "shadow-event/sha256:" + strings.Repeat("e", 64), Sequence: 2, RecordedAt: "2026-08-07T12:01:00Z", Type: "shadow_outcome_recorded", IdempotencyKey: "outcome-key-1", StudyID: "study-1", DecisionID: "decision-1", MarketObservedAt: "2026-08-06T12:00:00Z", OutcomeLabel: "success", OutcomeObservedAt: "2026-08-07T12:00:00Z", OutcomeSnapshotSHA256: strings.Repeat("c", 64), OutcomeReasonCodes: []string{"THESIS_CONFIRMED"}, OutcomeEvidenceRefs: []string{"source/outcome-1"}, OutcomeLabelContractSHA256: strings.Repeat("b", 64), EventHash: "sha256:" + strings.Repeat("e", 64)},
			OwnerReceipt: moduletrade.OwnerReceipt{
				ReceiptID: "receipt-1", RequestID: "outcome-1", AgentID: "shiro", Role: "worker", Purpose: "ledger_memory_write", DataScope: "internal",
				OwnerModule: "RenCrow_TRADE", Domain: "ledger", Operation: "shadow_outcome", Status: "completed", IdempotentReplay: false, SchemaVersion: 1,
				AuditRef: "shadow-event/sha256:" + strings.Repeat("e", 64), PolicyRevision: "policy-1", MigrationState: "embedded_current", ValidationState: "owner_validated", CompletedAt: "2026-08-14T00:00:00.123456789Z",
			},
		})
	}))
	defer server.Close()
	client, err := NewClient(server.URL, writeToken(t), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.RecordShadowOutcome(ownerContext(t, "outcome-1", "shiro", "worker", "ledger_memory_write"), "outcome-1", input)
	if err != nil || result.Environment != "SHADOW" || result.AuthorizesExternalExecution || result.PortfolioMutated || result.KnowledgePromoted {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestClientShadowOutcomeReportUsesAuthenticatedReadOnlyRoute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/shadow/outcomes/report" || request.URL.Query().Get("study_id") != "study-1" || request.Method != http.MethodGet {
			http.NotFound(writer, request)
			return
		}
		if request.Header.Get("Authorization") != "Bearer "+testToken || request.Header.Get("X-Correlation-ID") != "report-1" || !assertOwnerHeaders(request, "shiro", "worker", "ledger_memory_read", "report-1") {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(writer).Encode(moduletrade.PrivateShadowOutcomeReport{
			ContractVersion: moduletrade.PrivateContractVersion, ServiceStatus: "ready", CorrelationID: "report-1", ExecutionMode: "DISABLED", Environment: "SHADOW",
			Report: moduletrade.ShadowOutcomeReport{SchemaVersion: 1, ContractVersion: moduletrade.ShadowOutcomeReportContractVersion, StudyID: "study-1", Environment: "SHADOW", ObservationCount: 2, OutcomeCount: 1, PendingOutcomeCount: 1, LabelCounts: map[string]int64{"success": 1, "failure": 0, "neutral": 0, "inconclusive": 0}, ReviewState: "review_required"},
			OwnerEvidence: moduletrade.OwnerEvidence{
				AgentID: "shiro", Role: "worker", Purpose: "ledger_memory_read", DataScope: "internal", RequestID: "report-1",
				OwnerModule: "RenCrow_TRADE", Domain: "ledger", Operation: "shadow_outcome_report", CorrelationID: "report-1",
				ProvenanceRef: "ledger/genesis", RetrievedAt: "2026-08-14T00:00:00.123456789Z", FreshnessState: "observed_at_read", ValidationState: "owner_route_succeeded", BudgetLimit: 1, ReturnedCount: 1,
			},
		})
	}))
	defer server.Close()
	client, err := NewClient(server.URL, writeToken(t), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.ShadowOutcomeReport(ownerContext(t, "report-1", "shiro", "worker", "ledger_memory_read"), "report-1", "study-1")
	if err != nil || result.Report.StudyID != "study-1" || result.Report.ReviewState != "review_required" || result.AuthorizesExternalExecution || result.PortfolioMutated || result.KnowledgePromoted {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestClientShadowOutcomeReportUsesEventAuditRefRoute(t *testing.T) {
	eventID := "shadow-event/sha256:" + strings.Repeat("e", 64)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/shadow/outcomes/report" || request.URL.Query().Get("event_id") != eventID || request.URL.Query().Get("study_id") != "" || request.Method != http.MethodGet {
			http.NotFound(writer, request)
			return
		}
		if request.Header.Get("Authorization") != "Bearer "+testToken || request.Header.Get("X-Correlation-ID") != "event-report-1" || !assertOwnerHeaders(request, "shiro", "worker", "ledger_memory_read", "event-report-1") {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(writer).Encode(shadowOutcomeReportResponseForTest("event-report-1", "study-1", eventID))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, writeToken(t), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.ShadowOutcomeReport(ownerContext(t, "event-report-1", "shiro", "worker", "ledger_memory_read"), "event-report-1", eventID)
	if err != nil || result.Report.StudyID != "study-1" || result.OwnerEvidence.ProvenanceRef != eventID {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestClientShadowOutcomeReportRejectsMalformedQueryBeforeNetwork(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls++
		http.Error(writer, "unexpected network call", http.StatusInternalServerError)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, writeToken(t), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	queries := []string{
		"shadow-event/sha256:" + strings.Repeat("E", 64),
		"shadow-event/sha256:" + strings.Repeat("e", 63),
		"shadow-event/sha256:" + strings.Repeat("g", 64),
		"shadow-event/not-a-hash",
		"study/1",
	}
	for _, query := range queries {
		if _, err := client.ShadowOutcomeReport(ownerContext(t, "malformed-report", "shiro", "worker", "ledger_memory_read"), "malformed-report", query); err == nil {
			t.Fatalf("malformed query %q was accepted", query)
		}
	}
	if calls != 0 {
		t.Fatalf("malformed query reached network %d times", calls)
	}
}

func TestClientShadowOutcomeReportRejectsEventResponseBindingMismatch(t *testing.T) {
	eventID := "shadow-event/sha256:" + strings.Repeat("e", 64)
	for _, test := range []struct {
		name       string
		studyID    string
		provenance string
	}{
		{name: "invalid study id", studyID: "study/1", provenance: eventID},
		{name: "provenance mismatch", studyID: "study-1", provenance: "shadow-event/sha256:" + strings.Repeat("f", 64)},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Query().Get("event_id") != eventID {
					http.NotFound(writer, request)
					return
				}
				_ = json.NewEncoder(writer).Encode(shadowOutcomeReportResponseForTest("event-binding-mismatch", test.studyID, test.provenance))
			}))
			defer server.Close()
			client, err := NewClient(server.URL, writeToken(t), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.ShadowOutcomeReport(ownerContext(t, "event-binding-mismatch", "shiro", "worker", "ledger_memory_read"), "event-binding-mismatch", eventID); err == nil {
				t.Fatal("event response binding mismatch was accepted")
			}
		})
	}
}

func shadowOutcomeReportResponseForTest(correlationID, studyID, provenance string) moduletrade.PrivateShadowOutcomeReport {
	return moduletrade.PrivateShadowOutcomeReport{
		ContractVersion: moduletrade.PrivateContractVersion, ServiceStatus: "ready", CorrelationID: correlationID, ExecutionMode: "DISABLED", Environment: "SHADOW",
		Report: moduletrade.ShadowOutcomeReport{
			SchemaVersion: 1, ContractVersion: moduletrade.ShadowOutcomeReportContractVersion, StudyID: studyID, Environment: "SHADOW",
			ObservationCount: 2, OutcomeCount: 1, PendingOutcomeCount: 1,
			LabelCounts: map[string]int64{"success": 1, "failure": 0, "neutral": 0, "inconclusive": 0}, ReviewState: "review_required",
		},
		OwnerEvidence: moduletrade.OwnerEvidence{
			AgentID: "shiro", Role: "worker", Purpose: "ledger_memory_read", DataScope: "internal", RequestID: correlationID,
			OwnerModule: "RenCrow_TRADE", Domain: "ledger", Operation: "shadow_outcome_report", CorrelationID: correlationID,
			ProvenanceRef: provenance, RetrievedAt: "2026-08-14T00:00:00.123456789Z", FreshnessState: "observed_at_read", ValidationState: "owner_route_succeeded", BudgetLimit: 1, ReturnedCount: 1,
		},
	}
}

func TestOwnerMethodsRejectMissingOrWrongScopeBeforeNetwork(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls++
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, writeToken(t), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	input := moduletrade.RiskPreviewRequest{
		ContractVersion: moduletrade.RiskPreviewRequestContractVersion, RequestID: "scope-1",
		Plan: moduletrade.RiskPreviewPlan{
			ContractVersion: moduletrade.RiskPreviewPlanContractVersion, PlanID: "plan-1", PolicyRevision: "policy-1", AsOf: "2026-08-14T00:00:00Z",
			Selection: moduletrade.RiskPreviewSelection{InstrumentID: "JP-TEST"}, Proposal: moduletrade.RiskPreviewBuyProposal{InstrumentID: "JP-TEST"},
			ExitContract: moduletrade.RiskPreviewExitContract{ContractID: "exit-1", InstrumentID: "JP-TEST"},
		},
	}
	if _, err := client.PreviewRisk(context.Background(), input.RequestID, input); err == nil {
		t.Fatal("missing owner scope must be rejected")
	}
	if _, err := client.PreviewRisk(ownerContext(t, input.RequestID, "shiro", "worker", "portfolio_memory_write"), input.RequestID, input); err == nil {
		t.Fatal("wrong owner purpose must be rejected")
	}
	if _, err := client.ReadPortfolioSnapshot(context.Background()); err == nil {
		t.Fatal("portfolio read without owner scope must be rejected")
	}
	if _, err := client.EnsurePortfolioInitialized(ownerContext(t, "portfolio-scope-1", "shiro", "worker", "portfolio_memory_read")); err == nil {
		t.Fatal("portfolio write with wrong owner purpose must be rejected")
	}
	wrongRole := toolcontext.ToolExecutionScope{
		RequestID: input.RequestID, ActorKind: toolcontext.ActorKindAgent, ActorID: "shiro", AgentRole: "heavy", Purpose: "portfolio_memory_read",
		AllowedDataScopes: []string{toolcontext.DataScopeInternal}, AuthenticationSource: toolcontext.AuthenticationSourceAgentOrchestrator,
	}
	if _, err := client.PreviewRisk(toolcontext.WithToolExecutionScope(context.Background(), wrongRole), input.RequestID, input); err == nil {
		t.Fatal("wrong owner role must be rejected")
	}
	if calls != 0 {
		t.Fatalf("invalid owner scope reached network %d times", calls)
	}
}

func TestClientMemoryOwnerRoutesUseTypedAuthenticatedContracts(t *testing.T) {
	type memoryCall struct {
		name      string
		method    string
		path      string
		requestID string
		agentID   string
		role      string
		purpose   string
		invoke    func(*Client, context.Context) error
	}
	cases := []memoryCall{
		{name: "portfolio read", method: http.MethodGet, path: "/v1/memory/portfolio/snapshot", requestID: "portfolio-read-1", agentID: "shiro", role: "worker", purpose: "portfolio_memory_read", invoke: func(client *Client, ctx context.Context) error {
			_, err := client.ReadPortfolioSnapshot(ctx)
			return err
		}},
		{name: "portfolio write", method: http.MethodPost, path: "/v1/memory/portfolio/ensure-initialized", requestID: "portfolio-write-1", agentID: "shiro", role: "worker", purpose: "portfolio_memory_write", invoke: func(client *Client, ctx context.Context) error {
			_, err := client.EnsurePortfolioInitialized(ctx)
			return err
		}},
		{name: "source read", method: http.MethodGet, path: "/v1/memory/source/records/source-record-1", requestID: "source-read-1", agentID: "shiro", role: "worker", purpose: "source_memory_read", invoke: func(client *Client, ctx context.Context) error {
			_, err := client.ReadSourceRecord(ctx, "source-record-1")
			return err
		}},
		{name: "source write", method: http.MethodPost, path: "/v1/memory/source/collect", requestID: "source-write-1", agentID: "shiro", role: "worker", purpose: "source_memory_write", invoke: func(client *Client, ctx context.Context) error {
			_, err := client.CollectSource(ctx, "source-definition-1")
			return err
		}},
		{name: "learning read", method: http.MethodGet, path: "/v1/memory/learning/candidates/candidate-record-1", requestID: "learning-read-1", agentID: "shiro", role: "worker", purpose: "learning_memory_read", invoke: func(client *Client, ctx context.Context) error {
			_, err := client.ReadLearningCandidate(ctx, "candidate-record-1")
			return err
		}},
		{name: "learning write", method: http.MethodPost, path: "/v1/memory/learning/import-candidate", requestID: "learning-write-1", agentID: "shiro", role: "worker", purpose: "learning_memory_write", invoke: func(client *Client, ctx context.Context) error {
			_, err := client.ImportLearningCandidate(ctx, "candidate-definition-1")
			return err
		}},
		{name: "market read", method: http.MethodGet, path: "/v1/memory/market/snapshots/snapshot-1", requestID: "market-read-1", agentID: "kuro", role: "heavy", purpose: "market_memory_read", invoke: func(client *Client, ctx context.Context) error {
			_, err := client.ReadMarketSnapshot(ctx, "snapshot-1")
			return err
		}},
		{name: "market write", method: http.MethodPost, path: "/v1/memory/market/import-snapshot", requestID: "market-write-1", agentID: "kuro", role: "heavy", purpose: "market_memory_write", invoke: func(client *Client, ctx context.Context) error {
			_, err := client.ImportMarketSnapshot(ctx, "run-1", "instrument-1", "2026-08-14")
			return err
		}},
		{name: "replay read", method: http.MethodGet, path: "/v1/memory/replay/decisions/decision-1", requestID: "replay-read-1", agentID: "shiro", role: "worker", purpose: "replay_memory_read", invoke: func(client *Client, ctx context.Context) error {
			_, err := client.ReadReplayDecision(ctx, "decision-1")
			return err
		}},
		{name: "replay write", method: http.MethodPost, path: "/v1/memory/replay/record-decision", requestID: "replay-write-1", agentID: "shiro", role: "worker", purpose: "replay_memory_write", invoke: func(client *Client, ctx context.Context) error {
			_, err := client.RecordReplayDecision(ctx, "run-1", "instrument-1", "2026-08-14", moduletrade.MemoryActionSelect)
			return err
		}},
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+testToken {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		expectedMethod := map[string]string{
			"/v1/memory/portfolio/snapshot":                     http.MethodGet,
			"/v1/memory/portfolio/ensure-initialized":           http.MethodPost,
			"/v1/memory/source/records/source-record-1":         http.MethodGet,
			"/v1/memory/source/collect":                         http.MethodPost,
			"/v1/memory/learning/candidates/candidate-record-1": http.MethodGet,
			"/v1/memory/learning/import-candidate":              http.MethodPost,
			"/v1/memory/market/snapshots/snapshot-1":            http.MethodGet,
			"/v1/memory/market/import-snapshot":                 http.MethodPost,
			"/v1/memory/replay/decisions/decision-1":            http.MethodGet,
			"/v1/memory/replay/record-decision":                 http.MethodPost,
		}
		if request.Method != expectedMethod[request.URL.Path] {
			http.Error(writer, "invalid method", http.StatusMethodNotAllowed)
			return
		}
		if !assertOwnerHeaders(request, request.Header.Get("X-RenCrow-Agent-ID"), request.Header.Get("X-RenCrow-Agent-Role"), request.Header.Get("X-RenCrow-Request-Purpose"), request.Header.Get("X-Request-ID")) || request.Header.Get("X-Correlation-ID") != request.Header.Get("X-Request-ID") {
			http.Error(writer, "invalid owner headers", http.StatusForbidden)
			return
		}
		if request.Method == http.MethodPost {
			var payload map[string]any
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				http.Error(writer, "invalid request body", http.StatusBadRequest)
				return
			}
			if payload["request_id"] != request.Header.Get("X-Request-ID") || payload["contract_version"] != moduletrade.MemoryOwnerContractVersion {
				http.Error(writer, "untrusted request identity", http.StatusBadRequest)
				return
			}
		}
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/v1/memory/portfolio/snapshot":
			_ = json.NewEncoder(writer).Encode(moduletrade.PortfolioSnapshotReadResponse{ContractVersion: moduletrade.PrivateContractVersion, ServiceStatus: "ready", CorrelationID: request.Header.Get("X-Correlation-ID"), ExecutionMode: "DISABLED", LearningMode: "OFFLINE_AVAILABLE", MemorySource: moduletrade.MemoryOwnerSource, Snapshot: clientPortfolioSnapshot(), OwnerEvidence: clientMemoryEvidence(request, "portfolio_memory_read", "portfolio", "portfolio_snapshot")})
		case "/v1/memory/portfolio/ensure-initialized":
			snapshot := clientPortfolioSnapshot()
			_ = json.NewEncoder(writer).Encode(moduletrade.PortfolioSnapshotWriteResponse{ContractVersion: moduletrade.PrivateContractVersion, ServiceStatus: "ready", CorrelationID: request.Header.Get("X-Correlation-ID"), ExecutionMode: "DISABLED", LearningMode: "OFFLINE_AVAILABLE", MemorySource: moduletrade.MemoryOwnerSource, Snapshot: snapshot, OwnerReceipt: clientMemoryReceipt(request, "portfolio_memory_write", "portfolio", "ensure_initialized", snapshot.LatestEventHash)})
		case "/v1/memory/source/records/source-record-1":
			_ = json.NewEncoder(writer).Encode(moduletrade.SourceRecordReadResponse{ContractVersion: moduletrade.PrivateContractVersion, ServiceStatus: "ready", CorrelationID: request.Header.Get("X-Correlation-ID"), ExecutionMode: "DISABLED", LearningMode: "OFFLINE_AVAILABLE", MemorySource: moduletrade.MemoryOwnerSource, Record: clientSourceRecord(), OwnerEvidence: clientMemoryEvidence(request, "source_memory_read", "source", "source_record")})
		case "/v1/memory/source/collect":
			_ = json.NewEncoder(writer).Encode(moduletrade.SourceRecordWriteResponse{ContractVersion: moduletrade.PrivateContractVersion, ServiceStatus: "ready", CorrelationID: request.Header.Get("X-Correlation-ID"), ExecutionMode: "DISABLED", LearningMode: "OFFLINE_AVAILABLE", MemorySource: moduletrade.MemoryOwnerSource, Record: clientSourceRecord(), OwnerReceipt: clientMemoryReceipt(request, "source_memory_write", "source", "collect_source", "source-record-1")})
		case "/v1/memory/learning/candidates/candidate-record-1":
			_ = json.NewEncoder(writer).Encode(moduletrade.LearningCandidateReadResponse{ContractVersion: moduletrade.PrivateContractVersion, ServiceStatus: "ready", CorrelationID: request.Header.Get("X-Correlation-ID"), ExecutionMode: "DISABLED", LearningMode: "OFFLINE_AVAILABLE", MemorySource: moduletrade.MemoryOwnerSource, Record: clientLearningCandidate(), OwnerEvidence: clientMemoryEvidence(request, "learning_memory_read", "learning", "learning_candidate")})
		case "/v1/memory/learning/import-candidate":
			_ = json.NewEncoder(writer).Encode(moduletrade.LearningCandidateWriteResponse{ContractVersion: moduletrade.PrivateContractVersion, ServiceStatus: "ready", CorrelationID: request.Header.Get("X-Correlation-ID"), ExecutionMode: "DISABLED", LearningMode: "OFFLINE_AVAILABLE", MemorySource: moduletrade.MemoryOwnerSource, Record: clientLearningCandidate(), OwnerReceipt: clientMemoryReceipt(request, "learning_memory_write", "learning", "import_learning_candidate", "candidate-record-1")})
		case "/v1/memory/market/snapshots/snapshot-1":
			_ = json.NewEncoder(writer).Encode(moduletrade.MarketSnapshotReadResponse{ContractVersion: moduletrade.PrivateContractVersion, ServiceStatus: "ready", CorrelationID: request.Header.Get("X-Correlation-ID"), ExecutionMode: "DISABLED", LearningMode: "OFFLINE_AVAILABLE", MemorySource: moduletrade.MemoryOwnerSource, Record: clientMarketSnapshot(), OwnerEvidence: clientMemoryEvidence(request, "market_memory_read", "market", "market_snapshot")})
		case "/v1/memory/market/import-snapshot":
			_ = json.NewEncoder(writer).Encode(moduletrade.MarketSnapshotWriteResponse{ContractVersion: moduletrade.PrivateContractVersion, ServiceStatus: "ready", CorrelationID: request.Header.Get("X-Correlation-ID"), ExecutionMode: "DISABLED", LearningMode: "OFFLINE_AVAILABLE", MemorySource: moduletrade.MemoryOwnerSource, Record: clientMarketSnapshot(), OwnerReceipt: clientMemoryReceipt(request, "market_memory_write", "market", "import_market_snapshot", "snapshot-1")})
		case "/v1/memory/replay/decisions/decision-1":
			_ = json.NewEncoder(writer).Encode(moduletrade.ReplayDecisionReadResponse{ContractVersion: moduletrade.PrivateContractVersion, ServiceStatus: "ready", CorrelationID: request.Header.Get("X-Correlation-ID"), ExecutionMode: "DISABLED", LearningMode: "OFFLINE_AVAILABLE", MemorySource: moduletrade.MemoryOwnerSource, Record: clientReplayDecision(), OwnerEvidence: clientMemoryEvidence(request, "replay_memory_read", "replay", "replay_decision")})
		case "/v1/memory/replay/record-decision":
			_ = json.NewEncoder(writer).Encode(moduletrade.ReplayDecisionWriteResponse{ContractVersion: moduletrade.PrivateContractVersion, ServiceStatus: "ready", CorrelationID: request.Header.Get("X-Correlation-ID"), ExecutionMode: "DISABLED", LearningMode: "OFFLINE_AVAILABLE", MemorySource: moduletrade.MemoryOwnerSource, Record: clientReplayDecision(), OwnerReceipt: clientMemoryReceipt(request, "replay_memory_write", "replay", "record_replay_decision", "decision-1")})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	client, err := NewClient(server.URL, writeToken(t), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			ctx := ownerContext(t, test.requestID, test.agentID, test.role, test.purpose)
			if err := test.invoke(client, ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestClientMemoryOwnerRejectsUnsafePathAndStrictResponses(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		switch request.URL.Path {
		case "/v1/memory/source/records/source-record-1":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"contract_version":"trade-private/v1","service_status":"ready","correlation_id":"memory-1","memory_source":"RenCrow_TRADE","record":{},"owner_evidence":{},"unknown":true}`))
		default:
			writer.WriteHeader(http.StatusTeapot)
		}
	}))
	defer server.Close()
	client, err := NewClient(server.URL, writeToken(t), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx := ownerContext(t, "memory-1", "shiro", "worker", "source_memory_read")
	if _, err := client.ReadSourceRecord(ctx, "source/record"); err == nil {
		t.Fatal("slash-containing path ID must be rejected before network")
	}
	if calls != 0 {
		t.Fatalf("unsafe path reached network %d times", calls)
	}
	if _, err := client.ReadSourceRecord(ctx, "source-record-1"); err == nil {
		t.Fatal("unknown response field must be rejected")
	}

	serverError := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer serverError.Close()
	client, err = NewClient(serverError.URL, writeToken(t), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.ReadSourceRecord(ctx, "source-record-1")
	var serviceErr *ServiceError
	if !errors.As(err, &serviceErr) || serviceErr.HTTPStatus() != http.StatusServiceUnavailable {
		t.Fatalf("expected ServiceError(503), got %v", err)
	}
}

func TestClientPortfolioOwnerRejectsStrictResponsesAndServiceErrors(t *testing.T) {
	mode := "unknown"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/memory/portfolio/snapshot" || request.Method != http.MethodGet {
			http.NotFound(writer, request)
			return
		}
		if mode == "status" {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		response := moduletrade.PortfolioSnapshotReadResponse{
			ContractVersion: moduletrade.PrivateContractVersion, ServiceStatus: "ready", CorrelationID: request.Header.Get("X-Correlation-ID"), ExecutionMode: "DISABLED", LearningMode: "OFFLINE_AVAILABLE",
			MemorySource: moduletrade.MemoryOwnerSource, Snapshot: clientPortfolioSnapshot(), OwnerEvidence: clientMemoryEvidence(request, "portfolio_memory_read", "portfolio", "portfolio_snapshot"),
		}
		payload, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		switch mode {
		case "unknown":
			payload = append(payload[:len(payload)-1], []byte(`,"unknown":true}`)...)
		case "trailing":
			payload = append(payload, []byte(`{}`)...)
		case "identity":
			response.OwnerEvidence.AgentID = "kuro"
			payload, err = json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write(payload)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, writeToken(t), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx := ownerContext(t, "portfolio-strict-1", "shiro", "worker", "portfolio_memory_read")
	if _, err := client.ReadPortfolioSnapshot(ctx); err == nil {
		t.Fatal("unknown response field must be rejected")
	}
	mode = "trailing"
	if _, err := client.ReadPortfolioSnapshot(ctx); err == nil {
		t.Fatal("trailing JSON must be rejected")
	}
	mode = "identity"
	if _, err := client.ReadPortfolioSnapshot(ctx); err == nil {
		t.Fatal("owner evidence identity mismatch must be rejected")
	}
	mode = "status"
	_, err = client.ReadPortfolioSnapshot(ctx)
	var serviceErr *ServiceError
	if !errors.As(err, &serviceErr) || serviceErr.HTTPStatus() != http.StatusServiceUnavailable {
		t.Fatalf("expected ServiceError(503), got %v", err)
	}
}
