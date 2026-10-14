package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	sttinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/stt"
	modulestt "github.com/Nyukimin/RenCrow_CORE/modules/stt"
	"golang.org/x/net/websocket"
)

// /stt relays a client connection to the RenCrow_STT audio stream.
//
// CORE no longer detects speech or accumulates utterances: the Viewer, PORTAL
// and the probe stream PCM16 audio (and, for the Viewer, start / stop), CORE
// forwards it to one stream of RenCrow_STT per connection (protocol
// stt.stream.v1), and RenCrow_STT decides where an utterance ends and returns
// speech_start / partial / final / error / cancelled. The wire form of the
// client side (protocol "utterance.v1") is in docs/06_Public_API仕様.md.
//
// What this file owns is the boundary:
//
//   - which client messages become which stream messages, and which events come
//     back with which seq (modulestt.RelayTable holds the rules; this file
//     does no JSON building of its own beyond marshalling them),
//   - the invariant that every announced utterance ends with exactly one
//     terminal event, even when the stream dies, goes silent, or the client
//     leaves,
//   - liveness, the ping and the watchdog, with a replaceable clock,
//   - handing the evidence of each recognition to the recorder.
//
// One goroutine (the session loop) owns all state and does every write, so the
// order in which events are stamped is the order in which they are sent, and a
// client disconnect racing an upstream failure resolves to whichever the loop
// handles first. The two readers only move frames into channels.

const (
	// Concurrent /stt connections. STT allows twice as many (32), so a slot CORE
	// has released is never still counted on the STT side.
	sttRelayDefaultMaxConnections = 16
	// A client frame carries at most one maximum-length utterance (PCM at 32000
	// B/s, 30 s) plus slack. The Viewer sends what it gathered while connecting as
	// one frame, so this stays far above STT's own 64 KiB; the relay splits it.
	sttRelayMaxClientFrameBytes = 30*16000*2 + 8*1024
	// A connection with no client frame and no waiting utterance for this long is dropped.
	sttRelayDefaultConnIdleTimeout = 5 * time.Minute
	// A peer that stops reading must not stall the session loop.
	sttRelayDefaultWriteTimeout = 10 * time.Second
	sttRelayDefaultPingInterval = 10 * time.Second
	// The stream is given up on when no byte arrived from it for this long. A
	// false alarm loses terminals and a late alarm only delays them.
	sttRelayDefaultLivenessTimeout = 30 * time.Second
	sttRelayDefaultTickInterval    = time.Second
	// Messages a reader may hand to the session loop ahead of processing.
	sttRelayInboundBuffer = 2
	// The close frame is a courtesy and must not wait for a client that stopped reading.
	sttRelayCloseWriteTimeout = time.Second

	sttRelayUnavailableMessage = "speech recognition is unavailable"
	sttRelayBusyMessage        = "speech recognition is busy"
	sttRelayFailedMessage      = "the speech recognition stream failed"
	sttRelayWAVMessage         = "WAV frames are not accepted: send raw 16 kHz mono PCM16"
	sttRelayLimitMessage       = "a frame exceeded the size limit; the session is closed"
)

// sttUpstream is the RenCrow_STT stream as the relay uses it.
// *sttinfra.StreamConn is the implementation; tests replace it.
type sttUpstream interface {
	Send(sttinfra.StreamFrame) error
	Receive() (sttinfra.StreamFrame, error)
	Ping() error
	LastReceived() time.Time
	Close() error
}

// sttUpstreamOpener opens the stream, sends sessionStart and returns the first
// text frame back (session_ready).
type sttUpstreamOpener func(ctx context.Context, sessionStart []byte) (sttUpstream, []byte, error)

// streamClientOpener adapts the WebSocket client to the relay's seam.
func streamClientOpener(client sttinfra.StreamClient) sttUpstreamOpener {
	return func(ctx context.Context, sessionStart []byte) (sttUpstream, []byte, error) {
		conn, ready, err := client.Open(ctx, sessionStart)
		if err != nil {
			return nil, nil, err
		}
		return conn, ready, nil
	}
}

// sttTerminalRecorder takes the record of a terminal without ever blocking.
type sttTerminalRecorder interface {
	Submit(sttinfra.UtteranceRecord) bool
}

// relayClock is the time the session loop lives by. Tests move it by hand.
type relayClock interface {
	Now() time.Time
	NewTicker(d time.Duration) (<-chan time.Time, func())
}

type realRelayClock struct{}

func (realRelayClock) Now() time.Time { return time.Now() }

func (realRelayClock) NewTicker(d time.Duration) (<-chan time.Time, func()) {
	ticker := time.NewTicker(d)
	return ticker.C, ticker.Stop
}

