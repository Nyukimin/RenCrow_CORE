package line

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewMessageSender(t *testing.T) {
	sender := NewMessageSender("test-access-token")

	if sender == nil {
		t.Fatal("NewMessageSender should not return nil")
	}
}

func TestMessageSender_SendPushMessage_Success(t *testing.T) {
	// Mock LINE API server
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request method
		if r.Method != http.MethodPost {
			t.Errorf("Expected POST request, got %s", r.Method)
		}

		// Verify authorization header
		authHeader := r.Header.Get("Authorization")
		expectedAuth := "Bearer test-token"
		if authHeader != expectedAuth {
			t.Errorf("Expected Authorization '%s', got '%s'", expectedAuth, authHeader)
		}

		// Verify content type
		contentType := r.Header.Get("Content-Type")
		if contentType != "application/json" {
			t.Errorf("Expected Content-Type 'application/json', got '%s'", contentType)
		}

		// Verify request body
		var payload map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("Failed to decode request body: %v", err)
		}

		if payload["to"] != "U123456" {
			t.Errorf("Expected 'to' field 'U123456', got '%v'", payload["to"])
		}

		messages, ok := payload["messages"].([]interface{})
		if !ok || len(messages) == 0 {
			t.Error("Expected 'messages' array to be non-empty")
		}

		// Return success
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	defer mockServer.Close()

	sender := NewMessageSender("test-token")
	sender.apiEndpoint = mockServer.URL // Override endpoint for testing

	err := sender.SendPushMessage(context.Background(), "U123456", "Hello, World!")
	if err != nil {
		t.Errorf("SendPushMessage failed: %v", err)
	}
}

