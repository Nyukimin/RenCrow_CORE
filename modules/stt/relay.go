package stt

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// The relay rules of /stt. CORE no longer detects speech: it relays the audio
// of a client connection to the RenCrow_STT audio stream (protocol
// stt.stream.v1, docs/06_Public_API仕様.md "WS /stt") and relays the events
// back. This file holds the decisions of that relay that need no I/O, so they
// are tested without a socket:
//
//   - which client control becomes which upstream message (MapControl),
//   - which upstream events are accepted, with the single bookkeeping that
//     keeps "every announced utterance gets exactly one terminal event" true at
//     the relay boundary (RelayTable),
//   - which terminal events are recorded, and when a silent upstream is given
//     up on (the watchdog).
//
// Opening sockets, timers and writes stay with the caller (cmd/rencrow).

const (
	// StreamProtocol is the protocol RenCrow_STT's audio stream speaks.
	StreamProtocol = "stt.stream.v1"
	// StreamOrigin is the fixed Origin CORE sends when it opens the stream. The
	// stream accepts a request with an Origin only when it equals this value, so
	// a browser page cannot reach it. The host does not exist (.invalid).
	StreamOrigin = "http://rencrow-core.invalid"

	// DefaultRelayMaxPending bounds the utterances that have no terminal event
	// yet. STT queues at most 8 per connection, so reaching it means STT is broken.
	DefaultRelayMaxPending = 64
	// RelayWatchdogSlack is added to the terminal deadline STT announces before
	// CORE gives up on an utterance. STT answers within its own deadline; the
	// watchdog is the last net for a job that stops while the stream stays alive.
	RelayWatchdogSlack = 60 * time.Second

	ErrorCodeSTTBusy                = "stt_busy"
	ErrorCodeSTTFailed              = "stt_failed"
	ErrorCodeLimitExceeded          = "stt_limit_exceeded"
	ErrorCodeUnsupportedAudioFormat = "unsupported_audio_format"

	streamMessageSessionStart    = "session.start"
	streamMessageUtteranceStart  = "utterance.start"
	streamMessageUtteranceStop   = "utterance.stop"
	streamMessageUtteranceCancel = "utterance.cancel"

	// WebSocketEventTypeSessionReady is the first event of the stream.
	WebSocketEventTypeSessionReady = "session_ready"

	maxStreamRequestIDLength = 128
)

var (
	// ErrUpstreamEventInvalid marks an upstream text frame that is not a JSON object with a type.
	ErrUpstreamEventInvalid = errors.New("upstream event is not a JSON object with a type")
	// ErrUpstreamEventMissingSeq marks an upstream event without the hop sequence number.
	ErrUpstreamEventMissingSeq = errors.New("upstream event has no integer seq")

	// ErrStreamProtocolMismatch marks a session_ready of another protocol.
	ErrStreamProtocolMismatch = errors.New("upstream does not speak " + StreamProtocol)
	// ErrStreamSessionReadyInvalid marks a session_ready CORE cannot rely on.
	ErrStreamSessionReadyInvalid = errors.New("upstream session_ready is invalid")
)

// StreamLimits are the limits STT announces in session_ready. CORE takes its
// watchdog and its frame splitting from them instead of constants.
type StreamLimits struct {
	MaxUtterance     time.Duration
	TerminalDeadline time.Duration
	MaxFrameBytes    int
	QueueDepth       int
}

// SessionReady is the validated first event of the stream.
type SessionReady struct {
	Seq       int64
	Provider  string
	Target    string
	Language  string
	RequestID string
	Limits    StreamLimits
}

// BuildSessionStartMessage is the first message CORE sends upstream. It carries
// no options: the segmentation defaults are owned by STT's configuration alone.
// requestID correlates STT's log with the Action CORE records per utterance.
func BuildSessionStartMessage(requestID string) map[string]any {
	return map[string]any{
		"type":        streamMessageSessionStart,
		"protocol":    StreamProtocol,
		"sample_rate": DefaultSampleRate,
		"channels":    1,
		"format":      "pcm_s16le",
		"request_id":  requestID,
	}
}

// ValidStreamRequestID reports whether id satisfies the stream's request_id
// rule: 1 to 128 characters of [A-Za-z0-9._:-].
func ValidStreamRequestID(id string) bool {
	if id == "" || len(id) > maxStreamRequestIDLength {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == ':' || r == '-':
		default:
			return false
		}
	}
	return true
}