// sttRelayConfig bounds and wires the /stt relay. The zero value of every limit
// means its default.
type sttRelayConfig struct {
	// Enabled is false when stt.enabled is off: the connection is told so and closed.
	Enabled bool
	// Open opens the stream. Required when Enabled.
	Open sttUpstreamOpener
	// Recorder receives the record of each terminal. nil records nothing.
	Recorder sttTerminalRecorder
	// RequestID makes the correlation id sent in session.start.
	RequestID func() string
	Clock     relayClock
	Logf      func(format string, args ...any)

	MaxConnections int
	// MaxPending bounds the utterances without a terminal event (default 64).
	MaxPending int
	// MaxClientFrameBytes bounds one client frame.
	MaxClientFrameBytes int
	// ConnIdleTimeout drops a connection with nothing waiting and nothing received.
	ConnIdleTimeout time.Duration
	WriteTimeout    time.Duration
	PingInterval    time.Duration
	// LivenessTimeout is how long the stream may go without delivering a byte.
	LivenessTimeout time.Duration
	TickInterval    time.Duration
}

func (c sttRelayConfig) withDefaults() sttRelayConfig {
	if c.MaxConnections <= 0 {
		c.MaxConnections = sttRelayDefaultMaxConnections
	}
	if c.MaxPending <= 0 {
		c.MaxPending = modulestt.DefaultRelayMaxPending
	}
	if c.MaxClientFrameBytes <= 0 {
		c.MaxClientFrameBytes = sttRelayMaxClientFrameBytes
	}
	if c.ConnIdleTimeout <= 0 {
		c.ConnIdleTimeout = sttRelayDefaultConnIdleTimeout
	}
	if c.WriteTimeout <= 0 {
		c.WriteTimeout = sttRelayDefaultWriteTimeout
	}
	if c.PingInterval <= 0 {
		c.PingInterval = sttRelayDefaultPingInterval
	}
	if c.LivenessTimeout <= 0 {
		c.LivenessTimeout = sttRelayDefaultLivenessTimeout
	}
	if c.TickInterval <= 0 {
		c.TickInterval = sttRelayDefaultTickInterval
	}
	if c.RequestID == nil {
		c.RequestID = func() string { return "stt-" + uuid.NewString() }
	}
	if c.Clock == nil {
		c.Clock = realRelayClock{}
	}
	if c.Logf == nil {
		c.Logf = log.Printf
	}
	return c
}

// newSTTRelayHandler serves /stt.
func newSTTRelayHandler(cfg sttRelayConfig) http.Handler {
	cfg = cfg.withDefaults()
	upgrade := websocket.Handler(func(conn *websocket.Conn) {
		defer conn.Close()
		// The library default is 32 MiB per frame, read whole into memory.
		conn.MaxPayloadBytes = cfg.MaxClientFrameBytes
		if !cfg.Enabled || cfg.Open == nil {
			_ = sendSTTEvent(conn, modulestt.BuildErrorEvent("stt provider is not configured"))
			return
		}
		newSTTRelaySession(conn, cfg).run()
	})
	slots := make(chan struct{}, cfg.MaxConnections)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			http.Error(w, "too many STT connections", http.StatusServiceUnavailable)
			return
		}
		upgrade.ServeHTTP(w, r)
	})
}

func sendSTTEvent(conn *websocket.Conn, event map[string]any) error {
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return sttinfra.SendFrame(conn, sttinfra.StreamFrame{Kind: sttinfra.FrameText, Data: body})
}

type sttClientItem struct {
	frame sttinfra.StreamFrame
	err   error
}

type sttUpstreamItem struct {
	frame sttinfra.StreamFrame
	err   error
}

// sttRelaySession is one /stt connection and its stream. Only run's goroutine
// touches its fields, apart from the two readers, which only use conn, up and
// the channels.
type sttRelaySession struct {
	conn *websocket.Conn
	cfg  sttRelayConfig

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	up        sttUpstream
	requestID string
	ready     modulestt.SessionReady
	table     *modulestt.RelayTable

	clientIn chan sttClientItem
	upIn     chan sttUpstreamItem

	seq        int64
	clientDown bool
	ended      bool
	// sessionErrorRelayed is set once STT's own session-level error went to the
	// client: it ended the session with a reason, so CORE adds none of its own.
	sessionErrorRelayed bool
	lastInbound         time.Time
	lastPing            time.Time
	traces              map[string]*sttTimingTrace
}

