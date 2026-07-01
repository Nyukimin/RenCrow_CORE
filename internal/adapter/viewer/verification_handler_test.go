package viewer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	domainverification "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/verification"
)

type stubVerificationReader struct {
	items []domainverification.VerificationReport
}

func (s stubVerificationReader) ListRecent(context.Context, int) ([]domainverification.VerificationReport, error) {
	return s.items, nil
}

func (s stubVerificationReader) GetByJobID(_ context.Context, jobID string) (domainverification.VerificationReport, error) {
	for _, item := range s.items {
		if item.JobID == jobID {
			return item, nil
		}
	}
	return domainverification.VerificationReport{}, errNotFoundForTest{}
}

func (s stubVerificationReader) Summary(context.Context) (map[string]map[string]int, error) {
	return map[string]map[string]int{
		"status": {string(domainverification.StatusWeaklySupported): len(s.items)},
	}, nil
}

type errNotFoundForTest struct{}

func (errNotFoundForTest) Error() string { return "not found" }

func TestHandleVerificationRecent(t *testing.T) {
	handler := HandleVerificationRecent(stubVerificationReader{items: []domainverification.VerificationReport{testVerificationReport()}})
	req := httptest.NewRequest(http.MethodGet, "/viewer/verification/recent", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "job-1") {
		t.Fatalf("expected report body, got %s", rec.Body.String())
	}
}

func TestHandleVerificationDetailRequiresJobID(t *testing.T) {
	handler := HandleVerificationDetail(stubVerificationReader{})
	req := httptest.NewRequest(http.MethodGet, "/viewer/verification/detail", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestHandleVerificationSummary(t *testing.T) {
	handler := HandleVerificationSummary(stubVerificationReader{items: []domainverification.VerificationReport{testVerificationReport()}})
	req := httptest.NewRequest(http.MethodGet, "/viewer/verification/summary", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "weakly_supported") {
		t.Fatalf("expected summary body, got %s", rec.Body.String())
	}
}

func TestHandleVerificationUnavailable(t *testing.T) {
	handler := HandleVerificationUnavailable()
	req := httptest.NewRequest(http.MethodGet, "/viewer/verification/recent", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "verification store unavailable") {
		t.Fatalf("expected unavailable body, got %s", rec.Body.String())
	}
}

func TestHandleVerificationUnavailableOptional(t *testing.T) {
	handler := HandleVerificationUnavailable()
	req := httptest.NewRequest(http.MethodGet, "/viewer/verification/recent?viewer_optional=1", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"status":503`) || !strings.Contains(rec.Body.String(), "verification store unavailable") {
		t.Fatalf("expected unavailable json body, got %s", rec.Body.String())
	}
}

func testVerificationReport() domainverification.VerificationReport {
	return domainverification.VerificationReport{
		ID:           "verify_job-1",
		JobID:        "job-1",
		SessionID:    "session-1",
		Route:        "CHAT",
		Status:       domainverification.StatusWeaklySupported,
		TriggerLevel: domainverification.TriggerMedium,
		CreatedAt:    time.Now().UTC(),
	}
}
