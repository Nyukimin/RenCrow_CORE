package stt

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var relayEpoch = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

func testStreamLimits() StreamLimits {
	return StreamLimits{
		MaxUtterance:     30 * time.Second,
		TerminalDeadline: 75 * time.Second,
		MaxFrameBytes:    65536,
		QueueDepth:       8,
	}
}

func newTestRelayTable() *RelayTable {
	return NewRelayTable(testStreamLimits(), DefaultRelayMaxPending)
}

// upstream builds the event STT would send: the hop seq is always present.
func upstream(t *testing.T, seq int, payload string) UpstreamEvent {
	t.Helper()
	body := strings.Replace(payload, "{", `{"seq":`+itoa(seq)+",", 1)
	ev, err := ParseUpstreamEvent([]byte(body))
	if err != nil {
		t.Fatalf("ParseUpstreamEvent(%s) = %v", body, err)
	}
	return ev
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for ; n > 0; n /= 10 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
	}
	return string(digits)
}

func TestRelayTableForwardsAStartWithAnIDAndKeepsItForTheStop(t *testing.T) {
	table := newTestRelayTable()
	got := table.MapControl(Control{Type: "start", UtteranceID: "v1-1", SampleRate: 16000, Channels: 1, Format: "pcm_s16le"}, relayEpoch)
	if got.ClientEvent != nil {
		t.Fatalf("a valid start must not produce a client event: %#v", got.ClientEvent)
	}
	if got.Upstream["type"] != "utterance.start" || got.Upstream["utterance_id"] != "v1-1" {
		t.Fatalf("start must map to utterance.start with the client's id: %#v", got.Upstream)
	}
	// The Viewer's stop carries no id: CORE adds the id of the last start.
	stop := table.MapControl(Control{Type: "stop"}, relayEpoch)
	if stop.Upstream["type"] != "utterance.stop" || stop.Upstream["utterance_id"] != "v1-1" {
		t.Fatalf("an id-less stop must name the last start: %#v", stop.Upstream)
	}
	if !stop.Stop || stop.StopUtteranceID != "v1-1" {
		t.Fatalf("the decision says which utterance the stop addresses: %#v", stop)
	}
	legacy := table.MapControl(Control{Type: "final_pending"}, relayEpoch)
	if legacy.Upstream["type"] != "utterance.stop" || !legacy.Stop {
		t.Fatalf("final_pending is the legacy name of stop: %#v", legacy.Upstream)
	}
	cancel := table.MapControl(Control{Type: "cancel"}, relayEpoch)
	if cancel.Upstream["type"] != "utterance.cancel" || cancel.Upstream["utterance_id"] != "v1-1" || cancel.Stop {
		t.Fatalf("an id-less cancel must name the last start: %#v", cancel)
	}
}

func TestRelayTableKeepsTheClientsOwnIDOnStopAndCancel(t *testing.T) {
	table := newTestRelayTable()
	table.MapControl(Control{Type: "start", UtteranceID: "a"}, relayEpoch)
	stop := table.MapControl(Control{Type: "stop", UtteranceID: "b"}, relayEpoch)
	if stop.Upstream["utterance_id"] != "b" {
		t.Fatalf("an id the client chose is never replaced: %#v", stop.Upstream)
	}
	cancel := table.MapControl(Control{Type: "cancel", UtteranceID: "b"}, relayEpoch)
	if cancel.Upstream["utterance_id"] != "b" {
		t.Fatalf("an id the client chose is never replaced: %#v", cancel.Upstream)
	}
}

func TestRelayTableSendsAnIDLessStopWhenNoStartWasSeen(t *testing.T) {
	table := newTestRelayTable()
	stop := table.MapControl(Control{Type: "stop"}, relayEpoch)
	if stop.Upstream["type"] != "utterance.stop" {
		t.Fatalf("PORTAL sends stop without a start: %#v", stop.Upstream)
	}
	if _, has := stop.Upstream["utterance_id"]; has {
		t.Fatalf("with no start there is no id to add: %#v", stop.Upstream)
	}
}

