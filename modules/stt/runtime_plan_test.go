package stt

import (
	"testing"
	"time"
)

func TestBuildRuntimeProviderPlanDisabled(t *testing.T) {
	_, ok := BuildRuntimeProviderPlan(RuntimeConfig{})
	if ok {
		t.Fatal("BuildRuntimeProviderPlan() ok = true, want false")
	}
}

func TestBuildRuntimeProviderPlanMapsProviderOptions(t *testing.T) {
	plan, ok := BuildRuntimeProviderPlan(RuntimeConfig{
		Enabled:     true,
		Provider:    " external_http ",
		Language:    " ja ",
		Model:       " whisper-large-v3 ",
		TimeoutMS:   12000,
		BusyPolicy:  " queue_latest ",
		ProviderURL: " http://127.0.0.1:8766/stt/file ",
		SaveAudio:   true,
	})
	if !ok {
		t.Fatal("BuildRuntimeProviderPlan() ok = false, want true")
	}
	if !plan.Enabled || plan.Provider != ProviderExternalHTTP || plan.Language != "ja" || plan.Model != "whisper-large-v3" {
		t.Fatalf("BuildRuntimeProviderPlan() basic fields = %#v", plan)
	}
	if plan.Timeout != 12*time.Second || plan.BusyPolicy != "queue_latest" || !plan.SaveAudio {
		t.Fatalf("BuildRuntimeProviderPlan() option fields = %#v", plan)
	}
	if plan.ExternalHTTPURL != "http://127.0.0.1:8766/stt/file" {
		t.Fatalf("BuildRuntimeProviderPlan() ExternalHTTPURL = %q", plan.ExternalHTTPURL)
	}
}

func TestBuildRuntimeProviderPlanAppliesProviderDefaults(t *testing.T) {
	plan, ok := BuildRuntimeProviderPlan(RuntimeConfig{Enabled: true})
	if !ok {
		t.Fatal("BuildRuntimeProviderPlan() ok = false, want true")
	}
	if plan.Provider != ProviderExternalHTTP || plan.Language != DefaultProviderLanguage {
		t.Fatalf("BuildRuntimeProviderPlan() default provider/language = %#v", plan)
	}
	if plan.Timeout != DefaultProviderTimeout || plan.BusyPolicy != BusyPolicyQueueLatest {
		t.Fatalf("BuildRuntimeProviderPlan() default timeout/busy = %#v", plan)
	}
}

func TestApplyProviderDefaultsTrimsExplicitValues(t *testing.T) {
	got := ApplyProviderDefaults(ProviderDefaultsConfig{
		Provider:   " openai-api ",
		Language:   " en ",
		Timeout:    3 * time.Second,
		BusyPolicy: " reject ",
	})
	if got.Provider != ProviderOpenAIAPI || got.Language != "en" || got.Timeout != 3*time.Second || got.BusyPolicy != BusyPolicyReject {
		t.Fatalf("ApplyProviderDefaults() = %#v", got)
	}
}
