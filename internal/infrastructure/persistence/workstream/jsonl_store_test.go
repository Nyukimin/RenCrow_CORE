package workstream

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
)

func TestJSONLStoreSaveAndListWorkstreamRecords(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	ctx := context.Background()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	if err := store.SaveWorkstream(ctx, domainworkstream.Workstream{
		WorkstreamID: "ws_1",
		Name:         "収益化",
		Status:       domainworkstream.StatusActive,
		CreatedAt:    now,
	}); err != nil {
		t.Fatalf("SaveWorkstream failed: %v", err)
	}
	if err := store.SaveGoal(ctx, domainworkstream.Goal{
		GoalID:          "goal_1",
		WorkstreamID:    "ws_1",
		Title:           "低単価商品を作る",
		SuccessCriteria: []string{"対象読者が明確"},
		Verification:    []string{"Revenue checklist"},
		Status:          domainworkstream.StatusActive,
		CreatedAt:       now,
	}); err != nil {
		t.Fatalf("SaveGoal failed: %v", err)
	}
	if err := store.SaveArtifact(ctx, domainworkstream.Artifact{
		ArtifactID:   "art_1",
		WorkstreamID: "ws_1",
		Type:         "markdown",
		FilePath:     "vault/workstreams/ws_1/STATUS.md",
		Status:       "draft",
		CreatedAt:    now,
	}); err != nil {
		t.Fatalf("SaveArtifact failed: %v", err)
	}
	if err := store.SaveArtifactAnnotation(ctx, domainworkstream.ArtifactAnnotation{
		AnnotationID: "ann_1",
		ArtifactID:   "art_1",
		Comment:      "見出しが抽象的",
		Status:       "open",
		CreatedAt:    now,
	}); err != nil {
		t.Fatalf("SaveArtifactAnnotation failed: %v", err)
	}
	if err := store.SaveSteeringItem(ctx, domainworkstream.SteeringItem{
		SteeringID:   "stq_1",
		WorkstreamID: "ws_1",
		Instruction:  "CTAを具体化する",
		Status:       "pending",
		CreatedAt:    now,
	}); err != nil {
		t.Fatalf("SaveSteeringItem failed: %v", err)
	}
	if err := store.SaveHeartbeatSchedule(ctx, domainworkstream.HeartbeatSchedule{
		HeartbeatID:  "hb_1",
		WorkstreamID: "ws_1",
		ScheduleText: "daily 08:00",
		Task:         "draft report only",
		Status:       domainworkstream.StatusActive,
		CreatedAt:    now,
	}); err != nil {
		t.Fatalf("SaveHeartbeatSchedule failed: %v", err)
	}
	if err := store.SaveVaultUpdateLog(ctx, domainworkstream.VaultUpdateLog{
		UpdateID:     "upd_1",
		WorkstreamID: "ws_1",
		FilePath:     "vault/workstreams/ws_1/STATUS.md",
		UpdateType:   "status",
		ReviewStatus: "pending",
		CreatedAt:    now,
	}); err != nil {
		t.Fatalf("SaveVaultUpdateLog failed: %v", err)
	}

	workstreams, err := store.ListWorkstreams(ctx, 10)
	if err != nil || len(workstreams) != 1 || workstreams[0].WorkstreamID != "ws_1" {
		t.Fatalf("workstreams=%#v err=%v", workstreams, err)
	}
	goals, err := store.ListGoals(ctx, 10)
	if err != nil || len(goals) != 1 || goals[0].GoalID != "goal_1" {
		t.Fatalf("goals=%#v err=%v", goals, err)
	}
	artifacts, err := store.ListArtifacts(ctx, 10)
	if err != nil || len(artifacts) != 1 || artifacts[0].ArtifactID != "art_1" {
		t.Fatalf("artifacts=%#v err=%v", artifacts, err)
	}
	annotations, err := store.ListArtifactAnnotations(ctx, 10)
	if err != nil || len(annotations) != 1 || annotations[0].AnnotationID != "ann_1" {
		t.Fatalf("annotations=%#v err=%v", annotations, err)
	}
	steering, err := store.ListSteeringItems(ctx, 10)
	if err != nil || len(steering) != 1 || steering[0].SteeringID != "stq_1" {
		t.Fatalf("steering=%#v err=%v", steering, err)
	}
	heartbeats, err := store.ListHeartbeatSchedules(ctx, 10)
	if err != nil || len(heartbeats) != 1 || heartbeats[0].HeartbeatID != "hb_1" {
		t.Fatalf("heartbeats=%#v err=%v", heartbeats, err)
	}
	vaultUpdates, err := store.ListVaultUpdateLogs(ctx, 10)
	if err != nil || len(vaultUpdates) != 1 || vaultUpdates[0].UpdateID != "upd_1" {
		t.Fatalf("vaultUpdates=%#v err=%v", vaultUpdates, err)
	}
}

