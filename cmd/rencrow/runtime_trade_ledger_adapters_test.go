package main

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	tradeshadowobservation "github.com/Nyukimin/RenCrow_CORE/internal/application/tradeshadowobservation"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

type runtimeTradeLedgerClientCall struct {
	Method string
	Args   []string
	Scope  domaintool.ToolExecutionScope
}

type runtimeTradeLedgerClientStub struct {
	Calls            []runtimeTradeLedgerClientCall
	Replay           moduletrade.ReplayDecision
	Market           moduletrade.MarketSnapshot
	Report           moduletrade.ShadowOutcomeReport
	ReportStudy      string
	ReportProvenance string
}

func (stub *runtimeTradeLedgerClientStub) capture(ctx context.Context, method string, args ...string) domaintool.ToolExecutionScope {
	scope, _ := domaintool.ToolExecutionScopeFromContext(ctx)
	stub.Calls = append(stub.Calls, runtimeTradeLedgerClientCall{Method: method, Args: append([]string(nil), args...), Scope: scope})
	return scope
}

func (stub *runtimeTradeLedgerClientStub) ReadReplayDecision(ctx context.Context, decisionID string) (moduletrade.ReplayDecisionReadResponse, error) {
	scope := stub.capture(ctx, "ReadReplayDecision", decisionID)
	record := stub.Replay
	if record.DecisionID == "" {
		record = runtimeTradeMemoryReplayRecord(decisionID)
	}
	return moduletrade.ReplayDecisionReadResponse{
		ContractVersion: moduletrade.PrivateContractVersion, ServiceStatus: "ready", CorrelationID: scope.RequestID,
		ExecutionMode: "DISABLED", LearningMode: "OFFLINE_AVAILABLE", MemorySource: moduletrade.MemoryOwnerSource,
		Record: record, OwnerEvidence: runtimeTradeMemoryEvidence(scope, "replay", "replay_decision"),
	}, nil
}

func (stub *runtimeTradeLedgerClientStub) ReadMarketSnapshot(ctx context.Context, snapshotID string) (moduletrade.MarketSnapshotReadResponse, error) {
	scope := stub.capture(ctx, "ReadMarketSnapshot", snapshotID)
	record := stub.Market
	if record.SnapshotID == "" {
		record = runtimeTradeMemoryMarketRecord(snapshotID)
	}
	return moduletrade.MarketSnapshotReadResponse{
		ContractVersion: moduletrade.PrivateContractVersion, ServiceStatus: "ready", CorrelationID: scope.RequestID,
		ExecutionMode: "DISABLED", LearningMode: "OFFLINE_AVAILABLE", MemorySource: moduletrade.MemoryOwnerSource,
		Record: record, OwnerEvidence: runtimeTradeMemoryEvidence(scope, "market", "market_snapshot"),
	}, nil
}

func (stub *runtimeTradeLedgerClientStub) ShadowOutcomeReport(ctx context.Context, correlationID, studyID string) (moduletrade.PrivateShadowOutcomeReport, error) {
	scope := stub.capture(ctx, "ShadowOutcomeReport", correlationID, studyID)
	report := stub.Report
	if report.StudyID == "" {
		report = runtimeTradeLedgerOutcomeReport(stub.ReportStudy)
		if report.StudyID == "" {
			report = runtimeTradeLedgerOutcomeReport(studyID)
		}
	}
	evidence := runtimeTradeLedgerEvidence(scope, "shadow_outcome_report")
	if stub.ReportProvenance != "" {
		evidence.ProvenanceRef = stub.ReportProvenance
	}
	return moduletrade.PrivateShadowOutcomeReport{
		ContractVersion: moduletrade.PrivateContractVersion, ServiceStatus: "ready", CorrelationID: correlationID,
		ExecutionMode: "DISABLED", LearningMode: "OFFLINE_AVAILABLE", Environment: "SHADOW",
		Report: report, OwnerEvidence: evidence,
	}, nil
}

