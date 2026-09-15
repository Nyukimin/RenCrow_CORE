package llmfit

import (
	"strings"
	"time"

	domainllmops "github.com/Nyukimin/RenCrow_CORE/internal/domain/llmops"
)

// Mapping is one-way: llmfit DTO -> RenCrow domain. There is no reverse path.
//
// Null / missing handling:
//   - pointer DTO fields (best_quant, estimated_tps, measured_tps, prefill_tps,
//     ttft_ms, disk_size_gb, license): JSON null or absent -> nil / "";
//     a reported 0 -> &0.
//   - scalar DTO fields: absent -> zero value, kept as-is.
//   - gpus[]: elements without a name and without vram are dropped; when no
//     usable element remains, one GPUProfile is assembled from the gpu_*
//     scalar fields (has_gpu / gpu_name / gpu_vram_gb / gpu_count).
//   - node.name: the configured node id wins. llmfit reports "unknown-node"
//     when it cannot resolve a hostname, and the CLI shape has no node at all.

// llmfitUnknownNodeName is what llmfit reports when it cannot name the host.
const llmfitUnknownNodeName = "unknown-node"

var (
	noteTextKeys     = []string{"message", "text", "note", "label", "name"}
	capabilityIDKeys = []string{"id", "name", "label"}
)

func mapSystem(nodeID string, resp systemResponse, collectedAt time.Time) domainllmops.NodeHardwareProfile {
	sys := resp.System
	gpus := mapGPUs(sys)
	gpuCount := sys.GPUCount
	if described := totalGPUCount(gpus); gpuCount < described {
		// llmfit reported fewer GPUs than it described; trust the described list.
		gpuCount = described
	}
	return domainllmops.NodeHardwareProfile{
		NodeID:         nodeID,
		NodeName:       resolveNodeName(nodeID, resp.Node.Name),
		OS:             strings.TrimSpace(resp.Node.OS),
		CPUName:        strings.TrimSpace(sys.CPUName),
		CPUCores:       sys.CPUCores,
		TotalRAMGB:     sys.TotalRAMGB,
		AvailableRAMGB: sys.AvailableRAMGB,
		HasGPU:         sys.HasGPU,
		GPUCount:       gpuCount,
		GPUs:           gpus,
		UnifiedMemory:  sys.UnifiedMemory,
		Backend:        strings.TrimSpace(sys.Backend),
		CollectedAt:    collectedAt,
		Source:         domainllmops.SourceLLMFit,
	}
}

// resolveNodeName prefers the configured node id (the operator's name for the
// node) over llmfit's reported host name, which is "unknown-node" in practice
// and absent in CLI output. The reported name is only used when no id exists.
func resolveNodeName(nodeID, reported string) string {
	if id := strings.TrimSpace(nodeID); id != "" {
		return id
	}
	reported = strings.TrimSpace(reported)
	if strings.EqualFold(reported, llmfitUnknownNodeName) {
		return ""
	}
	return reported
}

func mapGPUs(sys systemDTO) []domainllmops.GPUProfile {
	gpus := make([]domainllmops.GPUProfile, 0, len(sys.GPUs))
	for _, dto := range sys.GPUs {
		gpu := domainllmops.GPUProfile{
			Name:               strings.TrimSpace(dto.Name),
			VRAMGB:             derefFloat(dto.VRAMGB),
			MemoryBandwidthGBs: derefFloat(dto.MemoryBandwidthGBps),
			Count:              dto.Count,
		}
		if gpu.Name == "" && gpu.VRAMGB == 0 {
			continue
		}
		if gpu.Count <= 0 {
			gpu.Count = 1
		}
		gpus = append(gpus, gpu)
	}
	if len(gpus) == 0 && (sys.HasGPU || sys.GPUName != nil || sys.GPUVRAMGB != nil) {
		gpu := domainllmops.GPUProfile{Count: sys.GPUCount}
		if sys.GPUName != nil {
			gpu.Name = strings.TrimSpace(*sys.GPUName)
		}
		if sys.GPUVRAMGB != nil {
			gpu.VRAMGB = *sys.GPUVRAMGB
		}
		if gpu.Count <= 0 {
			gpu.Count = 1
		}
		if gpu.Name != "" || gpu.VRAMGB > 0 {
			gpus = append(gpus, gpu)
		}
	}
	// llmfit has no per-GPU free-memory field. system.gpu_available_gb is a
	// node-wide value, so it is attributed to the GPU only when the node has
	// exactly one GPU; with several GPUs the split is unknown and stays 0
	// (RenCrow does not guess a distribution).
	if len(gpus) == 1 && gpus[0].Count == 1 && sys.GPUAvailableGB != nil {
		gpus[0].AvailableVRAMGB = *sys.GPUAvailableGB
	}
	return gpus
}

