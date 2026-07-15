package viewer

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlePageRedirectsExternalModesToPortal(t *testing.T) {
	t.Setenv("RENCROW_PORTAL_URL", "http://127.0.0.1:18791")

	tests := []struct {
		path string
		want string
	}{
		{path: "/viewer?mode=lab", want: "http://127.0.0.1:18791/?mode=lab"},
		{path: "/viewer?mode=view", want: "http://127.0.0.1:18791/?mode=view"},
		{path: "/viewer?mode=live", want: "http://127.0.0.1:18791/?mode=view"},
	}
	for _, tt := range tests {
		rec := httptest.NewRecorder()
		HandlePage(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
		if rec.Code != http.StatusTemporaryRedirect {
			t.Fatalf("%s status=%d", tt.path, rec.Code)
		}
		if got := rec.Header().Get("Location"); got != tt.want {
			t.Fatalf("%s location=%q want=%q", tt.path, got, tt.want)
		}
	}
}

func TestHandlePageKeepsDebugViewerInCore(t *testing.T) {
	t.Setenv("RENCROW_PORTAL_URL", "http://127.0.0.1:18791")
	rec := httptest.NewRecorder()
	HandlePage(rec, httptest.NewRequest(http.MethodGet, "/viewer?tab=ops", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "RenCrow") {
		t.Fatal("debug Viewer HTML was not served")
	}
}
