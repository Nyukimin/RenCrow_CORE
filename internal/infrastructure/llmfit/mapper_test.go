package llmfit

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domainllmops "github.com/Nyukimin/RenCrow_CORE/internal/domain/llmops"
)

// testdata/system.json, top.json and health.json are real llmfit v1.1.15
// responses (no private addresses; node.name is "unknown-node").
// testdata/system_cli.json is the CLI shape derived from system.json
// ({"providers":...,"system":...}, no "node").
// The inline fixtures below cover edge cases the real node does not exhibit
// (multi GPU, unknown values, object-shaped notes).

func readFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(data)
}

const systemFixtureMultiGPU = `{
 "node":{"name":"gpu-box","os":"linux","extra_unknown":"x"},
 "system":{"total_ram_gb":64,"available_ram_gb":40.5,"cpu_cores":16,"cpu_name":"Ryzen 9",
  "has_gpu":true,"gpu_vram_gb":16,"gpu_available_gb":15,"gpu_name":"RX 6800","gpu_count":2,
  "unified_memory":false,"backend":"vulkan","future_field":{"nested":true},
  "gpus":[{"backend":"vulkan","count":1,"memory_bandwidth_gbps":512,"name":"RX 6800","unified_memory":false,"vram_gb":16,"unknown":1},
          {"backend":"vulkan","count":2,"memory_bandwidth_gbps":null,"name":"RX 6800 XT","unified_memory":false,"vram_gb":16.0}]}}`

// systemFixtureGroupedGPUs mirrors a node with four identical cards: llmfit
// groups them into one gpus[] element with count=4 and reports no free VRAM.
const systemFixtureGroupedGPUs = `{
 "node":{"name":"unknown-node","os":"linux"},
 "system":{"total_ram_gb":62.7,"available_ram_gb":55.1,"cpu_cores":16,"cpu_name":"Ryzen 9",
  "has_gpu":true,"gpu_vram_gb":15.98,"gpu_available_gb":null,"gpu_name":"Radeon RX 6800/6800 XT / 6900 XT","gpu_count":4,
  "unified_memory":false,"backend":"Vulkan",
  "gpus":[{"backend":"Vulkan","count":4,"memory_bandwidth_gbps":512,"name":"Radeon RX 6800/6800 XT / 6900 XT","unified_memory":false,"vram_gb":15.98}]}}`

// systemFixtureZeroAvailable reports a single GPU whose free VRAM is exactly 0.
// A reported zero must stay distinct from "not reported" (null).
const systemFixtureZeroAvailable = `{
 "node":{"name":"busy","os":"linux"},
 "system":{"total_ram_gb":32,"available_ram_gb":10,"cpu_cores":8,"cpu_name":"x",
  "has_gpu":true,"gpu_vram_gb":24,"gpu_available_gb":0,"gpu_name":"RTX 3090","gpu_count":1,
  "unified_memory":false,"backend":"cuda",
  "gpus":[{"backend":"cuda","count":1,"memory_bandwidth_gbps":936,"name":"RTX 3090","unified_memory":false,"vram_gb":24}]}}`

const systemFixtureScalarFallback = `{
 "node":{"name":"unknown-node","os":"macos"},
 "system":{"total_ram_gb":32,"available_ram_gb":20,"cpu_cores":10,"cpu_name":"Apple M2",
  "has_gpu":true,"gpu_vram_gb":32,"gpu_available_gb":20,"gpu_name":"Apple M2 GPU","gpu_count":1,
  "unified_memory":true,"backend":"metal","gpus":[]}}`

const systemFixtureNoGPU = `{
 "node":{"name":"cpu-box","os":"linux"},
 "system":{"total_ram_gb":8,"available_ram_gb":4,"cpu_cores":4,"cpu_name":"Celeron",
  "has_gpu":false,"gpu_vram_gb":null,"gpu_available_gb":null,"gpu_name":null,"gpu_count":0,
  "unified_memory":false,"backend":"cpu","gpus":[]}}`

