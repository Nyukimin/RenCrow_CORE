package llmops

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterRoutesKeepsLLMOpsViewerPaths(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Dependencies{Routes: Routes{
		Nodes:       statusHandler(http.StatusOK),
		Node:        statusHandler(http.StatusAccepted),
		NodeModels:  statusHandler(http.StatusNoContent),
		ModelMatrix: statusHandler(http.StatusPartialContent),
		Refresh:     statusHandler(http.StatusCreated),
	}})

	tests := []struct {
		method string
		path   string
		want   int
	}{
		{method: http.MethodGet, path: "/viewer/llm-ops/nodes", want: http.StatusOK},
		{method: http.MethodGet, path: "/viewer/llm-ops/node?node_id=a", want: http.StatusAccepted},
		{method: http.MethodGet, path: "/viewer/llm-ops/node/models?node_id=a", want: http.StatusNoContent},
		{method: http.MethodGet, path: "/viewer/llm-ops/model/matrix?model_id=m", want: http.StatusPartialContent},
		{method: http.MethodPost, path: "/viewer/llm-ops/refresh", want: http.StatusCreated},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))
			if rec.Code != tt.want {
				t.Fatalf("status=%d want=%d", rec.Code, tt.want)
			}
		})
	}
}

func TestRegisterRoutesSkipsNilHandlers(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Dependencies{})

	for _, path := range []string{"/viewer/llm-ops/nodes", "/viewer/llm-ops/node", "/viewer/llm-ops/node/models", "/viewer/llm-ops/model/matrix", "/viewer/llm-ops/refresh"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status=%d want=%d", path, rec.Code, http.StatusNotFound)
		}
	}
}

func TestRegisterRoutesToleratesNilMux(t *testing.T) {
	RegisterRoutes(nil, Dependencies{Routes: Routes{Nodes: statusHandler(http.StatusOK)}})
	if err := StartBackground(nil, Dependencies{}); err != nil {
		t.Fatalf("StartBackground: %v", err)
	}
}

func statusHandler(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}
}
