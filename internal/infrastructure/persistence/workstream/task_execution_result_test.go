package workstream

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	tool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestTaskCenterExecutionResultArtifactIsScopedImmutableAndIdempotent(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "task-center-result.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	result := json.RawMessage(`{"kind":"movie.catalog.lookup","not_found":true}`)
	taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()
	goalID, workstreamID := seedTaskCenterResultGoal(t, store, taskID, runID)
	artifactID, err := domaintask.TaskCenterResultArtifactID(taskID, runID)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(result)
	inputDigest, err := domaintask.TaskCenterKindInputSHA256(json.RawMessage(`{"kind":"movie","name":"Arrival","limit":10}`))
	if err != nil {
		t.Fatal(err)
	}
	reference := domaintask.TaskCenterResultReference{
		ArtifactID: artifactID, SourceGoalID: goalID, WorkstreamID: workstreamID,
		TaskID: taskID, RunID: runID, KindID: "movie.catalog.lookup", KindInputSHA256: inputDigest,
		ResultSHA256: hex.EncodeToString(digest[:]), DataScope: domainworkstream.TaskPlanDataScopePublic,
	}
	authorizeCalls := 0
	authorize := func(context.Context, domaintask.TaskCenterResultReference) error { authorizeCalls++; return nil }
	if err := store.SaveTaskCenterExecutionResult(ctx, reference, result, authorize); err != nil {
		t.Fatalf("SaveTaskCenterExecutionResult first write: %v", err)
	}
	if err := store.SaveTaskCenterExecutionResult(ctx, reference, result, authorize); err != nil {
		t.Fatalf("SaveTaskCenterExecutionResult retry: %v", err)
	}
	gotReference, gotResult, err := store.GetTaskCenterExecutionResult(ctx, reference, authorize)
	if err != nil || gotReference != reference || string(gotResult) != string(result) {
		t.Fatalf("GetTaskCenterExecutionResult = %#v, %s, %v", gotReference, gotResult, err)
	}
	if authorizeCalls != 4 {
		t.Fatalf("authorization calls = %d, want 4", authorizeCalls)
	}

	changed := json.RawMessage(`{"kind":"movie.catalog.lookup","not_found":false}`)
	changedDigest := sha256.Sum256(changed)
	changedReference := reference
	changedReference.ResultSHA256 = hex.EncodeToString(changedDigest[:])
	if err := store.SaveTaskCenterExecutionResult(ctx, changedReference, changed, authorize); !errors.Is(err, domainworkstream.ErrTaskPlanConflict) {
		t.Fatalf("same artifact identity with changed result error = %v, want conflict", err)
	}

	if err := store.SaveTaskCenterExecutionResult(ctx, reference, result, func(context.Context, domaintask.TaskCenterResultReference) error {
		return domainworkstream.ErrTaskPlanForbidden
	}); !errors.Is(err, domainworkstream.ErrTaskPlanForbidden) {
		t.Fatalf("unauthorized result write error = %v, want forbidden", err)
	}
	items, err := store.ListArtifacts(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Type == TaskCenterExecutionResultArtifactType || item.ArtifactID == artifactID {
			t.Fatalf("generic artifact list exposed private result: %#v", item)
		}
	}
}

