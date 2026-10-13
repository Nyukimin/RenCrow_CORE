package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	modulestt "github.com/Nyukimin/RenCrow_CORE/modules/stt"
	"golang.org/x/net/websocket"
)

// Resource limits of one /stt session. Every limit is a bound on memory,
// goroutines or queued work that a client controls; none may be exceeded by a
// misbehaving client, and a session that is closed for exceeding one still
// gives every unfinished utterance exactly one terminal event first.

// drain collects events until the connection closes or d elapses.
func (c *sttTestClient) drain(d time.Duration) (events []map[string]any, closed bool) {
	deadline := time.Now().Add(d)
	for {
		ev, ok := c.next(time.Until(deadline))
		if !ok {
			select {
			case _, open := <-c.events:
				return events, !open
			default:
				return events, time.Now().Before(deadline)
			}
		}
		events = append(events, ev)
	}
}

// trySend sends without failing the test: once the server has refused the
// session it closes the socket, and later writes legitimately fail.
func (c *sttTestClient) trySend(payload any) error {
	return websocket.Message.Send(c.conn, payload)
}

func terminalsOf(events []map[string]any) []map[string]any {
	var out []map[string]any
	for _, ev := range events {
		if terminalEvent(ev) {
			out = append(out, ev)
		}
	}
	return out
}

func countTerminalsFor(events []map[string]any, id string) int {
	n := 0
	for _, ev := range terminalsOf(events) {
		if ev["utterance_id"] == id {
			n++
		}
	}
	return n
}

func limitsTestConfig() sttSessionConfig {
	cfg := testSTTSessionConfig()
	cfg.Limits.MaxLength = time.Second
	return cfg
}

func TestSTTMaxFrameBytesFollowsTheLongestUtterance(t *testing.T) {
	def := sttMaxFrameBytes(modulestt.UtteranceLimits{}.WithDefaults())
	pcm30s := 30 * 16000 * 2
	if def < pcm30s+44 {
		t.Fatalf("default frame limit %d cannot carry a 30 s WAV (%d bytes)", def, pcm30s+44)
	}
	if def > 1<<20 {
		t.Fatalf("default frame limit %d is far above one maximum utterance; it must stay near 1 MiB", def)
	}
	long := sttMaxFrameBytes(modulestt.UtteranceLimits{MaxLength: 120 * time.Second}.WithDefaults())
	if long < 120*16000*2+44 || long > 120*16000*2+64*1024 {
		t.Fatalf("limit for 120 s = %d", long)
	}
	if tiny := sttMaxFrameBytes(modulestt.UtteranceLimits{MaxLength: 20 * time.Millisecond}.WithDefaults()); tiny < 64*1024 {
		t.Fatalf("control messages and small frames must always fit, got %d", tiny)
	}
}

func TestSTTSessionAcceptsAFullLengthWAVInOneFrameAndAFastBurstOfFrames(t *testing.T) {
	cfg := limitsTestConfig()
	provider := newScriptedSTTProvider()
	c := dialSTT(t, provider, cfg)
	c.audio(modulestt.PCM16LEToWAV(sttTestPCM(1000, 3000), 16000)) // exactly MaxLength in one frame
	c.control(map[string]any{"type": "stop"})
	if final := c.until("final", eventType("final")); final["text"] != "t1" || asInt(final["audio_ms"]) != 1000 {
		t.Fatalf("a maximum-length utterance in one frame must be accepted: %#v", final)
	}

	cfg = testSTTSessionConfig()
	cfg.Limits.MaxLength = 30 * time.Second
	c = dialSTT(t, newScriptedSTTProvider(), cfg)
	c.control(map[string]any{"type": "start"})
	for i := 0; i < 300; i++ { // 30 s sent as fast as the connection allows, like the probe script
		c.tone(100)
	}
	c.control(map[string]any{"type": "stop"})
	if final := c.until("final", eventType("final")); final["end_reason"] != "stop" || asInt(final["audio_ms"]) != 30000 {
		t.Fatalf("a legitimate fast burst must not trip a limit: %#v", final)
	}
}

