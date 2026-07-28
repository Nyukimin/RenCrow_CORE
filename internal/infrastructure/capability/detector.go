package capability

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
)

// CapabilityDetector discovers logical RenCrow_LLM aliases and local tools.
// Physical LLM endpoints are intentionally outside CORE.
type CapabilityDetector struct {
	gatewayBaseURL string
	gatewayAPIKey  string
	qualityMap     map[string]int
	toolRegistry   capability.ToolRegistry
}

func NewCapabilityDetector(cfg *config.Config) *CapabilityDetector {
	detector := &CapabilityDetector{qualityMap: map[string]int{}}
	if cfg == nil {
		return detector
	}
	detector.gatewayBaseURL = strings.TrimRight(strings.TrimSpace(cfg.LLMGateway.BaseURL), "/")
	if detector.gatewayBaseURL == "" {
		detector.gatewayBaseURL = "http://127.0.0.1:8090"
	}
	if envName := strings.TrimSpace(cfg.LLMGateway.APIKeyEnv); envName != "" {
		detector.gatewayAPIKey = strings.TrimSpace(os.Getenv(envName))
	}
	for key, value := range cfg.Capability.LLMQualityOverrides {
		detector.qualityMap[key] = value
	}
	return detector
}

func (d *CapabilityDetector) WithToolRegistry(registry capability.ToolRegistry) *CapabilityDetector {
	d.toolRegistry = registry
	return d
}

func (d *CapabilityDetector) Detect(ctx context.Context) (capability.NodeCapabilities, error) {
	hostname, _ := os.Hostname()
	totalMB, availMB := readMemoryInfo()

	llms, err := ProbeRenCrowLLM(ctx, d.gatewayBaseURL, d.gatewayAPIKey, d.qualityMap)
	if err != nil {
		return capability.NodeCapabilities{}, err
	}

	var tools []capability.ToolCapability
	if d.toolRegistry != nil {
		entries, listErr := d.toolRegistry.ListForPlatform(ctx, runtime.GOOS)
		if listErr == nil {
			tools = make([]capability.ToolCapability, 0, len(entries))
			for _, entry := range entries {
				tools = append(tools, capability.ToolCapability{
					Name: entry.Name, Platforms: entry.Platforms, Source: string(entry.Source),
				})
			}
		}
	}

	return capability.NodeCapabilities{
		NodeID:   fmt.Sprintf("%s/%d", hostname, os.Getpid()),
		Platform: capability.PlatformInfo{OS: runtime.GOOS, Arch: runtime.GOARCH},
		Memory:   capability.MemoryInfo{TotalMB: totalMB, AvailableMB: availMB},
		LLMs:     llms,
		Tools:    tools,
	}, nil
}
