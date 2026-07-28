package worker

import "testing"

func TestBuildCoderCapabilityPlansUsesDetectedAliasQualityAndAvailability(t *testing.T) {
	got := BuildCoderCapabilityPlans(
		[]LLMCapability{{ProviderName: "rencrow_llm", ModelName: "coder1", Available: true, Quality: 4}},
		[]CoderSlotConfig{{Name: "coder1", Enabled: true}},
		nil,
	)
	if len(got) != 1 {
		t.Fatalf("BuildCoderCapabilityPlans() len = %d, want 1", len(got))
	}
	if got[0].Name != "coder1" || got[0].Quality != 4 || !got[0].Available {
		t.Fatalf("BuildCoderCapabilityPlans() = %#v", got)
	}
}

func TestCoderSlotNamePolicy(t *testing.T) {
	if got := NormalizeCoderSlotName(" Coder2 "); got != "coder2" {
		t.Fatalf("NormalizeCoderSlotName() = %q", got)
	}
	if got := CoderSlotIndex("coder1"); got != 0 {
		t.Fatalf("CoderSlotIndex(coder1) = %d", got)
	}
	if got := CoderSlotIndex(" Coder4 "); got != 3 {
		t.Fatalf("CoderSlotIndex(coder4) = %d", got)
	}
	if got := CoderSlotIndex("coder5"); got != -1 {
		t.Fatalf("CoderSlotIndex(coder5) = %d", got)
	}
}

func TestBuildCoderSetupPlansKeepsOrderAndDisabledEntries(t *testing.T) {
	got := BuildCoderSetupPlans([]CoderSlotConfig{
		{Name: " Coder1 ", Enabled: false, DisplayName: "赤"},
		{Name: "Coder2", Enabled: true, DisplayName: "青"},
		{Name: " ", Enabled: true},
	})

	if len(got) != 2 {
		t.Fatalf("plans len = %d, want 2: %#v", len(got), got)
	}
	if got[0].Name != "coder1" || got[0].Enabled || got[0].DisplayName != "赤" {
		t.Fatalf("disabled plan = %#v", got[0])
	}
	if got[1].Name != "coder2" || !got[1].Enabled || got[1].DisplayName != "青" {
		t.Fatalf("enabled plan = %#v", got[1])
	}
}

func TestBuildCoderSetupPlansInitializesSharedLightMemoryOnce(t *testing.T) {
	got := BuildCoderSetupPlans([]CoderSlotConfig{
		{Name: "coder1", Enabled: true, LightMemoryEnabled: true, LightMemoryMaxTurns: 0},
		{Name: "coder2", Enabled: true, LightMemoryEnabled: true, LightMemoryMaxTurns: 9},
		{Name: "coder3", Enabled: true, LightMemoryEnabled: false},
	})

	if len(got) != 3 {
		t.Fatalf("plans len = %d, want 3: %#v", len(got), got)
	}
	if !got[0].UseLightMemory || !got[0].InitializeSharedLightMemory || got[0].SharedLightMemoryMaxTurns != DefaultLightMemoryMaxTurns {
		t.Fatalf("first light memory plan = %#v", got[0])
	}
	if !got[1].UseLightMemory || got[1].InitializeSharedLightMemory || got[1].SharedLightMemoryMaxTurns != 9 {
		t.Fatalf("second light memory plan = %#v", got[1])
	}
	if got[2].UseLightMemory || got[2].InitializeSharedLightMemory {
		t.Fatalf("third plan should not use light memory: %#v", got[2])
	}
}

func TestBuildCoderCapabilityPlansUsesSlotOverride(t *testing.T) {
	got := BuildCoderCapabilityPlans(nil, []CoderSlotConfig{
		{Name: "coder1", Enabled: true},
		{Name: "coder2", Enabled: true},
	}, map[string]int{"coder1": 5})
	if len(got) != 2 {
		t.Fatalf("BuildCoderCapabilityPlans() len = %d, want 2", len(got))
	}
	if got[0].Quality != 5 || got[0].Available {
		t.Fatalf("override coder plan = %#v", got[0])
	}
	if got[1].Quality != 0 || got[1].Available {
		t.Fatalf("unknown coder plan = %#v", got[1])
	}
}

func TestBuildCoderCapabilityPlansReturnsNilWhenNoQualityKnown(t *testing.T) {
	got := BuildCoderCapabilityPlans(nil, []CoderSlotConfig{
		{Name: "coder1", Enabled: true},
	}, nil)
	if got != nil {
		t.Fatalf("BuildCoderCapabilityPlans() = %#v, want nil", got)
	}
}
