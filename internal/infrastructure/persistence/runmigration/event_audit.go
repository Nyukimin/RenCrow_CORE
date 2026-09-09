package runmigration

import (
	"errors"
	"strings"
	"time"

	core "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// The former AI Workflow record_workflow_event API accepted an audit label in
// run_id. Step02 mapped that label syntactically; it did not issue a Task Run.
// Preserve the label as audit evidence without inventing execution history.
func (c *cohort) restoreLegacyAuditEvent(e *core.EventEnvelope) (bool, error) {
	if e.ComponentID != "ai_workflow" || e.EventType != "owner_route_e2e" {
		return false, nil
	}
	reference, ok := e.Payload["run_id"].(string)
	if !ok {
		return false, nil
	}
	source, ok := e.Payload["event_id"].(string)
	if !ok || !strings.HasPrefix(source, "ai-workflow-event/sha256:") {
		return false, errors.New("unknown historical audit producer")
	}
	mappedEvent, err := core.NewMigrationID(core.CanonicalEventID, "ai_workflow_event", "event_id", source)
	if err != nil || mappedEvent != string(e.EventID) {
		return false, errors.New("historical audit Event mapping mismatch")
	}
	mappedRun, err := core.NewMigrationID(core.CanonicalRunID, "ai_workflow_event", "run_id", reference)
	if err != nil || mappedRun != string(e.RunID) || e.TaskID != "" || len(c.rawRuns[mappedRun]) != 0 {
		return false, errors.New("historical audit has execution ownership conflict")
	}
	actor, ok := e.Payload["agent"].(string)
	if !ok || validateMigrationActor(actor) != nil || e.ActorID != "" || e.ActorKind != "" || e.Payload["event_type"] != e.EventType || e.Payload["status"] != "completed" {
		return false, errors.New("historical audit attribution or shape invalid")
	}
	for _, field := range []string{"created_at", "completed_at"} {
		value, ok := e.Payload[field].(string)
		if !ok {
			return false, errors.New("historical audit time missing")
		}
		at, err := time.Parse(time.RFC3339Nano, value)
		if err != nil || !at.Equal(e.OccurredAt) {
			return false, errors.New("historical audit is not an atomic observation")
		}
	}
	if _, exists := e.Payload["audit_reference"]; exists {
		return false, errors.New("historical audit reference conflict")
	}
	e.Payload["audit_reference"] = reference
	delete(e.Payload, "run_id")
	e.RunID = ""
	e.ActorKind, e.ActorID = "agent", actor
	c.counts["historical_audit_labels"]++
	return true, nil
}
