package viewer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	llmopsapp "github.com/Nyukimin/RenCrow_CORE/internal/application/llmops"
	domainllmops "github.com/Nyukimin/RenCrow_CORE/internal/domain/llmops"
)

// LLMOpsService is the read model consumed by the LLM Ops viewer handlers.
// The application CapabilityService satisfies it; tests use a stub.
type LLMOpsService interface {
	Nodes(ctx context.Context) []llmopsapp.NodeSnapshot
	Node(ctx context.Context, nodeID string) (llmopsapp.NodeSnapshot, bool)
	NodeModels(ctx context.Context, nodeID string, query llmopsapp.ModelsQuery) (llmopsapp.ModelsSnapshot, bool)
	ModelMatrix(ctx context.Context, modelID string) []llmopsapp.MatrixEntry
	Refresh(ctx context.Context, nodeIDs ...string) llmopsapp.RefreshResult
}

// LLMOpsHandlerOptions wires the LLM Ops handlers. A nil Service yields 503.
type LLMOpsHandlerOptions struct {
	Service LLMOpsService
	Now     func() time.Time
}

const (
	llmOpsMaxLimit          = 200
	llmOpsMaxRefreshBody    = 4096
	llmOpsFitLevelUnknown   = "unknown"
	llmOpsMatrixNotListed   = llmOpsFitLevelUnknown
	llmOpsServiceUnavailMsg = "llm ops service unavailable"
)

// Response contracts. Field names are shared with the viewer frontend; do not
// rename them.

type llmOpsGPUView struct {
	Name               string  `json:"name"`
	VRAMGB             float64 `json:"vram_gb"`
	AvailableVRAMGB    float64 `json:"available_vram_gb"` // 0 when llmfit reports no per-GPU free value
	MemoryBandwidthGBs float64 `json:"memory_bandwidth_gbs"`
	Count              int     `json:"count"` // identical devices grouped under this entry
}

type llmOpsNodeView struct {
	NodeID         string          `json:"node_id"`
	NodeName       string          `json:"node_name"`
	Status         string          `json:"status"`
	OS             string          `json:"os"`
	CPUName        string          `json:"cpu_name"`
	CPUCores       int             `json:"cpu_cores"`
	TotalRAMGB     float64         `json:"total_ram_gb"`
	AvailableRAMGB float64         `json:"available_ram_gb"`
	HasGPU         bool            `json:"has_gpu"`
	GPUCount       int             `json:"gpu_count"`
	GPUs           []llmOpsGPUView `json:"gpus"`
	UnifiedMemory  bool            `json:"unified_memory"`
	Backend        string          `json:"backend"`
	CollectedAt    *string         `json:"collected_at"`
	Source         string          `json:"source"`
	Error          string          `json:"error"`
}

type llmOpsScoresView struct {
	Quality float64 `json:"quality"`
	Speed   float64 `json:"speed"`
	Fit     float64 `json:"fit"`
	Context float64 `json:"context"`
}

type llmOpsContextView struct {
	Native    int `json:"native"`
	Usable    int `json:"usable"`
	Evaluated int `json:"evaluated"`
}

type llmOpsMemoryView struct {
	RequiredGB     float64 `json:"required_gb"`
	AvailableGB    float64 `json:"available_gb"`
	UtilizationPct float64 `json:"utilization_pct"`
}

type llmOpsPerformanceView struct {
	EstimatedTPS *float64 `json:"estimated_tps"`
	MeasuredTPS  *float64 `json:"measured_tps"`
	PrefillTPS   *float64 `json:"prefill_tps"`
	TTFTMs       *float64 `json:"ttft_ms"`
}