type runtimeTradeLedgerRecorderStub struct {
	Calls     []tradeshadowobservation.Request
	Scopes    []domaintool.ToolExecutionScope
	Replay    bool
	Missing   bool
	Unsafe    bool
	BadPolicy bool
}

func (stub *runtimeTradeLedgerRecorderStub) Record(ctx context.Context, request tradeshadowobservation.Request) (tradeshadowobservation.Result, error) {
	scope, _ := domaintool.ToolExecutionScopeFromContext(ctx)
	stub.Calls = append(stub.Calls, request)
	stub.Scopes = append(stub.Scopes, scope)
	if stub.Missing {
		return tradeshadowobservation.Result{}, nil
	}
	record := runtimeTradeLedgerObservationRecord(scope, request, stub.Replay)
	if stub.Unsafe {
		record.AuthorizesExternalExecution = true
	}
	result := tradeshadowobservation.Result{PolicyDecision: moduletrade.PolicyDecision{Capability: tradeshadowobservation.Capability, Status: "allowed"}, Record: &record}
	if stub.BadPolicy {
		result.PolicyDecision.Status = "blocked"
	}
	return result, nil
}

func TestTradeLedgerAdaptersRegisterRoutesAndBindOwnerData(t *testing.T) {
	client := &runtimeTradeLedgerClientStub{Replay: func() moduletrade.ReplayDecision {
		decision := runtimeTradeMemoryReplayRecord("decision-1")
		decision.Action = moduletrade.MemoryActionSelect
		return decision
	}()}
	recorder := &runtimeTradeLedgerRecorderStub{Replay: true}
	recall := newRuntimeDataRecallRegistry()
	write := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataRecallTradeLedger(recall, client); err != nil {
		t.Fatalf("register ledger recall: %v", err)
	}
	if err := registerRuntimeDataWriteTradeLedger(write, recorder, client); err != nil {
		t.Fatalf("register ledger write: %v", err)
	}
	if got, want := recall.Snapshot(), []runtimeDataRecallRoute{{Store: runtimeTradeMemoryStore, Operation: runtimeTradeLedgerOutcomeReportOperation, Access: dataRecallAccessInternal}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("recall routes=%#v want=%#v", got, want)
	}
	if got, want := write.Snapshot(), []runtimeDataWriteRoute{{Store: runtimeTradeMemoryStore, Operation: runtimeTradeLedgerObservationOperation, Access: dataRecallAccessInternal, RequiredPayloadFields: []string{"decision_id", "study_id"}}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("write routes=%#v want=%#v", got, want)
	}

	ctx := runtimeTradeMemoryTestContext(t, "parent-ledger-memory", "shiro", "worker", "ops", "user-42")
	value, err := recall.Recall(ctx, tools.DataRecallRequest{Store: runtimeTradeMemoryStore, Operation: runtimeTradeLedgerOutcomeReportOperation, Query: "study-1", Limit: 1})
	if err != nil {
		t.Fatalf("ledger report recall: %v", err)
	}
	result, ok := value.(runtimeDataRecallResult)
	if !ok || len(result.Records) != 1 {
		t.Fatalf("ledger report result=%#v", value)
	}
	if _, ok := result.Records[0]["owner_evidence"].(moduletrade.OwnerEvidence); !ok {
		t.Fatalf("ledger report owner evidence missing: %#v", result.Records[0])
	}
	encoded, marshalErr := json.Marshal(result.Records[0])
	if marshalErr != nil || strings.Contains(string(encoded), "raw_artifact_ref") || strings.Contains(string(encoded), "/secret") || strings.Contains(string(encoded), "filesystem") || strings.Contains(string(encoded), "SELECT ") {
		t.Fatalf("ledger report leaked unsafe projection: %s err=%v", encoded, marshalErr)
	}
	if len(client.Calls) != 1 || client.Calls[0].Method != "ShadowOutcomeReport" || !reflect.DeepEqual(client.Calls[0].Args[1:], []string{"study-1"}) || client.Calls[0].Scope.ActorID != "shiro" || client.Calls[0].Scope.AgentRole != "worker" || client.Calls[0].Scope.Purpose != runtimeTradeLedgerReadPurpose || client.Calls[0].Scope.AuthenticatedUserID != "user-42" || !client.Calls[0].Scope.Allows(domaintool.DataScopeInternal) {
		t.Fatalf("ledger report owner scope/call=%#v", client.Calls)
	}
	reportChild := client.Calls[0].Scope.RequestID

	writeValue, err := write.Write(ctx, tools.DataWriteRequest{Store: runtimeTradeMemoryStore, Operation: runtimeTradeLedgerObservationOperation, Payload: map[string]any{"study_id": "study-1", "decision_id": "decision-1"}})
	if err != nil {
		t.Fatalf("ledger observation write: %v", err)
	}
	receipt, ok := writeValue.(runtimeDataWriteReceipt)
	if !ok || receipt.SchemaVersion != "7" || receipt.MigrationState != "embedded_current" || receipt.ValidationState != "owner_validated" || receipt.AuditRef == "" || receipt.PolicyRevision != "policy-test" || receipt.IdempotencyKey == "" || !receipt.IdempotentReplay {
		t.Fatalf("ledger write receipt=%#v", writeValue)
	}
	if len(recorder.Calls) != 1 || len(recorder.Scopes) != 1 {
		t.Fatalf("recorder calls=%#v scopes=%#v", recorder.Calls, recorder.Scopes)
	}
	observation := recorder.Calls[0]
	writeScope := recorder.Scopes[0]
	if observation.RequestID != writeScope.RequestID || observation.TraceID != "parent-ledger-memory" || observation.Requester != "shiro" || !observation.RequestAllowed {
		t.Fatalf("recorder trusted request=%#v scope=%#v", observation, writeScope)
	}
	if writeScope.ActorID != "shiro" || writeScope.AgentRole != "worker" || writeScope.Purpose != runtimeTradeLedgerWritePurpose || writeScope.AuthenticatedUserID != "user-42" || !writeScope.Allows(domaintool.DataScopeInternal) {
		t.Fatalf("recorder child scope=%#v", writeScope)
	}
	expectedObservation := moduletrade.ShadowObservationInput{
		IdempotencyKey: writeScope.RequestID, StudyID: "study-1", DecisionID: "decision-1", ActorID: "shiro", InstrumentID: "instrument-1",
		DecisionKind: "select", MarketObservedAt: "2026-08-13T01:02:03Z", ContextSnapshotSHA256: strings.Repeat("7", 64),
		OutcomeLabelContractSHA256: runtimeTradeLedgerOutcomeLabelContract,
		ReasonCodes:                []string{"REPLAY_DECISION_OWNER_VERIFIED", "MARKET_SNAPSHOT_OWNER_VERIFIED"},
		EvidenceRefs:               []string{"decision-1", "snapshot-1"},
	}
	if !reflect.DeepEqual(observation.Observation, expectedObservation) || receipt.IdempotencyKey != writeScope.RequestID {
		t.Fatalf("derived owner observation=%#v want=%#v receipt=%#v", observation.Observation, expectedObservation, receipt)
	}
	if len(client.Calls) != 3 || client.Calls[1].Method != "ReadReplayDecision" || !reflect.DeepEqual(client.Calls[1].Args, []string{"decision-1"}) || client.Calls[2].Method != "ReadMarketSnapshot" || !reflect.DeepEqual(client.Calls[2].Args, []string{"snapshot-1"}) {
		t.Fatalf("owner verification calls=%#v", client.Calls)
	}
	if client.Calls[0].Args[0] != client.Calls[0].Scope.RequestID {
		t.Fatalf("outcome report correlation=%q scope request=%q", client.Calls[0].Args[0], client.Calls[0].Scope.RequestID)
	}
	if client.Calls[1].Scope.Purpose != runtimeTradeMemoryReplayReadPurpose || client.Calls[2].Scope.Purpose != runtimeTradeMemoryMarketReadPurpose {
		t.Fatalf("owner verification purposes=%#v", client.Calls[1:])
	}
	if reportChild == client.Calls[1].Scope.RequestID || client.Calls[1].Scope.RequestID == client.Calls[2].Scope.RequestID || client.Calls[2].Scope.RequestID == writeScope.RequestID {
		t.Fatalf("distinct ledger child IDs collapsed: report=%q replay=%q market=%q write=%q", reportChild, client.Calls[1].Scope.RequestID, client.Calls[2].Scope.RequestID, writeScope.RequestID)
	}
	if len(writeScope.RequestID) > 128 || !strings.HasPrefix(writeScope.RequestID, "trade-memory-") {
		t.Fatalf("write child request ID=%q", writeScope.RequestID)
	}

	if _, err := recall.Recall(ctx, tools.DataRecallRequest{Store: runtimeTradeMemoryStore, Operation: runtimeTradeLedgerOutcomeReportOperation, Query: "study-1", Limit: 1}); err != nil {
		t.Fatalf("repeat ledger report recall: %v", err)
	}
	if got := client.Calls[len(client.Calls)-1].Scope.RequestID; got != reportChild {
		t.Fatalf("same report request child ID=%q want=%q", got, reportChild)
	}
	repeatValue, err := write.Write(ctx, tools.DataWriteRequest{Store: runtimeTradeMemoryStore, Operation: runtimeTradeLedgerObservationOperation, Payload: map[string]any{"study_id": "study-1", "decision_id": "decision-1"}})
	if err != nil {
		t.Fatalf("repeat ledger observation write: %v", err)
	}
	repeatReceipt, ok := repeatValue.(runtimeDataWriteReceipt)
	if !ok || !repeatReceipt.IdempotentReplay || repeatReceipt.IdempotencyKey != writeScope.RequestID || repeatReceipt.AuditRef != receipt.AuditRef {
		t.Fatalf("repeat ledger receipt=%#v", repeatValue)
	}
	if got := recorder.Scopes[len(recorder.Scopes)-1].RequestID; got != writeScope.RequestID {
		t.Fatalf("same observation request child ID=%q want=%q", got, writeScope.RequestID)
	}

	// Heavy/Kuro has no common registry runner. Verify the actual executable
	// boundary rejects a worker scope whose purpose is not the fixed ops route.
	wrongPurpose := runtimeTradeMemoryTestContext(t, "wrong-purpose-ledger", "shiro", "worker", runtimeTradeLedgerWritePurpose, "user-42")
	if _, err := write.Write(wrongPurpose, tools.DataWriteRequest{Store: runtimeTradeMemoryStore, Operation: runtimeTradeLedgerObservationOperation, Payload: map[string]any{"study_id": "study-1", "decision_id": "decision-1"}}); err == nil {
		t.Fatal("ledger write accepted a non-ops worker purpose")
	}
}

