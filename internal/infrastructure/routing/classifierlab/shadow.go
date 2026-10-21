package classifierlab

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
)

var (
	ErrShadowDrainTimeout = errors.New("classifierlab shadow drain timed out")
	ErrNilServiceContext  = errors.New("classifierlab service context is nil")
)

type RecorderFactory func(RecordingConfig) (ComparisonRecorder, error)

// ShadowStats is a body-free aggregate for one shadow-classifier process.
// Completed counts candidate calls that returned, including errors; candidate
// errors are a subset. PendingAtShutdown snapshots accepted queued and active
// work when Close first stops submissions. CancelledAtShutdown counts accepted
// work abandoned or returned with an error after worker cancellation. The
// wrapper cannot observe all turns, so TurnsTotal stays nil and is explicitly
// marked unmeasured. At completed termination, enqueued equals completed plus
// stale and shutdown drops; while termination is incomplete, OutstandingRemaining
// records accepted work that has not yet reached one of those terminal counts.
type ShadowStats struct {
	ClassifierReached      uint64  `json:"classifier_reached"`
	Sampled                uint64  `json:"sampled"`
	Enqueued               uint64  `json:"enqueued"`
	SampledOut             uint64  `json:"sampled_out"`
	QueueFullDropped       uint64  `json:"dropped_queue_full"`
	OversizedInputDropped  uint64  `json:"dropped_too_large"`
	InvalidSnapshotDropped uint64  `json:"dropped_invalid_snapshot"`
	CanceledDropped        uint64  `json:"dropped_request_cancelled"`
	StaleDropped           uint64  `json:"dropped_stale"`
	ShutdownDropped        uint64  `json:"dropped_shutdown"`
	PendingAtShutdown      uint64  `json:"pending_at_shutdown"`
	CancelledAtShutdown    uint64  `json:"cancelled_at_shutdown"`
	OutstandingRemaining   uint64  `json:"outstanding_remaining"`
	RejectedAfterShutdown  uint64  `json:"rejected_after_shutdown"`
	CandidateCalls         uint64  `json:"candidate_calls"`
	Completed              uint64  `json:"completed"`
	CandidateErrors        uint64  `json:"candidate_errors"`
	CandidatePanics        uint64  `json:"candidate_panics"`
	RecorderDropped        uint64  `json:"recording_dropped"`
	TurnsTotal             *uint64 `json:"turns_total"`
	TurnsTotalStatus       string  `json:"turns_total_status"`
	TerminationComplete    bool    `json:"termination_complete"`
}

type shadowJob struct {
	input      CandidateInput
	record     ComparisonRecord
	enqueuedAt time.Time
}

type shadowCounters struct {
	classifierReached      atomic.Uint64
	sampled                atomic.Uint64
	enqueued               atomic.Uint64
	sampledOut             atomic.Uint64
	queueFullDropped       atomic.Uint64
	oversizedInputDropped  atomic.Uint64
	invalidSnapshotDropped atomic.Uint64
	canceledDropped        atomic.Uint64
	staleDropped           atomic.Uint64
	shutdownDropped        atomic.Uint64
	pendingAtShutdown      atomic.Uint64
	cancelledAtShutdown    atomic.Uint64
	rejectedAfterShutdown  atomic.Uint64
	outstanding            atomic.Int64
	candidateCalls         atomic.Uint64
	completed              atomic.Uint64
	candidateErrors        atomic.Uint64
	candidatePanics        atomic.Uint64
	recorderDropped        atomic.Uint64
}

type ShadowClassifier struct {
	primary          agent.Classifier
	candidate        CandidateClassifier
	recorder         ComparisonRecorder
	policySHA256     string
	sampleRate       float64
	sampleSeed       int64
	maxJobAge        time.Duration
	candidateTimeout time.Duration
	drainTimeout     time.Duration
	includeMessage   bool
	jobs             chan shadowJob
	workerCtx        context.Context
	cancelWorkers    context.CancelFunc
	stopService      func() bool
	serviceDone      <-chan struct{}
	closed           atomic.Bool
	closeOnce        sync.Once
	queueMu          sync.RWMutex
	workers          sync.WaitGroup
	done             chan struct{}
	finishErr        error
	counters         shadowCounters
	accountingMu     sync.Mutex
}

