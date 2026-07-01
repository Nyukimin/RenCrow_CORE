package orchestrator

import (
	"sort"
	"strings"

	domainnode "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/node"
)

// NodeSelector chooses a node from candidates based on capability matching.
type NodeSelector struct{}

func NewNodeSelector() *NodeSelector { return &NodeSelector{} }

func (s *NodeSelector) Select(candidates []string, caps map[string]domainnode.ResourceProfile, req domainnode.TaskRequirement) string {
	if len(candidates) == 0 {
		return ""
	}
	matched := make([]string, 0, len(candidates))
	for _, id := range candidates {
		cap, ok := caps[id]
		if !ok {
			continue
		}
		if req.Matches(cap) {
			matched = append(matched, id)
		}
	}
	if len(matched) == 0 {
		return ""
	}
	sort.Strings(matched)
	for _, id := range matched {
		if strings.EqualFold(id, "coder3") {
			// Default preference: coder3(Claude) for broad coding tasks when available.
			return id
		}
	}
	return matched[0]
}

func inferTaskRequirement(msg string) domainnode.TaskRequirement {
	m := strings.ToLower(msg)
	return domainnode.TaskRequirement{
		NeedsAudioOut: strings.Contains(m, "tts") || strings.Contains(m, "audio") || strings.Contains(m, "voice"),
		NeedsBrowser:  strings.Contains(m, "browser") || strings.Contains(m, "chrome") || strings.Contains(m, "canvas"),
		NeedsGPU:      strings.Contains(m, "gpu") || strings.Contains(m, "cuda"),
	}
}
