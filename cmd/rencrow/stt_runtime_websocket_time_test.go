package main

import (
	"fmt"
	"testing"
	"time"

	sttinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/stt"
)

// What the relay does with time: the ping, the liveness deadline, the watchdog
// of a terminal event that never comes, and the idle limit. The clock and the
// upstream are replaced so that 165 seconds take no time.

type timedRelay struct {
	t      *testing.T
	clock  *testRelayClock
	up     *fakeUpstream
	client *relayClient
	sink   *recordSink
	seq    int
}

func startTimedRelay(t *testing.T, tweak func(*sttRelayConfig)) *timedRelay {
	t.Helper()
	clock := newTestRelayClock()
	up := newFakeUpstream(clock)
	sink := &recordSink{}
	cfg := sttRelayConfig{
		Enabled:   true,
		Open:      fixedOpener(up, testReadyJSON),
		Clock:     clock,
		Recorder:  sink,
		RequestID: func() string { return "req-timed" },
	}
	if tweak != nil {
		tweak(&cfg)
	}
	client := dialRelay(t, serveRelay(t, cfg))
	client.ExpectReady()
	return &timedRelay{t: t, clock: clock, up: up, client: client, sink: sink, seq: 1}
}

// deliver sends an STT event with the next hop seq.
func (r *timedRelay) deliver(payload string) {
	r.seq++
	r.up.Deliver(fmt.Sprintf(`{"seq":%d,%s`, r.seq, payload[1:]))
}

// advance moves time forward in steps, with traffic from STT before each step,
// so only the deadline under test can expire.
func (r *timedRelay) advance(total, step time.Duration) {
	for elapsed := time.Duration(0); elapsed < total; elapsed += step {
		r.up.Touch()
		r.clock.Advance(step)
		time.Sleep(10 * time.Millisecond) // let the relay loop take the tick
	}
}

func TestSTTRelayPingsTheStreamEveryTenSecondsOfItsClock(t *testing.T) {
	r := startTimedRelay(t, nil)
	r.advance(5*time.Second, 5*time.Second)
	if got := r.up.Pings(); got != 0 {
		t.Fatalf("pings after 5s = %d, want 0", got)
	}
	r.advance(5*time.Second, 5*time.Second)
	waitUntil(t, "the first ping", func() bool { return r.up.Pings() == 1 })
	r.advance(10*time.Second, 5*time.Second)
	waitUntil(t, "the second ping", func() bool { return r.up.Pings() == 2 })
}

func TestSTTRelayGivesUpOnAStreamThatHasBeenSilentForThirtySeconds(t *testing.T) {
	r := startTimedRelay(t, nil)
	r.deliver(`{"type":"speech_start","utterance_id":"u1"}`)
	r.client.Expect("speech_start")

	r.clock.Advance(29 * time.Second) // 29 s without a byte from STT: still alive
	r.client.NoEvent(150 * time.Millisecond)
	if r.up.IsClosed() {
		t.Fatal("closed after 29 seconds of silence; the limit is 30")
	}

	r.clock.Advance(2 * time.Second) // 31 s
	events := r.client.WaitClosed()
	if len(events) != 2 || events[0]["utterance_id"] != "u1" || events[0]["code"] != "stt_failed" || events[1]["code"] != "stt_failed" {
		t.Fatalf("events = %v, want an error for u1 and then the session error", events)
	}
	if !r.up.IsClosed() {
		t.Fatal("the upstream must be closed")
	}
}

func TestSTTRelayKeepsAStreamAliveThatKeepsSendingBytes(t *testing.T) {
	r := startTimedRelay(t, nil)
	for i := 0; i < 5; i++ { // 100 s in all, never 30 s without a byte
		r.up.Touch()
		r.clock.Advance(20 * time.Second)
		time.Sleep(10 * time.Millisecond)
	}
	r.client.NoEvent(150 * time.Millisecond)
	if r.up.IsClosed() {
		t.Fatal("a stream that answers pings is alive")
	}
}

func TestSTTRelayCompletesEveryWaitingUtteranceWhenATerminalIsOverdue(t *testing.T) {
	r := startTimedRelay(t, nil)
	r.deliver(`{"type":"speech_start","utterance_id":"u1"}`)
	r.client.Expect("speech_start")
	r.deliver(`{"type":"speech_start","utterance_id":"u2"}`)
	r.client.Expect("speech_start")

	// max_utterance_ms + terminal_deadline_ms + 60 s = 30 + 75 + 60 = 165 s from
	// the speech_start, taken from session_ready.limits.
	r.advance(160*time.Second, 10*time.Second)
	r.client.NoEvent(150 * time.Millisecond)
	if r.up.IsClosed() {
		t.Fatal("the watchdog fired early")
	}
	r.advance(10*time.Second, 5*time.Second)

	events := r.client.WaitClosed()
	var ids []string
	for _, e := range events {
		ids = append(ids, fmt.Sprintf("%v/%v", e["code"], e["utterance_id"]))
	}
	if fmt.Sprint(ids) != "[stt_failed/u1 stt_failed/u2 stt_failed/<nil>]" {
		t.Fatalf("events = %v: STT answers within its deadline, so a missing terminal means the stream is unhealthy "+
			"and every waiting utterance is completed, then the session ends", ids)
	}
	if records := r.sink.All(); len(records) != 0 {
		t.Fatalf("terminals CORE made up are not recorded: %+v", records)
	}
}

