package line

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	linePushAPIEndpoint  = "https://api.line.me/v2/bot/message/push"
	lineReplyAPIEndpoint = "https://api.line.me/v2/bot/message/reply"
)

// MessageSender sends messages to LINE users
type MessageSender struct {
	accessToken   string
	apiEndpoint   string // Push API endpoint (can be overridden for testing)
	replyEndpoint string // Reply API endpoint (can be overridden for testing)
	httpClient    *http.Client
}

// NewMessageSender creates a new MessageSender
func NewMessageSender(accessToken string) *MessageSender {
	return &MessageSender{
		accessToken:   accessToken,
		apiEndpoint:   linePushAPIEndpoint,
		replyEndpoint: lineReplyAPIEndpoint,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// SendPushMessage sends a push message to a LINE user
func (s *MessageSender) SendPushMessage(ctx context.Context, userID, message string) error {
	if userID == "" {
		return fmt.Errorf("userID cannot be empty")
	}
	if message == "" {
		return fmt.Errorf("message cannot be empty")
	}

	textMsg, err := buildTextMessage(message)
	if err != nil {
		return fmt.Errorf("failed to build text message: %w", err)
	}

	payload := map[string]interface{}{
		"to":       userID,
		"messages": []map[string]interface{}{textMsg},
	}

	return s.callAPI(ctx, s.apiEndpoint, payload)
}

// SendReplyMessage sends a reply message using a reply token
func (s *MessageSender) SendReplyMessage(ctx context.Context, replyToken, message string) error {
	if replyToken == "" {
		return fmt.Errorf("replyToken cannot be empty")
	}
	if message == "" {
		return fmt.Errorf("message cannot be empty")
	}

	textMsg, err := buildTextMessage(message)
	if err != nil {
		return fmt.Errorf("failed to build text message: %w", err)
	}

	payload := map[string]interface{}{
		"replyToken": replyToken,
		"messages":   []map[string]interface{}{textMsg},
	}

	return s.callAPI(ctx, s.replyEndpoint, payload)
}

// SendReplyMessageWithQuote sends a reply message with quote token
func (s *MessageSender) SendReplyMessageWithQuote(ctx context.Context, replyToken, message, quoteToken string) error {
	if replyToken == "" {
		return fmt.Errorf("replyToken cannot be empty")
	}
	if message == "" {
		return fmt.Errorf("message cannot be empty")
	}

	textMsg, err := buildTextMessageWithQuote(message, quoteToken)
	if err != nil {
		return fmt.Errorf("failed to build text message with quote: %w", err)
	}

	payload := map[string]interface{}{
		"replyToken": replyToken,
		"messages":   []map[string]interface{}{textMsg},
	}

	return s.callAPI(ctx, s.replyEndpoint, payload)
}

// SendPushMessageWithImage sends a push message with an image
func (s *MessageSender) SendPushMessageWithImage(ctx context.Context, userID, originalURL, previewURL string) error {
	if userID == "" {
		return fmt.Errorf("userID cannot be empty")
	}

	imageMsg, err := buildImageMessage(originalURL, previewURL)
	if err != nil {
		return fmt.Errorf("failed to build image message: %w", err)
	}

	payload := map[string]interface{}{
		"to":       userID,
		"messages": []map[string]interface{}{imageMsg},
	}

	return s.callAPI(ctx, s.apiEndpoint, payload)
}

// callAPI makes an authenticated HTTP request to LINE Messaging API
func (s *MessageSender) callAPI(ctx context.Context, endpoint string, payload interface{}) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.accessToken)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("LINE API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// buildTextMessage creates a LINE text message object
func buildTextMessage(text string) (map[string]interface{}, error) {
	if text == "" {
		return nil, fmt.Errorf("text cannot be empty")
	}

	return map[string]interface{}{
		"type": "text",
		"text": text,
	}, nil
}

// buildTextMessageWithQuote creates a LINE text message with quote token
func buildTextMessageWithQuote(text, quoteToken string) (map[string]interface{}, error) {
	if text == "" {
		return nil, fmt.Errorf("text cannot be empty")
	}

	msg := map[string]interface{}{
		"type": "text",
		"text": text,
	}

	if quoteToken != "" {
		msg["quoteToken"] = quoteToken
	}

	return msg, nil
}

// buildImageMessage creates a LINE image message object
func buildImageMessage(originalURL, previewURL string) (map[string]interface{}, error) {
	if originalURL == "" {
		return nil, fmt.Errorf("originalURL cannot be empty")
	}
	if previewURL == "" {
		return nil, fmt.Errorf("previewURL cannot be empty")
	}

	return map[string]interface{}{
		"type":              "image",
		"originalContentUrl": originalURL,
		"previewImageUrl":    previewURL,
	}, nil
}
