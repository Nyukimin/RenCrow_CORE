package runmigration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	identitymigration "github.com/Nyukimin/RenCrow_CORE/internal/application/identitymigration"
	core "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// This evidence is an immutable retired producer file, not a runtime store.
// Replay the existing Step02 converter and require its entire envelope to match
// before recovering source identity that Step02 intentionally removed.
type legacyTraceRow struct {
	EventID        string    `json:"event_id"`
	RunID          string    `json:"run_id"`
	EventType      string    `json:"event_type"`
	Actor          string    `json:"actor"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
	PayloadSummary string    `json:"payload_summary"`
	ParentEventID  string    `json:"parent_event_id,omitempty"`
}

func (c *cohort) restoreLegacyTraceHistory(ctx context.Context, databases []*databaseInput, input map[string][]byte) error {
	path := c.inventory.LegacyTraceSource
	if path == "" {
		return nil
	}
	var source []legacyTraceRow
	var legacy []identitymigration.LegacyEvent
	for _, line := range jsonLines(input[path]) {
		var row legacyTraceRow
		if err := strictJSON(line, &row); err != nil {
			return err
		}
		if row.RunID == "" || row.CreatedAt.IsZero() || row.CreatedAt.After(c.inventory.SnapshotAt) {
			return errors.New("invalid archived trace execution boundary")
		}
		source = append(source, row)
		legacy = append(legacy, identitymigration.LegacyEvent{SourceTable: "trace_event", EventID: row.EventID, RunID: row.RunID, EventType: row.EventType, ParentEventID: row.ParentEventID, OccurredAt: row.CreatedAt, Payload: map[string]any{"actor": row.Actor, "status": row.Status, "payload_summary": row.PayloadSummary}})
	}
	if len(source) == 0 {
		return errors.New("empty legacy trace evidence")
	}
	converted, err := identitymigration.ConvertLegacyEvents("superagent", legacy)
	if err != nil {
		return err
	}
	var events *databaseInput
	for _, db := range databases {
		if db.role == "events" {
			events = db
		}
	}
	if events == nil {
		return errors.New("legacy trace evidence requires Event store")
	}
	c.legacyTraceEvents = map[core.EventID]legacyTraceRow{}
	type pair struct {
		start, end *legacyTraceRow
		parent     core.RunID
	}
	children := map[string]pair{}
	for i, expected := range converted.Events {
		var raw []byte
		if err := events.db.QueryRowContext(ctx, "SELECT envelope_json FROM event_envelope WHERE event_id = ?", string(expected.EventID)).Scan(&raw); err != nil {
			return err
		}
		var actual core.EventEnvelope
		if err := strictJSON(raw, &actual); err != nil {
			return err
		}
		actual.EventSeq = 0 // assigned later by the Event store, not the producer
		if !bytes.Equal(marshalLine(actual), marshalLine(expected)) {
			return errors.New("archived trace does not match canonical Event")
		}
		row := source[i]
		c.legacyTraceEvents[expected.EventID] = row
		if strings.HasPrefix(row.EventType, "lead_agent_") {
			if _, err := c.resolve("", "run_id", string(expected.RunID)); err != nil {
				return fmt.Errorf("archived Lead Event %s requires explicit actor attribution for %s: %w", expected.EventID, expected.RunID, err)
			}
			continue
		}
		if !strings.HasPrefix(row.EventType, "subagent_") || row.Actor != "Subagent" {
			return errors.New("unsupported archived trace event")
		}
		proof := expected
		proof.Payload = map[string]any{"event_id": row.EventID}
		child, err := legacyChildEventReference(proof)
		if err != nil {
			return err
		}
		p := children[child]
		if p.parent != "" && p.parent != expected.RunID {
			return errors.New("archived child has conflicting parents")
		}
		p.parent = expected.RunID
		switch row.EventType {
		case "subagent_started":
			if p.start != nil || row.Status != "running" {
				return errors.New("ambiguous archived child start")
			}
			p.start = &row
		case "subagent_completed", "subagent_failed":
			if p.end != nil || strings.TrimPrefix(row.EventType, "subagent_") != row.Status {
				return errors.New("ambiguous archived child completion")
			}
			p.end = &row
		default:
			return errors.New("unsupported archived child event")
		}
		children[child] = p
	}
	ids := make([]string, 0, len(children))
	for id := range children {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		p := children[id]
		if p.start == nil || p.end == nil || p.end.CreatedAt.Before(p.start.CreatedAt) {
			return errors.New("archived child requires complete execution pair")
		}
		m := map[string]json.RawMessage{}
		setField(m, "subagent_id", id)
		setField(m, "agent_type", p.start.Actor)
		setField(m, "created_at", p.start.CreatedAt)
		if err := restoreLegacyChildActor(m); err != nil {
			return err
		}
		parent, err := c.resolve("", "run_id", string(p.parent))
		if err != nil {
			return err
		}
		// An archive must not create a second owner for an existing execution.
		if len(c.rawRuns[id]) != 0 {
			return errors.New("archived child already has an execution owner")
		}
		runID, err := c.bind("superagent", "subagent_id", id, "", textField(m, "actor_id"), p.end.Status, p.start.CreatedAt, p.end.CreatedAt)
		if err != nil {
			return err
		}
		t := c.tasks[c.runs[runID].TaskID]
		t.ParentTaskID = parent.TaskID
		c.tasks[t.TaskID] = t
	}
	c.counts["archived_trace_events"] = len(source)
	c.counts["archived_child_runs"] = len(children)
	return nil
}
