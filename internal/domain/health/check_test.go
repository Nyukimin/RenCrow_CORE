package health

import (
	"testing"
	"time"
)

func TestAggregate_AllOK(t *testing.T) {
	results := []CheckResult{
		{Name: "check1", Status: StatusOK, Duration: 10 * time.Millisecond},
		{Name: "check2", Status: StatusOK, Duration: 20 * time.Millisecond},
	}
	report := Aggregate(results)

	if report.Status != StatusOK {
		t.Errorf("expected OK, got %s", report.Status)
	}
	if len(report.Checks) != 2 {
		t.Errorf("expected 2 checks, got %d", len(report.Checks))
	}
}

func TestAggregate_OneDegraded(t *testing.T) {
	results := []CheckResult{
		{Name: "check1", Status: StatusOK},
		{Name: "check2", Status: StatusDegraded, Message: "slow"},
	}
	report := Aggregate(results)

	if report.Status != StatusDegraded {
		t.Errorf("expected degraded, got %s", report.Status)
	}
}

func TestAggregate_OneDown(t *testing.T) {
	results := []CheckResult{
		{Name: "check1", Status: StatusOK},
		{Name: "check2", Status: StatusDown, Message: "unreachable"},
	}
	report := Aggregate(results)

	if report.Status != StatusDown {
		t.Errorf("expected down, got %s", report.Status)
	}
}

func TestAggregate_DownTakesPrecedence(t *testing.T) {
	results := []CheckResult{
		{Name: "check1", Status: StatusDegraded},
		{Name: "check2", Status: StatusDown},
		{Name: "check3", Status: StatusOK},
	}
	report := Aggregate(results)

	if report.Status != StatusDown {
		t.Errorf("expected down (takes precedence), got %s", report.Status)
	}
}

func TestAggregate_Empty(t *testing.T) {
	report := Aggregate(nil)

	if report.Status != StatusOK {
		t.Errorf("expected OK for empty checks, got %s", report.Status)
	}
	if report.Timestamp.IsZero() {
		t.Error("expected non-zero timestamp")
	}
}
