package identitymigration

import (
	"reflect"
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestConvertLegacyEventsIsDeterministicAndDropsKnownRunReferenceMisuse(t *testing.T) {
	at := time.Date(2026, 8, 14, 13, 43, 50, 0, time.UTC)
	legacy := []LegacyEvent{
		{SourceTable: "trace_event", EventID: "evt_started_old", RunID: "run_lead_old", EventType: "lead_agent_started", OccurredAt: at, Payload: map[string]any{"status": "running"}},
		{SourceTable: "trace_event", EventID: "evt_subagent_old", ParentEventID: "run_lead_old", RunID: "run_lead_old", EventType: "subagent_started", OccurredAt: at.Add(time.Second), Payload: map[string]any{"status": "running"}},
	}

	first, err := ConvertLegacyEvents("superagent", legacy)
	if err != nil {
		t.Fatalf("ConvertLegacyEvents() error = %v", err)
	}
	second, err := ConvertLegacyEvents("superagent", legacy)
	if err != nil {
		t.Fatalf("ConvertLegacyEvents() second error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("conversion is not deterministic:\nfirst=%#v\nsecond=%#v", first, second)
	}
	if first.Manifest.InputCount != 2 || first.Manifest.ConvertedCount != 2 || first.Manifest.DroppedRunAsParentCount != 1 {
		t.Fatalf("manifest = %#v", first.Manifest)
	}
	if first.Manifest.DroppedRunAsParentReason != "legacy_parent_event_id_referenced_run_id" {
		t.Fatalf("dropped run-as-parent reason = %q", first.Manifest.DroppedRunAsParentReason)
	}
	if first.Events[0].TraceID != first.Events[1].TraceID || first.Events[0].RunID != first.Events[1].RunID {
		t.Fatalf("same legacy run did not map to same trace/run: %#v", first.Events)
	}
	if first.Events[1].CausationEventID != "" {
		t.Fatalf("known RunID misuse became causation: %q", first.Events[1].CausationEventID)
	}
	if err := modulecore.ValidateEventEnvelopeGraph(first.Events); err != nil {
		t.Fatalf("converted graph invalid: %v", err)
	}
}

func TestConvertLegacyEventsMapsResolvableCausation(t *testing.T) {
	at := time.Date(2026, 8, 29, 1, 0, 0, 0, time.UTC)
	legacy := []LegacyEvent{
		{SourceTable: "ai_workflow_event", EventID: "root", EventType: "command_started", OccurredAt: at},
		{SourceTable: "ai_workflow_event", EventID: "child", ParentEventID: "root", EventType: "command_completed", OccurredAt: at.Add(time.Second)},
	}
	converted, err := ConvertLegacyEvents("ai_workflow", legacy)
	if err != nil {
		t.Fatalf("ConvertLegacyEvents() error = %v", err)
	}
	if converted.Events[1].CausationEventID != converted.Events[0].EventID {
		t.Fatalf("causation = %q, want %q", converted.Events[1].CausationEventID, converted.Events[0].EventID)
	}
}

func TestConvertLegacyEventsRejectsUnknownMissingParent(t *testing.T) {
	_, err := ConvertLegacyEvents("superagent", []LegacyEvent{{
		SourceTable: "trace_event", EventID: "child", ParentEventID: "missing", RunID: "run_1",
		EventType: "child", OccurredAt: time.Now().UTC(),
	}})
	if err == nil {
		t.Fatal("ConvertLegacyEvents() error = nil, want unresolved parent rejection")
	}
}

func TestConvertLegacyEventsWithoutRunCreatesIndependentTrace(t *testing.T) {
	converted, err := ConvertLegacyEvents("ai_workflow", []LegacyEvent{{
		SourceTable: "ai_workflow_event", EventID: "event_without_run", EventType: "worktree_created", OccurredAt: time.Now().UTC(),
	}})
	if err != nil {
		t.Fatalf("ConvertLegacyEvents() error = %v", err)
	}
	if converted.Events[0].TraceID == "" || converted.Events[0].RunID != "" {
		t.Fatalf("event = %#v", converted.Events[0])
	}
}
