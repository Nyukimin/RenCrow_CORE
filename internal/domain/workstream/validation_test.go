package workstream

import (
	"strings"
	"testing"
	"time"
)

func TestValidateGoalRequiresSuccessCriteriaAndVerification(t *testing.T) {
	now := time.Date(2026, 5, 20, 6, 50, 0, 0, time.UTC)
	goal := Goal{
		GoalID:       "goal_1",
		WorkstreamID: "ws_1",
		Title:        "LPを作る",
	}
	if err := ValidateGoal(goal); err == nil {
		t.Fatal("expected missing success criteria to fail")
	}
	goal.SuccessCriteria = []string{"CTAがある"}
	if err := ValidateGoal(goal); err == nil {
		t.Fatal("expected missing verification to fail")
	}
	goal.Verification = []string{"Viewerで確認する"}
	if err := ValidateGoal(goal); err == nil {
		t.Fatal("expected missing created_at to fail")
	}
	goal.CreatedAt = now
	if err := ValidateGoal(goal); err != nil {
		t.Fatalf("ValidateGoal failed: %v", err)
	}
}

func TestValidateWorkstreamRequiresIdentityAndStatus(t *testing.T) {
	now := time.Date(2026, 5, 20, 6, 50, 0, 0, time.UTC)
	item := Workstream{}
	if err := ValidateWorkstream(item); err == nil {
		t.Fatal("expected missing workstream_id to fail")
	}
	item.WorkstreamID = "ws_1"
	if err := ValidateWorkstream(item); err == nil {
		t.Fatal("expected missing name to fail")
	}
	item.Name = "収益化"
	if err := ValidateWorkstream(item); err == nil {
		t.Fatal("expected missing status to fail")
	}
	item.Status = StatusActive
	if err := ValidateWorkstream(item); err == nil {
		t.Fatal("expected missing created_at to fail")
	}
	item.CreatedAt = now
	if err := ValidateWorkstream(item); err != nil {
		t.Fatalf("ValidateWorkstream failed: %v", err)
	}
}

func TestValidateArtifactRequiresContractFields(t *testing.T) {
	now := time.Date(2026, 5, 20, 6, 50, 0, 0, time.UTC)
	item := Artifact{}
	if err := ValidateArtifact(item); err == nil {
		t.Fatal("expected missing artifact_id to fail")
	}
	item.ArtifactID = "art_1"
	if err := ValidateArtifact(item); err == nil {
		t.Fatal("expected missing workstream_id to fail")
	}
	item.WorkstreamID = "ws_1"
	if err := ValidateArtifact(item); err == nil {
		t.Fatal("expected missing type to fail")
	}
	item.Type = "markdown"
	if err := ValidateArtifact(item); err == nil {
		t.Fatal("expected missing status to fail")
	}
	item.Status = "draft"
	if err := ValidateArtifact(item); err == nil {
		t.Fatal("expected missing created_at to fail")
	}
	item.CreatedAt = now
	if err := ValidateArtifact(item); err != nil {
		t.Fatalf("ValidateArtifact failed: %v", err)
	}
}

func TestValidateArtifactAnnotationRequiresComment(t *testing.T) {
	now := time.Date(2026, 5, 20, 6, 50, 0, 0, time.UTC)
	item := ArtifactAnnotation{}
	if err := ValidateArtifactAnnotation(item); err == nil {
		t.Fatal("expected missing annotation_id to fail")
	}
	item.AnnotationID = "ann_1"
	if err := ValidateArtifactAnnotation(item); err == nil {
		t.Fatal("expected missing artifact_id to fail")
	}
	item.ArtifactID = "art_1"
	if err := ValidateArtifactAnnotation(item); err == nil {
		t.Fatal("expected missing comment to fail")
	}
	item.Comment = "見出しが抽象的"
	if err := ValidateArtifactAnnotation(item); err == nil {
		t.Fatal("expected missing status to fail")
	}
	item.Status = "open"
	if err := ValidateArtifactAnnotation(item); err == nil {
		t.Fatal("expected missing created_at to fail")
	}
	item.CreatedAt = now
	if err := ValidateArtifactAnnotation(item); err != nil {
		t.Fatalf("ValidateArtifactAnnotation failed: %v", err)
	}
}

