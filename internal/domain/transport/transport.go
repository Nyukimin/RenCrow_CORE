package transport

import (
	"context"
	"fmt"
	"time"
)

// Transport はAgent間通信の抽象化インターフェース
type Transport interface {
	Send(ctx context.Context, msg Message) error
	Receive(ctx context.Context) (Message, error)
	Close() error
	IsHealthy() bool
}

// MessageType はメッセージ種別
type MessageType string

const (
	MessageTypeTask     MessageType = "task"
	MessageTypeResult   MessageType = "result"
	MessageTypeError    MessageType = "error"
	MessageTypeIdleChat MessageType = "idle_chat"
)

// Message はAgent間通信メッセージ
type Message struct {
	From      string                 `json:"from"`
	To        string                 `json:"to"`
	SessionID string                 `json:"session_id"`
	JobID     string                 `json:"job_id"`
	Type      MessageType            `json:"type,omitempty"`
	Content   string                 `json:"message"`
	Context   map[string]interface{} `json:"context,omitempty"`
	Proposal  *ProposalPayload       `json:"proposal,omitempty"`
	Result    *ResultPayload         `json:"result,omitempty"`
	Timestamp string                 `json:"timestamp"`
}

// ProposalPayload はProposalのTransport用DTO
type ProposalPayload struct {
	Plan     string `json:"plan"`
	Patch    string `json:"patch"`
	Risk     string `json:"risk"`
	CostHint string `json:"cost_hint"`
}

// ResultPayload は実行結果のTransport用DTO
type ResultPayload struct {
	Success       bool                   `json:"success"`
	Summary       string                 `json:"summary"`
	ExecutedCmds  int                    `json:"executed_cmds"`
	FailedCmds    int                    `json:"failed_cmds"`
	GitCommit     string                 `json:"git_commit,omitempty"`
	Results       []CommandResultPayload `json:"results,omitempty"`
	FailureKind   string                 `json:"failure_kind,omitempty"`
	FailureReason string                 `json:"failure_reason,omitempty"`
	Retryable     bool                   `json:"retryable,omitempty"`
	FailedIndex   int                    `json:"failed_index,omitempty"`
}

// CommandResultPayload はコマンド実行結果のTransport用DTO
type CommandResultPayload struct {
	Command string `json:"command"`
	Target  string `json:"target"`
	Success bool   `json:"success"`
	Output  string `json:"output,omitempty"`
	Error   string `json:"error,omitempty"`
}

// NewMessage は新しいメッセージを作成
func NewMessage(from, to, sessionID, jobID, content string) Message {
	return Message{
		From:      from,
		To:        to,
		SessionID: sessionID,
		JobID:     jobID,
		Type:      MessageTypeTask,
		Content:   content,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
}

// NewErrorMessage はエラーメッセージを作成
func NewErrorMessage(from, to, sessionID, jobID, errMsg string) Message {
	return Message{
		From:      from,
		To:        to,
		SessionID: sessionID,
		JobID:     jobID,
		Type:      MessageTypeError,
		Content:   errMsg,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
}

// Validate はメッセージの妥当性を検証
func (m Message) Validate() error {
	if m.From == "" {
		return fmt.Errorf("message.from is required")
	}
	if m.To == "" {
		return fmt.Errorf("message.to is required")
	}
	if m.Timestamp == "" {
		return fmt.Errorf("message.timestamp is required")
	}
	if _, err := time.Parse(time.RFC3339, m.Timestamp); err != nil {
		return fmt.Errorf("message.timestamp must be RFC3339 format: %w", err)
	}
	return nil
}
