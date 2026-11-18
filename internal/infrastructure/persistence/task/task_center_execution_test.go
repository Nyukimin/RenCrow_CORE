package task

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func makeTypedTaskForPersistenceTest(t *testing.T, now time.Time) (domaintask.Task, domaintask.Run, domaintask.SharedRoleContext) {
	t.Helper()
	taskID, goalID, workstreamID := modulecore.NewTaskID(), modulecore.NewGoalID(), modulecore.NewWorkstreamID()
	timing := modulecore.TaskTiming{AllowedWindows: []modulecore.TaskTimeWindow{}}
	timingDigest, err := domaintask.TaskTimingSHA256(timing)
	if err != nil {
		t.Fatal(err)
	}
	kindInput := json.RawMessage(`{"kind":"movie"}`)
	kindDigest, err := domaintask.TaskCenterKindInputSHA256(kindInput)
	if err != nil {
		t.Fatal(err)
	}
	task := domaintask.Task{
		TaskID: taskID, Title: "lookup", Route: domaintask.RouteGeneral, Assignee: "mio",
		Status: domaintask.StatusRunning, Priority: domaintask.PriorityNormal,
		WorkstreamID: workstreamID, GoalID: goalID, Timing: &timing,
		TaskCenterExecution: &domaintask.TaskCenterExecution{
			KindID: "movie.catalog.lookup", KindInputVersion: 1, KindInputSHA256: kindDigest,
			SourceGoalID: goalID, PreparedRevision: 1, TimingSHA256: timingDigest,
		},
		InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked,
		CreatedAt:       now, UpdatedAt: now, StartedAt: &now,
	}
	run := domaintask.Run{
		WriterGeneration: 1, RunID: modulecore.NewRunID(), TaskID: taskID,
		StartReason: domaintask.RunStartReasonFirst, Assignee: "mio",
		Status: domaintask.RunStatusRunning, StartedAt: now,
	}
	shared := domaintask.SharedRoleContext{TaskID: taskID, UserIntent: "lookup", UpdatedAt: now}
	return task, run, shared
}

func TestRecoverTaskCenterExecutionFenceRequiresExactResultProofAfterRestart(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	task, run, shared := makeTypedTaskForPersistenceTest(t, now)
	initialGeneration, err := store.WriterGeneration()
	if err != nil {
		t.Fatal(err)
	}
	run.WriterGeneration = initialGeneration
	_, err = store.ExecuteIdempotentGlobalTaskOperation(ctx, "test:seed-center-run", strings.Repeat("a", 64), func(tx domaintask.Store) (json.RawMessage, error) {
		writer, ok := tx.(domaintask.TaskCenterExecutionWriter)
		if !ok {
			t.Fatal("transaction store lacks TaskCenter typed writer")
		}
		if err := writer.SaveTaskCenterExecutionState(ctx, task, run, &shared, func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext) error { return nil }); err != nil {
			return nil, err
		}
		return json.RawMessage(`{"created":true}`), nil
	})
	if err != nil {
		t.Fatalf("seed running typed Task: %v", err)
	}
	result := json.RawMessage(`{"matches":[]}`)
	resultDigest := sha256.Sum256(result)
	artifactID, err := domaintask.TaskCenterResultArtifactID(task.TaskID, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	reference := domaintask.TaskCenterResultReference{
		ArtifactID: artifactID, SourceGoalID: task.GoalID, WorkstreamID: task.WorkstreamID, TaskID: task.TaskID, RunID: run.RunID,
		KindID: task.TaskCenterExecution.KindID, KindInputSHA256: task.TaskCenterExecution.KindInputSHA256,
		ResultSHA256: hex.EncodeToString(resultDigest[:]), DataScope: domaintask.TaskCenterResultScopePublic,
	}
	const fenceID = "test:stale-result-capability"
	if err := store.persistTaskExecutionFenceAcquire(ctx, task.TaskID, fenceID, false); err != nil {
		t.Fatalf("persist active effect capability: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewJSONLStore(root)
	if err != nil {
		t.Fatalf("reopen with active effect capability: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close reopened Task store: %v", err)
		}
	})
	currentGeneration, err := store.WriterGeneration()
	if err != nil || currentGeneration <= initialGeneration {
		t.Fatalf("new generation=%d old=%d error=%v", currentGeneration, initialGeneration, err)
	}
	proofAvailable := false
	authorize := func(_ context.Context, gotTask domaintask.Task, gotRun domaintask.Run, gotShared domaintask.SharedRoleContext, gotReference domaintask.TaskCenterResultReference) error {
		if gotTask.TaskID != task.TaskID || gotRun.RunID != run.RunID || gotReference != reference || gotShared.TaskID != task.TaskID || !proofAvailable {
			return domaintask.ErrTaskCenterExecutionReceiptForbidden
		}
		return nil
	}
	if err := store.RecoverTaskCenterExecutionFence(ctx, task.TaskID, run.RunID, reference, authorize); err == nil {
		t.Fatal("stale effect capability recovered without canonical result proof")
	}
	if store.activeFences[task.TaskID] != fenceID {
		t.Fatalf("failed proof changed active fence: %q", store.activeFences[task.TaskID])
	}
	proofAvailable = true
	if err := store.RecoverTaskCenterExecutionFence(ctx, task.TaskID, run.RunID, reference, authorize); err != nil {
		t.Fatalf("recover exact result capability: %v", err)
	}
	if store.activeFences[task.TaskID] != "" || store.fenceByID[fenceID].Active || store.fenceByID[fenceID].ReleaseGeneration != currentGeneration {
		t.Fatalf("recovered fence state is inconsistent: active=%q record=%+v", store.activeFences[task.TaskID], store.fenceByID[fenceID])
	}
	gotTask, gotRun, err := store.GetTaskCenterExecutionState(ctx, task.TaskID, run.RunID, func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext) error { return nil })
	if err != nil || gotTask.Status != domaintask.StatusRunning || gotRun.Status != domaintask.RunStatusRunning || gotRun.WriterGeneration != initialGeneration {
		t.Fatalf("fence recovery changed Task/Run lifecycle: task=%+v run=%+v err=%v", gotTask, gotRun, err)
	}
}