func TestJSONLStoreListsLatestVaultUpdatePerID(t *testing.T) {
	ctx := context.Background()
	store := NewJSONLStore(t.TempDir())
	now := time.Date(2026, 5, 20, 0, 20, 0, 0, time.UTC)
	for _, item := range []domainworkstream.VaultUpdateLog{
		{
			UpdateID:        "upd_1",
			WorkstreamID:    "ws_1",
			FilePath:        "vault/workstreams/ws_1/STATUS.md",
			ProposedContent: "draft",
			ReviewStatus:    "pending",
			CreatedAt:       now,
		},
		{
			UpdateID:        "upd_1",
			WorkstreamID:    "ws_1",
			FilePath:        "vault/workstreams/ws_1/STATUS.md",
			ProposedContent: "approved",
			ReviewStatus:    "approved",
			CreatedAt:       now.Add(time.Second),
		},
	} {
		if err := store.SaveVaultUpdateLog(ctx, item); err != nil {
			t.Fatalf("SaveVaultUpdateLog failed: %v", err)
		}
	}
	vaultUpdates, err := store.ListVaultUpdateLogs(ctx, 10)
	if err != nil {
		t.Fatalf("ListVaultUpdateLogs failed: %v", err)
	}
	if len(vaultUpdates) != 1 || vaultUpdates[0].UpdateID != "upd_1" || vaultUpdates[0].ReviewStatus != "approved" {
		t.Fatalf("vaultUpdates=%#v, want latest approved state only", vaultUpdates)
	}
}

func TestJSONLStoreRejectsGoalWithoutContract(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	err := store.SaveGoal(context.Background(), domainworkstream.Goal{
		GoalID:       "goal_1",
		WorkstreamID: "ws_1",
		Title:        "missing contract",
	})
	if err == nil {
		t.Fatal("expected missing success criteria / verification to fail")
	}
}

func TestJSONLStoreRejectsInvalidArtifactAndSteering(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	ctx := context.Background()
	if err := store.SaveArtifact(ctx, domainworkstream.Artifact{
		ArtifactID: "art_1",
	}); err == nil {
		t.Fatal("expected invalid artifact to fail")
	}
	if err := store.SaveArtifactAnnotation(ctx, domainworkstream.ArtifactAnnotation{
		AnnotationID: "ann_1",
	}); err == nil {
		t.Fatal("expected invalid artifact annotation to fail")
	}
	if err := store.SaveSteeringItem(ctx, domainworkstream.SteeringItem{
		SteeringID: "stq_1",
	}); err == nil {
		t.Fatal("expected invalid steering item to fail")
	}
	if err := store.SaveHeartbeatSchedule(ctx, domainworkstream.HeartbeatSchedule{
		HeartbeatID: "hb_1",
	}); err == nil {
		t.Fatal("expected invalid heartbeat schedule to fail")
	}
	if err := store.SaveVaultUpdateLog(ctx, domainworkstream.VaultUpdateLog{
		UpdateID: "upd_1",
	}); err == nil {
		t.Fatal("expected invalid vault update log to fail")
	}
}

