package runmigration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	core "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Step16's preserved before/after cohort proves this exact QueueItemID mapping.
// Join only explicit queue references and one execution on the same Trace;
// conversation text, timestamps, and goal similarity are not ownership proof.
func readLegacyQueueReferences(ctx context.Context, databases []*databaseInput) (map[string]string, error) {
	queueTrace, traceQueue, traceRun := map[string]string{}, map[string]string{}, map[string]string{}
	ambiguousRuns := map[string]bool{}
	for _, db := range databases {
		if db.role != "events" {
			continue
		}
		rows, err := db.db.QueryContext(ctx, "SELECT envelope_json FROM event_envelope WHERE event_type IN ('run_queue.claimed','run_queue.completed','run_queue.failed','lead_agent.started','lead_agent.completed') ORDER BY event_seq")
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var raw []byte
			if err = rows.Scan(&raw); err != nil {
				rows.Close()
				return nil, err
			}
			var event core.EventEnvelope
			if err = strictJSON(raw, &event); err != nil {
				rows.Close()
				return nil, err
			}
			if event.ComponentID != "superagent" {
				continue
			}
			trace := string(event.TraceID)
			if err = event.TraceID.Validate(); err != nil {
				rows.Close()
				return nil, err
			}
			switch event.EventType {
			case "run_queue.claimed", "run_queue.completed", "run_queue.failed":
				old, ok := event.Payload["queue_reference"].(string)
				if !ok || old == "" {
					continue
				}
				id, err := core.NewMigrationID(core.CanonicalQueueItemID, "run_queue", "queue_item_id", old)
				if err != nil {
					rows.Close()
					return nil, err
				}
				if (queueTrace[id] != "" && queueTrace[id] != trace) || (traceQueue[trace] != "" && traceQueue[trace] != id) {
					rows.Close()
					return nil, errors.New("ambiguous legacy queue Trace")
				}
				queueTrace[id], traceQueue[trace] = trace, id
			case "lead_agent.started", "lead_agent.completed":
				run, ok := event.Payload["run_reference"].(string)
				if !ok || run == "" {
					continue
				}
				if traceRun[trace] != "" && traceRun[trace] != run {
					ambiguousRuns[trace] = true
				}
				traceRun[trace] = run
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	result := map[string]string{}
	for queue, trace := range queueTrace {
		if ambiguousRuns[trace] {
			return nil, errors.New("ambiguous legacy execution Trace")
		}
		if traceRun[trace] != "" {
			result[queue] = traceRun[trace]
		}
	}
	return result, nil
}

func (c *cohort) restoreLegacyQueueReference(m map[string]json.RawMessage) error {
	if textField(m, "run_start_reason") != "" {
		return nil
	}
	reference := textField(m, "run_id")
	if proven := c.queueReferences[textField(m, "queue_item_id")]; proven != "" {
		// The former scheduler also stored its input job key in run_id, while
		// the old LeadAgent recorder emitted run_lead_<that exact input>.
		inputKey, legacyLead := strings.CutPrefix(proven, "run_lead_")
		if reference != "" && reference != proven && (!legacyLead || inputKey != reference) {
			return errors.New("queue execution reference conflicts with Trace")
		}
		reference = proven
	}
	if reference == "" {
		return errors.New("legacy queue has no proven execution reference")
	}
	run, err := c.resolve("superagent", "run_id", reference)
	if err != nil {
		return err
	}
	setField(m, "run_id", reference)
	// Use the canonical owner Run's declared reason, not the queue action label.
	setField(m, "run_start_reason", run.StartReason)
	return nil
}