const systemFixtureUnknownGPUShape = `{
 "node":{"name":"odd","os":"linux"},
 "system":{"total_ram_gb":8,"available_ram_gb":4,"cpu_cores":4,"cpu_name":"x",
  "has_gpu":true,"gpu_vram_gb":12,"gpu_available_gb":11,"gpu_name":"RTX 3060","gpu_count":1,
  "unified_memory":false,"backend":"cuda","gpus":[{"weird_key":"value"},"a-string",42,null]}}`

const modelsFixture = `{
 "node":{"name":"unknown-node","os":"linux"},"system":{},"total_models":4,"returned_models":4,"filters":{},
 "models":[
  {"name":"qwen3-coder-30b","provider":"Qwen","parameter_count":"30B","params_b":30.5,
   "context_length":262144,"usable_context":65536,"effective_context_length":32768,
   "use_case":"Code generation","category":"Coding","release_date":"2026-01-01","is_moe":true,
   "fit_level":"good","fit_label":"Good","run_mode":"gpu","run_mode_label":"GPU","score":81.5,
   "score_components":{"quality":90,"speed":70,"fit":80,"context":85},
   "estimated_tps":42.5,"estimate_confidence":"calibrated","estimate_confidence_label":"Calibrated",
   "prefill_tps":900,"ttft_ms":120,"runtime":"llamacpp","runtime_label":"llama.cpp","best_quant":"Q4_K_M",
   "memory_required_gb":18.2,"memory_available_gb":31,"utilization_pct":58.7,"moe_offloaded_gb":2.5,"total_memory_gb":18.2,
   "notes":["fits with room"],"gguf_sources":[],"capabilities":["chat","tools"],"capability_ids":["chat","tools"],
   "license":"apache-2.0","supports_tp":[],"installed":true,"disk_size_gb":17.9,"ollama_name":"qwen3-coder:30b",
   "estimate_basis":{"method":"bandwidth","gpu_bandwidth_gbps":512,"ddr_bandwidth_gbps":null,"local_calibration":null,"efficiency":0.6,"assumed_context":32768},
   "verify_command":"llmfit bench qwen3-coder-30b","measured_tps":38.1,"brand_new_key":123},
  {"name":"gemma-4-27b","provider":"Google","parameter_count":"27B","params_b":27,
   "context_length":131072,"usable_context":0,"effective_context_length":0,
   "is_moe":false,"fit_level":"too_tight","run_mode":"cpu","score":0,
   "score_components":{"quality":0,"speed":0,"fit":0,"context":0},
   "estimated_tps":0,"estimate_confidence":"unsupported","prefill_tps":null,"ttft_ms":null,
   "runtime":"","best_quant":null,"memory_required_gb":60,"memory_available_gb":31,"utilization_pct":193.5,
   "notes":[],"capabilities":[],"capability_ids":[],"license":null,"installed":false,"disk_size_gb":0,"measured_tps":null,"moe_offloaded_gb":null},
  {"name":"future-model","provider":"X","parameter_count":"1B","params_b":1,
   "fit_level":"perfect","score":99,"estimate_confidence":"oracle_v2","estimated_tps":null,
   "capabilities":[{"id":"vision","label":"Vision"}],"notes":[{"message":"object note"}]},
  {"provider":"NoName","params_b":1}
 ]}`

