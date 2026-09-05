package execution

import (
	"fmt"
	"strings"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Decision はポリシー判定結果
// allow: 実行許可, deny: 実行拒否
type Decision string

const (
	DecisionAllow Decision = "allow"
	DecisionDeny  Decision = "deny"
)

// Status は実行状態
type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusDenied    Status = "denied"
	StatusCanceled  Status = "canceled"
)

// PolicyDecision はポリシー評価結果
type PolicyDecision struct {
	Decision      Decision `json:"decision"`
	Reason        string   `json:"reason,omitempty"`
	MatchedRuleID string   `json:"matched_rule_id,omitempty"`
}

// Action は1回のツール実行要求
type Action struct {
	TaskID      modulecore.TaskID  `json:"task_id"`
	TraceID     modulecore.TraceID `json:"trace_id,omitempty"`
	ActionID    string             `json:"action_id"`
	Tool        string             `json:"tool"`
	Arguments   map[string]any     `json:"arguments"`
	RequestedBy string             `json:"requested_by"`
	RequestedAt time.Time          `json:"requested_at"`
}

// Record は実行監査レコード
type Record struct {
	TaskID      modulecore.TaskID  `json:"task_id"`
	TraceID     modulecore.TraceID `json:"trace_id,omitempty"`
	ActionID    string             `json:"action_id"`
	Tool        string             `json:"tool"`
	RequestedBy string             `json:"requested_by"`
	Arguments   map[string]any     `json:"arguments,omitempty"`
	EventType   string             `json:"event_type,omitempty"` // security.decision|security.violation
	Decision    Decision           `json:"decision"`
	Status      Status             `json:"status"`
	Reason      string             `json:"reason,omitempty"`
	Error       string             `json:"error,omitempty"`
	StartedAt   time.Time          `json:"started_at"`
	FinishedAt  *time.Time         `json:"finished_at,omitempty"`
}

// Validate checks the canonical identity boundary before an action is
// evaluated or persisted. ActionID remains a legacy execution-local key until
// the ActionID cutover; it is intentionally not reinterpreted here.
func (a Action) Validate() error {
	if err := a.TaskID.Validate(); err != nil {
		return fmt.Errorf("task_id: %w", err)
	}
	if a.TraceID != "" {
		if err := a.TraceID.Validate(); err != nil {
			return fmt.Errorf("trace_id: %w", err)
		}
	}
	if strings.TrimSpace(a.ActionID) == "" {
		return fmt.Errorf("action_id is required")
	}
	return nil
}

// Validate checks identities on an execution audit record before persistence.
func (r Record) Validate() error {
	if err := r.TaskID.Validate(); err != nil {
		return fmt.Errorf("task_id: %w", err)
	}
	if r.TraceID != "" {
		if err := r.TraceID.Validate(); err != nil {
			return fmt.Errorf("trace_id: %w", err)
		}
	}
	if strings.TrimSpace(r.ActionID) == "" {
		return fmt.Errorf("action_id is required")
	}
	return nil
}

// IsTerminal は終端状態判定
func (s Status) IsTerminal() bool {
	switch s {
	case StatusSucceeded, StatusFailed, StatusDenied, StatusCanceled:
		return true
	default:
		return false
	}
}

// CanTransition は状態遷移の妥当性を判定する
func CanTransition(from, to Status) bool {
	if from == to {
		return true
	}
	if from.IsTerminal() {
		return false
	}
	switch from {
	case StatusPending:
		return to == StatusRunning || to == StatusDenied || to == StatusCanceled
	case StatusRunning:
		return to == StatusSucceeded || to == StatusFailed
	default:
		return false
	}
}
