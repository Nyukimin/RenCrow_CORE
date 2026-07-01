package worker

import "testing"

func TestBuildCoderCapabilityPlansUsesDetectedLLMQualityAndAvailability(t *testing.T) {
	got := BuildCoderCapabilityPlans(
		[]LLMCapability{{ProviderName: "ollama", ModelName: "coder", Available: true, Quality: 4}},
		[]CoderSlotConfig{{Name: "coder1", Provider: "ollama", Model: "coder", Enabled: true}},
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

func TestCoderProviderIsExternal(t *testing.T) {
	tests := []struct {
		provider string
		want     bool
	}{
		{provider: "local_openai", want: false},
		{provider: "ollama", want: false},
		{provider: "openai", want: true},
		{provider: "claude", want: true},
		{provider: "deepseek", want: true},
	}
	for _, tt := range tests {
		if got := CoderProviderIsExternal(tt.provider); got != tt.want {
			t.Fatalf("CoderProviderIsExternal(%q) = %t, want %t", tt.provider, got, tt.want)
		}
	}
}

func TestBuildExternalCoderPolicyNormalizesNames(t *testing.T) {
	got := BuildExternalCoderPolicy([]CoderSlotConfig{
		{Name: " Coder1 ", Provider: "local_openai"},
		{Name: "Coder2", Provider: "openai"},
		{Name: " ", Provider: "claude"},
	})

	if len(got) != 2 {
		t.Fatalf("policy len = %d, want 2: %#v", len(got), got)
	}
	if got["coder1"] {
		t.Fatalf("coder1 should be local: %#v", got)
	}
	if !got["coder2"] {
		t.Fatalf("coder2 should be external: %#v", got)
	}
}

func TestBuildCoderSetupPlansKeepsOrderAndDisabledEntries(t *testing.T) {
	got := BuildCoderSetupPlans([]CoderSlotConfig{
		{Name: " Coder1 ", Enabled: false, DisplayName: "赤", Provider: "local_openai", Model: "one"},
		{Name: "Coder2", Enabled: true, DisplayName: "青", Provider: "openai", Model: "two"},
		{Name: " ", Enabled: true},
	})

	if len(got) != 2 {
		t.Fatalf("plans len = %d, want 2: %#v", len(got), got)
	}
	if got[0].Name != "coder1" || got[0].Enabled || got[0].DisplayName != "赤" {
		t.Fatalf("disabled plan = %#v", got[0])
	}
	if got[1].Name != "coder2" || !got[1].Enabled || got[1].Provider != "openai" || got[1].Model != "two" {
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

func TestBuildCoderCapabilityPlansUsesOverrideThenProviderDefault(t *testing.T) {
	got := BuildCoderCapabilityPlans(nil, []CoderSlotConfig{
		{Name: "coder1", Provider: "openai", Model: "custom", APIKey: "key", Enabled: true},
		{Name: "coder2", Provider: "deepseek", Model: "deepseek-coder", Enabled: true},
	}, map[string]int{"custom": 5})
	if len(got) != 2 {
		t.Fatalf("BuildCoderCapabilityPlans() len = %d, want 2", len(got))
	}
	if got[0].Quality != 5 || !got[0].Available {
		t.Fatalf("override coder plan = %#v", got[0])
	}
	if got[1].Quality != 3 || got[1].Available {
		t.Fatalf("default coder plan = %#v", got[1])
	}
}

func TestBuildCoderCapabilityPlansReturnsNilWhenNoQualityKnown(t *testing.T) {
	got := BuildCoderCapabilityPlans(nil, []CoderSlotConfig{
		{Name: "coder1", Provider: "unknown", Model: "unknown", APIKey: "key", Enabled: true},
	}, nil)
	if got != nil {
		t.Fatalf("BuildCoderCapabilityPlans() = %#v, want nil", got)
	}
}