func TestSTTSessionOversizedFrameEndsTheOpenUtteranceOnceAndClosesTheConnection(t *testing.T) {
	cfg := limitsTestConfig()
	provider := newScriptedSTTProvider()
	c := dialSTT(t, provider, cfg)
	c.control(map[string]any{"type": "start", "utterance_id": "a"})
	c.tone(300)
	c.audio(make([]byte, sttMaxFrameBytes(cfg.Limits.WithDefaults())+1))

	events, closed := c.drain(3 * time.Second)
	if !closed {
		t.Fatal("the connection must be closed after an oversized frame")
	}
	if n := countTerminalsFor(events, "a"); n != 1 {
		t.Fatalf("utterance a got %d terminal events, want exactly 1: %#v", n, events)
	}
	for _, ev := range terminalsOf(events) {
		if ev["type"] != "error" || ev["code"] != "stt_limit_exceeded" {
			t.Fatalf("terminal = %#v", ev)
		}
	}
	if provider.calls() != 0 {
		t.Fatalf("an aborted session must not start recognition, calls=%d", provider.calls())
	}
}

func TestSTTSessionHugeDeclaredFrameIsRefusedWithoutBeingRead(t *testing.T) {
	c := dialSTT(t, newScriptedSTTProvider(), limitsTestConfig())
	// 8 MiB is far above the limit and well below the library default; it must be refused all the same.
	// The server closes the socket while the client is still writing, so the send may fail.
	_ = c.trySend(make([]byte, 8<<20))
	events, closed := c.drain(3 * time.Second)
	if !closed {
		t.Fatalf("connection stayed open after an 8 MiB frame: %#v", events)
	}
}

func TestSTTSessionOversizedFrameGivesEveryUnfinishedUtteranceExactlyOneError(t *testing.T) {
	cfg := limitsTestConfig()
	provider := newScriptedSTTProvider()
	provider.blockOn = map[int]bool{1: true}
	c := dialSTT(t, provider, cfg)
	for _, id := range []string{"a", "b"} { // a: in recognition, b: queued behind it
		c.control(map[string]any{"type": "start", "utterance_id": id})
		c.tone(300)
		c.control(map[string]any{"type": "stop"})
	}
	for provider.calls() < 1 {
		time.Sleep(5 * time.Millisecond)
	}
	c.control(map[string]any{"type": "start", "utterance_id": "c"}) // c: still being received
	c.tone(300)
	c.audio(make([]byte, sttMaxFrameBytes(cfg.Limits.WithDefaults())+1))

	events, closed := c.drain(3 * time.Second)
	if !closed {
		t.Fatal("the connection must be closed")
	}
	terminals := terminalsOf(events)
	if len(terminals) != 3 {
		t.Fatalf("terminals = %#v, want one each for a, b and c", terminals)
	}
	for i, id := range []string{"a", "b", "c"} {
		if terminals[i]["utterance_id"] != id || terminals[i]["type"] != "error" || terminals[i]["code"] != "stt_limit_exceeded" {
			t.Fatalf("terminal %d = %#v, want an stt_limit_exceeded error for %s, in utterance order", i, terminals[i], id)
		}
	}
	select {
	case call := <-provider.cancelled:
		if call != 1 {
			t.Fatalf("cancelled call = %d", call)
		}
	case <-time.After(time.Second):
		t.Fatal("the running recognition must be aborted")
	}
	if provider.calls() != 1 {
		t.Fatalf("the queued utterance reached the engine after the abort, calls=%d", provider.calls())
	}
}

func TestSTTSessionControlFloodIsBoundedAndClosesTheConnection(t *testing.T) {
	cfg := limitsTestConfig()
	cfg.MaxQueuedEvents = 16
	provider := newScriptedSTTProvider()
	provider.blockOn = map[int]bool{1: true}
	c := dialSTT(t, provider, cfg)
	c.control(map[string]any{"type": "start", "utterance_id": "a"})
	c.tone(300)
	c.control(map[string]any{"type": "stop"})
	for provider.calls() < 1 {
		time.Sleep(5 * time.Millisecond)
	}
	for i := 0; i < 500; i++ { // each stop with nothing active is owed an empty final
		if err := c.trySend(`{"type":"stop"}`); err != nil {
			break // the server already closed the session
		}
	}

	events, closed := c.drain(3 * time.Second)
	if !closed {
		t.Fatal("a flood must end with the connection closed")
	}
	if n := countTerminalsFor(events, "a"); n != 1 {
		t.Fatalf("utterance a got %d terminal events, want exactly 1", n)
	}
	terminals := terminalsOf(events)
	if len(terminals) > cfg.MaxQueuedEvents+2 {
		t.Fatalf("%d terminal events for a flood of 500 controls; the queue must be bounded by %d", len(terminals), cfg.MaxQueuedEvents)
	}
	last := terminals[len(terminals)-1]
	if last["type"] != "error" || last["code"] != "stt_limit_exceeded" {
		t.Fatalf("the session must say why it closed, last terminal = %#v", last)
	}
}

