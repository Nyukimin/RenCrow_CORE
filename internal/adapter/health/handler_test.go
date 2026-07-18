package health

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	domainhealth "github.com/Nyukimin/RenCrow_CORE/internal/domain/health"
)

type stubCheckRunner struct {
	report domainhealth.HealthReport
}

func (s *stubCheckRunner) RunChecks(_ context.Context) domainhealth.HealthReport {
	return s.report
}

func (s *stubCheckRunner) IsReady(_ context.Context) bool {
	return s.report.Status == domainhealth.StatusOK
}

type contextAwareSlowRunner struct {
	report domainhealth.HealthReport
}

func (s *contextAwareSlowRunner) RunChecks(ctx context.Context) domainhealth.HealthReport {
	<-ctx.Done()
	return s.report
}

func (s *contextAwareSlowRunner) IsReady(ctx context.Context) bool {
	<-ctx.Done()
	return false
}

func newStubOK() *stubCheckRunner {
	return &stubCheckRunner{
		report: domainhealth.HealthReport{
			Status: domainhealth.StatusOK,
			Checks: []domainhealth.CheckResult{
				{Name: "mock", Status: domainhealth.StatusOK, Message: "ok", Duration: time.Millisecond},
			},
			Timestamp: time.Now(),
		},
	}
}

func newStubDown() *stubCheckRunner {
	return &stubCheckRunner{
		report: domainhealth.HealthReport{
			Status: domainhealth.StatusDown,
			Checks: []domainhealth.CheckResult{
				{Name: "mock", Status: domainhealth.StatusDown, Message: "unreachable", Duration: time.Millisecond},
			},
			Timestamp: time.Now(),
		},
	}
}

func TestHandleHealth_OK(t *testing.T) {
	h := NewHandler(newStubOK())
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	h.HandleHealth(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("expected status ok, got %v", body["status"])
	}
}

func TestHandleLive_DoesNotRunDependencyChecks(t *testing.T) {
	h := NewHandler(&contextAwareSlowRunner{report: newStubDown().report})
	req := httptest.NewRequest("GET", "/health/live", nil)
	w := httptest.NewRecorder()

	start := time.Now()
	h.HandleLive(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("liveness handler ran dependency checks: %s", elapsed)
	}
	if got := w.Body.String(); got != "{\"alive\":true}\n" {
		t.Fatalf("unexpected body: %q", got)
	}
}

func TestHandleHealth_Down(t *testing.T) {
	h := NewHandler(newStubDown())
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	h.HandleHealth(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

func TestHandleHealth_UsesRequestTimeout(t *testing.T) {
	h := NewHandlerWithTimeout(&contextAwareSlowRunner{report: newStubDown().report}, 20*time.Millisecond)
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()

	start := time.Now()
	h.HandleHealth(w, req)
	elapsed := time.Since(start)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("health handler took %s, want bounded timeout", elapsed)
	}
}

func TestHandleReady_True(t *testing.T) {
	h := NewHandler(newStubOK())
	req := httptest.NewRequest("GET", "/ready", nil)
	w := httptest.NewRecorder()
	h.HandleReady(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestHandleReady_False(t *testing.T) {
	h := NewHandler(newStubDown())
	req := httptest.NewRequest("GET", "/ready", nil)
	w := httptest.NewRecorder()
	h.HandleReady(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

func TestHandleReady_UsesRequestTimeout(t *testing.T) {
	h := NewHandlerWithTimeout(&contextAwareSlowRunner{report: newStubDown().report}, 20*time.Millisecond)
	req := httptest.NewRequest("GET", "/ready", nil)
	w := httptest.NewRecorder()

	start := time.Now()
	h.HandleReady(w, req)
	elapsed := time.Since(start)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("ready handler took %s, want bounded timeout", elapsed)
	}
}

func TestHandleHealth_JSONStructure(t *testing.T) {
	h := NewHandler(newStubOK())
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	h.HandleHealth(w, req)

	var body struct {
		Status    string `json:"status"`
		Timestamp string `json:"timestamp"`
		Checks    []struct {
			Name       string  `json:"name"`
			Status     string  `json:"status"`
			Message    string  `json:"message"`
			DurationMs float64 `json:"duration_ms"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body.Timestamp == "" {
		t.Error("expected non-empty timestamp")
	}
	if len(body.Checks) != 1 {
		t.Errorf("expected 1 check, got %d", len(body.Checks))
	}
	if body.Checks[0].Name != "mock" {
		t.Errorf("expected check name 'mock', got %s", body.Checks[0].Name)
	}
}