// BuildClassifier constructs the shadow path only for explicit shadow mode.
// Off mode returns the original classifier without calling either factory.
func BuildClassifier(
	serviceCtx context.Context,
	primary agent.Classifier,
	cfg Config,
	candidateFactory CandidateFactory,
	recorderFactory RecorderFactory,
) (agent.Classifier, *ShadowClassifier, error) {
	if err := cfg.Validate(); err != nil {
		return nil, nil, err
	}
	if primary == nil {
		return nil, nil, nil
	}
	if cfg.Mode == ModeOff {
		return primary, nil, nil
	}
	if serviceCtx == nil {
		return nil, nil, ErrNilServiceContext
	}
	if candidateFactory == nil {
		return nil, nil, errors.New("shadow mode requires a candidate factory")
	}
	if recorderFactory == nil {
		return nil, nil, errors.New("shadow mode requires a recorder factory")
	}

	candidate, err := candidateFactory(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("create shadow candidate: %w", err)
	}
	if isNilInterface(candidate) {
		return nil, nil, errors.New("candidate factory returned nil")
	}
	recorder, err := recorderFactory(cfg.Recording)
	if err != nil {
		return nil, nil, fmt.Errorf("create shadow recorder: %w", err)
	}
	if isNilInterface(recorder) {
		return nil, nil, errors.New("recorder factory returned nil")
	}

	workerCtx, cancelWorkers := context.WithCancel(context.Background())
	stopService := context.AfterFunc(serviceCtx, cancelWorkers)
	shadow := &ShadowClassifier{
		primary:          primary,
		candidate:        candidate,
		recorder:         recorder,
		policySHA256:     cfg.Candidate.ExpectedPolicySHA256,
		sampleRate:       cfg.Shadow.SampleRate,
		sampleSeed:       cfg.Shadow.SampleSeed,
		maxJobAge:        time.Duration(cfg.Shadow.MaxJobAgeMS) * time.Millisecond,
		candidateTimeout: time.Duration(cfg.Candidate.TimeoutMS) * time.Millisecond,
		drainTimeout:     time.Duration(cfg.Shadow.DrainTimeoutMS) * time.Millisecond,
		includeMessage:   cfg.Recording.IncludeMessageText,
		jobs:             make(chan shadowJob, cfg.Shadow.QueueCapacity),
		workerCtx:        workerCtx,
		cancelWorkers:    cancelWorkers,
		stopService:      stopService,
		serviceDone:      serviceCtx.Done(),
		done:             make(chan struct{}),
	}
	for range cfg.Shadow.Workers {
		shadow.workers.Add(1)
		go shadow.worker()
	}
	go shadow.finishWhenWorkersExit()
	return shadow, shadow, nil
}