// ParseSessionReady validates the first upstream frame. The protocol must be
// stt.stream.v1 and the limits CORE derives its deadlines from must be present
// and positive; a request_id that is present must be the one CORE sent.
func ParseSessionReady(payload []byte, wantRequestID string) (SessionReady, error) {
	obj, err := decodeUpstreamObject(payload)
	if err != nil {
		return SessionReady{}, fmt.Errorf("%w: %v", ErrStreamSessionReadyInvalid, err)
	}
	if typ, _ := obj["type"].(string); typ != WebSocketEventTypeSessionReady {
		return SessionReady{}, fmt.Errorf("%w: first event is %q, want %s", ErrStreamSessionReadyInvalid, typ, WebSocketEventTypeSessionReady)
	}
	if protocol, _ := obj["protocol"].(string); protocol != StreamProtocol {
		return SessionReady{}, fmt.Errorf("%w: protocol %q", ErrStreamProtocolMismatch, protocol)
	}
	ready := SessionReady{}
	ready.Seq, _ = jsonInt(obj["seq"])
	ready.Provider, _ = obj["provider"].(string)
	ready.Target, _ = obj["target"].(string)
	ready.Language, _ = obj["language"].(string)
	ready.RequestID, _ = obj["request_id"].(string)
	if ready.RequestID != "" && wantRequestID != "" && ready.RequestID != wantRequestID {
		return SessionReady{}, fmt.Errorf("%w: request_id differs from the one CORE sent", ErrStreamSessionReadyInvalid)
	}
	limits, _ := obj["limits"].(map[string]any)
	maxUtterance, okUtterance := jsonInt(limits["max_utterance_ms"])
	maxFrame, okFrame := jsonInt(limits["max_frame_bytes"])
	deadline, okDeadline := jsonInt(limits["terminal_deadline_ms"])
	if limits == nil || !okUtterance || !okFrame || !okDeadline || maxUtterance <= 0 || maxFrame <= 0 || deadline <= 0 {
		return SessionReady{}, fmt.Errorf("%w: limits.max_utterance_ms, max_frame_bytes and terminal_deadline_ms must be positive", ErrStreamSessionReadyInvalid)
	}
	queueDepth, _ := jsonInt(limits["queue_depth"])
	ready.Limits = StreamLimits{
		MaxUtterance:     time.Duration(maxUtterance) * time.Millisecond,
		TerminalDeadline: time.Duration(deadline) * time.Millisecond,
		MaxFrameBytes:    int(maxFrame),
		QueueDepth:       int(queueDepth),
	}
	return ready, nil
}

// SplitStreamFrames cuts one client frame into parts of at most max bytes, in
// order, without losing a byte. The Viewer sends the audio it gathered while
// the connection was opening as one frame, which can exceed what STT accepts.
func SplitStreamFrames(payload []byte, max int) [][]byte {
	if len(payload) == 0 {
		return nil
	}
	if max <= 0 || len(payload) <= max {
		return [][]byte{payload}
	}
	parts := make([][]byte, 0, len(payload)/max+1)
	for len(payload) > 0 {
		n := max
		if n > len(payload) {
			n = len(payload)
		}
		parts = append(parts, payload[:n])
		payload = payload[n:]
	}
	return parts
}

// UpstreamEvent is one accepted event of the stream. Its payload is relayed as
// it is, except for the hop sequence number: CORE owns the client-facing seq.
type UpstreamEvent struct {
	Type        string
	UtteranceID string
	// Seq is STT's hop sequence number. CORE only checks that it increases.
	Seq     int64
	payload map[string]any
}

// ParseUpstreamEvent decodes an upstream text frame: a JSON object with a type
// and the hop sequence number. Which types are relayed is decided by
// RelayTable.HandleUpstream, after the sequence check, so an event that is
// dropped still counts toward the monotonic check.
func ParseUpstreamEvent(payload []byte) (UpstreamEvent, error) {
	obj, err := decodeUpstreamObject(payload)
	if err != nil {
		return UpstreamEvent{}, fmt.Errorf("%w: %v", ErrUpstreamEventInvalid, err)
	}
	typ, _ := obj["type"].(string)
	if typ == "" {
		return UpstreamEvent{}, ErrUpstreamEventInvalid
	}
	seq, ok := jsonInt(obj["seq"])
	if !ok {
		return UpstreamEvent{}, ErrUpstreamEventMissingSeq
	}
	id, _ := obj["utterance_id"].(string)
	delete(obj, "seq")
	return UpstreamEvent{Type: typ, UtteranceID: id, Seq: seq, payload: obj}, nil
}