func TestTradeLedgerRecallAcceptsEventAuditRefAndBindsOwnerResponse(t *testing.T) {
	eventID := "shadow-event/sha256:" + strings.Repeat("e", 64)
	client := &runtimeTradeLedgerClientStub{ReportStudy: "study-1", ReportProvenance: eventID}
	recall := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallTradeLedger(recall, client); err != nil {
		t.Fatal(err)
	}
	ctx := runtimeTradeMemoryTestContext(t, "event-ledger-memory", "shiro", "worker", "ops", "user-42")
	value, err := recall.Recall(ctx, tools.DataRecallRequest{Store: runtimeTradeMemoryStore, Operation: runtimeTradeLedgerOutcomeReportOperation, Query: eventID, Limit: 1})
	if err != nil {
		t.Fatalf("event ledger report recall: %v", err)
	}
	result, ok := value.(runtimeDataRecallResult)
	if !ok || len(result.Records) != 1 || len(client.Calls) != 1 {
		t.Fatalf("result/calls=%#v/%#v", value, client.Calls)
	}
	if client.Calls[0].Method != "ShadowOutcomeReport" || !reflect.DeepEqual(client.Calls[0].Args[1:], []string{eventID}) || client.Calls[0].Scope.Purpose != runtimeTradeLedgerReadPurpose {
		t.Fatalf("event owner call=%#v", client.Calls[0])
	}
}