func totalGPUCount(gpus []domainllmops.GPUProfile) int {
	total := 0
	for _, gpu := range gpus {
		total += gpu.Count
	}
	return total
}

// mapModels converts the models payload. Elements without a model name are
// skipped (partial response); the number skipped is returned for logging.
func mapModels(nodeID string, resp modelsResponse, collectedAt time.Time) ([]domainllmops.ModelFitAssessment, int) {
	models := make([]domainllmops.ModelFitAssessment, 0, len(resp.Models))
	skipped := 0
	for _, dto := range resp.Models {
		model, ok := mapModel(nodeID, dto, collectedAt)
		if !ok {
			skipped++
			continue
		}
		models = append(models, model)
	}
	return models, skipped
}

func mapModel(nodeID string, dto modelDTO, collectedAt time.Time) (domainllmops.ModelFitAssessment, bool) {
	name := strings.TrimSpace(dto.Name)
	if name == "" {
		return domainllmops.ModelFitAssessment{}, false
	}
	capabilities := dto.CapabilityIDs
	if len(capabilities) == 0 {
		capabilities = stringsFromAny(dto.Capabilities, capabilityIDKeys)
	}
	if capabilities == nil {
		capabilities = []string{}
	}
	return domainllmops.ModelFitAssessment{
		NodeID:                 nodeID,
		ModelID:                name,
		Provider:               strings.TrimSpace(dto.Provider),
		ParameterCount:         strings.TrimSpace(dto.ParameterCount),
		ParamsB:                dto.ParamsB,
		IsMoE:                  dto.IsMoE,
		FitLevel:               domainllmops.FitLevel(strings.TrimSpace(dto.FitLevel)),
		Score:                  dto.Score,
		QualityScore:           dto.ScoreComponents.Quality,
		SpeedScore:             dto.ScoreComponents.Speed,
		FitScore:               dto.ScoreComponents.Fit,
		ContextScore:           dto.ScoreComponents.Context,
		Runtime:                strings.TrimSpace(dto.Runtime),
		RunMode:                strings.TrimSpace(dto.RunMode),
		BestQuant:              copyStringPtr(dto.BestQuant),
		ContextLength:          dto.ContextLength,          // Native
		UsableContext:          dto.UsableContext,          // Usable
		EffectiveContextLength: dto.EffectiveContextLength, // Evaluated
		MemoryRequiredGB:       dto.MemoryRequiredGB,
		MemoryAvailableGB:      dto.MemoryAvailableGB,
		UtilizationPct:         dto.UtilizationPct,
		EstimatedTPS:           copyFloatPtr(dto.EstimatedTPS),
		MeasuredTPS:            copyFloatPtr(dto.MeasuredTPS),
		PrefillTPS:             copyFloatPtr(dto.PrefillTPS),
		TTFTMs:                 copyFloatPtr(dto.TTFTMs),
		EstimateConfidence:     domainllmops.EstimateConfidence(strings.TrimSpace(dto.EstimateConfidence)),
		Installed:              dto.Installed,
		DiskSizeGB:             copyFloatPtr(dto.DiskSizeGB),
		Capabilities:           capabilities,
		License:                derefString(dto.License),
		Notes:                  stringsFromAny(dto.Notes, noteTextKeys),
		CollectedAt:            collectedAt,
	}, true
}

func copyStringPtr(p *string) *string {
	if p == nil {
		return nil
	}
	v := strings.TrimSpace(*p)
	return &v
}

func copyFloatPtr(p *float64) *float64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func derefFloat(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}

// stringsFromAny reduces a loosely decoded array to strings. String elements
// are kept; object elements contribute the first non-empty value among keys.
// The result is never nil so JSON output stays an array.
func stringsFromAny(items []any, keys []string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		switch v := item.(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				out = append(out, s)
			}
		case map[string]any:
			if s := lookupString(v, keys); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func lookupString(obj map[string]any, keys []string) string {
	for _, key := range keys {
		if raw, ok := obj[key]; ok {
			if s, ok := raw.(string); ok {
				if s = strings.TrimSpace(s); s != "" {
					return s
				}
			}
		}
	}
	return ""
}
