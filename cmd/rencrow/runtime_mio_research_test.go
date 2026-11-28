package main

import (
	"context"
	"testing"

	domainagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	contract "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
)

func TestRuntimeMioResearchUsesSharedRunAndPreservesStructuredOriginalRequest(t *testing.T) {
	original := `{"schema_version":"rencrow.research.request.v1","mode":"collect","objective":"Collect primary sources about the 2026-11-01 release","source_kinds":["pypi"],"outputs":{"report":{"required":true,"format":"markdown"},"save":[],"evidence":{"min_documents":1,"min_independent_sources":1,"require_primary_for_material_claims":true}},"limits":{"search_calls":3,"candidates":5,"fetch_attempts":4,"max_documents":3,"deadline_seconds":120,"analysis_calls":2},"cost_profile":"zero_incremental_cost"}`
	address, err := conversation.NewChannelAddress("viewer", "cmd:ren")
	if err != nil {
		t.Fatal(err)
	}
	input, err := conversation.NewTurnInput(modulecore.NewTaskID(), original, address)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := domainexecution.WithIdentity(context.Background(), modulecore.NewTaskID(), modulecore.NewRunID(), modulecore.NewTraceID())
	if err != nil {
		t.Fatal(err)
	}
	userScope, err := tool.NewToolExecutionScope("http-request", tool.ActorKindUser, "ren", "ren", []string{tool.DataScopePublic, tool.DataScopeUser}, tool.AuthenticationSourceHTTP)
	if err != nil {
		t.Fatal(err)
	}
	ctx = tool.WithToolExecutionScope(ctx, userScope)
	ctx, err = tool.DeriveAgentToolExecutionScope(ctx, "mio-research", "mio", "chat", "chat", false)
	if err != nil {
		t.Fatal(err)
	}
	terminal := contract.Result{
		SchemaVersion: contract.ResultSchema, RunID: "run_01a21c1b-cc68-7361-808f-6e7e79d80625", Status: "succeeded",
		Report: "Verified report", Receipts: []contract.Receipt{{Digest: "receipt-digest"}},
	}
	runner := &mioWebQueryStub{response: tool.NewSuccess(terminal)}
	result, err := newRuntimeMioRetrieval(nil, nil).withWebQueries(runner).Retrieve(ctx, input, domainagent.MioRetrievalRequest{
		SourceKind: domainagent.MioRetrievalSourceResearch, Query: "research release details", References: []string{}, InformationNeeded: "release evidence",
	})
	if err != nil || result.Status != domainagent.MioRetrievalStatusFound || result.Research == nil || result.Research.Status != "succeeded" {
		t.Fatalf("Research terminal = %#v, err=%v", result, err)
	}
	if len(runner.calls) != 1 || runner.calls[0] != "research.run" {
		t.Fatalf("Research calls = %v, want one research.run", runner.calls)
	}
	request, ok := runner.args[0]["request"].(map[string]any)
	if !ok || request["objective"] != "Collect primary sources about the 2026-11-01 release" || request["mode"] != "collect" {
		t.Fatalf("Research normalizer dropped structured objective or mode: %#v", runner.args[0])
	}
	if _, ok := request["source_kinds"].([]any); !ok {
		t.Fatalf("Research normalizer dropped source kind: %#v", request)
	}
}

func TestRuntimeMioResearchRejectsComplexUnstructuredIntentBeforeRunner(t *testing.T) {
	address, err := conversation.NewChannelAddress("viewer", "cmd:ren")
	if err != nil {
		t.Fatal(err)
	}
	input, err := conversation.NewTurnInput(modulecore.NewTaskID(), "Research the latest release from 2026-11-01", address)
	if err != nil {
		t.Fatal(err)
	}
	ctx := authenticatedMioRetrievalContext(t, "ren")
	runner := &mioWebQueryStub{}
	result, err := newRuntimeMioRetrieval(nil, nil).withWebQueries(runner).Retrieve(ctx, input, domainagent.MioRetrievalRequest{
		SourceKind: domainagent.MioRetrievalSourceResearch, Query: "latest release", References: []string{}, InformationNeeded: "release date",
	})
	if err != nil || result.Status != domainagent.MioRetrievalStatusUnavailable || len(runner.calls) != 0 {
		t.Fatalf("unstructured explicit conditions were silently reduced: result=%#v calls=%v err=%v", result, runner.calls, err)
	}
}

func TestRuntimeMioResearchKeepsManualSeedAsTypedWaitingReceipt(t *testing.T) {
	address, err := conversation.NewChannelAddress("viewer", "cmd:ren")
	if err != nil {
		t.Fatal(err)
	}
	input, err := conversation.NewTurnInput(modulecore.NewTaskID(), "Research this topic", address)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := domainexecution.WithIdentity(context.Background(), modulecore.NewTaskID(), modulecore.NewRunID(), modulecore.NewTraceID())
	if err != nil {
		t.Fatal(err)
	}
	userScope, err := tool.NewToolExecutionScope("http-request", tool.ActorKindUser, "ren", "ren", []string{tool.DataScopePublic, tool.DataScopeUser}, tool.AuthenticationSourceHTTP)
	if err != nil {
		t.Fatal(err)
	}
	ctx = tool.WithToolExecutionScope(ctx, userScope)
	ctx, err = tool.DeriveAgentToolExecutionScope(ctx, "mio-research", "mio", "chat", "chat", false)
	if err != nil {
		t.Fatal(err)
	}
	runner := &mioWebQueryStub{response: tool.NewSuccess(contract.Result{
		SchemaVersion: contract.ResultSchema, RunID: "run_01a21c1b-cc68-7361-808f-6e7e79d80625", Status: "waiting_input", Missing: []string{"manual_seed"},
	})}
	result, err := newRuntimeMioRetrieval(nil, nil).withWebQueries(runner).Retrieve(ctx, input, domainagent.MioRetrievalRequest{
		SourceKind: domainagent.MioRetrievalSourceResearch, Query: "research request", References: []string{}, InformationNeeded: "await manual seed",
	})
	if err != nil || result.Research == nil || result.Research.Status != "waiting_input" || len(result.Research.Missing) != 1 || result.Research.Missing[0] != "manual_seed" {
		t.Fatalf("waiting_input was lost or rewritten: result=%#v err=%v", result, err)
	}
}
