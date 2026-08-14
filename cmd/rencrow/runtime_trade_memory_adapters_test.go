package main

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

type runtimeTradeMemoryCall struct {
	Method string
	Args   []string
	Scope  domaintool.ToolExecutionScope
}

type runtimeTradeMemoryClientStub struct {
	Calls              []runtimeTradeMemoryCall
	portfolioResponse  func(domaintool.ToolExecutionScope) moduletrade.PortfolioSnapshotReadResponse
	portfolioReadError error
}

func (s *runtimeTradeMemoryClientStub) capture(ctx context.Context, method string, args ...string) domaintool.ToolExecutionScope {
	scope, _ := domaintool.ToolExecutionScopeFromContext(ctx)
	s.Calls = append(s.Calls, runtimeTradeMemoryCall{Method: method, Args: append([]string(nil), args...), Scope: scope})
	return scope
}

func (s *runtimeTradeMemoryClientStub) ReadSourceRecord(ctx context.Context, recordID string) (moduletrade.SourceRecordReadResponse, error) {
	scope := s.capture(ctx, "ReadSourceRecord", recordID)
	return runtimeTradeMemorySourceReadResponse(scope, recordID), nil
}

func (s *runtimeTradeMemoryClientStub) CollectSource(ctx context.Context, sourceDefinitionID string) (moduletrade.SourceRecordWriteResponse, error) {
	scope := s.capture(ctx, "CollectSource", sourceDefinitionID)
	record := runtimeTradeMemorySourceRecord("source-written")
	return moduletrade.SourceRecordWriteResponse{
		ContractVersion: moduletrade.PrivateContractVersion, ServiceStatus: "ready", CorrelationID: scope.RequestID,
		ExecutionMode: "DISABLED", LearningMode: "OFFLINE_AVAILABLE", MemorySource: moduletrade.MemoryOwnerSource,
		Record: record, OwnerReceipt: runtimeTradeMemoryReceipt(scope, "source", "collect_source", record.SourceRecordID, false),
	}, nil
}

func (s *runtimeTradeMemoryClientStub) ReadLearningCandidate(ctx context.Context, recordID string) (moduletrade.LearningCandidateReadResponse, error) {
	scope := s.capture(ctx, "ReadLearningCandidate", recordID)
	record := runtimeTradeMemoryLearningRecord(recordID)
	return moduletrade.LearningCandidateReadResponse{
		ContractVersion: moduletrade.PrivateContractVersion, ServiceStatus: "ready", CorrelationID: scope.RequestID,
		ExecutionMode: "DISABLED", LearningMode: "OFFLINE_AVAILABLE", MemorySource: moduletrade.MemoryOwnerSource,
		Record: record, OwnerEvidence: runtimeTradeMemoryEvidence(scope, "learning", "learning_candidate"),
	}, nil
}

func (s *runtimeTradeMemoryClientStub) ImportLearningCandidate(ctx context.Context, candidateDefinitionID string) (moduletrade.LearningCandidateWriteResponse, error) {
	scope := s.capture(ctx, "ImportLearningCandidate", candidateDefinitionID)
	record := runtimeTradeMemoryLearningRecord("candidate-written")
	return moduletrade.LearningCandidateWriteResponse{
		ContractVersion: moduletrade.PrivateContractVersion, ServiceStatus: "ready", CorrelationID: scope.RequestID,
		ExecutionMode: "DISABLED", LearningMode: "OFFLINE_AVAILABLE", MemorySource: moduletrade.MemoryOwnerSource,
		Record: record, OwnerReceipt: runtimeTradeMemoryReceipt(scope, "learning", "import_learning_candidate", record.CandidateRecordID, false),
	}, nil
}

func (s *runtimeTradeMemoryClientStub) ReadMarketSnapshot(ctx context.Context, snapshotID string) (moduletrade.MarketSnapshotReadResponse, error) {
	scope := s.capture(ctx, "ReadMarketSnapshot", snapshotID)
	record := runtimeTradeMemoryMarketRecord(snapshotID)
	return moduletrade.MarketSnapshotReadResponse{
		ContractVersion: moduletrade.PrivateContractVersion, ServiceStatus: "ready", CorrelationID: scope.RequestID,
		ExecutionMode: "DISABLED", LearningMode: "OFFLINE_AVAILABLE", MemorySource: moduletrade.MemoryOwnerSource,
		Record: record, OwnerEvidence: runtimeTradeMemoryEvidence(scope, "market", "market_snapshot"),
	}, nil
}

