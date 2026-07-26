package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	domainhealth "github.com/Nyukimin/RenCrow_CORE/internal/domain/health"
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

// TestRunDoctorCommand_AuditDirUsesHostPathSeparator は監査ディレクトリの
// 判定がホストOSの区切り文字を正しく扱うことを確認する
//
// path.Dir はスラッシュ専用であり、Windows の "C:\logs\audit.jsonl" に対して
// "." を返す。その結果 ensureDir(".") が常に成功し、監査ディレクトリの
// 書込可否チェックが無効化される。
func TestRunDoctorCommand_AuditDirUsesHostPathSeparator(t *testing.T) {
	auditPath := filepath.Join("C:", "rencrow-logs", "audit.jsonl")
	wantDir := filepath.Dir(auditPath)

	cfg := &config.Config{
		Security: config.SecurityConfig{
			Enabled:    true,
			PolicyMode: "strict",
			Audit: config.SecurityAuditConfig{
				Enabled: true,
				Path:    auditPath,
			},
		},
	}
	var out, errOut bytes.Buffer
	var gotDir string

	runDoctorCommand(
		[]string{"--json"},
		cfg,
		&fakeDoctorHealthChecker{status: domainhealth.StatusOK},
		true,
		func(_ string) error { return nil },
		func(dir string) error {
			gotDir = dir
			return nil
		},
		&out,
		&errOut,
		fixedNow,
	)

	if gotDir != wantDir {
		t.Fatalf("ensureDir received %q, want %q", gotDir, wantDir)
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
