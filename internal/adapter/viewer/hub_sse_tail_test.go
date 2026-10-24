package viewer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	"github.com/Nyukimin/RenCrow_CORE/internal/turntiming"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// maxInt64LastEventID is a legacy numeric cursor that older clients used as a
// "subscribe from now" sentinel. Numeric cursors now request an explicit reset.
const maxInt64LastEventID = "9223372036854775807"

// serveTail runs HandleSSE for a request and cancels it after the given number
// of flushes (or after one second as a safety net). It returns the recorder.
func serveTail(t *testing.T, hub *EventHub, target, lastEventID string, cancelOnFlush int) *cancelAfterFlushWriter {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, target, nil).WithContext(ctx)
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	rec := &cancelAfterFlushWriter{
		ResponseRecorder: httptest.NewRecorder(),
		cancel:           cancel,
		cancelOn:         cancelOnFlush,
	}
	hub.HandleSSE(rec, req)
	return rec
}

// publishOnConnect emits the given events once, right after the first client
// has been registered, and records every client-count change.
func publishOnConnect(hub *EventHub, events ...orchestrator.OrchestratorEvent) *clientCountLog {
	counts := &clientCountLog{}
	hub.SetClientCountListener(func(count int) {
		counts.add(count)
		if count == 1 {
			for _, event := range events {
				hub.OnEvent(event)
			}
		}
	})
	return counts
}

type clientCountLog struct {
	mu     sync.Mutex
	values []int
}

func (c *clientCountLog) add(count int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.values = append(c.values, count)
}

func (c *clientCountLog) snapshot() []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int(nil), c.values...)
}

func sequencedEvent(eventType string, content string, seq modulecore.EventSeq) orchestrator.OrchestratorEvent {
	event := orchestrator.NewEvent(eventType, "mio", "user", content, "CHAT", "", "s1", "viewer", "u1")
	event.EventSeq = seq
	return event
}

func TestHandleSSEWithFilter_DropsUnsequencedConversationEvents(t *testing.T) {
	hub := NewEventHub(10)
	event := orchestrator.NewEvent("conversation.message", "mio", "user", "UNSEQUENCED-CONVERSATION", "CHAT", "", "s1", "viewer", "u1")
	hub.OnEvent(event)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/viewer/events", nil).WithContext(ctx)
	rec := &cancelAfterFlushWriter{ResponseRecorder: httptest.NewRecorder(), cancel: cancel, cancelOn: 1}
	hub.HandleSSEWithFilter(rec, req, func(context.Context, orchestrator.OrchestratorEvent) (bool, error) {
		return true, nil
	})

	if strings.Contains(rec.Body.String(), "UNSEQUENCED-CONVERSATION") {
		t.Fatalf("general SSE exposed a seq0 conversation event despite an inclusive caller filter: %q", rec.Body.String())
	}
}

type delayedControlReplayReader struct {
	entered chan struct{}
	release <-chan struct{}
	cursor  orchestrator.OrchestratorEvent
	next    orchestrator.OrchestratorEvent
	once    sync.Once
}

type delayedSessionControlReplayReader struct {
	entered chan struct{}
	release <-chan struct{}
	event   orchestrator.OrchestratorEvent
	once    sync.Once
}

func (r *delayedSessionControlReplayReader) ReplaySessionPublicPage(ctx context.Context, _ SessionPublicReplayScope, after, through modulecore.EventSeq, _ int) ([]orchestrator.OrchestratorEvent, modulecore.EventSeq, error) {
	r.once.Do(func() { close(r.entered) })
	select {
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	case <-r.release:
	}
	if through == 0 {
		through = r.event.EventSeq
	}
	if r.event.EventSeq > after && r.event.EventSeq <= through {
		return []orchestrator.OrchestratorEvent{r.event}, through, nil
	}
	return nil, through, nil
}

func (r *delayedSessionControlReplayReader) ReadSessionPublicMessage(context.Context, SessionPublicReplayScope, modulecore.EventID, modulecore.EventSeq) (orchestrator.OrchestratorEvent, bool, error) {
	return orchestrator.OrchestratorEvent{}, false, nil
}

