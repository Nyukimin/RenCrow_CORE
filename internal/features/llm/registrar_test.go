package llm

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterRoutesRegistersLLMOpsRoutes(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Dependencies{LLMOps: LLMOpsRoutes{
		Health:  statusHandler(http.StatusOK),
		Status:  statusHandler(http.StatusCreated),
		Start:   statusHandler(http.StatusAccepted),
		Stop:    statusHandler(http.StatusNoContent),
		Restart: statusHandler(http.StatusResetContent),
	}})

	tests := map[string]int{
		"/viewer/llm-ops/health":  http.StatusOK,
		"/viewer/llm-ops/status":  http.StatusCreated,
		"/viewer/llm-ops/start":   http.StatusAccepted,
		"/viewer/llm-ops/stop":    http.StatusNoContent,
		"/viewer/llm-ops/restart": http.StatusResetContent,
	}
	for path, want := range tests {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s status = %d, want %d", path, rec.Code, want)
		}
	}
}

func TestRegisterRoutesSkipsMissingLLMOpsHandlers(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Dependencies{})

	req := httptest.NewRequest(http.MethodGet, "/viewer/llm-ops/health", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func statusHandler(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}
}