// Classify always returns exactly the decision and error returned by primary.
func (s *ShadowClassifier) Classify(ctx context.Context, input conversation.TurnInput) (routing.Decision, error) {
	if s == nil || s.primary == nil {
		return routing.Decision{}, errors.New("classifierlab shadow classifier is not initialized")
	}
	s.counters.classifierReached.Add(1)
	candidateInput, snapshotErr := snapshotInput(input, s.policySHA256)
	primaryStarted := time.Now()
	decision, primaryErr := s.primary.Classify(ctx, input)
	primaryDuration := time.Since(primaryStarted)
	if snapshotErr != nil {
		if errors.Is(snapshotErr, ErrCandidateBodyTooLarge) {
			s.counters.oversizedInputDropped.Add(1)
		} else {
			s.counters.invalidSnapshotDropped.Add(1)
		}
		return decision, primaryErr
	}

	if ctx == nil || ctx.Err() != nil {
		s.counters.canceledDropped.Add(1)
		return decision, primaryErr
	}
	if channelClosed(s.serviceDone) {
		s.counters.canceledDropped.Add(1)
		return decision, primaryErr
	}
	if s.closed.Load() {
		s.counters.rejectedAfterShutdown.Add(1)
		return decision, primaryErr
	}
	if s.workerCtx.Err() != nil {
		s.counters.rejectedAfterShutdown.Add(1)
		return decision, primaryErr
	}
	if !selectedForShadow(s.sampleSeed, candidateInput.RequestID, s.sampleRate) {
		s.counters.sampledOut.Add(1)
		return decision, primaryErr
	}
	s.counters.sampled.Add(1)

	decisionSnapshot := cloneDecision(decision)
	record := ComparisonRecord{
		RequestID:            candidateInput.RequestID,
		CorrelationID:        string(input.TraceID()),
		PrimaryDecision:      decisionSnapshot,
		PrimaryDurationNanos: primaryDuration.Nanoseconds(),
	}
	if s.includeMessage {
		record.Message = candidateInput.Message
	}
	if primaryErr != nil {
		record.PrimaryError = "present"
	}

	job := shadowJob{input: candidateInput, record: record, enqueuedAt: time.Now()}
	s.queueMu.RLock()
	defer s.queueMu.RUnlock()
	if s.closed.Load() {
		s.counters.rejectedAfterShutdown.Add(1)
		return decision, primaryErr
	}
	if channelClosed(s.serviceDone) {
		s.counters.canceledDropped.Add(1)
		return decision, primaryErr
	}
	if s.workerCtx.Err() != nil {
		s.counters.rejectedAfterShutdown.Add(1)
		return decision, primaryErr
	}
	s.accountingMu.Lock()
	s.counters.outstanding.Add(1)
	select {
	case s.jobs <- job:
		s.counters.enqueued.Add(1)
	default:
		s.counters.outstanding.Add(-1)
		s.counters.queueFullDropped.Add(1)
	}
	s.accountingMu.Unlock()
	return decision, primaryErr
}

func channelClosed(done <-chan struct{}) bool {
	if done == nil {
		return false
	}
	select {
	case <-done:
		return true
	default:
		return false
	}
}

func (s *ShadowClassifier) Stats() ShadowStats {
	if s == nil {
		return ShadowStats{}
	}
	s.accountingMu.Lock()
	stats := ShadowStats{
		ClassifierReached:      s.counters.classifierReached.Load(),
		Sampled:                s.counters.sampled.Load(),
		Enqueued:               s.counters.enqueued.Load(),
		SampledOut:             s.counters.sampledOut.Load(),
		QueueFullDropped:       s.counters.queueFullDropped.Load(),
		OversizedInputDropped:  s.counters.oversizedInputDropped.Load(),
		InvalidSnapshotDropped: s.counters.invalidSnapshotDropped.Load(),
		CanceledDropped:        s.counters.canceledDropped.Load(),
		StaleDropped:           s.counters.staleDropped.Load(),
		ShutdownDropped:        s.counters.shutdownDropped.Load(),
		PendingAtShutdown:      s.counters.pendingAtShutdown.Load(),
		CancelledAtShutdown:    s.counters.cancelledAtShutdown.Load(),
		OutstandingRemaining:   nonnegativeCount(s.counters.outstanding.Load()),
		RejectedAfterShutdown:  s.counters.rejectedAfterShutdown.Load(),
		CandidateCalls:         s.counters.candidateCalls.Load(),
		Completed:              s.counters.completed.Load(),
		CandidateErrors:        s.counters.candidateErrors.Load(),
		CandidatePanics:        s.counters.candidatePanics.Load(),
		RecorderDropped:        s.counters.recorderDropped.Load(),
		TurnsTotal:             nil,
		TurnsTotalStatus:       "unmeasured",
		TerminationComplete:    channelClosed(s.done),
	}
	s.accountingMu.Unlock()
	if s.recorder != nil {
		stats.RecorderDropped += s.recorder.Dropped()
		if doneRecorder, ok := s.recorder.(interface{ Done() <-chan struct{} }); ok {
			stats.TerminationComplete = stats.TerminationComplete && channelClosed(doneRecorder.Done())
		}
	}
	return stats
}

