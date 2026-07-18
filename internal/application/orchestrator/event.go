package orchestrator

import (
	"fmt"
	"strings"
	"time"
)

var jst = time.FixedZone("JST", 9*60*60)

// EventListener receives orchestrator events for external monitoring
type EventListener interface {
	OnEvent(ev OrchestratorEvent)
}

// OrchestratorEvent represents a significant event in message processing
type OrchestratorEvent struct {
	Seq        int64  `json:"seq,omitempty"`         // monotonic event sequence (set by EventHub)
	Type       string `json:"type"`                  // message.received, routing.decision, agent.start, agent.response
	From       string `json:"from"`                  // source agent
	To         string `json:"to,omitempty"`          // target agent
	Content    string `json:"content"`               // message content
	RawContent string `json:"raw_content,omitempty"` // unedited model output for diagnostics
	MessageID  string `json:"message_id,omitempty"`  // stable message identifier within a session
	TurnIndex  int    `json:"turn_index,omitempty"`  // stable turn order within a session
	Category   string `json:"category,omitempty"`    // domain-specific category (e.g. IdleChat topic category)
	Strategy   string `json:"strategy,omitempty"`    // domain-specific strategy (e.g. IdleChat topic strategy)
	Route      string `json:"route,omitempty"`       // routing category
	JobID      string `json:"job_id,omitempty"`      // task identifier
	SessionID  string `json:"session_id,omitempty"`  // session identifier
	Channel    string `json:"channel,omitempty"`     // channel identifier
	ChatID     string `json:"chat_id,omitempty"`     // chat identifier
	Timestamp  string `json:"timestamp"`
}

// NewEvent creates a new OrchestratorEvent with the current timestamp
func NewEvent(eventType, from, to, content, route, jobID, sessionID, channel, chatID string) OrchestratorEvent {
	return OrchestratorEvent{
		Type:      eventType,
		From:      from,
		To:        to,
		Content:   content,
		Route:     route,
		JobID:     jobID,
		SessionID: sessionID,
		Channel:   channel,
		ChatID:    chatID,
		Timestamp: time.Now().In(jst).Format(time.RFC3339),
	}
}

func isConversationMessageEvent(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case "message.received", "agent.response", "agent.delegate", "agent.acknowledge", "agent.report":
		return true
	default:
		return false
	}
}

func conversationIdentitySession(sessionID, chatID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID != "" {
		return sessionID
	}
	chatID = strings.TrimSpace(chatID)
	if chatID != "" {
		return chatID
	}
	return "chat"
}

func conversationMessageID(sessionID string, turnIndex int) string {
	sessionID = conversationIdentitySession(sessionID, "")
	if turnIndex < 1 {
		turnIndex = 1
	}
	return fmt.Sprintf("%s:chat:msg:%04d", sessionID, turnIndex)
}