func TestRelayTableDoesNotForwardAStartWithoutAnID(t *testing.T) {
	table := newTestRelayTable()
	got := table.MapControl(Control{Type: "start", SampleRate: 16000, Channels: 1, Format: "pcm_s16le"}, relayEpoch)
	if got.Upstream != nil || got.ClientEvent != nil {
		t.Fatalf("forwarding it would open an empty utterance right after the connection: %#v", got)
	}
}

func TestRelayTableRejectsAnUnsupportedFormatAsASessionLevelError(t *testing.T) {
	table := newTestRelayTable()
	got := table.MapControl(Control{Type: "start", UtteranceID: "live", SampleRate: 48000}, relayEpoch)
	if got.Upstream != nil {
		t.Fatalf("a rejected start must not reach STT: %#v", got.Upstream)
	}
	if got.ClientEvent["type"] != WebSocketEventTypeError || got.ClientEvent["code"] != ErrorCodeUnsupportedAudioFormat {
		t.Fatalf("client event = %#v", got.ClientEvent)
	}
	if _, has := got.ClientEvent["utterance_id"]; has {
		t.Fatalf("the rejection must not borrow an utterance id: %#v", got.ClientEvent)
	}
	// The rejected start left the last-start id alone.
	stop := table.MapControl(Control{Type: "stop"}, relayEpoch)
	if _, has := stop.Upstream["utterance_id"]; has {
		t.Fatalf("a rejected start must not become the stop's target: %#v", stop.Upstream)
	}
}

func TestRelayTableIgnoresUnknownControls(t *testing.T) {
	table := newTestRelayTable()
	for _, typ := range []string{"config", "ping", "", "session.start", "utterance.stop"} {
		if got := table.MapControl(Control{Type: typ}, relayEpoch); got.Upstream != nil || got.ClientEvent != nil {
			t.Fatalf("control %q must be dropped, not forwarded: %#v", typ, got)
		}
	}
}

func TestRelayTableConfirmsLastStartIDFromTheFirstSpeechStartOnly(t *testing.T) {
	table := newTestRelayTable()
	table.MapControl(Control{Type: "start", UtteranceID: "srv-looking"}, relayEpoch)
	// STT replaced the id (a reserved prefix): the stop must follow STT's id.
	table.HandleUpstream(upstream(t, 1, `{"type":"speech_start","utterance_id":"srv-1","offset_ms":0}`), relayEpoch)
	stop := table.MapControl(Control{Type: "stop"}, relayEpoch)
	if stop.Upstream["utterance_id"] != "srv-1" {
		t.Fatalf("lastStartID must be overwritten by STT's id: %#v", stop.Upstream)
	}
	// A later speech_start (an automatic one, or the continuation of a max-length split)
	// does not move it: the continuation still answers to the original id.
	table.HandleUpstream(upstream(t, 2, `{"type":"speech_start","utterance_id":"srv-2","offset_ms":900}`), relayEpoch)
	stop = table.MapControl(Control{Type: "stop"}, relayEpoch)
	if stop.Upstream["utterance_id"] != "srv-1" {
		t.Fatalf("only the speech_start answering the start confirms the id: %#v", stop.Upstream)
	}
}

