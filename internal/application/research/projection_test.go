package research

import (
	"context"
	"encoding/json"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	taskstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	core "github.com/Nyukimin/RenCrow_CORE/modules/core"
	wg "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestResultRecorderUsesExactCASJournalAndTaskOwner(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := taskstore.NewJSONLStoreWithOptions(filepath.Join(root, "tasks"), taskstore.OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	tasks := taskmanager.New(store, taskmanager.DefaultParallelLimits())
	tid := core.NewTaskID()
	_, run, err := tasks.CreateAndStartRun(ctx, domaintask.Task{TaskID: tid, Title: "Research collect", OwnerID: "ren", Assignee: "ren", ReadOnly: true, Route: domaintask.RouteResearch, ResearchBinding: &domaintask.ResearchExecutionBinding{SchemaVersion: domaintask.ResearchExecutionBindingSchema, DataScope: "user", OwnerID: "ren", OwnerRoute: "core.research.local"}}, domaintask.SharedRoleContext{TaskID: tid, UserIntent: "Authenticated Research request"})
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := engine.NewFileStore(filepath.Join(root, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = artifacts.Close() })
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(root, "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l1.Close() })
	text := "Private source body retained only in the authorized artifact owner."
	adapter := NewAdapters(nil, &fetchStub{result: wg.FetchResponse{Status: "ok", URL: "https://example.org/source", FinalURL: "https://example.org/source", ExtractedText: text, ExtractedChars: len([]rune(text))}}, l1).WithSourcePolicy(func(context.Context, c.Candidate) (bool, bool) { return true, true })
	e := &engine.Engine{Ports: engine.Ports{
		Store: artifacts, Acquisition: adapter, Owner: adapter,
		Capabilities: []c.Capability{{ProviderID: "direct", SourceKinds: []string{"web"}, FetchText: true, Enabled: true, Ready: true, FreeAllowed: true}},
	}}
	env := c.Envelope{TaskID: string(tid), RunID: string(run.RunID), TraceID: string(core.NewTraceID()), RequestID: string(core.NewRequestID()), Actor: "ren", OwnerID: "ren", DataScope: "user", PolicyRevision: "fixture.v1"}
	recorder := &ResultRecorder{Store: artifacts, Tasks: tasks}
	if err := recorder.Preflight(ctx, env); err != nil {
		t.Fatalf("generic Research owner admission failed preflight: %v", err)
	}
	result, err := e.Run(ctx, env, c.Request{SchemaVersion: c.RequestSchema, Mode: "collect", Objective: "private research objective", SeedURLs: []string{"https://example.org/source"}})
	if err != nil || result.Status != "succeeded" {
		t.Fatalf("%+v %v", result, err)
	}
	active, err := recorder.Reconcile(ctx, env, result)
	if err != nil || active.Status != "running" || !contains(active.Missing, "task_receipt") {
		t.Fatalf("unclosed Task/Run was reported complete: %+v %v", active, err)
	}
	projected, err := recorder.RecordWithProjection(ctx, env, result)
	if err != nil {
		t.Fatal(err)
	}
	if projected.ArtifactRef == "" || projected.ResultDigest != digestBytes(mustMarshalResearchResult(t, result)) {
		t.Fatalf("recorded projection lost its authoritative CAS/digest binding: %+v", projected)
	}
	if err := ValidateResearchResultProjection(env, result, projected, projected.ArtifactRef, projected.RequiredReceiptsComplete); err != nil {
		t.Fatalf("valid owner projection rejected: %v", err)
	}
	for _, mutate := range []struct {
		name string
		edit func(*c.Envelope, *domaintask.ResearchResultProjection, *string)
	}{
		{name: "task", edit: func(bound *c.Envelope, _ *domaintask.ResearchResultProjection, _ *string) {
			bound.TaskID = string(core.NewTaskID())
		}},
		{name: "run", edit: func(bound *c.Envelope, _ *domaintask.ResearchResultProjection, _ *string) {
			bound.RunID = string(core.NewRunID())
		}},
		{name: "actor", edit: func(bound *c.Envelope, _ *domaintask.ResearchResultProjection, _ *string) {
			bound.Actor = "foreign-actor"
		}},
		{name: "scope", edit: func(bound *c.Envelope, _ *domaintask.ResearchResultProjection, _ *string) { bound.DataScope = "public" }},
		{name: "owner", edit: func(bound *c.Envelope, _ *domaintask.ResearchResultProjection, _ *string) {
			bound.OwnerID = "foreign-owner"
		}},
		{name: "digest", edit: func(_ *c.Envelope, projection *domaintask.ResearchResultProjection, _ *string) {
			projection.ResultDigest = "mutated"
		}},
		{name: "CAS reference", edit: func(_ *c.Envelope, projection *domaintask.ResearchResultProjection, artifactRef *string) {
			projection.ArtifactRef = "mutated-ref"
			*artifactRef = projected.ArtifactRef
		}},
		{name: "receipt completeness", edit: func(_ *c.Envelope, projection *domaintask.ResearchResultProjection, _ *string) {
			projection.RequiredReceiptsComplete = false
		}},
		{name: "receipt state", edit: func(_ *c.Envelope, projection *domaintask.ResearchResultProjection, _ *string) {
			projection.ReceiptState = "pending"
		}},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			badEnv, badProjection, badArtifactRef := env, projected, projected.ArtifactRef
			mutate.edit(&badEnv, &badProjection, &badArtifactRef)
			if err := ValidateResearchResultProjection(badEnv, result, badProjection, badArtifactRef, badProjection.RequiredReceiptsComplete); err == nil {
				t.Fatal("mutated Research projection metadata accepted")
			}
		})
	}
	if err := recorder.Record(ctx, env, result); err != nil {
		t.Fatalf("same projection not idempotent: %v", err)
	}
	task, err := tasks.Get(ctx, tid)
	if err != nil {
		t.Fatal(err)
	}
	p := task.ResearchResult
	if p == nil || !reflect.DeepEqual(*p, projected) || p.Status != "succeeded" || p.Counts.RecordsSaved != 1 || p.Counts.BoardPostsCreated != 0 || p.ReceiptState != "verified" || !p.RequiredReceiptsComplete || len(p.VerifiedReceiptRefs) != 1 {
		t.Fatalf("incorrect facts: %+v", p)
	}
	if strings.Contains(p.ArtifactRef, "example.org") || strings.Contains(task.Summary, text) {
		t.Fatal("private content leaked into Task facts")
	}
	stillActive, err := recorder.Reconcile(ctx, env, result)
	if err != nil || stillActive.Status != "running" {
		t.Fatalf("projection before Task close was reported terminal: %+v %v", stillActive, err)
	}
	if err := tasks.CloseStartedRun(ctx, tid, run.RunID, "ren", taskmanager.RunOutcomeOf(nil, "Research completed"), nil); err != nil {
		t.Fatal(err)
	}
	closed, err := recorder.Reconcile(ctx, env, result)
	if err != nil || closed.Status != "succeeded" || contains(closed.Missing, "task_receipt") {
		t.Fatalf("verified Task/Run closure was not reconciled: %+v %v", closed, err)
	}
	publicTaskID := core.NewTaskID()
	publicTask, publicRun, err := tasks.CreateAndStartRun(ctx, domaintask.Task{TaskID: publicTaskID, Title: "Delegated public work", OwnerID: "delegator", Assignee: "ren", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{TaskID: publicTaskID, UserIntent: "public Research status"})
	if err != nil {
		t.Fatal(err)
	}
	publicEnv := c.Envelope{TaskID: string(publicTask.TaskID), RunID: string(publicRun.RunID), TraceID: string(core.NewTraceID()), RequestID: string(core.NewRequestID()), Actor: "ren", DataScope: "public", PolicyRevision: "fixture.v1"}
	publicResult, err := e.Run(ctx, publicEnv, c.Request{SchemaVersion: c.RequestSchema, Mode: "collect", Objective: "public Research status", SeedURLs: []string{"https://example.org/source"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder.Record(ctx, publicEnv, publicResult); err != nil {
		t.Fatal(err)
	}
	if err := tasks.CloseStartedRun(ctx, publicTask.TaskID, publicRun.RunID, "ren", taskmanager.RunOutcomeOf(nil, "public Research completed"), nil); err != nil {
		t.Fatal(err)
	}
	publicClosed, err := recorder.Reconcile(ctx, publicEnv, publicResult)
	if err != nil || publicClosed.Status != "succeeded" || contains(publicClosed.Missing, "task_receipt") {
		t.Fatalf("public Research on an ordinary delegated Task did not reconcile: %+v %v", publicClosed, err)
	}
	userTaskID := core.NewTaskID()
	userTask := domaintask.Task{TaskID: userTaskID, Title: "Mio CHAT Research", OwnerID: "mio", Assignee: "mio", Route: domaintask.RouteCHAT}
	userTask.OriginSessionID = core.NewSessionID()
	userAddress, err := conversation.NewChannelAddress("viewer", "cmd:ren")
	if err != nil {
		t.Fatal(err)
	}
	userBinding, err := conversation.NewSessionBindingWithPublicationIncarnation(
		userTask.OriginSessionID, userAddress, conversation.SessionAudiencePrivate, "ren", 1, "0123456789abcdef0123456789abcdef",
	)
	if err != nil {
		t.Fatal(err)
	}
	userCreateContext := conversation.WithSessionBinding(ctx, userBinding)
	createdUserTask, userRun, err := tasks.CreateAndStartRun(userCreateContext, userTask, domaintask.SharedRoleContext{TaskID: userTaskID, UserIntent: "Authenticated Mio CHAT Research"})
	if err != nil {
		t.Fatal(err)
	}
	userEnv := c.Envelope{TaskID: string(createdUserTask.TaskID), RunID: string(userRun.RunID), TraceID: string(core.NewTraceID()), RequestID: string(core.NewRequestID()), Actor: "mio", OwnerID: "ren", DataScope: "user", PolicyRevision: "fixture.v1"}
	publicForgery := userEnv
	publicForgery.OwnerID = ""
	publicForgery.DataScope = "public"
	if err := recorder.Preflight(ctx, publicForgery); err == nil {
		t.Fatal("private-origin Mio Task passed the public Research preflight")
	}
	userResult, err := e.Run(ctx, userEnv, c.Request{SchemaVersion: c.RequestSchema, Mode: "collect", Objective: "authenticated Mio CHAT research", SeedURLs: []string{"https://example.org/source"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder.Record(ctx, userEnv, userResult); err != nil {
		t.Fatalf("Record ordinary authenticated user Task: %v", err)
	}
	if err := tasks.CloseStartedRun(ctx, createdUserTask.TaskID, userRun.RunID, "mio", taskmanager.RunOutcomeOf(nil, "Research completed"), nil); err != nil {
		t.Fatal(err)
	}
	userClosed, err := recorder.Reconcile(ctx, userEnv, userResult)
	if err != nil || userClosed.Status != "succeeded" || contains(userClosed.Missing, "task_receipt") {
		t.Fatalf("canonical private-origin user Research did not reconcile: %+v %v", userClosed, err)
	}
	storedUserTask, err := tasks.Get(ctx, createdUserTask.TaskID)
	if err != nil || storedUserTask.OwnerID != "mio" || storedUserTask.Assignee != "mio" || storedUserTask.ResearchResult == nil || storedUserTask.ResearchResult.OwnerID != "ren" {
		t.Fatalf("Research data owner changed Task execution identity: task=%+v err=%v", storedUserTask, err)
	}
	tampered := result
	tampered.Counts.BoardPostsCreated = 99
	if err := recorder.Record(ctx, env, tampered); err == nil {
		t.Fatal("modified result accepted without exact journal proof")
	}
}

func mustMarshalResearchResult(t *testing.T, result c.Result) []byte {
	t.Helper()
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestResearchProjectionPreservesLocalEvidenceCounter(t *testing.T) {
	result := c.Result{RunID: "research-local", Status: "succeeded", Counts: c.Counts{DocumentsFetched: 2, LocalDocumentsReused: 1}}
	projection := researchProjection(c.Envelope{}, result, "digest", "ref")
	encoded, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	var decoded domaintask.ResearchResultProjection
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Counts.LocalDocumentsReused != 1 || decoded.Counts.DocumentsFetched != 2 || !strings.Contains(decoded.TaskSummary(), "external_documents_acquired=1") {
		t.Fatalf("Task projection lost local/external acquisition separation: %s", encoded)
	}
}

func TestResearchProjectionPreservesNewLocalMaterialsInExternalAcquisitionCount(t *testing.T) {
	result := c.Result{RunID: "research-local-materials", Status: "succeeded", Counts: c.Counts{DocumentsFetched: 3, LocalMaterialsAcquired: 2}}
	projection := researchProjection(c.Envelope{}, result, "digest", "ref")
	if projection.Counts.DocumentsFetched != 3 || projection.Counts.LocalDocumentsReused != 0 || projection.Counts.LocalMaterialsAcquired != 2 || !strings.Contains(projection.TaskSummary(), "external_documents_acquired=1") {
		t.Fatalf("local owner-read materials were counted as external acquisition: %+v (%s)", projection.Counts, projection.TaskSummary())
	}
}

func TestResultRecorderPreflightRejectsTypedPrivateTaskBeforeEngineRun(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := taskstore.NewJSONLStoreWithOptions(filepath.Join(root, "tasks"), taskstore.OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tasks := taskmanager.New(store, taskmanager.DefaultParallelLimits())
	timing := core.TaskTiming{AllowedWindows: []core.TaskTimeWindow{}}
	timingDigest, err := domaintask.TaskTimingSHA256(timing)
	if err != nil {
		t.Fatal(err)
	}
	kindInput := json.RawMessage(`{"information":"","kind":"movie","limit":1,"name":"The Film"}`)
	inputDigest, err := domaintask.TaskCenterKindInputSHA256(kindInput)
	if err != nil {
		t.Fatal(err)
	}
	goalID, workstreamID := core.NewGoalID(), core.NewWorkstreamID()
	draft := domaintask.Task{
		Title: "typed private task", Route: domaintask.RouteGeneral, Assignee: "mio", Priority: domaintask.PriorityNormal,
		InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked, ReadOnly: true, GoalID: goalID, WorkstreamID: workstreamID,
		PrivateAccess: &domaintask.TaskPrivateAccess{Scope: domaintask.TaskAccessScopeUser, AuthenticatedUserID: "user-1"},
		Timing:        &timing,
		TaskCenterExecution: &domaintask.TaskCenterExecution{
			KindID: taskmanager.TaskCenterMovieCatalogLookupKind, KindInputVersion: 1, KindInputSHA256: inputDigest,
			SourceGoalID: goalID, PreparedRevision: 1, AssignmentEpoch: 0, TimingSHA256: timingDigest,
		},
	}
	admit := taskmanager.TaskCenterExecutionAdmissionCheck(func(context.Context, domaintask.Task, domaintask.SharedRoleContext, json.RawMessage) error {
		return nil
	})
	readReceipt := domaintask.TaskCenterExecutionReceiptReadAuthorizationCheck(func(context.Context, domaintask.TaskCenterExecutionReceiptReadInput) error { return nil })
	task, run, err := tasks.EnsureTaskCenterExecution(ctx, core.NewRequestID(), 1, draft, domaintask.SharedRoleContext{UserIntent: "typed private research"}, kindInput, admit, readReceipt)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &ResultRecorder{Tasks: tasks}
	env := c.Envelope{TaskID: string(task.TaskID), RunID: string(run.RunID), Actor: "mio", OwnerID: "user-1", DataScope: "user", PolicyRevision: "fixture.v1"}
	if err := recorder.Preflight(ctx, env); err == nil {
		t.Fatal("typed private Task was admitted to Research before Engine side effects")
	}
}

func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}
