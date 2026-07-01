//go:build e2e

package e2e_test

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/pkg/rencrowclient"
)

func TestE2E_WorkstreamStatusClientCurrentView(t *testing.T) {
	if os.Getenv("PICOCLAW_LIVE_E2E") != "1" {
		t.Skip("set PICOCLAW_LIVE_E2E=1 to verify live Workstream status client")
	}

	baseURL := liveBaseURL()
	client, err := rencrowclient.New(baseURL, rencrowclient.WithHTTPClient(&http.Client{Timeout: 5 * time.Second}))
	if err != nil {
		t.Fatalf("create RenCrow client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	status, err := client.WorkstreamStatus(ctx, 20)
	if err != nil {
		t.Fatalf("WorkstreamStatus() live call failed at %s: %v", baseURL, err)
	}
	if len(status.Goals) == 0 {
		t.Fatalf("live Workstream status has no goals; cannot verify goal current view")
	}
	if len(status.Artifacts) == 0 {
		t.Fatalf("live Workstream status has no artifacts; cannot verify artifact current view")
	}

	foundWaitingGoal := false
	for _, goal := range status.Goals {
		if goal.Status == "waiting" && goal.WorkstreamID != "" && goal.Title != "" {
			foundWaitingGoal = true
			break
		}
	}
	if !foundWaitingGoal {
		t.Fatalf("live Workstream status did not include a waiting goal with workstream_id and title")
	}

	foundPendingReviewArtifact := false
	for _, artifact := range status.Artifacts {
		if artifact.Status == "pending_review" && artifact.WorkstreamID != "" && artifact.Type != "" {
			foundPendingReviewArtifact = true
			break
		}
	}
	if !foundPendingReviewArtifact {
		t.Fatalf("live Workstream status did not include a pending_review artifact with workstream_id and artifact_type")
	}
}

func TestE2E_WorkstreamVaultUpdatePreviewAndApprovedApply(t *testing.T) {
	if os.Getenv("PICOCLAW_LIVE_E2E") != "1" {
		t.Skip("set PICOCLAW_LIVE_E2E=1 to verify live Workstream vault apply")
	}
	if os.Getenv("PICOCLAW_LIVE_WORKSTREAM_VAULT_E2E") != "1" {
		t.Skip("set PICOCLAW_LIVE_WORKSTREAM_VAULT_E2E=1 to allow an isolated live vault file write")
	}

	baseURL := liveBaseURL()
	client, err := rencrowclient.New(baseURL, rencrowclient.WithHTTPClient(&http.Client{Timeout: 10 * time.Second}))
	if err != nil {
		t.Fatalf("create RenCrow client: %v", err)
	}

	suffix := time.Now().UTC().Format("20060102150405.000000000")
	workstreamID := "ws_vault_apply_" + suffix
	updateID := "vault_apply_" + suffix
	filePath := "live-e2e/" + workstreamID + "/STATUS.md"
	proposed := "# Live Vault Apply E2E\n\nupdate_id: " + updateID + "\nstatus: applied\n"
	item := rencrowclient.WorkstreamVaultUpdate{
		UpdateID:        updateID,
		WorkstreamID:    workstreamID,
		FilePath:        filePath,
		ReviewStatus:    "pending",
		ProposedContent: proposed,
		CreatedAt:       time.Now().UTC(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	created, err := client.CreateWorkstreamVaultUpdate(ctx, item)
	if err != nil {
		t.Fatalf("CreateWorkstreamVaultUpdate() live call failed at %s: %v", baseURL, err)
	}
	if created.Applied || created.VaultUpdate.ReviewStatus != "pending" {
		t.Fatalf("create response=%+v, want pending without apply", created)
	}

	preview, err := client.PreviewWorkstreamVaultUpdate(ctx, item)
	if err != nil {
		t.Fatalf("PreviewWorkstreamVaultUpdate() live call failed at %s: %v", baseURL, err)
	}
	if preview.Preview.UpdateID != updateID || !strings.Contains(preview.Preview.ProposedContent, updateID) || preview.Preview.UnifiedDiff == "" {
		t.Fatalf("preview response=%+v, want proposed content and diff", preview)
	}

	item.ReviewStatus = "approved"
	reviewed, err := client.ReviewWorkstreamVaultUpdate(ctx, item)
	if err != nil {
		t.Fatalf("ReviewWorkstreamVaultUpdate() live call failed at %s: %v", baseURL, err)
	}
	if !reviewed.Applied || reviewed.AppliedPath == "" || reviewed.VaultUpdate.ReviewStatus != "approved" {
		t.Fatalf("review response=%+v, want approved applied vault update", reviewed)
	}
	assertE2EFile(t, reviewed.AppliedPath, proposed)

	status, err := client.WorkstreamStatus(ctx, 100)
	if err != nil {
		t.Fatalf("WorkstreamStatus() live call failed at %s: %v", baseURL, err)
	}
	foundApproved := false
	foundApplied := false
	for _, update := range status.VaultUpdates {
		if update.UpdateID != updateID {
			continue
		}
		if update.ReviewStatus == "approved" {
			foundApproved = true
		}
		if update.Applied && update.AppliedPath == reviewed.AppliedPath {
			foundApplied = true
		}
	}
	if !foundApproved || !foundApplied {
		t.Fatalf("status vault_updates did not expose latest approved applied evidence for %s: approved=%v applied=%v", updateID, foundApproved, foundApplied)
	}
}
