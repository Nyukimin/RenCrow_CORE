package task

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func typedDraftWithoutOwnerIDs(t *testing.T) (Task, SharedRoleContext, json.RawMessage) {
	t.Helper()
	goalID := modulecore.NewGoalID()
	workstreamID := modulecore.NewWorkstreamID()
	timing := modulecore.TaskTiming{AllowedWindows: []modulecore.TaskTimeWindow{}}
	timingDigest, err := TaskTimingSHA256(timing)
	if err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"kind":"movie"}`)
	inputDigest, err := TaskCenterKindInputSHA256(input)
	if err != nil {
		t.Fatal(err)
	}
	draft := validTask()
	draft.TaskID = ""
	draft.CreatedAt = time.Time{}
	draft.UpdatedAt = time.Time{}
	draft.Title = "movie lookup"
	draft.GoalID = goalID
	draft.WorkstreamID = workstreamID
	draft.Timing = &timing
	draft.TaskCenterExecution = &TaskCenterExecution{
		KindID: "movie.catalog.lookup", KindInputVersion: 1, KindInputSHA256: inputDigest,
		SourceGoalID: goalID, PreparedRevision: 4, AssignmentEpoch: 0, TimingSHA256: timingDigest,
	}
	shared := SharedRoleContext{UserIntent: "lookup frozen catalog input"}
	return draft, shared, input
}

func TestTaskCenterExecutionValidatesTypedSourceAndTiming(t *testing.T) {
	goalID := modulecore.NewGoalID()
	workstreamID := modulecore.NewWorkstreamID()
	timing := modulecore.TaskTiming{AllowedWindows: []modulecore.TaskTimeWindow{}}
	timingDigest, err := TaskTimingSHA256(timing)
	if err != nil {
		t.Fatal(err)
	}
	kindDigest, err := TaskCenterKindInputSHA256(json.RawMessage(`{"kind":"movie"}`))
	if err != nil {
		t.Fatal(err)
	}
	value := validTask()
	value.GoalID, value.WorkstreamID, value.Timing = goalID, workstreamID, &timing
	value.TaskCenterExecution = &TaskCenterExecution{
		KindID: "movie.catalog.lookup", KindInputVersion: 1, KindInputSHA256: kindDigest,
		SourceGoalID: goalID, PreparedRevision: 1, AssignmentEpoch: 0, TimingSHA256: timingDigest,
	}
	if err := value.Validate(); err != nil {
		t.Fatalf("valid TaskCenter execution descriptor: %v", err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Task
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("round-tripped typed Task: %v", err)
	}

	changedTiming := *decoded.Timing
	changedTiming.Timezone = "UTC"
	decoded.Timing = &changedTiming
	if err := decoded.Validate(); err == nil {
		t.Fatal("typed Task accepted timing that differs from its immutable digest")
	}
}

func TestTaskCenterExecutionRequiresCanonicalDigestsAndExactGoal(t *testing.T) {
	goalID := modulecore.NewGoalID()
	timing := modulecore.TaskTiming{AllowedWindows: []modulecore.TaskTimeWindow{}}
	timingDigest, _ := TaskTimingSHA256(timing)
	kindDigest, _ := TaskCenterKindInputSHA256(json.RawMessage(`{"x":1}`))
	value := validTask()
	value.GoalID = goalID
	value.WorkstreamID = modulecore.NewWorkstreamID()
	value.Timing = &timing
	value.TaskCenterExecution = &TaskCenterExecution{
		KindID: "kind.one", KindInputVersion: 1, KindInputSHA256: kindDigest,
		SourceGoalID: goalID, PreparedRevision: 1, TimingSHA256: timingDigest,
	}
	for name, mutate := range map[string]func(*Task){
		"wrong goal":       func(task *Task) { task.TaskCenterExecution.SourceGoalID = modulecore.NewGoalID() },
		"bad input digest": func(task *Task) { task.TaskCenterExecution.KindInputSHA256 = strings.Repeat("A", 64) },
		"missing timing":   func(task *Task) { task.Timing = nil },
		"bad kind version": func(task *Task) { task.TaskCenterExecution.KindInputVersion = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := value
			execution := *value.TaskCenterExecution
			candidate.TaskCenterExecution = &execution
			mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("invalid typed Task was accepted")
			}
		})
	}
}

func TestLegacyTaskJSONOmitsOptionalTimingAndTypedDescriptor(t *testing.T) {
	encoded, err := json.Marshal(validTask())
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatal(err)
	}
	if _, exists := object["timing"]; exists {
		t.Fatal("legacy Task gained a timing field")
	}
	if _, exists := object["task_center_execution"]; exists {
		t.Fatal("legacy Task gained a TaskCenter execution field")
	}
}

func TestTaskTimingReadyLeavesLegacyUnknownAndChecksExplicitSnapshot(t *testing.T) {
	if err := validTask().ValidateStartTiming(time.Now()); err != nil {
		t.Fatalf("nil legacy timing should remain unspecified: %v", err)
	}
	start := time.Now().Add(time.Hour)
	task := validTask()
	task.Timing = &modulecore.TaskTiming{ScheduledStartAt: &start, Timezone: "UTC", AllowedWindows: []modulecore.TaskTimeWindow{}}
	if err := task.ValidateStartTiming(time.Now()); err == nil {
		t.Fatal("Task started before scheduled_start_at")
	}
}

func TestGameTaskAdmitsOneCompletedSessionCondition(t *testing.T) {
	task := validTask()
	task.Route = RouteGAME
	task.Timing = &modulecore.TaskTiming{CompletionCondition: &modulecore.TaskCompletionCondition{Kind: "count", TargetCount: 1, CountUnit: "game"}}
	if err := task.ValidateStartTiming(time.Now()); err != nil {
		t.Fatalf("single GAME session should be supported by the game owner: %v", err)
	}
	task.Timing.CompletionCondition.TargetCount = 2
	if err := task.ValidateStartTiming(time.Now()); err == nil {
		t.Fatal("multiple games must not be admitted as one session")
	}
	task.Route = RouteCHAT
	task.Timing.CompletionCondition.TargetCount = 1
	if err := task.ValidateStartTiming(time.Now()); err == nil {
		t.Fatal("a non-game owner must not claim GAME session support")
	}
}

func TestGameTaskAdmitsVerifiedUntilGameCompletes(t *testing.T) {
	task := validTask()
	task.Route = RouteGAME
	task.Timing = &modulecore.TaskTiming{CompletionCondition: &modulecore.TaskCompletionCondition{Kind: "until_complete", Criteria: "game_session_completed"}}
	if err := task.ValidateStartTiming(time.Now()); err != nil {
		t.Fatalf("verified session completion should be supported: %v", err)
	}
	task.Timing.CompletionCondition.Criteria = "arbitrary unsupported outcome"
	if err := task.ValidateStartTiming(time.Now()); err == nil {
		t.Fatal("unverified completion criteria must not be admitted")
	}
}

func TestTaskCenterIdentityHashesPreCreationBlankIDsAndMatchesOwnerGeneratedState(t *testing.T) {
	draft, shared, input := typedDraftWithoutOwnerIDs(t)
	key := modulecore.NewRequestID()
	operationID, hash, err := BuildTaskCenterExecutionOperationIdentity(key, 4, draft, shared, input)
	if err != nil {
		t.Fatalf("BuildTaskCenterExecutionOperationIdentity: %v", err)
	}
	replayedID, replayedHash, err := BuildTaskCenterExecutionOperationIdentity(key, 4, draft, shared, input)
	if err != nil || replayedID != operationID || replayedHash != hash {
		t.Fatalf("identity replay = (%q,%q,%v), want (%q,%q,nil)", replayedID, replayedHash, err, operationID, hash)
	}
	changedShared := shared
	changedShared.UserIntent = "different frozen context"
	_, changedHash, err := BuildTaskCenterExecutionOperationIdentity(key, 4, draft, changedShared, input)
	if err != nil || changedHash == hash {
		t.Fatalf("changed shared context hash=%q err=%v, want conflict digest", changedHash, err)
	}

	stored := draft
	stored.TaskID = modulecore.NewTaskID()
	stored.ApplyDefaults(time.Date(2026, 11, 18, 1, 2, 3, 0, time.UTC))
	started := stored.CreatedAt
	stored.Status = StatusSucceeded
	stored.StartedAt = &started
	finished := started.Add(time.Minute)
	stored.FinishedAt = &finished
	stored.UpdatedAt = finished
	stored.Summary = "lookup persisted"
	if !TaskCenterDraftMatchesStored(draft, stored) {
		t.Fatal("stored typed Task with owner-generated identity/lifecycle fields did not match its frozen draft")
	}
	stored.Assignee = "different-owner"
	if TaskCenterDraftMatchesStored(draft, stored) {
		t.Fatal("stored Task with changed assignee matched frozen draft")
	}
	wrongID := draft
	wrongID.TaskID = modulecore.NewTaskID()
	if TaskCenterDraftMatchesStored(wrongID, stored) {
		t.Fatal("stored Task matched a frozen draft with a different explicit TaskID")
	}
}
