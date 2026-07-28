package capability

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
)

const probeTimeout = 5 * time.Second

func ProbeRenCrowLLM(ctx context.Context, baseURL, apiKey string, qualityMap map[string]int) ([]capability.LLMCapability, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/v1/models", nil)
	if err != nil {
		return nil, fmt.Errorf("RenCrow_LLM probe request: %w", err)
	}
	if token := strings.TrimSpace(apiKey); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return []capability.LLMCapability{{ProviderName: "rencrow_llm", Available: false}}, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return []capability.LLMCapability{{ProviderName: "rencrow_llm", Available: false}}, nil
	}

	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("RenCrow_LLM probe parse: %w", err)
	}
	caps := make([]capability.LLMCapability, 0, len(payload.Data))
	for _, model := range payload.Data {
		alias := strings.TrimSpace(model.ID)
		if alias == "" {
			continue
		}
		quality := qualityMap[alias]
		if quality == 0 {
			quality = qualityMap["rencrow_llm"]
		}
		if quality == 0 {
			quality = 3
		}
		caps = append(caps, capability.LLMCapability{
			ProviderName: "rencrow_llm",
			ModelName:    alias,
			Available:    true,
			Quality:      quality,
		})
	}
	if len(caps) == 0 {
		return []capability.LLMCapability{{ProviderName: "rencrow_llm", Available: false}}, nil
	}
	return caps, nil
}
