package sandbox

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterRoutesRegistersSandboxPaths(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Dependencies{Routes: Routes{
		Status:                statusHandler(http.StatusOK),
		Promotion:             statusHandler(http.StatusCreated),
		PromotionApply:        statusHandler(http.StatusAccepted),
		PromotionRollback:     statusHandler(http.StatusNoContent),
		PromotionPreview:      statusHandler(http.StatusPartialContent),
		PromotionManualReview: statusHandler(http.StatusResetContent),
		WorktreeCreate:        statusHandler(http.StatusAlreadyReported),
		WorktreeClose:         statusHandler(http.StatusIMUsed),
	}})

	tests := []struct {
		path string
		want int
	}{
		{path: "/viewer/sandbox", want: http.StatusOK},
		{path: "/viewer/sandbox/promotions", want: http.StatusCreated},
		{path: "/viewer/sandbox/promotions/apply", want: http.StatusAccepted},
		{path: "/viewer/sandbox/promotions/rollback", want: http.StatusNoContent},
		{path: "/viewer/sandbox/promotions/preview", want: http.StatusPartialContent},
		{path: "/viewer/sandbox/promotions/manual-review", want: http.StatusResetContent},
		{path: "/viewer/sandbox/worktrees/create", want: http.StatusAlreadyReported},
		{path: "/viewer/sandbox/worktrees/close", want: http.StatusIMUsed},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != tt.want {
				t.Fatalf("status=%d want=%d", rec.Code, tt.want)
			}
		})
	}
}

func TestRegisterRoutesSkipsNilHandlers(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Dependencies{})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/viewer/sandbox", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=%d", rec.Code, http.StatusNotFound)
	}
}

func statusHandler(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}
}
