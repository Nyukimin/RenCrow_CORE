package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	sttinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/stt"
	modulestt "github.com/Nyukimin/RenCrow_CORE/modules/stt"
	"golang.org/x/net/websocket"
)

// --- fake provider ---------------------------------------------------------

type scriptedSTTProvider struct {
	mu        sync.Mutex
	wavs      [][]byte
	text      func(call int) string
	failOn    map[int]error
	delay     time.Duration
	blockOn   map[int]bool // calls that wait for release or ctx cancellation
	release   chan struct{}
	cancelled chan int // call number whose ctx was cancelled
}

func newScriptedSTTProvider() *scriptedSTTProvider {
	return &scriptedSTTProvider{
		text:      func(call int) string { return fmt.Sprintf("t%d", call) },
		release:   make(chan struct{}),
		cancelled: make(chan int, 16),
	}
}

func (p *scriptedSTTProvider) Name() string { return "scripted" }

func (p *scriptedSTTProvider) Health(context.Context) sttinfra.Health {
	return sttinfra.Health{Status: "ok", Provider: p.Name(), Ready: true}
}

func (p *scriptedSTTProvider) Transcribe(ctx context.Context, wav []byte) (sttinfra.Result, error) {
	p.mu.Lock()
	p.wavs = append(p.wavs, append([]byte(nil), wav...))
	call := len(p.wavs)
	block := p.blockOn[call]
	failure := p.failOn[call]
	text := p.text(call)
	p.mu.Unlock()
	if block {
		select {
		case <-p.release:
		case <-ctx.Done():
			p.cancelled <- call
			return sttinfra.Result{}, ctx.Err()
		}
	}
	if p.delay > 0 {
		select {
		case <-time.After(p.delay):
		case <-ctx.Done():
			p.cancelled <- call
			return sttinfra.Result{}, ctx.Err()
		}
	}
	if failure != nil {
		return sttinfra.Result{}, failure
	}
	return sttinfra.Result{Text: text, Language: "ja"}, nil
}

func (p *scriptedSTTProvider) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.wavs)
}

func (p *scriptedSTTProvider) wav(call int) []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.wavs[call-1]
}

// --- client helpers ---------------------------------------------------------

type sttTestClient struct {
	t      *testing.T
	conn   *websocket.Conn
	events chan map[string]any
}

func testSTTSessionConfig() sttSessionConfig {
	return sttSessionConfig{
		Limits: modulestt.UtteranceLimits{
			MinSpeech:           100 * time.Millisecond,
			MaxLength:           2 * time.Second,
			IdleTimeout:         300 * time.Millisecond,
			SilenceAbsThreshold: 220,
		},
		QueueDepth: 8,
	}
}

func dialSTT(t *testing.T, provider sttinfra.Provider, cfg sttSessionConfig) *sttTestClient {
	t.Helper()
	mux := http.NewServeMux()
	registerSTTRoutes(mux, handleSTTWebSocketProviderWithConfig(provider, cfg))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	conn, err := websocket.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/stt", "", "http://localhost/")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c := &sttTestClient{t: t, conn: conn, events: make(chan map[string]any, 256)}
	t.Cleanup(func() { _ = conn.Close() })
	go func() {
		defer close(c.events)
		for {
			var raw string
			if err := websocket.Message.Receive(conn, &raw); err != nil {
				return
			}
			var ev map[string]any
			if err := json.Unmarshal([]byte(raw), &ev); err != nil {
				t.Errorf("undecodable event %q: %v", raw, err)
				return
			}
			c.events <- ev
		}
	}()
	return c
}

func (c *sttTestClient) control(msg map[string]any) {
	c.t.Helper()
	b, _ := json.Marshal(msg)
	if err := websocket.Message.Send(c.conn, string(b)); err != nil {
		c.t.Fatalf("send control %s: %v", b, err)
	}
}

func (c *sttTestClient) audio(payload []byte) {
	c.t.Helper()
	if err := websocket.Message.Send(c.conn, payload); err != nil {
		c.t.Fatalf("send audio: %v", err)
	}
}