type llmOpsModelView struct {
	ModelID            string                `json:"model_id"`
	Provider           string                `json:"provider"`
	ParameterCount     string                `json:"parameter_count"`
	ParamsB            float64               `json:"params_b"`
	IsMoE              bool                  `json:"is_moe"`
	FitLevel           string                `json:"fit_level"`
	Score              float64               `json:"score"`
	Scores             llmOpsScoresView      `json:"scores"`
	Runtime            string                `json:"runtime"`
	RunMode            string                `json:"run_mode"`
	BestQuant          *string               `json:"best_quant"`
	Context            llmOpsContextView     `json:"context"`
	Memory             llmOpsMemoryView      `json:"memory"`
	Performance        llmOpsPerformanceView `json:"performance"`
	EstimateConfidence string                `json:"estimate_confidence"`
	Installed          bool                  `json:"installed"`
	DiskSizeGB         *float64              `json:"disk_size_gb"`
	Capabilities       []string              `json:"capabilities"`
	License            string                `json:"license"`
	Notes              []string              `json:"notes"`
}

type llmOpsMatrixEntryView struct {
	NodeID             string   `json:"node_id"`
	Status             string   `json:"status"`
	FitLevel           string   `json:"fit_level"`
	Score              float64  `json:"score"`
	BestQuant          *string  `json:"best_quant"`
	Runtime            string   `json:"runtime"`
	UsableContext      int      `json:"usable_context"`
	EstimatedTPS       *float64 `json:"estimated_tps"`
	MeasuredTPS        *float64 `json:"measured_tps"`
	EstimateConfidence string   `json:"estimate_confidence"`
	CollectedAt        *string  `json:"collected_at"`
	Error              string   `json:"error"`
}

// HandleLLMOpsNodes serves GET /viewer/llm-ops/nodes (Node Overview).
func HandleLLMOpsNodes(opts LLMOpsHandlerOptions) http.HandlerFunc {
	now := llmOpsClock(opts.Now)
	return func(w http.ResponseWriter, r *http.Request) {
		if opts.Service == nil {
			http.Error(w, llmOpsServiceUnavailMsg, http.StatusServiceUnavailable)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		snapshots := opts.Service.Nodes(r.Context())
		nodes := make([]llmOpsNodeView, 0, len(snapshots))
		for _, snap := range snapshots {
			nodes = append(nodes, llmOpsNodeViewFrom(snap))
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"generated_at": llmOpsTimestamp(now()),
			"nodes":        nodes,
		})
	}
}