func TestDecodeRealSystemFixture(t *testing.T) {
	resp, err := decodeSystem(strings.NewReader(readFixture(t, "system.json")))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	at := time.Date(2026, 9, 15, 1, 2, 3, 0, time.UTC)
	profile := mapSystem("node-a", resp, at)

	if profile.NodeID != "node-a" || profile.NodeName != "node-a" {
		t.Fatalf("configured node id must win over llmfit's unknown-node: %+v", profile)
	}
	if profile.OS != "linux" || profile.CPUCores != 4 || !strings.HasPrefix(profile.CPUName, "Intel(R) Core(TM) i5-2520M") {
		t.Fatalf("cpu/os mismatch: %+v", profile)
	}
	if profile.TotalRAMGB != 7.67 || profile.AvailableRAMGB != 3.78 {
		t.Fatalf("ram mismatch: %+v", profile)
	}
	if !profile.HasGPU || profile.GPUCount != 1 || profile.Backend != "SYCL" || !profile.UnifiedMemory {
		t.Fatalf("gpu summary mismatch: %+v", profile)
	}
	if len(profile.GPUs) != 1 {
		t.Fatalf("gpus=%d want 1: %+v", len(profile.GPUs), profile.GPUs)
	}
	gpu := profile.GPUs[0]
	if !strings.HasPrefix(gpu.Name, "Intel 2nd Generation Core Processor") || gpu.VRAMGB != 7.67 || gpu.Count != 1 {
		t.Fatalf("gpu mismatch: %+v", gpu)
	}
	if gpu.MemoryBandwidthGBs != 0 {
		t.Fatalf("null memory_bandwidth_gbps must map to 0, got %v", gpu.MemoryBandwidthGBs)
	}
	if gpu.AvailableVRAMGB != nil {
		t.Fatalf("null gpu_available_gb must map to nil (unknown), got %v", *gpu.AvailableVRAMGB)
	}
	if !profile.CollectedAt.Equal(at) || profile.Source != domainllmops.SourceLLMFit {
		t.Fatalf("collected_at/source mismatch: %+v", profile)
	}
}

func TestDecodeCLISystemFixtureWithoutNode(t *testing.T) {
	resp, err := decodeSystem(strings.NewReader(readFixture(t, "system_cli.json")))
	if err != nil {
		t.Fatalf("decode must tolerate the CLI shape (providers, no node): %v", err)
	}
	profile := mapSystem("local", resp, time.Now())
	if profile.NodeName != "local" || profile.OS != "" {
		t.Fatalf("CLI shape must fall back to the configured id and leave OS empty: %+v", profile)
	}
	if profile.Backend != "SYCL" || len(profile.GPUs) != 1 || profile.GPUs[0].VRAMGB != 7.67 {
		t.Fatalf("system block must still map: %+v", profile)
	}
}

func TestDecodeRealHealthFixture(t *testing.T) {
	resp, err := decodeHealth(strings.NewReader(readFixture(t, "health.json")))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "ok" || resp.Node.Name != "unknown-node" || resp.Node.OS != "linux" {
		t.Fatalf("health mismatch: %+v", resp)
	}
}

func TestMapSystemMultiGPU(t *testing.T) {
	resp, err := decodeSystem(strings.NewReader(systemFixtureMultiGPU))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	profile := mapSystem("node-a", resp, time.Now())
	if profile.NodeName != "node-a" {
		t.Fatalf("configured id must be the node name even when llmfit reports a host name: %q", profile.NodeName)
	}
	if len(profile.GPUs) != 2 {
		t.Fatalf("gpus=%d want 2: %+v", len(profile.GPUs), profile.GPUs)
	}
	if profile.GPUs[0] != (domainllmops.GPUProfile{Name: "RX 6800", VRAMGB: 16, MemoryBandwidthGBs: 512, Count: 1}) {
		t.Fatalf("gpu[0] mismatch: %+v", profile.GPUs[0])
	}
	if profile.GPUs[1] != (domainllmops.GPUProfile{Name: "RX 6800 XT", VRAMGB: 16, Count: 2}) {
		t.Fatalf("gpu[1] mismatch: %+v", profile.GPUs[1])
	}
	if profile.GPUCount != 3 {
		t.Fatalf("gpu_count must not be below the described total (1+2), got %d", profile.GPUCount)
	}
	for _, gpu := range profile.GPUs {
		if gpu.AvailableVRAMGB != nil {
			t.Fatalf("node-wide gpu_available_gb must not be attributed to one of several GPUs: %+v", gpu)
		}
	}
}