func (c *sttTestClient) tone(ms int) { c.audio(sttTestPCM(ms, 3000)) }

func (c *sttTestClient) next(d time.Duration) (map[string]any, bool) {
	select {
	case ev, ok := <-c.events:
		return ev, ok
	case <-time.After(d):
		return nil, false
	}
}

// until returns the first event matching pred, failing the test on timeout.
func (c *sttTestClient) until(what string, pred func(map[string]any) bool) map[string]any {
	c.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ev, ok := c.next(time.Until(deadline))
		if !ok {
			break
		}
		if pred(ev) {
			return ev
		}
	}
	c.t.Fatalf("timed out waiting for %s", what)
	return nil
}

func eventType(typ string) func(map[string]any) bool {
	return func(ev map[string]any) bool { return ev["type"] == typ }
}

func terminalEvent(ev map[string]any) bool {
	return ev["type"] == "final" || ev["type"] == "error" || ev["type"] == "cancelled"
}

// terminals reads n terminal events (final/error/cancelled) in arrival order.
func (c *sttTestClient) terminals(n int) []map[string]any {
	c.t.Helper()
	var out []map[string]any
	for len(out) < n {
		out = append(out, c.until(fmt.Sprintf("terminal event %d of %d", len(out)+1, n), terminalEvent))
	}
	return out
}

func sttTestPCM(ms int, amplitude int16) []byte {
	samples := ms * 16
	out := make([]byte, samples*2)
	for i := 0; i < samples; i++ {
		binary.LittleEndian.PutUint16(out[i*2:], uint16(amplitude))
	}
	return out
}

func asInt(v any) int {
	f, _ := v.(float64)
	return int(f)
}

// --- tests -----------------------------------------------------------------

func TestSTTSessionAnnouncesProtocolAndLimit(t *testing.T) {
	c := dialSTT(t, newScriptedSTTProvider(), testSTTSessionConfig())
	c.until("session_info", eventType("session_info"))
	ready := c.until("ready", eventType("ready"))
	if ready["sample_rate"] != float64(16000) || ready["protocol"] != "utterance.v1" || ready["max_utterance_ms"] != float64(2000) {
		t.Fatalf("ready = %#v", ready)
	}
}

func TestSTTSessionTranscribesOncePerUtteranceNotPerFrame(t *testing.T) {
	provider := newScriptedSTTProvider()
	provider.text = func(int) string { return "こんにちは" }
	c := dialSTT(t, provider, testSTTSessionConfig())

	c.control(map[string]any{"type": "start", "utterance_id": "utt-1", "sample_rate": 16000, "channels": 1, "format": "pcm_s16le"})
	var sent []byte
	for i := 0; i < 10; i++ {
		frame := sttTestPCM(100, int16(1000+i*10))
		sent = append(sent, frame...)
		c.audio(frame)
	}
	time.Sleep(150 * time.Millisecond)
	if got := provider.calls(); got != 0 {
		t.Fatalf("provider was called %d times before the utterance ended; frames must only accumulate", got)
	}

	c.control(map[string]any{"type": "stop", "utterance_id": "utt-1"})
	final := c.until("final", eventType("final"))
	if final["text"] != "こんにちは" || final["utterance_id"] != "utt-1" || final["end_reason"] != "stop" {
		t.Fatalf("final = %#v", final)
	}
	if asInt(final["audio_ms"]) != 1000 {
		t.Fatalf("audio_ms = %v, want 1000", final["audio_ms"])
	}
	if got := provider.calls(); got != 1 {
		t.Fatalf("provider calls = %d, want exactly 1 per utterance", got)
	}

	wav := provider.wav(1)
	if len(wav) != 44+len(sent) || !modulestt.IsWAV(wav) {
		t.Fatalf("WAV length %d, want %d with a RIFF header", len(wav), 44+len(sent))
	}
	if binary.LittleEndian.Uint16(wav[22:24]) != 1 || binary.LittleEndian.Uint32(wav[24:28]) != 16000 || binary.LittleEndian.Uint16(wav[34:36]) != 16 {
		t.Fatalf("WAV is not 16 kHz mono PCM16: %v", wav[:44])
	}
	if binary.LittleEndian.Uint32(wav[40:44]) != uint32(len(sent)) || string(wav[44:]) != string(sent) {
		t.Fatal("WAV must carry every frame of the utterance, in order")
	}
}

