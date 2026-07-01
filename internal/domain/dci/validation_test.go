package dci

import (
	"strings"
	"testing"
	"time"
)

func TestValidateSearchTraceRejectsMalformedTrace(t *testing.T) {
	now := time.Date(2026, 5, 20, 8, 0, 0, 0, time.UTC)
	valid := func() SearchTrace {
		return SearchTrace{
			EventID:            "evt_dci_1",
			StartedAt:          now,
			EndedAt:            now.Add(time.Second),
			Actor:              "Worker",
			Mode:               "dci",
			UserQuery:          "DCI",
			Status:             "completed",
			FinalEvidenceCount: 1,
			Steps: []SearchStep{{
				StepNo:      1,
				Tool:        "file_read",
				ResultCount: 1,
				Status:      "completed",
				CreatedAt:   now,
			}},
		}
	}
	tests := []struct {
		name   string
		mutate func(*SearchTrace)
		want   string
	}{
		{name: "missing started_at", mutate: func(trace *SearchTrace) {
			trace.StartedAt = time.Time{}
		}, want: "started_at"},
		{name: "terminal missing ended_at", mutate: func(trace *SearchTrace) {
			trace.EndedAt = time.Time{}
		}, want: "ended_at"},
		{name: "failed missing error", mutate: func(trace *SearchTrace) {
			trace.Status = "failed"
			trace.ErrorMessage = ""
		}, want: "error_message"},
		{name: "negative evidence count", mutate: func(trace *SearchTrace) {
			trace.FinalEvidenceCount = -1
		}, want: "final_evidence_count"},
		{name: "duplicate step", mutate: func(trace *SearchTrace) {
			trace.Steps = append(trace.Steps, trace.Steps[0])
		}, want: "duplicate step_no"},
		{name: "step missing created_at", mutate: func(trace *SearchTrace) {
			trace.Steps[0].CreatedAt = time.Time{}
		}, want: "created_at"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			trace := valid()
			tt.mutate(&trace)
			err := ValidateSearchTrace(trace)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateSearchTrace() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestValidateSearchTraceAcceptsCompleteTrace(t *testing.T) {
	now := time.Date(2026, 5, 20, 8, 0, 0, 0, time.UTC)
	err := ValidateSearchTrace(SearchTrace{
		EventID:            "evt_dci_1",
		StartedAt:          now,
		EndedAt:            now.Add(time.Second),
		Actor:              "Worker",
		Mode:               "dci",
		UserQuery:          "DCI",
		Status:             "completed",
		FinalEvidenceCount: 1,
		Steps: []SearchStep{{
			StepNo:      1,
			Tool:        "file_read",
			ResultCount: 1,
			Status:      "ok",
			CreatedAt:   now,
		}},
	})
	if err != nil {
		t.Fatalf("ValidateSearchTrace() error = %v", err)
	}
}

func TestValidateSearchStepAcceptsTerminalAndErrorStatuses(t *testing.T) {
	now := time.Date(2026, 5, 20, 8, 0, 0, 0, time.UTC)
	for _, status := range []string{"ok", "completed", "stopped"} {
		if err := ValidateSearchStep(SearchStep{StepNo: 1, Tool: "rg", Status: status, CreatedAt: now}); err != nil {
			t.Fatalf("ValidateSearchStep(%s) failed: %v", status, err)
		}
	}
	if err := ValidateSearchStep(SearchStep{StepNo: 1, Tool: "rg", Status: "error", ErrorMessage: "boom", CreatedAt: now}); err != nil {
		t.Fatalf("error status with message should validate: %v", err)
	}
}

func TestValidateSearchTraceRequiredFieldsAndStatuses(t *testing.T) {
	now := time.Date(2026, 5, 20, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "event id", err: ValidateSearchTrace(SearchTrace{StartedAt: now, EndedAt: now, Actor: "Worker", Mode: "dci", UserQuery: "query", Status: "completed"}), want: "event_id"},
		{name: "actor", err: ValidateSearchTrace(SearchTrace{EventID: "evt_1", StartedAt: now, EndedAt: now, Mode: "dci", UserQuery: "query", Status: "completed"}), want: "actor"},
		{name: "mode", err: ValidateSearchTrace(SearchTrace{EventID: "evt_1", StartedAt: now, EndedAt: now, Actor: "Worker", UserQuery: "query", Status: "completed"}), want: "mode"},
		{name: "query", err: ValidateSearchTrace(SearchTrace{EventID: "evt_1", StartedAt: now, EndedAt: now, Actor: "Worker", Mode: "dci", Status: "completed"}), want: "user_query"},
		{name: "status", err: ValidateSearchTrace(SearchTrace{EventID: "evt_1", StartedAt: now, EndedAt: now, Actor: "Worker", Mode: "dci", UserQuery: "query"}), want: "status"},
		{name: "invalid status", err: ValidateSearchTrace(SearchTrace{EventID: "evt_1", StartedAt: now, EndedAt: now, Actor: "Worker", Mode: "dci", UserQuery: "query", Status: "running"}), want: "invalid status"},
		{name: "step no", err: ValidateSearchStep(SearchStep{Tool: "rg", Status: "ok", CreatedAt: now}), want: "step_no"},
		{name: "step tool", err: ValidateSearchStep(SearchStep{StepNo: 1, Status: "ok", CreatedAt: now}), want: "tool"},
		{name: "step status", err: ValidateSearchStep(SearchStep{StepNo: 1, Tool: "rg", CreatedAt: now}), want: "status"},
		{name: "step invalid status", err: ValidateSearchStep(SearchStep{StepNo: 1, Tool: "rg", Status: "done", CreatedAt: now}), want: "invalid status"},
		{name: "step error message", err: ValidateSearchStep(SearchStep{StepNo: 1, Tool: "rg", Status: "error", CreatedAt: now}), want: "error_message"},
		{name: "step result count", err: ValidateSearchStep(SearchStep{StepNo: 1, Tool: "rg", Status: "ok", ResultCount: -1, CreatedAt: now}), want: "result_count"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err == nil || !strings.Contains(tt.err.Error(), tt.want) {
				t.Fatalf("err=%v, want %s", tt.err, tt.want)
			}
		})
	}
}
