package task

import (
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Task はユーザーからの指示を表す値オブジェクト
type Task struct {
	jobID          JobID
	turnID         modulecore.TurnID
	traceID        modulecore.TraceID
	rootTaskID     modulecore.TaskID
	userMessageID  modulecore.MessageID
	agentMessageID modulecore.MessageID
	userMessage    string
	channel        string
	chatID         string
	sessionID      string
	attachments    []attachment.Attachment
	recipient      string
	forcedRoute    routing.Route // 明示的なルート指定（オプション）
	route          routing.Route // 決定されたルート
}

// NewTask は新しいTaskを作成
func NewTask(jobID JobID, userMessage, channel, chatID string) Task {
	return Task{
		jobID:          jobID,
		turnID:         modulecore.NewTurnID(),
		traceID:        modulecore.NewTraceID(),
		rootTaskID:     modulecore.NewTaskID(),
		userMessageID:  modulecore.NewMessageID(),
		agentMessageID: modulecore.NewMessageID(),
		userMessage:    userMessage,
		channel:        channel,
		chatID:         chatID,
		forcedRoute:    "",
		route:          "",
	}
}

// JobID はジョブIDを返す
func (t Task) JobID() JobID {
	return t.jobID
}

// TurnID は会話ターンの正規IDを返す。
func (t Task) TurnID() modulecore.TurnID {
	return t.turnID
}

// TraceID は会話ターンを追跡する正規IDを返す。
func (t Task) TraceID() modulecore.TraceID {
	return t.traceID
}

// RootTaskID は会話ターンのルートタスク正規IDを返す。
func (t Task) RootTaskID() modulecore.TaskID {
	return t.rootTaskID
}

// UserMessageID は利用者発話の正規IDを返す。
func (t Task) UserMessageID() modulecore.MessageID {
	return t.userMessageID
}

// AgentMessageID はAgent発話の正規IDを返す。
func (t Task) AgentMessageID() modulecore.MessageID {
	return t.agentMessageID
}

// UserMessage はユーザーメッセージを返す
func (t Task) UserMessage() string {
	return t.userMessage
}

// Channel はチャネルを返す
func (t Task) Channel() string {
	return t.channel
}

// ChatID はチャットIDを返す
func (t Task) ChatID() string {
	return t.chatID
}

// SessionID はCORE正本の会話セッションIDを返す。
func (t Task) SessionID() string {
	return t.sessionID
}

// Attachments はユーザー入力に添付されたファイルを返す。
func (t Task) Attachments() []attachment.Attachment {
	return append([]attachment.Attachment(nil), t.attachments...)
}

// ViewerRecipient returns the requested Viewer chat recipient / speaker.
func (t Task) ViewerRecipient() string {
	return t.recipient
}

// ForcedRoute は強制ルートを返す
func (t Task) ForcedRoute() routing.Route {
	return t.forcedRoute
}

// Route は決定されたルートを返す
func (t Task) Route() routing.Route {
	return t.route
}

// WithForcedRoute は強制ルートを設定した新しいTaskを返す
func (t Task) WithForcedRoute(route routing.Route) Task {
	t.forcedRoute = route
	return t
}

// WithRoute はルートを設定した新しいTaskを返す
func (t Task) WithRoute(route routing.Route) Task {
	t.route = route
	return t
}

// WithUserMessage returns a new task with the updated user message.
func (t Task) WithUserMessage(message string) Task {
	t.userMessage = message
	return t
}

// WithSessionID はCORE正本の会話セッションIDを設定した新しいTaskを返す。
func (t Task) WithSessionID(sessionID string) Task {
	t.sessionID = sessionID
	return t
}

// WithConversationIdentity は会話ターンの正規identityを設定した新しいTaskを返す。
func (t Task) WithConversationIdentity(turnID modulecore.TurnID, traceID modulecore.TraceID, rootTaskID modulecore.TaskID, userMessageID, agentMessageID modulecore.MessageID) Task {
	t.turnID = turnID
	t.traceID = traceID
	t.rootTaskID = rootTaskID
	t.userMessageID = userMessageID
	t.agentMessageID = agentMessageID
	return t
}

// WithAttachments returns a new task with user attachments.
func (t Task) WithAttachments(attachments []attachment.Attachment) Task {
	t.attachments = append([]attachment.Attachment(nil), attachments...)
	return t
}

// WithViewerRecipient returns a new task with the requested Viewer chat speaker.
func (t Task) WithViewerRecipient(recipient string) Task {
	t.recipient = recipient
	return t
}

// HasForcedRoute は強制ルートが設定されているかを判定
func (t Task) HasForcedRoute() bool {
	return t.forcedRoute != ""
}