func TestMessageSender_SendPushMessage_APIError(t *testing.T) {
	// Mock LINE API server returning error
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"message": "Invalid request"}`))
	}))
	defer mockServer.Close()

	sender := NewMessageSender("test-token")
	sender.apiEndpoint = mockServer.URL

	err := sender.SendPushMessage(context.Background(), "U123456", "Test message")
	if err == nil {
		t.Error("Expected error for API error response")
	}
}

func TestMessageSender_SendPushMessage_EmptyUserID(t *testing.T) {
	sender := NewMessageSender("test-token")

	err := sender.SendPushMessage(context.Background(), "", "Test message")
	if err == nil {
		t.Error("Expected error for empty user ID")
	}
}

func TestMessageSender_SendPushMessage_EmptyMessage(t *testing.T) {
	sender := NewMessageSender("test-token")

	err := sender.SendPushMessage(context.Background(), "U123456", "")
	if err == nil {
		t.Error("Expected error for empty message")
	}
}

func TestMessageSender_SendReplyMessage_Success(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("Failed to decode request body: %v", err)
		}

		if payload["replyToken"] != "test-reply-token" {
			t.Errorf("Expected 'replyToken' field 'test-reply-token', got '%v'", payload["replyToken"])
		}

		messages, ok := payload["messages"].([]interface{})
		if !ok || len(messages) == 0 {
			t.Error("Expected 'messages' array to be non-empty")
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	defer mockServer.Close()

	sender := NewMessageSender("test-token")
	sender.replyEndpoint = mockServer.URL

	err := sender.SendReplyMessage(context.Background(), "test-reply-token", "Reply message")
	if err != nil {
		t.Errorf("SendReplyMessage failed: %v", err)
	}
}

func TestMessageSender_SendReplyMessage_EmptyReplyToken(t *testing.T) {
	sender := NewMessageSender("test-token")

	err := sender.SendReplyMessage(context.Background(), "", "Test message")
	if err == nil {
		t.Error("Expected error for empty reply token")
	}
}

func TestBuildTextMessage(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		wantErr bool
	}{
		{
			name:    "Valid message",
			text:    "Hello, World!",
			wantErr: false,
		},
		{
			name:    "Long message",
			text:    "This is a very long message that should still work fine",
			wantErr: false,
		},
		{
			name:    "Empty message",
			text:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg, err := buildTextMessage(tt.text)
			if (err != nil) != tt.wantErr {
				t.Errorf("buildTextMessage() error = %v, wantErr %v", err, tt.wantErr)
			}

			if !tt.wantErr {
				if msg["type"] != "text" {
					t.Errorf("Expected type 'text', got '%s'", msg["type"])
				}
				if msg["text"] != tt.text {
					t.Errorf("Expected text '%s', got '%s'", tt.text, msg["text"])
				}
			}
		})
	}
}

func TestBuildTextMessageWithQuote(t *testing.T) {
	text := "Reply message"
	quoteToken := "quote-token-123"

	msg, err := buildTextMessageWithQuote(text, quoteToken)
	if err != nil {
		t.Fatalf("buildTextMessageWithQuote() failed: %v", err)
	}

	if msg["type"] != "text" {
		t.Errorf("Expected type 'text', got '%s'", msg["type"])
	}
	if msg["text"] != text {
		t.Errorf("Expected text '%s', got '%s'", text, msg["text"])
	}
	if msg["quoteToken"] != quoteToken {
		t.Errorf("Expected quoteToken '%s', got '%v'", quoteToken, msg["quoteToken"])
	}
}

func TestBuildImageMessage(t *testing.T) {
	originalURL := "https://example.com/original.jpg"
	previewURL := "https://example.com/preview.jpg"

	msg, err := buildImageMessage(originalURL, previewURL)
	if err != nil {
		t.Fatalf("buildImageMessage() failed: %v", err)
	}

	if msg["type"] != "image" {
		t.Errorf("Expected type 'image', got '%s'", msg["type"])
	}
	if msg["originalContentUrl"] != originalURL {
		t.Errorf("Expected originalContentUrl '%s', got '%v'", originalURL, msg["originalContentUrl"])
	}
	if msg["previewImageUrl"] != previewURL {
		t.Errorf("Expected previewImageUrl '%s', got '%v'", previewURL, msg["previewImageUrl"])
	}
}

func TestBuildImageMessage_EmptyURL(t *testing.T) {
	_, err := buildImageMessage("", "https://example.com/preview.jpg")
	if err == nil {
		t.Error("Expected error for empty originalURL")
	}

	_, err = buildImageMessage("https://example.com/original.jpg", "")
	if err == nil {
		t.Error("Expected error for empty previewURL")
	}
}

func TestMessageSender_SendPushMessageWithImage(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]interface{}
		json.NewDecoder(r.Body).Decode(&payload)

		messages, ok := payload["messages"].([]interface{})
		if !ok || len(messages) == 0 {
			t.Error("Expected messages array")
		}

		msg := messages[0].(map[string]interface{})
		if msg["type"] != "image" {
			t.Errorf("Expected message type 'image', got '%v'", msg["type"])
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	defer mockServer.Close()

	sender := NewMessageSender("test-token")
	sender.apiEndpoint = mockServer.URL

	err := sender.SendPushMessageWithImage(context.Background(), "U123456", "https://example.com/image.jpg", "https://example.com/preview.jpg")
	if err != nil {
		t.Errorf("SendPushMessageWithImage failed: %v", err)
	}
}

func TestMessageSender_SendReplyMessageWithQuote(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]interface{}
		json.NewDecoder(r.Body).Decode(&payload)

		messages, ok := payload["messages"].([]interface{})
		if !ok || len(messages) == 0 {
			t.Error("Expected messages array")
		}

		msg := messages[0].(map[string]interface{})
		if msg["quoteToken"] == nil {
			t.Error("Expected quoteToken in message")
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	defer mockServer.Close()

	sender := NewMessageSender("test-token")
	sender.replyEndpoint = mockServer.URL

	err := sender.SendReplyMessageWithQuote(context.Background(), "reply-token-123", "Reply text", "quote-token-123")
	if err != nil {
		t.Errorf("SendReplyMessageWithQuote failed: %v", err)
	}
}