func (s *runtimeTradeMemoryClientStub) ImportMarketSnapshot(ctx context.Context, runID, instrumentID, tradeDate string) (moduletrade.MarketSnapshotWriteResponse, error) {
	scope := s.capture(ctx, "ImportMarketSnapshot", runID, instrumentID, tradeDate)
	record := runtimeTradeMemoryMarketRecord("snapshot-written")
	record.RunID, record.InstrumentID, record.TradeDate = runID, instrumentID, tradeDate
	return moduletrade.MarketSnapshotWriteResponse{
		ContractVersion: moduletrade.PrivateContractVersion, ServiceStatus: "ready", CorrelationID: scope.RequestID,
		ExecutionMode: "DISABLED", LearningMode: "OFFLINE_AVAILABLE", MemorySource: moduletrade.MemoryOwnerSource,
		Record: record, OwnerReceipt: runtimeTradeMemoryReceipt(scope, "market", "import_market_snapshot", record.SnapshotID, false),
	}, nil
}

func (s *runtimeTradeMemoryClientStub) ReadReplayDecision(ctx context.Context, decisionID string) (moduletrade.ReplayDecisionReadResponse, error) {
	scope := s.capture(ctx, "ReadReplayDecision", decisionID)
	record := runtimeTradeMemoryReplayRecord(decisionID)
	return moduletrade.ReplayDecisionReadResponse{
		ContractVersion: moduletrade.PrivateContractVersion, ServiceStatus: "ready", CorrelationID: scope.RequestID,
		ExecutionMode: "DISABLED", LearningMode: "OFFLINE_AVAILABLE", MemorySource: moduletrade.MemoryOwnerSource,
		Record: record, OwnerEvidence: runtimeTradeMemoryEvidence(scope, "replay", "replay_decision"),
	}, nil
}

func (s *runtimeTradeMemoryClientStub) RecordReplayDecision(ctx context.Context, runID, instrumentID, tradeDate, action string) (moduletrade.ReplayDecisionWriteResponse, error) {
	scope := s.capture(ctx, "RecordReplayDecision", runID, instrumentID, tradeDate, action)
	record := runtimeTradeMemoryReplayRecord("decision-written")
	record.RunID, record.InstrumentID, record.TradeDate, record.Action = runID, instrumentID, tradeDate, action
	return moduletrade.ReplayDecisionWriteResponse{
		ContractVersion: moduletrade.PrivateContractVersion, ServiceStatus: "ready", CorrelationID: scope.RequestID,
		ExecutionMode: "DISABLED", LearningMode: "OFFLINE_AVAILABLE", MemorySource: moduletrade.MemoryOwnerSource,
		Record: record, OwnerReceipt: runtimeTradeMemoryReceipt(scope, "replay", "record_replay_decision", record.DecisionID, true),
	}, nil
}

func (s *runtimeTradeMemoryClientStub) ReadPortfolioSnapshot(ctx context.Context) (moduletrade.PortfolioSnapshotReadResponse, error) {
	scope := s.capture(ctx, "ReadPortfolioSnapshot")
	if s.portfolioReadError != nil {
		return moduletrade.PortfolioSnapshotReadResponse{}, s.portfolioReadError
	}
	if s.portfolioResponse != nil {
		return s.portfolioResponse(scope), nil
	}
	return runtimeTradeMemoryPortfolioReadResponse(scope), nil
}

func (s *runtimeTradeMemoryClientStub) EnsurePortfolioInitialized(ctx context.Context) (moduletrade.PortfolioSnapshotWriteResponse, error) {
	scope := s.capture(ctx, "EnsurePortfolioInitialized")
	snapshot := runtimeTradeMemoryPortfolioSnapshot()
	return moduletrade.PortfolioSnapshotWriteResponse{
		ContractVersion: moduletrade.PrivateContractVersion, ServiceStatus: "ready", CorrelationID: scope.RequestID,
		ExecutionMode: "DISABLED", LearningMode: "OFFLINE_AVAILABLE", MemorySource: moduletrade.MemoryOwnerSource,
		Snapshot: snapshot, OwnerReceipt: runtimeTradeMemoryReceipt(scope, "portfolio", "ensure_initialized", snapshot.LatestEventHash, true),
	}, nil
}

