//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	"github.com/Nyukimin/RenCrow_CORE/pkg/rencrowclient"
)

func TestE2E_AIWorkflowExternalControlClientUsesSynchronousPolicy(t *testing.T) {
	if os.Getenv("RENCROW_LIVE_E2E") != "1" {
		t.Skip("set RENCROW_LIVE_E2E=1 to verify live AI Workflow external control client")
	}

	baseURL := liveBaseURL()
	client, err := rencrowclient.New(baseURL, rencrowclient.WithHTTPClient(&http.Client{Timeout: 5 * time.Second}))
	if err != nil {
		t.Fatalf("create RenCrow client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := client.CheckExternalControl(ctx, rencrowclient.ExternalControlRequest{
		Actor:     "Worker",
		ChannelID: "viewer",
		Action:    "promotion_apply",
	})
	if err != nil {
		t.Fatalf("CheckExternalControl() live call failed at %s: %v", baseURL, err)
	}
	if resp.Decision.Status != "allowed" {
		t.Fatalf("external control decision = %+v, want allowed", resp.Decision)
	}

	statusResp, err := http.Get(baseURL + "/viewer/ai-workflow?limit=20")
	if err != nil {
		t.Fatalf("live /viewer/ai-workflow failed at %s: %v", baseURL, err)
	}
	defer statusResp.Body.Close()
	if statusResp.StatusCode != http.StatusOK {
		t.Fatalf("live /viewer/ai-workflow status=%d, want 200", statusResp.StatusCode)
	}
	var body struct {
		Events []modulecore.EventEnvelope `json:"events"`
	}
	if err := json.NewDecoder(statusResp.Body).Decode(&body); err != nil {
		t.Fatalf("decode live AI Workflow status: %v", err)
	}
	for _, event := range body.Events {
		if event.EventType == "external_control_policy.checked" && event.Payload["status"] == "allowed" {
			return
		}
	}
	t.Fatalf("live AI Workflow status did not include recent external_control_policy_checked allowed event")
}

func TestE2E_AIWorkflowPromotionWorkflowSupportsExplicitPreviewOnly(t *testing.T) {
	if os.Getenv("RENCROW_LIVE_E2E") != "1" {
		t.Skip("set RENCROW_LIVE_E2E=1 to verify live AI Workflow promotion workflow client")
	}
	if os.Getenv("RENCROW_LIVE_SANDBOX_E2E") != "1" {
		t.Skip("set RENCROW_LIVE_SANDBOX_E2E=1 with sandbox enabled to verify live promotion workflow")
	}

	baseURL := liveBaseURL()
	client, err := rencrowclient.New(baseURL, rencrowclient.WithHTTPClient(&http.Client{Timeout: 5 * time.Second}))
	if err != nil {
		t.Fatalf("create RenCrow client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	suffix := time.Now().UTC().Format("20060102150405.000000000")
	promotionID := "promo_aiworkflow_client_" + suffix
	sandboxID := "sbx_aiworkflow_client_" + suffix
	resp, err := client.SubmitPromotionWorkflow(ctx, rencrowclient.PromotionWorkflowRequest{
		Promotion: rencrowclient.PromotionRequest{
			PromotionID:               promotionID,
			SandboxID:                 sandboxID,
			WorkstreamID:              "ws_aiworkflow_promotion_" + suffix,
			GoalID:                    "goal_aiworkflow_promotion_" + suffix,
			RequestedBy:               "Worker",
			TargetPath:                "internal/example.go",
			DiffPath:                  "sandbox/live-e2e/diff.patch",
			TestResultPath:            "sandbox/live-e2e/test.log",
			Reason:                    "live E2E: verify promotion workflow can remain preview-only",
			RollbackPlanPath:          "sandbox/live-e2e/rollback.md",
			PostApplyVerificationPath: "sandbox/live-e2e/post-apply.md",
			CreatedAt:                 time.Now().UTC(),
		},
		ApplyAfterPolicyPass:      false,
		AppliedBy:                 "Worker",
		PostApplyVerificationPath: "sandbox/live-e2e/post-apply.md",
		ExternalControl: &rencrowclient.ExternalControlRequest{
			Actor:     "Worker",
			ChannelID: "viewer",
			Action:    "promotion_apply",
		},
	})
	if err != nil {
		t.Fatalf("SubmitPromotionWorkflow() live call failed at %s: %v", baseURL, err)
	}
	if resp.PromotionResponse.Decision.Status != "passed" {
		t.Fatalf("promotion gate decision = %+v, want passed", resp.PromotionResponse.Decision)
	}
	if resp.Applied || resp.ApplyResponse != nil || resp.SkippedReason != "apply_after_policy_pass is false" {
		t.Fatalf("promotion workflow response=%+v, want explicit preview-only result", resp)
	}

	status, err := client.SandboxStatus(ctx, 100)
	if err != nil {
		t.Fatalf("SandboxStatus() live call failed at %s: %v", baseURL, err)
	}
	foundPromotion := false
	for _, promotion := range status.Promotions {
		if promotion.PromotionID == promotionID {
			foundPromotion = true
			break
		}
	}
	if !foundPromotion {
		t.Fatalf("live Sandbox status did not include promotion request %q", promotionID)
	}
	foundGatePassed := false
	foundAppliedLog := false
	for _, log := range status.GateLogs {
		if log.PromotionID != promotionID {
			continue
		}
		switch log.GateStatus {
		case "passed":
			foundGatePassed = true
		case "promotion_applied":
			foundAppliedLog = true
		}
	}
	if !foundGatePassed {
		t.Fatalf("live Sandbox status did not include passed gate log for promotion %q", promotionID)
	}
	if foundAppliedLog {
		t.Fatalf("live Sandbox status included promotion_applied log for preview-only promotion %q", promotionID)
	}
	foundRollbackArtifact := false
	foundPostApplyArtifact := false
	for _, artifact := range status.Artifacts {
		if artifact.SandboxID != sandboxID {
			continue
		}
		switch artifact.Type {
		case "rollback_plan":
			foundRollbackArtifact = true
		case "post_apply_verification":
			foundPostApplyArtifact = true
		}
	}
	if !foundRollbackArtifact || !foundPostApplyArtifact {
		t.Fatalf("live Sandbox status missing rollback/post-apply artifacts for sandbox=%s rollback=%v post_apply=%v", sandboxID, foundRollbackArtifact, foundPostApplyArtifact)
	}
}

func liveBaseURL() string {
	baseURL := strings.TrimRight(os.Getenv("RENCROW_LIVE_BASE_URL"), "/")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:18790"
	}
	return baseURL
}