func TestSTTSessionIgnoresAnOversizedUtteranceIDAndOversizedControlText(t *testing.T) {
	c := dialSTT(t, newScriptedSTTProvider(), testSTTSessionConfig())
	c.control(map[string]any{"type": "start", "utterance_id": strings.Repeat("x", 4096)})
	start := c.until("speech_start", eventType("speech_start"))
	id, _ := start["utterance_id"].(string)
	if id == "" || len(id) > 64 || strings.Contains(id, "xxxx") {
		t.Fatalf("an oversized utterance_id must be replaced, got %q", id)
	}

	c.tone(300)
	c.control(map[string]any{"type": "stop", "pad": strings.Repeat("y", modulestt.MaxControlMessageBytes)})
	c.quiet(150 * time.Millisecond) // too large to be a control message: it is audio and does not finalize
	c.control(map[string]any{"type": "stop"})
	if final := c.until("final", eventType("final")); final["utterance_id"] != id {
		t.Fatalf("final = %#v", final)
	}
}

func sttServerURL(t *testing.T, provider *scriptedSTTProvider, cfg sttSessionConfig) string {
	t.Helper()
	mux := http.NewServeMux()
	registerSTTRoutes(mux, handleSTTWebSocketProviderWithConfig(provider, cfg))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http") + "/stt"
}