func decodeUpstreamObject(payload []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber() // numbers are relayed with the text STT wrote
	var obj map[string]any
	if err := decoder.Decode(&obj); err != nil {
		return nil, err
	}
	if obj == nil {
		return nil, errors.New("not a JSON object")
	}
	return obj, nil
}

func jsonInt(v any) (int64, bool) {
	switch n := v.(type) {
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	case float64:
		return int64(n), n == float64(int64(n))
	default:
		return 0, false
	}
}

func jsonBool(payload map[string]any, key string) (value, present bool) {
	b, ok := payload[key].(bool)
	return b, ok
}

func jsonString(payload map[string]any, key string) string {
	s, _ := payload[key].(string)
	return s
}

func jsonMillis(payload map[string]any, key string) int64 {
	n, _ := jsonInt(payload[key])
	return n
}

// ControlDecision is what the relay does with one client control message:
// send Upstream to STT, send ClientEvent to the client, or neither.
type ControlDecision struct {
	Upstream    map[string]any
	ClientEvent map[string]any
	// Stop is set when Upstream is a stop. StopUtteranceID names the utterance it
	// addresses, "" when it addresses whichever is active.
	Stop            bool
	StopUtteranceID string
}

// TerminalRecord is a terminal event that must be recorded as an Action.
type TerminalRecord struct {
	UtteranceID  string
	Type         string
	Code         string
	EmptyReason  string
	EndReason    string
	AudioMS      int64
	QueueMS      int64
	ProcessingMS int64
	// TargetCalledMissing is set when STT sent no target_called on a terminal it
	// owns. The recording rule then errs on the side of recording: a missing flag
	// must not make the evidence of an external effect disappear silently.
	TargetCalledMissing bool
}

// EventDecision is what the relay does with one upstream event.
type EventDecision struct {
	// Forward is true when Event goes to the client, stamped by the caller with
	// UtteranceID and CORE's own seq.
	Forward     bool
	Event       map[string]any
	UtteranceID string
	// Terminal is true when Event ends the utterance UtteranceID.
	Terminal bool
	// Record is set when the terminal must be recorded.
	Record *TerminalRecord
	// Drop says why an event was not forwarded (a duplicate terminal, an event of
	// an utterance that has none waiting). The caller logs it.
	Drop string
	// Violation is set when STT broke the stream contract badly enough that the
	// relay must be closed and its waiting utterances completed.
	Violation string
	// EndsSession is set on a session-level error that STT sends just before it
	// closes the connection (see sessionEndingErrorCodes). The client has been told
	// why the session ends, so CORE adds no session error of its own when the
	// close follows.
	EndsSession bool
}

// sessionEndingErrorCodes are the session-level error codes after which STT
// closes the connection (contract 4.5 and 4.6). A session-level error that keeps
// the connection (an unknown message type, a WAV frame in the middle) is not in
// the list: a failure that follows it still needs CORE's own session error.
var sessionEndingErrorCodes = map[string]bool{
	"protocol_violation":    true,
	"invalid_session_start": true,
	"invalid_option":        true,
	ErrorCodeSTTFailed:      true,
}

// RelayTable is the bookkeeping of one relayed connection. It is owned by the
// relay loop and is not safe for concurrent use.
//
// Its job is the invariant of the relay boundary: an utterance that STT
// announced (speech_start) gets exactly one terminal event, whoever ends it.
// A terminal of an id that is not waiting is a duplicate and is dropped, so an
// utterance that CORE completed itself (watchdog, upstream failure) cannot get
// a second terminal from STT later.
type RelayTable struct {
	limits StreamLimits
	max    int

	order    []string // announcement order
	deadline map[string]time.Time

	// lastStartID is the id CORE adds to a stop or cancel the client sent without
	// one (the Viewer's). It is empty on a connection that sent no start (PORTAL),
	// whose id-less stop passes as it is. After a start it holds the client's id
	// until STT announces an utterance, then always the id STT announced last.
	lastStartID string

	lastSeq int64
	haveSeq bool
}

