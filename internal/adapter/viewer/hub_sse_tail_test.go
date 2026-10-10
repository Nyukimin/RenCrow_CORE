package viewer

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// maxInt64LastEventID is the literal value that older clients sent as
// Last-Event-ID to mean "subscribe from now". It is not a real EventSeq and
// must keep being rejected as a cursor ahead of the canonical window.
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
			counts := publishOnConnect(hub, sequencedEvent("agent.response", "LIVE-AGENT", 2))

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
			if got := strings.Count(body, "id: 2\n"); got != 1 {
				t.Fatalf("live id line appeared %d times, want once: %q", got, body)
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
	if got := strings.Count(body, "id: "); got != 1 || !strings.Contains(body, "id: 5\n") {
		t.Fatalf("only the durable event may carry an id line, got %d id lines: %q", got, body)
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

	rec := serveTail(t, hub, "/viewer/events?from=now", "1", 1)

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %q", rec.Code, body)
	}
	if reader.calls == 0 {
		t.Fatal("a positive Last-Event-ID must resume through the durable reader even with from=now")
	}
	for sequence := 2; sequence <= 3; sequence++ {
		if got := strings.Count(body, "id: "+strconv.Itoa(sequence)+"\n"); got != 1 {
			t.Fatalf("resumed event_seq %d appeared %d times, want once: %q", sequence, got, body)
		}
	}
	if strings.Contains(body, "id: 1\n") {
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
func newCanonicalHub(t *testing.T, persisted int) (*EventHub, modulecore.EventSeq) {
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
	var latest modulecore.EventSeq
	for index := 1; index <= persisted; index++ {
		event := orchestrator.NewEventWithTraceID(traceID, "entry.stage", "chrome", "system", "persisted-"+strconv.Itoa(index), "CHAT", "", sessionID, "local", "chrome")
		projected, err := archive.AppendSequenced(event)
		if err != nil {
			t.Fatalf("AppendSequenced(%d) error = %v", index, err)
		}
		latest = projected.EventSeq
	}
	hub := NewEventHub(200)
	hub.SetReplayReader(archive)
	return hub, latest
}

// A Last-Event-ID is a canonical EventSeq. The accepted range ends at the
// canonical watermark; anything ahead (including the old MaxInt64 "from now"
// sentinel) stays a 409. "From now" is expressed with ?from=now instead.
func TestHandleSSE_CanonicalCursorBoundaryAndTailAgainstSQLite(t *testing.T) {
	hub, latest := newCanonicalHub(t, 3)
	cases := []struct {
		name        string
		target      string
		lastEventID string
		want        int
	}{
		{"no header replays the in-memory snapshot", "/viewer/events", "", http.StatusOK},
		{"latest real EventSeq resumes", "/viewer/events", strconv.FormatInt(int64(latest), 10), http.StatusOK},
		{"latest+1 is ahead of the canonical window", "/viewer/events", strconv.FormatInt(int64(latest)+1, 10), http.StatusConflict},
		{"MaxInt64 sentinel stays rejected", "/viewer/events", maxInt64LastEventID, http.StatusConflict},
		{"from=now alone tails", "/viewer/events?from=now", "", http.StatusOK},
		{"from=now with Last-Event-ID 0 tails", "/viewer/events?from=now", "0", http.StatusOK},
		{"from=now does not rescue an out-of-window cursor", "/viewer/events?from=now", maxInt64LastEventID, http.StatusConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var logs strings.Builder
			log.SetOutput(&logs)
			t.Cleanup(func() { log.SetOutput(os.Stderr) })

			rec := serveTail(t, hub, tc.target, tc.lastEventID, 1)

			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (server-log=%q)", rec.Code, tc.want, strings.TrimSpace(logs.String()))
			}
			if tc.want == http.StatusConflict && !strings.Contains(logs.String(), "event replay cursor is outside the canonical window") {
				t.Fatalf("409 reason in server log = %q, want 'outside the canonical window'", strings.TrimSpace(logs.String()))
			}
			if hub.ClientCount() != 0 {
				t.Fatalf("client leaked after request: %d", hub.ClientCount())
			}
		})
	}
}