func TestTradeMemoryAdaptersRegisterAllRoutesAndPreserveOwnerScope(t *testing.T) {
	client := &runtimeTradeMemoryClientStub{}
	recall := newRuntimeDataRecallRegistry()
	write := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataRecallTradeMemory(recall, client); err != nil {
		t.Fatalf("register recall: %v", err)
	}
	if err := registerRuntimeDataWriteTradeMemory(write, client); err != nil {
		t.Fatalf("register write: %v", err)
	}
	wantRecall := []runtimeDataRecallRoute{
		{Store: runtimeTradeMemoryStore, Operation: "learning_candidate", Access: dataRecallAccessInternal},
		{Store: runtimeTradeMemoryStore, Operation: "market_snapshot", Access: dataRecallAccessInternal},
		{Store: runtimeTradeMemoryStore, Operation: "portfolio_snapshot", Access: dataRecallAccessInternal},
		{Store: runtimeTradeMemoryStore, Operation: "replay_decision", Access: dataRecallAccessInternal},
		{Store: runtimeTradeMemoryStore, Operation: "source_record", Access: dataRecallAccessInternal},
	}
	if got := recall.Snapshot(); !reflect.DeepEqual(got, wantRecall) {
		t.Fatalf("recall routes = %#v, want %#v", got, wantRecall)
	}
	wantWrite := []runtimeDataWriteRoute{
		{Store: runtimeTradeMemoryStore, Operation: "collect_source", Access: dataRecallAccessInternal, RequiredPayloadFields: []string{"source_definition_id"}},
		{Store: runtimeTradeMemoryStore, Operation: "ensure_portfolio_initialized", Access: dataRecallAccessInternal},
		{Store: runtimeTradeMemoryStore, Operation: "import_learning_candidate", Access: dataRecallAccessInternal, RequiredPayloadFields: []string{"candidate_definition_id"}},
		{Store: runtimeTradeMemoryStore, Operation: "import_market_snapshot", Access: dataRecallAccessInternal, RequiredPayloadFields: []string{"instrument_id", "run_id", "trade_date"}},
		{Store: runtimeTradeMemoryStore, Operation: "record_replay_decision", Access: dataRecallAccessInternal, RequiredPayloadFields: []string{"action", "instrument_id", "run_id", "trade_date"}},
	}
	if got := write.Snapshot(); !reflect.DeepEqual(got, wantWrite) {
		t.Fatalf("write routes = %#v, want %#v", got, wantWrite)
	}

	ctx := runtimeTradeMemoryTestContext(t, "parent-trade-memory", "shiro", "worker", "ops", "user-42")
	readCases := []struct {
		operation string
		query     string
		method    string
		purpose   string
		args      []string
	}{
		{"source_record", "source-1", "ReadSourceRecord", runtimeTradeMemorySourceReadPurpose, []string{"source-1"}},
		{"learning_candidate", "candidate-1", "ReadLearningCandidate", runtimeTradeMemoryLearningReadPurpose, []string{"candidate-1"}},
		{"market_snapshot", "snapshot-1", "ReadMarketSnapshot", runtimeTradeMemoryMarketReadPurpose, []string{"snapshot-1"}},
		{"replay_decision", "decision-1", "ReadReplayDecision", runtimeTradeMemoryReplayReadPurpose, []string{"decision-1"}},
		{"portfolio_snapshot", "current", "ReadPortfolioSnapshot", runtimeTradeMemoryPortfolioReadPurpose, nil},
	}
	for _, test := range readCases {
		value, err := recall.Recall(ctx, tools.DataRecallRequest{Store: runtimeTradeMemoryStore, Operation: test.operation, Query: test.query, Limit: 1})
		if err != nil {
			t.Fatalf("recall %s error=%v", test.operation, err)
		}
		result, ok := value.(runtimeDataRecallResult)
		if !ok || len(result.Records) != 1 {
			t.Fatalf("recall %s result=%#v", test.operation, value)
		}
		if _, ok := result.Records[0]["owner_evidence"].(moduletrade.OwnerEvidence); !ok {
			t.Fatalf("recall %s owner evidence missing or untyped: %#v", test.operation, result.Records[0])
		}
		encoded, marshalErr := json.Marshal(result.Records[0])
		if marshalErr != nil || strings.Contains(string(encoded), "raw_artifact_ref") || strings.Contains(string(encoded), "/secret") || strings.Contains(string(encoded), "filesystem") {
			t.Fatalf("recall %s leaked unsafe projection: %s err=%v", test.operation, encoded, marshalErr)
		}
		call := client.Calls[len(client.Calls)-1]
		if call.Method != test.method || !reflect.DeepEqual(call.Args, test.args) || call.Scope.ActorID != "shiro" || call.Scope.AgentRole != "worker" || call.Scope.Purpose != test.purpose || call.Scope.AuthenticatedUserID != "user-42" || !call.Scope.Allows(domaintool.DataScopeInternal) {
			t.Fatalf("recall %s child scope/call = %#v, want method=%s purpose=%s", test.operation, call, test.method, test.purpose)
		}
		if len(call.Scope.RequestID) > 128 || !strings.HasPrefix(call.Scope.RequestID, "trade-memory-") {
			t.Fatalf("recall %s child request ID = %q", test.operation, call.Scope.RequestID)
		}
	}
	firstChild := client.Calls[0].Scope.RequestID
	if _, err := recall.Recall(ctx, tools.DataRecallRequest{Store: runtimeTradeMemoryStore, Operation: "source_record", Query: "source-1", Limit: 1}); err != nil {
		t.Fatalf("repeat source recall: %v", err)
	}
	if got := client.Calls[len(client.Calls)-1].Scope.RequestID; got != firstChild {
		t.Fatalf("same parent/route/query child ID = %q, want %q", got, firstChild)
	}
	if client.Calls[0].Scope.RequestID == client.Calls[1].Scope.RequestID {
		t.Fatalf("different routes unexpectedly share child ID: %#v", client.Calls[:2])
	}

	writeCases := []struct {
		operation string
		payload   map[string]any
		method    string
		purpose   string
		args      []string
		replay    bool
	}{
		{"collect_source", map[string]any{"source_definition_id": "source-def"}, "CollectSource", runtimeTradeMemorySourceWritePurpose, []string{"source-def"}, false},
		{"import_learning_candidate", map[string]any{"candidate_definition_id": "candidate-def"}, "ImportLearningCandidate", runtimeTradeMemoryLearningWritePurpose, []string{"candidate-def"}, false},
		{"import_market_snapshot", map[string]any{"run_id": "run-1", "instrument_id": "instrument-1", "trade_date": "2026-08-13"}, "ImportMarketSnapshot", runtimeTradeMemoryMarketWritePurpose, []string{"run-1", "instrument-1", "2026-08-13"}, false},
		{"record_replay_decision", map[string]any{"run_id": "run-1", "instrument_id": "instrument-1", "trade_date": "2026-08-13", "action": "select"}, "RecordReplayDecision", runtimeTradeMemoryReplayWritePurpose, []string{"run-1", "instrument-1", "2026-08-13", "select"}, true},
		{"ensure_portfolio_initialized", map[string]any{}, "EnsurePortfolioInitialized", runtimeTradeMemoryPortfolioWritePurpose, nil, true},
	}
	for _, test := range writeCases {
		value, err := write.Write(ctx, tools.DataWriteRequest{Store: runtimeTradeMemoryStore, Operation: test.operation, Payload: test.payload})
		if err != nil {
			t.Fatalf("write %s error: %v", test.operation, err)
		}
		receipt, ok := value.(runtimeDataWriteReceipt)
		if !ok || receipt.SchemaVersion != "7" || receipt.MigrationState != "embedded_current" || receipt.ValidationState != "owner_validated" || receipt.AuditRef == "" || receipt.IdempotencyKey == "" || receipt.PolicyRevision != "policy-test" {
			t.Fatalf("write %s receipt=%#v", test.operation, value)
		}
		call := client.Calls[len(client.Calls)-1]
		if call.Method != test.method || !reflect.DeepEqual(call.Args, test.args) || !reflect.DeepEqual(call.Scope.AllowedDataScopes, []string{domaintool.DataScopeUser, domaintool.DataScopeInternal}) || call.Scope.Purpose != test.purpose || call.Scope.AuthenticatedUserID != "user-42" {
			t.Fatalf("write %s child scope/call=%#v", test.operation, call)
		}
		if receipt.IdempotencyKey != call.Scope.RequestID || receipt.IdempotentReplay != test.replay {
			t.Fatalf("write %s receipt did not preserve owner receipt identity/replay: receipt=%#v child=%#v", test.operation, receipt, call.Scope)
		}
	}
}

