package toolharness

import (
	"strings"
	"testing"
	"time"
)

func TestValidateEventRejectsMalformedEvent(t *testing.T) {
	now := time.Date(2026, 5, 20, 8, 10, 0, 0, time.UTC)
	valid := func() Event {
		return Event{
			EventID:          "evt_tool_harness_1",
			ToolName:         "file_read",
			RawInputHash:     "abc123",
			ValidationStatus: ValidationStatusRepaired,
			Repairs:          []Repair{{Type: "markdown_autolink_path_unwrap", Path: []string{"path"}}},
			CreatedAt:        now,
		}
	}
	tests := []struct {
		name   string
		mutate func(*Event)
		want   string
	}{
		{name: "missing event id", mutate: func(event *Event) {
			event.EventID = ""
		}, want: "event_id"},
		{name: "missing created at", mutate: func(event *Event) {
			event.CreatedAt = time.Time{}
		}, want: "created_at"},
		{name: "invalid status", mutate: func(event *Event) {
			event.ValidationStatus = "done"
		}, want: "validation_status"},
		{name: "valid with repair evidence", mutate: func(event *Event) {
			event.ValidationStatus = ValidationStatusValid
		}, want: "valid event must not include repair evidence"},
		{name: "repaired without evidence", mutate: func(event *Event) {
			event.Repairs = nil
		}, want: "repaired event requires repair evidence"},
		{name: "repair missing type", mutate: func(event *Event) {
			event.Repairs[0].Type = ""
		}, want: "repair type"},
		{name: "relation default missing field", mutate: func(event *Event) {
			event.Repairs = nil
			event.RelationDefaults = []RelationDefault{{Reason: "limit was provided without offset"}}
		}, want: "relation default field"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := valid()
			tt.mutate(&event)
			err := ValidateEvent(event)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateEvent() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestValidateEventAcceptsTimestampedEvents(t *testing.T) {
	now := time.Date(2026, 5, 20, 8, 10, 0, 0, time.UTC)
	for _, event := range []Event{
		{
			EventID:          "evt_valid",
			ToolName:         "file_read",
			RawInputHash:     "abc123",
			ValidationStatus: ValidationStatusValid,
			CreatedAt:        now,
		},
		{
			EventID:          "evt_repaired",
			ToolName:         "file_read",
			RawInputHash:     "def456",
			ValidationStatus: ValidationStatusRepaired,
			RelationDefaults: []RelationDefault{{Field: "offset", Value: 0, Reason: "limit was provided without offset"}},
			CreatedAt:        now,
		},
	} {
		if err := ValidateEvent(event); err != nil {
			t.Fatalf("ValidateEvent(%s) error = %v", event.EventID, err)
		}
	}
}
