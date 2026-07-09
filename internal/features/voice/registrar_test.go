package voice

import (
	"net/http"
	"net/http/httptest"
	"testing"

	sttfeature "github.com/Nyukimin/RenCrow_CORE/internal/features/stt"
	ttsfeature "github.com/Nyukimin/RenCrow_CORE/internal/features/tts"
)

func TestRegisterRoutesRegistersVoiceAudioAndSubfeaturePaths(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Dependencies{
		Routes: Routes{
			VoiceChat:         http.HandlerFunc(statusHandler(http.StatusOK)),
			AudioRouterEvents: statusHandler(http.StatusAccepted),
			ActiveControl:     statusHandler(http.StatusCreated),
		},
		STT: sttfeature.Dependencies{Routes: sttfeature.Routes{
			ClientLog: statusHandler(http.StatusNoContent),
		}},
		TTS: ttsfeature.Dependencies{Routes: ttsfeature.Routes{
			Audio: statusHandler(http.StatusPartialContent),
		}},
	})

	tests := []struct {
		path string
		want int
	}{
		{path: "/voice-chat", want: http.StatusOK},
		{path: "/voice-chat-ws", want: http.StatusOK},
		{path: "/audio-router/events", want: http.StatusAccepted},
		{path: "/viewer/active-control", want: http.StatusCreated},
		{path: "/viewer/stt/log", want: http.StatusNoContent},
		{path: "/viewer/tts/audio", want: http.StatusPartialContent},
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
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/voice-chat", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=%d", rec.Code, http.StatusNotFound)
	}
}

func statusHandler(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}
}
