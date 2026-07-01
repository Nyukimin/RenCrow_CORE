package llm

import (
	"strings"
	"testing"
)

func TestBuildCoderProviderPlanDisabled(t *testing.T) {
	plan, err := BuildCoderProviderPlan(CoderProviderConfig{Provider: CoderProviderOpenAI})
	if err != nil {
		t.Fatalf("BuildCoderProviderPlan() unexpected error: %v", err)
	}
	if plan.Enabled {
		t.Fatalf("BuildCoderProviderPlan() = %#v, want disabled zero plan", plan)
	}
}

func TestBuildCoderProviderPlanRequiresAPIKeyForExternalProviders(t *testing.T) {
	for _, provider := range []string{CoderProviderDeepSeek, CoderProviderOpenAI, CoderProviderClaude, CoderProviderGemini} {
		t.Run(provider, func(t *testing.T) {
			_, err := BuildCoderProviderPlan(CoderProviderConfig{Enabled: true, Provider: provider, Model: "model"})
			if err == nil || !strings.Contains(err.Error(), "requires api_key") {
				t.Fatalf("BuildCoderProviderPlan() err = %v, want api key requirement", err)
			}
		})
	}
}

func TestBuildCoderProviderPlanRequiresBaseURLForLocalProviders(t *testing.T) {
	for _, provider := range []string{CoderProviderLocalOpenAI, CoderProviderOllama} {
		t.Run(provider, func(t *testing.T) {
			_, err := BuildCoderProviderPlan(CoderProviderConfig{Enabled: true, Provider: provider, Model: "model"})
			if err == nil || !strings.Contains(err.Error(), "requires base_url") {
				t.Fatalf("BuildCoderProviderPlan() err = %v, want base url requirement", err)
			}
		})
	}
}

func TestBuildCoderProviderPlanNormalizesProviderFields(t *testing.T) {
	plan, err := BuildCoderProviderPlan(CoderProviderConfig{
		Enabled:  true,
		Provider: " LOCAL_OPENAI ",
		Model:    " Worker ",
		APIKey:   " key ",
		BaseURL:  " http://127.0.0.1:8080 ",
	})
	if err != nil {
		t.Fatalf("BuildCoderProviderPlan() unexpected error: %v", err)
	}
	if plan.Provider != CoderProviderLocalOpenAI || plan.Model != "Worker" || plan.APIKey != "key" || plan.BaseURL != "http://127.0.0.1:8080" {
		t.Fatalf("BuildCoderProviderPlan() = %#v", plan)
	}
	if plan.Timeout != CoderLocalOpenAITimeout {
		t.Fatalf("BuildCoderProviderPlan() timeout = %s, want %s", plan.Timeout, CoderLocalOpenAITimeout)
	}
}

func TestBuildCoderProviderPlanRejectsUnknownProvider(t *testing.T) {
	_, err := BuildCoderProviderPlan(CoderProviderConfig{Enabled: true, Provider: "unknown-provider"})
	if err == nil || !strings.Contains(err.Error(), "unknown provider: unknown-provider") {
		t.Fatalf("BuildCoderProviderPlan() err = %v, want unknown provider", err)
	}
}
