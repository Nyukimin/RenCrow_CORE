package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	domainrevenue "github.com/Nyukimin/RenCrow_CORE/internal/domain/revenue"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	revenuepersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/revenue"
	workstreampersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/workstream"
)

func TestRevenueOpportunityWorkstreamChainPersistsSameTrace(t *testing.T) {
	tempDir := t.TempDir()
	revenueStore, err := revenuepersistence.NewSQLiteStore(filepath.Join(tempDir, "revenue.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore(revenue): %v", err)
	}
	t.Cleanup(func() { _ = revenueStore.Close() })
	workstreamStore, err := workstreampersistence.NewSQLiteStore(filepath.Join(tempDir, "workstream.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore(workstream): %v", err)
	}
	t.Cleanup(func() { _ = workstreamStore.Close() })

	opportunityRec := httptest.NewRecorder()
	HandleRevenueOpportunities(revenueStore).ServeHTTP(
		opportunityRec,
		httptest.NewRequest(http.MethodPost, "/viewer/revenue/opportunities", bytes.NewBufferString(
			`{"opportunity_id":"opp-integration","source_kind":"note","title":"Integration opportunity"}`,
		)),
	)
	if opportunityRec.Code != http.StatusCreated {
		t.Fatalf("opportunity status=%d body=%s", opportunityRec.Code, opportunityRec.Body.String())
	}

	chainRec := httptest.NewRecorder()
	HandleRevenueOpportunityWorkstreamGoal(revenueStore, workstreamStore).ServeHTTP(
		chainRec,
		httptest.NewRequest(http.MethodPost, "/viewer/revenue/opportunities/workstream-goal", bytes.NewBufferString(
			`{"opportunity_id":"opp-integration","workstream_id":"ws-integration"}`,
		)),
	)
	if chainRec.Code != http.StatusCreated {
		t.Fatalf("chain status=%d body=%s", chainRec.Code, chainRec.Body.String())
	}
	var response struct {
		Goal                   domainworkstream.Goal                 `json:"goal"`
		Artifact               domainworkstream.Artifact             `json:"artifact"`
		Approval               domainrevenue.HumanDecisionGateRecord `json:"approval"`
		ExternalActionsApplied bool                                  `json:"external_actions_applied"`
	}
	if err := json.Unmarshal(chainRec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Goal.TraceID == "" || response.Artifact.TraceID != response.Goal.TraceID ||
		response.Approval.TraceID != response.Goal.TraceID || response.ExternalActionsApplied {
		t.Fatalf("response chain mismatch: %#v", response)
	}

	ctx := context.Background()
	goals, err := workstreamStore.ListGoals(ctx, 10)
	if err != nil {
		t.Fatalf("ListGoals: %v", err)
	}
	artifacts, err := workstreamStore.ListArtifacts(ctx, 10)
	if err != nil {
		t.Fatalf("ListArtifacts: %v", err)
	}
	approvals, err := revenueStore.ListHumanDecisionGateRecords(ctx, 10)
	if err != nil {
		t.Fatalf("ListHumanDecisionGateRecords: %v", err)
	}
	if len(goals) != 1 || len(artifacts) != 1 || len(approvals) != 1 {
		t.Fatalf("persisted chain goals=%#v artifacts=%#v approvals=%#v", goals, artifacts, approvals)
	}
	if goals[0].TraceID != response.Goal.TraceID || artifacts[0].TraceID != response.Goal.TraceID ||
		approvals[0].TraceID != response.Goal.TraceID || approvals[0].ApprovalStatus != "pending" {
		t.Fatalf("persisted trace mismatch goals=%#v artifacts=%#v approvals=%#v", goals, artifacts, approvals)
	}
}