// NewRelayTable returns an empty table. limits come from session_ready.
func NewRelayTable(limits StreamLimits, maxPending int) *RelayTable {
	if maxPending <= 0 {
		maxPending = DefaultRelayMaxPending
	}
	return &RelayTable{limits: limits, max: maxPending, deadline: make(map[string]time.Time)}
}

// NoteSessionReady sets the baseline of the hop sequence: the events that
// follow must have a larger seq than session_ready.
func (t *RelayTable) NoteSessionReady(seq int64) {
	t.lastSeq, t.haveSeq = seq, true
}

// MapControl decides what a client control message becomes. Only start, stop
// (and its legacy name final_pending) and cancel are relayed; everything else
// is dropped. The Viewer's stop carries no utterance id, so CORE adds the id of
// the utterance STT announced last (contract 5.1 and 3.7 j.), not the id of the
// client's start: STT splits an utterance at a breath, and the continuation does
// not inherit the client's id (3.7 i.).
func (t *RelayTable) MapControl(c Control, now time.Time) ControlDecision {
	switch {
	case c.Type == "start":
		if message := c.AudioFormatError(); message != "" {
			// The rejected start never began anything, so its error names no utterance.
			return ControlDecision{ClientEvent: BuildErrorEventWithCode(ErrorCodeUnsupportedAudioFormat, message)}
		}
		if c.UtteranceID == "" {
			// session.start already covers a client that never marks utterances;
			// forwarding would open an empty utterance right after the connection.
			return ControlDecision{}
		}
		t.lastStartID = c.UtteranceID
		return ControlDecision{Upstream: map[string]any{"type": streamMessageUtteranceStart, "utterance_id": c.UtteranceID}}
	case IsFinalizeControl(c.Type):
		id := t.controlTarget(c)
		t.shortenWatchdog(id, now.Add(t.limits.TerminalDeadline+RelayWatchdogSlack))
		return ControlDecision{Upstream: namedUtteranceMessage(streamMessageUtteranceStop, id), Stop: true, StopUtteranceID: id}
	case c.Type == "cancel":
		return ControlDecision{Upstream: namedUtteranceMessage(streamMessageUtteranceCancel, t.controlTarget(c))}
	default:
		return ControlDecision{}
	}
}

func (t *RelayTable) controlTarget(c Control) string {
	if c.UtteranceID != "" {
		return c.UtteranceID
	}
	return t.lastStartID
}

func namedUtteranceMessage(messageType, id string) map[string]any {
	msg := map[string]any{"type": messageType}
	if id != "" {
		msg["utterance_id"] = id
	}
	return msg
}

// shortenWatchdog moves the deadline of the named utterance (every waiting one
// when id is empty: an id-less stop closes whichever utterance is active, which
// CORE cannot tell) to d, never later than it already is.
func (t *RelayTable) shortenWatchdog(id string, d time.Time) {
	for _, waiting := range t.order {
		if (id == "" || waiting == id) && d.Before(t.deadline[waiting]) {
			t.deadline[waiting] = d
		}
	}
}

// HandleUpstream decides what happens to one upstream event.
func (t *RelayTable) HandleUpstream(ev UpstreamEvent, now time.Time) EventDecision {
	if t.haveSeq && ev.Seq <= t.lastSeq {
		return EventDecision{Violation: fmt.Sprintf("upstream seq %d does not follow %d", ev.Seq, t.lastSeq)}
	}
	t.lastSeq, t.haveSeq = ev.Seq, true

	switch ev.Type {
	case WebSocketEventTypeSessionReady:
		return EventDecision{Drop: "a second session_ready"}
	case WebSocketEventTypeSpeechStart, WebSocketEventTypePartial, WebSocketEventTypeFinal,
		WebSocketEventTypeError, WebSocketEventTypeCancelled:
		// relayed below
	default:
		return EventDecision{Drop: fmt.Sprintf("event type %q is not relayed", ev.Type)}
	}
	switch ev.Type {
	case WebSocketEventTypeSpeechStart:
		return t.handleSpeechStart(ev, now)
	case WebSocketEventTypePartial:
		if !t.waiting(ev.UtteranceID) {
			return EventDecision{Drop: "a partial of an utterance that is not waiting"}
		}
		return t.forward(ev, false)
	default: // final, error, cancelled
		return t.handleTerminal(ev)
	}
}

