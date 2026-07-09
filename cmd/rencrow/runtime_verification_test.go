package main

import (
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
)

func TestBuildVerificationRuntimeEnabledWiresPipelineAndViewerHandlers(t *testing.T) {
	deps := &Dependencies{}
	cfg := &config.Config{
		WorkspaceDir: t.TempDir(),
		Verification: config.VerificationConfig{
			Enabled:      true,
			Mode:         "dry_run",
			DefaultLevel: "high",
			ReportPath:   filepath.Join(t.TempDir(), "verification_report.jsonl"),
		},
	}

	runtime := buildVerificationRuntime(cfg, deps, nil)

	if runtime.Pipeline == nil {
		t.Fatal("expected verification pipeline")
	}
	if runtime.Store == nil {
		t.Fatal("expected verification report store")
	}
	if deps.verificationRecent == nil || deps.verificationDetail == nil || deps.verificationSummary == nil {
		t.Fatal("expected viewer verification handlers")
	}
}

func TestBuildVerificationRuntimeDisabledWiresUnavailableHandlers(t *testing.T) {
	deps := &Dependencies{}
	cfg := &config.Config{Verification: config.VerificationConfig{Enabled: false}}

	runtime := buildVerificationRuntime(cfg, deps, nil)

	if runtime.Pipeline != nil || runtime.Store != nil {
		t.Fatal("disabled verification should not build runtime")
	}
	if deps.verificationRecent == nil || deps.verificationDetail == nil || deps.verificationSummary == nil {
		t.Fatal("disabled verification should wire unavailable viewer handlers")
	}
}
