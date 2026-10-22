package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/conversationruntime"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestEventRelayPublishesPrecommittedBundleOnceWithoutAppending(t *testing.T) {
	store := newResponseFirstEventStore()
	historicalAudio := responseFirstEnvelope(t, "tts.audio_chunk", "old audio")
	persistResponseFirst(t, store, historicalAudio)
	first := responseFirstEnvelope(t, "agent.response", "visible reply")
	private := modulecore.NewEventEnvelope(first.TraceID, first.EventID, nil, "delegation_mailbox", "conversation.delegation_instruction", time.Now().UTC(), map[string]any{"text": "private instruction"})
	private.SessionID, private.TurnID = first.SessionID, first.TurnID
	second := responseFirstEnvelopeWithIDs(t, "agent.response", "second reply", first.TraceID, first.SessionID, first.TurnID)
	committed := persistResponseFirst(t, store, first, private, second)

	hub := viewer.NewEventHub(8)
	marker := &responseFirstMarker{}
	relay := responseFirstRelay(t, store, hub, marker)
	precommittedAppendCalls := store.appendCount()

	if err := publishResponseFirst(t, relay, committed); err != nil {
		t.Fatalf("PublishCommitted() error = %v", err)
	}
	if err := publishResponseFirst(t, relay, committed); err != nil {
		t.Fatalf("recovery PublishCommitted() error = %v", err)
	}

	history := hub.History()
	if len(history) != 2 {
		t.Fatalf("published events=%d, want two public orchestrator events: %+v", len(history), history)
	}
	if history[0].EventID != first.EventID || history[0].EventSeq != 2 || history[1].EventID != second.EventID || history[1].EventSeq != 4 {
		t.Fatalf("public history=%+v, want public events at canonical sequences 2 and 4", history)
	}
	if got := store.appendCount(); got != precommittedAppendCalls {
		t.Fatalf("append calls after publication=%d, want unchanged precommitted count %d", got, precommittedAppendCalls)
	}
	if marker.calls != 0 {
		t.Fatalf("v2 response publication marker calls=%d, want zero because the committed response event proves delivery", marker.calls)
	}
}

func TestEventRelayPublishesAcceptedInputBeforeCommittedResponse(t *testing.T) {
	store := newResponseFirstEventStore()
	input := responseFirstEnvelope(t, "conversation.user_message", "accepted input")
	committedInput := persistResponseFirst(t, store, input).Events[0]
	hub := viewer.NewEventHub(8)
	relay := responseFirstRelay(t, store, hub, nil)
	acceptedAppendCalls := store.appendCount()

	if err := publishAcceptedResponseFirst(t, relay, committedInput); err != nil {
		t.Fatalf("PublishAcceptedInput() error = %v", err)
	}
	if got := hub.History(); len(got) != 1 || got[0].EventID != committedInput.EventID || got[0].EventSeq != committedInput.EventSeq {
		t.Fatalf("accepted history=%+v, want the persisted input once", got)
	}

	response := responseFirstEnvelopeWithIDs(t, "agent.response", "committed response", input.TraceID, input.SessionID, input.TurnID)
	committedResponse := persistResponseFirst(t, store, response)
	if err := publishResponseFirst(t, relay, committedResponse); err != nil {
		t.Fatalf("PublishCommitted() error = %v", err)
	}
	if err := publishAcceptedResponseFirst(t, relay, committedInput); err != nil {
		t.Fatalf("recovery PublishAcceptedInput() error = %v", err)
	}

	history := hub.History()
	if len(history) != 2 || history[0].EventSeq != 1 || history[1].EventSeq != 2 || history[0].Content != "accepted input" || history[1].Content != "committed response" {
		t.Fatalf("accepted input and response are not published once in sequence order: %+v", history)
	}
	if got := store.appendCount(); got != acceptedAppendCalls+1 {
		t.Fatalf("append calls after accepted and committed publication=%d, want one additional persistence append for the response only", got)
	}
}