func TestRelayTableTracksAnUtteranceFromSpeechStartToItsSingleTerminal(t *testing.T) {
	table := newTestRelayTable()
	start := table.HandleUpstream(upstream(t, 1, `{"type":"speech_start","utterance_id":"u1","offset_ms":0}`), relayEpoch)
	if !start.Forward || start.UtteranceID != "u1" || start.Event["type"] != "speech_start" {
		t.Fatalf("speech_start = %#v", start)
	}
	if _, has := start.Event["seq"]; has {
		t.Fatalf("the hop seq of STT must not be forwarded; CORE stamps its own: %#v", start.Event)
	}
	if got := table.Pending(); len(got) != 1 || got[0] != "u1" {
		t.Fatalf("Pending() = %v", got)
	}

	partial := table.HandleUpstream(upstream(t, 2, `{"type":"partial","utterance_id":"u1","text":"こん"}`), relayEpoch)
	if !partial.Forward || partial.Terminal {
		t.Fatalf("partial = %#v", partial)
	}

	final := table.HandleUpstream(upstream(t, 3, `{"type":"final","utterance_id":"u1","text":"こんにちは","end_reason":"silence","audio_ms":900,"target_called":true}`), relayEpoch)
	if !final.Forward || !final.Terminal || final.Event["text"] != "こんにちは" || final.Event["end_reason"] != "silence" {
		t.Fatalf("final = %#v", final)
	}
	if len(table.Pending()) != 0 {
		t.Fatalf("a terminal removes the utterance: %v", table.Pending())
	}

	dup := table.HandleUpstream(upstream(t, 4, `{"type":"final","utterance_id":"u1","text":"again","target_called":true}`), relayEpoch)
	if dup.Forward || dup.Drop == "" || dup.Record != nil {
		t.Fatalf("a second terminal of the same id must be dropped, not forwarded or recorded: %#v", dup)
	}
	late := table.HandleUpstream(upstream(t, 5, `{"type":"partial","utterance_id":"u1","text":"late"}`), relayEpoch)
	if late.Forward || late.Drop == "" {
		t.Fatalf("nothing of an utterance is forwarded after its terminal: %#v", late)
	}
}

func TestRelayTableForwardsTerminalsWithoutAnIDWithoutTrackingThem(t *testing.T) {
	table := newTestRelayTable()
	empty := table.HandleUpstream(upstream(t, 1, `{"type":"final","text":"","empty_reason":"no_active_utterance","end_reason":"stop","target_called":false}`), relayEpoch)
	if !empty.Forward || empty.Record != nil {
		t.Fatalf("the no_active_utterance final has no utterance to track or record: %#v", empty)
	}
	sessionErr := table.HandleUpstream(upstream(t, 2, `{"type":"error","code":"unknown_message_type","error":"x"}`), relayEpoch)
	if !sessionErr.Forward || sessionErr.Terminal {
		t.Fatalf("a session-level error ends no utterance: %#v", sessionErr)
	}
}

func TestRelayTableDropsDuplicateAndUnannouncedStarts(t *testing.T) {
	table := newTestRelayTable()
	table.HandleUpstream(upstream(t, 1, `{"type":"speech_start","utterance_id":"u1"}`), relayEpoch)
	dup := table.HandleUpstream(upstream(t, 2, `{"type":"speech_start","utterance_id":"u1"}`), relayEpoch)
	if dup.Forward || dup.Drop == "" {
		t.Fatalf("an id that is already waiting cannot start again: %#v", dup)
	}
	noID := table.HandleUpstream(upstream(t, 3, `{"type":"speech_start"}`), relayEpoch)
	if noID.Forward || noID.Drop == "" {
		t.Fatalf("a speech_start without an id cannot be matched to a terminal: %#v", noID)
	}
	unknown := table.HandleUpstream(upstream(t, 4, `{"type":"cancelled","utterance_id":"never-announced"}`), relayEpoch)
	if unknown.Forward || unknown.Drop == "" {
		t.Fatalf("a terminal of an utterance that was never announced is dropped: %#v", unknown)
	}
}

func TestRelayTableFlagsAHopSeqThatDoesNotIncrease(t *testing.T) {
	table := newTestRelayTable()
	table.HandleUpstream(upstream(t, 5, `{"type":"speech_start","utterance_id":"u1"}`), relayEpoch)
	for _, seq := range []int{5, 4} {
		got := table.HandleUpstream(upstream(t, seq, `{"type":"partial","utterance_id":"u1","text":"x"}`), relayEpoch)
		if got.Violation == "" || got.Forward {
			t.Fatalf("seq %d after 5 must close the relay: %#v", seq, got)
		}
	}
}

