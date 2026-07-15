package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	domainadvisor "github.com/Nyukimin/RenCrow_CORE/internal/domain/advisor"
	domainagentprofile "github.com/Nyukimin/RenCrow_CORE/internal/domain/agentprofile"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainrelation "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgerelation"
	advisorpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/advisor"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

func main() {
	runtimeRoot := os.Getenv("RENCROW_E2E_RUNTIME")
	if runtimeRoot == "" {
		panic("RENCROW_E2E_RUNTIME is required")
	}
	ctx := context.Background()
	now := time.Now().UTC()
	longSuffix := "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

	advisorStore := advisorpersistence.NewJSONLStore(filepath.Join(runtimeRoot, "workspace", "logs", "advisor"))
	runID := "advisor-run-e2e-" + longSuffix
	must(advisorStore.SaveAdviceRun(ctx, domainadvisor.AdviceRunRecord{
		RunID: runID, RequestedByAgent: "shiro", AdvisorID: domainadvisor.AdvisorCodex,
		Purpose: "browser E2E evidence", ApprovalMode: "advice_only",
		Status: domainadvisor.AdviceStatus(domainadvisor.StatusCompleted), Summary: "safe summary",
		StartedAt: now.Add(-250 * time.Millisecond), FinishedAt: now, LatencyMillis: 250,
	}))
	must(advisorStore.SaveAdvisorScoreSnapshot(ctx, domainadvisor.AdvisorScoreSnapshot{
		SnapshotID: "advisor-score-e2e", AdvisorID: domainadvisor.AdvisorCodex,
		WindowStart: now.Add(-time.Hour), WindowEnd: now, RequestCount: 1, CompletedCount: 1,
		AdoptedCount: 1, SuccessCount: 1, AvgLatencyMillis: 250, Score: 1, CreatedAt: now,
	}))
	must(advisorStore.SaveAgentPolicyDecision(ctx, domainagentprofile.PolicyDecision{
		DecisionID: "policy-decision-e2e", AgentID: "shiro", Action: "external_publish",
		Decision: domainagentprofile.PolicyApprovalRequired, Reason: "browser E2E approval boundary", CreatedAt: now,
	}))

	l1Path := filepath.Join(runtimeRoot, "workspace", "l1.db")
	must(os.MkdirAll(filepath.Dir(l1Path), 0o755))
	l1, err := l1sqlite.NewL1SQLiteStore(l1Path)
	must(err)
	defer l1.Close()

	itemIDs := make([]string, 0, 3)
	for index := 1; index <= 3; index++ {
		eventID := fmt.Sprintf("relation-e2e-%d", index)
		staged, saveErr := l1.SaveStagingItem(ctx, l1sqlite.L1StagingItem{
			Kind: l1sqlite.L1StagingKindExternalFetch, Namespace: "kb:general", EventID: eventID,
			SourceID: "browser-e2e", SourceURL: fmt.Sprintf("https://example.invalid/e2e/%d", index),
			FetchedAt:    now.Add(-time.Duration(index) * time.Minute),
			RawText:      fmt.Sprintf("Browser E2E relation body %d %s", index, longSuffix),
			SummaryDraft: fmt.Sprintf("safe relation summary %d", index), Keywords: []string{"e2e", "relation"},
			LicenseNote: "synthetic browser E2E fixture", Meta: map[string]interface{}{"title": fmt.Sprintf("E2E Relation %d", index)},
		})
		must(saveErr)
		_, validateErr := l1.ValidateStagingItem(ctx, staged.ID, l1sqlite.L1StagingValidationPolicy{
			SourceTrustScores: map[string]float64{"browser-e2e": 1}, MinimumTrustScore: 0.5, Now: now,
		})
		must(validateErr)
		item, promoteErr := l1.PromoteValidatedStagingItemToKnowledge(ctx, staged.ID, "general")
		must(promoteErr)
		itemIDs = append(itemIDs, item.ID)
	}
	must(l1.SaveKnowledgeEntity(ctx, l1sqlite.L1KnowledgeEntity{
		EntityID: "entity:browser-e2e", CanonicalName: "Browser E2E", EntityType: "test",
	}))
	for _, itemID := range itemIDs {
		must(l1.SaveKnowledgeItemEntity(ctx, l1sqlite.L1KnowledgeItemEntity{
			ItemID: itemID, EntityID: "entity:browser-e2e", RelationKind: "mentions", Score: 1, Evidence: "synthetic fixture",
		}))
	}
	must(l1.SaveKnowledgeItemRelation(ctx, domainrelation.Relation{
		SrcItemID: itemIDs[0], DstItemID: itemIDs[1], RelationType: domainrelation.RelationSameEntity,
		Score: 5, Evidence: "same synthetic entity",
	}))
	must(l1.SaveKnowledgeItemRelation(ctx, domainrelation.Relation{
		SrcItemID: itemIDs[1], DstItemID: itemIDs[2], RelationType: domainrelation.RelationSameTopic,
		Score: 4, Evidence: "same synthetic topic",
	}))
	must(l1.SaveRecallTrace(ctx, domainconversation.RecallTrace{
		ResponseID: "response-e2e-" + longSuffix, SessionID: "session-browser-e2e", Role: "mio", CreatedAt: now,
		Items: []domainconversation.RecallTraceItem{
			{Layer: "L3", Kind: "knowledge_relation", SourceID: itemIDs[1], Status: "injected", Summary: "safe injected fixture"},
			{Layer: "L3", Kind: "knowledge_relation", SourceID: itemIDs[2], Status: "rejected", Summary: "safe rejected fixture"},
		},
	}))
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
