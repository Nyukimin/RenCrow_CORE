package memory

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterRoutesRegistersMemoryPaths(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Dependencies{Routes: Routes{
		Owner:              statusHandler(http.StatusContinue),
		Snapshot:           statusHandler(http.StatusOK),
		Layers:             statusHandler(http.StatusCreated),
		Events:             statusHandler(http.StatusAccepted),
		State:              statusHandler(http.StatusNoContent),
		Promote:            statusHandler(http.StatusPartialContent),
		User:               statusHandler(http.StatusResetContent),
		UserState:          statusHandler(http.StatusAlreadyReported),
		UserForget:         statusHandler(http.StatusIMUsed),
		UserSupersede:      statusHandler(http.StatusMultiStatus),
		RecallPack:         statusHandler(http.StatusBadRequest),
		RecallTraces:       statusHandler(http.StatusConflict),
		ProfilePromotions:  statusHandler(http.StatusTeapot),
		ProfileRetry:       statusHandler(http.StatusEarlyHints),
		ChatGPTImportOwner: statusHandler(http.StatusAccepted),
	}})

	tests := []struct {
		path string
		want int
	}{
		{path: "/v1/memory/user", want: http.StatusContinue},
		{path: "/v1/memory/user/mem-1", want: http.StatusContinue},
		{path: "/viewer/memory/lifecycle/plan", want: http.StatusContinue},
		{path: "/viewer/memory/lifecycle/run", want: http.StatusContinue},
		{path: "/viewer/memory/export/parquet", want: http.StatusContinue},
		{path: "/viewer/memory/export/export-request-1", want: http.StatusContinue},
		{path: "/viewer/memory/snapshot", want: http.StatusOK},
		{path: "/viewer/memory/layers", want: http.StatusCreated},
		{path: "/viewer/memory/events", want: http.StatusAccepted},
		{path: "/viewer/memory/state", want: http.StatusNoContent},
		{path: "/viewer/memory/promote", want: http.StatusPartialContent},
		{path: "/viewer/memory/user", want: http.StatusResetContent},
		{path: "/viewer/memory/user/archive", want: http.StatusContinue},
		{path: "/viewer/memory/user/state", want: http.StatusAlreadyReported},
		{path: "/viewer/memory/user/forget", want: http.StatusIMUsed},
		{path: "/viewer/memory/user/supersede", want: http.StatusMultiStatus},
		{path: "/viewer/memory/recall-pack", want: http.StatusBadRequest},
		{path: "/viewer/recall/traces", want: http.StatusConflict},
		{path: "/viewer/memory/profile-promotions", want: http.StatusTeapot},
		{path: "/viewer/memory/profile-promotions/retry", want: http.StatusEarlyHints},
		{path: "/viewer/memory/import/chatgpt", want: http.StatusNotFound},
		{path: "/viewer/memory/import/chatgpt/confirm", want: http.StatusNotFound},
		{path: "/v1/memory/import/chatgpt", want: http.StatusAccepted},
		{path: "/v1/memory/import/chatgpt/export-1", want: http.StatusAccepted},
		{path: "/v1/memory/import/chatgpt/confirm", want: http.StatusAccepted},
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

func TestRegisterRoutesKeepsChatGPTOwnerPrefixBounded(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Dependencies{Routes: Routes{ChatGPTImportOwner: statusHandler(http.StatusAccepted)}})
	for _, path := range []string{
		"/v1/memory/import/chatgpt/",
		"/v1/memory/import/chatgpt/export-1/extra",
		"/v1/memory/import/chatgpt/confirm/",
	} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusAccepted {
				// The registrar owns route reachability; exact path semantics are
				// enforced by the single owner handler.
				t.Fatalf("status=%d want registered owner handler=%d", rec.Code, http.StatusAccepted)
			}
		})
	}
}

func TestRegisterRoutesDoesNotExpandLifecyclePaths(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Dependencies{Routes: Routes{Owner: statusHandler(http.StatusContinue)}})
	for _, path := range []string{
		"/viewer/memory/lifecycle/plan/",
		"/viewer/memory/lifecycle/run/",
		"/viewer/memory/lifecycle/plan/extra",
		"/viewer/memory/lifecycle/run/extra",
	} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status=%d body=%s, want exact lifecycle route only", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestRegisterRoutesSkipsNilHandlers(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Dependencies{})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/viewer/memory/snapshot", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=%d", rec.Code, http.StatusNotFound)
	}
}

func statusHandler(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}
}
