//go:build e2e

package e2e_test

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/pkg/rencrowclient"
)

func TestE2E_RevenueExternalSendClientAuditsPolicyBlockedApply(t *testing.T) {
	if os.Getenv("RENCROW_LIVE_E2E") != "1" {
		t.Skip("set RENCROW_LIVE_E2E=1 to verify live Revenue external send client")
	}

	baseURL := liveBaseURL()
	client, err := rencrowclient.New(baseURL, rencrowclient.WithHTTPClient(&http.Client{Timeout: 5 * time.Second}))
	if err != nil {
		t.Fatalf("create RenCrow client: %v", err)
	}

	suffix := time.Now().UTC().Format("20060102150405")
	draftID := "rev_draft_client_e2e_" + suffix
	decisionID := "rev_decision_client_e2e_" + suffix
	applyID := "rev_apply_client_e2e_" + suffix
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	draft, err := client.CreateRevenueChannelDraft(ctx, rencrowclient.RevenueChannelDraft{
		DraftID:             draftID,
		Channel:             "email",
		Subject:             "Live client E2E audit boundary",
		Body:                "外部送信せず、同期policyの apply audit だけを確認する下書きです。",
		ApprovalStatus:      "not_required",
		ExternalSendApplied: true,
	})
	if err != nil {
		t.Fatalf("CreateRevenueChannelDraft() live call failed at %s: %v", baseURL, err)
	}
	if draft.Draft.ExternalSendApplied || draft.ExternalActionsApplied || draft.HumanApprovalRequiredForExternalSendApply {
		t.Fatalf("channel draft response=%+v, want draft-only without external send", draft)
	}

	decision, err := client.EvaluateRevenueHumanDecision(ctx, rencrowclient.RevenueHumanDecision{
		DecisionID:     decisionID,
		DecisionType:   "closed_channel_send",
		SubjectID:      draftID,
		Description:    "live client E2E: evaluate policy before external send apply",
		ApprovalStatus: "not_required",
	})
	if err != nil {
		t.Fatalf("EvaluateRevenueHumanDecision() live call failed at %s: %v", baseURL, err)
	}
	if decision.Record.DecisionID != decisionID || decision.Result.RequiresApproval || decision.Result.Status != "allowed" {
		t.Fatalf("policy decision response=%+v, want allowed without approval wait", decision)
	}

	apply, err := client.ApplyRevenueExternalSend(ctx, rencrowclient.RevenueExternalSendApplyRequest{
		ApplyID:       applyID,
		DraftID:       draftID,
		DecisionID:    decisionID,
		Destination:   "customer@example.invalid",
		HumanApproved: false,
	})
	if err != nil {
		t.Fatalf("ApplyRevenueExternalSend() live call failed at %s: %v", baseURL, err)
	}
	if apply.ExternalActionsApplied || apply.PostSendVerified || apply.Record.ApplyStatus != "blocked" || apply.Record.ExternalSendApplied {
		t.Fatalf("external send apply response=%+v, want blocked audit without send", apply)
	}
	if apply.Record.FailureReason != "external channel adapter is not configured" {
		t.Fatalf("failure_reason=%q", apply.Record.FailureReason)
	}

	status, err := client.RevenueStatus(ctx, 20)
	if err != nil {
		t.Fatalf("RevenueStatus() live call failed at %s: %v", baseURL, err)
	}
	if status.ExternalChannelAdapter != "unconfigured" || status.ExternalChannelAdapterConfigured == nil || *status.ExternalChannelAdapterConfigured || status.HumanApprovalRequiredForExternalSend == nil || *status.HumanApprovalRequiredForExternalSend {
		t.Fatalf("revenue external channel readiness=%+v", status)
	}
	for _, record := range status.ExternalSendApplyRecords {
		if record.ApplyID == applyID && record.ApplyStatus == "blocked" && !record.ExternalSendApplied {
			return
		}
	}
	t.Fatalf("live Revenue status did not include blocked external send audit for %s; records=%+v", applyID, status.ExternalSendApplyRecords)
}
