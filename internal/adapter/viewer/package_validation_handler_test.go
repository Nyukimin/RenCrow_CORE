package viewer

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	packagevalidationapp "github.com/Nyukimin/picoclaw_multiLLM/internal/application/packagevalidation"
)

func TestHandlePackageValidationBlocksPackageUpdate(t *testing.T) {
	handler := HandlePackageValidation(packagevalidationapp.NewService(t.TempDir()))
	rec := httptest.NewRecorder()

	handler(rec, httptest.NewRequest(http.MethodPost, "/viewer/package-validation", strings.NewReader(`{"paths":["go.mod"]}`)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{`"status":"blocked"`, `"install_allowed":false`, `"human_approved"`, `"rollback_evidence_path"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("response missing %s: %s", want, rec.Body.String())
		}
	}
}

func TestHandlePackageValidationRejectsBadPath(t *testing.T) {
	handler := HandlePackageValidation(packagevalidationapp.NewService(t.TempDir()))
	rec := httptest.NewRecorder()

	handler(rec, httptest.NewRequest(http.MethodPost, "/viewer/package-validation", strings.NewReader(`{"paths":["../go.mod"]}`)))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