func TestValidateSteeringItemRequiresInstruction(t *testing.T) {
	now := time.Date(2026, 5, 20, 6, 50, 0, 0, time.UTC)
	item := SteeringItem{}
	if err := ValidateSteeringItem(item); err == nil {
		t.Fatal("expected missing steering_id to fail")
	}
	item.SteeringID = "stq_1"
	if err := ValidateSteeringItem(item); err == nil {
		t.Fatal("expected missing workstream_id to fail")
	}
	item.WorkstreamID = "ws_1"
	if err := ValidateSteeringItem(item); err == nil {
		t.Fatal("expected missing instruction to fail")
	}
	item.Instruction = "CTAを直す"
	if err := ValidateSteeringItem(item); err == nil {
		t.Fatal("expected missing status to fail")
	}
	item.Status = "pending"
	if err := ValidateSteeringItem(item); err == nil {
		t.Fatal("expected missing created_at to fail")
	}
	item.CreatedAt = now
	if err := ValidateSteeringItem(item); err != nil {
		t.Fatalf("ValidateSteeringItem failed: %v", err)
	}
}

func TestValidateHeartbeatScheduleRequiresDraftTaskContract(t *testing.T) {
	now := time.Date(2026, 5, 20, 6, 50, 0, 0, time.UTC)
	item := HeartbeatSchedule{}
	if err := ValidateHeartbeatSchedule(item); err == nil {
		t.Fatal("expected missing heartbeat_id to fail")
	}
	item.HeartbeatID = "hb_1"
	if err := ValidateHeartbeatSchedule(item); err == nil {
		t.Fatal("expected missing workstream_id to fail")
	}
	item.WorkstreamID = "ws_1"
	if err := ValidateHeartbeatSchedule(item); err == nil {
		t.Fatal("expected missing schedule_text to fail")
	}
	item.ScheduleText = "daily 08:00"
	if err := ValidateHeartbeatSchedule(item); err == nil {
		t.Fatal("expected missing task to fail")
	}
	item.Task = "draft report only"
	if err := ValidateHeartbeatSchedule(item); err == nil {
		t.Fatal("expected missing status to fail")
	}
	item.Status = StatusActive
	if err := ValidateHeartbeatSchedule(item); err == nil {
		t.Fatal("expected missing created_at to fail")
	}
	item.CreatedAt = now
	if err := ValidateHeartbeatSchedule(item); err != nil {
		t.Fatalf("ValidateHeartbeatSchedule failed: %v", err)
	}
}

func TestValidateVaultUpdateLogRequiresReviewStatus(t *testing.T) {
	now := time.Date(2026, 5, 20, 6, 50, 0, 0, time.UTC)
	item := VaultUpdateLog{}
	if err := ValidateVaultUpdateLog(item); err == nil {
		t.Fatal("expected missing update_id to fail")
	}
	item.UpdateID = "upd_1"
	if err := ValidateVaultUpdateLog(item); err == nil {
		t.Fatal("expected missing workstream_id to fail")
	}
	item.WorkstreamID = "ws_1"
	if err := ValidateVaultUpdateLog(item); err == nil {
		t.Fatal("expected missing file_path to fail")
	}
	item.FilePath = "vault/workstreams/ws_1/STATUS.md"
	if err := ValidateVaultUpdateLog(item); err == nil {
		t.Fatal("expected missing review_status to fail")
	}
	item.ReviewStatus = "pending"
	if err := ValidateVaultUpdateLog(item); err == nil {
		t.Fatal("expected missing created_at to fail")
	}
	item.CreatedAt = now
	if err := ValidateVaultUpdateLog(item); err != nil {
		t.Fatalf("ValidateVaultUpdateLog failed: %v", err)
	}
}

