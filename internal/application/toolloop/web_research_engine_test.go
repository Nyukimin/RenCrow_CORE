package toolloop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	coreresearch "github.com/Nyukimin/RenCrow_CORE/internal/application/research"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	actionstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	taskstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	contract "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	engine "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
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
	raw := `{"schema_version":"rencrow.research.request.v1","mode":"collect","objective":"Explicit source and save requirements","source_kinds":["web","x"],"seed_urls":["https://example.org/source"],"outputs":{"save":[{"target":"artifact","required":true}]},"limits":{"max_documents":2},"cost_profile":"zero_incremental_cost"}`
	request, err := researchRequestFromUser(raw)
	if err != nil {
		t.Fatal(err)
	}
	owner := newResearchOwnerProjectionFixture(t, request)
	runner := &researchRunOnlyRunner{result: owner.response}
	evidence, err := NewWebResearcher(runner, nil).Gather(owner.ctx, nonToolCallingResearchProvider{}, llm.GenerateRequest{Messages: []llm.Message{{Role: "user", Type: llm.PromptContextUser, Content: raw}}}, nil)
	if err != nil || runner.calls != 1 {
		t.Fatalf("calls=%d err=%v", runner.calls, err)
	}
	if len(evidence) != 1 || evidence[0].Metadata["research_task_summary"] != owner.projection.TaskSummary() {
		t.Fatalf("completion summary did not come from the verified Task owner: evidence=%+v owner=%+v", evidence, owner.projection)
	}
	requestArgs := runner.args["request"].(map[string]any)
	kinds := requestArgs["source_kinds"].([]any)
	if len(kinds) != 2 || kinds[1] != "x" || requestArgs["mode"] != "collect" || requestArgs["outputs"].(map[string]any)["save"].([]any)[0].(map[string]any)["target"] != "artifact" {
		t.Fatalf("explicit contract was discarded: %+v", requestArgs)
	}
}

func TestWebResearchEnginePreservesRFC1149SourceSaveAndEvidenceRequest(t *testing.T) {
	raw := `{"schema_version":"rencrow.research.request.v1","mode":"collect","objective":"RFC1149原文を取得し、knowledge_stagingへ保存する","source_kinds":["web"],"seed_urls":["https://www.rfc-editor.org/rfc/rfc1149"],"outputs":{"report":{"required":true,"format":"markdown"},"save":[{"target":"knowledge_staging","required":true}],"evidence":{"min_documents":1,"min_independent_sources":1,"require_primary_for_material_claims":true}},"limits":{"search_calls":1,"candidates":3,"fetch_attempts":2,"max_documents":1,"deadline_seconds":30},"cost_profile":"zero_incremental_cost"}`
	request, err := researchRequestFromUser(raw)
	if err != nil {
		t.Fatal(err)
	}
	owner := newResearchOwnerProjectionFixture(t, request)
	runner := &researchRunOnlyRunner{result: owner.response}
	evidenceItems, err := NewWebResearcher(runner, nil).Gather(owner.ctx, nonToolCallingResearchProvider{}, llm.GenerateRequest{
		Messages: []llm.Message{{Role: "user", Type: llm.PromptContextUser, Content: raw}},
	}, nil)
	if err != nil || runner.calls != 1 {
		t.Fatalf("calls=%d err=%v", runner.calls, err)
	}
	if len(evidenceItems) != 1 || evidenceItems[0].Metadata["research_task_summary"] != owner.projection.TaskSummary() {
		t.Fatalf("completion summary did not come from the verified Task owner: evidence=%+v owner=%+v", evidenceItems, owner.projection)
	}
	requestArgs := runner.args["request"].(map[string]any)
	if requestArgs["mode"] != "collect" || requestArgs["objective"] != "RFC1149原文を取得し、knowledge_stagingへ保存する" {
		t.Fatalf("explicit Research mode/objective was not preserved: %+v", requestArgs)
	}
	if got := requestArgs["seed_urls"].([]any); len(got) != 1 || got[0] != "https://www.rfc-editor.org/rfc/rfc1149" {
		t.Fatalf("RFC1149 seed was not preserved: %+v", requestArgs["seed_urls"])
	}
	outputs := requestArgs["outputs"].(map[string]any)
	if save := outputs["save"].([]any); len(save) != 1 || save[0].(map[string]any)["target"] != "knowledge_staging" || save[0].(map[string]any)["required"] != true {
		t.Fatalf("required knowledge_staging output was not preserved: %+v", outputs["save"])
	}
	evidence := outputs["evidence"].(map[string]any)
	if evidence["min_documents"] != float64(1) || evidence["min_independent_sources"] != float64(1) || evidence["require_primary_for_material_claims"] != true {
		t.Fatalf("evidence conditions were not preserved: %+v", evidence)
	}
}