func TestJSONLStoreWithVaultCreatesInitialFiles(t *testing.T) {
	root := t.TempDir()
	vaultRoot := filepath.Join(root, "vault")
	store := NewJSONLStoreWithVault(filepath.Join(root, "logs"), vaultRoot)
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	if err := store.SaveWorkstream(context.Background(), domainworkstream.Workstream{
		WorkstreamID: "ws_revenue",
		Name:         "収益化",
		Description:  "Revenue loop",
		Status:       domainworkstream.StatusActive,
		CreatedAt:    now,
	}); err != nil {
		t.Fatalf("SaveWorkstream failed: %v", err)
	}

	for _, name := range []string{"README.md", "STATUS.md", "TODO.md", "OPEN_LOOPS.md", "ARTIFACTS.md", "NOTES.md", "MEMORY.md"} {
		if _, err := os.Stat(filepath.Join(vaultRoot, "ws_revenue", name)); err != nil {
			t.Fatalf("expected %s to be created: %v", name, err)
		}
	}
	workstreams, err := store.ListWorkstreams(context.Background(), 1)
	if err != nil {
		t.Fatalf("ListWorkstreams failed: %v", err)
	}
	if len(workstreams) != 1 || workstreams[0].VaultPath != filepath.Join(vaultRoot, "ws_revenue") {
		t.Fatalf("unexpected workstream record: %#v", workstreams)
	}
}