func TestTradeMemoryAdaptersRejectInvalidIdentityQueriesAndPayloads(t *testing.T) {
	client := &runtimeTradeMemoryClientStub{}
	recall := newRuntimeDataRecallRegistry()
	write := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataRecallTradeMemory(recall, client); err != nil {
		t.Fatal(err)
	}
	if err := registerRuntimeDataWriteTradeMemory(write, client); err != nil {
		t.Fatal(err)
	}
	invalidActor := runtimeTradeMemoryTestContext(t, "invalid-actor", "mio", "worker", "ops", "")
	if _, err := recall.Recall(invalidActor, tools.DataRecallRequest{Store: runtimeTradeMemoryStore, Operation: "source_record", Query: "source-1", Limit: 1}); err == nil {
		t.Fatal("recall accepted invalid Agent identity")
	}
	if _, err := write.Write(invalidActor, tools.DataWriteRequest{Store: runtimeTradeMemoryStore, Operation: "collect_source", Payload: map[string]any{"source_definition_id": "source-def"}}); err == nil {
		t.Fatal("write accepted invalid Agent identity")
	}
	ctx := runtimeTradeMemoryTestContext(t, "invalid-input", "shiro", "worker", "ops", "")
	if _, err := recall.Recall(ctx, tools.DataRecallRequest{Store: runtimeTradeMemoryStore, Operation: "portfolio_snapshot", Query: "latest", Limit: 1}); err == nil {
		t.Fatal("portfolio recall accepted a non-exact query")
	}
	invalidPayloads := []map[string]any{
		{},
		{"source_definition_id": "source-def", "request_id": "model-owned"},
		{"source_definition_id": nil},
		{"source_definition_id": "source-def", "path": "/secret"},
	}
	for _, payload := range invalidPayloads {
		if _, err := write.Write(ctx, tools.DataWriteRequest{Store: runtimeTradeMemoryStore, Operation: "collect_source", Payload: payload}); err == nil {
			t.Fatalf("collect_source accepted invalid payload %#v", payload)
		}
	}
	for _, payload := range []map[string]any{{"unexpected": true}, {"request_id": "model-owned"}} {
		if _, err := write.Write(ctx, tools.DataWriteRequest{Store: runtimeTradeMemoryStore, Operation: "ensure_portfolio_initialized", Payload: payload}); err == nil {
			t.Fatalf("ensure_portfolio_initialized accepted payload %#v", payload)
		}
	}
	if _, err := write.Write(ctx, tools.DataWriteRequest{Store: runtimeTradeMemoryStore, Operation: "record_replay_decision", Payload: map[string]any{"run_id": "run-1", "instrument_id": "instrument-1", "trade_date": "2026-08-13", "action": "buy"}}); err == nil {
		t.Fatal("record_replay_decision accepted invalid action")
	}

	// The common registry exposes these routes only to the executable
	// Shiro/worker/ops boundary. Heavy/Kuro has no registry runner and must not
	// be treated as an accepted callback identity.
	wrongPurpose := runtimeTradeMemoryTestContext(t, "wrong-purpose", "shiro", "worker", runtimeTradeMemorySourceReadPurpose, "user-42")
	if _, err := recall.Recall(wrongPurpose, tools.DataRecallRequest{Store: runtimeTradeMemoryStore, Operation: "source_record", Query: "source-1", Limit: 1}); err == nil {
		t.Fatal("recall accepted a non-ops worker purpose")
	}
	if _, err := write.Write(wrongPurpose, tools.DataWriteRequest{Store: runtimeTradeMemoryStore, Operation: "collect_source", Payload: map[string]any{"source_definition_id": "source-def"}}); err == nil {
		t.Fatal("write accepted a non-ops worker purpose")
	}
	kuroCtx := runtimeTradeMemoryTestContext(t, "kuro-direct", "kuro", "heavy", "ops", "user-42")
	registration := recall.registrations[runtimeDataRecallKey{store: runtimeTradeMemoryStore, operation: "source_record"}]
	if _, err := registration.callback(kuroCtx, tools.DataRecallRequest{Store: runtimeTradeMemoryStore, Operation: "source_record", Query: "source-1", Limit: 1}); err == nil {
		t.Fatal("direct Kuro callback was accepted outside the executable Shiro boundary")
	}
}

