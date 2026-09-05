package execution

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestExecutionReportValidate(t *testing.T) {
	r := ExecutionReport{
		TaskID:     modulecore.NewTaskID(),
		Goal:       "TTS実装して",
		Status:     "passed",
		CreatedAt:  time.Now().UTC(),
		FinishedAt: time.Now().UTC(),
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("expected valid report, got %v", err)
	}

	r.TaskID = ""
	if err := r.Validate(); err == nil {
		t.Fatal("expected validation error for empty task id")
	}
}

func TestExecutionReportValidateRejectsMissingFields(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name string
		item ExecutionReport
		want string
	}{
		{name: "missing task", item: ExecutionReport{Goal: "goal", Status: "passed", CreatedAt: now, FinishedAt: now}, want: "task_id"},
		{name: "missing goal", item: ExecutionReport{TaskID: modulecore.NewTaskID(), Status: "passed", CreatedAt: now, FinishedAt: now}, want: "goal"},
		{name: "missing status", item: ExecutionReport{TaskID: modulecore.NewTaskID(), Goal: "goal", CreatedAt: now, FinishedAt: now}, want: "status"},
		{name: "missing created", item: ExecutionReport{TaskID: modulecore.NewTaskID(), Goal: "goal", Status: "passed", FinishedAt: now}, want: "created_at"},
		{name: "missing finished", item: ExecutionReport{TaskID: modulecore.NewTaskID(), Goal: "goal", Status: "passed", CreatedAt: now}, want: "finished_at"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.item.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestExecutionReportLegacyJSONDoesNotPopulateTaskID(t *testing.T) {
	var report ExecutionReport
	legacyField := "job" + "_id"
	payload := []byte(`{"` + legacyField + `":"legacy","goal":"goal","status":"passed","created_at":"2026-09-05T00:00:00Z","finished_at":"2026-09-05T00:00:01Z"}`)
	if err := json.Unmarshal(payload, &report); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if err := report.Validate(); err == nil || !strings.Contains(err.Error(), "task_id") {
		t.Fatalf("legacy JSON must fail the canonical task boundary, got %v", err)
	}
}
