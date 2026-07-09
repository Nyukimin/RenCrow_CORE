package sandbox

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	domainsandbox "github.com/Nyukimin/RenCrow_CORE/internal/domain/sandbox"
)

func TestSQLiteStoreSaveAndListSandboxRecords(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "sandbox.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	if err := store.SaveSandbox(ctx, domainsandbox.SandboxRecord{
		SandboxID:    "sbx_1",
		WorkstreamID: "ws_1",
		GoalID:       "goal_1",
		Type:         "code",
		Path:         "sandbox/ws_1/sbx_1",
		Status:       domainsandbox.SandboxStatusActive,
		CreatedAt:    now,
	}); err != nil {
		t.Fatalf("SaveSandbox failed: %v", err)
	}
	if err := store.SaveSandboxArtifact(ctx, domainsandbox.SandboxArtifact{
		ArtifactID: "art_1",
		SandboxID:  "sbx_1",
		Type:       "rollback_plan",
		FilePath:   "sandbox/ws_1/sbx_1/reports/rollback.md",
		Status:     "pending_review",
		CreatedAt:  now,
	}); err != nil {
		t.Fatalf("SaveSandboxArtifact failed: %v", err)
	}
	if err := store.SavePromotionRequest(ctx, domainsandbox.PromotionRequest{
		PromotionID:         "prom_1",
		SandboxID:           "sbx_1",
		WorkstreamID:        "ws_1",
		GoalID:              "goal_1",
		TargetPath:          "docs/example.md",
		DiffPath:            "sandbox/ws_1/sbx_1/diff.patch",
		Reason:              "仕様反映",
		TestResultPath:      "sandbox/ws_1/sbx_1/test.txt",
		RollbackPlanPath:    "sandbox/ws_1/sbx_1/rollback.md",
		HumanApprovalStatus: domainsandbox.ApprovalPending,
		CreatedAt:           now,
	}); err != nil {
		t.Fatalf("SavePromotionRequest failed: %v", err)
	}
	if err := store.SavePromotionGateLog(ctx, domainsandbox.PromotionGateLog{
		EventID:             "evt_gate_1",
		PromotionID:         "prom_1",
		GateStatus:          domainsandbox.GateStatusNeedsReview,
		Reason:              "promotion requirements missing: human_approval",
		HumanApprovalStatus: domainsandbox.ApprovalPending,
		CreatedAt:           now,
	}); err != nil {
		t.Fatalf("SavePromotionGateLog failed: %v", err)
	}

	sandboxes, err := store.ListSandboxes(ctx, 10)
	if err != nil || len(sandboxes) != 1 || sandboxes[0].SandboxID != "sbx_1" {
		t.Fatalf("sandboxes=%#v err=%v", sandboxes, err)
	}
	artifacts, err := store.ListSandboxArtifacts(ctx, 10)
	if err != nil || len(artifacts) != 1 || artifacts[0].ArtifactID != "art_1" {
		t.Fatalf("artifacts=%#v err=%v", artifacts, err)
	}
	promotions, err := store.ListPromotionRequests(ctx, 10)
	if err != nil || len(promotions) != 1 || promotions[0].PromotionID != "prom_1" {
		t.Fatalf("promotions=%#v err=%v", promotions, err)
	}
	logs, err := store.ListPromotionGateLogs(ctx, 10)
	if err != nil || len(logs) != 1 || logs[0].EventID != "evt_gate_1" {
		t.Fatalf("logs=%#v err=%v", logs, err)
	}
}

func TestSQLiteStoreMissingRowsReturnEmptyLists(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "sandbox.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	if items, err := store.ListSandboxes(ctx, 10); err != nil || len(items) != 0 {
		t.Fatalf("sandboxes=%#v err=%v", items, err)
	}
	if items, err := store.ListSandboxArtifacts(ctx, 10); err != nil || len(items) != 0 {
		t.Fatalf("artifacts=%#v err=%v", items, err)
	}
	if items, err := store.ListPromotionRequests(ctx, 10); err != nil || len(items) != 0 {
		t.Fatalf("promotions=%#v err=%v", items, err)
	}
	if items, err := store.ListPromotionGateLogs(ctx, 10); err != nil || len(items) != 0 {
		t.Fatalf("logs=%#v err=%v", items, err)
	}
}