func TestParseUpstreamEventRequiresATypeAndASeq(t *testing.T) {
	if _, err := ParseUpstreamEvent([]byte(`{"type":"final","utterance_id":"u1"}`)); !errors.Is(err, ErrUpstreamEventMissingSeq) {
		t.Fatalf("an event without seq: %v", err)
	}
	if _, err := ParseUpstreamEvent([]byte(`{"seq":1.5,"type":"final"}`)); !errors.Is(err, ErrUpstreamEventMissingSeq) {
		t.Fatalf("a seq that is not an integer: %v", err)
	}
	for _, payload := range []string{`not json`, `[1]`, `null`, `{"seq":1}`, `{"seq":1,"type":7}`} {
		if _, err := ParseUpstreamEvent([]byte(payload)); !errors.Is(err, ErrUpstreamEventInvalid) {
			t.Fatalf("ParseUpstreamEvent(%s) = %v, want invalid", payload, err)
		}
	}
	ev, err := ParseUpstreamEvent([]byte(`{"seq":12,"type":"final","utterance_id":"u1","audio_ms":900}`))
	if err != nil || ev.Type != "final" || ev.UtteranceID != "u1" || ev.Seq != 12 {
		t.Fatalf("ParseUpstreamEvent() = %#v, %v", ev, err)
	}
}

func TestRelayTableRelaysOnlyTheEventTypesOnTheAllowList(t *testing.T) {
	table := newTestRelayTable()
	for i, typ := range []string{"status", "draft", "ready", "session_info", "pong", "utterance.start", "FINAL", "session_ready"} {
		got := table.HandleUpstream(upstream(t, i+1, `{"type":"`+typ+`","utterance_id":"u1"}`), relayEpoch)
		if got.Forward || got.Drop == "" || got.Violation != "" {
			t.Fatalf("type %q must be dropped and logged: %#v", typ, got)
		}
	}
	for i, typ := range []string{"speech_start", "partial", "final", "error", "cancelled"} {
		id := `"utterance_id":"u` + itoa(i) + `"`
		table.HandleUpstream(upstream(t, 100+i*10, `{"type":"speech_start",`+id+`}`), relayEpoch)
		if got := table.HandleUpstream(upstream(t, 101+i*10, `{"type":"`+typ+`",`+id+`,"target_called":false}`), relayEpoch); !got.Forward && typ != "speech_start" {
			t.Fatalf("type %q must be relayed: %#v", typ, got)
		}
	}
}

func TestRelayTableChecksTheSeqOfAnEventItDrops(t *testing.T) {
	table := newTestRelayTable()
	table.HandleUpstream(upstream(t, 5, `{"type":"status"}`), relayEpoch)
	got := table.HandleUpstream(upstream(t, 5, `{"type":"status"}`), relayEpoch)
	if got.Violation == "" {
		t.Fatalf("a dropped event is part of the hop sequence too: %#v", got)
	}
}

func TestRelayTableRefusesToTrackMoreThanTheLimit(t *testing.T) {
	table := NewRelayTable(testStreamLimits(), 3)
	for i := 1; i <= 3; i++ {
		got := table.HandleUpstream(upstream(t, i, `{"type":"speech_start","utterance_id":"u`+itoa(i)+`"}`), relayEpoch)
		if !got.Forward {
			t.Fatalf("utterance %d within the limit: %#v", i, got)
		}
	}
	over := table.HandleUpstream(upstream(t, 4, `{"type":"speech_start","utterance_id":"u4"}`), relayEpoch)
	if over.Violation == "" || over.Forward {
		t.Fatalf("one more than the limit means STT is broken: close the relay: %#v", over)
	}
}

