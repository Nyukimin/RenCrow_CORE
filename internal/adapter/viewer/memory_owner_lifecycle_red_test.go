package viewer

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// RED: the lifecycle owner route must be a CORE endpoint, not a missing-path
// response from the legacy owner handler.
func TestMemoryOwnerLifecyclePlanRouteIsAvailable(t *testing.T) {
	h := NewMemoryOwnerHandler(&memoryOwnerStoreStub{}, "ren", []byte("owner-token-012345678901234567890123"))
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/viewer/memory/lifecycle/plan", strings.NewReader("{}"))
	req.RemoteAddr = "127.0.0.1:18790"
	req.Header.Set("Authorization", "Bearer owner-token-012345678901234567890123")
	req.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
	req.Header.Set("X-RenCrow-Interaction-Profile", "cmd-control")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("lifecycle plan status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
}
