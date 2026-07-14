package advisor

import (
	"fmt"
	"strings"
	"time"
)

type AdviceStatus string

const StatusRejected = "rejected"

type AdviceRunRecord struct {
	RunID            string       `json:"run_id"`
	RequestID        string       `json:"request_id,omitempty"`
	TaskID           string       `json:"task_id,omitempty"`
	RequestedByAgent string       `json:"requested_by_agent"`
	AdvisorID        AdvisorID    `json:"advisor_id"`
	Purpose          string       `json:"purpose,omitempty"`
	PromptHash       string       `json:"prompt_hash,omitempty"`
	RiskClass        string       `json:"risk_class,omitempty"`
	ApprovalMode     string       `json:"approval_mode"`
	Status           AdviceStatus `json:"status"`
	Summary          string       `json:"summary,omitempty"`
	OutputHash       string       `json:"output_hash,omitempty"`
	Error            string       `json:"error,omitempty"`
	StartedAt        time.Time    `json:"started_at"`
	FinishedAt       time.Time    `json:"finished_at"`
	LatencyMillis    int64        `json:"latency_millis"`
}

func (r AdviceRunRecord) Validate() error {
	if strings.TrimSpace(r.RunID) == "" {
		return fmt.Errorf("run_id is required")
	}
	if strings.TrimSpace(string(r.AdvisorID)) == "" {
		return fmt.Errorf("advisor_id is required")
	}
	if strings.TrimSpace(r.RequestedByAgent) == "" {
		return fmt.Errorf("requested_by_agent is required")
	}
	switch strings.TrimSpace(r.ApprovalMode) {
	case "advice_only", "human_required":
	default:
		return fmt.Errorf("approval_mode must be advice_only or human_required")
	}
	switch string(r.Status) {
	case StatusCompleted, StatusFailed, StatusUnavailable, StatusRejected:
	default:
		return fmt.Errorf("unsupported advice status %q", r.Status)
	}
	if r.LatencyMillis < 0 {
		return fmt.Errorf("latency_millis must be >= 0")
	}
	if !r.StartedAt.IsZero() && !r.FinishedAt.IsZero() && r.FinishedAt.Before(r.StartedAt) {
		return fmt.Errorf("finished_at must not be before started_at")
	}
	return nil
}

type AdvisorScoreSnapshot struct {
	SnapshotID       string    `json:"snapshot_id"`
	AdvisorID        AdvisorID `json:"advisor_id"`
	CapabilityID     string    `json:"capability_id,omitempty"`
	WindowStart      time.Time `json:"window_start"`
	WindowEnd        time.Time `json:"window_end"`
	RequestCount     int       `json:"request_count"`
	CompletedCount   int       `json:"completed_count"`
	FailedCount      int       `json:"failed_count"`
	UnavailableCount int       `json:"unavailable_count"`
	AdoptedCount     int       `json:"adopted_count"`
	SuccessCount     int       `json:"success_count"`
	AvgLatencyMillis int64     `json:"avg_latency_millis"`
	AvgRevisionCount float64   `json:"avg_revision_count"`
	Score            float64   `json:"score"`
	CreatedAt        time.Time `json:"created_at"`
}

func (s AdvisorScoreSnapshot) Validate() error {
	if strings.TrimSpace(s.SnapshotID) == "" {
		return fmt.Errorf("snapshot_id is required")
	}
	if strings.TrimSpace(string(s.AdvisorID)) == "" {
		return fmt.Errorf("advisor_id is required")
	}
	for name, value := range map[string]int{
		"request_count": s.RequestCount, "completed_count": s.CompletedCount,
		"failed_count": s.FailedCount, "unavailable_count": s.UnavailableCount,
		"adopted_count": s.AdoptedCount, "success_count": s.SuccessCount,
	} {
		if value < 0 {
			return fmt.Errorf("%s must be >= 0", name)
		}
	}
	if s.AvgLatencyMillis < 0 || s.AvgRevisionCount < 0 {
		return fmt.Errorf("averages must be >= 0")
	}
	if s.Score < 0 || s.Score > 1 {
		return fmt.Errorf("score must be between 0 and 1")
	}
	return nil
}

type AdvisorAdoptionRecord struct {
	AdoptionID     string    `json:"adoption_id"`
	RunID          string    `json:"run_id"`
	TaskID         string    `json:"task_id,omitempty"`
	AdvisorID      AdvisorID `json:"advisor_id"`
	AdoptedByAgent string    `json:"adopted_by_agent"`
	Adopted        bool      `json:"adopted"`
	Outcome        string    `json:"outcome"`
	RevisionCount  int       `json:"revision_count"`
	Reason         string    `json:"reason,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

func (r AdvisorAdoptionRecord) Validate() error {
	if strings.TrimSpace(r.AdoptionID) == "" || strings.TrimSpace(r.RunID) == "" {
		return fmt.Errorf("adoption_id and run_id are required")
	}
	if strings.TrimSpace(string(r.AdvisorID)) == "" || strings.TrimSpace(r.AdoptedByAgent) == "" {
		return fmt.Errorf("advisor_id and adopted_by_agent are required")
	}
	switch strings.TrimSpace(r.Outcome) {
	case "success", "partial", "failed", "not_run":
	default:
		return fmt.Errorf("unsupported adoption outcome %q", r.Outcome)
	}
	if r.RevisionCount < 0 {
		return fmt.Errorf("revision_count must be >= 0")
	}
	if r.CreatedAt.IsZero() {
		return fmt.Errorf("created_at is required")
	}
	return nil
}
