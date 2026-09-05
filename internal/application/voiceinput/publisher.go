package voiceinput

import (
	"fmt"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	SurfaceVoiceChat       = "voice_chat"
	VoiceDirectEvidenceKey = "voice_direct"
)

type EventEmitter interface {
	Emit(eventType, from, to, content, route, taskID, sessionID, channel, chatID string)
}

type CorrelatedEventEmitter interface {
	EmitWithMessageID(eventType, from, to, content, route, taskID, sessionID, channel, chatID, messageID string)
}

type SessionTurnLogger interface {
	WriteUser(sessionID, channel, content string)
	WriteAssistant(sessionID, channel, route, taskID, content string)
}

type CorrelatedSessionTurnLogger interface {
	WriteUserWithIdentity(sessionID, channel, messageID, traceID, content string)
	WriteAssistantWithIdentity(sessionID, channel, route, taskID, messageID, traceID, content string)
}

type Publisher struct {
	Events     EventEmitter
	TurnLogger SessionTurnLogger
	Input      conversation.TurnInput
	EmitMetric func(kind, point string, startedAt time.Time, route, taskID, sessionID, channel, chatID, detail string)
}

type PublishResult struct {
	Input     conversation.TurnInput
	TaskID    modulecore.TaskID
	MessageID string
	TraceID   string
	Result    Result
}

func (p Publisher) Publish(result Result) (PublishResult, error) {
	if err := result.Validate(); err != nil {
		return PublishResult{}, err
	}
	if err := p.Input.Validate(); err != nil {
		return PublishResult{}, fmt.Errorf("voice publisher input is invalid: %w", err)
	}
	taskID := p.Input.RootTaskID()
	if err := taskID.Validate(); err != nil {
		return PublishResult{}, fmt.Errorf("voice publisher root task_id is invalid: %w", err)
	}
	address := p.Input.ChannelAddress()
	if result.UserText != p.Input.MessageText() {
		return PublishResult{}, fmt.Errorf("voice publisher user text does not match input message text")
	}
	if result.SessionID != p.Input.SessionID() {
		return PublishResult{}, fmt.Errorf("voice publisher session_id does not match input")
	}
	if result.Channel != address.ChannelType() {
		return PublishResult{}, fmt.Errorf("voice publisher channel does not match input channel address")
	}
	if result.ChatID != address.ExternalConversationID() {
		return PublishResult{}, fmt.Errorf("voice publisher chat_id does not match input channel address")
	}
	if p.Input.Route() != routing.RouteCHAT {
		return PublishResult{}, fmt.Errorf("voice publisher input route must be CHAT")
	}
	if result.Timings.PublishedAt.IsZero() {
		result.Timings.PublishedAt = time.Now()
	}
	taskIDValue := taskID.String()
	for _, identity := range []struct {
		name  string
		value string
	}{
		{name: "turn_id", value: string(p.Input.TurnID())},
		{name: "trace_id", value: string(p.Input.TraceID())},
		{name: "user_message_id", value: string(p.Input.UserMessageID())},
		{name: "agent_message_id", value: string(p.Input.AgentMessageID())},
	} {
		if taskIDValue == identity.value {
			return PublishResult{}, fmt.Errorf("voice publisher task_id must differ from %s", identity.name)
		}
	}
	traceID := string(p.Input.TraceID())
	userMessageID := string(p.Input.UserMessageID())
	responseMessageID := string(p.Input.AgentMessageID())
	sessionID := p.Input.SessionID()
	channel := address.ChannelType()
	chatID := address.ExternalConversationID()
	route := string(p.Input.Route())
	if p.Events != nil {
		if result.UserText != "" {
			p.emitMessage("message.received", "user", "mio", result.UserText, "", taskIDValue, sessionID, channel, chatID, userMessageID)
		}
		if p.EmitMetric != nil {
			p.EmitMetric("network", "server_received", result.Timings.StartedAt, "", taskIDValue, sessionID, channel, chatID, result.UtteranceID)
		}
		p.Events.Emit(
			"routing.decision",
			"mio",
			"",
			fmt.Sprintf(
				"confidence 100%% surface=%s target_agent=mio provider_alias=Chat evidence=%s:matched:CHAT utterance_id=%s",
				SurfaceVoiceChat,
				VoiceDirectEvidenceKey,
				result.UtteranceID,
			),
			route,
			taskIDValue,
			sessionID,
			channel,
			chatID,
		)
		if p.EmitMetric != nil {
			detail := fmt.Sprintf("surface=%s source=%s", SurfaceVoiceChat, VoiceDirectEvidenceKey)
			p.EmitMetric("llm", "route_decision", result.Timings.StartedAt, route, taskIDValue, sessionID, channel, chatID, detail)
			p.EmitMetric("llm", "dispatch_start", result.Timings.StartedAt, route, taskIDValue, sessionID, channel, chatID, detail)
		}
		p.emitMessage("agent.response", "mio", "user", result.Reply, route, taskIDValue, sessionID, channel, chatID, responseMessageID)
		if p.EmitMetric != nil {
			p.EmitMetric("llm", "response_complete", result.Timings.StartedAt, route, taskIDValue, sessionID, channel, chatID, fmt.Sprintf("utterance_id=%s response_len=%d", result.UtteranceID, len(result.Reply)))
		}
	}
	if p.TurnLogger != nil {
		if correlated, ok := p.TurnLogger.(CorrelatedSessionTurnLogger); ok {
			if result.UserText != "" {
				correlated.WriteUserWithIdentity(sessionID, channel, userMessageID, traceID, result.UserText)
			}
			correlated.WriteAssistantWithIdentity(sessionID, channel, route, taskIDValue, responseMessageID, traceID, result.Reply)
		} else {
			if result.UserText != "" {
				p.TurnLogger.WriteUser(sessionID, channel, result.UserText)
			}
			p.TurnLogger.WriteAssistant(sessionID, channel, route, taskIDValue, result.Reply)
		}
	}
	return PublishResult{Input: p.Input, TaskID: taskID, MessageID: responseMessageID, TraceID: traceID, Result: result}, nil
}

func (p Publisher) emitMessage(eventType, from, to, content, route, taskID, sessionID, channel, chatID, messageID string) {
	if correlated, ok := p.Events.(CorrelatedEventEmitter); ok && messageID != "" {
		correlated.EmitWithMessageID(eventType, from, to, content, route, taskID, sessionID, channel, chatID, messageID)
		return
	}
	p.Events.Emit(eventType, from, to, content, route, taskID, sessionID, channel, chatID)
}