func TestTradeMemoryPortfolioRecallAcceptsExactWriteReceiptAuditRef(t *testing.T) {
	client := &runtimeTradeMemoryClientStub{}
	recall := newRuntimeDataRecallRegistry()
	write := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataRecallTradeMemory(recall, client); err != nil {
		t.Fatal(err)
	}
	if err := registerRuntimeDataWriteTradeMemory(write, client); err != nil {
		t.Fatal(err)
	}
	ctx := runtimeTradeMemoryTestContext(t, "portfolio-audit-ref", "shiro", "worker", "ops", "user-42")
	writeValue, err := write.Write(ctx, tools.DataWriteRequest{
		Store: runtimeTradeMemoryStore, Operation: "ensure_portfolio_initialized", Payload: map[string]any{},
	})
	if err != nil {
		t.Fatalf("portfolio initialization failed: %v", err)
	}
	receipt, ok := writeValue.(runtimeDataWriteReceipt)
	if !ok || receipt.AuditRef == "" {
		t.Fatalf("portfolio initialization receipt = %#v", writeValue)
	}
	value, err := recall.Recall(ctx, tools.DataRecallRequest{
		Store: runtimeTradeMemoryStore, Operation: "portfolio_snapshot", Query: receipt.AuditRef, Limit: 1,
	})
	if err != nil {
		t.Fatalf("exact audit_ref recall failed: %v", err)
	}
	result, ok := value.(runtimeDataRecallResult)
	if !ok || len(result.Records) != 1 {
		t.Fatalf("result = %#v", value)
	}
	if len(client.Calls) != 2 || client.Calls[0].Method != "EnsurePortfolioInitialized" || client.Calls[1].Method != "ReadPortfolioSnapshot" {
		t.Fatalf("owner calls = %#v", client.Calls)
	}
}