func TestTaskCenterReceiptLookupDistinguishesAbsentOperationFromBrokenReceipt(t *testing.T) {
	ctx := context.Background()
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	draft, _, shared := makeTypedTaskForPersistenceTest(t, now)
	draft.TaskID = ""
	draft.Status = ""
	draft.Priority = ""
	draft.CreatedAt = time.Time{}
	draft.UpdatedAt = time.Time{}
	draft.StartedAt = nil
	draft.FinishedAt = nil
	shared.TaskID = ""
	kindInput := json.RawMessage(`{"kind":"movie"}`)
	key := modulecore.NewRequestID()
	input, err := domaintask.NewTaskCenterExecutionReceiptReadInput(key, 1, draft, shared, kindInput)
	if err != nil {
		t.Fatalf("build missing-receipt lookup input: %v", err)
	}
	authorize := func(context.Context, domaintask.TaskCenterExecutionReceiptReadInput) error { return nil }
	if _, _, err := store.LookupTaskCenterExecutionReceipt(ctx, input, authorize); !errors.Is(err, domaintask.ErrTaskCenterExecutionReceiptNotFound) || !errors.Is(err, ErrTaskOperationNotFound) {
		t.Fatalf("absent operation error=%v, want typed receipt-missing plus preserved persistence sentinel", err)
	}
	_, err = store.ExecuteIdempotentGlobalTaskOperation(ctx, input.OperationID, input.RequestHash, func(tx domaintask.Store) (json.RawMessage, error) {
		storedTask := draft
		storedTask.TaskID = modulecore.NewTaskID()
		storedTask.ApplyDefaults(now)
		storedTask.Status = domaintask.StatusRunning
		storedTask.StartedAt = &now
		storedRun := domaintask.Run{
			WriterGeneration: 1, RunID: modulecore.NewRunID(), TaskID: storedTask.TaskID,
			StartReason: domaintask.RunStartReasonFirst, Assignee: storedTask.Assignee,
			Status: domaintask.RunStatusRunning, StartedAt: now,
		}
		storedShared := shared
		storedShared.TaskID = storedTask.TaskID
		writer, ok := tx.(domaintask.TaskCenterExecutionWriter)
		if !ok {
			return nil, errors.New("transaction lacks typed TaskCenter writer")
		}
		if err := writer.SaveTaskCenterExecutionState(ctx, storedTask, storedRun, &storedShared, func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext) error { return nil }); err != nil {
			return nil, err
		}
		return json.Marshal(struct {
			TaskID modulecore.TaskID `json:"task_id"`
			RunID  modulecore.RunID  `json:"run_id"`
		}{TaskID: storedTask.TaskID, RunID: modulecore.NewRunID()})
	})
	if err != nil {
		t.Fatalf("seed receipt with dangling Task/Run references: %v", err)
	}
	if _, _, err := store.LookupTaskCenterExecutionReceipt(ctx, input, authorize); errors.Is(err, domaintask.ErrTaskCenterExecutionReceiptNotFound) || !errors.Is(err, domaintask.ErrTaskCenterExecutionReceiptCorrupt) || !errors.Is(err, domaintask.ErrNotFound) {
		t.Fatalf("broken receipt error=%v, want typed corrupt plus underlying not-found", err)
	}
}

func TestTaskCenterGlobalReceiptHidesRootlessResult(t *testing.T) {
	ctx := context.Background()
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 11, 18, 2, 3, 4, 0, time.UTC)
	task, run, shared := makeTypedTaskForPersistenceTest(t, now)
	generation, err := store.WriterGeneration()
	if err != nil {
		t.Fatal(err)
	}
	run.WriterGeneration = generation
	operationID := "rootless-private-result"
	requestHash := strings.Repeat("a", 64)
	_, err = store.ExecuteIdempotentGlobalTaskOperation(ctx, operationID, requestHash, func(tx domaintask.Store) (json.RawMessage, error) {
		writer, ok := tx.(domaintask.TaskCenterExecutionWriter)
		if !ok {
			t.Fatal("transaction store does not implement typed TaskCenter writer")
		}
		if err := writer.SaveTaskCenterExecutionState(ctx, task, run, &shared, func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext) error { return nil }); err != nil {
			return nil, err
		}
		return json.RawMessage(`{"nested":{"result":"sensitive"}}`), nil
	})
	if err != nil {
		t.Fatalf("ExecuteIdempotentGlobalTaskOperation: %v", err)
	}
	receipt, err := store.lookupTaskOperationRaw(ctx, operationID)
	if err != nil || !receipt.OwnerOnly {
		t.Fatalf("raw receipt owner_only=%v err=%v, want owner-only", receipt.OwnerOnly, err)
	}
	if _, err := store.LookupTaskOperation(ctx, operationID); !errors.Is(err, ErrTaskOperationNotFound) {
		t.Fatalf("generic LookupTaskOperation error=%v, want hidden not-found", err)
	}
	if result, found, err := store.LookupGlobalTaskOperation(ctx, operationID, requestHash); found || err != nil || result != nil {
		t.Fatalf("generic global lookup result=%s found=%v err=%v, want indistinguishable hidden absence", result, found, err)
	}
}
