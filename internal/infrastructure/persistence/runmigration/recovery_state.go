package runmigration

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// In source revision 43096d9 the recovery owner changed an abandoned running
// projection to blocked when no durable checkpoint existed, without recording
// execution completion. Close that exact legacy state at the migration boundary;
// do not claim that SnapshotAt was the original execution's historical end.
func closeLegacyRecoveryBlock(m map[string]json.RawMessage, snapshotAt time.Time) error {
	if textField(m, "agent_type") != "LeadAgent" || !strings.HasPrefix(textField(m, "run_id"), "run_lead_") ||
		textField(m, "task_id") != "" || textField(m, "status") != "blocked" ||
		textField(m, "summary") != "restart resume blocked: durable checkpoint is unavailable" {
		return nil
	}
	var completed time.Time
	if raw, ok := m["completed_at"]; ok {
		if err := json.Unmarshal(raw, &completed); err != nil {
			return errors.New("invalid legacy recovery completion time")
		}
	}
	if !completed.IsZero() {
		return nil
	}
	// This supported producer shape has no checkpoint. Partially populated or
	// contradictory checkpoint state needs separate evidence, not normalization.
	for _, field := range []string{"resume_policy", "checkpoint_id", "checkpoint_revision", "checkpoint_summary", "next_action"} {
		if _, ok := m[field]; ok {
			return errors.New("legacy recovery block contains checkpoint metadata")
		}
	}
	if raw, ok := m["last_checkpoint_at"]; ok {
		var checkpointAt time.Time
		if err := json.Unmarshal(raw, &checkpointAt); err != nil || !checkpointAt.IsZero() {
			return errors.New("legacy recovery block has a checkpoint timestamp")
		}
	}
	started := timeField(m, "started_at")
	if started.IsZero() || snapshotAt.IsZero() || snapshotAt.Before(started) {
		return errors.New("invalid legacy recovery snapshot boundary")
	}
	setField(m, "status", "interrupted")
	setField(m, "completed_at", snapshotAt)
	return nil
}
