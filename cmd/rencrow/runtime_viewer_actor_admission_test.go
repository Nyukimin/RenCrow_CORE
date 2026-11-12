package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	sessionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/session"
)

type viewerActorAdmissionRecorder struct {
	mu          sync.Mutex
	actor       string
	priority    domaintask.Priority
	acquired    chan struct{}
	released    chan struct{}
	releaseOnce sync.Once
}

func (a *viewerActorAdmissionRecorder) Acquire(_ context.Context, actor string, priority domaintask.Priority) (func(), error) {
	a.mu.Lock()
	a.actor, a.priority = actor, priority
	a.mu.Unlock()
	close(a.acquired)
	return func() { a.releaseOnce.Do(func() { close(a.released) }) }, nil
}

type viewerActorAdmissionProcessor struct {
	admission *viewerActorAdmissionRecorder
	called    chan error
}

func (p viewerActorAdmissionProcessor) ProcessMessage(_ context.Context, req orchestrator.ProcessMessageRequest) (orchestrator.ProcessMessageResponse, error) {
	select {
	case <-p.admission.acquired:
		p.called <- nil
		return orchestrator.ProcessMessageResponse{SessionID: req.SessionID, Response: "ok"}, nil
	default:
		err := context.DeadlineExceeded
		p.called <- err
		return orchestrator.ProcessMessageResponse{}, err
	}
}

func TestLegacyViewerDirectRecipientsAcquireHighActorAdmission(t *testing.T) {
	for _, actor := range []string{"mio", "shiro", "kuro", "midori"} {
		t.Run(actor, func(t *testing.T) {
			admission := &viewerActorAdmissionRecorder{acquired: make(chan struct{}), released: make(chan struct{})}
			deps := &Dependencies{agentAdmission: admission, canonicalEventStore: openViewerBridgeAdmissionStore(t)}
			processor := viewerActorAdmissionProcessor{admission: admission, called: make(chan error, 1)}
			handler := buildViewerBridgeHandlers(
				&config.Config{}, deps, "", ttsEntryRuntime{}, sessionpersistence.NewJSONSessionRepository(t.TempDir()),
			).ViewerSendFromOrch(processor)
			request := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{"message":"hello","to":"`+actor+`"}`))
			request.Header.Set("Content-Type", "application/json")
			attachViewerBridgeTestPresence(t, deps, request)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code < http.StatusOK || response.Code >= http.StatusMultipleChoices {
				t.Fatalf("Viewer status=%d body=%s", response.Code, response.Body.String())
			}
			select {
			case err := <-processor.called:
				if err != nil {
					t.Fatalf("legacy processor ran without acquired actor reservation: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("legacy Viewer processor was not called")
			}
			select {
			case <-admission.released:
			case <-time.After(2 * time.Second):
				t.Fatal("legacy Viewer actor reservation was not released after processing")
			}
			admission.mu.Lock()
			gotActor, gotPriority := admission.actor, admission.priority
			admission.mu.Unlock()
			if gotActor != actor || gotPriority != domaintask.PriorityHigh {
				t.Fatalf("legacy admission actor=%q priority=%q; want %q/high", gotActor, gotPriority, actor)
			}
		})
	}
}
