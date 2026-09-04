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

// EventListener receives orchestrator events at the synchronous canonical
// publication boundary. Production listeners must persist the event before
// projecting it to external monitoring surfaces and return any publication
// failure to the caller.
type EventListener interface {
	OnEvent(ev OrchestratorEvent) error
}

type eventPublicationFailure struct {
	err    error
	cancel context.CancelCauseFunc
}

// eventPublicationFailureTracker retains the first canonical publication
// failure for each active request trace. Event callbacks intentionally remain
// void for compatibility, so the owning ProcessMessage call observes this
// tracker at its request boundary.
type eventPublicationFailureTracker struct {
	mu     sync.Mutex
	active map[modulecore.TraceID]eventPublicationFailure
}

func newEventPublicationFailureTracker() *eventPublicationFailureTracker {
	return &eventPublicationFailureTracker{active: make(map[modulecore.TraceID]eventPublicationFailure)}
}

func (t *eventPublicationFailureTracker) Begin(traceID modulecore.TraceID, cancel context.CancelCauseFunc) {
	if t == nil || traceID.Validate() != nil {
		return
	}
	t.mu.Lock()
	if t.active == nil {
		t.active = make(map[modulecore.TraceID]eventPublicationFailure)
	}
	t.active[traceID] = eventPublicationFailure{cancel: cancel}
	t.mu.Unlock()
}

func (t *eventPublicationFailureTracker) Record(traceID modulecore.TraceID, err error) {
	if t == nil || traceID.Validate() != nil || err == nil {
		return
	}
	var cancel context.CancelCauseFunc
	t.mu.Lock()
	failure, ok := t.active[traceID]
	if !ok || failure.err != nil {
		t.mu.Unlock()
		return
	}
	failure.err = err
	t.active[traceID] = failure
	cancel = failure.cancel
	t.mu.Unlock()
	if cancel != nil {
		cancel(err)
	}
}

func (t *eventPublicationFailureTracker) Current(traceID modulecore.TraceID) error {
	if t == nil || traceID.Validate() != nil {
		return nil
	}
	t.mu.Lock()
	failure := t.active[traceID]
	t.mu.Unlock()
	return failure.err
}

func (t *eventPublicationFailureTracker) End(traceID modulecore.TraceID) error {
	if t == nil || traceID.Validate() != nil {
		return nil
	}
	t.mu.Lock()
	failure := t.active[traceID]
	delete(t.active, traceID)
	t.mu.Unlock()
	return failure.err
}

// OrchestratorEvent represents a significant event in message processing
type OrchestratorEvent struct {
	Seq        int64                 `json:"seq,omitempty"`         // monotonic event sequence (set by EventHub)
	Type       string                `json:"type"`                  // message.received, routing.decision, agent.start, agent.response
	From       string                `json:"from"`                  // source agent
	To         string                `json:"to,omitempty"`          // target agent
	Content    string                `json:"content"`               // message content
	RawContent string                `json:"raw_content,omitempty"` // unedited model output for diagnostics
	MessageID  string                `json:"message_id,omitempty"`  // globally unique identity of one logical message
	TurnIndex  int                   `json:"turn_index,omitempty"`  // stable turn order within a session
	Category   string                `json:"category,omitempty"`    // domain-specific category (e.g. IdleChat topic category)
	Strategy   string                `json:"strategy,omitempty"`    // domain-specific strategy (e.g. IdleChat topic strategy)
	Route      string                `json:"route,omitempty"`       // routing category
	JobID      string                `json:"job_id,omitempty"`      // task identifier
	TraceID    string                `json:"trace_id,omitempty"`    // root interaction correlation identifier
	SessionID  string                `json:"session_id,omitempty"`  // session identifier
	ThreadID   modulecore.ThreadID   `json:"thread_id,omitempty"`   // canonical conversation/work thread identifier
	ThreadSeq  modulecore.ThreadSeq  `json:"thread_seq,omitempty"`  // canonical thread sequence within the session
	ThreadKind modulecore.ThreadKind `json:"thread_kind,omitempty"` // canonical thread kind
	Channel    string                `json:"channel,omitempty"`     // channel identifier
	ChatID     string                `json:"chat_id,omitempty"`     // chat identifier
	Timestamp  string                `json:"timestamp"`
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
	boundResponseIDs   map[string]string
	responseMessageIDs map[string]string
}

func newConversationIdentityTracker() *conversationIdentityTracker {
	return &conversationIdentityTracker{
		turns:              map[string]int{},
		boundResponseIDs:   map[string]string{},
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
	if ev.MessageID == "" && ev.Type == "agent.response" && strings.TrimSpace(ev.JobID) != "" {
		ev.MessageID = t.boundResponseIDs[ev.JobID]
		delete(t.boundResponseIDs, ev.JobID)
	}
	if ev.MessageID == "" {
		ev.MessageID = conversationMessageID()
	}
	if ev.Type == "agent.response" && strings.TrimSpace(ev.JobID) != "" {
		t.responseMessageIDs[ev.JobID] = ev.MessageID
	}
}

func (t *conversationIdentityTracker) BindResponseMessageID(jobID string, messageID modulecore.MessageID) {
	if t == nil || strings.TrimSpace(jobID) == "" || messageID.Validate() != nil {
		return
	}
	t.mu.Lock()
	t.boundResponseIDs[jobID] = string(messageID)
	t.mu.Unlock()
}

func (t *conversationIdentityTracker) ReleaseResponseMessageID(jobID string) {
	if t == nil || strings.TrimSpace(jobID) == "" {
		return
	}
	t.mu.Lock()
	delete(t.boundResponseIDs, jobID)
	delete(t.responseMessageIDs, jobID)
	t.mu.Unlock()
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
