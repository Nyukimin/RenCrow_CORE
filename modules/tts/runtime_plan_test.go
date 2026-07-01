package tts

import (
	"reflect"
	"testing"
	"time"
)

func TestRuntimeProviderPriorityNormalizesConfiguredNames(t *testing.T) {
	got := RuntimeProviderPriority(RuntimeConfig{
		ProviderPriority: []string{" IRODORI ", "", " Eleven "},
	})
	want := []string{RuntimeProviderIrodori, RuntimeProviderEleven}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RuntimeProviderPriority() = %#v, want %#v", got, want)
	}
}

func TestRuntimeProviderPriorityDefaultsToIrodori(t *testing.T) {
	got := RuntimeProviderPriority(RuntimeConfig{
		ProviderPriority: []string{" ", "\t"},
	})
	want := []string{RuntimeProviderIrodori}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RuntimeProviderPriority() = %#v, want %#v", got, want)
	}
}

func TestBuildCommandSpecsTrimsNamesAndCopiesArgs(t *testing.T) {
	args := []string{"-q", "{file}"}
	got := BuildCommandSpecs([]CommandSpec{
		{Name: "  play  ", Args: args},
		{Name: " ", Args: []string{"ignored"}},
	})
	want := []CommandSpec{{Name: "play", Args: []string{"-q", "{file}"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildCommandSpecs() = %#v, want %#v", got, want)
	}
	args[0] = "mutated"
	if got[0].Args[0] != "-q" {
		t.Fatalf("BuildCommandSpecs() did not copy args: %#v", got[0].Args)
	}
}

func TestChooseRuntimeVoiceIDPrefersConfiguredIrodoriVoice(t *testing.T) {
	got := ChooseRuntimeVoiceID(RuntimeConfig{
		VoiceID: "default",
		Irodori: IrodoriRuntimeConfig{
			Enabled: true,
			VoiceID: "irodori",
		},
	})
	if got != "irodori" {
		t.Fatalf("ChooseRuntimeVoiceID() = %q, want irodori", got)
	}
}

func TestChooseRuntimeVoiceIDFallsBackToDefaultVoice(t *testing.T) {
	got := ChooseRuntimeVoiceID(RuntimeConfig{
		VoiceID: "default",
		Irodori: IrodoriRuntimeConfig{
			Enabled: true,
			VoiceID: " ",
		},
	})
	if got != "default" {
		t.Fatalf("ChooseRuntimeVoiceID() = %q, want default", got)
	}
}

func TestRuntimeProviderSelectionLogMessage(t *testing.T) {
	got, ok := RuntimeProviderSelectionLogMessage(RuntimeProviderSelectionLogInput{
		Name:     " IRODORI ",
		BaseURL:  " http://127.0.0.1:7870 ",
		Endpoint: " /v1/audio/speech ",
	})
	if !ok || got != "TTS Irodori bridge enabled (base=http://127.0.0.1:7870 endpoint=/v1/audio/speech)" {
		t.Fatalf("RuntimeProviderSelectionLogMessage() = %q,%t", got, ok)
	}
	if _, ok := RuntimeProviderSelectionLogMessage(RuntimeProviderSelectionLogInput{Name: "unknown"}); ok {
		t.Fatal("unknown provider should not produce selection log")
	}
}

func TestBuildRuntimeProviderPlanBuildsIrodoriPlan(t *testing.T) {
	plan, ok := BuildRuntimeProviderPlan(RuntimeConfig{
		Irodori: IrodoriRuntimeConfig{
			Enabled:        true,
			BaseURL:        "http://127.0.0.1:7870",
			EndpointPath:   "/v1/audio/speech",
			VoiceID:        "mio",
			TimeoutSec:     12,
			NumSteps:       20,
			ContextKVCache: true,
		},
	}, " IRODORI ", false)
	if !ok {
		t.Fatal("BuildRuntimeProviderPlan() ok = false, want true")
	}
	if !plan.Available || plan.Name != RuntimeProviderIrodori {
		t.Fatalf("BuildRuntimeProviderPlan() = %#v, want available irodori", plan)
	}
	if plan.Irodori.BaseURL != "http://127.0.0.1:7870" || plan.Irodori.Timeout != 12*time.Second {
		t.Fatalf("Irodori plan mismatch: %#v", plan.Irodori)
	}
	if !plan.Irodori.ContextKVCache || plan.Irodori.NumSteps != 20 {
		t.Fatalf("Irodori detail fields were not preserved: %#v", plan.Irodori)
	}
}

func TestBuildRuntimeProviderPlanReportsUnavailableProvidersWhenRequested(t *testing.T) {
	plan, ok := BuildRuntimeProviderPlan(RuntimeConfig{}, RuntimeProviderAzure, true)
	if !ok {
		t.Fatal("BuildRuntimeProviderPlan() ok = false, want true")
	}
	if plan.Available || plan.Name != RuntimeProviderAzure || plan.Unavailable == "" {
		t.Fatalf("BuildRuntimeProviderPlan() = %#v, want unavailable azure", plan)
	}

	_, ok = BuildRuntimeProviderPlan(RuntimeConfig{}, RuntimeProviderAzure, false)
	if ok {
		t.Fatal("BuildRuntimeProviderPlan() ok = true, want false without includeUnavailable")
	}
}

func TestBuildRuntimeProviderPlanReportsUnavailableIrodoriWhenRequested(t *testing.T) {
	plan, ok := BuildRuntimeProviderPlan(RuntimeConfig{
		Irodori: IrodoriRuntimeConfig{Enabled: true},
	}, RuntimeProviderIrodori, true)
	if !ok {
		t.Fatal("BuildRuntimeProviderPlan() ok = false, want true")
	}
	if plan.Available || plan.Name != RuntimeProviderIrodori || plan.Unavailable != "irodori is not configured" {
		t.Fatalf("BuildRuntimeProviderPlan() = %#v, want unavailable irodori", plan)
	}
}

func TestBuildRuntimeProviderPlansUsesPriorityOrder(t *testing.T) {
	plans := BuildRuntimeProviderPlans(RuntimeConfig{
		ProviderPriority: []string{RuntimeProviderAzure, RuntimeProviderIrodori},
		Irodori: IrodoriRuntimeConfig{
			Enabled: true,
			BaseURL: "http://127.0.0.1:7870",
		},
	}, true)
	if len(plans) != 2 {
		t.Fatalf("len(plans) = %d, want 2: %#v", len(plans), plans)
	}
	if plans[0].Name != RuntimeProviderAzure || plans[0].Available {
		t.Fatalf("first plan = %#v, want unavailable azure", plans[0])
	}
	if plans[1].Name != RuntimeProviderIrodori || !plans[1].Available {
		t.Fatalf("second plan = %#v, want available irodori", plans[1])
	}
}

func TestFirstRuntimeProviderPlan(t *testing.T) {
	plan, ok := FirstRuntimeProviderPlan(RuntimeConfig{
		Irodori: IrodoriRuntimeConfig{
			Enabled: true,
			BaseURL: "http://127.0.0.1:7870",
		},
	}, false)
	if !ok || plan.Name != RuntimeProviderIrodori || !plan.Available {
		t.Fatalf("FirstRuntimeProviderPlan() = %#v,%t", plan, ok)
	}
}
