// Package worker defines execution module contracts.
package worker

import (
	"context"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/modules/core"
	"github.com/Nyukimin/RenCrow_CORE/modules/llm"
)

type JobID string

const ToolProposalPatch = "proposal_patch"

type Action struct {
	JobID       JobID          `json:"job_id"`
	ActionID    string         `json:"action_id,omitempty"`
	Tool        string         `json:"tool"`
	Arguments   map[string]any `json:"arguments,omitempty"`
	RequestedBy string         `json:"requested_by,omitempty"`
	RequestedAt time.Time      `json:"requested_at,omitempty"`
}

type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusDenied    Status = "denied"
	StatusCanceled  Status = "canceled"
)

type Result struct {
	JobID      JobID          `json:"job_id"`
	Status     Status         `json:"status"`
	Output     string         `json:"output,omitempty"`
	Error      string         `json:"error,omitempty"`
	StartedAt  time.Time      `json:"started_at,omitempty"`
	FinishedAt time.Time      `json:"finished_at,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

type PatchExecutionSummary struct {
	Success       bool
	Summary       string
	ExecutedCmds  int
	FailedCmds    int
	GitCommit     string
	FailureKind   string
	FailureReason string
	Retryable     bool
	FailedIndex   int
}

type Executor interface {
	Health(ctx context.Context) core.HealthReport
	Execute(ctx context.Context, action Action) (Result, error)
}

type Planner interface {
	Plan(ctx context.Context, provider llm.Provider, request string) (Action, error)
}
