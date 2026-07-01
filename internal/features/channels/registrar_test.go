package channels

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterRoutesRegistersChannelAndEntryPaths(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Dependencies{Routes: Routes{
		Webhook:            http.HandlerFunc(statusHandler(http.StatusOK)),
		TelegramWebhook:    http.HandlerFunc(statusHandler(http.StatusCreated)),
		DiscordWebhook:     http.HandlerFunc(statusHandler(http.StatusAccepted)),
		SlackWebhook:       http.HandlerFunc(statusHandler(http.StatusNoContent)),
		Entry:              statusHandler(http.StatusPartialContent),
		ChromeBridge:       statusHandler(http.StatusResetContent),
		ChromeBridgeStatus: statusHandler(http.StatusAlreadyReported),
		ChromeBridgeEvents: statusHandler(http.StatusIMUsed),
	}})

	tests := []struct {
		path string
		want int
	}{
		{path: "/webhook", want: http.StatusOK},
		{path: "/webhook/telegram", want: http.StatusCreated},
		{path: "/webhook/discord", want: http.StatusAccepted},
		{path: "/webhook/slack", want: http.StatusNoContent},
		{path: "/entry", want: http.StatusPartialContent},
		{path: "/chrome/bridge", want: http.StatusResetContent},
		{path: "/chrome/bridge/status", want: http.StatusAlreadyReported},
		{path: "/chrome/bridge/events", want: http.StatusIMUsed},
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

func TestRegisterRoutesKeepsUnavailableWebhookRoutes(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Dependencies{})

	for _, path := range []string{"/webhook", "/webhook/telegram", "/webhook/discord", "/webhook/slack"} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status=%d want=%d", rec.Code, http.StatusServiceUnavailable)
			}
		})
	}
}

func statusHandler(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}
}