func (r *delayedControlReplayReader) ReplayPage(ctx context.Context, after, through modulecore.EventSeq, _ int) ([]orchestrator.OrchestratorEvent, modulecore.EventSeq, error) {
	r.once.Do(func() { close(r.entered) })
	select {
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	case <-r.release:
	}
	if through == 0 {
		through = r.next.EventSeq
	}
	if r.next.EventSeq > after && r.next.EventSeq <= through {
		return []orchestrator.OrchestratorEvent{r.next}, through, nil
	}
	return nil, through, nil
}

func (r *delayedControlReplayReader) GetByEventID(_ context.Context, id modulecore.EventID) (orchestrator.OrchestratorEvent, bool, error) {
	if id == r.cursor.EventID {
		return r.cursor, true, nil
	}
	if id == r.next.EventID {
		return r.next, true, nil
	}
	return orchestrator.OrchestratorEvent{}, false, nil
}

func TestHandleSSE_ControlSnapshotPrecedesDelayedHistoryAndReconnectsLatest(t *testing.T) {
	hub := NewEventHub(10)
	initial := IdleChatControlSnapshot{
		RuntimeEpoch: "runtime-a", StateRevision: 10, PlaybackState: "playing",
		PlaybackAllowed: true, PlaybackActive: true, PlaybackGeneration: 7,
		GenerationState: "idle", GenerationQuiesced: true, BackendResourceState: "unknown",
	}
	stopped := initial
	stopped.StateRevision = 11
	stopped.PlaybackState = "blocked"
	stopped.PlaybackAllowed = false
	stopped.PlaybackActive = false
	stopped.CleanupPending = true
	stopped.GenerationState = "pause_requested"
	stopped.GenerationQuiesced = false
	hub.PublishIdleChatControlSnapshot(initial)

	cursor := sequencedEvent("entry.stage", "cursor", 1)
	next := sequencedEvent("entry.stage", "DELAYED-HISTORY", 2)
	readerRelease := make(chan struct{})
	reader := &delayedControlReplayReader{entered: make(chan struct{}), release: readerRelease, cursor: cursor, next: next}
	hub.SetReplayReader(reader)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/viewer/events", nil).WithContext(ctx)
	req.Header.Set("Last-Event-ID", string(cursor.EventID))
	rec := &cancelAfterFlushWriter{ResponseRecorder: httptest.NewRecorder(), cancel: cancel, cancelOn: 3}
	done := make(chan struct{})
	go func() {
		hub.HandleSSE(rec, req)
		close(done)
	}()

	select {
	case <-reader.entered:
	case <-time.After(time.Second):
		t.Fatal("durable replay did not reach the intentional delay")
	}
	if body := rec.Body.String(); !strings.Contains(body, `"state_revision":10`) || strings.Contains(body, "DELAYED-HISTORY") {
		t.Fatalf("initial control state was not flushed before delayed history: %q", body)
	}
	hub.PublishIdleChatControlSnapshot(stopped)
	close(readerRelease)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SSE handler did not finish after history/control delivery")
	}

	body := rec.Body.String()
	initialAt := strings.Index(body, `"state_revision":10`)
	historyAt := strings.Index(body, "DELAYED-HISTORY")
	stoppedAt := strings.Index(body, `"state_revision":11`)
	if initialAt < 0 || historyAt < 0 || stoppedAt < 0 || !(initialAt < historyAt && historyAt < stoppedAt) {
		t.Fatalf("want initial snapshot, delayed history, then stop snapshot: %q", body)
	}
	for _, frame := range strings.Split(body, "\n\n") {
		if strings.HasPrefix(frame, "event: idlechat.control\n") && strings.Contains(frame, "id:") {
			t.Fatalf("control frame must not carry a canonical SSE id: %q", frame)
		}
	}
	if got := hub.History(); len(got) != 0 {
		t.Fatalf("control snapshots entered canonical replay history: %+v", got)
	}

	reconnect := serveTail(t, hub, "/viewer/events?from=now", "", 1)
	if !strings.Contains(reconnect.Body.String(), `"state_revision":11`) || strings.Contains(reconnect.Body.String(), "DELAYED-HISTORY") {
		t.Fatalf("reconnect did not receive only the latest control snapshot before tailing: %q", reconnect.Body.String())
	}
}

