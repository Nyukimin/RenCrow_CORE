package voiceinput

import (
	"fmt"
	"time"
)

const (
	SurfaceVoiceChat       = "voice_chat"
	VoiceDirectEvidenceKey = "voice_direct"
)

type EventEmitter interface {
	Emit(eventType, from, to, content, route, jobID, sessionID, channel, chatID string)
}

type SessionTurnLogger interface {
	WriteUser(sessionID, channel, content string)
	WriteAssistant(sessionID, channel, route, jobID, content string)
}

type Publisher struct {
	Events     EventEmitter
	TurnLogger SessionTurnLogger
	NewJobID   func() string
	EmitMetric func(kind, point string, startedAt time.Time, route, jobID, sessionID, channel, chatID, detail string)
}

type PublishResult struct {
	JobID  string
	Result Result
}

func (p Publisher) Publish(result Result) (PublishResult, error) {
	if err := result.Validate(); err != nil {
		return PublishResult{}, err
	}
	if result.Timings.PublishedAt.IsZero() {
		result.Timings.PublishedAt = time.Now()
	}
	jobID := ""
	if p.NewJobID != nil {
		jobID = p.NewJobID()
	}
	if p.Events != nil {
		if result.UserText != "" {
			p.Events.Emit("message.received", "user", "mio", result.UserText, "", "", result.SessionID, result.Channel, result.ChatID)
		}
		if p.EmitMetric != nil {
			p.EmitMetric("network", "server_received", result.Timings.StartedAt, "", "", result.SessionID, result.Channel, result.ChatID, result.UtteranceID)
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
		p.Events.Emit("agent.response", "mio", "user", result.Reply, "CHAT", jobID, result.SessionID, result.Channel, result.ChatID)
		if p.EmitMetric != nil {
			p.EmitMetric("llm", "response_complete", result.Timings.StartedAt, "CHAT", jobID, result.SessionID, result.Channel, result.ChatID, fmt.Sprintf("utterance_id=%s response_len=%d", result.UtteranceID, len(result.Reply)))
		}
	}
	if p.TurnLogger != nil {
		if result.UserText != "" {
			p.TurnLogger.WriteUser(result.SessionID, result.Channel, result.UserText)
		}
		p.TurnLogger.WriteAssistant(result.SessionID, result.Channel, "CHAT", jobID, result.Reply)
	}
	return PublishResult{JobID: jobID, Result: result}, nil
}
