package runmigration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	core "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type checkpointQueueReference struct {
	id          core.CheckpointID
	revision    int
	summary     string
	nextAction  string
	sourceRunID string
}

// prepareAgentRunCheckpoints resolves legacy AgentRun checkpoint identities
// from the immutable source rows before any execution identity is rewritten.
// Only AgentRuns with resume_policy=checkpoint and a missing checkpoint_id are
// returned for injection; explicit IDs remain source data and are merely
// checked for cross-Task conflicts. Successor Runs of one Task may share a checkpoint.
func (c *cohort) prepareAgentRunCheckpoints(ctx context.Context, d *databaseInput) (map[string]core.CheckpointID, error) {
	if d.role != "superagent" {
		return nil, nil
	}
	runRows, err := payloadRows(ctx, d, "agent_run")
	if err != nil {
		return nil, err
	}
	queueRows, err := payloadRows(ctx, d, "run_queue")
	if err != nil {
		return nil, err
	}
	queueByRun := map[string][]checkpointQueueReference{}
	checkpointOwners := map[string]string{}
	for _, m := range runRows {
		runID := textField(m, "run_id")
		checkpointID, hasCheckpoint, err := optionalCheckpointID(m)
		if err != nil {
			return nil, err
		}
		if !hasCheckpoint {
			continue
		}
		if runID == "" {
			return nil, errors.New("checkpoint AgentRun has no source Run identity")
		}
		if err := c.registerCheckpointOwner(checkpointOwners, string(checkpointID), runID); err != nil {
			return nil, err
		}
	}
	for _, m := range queueRows {
		if strings.TrimSpace(textField(m, "action")) != "resume" {
			continue
		}
		runID := textField(m, "run_id")
		if runID == "" {
			continue
		}
		checkpointID, hasCheckpoint, err := optionalCheckpointID(m)
		if err != nil {
			return nil, err
		}
		if !hasCheckpoint {
			return nil, errors.New("resume queue reference has no checkpoint ID")
		}
		revision, summary, nextAction, err := checkpointQueueMetadata(m)
		if err != nil {
			return nil, err
		}
		if err := c.registerCheckpointOwner(checkpointOwners, string(checkpointID), runID); err != nil {
			return nil, err
		}
		queueByRun[runID] = append(queueByRun[runID], checkpointQueueReference{
			id: checkpointID, revision: revision, summary: summary, nextAction: nextAction, sourceRunID: runID,
		})
	}

	prepared := map[string]core.CheckpointID{}
	for _, m := range runRows {
		runID := textField(m, "run_id")
		checkpointID, hasCheckpoint, err := optionalCheckpointID(m)
		if err != nil {
			return nil, err
		}
		policy, _, err := optionalStringField(m, "resume_policy")
		if err != nil {
			return nil, err
		}
		resume := strings.TrimSpace(policy) == "checkpoint"
		if !resume {
			continue
		}
		if runID == "" {
			return nil, errors.New("checkpoint resume has no source Run identity")
		}
		revision, summary, nextAction, err := checkpointAgentMetadata(m)
		if err != nil {
			return nil, err
		}
		if hasCheckpoint {
			if err := validateCheckpointQueueAgreement(queueByRun[runID], checkpointID, revision, summary, nextAction); err != nil {
				return nil, err
			}
			continue
		}
		refs := queueByRun[runID]
		if len(refs) > 1 {
			return nil, fmt.Errorf("ambiguous resume queue references for source Run %q", runID)
		}
		if len(refs) == 1 {
			ref := refs[0]
			if ref.revision != revision || ref.summary != summary || ref.nextAction != nextAction {
				return nil, fmt.Errorf("resume queue metadata disagrees with source Run %q", runID)
			}
			prepared[runID] = ref.id
			continue
		}
		encoded, err := json.Marshal([]any{runID, revision})
		if err != nil {
			return nil, err
		}
		mapped, err := core.NewMigrationID(core.CanonicalCheckpointID, "superagent.agent_run", "checkpoint", string(encoded))
		if err != nil {
			return nil, err
		}
		if err := c.registerCheckpointOwner(checkpointOwners, mapped, runID); err != nil {
			return nil, err
		}
		prepared[runID] = core.CheckpointID(mapped)
	}
	return prepared, nil
}