func TestEventHubKeepsActivePlaybackAgainstLateSameGenerationPreparation(t *testing.T) {
	hub := NewEventHub(10)
	active := IdleChatControlSnapshot{
		RuntimeEpoch: "runtime-a", StateRevision: 4, PlaybackState: "playing",
		PlaybackAllowed: true, PlaybackActive: true, PlaybackGeneration: 8,
		GenerationState: "running", GenerationQuiesced: false, BackendResourceState: "unknown",
	}
	latePreparation := active
	latePreparation.PlaybackState = "idlechat"
	latePreparation.PlaybackActive = false
	hub.PublishIdleChatControlSnapshot(active)
	hub.PublishIdleChatControlSnapshot(latePreparation)

	reconnect := serveTail(t, hub, "/viewer/events?from=now", "", 1)
	if !strings.Contains(reconnect.Body.String(), `"playback_active":true`) || !strings.Contains(reconnect.Body.String(), `"playback_generation":8`) {
		t.Fatalf("late same-generation preparation replaced active playback: %q", reconnect.Body.String())
	}
}

func TestHandleSessionPublicSSE_ControlSnapshotPrecedesDelayedHistoryAndStreamsLatest(t *testing.T) {
	hub := NewEventHub(10)
	sessionID := modulecore.NewSessionID()
	turnID := modulecore.NewTurnID()
	messageID := modulecore.NewMessageID()
	publicEvent := sequencedEvent("agent.response", "SCOPED-DELAYED-HISTORY", 2)
	publicEvent.SessionID = sessionID
	publicEvent.TurnID = turnID
	publicEvent.MessageID = messageID
	publicEvent.Visibility = "public"
	scope := SessionPublicReplayScope{SessionID: sessionID, OwnerID: "owner-1", ActorID: "viewer-user", CutoverSeq: 1}
	readerRelease := make(chan struct{})
	reader := &delayedSessionControlReplayReader{entered: make(chan struct{}), release: readerRelease, event: publicEvent}
	hub.SetSessionPublicReplayReader(reader)

	initial := IdleChatControlSnapshot{
		RuntimeEpoch: "runtime-session", StateRevision: 20, PlaybackState: "playing",
		PlaybackAllowed: true, PlaybackActive: true, PlaybackGeneration: 9,
		GenerationState: "idle", GenerationQuiesced: true, BackendResourceState: "unknown",
	}
	stopped := initial
	stopped.StateRevision = 21
	stopped.PlaybackState = "blocked"
	stopped.PlaybackAllowed = false
	stopped.PlaybackActive = false
	stopped.CleanupPending = true
	stopped.GenerationState = "pause_requested"
	stopped.GenerationQuiesced = false
	hub.PublishIdleChatControlSnapshot(initial)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/viewer/conversation/events", nil).WithContext(ctx)
	rec := &cancelAfterFlushWriter{ResponseRecorder: httptest.NewRecorder(), cancel: cancel, cancelOn: 3}
	done := make(chan struct{})
	go func() {
		hub.HandleSessionPublicSSE(rec, req, scope)
		close(done)
	}()

	select {
	case <-reader.entered:
	case <-time.After(time.Second):
		t.Fatal("owner-scoped replay did not reach the intentional delay")
	}
	if body := rec.Body.String(); !strings.Contains(body, "event: idlechat.control\n") || !strings.Contains(body, `"state_revision":20`) || strings.Contains(body, "SCOPED-DELAYED-HISTORY") {
		t.Fatalf("owner-scoped stream did not flush the initial control snapshot before delayed history: %q", body)
	}
	hub.PublishIdleChatControlSnapshot(stopped)
	close(readerRelease)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("owner-scoped SSE handler did not finish after history/control delivery")
	}

	body := rec.Body.String()
	initialAt := strings.Index(body, `"state_revision":20`)
	historyAt := strings.Index(body, "SCOPED-DELAYED-HISTORY")
	stoppedAt := strings.Index(body, `"state_revision":21`)
	if rec.Code != http.StatusOK || initialAt < 0 || historyAt < 0 || stoppedAt < 0 || !(initialAt < historyAt && historyAt < stoppedAt) {
		t.Fatalf("want initial control, authorized delayed history, then latest control; status=%d body=%q", rec.Code, body)
	}
	for _, frame := range strings.Split(body, "\n\n") {
		if strings.HasPrefix(frame, "event: idlechat.control\n") && (strings.Contains(frame, "id:") || strings.Contains(frame, `"event_seq"`)) {
			t.Fatalf("control frame must not carry canonical identity: %q", frame)
		}
	}
	if strings.Count(body, "SCOPED-DELAYED-HISTORY") != 1 {
		t.Fatalf("session replay no longer emits exactly one authorized public event: %q", body)
	}

	reconnect := serveSessionPublicTail(t, hub, scope, 1)
	if !strings.Contains(reconnect.Body.String(), `"state_revision":21`) {
		t.Fatalf("owner-scoped reconnect did not receive latest control snapshot: %q", reconnect.Body.String())
	}
}

