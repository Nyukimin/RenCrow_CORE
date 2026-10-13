package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	sttinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/stt"
	modulestt "github.com/Nyukimin/RenCrow_CORE/modules/stt"
	"golang.org/x/net/websocket"
)

// The Viewer endpoints speech with its own VAD and sends one utterance as
// start, PCM16 frames, stop. This session accumulates the frames of an
// utterance and calls the provider once for the whole utterance. It owns no
// speech detection: MaxLength and IdleTimeout are safety valves only.
//
// Protocol "utterance.v1" (docs/06_Public_API仕様.md): every utterance gets
// exactly one terminal event (final, error or cancelled), in utterance order.
// Clients that predate utterance_id keep working: CORE assigns the id.

const (
	defaultSTTQueueDepth = 8

	sttErrorBusy         = "stt_busy"
	sttErrorFailed       = "stt_failed"
	sttErrorProvider     = "stt_provider_error"
	sttErrorUnsupported  = "unsupported_audio_format"
	sttErrorLimit        = "stt_limit_exceeded"
	sttMaxIdleCheckEvery = 250 * time.Millisecond
	sttMinIdleCheckEvery = 10 * time.Millisecond
	// A peer that stops reading must not stall the session loop and its idle check.
	sttWriteTimeout = 10 * time.Second

	// Resource limits of /stt. Every one bounds something a client controls.
	//
	// A frame carries at most one maximum-length utterance (PCM at 32000 B/s) plus
	// a container header and a control message of slack; 30 s is about 0.96 MB.
	sttFrameSlackBytes = 8 * 1024
	sttMinFrameBytes   = 64 * 1024
	// Unsent terminal events per session, whatever their kind. Each is a few
	// hundred bytes; transcription jobs hold audio and are counted separately by
	// QueueDepth.
	defaultSTTMaxQueuedEvents = 64
	// Concurrent /stt connections. The Viewer opens one per utterance.
	defaultSTTMaxConnections = 16
	// A connection with nothing active, queued or received for this long is dropped.
	defaultSTTConnIdleTimeout = 5 * time.Minute
	// After a limit is exceeded the session flushes the terminal events it owes,
	// then closes; this bounds how long a stuck recognition can delay that.
	sttCloseGrace = 5 * time.Second
	// Messages the reader may hand to the session loop ahead of processing.
	sttInboundBuffer = 2

	sttLimitMessage = "the session exceeded a resource limit and is closed"
)

// sttSessionConfig bounds one /stt WebSocket session.
type sttSessionConfig struct {
	Limits       modulestt.UtteranceLimits
	NoisePhrases []string
	// QueueDepth bounds the utterances of one session that wait for or are in
	// recognition. Beyond it an utterance ends with an explicit stt_busy error.
	QueueDepth int
	// MaxFrameBytes bounds one WebSocket frame; a larger one closes the session.
	// Zero means sttMaxFrameBytes(Limits).
	MaxFrameBytes int
	// MaxQueuedEvents bounds the unsent terminal events of one session; one more
	// closes the session.
	MaxQueuedEvents int
	// MaxConnections bounds concurrent /stt connections; more are refused with 503.
	MaxConnections int
	// ConnIdleTimeout drops a connection that has nothing to do and sends nothing.
	ConnIdleTimeout time.Duration
}

func (c sttSessionConfig) withDefaults() sttSessionConfig {
	c.Limits = c.Limits.WithDefaults()
	if c.QueueDepth <= 0 {
		c.QueueDepth = defaultSTTQueueDepth
	}
	if c.MaxFrameBytes <= 0 {
		c.MaxFrameBytes = sttMaxFrameBytes(c.Limits)
	}
	if c.MaxQueuedEvents <= 0 {
		c.MaxQueuedEvents = defaultSTTMaxQueuedEvents
	}
	if c.MaxConnections <= 0 {
		c.MaxConnections = defaultSTTMaxConnections
	}
	if c.ConnIdleTimeout <= 0 {
		c.ConnIdleTimeout = defaultSTTConnIdleTimeout
	}
	return c
}

