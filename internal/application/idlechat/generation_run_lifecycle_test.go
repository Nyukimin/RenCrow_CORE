package idlechat

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type wordRunCompletionCall struct {
	taskID        modulecore.TaskID
	runID         modulecore.RunID
	actorID       string
	status        domaintask.Status
	summary       string
	waitingReason string
}

type wordRunVerificationCall struct {
	taskID  modulecore.TaskID
	runID   modulecore.RunID
	actorID string
	status  domaintask.Status
}

type wordRunLifecycleOwnerFake struct {
	task          domaintask.Task
	runs          []domaintask.Run
	getErr        error
	listErr       error
	completeErr   error
	verifyErr     error
	completeCalls []wordRunCompletionCall
	completeCtx   context.Context
	verifyCalls   []wordRunVerificationCall
	verifyCtx     context.Context
}

func (f *wordRunLifecycleOwnerFake) Create(_ context.Context, draft domaintask.Task, _ domaintask.SharedRoleContext) (domaintask.Task, error) {
	return draft, nil
}

func (f *wordRunLifecycleOwnerFake) StartRunWithReason(_ context.Context, _ modulecore.TaskID, _ domaintask.RunStartReason) (domaintask.Run, error) {
	return domaintask.Run{}, errors.New("not used by word run lifecycle tests")
}

func (f *wordRunLifecycleOwnerFake) Get(_ context.Context, _ modulecore.TaskID) (domaintask.Task, error) {
	if f.getErr != nil {
		return domaintask.Task{}, f.getErr
	}
	return f.task, nil
}

func (f *wordRunLifecycleOwnerFake) ListRuns(_ context.Context, _ domaintask.RunFilter) ([]domaintask.Run, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]domaintask.Run(nil), f.runs...), nil
}

func (f *wordRunLifecycleOwnerFake) VerifyRunCompletion(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, actorID string, status domaintask.Status) error {
	f.verifyCtx = ctx
	f.verifyCalls = append(f.verifyCalls, wordRunVerificationCall{taskID: taskID, runID: runID, actorID: actorID, status: status})
	return f.verifyErr
}

func (f *wordRunLifecycleOwnerFake) CompleteRun(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, actorID string, status domaintask.Status, summary, waitingReason string) (domaintask.Task, error) {
	f.completeCtx = ctx
	f.completeCalls = append(f.completeCalls, wordRunCompletionCall{
		taskID: taskID, runID: runID, actorID: actorID, status: status, summary: summary, waitingReason: waitingReason,
	})
	if f.completeErr != nil {
		return domaintask.Task{}, f.completeErr
	}
	return f.task, nil
}

func wordLifecycleTask(taskID modulecore.TaskID, status domaintask.Status) domaintask.Task {
	created := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	task := domaintask.Task{
		TaskID:          taskID,
		Title:           "IdleChat word topic",
		Route:           domaintask.RouteGeneral,
		Assignee:        "Shiro",
		Status:          status,
		Priority:        domaintask.PriorityNormal,
		InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked,
		CreatedAt:       created,
		UpdatedAt:       created,
	}
	if status == domaintask.StatusWaiting {
		task.WaitingReason = "checkpoint retry after dependency recovery"
	}
	if domaintask.IsTerminal(status) {
		finished := created.Add(time.Minute)
		task.FinishedAt = &finished
	}
	return task
}

func wordLifecycleRun(taskID modulecore.TaskID, runID modulecore.RunID, status domaintask.RunStatus, startedAt time.Time) domaintask.Run {
	run := domaintask.Run{
		WriterGeneration: 1,
		RunID:            runID,
		TaskID:           taskID,
		StartReason:      domaintask.RunStartReasonFirst,
		Assignee:         "Shiro",
		Status:           status,
		StartedAt:        startedAt,
	}
	if status != domaintask.RunStatusRunning {
		completed := startedAt.Add(time.Second)
		run.CompletedAt = &completed
	}
	return run
}

