package classifierlab

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestBuildClassifierOffReturnsPrimaryWithoutCreatingShadowDependencies(t *testing.T) {
	primary := &primaryStub{decision: routing.NewDecision(routing.RouteCHAT, 0.6, "primary")}
	var candidateFactories atomic.Int32
	var recorderFactories atomic.Int32
	classifier, shadow, err := BuildClassifier(
		context.Background(),
		primary,
		DefaultConfig(),
		func(Config) (CandidateClassifier, error) {
			candidateFactories.Add(1)
			return candidateStub(nil), nil
		},
		func(RecordingConfig) (ComparisonRecorder, error) {
			recorderFactories.Add(1)
			return &recordCollector{}, nil
		},
	)
	if err != nil {
		t.Fatalf("BuildClassifier() error = %v", err)
	}
	if classifier != primary || shadow != nil {
		t.Fatalf("off mode classifier=%T shadow=%v, want exact primary and nil shadow", classifier, shadow)
	}
	if candidateFactories.Load() != 0 || recorderFactories.Load() != 0 {
		t.Fatalf("off mode factories called: candidate=%d recorder=%d", candidateFactories.Load(), recorderFactories.Load())
	}
	got, err := classifier.Classify(context.Background(), testTurnInput(t, "off"))
	if err != nil || !reflect.DeepEqual(got, primary.decision) {
		t.Fatalf("off mode primary result=%+v error=%v", got, err)
	}
}

