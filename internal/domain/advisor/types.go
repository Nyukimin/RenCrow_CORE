package advisor

import (
	"fmt"
	"strings"
	"time"
)

type AdvisorID string

const (
	AdvisorCodex AdvisorID = "codex"
)

const (
	StatusCompleted   = "completed"
	StatusFailed      = "failed"
	StatusUnavailable = "unavailable"
)

type Capability struct {
	Domain      string
	Level       int
	Description string
}

type Profile struct {
	ID           AdvisorID
	DisplayName  string
	Provider     string
	Capabilities []Capability
	AllowedModes []string
	Disabled     bool
}

type ContextRef struct {
	Kind string
	Ref  string
}

type CostBudget struct {
	MaxDurationMillis int
	MaxTokens         int
}

type Artifact struct {
	Kind    string
	Ref     string
	Summary string
}

type TokenUsage struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}

type CostEstimate struct {
	Amount   float64
	Currency string
}

type AdviceRequest struct {
	ID               string
	TaskID           string
	RequestedByAgent string
	AdvisorID        AdvisorID
	Purpose          string
	Prompt           string
	ContextRefs      []ContextRef
	AllowedArtifacts []string
	RiskClass        string
	CostBudget       CostBudget
	TimeoutMillis    int
	ApprovalMode     string
	CreatedAt        time.Time
}

func (r AdviceRequest) Validate() error {
	if strings.TrimSpace(r.RequestedByAgent) == "" {
		return fmt.Errorf("requested_by_agent is required")
	}
	if strings.TrimSpace(string(r.AdvisorID)) == "" {
		return fmt.Errorf("advisor_id is required")
	}
	if strings.TrimSpace(r.Purpose) == "" {
		return fmt.Errorf("purpose is required")
	}
	if strings.TrimSpace(r.Prompt) == "" {
		return fmt.Errorf("prompt is required")
	}
	if r.TimeoutMillis < 0 {
		return fmt.Errorf("timeout_millis must be >= 0")
	}
	return nil
}

type AdviceResult struct {
	RequestID    string
	AdvisorID    AdvisorID
	Status       string
	Summary      string
	Plan         string
	Patch        string
	Tests        []string
	Risks        []string
	Artifacts    []Artifact
	TokenUsage   *TokenUsage
	CostEstimate *CostEstimate
	StartedAt    time.Time
	CompletedAt  time.Time
}

func (r AdviceResult) OutputText() string {
	if text := strings.TrimSpace(r.Summary); text != "" {
		return text
	}
	if text := strings.TrimSpace(r.Plan); text != "" {
		return text
	}
	if text := strings.TrimSpace(r.Patch); text != "" {
		return text
	}
	return ""
}

type Score struct {
	AdvisorID       AdvisorID
	Domain          string
	AdoptionRate    float64
	SuccessRate     float64
	AverageRework   float64
	RiskIncidentCnt int
	UpdatedAt       time.Time
}