func TestRelayTableDecidesWhichTerminalsAreRecorded(t *testing.T) {
	cases := []struct {
		name        string
		payload     string
		wantRecord  bool
		wantMissing bool
	}{
		{"recognised final", `{"type":"final","utterance_id":"u","text":"a","target_called":true,"audio_ms":1200,"queue_ms":3,"processing_ms":210}`, true, false},
		{"empty final that never called the target", `{"type":"final","utterance_id":"u","text":"","empty_reason":"too_short","target_called":false}`, false, false},
		{"target failure", `{"type":"error","utterance_id":"u","code":"STT_TARGET_FAILED","error":"x","target_called":true}`, true, false},
		{"failure before the target was called", `{"type":"error","utterance_id":"u","code":"STT_TARGET_UNAVAILABLE","error":"x","target_called":false}`, false, false},
		{"cancelled during recognition", `{"type":"cancelled","utterance_id":"u","target_called":true}`, true, false},
		{"cancelled while waiting", `{"type":"cancelled","utterance_id":"u","target_called":false}`, false, false},
		{"target_called missing on a final", `{"type":"final","utterance_id":"u","text":"a"}`, true, true},
		{"target_called missing on an error", `{"type":"error","utterance_id":"u","code":"stt_busy","error":"x"}`, true, true},
		{"target_called of the wrong type counts as missing", `{"type":"final","utterance_id":"u","text":"a","target_called":"yes"}`, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			table := newTestRelayTable()
			table.HandleUpstream(upstream(t, 1, `{"type":"speech_start","utterance_id":"u"}`), relayEpoch)
			got := table.HandleUpstream(upstream(t, 2, tc.payload), relayEpoch)
			if !got.Forward || !got.Terminal {
				t.Fatalf("decision = %#v", got)
			}
			if (got.Record != nil) != tc.wantRecord {
				t.Fatalf("Record = %#v, want recorded=%t", got.Record, tc.wantRecord)
			}
			if got.Record != nil && got.Record.TargetCalledMissing != tc.wantMissing {
				t.Fatalf("TargetCalledMissing = %t, want %t", got.Record.TargetCalledMissing, tc.wantMissing)
			}
		})
	}
}

func TestRelayTableRecordCarriesTheDiagnosticsOfTheTerminal(t *testing.T) {
	table := newTestRelayTable()
	table.HandleUpstream(upstream(t, 1, `{"type":"speech_start","utterance_id":"u"}`), relayEpoch)
	got := table.HandleUpstream(upstream(t, 2, `{"type":"final","utterance_id":"u","text":"a","end_reason":"silence","audio_ms":1200,"queue_ms":3,"processing_ms":210,"target_called":true}`), relayEpoch)
	r := got.Record
	if r == nil || r.UtteranceID != "u" || r.Type != WebSocketEventTypeFinal || r.EndReason != "silence" ||
		r.AudioMS != 1200 || r.QueueMS != 3 || r.ProcessingMS != 210 {
		t.Fatalf("Record = %#v", r)
	}
	table.HandleUpstream(upstream(t, 3, `{"type":"speech_start","utterance_id":"v"}`), relayEpoch)
	failed := table.HandleUpstream(upstream(t, 4, `{"type":"error","utterance_id":"v","code":"STT_TARGET_FAILED","error":"x","target_called":true}`), relayEpoch)
	if failed.Record == nil || failed.Record.Code != "STT_TARGET_FAILED" || failed.Record.Type != WebSocketEventTypeError {
		t.Fatalf("Record = %#v", failed.Record)
	}
}

func TestRelayTableWatchdogStartsAtSpeechStart(t *testing.T) {
	table := newTestRelayTable()
	table.HandleUpstream(upstream(t, 1, `{"type":"speech_start","utterance_id":"u1"}`), relayEpoch)
	// max_utterance_ms + terminal_deadline_ms + 60 s = 30 + 75 + 60 = 165 s.
	if got := table.Expired(relayEpoch.Add(165*time.Second - time.Millisecond)); len(got) != 0 {
		t.Fatalf("expired too early: %v", got)
	}
	got := table.Expired(relayEpoch.Add(165 * time.Second))
	if len(got) != 1 || got[0] != "u1" {
		t.Fatalf("Expired() = %v", got)
	}
	// Expiry only reports: the relay then fails as a whole, which drains the table.
	if pending := table.Pending(); len(pending) != 1 {
		t.Fatalf("Expired must not change the table: %v", pending)
	}
}

