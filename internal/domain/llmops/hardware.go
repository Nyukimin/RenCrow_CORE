// Package llmops holds the RenCrow-side domain model for LLM Ops / Hardware
// Capability observations. llmfit is the observation source; this package must
// not import llmfit DTOs, infrastructure packages, or net/http.
package llmops

import "time"

// SourceLLMFit is the observation source recorded on every Phase 1 profile.
const SourceLLMFit = "llmfit"

// NodeHardwareProfile is the hardware capability snapshot of one LLM node as
// observed by llmfit. Field layout follows the implementation spec chapter 3.
type NodeHardwareProfile struct {
	NodeID   string
	NodeName string
	OS       string

	CPUName  string
	CPUCores int

	TotalRAMGB     float64
	AvailableRAMGB float64

	HasGPU   bool
	GPUCount int
	GPUs     []GPUProfile

	UnifiedMemory bool
	Backend       string

	CollectedAt time.Time
	Source      string
}

// GPUProfile describes one GPU kind attached to a node. Count is the number
// of identical devices llmfit groups under this entry (llmfit reports gpus[]
// per kind with a count). AvailableVRAMGB is 0 when llmfit does not report a
// per-GPU free value (it only reports a node-wide gpu_available_gb).
type GPUProfile struct {
	Name               string
	VRAMGB             float64
	AvailableVRAMGB    float64
	MemoryBandwidthGBs float64
	Count              int
}