func TestTradeLedgerRecallRejectsEventResponseBindingMismatch(t *testing.T) {
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
			client := &runtimeTradeLedgerClientStub{ReportStudy: test.studyID, ReportProvenance: test.provenance}
			recall := newRuntimeDataRecallRegistry()
			if err := registerRuntimeDataRecallTradeLedger(recall, client); err != nil {
				t.Fatal(err)
			}
			ctx := runtimeTradeMemoryTestContext(t, "event-binding-ledger", "shiro", "worker", "ops", "user-42")
			if _, err := recall.Recall(ctx, tools.DataRecallRequest{Store: runtimeTradeMemoryStore, Operation: runtimeTradeLedgerOutcomeReportOperation, Query: eventID, Limit: 1}); err == nil {
				t.Fatal("event response binding mismatch was accepted")
			}
		})
	}
}

func TestTradeLedgerAdaptersMapOwnerActionsAndRejectSpoofing(t *testing.T) {
	for _, test := range []struct {
		action string
		kind   string
	}{
		{moduletrade.MemoryActionSelect, "select"},
		{moduletrade.MemoryActionAvoid, "exclude"},
		{moduletrade.MemoryActionObserve, "abstain"},
	} {
		client := &runtimeTradeLedgerClientStub{Replay: func() moduletrade.ReplayDecision {
			decision := runtimeTradeMemoryReplayRecord("decision-1")
			decision.Action = test.action
			return decision
		}()}
		recorder := &runtimeTradeLedgerRecorderStub{}
		write := newRuntimeDataWriteRegistry()
		if err := registerRuntimeDataWriteTradeLedger(write, recorder, client); err != nil {
			t.Fatal(err)
		}
		ctx := runtimeTradeMemoryTestContext(t, "action-"+test.action, "shiro", "worker", "ops", "user-42")
		if _, err := write.Write(ctx, tools.DataWriteRequest{Store: runtimeTradeMemoryStore, Operation: runtimeTradeLedgerObservationOperation, Payload: map[string]any{"study_id": "study-1", "decision_id": "decision-1"}}); err != nil {
			t.Fatalf("action %s write: %v", test.action, err)
		}
		if got := recorder.Calls[0].Observation.DecisionKind; got != test.kind {
			t.Fatalf("action %s mapped kind=%q want=%q", test.action, got, test.kind)
		}
	}

	client := &runtimeTradeLedgerClientStub{}
	recorder := &runtimeTradeLedgerRecorderStub{}
	write := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteTradeLedger(write, recorder, client); err != nil {
		t.Fatal(err)
	}
	ctx := runtimeTradeMemoryTestContext(t, "spoof-ledger", "shiro", "worker", "ops", "user-42")
	spoofedPayloads := []map[string]any{
		{"study_id": "study-1"},
		{"decision_id": "decision-1", "study_id": "study-1", "actor_id": "kuro"},
		{"decision_id": "decision-1", "study_id": "study-1", "request_id": "model-request"},
		{"decision_id": "decision-1", "study_id": "study-1", "purpose": "ledger_memory_write"},
		{"decision_id": "decision-1", "study_id": "study-1", "data_scope": "internal"},
		{"decision_id": "decision-1", "study_id": "study-1", "action": "avoid"},
		{"decision_id": "decision-1", "study_id": "study-1", "instrument_id": "spoofed"},
		{"decision_id": "decision-1", "study_id": "study-1", "market_observed_at": "2026-08-13T01:02:03Z"},
		{"decision_id": "decision-1", "study_id": "study-1", "policy": map[string]any{"allowed": true}},
		{"decision_id": "decision-1", "study_id": "study-1", "path": "/secret"},
	}
	for _, payload := range spoofedPayloads {
		if _, err := write.Write(ctx, tools.DataWriteRequest{Store: runtimeTradeMemoryStore, Operation: runtimeTradeLedgerObservationOperation, Payload: payload}); err == nil {
			t.Fatalf("spoofed payload accepted: %#v", payload)
		}
	}
	for _, payload := range []map[string]any{
		{"decision_id": "decision-1", "study_id": nil},
		{"decision_id": "decision/1", "study_id": "study-1"},
		{"decision_id": "decision-1", "study_id": "study/1"},
	} {
		if _, err := write.Write(ctx, tools.DataWriteRequest{Store: runtimeTradeMemoryStore, Operation: runtimeTradeLedgerObservationOperation, Payload: payload}); err == nil {
			t.Fatalf("invalid safe ID payload accepted: %#v", payload)
		}
	}
	if len(recorder.Calls) != 0 {
		t.Fatalf("invalid payloads reached recorder: %#v", recorder.Calls)
	}
	if _, err := write.Write(ctx, tools.DataWriteRequest{Store: runtimeTradeMemoryStore, Operation: runtimeTradeLedgerObservationOperation, Payload: map[string]any{"decision_id": "decision-1", "study_id": "study-1", "unknown": true}}); err == nil {
		t.Fatal("extra payload field accepted")
	}
}