func (t *RelayTable) handleSpeechStart(ev UpstreamEvent, now time.Time) EventDecision {
	if ev.UtteranceID == "" {
		return EventDecision{Drop: "a speech_start without an utterance_id"}
	}
	if t.waiting(ev.UtteranceID) {
		return EventDecision{Drop: "a speech_start of an utterance that is already waiting"}
	}
	if len(t.order) >= t.max {
		return EventDecision{Violation: fmt.Sprintf("%d utterances wait for a terminal event; STT queues at most %d", len(t.order), t.limits.QueueDepth)}
	}
	t.order = append(t.order, ev.UtteranceID)
	t.deadline[ev.UtteranceID] = now.Add(t.limits.MaxUtterance + t.limits.TerminalDeadline + RelayWatchdogSlack)
	if t.lastStartID != "" {
		// The connection sent a start, so its id-less stop gets an id. Every
		// accepted speech_start moves it: the one answering the start (STT may have
		// replaced the client's id), an automatic one, the continuation of a
		// max-length split, and the continuation after a split by silence. That
		// last one is what the stop of a Viewer that sent its stop first must reach.
		// An id that has been closed is harmless: STT ignores a stop that names a
		// finished utterance (contract 3.5, 3.7 j.).
		t.lastStartID = ev.UtteranceID
	}
	return t.forward(ev, false)
}

func (t *RelayTable) handleTerminal(ev UpstreamEvent) EventDecision {
	if ev.UtteranceID == "" {
		// A session-level error, or the empty final of a stop with nothing to
		// stop: no utterance to close, nothing to record.
		decision := t.forward(ev, false)
		decision.EndsSession = ev.Type == WebSocketEventTypeError && sessionEndingErrorCodes[jsonString(ev.payload, "code")]
		return decision
	}
	if !t.waiting(ev.UtteranceID) {
		return EventDecision{Drop: "a second terminal event, or one of an utterance that is not waiting"}
	}
	t.remove(ev.UtteranceID)
	decision := t.forward(ev, true)
	decision.Record = terminalRecord(ev)
	return decision
}

func (t *RelayTable) forward(ev UpstreamEvent, terminal bool) EventDecision {
	event := make(map[string]any, len(ev.payload))
	for key, value := range ev.payload {
		event[key] = value
	}
	return EventDecision{Forward: true, Event: event, UtteranceID: ev.UtteranceID, Terminal: terminal}
}

// terminalRecord applies the recording rule: a terminal STT owns is recorded
// when its target_called is true, and also when the flag is missing (the
// caller warns and counts it); false means the target was never called.
func terminalRecord(ev UpstreamEvent) *TerminalRecord {
	called, present := jsonBool(ev.payload, "target_called")
	if present && !called {
		return nil
	}
	return &TerminalRecord{
		UtteranceID:         ev.UtteranceID,
		Type:                ev.Type,
		Code:                jsonString(ev.payload, "code"),
		EmptyReason:         jsonString(ev.payload, "empty_reason"),
		EndReason:           jsonString(ev.payload, "end_reason"),
		AudioMS:             jsonMillis(ev.payload, "audio_ms"),
		QueueMS:             jsonMillis(ev.payload, "queue_ms"),
		ProcessingMS:        jsonMillis(ev.payload, "processing_ms"),
		TargetCalledMissing: !present,
	}
}

func (t *RelayTable) waiting(id string) bool {
	if id == "" {
		return false
	}
	_, ok := t.deadline[id]
	return ok
}

func (t *RelayTable) remove(id string) {
	delete(t.deadline, id)
	for i, waiting := range t.order {
		if waiting == id {
			t.order = append(t.order[:i], t.order[i+1:]...)
			return
		}
	}
}

// Expired returns the utterances whose watchdog deadline passed, in
// announcement order. STT promises a terminal event within its terminal
// deadline, so an expired utterance means the stream is unhealthy: the caller
// treats it as an upstream failure and completes every waiting utterance.
func (t *RelayTable) Expired(now time.Time) []string {
	var expired []string
	for _, id := range t.order {
		if !now.Before(t.deadline[id]) {
			expired = append(expired, id)
		}
	}
	return expired
}

// Pending returns the utterances without a terminal event, in announcement order.
func (t *RelayTable) Pending() []string {
	return append([]string(nil), t.order...)
}

// Drain removes and returns every waiting utterance in announcement order. It
// is idempotent.
func (t *RelayTable) Drain() []string {
	drained := t.order
	t.order = nil
	t.deadline = make(map[string]time.Time)
	return drained
}