func TestJSONLStoreWithVaultDoesNotOverwriteExistingFiles(t *testing.T) {
	root := t.TempDir()
	vaultRoot := filepath.Join(root, "vault")
	readme := filepath.Join(vaultRoot, "ws_existing", "README.md")
	if err := os.MkdirAll(filepath.Dir(readme), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(readme, []byte("existing content\n"), 0644); err != nil {
		t.Fatalf("write existing README: %v", err)
	}
	store := NewJSONLStoreWithVault(filepath.Join(root, "logs"), vaultRoot)

	if err := store.SaveWorkstream(context.Background(), domainworkstream.Workstream{
		WorkstreamID: "ws_existing",
		Name:         "Existing",
		Status:       domainworkstream.StatusActive,
		CreatedAt:    time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("SaveWorkstream failed: %v", err)
	}
	content, err := os.ReadFile(readme)
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	if string(content) != "existing content\n" {
		t.Fatalf("README was overwritten: %q", content)
	}
}

func TestJSONLStoreWithVaultRejectsUnsafeWorkstreamID(t *testing.T) {
	store := NewJSONLStoreWithVault(t.TempDir(), t.TempDir())
	err := store.SaveWorkstream(context.Background(), domainworkstream.Workstream{
		WorkstreamID: "../escape",
		Name:         "escape",
		Status:       domainworkstream.StatusActive,
		CreatedAt:    time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
	})
	if err == nil || !strings.Contains(err.Error(), "invalid workstream_id") {
		t.Fatalf("expected unsafe workstream id to fail, got %v", err)
	}
}

func TestJSONLStoreApplyVaultUpdateWritesApprovedProposedContent(t *testing.T) {
	root := t.TempDir()
	vaultRoot := filepath.Join(root, "vault")
	store := NewJSONLStoreWithVault(filepath.Join(root, "logs"), vaultRoot)

	appliedPath, err := store.ApplyVaultUpdate(context.Background(), domainworkstream.VaultUpdateLog{
		UpdateID:        "upd_1",
		WorkstreamID:    "ws_1",
		FilePath:        "ws_1/STATUS.md",
		ProposedContent: "# STATUS\n\napproved\n",
		ReviewStatus:    domainworkstream.VaultReviewApproved,
		CreatedAt:       time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("ApplyVaultUpdate failed: %v", err)
	}
	expectedPath := filepath.Join(vaultRoot, "ws_1", "STATUS.md")
	if appliedPath != expectedPath {
		t.Fatalf("path=%q want %q", appliedPath, expectedPath)
	}
	content, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("read applied file: %v", err)
	}
	if string(content) != "# STATUS\n\napproved\n" {
		t.Fatalf("content=%q", content)
	}
}

func TestJSONLStoreApplyVaultUpdateAppendsStructuredContent(t *testing.T) {
	root := t.TempDir()
	vaultRoot := filepath.Join(root, "vault")
	statusPath := filepath.Join(vaultRoot, "ws_1", "STATUS.md")
	if err := os.MkdirAll(filepath.Dir(statusPath), 0o755); err != nil {
		t.Fatalf("mkdir status dir: %v", err)
	}
	if err := os.WriteFile(statusPath, []byte("# STATUS\n\n## Current Goal\n\n既存\n"), 0o644); err != nil {
		t.Fatalf("write status: %v", err)
	}
	store := NewJSONLStoreWithVault(filepath.Join(root, "logs"), vaultRoot)
	item := domainworkstream.VaultUpdateLog{
		UpdateID:        "upd_append",
		WorkstreamID:    "ws_1",
		FilePath:        "ws_1/STATUS.md",
		UpdateType:      "append_status",
		ProposedContent: "- Next Action: Source Registry relation確認済み\n",
		ReviewStatus:    domainworkstream.VaultReviewApproved,
		CreatedAt:       time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
	}

	preview, err := store.PreviewVaultUpdate(context.Background(), item)
	if err != nil {
		t.Fatalf("PreviewVaultUpdate failed: %v", err)
	}
	if !strings.Contains(preview.ProposedContent, "## 2026-05-18 status upd_append") {
		t.Fatalf("preview proposed content missing structured heading: %q", preview.ProposedContent)
	}
	if !strings.Contains(preview.UnifiedDiff, "+## 2026-05-18 status upd_append") {
		t.Fatalf("preview diff missing appended heading: %q", preview.UnifiedDiff)
	}

	appliedPath, err := store.ApplyVaultUpdate(context.Background(), item)
	if err != nil {
		t.Fatalf("ApplyVaultUpdate failed: %v", err)
	}
	if appliedPath != statusPath {
		t.Fatalf("path=%q want %q", appliedPath, statusPath)
	}
	content, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatalf("read applied status: %v", err)
	}
	got := string(content)
	if !strings.Contains(got, "## Current Goal\n\n既存") {
		t.Fatalf("existing content was not preserved: %q", got)
	}
	if !strings.Contains(got, "## 2026-05-18 status upd_append") || !strings.Contains(got, "Source Registry relation確認済み") {
		t.Fatalf("structured append missing from content: %q", got)
	}
}

func TestJSONLStoreApplyVaultUpdateRejectsTraversalPath(t *testing.T) {
	store := NewJSONLStoreWithVault(t.TempDir(), t.TempDir())

	_, err := store.ApplyVaultUpdate(context.Background(), domainworkstream.VaultUpdateLog{
		UpdateID:        "upd_1",
		WorkstreamID:    "ws_1",
		FilePath:        "../outside.md",
		ProposedContent: "escape",
		ReviewStatus:    domainworkstream.VaultReviewApproved,
		CreatedAt:       time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
	})
	if err == nil || !strings.Contains(err.Error(), "escapes vault root") {
		t.Fatalf("expected traversal to fail, got %v", err)
	}
}

func TestJSONLStoreApplyVaultUpdateRequiresApprovedReview(t *testing.T) {
	store := NewJSONLStoreWithVault(t.TempDir(), t.TempDir())

	_, err := store.ApplyVaultUpdate(context.Background(), domainworkstream.VaultUpdateLog{
		UpdateID:        "upd_1",
		WorkstreamID:    "ws_1",
		FilePath:        "ws_1/STATUS.md",
		ProposedContent: "# STATUS\n",
		ReviewStatus:    domainworkstream.VaultReviewPending,
		CreatedAt:       time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
	})
	if err == nil || !strings.Contains(err.Error(), "must be approved") {
		t.Fatalf("expected non-approved review to fail, got %v", err)
	}
}
