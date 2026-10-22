package viewer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestHandleSSE_UsesLastEventIDForHistoryReplay(t *testing.T) {
	hub := NewEventHub(10)
	taskID := modulecore.NewTaskID().String()
	first := orchestrator.NewEvent("entry.stage", "chrome", "system", "received", "CHAT", taskID, "s1", "local", "u1")
	first.EventSeq = 1
	second := orchestrator.NewEvent("entry.stage", "chrome", "system", "planning", "CHAT", taskID, "s1", "local", "u1")
	second.EventSeq = 2
	hub.OnEvent(first)
	hub.OnEvent(second)

	req := httptest.NewRequest(http.MethodGet, "/viewer/events", nil)
	req.Header.Set("Last-Event-ID", "1")
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	rec := &cancelAfterFlushWriter{
		ResponseRecorder: httptest.NewRecorder(),
		cancel:           cancel,
		cancelOn:         1,
	}
	req = req.WithContext(ctx)

	hub.HandleSSE(rec, req)
	body := rec.Body.String()
	if strings.Contains(body, `"event_seq":1`) {
		t.Fatalf("expected event_seq=1 to be skipped, got: %s", body)
	}
	if !strings.Contains(body, `"event_seq":2`) {
		t.Fatalf("expected event_seq=2 in replay, got: %s", body)
	}
}

func TestHandleSSE_SendsHeartbeatComment(t *testing.T) {
	prev := sseHeartbeatInterval
	sseHeartbeatInterval = time.Millisecond
	defer func() { sseHeartbeatInterval = prev }()

	hub := NewEventHub(10)
	req := httptest.NewRequest(http.MethodGet, "/viewer/events", nil)
	ctx, cancel := context.WithTimeout(req.Context(), time.Second)
	defer cancel()
	rec := &cancelAfterFlushWriter{
		ResponseRecorder: httptest.NewRecorder(),
		cancel:           cancel,
		cancelOn:         2,
	}
	req = req.WithContext(ctx)

	hub.HandleSSE(rec, req)
	if !strings.Contains(rec.Body.String(), ": heartbeat") {
		t.Fatalf("expected SSE heartbeat comment, got: %s", rec.Body.String())
	}
}

func TestEventHubReportsClientCountChanges(t *testing.T) {
	hub := NewEventHub(10)
	var counts []int
	hub.SetClientCountListener(func(count int) {
		counts = append(counts, count)
	})

	first, _ := hub.Subscribe()
	second, _ := hub.Subscribe()
	if got := hub.ClientCount(); got != 2 {
		t.Fatalf("client count = %d, want 2", got)
	}
	hub.Unsubscribe(first)
	hub.Unsubscribe(first)
	hub.Unsubscribe(second)
	hub.Unsubscribe(second)
	if got := hub.ClientCount(); got != 0 {
		t.Fatalf("client count = %d, want 0", got)
	}
	if got, want := strings.Join(intsToStrings(counts), ","), "1,2,1,0"; got != want {
		t.Fatalf("client count notifications = %s, want %s", got, want)
	}
}

type cancelAfterFlushWriter struct {
	*httptest.ResponseRecorder
	cancel   context.CancelFunc
	flushes  int
	cancelOn int
}

func (w *cancelAfterFlushWriter) Flush() {
	w.flushes++
	w.ResponseRecorder.Flush()
	if w.flushes == w.cancelOn {
		w.cancel()
	}
}