func TestMapSystemKeepsGroupedGPUCountAndUnknownAvailableVRAM(t *testing.T) {
	resp, err := decodeSystem(strings.NewReader(systemFixtureGroupedGPUs))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	profile := mapSystem("quad", resp, time.Now())
	if len(profile.GPUs) != 1 {
		t.Fatalf("four identical cards must stay one grouped entry, got %d: %+v", len(profile.GPUs), profile.GPUs)
	}
	gpu := profile.GPUs[0]
	if gpu.Name != "Radeon RX 6800/6800 XT / 6900 XT" || gpu.Count != 4 || gpu.VRAMGB != 15.98 || gpu.MemoryBandwidthGBs != 512 {
		t.Fatalf("grouped entry must keep count=4 and per-card vram: %+v", gpu)
	}
	if profile.GPUCount != 4 {
		t.Fatalf("gpu_count=%d want 4", profile.GPUCount)
	}
	if total := gpu.VRAMGB * float64(gpu.Count); math.Abs(total-63.92) > 1e-9 {
		t.Fatalf("total VRAM must be reconstructible as vram_gb*count (63.92), got %v", total)
	}
	if gpu.AvailableVRAMGB != nil {
		t.Fatalf("null gpu_available_gb must map to nil (unknown), not %v", *gpu.AvailableVRAMGB)
	}
}

func TestMapSystemKeepsReportedZeroAvailableVRAM(t *testing.T) {
	resp, err := decodeSystem(strings.NewReader(systemFixtureZeroAvailable))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	profile := mapSystem("busy", resp, time.Now())
	if len(profile.GPUs) != 1 {
		t.Fatalf("gpus=%d want 1: %+v", len(profile.GPUs), profile.GPUs)
	}
	gpu := profile.GPUs[0]
	if gpu.AvailableVRAMGB == nil || *gpu.AvailableVRAMGB != 0 {
		t.Fatalf("a reported gpu_available_gb of 0 must map to &0 (known zero), got %v", gpu.AvailableVRAMGB)
	}
}

func TestMapSystemFallsBackToScalarGPUFields(t *testing.T) {
	resp, err := decodeSystem(strings.NewReader(systemFixtureScalarFallback))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	profile := mapSystem("mac", resp, time.Now())
	if !profile.UnifiedMemory || profile.Backend != "metal" || profile.NodeName != "mac" {
		t.Fatalf("unified memory / backend / name mismatch: %+v", profile)
	}
	if len(profile.GPUs) != 1 || profile.GPUCount != 1 {
		t.Fatalf("expected one fallback GPU: %+v", profile)
	}
	gpu := profile.GPUs[0]
	if gpu.Name != "Apple M2 GPU" || gpu.VRAMGB != 32 || gpu.Count != 1 || gpu.MemoryBandwidthGBs != 0 {
		t.Fatalf("fallback gpu mismatch: %+v", gpu)
	}
	if gpu.AvailableVRAMGB == nil || *gpu.AvailableVRAMGB != 20 {
		t.Fatalf("single GPU gets node-wide gpu_available_gb: %+v", gpu)
	}
}

func TestMapSystemUnknownGPUShapeFallsBackWithoutError(t *testing.T) {
	resp, err := decodeSystem(strings.NewReader(systemFixtureUnknownGPUShape))
	if err != nil {
		t.Fatalf("decode must tolerate unknown gpus element shapes: %v", err)
	}
	profile := mapSystem("odd", resp, time.Now())
	if len(profile.GPUs) != 1 || profile.GPUs[0].Name != "RTX 3060" || profile.GPUs[0].VRAMGB != 12 {
		t.Fatalf("expected scalar fallback GPU: %+v", profile.GPUs)
	}
	if free := profile.GPUs[0].AvailableVRAMGB; free == nil || *free != 11 {
		t.Fatalf("single fallback GPU gets node-wide gpu_available_gb: %+v", profile.GPUs[0])
	}
}

func TestMapSystemWithoutGPUKeepsEmptySlice(t *testing.T) {
	resp, err := decodeSystem(strings.NewReader(systemFixtureNoGPU))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	profile := mapSystem("cpu-box", resp, time.Now())
	if profile.HasGPU || profile.GPUCount != 0 || profile.GPUs == nil || len(profile.GPUs) != 0 {
		t.Fatalf("no-gpu node must have empty (non-nil) GPUs: %+v", profile)
	}
}