func TestTradeLedgerAdaptersRejectOwnerCrossBindingAndUnsafeResults(t *testing.T) {
	ctx := runtimeTradeMemoryTestContext(t, "cross-binding", "shiro", "worker", "ops", "user-42")
	for _, mutate := range []func(*moduletrade.MarketSnapshot){
		func(snapshot *moduletrade.MarketSnapshot) { snapshot.SnapshotID = "other-snapshot" },
		func(snapshot *moduletrade.MarketSnapshot) { snapshot.RunID = "other-run" },
		func(snapshot *moduletrade.MarketSnapshot) { snapshot.InstrumentID = "other-instrument" },
		func(snapshot *moduletrade.MarketSnapshot) { snapshot.TradeDate = "2026-08-14" },
	} {
		client := &runtimeTradeLedgerClientStub{Market: runtimeTradeMemoryMarketRecord("snapshot-1")}
		mutate(&client.Market)
		recorder := &runtimeTradeLedgerRecorderStub{}
		write := newRuntimeDataWriteRegistry()
		if err := registerRuntimeDataWriteTradeLedger(write, recorder, client); err != nil {
			t.Fatal(err)
		}
		if _, err := write.Write(ctx, tools.DataWriteRequest{Store: runtimeTradeMemoryStore, Operation: runtimeTradeLedgerObservationOperation, Payload: map[string]any{"decision_id": "decision-1", "study_id": "study-1"}}); err == nil {
			t.Fatal("cross-bound owner data accepted")
		}
		if len(recorder.Calls) != 0 {
			t.Fatalf("cross-bound owner data reached recorder: %#v", recorder.Calls)
		}
	}

	for _, test := range []struct {
		name    string
		mutate  func(*runtimeTradeLedgerRecorderStub)
		wantErr string
	}{
		{name: "missing record", mutate: func(stub *runtimeTradeLedgerRecorderStub) { stub.Missing = true }, wantErr: "missing"},
		{name: "blocked policy", mutate: func(stub *runtimeTradeLedgerRecorderStub) { stub.BadPolicy = true }, wantErr: "successful"},
		{name: "external execution", mutate: func(stub *runtimeTradeLedgerRecorderStub) { stub.Unsafe = true }, wantErr: "successful"},
	} {
		client := &runtimeTradeLedgerClientStub{}
		recorder := &runtimeTradeLedgerRecorderStub{}
		test.mutate(recorder)
		write := newRuntimeDataWriteRegistry()
		if err := registerRuntimeDataWriteTradeLedger(write, recorder, client); err != nil {
			t.Fatal(err)
		}
		if _, err := write.Write(ctx, tools.DataWriteRequest{Store: runtimeTradeMemoryStore, Operation: runtimeTradeLedgerObservationOperation, Payload: map[string]any{"decision_id": "decision-1", "study_id": "study-1"}}); err == nil || !strings.Contains(err.Error(), "callback") {
			t.Fatalf("%s err=%v want callback failure", test.name, err)
		}
	}

	invalidReportContext := runtimeTradeMemoryTestContext(t, "invalid-report", "shiro", "worker", "ops", "user-42")
	client := &runtimeTradeLedgerClientStub{}
	recall := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallTradeLedger(recall, client); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		"study/1", "study?1", "",
		"shadow-event/sha256:" + strings.Repeat("E", 64),
		"shadow-event/sha256:" + strings.Repeat("e", 63),
		"shadow-event/sha256:" + strings.Repeat("g", 64),
		"shadow-event/not-a-hash",
	} {
		if _, err := recall.Recall(invalidReportContext, tools.DataRecallRequest{Store: runtimeTradeMemoryStore, Operation: runtimeTradeLedgerOutcomeReportOperation, Query: query, Limit: 1}); err == nil {
			t.Fatalf("invalid outcome report query accepted: %q", query)
		}
	}
	if len(client.Calls) != 0 {
		t.Fatalf("invalid outcome report query reached owner: %#v", client.Calls)
	}
}

