package llmfit

import (
	"encoding/json"
	"io"
)

// DTOs mirror the llmfit JSON as observed on llmfit v1.1.15 (REST API.md plus
// the real responses captured in testdata/). They are internal to this
// package; the domain never sees them. Unknown keys are ignored by
// encoding/json, so schema additions on the llmfit side do not break parsing.
// Pointer fields keep "null / absent" distinguishable from a reported zero.

type nodeDTO struct {
	Name string `json:"name"`
	OS   string `json:"os"`
}

// healthResponse: {"node":{"name","os"},"status":"ok"}
type healthResponse struct {
	Status string  `json:"status"`
	Node   nodeDTO `json:"node"`
}

// systemResponse covers both shapes:
//
//	REST /api/v1/system      {"node":{"name","os"},"system":{...}}
//	CLI  llmfit system --json {"providers":{...},"system":{...}}   (no "node")
//
// "providers" (llama.cpp detection) is intentionally ignored.
type systemResponse struct {
	Node   nodeDTO   `json:"node"`
	System systemDTO `json:"system"`
}

type systemDTO struct {
	TotalRAMGB     float64  `json:"total_ram_gb"`
	AvailableRAMGB float64  `json:"available_ram_gb"`
	CPUCores       int      `json:"cpu_cores"`
	CPUName        string   `json:"cpu_name"`
	HasGPU         bool     `json:"has_gpu"`
	GPUVRAMGB      *float64 `json:"gpu_vram_gb"`
	GPUAvailableGB *float64 `json:"gpu_available_gb"` // observed null on v1.1.15
	GPUName        *string  `json:"gpu_name"`
	GPUCount       int      `json:"gpu_count"`
	UnifiedMemory  bool     `json:"unified_memory"`
	Backend        string   `json:"backend"`
	GPUs           []gpuDTO `json:"gpus"`
}

// gpuDTO is one gpus[] element as observed on v1.1.15:
// {backend, count, memory_bandwidth_gbps, name, unified_memory, vram_gb}.
// There is no per-GPU available memory field.
type gpuDTO struct {
	Backend             string   `json:"backend"`
	Count               int      `json:"count"`
	MemoryBandwidthGBps *float64 `json:"memory_bandwidth_gbps"` // observed null
	Name                string   `json:"name"`
	UnifiedMemory       bool     `json:"unified_memory"`
	VRAMGB              *float64 `json:"vram_gb"`
}

// UnmarshalJSON tolerates elements that are not objects (or objects of an
// unexpected shape): they decode to a zero gpuDTO and are dropped by the
// mapper instead of failing the whole system payload.
func (g *gpuDTO) UnmarshalJSON(data []byte) error {
	type plain gpuDTO
	var tmp plain
	if err := json.Unmarshal(data, &tmp); err != nil {
		*g = gpuDTO{}
		return nil
	}
	*g = gpuDTO(tmp)
	return nil
}

type modelsResponse struct {
	Node           nodeDTO    `json:"node"`
	TotalModels    int        `json:"total_models"`
	ReturnedModels int        `json:"returned_models"`
	Models         []modelDTO `json:"models"`
}

type scoreComponentsDTO struct {
	Quality float64 `json:"quality"`
	Speed   float64 `json:"speed"`
	Fit     float64 `json:"fit"`
	Context float64 `json:"context"`
}

// modelDTO: nullable fields observed on v1.1.15 are license, release_date,
// ollama_name, measured_tps, prefill_tps, ttft_ms, moe_offloaded_gb and the
// estimate_basis bandwidth / calibration fields. Display-only labels
// (fit_label, run_mode_label, runtime_label, estimate_confidence_label,
// category, use_case text) and estimate_basis are not mapped.
type modelDTO struct {
	Name                   string             `json:"name"`
	Provider               string             `json:"provider"`
	ParameterCount         string             `json:"parameter_count"`
	ParamsB                float64            `json:"params_b"`
	ContextLength          int                `json:"context_length"`           // Native Context
	UsableContext          int                `json:"usable_context"`           // Usable Context
	EffectiveContextLength int                `json:"effective_context_length"` // Evaluated Context
	IsMoE                  bool               `json:"is_moe"`
	FitLevel               string             `json:"fit_level"` // lower-case (perfect / good / marginal / too_tight)
	RunMode                string             `json:"run_mode"`  // lower-case (gpu / cpu / ...)
	Score                  float64            `json:"score"`
	ScoreComponents        scoreComponentsDTO `json:"score_components"`
	EstimatedTPS           *float64           `json:"estimated_tps"`
	EstimateConfidence     string             `json:"estimate_confidence"`
	PrefillTPS             *float64           `json:"prefill_tps"`
	TTFTMs                 *float64           `json:"ttft_ms"`
	Runtime                string             `json:"runtime"` // lower-case id (llamacpp / mlx / ...)
	BestQuant              *string            `json:"best_quant"`
	MemoryRequiredGB       float64            `json:"memory_required_gb"`
	MemoryAvailableGB      float64            `json:"memory_available_gb"`
	UtilizationPct         float64            `json:"utilization_pct"`
	// notes / capabilities are arrays of strings on v1.1.15; elements are
	// decoded loosely (string or object) and reduced to strings by the mapper.
	Notes         []any    `json:"notes"`
	Capabilities  []any    `json:"capabilities"`
	CapabilityIDs []string `json:"capability_ids"`
	License       *string  `json:"license"` // observed null
	Installed     bool     `json:"installed"`
	DiskSizeGB    *float64 `json:"disk_size_gb"`
	MeasuredTPS   *float64 `json:"measured_tps"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func decodeSystem(r io.Reader) (systemResponse, error) {
	var out systemResponse
	if err := json.NewDecoder(r).Decode(&out); err != nil {
		return systemResponse{}, err
	}
	return out, nil
}

func decodeModels(r io.Reader) (modelsResponse, error) {
	var out modelsResponse
	if err := json.NewDecoder(r).Decode(&out); err != nil {
		return modelsResponse{}, err
	}
	return out, nil
}

func decodeHealth(r io.Reader) (healthResponse, error) {
	var out healthResponse
	if err := json.NewDecoder(r).Decode(&out); err != nil {
		return healthResponse{}, err
	}
	return out, nil
}

// decodeErrorMessage extracts {"error":"..."} from an llmfit error body.
// Non-JSON bodies yield an empty string so callers fall back to the status.
func decodeErrorMessage(body []byte) string {
	var out errorResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return ""
	}
	return out.Error
}
