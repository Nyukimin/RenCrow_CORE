package execution

import (
	"strings"
	"testing"
	"time"
)

func TestExecutionReportValidate(t *testing.T) {
	r := ExecutionReport{
		JobID:      "j1",
		Goal:       "TTS実装して",
		Status:     "passed",
		CreatedAt:  time.Now().UTC(),
		FinishedAt: time.Now().UTC(),
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("expected valid report, got %v", err)
	}

	r.JobID = ""
	if err := r.Validate(); err == nil {
		t.Fatal("expected validation error for empty job id")
	}
}

func TestExecutionReportValidateRejectsMissingFields(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name string
		item ExecutionReport
		want string
	}{
		{name: "missing job", item: ExecutionReport{Goal: "goal", Status: "passed", CreatedAt: now, FinishedAt: now}, want: "job_id"},
		{name: "missing goal", item: ExecutionReport{JobID: "j1", Status: "passed", CreatedAt: now, FinishedAt: now}, want: "goal"},
		{name: "missing status", item: ExecutionReport{JobID: "j1", Goal: "goal", CreatedAt: now, FinishedAt: now}, want: "status"},
		{name: "missing created", item: ExecutionReport{JobID: "j1", Goal: "goal", Status: "passed", FinishedAt: now}, want: "created_at"},
		{name: "missing finished", item: ExecutionReport{JobID: "j1", Goal: "goal", Status: "passed", CreatedAt: now}, want: "finished_at"},
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
