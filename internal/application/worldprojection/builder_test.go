package worldprojection

import (
	"context"
	"errors"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domainprojection "github.com/Nyukimin/RenCrow_CORE/internal/domain/worldprojection"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func activeTask(id modulecore.TaskID, status domaintask.Status) domaintask.Task {
	now := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	return domaintask.Task{
		TaskID:          id,
		Title:           "assigned work",
		Route:           domaintask.RouteGeneral,
		Assignee:        "mio",
		Status:          status,
		Priority:        domaintask.PriorityNormal,
		InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked,
		CreatedAt:       now,
		UpdatedAt:       now.Add(time.Minute),
	}
}

func runningRun(taskID modulecore.TaskID, runID modulecore.RunID) domaintask.Run {
	return domaintask.Run{
		RunID:       runID,
		TaskID:      taskID,
		StartReason: domaintask.RunStartReasonFirst,
		Status:      domaintask.RunStatusRunning,
		StartedAt:   time.Date(2026, 10, 20, 9, 1, 0, 0, time.UTC),
	}
}

func validBuildInput() BuildWorkSetInput {
	return BuildWorkSetInput{
		WorldID:        "world-house",
		ActorID:        "mio",
		SourceEpoch:    "core-epoch-1",
		SourceRevision: 3,
		ValidUntil:     time.Date(2026, 10, 20, 13, 0, 0, 0, time.UTC),
	}
}

func TestBuildWorkSetProjectsActiveTasksAndExcludesTerminal(t *testing.T) {
	queuedID := modulecore.NewTaskID()
	runningID := modulecore.NewTaskID()
	waitingID := modulecore.NewTaskID()
	blockedID := modulecore.NewTaskID()
	succeededID := modulecore.NewTaskID()
	failedID := modulecore.NewTaskID()
	runID := modulecore.NewRunID()

	waiting := activeTask(waitingID, domaintask.StatusWaiting)
	waiting.WaitingReason = "waiting for user"
	input := validBuildInput()
	input.Tasks = []domaintask.Task{
		activeTask(queuedID, domaintask.StatusQueued),
		activeTask(runningID, domaintask.StatusRunning),
		waiting,
		activeTask(blockedID, domaintask.StatusBlocked),
		activeTask(succeededID, domaintask.StatusSucceeded),
		activeTask(failedID, domaintask.StatusFailed),
	}
	input.Runs = []domaintask.Run{runningRun(runningID, runID)}
	input.RequiresHousePresence = map[modulecore.TaskID]bool{runningID: true}
	input.ClassificationReasonCodes = map[modulecore.TaskID]string{blockedID: "dependency_blocked"}

	workSet, err := BuildWorkSet(input)
	if err != nil {
		t.Fatalf("BuildWorkSet() error = %v", err)
	}
	if len(workSet.ActiveWork) != 4 {
		t.Fatalf("active work count = %d, want 4: %+v", len(workSet.ActiveWork), workSet.ActiveWork)
	}
	byTask := make(map[string]domainprojection.WorkItem, len(workSet.ActiveWork))
	for _, item := range workSet.ActiveWork {
		byTask[item.TaskID] = item
	}
	if got := byTask[string(queuedID)]; got.ExecutionState != domainprojection.ExecutionQueued || got.RunID != "" {
		t.Fatalf("queued item = %+v", got)
	}
	if got := byTask[string(runningID)]; got.ExecutionState != domainprojection.ExecutionRunning ||
		got.RunID != string(runID) || !got.RequiresHousePresence {
		t.Fatalf("running item = %+v", got)
	}
	if got := byTask[string(waitingID)]; got.ExecutionState != domainprojection.ExecutionWaiting || got.RunID != "" {
		t.Fatalf("waiting item = %+v", got)
	}
	if got := byTask[string(blockedID)]; got.ExecutionState != domainprojection.ExecutionBlocked ||
		got.ClassificationReasonCode != "dependency_blocked" {
		t.Fatalf("blocked item = %+v", got)
	}
	for _, terminal := range []modulecore.TaskID{succeededID, failedID} {
		if _, exists := byTask[string(terminal)]; exists {
			t.Fatalf("terminal task %s was projected", terminal)
		}
	}
}

func TestBuildWorkSetFiltersToExactActorAssignee(t *testing.T) {
	mioID := modulecore.NewTaskID()
	shiroID := modulecore.NewTaskID()
	unassignedID := modulecore.NewTaskID()
	mioTask := activeTask(mioID, domaintask.StatusQueued)
	shiroTask := activeTask(shiroID, domaintask.StatusQueued)
	shiroTask.Assignee = "shiro"
	unassigned := activeTask(unassignedID, domaintask.StatusBlocked)
	unassigned.Assignee = ""
	input := validBuildInput()
	input.Tasks = []domaintask.Task{mioTask, shiroTask, unassigned}
	workSet, err := BuildWorkSet(input)
	if err != nil {
		t.Fatalf("BuildWorkSet() error = %v", err)
	}
	if len(workSet.ActiveWork) != 1 || workSet.ActiveWork[0].TaskID != string(mioID) {
		t.Fatalf("active work = %+v, want only %s", workSet.ActiveWork, mioID)
	}
}

func TestBuildWorkSetSeparatesWorldActorFromCoreAssignee(t *testing.T) {
	taskID := modulecore.NewTaskID()
	task := activeTask(taskID, domaintask.StatusQueued)
	task.Assignee = "Mio"
	input := validBuildInput()
	input.ActorID = "mio"
	input.Assignee = "Mio"
	input.Tasks = []domaintask.Task{task}
	workSet, err := BuildWorkSet(input)
	if err != nil {
		t.Fatalf("BuildWorkSet() error = %v", err)
	}
	if workSet.ActorID != "mio" || len(workSet.ActiveWork) != 1 || workSet.ActiveWork[0].TaskID != string(taskID) {
		t.Fatalf("work set = %+v", workSet)
	}
}

func TestBuildWorkSetRunningRequiresOneConfirmedRun(t *testing.T) {
	taskID := modulecore.NewTaskID()
	input := validBuildInput()
	input.Tasks = []domaintask.Task{activeTask(taskID, domaintask.StatusRunning)}
	if _, err := BuildWorkSet(input); err == nil {
		t.Fatal("BuildWorkSet() accepted running work without a run")
	}
	input.Runs = []domaintask.Run{
		runningRun(taskID, modulecore.NewRunID()),
		runningRun(taskID, modulecore.NewRunID()),
	}
	if _, err := BuildWorkSet(input); err == nil {
		t.Fatal("BuildWorkSet() accepted running work with two active runs")
	}
}

func TestBuildWorkSetEmptyFullSetIsNonNil(t *testing.T) {
	workSet, err := BuildWorkSet(validBuildInput())
	if err != nil {
		t.Fatalf("BuildWorkSet() error = %v", err)
	}
	if workSet.ActiveWork == nil || len(workSet.ActiveWork) != 0 {
		t.Fatalf("active_work = %#v, want non-nil empty slice", workSet.ActiveWork)
	}
}

func TestBuildWorkSetRejectsDuplicateTaskIDs(t *testing.T) {
	taskID := modulecore.NewTaskID()
	input := validBuildInput()
	input.Tasks = []domaintask.Task{
		activeTask(taskID, domaintask.StatusQueued),
		activeTask(taskID, domaintask.StatusBlocked),
	}
	if _, err := BuildWorkSet(input); err == nil {
		t.Fatal("BuildWorkSet() accepted duplicate task ids")
	}
}

type fakeTaskRunSource struct {
	tasks []domaintask.Task
	runs  []domaintask.Run
}

func (s fakeTaskRunSource) ListTasks(ctx context.Context, filter domaintask.Filter) ([]domaintask.Task, error) {
	var result []domaintask.Task
	for _, task := range s.tasks {
		if (filter.Status == "" || task.Status == filter.Status) &&
			(filter.Assignee == "" || task.Assignee == filter.Assignee) {
			result = append(result, task)
		}
	}
	return result, nil
}

func (s fakeTaskRunSource) ListRuns(ctx context.Context, filter domaintask.RunFilter) ([]domaintask.Run, error) {
	var result []domaintask.Run
	for _, run := range s.runs {
		if filter.Status == "" || run.Status == filter.Status {
			result = append(result, run)
		}
	}
	return result, nil
}

func TestBuildFullWorkSetReadsCanonicalStore(t *testing.T) {
	taskID := modulecore.NewTaskID()
	runID := modulecore.NewRunID()
	source := fakeTaskRunSource{
		tasks: []domaintask.Task{activeTask(taskID, domaintask.StatusRunning)},
		runs:  []domaintask.Run{runningRun(taskID, runID)},
	}
	input := validBuildInput()
	input.RequiresHousePresence = map[modulecore.TaskID]bool{taskID: true}
	workSet, err := BuildFullWorkSet(context.Background(), source, input)
	if err != nil {
		t.Fatalf("BuildFullWorkSet() error = %v", err)
	}
	if len(workSet.ActiveWork) != 1 || workSet.ActiveWork[0].RunID != string(runID) {
		t.Fatalf("active work = %+v", workSet.ActiveWork)
	}
}

func TestBuildLifePolicyValidatesFallbackSubset(t *testing.T) {
	input := BuildLifePolicyInput{
		WorldID:                        "world-house",
		ActorID:                        "mio",
		SourceEpoch:                    "core-epoch-1",
		SourceRevision:                 2,
		ValidUntil:                     time.Date(2026, 10, 20, 13, 0, 0, 0, time.UTC),
		PersonaRevision:                "persona-r1",
		ConditionProfileRef:            "condition-r1",
		AllowedNonCognitiveActivityIDs: []string{"read", "rest", "walk"},
		ExpiredFallbackActivityIDs:     []string{"rest"},
	}
	policy, err := BuildLifePolicy(input)
	if err != nil {
		t.Fatalf("BuildLifePolicy() error = %v", err)
	}
	if policy.PersonaRevision != "persona-r1" || len(policy.AllowedNonCognitiveActivityIDs) != 3 {
		t.Fatalf("policy = %+v", policy)
	}
	input.ExpiredFallbackActivityIDs = []string{"run"}
	if _, err := BuildLifePolicy(input); err == nil {
		t.Fatal("BuildLifePolicy() accepted a fallback outside allowed activities")
	}
}

// Failure knowledge: docs/world-r08-r10-open-decisions.md D-07 (a stale or
// expired WorkSet is unknown; nothing infers success, completion, or "no
// assignment") and CORE's canonical architecture doc.
//
// Failure / Problem: BuildFullWorkSet reads the complete CORE Task/Run set to
// build one full snapshot. A Task store that fails mid-read could be reported
// as a full set once the error is swallowed, and a fresh full set with no items
// legitimately means "nothing assigned right now", so the two become
// indistinguishable to the caller.
//
// Cause: the source boundary had no fail-closed test, so an unavailable store
// could become an empty WorkSet and reach WORLD as an assignment decision.
//
// Lesson / Invariant: a source failure must fail the call with no WorkSet
// returned. Only a store that answered may produce a full set, including a
// legitimately empty one.
//
// Enforcement: this test drives the source boundary with a failing store and
// requires the wrapped error and a zero WorkSet for both the Task and the Run
// read.
//
// Tests: TestBuildFullWorkSetFailsWhenSourceFails.

// failingTaskRunSource is a Task/Run owner view that refuses to answer, standing
// in for a stale or unavailable store.
type failingTaskRunSource struct {
	tasksErr error
	runsErr  error
}

func (s failingTaskRunSource) ListTasks(context.Context, domaintask.Filter) ([]domaintask.Task, error) {
	return nil, s.tasksErr
}

func (s failingTaskRunSource) ListRuns(context.Context, domaintask.RunFilter) ([]domaintask.Run, error) {
	return nil, s.runsErr
}

func TestBuildFullWorkSetFailsWhenSourceFails(t *testing.T) {
	unavailable := errors.New("task store unavailable")

	tests := []struct {
		name   string
		source failingTaskRunSource
	}{
		{name: "task list fails", source: failingTaskRunSource{tasksErr: unavailable}},
		{name: "run list fails", source: failingTaskRunSource{runsErr: unavailable}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workSet, err := BuildFullWorkSet(context.Background(), test.source, validBuildInput())
			if !errors.Is(err, unavailable) {
				t.Fatalf("BuildFullWorkSet() error = %v, want the source failure", err)
			}
			if workSet.ActiveWork != nil {
				t.Fatalf("a failed read returned active work %#v, want no WorkSet", workSet.ActiveWork)
			}
		})
	}
}