func newSTTRelaySession(conn *websocket.Conn, cfg sttRelayConfig) *sttRelaySession {
	ctx, cancel := context.WithCancel(context.Background())
	return &sttRelaySession{
		conn: conn, cfg: cfg, ctx: ctx, cancel: cancel,
		clientIn: make(chan sttClientItem, sttRelayInboundBuffer),
		upIn:     make(chan sttUpstreamItem, sttRelayInboundBuffer),
		traces:   make(map[string]*sttTimingTrace),
	}
}

func (s *sttRelaySession) run() {
	defer s.shutdown()
	if !s.openUpstream() {
		return
	}
	// Only now, with the stream open and validated, does the client hear ready.
	// What the Viewer sends before that waits in the socket, unread.
	s.send(modulestt.BuildSessionInfoEvent(s.ready.Provider), "")
	s.send(modulestt.BuildReadyEvent(s.ready.Limits.MaxUtterance), "")
	if s.clientDown {
		return
	}
	now := s.cfg.Clock.Now()
	s.lastInbound, s.lastPing = now, now

	s.wg.Add(2)
	go s.readClient()
	go s.readUpstream()
	tick, stopTick := s.cfg.Clock.NewTicker(s.cfg.TickInterval)
	defer stopTick()

	for !s.ended {
		select {
		case item := <-s.clientIn:
			s.onClient(item)
		case item := <-s.upIn:
			s.onUpstream(item)
		case <-tick:
			s.onTick()
		}
		if s.clientDown && !s.ended {
			s.clientGone(errors.New("write to the client failed"))
		}
	}
}

// shutdown closes both ends, in the order that unblocks both readers, and waits
// for them. It runs once, when the loop ends.
func (s *sttRelaySession) shutdown() {
	s.cancel()
	if s.up != nil {
		_ = s.up.Close()
	}
	_ = s.conn.SetWriteDeadline(time.Now().Add(sttRelayCloseWriteTimeout))
	_ = s.conn.Close()
	s.wg.Wait()
}

func (s *sttRelaySession) openUpstream() bool {
	s.requestID = s.cfg.RequestID()
	if !modulestt.ValidStreamRequestID(s.requestID) {
		s.cfg.Logf("[STT][relay] ERROR the generated request id is not valid for the STT stream")
		s.sendSessionError(modulestt.ErrorCodeSTTFailed, sttRelayUnavailableMessage)
		return false
	}
	start, err := json.Marshal(modulestt.BuildSessionStartMessage(s.requestID))
	if err != nil {
		s.cfg.Logf("[STT][relay] ERROR could not build session.start: %v", err)
		s.sendSessionError(modulestt.ErrorCodeSTTFailed, sttRelayUnavailableMessage)
		return false
	}
	up, readyFrame, err := s.cfg.Open(s.ctx, start)
	if err != nil {
		s.cfg.Logf("[STT][relay] WARN could not open the STT stream: %v", err)
		var openErr *sttinfra.StreamOpenError
		if errors.As(err, &openErr) && openErr.Status == http.StatusServiceUnavailable {
			// STT is at its connection limit: nothing was started, and a retry may succeed.
			s.sendSessionError(modulestt.ErrorCodeSTTBusy, sttRelayBusyMessage)
		} else {
			s.sendSessionError(modulestt.ErrorCodeSTTFailed, sttRelayUnavailableMessage)
		}
		return false
	}
	ready, err := modulestt.ParseSessionReady(readyFrame, s.requestID)
	if err != nil {
		_ = up.Close()
		s.cfg.Logf("[STT][relay] WARN the STT stream is not usable: %v", err)
		s.sendSessionError(modulestt.ErrorCodeSTTFailed, sttRelayUnavailableMessage)
		return false
	}
	s.up, s.ready = up, ready
	s.table = modulestt.NewRelayTable(ready.Limits, s.cfg.MaxPending)
	s.table.NoteSessionReady(ready.Seq)
	return true
}