func serveSessionPublicTail(t *testing.T, hub *EventHub, scope SessionPublicReplayScope, cancelOnFlush int) *cancelAfterFlushWriter {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/viewer/conversation/events?from=now", nil).WithContext(ctx)
	rec := &cancelAfterFlushWriter{ResponseRecorder: httptest.NewRecorder(), cancel: cancel, cancelOn: cancelOnFlush}
	hub.HandleSessionPublicSSE(rec, req, scope)
	return rec
}

type failingReplayReader struct{ calls int }

func (r *failingReplayReader) ReplayPage(context.Context, modulecore.EventSeq, modulecore.EventSeq, int) ([]orchestrator.OrchestratorEvent, modulecore.EventSeq, error) {
	r.calls++
	return nil, 0, errors.New("durable reader unavailable")
}

func TestHandleSSE_FromNowSkipsHistoryAndStreamsLiveOnce(t *testing.T) {
	for _, mode := range []string{"in-memory history", "durable reader"} {
		t.Run(mode, func(t *testing.T) {
			hub := NewEventHub(200)
			history := sequencedEvent("agent.response", "OLD-HISTORY", 1)
			var reader *replayPageReader
			if mode == "durable reader" {
				reader = &replayPageReader{events: []orchestrator.OrchestratorEvent{history}}
				hub.SetReplayReader(reader)
			}
			hub.OnEvent(history)
			live := sequencedEvent("agent.response", "LIVE-AGENT", 2)
			counts := publishOnConnect(hub, live)

			rec := serveTail(t, hub, "/viewer/events?from=now", "", 2)

			body := rec.Body.String()
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %q", rec.Code, body)
			}
			if strings.Contains(body, "OLD-HISTORY") {
				t.Fatalf("from=now must not replay history: %q", body)
			}
			if got := strings.Count(body, "LIVE-AGENT"); got != 1 {
				t.Fatalf("live event delivered %d times, want once: %q", got, body)
			}
			if got := strings.Count(body, "id: "+string(live.EventID)+"\n"); got != 1 {
				t.Fatalf("live EventID line appeared %d times, want once: %q", got, body)
			}
			if reader != nil && reader.calls != 0 {
				t.Fatalf("durable ReplayPage called %d times for a tail subscription, want 0", reader.calls)
			}
			if got := counts.snapshot(); len(got) != 2 || got[0] != 1 || got[1] != 0 {
				t.Fatalf("client count changes = %v, want [1 0]", got)
			}
			if hub.ClientCount() != 0 {
				t.Fatalf("client leaked after disconnect: %d", hub.ClientCount())
			}
		})
	}
}

func TestHandleSSE_FromNowDeliversTransientLiveEventsWithoutID(t *testing.T) {
	hub := NewEventHub(200)
	hub.SetReplayReader(&replayPageReader{})
	idle := sequencedEvent("idlechat.message", "LIVE-IDLE", 0)
	durable := sequencedEvent("agent.response", "LIVE-DURABLE", 5)
	publishOnConnect(hub, idle, durable)

	rec := serveTail(t, hub, "/viewer/events?from=now", "", 3)

	body := rec.Body.String()
	if got := strings.Count(body, "LIVE-IDLE"); got != 1 {
		t.Fatalf("transient live event delivered %d times, want once: %q", got, body)
	}
	if got := strings.Count(body, "LIVE-DURABLE"); got != 1 {
		t.Fatalf("durable live event delivered %d times, want once: %q", got, body)
	}
	if got := strings.Count(body, "id: "); got != 1 || !strings.Contains(body, "id: "+string(durable.EventID)+"\n") {
		t.Fatalf("only the durable event may carry an id line, got %d id lines: %q", got, body)
	}
}

