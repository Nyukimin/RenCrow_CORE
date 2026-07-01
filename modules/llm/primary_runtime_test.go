package llm

import (
	"testing"
	"time"
)

func TestBuildPrimaryProviderPlanLocal(t *testing.T) {
	plan := BuildPrimaryProviderPlan(PrimaryRuntimeConfig{
		LocalEnabled: true,
		Local: LocalRuntimeConfig{
			Provider:         "ollama",
			BaseURL:          "http://127.0.0.1:11434",
			ChatModel:        "chat-model",
			WorkerModel:      "worker-model",
			HeavyModel:       "heavy-model",
			WildModel:        "wild-model",
			TimeoutSec:       120,
			ModelConcurrency: 2,
		},
	})

	if plan.Mode != PrimaryModeLocal {
		t.Fatalf("mode = %q", plan.Mode)
	}
	if plan.Roles[PrimaryRoleChat].Model != "chat-model" || plan.Roles[PrimaryRoleWorker].Model != "worker-model" {
		t.Fatalf("role models = %+v", plan.Roles)
	}
	if plan.Roles[PrimaryRoleChat].Provider != LocalProviderOllama || plan.Roles[PrimaryRoleChat].Concurrency != 2 {
		t.Fatalf("chat config = %+v", plan.Roles[PrimaryRoleChat])
	}
	if plan.WarmupTimeout != LocalDefaultTimeout {
		t.Fatalf("warmup timeout = %s, want %s", plan.WarmupTimeout, LocalDefaultTimeout)
	}
}

func TestBuildPrimaryProviderPlanLocalWarmupUsesMaxRoleTimeout(t *testing.T) {
	plan := BuildPrimaryProviderPlan(PrimaryRuntimeConfig{
		LocalEnabled: true,
		Local: LocalRuntimeConfig{
			ChatModel:   "chat",
			WorkerModel: "worker",
			HeavyModel:  "heavy",
			WildModel:   "wild",
			TimeoutSec:  20,
		},
	})
	if plan.WarmupTimeout != 20*time.Second {
		t.Fatalf("warmup timeout = %s, want 20s", plan.WarmupTimeout)
	}
}

func TestBuildPrimaryProviderPlanLegacyOllama(t *testing.T) {
	plan := BuildPrimaryProviderPlan(PrimaryRuntimeConfig{
		LegacyOllama: LegacyOllamaRuntimeConfig{
			BaseURL:     "http://127.0.0.1:11434",
			ChatModel:   "chat-model",
			WorkerModel: "worker-model",
		},
	})

	if plan.Mode != PrimaryModeLegacyOllama {
		t.Fatalf("mode = %q", plan.Mode)
	}
	if plan.Roles[PrimaryRoleChat].Model != "chat-model" {
		t.Fatalf("chat model = %q", plan.Roles[PrimaryRoleChat].Model)
	}
	if plan.Roles[PrimaryRoleChat].NumCtx != LegacyOllamaChatNumCtx {
		t.Fatalf("chat num_ctx = %d", plan.Roles[PrimaryRoleChat].NumCtx)
	}
	for _, role := range []string{PrimaryRoleWorker, PrimaryRoleHeavy, PrimaryRoleWild} {
		if plan.Roles[role].Model != "worker-model" {
			t.Fatalf("%s model = %q", role, plan.Roles[role].Model)
		}
		if plan.Roles[role].NumCtx != LegacyOllamaWorkerNumCtx {
			t.Fatalf("%s num_ctx = %d", role, plan.Roles[role].NumCtx)
		}
	}
}

func TestBuildPrimaryProviderPlanLegacyWorkerFallsBackToChatModel(t *testing.T) {
	plan := BuildPrimaryProviderPlan(PrimaryRuntimeConfig{
		LegacyOllama: LegacyOllamaRuntimeConfig{
			BaseURL:   "http://127.0.0.1:11434",
			ChatModel: "chat-model",
		},
	})
	if plan.Roles[PrimaryRoleWorker].Model != "chat-model" {
		t.Fatalf("worker model = %q", plan.Roles[PrimaryRoleWorker].Model)
	}
}
