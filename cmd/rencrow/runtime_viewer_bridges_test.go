package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/speech"
	sessionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/session"
	"github.com/Nyukimin/RenCrow_CORE/internal/turntiming"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

type viewerBridgeErrorProcessor struct{}

func (viewerBridgeErrorProcessor) ProcessMessage(ctx context.Context, _ orchestrator.ProcessMessageRequest) (orchestrator.ProcessMessageResponse, error) {
	turntiming.Mark(ctx, turntiming.PointRouteDecided)
	turntiming.Mark(ctx, turntiming.PointDispatchStarted)
	return orchestrator.ProcessMessageResponse{}, errors.New("viewer processing failed")
}

type viewerBridgeCaptureProcessor struct {
	requests chan orchestrator.ProcessMessageRequest
}

func (p viewerBridgeCaptureProcessor) ProcessMessage(_ context.Context, req orchestrator.ProcessMessageRequest) (orchestrator.ProcessMessageResponse, error) {
	p.requests <- req
	return orchestrator.ProcessMessageResponse{SessionID: req.SessionID}, nil
}

func TestViewerBridgeResolvesProductionCanonicalSessionBeforeAcceptance(t *testing.T) {
	repo := sessionpersistence.NewJSONSessionRepository(t.TempDir())
	processor := viewerBridgeCaptureProcessor{requests: make(chan orchestrator.ProcessMessageRequest, 1)}
	deps := &Dependencies{canonicalEventStore: openViewerBridgeAdmissionStore(t)}
	factories := buildViewerBridgeHandlers(&config.Config{}, deps, "", ttsEntryRuntime{}, repo)
	handler := factories.ViewerSendFromOrch(processor)

	req := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{"message":"hello","to":"mio"}`))
	req.Header.Set("Content-Type", "application/json")
	attachViewerBridgeTestPresence(t, deps, req)
	response := httptest.NewRecorder()
	handler(response, req)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var accepted struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&accepted); err != nil {
		t.Fatal(err)
	}
	if err := modulecore.SessionID(accepted.SessionID).Validate(); err != nil {
		t.Fatalf("accepted session_id=%q: %v", accepted.SessionID, err)
	}
	select {
	case observed := <-processor.requests:
		if observed.SessionID != accepted.SessionID {
			t.Fatalf("processor session_id=%q want accepted %q", observed.SessionID, accepted.SessionID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("processor did not receive the accepted request")
	}
}

func TestViewerBridgeKeepsLegacyRouteWhenCombinedChatProcessorIsIneligible(t *testing.T) {
	cfg := &config.Config{TTS: config.TTSConfig{SpeechPlanEnabled: true}}
	processorEligible := runtimeCombinedChatAdmissionEnabled(
		cfg,
		runtimeRouteReplyClassifierStub{},
		runtimeCombinedTurnEngineStub{},
		true,
		false,
	)
	if processorEligible {
		t.Fatal("speech-plan configuration unexpectedly allowed combined CHAT admission")
	}
	processor := &viewerBridgeCountProcessor{done: make(chan struct{})}
	deps := &Dependencies{
		canonicalEventStore:           openViewerBridgeAdmissionStore(t),
		combinedChatProcessorEligible: processorEligible,
	}
	factories := buildViewerBridgeHandlers(
		cfg,
		deps,
		"",
		ttsEntryRuntime{},
		sessionpersistence.NewJSONSessionRepository(t.TempDir()),
	)
	handler := factories.ViewerSendFromOrch(processor)
	request := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{"message":"hello","to":"mio"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
	attachViewerBridgeTestPresence(t, deps, request)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want legacy route to remain available when combined CHAT is ineligible", response.Code, response.Body.String())
	}
	select {
	case <-processor.done:
	case <-time.After(time.Second):
		t.Fatal("legacy processor did not receive the request")
	}
	if processor.calls != 1 {
		t.Fatalf("legacy processor calls=%d, want 1", processor.calls)
	}
}

func TestViewerBridgeFailsClosedBeforeProcessorWhenEligibleOwnerQueueIsUnavailable(t *testing.T) {
	cfg := &config.Config{
		TaskStore:   config.TaskStoreConfig{Index: config.TaskStoreIndexConfig{Enabled: true}},
		ActionStore: config.ActionStoreConfig{Index: config.ActionStoreIndexConfig{Enabled: true}},
	}
	processorEligible := runtimeCombinedChatAdmissionEnabled(
		cfg,
		runtimeRouteReplyClassifierStub{},
		runtimeCombinedTurnEngineStub{},
		true,
		false,
	)
	if !processorEligible {
		t.Fatal("test fixture does not satisfy combined CHAT processor eligibility")
	}
	processor := &viewerBridgeCountProcessor{}
	deps := &Dependencies{
		combinedChatProcessorEligible: processorEligible,
		combinedChatAdmissionEnabled:  false,
	}
	handler := buildViewerBridgeHandlers(
		cfg,
		deps,
		"",
		ttsEntryRuntime{},
		sessionpersistence.NewJSONSessionRepository(t.TempDir()),
	).ViewerSendFromOrch(processor)
	request := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{"message":"hello","to":"mio"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
	attachViewerBridgeTestPresence(t, deps, request)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s, want fail-closed admission when a configured owner is not queue-bound", response.Code, response.Body.String())
	}
	if processor.calls != 0 {
		t.Fatalf("processor calls=%d, want 0 after pre-LLM admission rejection", processor.calls)
	}
}

type viewerBridgeSpeechGateProcessor struct {
	start  chan struct{}
	result chan error
}

func (p viewerBridgeSpeechGateProcessor) ProcessMessage(ctx context.Context, req orchestrator.ProcessMessageRequest) (orchestrator.ProcessMessageResponse, error) {
	<-p.start
	speechCtx, done := orchestrator.SpeechSynthesisContext(ctx)
	defer done()
	p.result <- speechCtx.Err()
	return orchestrator.ProcessMessageResponse{SessionID: req.SessionID}, nil
}

func TestViewerBridgeCancellationUsesSharedRegistryAndLatchesBeforeSpeechStart(t *testing.T) {
	processor := viewerBridgeSpeechGateProcessor{
		start:  make(chan struct{}),
		result: make(chan error, 1),
	}
	deps := &Dependencies{canonicalEventStore: openViewerBridgeAdmissionStore(t)}
	factories := buildViewerBridgeHandlers(
		&config.Config{},
		deps,
		"",
		ttsEntryRuntime{},
		sessionpersistence.NewJSONSessionRepository(t.TempDir()),
	)
	handler := factories.ViewerSendFromOrch(processor)

	req := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{"message":"hello","to":"mio"}`))
	req.Header.Set("Content-Type", "application/json")
	attachViewerBridgeTestPresence(t, deps, req)
	response := httptest.NewRecorder()
	handler(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q want=no-store", got)
	}
	var accepted struct {
		TurnID            string `json:"turn_id"`
		SpeechCancelToken string `json:"speech_cancel_token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.TurnID == "" || accepted.SpeechCancelToken == "" {
		t.Fatalf("accepted response missing turn cancellation identity: %+v", accepted)
	}

	cancelBody, err := json.Marshal(map[string]string{"speech_cancel_token": accepted.SpeechCancelToken})
	if err != nil {
		t.Fatal(err)
	}
	cancelResponse := httptest.NewRecorder()
	cancelRequest := httptest.NewRequest(http.MethodPost, "/viewer/tts/cancel", strings.NewReader(string(cancelBody)))
	factories.SpeechCancel.ServeHTTP(cancelResponse, cancelRequest)
	if cancelResponse.Code != http.StatusNoContent {
		t.Fatalf("cancel status=%d want=%d body=%s", cancelResponse.Code, http.StatusNoContent, cancelResponse.Body.String())
	}

	close(processor.start)
	select {
	case err := <-processor.result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("speech context error=%v want=%v", err, context.Canceled)
		}
	case <-time.After(time.Second):
		t.Fatal("speech processor did not complete")
	}
}

type viewerBridgePlaybackProgressProcessor struct {
	ready   chan struct{}
	started chan error
}

func (p viewerBridgePlaybackProgressProcessor) ProcessMessage(ctx context.Context, req orchestrator.ProcessMessageRequest) (orchestrator.ProcessMessageResponse, error) {
	progress := speech.PlaybackProgressFromContext(ctx)
	if progress == nil {
		p.started <- errors.New("Viewer playback progress was not attached to accepted turn")
		return orchestrator.ProcessMessageResponse{}, errors.New("Viewer playback progress is unavailable")
	}
	if err := progress.MarkReady(0); err != nil {
		p.started <- err
		return orchestrator.ProcessMessageResponse{}, err
	}
	close(p.ready)
	err := progress.WaitStarted(ctx, 0)
	p.started <- err
	return orchestrator.ProcessMessageResponse{SessionID: req.SessionID}, nil
}

func TestViewerBridgePlaybackProgressUsesAcceptedTurnAndActiveAudioOwner(t *testing.T) {
	resetActiveViewerControlForTest()
	defer resetActiveViewerControlForTest()
	activeViewerControl.Claim(moduletts.ViewerActiveKindAudio, "audio-owner")
	processor := viewerBridgePlaybackProgressProcessor{
		ready:   make(chan struct{}),
		started: make(chan error, 1),
	}
	deps := &Dependencies{canonicalEventStore: openViewerBridgeAdmissionStore(t)}
	factories := buildViewerBridgeHandlers(
		&config.Config{},
		deps,
		"",
		ttsEntryRuntime{},
		sessionpersistence.NewJSONSessionRepository(t.TempDir()),
	)
	handler := factories.ViewerSendFromOrch(processor)
	request := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{"message":"hello","to":"mio","viewer_client_id":"untrusted-send-id"}`))
	request.Header.Set("Content-Type", "application/json")
	attachViewerBridgeTestPresence(t, deps, request)
	response := httptest.NewRecorder()
	handler(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("send status=%d body=%s", response.Code, response.Body.String())
	}
	var accepted struct {
		RootTaskID string `json:"root_task_id"`
		TurnID     string `json:"turn_id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.RootTaskID == "" || accepted.TurnID == "" {
		t.Fatalf("accepted response lacks progress identity: %+v", accepted)
	}
	select {
	case <-processor.ready:
	case <-time.After(time.Second):
		t.Fatal("turn did not mark utterance ready")
	}

	for _, viewerClientID := range []string{"untrusted-send-id", "audio-owner"} {
		body, err := json.Marshal(map[string]any{
			"public_playback_ref": accepted.RootTaskID,
			"turn_id":             accepted.TurnID,
			"utterance_index":     0,
			"phase":               "started",
			"viewer_client_id":    viewerClientID,
		})
		if err != nil {
			t.Fatal(err)
		}
		progressResponse := httptest.NewRecorder()
		progressRequest := httptest.NewRequest(http.MethodPost, "/viewer/tts/progress", strings.NewReader(string(body)))
		factories.SpeechProgress.ServeHTTP(progressResponse, progressRequest)
		if progressResponse.Code != http.StatusNoContent {
			t.Fatalf("viewer=%s progress status=%d want=%d body=%s", viewerClientID, progressResponse.Code, http.StatusNoContent, progressResponse.Body.String())
		}
		if got := progressResponse.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("Cache-Control=%q want=no-store", got)
		}
	}
	select {
	case err := <-processor.started:
		if err != nil {
			t.Fatalf("accepted turn did not receive selected active audio progress: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("speech progress did not wake the accepted turn")
	}
}

type viewerBridgeTraceStore struct {
	events chan modulecore.EventEnvelope
}

func (s *viewerBridgeTraceStore) Append(_ context.Context, event modulecore.EventEnvelope) error {
	s.events <- event
	return nil
}

func (s *viewerBridgeTraceStore) AppendSequenced(_ context.Context, event modulecore.EventEnvelope) (modulecore.EventEnvelope, error) {
	event.EventSeq = 1
	s.events <- event
	return event, nil
}

func (s *viewerBridgeTraceStore) GetByID(context.Context, modulecore.EventID) (modulecore.EventEnvelope, bool, error) {
	return modulecore.EventEnvelope{}, false, nil
}

func (s *viewerBridgeTraceStore) ListByComponent(context.Context, string, int) ([]modulecore.EventEnvelope, error) {
	return nil, nil
}

func TestViewerAsyncErrorEventKeepsAcceptedIngressTrace(t *testing.T) {
	store := &viewerBridgeTraceStore{events: make(chan modulecore.EventEnvelope, 1)}
	archive, err := viewer.NewCanonicalEventLog(store)
	if err != nil {
		t.Fatalf("NewCanonicalEventLog() error = %v", err)
	}
	deps := &Dependencies{
		eventRelay:          &idleAwareEventListener{archive: archive},
		canonicalEventStore: openViewerBridgeAdmissionStore(t),
	}
	factories := buildViewerBridgeHandlers(&config.Config{}, deps, "", ttsEntryRuntime{}, sessionpersistence.NewJSONSessionRepository(t.TempDir()))
	handler := factories.ViewerSendFromOrch(viewerBridgeErrorProcessor{})

	req := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{"message":"hello","to":"mio","timings":true}`))
	attachViewerBridgeTestPresence(t, deps, req)
	response := httptest.NewRecorder()
	handler(response, req)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	var accepted struct {
		OK         bool   `json:"ok"`
		RootTaskID string `json:"root_task_id"`
		TraceID    string `json:"trace_id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&accepted); err != nil {
		t.Fatalf("decode accepted response: %v", err)
	}
	if !accepted.OK || accepted.RootTaskID == "" || accepted.TraceID == "" {
		t.Fatalf("accepted response = %+v, want root_task_id and trace_id", accepted)
	}
	if err := modulecore.TraceID(accepted.TraceID).Validate(); err != nil {
		t.Fatalf("accepted trace_id = %q is invalid: %v", accepted.TraceID, err)
	}
	if accepted.RootTaskID == accepted.TraceID {
		t.Fatalf("root_task_id and trace_id must remain distinct: %q", accepted.RootTaskID)
	}

	var event modulecore.EventEnvelope
	select {
	case event = <-store.events:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for viewer.error canonical event")
	}
	if event.EventType != "viewer.error" {
		t.Fatalf("event type = %q, want viewer.error", event.EventType)
	}
	taskID, _ := event.Payload["task_id"].(string)
	traceID, _ := event.Payload["trace_id"].(string)
	if taskID != accepted.RootTaskID {
		t.Fatalf("viewer.error task_id = %q, want accepted root_task_id %q", taskID, accepted.RootTaskID)
	}
	if traceID != accepted.TraceID {
		t.Fatalf("viewer.error trace_id = %q, want accepted trace_id %q", traceID, accepted.TraceID)
	}
	if event.TraceID != modulecore.TraceID(accepted.TraceID) {
		t.Fatalf("canonical envelope trace_id = %q, want accepted trace_id %q", event.TraceID, accepted.TraceID)
	}
	timing, ok := event.Payload["timing"].(map[string]any)
	if !ok {
		t.Fatalf("viewer.error timing = %#v, want partial timing snapshot", event.Payload["timing"])
	}
	if timing["schema_version"] != float64(turntiming.SchemaVersion) {
		t.Fatalf("viewer.error timing schema_version = %#v", timing["schema_version"])
	}
	points, ok := timing["points_ms"].(map[string]any)
	if !ok {
		t.Fatalf("viewer.error timing points_ms = %#v", timing["points_ms"])
	}
	for _, point := range []string{"handler_received", "accepted", "async_start", "route_decided", "dispatch_started"} {
		if _, ok := points[point]; !ok {
			t.Errorf("viewer.error timing missing partial point %q: %#v", point, points)
		}
	}
	if _, ok := points[string(turntiming.PointResponsePublicationStarted)]; ok {
		t.Fatalf("failed turn must not invent response_publication_started: %#v", points)
	}
}
