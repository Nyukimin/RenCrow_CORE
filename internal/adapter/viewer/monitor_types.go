package viewer

import (
	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/orchestrator"
	domainexecution "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/execution"
)

type StatusSnapshot struct {
	UpdatedAt string            `json:"updated_at"`
	Chat      ComponentSnapshot `json:"chat"`
	Worker    ComponentSnapshot `json:"worker"`
	Coders    CodersSnapshot    `json:"coders"`
}

type ComponentSnapshot struct {
	Status    string `json:"status"`
	AgentID   string `json:"agent_id,omitempty"`
	JobID     string `json:"job_id,omitempty"`
	Route     string `json:"route,omitempty"`
	LastEvent string `json:"last_event,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
	Preview   string `json:"preview,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

type CodersSnapshot struct {
	Status    string          `json:"status"`
	UpdatedAt string          `json:"updated_at,omitempty"`
	Items     []AgentSnapshot `json:"items"`
}

type AgentSnapshot struct {
	ID         string `json:"id"`
	Role       string `json:"role"`
	State      string `json:"state"`
	Route      string `json:"route,omitempty"`
	JobID      string `json:"job_id,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	LastEvent  string `json:"last_event,omitempty"`
	Preview    string `json:"preview,omitempty"`
	Reason     string `json:"reason,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
	EventCount int    `json:"event_count,omitempty"`
}

type JobSnapshot struct {
	JobID           string                           `json:"job_id"`
	Route           string                           `json:"route,omitempty"`
	Phase           string                           `json:"phase"`
	Owner           string                           `json:"owner,omitempty"`
	Status          string                           `json:"status"`
	TerminalOutcome string                           `json:"terminal_outcome,omitempty"`
	SessionID       string                           `json:"session_id,omitempty"`
	Channel         string                           `json:"channel,omitempty"`
	ChatID          string                           `json:"chat_id,omitempty"`
	StartedAt       string                           `json:"started_at,omitempty"`
	UpdatedAt       string                           `json:"updated_at,omitempty"`
	Summary         string                           `json:"summary,omitempty"`
	FailureKind     string                           `json:"failure_kind,omitempty"`
	FailureReason   string                           `json:"failure_reason,omitempty"`
	FinalUserReport string                           `json:"final_user_report,omitempty"`
	MioReported     bool                             `json:"mio_reported"`
	Events          []orchestrator.OrchestratorEvent `json:"events,omitempty"`
}

type JobFilter struct {
	Route     string
	Status    string
	Owner     string
	SessionID string
	ChatID    string
	Limit     int
}

type LogFilter struct {
	Type      string
	Agent     string
	Route     string
	JobID     string
	SessionID string
	ChatID    string
	Limit     int
}

type AgentDetail struct {
	Agent      AgentSnapshot                    `json:"agent"`
	ActiveJobs []JobSnapshot                    `json:"active_jobs"`
	Events     []orchestrator.OrchestratorEvent `json:"events"`
}

type AuditSummary struct {
	StoredLogs int            `json:"stored_logs"`
	ByType     map[string]int `json:"by_type"`
	ByAgent    map[string]int `json:"by_agent"`
	ByRoute    map[string]int `json:"by_route"`
}

type JobDetail struct {
	Item     JobSnapshot                      `json:"item"`
	Evidence *domainexecution.ExecutionReport `json:"evidence,omitempty"`
}
