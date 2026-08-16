package memory

import (
	"testing"
	"time"
)

func TestRankUserMemoriesForRecallIsStableAndPinnedFirst(t *testing.T) {
	now := time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC)
	items := []UserMemory{
		{ID: "z", Statement: "blue note", State: MemoryStateConfirmed, Sensitivity: "normal", Scope: "all_personas", Active: true, UpdatedAt: now},
		{ID: "a", Statement: "blue note", State: MemoryStateConfirmed, Sensitivity: "normal", Scope: "all_personas", Active: true, UpdatedAt: now},
		{ID: "pinned", Statement: "unrelated", State: MemoryStatePinned, Sensitivity: "normal", Scope: "all_personas", Active: true, UpdatedAt: now.Add(-time.Hour)},
	}
	decisions := RankUserMemoriesForRecall("blue", items, 2)
	if len(decisions) != 3 || decisions[0].Item.ID != "pinned" || decisions[0].Status != UserMemoryRecallStatusInjected || decisions[1].Item.ID != "a" || decisions[2].Status != UserMemoryRecallStatusBudgetDropped {
		t.Fatalf("decisions=%+v", decisions)
	}
}

func TestRankUserMemoriesForRecallForPersonaFiltersScopeBeforeBudget(t *testing.T) {
	items := []UserMemory{
		{ID: "shiro-only", Statement: "blue exact", State: MemoryStateConfirmed, Sensitivity: "normal", Scope: "shiro_only", Active: true},
		{ID: "shared", Statement: "other note", State: MemoryStateConfirmed, Sensitivity: "normal", Scope: "all_personas", Active: true},
		{ID: "unknown", Statement: "blue unknown", State: MemoryStateConfirmed, Sensitivity: "normal", Scope: "untrusted", Active: true},
	}
	decisions := RankUserMemoriesForRecallForPersona("blue", items, 1, "mio")
	status := map[string]UserMemoryRecallDecision{}
	for _, decision := range decisions {
		status[decision.Item.ID] = decision
	}
	if !status["shared"].Selected || status["shared"].Status != UserMemoryRecallStatusInjected {
		t.Fatalf("shared decision=%+v", status["shared"])
	}
	if status["shiro-only"].Selected || status["shiro-only"].Status != UserMemoryRecallStatusFilteredScope || status["unknown"].Status != UserMemoryRecallStatusFilteredScope {
		t.Fatalf("scope decisions=%+v", status)
	}
}

func TestRankUserMemoriesForRecallRecordsEveryExclusionReason(t *testing.T) {
	items := []UserMemory{
		{ID: "candidate", State: MemoryStateCandidate, Active: true, Scope: "all_personas"},
		{ID: "sensitive", State: MemoryStateConfirmed, Sensitivity: "sensitive", Active: true, Scope: "all_personas"},
		{ID: "inactive", State: MemoryStateConfirmed, Active: false, Scope: "all_personas"},
		{ID: "superseded", State: MemoryStateConfirmed, Active: true, SupersededBy: "new", Scope: "all_personas"},
		{ID: "decayed", State: MemoryStateConfirmed, Active: true, LifecycleStatus: "decayed", Scope: "all_personas"},
	}
	decisions := RankUserMemoriesForRecall("query", items, 1)
	for _, decision := range decisions {
		if decision.Status == "" || decision.Reason == "" {
			t.Fatalf("unreasoned exclusion=%+v", decision)
		}
	}
}