func seedTaskCenterResultGoal(t *testing.T, store *SQLiteStore, taskID modulecore.TaskID, runID modulecore.RunID) (modulecore.GoalID, modulecore.WorkstreamID) {
	t.Helper()
	now := time.Now().UTC()
	goalID, workstreamID := modulecore.NewGoalID(), modulecore.NewWorkstreamID()
	kindInput := json.RawMessage(`{"kind":"movie","name":"Arrival","limit":10}`)
	draft, _ := json.Marshal(domaintask.Task{Title: "Movie lookup", WorkstreamID: workstreamID, GoalID: goalID, Assignee: "mio", OwnerID: "mio", ReadOnly: true})
	shared, _ := json.Marshal(domaintask.SharedRoleContext{})
	input := domainworkstream.TaskPlanHandoffInput{
		Version: 1, Revision: 2, AssignmentEpoch: 1, KindID: domainworkstream.TaskPlanKindMovieCatalogLookup, KindInputVersion: 1,
		TaskDraft: draft, SharedContext: shared, KindInput: kindInput,
	}
	input.InputHash = domainworkstream.ComputeTaskPlanHandoffInputHash(input.TaskDraft, input.SharedContext, input.KindInput)
	goal := domainworkstream.Goal{
		GoalID: string(goalID), WorkstreamID: string(workstreamID), Title: "Movie lookup", Description: "Frozen lookup",
		SuccessCriteria: []string{"The lookup result is available."}, Verification: []string{"Review the result."},
		Status: domainworkstream.StatusWaiting, CreatedAt: now,
		TaskPlan: &domainworkstream.TaskPlan{
			DataScope: domainworkstream.TaskPlanDataScopePublic, CreatorKind: string(tool.ActorKindAgent), CreatedBy: "mio", Assignee: "mio",
			KindID: domainworkstream.TaskPlanKindMovieCatalogLookup, KindInput: kindInput, KindInputVersion: 1,
			Revision: 3, AssignmentEpoch: 1, PolicyRevision: strings.Repeat("a", 64), Timing: modulecore.TaskTiming{},
			HandoffState: domainworkstream.TaskPlanHandoffLinked, HandoffKey: string(modulecore.NewRequestID()),
			HandoffInput: &input, TaskID: taskID, RunID: runID, UpdatedAt: now,
		},
	}
	if err := domainworkstream.ValidateGoal(goal); err != nil {
		t.Fatalf("validate seeded source Goal: %v", err)
	}
	payload, err := json.Marshal(goal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO workstream_goal (goal_id, workstream_id, created_at, payload) VALUES (?, ?, ?, ?)`, goalID, workstreamID, now.Format(timeFormatRFC3339Nano), string(payload)); err != nil {
		t.Fatal(err)
	}
	return goalID, workstreamID
}

func TestTaskCenterExecutionResultArtifactRejectsForgedScopeAndDigest(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "task-center-result-invalid.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()
	artifactID, err := domaintask.TaskCenterResultArtifactID(taskID, runID)
	if err != nil {
		t.Fatal(err)
	}
	result := json.RawMessage(`{"ok":true}`)
	digest := sha256.Sum256(result)
	reference := domaintask.TaskCenterResultReference{
		ArtifactID: artifactID, SourceGoalID: modulecore.NewGoalID(), WorkstreamID: modulecore.NewWorkstreamID(), TaskID: taskID,
		RunID: runID, KindID: "movie.catalog.lookup", KindInputSHA256: strings.Repeat("b", 64),
		ResultSHA256: hex.EncodeToString(digest[:]), DataScope: domainworkstream.TaskPlanDataScopeUser,
	}
	if err := store.SaveTaskCenterExecutionResult(context.Background(), reference, result, func(context.Context, domaintask.TaskCenterResultReference) error { return nil }); !errors.Is(err, domainworkstream.ErrTaskPlanInvalid) {
		t.Fatalf("private ref without exact UID error = %v, want invalid", err)
	}
	reference.DataScope, reference.UserID = domainworkstream.TaskPlanDataScopePublic, ""
	reference.ResultSHA256 = strings.Repeat("c", 64)
	if err := store.SaveTaskCenterExecutionResult(context.Background(), reference, result, func(context.Context, domaintask.TaskCenterResultReference) error { return nil }); !errors.Is(err, domainworkstream.ErrTaskPlanConflict) {
		t.Fatalf("result digest mismatch error = %v, want conflict", err)
	}
}

func TestTaskPlanExecutionResultAndPublicCreatorAcceptanceCASReplay(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "task-center-result-cas.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()
	goalID, _ := seedTaskCenterResultGoal(t, store, taskID, runID)
	var goal domainworkstream.Goal
	var raw string
	if err := store.db.QueryRow(`SELECT payload FROM workstream_goal WHERE goal_id = ?`, goalID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw), &goal); err != nil {
		t.Fatal(err)
	}
	goal.TaskPlan.CreatorKind, goal.TaskPlan.CreatedBy = string(tool.ActorKindUser), "user-A"
	updated, _ := json.Marshal(goal)
	if _, err := store.db.Exec(`UPDATE workstream_goal SET payload = ? WHERE goal_id = ?`, string(updated), goalID); err != nil {
		t.Fatal(err)
	}
	agentScope := taskPlanTestAgentScope(t, "result-write", "mio", "user-A", tool.DataScopePublic)
	agentCtx := tool.WithToolExecutionScope(context.Background(), agentScope)
	artifactID, _ := domaintask.TaskCenterResultArtifactID(taskID, runID)
	inputDigest, err := domaintask.TaskCenterKindInputSHA256(json.RawMessage(`{"kind":"movie","name":"Arrival","limit":10}`))
	if err != nil {
		t.Fatal(err)
	}
	resultDigest := strings.Repeat("b", 64)
	criteriaDigest, _ := domainworkstream.TaskPlanCriteriaSHA256(goal.SuccessCriteria, goal.Verification)
	update := domainworkstream.TaskPlanExecutionResultUpdate{
		GoalID: string(goalID), ExpectedRevision: 3, ExpectedAssignmentEpoch: 1, ExpectedPolicyRevision: strings.Repeat("a", 64),
		IdempotencyKey: "taskcenter-result-cas", ArtifactID: artifactID, TaskID: taskID, RunID: runID,
		KindInputSHA256: inputDigest, ResultSHA256: resultDigest, CriteriaSHA256: criteriaDigest,
	}
	allowLinked := func(candidate domainworkstream.Goal) error {
		if candidate.GoalID != string(goalID) || candidate.TaskPlan == nil || candidate.TaskPlan.Assignee != "mio" {
			return domainworkstream.ErrTaskPlanForbidden
		}
		return nil
	}
	recorded, err := store.RecordTaskPlanExecutionResult(agentCtx, agentScope, update, allowLinked)
	if err != nil || recorded.Goal.TaskPlan.ExecutionResult == nil || recorded.Goal.TaskPlan.ExecutionResult.Verification != domainworkstream.TaskPlanResultVerificationPending {
		t.Fatalf("record execution result = %#v, %v", recorded, err)
	}
	replayed, err := store.RecordTaskPlanExecutionResult(agentCtx, agentScope, update, func(domainworkstream.Goal) error {
		t.Fatal("result authorization ran during receipt replay")
		return nil
	})
	if err != nil || !replayed.Replayed || replayed.Goal.TaskPlan.ExecutionResult.ResultSHA256 != resultDigest {
		t.Fatalf("record replay = %#v, %v", replayed, err)
	}
	userScope := taskPlanTestUserScope(t, "creator-accept")
	userCtx := tool.WithToolExecutionScope(context.Background(), userScope)
	accept := domainworkstream.TaskPlanExecutionResultAcceptance{
		GoalID: string(goalID), ExpectedRevision: 4, ExpectedAssignmentEpoch: 1, IdempotencyKey: "creator-accept-key",
		ArtifactID: artifactID, TaskID: taskID, RunID: runID, KindInputSHA256: inputDigest, ResultSHA256: resultDigest, CriteriaSHA256: criteriaDigest,
	}
	otherUser, err := tool.NewToolExecutionScope("other-user-accept", tool.ActorKindUser, "user-B", "user-B", []string{tool.DataScopePublic}, tool.AuthenticationSourceHTTP)
	if err != nil {
		t.Fatal(err)
	}
	otherCtx := tool.WithToolExecutionScope(context.Background(), otherUser)
	if _, err := store.AcceptTaskPlanExecutionResult(otherCtx, otherUser, accept, func(domainworkstream.Goal) error {
		return domainworkstream.ErrTaskPlanForbidden
	}); !errors.Is(err, domainworkstream.ErrTaskPlanForbidden) {
		t.Fatalf("non-creator public viewer acceptance error = %v, want forbidden", err)
	}
	creatorAuth := func(candidate domainworkstream.Goal) error {
		if candidate.TaskPlan == nil || candidate.TaskPlan.CreatorKind != string(tool.ActorKindUser) || candidate.TaskPlan.CreatedBy != "user-A" {
			return domainworkstream.ErrTaskPlanForbidden
		}
		return nil
	}
	accepted, err := store.AcceptTaskPlanExecutionResult(userCtx, userScope, accept, creatorAuth)
	if err != nil || accepted.Goal.Status != domainworkstream.StatusCompleted || accepted.Goal.TaskPlan.ExecutionResult.Verification != domainworkstream.TaskPlanResultVerificationAccepted || accepted.Goal.TaskPlan.ExecutionResult.AcceptedBy != "user-A" {
		t.Fatalf("creator acceptance = %#v, %v", accepted, err)
	}
	acceptReplay, err := store.AcceptTaskPlanExecutionResult(userCtx, userScope, accept, func(domainworkstream.Goal) error {
		t.Fatal("acceptance authorization ran during receipt replay")
		return nil
	})
	if err != nil || !acceptReplay.Replayed || acceptReplay.Goal.CompletedAt != accepted.Goal.CompletedAt {
		t.Fatalf("acceptance replay = %#v, %v", acceptReplay, err)
	}
	changed := accept
	changed.ResultSHA256 = strings.Repeat("c", 64)
	if _, err := store.AcceptTaskPlanExecutionResult(userCtx, userScope, changed, creatorAuth); !errors.Is(err, domainworkstream.ErrTaskPlanConflict) {
		t.Fatalf("same acceptance key with changed result error = %v, want conflict", err)
	}
}