func TestSTTHandlerRefusesConnectionsBeyondTheLimitAndFreesTheSlot(t *testing.T) {
	cfg := testSTTSessionConfig()
	cfg.MaxConnections = 2
	url := sttServerURL(t, newScriptedSTTProvider(), cfg)

	var open []*websocket.Conn
	for i := 0; i < 2; i++ {
		conn, err := websocket.Dial(url, "", "http://localhost/")
		if err != nil {
			t.Fatalf("connection %d: %v", i+1, err)
		}
		var first string
		if err := websocket.Message.Receive(conn, &first); err != nil { // wait until the session is live
			t.Fatalf("connection %d: %v", i+1, err)
		}
		open = append(open, conn)
	}
	defer func() {
		for _, conn := range open {
			_ = conn.Close()
		}
	}()
	if conn, err := websocket.Dial(url, "", "http://localhost/"); err == nil {
		_ = conn.Close()
		t.Fatal("a third connection must be refused before the upgrade")
	}
	// A plain GET shows the status the refused handshake received.
	resp, err := http.Get("http" + strings.TrimPrefix(url, "ws"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 while all slots are taken", resp.StatusCode)
	}

	_ = open[0].Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, err := websocket.Dial(url, "", "http://localhost/")
		if err == nil {
			_ = conn.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the slot was not released after a connection closed: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSTTSessionClosesAnIdleConnectionButNotOneWaitingForRecognition(t *testing.T) {
	cfg := testSTTSessionConfig()
	cfg.ConnIdleTimeout = 200 * time.Millisecond

	idle := dialSTT(t, newScriptedSTTProvider(), cfg)
	if _, closed := idle.drain(3 * time.Second); !closed {
		t.Fatal("a connection with nothing to do must be closed after the idle timeout")
	}

	provider := newScriptedSTTProvider()
	provider.delay = 700 * time.Millisecond // longer than the idle timeout
	busy := dialSTT(t, provider, cfg)
	busy.control(map[string]any{"type": "start", "utterance_id": "slow"})
	busy.tone(300)
	busy.control(map[string]any{"type": "stop"})
	if final := busy.until("final", eventType("final")); final["utterance_id"] != "slow" || final["text"] != "t1" {
		t.Fatalf("waiting for recognition is not idleness: %#v", final)
	}
	if _, closed := busy.drain(3 * time.Second); !closed {
		t.Fatal("once everything is answered the connection is idle and must close")
	}
}

func TestSTTSessionQueueOverflowStaysOrderedWithinTheBound(t *testing.T) {
	// Bounded queue + ordered terminals together: many cancelled/empty utterances
	// behind a slow recognition are answered in order until the bound is hit.
	cfg := limitsTestConfig()
	cfg.MaxQueuedEvents = 8
	provider := newScriptedSTTProvider()
	provider.blockOn = map[int]bool{1: true}
	c := dialSTT(t, provider, cfg)
	c.control(map[string]any{"type": "start", "utterance_id": "head"})
	c.tone(300)
	c.control(map[string]any{"type": "stop"})
	for provider.calls() < 1 {
		time.Sleep(5 * time.Millisecond)
	}
	for i := 0; i < 5; i++ {
		c.control(map[string]any{"type": "start", "utterance_id": fmt.Sprintf("q%d", i)})
		c.audio(sttTestPCM(200, 0)) // silent: owed an empty final, not a recognition
		c.control(map[string]any{"type": "stop"})
	}
	close(provider.release)
	got := c.terminals(6)
	if got[0]["utterance_id"] != "head" {
		t.Fatalf("head terminal = %#v", got[0])
	}
	for i := 0; i < 5; i++ {
		if got[i+1]["utterance_id"] != fmt.Sprintf("q%d", i) || got[i+1]["empty_reason"] != "silent" {
			t.Fatalf("terminal %d = %#v", i+1, got[i+1])
		}
	}
}

func TestSTTSessionStartAndBadFrameFloodsAreBoundedToo(t *testing.T) {
	cfg := limitsTestConfig()
	cfg.MaxQueuedEvents = 16
	for name, flood := range map[string]func(c *sttTestClient) error{
		"start": func(c *sttTestClient) error { return c.trySend(`{"type":"start"}`) },
		"bad wav": func(c *sttTestClient) error {
			return c.trySend(modulestt.PCM16LEToWAV(sttTestPCM(10, 3000), 8000))
		},
		"format-rejected start": func(c *sttTestClient) error { return c.trySend(`{"type":"start","sample_rate":8000}`) },
	} {
		t.Run(name, func(t *testing.T) {
			provider := newScriptedSTTProvider()
			provider.blockOn = map[int]bool{1: true}
			c := dialSTT(t, provider, cfg)
			c.control(map[string]any{"type": "start", "utterance_id": "head"})
			c.tone(300)
			c.control(map[string]any{"type": "stop"})
			for provider.calls() < 1 {
				time.Sleep(5 * time.Millisecond)
			}
			for i := 0; i < 500; i++ {
				if flood(c) != nil {
					break
				}
			}
			events, closed := c.drain(3 * time.Second)
			if !closed {
				t.Fatal("a flood must end with the connection closed")
			}
			if n := countTerminalsFor(events, "head"); n != 1 {
				t.Fatalf("utterance head got %d terminal events, want exactly 1", n)
			}
			if got := len(terminalsOf(events)); got > cfg.MaxQueuedEvents+2 {
				t.Fatalf("%d terminal events; the bound is %d", got, cfg.MaxQueuedEvents)
			}
		})
	}
}

func TestSTTSessionUserCancelWinsOverALaterAbortForTheSameUtterance(t *testing.T) {
	cfg := limitsTestConfig()
	provider := newScriptedSTTProvider()
	provider.blockOn = map[int]bool{1: true}
	c := dialSTT(t, provider, cfg)
	c.control(map[string]any{"type": "start", "utterance_id": "a"})
	c.tone(300)
	c.control(map[string]any{"type": "stop"})
	for provider.calls() < 1 {
		time.Sleep(5 * time.Millisecond)
	}
	c.control(map[string]any{"type": "cancel", "utterance_id": "a"})
	_ = c.trySend(make([]byte, sttMaxFrameBytes(cfg.Limits.WithDefaults())+1))

	events, closed := c.drain(3 * time.Second)
	if !closed {
		t.Fatal("the connection must be closed")
	}
	terminals := terminalsOf(events)
	if countTerminalsFor(events, "a") != 1 || len(terminals) != 1 || terminals[0]["type"] != "cancelled" {
		t.Fatalf("terminals = %#v; a cancel answered first must not be answered again by the abort", terminals)
	}
}
