package toolloop

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	actionstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	contract "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
)

type nonToolCallingResearchProvider struct{}

func (nonToolCallingResearchProvider) Generate(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
	return llm.GenerateResponse{}, nil
}
func (nonToolCallingResearchProvider) Name() string { return "non-tool-calling" }

type researchRunOnlyRunner struct {
	result   *tool.ToolResponse
	calls    int
	args     map[string]any
	boundIDs []domainexecution.BoundActionAttempt
}

func (r *researchRunOnlyRunner) ListTools(context.Context) ([]tool.ToolMetadata, error) {
	return webResearchMetadata(researchRunTool), nil
}
func (r *researchRunOnlyRunner) ExecuteV2(ctx context.Context, name string, args map[string]any) (*tool.ToolResponse, error) {
	if name != researchRunTool {
		return nil, context.Canceled
	}
	r.calls++
	r.args = args
	if actionID, attemptID, ok := domainexecution.BoundActionAttemptFromContext(ctx); ok {
		r.boundIDs = append(r.boundIDs, domainexecution.BoundActionAttempt{ActionID: actionID, AttemptID: attemptID})
	}
	return r.result, nil
}

func researchResultForTool(status string) contract.Result {
	missing := []string{}
	if status != "succeeded" {
		missing = append(missing, "manual_seed")
	}
	return contract.Result{
		SchemaVersion: contract.ResultSchema, RunID: "run_123", Status: status,
		Stages: []contract.Stage{}, Missing: missing, Errors: []contract.Fault{},
		Artifacts: []contract.ArtifactRef{}, Receipts: []contract.Receipt{},
	}
}