func TestRecordedResearchProjectionRejectsChangedOwnerMetadata(t *testing.T) {
	request, err := researchRequestFromUser(`{"schema_version":"rencrow.research.request.v1","mode":"collect","objective":"Search the official source","seed_urls":["https://example.org/source"]}`)
	if err != nil {
		t.Fatal(err)
	}
	owner := newResearchOwnerProjectionFixture(t, request)
	if _, err := recordedResearchProjection(owner.ctx, owner.response, owner.result); err != nil {
		t.Fatalf("actual Task-owner projection was rejected: %v", err)
	}
	for _, test := range []struct {
		name string
		edit func(*tool.ToolResponse)
	}{
		{name: "Task", edit: func(response *tool.ToolResponse) {
			projection := response.Metadata["research_result_projection"].(domaintask.ResearchResultProjection)
			projection.TaskID = modulecore.NewTaskID()
			response.Metadata["research_result_projection"] = projection
		}},
		{name: "Run", edit: func(response *tool.ToolResponse) {
			projection := response.Metadata["research_result_projection"].(domaintask.ResearchResultProjection)
			projection.ExecutionRunID = modulecore.NewRunID()
			response.Metadata["research_result_projection"] = projection
		}},
		{name: "Actor", edit: func(response *tool.ToolResponse) {
			projection := response.Metadata["research_result_projection"].(domaintask.ResearchResultProjection)
			projection.ActorID = "other-agent"
			response.Metadata["research_result_projection"] = projection
		}},
		{name: "scope", edit: func(response *tool.ToolResponse) {
			projection := response.Metadata["research_result_projection"].(domaintask.ResearchResultProjection)
			projection.DataScope = tool.DataScopeUser
			response.Metadata["research_result_projection"] = projection
		}},
		{name: "owner", edit: func(response *tool.ToolResponse) {
			projection := response.Metadata["research_result_projection"].(domaintask.ResearchResultProjection)
			projection.OwnerID = "other-owner"
			response.Metadata["research_result_projection"] = projection
		}},
		{name: "digest", edit: func(response *tool.ToolResponse) {
			projection := response.Metadata["research_result_projection"].(domaintask.ResearchResultProjection)
			projection.ResultDigest = "mutated-digest"
			response.Metadata["research_result_projection"] = projection
		}},
		{name: "required receipt completeness", edit: func(response *tool.ToolResponse) {
			projection := response.Metadata["research_result_projection"].(domaintask.ResearchResultProjection)
			projection.RequiredReceiptsComplete = false
			response.Metadata["research_result_projection"] = projection
		}},
		{name: "CAS reference", edit: func(response *tool.ToolResponse) {
			projection := response.Metadata["research_result_projection"].(domaintask.ResearchResultProjection)
			projection.ArtifactRef = "mutated-ref"
			response.Metadata["research_result_projection"] = projection
		}},
		{name: "CAS reference companion", edit: func(response *tool.ToolResponse) {
			response.Metadata["research_result_artifact_ref"] = "mutated-ref"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := cloneResearchToolResponse(owner.response)
			test.edit(response)
			if _, err := recordedResearchProjection(owner.ctx, response, owner.result); err == nil {
				t.Fatal("changed owner projection metadata accepted")
			}
		})
	}
	for _, test := range []struct {
		name string
		ctx  func() context.Context
	}{
		{name: "trusted Task identity", ctx: func() context.Context {
			ctx, err := domainexecution.WithIdentity(context.Background(), modulecore.NewTaskID(), modulecore.RunID(owner.result.ExecutionRunID), modulecore.NewTraceID())
			if err != nil {
				t.Fatal(err)
			}
			scope, ok := tool.ToolExecutionScopeFromContext(owner.ctx)
			if !ok {
				t.Fatal("owner fixture lost its trusted tool scope")
			}
			return tool.WithToolExecutionScope(ctx, scope)
		}},
		{name: "trusted Run identity", ctx: func() context.Context {
			ctx, err := domainexecution.WithIdentity(context.Background(), modulecore.TaskID(owner.result.TaskID), modulecore.NewRunID(), modulecore.NewTraceID())
			if err != nil {
				t.Fatal(err)
			}
			scope, ok := tool.ToolExecutionScopeFromContext(owner.ctx)
			if !ok {
				t.Fatal("owner fixture lost its trusted tool scope")
			}
			return tool.WithToolExecutionScope(ctx, scope)
		}},
		{name: "trusted actor", ctx: func() context.Context {
			scope, err := tool.NewToolExecutionScope("research-test", tool.ActorKindAgent, "other-agent", "", []string{tool.DataScopePublic}, tool.AuthenticationSourceAgentOrchestrator)
			if err != nil {
				t.Fatal(err)
			}
			return tool.WithToolExecutionScope(owner.ctx, scope)
		}},
		{name: "trusted scope", ctx: func() context.Context {
			scope, err := tool.NewToolExecutionScope("research-test", tool.ActorKindAgent, "midori", "other-owner", []string{tool.DataScopeUser}, tool.AuthenticationSourceAgentOrchestrator)
			if err != nil {
				t.Fatal(err)
			}
			return tool.WithToolExecutionScope(owner.ctx, scope)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := recordedResearchProjection(test.ctx(), owner.response, owner.result); err == nil {
				t.Fatal("changed trusted execution binding accepted")
			}
		})
	}
}

