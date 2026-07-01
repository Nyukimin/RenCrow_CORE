//go:build e2e

package e2e_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/pkg/rencrowclient"
)

func TestE2E_SandboxPromotionApplyAndRollbackWithHumanApproval(t *testing.T) {
	if os.Getenv("PICOCLAW_LIVE_E2E") != "1" {
		t.Skip("set PICOCLAW_LIVE_E2E=1 to verify live Sandbox promotion apply/rollback")
	}
	if os.Getenv("PICOCLAW_LIVE_SANDBOX_E2E") != "1" {
		t.Skip("set PICOCLAW_LIVE_SANDBOX_E2E=1 with sandbox enabled and isolated apply_root")
	}

	baseURL := liveBaseURL()
	client, err := rencrowclient.New(baseURL, rencrowclient.WithHTTPClient(&http.Client{Timeout: 10 * time.Second}))
	if err != nil {
		t.Fatalf("create RenCrow client: %v", err)
	}

	suffix := time.Now().UTC().Format("20060102150405.000000000")
	promotionID := "promo_sandbox_apply_rollback_" + suffix
	sandboxID := "sbx_sandbox_apply_rollback_" + suffix
	targetPath := filepath.ToSlash(filepath.Join("docs", "sandbox-apply-rollback-"+suffix+".md"))
	sandboxRoot := liveSandboxRoot()
	applyRoot := liveSandboxApplyRoot()

	writeE2EFile(t, filepath.Join(applyRoot, filepath.FromSlash(targetPath)), "one\ntwo\nthree\n")
	diffPath := filepath.ToSlash(filepath.Join("live-e2e", promotionID, "diff.patch"))
	testResultPath := filepath.ToSlash(filepath.Join("live-e2e", promotionID, "test.log"))
	rollbackPlanPath := filepath.ToSlash(filepath.Join("live-e2e", promotionID, "rollback.md"))
	postApplyPath := filepath.ToSlash(filepath.Join("live-e2e", promotionID, "post-apply.md"))
	writeE2EFile(t, filepath.Join(sandboxRoot, filepath.FromSlash(diffPath)), sandboxPromotionDiff(targetPath))
	writeE2EFile(t, filepath.Join(sandboxRoot, filepath.FromSlash(testResultPath)), "local verification fixture prepared\n")
	writeE2EFile(t, filepath.Join(sandboxRoot, filepath.FromSlash(rollbackPlanPath)), "rollback uses the reverse unified diff\n")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resp, err := client.SubmitPromotionWorkflow(ctx, rencrowclient.PromotionWorkflowRequest{
		Promotion: rencrowclient.PromotionRequest{
			PromotionID:               promotionID,
			SandboxID:                 sandboxID,
			WorkstreamID:              "ws_sandbox_apply_rollback_" + suffix,
			GoalID:                    "goal_sandbox_apply_rollback_" + suffix,
			RequestedBy:               "Worker",
			TargetPath:                targetPath,
			DiffPath:                  diffPath,
			TestResultPath:            testResultPath,
			Reason:                    "live E2E: verify sandbox promotion apply and rollback with human approval",
			RollbackPlanPath:          rollbackPlanPath,
			PostApplyVerificationPath: postApplyPath,
			HumanApprovalStatus:       "granted",
			CreatedAt:                 time.Now().UTC(),
		},
		ApplyAfterApproval:           true,
		AppliedBy:                    "Worker",
		ApplyTarget:                  targetPath,
		PostApplyVerificationPath:    postApplyPath,
		PostApplyVerificationCommand: "grep -q '^TWO$' " + shellQuote(filepath.Join(applyRoot, filepath.FromSlash(targetPath))) + " && printf verified",
		HumanApproved:                true,
		ExternalControl: &rencrowclient.ExternalControlRequest{
			Actor:         "Worker",
			ChannelID:     "viewer",
			Action:        "promotion_apply",
			HumanApproved: true,
		},
	})
	if err != nil {
		t.Fatalf("SubmitPromotionWorkflow() live call failed at %s: %v", baseURL, err)
	}
	if !resp.Applied || resp.ApplyResponse == nil || resp.ApplyResponse.DiffApplyResult == nil {
		t.Fatalf("promotion workflow response=%+v, want applied diff result", resp)
	}
	assertE2EFile(t, filepath.Join(applyRoot, filepath.FromSlash(targetPath)), "one\nTWO\nthree\n")

	rollback, err := client.RollbackPromotion(ctx, rencrowclient.PromotionApplyRequest{
		Promotion:                 resp.PromotionResponse.Promotion,
		AppliedBy:                 "Worker",
		ApplyTarget:               targetPath,
		PostApplyVerificationPath: postApplyPath,
		HumanApproved:             true,
	})
	if err != nil {
		t.Fatalf("RollbackPromotion() live call failed at %s: %v", baseURL, err)
	}
	if rollback.RollbackResult.Status != "rolled_back" || len(rollback.RollbackResult.AppliedFiles) == 0 {
		t.Fatalf("rollback response=%+v, want rolled_back with applied files", rollback)
	}
	assertE2EFile(t, filepath.Join(applyRoot, filepath.FromSlash(targetPath)), "one\ntwo\nthree\n")

	status, err := client.SandboxStatus(ctx, 100)
	if err != nil {
		t.Fatalf("SandboxStatus() live call failed at %s: %v", baseURL, err)
	}
	assertSandboxPromotionEvidence(t, status, promotionID, sandboxID, postApplyPath, rollbackPlanPath)
}