func TestResolveNodeName(t *testing.T) {
	if got := resolveNodeName("node-a", "unknown-node"); got != "node-a" {
		t.Fatalf("got %q", got)
	}
	if got := resolveNodeName("node-a", "real-host"); got != "node-a" {
		t.Fatalf("configured id must win, got %q", got)
	}
	if got := resolveNodeName("", "Unknown-Node"); got != "" {
		t.Fatalf("unknown-node without an id must be blank, got %q", got)
	}
	if got := resolveNodeName("", " real-host "); got != "real-host" {
		t.Fatalf("reported name is the last resort, got %q", got)
	}
}

func TestDecodeRealTopFixture(t *testing.T) {
	resp, err := decodeModels(strings.NewReader(readFixture(t, "top.json")))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.TotalModels != 4922 || resp.ReturnedModels != 2 || len(resp.Models) != 2 {
		t.Fatalf("envelope mismatch: total=%d returned=%d models=%d", resp.TotalModels, resp.ReturnedModels, len(resp.Models))
	}
	at := time.Date(2026, 9, 15, 4, 5, 6, 0, time.UTC)
	models, skipped := mapModels("node-a", resp, at)
	if skipped != 0 || len(models) != 2 {
		t.Fatalf("models=%d skipped=%d", len(models), skipped)
	}
	m := models[0]
	if m.NodeID != "node-a" || m.ModelID != "khazarai/Qwen3-4B-Qwen3.6-plus-Reasoning-Distilled-GGUF" || m.Provider != "khazarai" || m.ParameterCount != "3.1B" || m.ParamsB != 3.13 || m.IsMoE {
		t.Fatalf("identity mismatch: %+v", m)
	}
	if m.FitLevel != domainllmops.FitLevelPerfect || m.Score != 85.2 || m.QualityScore != 73 || m.SpeedScore != 100 || m.FitScore != 100 || m.ContextScore != 100 {
		t.Fatalf("scores mismatch: %+v", m)
	}
	if m.Runtime != "llamacpp" || m.RunMode != "gpu" || m.BestQuant == nil || *m.BestQuant != "Q8_0" {
		t.Fatalf("runtime/quant mismatch: %+v", m)
	}
	if m.ContextLength != 262144 || m.UsableContext != 113169 || m.EffectiveContextLength != 8192 {
		t.Fatalf("native/usable/evaluated context mismatch: %+v", m)
	}
	if m.MemoryRequiredGB != 4.06 || m.MemoryAvailableGB != 7.67 || m.UtilizationPct != 53 {
		t.Fatalf("memory mismatch: %+v", m)
	}
	if m.EstimatedTPS == nil || *m.EstimatedTPS != 25.6 || m.EstimateConfidence != domainllmops.EstimateConfidenceEstimated {
		t.Fatalf("estimate mismatch: %+v", m)
	}
	if m.MeasuredTPS != nil || m.PrefillTPS != nil || m.TTFTMs != nil {
		t.Fatalf("null measured/prefill/ttft must map to nil: %+v", m)
	}
	if m.License != "" {
		t.Fatalf("null license must map to empty string, got %q", m.License)
	}
	if m.Installed || m.DiskSizeGB == nil || *m.DiskSizeGB != 3.28 {
		t.Fatalf("installed/disk mismatch: %+v", m)
	}
	if len(m.Capabilities) != 1 || m.Capabilities[0] != "tool_use" || len(m.Notes) != 4 {
		t.Fatalf("capabilities/notes mismatch: %+v", m)
	}
	if !m.CollectedAt.Equal(at) {
		t.Fatalf("collected_at mismatch: %v", m.CollectedAt)
	}
}

