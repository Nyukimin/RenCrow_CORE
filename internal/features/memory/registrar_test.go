package memory

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterRoutesRegistersMemoryPaths(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Dependencies{Routes: Routes{
		Snapshot:      statusHandler(http.StatusOK),
		Layers:        statusHandler(http.StatusCreated),
		Events:        statusHandler(http.StatusAccepted),
		State:         statusHandler(http.StatusNoContent),
		Promote:       statusHandler(http.StatusPartialContent),
		User:          statusHandler(http.StatusResetContent),
		UserState:     statusHandler(http.StatusAlreadyReported),
		UserForget:    statusHandler(http.StatusIMUsed),
		UserSupersede: statusHandler(http.StatusMultiStatus),
		RecallPack:    statusHandler(http.StatusBadRequest),
		RecallTraces:  statusHandler(http.StatusConflict),
	}})

	tests := []struct {
		path string
		want int
	}{
		{path: "/viewer/memory/snapshot", want: http.StatusOK},
		{path: "/viewer/memory/layers", want: http.StatusCreated},
		{path: "/viewer/memory/events", want: http.StatusAccepted},
		{path: "/viewer/memory/state", want: http.StatusNoContent},
		{path: "/viewer/memory/promote", want: http.StatusPartialContent},
		{path: "/viewer/memory/user", want: http.StatusResetContent},
		{path: "/viewer/memory/user/state", want: http.StatusAlreadyReported},
		{path: "/viewer/memory/user/forget", want: http.StatusIMUsed},
		{path: "/viewer/memory/user/supersede", want: http.StatusMultiStatus},
		{path: "/viewer/memory/recall-pack", want: http.StatusBadRequest},
		{path: "/viewer/recall/traces", want: http.StatusConflict},
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