func TestValidateVaultUpdateReviewAllowsOnlyTerminalReviewStatus(t *testing.T) {
	item := VaultUpdateLog{
		UpdateID:     "upd_1",
		WorkstreamID: "ws_1",
		FilePath:     "vault/workstreams/ws_1/STATUS.md",
		ReviewStatus: VaultReviewPending,
		CreatedAt:    time.Date(2026, 5, 20, 6, 50, 0, 0, time.UTC),
	}
	if err := ValidateVaultUpdateReview(item); err == nil {
		t.Fatal("expected pending review status to fail")
	}
	item.ReviewStatus = VaultReviewApproved
	if err := ValidateVaultUpdateReview(item); err != nil {
		t.Fatalf("ValidateVaultUpdateReview approved failed: %v", err)
	}
	item.ReviewStatus = VaultReviewRejected
	if err := ValidateVaultUpdateReview(item); err != nil {
		t.Fatalf("ValidateVaultUpdateReview rejected failed: %v", err)
	}
}

func TestValidateWorkstreamRejectsMissingCreatedAt(t *testing.T) {
	cases := []struct {
		name string
		err  string
		run  func() error
	}{
		{
			name: "workstream",
			err:  "created_at",
			run: func() error {
				return ValidateWorkstream(Workstream{WorkstreamID: "ws_1", Name: "収益化", Status: StatusActive})
			},
		},
		{
			name: "goal",
			err:  "created_at",
			run: func() error {
				return ValidateGoal(Goal{
					GoalID:          "goal_1",
					WorkstreamID:    "ws_1",
					Title:           "LPを作る",
					SuccessCriteria: []string{"CTAがある"},
					Verification:    []string{"Viewerで確認する"},
					Status:          StatusActive,
				})
			},
		},
		{
			name: "artifact",
			err:  "created_at",
			run: func() error {
				return ValidateArtifact(Artifact{ArtifactID: "art_1", WorkstreamID: "ws_1", Type: "markdown", Status: "draft"})
			},
		},
		{
			name: "annotation",
			err:  "created_at",
			run: func() error {
				return ValidateArtifactAnnotation(ArtifactAnnotation{AnnotationID: "ann_1", ArtifactID: "art_1", Comment: "見出しが抽象的", Status: "open"})
			},
		},
		{
			name: "steering",
			err:  "created_at",
			run: func() error {
				return ValidateSteeringItem(SteeringItem{SteeringID: "stq_1", WorkstreamID: "ws_1", Instruction: "CTAを直す", Status: "pending"})
			},
		},
		{
			name: "heartbeat",
			err:  "created_at",
			run: func() error {
				return ValidateHeartbeatSchedule(HeartbeatSchedule{HeartbeatID: "hb_1", WorkstreamID: "ws_1", ScheduleText: "daily 08:00", Task: "draft report only", Status: StatusActive})
			},
		},
		{
			name: "vault update",
			err:  "created_at",
			run: func() error {
				return ValidateVaultUpdateLog(VaultUpdateLog{UpdateID: "upd_1", WorkstreamID: "ws_1", FilePath: "vault/workstreams/ws_1/STATUS.md", ReviewStatus: VaultReviewPending})
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if err == nil {
				t.Fatalf("expected %s error", tc.err)
			}
			if !strings.Contains(err.Error(), tc.err) {
				t.Fatalf("expected error to contain %q, got %v", tc.err, err)
			}
		})
	}
}

func TestValidateGoalRejectsCompletedWithoutCompletedAt(t *testing.T) {
	goal := Goal{
		GoalID:          "goal_1",
		WorkstreamID:    "ws_1",
		Title:           "LPを作る",
		SuccessCriteria: []string{"CTAがある"},
		Verification:    []string{"Viewerで確認する"},
		Status:          StatusCompleted,
		CreatedAt:       time.Date(2026, 5, 20, 6, 50, 0, 0, time.UTC),
	}
	err := ValidateGoal(goal)
	if err == nil {
		t.Fatal("expected completed_at error")
	}
	if !strings.Contains(err.Error(), "completed_at") {
		t.Fatalf("expected completed_at error, got %v", err)
	}
}
