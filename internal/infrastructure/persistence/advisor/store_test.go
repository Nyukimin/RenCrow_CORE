package advisor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	advisorDomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/advisor"
	domainagentprofile "github.com/Nyukimin/RenCrow_CORE/internal/domain/agentprofile"
)

type advisorStore interface {
	SaveAdviceRun(context.Context, advisorDomain.AdviceRunRecord) error
	ListAdviceRuns(context.Context, int) ([]advisorDomain.AdviceRunRecord, error)
	SaveAdvisorAdoption(context.Context, advisorDomain.AdvisorAdoptionRecord) error
	ListAdvisorAdoptions(context.Context, int) ([]advisorDomain.AdvisorAdoptionRecord, error)
	SaveAdvisorScoreSnapshot(context.Context, advisorDomain.AdvisorScoreSnapshot) error
	ListAdvisorScoreSnapshots(context.Context, int) ([]advisorDomain.AdvisorScoreSnapshot, error)
	SaveAgentPolicyDecision(context.Context, domainagentprofile.PolicyDecision) error
	ListAgentPolicyDecisions(context.Context, int) ([]domainagentprofile.PolicyDecision, error)
}

func TestAdvisorStoresRoundTripNewestFirst(t *testing.T) {
	tests := []struct {
		name string
		new  func(t *testing.T) advisorStore
	}{
		{name: "jsonl", new: func(t *testing.T) advisorStore {
			return NewJSONLStore(filepath.Join(t.TempDir(), "advisor"))
		}},
		{name: "sqlite", new: func(t *testing.T) advisorStore {
			store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "advisor.db"))
			if err != nil {
				t.Fatalf("NewSQLiteStore failed: %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })
			return store
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			store := tt.new(t)
			now := time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)
			for index, runID := range []string{"run-1", "run-2"} {
				run := advisorDomain.AdviceRunRecord{
					RunID:            runID,
					RequestedByAgent: "shiro",
					AdvisorID:        advisorDomain.AdvisorCodex,
					ApprovalMode:     "advice_only",
					Status:           advisorDomain.AdviceStatus(advisorDomain.StatusCompleted),
					PromptHash:       "prompt-hash",
					OutputHash:       "output-hash",
					StartedAt:        now.Add(time.Duration(index) * time.Minute),
					FinishedAt:       now.Add(time.Duration(index)*time.Minute + time.Second),
					LatencyMillis:    1000,
				}
				if err := store.SaveAdviceRun(ctx, run); err != nil {
					t.Fatalf("SaveAdviceRun failed: %v", err)
				}
			}
			runs, err := store.ListAdviceRuns(ctx, 1)
			if err != nil || len(runs) != 1 || runs[0].RunID != "run-2" {
				t.Fatalf("unexpected runs=%#v err=%v", runs, err)
			}

			adoption := advisorDomain.AdvisorAdoptionRecord{
				AdoptionID: "adopt-1", RunID: "run-2", AdvisorID: advisorDomain.AdvisorCodex,
				AdoptedByAgent: "shiro", Adopted: true, Outcome: "success", CreatedAt: now,
			}
			if err := store.SaveAdvisorAdoption(ctx, adoption); err != nil {
				t.Fatalf("SaveAdvisorAdoption failed: %v", err)
			}
			adoptions, err := store.ListAdvisorAdoptions(ctx, 10)
			if err != nil || len(adoptions) != 1 || adoptions[0].AdoptionID != "adopt-1" {
				t.Fatalf("unexpected adoptions=%#v err=%v", adoptions, err)
			}

			snapshot := advisorDomain.AdvisorScoreSnapshot{
				SnapshotID: "score-1", AdvisorID: advisorDomain.AdvisorCodex,
				WindowStart: now, WindowEnd: now.Add(24 * time.Hour), Score: 0.8, CreatedAt: now,
			}
			if err := store.SaveAdvisorScoreSnapshot(ctx, snapshot); err != nil {
				t.Fatalf("SaveAdvisorScoreSnapshot failed: %v", err)
			}
			snapshots, err := store.ListAdvisorScoreSnapshots(ctx, 10)
			if err != nil || len(snapshots) != 1 || snapshots[0].SnapshotID != "score-1" {
				t.Fatalf("unexpected snapshots=%#v err=%v", snapshots, err)
			}

			decision := domainagentprofile.PolicyDecision{
				DecisionID: "decision-1", AgentID: "shiro", Action: "ask_advisor",
				Decision: domainagentprofile.PolicyAllowed, Reason: "allowed", CreatedAt: now,
			}
			if err := store.SaveAgentPolicyDecision(ctx, decision); err != nil {
				t.Fatalf("SaveAgentPolicyDecision failed: %v", err)
			}
			decisions, err := store.ListAgentPolicyDecisions(ctx, 10)
			if err != nil || len(decisions) != 1 || decisions[0].DecisionID != "decision-1" {
				t.Fatalf("unexpected decisions=%#v err=%v", decisions, err)
			}
		})
	}
}

func TestJSONLStoreDoesNotPersistPromptOrRawOutputFields(t *testing.T) {
	root := filepath.Join(t.TempDir(), "advisor")
	store := NewJSONLStore(root)
	now := time.Now().UTC()
	err := store.SaveAdviceRun(context.Background(), advisorDomain.AdviceRunRecord{
		RunID: "run-1", RequestedByAgent: "shiro", AdvisorID: advisorDomain.AdvisorCodex,
		ApprovalMode: "advice_only", Status: advisorDomain.AdviceStatus(advisorDomain.StatusCompleted),
		PromptHash: "safe-prompt-hash", OutputHash: "safe-output-hash", StartedAt: now, FinishedAt: now,
	})
	if err != nil {
		t.Fatalf("SaveAdviceRun failed: %v", err)
	}
	payload, err := os.ReadFile(filepath.Join(root, "advisor_run.jsonl"))
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	for _, forbidden := range []string{"\"prompt\"", "\"raw_output\"", "\"output\""} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("persisted payload contains forbidden field %s: %s", forbidden, payload)
		}
	}
}
