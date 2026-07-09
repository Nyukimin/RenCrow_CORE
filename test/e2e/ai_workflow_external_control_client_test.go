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

	"github.com/Nyukimin/RenCrow_CORE/pkg/rencrowclient"
)

func TestE2E_AIWorkflowExternalControlClientRequiresApproval(t *testing.T) {
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
		Actor:         "Worker",
		ChannelID:     "viewer",
		Action:        "promotion_apply",
		HumanApproved: false,
	})
	if err != nil {
		t.Fatalf("CheckExternalControl() live call failed at %s: %v", baseURL, err)
	}
	if resp.Decision.Status != "needs_approval" || !resp.Decision.RequiresApproval {
		t.Fatalf("external control decision = %+v, want needs_approval with requires_approval=true", resp.Decision)
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
		WorkflowEvents []struct {
			EventType string `json:"event_type"`
			Status    string `json:"status"`
		} `json:"workflow_events"`
	}
	if err := json.NewDecoder(statusResp.Body).Decode(&body); err != nil {
		t.Fatalf("decode live AI Workflow status: %v", err)
	}
	for _, event := range body.WorkflowEvents {
		if event.EventType == "external_control_policy_checked" && event.Status == "needs_approval" {
			return
		}
	}
	t.Fatalf("live AI Workflow status did not include recent external_control_policy_checked needs_approval event")
}