// HandleLLMOpsNode serves GET /viewer/llm-ops/node?node_id= (Hardware Profile).
func HandleLLMOpsNode(opts LLMOpsHandlerOptions) http.HandlerFunc {
	now := llmOpsClock(opts.Now)
	return func(w http.ResponseWriter, r *http.Request) {
		if opts.Service == nil {
			http.Error(w, llmOpsServiceUnavailMsg, http.StatusServiceUnavailable)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		nodeID := strings.TrimSpace(r.URL.Query().Get("node_id"))
		if nodeID == "" {
			http.Error(w, "node_id is required", http.StatusBadRequest)
			return
		}
		snap, ok := opts.Service.Node(r.Context(), nodeID)
		if !ok {
			http.Error(w, "node not found", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"generated_at": llmOpsTimestamp(now()),
			"node":         llmOpsNodeViewFrom(snap),
		})
	}
}

// HandleLLMOpsNodeModels serves GET /viewer/llm-ops/node/models?node_id=
// with optional limit / use_case / runtime / min_fit / max_context.
func HandleLLMOpsNodeModels(opts LLMOpsHandlerOptions) http.HandlerFunc {
	now := llmOpsClock(opts.Now)
	return func(w http.ResponseWriter, r *http.Request) {
		if opts.Service == nil {
			http.Error(w, llmOpsServiceUnavailMsg, http.StatusServiceUnavailable)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		values := r.URL.Query()
		nodeID := strings.TrimSpace(values.Get("node_id"))
		if nodeID == "" {
			http.Error(w, "node_id is required", http.StatusBadRequest)
			return
		}
		query, err := parseLLMOpsModelsQuery(values)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		snap, ok := opts.Service.NodeModels(r.Context(), nodeID, query)
		if !ok {
			http.Error(w, "node not found", http.StatusNotFound)
			return
		}
		models := make([]llmOpsModelView, 0, len(snap.Models))
		for i := range snap.Models {
			models = append(models, llmOpsModelViewFrom(snap.Models[i]))
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"generated_at": llmOpsTimestamp(now()),
			"node_id":      snap.NodeID,
			"status":       string(snap.Status),
			"collected_at": llmOpsOptionalTimestamp(snap.CollectedAt),
			"error":        snap.Error,
			"models":       models,
		})
	}
}

// HandleLLMOpsModelMatrix serves GET /viewer/llm-ops/model/matrix?model_id=.
func HandleLLMOpsModelMatrix(opts LLMOpsHandlerOptions) http.HandlerFunc {
	now := llmOpsClock(opts.Now)
	return func(w http.ResponseWriter, r *http.Request) {
		if opts.Service == nil {
			http.Error(w, llmOpsServiceUnavailMsg, http.StatusServiceUnavailable)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		modelID := strings.TrimSpace(r.URL.Query().Get("model_id"))
		if modelID == "" {
			http.Error(w, "model_id is required", http.StatusBadRequest)
			return
		}
		entries := opts.Service.ModelMatrix(r.Context(), modelID)
		nodes := make([]llmOpsMatrixEntryView, 0, len(entries))
		for _, entry := range entries {
			nodes = append(nodes, llmOpsMatrixEntryViewFrom(entry))
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"generated_at": llmOpsTimestamp(now()),
			"model_id":     modelID,
			"nodes":        nodes,
		})
	}
}

// HandleLLMOpsRefresh serves POST /viewer/llm-ops/refresh. It only re-reads
// llmfit; an optional JSON body {"node_id": "..."} scopes the refresh.
func HandleLLMOpsRefresh(opts LLMOpsHandlerOptions) http.HandlerFunc {
	now := llmOpsClock(opts.Now)
	return func(w http.ResponseWriter, r *http.Request) {
		if opts.Service == nil {
			http.Error(w, llmOpsServiceUnavailMsg, http.StatusServiceUnavailable)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var nodeIDs []string
		if r.Body != nil {
			raw, err := io.ReadAll(io.LimitReader(r.Body, llmOpsMaxRefreshBody))
			if err != nil {
				http.Error(w, "failed to read request body", http.StatusBadRequest)
				return
			}
			if len(strings.TrimSpace(string(raw))) > 0 {
				var req struct {
					NodeID string `json:"node_id"`
				}
				if err := json.Unmarshal(raw, &req); err != nil {
					http.Error(w, "invalid JSON body", http.StatusBadRequest)
					return
				}
				if id := strings.TrimSpace(req.NodeID); id != "" {
					nodeIDs = []string{id}
				}
			}
		}
		result := opts.Service.Refresh(r.Context(), nodeIDs...)
		refreshed := result.Refreshed
		if refreshed == nil {
			refreshed = []string{}
		}
		failed := result.Failed
		if failed == nil {
			failed = map[string]string{}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"generated_at": llmOpsTimestamp(now()),
			"refreshed":    refreshed,
			"failed":       failed,
		})
	}
}

func parseLLMOpsModelsQuery(values map[string][]string) (llmopsapp.ModelsQuery, error) {
	get := func(key string) string {
		if list, ok := values[key]; ok && len(list) > 0 {
			return strings.TrimSpace(list[0])
		}
		return ""
	}
	var query llmopsapp.ModelsQuery
	if raw := get("limit"); raw != "" {
		limit, err := parseViewerLimit(raw, 0, llmOpsMaxLimit)
		if err != nil {
			return query, errors.New("invalid limit")
		}
		query.Limit = limit
	}
	if raw := get("max_context"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return query, errors.New("invalid max_context")
		}
		query.MaxContext = n
	}
	query.UseCase = get("use_case")
	query.Runtime = get("runtime")
	query.MinFit = get("min_fit")
	return llmopsapp.ValidateModelsQuery(query)
}