func TestSTTSessionEventsCarryUtteranceIDAndIncreasingSeq(t *testing.T) {
	c := dialSTT(t, newScriptedSTTProvider(), testSTTSessionConfig())
	c.control(map[string]any{"type": "start", "utterance_id": "utt-7"})
	c.tone(200)
	c.control(map[string]any{"type": "stop"})

	start := c.until("speech_start", eventType("speech_start"))
	final := c.until("final", eventType("final"))
	if start["utterance_id"] != "utt-7" || final["utterance_id"] != "utt-7" {
		t.Fatalf("speech_start=%#v final=%#v", start, final)
	}
	if asInt(final["seq"]) <= asInt(start["seq"]) || asInt(start["seq"]) < 1 {
		t.Fatalf("seq must increase within the session: start=%v final=%v", start["seq"], final["seq"])
	}
}

func TestSTTSessionLegacyClientWithoutUtteranceIDIsServed(t *testing.T) {
	provider := newScriptedSTTProvider()
	c := dialSTT(t, provider, testSTTSessionConfig())
	// What the shipped Viewer and scripts/stt_e2e_probe.py send.
	c.control(map[string]any{"type": "start", "sample_rate": 16000, "channels": 1, "format": "pcm_s16le"})
	for i := 0; i < 5; i++ {
		c.audio(sttTestPCM(100, 3000))
	}
	c.control(map[string]any{"type": "stop"})

	start := c.until("speech_start", eventType("speech_start"))
	final := c.until("final", eventType("final"))
	id, _ := final["utterance_id"].(string)
	if id == "" || start["utterance_id"] != id {
		t.Fatalf("CORE must assign and report an utterance id: start=%#v final=%#v", start, final)
	}
	if final["text"] != "t1" || provider.calls() != 1 {
		t.Fatalf("final=%#v calls=%d", final, provider.calls())
	}
}

func TestSTTSessionAcceptsRawPCMWithoutStartAndFinalPendingAlias(t *testing.T) {
	provider := newScriptedSTTProvider()
	c := dialSTT(t, provider, testSTTSessionConfig())
	for i := 0; i < 3; i++ {
		c.audio(sttTestPCM(100, 3000))
	}
	c.control(map[string]any{"type": "final_pending"})
	final := c.until("final", eventType("final"))
	if final["text"] != "t1" || provider.calls() != 1 {
		t.Fatalf("final=%#v calls=%d", final, provider.calls())
	}
}

func TestSTTSessionAcceptsWAVFrames(t *testing.T) {
	provider := newScriptedSTTProvider()
	c := dialSTT(t, provider, testSTTSessionConfig())
	c.control(map[string]any{"type": "config", "mimeType": "audio/wav"})
	c.audio(tinyTestWAV())
	c.control(map[string]any{"type": "final_pending"})
	final := c.until("final", eventType("final"))
	if final["text"] != "t1" {
		t.Fatalf("final = %#v", final)
	}
	if got := len(provider.wav(1)); got != 44+32000 {
		t.Fatalf("WAV frame payload must be re-wrapped without its header, got %d bytes", got)
	}
}

