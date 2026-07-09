package sandbox

import (
	"context"
	"testing"
	"time"

	domainsandbox "github.com/Nyukimin/RenCrow_CORE/internal/domain/sandbox"
)

func TestJSONLStoreSaveAndListSandboxes(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	ctx := context.Background()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	if err := store.SaveSandbox(ctx, domainsandbox.SandboxRecord{
		SandboxID: "sbx_1",
		Type:      "code",
		Path:      "sandbox/ws/sbx_1",
		Status:    domainsandbox.SandboxStatusActive,
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("SaveSandbox failed: %v", err)
	}
	if err := store.SaveSandbox(ctx, domainsandbox.SandboxRecord{
		SandboxID: "sbx_2",
		Type:      "artifact",
		Path:      "sandbox/ws/sbx_2",
		Status:    domainsandbox.SandboxStatusClosed,
		CreatedAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("SaveSandbox failed: %v", err)
	}

	items, err := store.ListSandboxes(ctx, 1)
	if err != nil {
		t.Fatalf("ListSandboxes failed: %v", err)
	}
	if len(items) != 1 || items[0].SandboxID != "sbx_2" {
		t.Fatalf("items = %#v", items)
	}
}

func TestJSONLStoreSaveAndListSandboxArtifacts(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	ctx := context.Background()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	if err := store.SaveSandboxArtifact(ctx, domainsandbox.SandboxArtifact{
		ArtifactID: "art_1",
		SandboxID:  "sbx_1",
		Type:       "report",
		FilePath:   "sandbox/sbx_1/reports/report.md",
		Title:      "Sandbox Report",
		Status:     "draft",
		CreatedAt:  now,
	}); err != nil {
		t.Fatalf("SaveSandboxArtifact failed: %v", err)
	}

	items, err := store.ListSandboxArtifacts(ctx, 10)
	if err != nil {
		t.Fatalf("ListSandboxArtifacts failed: %v", err)
	}
	if len(items) != 1 || items[0].ArtifactID != "art_1" {
		t.Fatalf("items = %#v", items)
	}
}

func TestJSONLStoreSaveAndListPromotionRequests(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	ctx := context.Background()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	if err := store.SavePromotionRequest(ctx, domainsandbox.PromotionRequest{
		PromotionID:         "prom_1",
		SandboxID:           "sbx_1",
		TargetPath:          "docs/a.md",
		DiffPath:            "sandbox/sbx_1/diff.patch",
		Reason:              "docs update",
		TestResultPath:      "sandbox/sbx_1/test.txt",
		RollbackPlanPath:    "sandbox/sbx_1/rollback.md",
		HumanApprovalStatus: domainsandbox.ApprovalPending,
		CreatedAt:           now,
	}); err != nil {
		t.Fatalf("SavePromotionRequest failed: %v", err)
	}

	items, err := store.ListPromotionRequests(ctx, 10)
	if err != nil {
		t.Fatalf("ListPromotionRequests failed: %v", err)
	}
	if len(items) != 1 || items[0].PromotionID != "prom_1" {
		t.Fatalf("items = %#v", items)
	}
}

func TestJSONLStoreSaveAndListPromotionGateLogs(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	ctx := context.Background()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	if err := store.SavePromotionGateLog(ctx, domainsandbox.PromotionGateLog{
		EventID:             "evt_promotion_gate_1",
		PromotionID:         "prom_1",
		GateStatus:          domainsandbox.GateStatusNeedsReview,
		Reason:              "promotion requirements missing: rollback_plan_path",
		HumanApprovalStatus: domainsandbox.ApprovalPending,
		CreatedAt:           now,
	}); err != nil {
		t.Fatalf("SavePromotionGateLog failed: %v", err)
	}

	items, err := store.ListPromotionGateLogs(ctx, 10)
	if err != nil {
		t.Fatalf("ListPromotionGateLogs failed: %v", err)
	}
	if len(items) != 1 || items[0].EventID != "evt_promotion_gate_1" {
		t.Fatalf("items = %#v", items)
	}
}

func TestJSONLStoreMissingFilesReturnEmptyLists(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	ctx := context.Background()

	sandboxes, err := store.ListSandboxes(ctx, 10)
	if err != nil {
		t.Fatalf("ListSandboxes failed: %v", err)
	}
	if len(sandboxes) != 0 {
		t.Fatalf("sandboxes = %#v", sandboxes)
	}
	artifacts, err := store.ListSandboxArtifacts(ctx, 10)
	if err != nil {
		t.Fatalf("ListSandboxArtifacts failed: %v", err)
	}
	if len(artifacts) != 0 {
		t.Fatalf("artifacts = %#v", artifacts)
	}
	promotions, err := store.ListPromotionRequests(ctx, 10)
	if err != nil {
		t.Fatalf("ListPromotionRequests failed: %v", err)
	}
	if len(promotions) != 0 {
		t.Fatalf("promotions = %#v", promotions)
	}
	logs, err := store.ListPromotionGateLogs(ctx, 10)
	if err != nil {
		t.Fatalf("ListPromotionGateLogs failed: %v", err)
	}
	if len(logs) != 0 {
		t.Fatalf("logs = %#v", logs)
	}
}
