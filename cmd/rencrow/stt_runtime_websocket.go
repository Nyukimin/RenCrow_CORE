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
	sttMaxIdleCheckEvery = 250 * time.Millisecond
	sttMinIdleCheckEvery = 10 * time.Millisecond
	// A peer that stops reading must not stall the session loop and its idle check.
	sttWriteTimeout = 10 * time.Second
)

// sttSessionConfig bounds one /stt WebSocket session.
type sttSessionConfig struct {
	Limits       modulestt.UtteranceLimits
	NoisePhrases []string
	// QueueDepth bounds the utterances of one session that wait for or are in
	// recognition. Beyond it an utterance ends with an explicit stt_busy error.
	QueueDepth int
}

func (c sttSessionConfig) withDefaults() sttSessionConfig {
	c.Limits = c.Limits.WithDefaults()
	if c.QueueDepth <= 0 {
		c.QueueDepth = defaultSTTQueueDepth
	}
	return c
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
	return websocket.Handler(func(conn *websocket.Conn) {
		defer conn.Close()
		if provider == nil {
			_ = sendSTTError(conn, "stt provider is not configured")
			return
		}
		newSTTSession(conn, provider, cfg, filter).run()
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
)

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

func (s *sttSession) run() {
	defer func() {
		// Disconnect or a failed send: cancel in-flight and queued recognition.
		s.cancel()
		s.wg.Wait()
	}()
	s.send(modulestt.BuildSessionInfoEvent(s.provider.Name()), "")
	s.send(modulestt.BuildReadyEvent(s.cfg.Limits.MaxLength), "")

	messages := make(chan []byte, 64)
	go s.readLoop(messages)
	s.wg.Add(1)
	go s.worker()

	idleTick := time.NewTicker(s.idleCheckInterval())
	defer idleTick.Stop()
	for {
		select {
		case payload, ok := <-messages:
			if !ok {
				return
			}
			s.handle(payload)
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
func (s *sttSession) readLoop(messages chan<- []byte) {
	defer close(messages)
	for {
		var payload []byte
		if err := websocket.Message.Receive(s.conn, &payload); err != nil {
			return
		}
		select {
		case messages <- payload:
		case <-s.ctx.Done():
			return
		}
	}
}

func (s *sttSession) handle(payload []byte) {
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
		s.queue.push(&sttJob{kind: sttJobError, id: id, errCode: sttErrorUnsupported, errMessage: message})
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
		s.queue.push(&sttJob{kind: sttJobFinal, endReason: modulestt.EndReasonStop, emptyReason: modulestt.EmptyReasonNoActiveUtterance})
	}
	// A stop that names an already-finished utterance has nothing left to do.
}

func (s *sttSession) handleCancel(control modulestt.Control) {
	if s.addressesActive(control.UtteranceID) {
		id := s.active.id
		s.active = nil
		s.queue.push(&sttJob{kind: sttJobCancelled, id: id})
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
			s.queue.push(&sttJob{kind: sttJobError, id: id, errCode: sttErrorUnsupported, errMessage: err.Error()})
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
	if s.active != nil && now.Sub(s.active.lastAt) >= s.cfg.Limits.IdleTimeout {
		s.finalize(modulestt.EndReasonIdleTimeout)
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
		s.queue.push(job)
		return
	}
	if !s.queue.admit(s.cfg.QueueDepth) {
		job.kind = sttJobError
		job.errCode = sttErrorBusy
		job.errMessage = "too many utterances are waiting for recognition"
		s.queue.push(job)
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

func (s *sttSession) worker() {
	defer s.wg.Done()
	for {
		job, ok := s.queue.pop(s.ctx)
		if !ok || s.ctx.Err() != nil {
			return // the session ended: queued utterances are dropped, not sent
		}
		s.runJob(job)
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

// reportCancellation answers a cancel control. When the session itself ended
// there is nobody left to tell.
func (s *sttSession) reportCancellation(job *sttJob) {
	if job.userCancelled.Load() {
		s.send(modulestt.BuildCancelledEvent(), job.id)
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
	mu      sync.Mutex
	items   []*sttJob
	pending int
	wake    chan struct{}
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

func (q *sttJobQueue) push(job *sttJob) {
	q.mu.Lock()
	q.items = append(q.items, job)
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
