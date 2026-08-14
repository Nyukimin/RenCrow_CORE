package tradeclient

import (
	"context"
	"encoding/json"
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
	return request.Header.Get("X-RenCrow-Agent-ID") == agentID &&
		request.Header.Get("X-RenCrow-Agent-Role") == role &&
		request.Header.Get("X-RenCrow-Request-Purpose") == purpose &&
		request.Header.Get("X-RenCrow-Data-Scope") == "internal" &&
		request.Header.Get("X-Request-ID") == requestID
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
		Dependencies:    moduletrade.DependencyStatuses{Broker: "disabled", Ledger: "unconfigured", MarketData: "unavailable"},
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
