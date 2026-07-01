package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	domainhealth "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/health"
)

type fakeDoctorHealthChecker struct {
	status domainhealth.Status
}

func (f *fakeDoctorHealthChecker) RunChecks(_ context.Context) domainhealth.HealthReport {
	return domainhealth.HealthReport{Status: f.status}
}

func TestRunDoctorCommand_JSONNoIssue(t *testing.T) {
	cfg := &config.Config{Security: config.SecurityConfig{Enabled: false}}
	var out, errOut bytes.Buffer

	code := runDoctorCommand(
		[]string{"--json"},
		cfg,
		&fakeDoctorHealthChecker{status: domainhealth.StatusOK},
		true,
		func(_ string) error { return nil },
		func(_ string) error { return nil },
		&out,
		&errOut,
		fixedNow,
	)
	if code != 0 {
		t.Fatalf("expected code 0, got %d", code)
	}
	var payload struct {
		OK        bool   `json:"ok"`
		Component string `json:"component"`
		Status    string `json:"status"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !payload.OK || payload.Component != "doctor" || payload.Status != "ok" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}

func TestRunDoctorCommand_JSONError(t *testing.T) {
	cfg := &config.Config{
		WorkspaceDir: "/workspace/missing",
		Security: config.SecurityConfig{
			Enabled:           true,
			PolicyMode:        "strict",
			WorkspaceEnforced: true,
		},
	}
	var out, errOut bytes.Buffer

	code := runDoctorCommand(
		[]string{"--json"},
		cfg,
		&fakeDoctorHealthChecker{status: domainhealth.StatusOK},
		false,
		func(_ string) error { return errors.New("not found") },
		func(_ string) error { return nil },
		&out,
		&errOut,
		fixedNow,
	)
	if code != 1 {
		t.Fatalf("expected code 1, got %d", code)
	}
	var payload struct {
		OK     bool   `json:"ok"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if payload.OK || payload.Status != "down" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}