func TestHandleSSE_DoesNotDuplicateEventBetweenSnapshotAndLive(t *testing.T) {
	hub := NewEventHub(10)
	event := orchestrator.NewEvent("entry.stage", "chrome", "system", "snapshot race", "CHAT", modulecore.NewTaskID().String(), "s1", "local", "u1")
	event.EventSeq = 1
	hub.SetClientCountListener(func(count int) {
		if count == 1 {
			hub.OnEvent(event)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/viewer/events", nil).WithContext(ctx)
	rec := &cancelAfterFlushWriter{
		ResponseRecorder: httptest.NewRecorder(),
		cancel:           cancel,
		cancelOn:         2,
	}

	hub.HandleSSE(rec, req)
	idLine := "id: " + strconv.FormatInt(int64(event.EventSeq), 10) + "\n"
	if got := strings.Count(rec.Body.String(), idLine); got != 1 {
		t.Fatalf("event_seq %d appeared %d times, want once: %s", event.EventSeq, got, rec.Body.String())
	}
	if got := hub.ClientCount(); got != 0 {
		t.Fatalf("client count after disconnect = %d, want 0", got)
	}
}

func TestEventHubPublishLiveGuardsMonotonicSequenceAndDeduplicatesLatest(t *testing.T) {
	hub := NewEventHub(10)
	first := orchestrator.NewEvent("agent.response", "mio", "user", "first", "CHAT", "", "", "viewer", "user")
	first.EventSeq = 1
	if published, err := hub.PublishLive(first); err != nil || !published {
		t.Fatalf("PublishLive(first) = (%t, %v), want (true, nil)", published, err)
	}
	if published, err := hub.PublishLive(first); err != nil || published {
		t.Fatalf("PublishLive(retry) = (%t, %v), want (false, nil) for exact retry", published, err)
	}

	third := orchestrator.NewEvent("agent.response", "mio", "user", "third", "CHAT", "", "", "viewer", "user")
	third.EventSeq = 3 // Component sequences may have gaps occupied by private events.
	if published, err := hub.PublishLive(third); err != nil || !published {
		t.Fatalf("PublishLive(sequence gap) = (%t, %v), want (true, nil)", published, err)
	}
	second := orchestrator.NewEvent("agent.response", "mio", "user", "second", "CHAT", "", "", "viewer", "user")
	second.EventSeq = 2
	if published, err := hub.PublishLive(second); published || !errors.Is(err, errEventHubLiveSequenceNotIncreasing) {
		t.Fatalf("PublishLive(out of order) = (%t, %v), want monotonic sequence error", published, err)
	}

	history := hub.History()
	if len(history) != 2 || history[0].EventSeq != 1 || history[1].EventSeq != 3 {
		t.Fatalf("history after duplicate and out-of-order publish=%+v, want sequences 1 and 3", history)
	}
}

func TestHandleSSEAdvancesLiveCursorOnlyAfterSuccessfulFrame(t *testing.T) {
	hub := NewEventHub(10)
	first := sequencedEvent("entry.stage", "first live", 1)
	second := sequencedEvent("entry.stage", "second live", 2)
	firstData, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal first live event: %v", err)
	}
	secondData, err := json.Marshal(second)
	if err != nil {
		t.Fatalf("marshal second live event: %v", err)
	}
	hub.SetClientCountListener(func(count int) {
		if count != 1 {
			return
		}
		hub.mu.Lock()
		defer hub.mu.Unlock()
		for ch := range hub.clients {
			ch <- firstData
			ch <- firstData
			ch <- secondData
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/viewer/events?from=now", nil).WithContext(ctx)
	rec := &cancelAfterFlushWriter{ResponseRecorder: httptest.NewRecorder(), cancel: cancel, cancelOn: 3}
	hub.HandleSSE(rec, req)

	body := rec.Body.String()
	if got := strings.Count(body, "id: 1\n"); got != 1 {
		t.Fatalf("live EventSeq 1 appeared %d times after a successful frame, want once: %q", got, body)
	}
	if got := strings.Count(body, "id: 2\n"); got != 1 {
		t.Fatalf("live EventSeq 2 appeared %d times, want once: %q", got, body)
	}
}

func TestHandleSSE_DoesNotReplayTransientTTSAudioHistory(t *testing.T) {
	hub := NewEventHub(10)
	hub.OnEvent(orchestrator.NewEvent("tts.audio_chunk", "tts", "user", `{"session_id":"s1","chunk_index":0,"character_id":"mio","audio_url":"http://example/audio.wav"}`, "TTS", "", "s1", "viewer", "viewer-user"))
	hub.OnEvent(orchestrator.NewEvent("tts.session_completed", "tts", "user", `{"session_id":"s1","character_id":"mio"}`, "TTS", "", "s1", "viewer", "viewer-user"))
	hub.OnEvent(orchestrator.NewEvent("agent.response", "mio", "user", "visible response", "CHAT", "j1", "s1", "viewer", "viewer-user"))

	req := httptest.NewRequest(http.MethodGet, "/viewer/events", nil)
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	rec := &cancelAfterFlushWriter{
		ResponseRecorder: httptest.NewRecorder(),
		cancel:           cancel,
		cancelOn:         1,
	}
	req = req.WithContext(ctx)

	hub.HandleSSE(rec, req)
	body := rec.Body.String()
	if strings.Contains(body, "tts.audio_chunk") || strings.Contains(body, "tts.session_completed") {
		t.Fatalf("transient TTS audio events must not be replayed from history: %s", body)
	}
	if !strings.Contains(body, "visible response") {
		t.Fatalf("non-transient history should still replay, got: %s", body)
	}
}

func intsToStrings(values []int) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, strconv.Itoa(value))
	}
	return out
}

func TestHandleSSE_DoesNotReplayIdleChatLiveHistory(t *testing.T) {
	hub := NewEventHub(10)
	hub.OnEvent(orchestrator.NewEvent("idlechat.message", "mio", "shiro", "old idle speech", "IDLECHAT", "", "idle-old", "idlechat", "idle-old"))
	hub.OnEvent(orchestrator.NewEvent("idlechat.summary", "shiro", "user", "old idle summary", "IDLECHAT", "", "idle-old", "idlechat", "idle-old"))
	hub.OnEvent(orchestrator.NewEvent("agent.response", "mio", "user", "visible response", "CHAT", "j1", "s1", "viewer", "viewer-user"))

	req := httptest.NewRequest(http.MethodGet, "/viewer/events", nil)
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	rec := &cancelAfterFlushWriter{
		ResponseRecorder: httptest.NewRecorder(),
		cancel:           cancel,
		cancelOn:         1,
	}
	req = req.WithContext(ctx)

	hub.HandleSSE(rec, req)
	body := rec.Body.String()
	if strings.Contains(body, "old idle speech") || strings.Contains(body, "old idle summary") {
		t.Fatalf("idlechat live events must not be replayed from SSE history: %s", body)
	}
	if !strings.Contains(body, "visible response") {
		t.Fatalf("non-idle history should still replay, got: %s", body)
	}
}

func TestHandleAudioRouterSSE_FiltersAndStreamsOnlyLiveAudioChunks(t *testing.T) {
	hub := NewEventHub(10)
	hub.OnEvent(orchestrator.NewEvent("entry.stage", "chrome", "system", "received", "CHAT", "j1", "s1", "local", "u1"))
	hub.OnEvent(orchestrator.NewEvent("tts.audio_chunk", "tts", "user", `{"session_id":"s1","chunk_index":0,"character_id":"mio","audio_url":"http://example/audio.wav"}`, "TTS", "", "s1", "viewer", "viewer-user"))

	req := httptest.NewRequest(http.MethodGet, "/audio-router/events", nil)
	req.Header.Set("Last-Event-ID", "0")
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	rec := &cancelAfterFlushWriter{
		ResponseRecorder: httptest.NewRecorder(),
		cancel:           cancel,
		cancelOn:         1,
	}
	req = req.WithContext(ctx)

	HandleAudioRouterSSE(hub)(rec, req)
	body := rec.Body.String()
	if strings.Contains(body, `"eventType":"entry.stage"`) || strings.Contains(body, "received") {
		t.Fatalf("unexpected non-audio event in stream: %s", body)
	}
	if strings.Contains(body, `"character_id":"mio"`) || strings.Contains(body, "event: tts.audio_chunk") {
		t.Fatalf("historical audio chunks must not replay into audio router stream: %s", body)
	}
}

type replayPageReader struct {
	events          []orchestrator.OrchestratorEvent
	through         modulecore.EventSeq
	pageSize        int
	emptyAfterFirst bool
	badPage         bool
	calls           int
}

func (r *replayPageReader) ReplayPage(ctx context.Context, after, through modulecore.EventSeq, limit int) ([]orchestrator.OrchestratorEvent, modulecore.EventSeq, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	r.calls++
	if limit != replayPageLimit {
		return nil, 0, errors.New("unexpected replay page limit")
	}
	if through == 0 {
		through = r.through
		if through == 0 {
			for _, event := range r.events {
				if event.EventSeq > through {
					through = event.EventSeq
				}
			}
		}
	}
	if r.emptyAfterFirst && r.calls > 1 {
		return nil, through, nil
	}
	if r.badPage {
		return []orchestrator.OrchestratorEvent{{EventSeq: after}}, through, nil
	}
	page := make([]orchestrator.OrchestratorEvent, 0, len(r.events))
	for _, event := range r.events {
		if event.EventSeq > after && event.EventSeq <= through {
			page = append(page, event)
		}
	}
	if r.pageSize > 0 && len(page) > r.pageSize {
		page = page[:r.pageSize]
	}
	if len(page) > limit {
		page = page[:limit]
	}
	return page, through, nil
}

func replayTestEvents(count int) []orchestrator.OrchestratorEvent {
	events := make([]orchestrator.OrchestratorEvent, 0, count)
	for index := 1; index <= count; index++ {
		event := orchestrator.NewEvent("entry.stage", "chrome", "system", "event-"+strconv.Itoa(index), "CHAT", modulecore.NewTaskID().String(), "s1", "local", "u1")
		event.EventSeq = modulecore.EventSeq(index)
		events = append(events, event)
	}
	return events
}

func TestHandleSSE_ReplaysDurableHistoryBeyondHubCapacity(t *testing.T) {
	events := replayTestEvents(5)
	hub := NewEventHub(2)
	reader := &replayPageReader{events: events}
	hub.SetReplayReader(reader)
	for _, event := range events {
		hub.OnEvent(event)
	}

	req := httptest.NewRequest(http.MethodGet, "/viewer/events", nil)
	req.Header.Set("Last-Event-ID", "1")
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	req = req.WithContext(ctx)
	rec := &cancelAfterFlushWriter{ResponseRecorder: httptest.NewRecorder(), cancel: cancel, cancelOn: 1}
	hub.HandleSSE(rec, req)
	body := rec.Body.String()
	for sequence := 2; sequence <= 5; sequence++ {
		idLine := "id: " + strconv.Itoa(sequence) + "\n"
		if got := strings.Count(body, idLine); got != 1 {
			t.Fatalf("event_seq %d appeared %d times, want once: %s", sequence, got, body)
		}
	}
	if reader.calls == 0 {
		t.Fatal("durable replay reader was not called")
	}
}

func TestHandleSSE_DeduplicatesDurableReplayAndLiveOverlap(t *testing.T) {
	events := replayTestEvents(3)
	hub := NewEventHub(2)
	hub.OnEvent(events[0])
	hub.OnEvent(events[1])
	hub.SetReplayReader(&replayPageReader{events: events})
	hub.SetClientCountListener(func(count int) {
		if count == 1 {
			hub.OnEvent(events[2])
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/viewer/events", nil)
	req.Header.Set("Last-Event-ID", "1")
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	req = req.WithContext(ctx)
	rec := &cancelAfterFlushWriter{ResponseRecorder: httptest.NewRecorder(), cancel: cancel, cancelOn: 1}
	hub.HandleSSE(rec, req)
	body := rec.Body.String()
	for sequence := 2; sequence <= 3; sequence++ {
		idLine := "id: " + strconv.Itoa(sequence) + "\n"
		if got := strings.Count(body, idLine); got != 1 {
			t.Fatalf("overlap event_seq %d appeared %d times, want once: %s", sequence, got, body)
		}
	}
}

func TestEventHubSlowSubscriberIsClosedAndCanReconnect(t *testing.T) {
	events := replayTestEvents(65)
	hub := NewEventHub(2)
	hub.SetReplayReader(&replayPageReader{events: events})
	stalled, _ := hub.Subscribe()
	for _, event := range events {
		hub.OnEvent(event)
	}
	if got := hub.ClientCount(); got != 0 {
		t.Fatalf("slow subscriber client count = %d, want 0", got)
	}
	for {
		if _, ok := <-stalled; !ok {
			break
		}
	}

	reconnected, snapshot := hub.Subscribe()
	defer hub.Unsubscribe(reconnected)
	var replayed []modulecore.EventSeq
	cursor, err := hub.Replay(context.Background(), 1, snapshot, func(event orchestrator.OrchestratorEvent) error {
		replayed = append(replayed, event.EventSeq)
		return nil
	})
	if err != nil {
		t.Fatalf("reconnect Replay() error = %v", err)
	}
	want := make([]modulecore.EventSeq, 0, len(events)-1)
	for sequence := 2; sequence <= len(events); sequence++ {
		want = append(want, modulecore.EventSeq(sequence))
	}
	if cursor != modulecore.EventSeq(len(events)) || !reflect.DeepEqual(replayed, want) {
		t.Fatalf("reconnect replay count=%d cursor=%d, want count=%d cursor=%d", len(replayed), cursor, len(want), len(events))
	}
}

func TestEventHubReplayRejectsMissingFinalPageAndCancelledContext(t *testing.T) {
	events := replayTestEvents(2)
	hub := NewEventHub(2)
	hub.SetReplayReader(&replayPageReader{events: events, through: 3, emptyAfterFirst: true})
	if _, err := hub.Replay(context.Background(), 1, nil, func(orchestrator.OrchestratorEvent) error { return nil }); err == nil {
		t.Fatal("Replay() error = nil when reader ends before fixed watermark")
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := hub.Replay(cancelled, 1, nil, func(orchestrator.OrchestratorEvent) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Replay() error = %v, want context.Canceled", err)
	}
}

func TestEventHubReplayDoesNotAdvanceCursorOnEmitError(t *testing.T) {
	hub := NewEventHub(2)
	events := replayTestEvents(2)
	cursor, err := hub.Replay(context.Background(), 0, events, func(orchestrator.OrchestratorEvent) error {
		return errors.New("writer failed")
	})
	if err == nil || cursor != 0 {
		t.Fatalf("Replay() cursor=%d error=%v, want cursor 0 and writer error", cursor, err)
	}
}

func TestHandleSSERejectsInvalidLastEventID(t *testing.T) {
	hub := NewEventHub(2)
	for _, value := range []string{"not-a-seq", "-1"} {
		t.Run(value, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/viewer/events", nil)
			req.Header.Set("Last-Event-ID", value)
			rec := httptest.NewRecorder()
			hub.HandleSSE(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("Last-Event-ID %q status = %d, want 400", value, rec.Code)
			}
			if hub.ClientCount() != 0 {
				t.Fatalf("invalid cursor %q subscribed a client", value)
			}
		})
	}
}

func TestHandleSSE_HidesResponseFirstMessagesFromGlobalStreamReplayAndLive(t *testing.T) {
	newV2Event := func(seq int64, visibility string) orchestrator.OrchestratorEvent {
		event := orchestrator.NewEvent("agent.response", "mio", "user", "v2-private-to-global-stream", "CHAT", "", "v2-session", "viewer", "viewer-user")
		event.EventSeq = modulecore.EventSeq(seq)
		event.SessionID = modulecore.NewSessionID()
		event.MessageID = modulecore.NewMessageID()
		event.TurnID = modulecore.NewTurnID()
		event.Kind = "reply"
		event.Visibility = visibility
		return event
	}

	for _, visibility := range []string{"public", "private"} {
		for _, delivery := range []string{"replay", "live"} {
			t.Run(delivery+"/"+visibility, func(t *testing.T) {
				hub := NewEventHub(10)
				if delivery == "replay" {
					hub.OnEvent(newV2Event(1, visibility))
					req := httptest.NewRequest(http.MethodGet, "/viewer/events", nil)
					ctx, cancel := context.WithCancel(req.Context())
					defer cancel()
					rec := &cancelAfterFlushWriter{ResponseRecorder: httptest.NewRecorder(), cancel: cancel, cancelOn: 1}
					hub.HandleSSE(rec, req.WithContext(ctx))
					if strings.Contains(rec.Body.String(), "v2-private-to-global-stream") {
						t.Fatalf("global replay exposed a %s response-first message: %s", visibility, rec.Body.String())
					}
					return
				}
				ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
				defer cancel()
				hub.SetClientCountListener(func(count int) {
					if count == 1 {
						hub.OnEvent(newV2Event(1, visibility))
					}
				})
				req := httptest.NewRequest(http.MethodGet, "/viewer/events?from=now", nil).WithContext(ctx)
				rec := httptest.NewRecorder()
				hub.HandleSSE(rec, req)
				if strings.Contains(rec.Body.String(), "v2-private-to-global-stream") {
					t.Fatalf("global live stream exposed a %s response-first message: %s", visibility, rec.Body.String())
				}
			})
		}
	}
}

func TestHandleSessionPublicSSEReplaysCommittedMessagesOnceAfterRestart(t *testing.T) {
	fixture := seedSessionPublicReplayFixture(t)
	store, err := eventstore.NewSQLiteStore(fixture.path)
	if err != nil {
		t.Fatalf("reopen EventStore: %v", err)
	}
	defer store.Close()
	archive, err := NewCanonicalEventLog(store)
	if err != nil {
		t.Fatalf("NewCanonicalEventLog after restart: %v", err)
	}
	hub := NewEventHub(10)
	hub.SetSessionPublicReplayReader(archive)

	request := httptest.NewRequest(http.MethodGet, "/viewer/conversation/events", nil)
	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	request = request.WithContext(ctx)
	response := &cancelAfterFlushWriter{ResponseRecorder: httptest.NewRecorder(), cancel: cancel, cancelOn: 1}
	hub.HandleSessionPublicSSE(response, request, fixture.scope)

	body := response.Body.String()
	for _, messageID := range []modulecore.MessageID{fixture.inputMessageID, fixture.reply.MessageID, fixture.clarification.MessageID} {
		if got := strings.Count(body, `"message_id":"`+string(messageID)+`"`); got != 1 {
			t.Errorf("message %s appeared %d times in restart replay, want once: %s", messageID, got, body)
		}
	}
	for _, event := range []modulecore.EventEnvelope{fixture.reply, fixture.clarification} {
		if got := strings.Count(body, "id: "+strconv.FormatInt(int64(event.EventSeq), 10)+"\n"); got != 1 {
			t.Errorf("event sequence %d appeared %d times, want once", event.EventSeq, got)
		}
	}
	if strings.Contains(body, "PRIVATE_SENTINEL") || strings.Contains(body, "other session") {
		t.Fatalf("owner-scoped stream exposed private or other-session content: %s", body)
	}
	if got := hub.ClientCount(); got != 0 {
		t.Fatalf("subscriber count after disconnect=%d, want 0", got)
	}
}

func TestHandleSessionPublicSSEAuthorizesLiveEventsAgainstPublicIndex(t *testing.T) {
	fixture := seedSessionPublicReplayFixture(t)
	store, err := eventstore.NewSQLiteStore(fixture.path)
	if err != nil {
		t.Fatalf("reopen EventStore: %v", err)
	}
	defer store.Close()
	archive, err := NewCanonicalEventLog(store)
	if err != nil {
		t.Fatalf("NewCanonicalEventLog: %v", err)
	}
	publicEvent, found, err := archive.GetByEventID(context.Background(), fixture.reply.EventID)
	if err != nil || !found {
		t.Fatalf("load public live event found=%t err=%v", found, err)
	}
	privateEvent := orchestrator.NewEvent("conversation.clarification_question", "mio", "shiro", "PRIVATE_SENTINEL", "CHAT", "", string(fixture.scope.SessionID), "viewer", "viewer-user")
	privateEvent.EventID, privateEvent.EventSeq = fixture.private.EventID, fixture.private.EventSeq
	privateEvent.SessionID, privateEvent.TurnID, privateEvent.MessageID = fixture.scope.SessionID, fixture.private.TurnID, fixture.private.MessageID
	privateEvent.Visibility, privateEvent.Kind = "private", "clarification_question"

	hub := NewEventHub(10)
	hub.SetSessionPublicReplayReader(archive)
	hub.SetClientCountListener(func(count int) {
		if count == 1 {
			hub.OnEvent(publicEvent)
			hub.OnEvent(privateEvent)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/viewer/conversation/events?from=now", nil).WithContext(ctx)
	response := httptest.NewRecorder()
	hub.HandleSessionPublicSSE(response, request, fixture.scope)
	body := response.Body.String()
	if !strings.Contains(body, "public answer") {
		t.Fatalf("owner-scoped live stream omitted indexed public message: %s", body)
	}
	if strings.Contains(body, "PRIVATE_SENTINEL") {
		t.Fatalf("owner-scoped live stream exposed a private event: %s", body)
	}
}

type sessionPublicReplayFixture struct {
	path           string
	scope          SessionPublicReplayScope
	inputMessageID modulecore.MessageID
	reply          modulecore.EventEnvelope
	clarification  modulecore.EventEnvelope
	private        modulecore.EventEnvelope
}

func seedSessionPublicReplayFixture(t *testing.T) sessionPublicReplayFixture {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "session-public-restart.sqlite")
	store, err := eventstore.NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("create EventStore: %v", err)
	}
	sessionID, turnID, traceID := modulecore.NewSessionID(), modulecore.NewTurnID(), modulecore.NewTraceID()
	occurredAt := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	input := modulecore.NewEventEnvelope(traceID, "", nil, "orchestrator", "conversation.user_message", occurredAt, map[string]any{
		"from": "user", "source_actor_id": "viewer-user", "to": "mio", "kind": "user_message", "text": "public input", "content": "public input", "visibility": "public",
	})
	input.SessionID, input.TurnID, input.MessageID = sessionID, turnID, modulecore.NewMessageID()
	accepted, err := store.AcceptTurn(ctx, eventstore.AcceptInput{
		OperationID: "accept-session-public-replay", OwnerID: "owner-1", RuntimeVersion: "v2",
		ClientSubmissionID: "submission-session-public-replay", ActorID: "viewer-user", TriggerKind: "user_message",
		GenerationEpoch: 1, CreateNewSession: true, InputEvent: input,
		InputMessage: eventstore.MessageIndex{
			MessageID: input.MessageID, EventID: input.EventID, SessionID: sessionID, TurnID: turnID,
			FromActor: "viewer-user", ToKind: "agent", ToActor: "mio", Kind: "user_message", Visibility: "public",
		},
	})
	if err != nil {
		_ = store.Close()
		t.Fatalf("AcceptTurn: %v", err)
	}
	input = accepted.InputEvent
	reply := modulecore.NewEventEnvelope(traceID, input.EventID, nil, "orchestrator", "agent.response", occurredAt.Add(time.Second), map[string]any{
		"from": "mio", "source_actor_id": "mio", "to": "user", "kind": "reply", "reply_to_message_id": string(input.MessageID), "text": "public answer", "content": "public answer", "visibility": "public",
	})
	reply.SessionID, reply.TurnID, reply.MessageID = sessionID, turnID, modulecore.NewMessageID()
	clarification := modulecore.NewEventEnvelope(traceID, input.EventID, nil, "orchestrator", "agent.response", occurredAt.Add(2*time.Second), map[string]any{
		"from": "mio", "source_actor_id": "mio", "to": "user", "kind": "clarification_prompt", "delegation_id": string(modulecore.NewRequestID()),
		"reply_to_message_id": string(input.MessageID), "availability": "free", "text": "public question", "content": "public question", "visibility": "public",
	})
	clarification.SessionID, clarification.TurnID, clarification.MessageID = sessionID, turnID, modulecore.NewMessageID()
	private := modulecore.NewEventEnvelope(traceID, input.EventID, nil, "conversation_runtime", "conversation.clarification_question", occurredAt.Add(3*time.Second), map[string]any{
		"from": "mio", "source_actor_id": "mio", "to": "shiro", "kind": "clarification_question", "text": "PRIVATE_SENTINEL", "content": "PRIVATE_SENTINEL", "visibility": "private",
	})
	private.SessionID, private.TurnID, private.MessageID = sessionID, turnID, modulecore.NewMessageID()
	private.ActorKind, private.ActorID = "agent", "mio"
	batch, err := store.CommitTurnBundle(ctx, eventstore.CommitInput{
		OperationID: "commit-session-public-replay", SessionID: sessionID, TurnID: turnID, GenerationEpoch: 1, ResultState: "completed",
		Events: []modulecore.EventEnvelope{reply, clarification, private},
		MessageIndexes: []eventstore.MessageIndex{
			{MessageID: reply.MessageID, EventID: reply.EventID, SessionID: sessionID, TurnID: turnID, FromActor: "mio", ToKind: "user", ToActor: "owner-1", Kind: "reply", ReplyToMessageID: input.MessageID, Visibility: "public"},
			{MessageID: clarification.MessageID, EventID: clarification.EventID, SessionID: sessionID, TurnID: turnID, FromActor: "mio", ToKind: "user", ToActor: "owner-1", Kind: "clarification_prompt", ReplyToMessageID: input.MessageID, Visibility: "public"},
			{MessageID: private.MessageID, EventID: private.EventID, SessionID: sessionID, TurnID: turnID, FromActor: "mio", ToKind: "agent", ToActor: "shiro", Kind: "clarification_question", Visibility: "private"},
		},
	})
	if err != nil {
		_ = store.Close()
		t.Fatalf("CommitTurnBundle: %v", err)
	}
	if len(batch.Events) != 3 {
		_ = store.Close()
		t.Fatalf("committed event count=%d, want 3", len(batch.Events))
	}
	reply, clarification = batch.Events[0], batch.Events[1]
	state, found, err := store.ReadSessionState(ctx, sessionID, "owner-1")
	if err != nil || !found {
		_ = store.Close()
		t.Fatalf("ReadSessionState found=%t err=%v", found, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close EventStore before restart: %v", err)
	}
	return sessionPublicReplayFixture{
		path: path, scope: SessionPublicReplayScope{SessionID: sessionID, OwnerID: "owner-1", ActorID: "viewer-user", CutoverSeq: state.CutoverSeq},
		inputMessageID: input.MessageID, reply: reply, clarification: clarification, private: batch.Events[2],
	}
}

func TestEventHubReplaysCanonicalSQLiteHistoryAcrossRestartAndPages(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "event-store.sqlite")
	store, err := eventstore.NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	archive, err := NewCanonicalEventLog(store)
	if err != nil {
		_ = store.Close()
		t.Fatalf("NewCanonicalEventLog() error = %v", err)
	}
	const total = replayPageLimit + 2
	sessionID := string(modulecore.NewSessionID())
	traceID := modulecore.NewTraceID()
	persisted := make([]orchestrator.OrchestratorEvent, 0, total)
	for index := 1; index <= total; index++ {
		event := orchestrator.NewEventWithTraceID(traceID, "entry.stage", "chrome", "system", "persisted-"+strconv.Itoa(index), "CHAT", "", sessionID, "local", "chrome")
		projected, err := archive.AppendSequenced(event)
		if err != nil {
			_ = store.Close()
			t.Fatalf("AppendSequenced(%d) error = %v", index, err)
		}
		persisted = append(persisted, projected)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("initial event store Close() error = %v", err)
	}

	store, err = eventstore.NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("reopen NewSQLiteStore() error = %v", err)
	}
	defer func() { _ = store.Close() }()
	archive, err = NewCanonicalEventLog(store)
	if err != nil {
		t.Fatalf("reopen NewCanonicalEventLog() error = %v", err)
	}
	hub := NewEventHub(2)
	hub.SetReplayReader(archive)
	for _, event := range persisted[total-2:] {
		hub.OnEvent(event)
	}
	client, snapshot := hub.Subscribe()
	defer hub.Unsubscribe(client)

	var replayed []modulecore.EventSeq
	cursor, err := hub.Replay(ctx, 1, snapshot, func(event orchestrator.OrchestratorEvent) error {
		replayed = append(replayed, event.EventSeq)
		return nil
	})
	if err != nil {
		t.Fatalf("replay after SQLite restart error = %v", err)
	}
	want := make([]modulecore.EventSeq, 0, total-1)
	for sequence := 2; sequence <= total; sequence++ {
		want = append(want, modulecore.EventSeq(sequence))
	}
	if cursor != total || !reflect.DeepEqual(replayed, want) {
		t.Fatalf("replayed count=%d cursor=%d, want count=%d cursor=%d", len(replayed), cursor, len(want), total)
	}
}