func TestE2E_AIWorkflowCommandContextAndSuperAgentTraceSameRun(t *testing.T) {
	if os.Getenv("RENCROW_LIVE_E2E") != "1" {
		t.Skip("set RENCROW_LIVE_E2E=1 to verify live AI Workflow command/context client")
	}

	baseURL := liveBaseURL()
	client, err := rencrowclient.New(baseURL, rencrowclient.WithHTTPClient(&http.Client{Timeout: 5 * time.Second}))
	if err != nil {
		t.Fatalf("create RenCrow client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	status, err := client.AIWorkflowStatus(ctx, 100)
	if err != nil {
		t.Fatalf("AIWorkflowStatus() live call failed at %s: %v", baseURL, err)
	}
	if len(status.CommandRegistries) == 0 {
		t.Fatalf("live AI Workflow has no command registries; cannot verify command invocation flow")
	}
	commandName := status.CommandRegistries[0].CommandName
	for _, command := range status.CommandRegistries {
		if command.CommandName == "tool-harness-check" || command.CommandName == "/tool-harness-check" {
			commandName = command.CommandName
			break
		}
	}

	suffix := time.Now().UTC().Format("20060102150405.000000000")
	workstreamID := "ws_aiworkflow_client_" + suffix
	runID := "run_aiworkflow_client_" + suffix
	if err := client.CreateAgentRun(ctx, rencrowclient.AgentRun{
		RunID:        runID,
		WorkstreamID: workstreamID,
		AgentType:    "LeadAgent",
		Goal:         "live E2E AI Workflow command/context and SuperAgent trace same run",
		Status:       "running",
		StartedAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateAgentRun() live call failed at %s: %v", baseURL, err)
	}
	traceID := "trace_aiworkflow_client_" + suffix
	if err := client.CreateTraceEvent(ctx, rencrowclient.TraceEvent{
		EventID:        traceID,
		RunID:          runID,
		EventType:      "ai_workflow_client_e2e",
		Actor:          "Worker",
		PayloadSummary: workstreamID,
		Status:         "recorded",
		CreatedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateTraceEvent() live call failed at %s: %v", baseURL, err)
	}
	commandResp, err := client.RunCommand(ctx, rencrowclient.CommandRunRequest{
		CommandName:  commandName,
		WorkstreamID: workstreamID,
		RunID:        runID,
		Agent:        "Worker",
		Text:         "live E2E command/context client flow; do not modify files",
	})
	if err != nil {
		t.Fatalf("RunCommand() live call failed at %s: %v", baseURL, err)
	}
	if commandResp.Event.EventType != "command_invoked" || commandResp.Event.Status != "requested" {
		t.Fatalf("command response event = %+v, want command_invoked/requested", commandResp.Event)
	}
	if commandResp.Event.RunID != runID || commandResp.Event.WorkstreamID != workstreamID {
		t.Fatalf("command response event run/workstream = %+v, want run=%s workstream=%s", commandResp.Event, runID, workstreamID)
	}

	contextEventID := "ctx_aiworkflow_client_" + suffix
	budgetResp, err := client.CheckContextBudget(ctx, rencrowclient.ContextUsage{
		EventID:       contextEventID,
		SessionID:     runID,
		Agent:         "Worker",
		Model:         "live-e2e",
		ContextTokens: 128,
		ToolCallCount: 1,
		CreatedAt:     time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("CheckContextBudget() live call failed at %s: %v", baseURL, err)
	}
	if budgetResp.ContextUsage.EventID != contextEventID {
		t.Fatalf("context budget response = %+v, want event_id=%s", budgetResp, contextEventID)
	}

	after, err := client.AIWorkflowStatus(ctx, 100)
	if err != nil {
		t.Fatalf("AIWorkflowStatus() live follow-up failed at %s: %v", baseURL, err)
	}
	foundCommandEvent := false
	for _, event := range after.WorkflowEvents {
		if event.EventID == commandResp.Event.EventID && event.EventType == "command_invoked" && event.CommandName == commandName && event.RunID == runID && event.WorkstreamID == workstreamID {
			foundCommandEvent = true
			break
		}
	}
	if !foundCommandEvent {
		t.Fatalf("live AI Workflow status did not include command event %q for command %q", commandResp.Event.EventID, commandName)
	}
	foundContextUsage := false
	for _, usage := range after.ContextUsages {
		if usage.EventID == contextEventID && usage.SessionID == runID {
			foundContextUsage = true
			break
		}
	}
	if !foundContextUsage {
		t.Fatalf("live AI Workflow status did not include context usage %q for run %q", contextEventID, runID)
	}
	superagent, err := client.SuperAgentStatus(ctx, 100)
	if err != nil {
		t.Fatalf("SuperAgentStatus() live follow-up failed at %s: %v", baseURL, err)
	}
	foundRun := false
	for _, run := range superagent.AgentRuns {
		if run.RunID == runID && run.WorkstreamID == workstreamID {
			foundRun = true
			break
		}
	}
	if !foundRun {
		t.Fatalf("live SuperAgent status did not include run %q for workstream %q", runID, workstreamID)
	}
	foundTrace := false
	for _, event := range superagent.TraceEvents {
		if event.EventID == traceID && event.RunID == runID {
			foundTrace = true
			break
		}
	}
	if !foundTrace {
		t.Fatalf("live SuperAgent status did not include trace %q for run %q", traceID, runID)
	}
}

func TestE2E_AIWorkflowPromotionWorkflowRequiresHumanApprovalBeforeApply(t *testing.T) {
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
			Reason:                    "live E2E: verify promotion workflow stops before apply without final human approval",
			RollbackPlanPath:          "sandbox/live-e2e/rollback.md",
			PostApplyVerificationPath: "sandbox/live-e2e/post-apply.md",
			HumanApprovalStatus:       "granted",
			CreatedAt:                 time.Now().UTC(),
		},
		ApplyAfterApproval:        true,
		AppliedBy:                 "Worker",
		PostApplyVerificationPath: "sandbox/live-e2e/post-apply.md",
		HumanApproved:             false,
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
	if resp.PromotionResponse.Decision.Status != "approve" {
		t.Fatalf("promotion gate decision = %+v, want approve", resp.PromotionResponse.Decision)
	}
	if resp.Applied || resp.ApplyResponse != nil || resp.SkippedReason != "human approval is required before apply" {
		t.Fatalf("promotion workflow response=%+v, want skipped before apply for missing human approval", resp)
	}

	status, err := client.SandboxStatus(ctx, 100)
	if err != nil {
		t.Fatalf("SandboxStatus() live call failed at %s: %v", baseURL, err)
	}
	foundPromotion := false
	for _, promotion := range status.Promotions {
		if promotion.PromotionID == promotionID && promotion.HumanApprovalStatus == "granted" {
			foundPromotion = true
			break
		}
	}
	if !foundPromotion {
		t.Fatalf("live Sandbox status did not include promotion request %q", promotionID)
	}
	foundGateApprove := false
	foundAppliedLog := false
	for _, log := range status.GateLogs {
		if log.PromotionID != promotionID {
			continue
		}
		switch log.GateStatus {
		case "approve":
			foundGateApprove = true
		case "promotion_applied":
			foundAppliedLog = true
		}
	}
	if !foundGateApprove {
		t.Fatalf("live Sandbox status did not include approve gate log for promotion %q", promotionID)
	}
	if foundAppliedLog {
		t.Fatalf("live Sandbox status included promotion_applied log for promotion %q despite missing final human approval", promotionID)
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