func TestSTTSessionSkipsTooShortAndSilentUtterances(t *testing.T) {
	provider := newScriptedSTTProvider()
	c := dialSTT(t, provider, testSTTSessionConfig())

	c.control(map[string]any{"type": "start", "utterance_id": "click"})
	c.audio(sttTestPCM(500, 0))
	c.audio(sttTestPCM(40, 9000))
	c.audio(sttTestPCM(500, 0))
	c.control(map[string]any{"type": "stop"})

	c.control(map[string]any{"type": "start", "utterance_id": "quiet"})
	c.audio(sttTestPCM(800, 0))
	c.control(map[string]any{"type": "stop"})

	got := c.terminals(2)
	if got[0]["utterance_id"] != "click" || got[0]["type"] != "final" || got[0]["text"] != "" || got[0]["empty_reason"] != "too_short" {
		t.Fatalf("short utterance = %#v", got[0])
	}
	if got[1]["utterance_id"] != "quiet" || got[1]["text"] != "" || got[1]["empty_reason"] != "silent" {
		t.Fatalf("silent utterance = %#v", got[1])
	}
	if provider.calls() != 0 {
		t.Fatalf("a fragment or silence must never reach the engine, calls=%d", provider.calls())
	}
}

func TestSTTSessionStopWithNothingActiveReturnsEmptyFinal(t *testing.T) {
	provider := newScriptedSTTProvider()
	c := dialSTT(t, provider, testSTTSessionConfig())
	c.control(map[string]any{"type": "stop"})
	final := c.until("final", eventType("final"))
	if final["text"] != "" || final["empty_reason"] != "no_active_utterance" || provider.calls() != 0 {
		t.Fatalf("final = %#v calls=%d; a client waiting for its final must not hang", final, provider.calls())
	}
}

func TestSTTSessionMaxLengthClosesAndContinuesInNewUtterance(t *testing.T) {
	provider := newScriptedSTTProvider()
	cfg := testSTTSessionConfig()
	cfg.Limits.MaxLength = time.Second
	c := dialSTT(t, provider, cfg)

	c.control(map[string]any{"type": "start", "utterance_id": "long"})
	for i := 0; i < 25; i++ { // 2.5 s
		c.tone(100)
	}
	c.control(map[string]any{"type": "stop"})

	got := c.terminals(3)
	wantReasons := []string{"max_length", "max_length", "stop"}
	wantAudio := []int{1000, 1000, 500}
	seen := map[string]bool{}
	for i, ev := range got {
		id, _ := ev["utterance_id"].(string)
		if ev["type"] != "final" || ev["end_reason"] != wantReasons[i] || asInt(ev["audio_ms"]) != wantAudio[i] || ev["text"] != fmt.Sprintf("t%d", i+1) {
			t.Fatalf("segment %d = %#v", i, ev)
		}
		if id == "" || seen[id] {
			t.Fatalf("each segment is its own utterance, id %q repeated", id)
		}
		seen[id] = true
	}
	if got[0]["utterance_id"] != "long" {
		t.Fatalf("the first segment keeps the client's id, got %v", got[0]["utterance_id"])
	}
	if provider.calls() != 3 {
		t.Fatalf("calls = %d, want one per segment", provider.calls())
	}
}

func TestSTTSessionIdleTimeoutClosesUtteranceWithoutStop(t *testing.T) {
	provider := newScriptedSTTProvider()
	cfg := testSTTSessionConfig()
	cfg.Limits.IdleTimeout = 150 * time.Millisecond
	c := dialSTT(t, provider, cfg)

	c.control(map[string]any{"type": "start", "utterance_id": "abandoned"})
	for i := 0; i < 5; i++ {
		c.tone(100)
	}
	final := c.until("final from idle timeout", eventType("final"))
	if final["utterance_id"] != "abandoned" || final["end_reason"] != "idle_timeout" || final["text"] != "t1" {
		t.Fatalf("final = %#v", final)
	}
}

