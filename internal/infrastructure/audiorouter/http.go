package audiorouter

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type HTTPDownloader struct {
	client *http.Client
}

func NewHTTPDownloader(timeout time.Duration) *HTTPDownloader {
	return &HTTPDownloader{client: &http.Client{Timeout: timeout}}
}

func (d *HTTPDownloader) Download(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if text := strings.TrimSpace(string(respBody)); text != "" {
			return nil, fmt.Errorf("bad status: %d: %s", resp.StatusCode, text)
		}
		return nil, fmt.Errorf("bad status: %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

type SSEClientConfig struct {
	URL            string
	ConnectTimeout time.Duration
	RetryDelay     time.Duration
	OnConnect      func()
	OnDisconnect   func(error)
}

type SSEClient struct {
	cfg    SSEClientConfig
	client *http.Client
}

func NewSSEClient(cfg SSEClientConfig) *SSEClient {
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = 5 * time.Second
	}
	if cfg.RetryDelay <= 0 {
		cfg.RetryDelay = 2 * time.Second
	}
	return &SSEClient{
		cfg:    cfg,
		client: &http.Client{Timeout: cfg.ConnectTimeout},
	}
}

func (c *SSEClient) Run(ctx context.Context, handler func(id int64, ev Event) error) error {
	var lastEventID string
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		nextID, err := c.consumeOnce(ctx, lastEventID, handler)
		if c.cfg.OnDisconnect != nil {
			c.cfg.OnDisconnect(err)
		}
		if err == nil {
			return nil
		}
		if nextID != "" {
			lastEventID = nextID
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(c.cfg.RetryDelay):
		}
	}
}

func (c *SSEClient) consumeOnce(ctx context.Context, lastEventID string, handler func(id int64, ev Event) error) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.URL, nil)
	if err != nil {
		return lastEventID, err
	}
	req.Header.Set("Accept", "text/event-stream")
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return lastEventID, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if text := strings.TrimSpace(string(respBody)); text != "" {
			return lastEventID, fmt.Errorf("unexpected status: %d: %s", resp.StatusCode, text)
		}
		return lastEventID, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}
	if c.cfg.OnConnect != nil {
		c.cfg.OnConnect()
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var eventID string
	var dataLines []string
	flush := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		var ev Event
		if err := json.Unmarshal([]byte(strings.Join(dataLines, "\n")), &ev); err != nil {
			dataLines = dataLines[:0]
			return nil
		}
		id, _ := strconv.ParseInt(strings.TrimSpace(eventID), 10, 64)
		if err := handler(id, ev); err != nil {
			return err
		}
		if eventID != "" {
			lastEventID = eventID
		}
		dataLines = dataLines[:0]
		return nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				return lastEventID, err
			}
			continue
		}
		switch {
		case strings.HasPrefix(line, "id:"):
			eventID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := flush(); err != nil {
		return lastEventID, err
	}
	if err := scanner.Err(); err != nil {
		return lastEventID, err
	}
	return lastEventID, io.EOF
}
