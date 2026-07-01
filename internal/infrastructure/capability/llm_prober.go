package capability

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/capability"
)

const probeTimeout = 5 * time.Second

// defaultQuality は llm_quality_overrides に記載のないモデルのデフォルト品質ランク
var defaultQuality = map[string]int{
	"ollama":   2,
	"claude":   5,
	"openai":   4,
	"deepseek": 3,
}

// ollamaTagsResponse は Ollama /api/tags レスポンスの一部
type ollamaTagsResponse struct {
	Models []ollamaModelInfo `json:"models"`
}

type ollamaModelInfo struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"` // bytes
	Details struct {
		ParameterSize string `json:"parameter_size"`
	} `json:"details"`
}

// ProbeOllama は Ollama の /api/tags を呼び出してモデル一覧を取得する。
// qualityMap に記載がないモデルはデフォルト品質（2）を使用する。
func ProbeOllama(ctx context.Context, baseURL string, qualityMap map[string]int) ([]capability.LLMCapability, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/tags", nil)
	if err != nil {
		return nil, fmt.Errorf("ollama probe request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// 疎通失敗は利用不可として返す（エラーではない）
		return []capability.LLMCapability{
			{ProviderName: "ollama", Available: false},
		}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return []capability.LLMCapability{
			{ProviderName: "ollama", Available: false},
		}, nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ollama probe read body: %w", err)
	}

	var tagsResp ollamaTagsResponse
	if err := json.Unmarshal(body, &tagsResp); err != nil {
		return nil, fmt.Errorf("ollama probe parse: %w", err)
	}

	caps := make([]capability.LLMCapability, 0, len(tagsResp.Models))
	for _, m := range tagsResp.Models {
		q := qualityMap[m.Name]
		if q == 0 {
			q = defaultQuality["ollama"]
		}
		caps = append(caps, capability.LLMCapability{
			ProviderName: "ollama",
			ModelName:    m.Name,
			MaxMemoryMB:  uint64(m.Size) / (1024 * 1024),
			Available:    true,
			Quality:      q,
		})
	}

	if len(caps) == 0 {
		// Ollama は動いているがモデルがない
		caps = append(caps, capability.LLMCapability{
			ProviderName: "ollama",
			Available:    false,
		})
	}

	return caps, nil
}

// ProbeAPIProvider は API キーの有無でプロバイダーの利用可否を判定する。
// テスト呼び出しは行わない（コスト・遅延を避けるため）。
func ProbeAPIProvider(providerName, apiKey, model string, quality int) capability.LLMCapability {
	available := apiKey != ""
	if quality == 0 {
		quality = defaultQuality[providerName]
	}
	return capability.LLMCapability{
		ProviderName: providerName,
		ModelName:    model,
		Available:    available,
		Quality:      quality,
	}
}