func TestRelayTableWatchdogShortensAfterAStopForTheNamedUtterance(t *testing.T) {
	table := newTestRelayTable()
	table.HandleUpstream(upstream(t, 1, `{"type":"speech_start","utterance_id":"u1"}`), relayEpoch)
	table.HandleUpstream(upstream(t, 2, `{"type":"speech_start","utterance_id":"u2"}`), relayEpoch)
	stopAt := relayEpoch.Add(10 * time.Second)
	table.MapControl(Control{Type: "stop", UtteranceID: "u1"}, stopAt)
	// terminal_deadline_ms + 60 s = 135 s from the stop, only for u1.
	expired := table.Expired(stopAt.Add(135 * time.Second))
	if len(expired) != 1 || expired[0] != "u1" {
		t.Fatalf("Expired() = %v, want only u1", expired)
	}
}

func TestRelayTableAnIDLessStopShortensEveryWaitingUtterance(t *testing.T) {
	table := newTestRelayTable()
	table.HandleUpstream(upstream(t, 1, `{"type":"speech_start","utterance_id":"u1"}`), relayEpoch)
	table.HandleUpstream(upstream(t, 2, `{"type":"speech_start","utterance_id":"u2"}`), relayEpoch)
	stopAt := relayEpoch.Add(10 * time.Second)
	table.MapControl(Control{Type: "stop"}, stopAt)
	expired := table.Expired(stopAt.Add(135 * time.Second))
	if len(expired) != 2 {
		t.Fatalf("an id-less stop closes whichever utterance is active, which CORE cannot tell: Expired() = %v", expired)
	}
}

func TestRelayTableAStopNeverLengthensAnExistingDeadline(t *testing.T) {
	table := newTestRelayTable()
	table.HandleUpstream(upstream(t, 1, `{"type":"speech_start","utterance_id":"u1"}`), relayEpoch)
	// The speech_start deadline is at 165 s. A stop at 100 s would give 235 s.
	table.MapControl(Control{Type: "stop", UtteranceID: "u1"}, relayEpoch.Add(100*time.Second))
	if got := table.Expired(relayEpoch.Add(165 * time.Second)); len(got) != 1 {
		t.Fatalf("the earlier deadline must stay: %v", got)
	}
}

func TestRelayTableDrainReturnsTheWaitingUtterancesInAnnouncementOrder(t *testing.T) {
	table := newTestRelayTable()
	for i, id := range []string{"b", "a", "c"} {
		table.HandleUpstream(upstream(t, i+1, `{"type":"speech_start","utterance_id":"`+id+`"}`), relayEpoch)
	}
	table.HandleUpstream(upstream(t, 4, `{"type":"cancelled","utterance_id":"a","target_called":false}`), relayEpoch)
	got := table.Drain()
	if strings.Join(got, ",") != "b,c" {
		t.Fatalf("Drain() = %v", got)
	}
	if len(table.Pending()) != 0 || len(table.Drain()) != 0 {
		t.Fatal("Drain empties the table and is idempotent")
	}
}

func TestParseSessionReadyValidatesTheProtocolAndTakesTheLimits(t *testing.T) {
	ready, err := ParseSessionReady([]byte(`{"type":"session_ready","seq":1,"protocol":"stt.stream.v1","provider":"rencrow_stt","target":"t","language":"ja","sample_rate":16000,"request_id":"req-1",`+
		`"limits":{"max_utterance_ms":30000,"max_frame_bytes":65536,"queue_depth":8,"terminal_deadline_ms":75000}}`), "req-1")
	if err != nil {
		t.Fatalf("ParseSessionReady() = %v", err)
	}
	if ready.Provider != "rencrow_stt" || ready.Limits.MaxUtterance != 30*time.Second || ready.Limits.MaxFrameBytes != 65536 ||
		ready.Limits.QueueDepth != 8 || ready.Limits.TerminalDeadline != 75*time.Second || ready.Seq != 1 {
		t.Fatalf("ParseSessionReady() = %#v", ready)
	}
}

