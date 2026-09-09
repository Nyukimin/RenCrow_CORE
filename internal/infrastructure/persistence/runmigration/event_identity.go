package runmigration

import (
	"errors"
	"strconv"
	"strings"

	task "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	core "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Event identity is projected from the execution owner, never from a mechanism
// label. In particular a child event belongs to the child's Run and Task.
func (c *cohort) bindEventIdentity(e *core.EventEnvelope) error {
	if _, err := c.restoreLegacyAuditEvent(e); err != nil {
		return err
	}
	var owner task.Run
	hasOwner := false
	bind := func(reference string) error {
		if reference == "" {
			return nil
		}
		r, err := c.resolve("superagent", "run_id", reference)
		if err != nil {
			return err
		}
		if hasOwner && r.RunID != owner.RunID {
			return errors.New("Event execution references conflict")
		}
		owner, hasOwner = r, true
		return nil
	}
	if e.RunID != "" {
		r, err := c.resolve("", "run_id", string(e.RunID))
		if err != nil {
			return err
		}
		owner, hasOwner = r, true
	}
	if e.ComponentID == "superagent" {
		known := false
		switch e.EventType {
		case "lead_agent.started", "lead_agent.completed", "lead_agent.failed", "lead_agent.resumed",
			"lead_agent_started", "lead_agent_completed", "lead_agent_failed", "lead_agent_resumed",
			"subagent.started", "subagent.completed", "subagent.failed", "subagent_started", "subagent_completed", "subagent_failed",
			"run_queue.claimed", "run_queue.completed", "run_queue.failed", "run_queue_claimed", "run_queue_completed", "run_queue_failed",
			"owner_route_e2e_observed", "owner_route_json_handoff_verified", "owner_route_recalled":
			known = true
		}
		if known {
			for _, key := range []string{"run_id", "run_reference"} {
				if raw, exists := e.Payload[key]; exists {
					value, ok := raw.(string)
					if !ok || value == "" {
						return errors.New("invalid Event execution reference")
					}
					if key == "run_reference" && strings.HasPrefix(e.EventType, "run_queue.") {
						if queue, ok := e.Payload["queue_reference"].(string); ok && queue != "" {
							id, err := core.NewMigrationID(core.CanonicalQueueItemID, "run_queue", "queue_item_id", queue)
							if err != nil {
								return err
							}
							value, err = c.provenQueueRun(id, value)
							if err != nil {
								return err
							}
						}
					}
					if err := bind(value); err != nil {
						return err
					}
					delete(e.Payload, key)
				}
			}
			child := strings.HasPrefix(e.EventType, "subagent.") || strings.HasPrefix(e.EventType, "subagent_")
			if child && !(hasOwner && e.TaskID == owner.TaskID && c.canonicalRuns[owner.RunID]) {
				reference, _ := e.Payload["task_reference"].(string)
				if reference == "" {
					var err error
					proof := *e
					if source, ok := c.legacyTraceEvents[e.EventID]; ok {
						proof.Payload = map[string]any{"event_id": source.EventID}
					}
					reference, err = legacyChildEventReference(proof)
					if err != nil {
						return err
					}
				}
				r, err := c.resolve("superagent", "subagent_id", reference)
				if err != nil {
					return err
				}
				if !hasOwner || c.tasks[r.TaskID].ParentTaskID != owner.TaskID {
					return errors.New("child Event parent ownership mismatch")
				}
				owner = r
				delete(e.Payload, "task_reference")
			}
		}
	}
	for _, key := range []string{"run_id", "parent_run_id", "trace_run_id", "generation_id", "subagent_id"} {
		if _, ok := e.Payload[key]; ok {
			return errors.New("legacy Event payload requires type-specific conversion")
		}
	}
	if hasOwner {
		if e.TaskID != "" && e.TaskID != owner.TaskID {
			return errors.New("Event Task ownership conflicts")
		}
		if e.ActorID != "" && (!strings.EqualFold(e.ActorID, owner.Assignee) || e.ActorKind != "agent") {
			return errors.New("Event Actor ownership conflicts")
		}
		e.TaskID, e.RunID = owner.TaskID, owner.RunID
		e.ActorKind, e.ActorID = "agent", strings.ToLower(owner.Assignee)
	}
	if e.TaskID != "" {
		if _, ok := c.tasks[e.TaskID]; !ok {
			return errors.New("orphan Event Task")
		}
	}
	return nil
}

func legacyChildEventReference(e core.EventEnvelope) (string, error) {
	source, ok := e.Payload["event_id"].(string)
	if !ok {
		return "", errors.New("legacy child Event needs original source identity")
	}
	mapped, err := core.NewMigrationID(core.CanonicalEventID, "trace_event", "event_id", source)
	if err != nil || mapped != string(e.EventID) {
		return "", errors.New("legacy child Event source identity mismatch")
	}
	tail, ok := strings.CutPrefix(source, "evt_subagent_")
	if !ok {
		return "", errors.New("unknown legacy child Event producer")
	}
	status, tail, ok := strings.Cut(tail, "_")
	if !ok {
		return "", errors.New("invalid legacy child Event reference")
	}
	expected, ok := strings.CutPrefix(e.EventType, "subagent_")
	if !ok || status != expected {
		return "", errors.New("legacy child Event kind mismatch")
	}
	split := strings.LastIndex(tail, "_")
	if split < 0 {
		return "", errors.New("invalid legacy child Event timestamp")
	}
	if tail[split+1:] != strconv.FormatInt(e.OccurredAt.UnixNano(), 10) {
		return "", errors.New("legacy child Event timestamp mismatch")
	}
	return tail[:split], nil
}
