package workstream

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	domainworkstream "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/workstream"
)

func TestSQLiteStoreSavesAndListsWorkstreamRecords(t *testing.T) {
	vaultRoot := filepath.Join(t.TempDir(), "vault")
	store, err := NewSQLiteStoreWithVault(filepath.Join(t.TempDir(), "workstream.db"), vaultRoot)
	if err != nil {
		t.Fatalf("NewSQLiteStoreWithVault() error = %v", err)
	}
	defer store.Close()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	if err := store.SaveWorkstream(context.Background(), domainworkstream.Workstream{
		WorkstreamID: "ws_1",
		Name:         "収益化",
		Status:       domainworkstream.StatusActive,
		CreatedAt:    now,
	}); err != nil {
		t.Fatalf("SaveWorkstream() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(vaultRoot, "ws_1", "STATUS.md")); err != nil {
		t.Fatalf("expected vault STATUS.md: %v", err)
	}
	if err := store.SaveGoal(context.Background(), domainworkstream.Goal{
		GoalID:          "goal_1",
		WorkstreamID:    "ws_1",
		Title:           "LPを作る",
		SuccessCriteria: []string{"CTAがある"},
		Verification:    []string{"Viewerで確認する"},
		Status:          domainworkstream.StatusActive,
		CreatedAt:       now,
	}); err != nil {
		t.Fatalf("SaveGoal() error = %v", err)
	}
	if err := store.SaveArtifact(context.Background(), domainworkstream.Artifact{
		ArtifactID:   "art_1",
		WorkstreamID: "ws_1",
		Type:         "markdown",
		Status:       "draft",
		CreatedAt:    now,
	}); err != nil {
		t.Fatalf("SaveArtifact() error = %v", err)
	}
	if err := store.SaveArtifactAnnotation(context.Background(), domainworkstream.ArtifactAnnotation{
		AnnotationID: "ann_1",
		ArtifactID:   "art_1",
		Comment:      "見出しが抽象的",
		Status:       "open",
		CreatedAt:    now,
	}); err != nil {
		t.Fatalf("SaveArtifactAnnotation() error = %v", err)
	}
	if err := store.SaveSteeringItem(context.Background(), domainworkstream.SteeringItem{
		SteeringID:   "stq_1",
		WorkstreamID: "ws_1",
		Instruction:  "CTAを直す",
		Status:       "pending",
		CreatedAt:    now,
	}); err != nil {
		t.Fatalf("SaveSteeringItem() error = %v", err)
	}
	if err := store.SaveHeartbeatSchedule(context.Background(), domainworkstream.HeartbeatSchedule{
		HeartbeatID:  "hb_1",
		WorkstreamID: "ws_1",
		ScheduleText: "daily 08:00",
		Task:         "確認",
		Status:       domainworkstream.StatusActive,
		CreatedAt:    now,
	}); err != nil {
		t.Fatalf("SaveHeartbeatSchedule() error = %v", err)
	}
	if err := store.SaveVaultUpdateLog(context.Background(), domainworkstream.VaultUpdateLog{
		UpdateID:     "vu_1",
		WorkstreamID: "ws_1",
		FilePath:     "STATUS.md",
		ReviewStatus: "pending",
		CreatedAt:    now,
	}); err != nil {
		t.Fatalf("SaveVaultUpdateLog() error = %v", err)
	}
	assertOne := func(name string, err error, got int) {
		t.Helper()
		if err != nil || got != 1 {
			t.Fatalf("%s count = %d, err = %v", name, got, err)
		}
	}
	workstreams, err := store.ListWorkstreams(context.Background(), 10)
	assertOne("workstreams", err, len(workstreams))
	goals, err := store.ListGoals(context.Background(), 10)
	assertOne("goals", err, len(goals))
	artifacts, err := store.ListArtifacts(context.Background(), 10)
	assertOne("artifacts", err, len(artifacts))
	annotations, err := store.ListArtifactAnnotations(context.Background(), 10)
	assertOne("annotations", err, len(annotations))
	steering, err := store.ListSteeringItems(context.Background(), 10)
	assertOne("steering", err, len(steering))
	heartbeats, err := store.ListHeartbeatSchedules(context.Background(), 10)
	assertOne("heartbeats", err, len(heartbeats))
	vaultUpdates, err := store.ListVaultUpdateLogs(context.Background(), 10)
	assertOne("vault updates", err, len(vaultUpdates))
}

func TestSQLiteStoreRejectsGoalWithoutSuccessCriteria(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "workstream.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	err = store.SaveGoal(context.Background(), domainworkstream.Goal{
		GoalID:       "goal_1",
		WorkstreamID: "ws_1",
		Title:        "LPを作る",
		Status:       domainworkstream.StatusActive,
		CreatedAt:    time.Now(),
	})
	if err == nil {
		t.Fatal("expected goal without success criteria to fail")
	}
}
