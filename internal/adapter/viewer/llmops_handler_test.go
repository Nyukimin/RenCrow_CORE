package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	llmopsapp "github.com/Nyukimin/RenCrow_CORE/internal/application/llmops"
	domainllmops "github.com/Nyukimin/RenCrow_CORE/internal/domain/llmops"
)

type stubLLMOpsService struct {
	nodes     []llmopsapp.NodeSnapshot
	models    map[string]llmopsapp.ModelsSnapshot
	matrix    []llmopsapp.MatrixEntry
	refresh   llmopsapp.RefreshResult
	lastQuery llmopsapp.ModelsQuery
	lastIDs   []string
}

func (s *stubLLMOpsService) Nodes(context.Context) []llmopsapp.NodeSnapshot { return s.nodes }

func (s *stubLLMOpsService) Node(_ context.Context, id string) (llmopsapp.NodeSnapshot, bool) {
	for _, n := range s.nodes {
		if n.NodeID == id {
			return n, true
		}
	}
	return llmopsapp.NodeSnapshot{}, false
}

func (s *stubLLMOpsService) NodeModels(_ context.Context, id string, q llmopsapp.ModelsQuery) (llmopsapp.ModelsSnapshot, bool) {
	s.lastQuery = q
	snap, ok := s.models[id]
	return snap, ok
}

func (s *stubLLMOpsService) ModelMatrix(context.Context, string) []llmopsapp.MatrixEntry {
	return s.matrix
}

func (s *stubLLMOpsService) Refresh(_ context.Context, ids ...string) llmopsapp.RefreshResult {
	s.lastIDs = ids
	return s.refresh
}

var llmOpsTestNow = func() time.Time { return time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC) }

func llmOpsTestProfile() *domainllmops.NodeHardwareProfile {
	return &domainllmops.NodeHardwareProfile{
		NodeID: "node-a", NodeName: "gpu-box", OS: "linux", CPUName: "Ryzen", CPUCores: 16,
		TotalRAMGB: 64, AvailableRAMGB: 40, HasGPU: true, GPUCount: 1,
		GPUs:          []domainllmops.GPUProfile{{Name: "RX 6800", VRAMGB: 16, AvailableVRAMGB: ptrFloat(15), MemoryBandwidthGBs: 512, Count: 1}},
		UnifiedMemory: false, Backend: "vulkan", Source: domainllmops.SourceLLMFit,
	}
}

func decodeLLMOpsBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v body=%s", err, rec.Body.String())
	}
	return body
}

func TestLLMOpsHandlersReturn503WithoutService(t *testing.T) {
	handlers := map[string]http.HandlerFunc{
		"nodes":   HandleLLMOpsNodes(LLMOpsHandlerOptions{}),
		"node":    HandleLLMOpsNode(LLMOpsHandlerOptions{}),
		"models":  HandleLLMOpsNodeModels(LLMOpsHandlerOptions{}),
		"matrix":  HandleLLMOpsModelMatrix(LLMOpsHandlerOptions{}),
		"refresh": HandleLLMOpsRefresh(LLMOpsHandlerOptions{}),
	}
	for name, h := range handlers {
		method := http.MethodGet
		if name == "refresh" {
			method = http.MethodPost
		}
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(method, "/viewer/llm-ops/x", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s: status=%d want 503", name, rec.Code)
		}
	}
}

func TestLLMOpsHandlersRejectWrongMethods(t *testing.T) {
	svc := &stubLLMOpsService{}
	opts := LLMOpsHandlerOptions{Service: svc, Now: llmOpsTestNow}
	for name, h := range map[string]http.HandlerFunc{
		"nodes": HandleLLMOpsNodes(opts), "node": HandleLLMOpsNode(opts), "models": HandleLLMOpsNodeModels(opts), "matrix": HandleLLMOpsModelMatrix(opts),
	} {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodPost, "/viewer/llm-ops/x", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s: POST status=%d want 405", name, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	HandleLLMOpsRefresh(opts)(rec, httptest.NewRequest(http.MethodGet, "/viewer/llm-ops/refresh", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("refresh GET status=%d want 405", rec.Code)
	}
}

