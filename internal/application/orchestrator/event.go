package orchestrator

import (
	"context"
	"strings"
	"sync"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type canonicalTraceContextKey struct{}

func contextWithCanonicalTrace(ctx context.Context, traceID modulecore.TraceID) context.Context {
	if traceID.Validate() != nil {
		return ctx
	}
	return context.WithValue(ctx, canonicalTraceContextKey{}, traceID)
}

func canonicalTraceFromContext(ctx context.Context) modulecore.TraceID {
	if ctx != nil {
		if traceID, ok := ctx.Value(canonicalTraceContextKey{}).(modulecore.TraceID); ok && traceID.Validate() == nil {
			return traceID
		}
	}
	return modulecore.NewTraceID()
}

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
	MessageID  string `json:"message_id,omitempty"`  // globally unique identity of one logical message
	TurnIndex  int    `json:"turn_index,omitempty"`  // stable turn order within a session
	Category   string `json:"category,omitempty"`    // domain-specific category (e.g. IdleChat topic category)
	Strategy   string `json:"strategy,omitempty"`    // domain-specific strategy (e.g. IdleChat topic strategy)
	Route      string `json:"route,omitempty"`       // routing category
	JobID      string `json:"job_id,omitempty"`      // task identifier
	TraceID    string `json:"trace_id,omitempty"`    // root interaction correlation identifier
	SessionID  string `json:"session_id,omitempty"`  // session identifier
	Channel    string `json:"channel,omitempty"`     // channel identifier
	ChatID     string `json:"chat_id,omitempty"`     // chat identifier
	Timestamp  string `json:"timestamp"`
}

// NewEvent creates a new OrchestratorEvent with the current timestamp
func NewEvent(eventType, from, to, content, route, jobID, sessionID, channel, chatID string) OrchestratorEvent {
	return NewEventWithTraceID(modulecore.NewTraceID(), eventType, from, to, content, route, jobID, sessionID, channel, chatID)
}

// NewEventWithTraceID creates an event inside an owner-assigned canonical
// trace. Callers must create the TraceID once at the root interaction boundary.
func NewEventWithTraceID(traceID modulecore.TraceID, eventType, from, to, content, route, jobID, sessionID, channel, chatID string) OrchestratorEvent {
	return OrchestratorEvent{
		Type:      eventType,
		From:      from,
		To:        to,
		Content:   content,
		Route:     route,
		JobID:     jobID,
		TraceID:   string(traceID),
		SessionID: sessionID,
		Channel:   channel,
		ChatID:    chatID,
		Timestamp: time.Now().In(jst).Format(time.RFC3339),
	}
}

type eventTraceBindings struct {
	mu    sync.RWMutex
	byJob map[string]modulecore.TraceID
}

func newEventTraceBindings() *eventTraceBindings {
	return &eventTraceBindings{byJob: make(map[string]modulecore.TraceID)}
}

func (b *eventTraceBindings) Bind(jobID string, traceID modulecore.TraceID) {
	if b == nil || strings.TrimSpace(jobID) == "" || traceID.Validate() != nil {
		return
	}
	b.mu.Lock()
	b.byJob[jobID] = traceID
	b.mu.Unlock()
}

func (b *eventTraceBindings) Release(jobID string) {
	if b == nil || strings.TrimSpace(jobID) == "" {
		return
	}
	b.mu.Lock()
	delete(b.byJob, jobID)
	b.mu.Unlock()
}

func (b *eventTraceBindings) Resolve(jobID string) modulecore.TraceID {
	if b != nil && strings.TrimSpace(jobID) != "" {
		b.mu.RLock()
		traceID := b.byJob[jobID]
		b.mu.RUnlock()
		if traceID.Validate() == nil {
			return traceID
		}
	}
	return modulecore.NewTraceID()
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

func conversationMessageID() string {
	return string(modulecore.NewMessageID())
}

type conversationIdentityTracker struct {
	mu                 sync.Mutex
	turns              map[string]int
	responseMessageIDs map[string]string
}

func newConversationIdentityTracker() *conversationIdentityTracker {
	return &conversationIdentityTracker{
		turns:              map[string]int{},
		responseMessageIDs: map[string]string{},
	}
}

func (t *conversationIdentityTracker) Assign(ev *OrchestratorEvent, preferredMessageID string) {
	if t == nil || ev == nil || !isConversationMessageEvent(ev.Type) {
		return
	}
	sessionID := conversationIdentitySession(ev.SessionID, ev.ChatID)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.turns[sessionID]++
	ev.TurnIndex = t.turns[sessionID]
	ev.MessageID = strings.TrimSpace(preferredMessageID)
	if ev.MessageID == "" {
		ev.MessageID = conversationMessageID()
	}
	if ev.Type == "agent.response" && strings.TrimSpace(ev.JobID) != "" {
		t.responseMessageIDs[ev.JobID] = ev.MessageID
	}
}

func (t *conversationIdentityTracker) TakeResponseMessageID(jobID string) string {
	if t == nil || strings.TrimSpace(jobID) == "" {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	messageID := t.responseMessageIDs[jobID]
	delete(t.responseMessageIDs, jobID)
	return messageID
}