func TestTradeMemoryPortfolioRecallCurrentRemainsCompatible(t *testing.T) {
	client := &runtimeTradeMemoryClientStub{}
	recall := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallTradeMemory(recall, client); err != nil {
		t.Fatal(err)
	}
	ctx := runtimeTradeMemoryTestContext(t, "portfolio-current", "shiro", "worker", "ops", "user-42")
	value, err := recall.Recall(ctx, tools.DataRecallRequest{
		Store: runtimeTradeMemoryStore, Operation: "portfolio_snapshot", Query: "current", Limit: 1,
	})
	if err != nil {
		t.Fatalf("current portfolio recall failed: %v", err)
	}
	result, ok := value.(runtimeDataRecallResult)
	if !ok || len(result.Records) != 1 || len(client.Calls) != 1 || client.Calls[0].Method != "ReadPortfolioSnapshot" {
		t.Fatalf("current result/calls = %#v / %#v", value, client.Calls)
	}
}

func TestTradeMemoryPortfolioRecallRejectsAuditRefSnapshotMismatch(t *testing.T) {
	client := &runtimeTradeMemoryClientStub{
		portfolioResponse: func(scope domaintool.ToolExecutionScope) moduletrade.PortfolioSnapshotReadResponse {
			response := runtimeTradeMemoryPortfolioReadResponse(scope)
			response.Snapshot.LatestEventHash = runtimeTradeMemoryHash('9')
			return response
		},
	}
	recall := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallTradeMemory(recall, client); err != nil {
		t.Fatal(err)
	}
	ctx := runtimeTradeMemoryTestContext(t, "portfolio-snapshot-mismatch", "shiro", "worker", "ops", "user-42")
	if _, err := recall.Recall(ctx, tools.DataRecallRequest{
		Store: runtimeTradeMemoryStore, Operation: "portfolio_snapshot", Query: runtimeTradeMemoryHash('8'), Limit: 1,
	}); err == nil {
		t.Fatal("snapshot hash mismatch was accepted")
	}
}

func TestTradeMemoryPortfolioRecallRejectsAuditRefEvidenceMismatch(t *testing.T) {
	client := &runtimeTradeMemoryClientStub{
		portfolioResponse: func(scope domaintool.ToolExecutionScope) moduletrade.PortfolioSnapshotReadResponse {
			response := runtimeTradeMemoryPortfolioReadResponse(scope)
			response.OwnerEvidence.ProvenanceRef = runtimeTradeMemoryHash('9')
			return response
		},
	}
	recall := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallTradeMemory(recall, client); err != nil {
		t.Fatal(err)
	}
	ctx := runtimeTradeMemoryTestContext(t, "portfolio-evidence-mismatch", "shiro", "worker", "ops", "user-42")
	if _, err := recall.Recall(ctx, tools.DataRecallRequest{
		Store: runtimeTradeMemoryStore, Operation: "portfolio_snapshot", Query: runtimeTradeMemoryHash('8'), Limit: 1,
	}); err == nil {
		t.Fatal("evidence provenance mismatch was accepted")
	}
}

func TestTradeMemoryPortfolioRecallRejectsMalformedAuditRefBeforeOwnerCall(t *testing.T) {
	queries := []string{
		"SHA256:" + strings.Repeat("a", 64),
		"sha256:" + strings.Repeat("a", 63),
		"sha256:" + strings.Repeat("g", 64),
	}
	for _, query := range queries {
		t.Run(query, func(t *testing.T) {
			client := &runtimeTradeMemoryClientStub{}
			recall := newRuntimeDataRecallRegistry()
			if err := registerRuntimeDataRecallTradeMemory(recall, client); err != nil {
				t.Fatal(err)
			}
			ctx := runtimeTradeMemoryTestContext(t, "portfolio-invalid-query", "shiro", "worker", "ops", "user-42")
			if _, err := recall.Recall(ctx, tools.DataRecallRequest{
				Store: runtimeTradeMemoryStore, Operation: "portfolio_snapshot", Query: query, Limit: 1,
			}); err == nil {
				t.Fatalf("malformed query %q was accepted", query)
			}
			if len(client.Calls) != 0 {
				t.Fatalf("owner was called for malformed query %q: %#v", query, client.Calls)
			}
		})
	}
}

