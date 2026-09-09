package runmigration

import (
	"context"
	"testing"
	"time"

	core "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestMigrationEventKeepsAuditLabelWithoutInventingRun(t *testing.T) {
	f := newFullCohortFixture(t)
	e := legacyAuditFixture(f.options.Inventory.SnapshotAt.Add(-time.Minute))
	appendMigrationEvent(t, &f, e)
	got := applyAndReadMigrationEvent(t, &f, e.EventID)
	if got.RunID != "" || got.TaskID != "" || got.ActorID != "shiro" || got.Payload["audit_reference"] != "audit-label" {
		t.Fatalf("audit promoted to execution: %#v", got)
	}
	if _, ok := got.Payload["run_id"]; ok {
		t.Fatal("invalid audit Run field retained")
	}
}

func TestMigrationEventRejectsUnprovenAuditMapping(t *testing.T) {
	f := newFullCohortFixture(t)
	e := legacyAuditFixture(f.options.Inventory.SnapshotAt.Add(-time.Minute))
	e.EventID = core.NewEventID()
	appendMigrationEvent(t, &f, e)
	if _, err := Run(context.Background(), f.options); err == nil {
		t.Fatal("unproven audit source accepted")
	}
}

func legacyAuditFixture(at time.Time) core.EventEnvelope {
	const source = "ai-workflow-event/sha256:15ff891b946f1cba8ee3c1acc18230aecd0409c3224ddc8be65d2be032160faf"
	e := core.NewRootEventEnvelope("ai_workflow", "owner_route_e2e", at, map[string]any{"event_id": source, "event_type": "owner_route_e2e", "run_id": "audit-label", "agent": "shiro", "status": "completed", "created_at": at.Format(time.RFC3339Nano), "completed_at": at.Format(time.RFC3339Nano)})
	id, _ := core.NewMigrationID(core.CanonicalEventID, "ai_workflow_event", "event_id", source)
	e.EventID = core.EventID(id)
	id, _ = core.NewMigrationID(core.CanonicalRunID, "ai_workflow_event", "run_id", "audit-label")
	e.RunID = core.RunID(id)
	return e
}
