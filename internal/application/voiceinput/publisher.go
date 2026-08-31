package voiceinput

import (
	"fmt"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
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
	Events       EventEmitter
	TurnLogger   SessionTurnLogger
	TraceID      modulecore.TraceID
	NewJobID     func() string
	NewMessageID func() string
	EmitMetric   func(kind, point string, startedAt time.Time, route, jobID, sessionID, channel, chatID, detail string)
}

type PublishResult struct {
	JobID     string
	MessageID string
	TraceID   string
	Result    Result
}

func (p Publisher) Publish(result Result) (PublishResult, error) {
	if err := result.Validate(); err != nil {
		return PublishResult{}, err
	}
	if err := p.TraceID.Validate(); err != nil {
		return PublishResult{}, fmt.Errorf("voice publisher trace_id is invalid: %w", err)
	}
	if result.Timings.PublishedAt.IsZero() {
		result.Timings.PublishedAt = time.Now()
	}
	jobID := ""
	if p.NewJobID != nil {
		jobID = p.NewJobID()
	}
	if jobID != "" && jobID == string(p.TraceID) {
		return PublishResult{}, fmt.Errorf("voice publisher trace_id must differ from job_id")
	}
	userMessageID := p.newMessageID()
	responseMessageID := p.newMessageID()
	if p.Events != nil {
		if result.UserText != "" {
			p.emitMessage("message.received", "user", "mio", result.UserText, "", jobID, result.SessionID, result.Channel, result.ChatID, userMessageID)
		}
		if p.EmitMetric != nil {
			p.EmitMetric("network", "server_received", result.Timings.StartedAt, "", jobID, result.SessionID, result.Channel, result.ChatID, result.UtteranceID)
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
			"CHAT",
			jobID,
			result.SessionID,
			result.Channel,
			result.ChatID,
		)
		if p.EmitMetric != nil {
			detail := fmt.Sprintf("surface=%s source=%s", SurfaceVoiceChat, VoiceDirectEvidenceKey)
			p.EmitMetric("llm", "route_decision", result.Timings.StartedAt, "CHAT", jobID, result.SessionID, result.Channel, result.ChatID, detail)
			p.EmitMetric("llm", "dispatch_start", result.Timings.StartedAt, "CHAT", jobID, result.SessionID, result.Channel, result.ChatID, detail)
		}
		p.emitMessage("agent.response", "mio", "user", result.Reply, "CHAT", jobID, result.SessionID, result.Channel, result.ChatID, responseMessageID)
		if p.EmitMetric != nil {
			p.EmitMetric("llm", "response_complete", result.Timings.StartedAt, "CHAT", jobID, result.SessionID, result.Channel, result.ChatID, fmt.Sprintf("utterance_id=%s response_len=%d", result.UtteranceID, len(result.Reply)))
		}
	}
	if p.TurnLogger != nil {
		if correlated, ok := p.TurnLogger.(CorrelatedSessionTurnLogger); ok {
			if result.UserText != "" {
				correlated.WriteUserWithIdentity(result.SessionID, result.Channel, userMessageID, string(p.TraceID), result.UserText)
			}
			correlated.WriteAssistantWithIdentity(result.SessionID, result.Channel, "CHAT", jobID, responseMessageID, string(p.TraceID), result.Reply)
		} else {
			if result.UserText != "" {
				p.TurnLogger.WriteUser(result.SessionID, result.Channel, result.UserText)
			}
			p.TurnLogger.WriteAssistant(result.SessionID, result.Channel, "CHAT", jobID, result.Reply)
		}
	}
	return PublishResult{JobID: jobID, MessageID: responseMessageID, TraceID: string(p.TraceID), Result: result}, nil
}

func (p Publisher) newMessageID() string {
	if p.NewMessageID == nil {
		return ""
	}
	return p.NewMessageID()
}

func (p Publisher) emitMessage(eventType, from, to, content, route, jobID, sessionID, channel, chatID, messageID string) {
	if correlated, ok := p.Events.(CorrelatedEventEmitter); ok && messageID != "" {
		correlated.EmitWithMessageID(eventType, from, to, content, route, jobID, sessionID, channel, chatID, messageID)
		return
	}
	p.Events.Emit(eventType, from, to, content, route, jobID, sessionID, channel, chatID)
}