func runtimeTradeMemoryTestContext(t *testing.T, requestID, actorID, role, purpose, userID string) context.Context {
	t.Helper()
	scope := domaintool.ToolExecutionScope{
		RequestID: requestID, ActorKind: domaintool.ActorKindAgent, ActorID: actorID,
		AuthenticatedUserID: userID, AllowedDataScopes: []string{domaintool.DataScopeUser, domaintool.DataScopeInternal},
		AuthenticationSource: domaintool.AuthenticationSourceAgentOrchestrator, AgentRole: role, Purpose: purpose,
	}
	if userID == "" {
		scope.AllowedDataScopes = []string{domaintool.DataScopeInternal}
	}
	if err := scope.Validate(); err != nil {
		t.Fatalf("test scope invalid: %v", err)
	}
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}

func runtimeTradeMemorySourceReadResponse(scope domaintool.ToolExecutionScope, recordID string) moduletrade.SourceRecordReadResponse {
	return moduletrade.SourceRecordReadResponse{
		ContractVersion: moduletrade.PrivateContractVersion, ServiceStatus: "ready", CorrelationID: scope.RequestID,
		ExecutionMode: "DISABLED", LearningMode: "OFFLINE_AVAILABLE", MemorySource: moduletrade.MemoryOwnerSource,
		Record: runtimeTradeMemorySourceRecord(recordID), OwnerEvidence: runtimeTradeMemoryEvidence(scope, "source", "source_record"),
	}
}

func runtimeTradeMemorySourceRecord(recordID string) moduletrade.SourceRecord {
	return moduletrade.SourceRecord{
		RecordVersion: 1, SourceRecordID: recordID, CaptureNonce: "0123456789abcdef", SourceDefinitionID: "source-def",
		SourceDefinitionHash: runtimeTradeMemoryHash('1'), Title: "Safe source", Publisher: "Publisher", Jurisdiction: "JP",
		Category: "foundation", Language: "en", SourceURL: "https://example.com/source", FinalURL: "https://example.com/source",
		TermsReference: "terms", LicenseStatus: "review_required", Status: moduletrade.MemorySourceStatus,
		ObservedAt: "2026-08-13T01:02:03Z", PointInTimeAvailableAt: "2026-08-13T01:02:03Z", HTTPStatus: 200,
		MediaType: "text/html", ByteSize: 12, ContentHash: runtimeTradeMemoryHash('2'), Tags: []string{"safe"},
	}
}

func runtimeTradeMemoryLearningRecord(recordID string) moduletrade.LearningCandidateRecord {
	return moduletrade.LearningCandidateRecord{
		RecordVersion: 1, CandidateRecordID: recordID, CandidateDefinitionID: "candidate-def", Status: moduletrade.MemoryCandidateState,
		Title: "Safe candidate", Statement: "A bounded statement", BoundSources: []moduletrade.BoundSource{{
			SourceDefinitionID: "source-def", SourceRecordID: "source-record", ContentHash: runtimeTradeMemoryHash('2'),
			ObservedAt: "2026-08-13T01:02:03Z", Locator: "safe locator",
		}}, Applicability: []string{"review"}, Limitations: []string{"limited"},
		InvalidationConditions: []string{"updated"}, Tags: []string{"safe"}, ContentHash: runtimeTradeMemoryHash('3'),
	}
}

func runtimeTradeMemoryMarketRecord(snapshotID string) moduletrade.MarketSnapshot {
	return moduletrade.MarketSnapshot{
		SnapshotID: snapshotID, SchemaVersion: 1, InstrumentID: "instrument-1", Symbol: "1305.T", Name: "Safe instrument",
		AssetType: "equity", Venue: "TSE", Currency: "JPY", TradeDate: "2026-08-13", AvailableAt: "2026-08-13T01:02:03Z",
		Open: 100, High: 110, Low: 90, Close: 105, AdjClose: 105, Volume: 1000, SourceName: "dataset",
		RunID: "run-1", PlanID: "plan-1", PlanHash: runtimeTradeMemoryHash('4'), DatasetID: "dataset-1",
		DatasetHash: runtimeTradeMemoryHash('5'), DatasetSourceRef: "dataset-ref", CodeRevision: "revision",
		ContentHash: runtimeTradeMemoryHash('6'),
	}
}

