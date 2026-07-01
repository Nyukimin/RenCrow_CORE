package capability

import (
	"context"
	"fmt"
	"maps"
	"os"
	"runtime"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/capability"
)

// coderProbeTarget は API プロバイダー1件の疎通確認情報
type coderProbeTarget struct {
	providerName string
	apiKey       string
	model        string
	quality      int
}

// CapabilityDetector は起動時のケイパビリティ検出を担当する
type CapabilityDetector struct {
	ollamaBaseURL string
	coders        []coderProbeTarget
	qualityMap    map[string]int
	toolRegistry  capability.ToolRegistry // Phase 2: nil の場合は Tools を空にする
}

// NewCapabilityDetector は設定から CapabilityDetector を構築する
func NewCapabilityDetector(cfg *config.Config) *CapabilityDetector {
	qualityMap := make(map[string]int)
	maps.Copy(qualityMap, cfg.Capability.LLMQualityOverrides)

	coders := []coderProbeTarget{
		{providerName: "claude", apiKey: cfg.Claude.APIKey, model: cfg.Claude.Model, quality: qualityMap["claude"]},
		{providerName: "openai", apiKey: cfg.OpenAI.APIKey, model: cfg.OpenAI.Model, quality: qualityMap["openai"]},
		{providerName: "deepseek", apiKey: cfg.DeepSeek.APIKey, model: cfg.DeepSeek.Model, quality: qualityMap["deepseek"]},
	}

	return &CapabilityDetector{
		ollamaBaseURL: cfg.Ollama.BaseURL,
		coders:        coders,
		qualityMap:    qualityMap,
	}
}

// WithToolRegistry は ToolRegistry を設定する（Builder パターン）
func (d *CapabilityDetector) WithToolRegistry(registry capability.ToolRegistry) *CapabilityDetector {
	d.toolRegistry = registry
	return d
}

// Detect はこのノードのケイパビリティを検出して返す
func (d *CapabilityDetector) Detect(ctx context.Context) (capability.NodeCapabilities, error) {
	hostname, _ := os.Hostname()
	nodeID := fmt.Sprintf("%s/%d", hostname, os.Getpid())

	totalMB, availMB := readMemoryInfo()

	var llms []capability.LLMCapability

	// Ollama の疎通確認
	if d.ollamaBaseURL != "" {
		ollamaLLMs, err := ProbeOllama(ctx, d.ollamaBaseURL, d.qualityMap)
		if err == nil {
			llms = append(llms, ollamaLLMs...)
		}
	}

	// API プロバイダーの疎通確認（キーの有無のみ）
	for _, c := range d.coders {
		llms = append(llms, ProbeAPIProvider(c.providerName, c.apiKey, c.model, c.quality))
	}

	// ToolRegistry からこのプラットフォームで使えるツールを取得
	var tools []capability.ToolCapability
	if d.toolRegistry != nil {
		entries, err := d.toolRegistry.ListForPlatform(ctx, runtime.GOOS)
		if err == nil {
			tools = make([]capability.ToolCapability, 0, len(entries))
			for _, e := range entries {
				tools = append(tools, capability.ToolCapability{
					Name:      e.Name,
					Platforms: e.Platforms,
					Source:    string(e.Source),
				})
			}
		}
	}

	return capability.NodeCapabilities{
		NodeID: nodeID,
		Platform: capability.PlatformInfo{
			OS:   runtime.GOOS,
			Arch: runtime.GOARCH,
		},
		Memory: capability.MemoryInfo{
			TotalMB:     totalMB,
			AvailableMB: availMB,
		},
		LLMs:  llms,
		Tools: tools,
	}, nil
}
