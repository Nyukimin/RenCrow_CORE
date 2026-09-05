package transport

import (
	"context"
	"fmt"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
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
	TaskID    modulecore.TaskID      `json:"task_id"`
	Type      MessageType            `json:"type,omitempty"`
	Content   string                 `json:"message"`
	Context   map[string]interface{} `json:"context,omitempty"`
	Proposal  *ProposalPayload       `json:"proposal,omitempty"`
	Result    *ResultPayload         `json:"result,omitempty"`
	TurnInput *TurnInputContext      `json:"turn_input,omitempty"`
	Timestamp string                 `json:"timestamp"`
}

// TurnInputContext is the deterministic transport projection of a
// conversation.TurnInput. SessionID and message text remain on Message so the
// transport wire has one owner for each of those values.
type TurnInputContext struct {
	RootTaskID             modulecore.TaskID       `json:"root_task_id"`
	TurnID                 modulecore.TurnID       `json:"turn_id"`
	TraceID                modulecore.TraceID      `json:"trace_id"`
	UserMessageID          modulecore.MessageID    `json:"user_message_id"`
	AgentMessageID         modulecore.MessageID    `json:"agent_message_id"`
	ChannelType            string                  `json:"channel_type"`
	ExternalConversationID string                  `json:"external_conversation_id"`
	Attachments            []attachment.Attachment `json:"attachments,omitempty"`
	ViewerRecipient        string                  `json:"viewer_recipient,omitempty"`
	ForcedRoute            routing.Route           `json:"forced_route,omitempty"`
	Route                  routing.Route           `json:"route,omitempty"`
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
func NewMessage(from, to, sessionID string, taskID modulecore.TaskID, content string) Message {
	return Message{
		From:      from,
		To:        to,
		SessionID: sessionID,
		TaskID:    taskID,
		Type:      MessageTypeTask,
		Content:   content,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
}

// NewTurnInputMessage creates a task message carrying the exact conversation
// identity assigned to the user input. TaskID identifies the execution and
// remains separate from the root task carried by TurnInput.
func NewTurnInputMessage(from, to string, taskID modulecore.TaskID, input conversation.TurnInput) (Message, error) {
	if err := taskID.Validate(); err != nil {
		return Message{}, fmt.Errorf("message.task_id is invalid: %w", err)
	}
	if err := input.Validate(); err != nil {
		return Message{}, fmt.Errorf("turn input is invalid: %w", err)
	}
	address := input.ChannelAddress()
	message := NewMessage(from, to, input.SessionID(), taskID, input.MessageText())
	message.TurnInput = &TurnInputContext{
		RootTaskID:             input.RootTaskID(),
		TurnID:                 input.TurnID(),
		TraceID:                input.TraceID(),
		UserMessageID:          input.UserMessageID(),
		AgentMessageID:         input.AgentMessageID(),
		ChannelType:            address.ChannelType(),
		ExternalConversationID: address.ExternalConversationID(),
		Attachments:            input.Attachments(),
		ViewerRecipient:        input.ViewerRecipient(),
		ForcedRoute:            input.ForcedRoute(),
		Route:                  input.Route(),
	}
	return message, nil
}

// ReconstructTurnInput restores the exact conversation input carried by this
// message. A projection is mandatory; messages never synthesize a new input or
// derive one from TaskID.
func (m Message) ReconstructTurnInput() (conversation.TurnInput, error) {
	if m.TurnInput == nil {
		return conversation.TurnInput{}, fmt.Errorf("message.turn_input is required")
	}
	projection := m.TurnInput
	address, err := conversation.NewChannelAddress(projection.ChannelType, projection.ExternalConversationID)
	if err != nil {
		return conversation.TurnInput{}, fmt.Errorf("message.turn_input channel address is invalid: %w", err)
	}
	if address.ChannelType() != projection.ChannelType || address.ExternalConversationID() != projection.ExternalConversationID {
		return conversation.TurnInput{}, fmt.Errorf("message.turn_input channel address is not normalized")
	}
	input, err := conversation.ReconstructTurnInput(
		projection.RootTaskID,
		projection.TurnID,
		projection.TraceID,
		projection.UserMessageID,
		projection.AgentMessageID,
		m.Content,
		address,
	)
	if err != nil {
		return conversation.TurnInput{}, fmt.Errorf("message.turn_input identity is invalid: %w", err)
	}
	return input.
		WithSessionID(m.SessionID).
		WithAttachments(projection.Attachments).
		WithViewerRecipient(projection.ViewerRecipient).
		WithForcedRoute(projection.ForcedRoute).
		WithRoute(projection.Route), nil
}

// NewErrorMessage はエラーメッセージを作成
func NewErrorMessage(from, to, sessionID string, taskID modulecore.TaskID, errMsg string) Message {
	return Message{
		From:      from,
		To:        to,
		SessionID: sessionID,
		TaskID:    taskID,
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
	if err := m.TaskID.Validate(); err != nil {
		return fmt.Errorf("message.task_id is invalid: %w", err)
	}
	if m.Timestamp == "" {
		return fmt.Errorf("message.timestamp is required")
	}
	if _, err := time.Parse(time.RFC3339, m.Timestamp); err != nil {
		return fmt.Errorf("message.timestamp must be RFC3339 format: %w", err)
	}
	if m.TurnInput != nil {
		if _, err := m.ReconstructTurnInput(); err != nil {
			return err
		}
	}
	return nil
}
