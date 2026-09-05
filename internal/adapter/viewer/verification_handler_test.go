package viewer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	domainverification "github.com/Nyukimin/RenCrow_CORE/internal/domain/verification"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type stubVerificationReader struct {
	items []domainverification.VerificationReport
}

func (s stubVerificationReader) ListRecent(context.Context, int) ([]domainverification.VerificationReport, error) {
	return s.items, nil
}

func (s stubVerificationReader) GetByTaskID(_ context.Context, taskID modulecore.TaskID) (domainverification.VerificationReport, error) {
	for _, item := range s.items {
		if item.TaskID == taskID {
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
	report := testVerificationReport()
	handler := HandleVerificationRecent(stubVerificationReader{items: []domainverification.VerificationReport{report}})
	req := httptest.NewRequest(http.MethodGet, "/viewer/verification/recent", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), string(report.TaskID)) {
		t.Fatalf("expected report body, got %s", rec.Body.String())
	}
}

func TestHandleVerificationDetailRequiresTaskID(t *testing.T) {
	handler := HandleVerificationDetail(stubVerificationReader{})
	req := httptest.NewRequest(http.MethodGet, "/viewer/verification/detail", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestHandleVerificationDetailUsesCanonicalTaskIDOnly(t *testing.T) {
	report := testVerificationReport()
	handler := HandleVerificationDetail(stubVerificationReader{items: []domainverification.VerificationReport{report}})

	valid := httptest.NewRecorder()
	handler(valid, httptest.NewRequest(http.MethodGet, "/viewer/verification/detail?task_id="+string(report.TaskID), nil))
	if valid.Code != http.StatusOK {
		t.Fatalf("valid TaskID status=%d body=%s", valid.Code, valid.Body.String())
	}

	legacyKey := "job" + "_" + "id"
	for _, target := range []string{"/viewer/verification/detail?task_id=bad", "/viewer/verification/detail?" + legacyKey + "=legacy"} {
		recorder := httptest.NewRecorder()
		handler(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("legacy/invalid query accepted: target=%s status=%d", target, recorder.Code)
		}
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
		ID:           "verify_1",
		TaskID:       modulecore.NewTaskID(),
		SessionID:    "session-1",
		Route:        "CHAT",
		Status:       domainverification.StatusWeaklySupported,
		TriggerLevel: domainverification.TriggerMedium,
		CreatedAt:    time.Now().UTC(),
	}
}