func cloneResearchToolResponse(response *tool.ToolResponse) *tool.ToolResponse {
	clone := *response
	clone.Metadata = make(map[string]any, len(response.Metadata))
	for key, value := range response.Metadata {
		clone.Metadata[key] = value
	}
	return &clone
}

type recordedResearchOwnerFixture struct {
	ctx        context.Context
	result     contract.Result
	response   *tool.ToolResponse
	projection domaintask.ResearchResultProjection
}

func newResearchOwnerProjectionFixture(t *testing.T, request contract.Request) recordedResearchOwnerFixture {
	t.Helper()
	root := t.TempDir()
	taskLog, err := taskstore.NewJSONLStoreWithOptions(filepath.Join(root, "tasks"), taskstore.OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = taskLog.Close() })
	tasks := taskmanager.New(taskLog, taskmanager.DefaultParallelLimits())
	const actor = "midori"
	taskID := modulecore.NewTaskID()
	task, run, err := tasks.CreateAndStartRun(context.Background(), domaintask.Task{
		TaskID: taskID, Title: "Typed Research projection fixture", OwnerID: actor, Assignee: actor,
		ReadOnly: true, Route: domaintask.RouteResearch,
	}, domaintask.SharedRoleContext{TaskID: taskID, UserIntent: request.Objective})
	if err != nil {
		t.Fatal(err)
	}
	traceID := modulecore.NewTraceID()
	ctx, err := domainexecution.WithIdentity(context.Background(), task.TaskID, run.RunID, traceID)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := tool.NewToolExecutionScope("research-test", tool.ActorKindAgent, actor, "", []string{tool.DataScopePublic}, tool.AuthenticationSourceAgentOrchestrator)
	if err != nil {
		t.Fatal(err)
	}
	ctx = tool.WithToolExecutionScope(ctx, scope)
	env := contract.Envelope{
		TaskID: string(task.TaskID), RunID: string(run.RunID), TraceID: string(traceID),
		RequestID: scope.RequestID, Actor: scope.ActorID, DataScope: tool.DataScopePublic,
	}
	store, err := engine.NewFileStore(filepath.Join(root, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	owner := &coreresearch.ResultRecorder{Store: store, Tasks: tasks}
	if err := owner.Preflight(ctx, env); err != nil {
		t.Fatalf("canonical Task/Run fixture failed owner preflight: %v", err)
	}
	requestJSON, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	requestDigest := sha256.Sum256(requestJSON)
	requestHash := hex.EncodeToString(requestDigest[:])
	now := time.Now().UTC()
	result := contract.Result{
		SchemaVersion: contract.ResultSchema, RunID: string(run.RunID), TaskID: string(task.TaskID), ExecutionRunID: string(run.RunID),
		Status: "succeeded", Counts: contract.Counts{Candidates: len(request.SeedURLs), FetchAttempts: len(request.SeedURLs), DocumentsFetched: len(request.SeedURLs)},
		Stages: []contract.Stage{}, Missing: []string{}, Errors: []contract.Fault{}, Artifacts: []contract.ArtifactRef{}, Receipts: []contract.Receipt{},
	}
	sourceURL := "https://example.org/source"
	if len(request.SeedURLs) > 0 {
		sourceURL = request.SeedURLs[0]
	}
	documentText := "owner fixture source"
	documentDigest := sha256.Sum256([]byte(documentText))
	documentRef, err := store.Put(ctx, []byte(documentText))
	if err != nil {
		t.Fatal(err)
	}
	document := contract.Document{
		ID: "document-fixture", SourceKind: "web", ProviderID: "direct", SourceRef: sourceURL,
		OriginalURL: sourceURL, FinalURL: sourceURL, Title: "Source",
		RetrievedAt: now, ContentKind: "html", ContentScope: tool.DataScopePublic, ContentState: "complete",
		ExtractedHash: hex.EncodeToString(documentDigest[:]), ArtifactRef: documentRef.Ref, Text: documentText,
		RetentionAllowed: true, Complete: true, Primary: true, IndependentSource: sourceURL,
	}
	collection := contract.Collection{
		SchemaVersion: "rencrow.research.collection.v1", Envelope: env, InputDigest: requestHash,
		CreatedAt: now, Documents: []contract.Document{}, Errors: []contract.Fault{},
	}
	if len(request.SeedURLs) > 0 {
		collection.Documents = []contract.Document{document}
	}
	needKnowledgeStaging := false
	for _, target := range request.Outputs.Save {
		if target.Target == "knowledge_staging" {
			needKnowledgeStaging = true
		}
	}
	if needKnowledgeStaging {
		result.Counts.RecordsSaved = 1
	}
	collectionRef, err := store.Put(ctx, []byte(mustJSON(t, collection)))
	if err != nil {
		t.Fatal(err)
	}
	result.Artifacts = append(result.Artifacts, contract.ArtifactRef{Kind: "collection", Hash: collectionRef.Hash, Ref: collectionRef.Ref})
	stored := collection
	storedCollection := &stored
	if needKnowledgeStaging {
		result.Receipts = append(result.Receipts, contract.Receipt{
			Owner: actor, OwnerRoute: "knowledge_staging", AuditRef: "audit:research-fixture",
			CanonicalID: "staging:research-fixture", Digest: document.ExtractedHash,
			State: "committed", Scope: env.DataScope, CommittedAt: now, VerifiedAt: now,
		})
	}
	if request.Outputs.Report.Required {
		result.Report = "Owner produced a bounded fixture report."
		reportRef, err := store.Put(ctx, []byte(result.Report))
		if err != nil {
			t.Fatal(err)
		}
		result.Artifacts = append(result.Artifacts, contract.ArtifactRef{Kind: "report", Hash: reportRef.Hash, Ref: reportRef.Ref})
	}
	if err := store.Save(ctx, contract.RunRecord{Envelope: env, Request: request, RequestHash: requestHash, Result: result, Collection: storedCollection}); err != nil {
		t.Fatal(err)
	}
	projection, err := owner.RecordWithProjection(ctx, env, result)
	if err != nil {
		t.Fatalf("real Task owner rejected the Research run fixture: %v", err)
	}
	persisted, err := tasks.Get(ctx, task.TaskID)
	if err != nil || persisted.ResearchResult == nil || !reflect.DeepEqual(*persisted.ResearchResult, projection) {
		t.Fatalf("owner Task projection differs from its returned value: projection=%+v persisted=%+v err=%v", projection, persisted.ResearchResult, err)
	}
	response := tool.NewSuccess(result)
	response.Metadata = map[string]any{
		"research_result_projection":   projection,
		"research_result_artifact_ref": projection.ArtifactRef,
	}
	return recordedResearchOwnerFixture{ctx: ctx, result: result, response: response, projection: projection}
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
