package line

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	lineContentEndpoint = "https://api-data.line.me/v2/bot/message/%s/content"
)

// MediaDownloader downloads media content from LINE
type MediaDownloader struct {
	accessToken     string
	contentEndpoint string // Can be overridden for testing
	httpClient      *http.Client
}

// NewMediaDownloader creates a new MediaDownloader
func NewMediaDownloader(accessToken string) *MediaDownloader {
	return &MediaDownloader{
		accessToken:     accessToken,
		contentEndpoint: lineContentEndpoint,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// DownloadContent downloads media content by message ID
func (d *MediaDownloader) DownloadContent(ctx context.Context, messageID string) ([]byte, error) {
	if messageID == "" {
		return nil, fmt.Errorf("messageID cannot be empty")
	}

	endpoint := fmt.Sprintf(d.contentEndpoint, messageID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+d.accessToken)

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if text := strings.TrimSpace(string(respBody)); text != "" {
			return nil, fmt.Errorf("LINE API error (status %d): %s", resp.StatusCode, text)
		}
		return nil, fmt.Errorf("LINE API error (status %d)", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	return data, nil
}

// isBotMention checks if the bot is mentioned in group/room chat
func isBotMention(sourceType string, mentionees []Mentionee, botUserID string) bool {
	// User chat - always process
	if sourceType == "user" {
		return true
	}

	// Group/Room chat - check if bot is mentioned
	for _, mention := range mentionees {
		if mention.UserID == botUserID {
			return true
		}
	}

	return false
}

// extractQuoteToken extracts quote token from webhook event
func extractQuoteToken(event WebhookEvent) string {
	return event.Message.QuoteToken
}
