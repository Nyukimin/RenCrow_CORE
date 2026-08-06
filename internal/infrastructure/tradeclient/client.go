package tradeclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

const maxResponseBytes = 1 << 20

type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

type ServiceError struct {
	StatusCode int
}

func (err *ServiceError) Error() string {
	return fmt.Sprintf("TRADE private API returned HTTP %d", err.StatusCode)
}

func NewClient(baseURL, tokenFile string, timeout time.Duration) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || parsed.Scheme != "http" {
		return nil, fmt.Errorf("TRADE base URL must be an absolute HTTP URL")
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("TRADE timeout must be positive")
	}
	token, err := readTokenFile(tokenFile)
	if err != nil {
		return nil, fmt.Errorf("read TRADE control token: %w", err)
	}
	return &Client{baseURL: baseURL, token: token, httpClient: &http.Client{Timeout: timeout}}, nil
}

func (client *Client) Status(ctx context.Context, correlationID string) (moduletrade.PrivateStatus, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.baseURL+"/v1/status", nil)
	if err != nil {
		return moduletrade.PrivateStatus{}, fmt.Errorf("create TRADE status request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+client.token)
	if strings.TrimSpace(correlationID) != "" {
		request.Header.Set("X-Correlation-ID", correlationID)
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return moduletrade.PrivateStatus{}, fmt.Errorf("TRADE status request failed: %w", err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return moduletrade.PrivateStatus{}, fmt.Errorf("read TRADE status response: %w", err)
	}
	if len(payload) > maxResponseBytes {
		return moduletrade.PrivateStatus{}, fmt.Errorf("TRADE status response exceeds %d bytes", maxResponseBytes)
	}
	if response.StatusCode != http.StatusOK {
		return moduletrade.PrivateStatus{}, &ServiceError{StatusCode: response.StatusCode}
	}
	var status moduletrade.PrivateStatus
	if err := json.Unmarshal(payload, &status); err != nil {
		return moduletrade.PrivateStatus{}, fmt.Errorf("decode TRADE status response: %w", err)
	}
	if err := status.ValidateDisabledFoundation(); err != nil {
		return moduletrade.PrivateStatus{}, fmt.Errorf("validate TRADE status response: %w", err)
	}
	return status, nil
}

func readTokenFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("token path must reference a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("token file must be owner-only")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(payload))
	if len(token) < 32 || strings.ContainsAny(token, " \t\r\n") {
		return "", errors.New("token must be a single value of at least 32 bytes")
	}
	return token, nil
}