func (s *sttRelaySession) readClient() {
	defer s.wg.Done()
	for {
		frame, err := sttinfra.ReceiveFrame(s.conn)
		select {
		case s.clientIn <- sttClientItem{frame: frame, err: err}:
		case <-s.ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

func (s *sttRelaySession) readUpstream() {
	defer s.wg.Done()
	for {
		frame, err := s.up.Receive()
		select {
		case s.upIn <- sttUpstreamItem{frame: frame, err: err}:
		case <-s.ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

// onClient handles one frame from the client. A text frame is a control
// message and a binary frame is audio; the kind alone decides, never the content.
func (s *sttRelaySession) onClient(item sttClientItem) {
	if item.err != nil {
		s.clientGone(item.err)
		return
	}
	s.lastInbound = s.cfg.Clock.Now()
	switch item.frame.Kind {
	case sttinfra.FrameText:
		s.onClientControl(item.frame.Data)
	case sttinfra.FrameBinary:
		s.onClientAudio(item.frame.Data)
	}
}

func (s *sttRelaySession) onClientControl(data []byte) {
	control, ok := modulestt.ParseControl(data)
	if !ok {
		s.cfg.Logf("[STT][relay] dropped a text frame that is not a control message (%d bytes)", len(data))
		return
	}
	decision := s.table.MapControl(control, s.cfg.Clock.Now())
	if decision.ClientEvent != nil {
		s.send(decision.ClientEvent, "")
	}
	if decision.Upstream == nil {
		if decision.ClientEvent == nil {
			s.cfg.Logf("[STT][relay] dropped the control message %q: it is not relayed", control.Type)
		}
		return
	}
	body, err := json.Marshal(decision.Upstream)
	if err != nil {
		s.cfg.Logf("[STT][relay] ERROR could not marshal a control message: %v", err)
		return
	}
	if s.sendUpstream(sttinfra.StreamFrame{Kind: sttinfra.FrameText, Data: body}) && decision.Stop {
		s.noteStop(decision.StopUtteranceID)
	}
}

func (s *sttRelaySession) onClientAudio(data []byte) {
	if len(data) == 0 {
		return
	}
	if modulestt.IsWAV(data) {
		// A WAV header played as PCM is a burst of noise, so it is refused. CORE
		// does not follow utterances, so the error names none.
		s.send(modulestt.BuildErrorEventWithCode(modulestt.ErrorCodeUnsupportedAudioFormat, sttRelayWAVMessage), "")
		return
	}
	for _, part := range modulestt.SplitStreamFrames(data, s.ready.Limits.MaxFrameBytes) {
		if !s.sendUpstream(sttinfra.StreamFrame{Kind: sttinfra.FrameBinary, Data: part}) {
			return
		}
	}
}

// sendUpstream writes one frame to the stream. A failed write means the stream
// is gone, and ends the session through the completion path.
func (s *sttRelaySession) sendUpstream(frame sttinfra.StreamFrame) bool {
	if err := s.up.Send(frame); err != nil {
		s.failUpstream("write to the STT stream failed: " + err.Error())
		return false
	}
	return true
}

// onUpstream handles one frame from the stream.
func (s *sttRelaySession) onUpstream(item sttUpstreamItem) {
	if item.err != nil {
		s.failUpstream("the STT stream ended: " + item.err.Error())
		return
	}
	if item.frame.Kind != sttinfra.FrameText {
		s.failUpstream("the STT stream sent a frame that is not text")
		return
	}
	event, err := modulestt.ParseUpstreamEvent(item.frame.Data)
	if err != nil {
		s.failUpstream(err.Error())
		return
	}
	now := s.cfg.Clock.Now()
	decision := s.table.HandleUpstream(event, now)
	switch {
	case decision.Violation != "":
		s.failUpstream(decision.Violation)
	case decision.Forward:
		s.send(decision.Event, decision.UtteranceID)
		if decision.UtteranceID == "" && event.Type == modulestt.WebSocketEventTypeError {
			s.sessionErrorRelayed = true
		}
		s.traceUpstream(event.Type, decision.UtteranceID, now)
		if decision.Record != nil {
			// The record is decided by the terminal STT sent, not by whether the
			// client could be told; the target was called either way.
			s.submit(sttinfra.RecordFromTerminal(s.requestID, *decision.Record))
		}
	case decision.Drop != "":
		s.cfg.Logf("[STT][relay] dropped an STT event of type %q: %s", event.Type, decision.Drop)
	}
}

// onTick runs the checks that depend on time: the ping, the liveness of the
// stream, the watchdog of terminal events, and the idle limit.
func (s *sttRelaySession) onTick() {
	now := s.cfg.Clock.Now()
	if now.Sub(s.lastPing) >= s.cfg.PingInterval {
		s.lastPing = now
		if err := s.up.Ping(); err != nil {
			s.failUpstream("ping to the STT stream failed: " + err.Error())
			return
		}
	}
	if silent := now.Sub(s.up.LastReceived()); silent > s.cfg.LivenessTimeout {
		s.failUpstream(fmt.Sprintf("no byte from the STT stream for %s", silent.Round(time.Second)))
		return
	}
	if overdue := s.table.Expired(now); len(overdue) > 0 {
		s.failUpstream(fmt.Sprintf("%d utterance(s) are past the time STT promised a terminal event", len(overdue)))
		return
	}
	if len(s.table.Pending()) == 0 && now.Sub(s.lastInbound) >= s.cfg.ConnIdleTimeout {
		s.ended = true // nothing is owed and nothing arrives: the peer holds a slot for no reason
	}
}

// failUpstream ends the session because the stream cannot be relied on any
// more (it ended, went silent, broke the protocol, or an utterance is overdue).
// Every utterance without a terminal event gets exactly one error, in the order
// it was announced, then the session ends with a session-level error. STT keeps
// no state, so nothing is retried: the client reconnects. These terminals are
// CORE's own and are not recorded.
func (s *sttRelaySession) failUpstream(reason string) {
	if s.ended {
		return
	}
	waiting := s.table.Drain()
	s.cfg.Logf("[STT][relay] WARN the STT stream failed with %d utterance(s) waiting: %s", len(waiting), reason)
	for _, id := range waiting {
		s.send(modulestt.BuildErrorEventWithCode(modulestt.ErrorCodeSTTFailed, sttRelayFailedMessage), id)
	}
	if !s.sessionErrorRelayed {
		s.send(modulestt.BuildErrorEventWithCode(modulestt.ErrorCodeSTTFailed, sttRelayFailedMessage), "")
	}
	s.ended = true
}

// clientGone ends the session because the client left (or sent a frame over the
// limit, or could not be written to). Closing the stream aborts whatever STT was
// doing, and STT answers nothing to a closed stream, so each waiting utterance is
// recorded as interrupted by the disconnect. target_called is not known.
func (s *sttRelaySession) clientGone(cause error) {
	if s.ended {
		return
	}
	waiting := s.table.Drain()
	if errors.Is(cause, websocket.ErrFrameTooLarge) {
		s.cfg.Logf("[STT][relay] WARN a client frame exceeded the size limit; closing with %d utterance(s) waiting", len(waiting))
		for _, id := range waiting {
			s.send(modulestt.BuildErrorEventWithCode(modulestt.ErrorCodeLimitExceeded, sttRelayLimitMessage), id)
		}
	}
	for _, id := range waiting {
		s.submit(sttinfra.AbortedRecord(s.requestID, id))
	}
	s.ended = true
}

func (s *sttRelaySession) submit(rec sttinfra.UtteranceRecord) {
	if s.cfg.Recorder != nil {
		s.cfg.Recorder.Submit(rec) // never blocks; a loss is counted and logged by the recorder
	}
}

func (s *sttRelaySession) sendSessionError(code, message string) {
	s.send(modulestt.BuildErrorEventWithCode(code, message), "")
}

// send stamps the event with the session's own seq and writes it to the client
// as a text frame. A failed write marks the client down; the loop then ends the
// session.
func (s *sttRelaySession) send(event map[string]any, utteranceID string) {
	if s.clientDown {
		return
	}
	s.seq++
	body, err := json.Marshal(modulestt.StampUtteranceEvent(event, utteranceID, s.seq))
	if err != nil {
		s.cfg.Logf("[STT][relay] ERROR could not marshal an event for the client: %v", err)
		return
	}
	_ = s.conn.SetWriteDeadline(time.Now().Add(s.cfg.WriteTimeout))
	err = sttinfra.SendFrame(s.conn, sttinfra.StreamFrame{Kind: sttinfra.FrameText, Data: body})
	// The deadline belongs to this write. The library answers a ping from inside
	// the client reader with whatever deadline is current; one left behind would
	// have expired and the answer would fail without anyone noticing.
	_ = s.conn.SetWriteDeadline(time.Time{})
	if err != nil {
		s.clientDown = true
	}
}

// noteStop feeds the timing trace (STT_TIMING_TRACE) with the one moment the
// relay can observe: when stop went to STT.
func (s *sttRelaySession) noteStop(utteranceID string) {
	if !sttTimingTraceEnabled() {
		return
	}
	now := s.cfg.Clock.Now()
	if utteranceID != "" {
		if trace := s.traces[utteranceID]; trace != nil {
			trace.markStop(now)
		}
		return
	}
	for _, trace := range s.traces {
		trace.markStop(now)
	}
}

func (s *sttRelaySession) traceUpstream(eventType, utteranceID string, now time.Time) {
	if !sttTimingTraceEnabled() || utteranceID == "" {
		return
	}
	switch eventType {
	case modulestt.WebSocketEventTypeSpeechStart:
		s.traces[utteranceID] = newSTTTimingTrace("relay", now)
	case modulestt.WebSocketEventTypeFinal, modulestt.WebSocketEventTypeError, modulestt.WebSocketEventTypeCancelled:
		if trace := s.traces[utteranceID]; trace != nil {
			trace.logTerminal(eventType, now)
		}
		delete(s.traces, utteranceID)
	}
}
