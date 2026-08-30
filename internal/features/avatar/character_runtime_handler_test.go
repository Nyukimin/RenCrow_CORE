package avatar

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/characterruntime"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
)

type eventCapture struct {
	events []orchestrator.OrchestratorEvent
	err    error
}

func (c *eventCapture) OnEvent(event orchestrator.OrchestratorEvent) error {
	if c.err != nil {
		return c.err
	}
	c.events = append(c.events, event)
	return nil
}

func TestHandleCharacterRuntimeEmitsCorrelatedTurns(t *testing.T) {
	events := &eventCapture{}
	rec := httptest.NewRecorder()
	HandleCharacterRuntime(characterruntime.NewService(), events).ServeHTTP(
		rec,
		httptest.NewRequest(http.MethodPost, "/viewer/character-runtime", strings.NewReader(`{"user_message":"確認して","characters":["mio","shiro"]}`)),
	)
	if rec.Code != http.StatusCreated || len(events.events) != 2 {
		t.Fatalf("status=%d events=%#v body=%s", rec.Code, events.events, rec.Body.String())
	}
	if !strings.HasPrefix(events.events[0].TraceID, "trc_") || events.events[0].TraceID != events.events[1].TraceID {
		t.Fatalf("trace mismatch: %#v", events.events)
	}
	if !strings.HasPrefix(events.events[0].MessageID, "msg_") || events.events[0].MessageID == events.events[1].MessageID {
		t.Fatalf("message identity mismatch: %#v", events.events)
	}
}

func TestHandleCharacterRuntimePublicationFailureIsNotSuccess(t *testing.T) {
	events := &eventCapture{err: errors.New("canonical append failed")}
	rec := httptest.NewRecorder()
	HandleCharacterRuntime(characterruntime.NewService(), events).ServeHTTP(
		rec,
		httptest.NewRequest(http.MethodPost, "/viewer/character-runtime", strings.NewReader(`{"user_message":"確認して"}`)),
	)
	if rec.Code == http.StatusCreated || rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s, want service unavailable", rec.Code, rec.Body.String())
	}
}