func TestParseSessionReadyRejectsWhatCoreCannotRelyOn(t *testing.T) {
	good := `"seq":1,"protocol":"stt.stream.v1","provider":"rencrow_stt","limits":{"max_utterance_ms":30000,"max_frame_bytes":65536,"queue_depth":8,"terminal_deadline_ms":75000}`
	cases := map[string]struct {
		payload string
		want    error
	}{
		"other protocol":   {`{"type":"session_ready","seq":1,"protocol":"stt.stream.v2","provider":"p","limits":{"max_utterance_ms":1,"max_frame_bytes":2,"terminal_deadline_ms":3}}`, ErrStreamProtocolMismatch},
		"no protocol":      {`{"type":"session_ready","seq":1,"limits":{"max_utterance_ms":1,"max_frame_bytes":2,"terminal_deadline_ms":3}}`, ErrStreamProtocolMismatch},
		"wrong type":       {`{"type":"ready",` + good + `}`, ErrStreamSessionReadyInvalid},
		"no limits":        {`{"type":"session_ready","seq":1,"protocol":"stt.stream.v1","provider":"p"}`, ErrStreamSessionReadyInvalid},
		"no deadline":      {`{"type":"session_ready","seq":1,"protocol":"stt.stream.v1","provider":"p","limits":{"max_utterance_ms":30000,"max_frame_bytes":65536}}`, ErrStreamSessionReadyInvalid},
		"zero frame size":  {`{"type":"session_ready","seq":1,"protocol":"stt.stream.v1","provider":"p","limits":{"max_utterance_ms":30000,"max_frame_bytes":0,"terminal_deadline_ms":75000}}`, ErrStreamSessionReadyInvalid},
		"not json":         {`oops`, ErrStreamSessionReadyInvalid},
		"other request id": {`{"type":"session_ready","request_id":"someone-else",` + good + `}`, ErrStreamSessionReadyInvalid},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseSessionReady([]byte(tc.payload), "req-1"); !errors.Is(err, tc.want) {
				t.Fatalf("ParseSessionReady() = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestBuildSessionStartMessageCarriesTheProtocolAudioFormatAndRequestID(t *testing.T) {
	msg := BuildSessionStartMessage("stt-1")
	want := map[string]any{
		"type": "session.start", "protocol": "stt.stream.v1", "sample_rate": 16000,
		"channels": 1, "format": "pcm_s16le", "request_id": "stt-1",
	}
	for key, value := range want {
		if msg[key] != value {
			t.Fatalf("session.start[%s] = %#v, want %#v (message %#v)", key, msg[key], value, msg)
		}
	}
	if _, has := msg["options"]; has {
		t.Fatalf("CORE sends no options: the defaults belong to STT's config alone: %#v", msg)
	}
	if !ValidStreamRequestID("stt-1") || ValidStreamRequestID("") || ValidStreamRequestID("a b") || ValidStreamRequestID(strings.Repeat("a", 129)) {
		t.Fatal("request ids are 1..128 characters of [A-Za-z0-9._:-]")
	}
}

func TestSplitStreamFramesKeepsEveryByteUnderTheLimit(t *testing.T) {
	payload := make([]byte, 200001)
	for i := range payload {
		payload[i] = byte(i)
	}
	parts := SplitStreamFrames(payload, 65536)
	total := 0
	for _, part := range parts {
		if len(part) == 0 || len(part) > 65536 {
			t.Fatalf("part of %d bytes", len(part))
		}
		total += len(part)
	}
	if total != len(payload) || len(parts) != 4 {
		t.Fatalf("parts=%d total=%d", len(parts), total)
	}
	if got := SplitStreamFrames(payload[:100], 65536); len(got) != 1 || len(got[0]) != 100 {
		t.Fatalf("a frame under the limit is sent as it is: %d parts", len(got))
	}
	if got := SplitStreamFrames(nil, 65536); len(got) != 0 {
		t.Fatalf("nothing to send: %d parts", len(got))
	}
}