func TestSTTSessionConsecutiveUtterancesNeverDropAFinal(t *testing.T) {
	provider := newScriptedSTTProvider()
	provider.delay = 200 * time.Millisecond
	c := dialSTT(t, provider, testSTTSessionConfig())

	for i := 1; i <= 3; i++ {
		c.control(map[string]any{"type": "start", "utterance_id": fmt.Sprintf("u%d", i)})
		c.tone(300)
		c.control(map[string]any{"type": "stop"})
	}

	got := c.terminals(3)
	lastSeq := 0
	for i, ev := range got {
		if ev["type"] != "final" || ev["utterance_id"] != fmt.Sprintf("u%d", i+1) || ev["text"] != fmt.Sprintf("t%d", i+1) {
			t.Fatalf("final %d = %#v; every utterance needs its own final, in order", i+1, ev)
		}
		if seq := asInt(ev["seq"]); seq <= lastSeq {
			t.Fatalf("seq not increasing: %d after %d", seq, lastSeq)
		} else {
			lastSeq = seq
		}
	}
}

func TestSTTSessionDropsOnlyWholeUtteranceHallucinations(t *testing.T) {
	provider := newScriptedSTTProvider()
	provider.text = func(call int) string {
		return []string{"ご視聴ありがとうございました。", "また見てね", "ご視聴ありがとうございましたと言って", "今日の予定"}[call-1]
	}
	cfg := testSTTSessionConfig()
	cfg.NoisePhrases = []string{"また見てね"}
	c := dialSTT(t, provider, cfg)
	for i := 0; i < 4; i++ {
		c.control(map[string]any{"type": "start"})
		c.tone(300)
		c.control(map[string]any{"type": "stop"})
	}
	got := c.terminals(4)
	wantText := []string{"", "", "ご視聴ありがとうございましたと言って", "今日の予定"}
	for i, ev := range got {
		if ev["text"] != wantText[i] {
			t.Fatalf("final %d text = %q, want %q (%#v)", i+1, ev["text"], wantText[i], ev)
		}
	}
	if got[0]["empty_reason"] != "filtered" || got[1]["empty_reason"] != "filtered" {
		t.Fatalf("filtered finals must say why: %#v %#v", got[0], got[1])
	}
}

func TestSTTSessionProviderFailureIsTerminalForThatUtteranceOnly(t *testing.T) {
	provider := newScriptedSTTProvider()
	provider.failOn = map[int]error{1: sttinfra.NewError(sttinfra.ErrorProviderFailure, "provider status=502", nil)}
	c := dialSTT(t, provider, testSTTSessionConfig())
	for _, id := range []string{"bad", "good"} {
		c.control(map[string]any{"type": "start", "utterance_id": id})
		c.tone(300)
		c.control(map[string]any{"type": "stop"})
	}
	got := c.terminals(2)
	if got[0]["type"] != "error" || got[0]["utterance_id"] != "bad" || got[0]["code"] != sttinfra.ErrorProviderFailure {
		t.Fatalf("first terminal = %#v", got[0])
	}
	if got[1]["type"] != "final" || got[1]["utterance_id"] != "good" || got[1]["text"] != "t2" {
		t.Fatalf("a failure must not poison the session: %#v", got[1])
	}
}

func TestSTTSessionRejectsUnsupportedAudioFormatInStart(t *testing.T) {
	provider := newScriptedSTTProvider()
	c := dialSTT(t, provider, testSTTSessionConfig())
	c.control(map[string]any{"type": "start", "utterance_id": "bad-format", "sample_rate": 48000})
	ev := c.until("error", eventType("error"))
	if ev["code"] != "unsupported_audio_format" || ev["utterance_id"] != "bad-format" {
		t.Fatalf("error = %#v", ev)
	}
	if provider.calls() != 0 {
		t.Fatal("nothing may be transcribed for a rejected start")
	}
}

