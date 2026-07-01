package sandbox

import (
	"strings"
	"testing"
	"time"
)

func TestValidateSandboxRejectsMissingCreatedAt(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{name: "sandbox", err: ValidateSandboxRecord(SandboxRecord{SandboxID: "sbx_1", Type: "worktree", Path: "/tmp/sbx", Status: SandboxStatusActive})},
		{name: "artifact", err: ValidateSandboxArtifact(SandboxArtifact{ArtifactID: "art_1", SandboxID: "sbx_1", Type: "rollback_plan", FilePath: "rollback.md", Status: "completed"})},
		{name: "promotion", err: ValidatePromotionRequest(PromotionRequest{PromotionID: "promo_1", SandboxID: "sbx_1", TargetPath: "internal/app.go"})},
		{name: "gate", err: ValidatePromotionGateLog(PromotionGateLog{EventID: "evt_1", PromotionID: "promo_1", GateStatus: GateStatusNeedsReview})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err == nil || !strings.Contains(tc.err.Error(), "created_at") {
				t.Fatalf("err=%v, want created_at", tc.err)
			}
		})
	}
}

func TestValidateSandboxAcceptsTimestampedRecords(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 50, 0, 0, time.UTC)
	if err := ValidateSandboxRecord(SandboxRecord{SandboxID: "sbx_1", Type: "worktree", Path: "/tmp/sbx", Status: SandboxStatusActive, CreatedAt: now}); err != nil {
		t.Fatalf("sandbox should be valid: %v", err)
	}
	if err := ValidateSandboxArtifact(SandboxArtifact{ArtifactID: "art_1", SandboxID: "sbx_1", Type: "rollback_plan", FilePath: "rollback.md", Status: "completed", CreatedAt: now}); err != nil {
		t.Fatalf("artifact should be valid: %v", err)
	}
	if err := ValidatePromotionRequest(PromotionRequest{PromotionID: "promo_1", SandboxID: "sbx_1", TargetPath: "internal/app.go", CreatedAt: now}); err != nil {
		t.Fatalf("promotion should be valid: %v", err)
	}
	if err := ValidatePromotionGateLog(PromotionGateLog{EventID: "evt_1", PromotionID: "promo_1", GateStatus: GateStatusNeedsReview, CreatedAt: now}); err != nil {
		t.Fatalf("gate log should be valid: %v", err)
	}
}

func TestValidateSandboxRejectsMissingRequiredFields(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 50, 0, 0, time.UTC)
	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "sandbox missing id", err: ValidateSandboxRecord(SandboxRecord{Type: "worktree", Path: "/tmp/sbx", Status: SandboxStatusActive, CreatedAt: now}), want: "sandbox_id"},
		{name: "sandbox missing type", err: ValidateSandboxRecord(SandboxRecord{SandboxID: "sbx_1", Path: "/tmp/sbx", Status: SandboxStatusActive, CreatedAt: now}), want: "type"},
		{name: "sandbox missing path", err: ValidateSandboxRecord(SandboxRecord{SandboxID: "sbx_1", Type: "worktree", Status: SandboxStatusActive, CreatedAt: now}), want: "path"},
		{name: "sandbox missing status", err: ValidateSandboxRecord(SandboxRecord{SandboxID: "sbx_1", Type: "worktree", Path: "/tmp/sbx", CreatedAt: now}), want: "status"},
		{name: "artifact missing id", err: ValidateSandboxArtifact(SandboxArtifact{SandboxID: "sbx_1", Type: "rollback_plan", FilePath: "rollback.md", Status: "completed", CreatedAt: now}), want: "artifact_id"},
		{name: "artifact missing sandbox id", err: ValidateSandboxArtifact(SandboxArtifact{ArtifactID: "art_1", Type: "rollback_plan", FilePath: "rollback.md", Status: "completed", CreatedAt: now}), want: "sandbox_id"},
		{name: "artifact missing type", err: ValidateSandboxArtifact(SandboxArtifact{ArtifactID: "art_1", SandboxID: "sbx_1", FilePath: "rollback.md", Status: "completed", CreatedAt: now}), want: "artifact_type"},
		{name: "artifact missing path", err: ValidateSandboxArtifact(SandboxArtifact{ArtifactID: "art_1", SandboxID: "sbx_1", Type: "rollback_plan", Status: "completed", CreatedAt: now}), want: "file_path"},
		{name: "artifact missing status", err: ValidateSandboxArtifact(SandboxArtifact{ArtifactID: "art_1", SandboxID: "sbx_1", Type: "rollback_plan", FilePath: "rollback.md", CreatedAt: now}), want: "status"},
		{name: "promotion missing id", err: ValidatePromotionRequest(PromotionRequest{SandboxID: "sbx_1", TargetPath: "internal/app.go", CreatedAt: now}), want: "promotion_id"},
		{name: "promotion missing sandbox id", err: ValidatePromotionRequest(PromotionRequest{PromotionID: "promo_1", TargetPath: "internal/app.go", CreatedAt: now}), want: "sandbox_id"},
		{name: "promotion missing target path", err: ValidatePromotionRequest(PromotionRequest{PromotionID: "promo_1", SandboxID: "sbx_1", CreatedAt: now}), want: "target_path"},
		{name: "gate missing event id", err: ValidatePromotionGateLog(PromotionGateLog{PromotionID: "promo_1", GateStatus: GateStatusNeedsReview, CreatedAt: now}), want: "event_id"},
		{name: "gate missing promotion id", err: ValidatePromotionGateLog(PromotionGateLog{EventID: "evt_1", GateStatus: GateStatusNeedsReview, CreatedAt: now}), want: "promotion_id"},
		{name: "gate missing status", err: ValidatePromotionGateLog(PromotionGateLog{EventID: "evt_1", PromotionID: "promo_1", CreatedAt: now}), want: "gate_status"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err == nil || !strings.Contains(tc.err.Error(), tc.want) {
				t.Fatalf("err=%v, want %q", tc.err, tc.want)
			}
		})
	}
}
