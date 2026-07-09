package viewer

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	otelexportapp "github.com/Nyukimin/RenCrow_CORE/internal/application/otelexport"
)

func TestHandleOTelExportDryRun(t *testing.T) {
	handler := HandleOTelExport(otelexportapp.NewService(""))
	rec := httptest.NewRecorder()

	handler(rec, httptest.NewRequest(http.MethodPost, "/viewer/otel/export", strings.NewReader(`{
		"dry_run": true,
		"events": [{"name":"heartbeat","attributes":{"token":"secret","status":"ok"}}]
	}`)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{`"status":"preview"`, `"exported":1`, `"token"`, "[REDACTED]"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("response missing %s: %s", want, rec.Body.String())
		}
	}
}