func nonnegativeCount(value int64) uint64 {
	if value < 0 {
		return 0
	}
	return uint64(value)
}

// Close stops new shadow jobs, drains queued work for at most the configured
// duration, and then asks a closeable recorder to flush its own bounded queue.
func (s *ShadowClassifier) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.closeOnce.Do(func() {
		s.queueMu.Lock()
		s.closed.Store(true)
		s.accountingMu.Lock()
		if outstanding := s.counters.outstanding.Load(); outstanding > 0 {
			s.counters.pendingAtShutdown.Store(uint64(outstanding))
		}
		s.accountingMu.Unlock()
		close(s.jobs)
		s.queueMu.Unlock()
	})
	timer := time.NewTimer(s.drainTimeout)
	defer timer.Stop()
	select {
	case <-s.done:
		return s.finishErr
	case <-ctx.Done():
		s.cancelWorkers()
		return ctx.Err()
	case <-timer.C:
		s.cancelWorkers()
		return ErrShadowDrainTimeout
	}
}

func (s *ShadowClassifier) worker() {
	defer s.workers.Done()
	for {
		select {
		case <-s.workerCtx.Done():
			s.dropPendingOnShutdown()
			return
		case job, ok := <-s.jobs:
			if !ok {
				return
			}
			if s.workerCtx.Err() != nil {
				s.countShutdownCancellation()
				continue
			}
			s.process(job)
		}
	}
}

func (s *ShadowClassifier) process(job shadowJob) {
	if time.Since(job.enqueuedAt) > s.maxJobAge {
		s.accountingMu.Lock()
		s.counters.staleDropped.Add(1)
		s.counters.outstanding.Add(-1)
		s.accountingMu.Unlock()
		return
	}
	s.counters.candidateCalls.Add(1)
	started := time.Now()
	ctx, cancel := context.WithTimeout(s.workerCtx, s.candidateTimeout)
	result, callErr, panicked := s.callCandidate(ctx, job.input)
	cancel()
	s.accountingMu.Lock()
	s.counters.completed.Add(1)
	s.counters.outstanding.Add(-1)
	if callErr != nil && s.workerCtx.Err() != nil {
		s.counters.cancelledAtShutdown.Add(1)
	}
	s.accountingMu.Unlock()
	job.record.CandidateDurationNS = time.Since(started).Nanoseconds()
	if panicked {
		s.counters.candidatePanics.Add(1)
	}
	if callErr != nil {
		s.counters.candidateErrors.Add(1)
		job.record.CandidateError = candidateErrorCode(callErr, panicked)
	} else {
		cloned := cloneCandidateResult(result)
		job.record.CandidateResult = &cloned
	}
	s.tryRecord(job.record)
}

func (s *ShadowClassifier) countShutdownCancellation() {
	s.accountingMu.Lock()
	s.counters.shutdownDropped.Add(1)
	s.counters.cancelledAtShutdown.Add(1)
	s.counters.outstanding.Add(-1)
	s.accountingMu.Unlock()
}

func (s *ShadowClassifier) callCandidate(ctx context.Context, input CandidateInput) (result CandidateResult, err error, panicked bool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result = CandidateResult{}
			err = errors.New("candidate panic")
			panicked = true
		}
	}()
	result, err = s.candidate.ClassifyCandidate(ctx, input)
	return result, err, false
}

func (s *ShadowClassifier) tryRecord(record ComparisonRecord) {
	accepted := false
	panicked := false
	func() {
		defer func() {
			if recover() != nil {
				panicked = true
				accepted = false
			}
		}()
		accepted = s.recorder.TryRecord(cloneComparisonRecord(record))
	}()
	if !accepted && panicked {
		s.counters.recorderDropped.Add(1)
	}
}

