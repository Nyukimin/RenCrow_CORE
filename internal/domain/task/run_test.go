package task

import (
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestRunValidatesCanonicalIdentityAndTerminalTimestamp(t *testing.T) {
	now := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	run := Run{
		RunID:       modulecore.NewRunID(),
		TaskID:      modulecore.NewTaskID(),
		StartReason: RunStartReasonFirst,
		Status:      RunStatusRunning,
		StartedAt:   now,
	}
	if err := run.Validate(); err != nil {
		t.Fatalf("valid running run rejected: %v", err)
	}

	completedAt := now.Add(time.Minute)
	run.Status = RunStatusSucceeded
	run.CompletedAt = &completedAt
	if err := run.Validate(); err != nil {
		t.Fatalf("valid terminal run rejected: %v", err)
	}

	run.RunID = modulecore.RunID("run_not-a-uuid")
	if err := run.Validate(); err == nil {
		t.Fatal("non-canonical run ID accepted")
	}

	run.RunID = modulecore.NewRunID()
	run.CompletedAt = nil
	if err := run.Validate(); err == nil {
		t.Fatal("terminal run without completed_at accepted")
	}
}

func TestRunStartReasonsAndStatusesAreClosedSet(t *testing.T) {
	reasons := []RunStartReason{
		RunStartReasonFirst,
		RunStartReasonProcessRestartResume,
		RunStartReasonLeaseReacquire,
		RunStartReasonAgentReassignment,
		RunStartReasonCheckpointResume,
		RunStartReasonExplicitRerun,
	}
	statuses := []RunStatus{
		RunStatusRunning,
		RunStatusSucceeded,
		RunStatusFailed,
		RunStatusCancelled,
		RunStatusWaiting,
		RunStatusBlocked,
		RunStatusInterrupted,
		RunStatusReassigned,
		RunStatusSuperseded,
	}
	now := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	for _, reason := range reasons {
		for _, status := range statuses {
			run := Run{
				RunID:       modulecore.NewRunID(),
				TaskID:      modulecore.NewTaskID(),
				StartReason: reason,
				Status:      status,
				StartedAt:   now,
			}
			if status != RunStatusRunning {
				completedAt := now.Add(time.Minute)
				run.CompletedAt = &completedAt
			}
			if err := run.Validate(); err != nil {
				t.Fatalf("reason=%s status=%s rejected: %v", reason, status, err)
			}
		}
	}
}
