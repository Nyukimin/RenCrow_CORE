// Package pronunciationtool calls the RenCrow_TTS pronunciation review Tool.
package pronunciationtool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/pronunciationcheck"
)

const maxResponseBytes = 2 * 1024 * 1024

type Client struct {
	baseURL      *url.URL
	httpClient   *http.Client
	pollInterval time.Duration
}

type latestReport struct {
	pronunciationcheck.ToolReport
	Running bool `json:"running"`
}

func NewClient(baseURL string, httpClient *http.Client, pollInterval time.Duration) *Client {
	parsed, _ := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 45 * time.Minute}
	}
	if pollInterval <= 0 {
		pollInterval = 5 * time.Second
	}
	return &Client{baseURL: parsed, httpClient: httpClient, pollInterval: pollInterval}
}

func (c *Client) Run(ctx context.Context) (pronunciationcheck.ToolReport, error) {
	if c == nil || c.baseURL == nil || c.baseURL.Scheme == "" || c.baseURL.Host == "" {
		return pronunciationcheck.ToolReport{}, errors.New("pronunciation Tool base URL is invalid")
	}
	if c.httpClient.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.httpClient.Timeout)
		defer cancel()
	}
	baseline, _ := c.latest(ctx)
	status, err := c.request(ctx, http.MethodPost, "/api/daily/run", nil)
	if err != nil {
		return pronunciationcheck.ToolReport{}, err
	}
	if status != http.StatusAccepted && status != http.StatusConflict {
		return pronunciationcheck.ToolReport{}, fmt.Errorf("pronunciation Tool start returned HTTP %d", status)
	}
	for {
		report, err := c.latest(ctx)
		if err != nil {
			return pronunciationcheck.ToolReport{}, err
		}
		if !report.Running && strings.TrimSpace(report.StartedAt) != "" && report.StartedAt != baseline.StartedAt {
			return report.ToolReport, nil
		}
		if err := sleepContext(ctx, c.pollInterval); err != nil {
			return pronunciationcheck.ToolReport{}, err
		}
	}
}

func (c *Client) Snapshot(ctx context.Context, nameMatch string) (pronunciationcheck.GPUSnapshot, error) {
	var snapshot pronunciationcheck.GPUSnapshot
	status, err := c.request(ctx, http.MethodGet, "/api/gpu/status", &snapshot)
	if err != nil {
		return pronunciationcheck.GPUSnapshot{}, err
	}
	if status != http.StatusOK {
		return pronunciationcheck.GPUSnapshot{}, fmt.Errorf("pronunciation Tool GPU status returned HTTP %d", status)
	}
	if match := strings.ToLower(strings.TrimSpace(nameMatch)); match != "" && !strings.Contains(strings.ToLower(snapshot.Name), match) {
		return pronunciationcheck.GPUSnapshot{}, fmt.Errorf("pronunciation Tool returned unexpected GPU %q", snapshot.Name)
	}
	return snapshot, nil
}

func (c *Client) latest(ctx context.Context) (latestReport, error) {
	var report latestReport
	status, err := c.request(ctx, http.MethodGet, "/api/daily/latest", &report)
	if err != nil {
		return latestReport{}, err
	}
	if status != http.StatusOK {
		return latestReport{}, fmt.Errorf("pronunciation Tool latest returned HTTP %d", status)
	}
	return report, nil
}

func (c *Client) request(ctx context.Context, method string, path string, output any) (int, error) {
	endpoint := *c.baseURL
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + path
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("pronunciation Tool request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return 0, err
	}
	if len(body) > maxResponseBytes {
		return 0, errors.New("pronunciation Tool response exceeded size limit")
	}
	if resp.StatusCode >= 400 && resp.StatusCode != http.StatusConflict {
		return resp.StatusCode, fmt.Errorf("pronunciation Tool returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if output != nil && len(body) > 0 {
		if err := json.Unmarshal(body, output); err != nil {
			return 0, fmt.Errorf("decode pronunciation Tool response: %w", err)
		}
	}
	return resp.StatusCode, nil
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
