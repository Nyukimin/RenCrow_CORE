package task

import (
	"encoding/json"
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

func TestRunValidatesOptionalStartCheckpointSHA256(t *testing.T) {
	now := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	run := Run{
		RunID:                 modulecore.NewRunID(),
		TaskID:                modulecore.NewTaskID(),
		StartReason:           RunStartReasonCheckpointResume,
		Status:                RunStatusRunning,
		StartedAt:             now,
		StartCheckpointSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	if err := run.Validate(); err != nil {
		t.Fatalf("valid checkpoint digest rejected: %v", err)
	}
	encoded, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) == "" || !containsJSONField(string(encoded), "start_checkpoint_sha256") {
		t.Fatalf("checkpoint digest was not serialized: %s", encoded)
	}

	for _, digest := range []string{
		"0123456789ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef",
		"0123456789abcdef",
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdeg",
	} {
		run.StartCheckpointSHA256 = digest
		if err := run.Validate(); err == nil {
			t.Fatalf("invalid checkpoint digest accepted: %q", digest)
		}
	}
}

func containsJSONField(encoded, field string) bool {
	return len(encoded) > 0 && string(encoded) != "null" &&
		// Keep this assertion independent of JSON object ordering.
		func() bool {
			var value map[string]any
			if err := json.Unmarshal([]byte(encoded), &value); err != nil {
				return false
			}
			_, ok := value[field]
			return ok
		}()
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

func TestCanStartRunFromCheckpointUsesCanonicalPredecessorPolicy(t *testing.T) {
	allowed := []struct {
		status RunStatus
		reason RunStartReason
	}{
		{RunStatusWaiting, RunStartReasonCheckpointResume},
		{RunStatusInterrupted, RunStartReasonCheckpointResume},
		{RunStatusSucceeded, RunStartReasonExplicitRerun},
		{RunStatusFailed, RunStartReasonExplicitRerun},
		{RunStatusCancelled, RunStartReasonExplicitRerun},
	}
	for _, item := range allowed {
		if !CanStartRunFromCheckpoint(item.status, item.reason) {
			t.Errorf("CanStartRunFromCheckpoint(%s, %s) = false, want true", item.status, item.reason)
		}
	}
	for _, item := range []struct {
		status RunStatus
		reason RunStartReason
	}{
		{RunStatusRunning, RunStartReasonCheckpointResume},
		{RunStatusSucceeded, RunStartReasonCheckpointResume},
		{RunStatusWaiting, RunStartReasonExplicitRerun},
		{RunStatusBlocked, RunStartReasonExplicitRerun},
		{RunStatusWaiting, RunStartReasonFirst},
	} {
		if CanStartRunFromCheckpoint(item.status, item.reason) {
			t.Errorf("CanStartRunFromCheckpoint(%s, %s) = true, want false", item.status, item.reason)
		}
	}
}
