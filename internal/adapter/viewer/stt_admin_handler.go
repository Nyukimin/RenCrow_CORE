package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type STTAdminOptions struct {
	BaseURL      string
	Client       *http.Client
	InitialDelay time.Duration
	PollInterval time.Duration
	MaxWait      time.Duration
}

type sttHealthPayload struct {
	OK     bool   `json:"ok"`
	Status string `json:"status"`
	Ready  struct {
		ModelLoaded bool `json:"model_loaded"`
	} `json:"ready"`
}

func HandleSTTRestart(opts STTAdminOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		baseURL := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
		if baseURL == "" {
			http.Error(w, "stt base url is not configured", http.StatusServiceUnavailable)
			return
		}
		client := opts.Client
		if client == nil {
			client = &http.Client{Timeout: 3 * time.Second}
		}
		initialDelay := opts.InitialDelay
		if initialDelay <= 0 {
			initialDelay = 1500 * time.Millisecond
		}
		pollInterval := opts.PollInterval
		if pollInterval <= 0 {
			pollInterval = 1 * time.Second
		}
		maxWait := opts.MaxWait
		if maxWait <= 0 {
			maxWait = 30 * time.Second
		}

		restartBody, restartStatus, err := postSTTRestart(r.Context(), client, baseURL)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if restartStatus != http.StatusAccepted {
			http.Error(w, fmt.Sprintf("stt restart failed: HTTP %d: %s", restartStatus, restartBody), http.StatusBadGateway)
			return
		}

		timer := time.NewTimer(initialDelay)
		select {
		case <-r.Context().Done():
			timer.Stop()
			return
		case <-timer.C:
		}

		started := time.Now()
		healthBody, err := waitSTTReady(r.Context(), client, baseURL, pollInterval, maxWait)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusGatewayTimeout)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":             false,
				"status":         "restart_wait_timeout",
				"service":        "stt-gateway",
				"restart_status": restartStatus,
				"restart_body":   restartBody,
				"last_error":     err.Error(),
				"wait_ms":        time.Since(started).Milliseconds(),
			})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":             true,
			"status":         "ready",
			"service":        "stt-gateway",
			"restart_status": restartStatus,
			"restart_body":   restartBody,
			"health_body":    healthBody,
			"wait_ms":        time.Since(started).Milliseconds(),
		})
	}
}

func postSTTRestart(ctx context.Context, client *http.Client, baseURL string) (string, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/admin/restart", bytes.NewReader(nil))
	if err != nil {
		return "", 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("stt restart unavailable: %w", err)
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return strings.TrimSpace(string(bodyBytes)), resp.StatusCode, nil
}

func waitSTTReady(ctx context.Context, client *http.Client, baseURL string, pollInterval, maxWait time.Duration) (string, error) {
	deadline := time.Now().Add(maxWait)
	var lastErr string
	for {
		body, ok, err := fetchSTTHealth(client, baseURL+"/health")
		if err == nil && ok {
			return body, nil
		}
		if err != nil {
			lastErr = err.Error()
		} else {
			lastErr = body
		}
		if time.Now().After(deadline) {
			if strings.TrimSpace(lastErr) == "" {
				lastErr = "stt health did not become ready"
			}
			return "", errors.New(lastErr)
		}
		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
	}
}

func fetchSTTHealth(client *http.Client, endpoint string) (string, bool, error) {
	body, httpOK, err := fetchEndpoint(client, endpoint)
	if err != nil {
		return body, false, err
	}
	return body, httpOK && isSTTHealthReady(body), nil
}

func isSTTHealthReady(body string) bool {
	var payload sttHealthPayload
	if err := json.Unmarshal([]byte(strings.TrimSpace(body)), &payload); err != nil {
		return false
	}
	return payload.OK && strings.EqualFold(strings.TrimSpace(payload.Status), "ready") && payload.Ready.ModelLoaded
}