// sttMaxFrameBytes is the largest WebSocket frame accepted: one maximum-length
// utterance of 16 kHz PCM16 plus slack for a WAV header or a control message.
func sttMaxFrameBytes(limits modulestt.UtteranceLimits) int {
	limits = limits.WithDefaults()
	pcmBytes := int(limits.MaxLength * modulestt.DefaultSampleRate * 2 / time.Second)
	if frame := pcmBytes + sttFrameSlackBytes; frame > sttMinFrameBytes {
		return frame
	}
	return sttMinFrameBytes
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func handleSTTWebSocketProvider(provider sttinfra.Provider) http.Handler {
	return handleSTTWebSocketProviderWithConfig(provider, sttSessionConfig{})
}

func handleSTTWebSocketProviderWithConfig(provider sttinfra.Provider, cfg sttSessionConfig) http.Handler {
	cfg = cfg.withDefaults()
	filter := modulestt.NewNoisePhraseFilter(cfg.NoisePhrases)
	upgrade := websocket.Handler(func(conn *websocket.Conn) {
		defer conn.Close()
		// The library default is 32 MiB per frame, read whole into memory.
		conn.MaxPayloadBytes = cfg.MaxFrameBytes
		if provider == nil {
			_ = sendSTTError(conn, "stt provider is not configured")
			return
		}
		newSTTSession(conn, provider, cfg, filter).run()
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

// sttUtterance is the utterance being received. Only the session loop touches it.
type sttUtterance struct {
	id string
	// origin is the id the client chose. A continuation created by the max-length
	// split keeps it, so stop/cancel naming the original id still reach it.
	origin string
	buf    *modulestt.UtteranceBuffer
	lastAt time.Time
	trace  *sttTimingTrace
}

type sttJobKind int

const (
	sttJobTranscribe sttJobKind = iota
	sttJobFinal                 // terminal final without recognition (empty_reason set)
	sttJobError
	sttJobCancelled
	sttJobClose // last job of an aborted session: every terminal event before it has been sent
)

type sttCloseInfo struct{ code, message string }

// sttJob is one terminal event owed to the client. Every utterance ends in a
// job, and jobs are worked in order, so terminals never overtake each other.
type sttJob struct {
	kind        sttJobKind
	id          string
	endReason   string
	audio       time.Duration
	emptyReason string
	errCode     string
	errMessage  string
	trace       *sttTimingTrace

	wav           []byte
	ctx           context.Context
	cancel        context.CancelFunc
	userCancelled atomic.Bool
}

type sttSession struct {
	conn     *websocket.Conn
	provider sttinfra.Provider
	cfg      sttSessionConfig
	filter   *modulestt.NoisePhraseFilter

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	sendMu sync.Mutex
	seq    int64

	queue *sttJobQueue

	mu   sync.Mutex
	byID map[string]*sttJob // transcription jobs queued or in flight
	// closeInfo is set once when the session is aborted for exceeding a limit.
	closeInfo   *sttCloseInfo
	closeTimer  *time.Timer
	lastInbound time.Time // owned by the session loop

	// Owned by the session loop.
	active  *sttUtterance
	counter int
}

func newSTTSession(conn *websocket.Conn, provider sttinfra.Provider, cfg sttSessionConfig, filter *modulestt.NoisePhraseFilter) *sttSession {
	ctx, cancel := context.WithCancel(context.Background())
	return &sttSession{
		conn:     conn,
		provider: provider,
		cfg:      cfg,
		filter:   filter,
		ctx:      ctx,
		cancel:   cancel,
		queue:    newSTTJobQueue(),
		byID:     make(map[string]*sttJob),
	}
}

// sttInbound is one thing the reader hands to the session loop: a message, or
// the reason the stream can no longer be read (an oversized frame).
type sttInbound struct {
	payload []byte
	err     error
}

func (s *sttSession) run() {
	defer func() {
		// Disconnect, a failed send, or the end of an aborted session: cancel
		// in-flight and queued recognition.
		s.cancel()
		s.mu.Lock()
		if s.closeTimer != nil {
			s.closeTimer.Stop()
		}
		s.mu.Unlock()
		s.wg.Wait()
	}()
	s.send(modulestt.BuildSessionInfoEvent(s.provider.Name()), "")
	s.send(modulestt.BuildReadyEvent(s.cfg.Limits.MaxLength), "")

	s.lastInbound = time.Now()
	inbound := make(chan sttInbound, sttInboundBuffer)
	go s.readLoop(inbound)
	s.wg.Add(1)
	go s.worker()

	idleTick := time.NewTicker(s.idleCheckInterval())
	defer idleTick.Stop()
	for {
		select {
		case in, ok := <-inbound:
			if !ok {
				if !s.closing() {
					return // the peer is gone
				}
				inbound = nil // aborted: keep running until the owed terminals are sent
				continue
			}
			if in.err != nil {
				s.abort(sttErrorLimit, "a frame exceeded the size limit; "+sttLimitMessage)
				continue
			}
			s.handle(in.payload)
		case now := <-idleTick.C:
			s.closeIfIdle(now)
		case <-s.ctx.Done():
			return
		}
	}
}

func (s *sttSession) idleCheckInterval() time.Duration {
	interval := s.cfg.Limits.IdleTimeout / 4
	if interval > sttMaxIdleCheckEvery {
		interval = sttMaxIdleCheckEvery
	}
	if interval < sttMinIdleCheckEvery {
		interval = sttMinIdleCheckEvery
	}
	return interval
}

// readLoop has no read deadline: a deadline that fires mid-frame can corrupt
// the stream. Silence is handled by the session loop's idle check instead.
// A frame over MaxFrameBytes is not read off the wire: the loop reports it and
// stops, and the connection is closed rather than draining what the peer sends.
func (s *sttSession) readLoop(inbound chan<- sttInbound) {
	defer close(inbound)
	for {
		var payload []byte
		err := websocket.Message.Receive(s.conn, &payload)
		if errors.Is(err, websocket.ErrFrameTooLarge) {
			select {
			case inbound <- sttInbound{err: err}:
			case <-s.ctx.Done():
			}
			return
		}
		if err != nil {
			return
		}
		select {
		case inbound <- sttInbound{payload: payload}:
		case <-s.ctx.Done():
			return
		}
	}
}

func (s *sttSession) handle(payload []byte) {
	if s.closing() {
		return // an aborted session takes no more input
	}
	s.lastInbound = time.Now()
	if len(payload) == 0 {
		return
	}
	if control, ok := modulestt.ParseControl(payload); ok {
		s.handleControl(control)
		return
	}
	s.handleAudio(payload)
}

func (s *sttSession) handleControl(control modulestt.Control) {
	switch {
	case control.Type == "start":
		s.handleStart(control)
	case modulestt.IsFinalizeControl(control.Type):
		s.handleStop(control)
	case control.Type == "cancel":
		s.handleCancel(control)
	default:
		// "config" and other legacy messages carry nothing CORE needs.
	}
}

func (s *sttSession) handleStart(control modulestt.Control) {
	if message := control.AudioFormatError(); message != "" {
		// The rejected start never began an utterance and leaves the active one
		// alone, so its error must not borrow the id of a live utterance.
		id := control.UtteranceID
		if s.idInUse(id) {
			id = ""
		}
		s.push(&sttJob{kind: sttJobError, id: id, errCode: sttErrorUnsupported, errMessage: message})
		return
	}
	if s.active != nil {
		s.finalize(modulestt.EndReasonRestart)
	}
	s.begin(control.UtteranceID)
}

func (s *sttSession) handleStop(control modulestt.Control) {
	if s.addressesActive(control.UtteranceID) {
		s.finalize(modulestt.EndReasonStop)
		return
	}
	if s.active == nil && control.UtteranceID == "" {
		// A client waiting for its final must never hang: the utterance it wants
		// to stop was already closed (idle timeout, max length) or never began.
		s.push(&sttJob{kind: sttJobFinal, endReason: modulestt.EndReasonStop, emptyReason: modulestt.EmptyReasonNoActiveUtterance})
	}
	// A stop that names an already-finished utterance has nothing left to do.
}

func (s *sttSession) handleCancel(control modulestt.Control) {
	if s.addressesActive(control.UtteranceID) {
		id := s.active.id
		s.active = nil
		s.push(&sttJob{kind: sttJobCancelled, id: id})
		return
	}
	if control.UtteranceID == "" {
		return
	}
	s.mu.Lock()
	job := s.byID[control.UtteranceID]
	s.mu.Unlock()
	if job != nil {
		job.userCancelled.Store(true)
		job.cancel()
	}
}

func (s *sttSession) handleAudio(payload []byte) {
	pcm := payload
	if modulestt.IsWAV(payload) {
		var err error
		if pcm, err = modulestt.WAVToPCM16(payload); err != nil {
			// Audio CORE cannot use ends the utterance it belonged to, with that
			// utterance's single terminal event.
			id := ""
			if s.active != nil {
				id = s.active.id
				s.active = nil
			}
			s.push(&sttJob{kind: sttJobError, id: id, errCode: sttErrorUnsupported, errMessage: err.Error()})
			return
		}
	}
	if s.active == nil {
		// Without a start control only audible audio opens an utterance, so a
		// client streaming silence does not produce empty utterances.
		if !modulestt.IsVoicedPCM(pcm, s.cfg.Limits.SilenceAbsThreshold) {
			return
		}
		s.begin("")
	}
	s.append(pcm, time.Now())
}

func (s *sttSession) append(pcm []byte, now time.Time) {
	s.active.lastAt = now
	s.active.trace.markAudio(now)
	rest := s.active.buf.Append(pcm)
	for len(rest) > 0 {
		origin := s.active.origin
		s.finalize(modulestt.EndReasonMaxLength)
		s.begin("")
		s.active.origin = origin
		s.active.lastAt = now
		next := s.active.buf.Append(rest)
		if len(next) >= len(rest) {
			return // no progress; drop the remainder rather than spin
		}
		rest = next
	}
}

// idInUse reports whether id names the active utterance or one still waiting
// for or in recognition. Terminal events are matched to utterances by id, so a
// live id is never reused.
func (s *sttSession) idInUse(id string) bool {
	if id == "" {
		return false
	}
	if s.active != nil && s.active.id == id {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, taken := s.byID[id]
	return taken
}

// addressesActive reports whether a control naming id (or naming none) applies
// to the active utterance.
func (s *sttSession) addressesActive(id string) bool {
	if s.active == nil {
		return false
	}
	return id == "" || id == s.active.id || (s.active.origin != "" && id == s.active.origin)
}

func (s *sttSession) begin(requestedID string) {
	id, origin := requestedID, requestedID
	if id == "" || s.idInUse(id) {
		origin = ""
		for {
			s.counter++
			id = fmt.Sprintf("srv-%d", s.counter)
			if !s.idInUse(id) {
				break
			}
		}
	}
	s.active = &sttUtterance{
		id:     id,
		origin: origin,
		buf:    modulestt.NewUtteranceBuffer(s.cfg.Limits),
		lastAt: time.Now(),
		trace:  newSTTTimingTrace("utterance"),
	}
	s.send(modulestt.BuildSpeechStartEvent(), id)
}

func (s *sttSession) closeIfIdle(now time.Time) {
	if s.closing() {
		return
	}
	if s.active != nil && now.Sub(s.active.lastAt) >= s.cfg.Limits.IdleTimeout {
		s.finalize(modulestt.EndReasonIdleTimeout)
	}
	// Nothing active, nothing owed and nothing received: the peer holds a slot
	// (and a goroutine) for no reason.
	if s.active == nil && s.queue.outstanding() == 0 && now.Sub(s.lastInbound) >= s.cfg.ConnIdleTimeout {
		s.cancel()
	}
}

// finalize closes the active utterance and queues its terminal event.
func (s *sttSession) finalize(endReason string) {
	u := s.active
	s.active = nil
	u.trace.markVoice(time.Now())
	job := &sttJob{id: u.id, endReason: endReason, audio: u.buf.Duration(), trace: u.trace}
	if reason := u.buf.EmptyReason(); reason != "" {
		job.kind = sttJobFinal
		job.emptyReason = reason
		s.push(job)
		return
	}
	if s.queueFull() {
		s.overLimit(job) // before admit: nothing to give back
		return
	}
	if !s.queue.admit(s.cfg.QueueDepth) {
		job.kind = sttJobError
		job.errCode = sttErrorBusy
		job.errMessage = "too many utterances are waiting for recognition"
		s.push(job)
		return
	}
	job.kind = sttJobTranscribe
	job.wav = u.buf.WAV()
	job.ctx, job.cancel = context.WithCancel(s.ctx)
	s.mu.Lock()
	s.byID[job.id] = job
	s.mu.Unlock()
	s.queue.push(job)
}

// queueFull reports whether the session already owes MaxQueuedEvents events.
func (s *sttSession) queueFull() bool {
	return s.queue.outstanding() >= s.cfg.MaxQueuedEvents
}

// push queues a terminal event, or ends the session when the queue is full. A
// client cannot make the queue grow without bound by repeating controls that
// each owe an answer while a recognition is in progress.
func (s *sttSession) push(job *sttJob) {
	if s.queueFull() {
		s.overLimit(job)
		return
	}
	s.queue.push(job)
}

// overLimit turns the job that did not fit into the error that explains the
// close, so its utterance still gets its one terminal event, then aborts.
func (s *sttSession) overLimit(job *sttJob) {
	job.kind = sttJobError
	job.errCode = sttErrorLimit
	job.errMessage = "too many unanswered events; " + sttLimitMessage
	job.wav = nil
	s.queue.push(job)
	s.abort(sttErrorLimit, job.errMessage)
}

func (s *sttSession) closing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeInfo != nil
}

func (s *sttSession) closeReason() (sttCloseInfo, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closeInfo == nil {
		return sttCloseInfo{}, false
	}
	return *s.closeInfo, true
}

// abort ends the session because a limit was exceeded. Every utterance that has
// no terminal event yet gets exactly one error that names the limit: the one
// being received, the queued ones (never sent to the engine) and the one in
// recognition (its request is cancelled). The loop then ignores input, the
// worker sends those events in utterance order, and the last job closes the
// connection; sttCloseGrace bounds the wait.
func (s *sttSession) abort(code, message string) {
	s.mu.Lock()
	if s.closeInfo != nil {
		s.mu.Unlock()
		return
	}
	s.closeInfo = &sttCloseInfo{code: code, message: message}
	s.closeTimer = time.AfterFunc(sttCloseGrace, s.cancel)
	pending := make([]*sttJob, 0, len(s.byID))
	for _, job := range s.byID {
		pending = append(pending, job)
	}
	s.mu.Unlock()

	if s.active != nil {
		id := s.active.id
		s.active = nil
		s.queue.push(&sttJob{kind: sttJobError, id: id, errCode: code, errMessage: message})
	}
	for _, job := range pending {
		job.cancel() // reportCancellation answers it with the close reason
	}
	s.queue.push(&sttJob{kind: sttJobClose})
}

func (s *sttSession) worker() {
	defer s.wg.Done()
	for {
		job, ok := s.queue.pop(s.ctx)
		if !ok || s.ctx.Err() != nil {
			return // the session ended: queued utterances are dropped, not sent
		}
		s.runJob(job)
		s.queue.done()
	}
}

func (s *sttSession) runJob(job *sttJob) {
	switch job.kind {
	case sttJobFinal:
		s.sendFinal(job, "", job.emptyReason)
	case sttJobError:
		s.send(modulestt.BuildErrorEventWithCode(job.errCode, job.errMessage), job.id)
	case sttJobCancelled:
		s.send(modulestt.BuildCancelledEvent(), job.id)
	case sttJobTranscribe:
		s.transcribe(job)
	case sttJobClose:
		s.cancel()
	}
}

func (s *sttSession) transcribe(job *sttJob) {
	defer s.finishTranscription(job)
	if job.ctx.Err() != nil {
		s.reportCancellation(job)
		return
	}
	result, err := s.provider.Transcribe(job.ctx, job.wav)
	if job.ctx.Err() != nil {
		s.reportCancellation(job)
		return
	}
	if err != nil {
		code := sttErrorFailed
		var sttErr *sttinfra.Error
		if errors.As(err, &sttErr) && sttErr.Code != "" {
			code = sttErr.Code
		}
		s.send(modulestt.BuildErrorEventWithCode(code, "stt inference failed: "+err.Error()), job.id)
		return
	}
	outcome := modulestt.ClassifyTranscript(result.Text, s.filter)
	if outcome.ProviderError {
		s.send(modulestt.BuildErrorEventWithCode(sttErrorProvider, modulestt.ProviderTranscriptErrorMessage), job.id)
		return
	}
	s.sendFinal(job, outcome.Text, outcome.EmptyReason)
}

// reportCancellation gives a cancelled recognition its one terminal event: a
// cancel control is answered with cancelled, an abort with the close reason.
// Both are set by the session loop alone, so they cannot both apply. When the
// peer simply went away there is nobody left to tell.
func (s *sttSession) reportCancellation(job *sttJob) {
	if job.userCancelled.Load() {
		s.send(modulestt.BuildCancelledEvent(), job.id)
		return
	}
	if reason, ok := s.closeReason(); ok {
		s.send(modulestt.BuildErrorEventWithCode(reason.code, reason.message), job.id)
	}
}

func (s *sttSession) finishTranscription(job *sttJob) {
	job.cancel()
	s.mu.Lock()
	if s.byID[job.id] == job {
		delete(s.byID, job.id)
	}
	s.mu.Unlock()
	s.queue.release()
}

func (s *sttSession) sendFinal(job *sttJob, text, emptyReason string) {
	if s.send(modulestt.BuildUtteranceFinalEvent(text, job.endReason, emptyReason, job.audio), job.id) && job.trace != nil {
		job.trace.logFinal("session", job.endReason, text)
	}
}

// send stamps the event with the session sequence and writes it. A failed
// write ends the session: the peer is gone.
func (s *sttSession) send(event map[string]any, utteranceID string) bool {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	s.seq++
	_ = s.conn.SetWriteDeadline(time.Now().Add(sttWriteTimeout))
	if err := sendSTTEvent(s.conn, modulestt.StampUtteranceEvent(event, utteranceID, s.seq)); err != nil {
		s.cancel()
		return false
	}
	return true
}

// sttJobQueue is an unbounded in-order queue of terminal-event jobs. Memory is
// bounded by admit: only jobs that hold audio count against the depth.
type sttJobQueue struct {
	mu         sync.Mutex
	items      []*sttJob
	pending    int // transcription jobs holding audio
	unfinished int // jobs pushed and not yet fully handled by the worker
	wake       chan struct{}
}

func newSTTJobQueue() *sttJobQueue {
	return &sttJobQueue{wake: make(chan struct{}, 1)}
}

func (q *sttJobQueue) admit(depth int) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.pending >= depth {
		return false
	}
	q.pending++
	return true
}

func (q *sttJobQueue) release() {
	q.mu.Lock()
	q.pending--
	q.mu.Unlock()
}

// outstanding is the number of terminal events queued or being sent.
func (q *sttJobQueue) outstanding() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.unfinished
}

// done marks the job the worker has just finished.
func (q *sttJobQueue) done() {
	q.mu.Lock()
	q.unfinished--
	q.mu.Unlock()
}

func (q *sttJobQueue) push(job *sttJob) {
	q.mu.Lock()
	q.items = append(q.items, job)
	q.unfinished++
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *sttJobQueue) pop(ctx context.Context) (*sttJob, bool) {
	for {
		if ctx.Err() != nil {
			return nil, false
		}
		q.mu.Lock()
		if len(q.items) > 0 {
			job := q.items[0]
			q.items[0] = nil
			q.items = q.items[1:]
			q.mu.Unlock()
			return job, true
		}
		q.mu.Unlock()
		select {
		case <-q.wake:
		case <-ctx.Done():
			return nil, false
		}
	}
}

func sendSTTEvent(conn *websocket.Conn, event map[string]any) error {
	b, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return websocket.Message.Send(conn, string(b))
}

func sendSTTError(conn *websocket.Conn, message string) error {
	return sendSTTEvent(conn, modulestt.BuildErrorEvent(message))
}
