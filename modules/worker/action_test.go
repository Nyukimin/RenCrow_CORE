package worker

import (
	"testing"
	"time"
)

func TestExtractProposalPatchArgs(t *testing.T) {
	got, err := ExtractProposalPatchArgs(Action{
		Tool: ToolProposalPatch,
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
	_, err := ExtractProposalPatchArgs(Action{Tool: "tts"})
	if err == nil {
		t.Fatal("expected unsupported tool error")
	}
}

func TestExtractProposalPatchArgsRequiresPlanAndPatch(t *testing.T) {
	_, err := ExtractProposalPatchArgs(Action{
		Tool:      ToolProposalPatch,
		Arguments: map[string]any{"plan": "plan only"},
	})
	if err == nil {
		t.Fatal("expected missing patch error")
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
	got := BuildFailedResult("job-1", StatusDenied, "unsupported", startedAt, finishedAt)
	if got.JobID != "job-1" || got.Status != StatusDenied || got.Error != "unsupported" || !got.StartedAt.Equal(startedAt) || !got.FinishedAt.Equal(finishedAt) {
		t.Fatalf("unexpected failed result: %+v", got)
	}
}

func TestBuildPatchExecutionResult(t *testing.T) {
	startedAt := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	finishedAt := startedAt.Add(time.Second)
	got := BuildPatchExecutionResult("job-1", &PatchExecutionSummary{Success: true, Summary: "done", ExecutedCmds: 1}, startedAt, finishedAt)
	if got.JobID != "job-1" || got.Status != StatusSucceeded || got.Output != "done" || got.Metadata["executed_cmds"] != 1 {
		t.Fatalf("unexpected patch result: %+v", got)
	}
}

func TestBuildActionErrorResultDeniesUnsupportedTool(t *testing.T) {
	startedAt := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	got := BuildActionErrorResult(Action{JobID: "job-1", Tool: "tts"}, errString("unsupported"), startedAt, startedAt)
	if got.Status != StatusDenied || got.Error != "unsupported" {
		t.Fatalf("unexpected action error result: %+v", got)
	}
}

func TestBuildActionErrorResultFailsSupportedTool(t *testing.T) {
	startedAt := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	got := BuildActionErrorResult(Action{JobID: "job-1", Tool: ToolProposalPatch}, errString("missing patch"), startedAt, startedAt)
	if got.Status != StatusFailed || got.Error != "missing patch" {
		t.Fatalf("unexpected action error result: %+v", got)
	}
}

type errString string

func (e errString) Error() string {
	return string(e)
}
