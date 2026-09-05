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
	sessionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/session"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type viewerBridgeErrorProcessor struct{}

func (viewerBridgeErrorProcessor) ProcessMessage(context.Context, orchestrator.ProcessMessageRequest) (orchestrator.ProcessMessageResponse, error) {
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
	factories := buildViewerBridgeHandlers(&config.Config{}, &Dependencies{}, "", ttsEntryRuntime{}, repo)
	handler := factories.ViewerSendFromOrch(processor)

	req := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{"message":"hello","to":"mio"}`))
	req.Header.Set("Content-Type", "application/json")
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
	deps := &Dependencies{eventRelay: &idleAwareEventListener{archive: archive}}
	factories := buildViewerBridgeHandlers(&config.Config{}, deps, "", ttsEntryRuntime{}, sessionpersistence.NewJSONSessionRepository(t.TempDir()))
	handler := factories.ViewerSendFromOrch(viewerBridgeErrorProcessor{})

	req := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{"message":"hello","to":"mio"}`))
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
}
