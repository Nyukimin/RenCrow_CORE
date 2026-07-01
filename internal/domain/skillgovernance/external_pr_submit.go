package skillgovernance

import (
	"errors"
	"strings"
	"time"
)

const (
	ExternalPRSubmitStatusBlocked = "blocked"
	ExternalPRSubmitStatusFailed  = "failed"
	ExternalPRSubmitStatusCreated = "created"
)

func ValidateExternalPRSubmitRecord(record ExternalPRSubmitRecord) error {
	if strings.TrimSpace(record.SubmitID) == "" {
		return errors.New("submit_id is required")
	}
	if strings.TrimSpace(record.ContributionEventID) == "" {
		return errors.New("contribution_event_id is required")
	}
	if strings.TrimSpace(record.Repo) == "" {
		return errors.New("repo is required")
	}
	if strings.TrimSpace(record.Title) == "" {
		return errors.New("title is required")
	}
	if !record.HumanApproved {
		return errors.New("human approval is required before external PR submit")
	}
	if record.ApprovalStatus != "approved" {
		return errors.New("approval_status must be approved before external PR submit")
	}
	if strings.TrimSpace(record.SubmitStatus) == "" {
		return errors.New("submit_status is required")
	}
	switch record.SubmitStatus {
	case ExternalPRSubmitStatusBlocked, ExternalPRSubmitStatusFailed, ExternalPRSubmitStatusCreated:
	default:
		return errors.New("submit_status must be blocked, failed, or created")
	}
	if record.SubmitStatus == ExternalPRSubmitStatusCreated && !record.ExternalPRCreated {
		return errors.New("submit_status=created requires external_pr_created=true")
	}
	if record.SubmitStatus != ExternalPRSubmitStatusCreated && record.ExternalPRCreated {
		return errors.New("external_pr_created=true requires submit_status=created")
	}
	if !record.ExternalPRCreated && record.PostSubmitVerified {
		return errors.New("post_submit_verified requires external_pr_created=true")
	}
	if record.ExternalPRCreated && strings.TrimSpace(record.PRURL) == "" {
		return errors.New("pr_url is required when external_pr_created is true")
	}
	if record.ExternalPRCreated && !record.PostSubmitVerified {
		return errors.New("post_submit_verified is required when external_pr_created is true")
	}
	if record.PostSubmitVerified && strings.TrimSpace(record.PostSubmitEvidence) == "" {
		return errors.New("post_submit_evidence is required when post_submit_verified is true")
	}
	if !record.ExternalPRCreated && strings.TrimSpace(record.FailureReason) == "" {
		return errors.New("failure_reason is required when external PR was not created")
	}
	if record.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	return nil
}

func NewBlockedExternalPRSubmitRecord(input ExternalPRSubmitRecord, now time.Time) (ExternalPRSubmitRecord, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	record := input
	record.ApprovalStatus = "approved"
	record.SubmitStatus = ExternalPRSubmitStatusBlocked
	record.PRURL = ""
	record.FailureReason = "external PR adapter is not configured"
	record.ExternalPRCreated = false
	record.PostSubmitVerified = false
	record.PostSubmitEvidence = ""
	record.PRAdapter = "unconfigured"
	record.CreatedAt = now
	if err := ValidateExternalPRSubmitRecord(record); err != nil {
		return ExternalPRSubmitRecord{}, err
	}
	return record, nil
}
