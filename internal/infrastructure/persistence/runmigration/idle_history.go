package runmigration

import (
	"encoding/json"
	"errors"
	"time"
)

type legacyIdleRevision struct {
	episode, actor, task, status   string
	revision                       int
	created, updated, productionAt time.Time
}

// Legacy generations reused one execution identity across repair revisions.
// Bind that historical execution once, after reading its production history.
// Playback updates do not move execution completion; canonical Runs are never
// reinterpreted from projections. Every original artifact row is still emitted.
func (c *cohort) prepareIdleHistory(b []byte) error {
	latest := map[string]legacyIdleRevision{}
	var order []string
	for _, line := range jsonLines(b) {
		var m map[string]json.RawMessage
		if err := strictJSON(line, &m); err != nil {
			return err
		}
		id := textField(m, "generation_id")
		if id == "" {
			continue
		}
		if textField(m, "run_id") != "" {
			return errors.New("multiple execution identity fields")
		}
		current := legacyIdleRevision{episode: textField(m, "episode_id"), actor: textField(m, "initiated_by"), task: textField(m, "task_id"), status: textField(m, "production_status"), created: timeField(m, "created_at"), updated: timeField(m, "updated_at")}
		if err := json.Unmarshal(m["revision"], &current.revision); err != nil || current.revision < 1 {
			return errors.New("invalid legacy IdleChat revision")
		}
		if current.episode == "" || current.created.IsZero() || current.updated.IsZero() || current.updated.Before(current.created) {
			return errors.New("invalid legacy IdleChat history time or episode")
		}
		current.productionAt = current.updated
		if _, err := migrationRunStatus(current.status); err != nil {
			return err
		}
		if previous, ok := latest[id]; ok {
			if current.episode != previous.episode || current.actor != previous.actor || current.task != previous.task || !current.created.Equal(previous.created) {
				return errors.New("legacy IdleChat history ownership changed")
			}
			if current.revision < previous.revision || current.updated.Before(previous.updated) {
				return errors.New("legacy IdleChat history goes backwards")
			}
			if current.revision == previous.revision {
				if current.status != previous.status {
					return errors.New("legacy IdleChat revision has conflicting production status")
				}
				current.productionAt = previous.productionAt
			}
		} else {
			order = append(order, id)
		}
		latest[id] = current
	}
	for _, id := range order {
		row := latest[id]
		if _, err := c.bind("idlechat", "generation_id", id, row.task, row.actor, row.status, row.created, row.productionAt); err != nil {
			return err
		}
	}
	return nil
}
