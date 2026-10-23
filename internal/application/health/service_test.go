package health

import (
	"context"
	"testing"
	"time"

	domainhealth "github.com/Nyukimin/RenCrow_CORE/internal/domain/health"
)

type mockCheck struct {
	name              string
	status            domainhealth.Status
	msg               string
	readinessOptional bool
}

func (c *mockCheck) Name() string { return c.name }

func (c *mockCheck) ReadinessOptional() bool { return c.readinessOptional }

func (c *mockCheck) Run(_ context.Context) domainhealth.CheckResult {
	return domainhealth.CheckResult{
		Name:     c.name,
		Status:   c.status,
		Message:  c.msg,
		Duration: 1 * time.Millisecond,
	}
}

func TestHealthService_RunChecks_AllOK(t *testing.T) {
	svc := NewHealthService(
		&mockCheck{name: "db", status: domainhealth.StatusOK, msg: "connected"},
		&mockCheck{name: "cache", status: domainhealth.StatusOK, msg: "connected"},
	)

	report := svc.RunChecks(context.Background())

	if report.Status != domainhealth.StatusOK {
		t.Errorf("expected OK, got %s", report.Status)
	}
	if len(report.Checks) != 2 {
		t.Errorf("expected 2 checks, got %d", len(report.Checks))
	}
}

func TestHealthService_RunChecks_OneDown(t *testing.T) {
	svc := NewHealthService(
		&mockCheck{name: "db", status: domainhealth.StatusOK},
		&mockCheck{name: "runtime_dependency", status: domainhealth.StatusDown, msg: "unreachable"},
	)

	report := svc.RunChecks(context.Background())

	if report.Status != domainhealth.StatusDown {
		t.Errorf("expected down, got %s", report.Status)
	}
}

func TestHealthService_IsReady_True(t *testing.T) {
	svc := NewHealthService(
		&mockCheck{name: "db", status: domainhealth.StatusOK},
	)

	if !svc.IsReady(context.Background()) {
		t.Error("expected ready=true")
	}
}

func TestHealthService_IsReady_False(t *testing.T) {
	svc := NewHealthService(
		&mockCheck{name: "runtime_dependency", status: domainhealth.StatusDown},
	)

	if svc.IsReady(context.Background()) {
		t.Error("expected ready=false")
	}
}

func TestHealthService_IsReady_ExistingDegradedCheckFails(t *testing.T) {
	svc := NewHealthService(
		&mockCheck{name: "runtime_dependency", status: domainhealth.StatusDegraded},
	)

	if svc.IsReady(context.Background()) {
		t.Error("expected an existing degraded check to make the service not ready")
	}
}

func TestHealthService_IsReady_CheckpointDegradedCheckDoesNotFail(t *testing.T) {
	svc := NewHealthService(
		&mockCheck{name: "event_store_checkpoint", status: domainhealth.StatusDegraded, readinessOptional: true},
	)

	if !svc.IsReady(context.Background()) {
		t.Error("expected a readiness-optional degraded checkpoint check to remain ready")
	}
}

func TestHealthService_IsReady_CheckpointDegradedWithDownCheckFails(t *testing.T) {
	svc := NewHealthService(
		&mockCheck{name: "event_store_checkpoint", status: domainhealth.StatusDegraded, readinessOptional: true},
		&mockCheck{name: "runtime_dependency", status: domainhealth.StatusDown},
	)

	if svc.IsReady(context.Background()) {
		t.Error("expected a down check to make the service not ready with a degraded checkpoint")
	}
}

func TestHealthService_IsReady_UnknownStatusFailsClosed(t *testing.T) {
	svc := NewHealthService(
		&mockCheck{name: "runtime_dependency", status: domainhealth.Status("unknown")},
	)

	if svc.IsReady(context.Background()) {
		t.Error("expected an unknown check status to make the service not ready")
	}
}

func TestHealthService_NoChecks(t *testing.T) {
	svc := NewHealthService()

	report := svc.RunChecks(context.Background())
	if report.Status != domainhealth.StatusOK {
		t.Errorf("expected OK for no checks, got %s", report.Status)
	}
	if !svc.IsReady(context.Background()) {
		t.Error("expected ready=true for no checks")
	}
}
