package main

import (
	"context"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	domainhealth "github.com/Nyukimin/RenCrow_CORE/internal/domain/health"
	modulellm "github.com/Nyukimin/RenCrow_CORE/modules/llm"
)

type fakeDomainHealthCheck struct {
	name   string
	result domainhealth.CheckResult
}

func (c fakeDomainHealthCheck) Name() string {
	return c.name
}

func (c fakeDomainHealthCheck) Run(context.Context) domainhealth.CheckResult {
	if c.result.Name == "" {
		c.result.Name = c.name
	}
	return c.result
}

func TestHealthCheckedLLMProviderReflectsBackendDown(t *testing.T) {
	provider := modulellm.NewHealthCheckedProvider(fakeModuleLLMProvider{name: "worker-provider"}, moduleLLMDomainHealthCheck{
		check: fakeDomainHealthCheck{
			name: "local_llm_worker",
			result: domainhealth.CheckResult{
				Status:   domainhealth.StatusDown,
				Message:  "connection failed",
				Duration: 25 * time.Millisecond,
			},
		},
	})

	got := provider.Health(context.Background())
	if got.Status != "down" || got.Ready {
		t.Fatalf("backend down was not reflected: %+v", got)
	}
	if got.Detail != "connection failed" || got.Metadata["check"] != "local_llm_worker" {
		t.Fatalf("health detail/metadata missing: %+v", got)
	}
}

func TestHealthCheckedLLMProviderReflectsBackendOK(t *testing.T) {
	provider := modulellm.NewHealthCheckedProvider(fakeModuleLLMProvider{name: "chat-provider"}, moduleLLMDomainHealthCheck{
		check: fakeDomainHealthCheck{
			name: "local_llm_chat",
			result: domainhealth.CheckResult{
				Status:  domainhealth.StatusOK,
				Message: "reachable",
			},
		},
	})

	got := provider.Health(context.Background())
	if got.Status != "live" || !got.Ready {
		t.Fatalf("backend ok was not reflected: %+v", got)
	}
	if got.Detail != "reachable" {
		t.Fatalf("health detail missing: %+v", got)
	}
}

func TestWrapModuleLLMProvidersWithHealthChecksOnlyForLocalOpenAI(t *testing.T) {
	providers := map[string]modulellm.Provider{
		"chat":   fakeModuleLLMProvider{name: "chat-provider"},
		"worker": fakeModuleLLMProvider{name: "worker-provider"},
	}
	cfg := &config.Config{
		LocalLLM: config.LocalLLMConfig{
			Enabled:       true,
			Provider:      "local_openai",
			ChatBaseURL:   "http://127.0.0.1:1",
			WorkerBaseURL: "http://127.0.0.1:1",
			ChatModel:     "chat-model",
			WorkerModel:   "worker-model",
			TimeoutSec:    1,
		},
	}

	wrapped := wrapModuleLLMProvidersWithHealthChecks(cfg, providers)
	if _, ok := wrapped["chat"].(modulellm.HealthCheckedProvider); !ok {
		t.Fatalf("chat provider was not health-wrapped: %#v", wrapped["chat"])
	}
	if _, ok := wrapped["worker"].(modulellm.HealthCheckedProvider); !ok {
		t.Fatalf("worker provider was not health-wrapped: %#v", wrapped["worker"])
	}

	plain := wrapModuleLLMProvidersWithHealthChecks(&config.Config{}, providers)
	if _, ok := plain["chat"].(modulellm.HealthCheckedProvider); ok {
		t.Fatalf("non-local-openai provider should not be health-wrapped")
	}
}
