package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sttinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/stt"
	"golang.org/x/net/websocket"
)

// /stt relays the audio of a client connection to the RenCrow_STT audio stream
// and the stream's events back. These tests run the relay against a stand-in
// that speaks the real stream protocol. What is under test is the relay's
// boundary: what crosses it, in which form, and that every announced utterance
// ends with exactly one terminal event.

func TestSTTRelayOpensTheStreamBeforeReadyAndAnnouncesWhatSTTAnnounced(t *testing.T) {
	fake := startFakeSTTServer(t)
	fake.Limits = `{"max_utterance_ms":12000,"max_frame_bytes":65536,"queue_depth":8,"terminal_deadline_ms":75000}`
	url := serveRelay(t, realStreamConfig(fake, nil, nil))

	client := dialRelay(t, url)
	conn := fake.Accept()

	start := fake.SessionStarts()[0]
	for key, want := range map[string]any{
		"type": "session.start", "protocol": "stt.stream.v1", "sample_rate": float64(16000),
		"channels": float64(1), "format": "pcm_s16le", "request_id": "req-test",
	} {
		if start[key] != want {
			t.Fatalf("session.start[%s] = %#v, want %#v (message %#v)", key, start[key], want, start)
		}
	}
	if _, has := start["options"]; has {
		t.Fatalf("CORE sends no options; the defaults are STT's own: %#v", start)
	}
	if origins := fake.Origins(); len(origins) != 1 || origins[0] != "http://rencrow-core.invalid" {
		t.Fatalf("CORE must send the fixed Origin the stream accepts, got %v", origins)
	}
	if conn.RequestID != "req-test" {
		t.Fatalf("request id = %q", conn.RequestID)
	}

	info := client.Expect("session_info")
	if info["provider"] != "rencrow_stt" || info["seq"] != float64(1) {
		t.Fatalf("session_info = %#v", info)
	}
	ready := client.Expect("ready")
	if ready["protocol"] != "utterance.v1" || ready["sample_rate"] != float64(16000) || ready["seq"] != float64(2) {
		t.Fatalf("ready = %#v", ready)
	}
	if ready["max_utterance_ms"] != float64(12000) {
		t.Fatalf("max_utterance_ms must come from session_ready.limits, not from CORE: %#v", ready)
	}
}

func TestSTTRelayReportsAStreamThatCannotBeOpenedBeforeReady(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		wantCode string
	}{
		{"STT is full", http.StatusServiceUnavailable, "stt_busy"},
		{"an STT without the stream", http.StatusNotFound, "stt_failed"},
		{"an Origin the stream refuses", http.StatusForbidden, "stt_failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := startFakeSTTServer(t)
			fake.RefuseStatus = tt.status
			logs := &relayLog{}
			url := serveRelay(t, realStreamConfig(fake, nil, logs))

			client := dialRelay(t, url)
			event := client.Expect("error")

			if event["code"] != tt.wantCode {
				t.Fatalf("error = %#v, want code %s", event, tt.wantCode)
			}
			if _, has := event["utterance_id"]; has {
				t.Fatalf("no utterance exists yet: the error is session-level: %#v", event)
			}
			if text := fmt.Sprint(event); strings.Contains(text, "127.0.0.1") || strings.Contains(text, "http") {
				t.Fatalf("the client must not learn where STT is: %#v", event)
			}
			client.WaitClosed()
		})
	}
}

func TestSTTRelayReportsAStreamThatNobodyListensOn(t *testing.T) {
	fake := startFakeSTTServer(t)
	cfg := realStreamConfig(fake, nil, nil)
	cfg.Open = streamClientOpener(sttinfra.StreamClient{URL: "ws://127.0.0.1:1/v1/audio/stream", DialTimeout: time.Second})
	client := dialRelay(t, serveRelay(t, cfg))
	event := client.Expect("error")
	if event["code"] != "stt_failed" {
		t.Fatalf("error = %#v", event)
	}
	client.WaitClosed()
}