func runtimeTradeLedgerOutcomeReport(studyID string) moduletrade.ShadowOutcomeReport {
	return moduletrade.ShadowOutcomeReport{
		SchemaVersion: 1, ContractVersion: moduletrade.ShadowOutcomeReportContractVersion, StudyID: studyID, Environment: "SHADOW",
		ObservationCount: 3, OutcomeCount: 1, PendingOutcomeCount: 2,
		LabelCounts: map[string]int64{"success": 1, "failure": 0, "neutral": 0, "inconclusive": 0},
		ReturnCount: 1, ReturnSumBPS: 100, BenchmarkReturnCount: 1, BenchmarkReturnSumBPS: 50,
		ExcessReturnCount: 1, ExcessReturnSumBPS: 50, LabelContractSHA256: []string{strings.Repeat("a", 64)},
		ReviewState: "pending_outcomes", LatestEventHash: "sha256:" + strings.Repeat("b", 64),
	}
}

func runtimeTradeLedgerEvidence(scope domaintool.ToolExecutionScope, operation string) moduletrade.OwnerEvidence {
	return moduletrade.OwnerEvidence{
		AgentID: scope.ActorID, Role: scope.AgentRole, Purpose: scope.Purpose, DataScope: domaintool.DataScopeInternal,
		RequestID: scope.RequestID, OwnerModule: moduletrade.MemoryOwnerSource, Domain: "ledger", Operation: operation,
		CorrelationID: scope.RequestID, RequestTime: "2026-08-13T01:02:03Z", ProvenanceRef: "ledger/genesis",
		RetrievedAt: "2026-08-13T01:02:03Z", FreshnessState: "observed_at_read", ValidationState: "owner_route_succeeded",
		BudgetLimit: 1, ReturnedCount: 1,
	}
}