func llmOpsNodeViewFrom(snap llmopsapp.NodeSnapshot) llmOpsNodeView {
	view := llmOpsNodeView{
		NodeID:      snap.NodeID,
		Status:      string(snap.Status),
		GPUs:        []llmOpsGPUView{},
		CollectedAt: llmOpsOptionalTimestamp(snap.CollectedAt),
		Source:      domainllmops.SourceLLMFit,
		Error:       snap.Error,
	}
	if snap.Profile == nil {
		return view
	}
	p := snap.Profile
	view.NodeName = p.NodeName
	view.OS = p.OS
	view.CPUName = p.CPUName
	view.CPUCores = p.CPUCores
	view.TotalRAMGB = p.TotalRAMGB
	view.AvailableRAMGB = p.AvailableRAMGB
	view.HasGPU = p.HasGPU
	view.GPUCount = p.GPUCount
	view.UnifiedMemory = p.UnifiedMemory
	view.Backend = p.Backend
	if p.Source != "" {
		view.Source = p.Source
	}
	for _, gpu := range p.GPUs {
		view.GPUs = append(view.GPUs, llmOpsGPUView{Name: gpu.Name, VRAMGB: gpu.VRAMGB, AvailableVRAMGB: gpu.AvailableVRAMGB, MemoryBandwidthGBs: gpu.MemoryBandwidthGBs, Count: gpu.Count})
	}
	return view
}

func llmOpsModelViewFrom(m domainllmops.ModelFitAssessment) llmOpsModelView {
	capabilities := m.Capabilities
	if capabilities == nil {
		capabilities = []string{}
	}
	notes := m.Notes
	if notes == nil {
		notes = []string{}
	}
	return llmOpsModelView{
		ModelID:        m.ModelID,
		Provider:       m.Provider,
		ParameterCount: m.ParameterCount,
		ParamsB:        m.ParamsB,
		IsMoE:          m.IsMoE,
		FitLevel:       string(m.FitLevel),
		Score:          m.Score,
		Scores:         llmOpsScoresView{Quality: m.QualityScore, Speed: m.SpeedScore, Fit: m.FitScore, Context: m.ContextScore},
		Runtime:        m.Runtime,
		RunMode:        m.RunMode,
		BestQuant:      m.BestQuant,
		Context:        llmOpsContextView{Native: m.ContextLength, Usable: m.UsableContext, Evaluated: m.EffectiveContextLength},
		Memory:         llmOpsMemoryView{RequiredGB: m.MemoryRequiredGB, AvailableGB: m.MemoryAvailableGB, UtilizationPct: m.UtilizationPct},
		Performance: llmOpsPerformanceView{
			EstimatedTPS: m.EstimatedTPS,
			MeasuredTPS:  m.MeasuredTPS,
			PrefillTPS:   m.PrefillTPS,
			TTFTMs:       m.TTFTMs,
		},
		EstimateConfidence: string(m.EstimateConfidence),
		Installed:          m.Installed,
		DiskSizeGB:         m.DiskSizeGB,
		Capabilities:       capabilities,
		License:            m.License,
		Notes:              notes,
	}
}

func llmOpsMatrixEntryViewFrom(entry llmopsapp.MatrixEntry) llmOpsMatrixEntryView {
	view := llmOpsMatrixEntryView{
		NodeID:      entry.NodeID,
		Status:      string(entry.Status),
		FitLevel:    llmOpsMatrixNotListed,
		CollectedAt: llmOpsOptionalTimestamp(entry.CollectedAt),
		Error:       entry.Error,
	}
	if entry.Assessment == nil {
		return view
	}
	a := entry.Assessment
	view.FitLevel = string(a.FitLevel)
	if view.FitLevel == "" {
		view.FitLevel = llmOpsFitLevelUnknown
	}
	view.Score = a.Score
	view.BestQuant = a.BestQuant
	view.Runtime = a.Runtime
	view.UsableContext = a.UsableContext
	view.EstimatedTPS = a.EstimatedTPS
	view.MeasuredTPS = a.MeasuredTPS
	view.EstimateConfidence = string(a.EstimateConfidence)
	return view
}

func llmOpsClock(now func() time.Time) func() time.Time {
	if now == nil {
		return func() time.Time { return time.Now().UTC() }
	}
	return now
}

func llmOpsTimestamp(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func llmOpsOptionalTimestamp(t *time.Time) *string {
	if t == nil || t.IsZero() {
		return nil
	}
	s := llmOpsTimestamp(*t)
	return &s
}
