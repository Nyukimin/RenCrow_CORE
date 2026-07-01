package workstream

import (
	"errors"
	"strings"
)

func ValidateGoal(goal Goal) error {
	if strings.TrimSpace(goal.GoalID) == "" {
		return errors.New("goal_id is required")
	}
	if strings.TrimSpace(goal.WorkstreamID) == "" {
		return errors.New("workstream_id is required")
	}
	if strings.TrimSpace(goal.Title) == "" {
		return errors.New("title is required")
	}
	if len(goal.SuccessCriteria) == 0 {
		return errors.New("success_criteria is required")
	}
	if len(goal.Verification) == 0 {
		return errors.New("verification is required")
	}
	if goal.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	if strings.TrimSpace(goal.Status) == StatusCompleted && goal.CompletedAt.IsZero() {
		return errors.New("completed_at is required for completed goal")
	}
	return nil
}

func ValidateWorkstream(item Workstream) error {
	if strings.TrimSpace(item.WorkstreamID) == "" {
		return errors.New("workstream_id is required")
	}
	if strings.TrimSpace(item.Name) == "" {
		return errors.New("name is required")
	}
	if strings.TrimSpace(item.Status) == "" {
		return errors.New("status is required")
	}
	if item.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	return nil
}

func ValidateArtifact(item Artifact) error {
	if strings.TrimSpace(item.ArtifactID) == "" {
		return errors.New("artifact_id is required")
	}
	if strings.TrimSpace(item.WorkstreamID) == "" {
		return errors.New("workstream_id is required")
	}
	if strings.TrimSpace(item.Type) == "" {
		return errors.New("artifact_type is required")
	}
	if strings.TrimSpace(item.Status) == "" {
		return errors.New("status is required")
	}
	if item.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	return nil
}

func ValidateArtifactAnnotation(item ArtifactAnnotation) error {
	if strings.TrimSpace(item.AnnotationID) == "" {
		return errors.New("annotation_id is required")
	}
	if strings.TrimSpace(item.ArtifactID) == "" {
		return errors.New("artifact_id is required")
	}
	if strings.TrimSpace(item.Comment) == "" {
		return errors.New("comment is required")
	}
	if strings.TrimSpace(item.Status) == "" {
		return errors.New("status is required")
	}
	if item.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	return nil
}

func ValidateSteeringItem(item SteeringItem) error {
	if strings.TrimSpace(item.SteeringID) == "" {
		return errors.New("steering_id is required")
	}
	if strings.TrimSpace(item.WorkstreamID) == "" {
		return errors.New("workstream_id is required")
	}
	if strings.TrimSpace(item.Instruction) == "" {
		return errors.New("instruction is required")
	}
	if strings.TrimSpace(item.Status) == "" {
		return errors.New("status is required")
	}
	if item.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	return nil
}

func ValidateHeartbeatSchedule(item HeartbeatSchedule) error {
	if strings.TrimSpace(item.HeartbeatID) == "" {
		return errors.New("heartbeat_id is required")
	}
	if strings.TrimSpace(item.WorkstreamID) == "" {
		return errors.New("workstream_id is required")
	}
	if strings.TrimSpace(item.ScheduleText) == "" {
		return errors.New("schedule_text is required")
	}
	if strings.TrimSpace(item.Task) == "" {
		return errors.New("task is required")
	}
	if strings.TrimSpace(item.Status) == "" {
		return errors.New("status is required")
	}
	if item.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	return nil
}

func ValidateVaultUpdateLog(item VaultUpdateLog) error {
	if strings.TrimSpace(item.UpdateID) == "" {
		return errors.New("update_id is required")
	}
	if strings.TrimSpace(item.WorkstreamID) == "" {
		return errors.New("workstream_id is required")
	}
	if strings.TrimSpace(item.FilePath) == "" {
		return errors.New("file_path is required")
	}
	if strings.TrimSpace(item.ReviewStatus) == "" {
		return errors.New("review_status is required")
	}
	if item.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	return nil
}

func ValidateVaultUpdateReview(item VaultUpdateLog) error {
	if err := ValidateVaultUpdateLog(item); err != nil {
		return err
	}
	switch item.ReviewStatus {
	case VaultReviewApproved, VaultReviewRejected:
		return nil
	default:
		return errors.New("review_status must be approved or rejected")
	}
}
