package voiceinput

import (
	"fmt"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
)

const (
	SurfaceVoiceChat       = "voice_chat"
	VoiceDirectEvidenceKey = "voice_direct"
)

type EventEmitter interface {
	Emit(eventType, from, to, content, route, jobID, sessionID, channel, chatID string)
}

type CorrelatedEventEmitter interface {
	EmitWithMessageID(eventType, from, to, content, route, jobID, sessionID, channel, chatID, messageID string)
}

type SessionTurnLogger interface {
	WriteUser(sessionID, channel, content string)
	WriteAssistant(sessionID, channel, route, jobID, content string)
}

type CorrelatedSessionTurnLogger interface {
	WriteUserWithIdentity(sessionID, channel, messageID, traceID, content string)
	WriteAssistantWithIdentity(sessionID, channel, route, jobID, messageID, traceID, content string)
}

type Publisher struct {
	Events     EventEmitter
	TurnLogger SessionTurnLogger
	Input      conversation.TurnInput
	NewJobID   func() string
	EmitMetric func(kind, point string, startedAt time.Time, route, jobID, sessionID, channel, chatID, detail string)
}

type PublishResult struct {
	Input     conversation.TurnInput
	JobID     string
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
	jobID := ""
	if p.NewJobID != nil {
		jobID = p.NewJobID()
	}
	if jobID != "" && jobID == string(p.Input.TurnID()) {
		return PublishResult{}, fmt.Errorf("voice publisher job_id must differ from turn_id")
	}
	if jobID != "" && jobID == string(p.Input.TraceID()) {
		return PublishResult{}, fmt.Errorf("voice publisher job_id must differ from trace_id")
	}
	if jobID != "" && jobID == string(p.Input.RootTaskID()) {
		return PublishResult{}, fmt.Errorf("voice publisher job_id must differ from root_task_id")
	}
	if jobID != "" && jobID == string(p.Input.UserMessageID()) {
		return PublishResult{}, fmt.Errorf("voice publisher job_id must differ from user_message_id")
	}
	if jobID != "" && jobID == string(p.Input.AgentMessageID()) {
		return PublishResult{}, fmt.Errorf("voice publisher job_id must differ from agent_message_id")
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
			p.emitMessage("message.received", "user", "mio", result.UserText, "", jobID, sessionID, channel, chatID, userMessageID)
		}
		if p.EmitMetric != nil {
			p.EmitMetric("network", "server_received", result.Timings.StartedAt, "", jobID, sessionID, channel, chatID, result.UtteranceID)
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
			jobID,
			sessionID,
			channel,
			chatID,
		)
		if p.EmitMetric != nil {
			detail := fmt.Sprintf("surface=%s source=%s", SurfaceVoiceChat, VoiceDirectEvidenceKey)
			p.EmitMetric("llm", "route_decision", result.Timings.StartedAt, route, jobID, sessionID, channel, chatID, detail)
			p.EmitMetric("llm", "dispatch_start", result.Timings.StartedAt, route, jobID, sessionID, channel, chatID, detail)
		}
		p.emitMessage("agent.response", "mio", "user", result.Reply, route, jobID, sessionID, channel, chatID, responseMessageID)
		if p.EmitMetric != nil {
			p.EmitMetric("llm", "response_complete", result.Timings.StartedAt, route, jobID, sessionID, channel, chatID, fmt.Sprintf("utterance_id=%s response_len=%d", result.UtteranceID, len(result.Reply)))
		}
	}
	if p.TurnLogger != nil {
		if correlated, ok := p.TurnLogger.(CorrelatedSessionTurnLogger); ok {
			if result.UserText != "" {
				correlated.WriteUserWithIdentity(sessionID, channel, userMessageID, traceID, result.UserText)
			}
			correlated.WriteAssistantWithIdentity(sessionID, channel, route, jobID, responseMessageID, traceID, result.Reply)
		} else {
			if result.UserText != "" {
				p.TurnLogger.WriteUser(sessionID, channel, result.UserText)
			}
			p.TurnLogger.WriteAssistant(sessionID, channel, route, jobID, result.Reply)
		}
	}
	return PublishResult{Input: p.Input, JobID: jobID, MessageID: responseMessageID, TraceID: traceID, Result: result}, nil
}

func (p Publisher) emitMessage(eventType, from, to, content, route, jobID, sessionID, channel, chatID, messageID string) {
	if correlated, ok := p.Events.(CorrelatedEventEmitter); ok && messageID != "" {
		correlated.EmitWithMessageID(eventType, from, to, content, route, jobID, sessionID, channel, chatID, messageID)
		return
	}
	p.Events.Emit(eventType, from, to, content, route, jobID, sessionID, channel, chatID)
}