func runtimeTradeMemoryReplayRecord(decisionID string) moduletrade.ReplayDecision {
	return moduletrade.ReplayDecision{DecisionID: decisionID, SchemaVersion: 1, SnapshotID: "snapshot-1", RunID: "run-1", InstrumentID: "instrument-1", TradeDate: "2026-08-13", Action: moduletrade.MemoryActionObserve, ContentHash: runtimeTradeMemoryHash('7')}
}

func runtimeTradeMemoryPortfolioSnapshot() moduletrade.PortfolioSnapshot {
	return moduletrade.PortfolioSnapshot{
		SchemaVersion: 1, PortfolioID: "portfolio-1", Mode: "SIMULATION", Guaranteed: false, InitialCashJPY: 1_000_000,
		CashJPY: 1_000_000, RealizedPnLJPY: 0, ValuationStatus: "complete", Positions: []moduletrade.PortfolioPosition{{
			InstrumentID: "instrument-1", Quantity: 1, CostBasisJPY: 100,
		}}, EventCount: 1, LatestEventHash: runtimeTradeMemoryHash('8'),
	}
}

func runtimeTradeMemoryPortfolioReadResponse(scope domaintool.ToolExecutionScope) moduletrade.PortfolioSnapshotReadResponse {
	snapshot := runtimeTradeMemoryPortfolioSnapshot()
	evidence := runtimeTradeMemoryEvidence(scope, "portfolio", "portfolio_snapshot")
	evidence.ProvenanceRef = snapshot.LatestEventHash
	return moduletrade.PortfolioSnapshotReadResponse{
		ContractVersion: moduletrade.PrivateContractVersion, ServiceStatus: "ready", CorrelationID: scope.RequestID,
		ExecutionMode: "DISABLED", LearningMode: "OFFLINE_AVAILABLE", MemorySource: moduletrade.MemoryOwnerSource,
		Snapshot: snapshot, OwnerEvidence: evidence,
	}
}

func runtimeTradeMemoryEvidence(scope domaintool.ToolExecutionScope, domain, operation string) moduletrade.OwnerEvidence {
	freshnessState := ""
	validationState := ""
	switch domain {
	case "source":
		freshnessState = "source_observed_at"
		validationState = "source_record_integrity_verified"
	case "learning":
		freshnessState = "learning_candidate_observed_at"
		validationState = "learning_candidate_integrity_verified"
	case "market":
		freshnessState = "market_snapshot_available_at_read"
		validationState = "market_snapshot_integrity_verified"
	case "replay":
		freshnessState = "replay_snapshot_bound_at_read"
		validationState = "replay_decision_integrity_verified"
	case "portfolio":
		freshnessState = "observed_at_read"
		validationState = "owner_route_succeeded"
	default:
		panic("unexpected trade memory evidence domain: " + domain)
	}
	return moduletrade.OwnerEvidence{
		AgentID: scope.ActorID, Role: scope.AgentRole, Purpose: scope.Purpose, DataScope: domaintool.DataScopeInternal,
		RequestID: scope.RequestID, OwnerModule: moduletrade.MemoryOwnerSource, Domain: domain, Operation: operation,
		CorrelationID: scope.RequestID, RequestTime: "2026-08-13T01:02:03Z", ProvenanceRef: "provenance-" + domain,
		RetrievedAt: "2026-08-13T01:02:03Z", FreshnessState: freshnessState, ValidationState: validationState,
		BudgetLimit: 1, ReturnedCount: 1,
	}
}

func runtimeTradeMemoryReceipt(scope domaintool.ToolExecutionScope, domain, operation, auditRef string, replay bool) moduletrade.OwnerReceipt {
	return moduletrade.OwnerReceipt{
		ReceiptID: "receipt-" + scope.RequestID, RequestID: scope.RequestID, AgentID: scope.ActorID, Role: scope.AgentRole,
		Purpose: scope.Purpose, DataScope: domaintool.DataScopeInternal, OwnerModule: moduletrade.MemoryOwnerSource,
		Domain: domain, Operation: operation, Status: "completed", IdempotentReplay: replay, SchemaVersion: 7,
		AuditRef: auditRef, PolicyRevision: "policy-test", MigrationState: "embedded_current", ValidationState: "owner_validated",
		CompletedAt: "2026-08-13T01:02:03Z", CorrelationID: scope.RequestID,
	}
}

func runtimeTradeMemoryHash(fill byte) string {
	return "sha256:" + strings.Repeat(string(fill), 64)
}
