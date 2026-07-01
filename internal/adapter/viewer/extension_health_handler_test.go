package viewer

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHandleExtensionHealthSummarizesItems(t *testing.T) {
	handler := HandleExtensionHealth(ExtensionHealthOptions{
		Now: func() time.Time { return time.Date(2026, 6, 22, 7, 30, 0, 0, time.UTC) },
		Items: []ExtensionHealthItem{
			{ID: "tool-registry", Kind: "tool", Name: "Tool Registry", Source: "runtime", Configured: true, Loaded: true},
			{ID: "skill-governance", Kind: "skill", Name: "Skill Governance", Source: "config", Configured: false, Loaded: false},
			{ID: "broken-provider", Kind: "provider", Name: "Broken Provider", Source: "runtime", Status: "broken", Configured: true},
		},
	})
	rec := httptest.NewRecorder()

	handler(rec, httptest.NewRequest(http.MethodGet, "/viewer/extensions/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{`"checked_at":"2026-06-22T07:30:00Z"`, `"ok":1`, `"unconfigured":1`, `"broken":1`, `"id":"tool_registry"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("response missing %s: %s", want, rec.Body.String())
		}
	}
}
