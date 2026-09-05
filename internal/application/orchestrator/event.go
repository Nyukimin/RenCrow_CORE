package orchestrator

import (
	"context"
	"fmt"
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

const canonicalExecutionActorKind = "agent"

// eventExecutionIdentity is the task-scoped identity that may be attached to
// an orchestrator event only after the Task owner has issued the exact Run.
// It is deliberately separate from the event payload so the top-level fields
// remain the canonical projection boundary.
type eventExecutionIdentity struct {
	TaskID    modulecore.TaskID
	RunID     modulecore.RunID
	ActorKind string
	ActorID   string
}

type eventExecutionIdentityBindings struct {
	mu     sync.RWMutex
	byTask map[modulecore.TaskID]eventExecutionIdentity
}

func newEventExecutionIdentityBindings() *eventExecutionIdentityBindings {
	return &eventExecutionIdentityBindings{byTask: make(map[modulecore.TaskID]eventExecutionIdentity)}
}

// Bind records the exact execution identity for one Task. Rebinding with the
// same identity is idempotent; a different identity is a conflict and cannot
// silently replace the owner-issued Run.
func (b *eventExecutionIdentityBindings) Bind(taskID modulecore.TaskID, runID modulecore.RunID, actorKind, actorID string) error {
	identity := eventExecutionIdentity{
		TaskID:    taskID,
		RunID:     runID,
		ActorKind: actorKind,
		ActorID:   actorID,
	}
	if err := validateEventExecutionIdentity(identity); err != nil {
		return err
	}
	if b == nil {
		return fmt.Errorf("execution identity bindings are unavailable")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.byTask == nil {
		b.byTask = make(map[modulecore.TaskID]eventExecutionIdentity)
	}
	if existing, ok := b.byTask[taskID]; ok {
		if existing != identity {
			return fmt.Errorf("execution identity conflict for task %s: existing run=%s actor=%s/%s, requested run=%s actor=%s/%s", taskID, existing.RunID, existing.ActorKind, existing.ActorID, runID, identity.ActorKind, identity.ActorID)
		}
		return nil
	}
	b.byTask[taskID] = identity
	return nil
}

// Resolve returns only an explicitly bound identity. It never derives a RunID
// or actor from a Task, trace, route, payload, model, or provider.
func (b *eventExecutionIdentityBindings) Resolve(taskID modulecore.TaskID) (eventExecutionIdentity, bool) {
	if b == nil || taskID.Validate() != nil {
		return eventExecutionIdentity{}, false
	}
	b.mu.RLock()
	identity, ok := b.byTask[taskID]
	b.mu.RUnlock()
	return identity, ok
}

// Release removes the binding for exactly the supplied typed TaskID.
func (b *eventExecutionIdentityBindings) Release(taskID modulecore.TaskID) {
	if b == nil || taskID.Validate() != nil {
		return
	}
	b.mu.Lock()
	delete(b.byTask, taskID)
	b.mu.Unlock()
}

func validateEventExecutionIdentity(identity eventExecutionIdentity) error {
	if err := identity.TaskID.Validate(); err != nil {
		return fmt.Errorf("execution task_id is invalid: %w", err)
	}
	if err := identity.RunID.Validate(); err != nil {
		return fmt.Errorf("execution run_id is invalid: %w", err)
	}
	if identity.ActorKind != canonicalExecutionActorKind {
		return fmt.Errorf("execution actor_kind must be %q", canonicalExecutionActorKind)
	}
	actorID, err := canonicalCoreActor(identity.ActorID)
	if err != nil {
		return fmt.Errorf("execution actor_id is invalid: %w", err)
	}
	if identity.ActorID != actorID {
		return fmt.Errorf("execution actor_id must be canonical: %q", identity.ActorID)
	}
	return nil
}

// ValidateOrchestratorEventExecutionIdentity validates the optional
// task-scoped execution identity carried by an OrchestratorEvent. Task-only
// events are valid before Run issuance; a Run or actor claim is all-or-none.
func ValidateOrchestratorEventExecutionIdentity(event OrchestratorEvent) error {
	runSet := event.RunID != ""
	actorKindSet := strings.TrimSpace(event.ActorKind) != ""
	actorIDSet := strings.TrimSpace(event.ActorID) != ""
	if actorKindSet != actorIDSet {
		return fmt.Errorf("execution actor_kind and actor_id must be set together")
	}
	if !runSet {
		if actorKindSet || actorIDSet {
			return fmt.Errorf("execution actor identity requires run_id")
		}
		return nil
	}
	if event.TaskID == "" {
		return fmt.Errorf("execution run_id requires task_id")
	}
	identity := eventExecutionIdentity{
		TaskID:    event.TaskID,
		RunID:     event.RunID,
		ActorKind: event.ActorKind,
		ActorID:   event.ActorID,
	}
	return validateEventExecutionIdentity(identity)
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
	EventID            modulecore.EventID    `json:"event_id"`              // globally unique immutable event identity
	EventSeq           modulecore.EventSeq   `json:"event_seq,omitempty"`   // canonical store-owned append sequence
	Type               string                `json:"type"`                  // message.received, routing.decision, agent.start, agent.response
	From               string                `json:"from"`                  // source agent
	To                 string                `json:"to,omitempty"`          // target agent
	Content            string                `json:"content"`               // message content
	RawContent         string                `json:"raw_content,omitempty"` // unedited model output for diagnostics
	MessageID          modulecore.MessageID  `json:"message_id,omitempty"`  // globally unique identity of one logical message
	TurnIndex          int                   `json:"turn_index,omitempty"`  // stable turn order within a session
	TurnID             modulecore.TurnID     `json:"turn_id,omitempty"`     // canonical conversation turn identity
	Category           string                `json:"category,omitempty"`    // domain-specific category (e.g. IdleChat topic category)
	Strategy           string                `json:"strategy,omitempty"`    // domain-specific strategy (e.g. IdleChat topic strategy)
	Route              string                `json:"route,omitempty"`       // routing category
	TaskID             modulecore.TaskID     `json:"task_id,omitempty"`     // durable root or child execution task
	RunID              modulecore.RunID      `json:"run_id,omitempty"`      // owner-issued execution Run; absent before Run issuance
	ActorKind          string                `json:"actor_kind,omitempty"`  // canonical execution actor kind
	ActorID            string                `json:"actor_id,omitempty"`    // actual CORE Agent identity
	TraceID            modulecore.TraceID    `json:"trace_id,omitempty"`    // root interaction correlation identifier
	CausationEventID   modulecore.EventID    `json:"causation_event_id,omitempty"`
	DependencyEventIDs []modulecore.EventID  `json:"dependency_event_ids,omitempty"`
	SessionID          modulecore.SessionID  `json:"session_id,omitempty"`  // session identifier
	ThreadID           modulecore.ThreadID   `json:"thread_id,omitempty"`   // canonical conversation/work thread identifier
	ThreadSeq          modulecore.ThreadSeq  `json:"thread_seq,omitempty"`  // canonical thread sequence within the session
	ThreadKind         modulecore.ThreadKind `json:"thread_kind,omitempty"` // canonical thread kind
	Channel            string                `json:"channel,omitempty"`     // channel identifier
	ChatID             string                `json:"chat_id,omitempty"`     // chat identifier
	Timestamp          string                `json:"timestamp"`
}

// NewEvent creates a new OrchestratorEvent with the current timestamp
func NewEvent(eventType, from, to, content, route, taskID, sessionID, channel, chatID string) OrchestratorEvent {
	return NewEventWithTraceID(modulecore.NewTraceID(), eventType, from, to, content, route, taskID, sessionID, channel, chatID)
}

// NewEventWithTraceID creates an event inside an owner-assigned canonical
// trace. Callers must create the TraceID once at the root interaction boundary.
func NewEventWithTraceID(traceID modulecore.TraceID, eventType, from, to, content, route, taskID, sessionID, channel, chatID string) OrchestratorEvent {
	return OrchestratorEvent{
		EventID:   modulecore.NewEventID(),
		Type:      eventType,
		From:      from,
		To:        to,
		Content:   content,
		Route:     route,
		TaskID:    modulecore.TaskID(taskID),
		TraceID:   traceID,
		SessionID: modulecore.SessionID(sessionID),
		Channel:   channel,
		ChatID:    chatID,
		Timestamp: time.Now().In(jst).Format(time.RFC3339),
	}
}

type eventTraceBindings struct {
	mu     sync.RWMutex
	byTask map[string]modulecore.TraceID
}

func newEventTraceBindings() *eventTraceBindings {
	return &eventTraceBindings{byTask: make(map[string]modulecore.TraceID)}
}

func (b *eventTraceBindings) Bind(taskID string, traceID modulecore.TraceID) {
	if b == nil || strings.TrimSpace(taskID) == "" || traceID.Validate() != nil {
		return
	}
	b.mu.Lock()
	b.byTask[taskID] = traceID
	b.mu.Unlock()
}

func (b *eventTraceBindings) Release(taskID string) {
	if b == nil || strings.TrimSpace(taskID) == "" {
		return
	}
	b.mu.Lock()
	delete(b.byTask, taskID)
	b.mu.Unlock()
}

func (b *eventTraceBindings) Resolve(taskID string) modulecore.TraceID {
	if b != nil && strings.TrimSpace(taskID) != "" {
		b.mu.RLock()
		traceID := b.byTask[taskID]
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

func conversationMessageID() modulecore.MessageID {
	return modulecore.NewMessageID()
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
	sessionID := conversationIdentitySession(string(ev.SessionID), ev.ChatID)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.turns[sessionID]++
	ev.TurnIndex = t.turns[sessionID]
	ev.MessageID = modulecore.MessageID(strings.TrimSpace(preferredMessageID))
	if ev.MessageID == "" && ev.Type == "agent.response" && !ev.TaskID.IsZero() {
		ev.MessageID = modulecore.MessageID(t.boundResponseIDs[ev.TaskID.String()])
		delete(t.boundResponseIDs, ev.TaskID.String())
	}
	if ev.MessageID == "" {
		ev.MessageID = conversationMessageID()
	}
	if ev.Type == "agent.response" && !ev.TaskID.IsZero() {
		t.responseMessageIDs[ev.TaskID.String()] = string(ev.MessageID)
	}
}

func (t *conversationIdentityTracker) BindResponseMessageID(taskID string, messageID modulecore.MessageID) {
	if t == nil || strings.TrimSpace(taskID) == "" || messageID.Validate() != nil {
		return
	}
	t.mu.Lock()
	t.boundResponseIDs[taskID] = string(messageID)
	t.mu.Unlock()
}

func (t *conversationIdentityTracker) ReleaseResponseMessageID(taskID string) {
	if t == nil || strings.TrimSpace(taskID) == "" {
		return
	}
	t.mu.Lock()
	delete(t.boundResponseIDs, taskID)
	delete(t.responseMessageIDs, taskID)
	t.mu.Unlock()
}

func (t *conversationIdentityTracker) TakeResponseMessageID(taskID string) string {
	if t == nil || strings.TrimSpace(taskID) == "" {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	messageID := t.responseMessageIDs[taskID]
	delete(t.responseMessageIDs, taskID)
	return messageID
}