func optionalCheckpointID(m map[string]json.RawMessage) (core.CheckpointID, bool, error) {
	raw, ok := m["checkpoint_id"]
	if !ok || isJSONNull(raw) {
		return "", false, nil
	}
	value, _, err := optionalStringField(m, "checkpoint_id")
	if err != nil {
		return "", false, err
	}
	if value == "" {
		return "", false, nil
	}
	id := core.CheckpointID(value)
	if err := id.Validate(); err != nil {
		return "", false, fmt.Errorf("checkpoint_id is invalid: %w", err)
	}
	return id, true, nil
}

func optionalStringField(m map[string]json.RawMessage, key string) (string, bool, error) {
	raw, ok := m[key]
	if !ok || isJSONNull(raw) {
		return "", false, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", true, fmt.Errorf("%s must be a string", key)
	}
	return value, true, nil
}

func isJSONNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

func checkpointAgentMetadata(m map[string]json.RawMessage) (int, string, string, error) {
	revision, summary, nextAction, err := checkpointQueueMetadata(m)
	if err != nil {
		return 0, "", "", err
	}
	if raw, ok := m["last_checkpoint_at"]; ok && !isJSONNull(raw) {
		var atTime time.Time
		if err := json.Unmarshal(raw, &atTime); err != nil {
			return 0, "", "", errors.New("last_checkpoint_at must be a timestamp")
		}
		if atTime.IsZero() {
			return 0, "", "", errors.New("checkpoint resume requires revision, summary, next_action, and last_checkpoint_at")
		}
	} else {
		return 0, "", "", errors.New("checkpoint resume requires revision, summary, next_action, and last_checkpoint_at")
	}
	if revision <= 0 || strings.TrimSpace(summary) == "" || strings.TrimSpace(nextAction) == "" {
		return 0, "", "", errors.New("checkpoint resume requires revision, summary, next_action, and last_checkpoint_at")
	}
	return revision, summary, nextAction, nil
}

func checkpointQueueMetadata(m map[string]json.RawMessage) (int, string, string, error) {
	var revision int
	if raw, ok := m["checkpoint_revision"]; ok && !isJSONNull(raw) {
		if err := json.Unmarshal(raw, &revision); err != nil {
			return 0, "", "", errors.New("resume queue checkpoint_revision must be an integer")
		}
	}
	summary, _, err := optionalStringField(m, "checkpoint_summary")
	if err != nil {
		return 0, "", "", err
	}
	nextAction, _, err := optionalStringField(m, "next_action")
	if err != nil {
		return 0, "", "", err
	}
	return revision, summary, nextAction, nil
}

func validateCheckpointQueueAgreement(refs []checkpointQueueReference, checkpointID core.CheckpointID, revision int, summary, nextAction string) error {
	if len(refs) > 1 {
		return errors.New("ambiguous resume queue references for source Run")
	}
	if len(refs) == 0 {
		return nil
	}
	ref := refs[0]
	if ref.id != checkpointID {
		return errors.New("resume queue checkpoint ID conflicts with source Run")
	}
	if ref.revision != revision || ref.summary != summary || ref.nextAction != nextAction {
		return fmt.Errorf("resume queue metadata disagrees with source Run %q", ref.sourceRunID)
	}
	return nil
}

func (c *cohort) registerCheckpointOwner(owners map[string]string, checkpointID, sourceRunID string) error {
	if checkpointID == "" || sourceRunID == "" {
		return nil
	}
	run, err := c.resolve("superagent", "run_id", sourceRunID)
	if err != nil {
		return err
	}
	owner := string(run.TaskID)
	if previous, ok := owners[checkpointID]; ok && previous != owner {
		return errors.New("checkpoint ID belongs to different Tasks")
	}
	owners[checkpointID] = owner
	return nil
}

func injectAgentRunCheckpoint(m map[string]json.RawMessage, prepared map[string]core.CheckpointID, c *cohort) {
	runID := textField(m, "run_id")
	checkpointID, ok := prepared[runID]
	if !ok {
		return
	}
	setField(m, "checkpoint_id", checkpointID)
	c.counts["mapped_checkpoints"]++
}
