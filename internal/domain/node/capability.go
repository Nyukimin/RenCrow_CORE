package node

// ResourceProfile describes hardware resource constraints of an execution node.
// This is distinct from capability.NodeCapabilities, which holds self-detected LLM/tool capability.
type ResourceProfile struct {
	NodeID       string            `json:"node_id"`
	CPUCores     int               `json:"cpu_cores"`
	MemoryMB     int               `json:"memory_mb"`
	HasGPU       bool              `json:"has_gpu"`
	HasAudioOut  bool              `json:"has_audio_out"`
	HasBrowser   bool              `json:"has_browser"`
	NetworkClass string            `json:"network_class"` // offline|limited|full
	Labels       map[string]string `json:"labels,omitempty"`
}

// TaskRequirement describes resource requirements of a task.
type TaskRequirement struct {
	NeedsGPU      bool `json:"needs_gpu"`
	NeedsAudioOut bool `json:"needs_audio_out"`
	NeedsBrowser  bool `json:"needs_browser"`
	MaxLatencyMs  int  `json:"max_latency_ms,omitempty"`
}

func (r TaskRequirement) Matches(cap ResourceProfile) bool {
	if r.NeedsGPU && !cap.HasGPU {
		return false
	}
	if r.NeedsAudioOut && !cap.HasAudioOut {
		return false
	}
	if r.NeedsBrowser && !cap.HasBrowser {
		return false
	}
	return true
}
