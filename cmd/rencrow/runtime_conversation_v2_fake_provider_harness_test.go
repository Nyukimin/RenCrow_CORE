package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/llm/middleware"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	sessionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/session"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	responseFirstFixedHarnessWarmupCount          = 5
	responseFirstFixedHarnessMeasurementCount     = 20
	responseFirstFixedHarnessProviderSpanCapacity = responseFirstFixedHarnessWarmupCount + responseFirstFixedHarnessMeasurementCount
	responseFirstFixedHarnessHubEventCapacity     = 1024
	responseFirstFixedHarnessDelay                = 15 * time.Millisecond
	responseFirstFixedHarnessInput                = "fixed fake-provider benchmark input"
	responseFirstFixedHarnessOutput               = "fixed fake-provider benchmark response"
	responseFirstFixedHarnessTimeout              = 10 * time.Second
)

const responseFirstFixedHarnessOutputJSON = `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"user","text":"fixed fake-provider benchmark response"}}`

type responseFirstFixedHarnessSpan struct {
	started time.Time
	ended   time.Time
}

type responseFirstHarnessProvider struct {
	delay time.Duration

	mu            sync.Mutex
	spans         []responseFirstFixedHarnessSpan
	generateCalls int
	droppedSpans  int
}

func (p *responseFirstHarnessProvider) Generate(ctx context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.mu.Lock()
	p.generateCalls++
	p.mu.Unlock()
	started := time.Now()
	timer := time.NewTimer(p.delay)
	defer timer.Stop()

	var generateErr error
	select {
	case <-ctx.Done():
		generateErr = ctx.Err()
	case <-timer.C:
	}
	ended := time.Now()
	p.recordSpan(responseFirstFixedHarnessSpan{started: started, ended: ended})
	if generateErr != nil {
		return llm.GenerateResponse{}, generateErr
	}
	return llm.GenerateResponse{
		Content:   responseFirstFixedHarnessOutputJSON,
		RequestID: modulecore.NewRequestID(), ResponseID: modulecore.NewResponseID(), TokensUsed: 8,
	}, nil
}

func (p *responseFirstHarnessProvider) Name() string { return "response-first-fixed-delay-fake" }

func (p *responseFirstHarnessProvider) recordSpan(span responseFirstFixedHarnessSpan) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.spans) == cap(p.spans) {
		p.droppedSpans++
		return
	}
	p.spans = append(p.spans, span)
}

func (p *responseFirstHarnessProvider) spanSummaryBetween(start, end time.Time) (responseFirstFixedHarnessSpan, int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var match responseFirstFixedHarnessSpan
	matchCount := 0
	for _, span := range p.spans {
		if span.started.Before(start) || span.ended.After(end) {
			continue
		}
		match = span
		matchCount++
	}
	return match, matchCount, p.generateCalls
}

func (p *responseFirstHarnessProvider) snapshot() ([]responseFirstFixedHarnessSpan, int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	spans := make([]responseFirstFixedHarnessSpan, len(p.spans))
	copy(spans, p.spans)
	return spans, p.generateCalls, p.droppedSpans
}

type responseFirstFixedHarnessReceivedEvent struct {
	event      orchestrator.OrchestratorEvent
	receivedAt time.Time
}

type responseFirstFixedHarnessHubObserver struct {
	mu      sync.RWMutex
	events  []responseFirstFixedHarnessReceivedEvent
	dropped int

	notify chan struct{}
	done   chan struct{}
	err    error
}

func newResponseFirstFixedHarnessHubObserver(hub *viewer.EventHub) (*responseFirstFixedHarnessHubObserver, func()) {
	stream, _ := hub.Subscribe()
	observer := &responseFirstFixedHarnessHubObserver{
		events: make([]responseFirstFixedHarnessReceivedEvent, 0, responseFirstFixedHarnessHubEventCapacity),
		notify: make(chan struct{}, 1),
		done:   make(chan struct{}),
	}
	go func() {
		defer close(observer.done)
		for raw := range stream {
			receivedAt := time.Now()
			var event orchestrator.OrchestratorEvent
			// Decode at the Hub protocol boundary because matching the response to its turn and receipt requires typed correlation fields.
			if err := json.Unmarshal(raw, &event); err != nil {
				observer.mu.Lock()
				if observer.err == nil {
					observer.err = err
				}
				observer.mu.Unlock()
				select {
				case observer.notify <- struct{}{}:
				default:
				}
				continue
			}
			observer.recordEvent(responseFirstFixedHarnessReceivedEvent{event: event, receivedAt: receivedAt})
			select {
			case observer.notify <- struct{}{}:
			default:
			}
		}
	}()
	return observer, func() {
		hub.Unsubscribe(stream)
		<-observer.done
	}
}