func TestHandleSSE_ProjectsOptInTimingOnTerminalResponse(t *testing.T) {
	hub := NewEventHub(10)
	event := sequencedEvent("agent.response", "LIVE-TIMED", 1)
	event.Timing = &turntiming.Snapshot{
		SchemaVersion: turntiming.SchemaVersion,
		PointsMS:      map[string]float64{"handler_received": 0, "response_publication_started": 12.5},
	}
	publishOnConnect(hub, event)

	rec := serveTail(t, hub, "/viewer/events?from=now", "", 2)
	body := rec.Body.String()
	if !strings.Contains(body, `"type":"agent.response"`) {
		t.Fatalf("SSE stream is missing terminal response event: %s", body)
	}
	if !strings.Contains(body, `"timing":{"schema_version":1,"points_ms":{"handler_received":0,"response_publication_started":12.5}}`) {
		t.Fatalf("SSE terminal response is missing timing projection: %s", body)
	}
}

func TestHandleSSE_FromNowDoesNotNeedTheDurableReader(t *testing.T) {
	hub := NewEventHub(200)
	reader := &failingReplayReader{}
	hub.SetReplayReader(reader)
	publishOnConnect(hub, sequencedEvent("agent.response", "LIVE-AGENT", 2))

	rec := serveTail(t, hub, "/viewer/events?from=now", "", 2)

	if rec.Code != http.StatusOK || strings.Count(rec.Body.String(), "LIVE-AGENT") != 1 {
		t.Fatalf("tail must stream live without the durable reader: status=%d body=%q", rec.Code, rec.Body.String())
	}
	if reader.calls != 0 {
		t.Fatalf("durable reader called %d times, want 0", reader.calls)
	}
}

func TestHandleSSE_PositiveLastEventIDResumesAndIgnoresFromNow(t *testing.T) {
	events := replayTestEvents(3)
	hub := NewEventHub(10)
	reader := &replayPageReader{events: events}
	hub.SetReplayReader(reader)
	for _, event := range events {
		hub.OnEvent(event)
	}

	rec := serveTail(t, hub, "/viewer/events?from=now", string(events[0].EventID), 1)

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %q", rec.Code, body)
	}
	if reader.calls == 0 {
		t.Fatal("a positive Last-Event-ID must resume through the durable reader even with from=now")
	}
	for _, event := range events[1:] {
		if got := strings.Count(body, "id: "+string(event.EventID)+"\n"); got != 1 {
			t.Fatalf("resumed EventID %s appeared %d times, want once: %q", event.EventID, got, body)
		}
	}
	if strings.Contains(body, "id: "+string(events[0].EventID)+"\n") {
		t.Fatalf("resume must start after the cursor: %q", body)
	}
}

func TestHandleSSE_ZeroLastEventIDWithFromNowIsTail(t *testing.T) {
	hub := NewEventHub(10)
	hub.OnEvent(sequencedEvent("agent.response", "OLD-HISTORY", 1))
	publishOnConnect(hub, sequencedEvent("agent.response", "LIVE-AGENT", 2))

	rec := serveTail(t, hub, "/viewer/events?from=now", "0", 2)

	body := rec.Body.String()
	if strings.Contains(body, "OLD-HISTORY") || strings.Count(body, "LIVE-AGENT") != 1 {
		t.Fatalf("Last-Event-ID 0 with from=now must tail: %q", body)
	}
}

func TestHandleSSE_RejectsInvalidFromWithoutSubscribing(t *testing.T) {
	for _, target := range []string{
		"/viewer/events?from=latest",
		"/viewer/events?from=",
		"/viewer/events?from",
		"/viewer/events?from=NOW",
		"/viewer/events?from=now&from=now",
		"/viewer/events?from=now&from=latest",
	} {
		t.Run(target, func(t *testing.T) {
			hub := NewEventHub(10)
			hub.OnEvent(sequencedEvent("agent.response", "OLD-HISTORY", 1))
			counts := publishOnConnect(hub)

			rec := serveTail(t, hub, target, "", 1)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %q", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "OLD-HISTORY") {
				t.Fatalf("a rejected request must not stream history: %q", rec.Body.String())
			}
			if got := counts.snapshot(); len(got) != 0 {
				t.Fatalf("a rejected request subscribed a client: count changes = %v", got)
			}
		})
	}
}

func TestHandleSSE_InvalidLastEventIDIsRejectedBeforeFrom(t *testing.T) {
	hub := NewEventHub(10)
	rec := serveTail(t, hub, "/viewer/events?from=now", "not-a-seq", 1)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Last-Event-ID") {
		t.Fatalf("status = %d body = %q, want 400 naming Last-Event-ID", rec.Code, rec.Body.String())
	}
}