func TestSTTRelayWatchdogRunsFromTheStopOnceAnUtteranceWasStopped(t *testing.T) {
	r := startTimedRelay(t, nil)
	r.deliver(`{"type":"speech_start","utterance_id":"u1"}`)
	r.client.Expect("speech_start")
	r.client.Control(map[string]any{"type": "stop", "utterance_id": "u1"})
	waitUntil(t, "the stop to reach STT", func() bool { return len(r.up.Sent()) == 1 })

	// terminal_deadline_ms + 60 s = 135 s from the stop.
	r.advance(130*time.Second, 10*time.Second)
	r.client.NoEvent(150 * time.Millisecond)
	r.advance(10*time.Second, 5*time.Second)
	events := r.client.WaitClosed()
	if len(events) != 2 || events[0]["utterance_id"] != "u1" {
		t.Fatalf("events = %v", events)
	}
}

func TestSTTRelayClosesAnIdleConnectionOnlyWhenNothingWaitsForATerminal(t *testing.T) {
	shorter := func(cfg *sttRelayConfig) { cfg.ConnIdleTimeout = 60 * time.Second }

	t.Run("nothing waits", func(t *testing.T) {
		r := startTimedRelay(t, shorter)
		r.advance(50*time.Second, 10*time.Second)
		r.client.NoEvent(100 * time.Millisecond)
		r.advance(20*time.Second, 10*time.Second)
		r.client.WaitClosed()
		if !r.up.IsClosed() {
			t.Fatal("the upstream must be closed with the client")
		}
	})

	t.Run("an utterance waits", func(t *testing.T) {
		r := startTimedRelay(t, shorter)
		r.deliver(`{"type":"speech_start","utterance_id":"u1"}`)
		r.client.Expect("speech_start")
		r.advance(100*time.Second, 10*time.Second)
		r.client.NoEvent(150 * time.Millisecond)
		if r.up.IsClosed() {
			t.Fatal("a connection that waits for a terminal event is not idle")
		}
	})

	t.Run("client frames restart the clock", func(t *testing.T) {
		r := startTimedRelay(t, shorter)
		r.advance(50*time.Second, 10*time.Second)
		r.client.Audio(pcmTone(20))
		waitUntil(t, "the audio to reach STT", func() bool { return len(r.up.Sent()) == 1 })
		r.advance(50*time.Second, 10*time.Second)
		r.client.NoEvent(150 * time.Millisecond)
		if r.up.IsClosed() {
			t.Fatal("50 seconds after the last client frame is not idle yet")
		}
	})
}

func TestSTTRelayEndsTheSessionWhenAnUpstreamWriteFails(t *testing.T) {
	r := startTimedRelay(t, nil)
	r.deliver(`{"type":"speech_start","utterance_id":"u1"}`)
	r.client.Expect("speech_start")
	r.up.mu.Lock()
	r.up.sendErr = fmt.Errorf("broken pipe")
	r.up.mu.Unlock()

	r.client.Audio(pcmTone(20))
	events := r.client.WaitClosed()
	if len(events) != 2 || events[0]["utterance_id"] != "u1" || events[0]["code"] != "stt_failed" {
		t.Fatalf("events = %v", events)
	}
}

func TestSTTRelayAnEarlyTerminalForAStoppedUtteranceIsRelayedAndRecorded(t *testing.T) {
	// The Viewer sends stop after STT closed the utterance by silence: the final
	// arrives first and stop then names an utterance that is gone.
	r := startTimedRelay(t, nil)
	r.client.Control(map[string]any{"type": "start", "utterance_id": "v1"})
	waitUntil(t, "start", func() bool { return len(r.up.Sent()) == 1 })
	r.deliver(`{"type":"speech_start","utterance_id":"v1"}`)
	r.deliver(`{"type":"final","utterance_id":"v1","text":"","empty_reason":"too_short","end_reason":"silence","target_called":false}`)
	r.client.Events(2)

	r.client.Control(map[string]any{"type": "stop"})
	waitUntil(t, "stop", func() bool { return len(r.up.Sent()) == 2 })
	sent := r.up.Sent()[1]
	if sent.Kind != sttinfra.FrameText || string(sent.Data) != `{"type":"utterance.stop","utterance_id":"v1"}` {
		t.Fatalf("the stop must name the finished utterance so STT ignores it: %q", sent.Data)
	}
}