func TestEventRelaySerializesLegacyAndCommittedPublicationByEventSeq(t *testing.T) {
	store := newResponseFirstEventStore()
	store.blockNextAppend = true
	store.appendStarted = make(chan struct{})
	store.appendRelease = make(chan struct{})
	hub := viewer.NewEventHub(8)
	relay := responseFirstRelay(t, store, hub, nil)

	legacy := orchestrator.NewEvent("message.received", "user", "mio", "legacy input", "CHAT", modulecore.NewTaskID().String(), string(modulecore.NewSessionID()), "viewer", "chat-1")
	legacyDone := make(chan error, 1)
	go func() { legacyDone <- relay.OnEvent(legacy) }()
	select {
	case <-store.appendStarted:
	case <-time.After(time.Second):
		t.Fatal("legacy append did not reach the publication barrier")
	}

	committedEvent := responseFirstEnvelope(t, "agent.response", "committed reply")
	committed := persistResponseFirst(t, store, committedEvent)
	publishStarted := make(chan struct{})
	publishDone := make(chan error, 1)
	go func() {
		close(publishStarted)
		publishDone <- publishResponseFirst(t, relay, committed)
	}()
	select {
	case <-publishStarted:
	case <-time.After(time.Second):
		t.Fatal("committed publication did not start")
	}

	close(store.appendRelease)
	if err := <-legacyDone; err != nil {
		t.Fatalf("legacy OnEvent() error = %v", err)
	}
	if err := <-publishDone; err != nil {
		t.Fatalf("PublishCommitted() error = %v", err)
	}
	if err := publishResponseFirst(t, relay, committed); err != nil {
		t.Fatalf("recovery PublishCommitted() error = %v", err)
	}

	history := hub.History()
	if len(history) != 2 {
		t.Fatalf("published events=%d, want two after commit recovery: %+v", len(history), history)
	}
	if history[0].EventSeq != 1 || history[1].EventSeq != 2 || history[0].Content != "legacy input" || history[1].Content != "committed reply" {
		t.Fatalf("public events are not in canonical sequence order: %+v", history)
	}
}

func TestEventRelayReplayErrorKeepsCursorForRetry(t *testing.T) {
	store := newResponseFirstEventStore()
	committedEvent := responseFirstEnvelope(t, "agent.response", "retry reply")
	committed := persistResponseFirst(t, store, committedEvent)
	store.failNextRead = storeReplayError
	hub := viewer.NewEventHub(8)
	relay := responseFirstRelay(t, store, hub, nil)

	if err := publishResponseFirst(t, relay, committed); !errors.Is(err, storeReplayError) {
		t.Fatalf("first PublishCommitted() error = %v, want replay error", err)
	}
	if relay.publicationCursor != 0 {
		t.Fatalf("cursor after failed replay=%d, want unchanged zero", relay.publicationCursor)
	}
	if got := len(hub.History()); got != 0 {
		t.Fatalf("history after failed replay=%d, want zero", got)
	}

	if err := publishResponseFirst(t, relay, committed); err != nil {
		t.Fatalf("retry PublishCommitted() error = %v", err)
	}
	if err := publishResponseFirst(t, relay, committed); err != nil {
		t.Fatalf("recovery PublishCommitted() error = %v", err)
	}
	if relay.publicationCursor != 1 {
		t.Fatalf("cursor after successful retry=%d, want one", relay.publicationCursor)
	}
	if got := len(hub.History()); got != 1 {
		t.Fatalf("history after retry and recovery=%d, want one", got)
	}
}

var storeReplayError = errors.New("simulated replay error")

type responseFirstEventStore struct {
	mu sync.Mutex

	events          []modulecore.EventEnvelope
	appendCalls     int
	failNextRead    error
	blockNextAppend bool
	appendStarted   chan struct{}
	appendRelease   chan struct{}
}

func newResponseFirstEventStore() *responseFirstEventStore {
	return &responseFirstEventStore{}
}

func (s *responseFirstEventStore) Append(ctx context.Context, event modulecore.EventEnvelope) error {
	_, err := s.AppendSequenced(ctx, event)
	return err
}

func (s *responseFirstEventStore) AppendSequenced(ctx context.Context, event modulecore.EventEnvelope) (modulecore.EventEnvelope, error) {
	if ctx == nil {
		return modulecore.EventEnvelope{}, errors.New("append context is required")
	}
	if err := ctx.Err(); err != nil {
		return modulecore.EventEnvelope{}, err
	}
	s.mu.Lock()
	s.appendCalls++
	var latest modulecore.EventSeq
	for _, existing := range s.events {
		if existing.EventSeq > latest {
			latest = existing.EventSeq
		}
	}
	event.EventSeq = latest + 1
	s.events = append(s.events, event)
	block := s.blockNextAppend
	s.blockNextAppend = false
	started, release := s.appendStarted, s.appendRelease
	s.mu.Unlock()
	if block {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return modulecore.EventEnvelope{}, ctx.Err()
		}
	}
	return event, nil
}

