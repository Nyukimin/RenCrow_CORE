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
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type viewerBridgeErrorProcessor struct{}

func (viewerBridgeErrorProcessor) ProcessMessage(context.Context, orchestrator.ProcessMessageRequest) (orchestrator.ProcessMessageResponse, error) {
	return orchestrator.ProcessMessageResponse{}, errors.New("viewer processing failed")
}

type viewerBridgeTraceStore struct {
	events chan modulecore.EventEnvelope
}

func (s *viewerBridgeTraceStore) Append(_ context.Context, event modulecore.EventEnvelope) error {
	s.events <- event
	return nil
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
	factories := buildViewerBridgeHandlers(&config.Config{}, deps, "", ttsEntryRuntime{})
	handler := factories.ViewerSendFromOrch(viewerBridgeErrorProcessor{})

	req := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{"message":"hello","to":"mio"}`))
	response := httptest.NewRecorder()
	handler(response, req)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	var accepted struct {
		OK      bool   `json:"ok"`
		JobID   string `json:"job_id"`
		TraceID string `json:"trace_id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&accepted); err != nil {
		t.Fatalf("decode accepted response: %v", err)
	}
	if !accepted.OK || accepted.JobID == "" || accepted.TraceID == "" {
		t.Fatalf("accepted response = %+v, want job_id and trace_id", accepted)
	}
	if err := modulecore.TraceID(accepted.TraceID).Validate(); err != nil {
		t.Fatalf("accepted trace_id = %q is invalid: %v", accepted.TraceID, err)
	}
	if accepted.JobID == accepted.TraceID {
		t.Fatalf("job_id and trace_id must remain distinct: %q", accepted.JobID)
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
	jobID, _ := event.Payload["job_id"].(string)
	traceID, _ := event.Payload["trace_id"].(string)
	if jobID != accepted.JobID {
		t.Fatalf("viewer.error job_id = %q, want accepted job_id %q", jobID, accepted.JobID)
	}
	if traceID != accepted.TraceID {
		t.Fatalf("viewer.error trace_id = %q, want accepted trace_id %q", traceID, accepted.TraceID)
	}
	if event.TraceID != modulecore.TraceID(accepted.TraceID) {
		t.Fatalf("canonical envelope trace_id = %q, want accepted trace_id %q", event.TraceID, accepted.TraceID)
	}
}