func liveSandboxRoot() string {
	if root := strings.TrimSpace(os.Getenv("PICOCLAW_LIVE_SANDBOX_ROOT")); root != "" {
		return root
	}
	return "/home/nyukimi/picoclaw_multiLLM/workspace/sandbox-live-e2e"
}

func liveSandboxApplyRoot() string {
	if root := strings.TrimSpace(os.Getenv("PICOCLAW_LIVE_SANDBOX_APPLY_ROOT")); root != "" {
		return root
	}
	return "/tmp/picoclaw-sandbox-promotion-apply-root"
}

func sandboxPromotionDiff(targetPath string) string {
	return "diff --git a/" + targetPath + " b/" + targetPath + "\n" +
		"--- a/" + targetPath + "\n" +
		"+++ b/" + targetPath + "\n" +
		"@@ -1,3 +1,3 @@\n" +
		" one\n" +
		"-two\n" +
		"+TWO\n" +
		" three\n"
}

func writeE2EFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create %s parent: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertE2EFile(t *testing.T, path string, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("%s content=%q, want %q", path, string(data), want)
	}
}

func assertSandboxPromotionEvidence(t *testing.T, status rencrowclient.SandboxStatus, promotionID string, sandboxID string, postApplyPath string, rollbackPlanPath string) {
	t.Helper()
	foundPromotion := false
	foundApply := false
	foundRollback := false
	foundPostApplyArtifact := false
	foundRollbackArtifact := false
	for _, promotion := range status.Promotions {
		if promotion.PromotionID == promotionID && promotion.SandboxID == sandboxID && promotion.HumanApprovalStatus == "granted" {
			foundPromotion = true
			break
		}
	}
	for _, log := range status.GateLogs {
		if log.PromotionID != promotionID {
			continue
		}
		if log.GateStatus == "promotion_applied" && log.PostApplyVerification == postApplyPath {
			foundApply = true
		}
		if log.GateStatus == "rollback_executed" {
			foundRollback = true
		}
	}
	for _, artifact := range status.Artifacts {
		if artifact.SandboxID != sandboxID {
			continue
		}
		if artifact.Type == "post_apply_verification" && artifact.FilePath == postApplyPath && artifact.Status == "completed" {
			foundPostApplyArtifact = true
		}
		if artifact.Type == "rollback_execution" && artifact.FilePath == rollbackPlanPath && artifact.Status == "completed" {
			foundRollbackArtifact = true
		}
	}
	if !foundPromotion || !foundApply || !foundRollback || !foundPostApplyArtifact || !foundRollbackArtifact {
		t.Fatalf("sandbox evidence missing promotion=%v apply=%v rollback=%v post_apply_artifact=%v rollback_artifact=%v", foundPromotion, foundApply, foundRollback, foundPostApplyArtifact, foundRollbackArtifact)
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