// recordEvent stores protocol-decoded Hub events in a fixed-capacity buffer; overflow is counted without overwriting prior correlation evidence.
func (o *responseFirstFixedHarnessHubObserver) recordEvent(observed responseFirstFixedHarnessReceivedEvent) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.events) == cap(o.events) {
		o.dropped++
		return
	}
	o.events = append(o.events, observed)
}

func (o *responseFirstFixedHarnessHubObserver) waitForResponse(t *testing.T, turnID modulecore.TurnID) (responseFirstFixedHarnessReceivedEvent, bool) {
	t.Helper()
	timer := time.NewTimer(responseFirstFixedHarnessTimeout)
	defer timer.Stop()
	for {
		o.mu.RLock()
		for _, observed := range o.events {
			if observed.event.Type == "agent.response" && observed.event.TurnID == turnID {
				o.mu.RUnlock()
				return observed, true
			}
		}
		parseErr := o.err
		dropped := o.dropped
		o.mu.RUnlock()
		if parseErr != nil {
			t.Fatalf("decode EventHub event: %v", parseErr)
		}
		if dropped > 0 {
			return responseFirstFixedHarnessReceivedEvent{}, false
		}
		select {
		case <-o.notify:
		case <-timer.C:
			t.Fatalf("timed out waiting for EventHub response for turn %s", turnID)
		}
	}
}

func (o *responseFirstFixedHarnessHubObserver) snapshot() ([]responseFirstFixedHarnessReceivedEvent, error, int) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	events := make([]responseFirstFixedHarnessReceivedEvent, len(o.events))
	copy(events, o.events)
	return events, o.err, o.dropped
}

type responseFirstFixedHarnessReceipt struct {
	SessionID      string `json:"session_id"`
	InputMessageID string `json:"input_message_id"`
	TurnID         string `json:"turn_id"`
	TraceID        string `json:"trace_id"`
}

type responseFirstFixedHarnessSample struct {
	Ordinal                   int    `json:"ordinal"`
	Phase                     string `json:"phase"`
	SessionID                 string `json:"session_id"`
	TurnID                    string `json:"turn_id"`
	TraceID                   string `json:"trace_id"`
	InputMessageID            string `json:"input_message_id"`
	ResponseEventID           string `json:"response_event_id"`
	HandlerAcceptDurationNS   int64  `json:"handler_accept_duration_ns"`
	ResponseHubLatencyNS      int64  `json:"response_hub_latency_ns"`
	FakeGenerateSpanUnionNS   int64  `json:"fake_generate_span_union_ns"`
	ForegroundFrameworkWallNS int64  `json:"foreground_framework_wall_ns"`
}