func (s *responseFirstEventStore) GetByID(ctx context.Context, eventID modulecore.EventID) (modulecore.EventEnvelope, bool, error) {
	if ctx == nil {
		return modulecore.EventEnvelope{}, false, errors.New("read context is required")
	}
	if err := ctx.Err(); err != nil {
		return modulecore.EventEnvelope{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, event := range s.events {
		if event.EventID == eventID {
			return event, true, nil
		}
	}
	return modulecore.EventEnvelope{}, false, nil
}

func (s *responseFirstEventStore) ListByComponent(ctx context.Context, component string, limit int) ([]modulecore.EventEnvelope, error) {
	if ctx == nil {
		return nil, errors.New("read context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]modulecore.EventEnvelope, 0)
	for _, event := range s.events {
		if event.ComponentID == component {
			items = append(items, event)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].EventSeq > items[j].EventSeq })
	if limit >= 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *responseFirstEventStore) ReadComponentPage(ctx context.Context, component string, after, through modulecore.EventSeq, limit int) ([]modulecore.EventEnvelope, modulecore.EventSeq, error) {
	if ctx == nil {
		return nil, 0, errors.New("replay context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	if after < 0 || through < 0 || limit <= 0 {
		return nil, 0, errors.New("invalid replay query")
	}
	s.mu.Lock()
	if s.failNextRead != nil {
		err := s.failNextRead
		s.failNextRead = nil
		s.mu.Unlock()
		return nil, 0, err
	}
	defer s.mu.Unlock()
	var latest modulecore.EventSeq
	for _, event := range s.events {
		if event.ComponentID == component && event.EventSeq > latest {
			latest = event.EventSeq
		}
	}
	if through == 0 {
		through = latest
	}
	if after > through || through > latest {
		return nil, 0, errors.New("replay cursor is outside the component window")
	}
	page := make([]modulecore.EventEnvelope, 0, limit)
	for _, event := range s.events {
		if event.ComponentID == component && event.EventSeq > after && event.EventSeq <= through {
			page = append(page, event)
		}
	}
	sort.Slice(page, func(i, j int) bool { return page[i].EventSeq < page[j].EventSeq })
	if len(page) > limit {
		page = page[:limit]
	}
	return page, through, nil
}

func (s *responseFirstEventStore) appendCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendCalls
}

type responseFirstMarker struct {
	calls int
}

func (m *responseFirstMarker) MarkConversationTurnResponsePublished(context.Context, modulecore.TurnID, modulecore.TraceID, modulecore.SessionID, time.Time) error {
	m.calls++
	return nil
}

func responseFirstRelay(t *testing.T, store *responseFirstEventStore, hub *viewer.EventHub, marker *responseFirstMarker) *idleAwareEventListener {
	t.Helper()
	archive, err := viewer.NewCanonicalEventLog(store)
	if err != nil {
		t.Fatalf("NewCanonicalEventLog() error = %v", err)
	}
	relay := &idleAwareEventListener{hub: hub, archive: archive}
	if marker != nil {
		relay.responseMarker = marker
	}
	return relay
}

func responseFirstEnvelope(t *testing.T, eventType, content string) modulecore.EventEnvelope {
	t.Helper()
	return responseFirstEnvelopeWithIDs(t, eventType, content, modulecore.NewTraceID(), modulecore.NewSessionID(), modulecore.NewTurnID())
}

func responseFirstEnvelopeWithIDs(t *testing.T, eventType, content string, traceID modulecore.TraceID, sessionID modulecore.SessionID, turnID modulecore.TurnID) modulecore.EventEnvelope {
	t.Helper()
	event := orchestrator.NewEventWithTraceID(traceID, eventType, "mio", "user", content, "CHAT", "", string(sessionID), "viewer", "chat-1")
	event.TurnID = turnID
	event.MessageID = modulecore.NewMessageID()
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal orchestrator event: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("decode orchestrator event payload: %v", err)
	}
	envelope := modulecore.NewEventEnvelope(traceID, "", nil, "orchestrator", eventType, time.Now().UTC(), payload)
	envelope.EventID = event.EventID
	envelope.SessionID, envelope.TurnID, envelope.MessageID = sessionID, turnID, event.MessageID
	return envelope
}

func persistResponseFirst(t *testing.T, store *responseFirstEventStore, events ...modulecore.EventEnvelope) conversationruntime.CommittedBundle {
	t.Helper()
	bundle := conversationruntime.CommittedBundle{}
	for _, event := range events {
		persisted, err := store.AppendSequenced(context.Background(), event)
		if err != nil {
			t.Fatalf("persist committed event: %v", err)
		}
		bundle.Events = append(bundle.Events, persisted)
	}
	return bundle
}

func publishResponseFirst(t *testing.T, relay *idleAwareEventListener, bundle conversationruntime.CommittedBundle) error {
	t.Helper()
	publisher, ok := any(relay).(interface {
		PublishCommitted(context.Context, conversationruntime.CommittedBundle) error
	})
	if !ok {
		return fmt.Errorf("idleAwareEventListener does not implement committed publication")
	}
	return publisher.PublishCommitted(context.Background(), bundle)
}

func publishAcceptedResponseFirst(t *testing.T, relay *idleAwareEventListener, event modulecore.EventEnvelope) error {
	t.Helper()
	publisher, ok := any(relay).(interface {
		PublishAcceptedInput(context.Context, modulecore.EventEnvelope) error
	})
	if !ok {
		return fmt.Errorf("idleAwareEventListener does not implement accepted input publication")
	}
	return publisher.PublishAcceptedInput(context.Background(), event)
}