func TestSTTSessionCancelControlDiscardsTheActiveUtterance(t *testing.T) {
	provider := newScriptedSTTProvider()
	c := dialSTT(t, provider, testSTTSessionConfig())
	c.control(map[string]any{"type": "start", "utterance_id": "oops"})
	c.tone(500)
	c.control(map[string]any{"type": "cancel", "utterance_id": "oops"})
	ev := c.until("cancelled", eventType("cancelled"))
	if ev["utterance_id"] != "oops" {
		t.Fatalf("cancelled = %#v", ev)
	}

	c.control(map[string]any{"type": "start", "utterance_id": "again"})
	c.tone(300)
	c.control(map[string]any{"type": "stop"})
	final := c.until("final", eventType("final"))
	if final["utterance_id"] != "again" || provider.calls() != 1 {
		t.Fatalf("final=%#v calls=%d; the cancelled audio must not leak into the next utterance", final, provider.calls())
	}
	if got := len(provider.wav(1)); got != 44+300*32 {
		t.Fatalf("WAV carries %d bytes, want only the second utterance", got)
	}
}

func TestSTTSessionCancelControlAbortsAnInFlightUtterance(t *testing.T) {
	provider := newScriptedSTTProvider()
	provider.blockOn = map[int]bool{1: true}
	c := dialSTT(t, provider, testSTTSessionConfig())
	c.control(map[string]any{"type": "start", "utterance_id": "slow"})
	c.tone(300)
	c.control(map[string]any{"type": "stop"})
	for provider.calls() < 1 {
		time.Sleep(5 * time.Millisecond)
	}
	c.control(map[string]any{"type": "cancel", "utterance_id": "slow"})

	select {
	case call := <-provider.cancelled:
		if call != 1 {
			t.Fatalf("cancelled call = %d", call)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel must abort the in-flight transcription")
	}
	ev := c.until("terminal", terminalEvent)
	if ev["type"] != "cancelled" || ev["utterance_id"] != "slow" {
		t.Fatalf("terminal = %#v; a cancelled utterance gets cancelled, never final", ev)
	}
}

func TestSTTSessionDisconnectCancelsInFlightAndQueuedUtterances(t *testing.T) {
	provider := newScriptedSTTProvider()
	provider.blockOn = map[int]bool{1: true}
	c := dialSTT(t, provider, testSTTSessionConfig())
	for _, id := range []string{"a", "b"} {
		c.control(map[string]any{"type": "start", "utterance_id": id})
		c.tone(300)
		c.control(map[string]any{"type": "stop"})
	}
	for provider.calls() < 1 {
		time.Sleep(5 * time.Millisecond)
	}
	_ = c.conn.Close()

	select {
	case call := <-provider.cancelled:
		if call != 1 {
			t.Fatalf("cancelled call = %d", call)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a closed connection must cancel the in-flight transcription")
	}
	time.Sleep(200 * time.Millisecond)
	if provider.calls() != 1 {
		t.Fatalf("queued utterance reached the engine after disconnect, calls=%d", provider.calls())
	}
}

func TestSTTSessionQueueOverflowIsAnOrderedExplicitError(t *testing.T) {
	provider := newScriptedSTTProvider()
	provider.blockOn = map[int]bool{1: true}
	cfg := testSTTSessionConfig()
	cfg.QueueDepth = 2
	c := dialSTT(t, provider, cfg)
	for i := 1; i <= 4; i++ {
		c.control(map[string]any{"type": "start", "utterance_id": fmt.Sprintf("u%d", i)})
		c.tone(300)
		c.control(map[string]any{"type": "stop"})
	}
	for provider.calls() < 1 {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond) // let u3/u4 reach the queue limit
	close(provider.release)

	got := c.terminals(4)
	wantType := []string{"final", "final", "error", "error"}
	for i, ev := range got {
		if ev["type"] != wantType[i] || ev["utterance_id"] != fmt.Sprintf("u%d", i+1) {
			t.Fatalf("terminal %d = %#v; terminals must follow utterance order", i+1, ev)
		}
	}
	if got[2]["code"] != "stt_busy" || got[3]["code"] != "stt_busy" {
		t.Fatalf("overflow must say stt_busy: %#v %#v", got[2], got[3])
	}
	if provider.calls() != 2 {
		t.Fatalf("calls = %d, want only the two admitted utterances", provider.calls())
	}
}

func TestSTTSessionWithoutProviderReportsError(t *testing.T) {
	mux := http.NewServeMux()
	registerSTTRoutes(mux, handleSTTWebSocketProviderWithConfig(nil, testSTTSessionConfig()))
	server := httptest.NewServer(mux)
	defer server.Close()
	conn, err := websocket.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/stt", "", "http://localhost/")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var raw string
	if err := websocket.Message.Receive(conn, &raw); err != nil || !strings.Contains(raw, `"error"`) {
		t.Fatalf("first event = %q err=%v", raw, err)
	}
}

// --- review follow-ups: one terminal per utterance, ids, ordering ------------------------------

// quiet asserts that no event arrives for d.
func (c *sttTestClient) quiet(d time.Duration) {
	c.t.Helper()
	if ev, ok := c.next(d); ok {
		c.t.Fatalf("unexpected event: %#v", ev)
	}
}

func TestSTTSessionUnsupportedWAVFrameEndsTheActiveUtteranceExactlyOnce(t *testing.T) {
	provider := newScriptedSTTProvider()
	c := dialSTT(t, provider, testSTTSessionConfig())
	c.control(map[string]any{"type": "start", "utterance_id": "x"})
	c.tone(300)
	c.audio(modulestt.PCM16LEToWAV(sttTestPCM(100, 3000), 48000))

	ev := c.until("terminal", terminalEvent)
	if ev["type"] != "error" || ev["code"] != "unsupported_audio_format" || ev["utterance_id"] != "x" {
		t.Fatalf("terminal = %#v", ev)
	}
	// The utterance is over: a later stop must not produce a second terminal for "x".
	c.control(map[string]any{"type": "stop"})
	next := c.until("terminal", terminalEvent)
	if next["utterance_id"] == "x" {
		t.Fatalf("utterance x got a second terminal event: %#v", next)
	}
	if next["type"] != "final" || next["empty_reason"] != "no_active_utterance" || provider.calls() != 0 {
		t.Fatalf("stop after the failed utterance = %#v calls=%d", next, provider.calls())
	}
}

func TestSTTSessionRejectedStartDoesNotDisturbTheActiveUtterance(t *testing.T) {
	provider := newScriptedSTTProvider()
	c := dialSTT(t, provider, testSTTSessionConfig())
	c.control(map[string]any{"type": "start", "utterance_id": "a"})
	c.tone(300)
	c.control(map[string]any{"type": "start", "utterance_id": "a", "sample_rate": 48000})
	rejected := c.until("error", eventType("error"))
	if rejected["code"] != "unsupported_audio_format" {
		t.Fatalf("rejected = %#v", rejected)
	}
	if _, has := rejected["utterance_id"]; has {
		t.Fatalf("the rejection must not borrow the id of the live utterance: %#v", rejected)
	}
	c.control(map[string]any{"type": "stop"})
	final := c.until("final", eventType("final"))
	if final["utterance_id"] != "a" || final["text"] != "t1" {
		t.Fatalf("final = %#v", final)
	}
}

func TestSTTSessionStartInsideAnUtteranceClosesItAsRestart(t *testing.T) {
	c := dialSTT(t, newScriptedSTTProvider(), testSTTSessionConfig())
	c.control(map[string]any{"type": "start", "utterance_id": "a"})
	c.tone(300)
	c.control(map[string]any{"type": "start", "utterance_id": "b"})
	c.tone(300)
	c.control(map[string]any{"type": "stop"})
	got := c.terminals(2)
	if got[0]["utterance_id"] != "a" || got[0]["end_reason"] != "restart" || got[1]["utterance_id"] != "b" || got[1]["end_reason"] != "stop" {
		t.Fatalf("terminals = %#v", got)
	}
}

func TestSTTSessionStopNamingTheOriginalIDClosesTheContinuationOfASplitUtterance(t *testing.T) {
	cfg := testSTTSessionConfig()
	cfg.Limits.MaxLength = time.Second
	cfg.Limits.IdleTimeout = 30 * time.Second // the stop, not the idle valve, must close it
	c := dialSTT(t, newScriptedSTTProvider(), cfg)
	c.control(map[string]any{"type": "start", "utterance_id": "long"})
	for i := 0; i < 25; i++ {
		c.tone(100)
	}
	c.control(map[string]any{"type": "stop", "utterance_id": "long"})
	got := c.terminals(3)
	if got[2]["end_reason"] != "stop" {
		t.Fatalf("third segment = %#v; stop(\"long\") must reach the continuation", got[2])
	}
}

func TestSTTSessionStopForAnotherUtteranceIDIsIgnored(t *testing.T) {
	c := dialSTT(t, newScriptedSTTProvider(), testSTTSessionConfig())
	c.control(map[string]any{"type": "start", "utterance_id": "a"})
	c.tone(300)
	c.control(map[string]any{"type": "stop", "utterance_id": "zzz"})
	c.until("speech_start", eventType("speech_start"))
	c.quiet(100 * time.Millisecond)
	c.control(map[string]any{"type": "stop"})
	if final := c.until("final", eventType("final")); final["utterance_id"] != "a" {
		t.Fatalf("final = %#v", final)
	}
}

func TestSTTSessionSilentAudioWithoutStartOpensNoUtterance(t *testing.T) {
	provider := newScriptedSTTProvider()
	c := dialSTT(t, provider, testSTTSessionConfig())
	for i := 0; i < 5; i++ {
		c.audio(sttTestPCM(100, 0))
	}
	c.control(map[string]any{"type": "stop"})
	final := c.until("terminal", terminalEvent)
	if final["empty_reason"] != "no_active_utterance" || provider.calls() != 0 {
		t.Fatalf("terminal = %#v calls=%d; silence alone must not open an utterance", final, provider.calls())
	}
	if _, has := final["utterance_id"]; has {
		t.Fatalf("no utterance existed: %#v", final)
	}
}

func TestSTTSessionCancellingAQueuedUtteranceNeverReachesTheEngine(t *testing.T) {
	provider := newScriptedSTTProvider()
	provider.blockOn = map[int]bool{1: true}
	c := dialSTT(t, provider, testSTTSessionConfig())
	for _, id := range []string{"a", "b"} {
		c.control(map[string]any{"type": "start", "utterance_id": id})
		c.tone(300)
		c.control(map[string]any{"type": "stop"})
	}
	for provider.calls() < 1 {
		time.Sleep(5 * time.Millisecond)
	}
	c.control(map[string]any{"type": "cancel", "utterance_id": "b"})
	time.Sleep(50 * time.Millisecond)
	close(provider.release)
	got := c.terminals(2)
	if got[0]["type"] != "final" || got[0]["utterance_id"] != "a" || got[1]["type"] != "cancelled" || got[1]["utterance_id"] != "b" {
		t.Fatalf("terminals = %#v", got)
	}
	if provider.calls() != 1 {
		t.Fatalf("the cancelled queued utterance reached the engine, calls=%d", provider.calls())
	}
}

func TestSTTSessionAssignedIDsNeverCollideWithClientChosenOnes(t *testing.T) {
	provider := newScriptedSTTProvider()
	provider.blockOn = map[int]bool{1: true}
	c := dialSTT(t, provider, testSTTSessionConfig())
	c.control(map[string]any{"type": "start", "utterance_id": "srv-1"})
	c.tone(300)
	c.control(map[string]any{"type": "stop"})
	for provider.calls() < 1 {
		time.Sleep(5 * time.Millisecond)
	}
	for i := 0; i < 3; i++ { // no start: CORE assigns the id
		c.tone(100)
	}
	c.control(map[string]any{"type": "stop"})
	time.Sleep(50 * time.Millisecond)
	close(provider.release)
	got := c.terminals(2)
	if got[0]["utterance_id"] != "srv-1" || got[1]["utterance_id"] == "srv-1" || got[1]["utterance_id"] == nil {
		t.Fatalf("terminals = %#v", got)
	}
}