func (s *ShadowClassifier) dropPendingOnShutdown() {
	for {
		select {
		case _, ok := <-s.jobs:
			if !ok {
				return
			}
			s.countShutdownCancellation()
		default:
			return
		}
	}
}

func (s *ShadowClassifier) finishWhenWorkersExit() {
	s.workers.Wait()
	if closer, ok := s.recorder.(interface{ Close(context.Context) error }); ok {
		ctx, cancel := context.WithTimeout(context.Background(), s.drainTimeout)
		s.finishErr = closer.Close(ctx)
		cancel()
	}
	s.cancelWorkers()
	if s.stopService != nil {
		s.stopService()
	}
	close(s.done)
}

func snapshotInput(input conversation.TurnInput, policySHA256 string) (CandidateInput, error) {
	message := input.MessageText()
	requestID := string(input.TurnID())
	if strings.TrimSpace(message) == "" || !utf8.ValidString(message) || strings.TrimSpace(requestID) == "" ||
		strings.TrimSpace(string(input.TraceID())) == "" {
		return CandidateInput{}, errors.New("candidate snapshot has invalid message or identity")
	}
	candidateInput := CandidateInput{
		SchemaVersion: SchemaVersion,
		RequestID:     requestID,
		PolicySHA256:  policySHA256,
		Message:       message,
	}
	body, err := json.Marshal(candidateInput)
	if err != nil {
		return CandidateInput{}, fmt.Errorf("encode candidate snapshot: %w", err)
	}
	if len(body) > MaxHTTPBodyBytes {
		return CandidateInput{}, ErrCandidateBodyTooLarge
	}
	return candidateInput, nil
}

func selectedForShadow(seed int64, requestID string, sampleRate float64) bool {
	if sampleRate <= 0 || math.IsNaN(sampleRate) {
		return false
	}
	if sampleRate >= 1 {
		return true
	}
	var seedBytes [8]byte
	binary.LittleEndian.PutUint64(seedBytes[:], uint64(seed))
	hash := sha256.New()
	_, _ = hash.Write([]byte("rencrow-routing-classifier-test/v1\x00"))
	_, _ = hash.Write(seedBytes[:])
	_, _ = hash.Write([]byte(requestID))
	digest := hash.Sum(nil)
	value := binary.BigEndian.Uint64(digest[:8]) >> 11
	const denominator = float64(uint64(1) << 53)
	return float64(value)/denominator < sampleRate
}

func cloneDecision(decision routing.Decision) PrimaryDecisionSnapshot {
	return PrimaryDecisionSnapshot{
		Route:      decision.Route,
		Confidence: decision.Confidence,
		Reason:     decision.Reason,
		Evidence:   append([]routing.DecisionEvidence(nil), decision.Evidence...),
	}
}

func cloneComparisonRecord(record ComparisonRecord) ComparisonRecord {
	record.PrimaryDecision.Evidence = append([]routing.DecisionEvidence(nil), record.PrimaryDecision.Evidence...)
	if record.CandidateResult != nil {
		cloned := cloneCandidateResult(*record.CandidateResult)
		record.CandidateResult = &cloned
	}
	return record
}

func candidateErrorCode(err error, panicked bool) string {
	if panicked {
		return "CANDIDATE_PANIC"
	}
	if errors.Is(err, ErrCandidateBodyTooLarge) {
		return "BODY_TOO_LARGE"
	}
	if errors.Is(err, ErrInvalidCandidateOutput) {
		return "INVALID_MODEL_OUTPUT"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "TIMEOUT"
	}
	if errors.Is(err, context.Canceled) {
		return "CANCELED"
	}
	var httpErr *CandidateHTTPError
	if errors.As(err, &httpErr) {
		return fmt.Sprintf("HTTP_STATUS_%d", httpErr.StatusCode)
	}
	return "CANDIDATE_ERROR"
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	ref := reflect.ValueOf(value)
	switch ref.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return ref.IsNil()
	default:
		return false
	}
}

var _ agent.Classifier = (*ShadowClassifier)(nil)