func TestSTTRelayRefusesAStreamOfAnotherProtocol(t *testing.T) {
	fake := startFakeSTTServer(t)
	fake.Protocol = "stt.stream.v9"
	client := dialRelay(t, serveRelay(t, realStreamConfig(fake, nil, nil)))
	event := client.Expect("error")
	if event["code"] != "stt_failed" {
		t.Fatalf("error = %#v", event)
	}
	if !fake.Accept().Disconnected() {
		t.Fatal("CORE must close a stream it cannot rely on")
	}
	client.WaitClosed()
}

func TestSTTRelayWithSTTDisabledReportsItAndClosesWithoutDialling(t *testing.T) {
	fake := startFakeSTTServer(t)
	cfg := realStreamConfig(fake, nil, nil)
	cfg.Enabled = false
	client := dialRelay(t, serveRelay(t, cfg))
	event := client.Expect("error")
	if event["error"] != "stt provider is not configured" {
		t.Fatalf("error = %#v", event)
	}
	client.WaitClosed()
	select {
	case <-fake.conns:
		t.Fatal("a disabled STT must not open a stream")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestSTTRelayRelaysAViewerUtteranceWithItsKindsIntact(t *testing.T) {
	fake := startFakeSTTServer(t)
	client := dialRelay(t, serveRelay(t, realStreamConfig(fake, nil, nil)))
	conn := fake.Accept()
	client.ExpectReady()

	client.Control(map[string]any{"type": "start", "utterance_id": "v1-1", "sample_rate": 16000, "channels": 1, "format": "pcm_s16le"})
	audio := pcmTone(100)
	client.Audio(audio)
	client.Control(map[string]any{"type": "stop"})

	if msg := conn.NextControl(t); msg["type"] != "utterance.start" || msg["utterance_id"] != "v1-1" {
		t.Fatalf("start = %#v", msg)
	}
	frame := conn.Next(t)
	if frame.Kind != sttinfra.FrameBinary || string(frame.Data) != string(audio) {
		t.Fatalf("audio must cross as the same binary frame: kind %d, %d bytes", frame.Kind, len(frame.Data))
	}
	// The Viewer's stop has no id: CORE adds the id of the last start.
	if msg := conn.NextControl(t); msg["type"] != "utterance.stop" || msg["utterance_id"] != "v1-1" {
		t.Fatalf("stop = %#v", msg)
	}
}

func TestSTTRelayRelaysAPortalStreamWithoutAnyStart(t *testing.T) {
	fake := startFakeSTTServer(t)
	client := dialRelay(t, serveRelay(t, realStreamConfig(fake, nil, nil)))
	conn := fake.Accept()
	client.ExpectReady()

	client.Control(map[string]any{"type": "start", "sample_rate": 16000, "channels": 1, "format": "pcm_s16le"})
	client.Audio(pcmTone(20))
	client.Control(map[string]any{"type": "final_pending"})

	// The id-less start is not forwarded: it would open an empty utterance.
	if frame := conn.Next(t); frame.Kind != sttinfra.FrameBinary {
		t.Fatalf("the first thing STT must see is the audio, got kind %d %q", frame.Kind, frame.Data)
	}
	msg := conn.NextControl(t)
	if msg["type"] != "utterance.stop" {
		t.Fatalf("final_pending is stop: %#v", msg)
	}
	if _, has := msg["utterance_id"]; has {
		t.Fatalf("PORTAL has no id to name: %#v", msg)
	}
}

func TestSTTRelayDropsWhatItMustNotForwardAndKeepsServing(t *testing.T) {
	fake := startFakeSTTServer(t)
	logs := &relayLog{}
	client := dialRelay(t, serveRelay(t, realStreamConfig(fake, nil, logs)))
	conn := fake.Accept()
	client.ExpectReady()

	client.Text(`{"type":"config","mimeType":"audio/wav"}`)                  // a legacy control
	client.Text(`{"type":`)                                                  // not JSON
	client.Text("hello")                                                     // not a control at all
	client.Text(`{"type":"stop","pad":"` + strings.Repeat("x", 5000) + `"}`) // over the control size
	client.Text(`{"type":"session.start","protocol":"stt.stream.v1"}`)       // an upstream message, not a client one
	client.Text(`{"type":"utterance.stop","utterance_id":"x"}`)              // likewise
	client.Audio(nil)                                                        // an empty frame
	marker := pcmTone(40)
	client.Audio(marker)

	frame := conn.Next(t)
	if frame.Kind != sttinfra.FrameBinary || string(frame.Data) != string(marker) {
		t.Fatalf("only the marker may have reached STT, got kind %d %q", frame.Kind, frame.Data)
	}
	conn.Quiet(t, 100*time.Millisecond)
}

func TestSTTRelayRejectsAWAVFrameAsASessionLevelError(t *testing.T) {
	fake := startFakeSTTServer(t)
	client := dialRelay(t, serveRelay(t, realStreamConfig(fake, nil, nil)))
	conn := fake.Accept()
	client.ExpectReady()

	wav := make([]byte, 64)
	copy(wav[0:4], "RIFF")
	copy(wav[8:12], "WAVE")
	client.Audio(wav)

	event := client.Expect("error")
	if event["code"] != "unsupported_audio_format" {
		t.Fatalf("error = %#v", event)
	}
	if _, has := event["utterance_id"]; has {
		t.Fatalf("CORE does not know which utterance a frame belongs to: %#v", event)
	}
	// The connection stays usable, and nothing of the WAV reached STT.
	pcm := pcmTone(20)
	client.Audio(pcm)
	if frame := conn.Next(t); string(frame.Data) != string(pcm) {
		t.Fatalf("the WAV frame leaked upstream: %d bytes", len(frame.Data))
	}
}

func TestSTTRelayRejectsAStartWithAnUnsupportedFormatWithoutNamingAnUtterance(t *testing.T) {
	fake := startFakeSTTServer(t)
	client := dialRelay(t, serveRelay(t, realStreamConfig(fake, nil, nil)))
	conn := fake.Accept()
	client.ExpectReady()

	client.Control(map[string]any{"type": "start", "utterance_id": "live", "sample_rate": 48000})
	event := client.Expect("error")
	if event["code"] != "unsupported_audio_format" {
		t.Fatalf("error = %#v", event)
	}
	if _, has := event["utterance_id"]; has {
		t.Fatalf("a rejected start must not borrow the id of a live utterance: %#v", event)
	}
	conn.Quiet(t, 100*time.Millisecond)
}

func TestSTTRelaySplitsAFrameLargerThanSTTAccepts(t *testing.T) {
	fake := startFakeSTTServer(t)
	fake.Limits = `{"max_utterance_ms":30000,"max_frame_bytes":1024,"queue_depth":8,"terminal_deadline_ms":75000}`
	client := dialRelay(t, serveRelay(t, realStreamConfig(fake, nil, nil)))
	conn := fake.Accept()
	client.ExpectReady()

	// The Viewer sends what it gathered while the connection opened as one frame.
	payload := make([]byte, 3000)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	client.Audio(payload)

	var got []byte
	var sizes []int
	for len(got) < len(payload) {
		frame := conn.Next(t)
		if frame.Kind != sttinfra.FrameBinary || len(frame.Data) > 1024 {
			t.Fatalf("kind %d, %d bytes: every part must be binary and within STT's limit", frame.Kind, len(frame.Data))
		}
		sizes = append(sizes, len(frame.Data))
		got = append(got, frame.Data...)
	}
	if string(got) != string(payload) {
		t.Fatal("splitting lost or reordered bytes")
	}
	if fmt.Sprint(sizes) != "[1024 1024 952]" {
		t.Fatalf("part sizes = %v", sizes)
	}
}

func TestSTTRelayStampsEveryEventWithItsOwnSeqNotSTTs(t *testing.T) {
	fake := startFakeSTTServer(t)
	client := dialRelay(t, serveRelay(t, realStreamConfig(fake, nil, nil)))
	conn := fake.Accept()
	client.ExpectReady() // seq 1 and 2

	conn.Send(`{"type":"speech_start","utterance_id":"u1","offset_ms":0}`)
	conn.Send(`{"type":"partial","utterance_id":"u1","text":"こん"}`)
	conn.Send(`{"type":"final","utterance_id":"u1","text":"こんにちは","end_reason":"silence","audio_ms":900,"offset_ms":0,"end_offset_ms":900,"target_called":true}`)

	events := client.Events(3)
	wantTypes := []string{"speech_start", "partial", "final"}
	for i, event := range events {
		if event["type"] != wantTypes[i] || event["utterance_id"] != "u1" {
			t.Fatalf("event %d = %#v", i, event)
		}
		if event["seq"] != float64(3+i) {
			t.Fatalf("event %d seq = %v: CORE owns the seq space (STT's own started at 10)", i, event["seq"])
		}
	}
	final := events[2]
	if final["text"] != "こんにちは" || final["end_reason"] != "silence" || final["audio_ms"] != float64(900) ||
		final["end_offset_ms"] != float64(900) || final["target_called"] != true {
		t.Fatalf("the payload must cross as STT wrote it: %#v", final)
	}
}

func TestSTTRelayDeliversExactlyOneTerminalPerUtteranceInSTTsOrder(t *testing.T) {
	fake := startFakeSTTServer(t)
	client := dialRelay(t, serveRelay(t, realStreamConfig(fake, nil, nil)))
	conn := fake.Accept()
	client.ExpectReady()

	// STT recognises one utterance at a time, so the next speech_start can come
	// before the previous final.
	conn.Send(`{"type":"speech_start","utterance_id":"u1"}`)
	conn.Send(`{"type":"speech_start","utterance_id":"u2"}`)
	conn.Send(`{"type":"final","utterance_id":"u1","text":"one","target_called":true}`)
	conn.Send(`{"type":"final","utterance_id":"u1","text":"one again","target_called":true}`) // a duplicate
	conn.Send(`{"type":"error","utterance_id":"u2","code":"STT_TARGET_FAILED","error":"x","target_called":true}`)
	conn.Send(`{"type":"cancelled","utterance_id":"u2","target_called":false}`) // a second terminal for u2
	conn.Send(`{"type":"partial","utterance_id":"u1","text":"late"}`)           // after its terminal
	conn.Send(`{"type":"speech_start","utterance_id":"u3"}`)

	events := client.Events(5)
	var got []string
	for _, e := range events {
		got = append(got, fmt.Sprintf("%v:%v", e["type"], e["utterance_id"]))
	}
	want := "[speech_start:u1 speech_start:u2 final:u1 error:u2 speech_start:u3]"
	if fmt.Sprint(got) != want {
		t.Fatalf("events = %v, want %s", got, want)
	}
	client.NoEvent(100 * time.Millisecond)
}

func TestSTTRelayRelaysTerminalsWithoutAnUtteranceAndDropsEventsItDoesNotKnow(t *testing.T) {
	fake := startFakeSTTServer(t)
	logs := &relayLog{}
	client := dialRelay(t, serveRelay(t, realStreamConfig(fake, nil, logs)))
	conn := fake.Accept()
	client.ExpectReady()

	conn.Send(`{"type":"status","text":"retrying"}`)                 // not in the allow-list
	conn.Send(`{"type":"draft","text":"x"}`)                         // not in the allow-list
	conn.Send(`{"type":"session_ready","protocol":"stt.stream.v1"}`) // a second one
	conn.Send(`{"type":"final","text":"","empty_reason":"no_active_utterance","end_reason":"stop","target_called":false}`)
	conn.Send(`{"type":"error","code":"unknown_message_type","error":"x"}`)

	events := client.Events(2)
	if events[0]["type"] != "final" || events[0]["empty_reason"] != "no_active_utterance" {
		t.Fatalf("events[0] = %#v", events[0])
	}
	if events[1]["type"] != "error" || events[1]["code"] != "unknown_message_type" {
		t.Fatalf("events[1] = %#v", events[1])
	}
	client.NoEvent(100 * time.Millisecond)
	if text := logs.Text(); !strings.Contains(text, "status") {
		t.Fatalf("a dropped event type must be logged:\n%s", text)
	}
}

func TestSTTRelayRecordsTheTerminalsSTTOwnsWhenTheTargetWasCalled(t *testing.T) {
	fake := startFakeSTTServer(t)
	sink := &recordSink{}
	client := dialRelay(t, serveRelay(t, realStreamConfig(fake, sink, nil)))
	conn := fake.Accept()
	client.ExpectReady()

	for _, id := range []string{"u1", "u2", "u3", "u4"} {
		conn.Send(`{"type":"speech_start","utterance_id":"` + id + `"}`)
	}
	conn.Send(`{"type":"final","utterance_id":"u1","text":"a","audio_ms":900,"queue_ms":2,"processing_ms":150,"target_called":true}`)
	conn.Send(`{"type":"final","utterance_id":"u2","text":"","empty_reason":"too_short","target_called":false}`)
	conn.Send(`{"type":"error","utterance_id":"u3","code":"STT_TARGET_FAILED","error":"x","target_called":true}`)
	conn.Send(`{"type":"final","utterance_id":"u4","text":"d"}`) // the flag is missing
	client.Events(8)

	records := sink.WaitFor(t, 3)
	if len(records) != 3 {
		t.Fatalf("records = %+v, want u1, u3 and u4 only", records)
	}
	byID := map[string]sttinfra.UtteranceRecord{}
	for _, r := range records {
		byID[r.UtteranceID] = r
	}
	if r := byID["u1"]; r.RequestID != "req-test" || r.Outcome != sttinfra.OutcomeFinal || r.AudioMS != 900 || r.QueueMS != 2 || r.ProcessingMS != 150 || r.TargetCalled != sttinfra.TargetCalledTrue {
		t.Fatalf("u1 = %+v", r)
	}
	if r := byID["u3"]; r.Outcome != sttinfra.OutcomeError || r.Code != "STT_TARGET_FAILED" {
		t.Fatalf("u3 = %+v", r)
	}
	if r := byID["u4"]; r.TargetCalled != sttinfra.TargetCalledMissing {
		t.Fatalf("a missing flag errs on the side of recording: u4 = %+v", r)
	}
	if _, has := byID["u2"]; has {
		t.Fatal("an utterance whose target was never called has no external effect to record")
	}
}

func TestSTTRelayCompletesEveryWaitingUtteranceWhenSTTGoesAway(t *testing.T) {
	fake := startFakeSTTServer(t)
	sink := &recordSink{}
	client := dialRelay(t, serveRelay(t, realStreamConfig(fake, sink, nil)))
	conn := fake.Accept()
	client.ExpectReady()

	conn.Send(`{"type":"speech_start","utterance_id":"u1"}`)
	conn.Send(`{"type":"speech_start","utterance_id":"u2"}`)
	conn.Send(`{"type":"final","utterance_id":"u1","text":"one","target_called":true}`)
	client.Events(3)

	conn.Drop() // a crash: no terminal for u2 will ever come

	events := client.WaitClosed()
	if len(events) != 2 {
		t.Fatalf("events = %v, want an error for u2 and then the session error", events)
	}
	if events[0]["type"] != "error" || events[0]["code"] != "stt_failed" || events[0]["utterance_id"] != "u2" {
		t.Fatalf("the waiting utterance gets its own terminal: %#v", events[0])
	}
	if events[1]["type"] != "error" || events[1]["code"] != "stt_failed" {
		t.Fatalf("then the session ends with a session-level error: %#v", events[1])
	}
	if _, has := events[1]["utterance_id"]; has {
		t.Fatalf("the session error names no utterance: %#v", events[1])
	}
	// What CORE completed itself is not evidence of a target call.
	for _, r := range sink.All() {
		if r.UtteranceID == "u2" {
			t.Fatalf("a terminal CORE made up must not be recorded: %+v", r)
		}
	}
}

func TestSTTRelayClosesTheStreamWhenSTTBreaksItsSequence(t *testing.T) {
	fake := startFakeSTTServer(t)
	client := dialRelay(t, serveRelay(t, realStreamConfig(fake, nil, nil)))
	conn := fake.Accept()
	client.ExpectReady()

	conn.SendWithSeq(`{"type":"speech_start","utterance_id":"u1"}`, 20)
	client.Expect("speech_start")
	conn.SendWithSeq(`{"type":"partial","utterance_id":"u1","text":"x"}`, 20) // does not increase

	events := client.WaitClosed()
	if len(events) != 2 || events[0]["utterance_id"] != "u1" || events[0]["code"] != "stt_failed" {
		t.Fatalf("events = %v", events)
	}
	if !conn.Disconnected() {
		t.Fatal("CORE must close the stream it can no longer trust")
	}
}

func TestSTTRelayClosesTheStreamOnAFrameItCannotUse(t *testing.T) {
	cases := map[string]func(c *fakeSTTConn){
		"a binary frame from STT":        func(c *fakeSTTConn) { c.SendRaw(sttinfra.FrameBinary, []byte{1, 2, 3}) },
		"an event without seq":           func(c *fakeSTTConn) { c.SendWithSeq(`{"type":"partial","text":"x"}`, -1) },
		"text that is not a JSON object": func(c *fakeSTTConn) { c.SendRaw(sttinfra.FrameText, []byte("not json")) },
	}
	for name, trigger := range cases {
		t.Run(name, func(t *testing.T) {
			fake := startFakeSTTServer(t)
			client := dialRelay(t, serveRelay(t, realStreamConfig(fake, nil, nil)))
			conn := fake.Accept()
			client.ExpectReady()
			trigger(conn)
			events := client.WaitClosed()
			if len(events) == 0 || events[len(events)-1]["code"] != "stt_failed" {
				t.Fatalf("events = %v", events)
			}
		})
	}
}

func TestSTTRelayClosesTheStreamWhenTooManyUtterancesWait(t *testing.T) {
	fake := startFakeSTTServer(t)
	cfg := realStreamConfig(fake, nil, nil)
	cfg.MaxPending = 3
	logs := &relayLog{}
	cfg.Logf = logs.Logf
	client := dialRelay(t, serveRelay(t, cfg))
	conn := fake.Accept()
	client.ExpectReady()

	for i := 1; i <= 4; i++ {
		conn.Send(fmt.Sprintf(`{"type":"speech_start","utterance_id":"u%d"}`, i))
	}
	events := client.WaitClosed()
	var errorsFor []string
	for _, e := range events {
		if e["type"] == "error" && e["utterance_id"] != nil {
			errorsFor = append(errorsFor, fmt.Sprint(e["utterance_id"]))
		}
	}
	if fmt.Sprint(errorsFor) != "[u1 u2 u3]" {
		t.Fatalf("every waiting utterance gets its terminal, got %v from %v", errorsFor, events)
	}
	if !strings.Contains(logs.Text(), "WARN") {
		t.Fatalf("a broken STT is a WARN before the relay closes:\n%s", logs.Text())
	}
}

func TestSTTRelayAbortsWaitingUtterancesWhenTheClientLeaves(t *testing.T) {
	fake := startFakeSTTServer(t)
	sink := &recordSink{}
	client := dialRelay(t, serveRelay(t, realStreamConfig(fake, sink, nil)))
	conn := fake.Accept()
	client.ExpectReady()

	conn.Send(`{"type":"speech_start","utterance_id":"u1"}`)
	conn.Send(`{"type":"speech_start","utterance_id":"u2"}`)
	conn.Send(`{"type":"final","utterance_id":"u1","text":"x","target_called":true}`)
	client.Events(3)
	_ = client.ws.Close()

	if !conn.Disconnected() {
		t.Fatal("CORE must close the stream when the client leaves")
	}
	records := sink.WaitFor(t, 2)
	var aborted []sttinfra.UtteranceRecord
	for _, r := range records {
		if r.Outcome == sttinfra.OutcomeClientAborted {
			aborted = append(aborted, r)
		}
	}
	if len(aborted) != 1 || aborted[0].UtteranceID != "u2" || aborted[0].TargetCalled != sttinfra.TargetCalledUnknown || aborted[0].RequestID != "req-test" {
		t.Fatalf("u2 had no terminal: it is recorded as interrupted by the client's disconnect, target_called unknown. records = %+v", records)
	}
}

func TestSTTRelayLeavesNoRecordWhenTheClientLeavesBetweenUtterances(t *testing.T) {
	fake := startFakeSTTServer(t)
	sink := &recordSink{}
	client := dialRelay(t, serveRelay(t, realStreamConfig(fake, sink, nil)))
	conn := fake.Accept()
	client.ExpectReady()
	_ = client.ws.Close()
	if !conn.Disconnected() {
		t.Fatal("CORE must close the stream when the client leaves")
	}
	time.Sleep(100 * time.Millisecond)
	if records := sink.All(); len(records) != 0 {
		t.Fatalf("nothing was waiting: %+v", records)
	}
}

func TestSTTRelayAnswersAnOversizedClientFrameWithALimitErrorPerWaitingUtterance(t *testing.T) {
	fake := startFakeSTTServer(t)
	sink := &recordSink{}
	cfg := realStreamConfig(fake, sink, nil)
	cfg.MaxClientFrameBytes = 4096
	client := dialRelay(t, serveRelay(t, cfg))
	conn := fake.Accept()
	client.ExpectReady()

	conn.Send(`{"type":"speech_start","utterance_id":"u1"}`)
	client.Expect("speech_start")
	client.Audio(make([]byte, 8192)) // over the client frame limit

	events := client.WaitClosed()
	if len(events) != 1 || events[0]["utterance_id"] != "u1" || events[0]["code"] != "stt_limit_exceeded" {
		t.Fatalf("events = %v, want one stt_limit_exceeded for u1", events)
	}
	if !conn.Disconnected() {
		t.Fatal("CORE must close the stream")
	}
	records := sink.WaitFor(t, 1)
	if records[0].UtteranceID != "u1" || records[0].Outcome != sttinfra.OutcomeClientAborted {
		t.Fatalf("records = %+v", records)
	}
}

func TestSTTRelayRefusesConnectionsBeyondTheLimitAndFreesTheSlot(t *testing.T) {
	fake := startFakeSTTServer(t)
	cfg := realStreamConfig(fake, nil, nil)
	cfg.MaxConnections = 2
	url := serveRelay(t, cfg)

	first := dialRelay(t, url)
	fake.Accept()
	first.ExpectReady()
	second := dialRelay(t, url)
	fake.Accept()
	second.ExpectReady()

	_, err := websocket.Dial(url, "", "http://localhost/")
	if err == nil {
		t.Fatal("a third connection must be refused before the upgrade")
	}

	_ = first.ws.Close()
	waitUntil(t, "the slot to be freed", func() bool {
		ws, err := websocket.Dial(url, "", "http://localhost/")
		if err != nil {
			return false
		}
		_ = ws.Close()
		return true
	})
}

func TestSTTRelayRefusesTheUpgradeWith503WhenFull(t *testing.T) {
	fake := startFakeSTTServer(t)
	cfg := realStreamConfig(fake, nil, nil)
	cfg.MaxConnections = 1
	mux := http.NewServeMux()
	registerSTTRoutes(mux, newSTTRelayHandler(cfg))
	server := httptest.NewServer(mux)
	defer server.Close()
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/stt"

	holder := dialRelay(t, url)
	fake.Accept()
	holder.ExpectReady()

	resp, err := http.Get(server.URL + "/stt")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}

func TestSTTRelayDoesNotAddASessionErrorWhenSTTAlreadyEndedTheSessionWithOne(t *testing.T) {
	// A server-side close (a protocol violation, say) arrives as one error per
	// waiting utterance and then a session-level error, followed by the close.
	// The client has been told why; a second session error from CORE would be noise.
	fake := startFakeSTTServer(t)
	sink := &recordSink{}
	client := dialRelay(t, serveRelay(t, realStreamConfig(fake, sink, nil)))
	conn := fake.Accept()
	client.ExpectReady()

	conn.Send(`{"type":"speech_start","utterance_id":"u1"}`)
	client.Expect("speech_start")
	conn.Send(`{"type":"error","utterance_id":"u1","code":"protocol_violation","error":"x","target_called":false}`)
	conn.Send(`{"type":"error","code":"protocol_violation","error":"x"}`)
	conn.Drop()

	events := client.WaitClosed()
	if len(events) != 2 || events[0]["utterance_id"] != "u1" || events[0]["code"] != "protocol_violation" ||
		events[1]["code"] != "protocol_violation" || events[1]["utterance_id"] != nil {
		t.Fatalf("events = %v, want the two errors STT sent and nothing from CORE", events)
	}
}

func TestSTTRelayStillAnswersAPingLongAfterItsLastWriteToTheClient(t *testing.T) {
	fake := startFakeSTTServer(t)
	cfg := realStreamConfig(fake, nil, nil)
	cfg.WriteTimeout = 100 * time.Millisecond
	mux := http.NewServeMux()
	registerSTTRoutes(mux, newSTTRelayHandler(cfg))
	server := httptest.NewServer(mux)
	defer server.Close()
	tap := startTCPTap(t, strings.TrimPrefix(server.URL, "http://"))

	client := dialRelay(t, "ws://"+tap.Addr+"/stt")
	fake.Accept()
	client.ExpectReady()
	time.Sleep(300 * time.Millisecond) // well past the write timeout

	before := tap.fromTarget.Load()
	if err := sttinfra.SendFrame(client.ws, sttinfra.StreamFrame{Kind: sttinfra.FramePing}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for tap.fromTarget.Load() == before {
		if time.Now().After(deadline) {
			t.Fatal("the relay never answered the ping: the write deadline of its last event is still set on the connection")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSTTRelayStillCompletesWhenSTTSentASessionErrorThatKeptTheConnection(t *testing.T) {
	// unknown_message_type keeps the stream open. A crash after it is a new failure
	// the client has not been told about, so CORE says it.
	fake := startFakeSTTServer(t)
	client := dialRelay(t, serveRelay(t, realStreamConfig(fake, nil, nil)))
	conn := fake.Accept()
	client.ExpectReady()

	conn.Send(`{"type":"error","code":"unknown_message_type","error":"x"}`)
	client.Expect("error")
	conn.Drop()

	events := client.WaitClosed()
	if len(events) != 1 || events[0]["code"] != "stt_failed" || events[0]["utterance_id"] != nil {
		t.Fatalf("events = %v, want CORE's own session error after the crash", events)
	}
}
