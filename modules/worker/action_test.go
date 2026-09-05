package worker

import (
	"encoding/json"
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestExtractProposalPatchArgs(t *testing.T) {
	got, err := ExtractProposalPatchArgs(Action{
		TaskID: modulecore.NewTaskID(),
		Tool:   ToolProposalPatch,
		Arguments: map[string]any{
			"plan":      " plan text ",
			"patch":     " patch text ",
			"risk":      " low ",
			"cost_hint": " cheap ",
		},
	})
	if err != nil {
		t.Fatalf("ExtractProposalPatchArgs returned error: %v", err)
	}
	if got.Plan != "plan text" || got.Patch != "patch text" || got.Risk != "low" || got.CostHint != "cheap" {
		t.Fatalf("args were not normalized: %+v", got)
	}
}

func TestExtractProposalPatchArgsRejectsUnsupportedTool(t *testing.T) {
	_, err := ExtractProposalPatchArgs(Action{TaskID: modulecore.NewTaskID(), Tool: "tts"})
	if err == nil {
		t.Fatal("expected unsupported tool error")
	}
}

func TestExtractProposalPatchArgsRequiresPlanAndPatch(t *testing.T) {
	_, err := ExtractProposalPatchArgs(Action{
		TaskID:    modulecore.NewTaskID(),
		Tool:      ToolProposalPatch,
		Arguments: map[string]any{"plan": "plan only"},
	})
	if err == nil {
		t.Fatal("expected missing patch error")
	}
}

func TestExtractProposalPatchArgsRejectsMissingOrMalformedTaskID(t *testing.T) {
	for _, taskID := range []modulecore.TaskID{"", "not-a-task-id"} {
		_, err := ExtractProposalPatchArgs(Action{
			TaskID: taskID,
			Tool:   ToolProposalPatch,
			Arguments: map[string]any{
				"plan":  "plan",
				"patch": "patch",
			},
		})
		if err == nil {
			t.Fatalf("ExtractProposalPatchArgs() accepted invalid TaskID %q", taskID)
		}
	}
}

func TestPatchExecutionResultMapping(t *testing.T) {
	summary := &PatchExecutionSummary{
		Success:       true,
		Summary:       "done",
		ExecutedCmds:  2,
		FailedCmds:    1,
		GitCommit:     "abc123",
		FailureKind:   "test",
		FailureReason: "boom",
		Retryable:     true,
		FailedIndex:   3,
	}

	if got := ResultStatusFromPatchExecution(summary); got != StatusSucceeded {
		t.Fatalf("status = %s, want %s", got, StatusSucceeded)
	}
	if got := OutputFromPatchExecution(summary); got != "done" {
		t.Fatalf("output = %q", got)
	}
	metadata := MetadataFromPatchExecution(summary)
	if metadata["executed_cmds"] != 2 || metadata["failed_cmds"] != 1 || metadata["git_commit"] != "abc123" || metadata["retryable"] != true {
		t.Fatalf("metadata not mapped: %+v", metadata)
	}
	if got := ResultStatusFromPatchExecution(nil); got != StatusFailed {
		t.Fatalf("nil status = %s, want failed", got)
	}
}

func TestBuildFailedResult(t *testing.T) {
	startedAt := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	finishedAt := startedAt.Add(time.Second)
	taskID := modulecore.NewTaskID()
	got := BuildFailedResult(taskID, StatusDenied, "unsupported", startedAt, finishedAt)
	if got.TaskID != taskID || got.Status != StatusDenied || got.Error != "unsupported" || !got.StartedAt.Equal(startedAt) || !got.FinishedAt.Equal(finishedAt) {
		t.Fatalf("unexpected failed result: %+v", got)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("Result.Validate() error = %v", err)
	}
}

func TestBuildPatchExecutionResult(t *testing.T) {
	startedAt := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	finishedAt := startedAt.Add(time.Second)
	taskID := modulecore.NewTaskID()
	got := BuildPatchExecutionResult(taskID, &PatchExecutionSummary{Success: true, Summary: "done", ExecutedCmds: 1}, startedAt, finishedAt)
	if got.TaskID != taskID || got.Status != StatusSucceeded || got.Output != "done" || got.Metadata["executed_cmds"] != 1 {
		t.Fatalf("unexpected patch result: %+v", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var projection map[string]any
	if err := json.Unmarshal(encoded, &projection); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if projection["task_id"] != taskID.String() {
		t.Fatalf("result task_id = %#v, want %q", projection["task_id"], taskID)
	}
}

func TestBuildActionErrorResultDeniesUnsupportedTool(t *testing.T) {
	startedAt := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	got := BuildActionErrorResult(Action{TaskID: modulecore.NewTaskID(), Tool: "tts"}, errString("unsupported"), startedAt, startedAt)
	if got.Status != StatusDenied || got.Error != "unsupported" {
		t.Fatalf("unexpected action error result: %+v", got)
	}
}

func TestBuildActionErrorResultFailsSupportedTool(t *testing.T) {
	startedAt := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	got := BuildActionErrorResult(Action{TaskID: modulecore.NewTaskID(), Tool: ToolProposalPatch}, errString("missing patch"), startedAt, startedAt)
	if got.Status != StatusFailed || got.Error != "missing patch" {
		t.Fatalf("unexpected action error result: %+v", got)
	}
}

func TestResultValidateRejectsMissingOrMalformedTaskID(t *testing.T) {
	for _, taskID := range []modulecore.TaskID{"", "not-a-task-id"} {
		result := Result{TaskID: taskID, Status: StatusFailed}
		if err := result.Validate(); err == nil {
			t.Fatalf("Result.Validate() accepted invalid TaskID %q", taskID)
		}
	}
}

type errString string

func (e errString) Error() string {
	return string(e)
}
