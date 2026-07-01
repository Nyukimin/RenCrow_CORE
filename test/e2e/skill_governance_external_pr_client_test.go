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

func TestE2E_SkillGovernanceExternalPRClientRequiresApprovalAndAuditsBlockedSubmit(t *testing.T) {
	if os.Getenv("PICOCLAW_LIVE_E2E") != "1" {
		t.Skip("set PICOCLAW_LIVE_E2E=1 to verify live Skill Governance external PR client")
	}

	baseURL := liveBaseURL()
	client, err := rencrowclient.New(baseURL, rencrowclient.WithHTTPClient(&http.Client{Timeout: 5 * time.Second}))
	if err != nil {
		t.Fatalf("create RenCrow client: %v", err)
	}

	suffix := time.Now().UTC().Format("20060102150405")
	contributionEventID := "evt_skill_pr_client_e2e_" + suffix
	submitID := "submit_skill_pr_client_e2e_" + suffix
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	gate, err := client.EvaluateSkillGovernanceContributionGate(ctx, rencrowclient.SkillGovernanceContributionGateRequest{
		EventID:             contributionEventID,
		Repo:                "example/repo",
		TargetBranch:        "main",
		ProblemStatement:    "live client E2E: external PR submit must not create PR without configured adapter",
		ExistingPRsChecked:  true,
		RealProblemVerified: true,
		CoreChangeVerified:  true,
		DiffHumanApproved:   true,
		TestResult:          "PICOCLAW_LIVE_E2E=1 go test -tags=e2e ./test/e2e -run TestE2E_SkillGovernanceExternalPRClientRequiresApprovalAndAuditsBlockedSubmit",
	})
	if err != nil {
		t.Fatalf("EvaluateSkillGovernanceContributionGate() live call failed at %s: %v", baseURL, err)
	}
	if gate.Decision.Status != "passed" || !gate.Decision.CanContribute || gate.GateLog.EventID != contributionEventID {
		t.Fatalf("contribution gate=%+v, want passed event_id=%s", gate, contributionEventID)
	}

	_, err = client.SubmitSkillGovernanceExternalPR(ctx, rencrowclient.SkillGovernanceExternalPRSubmitRequest{
		SubmitID:            submitID + "_noapproval",
		ContributionEventID: contributionEventID,
		Repo:                "example/repo",
		Title:               "Live client E2E audit boundary",
		HumanApproved:       false,
	})
	if err == nil || !strings.Contains(err.Error(), "requires human_approved") {
		t.Fatalf("SubmitSkillGovernanceExternalPR() without approval err=%v, want client-side human_approved validation", err)
	}

	resp, err := client.SubmitSkillGovernanceExternalPR(ctx, rencrowclient.SkillGovernanceExternalPRSubmitRequest{
		SubmitID:            submitID,
		ContributionEventID: contributionEventID,
		Repo:                "example/repo",
		Title:               "Live client E2E audit boundary",
		DiffPath:            "workspace/logs/skill_governance/coder_evidence/e2e/skill_diff.md",
		TestResult:          "PICOCLAW_LIVE_E2E=1 go test -tags=e2e ./test/e2e -run TestE2E_SkillGovernanceExternalPRClientRequiresApprovalAndAuditsBlockedSubmit",
		HumanApproved:       true,
	})
	if err != nil {
		t.Fatalf("SubmitSkillGovernanceExternalPR() live call failed at %s: %v", baseURL, err)
	}
	if resp.ExternalPRCreated || resp.PostSubmitVerified || resp.Record.SubmitStatus != "blocked" {
		t.Fatalf("external PR submit response=%+v, want blocked audit without PR creation", resp)
	}
	if resp.Record.FailureReason != "external PR adapter is not configured" {
		t.Fatalf("failure_reason=%q", resp.Record.FailureReason)
	}

	status, err := client.SkillGovernanceStatus(ctx, 20)
	if err != nil {
		t.Fatalf("SkillGovernanceStatus() live call failed at %s: %v", baseURL, err)
	}
	if status.ExternalPRAdapter != "unconfigured" || status.ExternalPRAdapterConfigured == nil || *status.ExternalPRAdapterConfigured || status.HumanApprovalRequiredForPR == nil || !*status.HumanApprovalRequiredForPR {
		t.Fatalf("live Skill Governance external PR readiness=%+v", status)
	}
	for _, record := range status.ExternalPRSubmitRecords {
		if record.SubmitID == submitID && record.SubmitStatus == "blocked" && !record.ExternalPRCreated {
			return
		}
	}
	t.Fatalf("live Skill Governance status did not include blocked external PR audit for %s; records=%+v", submitID, status.ExternalPRSubmitRecords)
}