func TestWebResearchUsesRegisteredResearchRunWithoutToolCalling(t *testing.T) {
	result := researchResultForTool("succeeded")
	runner := &researchRunOnlyRunner{result: tool.NewSuccess(result)}
	objective := "この文字列はRecallにあるべきでない"
	userRequest := "公式情報を調査して mode=search_only https://example.org/source。"
	evidence, err := NewWebResearcher(runner, nil).Gather(webResearchContext(t), nonToolCallingResearchProvider{}, llm.GenerateRequest{
		Messages: []llm.Message{
			{Role: "user", Type: llm.PromptContextRecall, Content: objective},
			{Role: "user", Type: llm.PromptContextUser, Content: userRequest},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if runner.calls != 1 {
		t.Fatalf("research.run calls = %d, want exactly one", runner.calls)
	}
	request, ok := runner.args["request"].(map[string]any)
	if !ok {
		t.Fatalf("request argument has type %T", runner.args["request"])
	}
	if request["objective"] != userRequest || request["mode"] != "search_only" || request["cost_profile"] != "zero_incremental_cost" {
		t.Fatalf("unexpected bounded request: %#v", request)
	}
	if _, exists := runner.args["actor"]; exists {
		t.Fatalf("authority fields were passed as Tool arguments: %#v", runner.args)
	}
	seeds, ok := request["seed_urls"].([]any)
	if !ok || len(seeds) != 1 || seeds[0] != "https://example.org/source" {
		t.Fatalf("explicit safe URL was not used as a seed: %#v", request["seed_urls"])
	}
	if len(evidence) != 1 || evidence[0].Content != mustJSON(t, result) {
		t.Fatalf("model evidence must be only the typed Engine result: %#v", evidence)
	}
}

func TestWebResearchEngineRequiresExactPrivateGrant(t *testing.T) {
	runner := &researchRunOnlyRunner{result: tool.NewSuccess(researchResultForTool("succeeded"))}
	_, err := NewWebResearcher(runner, nil).Gather(webResearchContext(t), nonToolCallingResearchProvider{}, llm.GenerateRequest{
		Messages: []llm.Message{{Role: "user", Type: llm.PromptContextUser, Content: "調査して"}},
	}, []string{"web_gather.search"})
	if err == nil || runner.calls != 0 {
		t.Fatalf("ungranted research.run executed: calls=%d err=%v", runner.calls, err)
	}
}

func TestWebResearchEngineKeepsPartialResultStructuredAndDoesNotFallback(t *testing.T) {
	result := researchResultForTool("partial")
	runner := &researchRunOnlyRunner{result: tool.NewError(tool.ErrInternalError, "Research run completed with missing required outputs", map[string]any{"research_result": result})}
	evidence, err := NewWebResearcher(runner, nil).Gather(webResearchContext(t), nonToolCallingResearchProvider{}, llm.GenerateRequest{
		Messages: []llm.Message{{Role: "user", Type: llm.PromptContextUser, Content: "調査して"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if runner.calls != 1 || len(evidence) != 1 || evidence[0].Content != mustJSON(t, result) || evidence[0].Metadata["research_status"] != "partial" {
		t.Fatalf("partial result was lost or treated as completed success: calls=%d evidence=%#v", runner.calls, evidence)
	}
}

func TestWebResearchEngineBoundsObjectiveAndDefaultsMode(t *testing.T) {
	runner := &researchRunOnlyRunner{result: tool.NewSuccess(researchResultForTool("succeeded"))}
	objective := strings.Repeat("調", 2005)
	if _, err := NewWebResearcher(runner, nil).Gather(webResearchContext(t), nonToolCallingResearchProvider{}, llm.GenerateRequest{
		Messages: []llm.Message{{Role: "user", Type: llm.PromptContextUser, Content: objective}},
	}, nil); err == nil || runner.calls != 0 {
		t.Fatal("overlong objective was silently truncated or executed")
	}
}

func TestWebResearchEnginePreservesExplicitStructuredConditions(t *testing.T) {
	runner := &researchRunOnlyRunner{result: tool.NewSuccess(researchResultForTool("succeeded"))}
	raw := `{"schema_version":"rencrow.research.request.v1","mode":"collect","objective":"Explicit source and save requirements","source_kinds":["web","x"],"seed_urls":["https://example.org/source"],"outputs":{"save":[{"target":"artifact","required":true}]},"limits":{"max_documents":2},"cost_profile":"zero_incremental_cost"}`
	_, err := NewWebResearcher(runner, nil).Gather(webResearchContext(t), nonToolCallingResearchProvider{}, llm.GenerateRequest{Messages: []llm.Message{{Role: "user", Type: llm.PromptContextUser, Content: raw}}}, nil)
	if err != nil || runner.calls != 1 {
		t.Fatalf("calls=%d err=%v", runner.calls, err)
	}
	request := runner.args["request"].(map[string]any)
	kinds := request["source_kinds"].([]any)
	if len(kinds) != 2 || kinds[1] != "x" || request["mode"] != "collect" || request["outputs"].(map[string]any)["save"].([]any)[0].(map[string]any)["target"] != "artifact" {
		t.Fatalf("explicit contract was discarded: %+v", request)
	}
}

func TestWebResearchEngineDoesNotDiscardNaturalRequiredConditions(t *testing.T) {
	for _, objective := range []string{"直近7日のXリサーチをBOARDへ登録して", "今朝のYouTube情報を調査して", "調査結果をDBへ保存して"} {
		runner := &researchRunOnlyRunner{result: tool.NewSuccess(researchResultForTool("succeeded"))}
		_, err := NewWebResearcher(runner, nil).Gather(webResearchContext(t), nonToolCallingResearchProvider{}, llm.GenerateRequest{Messages: []llm.Message{{Role: "user", Type: llm.PromptContextUser, Content: objective}}}, nil)
		if err == nil || runner.calls != 0 {
			t.Fatalf("required conditions were silently weakened: %q", objective)
		}
	}
}

func TestWebResearchEngineReusesActionAttemptLifecycle(t *testing.T) {
	taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()
	ctx, err := domainexecution.WithIdentity(webResearchContext(t), taskID, runID, modulecore.NewTraceID())
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(".", ".research-actions-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	store, err := actionstore.NewJSONLStore(filepath.Join(root, "actions"))
	if err != nil {
		t.Fatal(err)
	}
	actions := actionmanager.New(store)
	runner := &researchRunOnlyRunner{result: tool.NewSuccess(researchResultForTool("succeeded"))}
	if _, err := NewWebResearcher(runner, actions).Gather(ctx, nonToolCallingResearchProvider{}, llm.GenerateRequest{
		Messages: []llm.Message{{Role: "user", Type: llm.PromptContextUser, Content: "調査して"}},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if len(runner.boundIDs) != 1 || runner.calls != 1 {
		t.Fatalf("Tool Action binding was not reused: calls=%d bindings=%v", runner.calls, runner.boundIDs)
	}
	actionsFound, err := actions.ListActions(ctx, domainaction.Filter{TaskID: taskID, RunID: runID})
	if err != nil || len(actionsFound) != 1 {
		t.Fatalf("actions=%d err=%v, want one completed research.run action", len(actionsFound), err)
	}
	if actionsFound[0].Name != researchRunTool || actionsFound[0].TaskID != taskID || actionsFound[0].RunID != runID {
		t.Fatalf("action binding owner/name mismatch: %+v", actionsFound[0])
	}
	if actionsFound[0].Status != domainaction.StatusSucceeded {
		t.Fatalf("Action was not completed as succeeded: %+v", actionsFound[0])
	}
	attempts, err := actions.ListAttempts(ctx, domainaction.AttemptFilter{ActionID: actionsFound[0].ActionID})
	if err != nil || len(attempts) != 1 || attempts[0].Status != domainaction.AttemptStatusSucceeded {
		t.Fatalf("attempt completion mismatch: attempts=%+v err=%v", attempts, err)
	}
}

func TestWebResearchPartialEngineResultFailsBoundAction(t *testing.T) {
	taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()
	ctx, err := domainexecution.WithIdentity(webResearchContext(t), taskID, runID, modulecore.NewTraceID())
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(".", ".research-actions-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	store, err := actionstore.NewJSONLStore(filepath.Join(root, "actions"))
	if err != nil {
		t.Fatal(err)
	}
	actions := actionmanager.New(store)
	runner := &researchRunOnlyRunner{result: tool.NewSuccess(researchResultForTool("partial"))}
	evidence, err := NewWebResearcher(runner, actions).Gather(ctx, nonToolCallingResearchProvider{}, llm.GenerateRequest{
		Messages: []llm.Message{{Role: "user", Type: llm.PromptContextUser, Content: "調査して"}},
	}, nil)
	if err != nil || len(evidence) != 1 || evidence[0].Metadata["research_status"] != "partial" {
		t.Fatalf("partial result was not preserved as structured evidence: evidence=%+v err=%v", evidence, err)
	}
	actionsFound, err := actions.ListActions(ctx, domainaction.Filter{TaskID: taskID, RunID: runID})
	if err != nil || len(actionsFound) != 1 || actionsFound[0].Status != domainaction.StatusFailed {
		t.Fatalf("partial Engine result was recorded as success: actions=%+v err=%v", actionsFound, err)
	}
	attempts, err := actions.ListAttempts(ctx, domainaction.AttemptFilter{ActionID: actionsFound[0].ActionID})
	if err != nil || len(attempts) != 1 || attempts[0].Status != domainaction.AttemptStatusFailed {
		t.Fatalf("partial Engine result did not fail its attempt: attempts=%+v err=%v", attempts, err)
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