func TestShadowClassifierReturnsPrimaryUnchangedAndQueuesBoundedSnapshot(t *testing.T) {
	primaryErr := errors.New("primary unavailable")
	decision := routing.NewDecisionWithEvidence(routing.RouteANALYZE, 0.25, "primary reason", routing.DecisionEvidence{
		Source: routing.EvidenceSourceClassifier, Matched: true, Route: routing.RouteANALYZE,
		Confidence: 0.25, Reason: "evidence before caller mutation",
	})
	primary := &primaryStub{decision: decision, err: primaryErr}
	start := make(chan CandidateInput, 1)
	release := make(chan struct{})
	records := &recordCollector{records: make(chan ComparisonRecord, 4)}
	serviceCtx := context.WithValue(context.Background(), contextKey("service-only"), "secret")
	config := validShadowConfig()
	config.Shadow.SampleRate = 1
	config.Shadow.QueueCapacity = 1
	config.Shadow.MaxJobAgeMS = 5000
	config.Candidate.TimeoutMS = 5000
	config.Shadow.DrainTimeoutMS = 1000

	classifier, shadow, err := BuildClassifier(serviceCtx, primary, config,
		func(Config) (CandidateClassifier, error) {
			return candidateStub(func(ctx context.Context, input CandidateInput) (CandidateResult, error) {
				if ctx.Value(contextKey("request-only")) != nil || ctx.Value(contextKey("service-only")) != nil {
					return CandidateResult{}, errors.New("request/service context value crossed the shadow boundary")
				}
				start <- input
				select {
				case <-release:
				case <-ctx.Done():
					return CandidateResult{}, ctx.Err()
				}
				return CandidateResult{Status: CandidateAbstain, ReasonCode: "LOW_CONFIDENCE"}, nil
			}), nil
		},
		func(RecordingConfig) (ComparisonRecorder, error) { return records, nil },
	)
	if err != nil {
		t.Fatalf("BuildClassifier() error = %v", err)
	}
	if classifier != shadow {
		t.Fatalf("classifier=%T, want shadow wrapper", classifier)
	}
	var releaseOnce sync.Once
	defer func() {
		releaseOnce.Do(func() { close(release) })
		_ = shadow.Close(context.Background())
	}()

	requestCtx := context.WithValue(context.Background(), contextKey("request-only"), "request secret")
	input := testTurnInput(t, "snapshot message")
	got, err := shadow.Classify(requestCtx, input)
	if err != primaryErr || !reflect.DeepEqual(got, decision) {
		t.Fatalf("shadow changed primary result: decision=%+v error=%v", got, err)
	}
	select {
	case candidateInput := <-start:
		if candidateInput.Message != "snapshot message" || candidateInput.RequestID != string(input.TurnID()) || candidateInput.PolicySHA256 != config.Candidate.ExpectedPolicySHA256 {
			t.Fatalf("candidate snapshot = %+v", candidateInput)
		}
	case <-time.After(time.Second):
		t.Fatal("candidate worker did not start")
	}
	second := testTurnInput(t, "queued")
	if _, err := shadow.Classify(context.Background(), second); err != primaryErr {
		t.Fatalf("second primary error = %v, want original error", err)
	}
	third := testTurnInput(t, "dropped")
	started := time.Now()
	if _, err := shadow.Classify(context.Background(), third); err != primaryErr {
		t.Fatalf("third primary error = %v, want original error", err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("full queue delayed primary by %s", elapsed)
	}
	if got := shadow.Stats(); got.Enqueued != 2 || got.QueueFullDropped != 1 || got.ClassifierReached != 3 || got.Sampled != 3 {
		t.Fatalf("queue counters = %+v, want 2 enqueued and 1 full drop", got)
	}

	decision.Evidence[0].Reason = "mutated after Classify returned"
	releaseOnce.Do(func() { close(release) })
	select {
	case record := <-records.records:
		if record.PrimaryDecision.Evidence[0].Reason != "evidence before caller mutation" {
			t.Fatalf("record shares mutable primary evidence: %+v", record.PrimaryDecision.Evidence)
		}
		if record.CorrelationID != string(input.TraceID()) {
			t.Fatalf("correlation ID = %q, want trace ID %q", record.CorrelationID, input.TraceID())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("comparison record was not emitted")
	}
}

func TestShadowClassifierDropsCanceledAndOversizedRequests(t *testing.T) {
	primary := &primaryStub{decision: routing.NewDecision(routing.RouteCHAT, 0.4, "primary")}
	var candidateCalls atomic.Int32
	config := validShadowConfig()
	config.Shadow.SampleRate = 1
	config.Candidate.TimeoutMS = 200
	config.Shadow.DrainTimeoutMS = 500
	_, shadow, err := BuildClassifier(context.Background(), primary, config,
		func(Config) (CandidateClassifier, error) {
			return candidateStub(func(context.Context, CandidateInput) (CandidateResult, error) {
				candidateCalls.Add(1)
				return CandidateResult{Status: CandidateAbstain, ReasonCode: "LOW_CONFIDENCE"}, nil
			}), nil
		},
		func(RecordingConfig) (ComparisonRecorder, error) { return &recordCollector{}, nil },
	)
	if err != nil {
		t.Fatalf("BuildClassifier() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = shadow.Classify(ctx, testTurnInput(t, "canceled"))
	_, _ = shadow.Classify(context.Background(), testTurnInput(t, strings.Repeat("x", MaxHTTPBodyBytes+1)))
	if got := shadow.Stats(); got.CanceledDropped != 1 || got.OversizedInputDropped != 1 || got.Enqueued != 0 || got.ClassifierReached != 2 {
		t.Fatalf("drop counters = %+v", got)
	}
	if err := shadow.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if candidateCalls.Load() != 0 {
		t.Fatalf("candidate calls = %d, want 0", candidateCalls.Load())
	}
}

func TestShadowClassifierDoesNotEnqueueAfterServiceCancellationAndPreservesPrimary(t *testing.T) {
	primaryErr := errors.New("primary service result")
	decision := routing.NewDecision(routing.RouteANALYZE, 0.75, "primary")
	serviceCtx, cancelService := context.WithCancel(context.Background())
	primary := &primaryStub{
		decision:   decision,
		err:        primaryErr,
		onClassify: cancelService,
	}
	var candidateCalls atomic.Int32
	config := validShadowConfig()
	config.Shadow.SampleRate = 1
	_, shadow, err := BuildClassifier(serviceCtx, primary, config,
		func(Config) (CandidateClassifier, error) {
			return candidateStub(func(context.Context, CandidateInput) (CandidateResult, error) {
				candidateCalls.Add(1)
				return CandidateResult{Status: CandidateAbstain, ReasonCode: "LOW_CONFIDENCE"}, nil
			}), nil
		},
		func(RecordingConfig) (ComparisonRecorder, error) { return &recordCollector{}, nil },
	)
	if err != nil {
		t.Fatalf("BuildClassifier() error = %v", err)
	}
	got, gotErr := shadow.Classify(context.Background(), testTurnInput(t, "service cancellation"))
	if gotErr != primaryErr || !reflect.DeepEqual(got, decision) {
		t.Fatalf("service cancellation changed primary result: decision=%+v error=%v", got, gotErr)
	}
	closeCtx, cancelClose := context.WithTimeout(context.Background(), time.Second)
	defer cancelClose()
	if err := shadow.Close(closeCtx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if stats := shadow.Stats(); stats.Enqueued != 0 || stats.CanceledDropped != 1 {
		t.Fatalf("service-cancel stats = %+v, want no enqueue and one canceled drop", stats)
	}
	if candidateCalls.Load() != 0 || primary.calls.Load() != 1 {
		t.Fatalf("candidate calls=%d primary calls=%d, want 0 and 1", candidateCalls.Load(), primary.calls.Load())
	}
}

func TestShadowClassifierContainsCandidatePanicAndTimeout(t *testing.T) {
	tests := []struct {
		name       string
		candidate  candidateFunc
		wantCode   string
		wantPanics uint64
	}{
		{
			name: "panic",
			candidate: func(context.Context, CandidateInput) (CandidateResult, error) {
				panic("private candidate detail")
			},
			wantCode:   "CANDIDATE_PANIC",
			wantPanics: 1,
		},
		{
			name: "timeout",
			candidate: func(ctx context.Context, _ CandidateInput) (CandidateResult, error) {
				<-ctx.Done()
				return CandidateResult{}, ctx.Err()
			},
			wantCode: "TIMEOUT",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			primaryErr := errors.New("primary error identity")
			decision := routing.NewDecision(routing.RoutePLAN, 0.7, "primary")
			primary := &primaryStub{decision: decision, err: primaryErr}
			records := &recordCollector{records: make(chan ComparisonRecord, 1)}
			config := validShadowConfig()
			config.Shadow.SampleRate = 1
			config.Candidate.TimeoutMS = 20
			config.Shadow.DrainTimeoutMS = 500
			classifier, shadow, err := BuildClassifier(context.Background(), primary, config,
				func(Config) (CandidateClassifier, error) { return candidateStub(test.candidate), nil },
				func(RecordingConfig) (ComparisonRecorder, error) { return records, nil },
			)
			if err != nil {
				t.Fatalf("BuildClassifier() error = %v", err)
			}
			got, gotErr := classifier.Classify(context.Background(), testTurnInput(t, "message"))
			if gotErr != primaryErr || !reflect.DeepEqual(got, decision) {
				t.Fatalf("candidate %s changed primary result: decision=%+v error=%v", test.name, got, gotErr)
			}
			select {
			case record := <-records.records:
				if record.CandidateError != test.wantCode {
					t.Fatalf("candidate error code = %q, want %q", record.CandidateError, test.wantCode)
				}
			case <-time.After(time.Second):
				t.Fatal("candidate failure was not recorded")
			}
			stats := shadow.Stats()
			if stats.CandidateErrors != 1 || stats.CandidatePanics != test.wantPanics {
				t.Fatalf("candidate failure stats = %+v", stats)
			}
			closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := shadow.Close(closeCtx); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		})
	}
}

func TestShadowClassifierCloseTimeoutCancelsAndCountsPendingJobs(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	primary := &primaryStub{decision: routing.NewDecision(routing.RouteCHAT, 0.4, "primary")}
	config := validShadowConfig()
	config.Shadow.SampleRate = 1
	config.Shadow.QueueCapacity = 1
	config.Shadow.DrainTimeoutMS = 1000
	config.Candidate.TimeoutMS = 5000
	classifier, shadow, err := BuildClassifier(context.Background(), primary, config,
		func(Config) (CandidateClassifier, error) {
			return candidateStub(func(context.Context, CandidateInput) (CandidateResult, error) {
				started <- struct{}{}
				<-release // Deliberately ignores cancellation to exercise bounded Close.
				return CandidateResult{Status: CandidateAbstain, ReasonCode: "LOW_CONFIDENCE"}, nil
			}), nil
		},
		func(RecordingConfig) (ComparisonRecorder, error) { return &recordCollector{}, nil },
	)
	if err != nil {
		t.Fatalf("BuildClassifier() error = %v", err)
	}
	classifier.Classify(context.Background(), testTurnInput(t, "running"))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("candidate worker did not start")
	}
	classifier.Classify(context.Background(), testTurnInput(t, "pending"))
	closeCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := shadow.Close(closeCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close() error = %v, want caller deadline", err)
	}
	partial := shadow.Stats()
	if partial.PendingAtShutdown != 2 || partial.OutstandingRemaining != 2 || partial.TerminationComplete {
		t.Fatalf("incomplete shutdown stats = %+v, want pending/remaining=2 and termination_complete=false", partial)
	}
	releaseOnce.Do(func() { close(release) })
	ctx, finish := context.WithTimeout(context.Background(), time.Second)
	defer finish()
	if err := shadow.Close(ctx); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	stats := shadow.Stats()
	if stats.ShutdownDropped != 1 || stats.PendingAtShutdown != 2 || stats.CancelledAtShutdown != 1 ||
		stats.OutstandingRemaining != 0 || !stats.TerminationComplete ||
		stats.Enqueued != stats.Completed+stats.StaleDropped+stats.ShutdownDropped+stats.OutstandingRemaining {
		t.Fatalf("shutdown stats = %+v, want pending=2 and cancelled=1", stats)
	}
}

func TestShadowStatsReportClassifierOutcomeCounters(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	config := validShadowConfig()
	config.Shadow.SampleRate = 1
	config.Shadow.QueueCapacity = 1
	config.Shadow.MaxJobAgeMS = 10
	config.Shadow.DrainTimeoutMS = 1000
	var candidateCalls atomic.Int32
	recorder := &rejectingRecorder{}
	_, shadow, err := BuildClassifier(context.Background(), &primaryStub{decision: routing.NewDecision(routing.RouteCHAT, 0.8, "primary")}, config,
		func(Config) (CandidateClassifier, error) {
			return candidateStub(func(context.Context, CandidateInput) (CandidateResult, error) {
				if candidateCalls.Add(1) == 1 {
					started <- struct{}{}
					<-release
				}
				return CandidateResult{Status: CandidateAbstain, ReasonCode: "LOW_CONFIDENCE"}, nil
			}), nil
		},
		func(RecordingConfig) (ComparisonRecorder, error) { return recorder, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	tooLarge := testTurnInput(t, strings.Repeat("x", MaxHTTPBodyBytes+1))
	if _, err := shadow.Classify(context.Background(), tooLarge); err != nil {
		t.Fatal(err)
	}
	if _, err := shadow.Classify(context.Background(), testTurnInput(t, "running")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first candidate did not start")
	}
	if _, err := shadow.Classify(context.Background(), testTurnInput(t, "will become stale")); err != nil {
		t.Fatal(err)
	}
	if _, err := shadow.Classify(context.Background(), testTurnInput(t, "queue full")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	if err := shadow.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	stats := shadow.Stats()
	if stats.ClassifierReached != 4 || stats.Sampled != 3 || stats.Enqueued != 2 ||
		stats.QueueFullDropped != 1 || stats.OversizedInputDropped != 1 || stats.StaleDropped != 1 ||
		stats.Completed != 1 || stats.CandidateErrors != 0 || stats.RecorderDropped != 1 {
		t.Fatalf("classifier outcome stats = %+v", stats)
	}
}

func TestShadowStatsAccountingRemainsConsistentAfterCandidateCompletes(t *testing.T) {
	recorder := &blockingRecorder{entered: make(chan struct{}, 1), release: make(chan struct{})}
	var releaseOnce sync.Once
	config := validShadowConfig()
	config.Shadow.SampleRate = 1
	config.Shadow.DrainTimeoutMS = 1000
	_, shadow, err := BuildClassifier(context.Background(), &primaryStub{decision: routing.NewDecision(routing.RouteCHAT, 0.8, "primary")}, config,
		func(Config) (CandidateClassifier, error) {
			return candidateStub(func(context.Context, CandidateInput) (CandidateResult, error) {
				return CandidateResult{Status: CandidateAbstain, ReasonCode: "LOW_CONFIDENCE"}, nil
			}), nil
		},
		func(RecordingConfig) (ComparisonRecorder, error) { return recorder, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		releaseOnce.Do(func() { close(recorder.release) })
		_ = shadow.Close(context.Background())
	}()
	if _, err := shadow.Classify(context.Background(), testTurnInput(t, "accounting")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-recorder.entered:
	case <-time.After(time.Second):
		t.Fatal("candidate did not complete and reach the recorder")
	}

	stats := shadow.Stats()
	if stats.Enqueued != stats.Completed+stats.StaleDropped+stats.ShutdownDropped+stats.OutstandingRemaining {
		t.Fatalf("accepted-work accounting does not reconcile during recording: %+v", stats)
	}
	if stats.Completed != 1 || stats.OutstandingRemaining != 0 {
		t.Fatalf("candidate completion accounting = %+v, want completed=1 and no candidate work outstanding", stats)
	}
	releaseOnce.Do(func() { close(recorder.release) })
	if err := shadow.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestShadowStatsCountActiveAndQueuedWorkCancelledAtShutdown(t *testing.T) {
	started := make(chan struct{}, 1)
	config := validShadowConfig()
	config.Shadow.SampleRate = 1
	config.Shadow.QueueCapacity = 2
	config.Shadow.DrainTimeoutMS = 1000
	config.Candidate.TimeoutMS = 5000
	_, shadow, err := BuildClassifier(context.Background(), &primaryStub{decision: routing.NewDecision(routing.RouteCHAT, 0.8, "primary")}, config,
		func(Config) (CandidateClassifier, error) {
			return candidateStub(func(ctx context.Context, _ CandidateInput) (CandidateResult, error) {
				started <- struct{}{}
				<-ctx.Done()
				return CandidateResult{}, ctx.Err()
			}), nil
		},
		func(RecordingConfig) (ComparisonRecorder, error) { return &recordCollector{}, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	shadow.Classify(context.Background(), testTurnInput(t, "active"))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("candidate did not start")
	}
	shadow.Classify(context.Background(), testTurnInput(t, "queued"))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := shadow.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close() error=%v, want deadline", err)
	}
	select {
	case <-shadow.done:
	case <-time.After(time.Second):
		t.Fatal("cancelled workers did not finish")
	}
	stats := shadow.Stats()
	if stats.PendingAtShutdown != 2 || stats.CancelledAtShutdown != 2 || stats.Completed != 1 || stats.CandidateErrors != 1 ||
		stats.OutstandingRemaining != 0 || !stats.TerminationComplete {
		t.Fatalf("cancelled shutdown stats = %+v", stats)
	}
}

func TestShadowClassifyAfterCloseSeparatesRejectionFromAcceptedDrops(t *testing.T) {
	primary := &primaryStub{decision: routing.NewDecision(routing.RouteCHAT, 0.8, "primary")}
	config := validShadowConfig()
	config.Shadow.SampleRate = 1
	classifier, shadow, err := BuildClassifier(context.Background(), primary, config,
		func(Config) (CandidateClassifier, error) {
			return candidateStub(func(context.Context, CandidateInput) (CandidateResult, error) {
				return CandidateResult{Status: CandidateAbstain, ReasonCode: "LOW_CONFIDENCE"}, nil
			}), nil
		},
		func(RecordingConfig) (ComparisonRecorder, error) { return &recordCollector{}, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	input := testTurnInput(t, "same primary behavior")
	decision, err := classifier.Classify(context.Background(), input)
	if err != nil || !reflect.DeepEqual(decision, primary.decision) {
		t.Fatalf("first primary result = %+v, %v", decision, err)
	}
	if err := shadow.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	decision, err = classifier.Classify(context.Background(), input)
	if err != nil || !reflect.DeepEqual(decision, primary.decision) {
		t.Fatalf("post-close primary result = %+v, %v", decision, err)
	}
	stats := shadow.Stats()
	if stats.ClassifierReached != 2 || stats.Sampled != 1 || stats.Enqueued != 1 ||
		stats.Completed != 1 || stats.ShutdownDropped != 0 || stats.RejectedAfterShutdown != 1 ||
		stats.OutstandingRemaining != 0 || !stats.TerminationComplete {
		t.Fatalf("post-close stats = %+v", stats)
	}
	if stats.Enqueued != stats.Completed+stats.StaleDropped+stats.ShutdownDropped+stats.OutstandingRemaining {
		t.Fatalf("accepted-work accounting does not reconcile: %+v", stats)
	}
}

func TestShadowSamplingIsDeterministicAndBounded(t *testing.T) {
	if selectedForShadow(12, "turn-id", 0) {
		t.Fatal("sample rate 0 selected a request")
	}
	if !selectedForShadow(12, "turn-id", 1) {
		t.Fatal("sample rate 1 dropped a request")
	}
	first := selectedForShadow(12, "turn-id", 0.37)
	for range 100 {
		if got := selectedForShadow(12, "turn-id", 0.37); got != first {
			t.Fatal("same seed and request ID produced inconsistent sampling")
		}
	}
}

func TestAsyncRecorderNeverBlocksProducerAndCopiesRecord(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	seen := make(chan ComparisonRecord, 2)
	recorder, err := NewAsyncRecorder(context.Background(), 1, func(_ context.Context, record ComparisonRecord) error {
		started <- struct{}{}
		<-release
		seen <- record
		return nil
	})
	if err != nil {
		t.Fatalf("NewAsyncRecorder() error = %v", err)
	}
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
		_ = recorder.Close(context.Background())
	}()

	first := ComparisonRecord{PrimaryDecision: PrimaryDecisionSnapshot{Evidence: []routing.DecisionEvidence{{Reason: "before"}}}}
	if !recorder.TryRecord(first) {
		t.Fatal("first record was not enqueued")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("recorder sink did not start")
	}
	if !recorder.TryRecord(ComparisonRecord{RequestID: "queued"}) {
		t.Fatal("second record was not enqueued in the bounded queue")
	}
	startedAt := time.Now()
	if recorder.TryRecord(ComparisonRecord{RequestID: "dropped"}) {
		t.Fatal("full recorder queue accepted an extra record")
	}
	if time.Since(startedAt) > 100*time.Millisecond {
		t.Fatal("TryRecord waited for blocked sink")
	}
	if recorder.Dropped() != 1 {
		t.Fatalf("dropped records = %d, want 1", recorder.Dropped())
	}
	first.PrimaryDecision.Evidence[0].Reason = "after"
	close(release)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := recorder.Close(ctx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("sink saw %d records, want 2", len(seen))
	}
	if record := <-seen; record.PrimaryDecision.Evidence[0].Reason != "before" {
		t.Fatalf("record shares mutable evidence: %+v", record.PrimaryDecision.Evidence)
	}
}

type primaryStub struct {
	decision   routing.Decision
	err        error
	calls      atomic.Int32
	onClassify func()
}

func (p *primaryStub) Classify(context.Context, conversation.TurnInput) (routing.Decision, error) {
	p.calls.Add(1)
	if p.onClassify != nil {
		p.onClassify()
	}
	return p.decision, p.err
}

type candidateFunc func(context.Context, CandidateInput) (CandidateResult, error)

func (f candidateFunc) ClassifyCandidate(ctx context.Context, input CandidateInput) (CandidateResult, error) {
	return f(ctx, input)
}

func candidateStub(fn candidateFunc) CandidateClassifier { return fn }

type recordCollector struct{ records chan ComparisonRecord }

func (r *recordCollector) TryRecord(record ComparisonRecord) bool {
	if r.records == nil {
		return true
	}
	select {
	case r.records <- record:
		return true
	default:
		return false
	}
}

func (r *recordCollector) Dropped() uint64 { return 0 }

type blockingRecorder struct {
	entered chan struct{}
	release chan struct{}
}

func (r *blockingRecorder) TryRecord(ComparisonRecord) bool {
	r.entered <- struct{}{}
	<-r.release
	return true
}

func (*blockingRecorder) Dropped() uint64 { return 0 }

type rejectingRecorder struct{ dropped atomic.Uint64 }

func (r *rejectingRecorder) TryRecord(ComparisonRecord) bool {
	r.dropped.Add(1)
	return false
}

func (r *rejectingRecorder) Dropped() uint64 { return r.dropped.Load() }

type contextKey string

func testTurnInput(t *testing.T, message string) conversation.TurnInput {
	t.Helper()
	address, err := conversation.NewChannelAddress("test", "conversation")
	if err != nil {
		t.Fatalf("NewChannelAddress() error = %v", err)
	}
	input, err := conversation.NewTurnInput(modulecore.NewTaskID(), message, address)
	if err != nil {
		t.Fatalf("NewTurnInput() error = %v", err)
	}
	return input
}

var _ agent.Classifier = (*primaryStub)(nil)
