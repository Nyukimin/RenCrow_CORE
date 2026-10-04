package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	domainverification "github.com/Nyukimin/RenCrow_CORE/internal/domain/verification"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/storagehost"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
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

	runtime := buildVerificationRuntime(cfg, deps, nil, nil)

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

	runtime := buildVerificationRuntime(cfg, deps, nil, nil)

	if runtime.Pipeline != nil || runtime.Store != nil {
		t.Fatal("disabled verification should not build runtime")
	}
	if deps.verificationRecent == nil || deps.verificationDetail == nil || deps.verificationSummary == nil {
		t.Fatal("disabled verification should wire unavailable viewer handlers")
	}
}

func TestBuildVerificationRuntimeUsesSelectedRemoteOwnerWithoutOpeningLocalReport(t *testing.T) {
	reportPath := filepath.Join(t.TempDir(), "missing-parent", "verification.jsonl")
	remote := &runtimeVerificationReportStoreStub{}
	deps := &Dependencies{}
	cfg := &config.Config{Verification: config.VerificationConfig{
		Enabled:      true,
		Mode:         "dry_run",
		DefaultLevel: "high",
		ReportPath:   reportPath,
	}}
	runtime := buildVerificationRuntime(cfg, deps, nil, remote)
	if runtime.Store != remote {
		t.Fatalf("selected report store=%T, want injected remote owner", runtime.Store)
	}
	if _, err := os.Stat(reportPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remote verification runtime created local report %q (stat err=%v)", reportPath, err)
	}
}

type runtimeVerificationReportStoreStub struct{}

func (*runtimeVerificationReportStoreStub) Save(context.Context, domainverification.VerificationReport) error {
	return nil
}

func (*runtimeVerificationReportStoreStub) ListRecent(context.Context, int) ([]domainverification.VerificationReport, error) {
	return []domainverification.VerificationReport{}, nil
}

func (*runtimeVerificationReportStoreStub) GetByTaskID(context.Context, modulecore.TaskID) (domainverification.VerificationReport, error) {
	return domainverification.VerificationReport{}, errors.New("not found")
}

func (*runtimeVerificationReportStoreStub) Summary(context.Context) (map[string]map[string]int, error) {
	return map[string]map[string]int{}, nil
}

var _ storagehost.VerificationReportGroupOwner = (*runtimeVerificationReportStoreStub)(nil)