func TestInspectWordRunRejectsIdentityAndShapeViolations(t *testing.T) {
	taskID := modulecore.NewTaskID()
	otherTaskID := modulecore.NewTaskID()
	runID := modulecore.NewRunID()
	start := time.Date(2026, 9, 8, 1, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		setup  func(*wordRunLifecycleOwnerFake)
		taskID modulecore.TaskID
		want   string
	}{
		{
			name:   "different task",
			taskID: otherTaskID,
			setup: func(f *wordRunLifecycleOwnerFake) {
				f.task = wordLifecycleTask(taskID, domaintask.StatusRunning)
				f.runs = []domaintask.Run{wordLifecycleRun(taskID, runID, domaintask.RunStatusRunning, start)}
			},
			want: "task",
		},
		{
			name:   "run belongs to another task",
			taskID: taskID,
			setup: func(f *wordRunLifecycleOwnerFake) {
				f.task = wordLifecycleTask(taskID, domaintask.StatusRunning)
				f.runs = []domaintask.Run{wordLifecycleRun(otherTaskID, runID, domaintask.RunStatusRunning, start)}
			},
			want: "task_id",
		},
		{
			name:   "empty assignee",
			taskID: taskID,
			setup: func(f *wordRunLifecycleOwnerFake) {
				f.task = wordLifecycleTask(taskID, domaintask.StatusRunning)
				run := wordLifecycleRun(taskID, runID, domaintask.RunStatusRunning, start)
				run.Assignee = "  "
				f.runs = []domaintask.Run{run}
			},
			want: "assignee",
		},
		{
			name:   "duplicate run",
			taskID: taskID,
			setup: func(f *wordRunLifecycleOwnerFake) {
				f.task = wordLifecycleTask(taskID, domaintask.StatusRunning)
				run := wordLifecycleRun(taskID, runID, domaintask.RunStatusRunning, start)
				f.runs = []domaintask.Run{run, run}
			},
			want: "duplicate",
		},
		{
			name:   "old run after resume",
			taskID: taskID,
			setup: func(f *wordRunLifecycleOwnerFake) {
				f.task = wordLifecycleTask(taskID, domaintask.StatusRunning)
				oldRun := wordLifecycleRun(taskID, runID, domaintask.RunStatusRunning, start)
				newRun := wordLifecycleRun(taskID, modulecore.NewRunID(), domaintask.RunStatusRunning, start.Add(time.Minute))
				f.runs = []domaintask.Run{newRun, oldRun}
			},
			want: "latest",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			owner := &wordRunLifecycleOwnerFake{}
			test.setup(owner)
			_, err := inspectGenerationRun(context.Background(), owner, test.taskID, runID)
			if err == nil {
				t.Fatal("inspectGenerationRun() succeeded for invalid identity/state")
			}
			if !containsWordLifecycleError(err, test.want) {
				t.Fatalf("inspectGenerationRun() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestInspectWordRunRejectsAmbiguousLatestAndOtherActiveRun(t *testing.T) {
	taskID := modulecore.NewTaskID()
	runID := modulecore.NewRunID()
	start := time.Date(2026, 9, 8, 2, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		runs []domaintask.Run
		want string
	}{
		{
			name: "same start time",
			runs: []domaintask.Run{
				wordLifecycleRun(taskID, runID, domaintask.RunStatusRunning, start),
				wordLifecycleRun(taskID, modulecore.NewRunID(), domaintask.RunStatusSucceeded, start),
			},
			want: "ambiguous",
		},
		{
			name: "other active run",
			runs: []domaintask.Run{
				wordLifecycleRun(taskID, runID, domaintask.RunStatusRunning, start),
				wordLifecycleRun(taskID, modulecore.NewRunID(), domaintask.RunStatusRunning, start.Add(-time.Minute)),
			},
			want: "active",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			owner := &wordRunLifecycleOwnerFake{task: wordLifecycleTask(taskID, domaintask.StatusRunning), runs: test.runs}
			_, err := inspectGenerationRun(context.Background(), owner, taskID, runID)
			if err == nil || !containsWordLifecycleError(err, test.want) {
				t.Fatalf("inspectGenerationRun() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestCompleteWordRunUsesExactActiveIdentity(t *testing.T) {
	taskID := modulecore.NewTaskID()
	runID := modulecore.NewRunID()
	owner := &wordRunLifecycleOwnerFake{
		task: wordLifecycleTask(taskID, domaintask.StatusRunning),
		runs: []domaintask.Run{wordLifecycleRun(taskID, runID, domaintask.RunStatusRunning, time.Now().UTC())},
	}
	ctx := context.WithValue(context.Background(), wordLifecycleContextKey{}, "caller")

	if err := completeGenerationRun(ctx, owner, taskID, runID, domaintask.StatusSucceeded, "finished", ""); err != nil {
		t.Fatalf("completeGenerationRun() error = %v", err)
	}
	if len(owner.completeCalls) != 1 {
		t.Fatalf("CompleteRun calls = %d, want 1", len(owner.completeCalls))
	}
	call := owner.completeCalls[0]
	if call.taskID != taskID || call.runID != runID || call.actorID != "Shiro" || call.status != domaintask.StatusSucceeded || call.summary != "finished" {
		t.Fatalf("CompleteRun call = %+v", call)
	}
	if owner.completeCtx != ctx {
		t.Fatal("completion context was not propagated")
	}
}

func TestCompleteWordRunWaitingUsesWaitingStatusAndReason(t *testing.T) {
	taskID := modulecore.NewTaskID()
	runID := modulecore.NewRunID()
	owner := &wordRunLifecycleOwnerFake{
		task: wordLifecycleTask(taskID, domaintask.StatusRunning),
		runs: []domaintask.Run{wordLifecycleRun(taskID, runID, domaintask.RunStatusRunning, time.Now().UTC())},
	}

	if err := completeGenerationRun(context.Background(), owner, taskID, runID, domaintask.StatusWaiting, "checkpoint", "awaiting dependency recovery"); err != nil {
		t.Fatalf("completeGenerationRun() error = %v", err)
	}
	if len(owner.completeCalls) != 1 || owner.completeCalls[0].status != domaintask.StatusWaiting || owner.completeCalls[0].waitingReason != "awaiting dependency recovery" {
		t.Fatalf("CompleteRun calls = %+v", owner.completeCalls)
	}
}

func TestCompleteWordRunTerminalIsIdempotentWithoutOwnerWrite(t *testing.T) {
	taskID := modulecore.NewTaskID()
	runID := modulecore.NewRunID()
	owner := &wordRunLifecycleOwnerFake{
		task:        wordLifecycleTask(taskID, domaintask.StatusSucceeded),
		runs:        []domaintask.Run{wordLifecycleRun(taskID, runID, domaintask.RunStatusSucceeded, time.Now().UTC())},
		completeErr: errors.New("CompleteRun must not be called"),
	}
	ctx := context.WithValue(context.Background(), wordLifecycleContextKey{}, "terminal-retry")

	if err := completeGenerationRun(ctx, owner, taskID, runID, domaintask.StatusSucceeded, "different summary", ""); err != nil {
		t.Fatalf("completeGenerationRun() terminal retry error = %v", err)
	}
	if len(owner.completeCalls) != 0 {
		t.Fatalf("CompleteRun calls = %d, want 0", len(owner.completeCalls))
	}
	if len(owner.verifyCalls) != 1 {
		t.Fatalf("VerifyRunCompletion calls = %d, want 1", len(owner.verifyCalls))
	}
	call := owner.verifyCalls[0]
	if call.taskID != taskID || call.runID != runID || call.actorID != "Shiro" || call.status != domaintask.StatusSucceeded {
		t.Fatalf("VerifyRunCompletion call = %+v", call)
	}
	if owner.verifyCtx != ctx {
		t.Fatal("verification context was not propagated")
	}
}

func TestCompleteWordRunRejectsDifferentTerminalStatusAndMissingWaitingReason(t *testing.T) {
	taskID := modulecore.NewTaskID()
	runID := modulecore.NewRunID()
	owner := &wordRunLifecycleOwnerFake{
		task:      wordLifecycleTask(taskID, domaintask.StatusFailed),
		runs:      []domaintask.Run{wordLifecycleRun(taskID, runID, domaintask.RunStatusFailed, time.Now().UTC())},
		verifyErr: errors.New("terminal status mismatch"),
	}
	if err := completeGenerationRun(context.Background(), owner, taskID, runID, domaintask.StatusSucceeded, "", ""); err == nil || !containsWordLifecycleError(err, "status") {
		t.Fatalf("different terminal status error = %v", err)
	}
	if len(owner.completeCalls) != 0 {
		t.Fatalf("CompleteRun calls after terminal mismatch = %d, want 0", len(owner.completeCalls))
	}
	if len(owner.verifyCalls) != 1 {
		t.Fatalf("VerifyRunCompletion calls after terminal mismatch = %d, want 1", len(owner.verifyCalls))
	}

	activeOwner := &wordRunLifecycleOwnerFake{
		task: wordLifecycleTask(taskID, domaintask.StatusRunning),
		runs: []domaintask.Run{wordLifecycleRun(taskID, runID, domaintask.RunStatusRunning, time.Now().UTC())},
	}
	if err := completeGenerationRun(context.Background(), activeOwner, taskID, runID, domaintask.StatusWaiting, "", " "); err == nil || !containsWordLifecycleError(err, "waiting reason") {
		t.Fatalf("missing waiting reason error = %v", err)
	}
	if len(activeOwner.completeCalls) != 0 {
		t.Fatalf("CompleteRun calls after missing reason = %d, want 0", len(activeOwner.completeCalls))
	}
}

func TestCompleteWordRunPropagatesOwnerError(t *testing.T) {
	taskID := modulecore.NewTaskID()
	runID := modulecore.NewRunID()
	wantErr := errors.New("owner completion failed")
	owner := &wordRunLifecycleOwnerFake{
		task:        wordLifecycleTask(taskID, domaintask.StatusRunning),
		runs:        []domaintask.Run{wordLifecycleRun(taskID, runID, domaintask.RunStatusRunning, time.Now().UTC())},
		completeErr: wantErr,
	}

	if err := completeGenerationRun(context.Background(), owner, taskID, runID, domaintask.StatusFailed, "failed", ""); !errors.Is(err, wantErr) {
		t.Fatalf("completeGenerationRun() error = %v, want %v", err, wantErr)
	}
}

type wordLifecycleContextKey struct{}

func containsWordLifecycleError(err error, want string) bool {
	return err != nil && strings.Contains(err.Error(), want)
}