func runtimeTradeLedgerObservationRecord(scope domaintool.ToolExecutionScope, request tradeshadowobservation.Request, replay bool) moduletrade.PrivateShadowObservation {
	eventHash := "sha256:" + strings.Repeat("c", 64)
	eventID := "shadow-event/" + eventHash
	return moduletrade.PrivateShadowObservation{
		ContractVersion: moduletrade.PrivateContractVersion, ServiceStatus: "ready", CorrelationID: request.RequestID,
		ExecutionMode: "DISABLED", LearningMode: "OFFLINE_AVAILABLE", RequestID: request.RequestID, Environment: "SHADOW",
		AuthorizesExternalExecution: false, PortfolioMutated: false, KnowledgePromoted: false, IdempotentReplay: replay,
		PolicyDecision: moduletrade.PolicyDecision{Capability: tradeshadowobservation.Capability, Status: "allowed", ModulePolicyRevision: "policy-test"},
		Event: moduletrade.ShadowObservationEvent{
			EventVersion: 1, EventID: eventID, Sequence: 1, RecordedAt: "2026-08-13T01:02:04Z", Type: "shadow_observation_recorded",
			ShadowObservationInput: request.Observation, EventHash: eventHash,
		},
		OwnerReceipt: runtimeTradeMemoryReceipt(scope, "ledger", "shadow_observation", eventID, replay),
	}
}
