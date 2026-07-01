package orchestrator

import (
	"testing"

	domainnode "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/node"
)

func TestNodeSelector_Select(t *testing.T) {
	s := NewNodeSelector()
	caps := map[string]domainnode.ResourceProfile{
		"coder1": {NodeID: "coder1", HasAudioOut: false},
		"coder2": {NodeID: "coder2", HasAudioOut: true},
		"coder3": {NodeID: "coder3", HasAudioOut: true},
	}
	got := s.Select([]string{"coder1", "coder2", "coder3"}, caps, domainnode.TaskRequirement{NeedsAudioOut: true})
	if got != "coder3" {
		t.Fatalf("expected coder3, got %s", got)
	}
}

func TestInferTaskRequirement(t *testing.T) {
	req := inferTaskRequirement("TTSを実装して。Chromeで確認")
	if !req.NeedsAudioOut || !req.NeedsBrowser {
		t.Fatalf("unexpected requirements: %+v", req)
	}
}