func TestHandleSSE_IgnoresUnknownQueryAndKeepsSnapshotWithoutFrom(t *testing.T) {
	for _, target := range []string{"/viewer/events", "/viewer/events?t=1&mode=chat"} {
		t.Run(target, func(t *testing.T) {
			hub := NewEventHub(10)
			hub.OnEvent(sequencedEvent("agent.response", "OLD-HISTORY", 1))

			rec := serveTail(t, hub, target, "", 1)

			if rec.Code != http.StatusOK || strings.Count(rec.Body.String(), "OLD-HISTORY") != 1 {
				t.Fatalf("without from=now the in-memory snapshot is replayed: status=%d body=%q", rec.Code, rec.Body.String())
			}
		})
	}
}

// newCanonicalHub wires the same objects as the production runtime: a real
// SQLite canonical event store, CanonicalEventLog and EventHub.SetReplayReader.
func newCanonicalHub(t *testing.T, persisted int) (*EventHub, modulecore.EventID) {
	t.Helper()
	store, err := eventstore.NewSQLiteStore(filepath.Join(t.TempDir(), "event-store.sqlite"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	archive, err := NewCanonicalEventLog(store)
	if err != nil {
		t.Fatalf("NewCanonicalEventLog() error = %v", err)
	}
	sessionID := string(modulecore.NewSessionID())
	traceID := modulecore.NewTraceID()
	var latest modulecore.EventID
	for index := 1; index <= persisted; index++ {
		event := orchestrator.NewEventWithTraceID(traceID, "entry.stage", "chrome", "system", "persisted-"+strconv.Itoa(index), "CHAT", "", sessionID, "local", "chrome")
		projected, err := archive.AppendSequenced(event)
		if err != nil {
			t.Fatalf("AppendSequenced(%d) error = %v", index, err)
		}
		latest = projected.EventID
	}
	hub := NewEventHub(200)
	hub.SetReplayReader(archive)
	return hub, latest
}

// Canonical cursors use EventID; numeric legacy cursors and unavailable IDs
// trigger reset and replay of the available canonical window.
func TestHandleSSE_CanonicalCursorBoundaryAndTailAgainstSQLite(t *testing.T) {
	hub, latest := newCanonicalHub(t, 3)
	lostEventID := string(modulecore.NewEventID())
	cases := []struct {
		name        string
		target      string
		lastEventID string
		want        int
		wantReset   bool
	}{
		{name: "no header replays the in-memory snapshot", target: "/viewer/events", want: http.StatusOK},
		{name: "latest stable EventID resumes", target: "/viewer/events", lastEventID: string(latest), want: http.StatusOK},
		{name: "lost stable EventID resets", target: "/viewer/events", lastEventID: lostEventID, want: http.StatusOK, wantReset: true},
		{name: "numeric legacy cursor resets", target: "/viewer/events", lastEventID: "1", want: http.StatusOK, wantReset: true},
		{name: "MaxInt64 legacy sentinel resets", target: "/viewer/events", lastEventID: maxInt64LastEventID, want: http.StatusOK, wantReset: true},
		{name: "from=now alone tails", target: "/viewer/events?from=now", want: http.StatusOK},
		{name: "from=now with Last-Event-ID 0 tails", target: "/viewer/events?from=now", lastEventID: "0", want: http.StatusOK},
		{name: "from=now with a numeric cursor resets", target: "/viewer/events?from=now", lastEventID: maxInt64LastEventID, want: http.StatusOK, wantReset: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := serveTail(t, hub, tc.target, tc.lastEventID, 1)

			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d: %q", rec.Code, tc.want, rec.Body.String())
			}
			if got := strings.Contains(rec.Body.String(), "event: reset\n"); got != tc.wantReset {
				t.Fatalf("reset frame present=%t, want %t: %q", got, tc.wantReset, rec.Body.String())
			}
			if tc.wantReset && !strings.Contains(rec.Body.String(), "id: reset\n") {
				t.Fatalf("reset frame did not set the reset continuation cursor: %q", rec.Body.String())
			}
			if hub.ClientCount() != 0 {
				t.Fatalf("client leaked after request: %d", hub.ClientCount())
			}
		})
	}
}