type responseFirstFixedHarnessReport struct {
	SchemaVersion                     string                            `json:"schema_version"`
	TestKind                          string                            `json:"test_kind"`
	Provider                          string                            `json:"provider"`
	ModelSpeedImprovementClaim        bool                              `json:"model_speed_improvement_claim"`
	OwnerID                           string                            `json:"owner_id"`
	ActorID                           string                            `json:"actor_id"`
	AssistantID                       string                            `json:"assistant_id"`
	InputMessage                      string                            `json:"input_message"`
	FixedOutput                       string                            `json:"fixed_output"`
	FixedGenerateDelayNS              int64                             `json:"fixed_generate_delay_ns"`
	WarmupCount                       int                               `json:"warmup_count"`
	MeasurementCount                  int                               `json:"measurement_count"`
	ObservedHubEventCount             int                               `json:"observed_hub_event_count"`
	HubEventBufferCapacity            int                               `json:"hub_event_buffer_capacity"`
	DroppedHubEventCount              int                               `json:"dropped_hub_event_count"`
	ObservedMetricsLatencyEventCount  int                               `json:"observed_metrics_latency_event_count"`
	ProviderGenerateCallCount         int                               `json:"provider_generate_call_count"`
	RecordedProviderSpanCount         int                               `json:"recorded_provider_span_count"`
	ProviderSpanBufferCapacity        int                               `json:"provider_span_buffer_capacity"`
	DroppedProviderSpanCount          int                               `json:"dropped_provider_span_count"`
	CompletedSampleCount              int                               `json:"completed_sample_count"`
	MeasurementComplete               bool                              `json:"measurement_complete"`
	ForegroundBoundaryValid           bool                              `json:"foreground_boundary_valid"`
	BackgroundDrainComplete           bool                              `json:"background_drain_complete"`
	ComparisonValid                   bool                              `json:"comparison_valid"`
	PerformanceAdoptionValid          bool                              `json:"performance_adoption_valid"`
	ComparisonInvalidReasons          []string                          `json:"comparison_invalid_reasons"`
	PublicReplayEligibleResponseCount int                               `json:"public_replay_eligible_response_count"`
	L1ProjectionExpectedMessageCount  int                               `json:"l1_projection_expected_message_count"`
	L1ProjectionObservedMessageCount  int                               `json:"l1_projection_observed_message_count"`
	L1ProjectionResidualMessageCount  int                               `json:"l1_projection_residual_message_count"`
	L1ProjectionComplete              bool                              `json:"l1_projection_complete"`
	L1ProjectionResidualReason        string                            `json:"l1_projection_residual_reason"`
	L1ProjectionWorkerStarted         bool                              `json:"l1_projection_worker_started"`
	L1ProjectionWorkerStopError       string                            `json:"l1_projection_worker_stop_error"`
	Samples                           []responseFirstFixedHarnessSample `json:"samples"`
	Summary                           responseFirstFixedHarnessSummary  `json:"measurement_summary"`
}

func TestResponseFirstFixedHarnessBuffersCountOverflowWithoutOverwriting(t *testing.T) {
	provider := &responseFirstHarnessProvider{spans: make([]responseFirstFixedHarnessSpan, 0, 1)}
	firstSpan := responseFirstFixedHarnessSpan{started: time.Unix(1, 0), ended: time.Unix(2, 0)}
	provider.recordSpan(firstSpan)
	provider.recordSpan(responseFirstFixedHarnessSpan{started: time.Unix(3, 0), ended: time.Unix(4, 0)})
	spans, _, droppedSpans := provider.snapshot()
	if len(spans) != 1 || spans[0] != firstSpan || droppedSpans != 1 || cap(provider.spans) != 1 {
		t.Fatalf("provider fixed buffer = spans:%+v cap:%d dropped:%d, want first span retained with one drop", spans, cap(provider.spans), droppedSpans)
	}

	observer := &responseFirstFixedHarnessHubObserver{events: make([]responseFirstFixedHarnessReceivedEvent, 0, 1)}
	firstEvent := responseFirstFixedHarnessReceivedEvent{event: orchestrator.OrchestratorEvent{Type: "agent.response", EventID: modulecore.NewEventID()}}
	observer.recordEvent(firstEvent)
	observer.recordEvent(responseFirstFixedHarnessReceivedEvent{event: orchestrator.OrchestratorEvent{Type: "agent.response", EventID: modulecore.NewEventID()}})
	events, _, droppedEvents := observer.snapshot()
	if len(events) != 1 || events[0].event.EventID != firstEvent.event.EventID || droppedEvents != 1 || cap(observer.events) != 1 {
		t.Fatalf("Hub fixed buffer = events:%+v cap:%d dropped:%d, want first event retained with one drop", events, cap(observer.events), droppedEvents)
	}
}

type responseFirstFixedHarnessSummary struct {
	ResponseLatencyP50NS     int64 `json:"response_hub_latency_p50_ns"`
	ResponseLatencyP95NS     int64 `json:"response_hub_latency_p95_ns"`
	GenerateSpanUnionTotalNS int64 `json:"fake_generate_span_union_total_ns"`
	FrameworkWallTotalNS     int64 `json:"foreground_framework_wall_total_ns"`
}