func TestDecodeModelsMapsNullsAndZeroes(t *testing.T) {
	resp, err := decodeModels(strings.NewReader(modelsFixture))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	at := time.Date(2026, 9, 15, 4, 5, 6, 0, time.UTC)
	models, skipped := mapModels("node-a", resp, at)
	if skipped != 1 {
		t.Fatalf("skipped=%d want 1 (element without name)", skipped)
	}
	if len(models) != 3 {
		t.Fatalf("models=%d want 3", len(models))
	}

	full := models[0]
	if full.ModelID != "qwen3-coder-30b" || full.Provider != "Qwen" || full.ParamsB != 30.5 || !full.IsMoE || full.FitLevel != domainllmops.FitLevelGood {
		t.Fatalf("identity mismatch: %+v", full)
	}
	if full.EstimatedTPS == nil || *full.EstimatedTPS != 42.5 || full.MeasuredTPS == nil || *full.MeasuredTPS != 38.1 {
		t.Fatalf("tps mismatch: %+v", full)
	}
	if full.PrefillTPS == nil || *full.PrefillTPS != 900 || full.TTFTMs == nil || *full.TTFTMs != 120 {
		t.Fatalf("prefill/ttft mismatch: %+v", full)
	}
	if full.EstimateConfidence != domainllmops.EstimateConfidenceCalibrated || !full.Installed || full.DiskSizeGB == nil || *full.DiskSizeGB != 17.9 || full.License != "apache-2.0" {
		t.Fatalf("confidence/installed/disk/license mismatch: %+v", full)
	}

	tight := models[1]
	if tight.FitLevel != domainllmops.FitLevelTooTight || tight.EstimateConfidence != domainllmops.EstimateConfidenceUnsupported {
		t.Fatalf("too_tight mismatch: %+v", tight)
	}
	if tight.BestQuant != nil {
		t.Fatalf("best_quant null must map to nil, got %q", *tight.BestQuant)
	}
	if tight.MeasuredTPS != nil || tight.PrefillTPS != nil || tight.TTFTMs != nil {
		t.Fatalf("null tps fields must map to nil: %+v", tight)
	}
	if tight.EstimatedTPS == nil || *tight.EstimatedTPS != 0 {
		t.Fatalf("estimated_tps 0 must be kept as reported zero, got %v", tight.EstimatedTPS)
	}
	if tight.DiskSizeGB == nil || *tight.DiskSizeGB != 0 {
		t.Fatalf("disk_size_gb 0 must be kept as reported zero, got %v", tight.DiskSizeGB)
	}
	if tight.License != "" {
		t.Fatalf("null license must map to empty string, got %q", tight.License)
	}
	if tight.Capabilities == nil || len(tight.Capabilities) != 0 || tight.Notes == nil || len(tight.Notes) != 0 {
		t.Fatalf("empty arrays must stay empty non-nil slices: %+v", tight)
	}

	future := models[2]
	if future.EstimateConfidence != domainllmops.EstimateConfidence("oracle_v2") {
		t.Fatalf("unknown estimate_confidence must be preserved, got %q", future.EstimateConfidence)
	}
	if future.EstimatedTPS != nil {
		t.Fatal("estimated_tps null must map to nil")
	}
	if len(future.Capabilities) != 1 || future.Capabilities[0] != "vision" {
		t.Fatalf("object capabilities must be reduced to ids: %+v", future.Capabilities)
	}
	if len(future.Notes) != 1 || future.Notes[0] != "object note" {
		t.Fatalf("object notes must be reduced to text: %+v", future.Notes)
	}
	if future.ContextLength != 0 || future.BestQuant != nil || future.DiskSizeGB != nil {
		t.Fatalf("missing fields must stay zero / nil: %+v", future)
	}
}

func TestDecodeModelsRejectsInvalidJSON(t *testing.T) {
	if _, err := decodeModels(strings.NewReader(`{"models":[`)); err == nil {
		t.Fatal("truncated JSON must fail")
	}
	if _, err := decodeSystem(strings.NewReader(`not json`)); err == nil {
		t.Fatal("non-JSON must fail")
	}
}

func TestDecodeErrorBody(t *testing.T) {
	if got := decodeErrorMessage([]byte(`{"error":"unknown runtime"}`)); got != "unknown runtime" {
		t.Fatalf("error message=%q", got)
	}
	if got := decodeErrorMessage([]byte(`<html>bad gateway</html>`)); got != "" {
		t.Fatalf("non-JSON error body must yield empty message, got %q", got)
	}
}