func TestHandleLLMOpsNodesProjectsOnlineOfflineStaleAndDisabled(t *testing.T) {
	staleAt := time.Date(2026, 9, 15, 11, 30, 0, 0, time.UTC)
	svc := &stubLLMOpsService{nodes: []llmopsapp.NodeSnapshot{
		{NodeID: "node-a", Status: llmopsapp.StatusOnline, Profile: llmOpsTestProfile(), CollectedAt: ptrTime(llmOpsTestNow())},
		{NodeID: "node-b", Status: llmopsapp.StatusOffline, Error: "connection refused"},
		{NodeID: "node-c", Status: llmopsapp.StatusStale, Profile: &domainllmops.NodeHardwareProfile{NodeID: "node-c", NodeName: "mac", OS: "macos", UnifiedMemory: true, Backend: "metal", HasGPU: true, GPUCount: 1}, CollectedAt: &staleAt, Error: "timeout"},
		{NodeID: "node-d", Status: llmopsapp.StatusDisabled},
	}}
	rec := httptest.NewRecorder()
	HandleLLMOpsNodes(LLMOpsHandlerOptions{Service: svc, Now: llmOpsTestNow})(rec, httptest.NewRequest(http.MethodGet, "/viewer/llm-ops/nodes", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("content-type=%q", ct)
	}
	body := decodeLLMOpsBody(t, rec)
	if body["generated_at"] != "2026-09-15T12:00:00Z" {
		t.Fatalf("generated_at=%v", body["generated_at"])
	}
	nodes := body["nodes"].([]any)
	if len(nodes) != 4 {
		t.Fatalf("nodes=%d", len(nodes))
	}

	online := nodes[0].(map[string]any)
	for key, want := range map[string]any{
		"node_id": "node-a", "node_name": "gpu-box", "status": "online", "os": "linux", "cpu_name": "Ryzen", "cpu_cores": float64(16),
		"total_ram_gb": float64(64), "available_ram_gb": float64(40), "has_gpu": true, "gpu_count": float64(1), "unified_memory": false,
		"backend": "vulkan", "collected_at": "2026-09-15T12:00:00Z", "source": "llmfit", "error": "",
	} {
		if online[key] != want {
			t.Fatalf("online[%s]=%v want %v", key, online[key], want)
		}
	}
	gpus := online["gpus"].([]any)
	gpu := gpus[0].(map[string]any)
	if gpu["name"] != "RX 6800" || gpu["vram_gb"] != float64(16) || gpu["available_vram_gb"] != float64(15) || gpu["memory_bandwidth_gbs"] != float64(512) || gpu["count"] != float64(1) {
		t.Fatalf("gpu=%v", gpu)
	}

	offline := nodes[1].(map[string]any)
	if offline["status"] != "offline" || offline["error"] != "connection refused" || offline["collected_at"] != nil || offline["source"] != "llmfit" {
		t.Fatalf("offline=%v", offline)
	}
	if offlineGPUs, ok := offline["gpus"].([]any); !ok || len(offlineGPUs) != 0 {
		t.Fatalf("offline gpus must be an empty array, got %v", offline["gpus"])
	}
	if offline["node_name"] != "" || offline["cpu_cores"] != float64(0) {
		t.Fatalf("offline profile fields must be zero values: %v", offline)
	}

	stale := nodes[2].(map[string]any)
	if stale["status"] != "stale" || stale["collected_at"] != "2026-09-15T11:30:00Z" || stale["error"] != "timeout" || stale["unified_memory"] != true || stale["backend"] != "metal" {
		t.Fatalf("stale=%v", stale)
	}
	if staleGPUs, ok := stale["gpus"].([]any); !ok || len(staleGPUs) != 0 {
		t.Fatalf("nil GPUs must serialize as empty array, got %v", stale["gpus"])
	}
	if disabled := nodes[3].(map[string]any); disabled["status"] != "disabled" {
		t.Fatalf("disabled=%v", disabled)
	}
}

func TestHandleLLMOpsNodesEmitsNullForUnknownAvailableVRAM(t *testing.T) {
	// llmfit groups identical cards (count=4) and reports gpu_available_gb null;
	// the view must keep the count and emit null, never 0, for the unknown value.
	grouped := llmOpsTestProfile()
	grouped.GPUCount = 4
	grouped.GPUs = []domainllmops.GPUProfile{{Name: "RX 6800", VRAMGB: 15.98, MemoryBandwidthGBs: 512, Count: 4}}
	reportedZero := llmOpsTestProfile()
	reportedZero.GPUs[0].AvailableVRAMGB = ptrFloat(0)
	svc := &stubLLMOpsService{nodes: []llmopsapp.NodeSnapshot{
		{NodeID: "node-a", Status: llmopsapp.StatusOnline, Profile: grouped, CollectedAt: ptrTime(llmOpsTestNow())},
		{NodeID: "node-b", Status: llmopsapp.StatusOnline, Profile: reportedZero, CollectedAt: ptrTime(llmOpsTestNow())},
	}}
	rec := httptest.NewRecorder()
	HandleLLMOpsNodes(LLMOpsHandlerOptions{Service: svc, Now: llmOpsTestNow})(rec, httptest.NewRequest(http.MethodGet, "/viewer/llm-ops/nodes", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"available_vram_gb":null`) {
		t.Fatalf("unknown free VRAM must serialize as JSON null, body=%s", rec.Body.String())
	}
	nodes := decodeLLMOpsBody(t, rec)["nodes"].([]any)
	unknown := nodes[0].(map[string]any)["gpus"].([]any)[0].(map[string]any)
	if v, ok := unknown["available_vram_gb"]; !ok || v != nil {
		t.Fatalf("available_vram_gb must be present and null when unknown, got %v (present=%v)", v, ok)
	}
	if unknown["count"] != float64(4) || unknown["vram_gb"] != 15.98 || unknown["name"] != "RX 6800" {
		t.Fatalf("grouped gpu must keep count and per-card vram: %v", unknown)
	}
	if nodes[0].(map[string]any)["gpu_count"] != float64(4) {
		t.Fatalf("gpu_count must stay 4: %v", nodes[0])
	}
	zero := nodes[1].(map[string]any)["gpus"].([]any)[0].(map[string]any)
	if zero["available_vram_gb"] != float64(0) {
		t.Fatalf("a reported 0 must stay 0 (known), not null: %v", zero)
	}
}

func TestHandleLLMOpsNodesReturnsEmptyArrayWithoutNodes(t *testing.T) {
	rec := httptest.NewRecorder()
	HandleLLMOpsNodes(LLMOpsHandlerOptions{Service: &stubLLMOpsService{}, Now: llmOpsTestNow})(rec, httptest.NewRequest(http.MethodGet, "/viewer/llm-ops/nodes", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"nodes":[]`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleLLMOpsNodeValidatesNodeID(t *testing.T) {
	svc := &stubLLMOpsService{nodes: []llmopsapp.NodeSnapshot{{NodeID: "node-a", Status: llmopsapp.StatusOnline, Profile: llmOpsTestProfile(), CollectedAt: ptrTime(llmOpsTestNow())}}}
	h := HandleLLMOpsNode(LLMOpsHandlerOptions{Service: svc, Now: llmOpsTestNow})

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/viewer/llm-ops/node", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing node_id status=%d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/viewer/llm-ops/node?node_id=ghost", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown node_id status=%d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/viewer/llm-ops/node?node_id=node-a", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := decodeLLMOpsBody(t, rec)
	node := body["node"].(map[string]any)
	if node["node_id"] != "node-a" || node["status"] != "online" || body["generated_at"] != "2026-09-15T12:00:00Z" {
		t.Fatalf("body=%v", body)
	}
}

func TestHandleLLMOpsNodeModelsProjectsEstimatedAndMeasured(t *testing.T) {
	estimated := 42.5
	measured := 38.1
	prefill := 900.0
	ttft := 120.0
	quant := "Q4_K_M"
	disk := 17.9
	svc := &stubLLMOpsService{models: map[string]llmopsapp.ModelsSnapshot{
		"node-a": {NodeID: "node-a", Status: llmopsapp.StatusOnline, CollectedAt: ptrTime(llmOpsTestNow()), Models: []domainllmops.ModelFitAssessment{
			{
				NodeID: "node-a", ModelID: "qwen3-coder-30b", Provider: "Qwen", ParameterCount: "30B", ParamsB: 30.5, IsMoE: true,
				FitLevel: domainllmops.FitLevelGood, Score: 81.5, QualityScore: 90, SpeedScore: 70, FitScore: 80, ContextScore: 85,
				Runtime: "llamacpp", RunMode: "gpu", BestQuant: &quant,
				ContextLength: 262144, UsableContext: 65536, EffectiveContextLength: 32768,
				MemoryRequiredGB: 18.2, MemoryAvailableGB: 31, UtilizationPct: 58.7,
				EstimatedTPS: &estimated, MeasuredTPS: &measured, PrefillTPS: &prefill, TTFTMs: &ttft,
				EstimateConfidence: domainllmops.EstimateConfidenceMeasuredLocal, Installed: true, DiskSizeGB: &disk,
				Capabilities: []string{"chat"}, License: "apache-2.0", Notes: []string{"ok"},
			},
			{
				NodeID: "node-a", ModelID: "gemma-4-27b", FitLevel: domainllmops.FitLevelTooTight, EstimatedTPS: &estimated,
				EstimateConfidence: domainllmops.EstimateConfidenceEstimated,
			},
		}},
		"node-b": {NodeID: "node-b", Status: llmopsapp.StatusOffline, Error: "connection refused"},
	}}
	h := HandleLLMOpsNodeModels(LLMOpsHandlerOptions{Service: svc, Now: llmOpsTestNow})

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/viewer/llm-ops/node/models?node_id=node-a&limit=5&use_case=coding&runtime=llamacpp&min_fit=marginal&max_context=32768", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if svc.lastQuery.Limit != 5 || svc.lastQuery.UseCase != "coding" || svc.lastQuery.Runtime != "llamacpp" || svc.lastQuery.MinFit != "marginal" || svc.lastQuery.MaxContext != 32768 {
		t.Fatalf("query not forwarded: %+v", svc.lastQuery)
	}
	body := decodeLLMOpsBody(t, rec)
	if body["node_id"] != "node-a" || body["status"] != "online" || body["collected_at"] != "2026-09-15T12:00:00Z" || body["error"] != "" {
		t.Fatalf("envelope=%v", body)
	}
	models := body["models"].([]any)
	if len(models) != 2 {
		t.Fatalf("models=%d", len(models))
	}
	measuredModel := models[0].(map[string]any)
	for key, want := range map[string]any{
		"model_id": "qwen3-coder-30b", "provider": "Qwen", "parameter_count": "30B", "params_b": 30.5, "is_moe": true,
		"fit_level": "good", "score": 81.5, "runtime": "llamacpp", "run_mode": "gpu", "best_quant": "Q4_K_M",
		"estimate_confidence": "measured_local", "installed": true, "disk_size_gb": 17.9, "license": "apache-2.0",
	} {
		if measuredModel[key] != want {
			t.Fatalf("model[%s]=%v want %v", key, measuredModel[key], want)
		}
	}
	scores := measuredModel["scores"].(map[string]any)
	if scores["quality"] != float64(90) || scores["speed"] != float64(70) || scores["fit"] != float64(80) || scores["context"] != float64(85) {
		t.Fatalf("scores=%v", scores)
	}
	ctxObj := measuredModel["context"].(map[string]any)
	if ctxObj["native"] != float64(262144) || ctxObj["usable"] != float64(65536) || ctxObj["evaluated"] != float64(32768) {
		t.Fatalf("context=%v", ctxObj)
	}
	mem := measuredModel["memory"].(map[string]any)
	if mem["required_gb"] != 18.2 || mem["available_gb"] != float64(31) || mem["utilization_pct"] != 58.7 {
		t.Fatalf("memory=%v", mem)
	}
	perf := measuredModel["performance"].(map[string]any)
	if perf["estimated_tps"] != 42.5 || perf["measured_tps"] != 38.1 || perf["prefill_tps"] != float64(900) || perf["ttft_ms"] != float64(120) {
		t.Fatalf("performance=%v", perf)
	}
	if caps := measuredModel["capabilities"].([]any); len(caps) != 1 || caps[0] != "chat" {
		t.Fatalf("capabilities=%v", measuredModel["capabilities"])
	}

	estimatedOnly := models[1].(map[string]any)
	perf = estimatedOnly["performance"].(map[string]any)
	if perf["estimated_tps"] != 42.5 || perf["measured_tps"] != nil || perf["prefill_tps"] != nil || perf["ttft_ms"] != nil {
		t.Fatalf("estimated-only performance=%v", perf)
	}
	if estimatedOnly["best_quant"] != nil || estimatedOnly["disk_size_gb"] != nil || estimatedOnly["fit_level"] != "too_tight" {
		t.Fatalf("estimated-only nulls: %v", estimatedOnly)
	}
	if caps, ok := estimatedOnly["capabilities"].([]any); !ok || len(caps) != 0 {
		t.Fatalf("nil capabilities must serialize as empty array: %v", estimatedOnly["capabilities"])
	}
	if notes, ok := estimatedOnly["notes"].([]any); !ok || len(notes) != 0 {
		t.Fatalf("nil notes must serialize as empty array: %v", estimatedOnly["notes"])
	}

	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/viewer/llm-ops/node/models?node_id=node-b", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("offline status=%d body=%s", rec.Code, rec.Body.String())
	}
	body = decodeLLMOpsBody(t, rec)
	if body["status"] != "offline" || body["error"] != "connection refused" || body["collected_at"] != nil {
		t.Fatalf("offline envelope=%v", body)
	}
	if models, ok := body["models"].([]any); !ok || len(models) != 0 {
		t.Fatalf("offline models must be an empty array: %v", body["models"])
	}
}

func TestHandleLLMOpsNodeModelsValidation(t *testing.T) {
	svc := &stubLLMOpsService{models: map[string]llmopsapp.ModelsSnapshot{"node-a": {NodeID: "node-a", Status: llmopsapp.StatusOnline}}}
	h := HandleLLMOpsNodeModels(LLMOpsHandlerOptions{Service: svc, Now: llmOpsTestNow})
	cases := map[string]int{
		"/viewer/llm-ops/node/models":                               http.StatusBadRequest,
		"/viewer/llm-ops/node/models?node_id=ghost":                 http.StatusNotFound,
		"/viewer/llm-ops/node/models?node_id=node-a&limit=abc":      http.StatusBadRequest,
		"/viewer/llm-ops/node/models?node_id=node-a&limit=0":        http.StatusBadRequest,
		"/viewer/llm-ops/node/models?node_id=node-a&min_fit=huge":   http.StatusBadRequest,
		"/viewer/llm-ops/node/models?node_id=node-a&runtime=sh":     http.StatusBadRequest,
		"/viewer/llm-ops/node/models?node_id=node-a&use_case=x":     http.StatusBadRequest,
		"/viewer/llm-ops/node/models?node_id=node-a&max_context=-1": http.StatusBadRequest,
		"/viewer/llm-ops/node/models?node_id=node-a&max_context=zz": http.StatusBadRequest,
		"/viewer/llm-ops/node/models?node_id=node-a":                http.StatusOK,
	}
	for target, want := range cases {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != want {
			t.Fatalf("%s: status=%d want %d body=%s", target, rec.Code, want, rec.Body.String())
		}
	}
}

func TestHandleLLMOpsModelMatrix(t *testing.T) {
	quant := "Q8_0"
	estimated := 12.0
	svc := &stubLLMOpsService{matrix: []llmopsapp.MatrixEntry{
		{NodeID: "node-a", Status: llmopsapp.StatusOnline, CollectedAt: ptrTime(llmOpsTestNow()), Assessment: &domainllmops.ModelFitAssessment{
			ModelID: "gemma-4-27b", FitLevel: domainllmops.FitLevelMarginal, Score: 55, BestQuant: &quant, Runtime: "mlx", UsableContext: 16384,
			EstimatedTPS: &estimated, EstimateConfidence: domainllmops.EstimateConfidenceEstimated,
		}},
		{NodeID: "node-b", Status: llmopsapp.StatusOnline, CollectedAt: ptrTime(llmOpsTestNow())},
		{NodeID: "node-c", Status: llmopsapp.StatusOffline, Error: "timeout"},
	}}
	h := HandleLLMOpsModelMatrix(LLMOpsHandlerOptions{Service: svc, Now: llmOpsTestNow})

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/viewer/llm-ops/model/matrix", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing model_id status=%d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/viewer/llm-ops/model/matrix?model_id=gemma-4-27b", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := decodeLLMOpsBody(t, rec)
	if body["model_id"] != "gemma-4-27b" || body["generated_at"] != "2026-09-15T12:00:00Z" {
		t.Fatalf("envelope=%v", body)
	}
	nodes := body["nodes"].([]any)
	if len(nodes) != 3 {
		t.Fatalf("nodes=%d", len(nodes))
	}
	fit := nodes[0].(map[string]any)
	for key, want := range map[string]any{
		"node_id": "node-a", "status": "online", "fit_level": "marginal", "score": float64(55), "best_quant": "Q8_0", "runtime": "mlx",
		"usable_context": float64(16384), "estimated_tps": float64(12), "measured_tps": nil, "estimate_confidence": "estimated",
		"collected_at": "2026-09-15T12:00:00Z", "error": "",
	} {
		if fit[key] != want {
			t.Fatalf("fit[%s]=%v want %v", key, fit[key], want)
		}
	}
	notListed := nodes[1].(map[string]any)
	if notListed["fit_level"] != "unknown" || notListed["best_quant"] != nil || notListed["estimated_tps"] != nil || notListed["score"] != float64(0) || notListed["status"] != "online" {
		t.Fatalf("node without assessment=%v", notListed)
	}
	offline := nodes[2].(map[string]any)
	if offline["status"] != "offline" || offline["fit_level"] != "unknown" || offline["error"] != "timeout" || offline["collected_at"] != nil {
		t.Fatalf("offline=%v", offline)
	}
}

func TestHandleLLMOpsModelMatrixEmptyNodes(t *testing.T) {
	rec := httptest.NewRecorder()
	HandleLLMOpsModelMatrix(LLMOpsHandlerOptions{Service: &stubLLMOpsService{}, Now: llmOpsTestNow})(rec, httptest.NewRequest(http.MethodGet, "/viewer/llm-ops/model/matrix?model_id=x", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"nodes":[]`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleLLMOpsRefresh(t *testing.T) {
	svc := &stubLLMOpsService{refresh: llmopsapp.RefreshResult{Refreshed: []string{"node-a"}, Failed: map[string]string{"node-b": "HTTP 503"}}}
	h := HandleLLMOpsRefresh(LLMOpsHandlerOptions{Service: svc, Now: llmOpsTestNow})

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/viewer/llm-ops/refresh", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := decodeLLMOpsBody(t, rec)
	if body["generated_at"] != "2026-09-15T12:00:00Z" {
		t.Fatalf("generated_at=%v", body["generated_at"])
	}
	if refreshed := body["refreshed"].([]any); len(refreshed) != 1 || refreshed[0] != "node-a" {
		t.Fatalf("refreshed=%v", body["refreshed"])
	}
	if failed := body["failed"].(map[string]any); failed["node-b"] != "HTTP 503" {
		t.Fatalf("failed=%v", body["failed"])
	}
	if len(svc.lastIDs) != 0 {
		t.Fatalf("empty body must refresh all nodes, got %v", svc.lastIDs)
	}

	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/viewer/llm-ops/refresh", strings.NewReader(`{"node_id":"node-b"}`)))
	if rec.Code != http.StatusOK || len(svc.lastIDs) != 1 || svc.lastIDs[0] != "node-b" {
		t.Fatalf("scoped refresh: status=%d ids=%v", rec.Code, svc.lastIDs)
	}

	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/viewer/llm-ops/refresh", strings.NewReader(`{"node_id":`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid body status=%d", rec.Code)
	}
}

func TestHandleLLMOpsRefreshEmptyCollections(t *testing.T) {
	rec := httptest.NewRecorder()
	HandleLLMOpsRefresh(LLMOpsHandlerOptions{Service: &stubLLMOpsService{}, Now: llmOpsTestNow})(rec, httptest.NewRequest(http.MethodPost, "/viewer/llm-ops/refresh", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"refreshed":[]`) || !strings.Contains(rec.Body.String(), `"failed":{}`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

func ptrFloat(v float64) *float64 { return &v }
