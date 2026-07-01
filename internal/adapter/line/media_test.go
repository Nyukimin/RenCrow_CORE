package line

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewMediaDownloader(t *testing.T) {
	downloader := NewMediaDownloader("test-token")
	if downloader == nil {
		t.Fatal("NewMediaDownloader should not return nil")
	}
}

func TestMediaDownloader_DownloadContent_Success(t *testing.T) {
	expectedContent := []byte("test image data")

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify authorization
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-token" {
			t.Errorf("Expected Authorization 'Bearer test-token', got '%s'", auth)
		}

		// Verify path
		if !strings.Contains(r.URL.Path, "/123456") {
			t.Errorf("Expected path to contain message ID, got '%s'", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
		w.Write(expectedContent)
	}))
	defer mockServer.Close()

	downloader := NewMediaDownloader("test-token")
	downloader.contentEndpoint = mockServer.URL + "/%s/content"

	data, err := downloader.DownloadContent(context.Background(), "123456")
	if err != nil {
		t.Fatalf("DownloadContent failed: %v", err)
	}

	if string(data) != string(expectedContent) {
		t.Errorf("Expected data '%s', got '%s'", string(expectedContent), string(data))
	}
}

func TestMediaDownloader_DownloadContent_EmptyMessageID(t *testing.T) {
	downloader := NewMediaDownloader("test-token")

	_, err := downloader.DownloadContent(context.Background(), "")
	if err == nil {
		t.Error("Expected error for empty message ID")
	}
}

func TestMediaDownloader_DownloadContent_APIError(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message": "Not found"}`))
	}))
	defer mockServer.Close()

	downloader := NewMediaDownloader("test-token")
	downloader.contentEndpoint = mockServer.URL + "/%s/content"

	_, err := downloader.DownloadContent(context.Background(), "123456")
	if err == nil {
		t.Error("Expected error for API error response")
	}
}

func TestMediaDownloader_DownloadContent_APIErrorIncludesResponseBody(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"message":"rate limited"}`))
	}))
	defer mockServer.Close()

	downloader := NewMediaDownloader("test-token")
	downloader.contentEndpoint = mockServer.URL + "/%s/content"

	_, err := downloader.DownloadContent(context.Background(), "123456")
	if err == nil {
		t.Fatal("Expected error for API error response")
	}
	if !strings.Contains(err.Error(), "status 429") || !strings.Contains(err.Error(), `{"message":"rate limited"}`) {
		t.Fatalf("DownloadContent() error = %q, want status and response body", err.Error())
	}
}

func TestIsBotMention(t *testing.T) {
	tests := []struct {
		name       string
		sourceType string
		mentionees []Mentionee
		botUserID  string
		expected   bool
	}{
		{
			name:       "User chat (always true)",
			sourceType: "user",
			mentionees: nil,
			botUserID:  "U123",
			expected:   true,
		},
		{
			name:       "Group chat with bot mention",
			sourceType: "group",
			mentionees: []Mentionee{
				{UserID: "U456"},
				{UserID: "U123"}, // Bot ID
			},
			botUserID: "U123",
			expected:  true,
		},
		{
			name:       "Group chat without bot mention",
			sourceType: "group",
			mentionees: []Mentionee{
				{UserID: "U456"},
				{UserID: "U789"},
			},
			botUserID: "U123",
			expected:  false,
		},
		{
			name:       "Room chat with bot mention",
			sourceType: "room",
			mentionees: []Mentionee{
				{UserID: "U123"}, // Bot ID
			},
			botUserID: "U123",
			expected:  true,
		},
		{
			name:       "Room chat without bot mention",
			sourceType: "room",
			mentionees: []Mentionee{},
			botUserID:  "U123",
			expected:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isBotMention(tt.sourceType, tt.mentionees, tt.botUserID)
			if result != tt.expected {
				t.Errorf("isBotMention() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestExtractQuoteToken(t *testing.T) {
	tests := []struct {
		name     string
		event    WebhookEvent
		expected string
	}{
		{
			name: "Event with quote token",
			event: WebhookEvent{
				Message: EventMessage{
					QuoteToken: "quote-token-123",
				},
			},
			expected: "quote-token-123",
		},
		{
			name: "Event without quote token",
			event: WebhookEvent{
				Message: EventMessage{
					QuoteToken: "",
				},
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractQuoteToken(tt.event)
			if result != tt.expected {
				t.Errorf("extractQuoteToken() = '%s', expected '%s'", result, tt.expected)
			}
		})
	}
}
