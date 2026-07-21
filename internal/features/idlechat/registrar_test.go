package idlechat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterRoutesKeepsIdleChatViewerPaths(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Dependencies{Routes: Routes{
		Start:       statusHandler(http.StatusAccepted),
		Stop:        statusHandler(http.StatusNoContent),
		Interrupt:   statusHandler(http.StatusResetContent),
		Status:      statusHandler(http.StatusOK),
		Collection:  statusHandler(http.StatusNonAuthoritativeInfo),
		Logs:        statusHandler(http.StatusPartialContent),
		Forecast:    statusHandler(http.StatusCreated),
		Story:       statusHandler(http.StatusConflict),
		StorySimple: statusHandler(http.StatusAlreadyReported),
	}})

	tests := []struct {
		path string
		want int
	}{
		{path: "/viewer/idlechat/start", want: http.StatusAccepted},
		{path: "/viewer/idlechat/stop", want: http.StatusNoContent},
		{path: "/viewer/idlechat/interrupt", want: http.StatusResetContent},
		{path: "/viewer/idlechat/status", want: http.StatusOK},
		{path: "/viewer/idlechat/collection", want: http.StatusNonAuthoritativeInfo},
		{path: "/viewer/idlechat/logs", want: http.StatusPartialContent},
		{path: "/viewer/idlechat/forecast", want: http.StatusCreated},
		{path: "/viewer/idlechat/story", want: http.StatusConflict},
		{path: "/viewer/idlechat/story-simple", want: http.StatusAlreadyReported},
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
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/viewer/idlechat/stop", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=%d", rec.Code, http.StatusNotFound)
	}
}

func TestStartBackgroundStartsIdleChatRuntime(t *testing.T) {
	bg := &fakeBackground{}
	if err := StartBackground(context.Background(), Dependencies{Background: bg}); err != nil {
		t.Fatalf("StartBackground returned error: %v", err)
	}
	if bg.starts != 1 {
		t.Fatalf("starts=%d want=1", bg.starts)
	}
}

func TestStartBackgroundAllowsMissingRuntime(t *testing.T) {
	if err := StartBackground(context.Background(), Dependencies{}); err != nil {
		t.Fatalf("StartBackground returned error: %v", err)
	}
}

type fakeBackground struct {
	starts int
}

func (f *fakeBackground) Start() {
	f.starts++
}

func statusHandler(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}
}
