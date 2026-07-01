package node

import "testing"

func TestTaskRequirement_Matches(t *testing.T) {
	cap := ResourceProfile{NodeID: "coder2", HasGPU: true, HasAudioOut: false, HasBrowser: true}
	if !(TaskRequirement{NeedsGPU: true}).Matches(cap) {
		t.Fatal("expected GPU requirement to match")
	}
	if (TaskRequirement{NeedsAudioOut: true}).Matches(cap) {
		t.Fatal("expected audio requirement to fail")
	}
	if !(TaskRequirement{NeedsBrowser: true}).Matches(cap) {
		t.Fatal("expected browser requirement to match")
	}
	if (TaskRequirement{NeedsGPU: true, NeedsBrowser: true}).Matches(ResourceProfile{HasGPU: true}) {
		t.Fatal("expected browser requirement to fail")
	}
	if (TaskRequirement{NeedsGPU: true, NeedsAudioOut: true}).Matches(ResourceProfile{HasGPU: true}) {
		t.Fatal("expected audio requirement to fail after GPU passed")
	}
	if !(TaskRequirement{NeedsGPU: true, NeedsAudioOut: true, NeedsBrowser: true}).Matches(ResourceProfile{HasGPU: true, HasAudioOut: true, HasBrowser: true}) {
		t.Fatal("expected combined satisfied requirements to match")
	}
	if !(TaskRequirement{}).Matches(ResourceProfile{}) {
		t.Fatal("empty requirement should match")
	}
}
