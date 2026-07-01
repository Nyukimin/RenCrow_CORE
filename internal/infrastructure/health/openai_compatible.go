package health

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	domainhealth "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/health"
)

// OpenAICompatibleChatCheck verifies an OpenAI-compatible local LLM endpoint.
// It intentionally uses the lightweight /v1/models readiness path for normal
// /health checks. Heavy inference probes belong in diagnostics, not liveness.
type OpenAICompatibleChatCheck struct {
	role    string
	baseURL string
	model   string
	apiKey  string
	client  *http.Client
}

// NewOpenAICompatibleChatCheck creates a lightweight health check for local OpenAI-compatible LLMs.
func NewOpenAICompatibleChatCheck(role, baseURL, model, apiKey string, timeout time.Duration) *OpenAICompatibleChatCheck {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	return &OpenAICompatibleChatCheck{
		role:    strings.TrimSpace(role),
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		model:   strings.TrimSpace(model),
		apiKey:  strings.TrimSpace(apiKey),
		client:  &http.Client{Timeout: timeout},
	}
}

func (c *OpenAICompatibleChatCheck) Name() string {
	role := strings.ToLower(strings.TrimSpace(c.role))
	if role == "" {
		role = "llm"
	}
	return fmt.Sprintf("local_llm_%s", role)
}

func (c *OpenAICompatibleChatCheck) Run(ctx context.Context) domainhealth.CheckResult {
	start := time.Now()
	if c.baseURL == "" {
		return domainhealth.CheckResult{Name: c.Name(), Status: domainhealth.StatusDown, Message: "base_url is empty", Duration: time.Since(start)}
	}
	if c.model == "" {
		return domainhealth.CheckResult{Name: c.Name(), Status: domainhealth.StatusDown, Message: "model is empty", Duration: time.Since(start)}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/models", nil)
	if err != nil {
		return domainhealth.CheckResult{Name: c.Name(), Status: domainhealth.StatusDown, Message: fmt.Sprintf("request creation failed: %v", err), Duration: time.Since(start)}
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return domainhealth.CheckResult{Name: c.Name(), Status: domainhealth.StatusDown, Message: fmt.Sprintf("connection failed: %v", err), Duration: time.Since(start)}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return domainhealth.CheckResult{Name: c.Name(), Status: domainhealth.StatusDown, Message: fmt.Sprintf("unexpected status %d: %s", resp.StatusCode, string(respBody)), Duration: time.Since(start)}
	}

	var parsed struct {
		Data []struct {
			ID           string `json:"id"`
			BackendModel string `json:"backend_model"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return domainhealth.CheckResult{Name: c.Name(), Status: domainhealth.StatusDown, Message: fmt.Sprintf("decode error: %v", err), Duration: time.Since(start)}
	}
	if len(parsed.Data) == 0 {
		return domainhealth.CheckResult{Name: c.Name(), Status: domainhealth.StatusDegraded, Message: fmt.Sprintf("%s endpoint live but returned no models via %s", c.model, c.baseURL), Duration: time.Since(start)}
	}
	for _, model := range parsed.Data {
		if strings.EqualFold(strings.TrimSpace(model.ID), c.model) {
			detail := strings.TrimSpace(model.BackendModel)
			if detail == "" {
				return domainhealth.CheckResult{Name: c.Name(), Status: domainhealth.StatusOK, Message: fmt.Sprintf("%s readiness ok via %s", c.model, c.baseURL), Duration: time.Since(start)}
			}
			return domainhealth.CheckResult{Name: c.Name(), Status: domainhealth.StatusOK, Message: fmt.Sprintf("%s readiness ok via %s backend=%s", c.model, c.baseURL, detail), Duration: time.Since(start)}
		}
	}

	return domainhealth.CheckResult{Name: c.Name(), Status: domainhealth.StatusDegraded, Message: fmt.Sprintf("%s endpoint live via %s but model alias is not listed", c.model, c.baseURL), Duration: time.Since(start)}
}
