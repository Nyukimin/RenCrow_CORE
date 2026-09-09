package runmigration

import (
	"context"
	"errors"
	"sort"

	core "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Some closed LeadAgent projections were retired before Step02, while their
// owner Events remain. Reconstruct only a complete, unambiguous execution pair
// and a separately declared Actor bound by the inventory hash.
func (c *cohort) restoreEventRunHistory(ctx context.Context, databases []*databaseInput) error {
	type history struct{ start, end *core.EventEnvelope }
	histories := map[string]history{}
	for id, actor := range c.inventory.LegacyRunActors {
		if core.RunID(id).Validate() != nil || validateMigrationActor(actor) != nil {
			return errors.New("invalid historical Event Run attribution")
		}
		if len(c.rawRuns[id]) != 0 {
			return errors.New("historical Event attribution cannot replace an existing Run")
		}
		histories[id] = history{}
	}
	for _, db := range databases {
		if db.role != "events" {
			continue
		}
		rows, err := db.db.QueryContext(ctx, "SELECT envelope_json FROM event_envelope WHERE event_type IN ('lead_agent_started','lead_agent_completed','lead_agent_failed') ORDER BY event_seq")
		if err != nil {
			return err
		}
		for rows.Next() {
			var raw []byte
			if err = rows.Scan(&raw); err != nil {
				rows.Close()
				return err
			}
			var event core.EventEnvelope
			if err = strictJSON(raw, &event); err != nil {
				rows.Close()
				return err
			}
			id := string(event.RunID)
			h, wanted := histories[id]
			if !wanted {
				continue
			}
			if event.ComponentID != "superagent" || event.TaskID != "" || event.ActorID != "" || event.ActorKind != "" || event.Payload["actor"] != "LeadAgent" {
				rows.Close()
				return errors.New("unsupported historical LeadAgent Event shape")
			}
			expected := "completed"
			switch event.EventType {
			case "lead_agent_started":
				expected = "running"
				if h.start != nil {
					rows.Close()
					return errors.New("duplicate historical execution start")
				}
				h.start = &event
			case "lead_agent_failed":
				expected = "failed"
				fallthrough
			default:
				if h.end != nil {
					rows.Close()
					return errors.New("duplicate historical execution completion")
				}
				h.end = &event
			}
			if event.Payload["status"] != expected {
				rows.Close()
				return errors.New("historical Event status disagrees with type")
			}
			histories[id] = h
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	ids := make([]string, 0, len(histories))
	for id := range histories {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		h := histories[id]
		if h.start == nil || h.end == nil {
			return errors.New("historical Event Run requires exactly one start and completion")
		}
		if h.start.TraceID != h.end.TraceID || h.end.OccurredAt.Before(h.start.OccurredAt) || h.end.OccurredAt.After(c.inventory.SnapshotAt) {
			return errors.New("historical Event execution boundary mismatch")
		}
		status, _ := h.end.Payload["status"].(string)
		if _, err := c.bind("superagent.event_history", "run_id", id, "", c.inventory.LegacyRunActors[id], status, h.start.OccurredAt, h.end.OccurredAt); err != nil {
			return err
		}
	}
	c.counts["event_history_runs"] = len(histories)
	return nil
}