func TestResponseFirstV2FixedFakeProviderFrameworkHarness(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := eventstore.NewSQLiteStore(filepath.Join(root, "event-store.sqlite"))
	if err != nil {
		t.Fatalf("open real SQLite EventStore: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close real SQLite EventStore: %v", err)
		}
	})
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(root, "chat-l1.sqlite"))
	if err != nil {
		t.Fatalf("open real SQLite Chat L1: %v", err)
	}
	t.Cleanup(func() {
		if err := l1.Close(); err != nil {
			t.Errorf("close real SQLite Chat L1: %v", err)
		}
	})

	archive, err := viewer.NewCanonicalEventLog(store)
	if err != nil {
		t.Fatalf("build canonical EventStore relay view: %v", err)
	}
	hub := viewer.NewEventHub(responseFirstFixedHarnessHubEventCapacity)
	hub.SetReplayReader(archive)
	hub.SetSessionPublicReplayReader(archive)
	monitor := viewer.NewMonitorStore(nil, archive)
	relay := &idleAwareEventListener{hub: hub, monitor: monitor, archive: archive}
	observer, closeObserver := newResponseFirstFixedHarnessHubObserver(hub)
	t.Cleanup(closeObserver)

	provider := &responseFirstHarnessProvider{
		delay: responseFirstFixedHarnessDelay,
		spans: make([]responseFirstFixedHarnessSpan, 0, responseFirstFixedHarnessProviderSpanCapacity),
	}
	conversationProvider := middleware.NewConversationProvider(
		provider,
		provider.Name(),
		domainai.ContextBudgetPolicy{},
	).WithExecutionSlot(conversationLLMExecutionSlot{tracker: newLLMBusyTracker()})
	runtime, err := newResponseFirstConversationRuntimeWithProjection(
		store,
		agent.NewMioAgent(nil, nil, nil, nil, nil, nil),
		conversationProvider,
		relay,
		responseFirstTestOwnerID,
		l1,
	)
	if err != nil {
		t.Fatalf("build production response-first runtime with real L1 projection owner: %v", err)
	}
	stopProjectionWorker := runtime.startResponseFirstDrainWithStatus()
	if stopProjectionWorker == nil {
		t.Fatal("production response-first background worker was not available for the selected real L1 projection owner")
	}
	projectionWorkerStarted := true
	projectionWorkerStopped := false
	t.Cleanup(func() {
		if projectionWorkerStopped {
			return
		}
		stopCtx, cancel := context.WithTimeout(context.Background(), responseFirstFixedHarnessTimeout)
		defer cancel()
		if err := stopProjectionWorker(stopCtx); err != nil {
			t.Errorf("stop production response-first background worker: %v", err)
		}
	})
	repo := sessionpersistence.NewJSONSessionRepository(t.TempDir())
	cfg := &config.Config{}
	cfg.ConversationRuntime.Version = "v2"
	factories := buildViewerBridgeHandlers(cfg, &Dependencies{
		responseFirst:       runtime,
		eventHub:            hub,
		eventRelay:          relay,
		canonicalEventStore: store,
	}, "", ttsEntryRuntime{}, repo)
	handler := factories.ViewerSendFromOrch(&viewerBridgeCountProcessor{})

	const totalTurns = responseFirstFixedHarnessWarmupCount + responseFirstFixedHarnessMeasurementCount
	samples := make([]responseFirstFixedHarnessSample, 0, totalTurns)
	receipts := make([]responseFirstFixedHarnessReceipt, 0, totalTurns)
	responseEvents := make([]orchestrator.OrchestratorEvent, 0, totalTurns)
	foregroundBoundaryInvalidReasons := make([]string, 0, 8)
	for index := 0; index < totalTurns; index++ {
		submissionID := fmt.Sprintf("response-first-fixed-harness-%02d", index+1)
		body, err := json.Marshal(map[string]any{
			"protocol_version": 2, "message": responseFirstFixedHarnessInput,
			"client_submission_id": submissionID, "to": "mio", "user_id": "untrusted-body-user",
		})
		if err != nil {
			t.Fatalf("encode Viewer v2 request %d: %v", index+1, err)
		}
		request := httptest.NewRequest(http.MethodPost, "/viewer/send", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()

		handlerStarted := time.Now()
		handler.ServeHTTP(response, request)
		handlerReturned := time.Now()
		if response.Code != http.StatusAccepted {
			t.Fatalf("Viewer v2 send %d status=%d body=%s, want durable 202 acceptance", index+1, response.Code, response.Body.String())
		}
		var receipt responseFirstFixedHarnessReceipt
		if err := json.Unmarshal(response.Body.Bytes(), &receipt); err != nil {
			t.Fatalf("decode Viewer v2 acceptance receipt %d: %v", index+1, err)
		}
		if receipt.SessionID == "" || receipt.InputMessageID == "" || receipt.TurnID == "" || receipt.TraceID == "" {
			t.Fatalf("Viewer v2 receipt %d has incomplete correlation: %+v", index+1, receipt)
		}
		receipts = append(receipts, receipt)

		turnID := modulecore.TurnID(receipt.TurnID)
		observed, found := observer.waitForResponse(t, turnID)
		if !found {
			foregroundBoundaryInvalidReasons = append(foregroundBoundaryInvalidReasons, fmt.Sprintf("Hub observer buffer overflow prevented response correlation for turn %s", turnID))
			break
		}
		if observed.receivedAt.Before(handlerStarted) || observed.receivedAt.Before(handlerReturned) {
			foregroundBoundaryInvalidReasons = append(foregroundBoundaryInvalidReasons, fmt.Sprintf("response for turn %s arrived outside handler-to-Hub interval", turnID))
		}
		if event := observed.event; event.Type != "agent.response" || event.From != "mio" || event.To != "user" ||
			event.Content != responseFirstFixedHarnessOutput || event.SessionID != modulecore.SessionID(receipt.SessionID) ||
			event.TurnID != turnID || event.TraceID != modulecore.TraceID(receipt.TraceID) || event.MessageID.Validate() != nil {
			t.Fatalf("Hub response correlation/content for turn %s = %+v; want public Mio response and matching receipt", turnID, event)
		}
		responseEvents = append(responseEvents, observed.event)

		span, matchingSpanCount, generateCalls := provider.spanSummaryBetween(handlerStarted, observed.receivedAt)
		if matchingSpanCount != 1 || generateCalls != index+1 {
			foregroundBoundaryInvalidReasons = append(foregroundBoundaryInvalidReasons, fmt.Sprintf("turn %s has %d matching fake Generate spans and cumulative call count %d, want one and %d", turnID, matchingSpanCount, generateCalls, index+1))
		}
		latency := observed.receivedAt.Sub(handlerStarted)
		var spanUnion time.Duration
		if matchingSpanCount > 0 {
			if span.started.Before(handlerStarted) || span.ended.After(observed.receivedAt) || !span.ended.After(span.started) {
				foregroundBoundaryInvalidReasons = append(foregroundBoundaryInvalidReasons, fmt.Sprintf("fake Generate span for turn %s is outside measured interval", turnID))
			}
			spanUnion = span.ended.Sub(span.started)
		}
		frameworkWall := latency - spanUnion
		if frameworkWall < 0 {
			foregroundBoundaryInvalidReasons = append(foregroundBoundaryInvalidReasons, fmt.Sprintf("foreground framework wall for turn %s is negative", turnID))
			frameworkWall = 0
		}
		phase := "measurement"
		if index < responseFirstFixedHarnessWarmupCount {
			phase = "warmup"
		}
		samples = append(samples, responseFirstFixedHarnessSample{
			Ordinal: index + 1, Phase: phase, SessionID: receipt.SessionID, TurnID: receipt.TurnID,
			TraceID: receipt.TraceID, InputMessageID: receipt.InputMessageID, ResponseEventID: string(observed.event.EventID),
			HandlerAcceptDurationNS: handlerReturned.Sub(handlerStarted).Nanoseconds(),
			ResponseHubLatencyNS:    latency.Nanoseconds(), FakeGenerateSpanUnionNS: spanUnion.Nanoseconds(),
			ForegroundFrameworkWallNS: frameworkWall.Nanoseconds(),
		})
	}

	providerSpans, generateCallCount, droppedProviderSpans := provider.snapshot()
	if generateCallCount != totalTurns {
		foregroundBoundaryInvalidReasons = append(foregroundBoundaryInvalidReasons, fmt.Sprintf("fake Provider Generate call count=%d, want %d", generateCallCount, totalTurns))
	}
	if len(receipts) != totalTurns {
		foregroundBoundaryInvalidReasons = append(foregroundBoundaryInvalidReasons, fmt.Sprintf("harness completed receipts=%d, want %d", len(receipts), totalTurns))
	}
	if len(responseEvents) != totalTurns {
		foregroundBoundaryInvalidReasons = append(foregroundBoundaryInvalidReasons, fmt.Sprintf("harness observed responses=%d, want %d", len(responseEvents), totalTurns))
	}
	if droppedProviderSpans > 0 {
		foregroundBoundaryInvalidReasons = append(foregroundBoundaryInvalidReasons, fmt.Sprintf("fake Provider span buffer dropped %d spans", droppedProviderSpans))
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), responseFirstFixedHarnessTimeout)
	projectionWorkerStopErr := stopProjectionWorker(stopCtx)
	stopCancel()
	projectionWorkerStopped = true
	for index, receipt := range receipts {
		turnID := modulecore.TurnID(receipt.TurnID)
		var observed *orchestrator.OrchestratorEvent
		if index < len(responseEvents) {
			observed = &responseEvents[index]
		}
		outcome, found, err := store.LoadCommittedOutcome(ctx, turnID)
		if err != nil || !found {
			t.Fatalf("read SQLite committed outcome for turn %s found=%t err=%v", turnID, found, err)
		}
		if outcome.SessionID != modulecore.SessionID(receipt.SessionID) || outcome.TurnID != turnID {
			t.Fatalf("SQLite commit identity = session %s turn %s, want receipt session %s turn %s", outcome.SessionID, outcome.TurnID, receipt.SessionID, turnID)
		}
		var storedResponses []modulecore.EventEnvelope
		for _, committed := range outcome.Events {
			if committed.EventType == "agent.response" && committed.Payload["visibility"] == "public" && committed.Payload["from"] == "mio" && committed.Payload["to"] == "user" {
				storedResponses = append(storedResponses, committed)
			}
		}
		if len(storedResponses) != 1 {
			t.Fatalf("SQLite committed public direct-response count for turn %s = %d, want one; events=%+v", turnID, len(storedResponses), outcome.Events)
		}
		stored := storedResponses[0]
		if stored.Payload["text"] != responseFirstFixedHarnessOutput ||
			stored.TraceID != modulecore.TraceID(receipt.TraceID) || stored.SessionID != modulecore.SessionID(receipt.SessionID) ||
			stored.TurnID != turnID {
			t.Fatalf("SQLite stored response does not match receipt for turn %s: stored=%+v receipt=%+v", turnID, stored, receipt)
		}
		if observed != nil {
			if stored.EventID != observed.EventID || stored.MessageID != observed.MessageID {
				t.Fatalf("SQLite stored response does not match Hub event for turn %s: stored=%+v hub=%+v", turnID, stored, *observed)
			}
		} else {
			foregroundBoundaryInvalidReasons = append(foregroundBoundaryInvalidReasons, fmt.Sprintf("stored response for turn %s could not be matched to a received Hub event", turnID))
		}
	}
	projected, err := l1.RecentBySession(ctx, receipts[len(receipts)-1].SessionID, totalTurns*2+1)
	if err != nil {
		t.Fatalf("inspect real SQLite L1 projected messages after foreground timing: %v", err)
	}
	projectionWorkerStopError := ""
	if projectionWorkerStopErr != nil {
		projectionWorkerStopError = projectionWorkerStopErr.Error()
	}
	userMessages, assistantMessages := 0, 0
	for _, item := range projected {
		if item.Source != "conversation_projection" {
			t.Fatalf("SQLite L1 row source=%q, want canonical conversation_projection", item.Source)
		}
		switch item.Message {
		case responseFirstFixedHarnessInput:
			userMessages++
		case responseFirstFixedHarnessOutput:
			assistantMessages++
		}
	}
	if userMessages+assistantMessages != len(projected) {
		t.Fatalf("SQLite L1 projection contains unexpected message values: user=%d assistant=%d rows=%d", userMessages, assistantMessages, len(projected))
	}
	projectionResidualReason := "none"
	if len(projected) < totalTurns*2 {
		projectionResidualReason = "L1 projection incomplete after worker stop; cause unresolved (deferred error observed externally)"
	} else if len(projected) > totalTurns*2 {
		projectionResidualReason = fmt.Sprintf("L1 projection row count after worker stop was %d; expected %d; cause unresolved", len(projected), totalTurns*2)
	} else if projectionWorkerStopErr != nil {
		projectionResidualReason = "worker stop returned an error despite the expected L1 row count; drain completion is unconfirmed"
	}

	tail, err := store.ReadCompleteSessionTail(ctx, modulecore.SessionID(receipts[0].SessionID), 0)
	if err != nil || !tail.Complete || len(tail.Messages) != totalTurns*2 {
		t.Fatalf("SQLite canonical session tail complete=%t messages=%d err=%v, want all %d persisted user/reply messages", tail.Complete, len(tail.Messages), err, totalTurns*2)
	}
	for index, receipt := range receipts {
		inputFound := false
		for _, message := range tail.Messages {
			if message.Index.TurnID == modulecore.TurnID(receipt.TurnID) && message.Index.MessageID == modulecore.MessageID(receipt.InputMessageID) &&
				message.Index.FromActor == responseFirstActorID && message.Index.ToActor == "mio" && message.Event.Payload["text"] == responseFirstFixedHarnessInput {
				inputFound = true
				break
			}
		}
		if !inputFound {
			t.Fatalf("SQLite session tail is missing the viewer-user input for turn %d (%s)", index+1, receipt.TurnID)
		}
	}

	// Owner-scoped public eligibility is checked after all foreground timing has ended.
	publicReplayEligibleResponseCount := 0
	for _, observed := range responseEvents {
		public, found, err := archive.ReadSessionPublicMessage(ctx, viewer.SessionPublicReplayScope{
			SessionID: observed.SessionID,
			OwnerID:   responseFirstTestOwnerID,
			ActorID:   responseFirstActorID,
		}, observed.EventID, observed.EventSeq)
		if err != nil || !found {
			foregroundBoundaryInvalidReasons = append(foregroundBoundaryInvalidReasons, fmt.Sprintf("public replay eligibility lookup failed for response event %s (found=%t, err=%v)", observed.EventID, found, err))
			continue
		}
		if public.EventID != observed.EventID || public.EventSeq != observed.EventSeq || public.SessionID != observed.SessionID ||
			public.TurnID != observed.TurnID || public.TraceID != observed.TraceID || public.MessageID != observed.MessageID ||
			public.From != observed.From || public.To != observed.To || public.Content != observed.Content || public.Visibility != "public" {
			foregroundBoundaryInvalidReasons = append(foregroundBoundaryInvalidReasons, fmt.Sprintf("public replay event %s did not match the received Hub response", observed.EventID))
			continue
		}
		publicReplayEligibleResponseCount++
	}
	if publicReplayEligibleResponseCount != totalTurns {
		foregroundBoundaryInvalidReasons = append(foregroundBoundaryInvalidReasons, fmt.Sprintf("public replay eligibility verified %d responses, want %d", publicReplayEligibleResponseCount, totalTurns))
	}

	observedHubEvents, observerErr, droppedHubEvents := observer.snapshot()
	if observerErr != nil {
		t.Fatalf("EventHub observer decode error: %v", observerErr)
	}
	if droppedHubEvents > 0 {
		foregroundBoundaryInvalidReasons = append(foregroundBoundaryInvalidReasons, fmt.Sprintf("Hub observer fixed buffer dropped %d events", droppedHubEvents))
	}
	metricsLatencyEvents := 0
	for _, observed := range observedHubEvents {
		if observed.event.Type == "metrics.latency" {
			metricsLatencyEvents++
		}
	}

	warmupBoundary := min(len(samples), responseFirstFixedHarnessWarmupCount)
	measurementSamples := samples[warmupBoundary:]
	latencies := make([]int64, 0, len(measurementSamples))
	var frameworkWallTotal int64
	for _, sample := range measurementSamples {
		latencies = append(latencies, sample.ResponseHubLatencyNS)
		frameworkWallTotal += sample.ForegroundFrameworkWallNS
	}
	spanWarmupBoundary := min(len(providerSpans), responseFirstFixedHarnessWarmupCount)
	generateUnionTotal := responseFirstFixedHarnessSpanUnion(providerSpans[spanWarmupBoundary:]).Nanoseconds()
	measurementComplete := len(samples) == totalTurns && len(responseEvents) == totalTurns && generateCallCount == totalTurns && len(providerSpans) == totalTurns
	if !measurementComplete {
		foregroundBoundaryInvalidReasons = append(foregroundBoundaryInvalidReasons, "foreground measurement did not complete all warmup and measurement turns")
	}
	foregroundBoundaryValid := measurementComplete && len(foregroundBoundaryInvalidReasons) == 0 && droppedHubEvents == 0 && droppedProviderSpans == 0
	backgroundDrainComplete := len(projected) == totalTurns*2 && projectionWorkerStopErr == nil
	comparisonInvalidReasons := append([]string(nil), foregroundBoundaryInvalidReasons...)
	if !backgroundDrainComplete {
		comparisonInvalidReasons = append(comparisonInvalidReasons, projectionResidualReason)
		if projectionWorkerStopError != "" {
			comparisonInvalidReasons = append(comparisonInvalidReasons, "L1 projection worker stop returned an error: "+projectionWorkerStopError)
		}
	}
	comparisonValid := foregroundBoundaryValid && backgroundDrainComplete
	report := responseFirstFixedHarnessReport{
		SchemaVersion: "rencrow.response-first.fake-provider-benchmark.v1", TestKind: "framework_difference_fake_provider",
		Provider: provider.Name(), ModelSpeedImprovementClaim: false,
		OwnerID: responseFirstTestOwnerID, ActorID: responseFirstActorID, AssistantID: "mio",
		InputMessage: responseFirstFixedHarnessInput, FixedOutput: responseFirstFixedHarnessOutput,
		FixedGenerateDelayNS: responseFirstFixedHarnessDelay.Nanoseconds(),
		WarmupCount:          responseFirstFixedHarnessWarmupCount, MeasurementCount: responseFirstFixedHarnessMeasurementCount,
		ObservedHubEventCount: len(observedHubEvents), HubEventBufferCapacity: cap(observer.events), DroppedHubEventCount: droppedHubEvents,
		ObservedMetricsLatencyEventCount: metricsLatencyEvents,
		ProviderGenerateCallCount:        generateCallCount, RecordedProviderSpanCount: len(providerSpans),
		ProviderSpanBufferCapacity: cap(provider.spans), DroppedProviderSpanCount: droppedProviderSpans,
		CompletedSampleCount: len(samples), MeasurementComplete: measurementComplete,
		ForegroundBoundaryValid: foregroundBoundaryValid, BackgroundDrainComplete: backgroundDrainComplete,
		ComparisonValid: comparisonValid, PerformanceAdoptionValid: false,
		ComparisonInvalidReasons:          comparisonInvalidReasons,
		PublicReplayEligibleResponseCount: publicReplayEligibleResponseCount,
		L1ProjectionExpectedMessageCount:  totalTurns * 2, L1ProjectionObservedMessageCount: len(projected),
		L1ProjectionResidualMessageCount: max(totalTurns*2-len(projected), 0),
		L1ProjectionComplete:             backgroundDrainComplete,
		L1ProjectionResidualReason:       projectionResidualReason,
		L1ProjectionWorkerStarted:        projectionWorkerStarted,
		L1ProjectionWorkerStopError:      projectionWorkerStopError,
		Samples:                          samples,
		Summary: responseFirstFixedHarnessSummary{
			ResponseLatencyP50NS:     responseFirstFixedHarnessPercentile(latencies, 0.50),
			ResponseLatencyP95NS:     responseFirstFixedHarnessPercentile(latencies, 0.95),
			GenerateSpanUnionTotalNS: generateUnionTotal, FrameworkWallTotalNS: frameworkWallTotal,
		},
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("encode machine-readable fake Provider result: %v", err)
	}
	t.Logf("RENCROW_RESPONSE_FIRST_FAKE_BENCHMARK_JSON=%s", encoded)
	if !report.ForegroundBoundaryValid {
		t.Errorf("fake Provider foreground measurement boundary is invalid: %v", report.ComparisonInvalidReasons)
	} else if !report.PerformanceAdoptionValid {
		t.Logf("performance comparison/adoption withheld: %v", report.ComparisonInvalidReasons)
	}
}

func responseFirstFixedHarnessSpanUnion(spans []responseFirstFixedHarnessSpan) time.Duration {
	if len(spans) == 0 {
		return 0
	}
	ordered := append([]responseFirstFixedHarnessSpan(nil), spans...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].started.Before(ordered[j].started) })
	start, end := ordered[0].started, ordered[0].ended
	var total time.Duration
	for _, span := range ordered[1:] {
		if span.started.After(end) {
			total += end.Sub(start)
			start, end = span.started, span.ended
			continue
		}
		if span.ended.After(end) {
			end = span.ended
		}
	}
	return total + end.Sub(start)
}

func responseFirstFixedHarnessPercentile(values []int64, percentile float64) int64 {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]int64(nil), values...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	index := int(math.Ceil(percentile*float64(len(ordered)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(ordered) {
		index = len(ordered) - 1
	}
	return ordered[index]
}
