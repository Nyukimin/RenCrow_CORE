package main

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	healthapp "github.com/Nyukimin/RenCrow_CORE/internal/application/health"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domainhealth "github.com/Nyukimin/RenCrow_CORE/internal/domain/health"
	infrahealth "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/health"
	executionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/execution"
	modulellm "github.com/Nyukimin/RenCrow_CORE/modules/llm"
)

type healthChecker interface {
	RunChecks(ctx context.Context) domainhealth.HealthReport
}

func loadExecutionStats(cfg *config.Config) (map[domainexecution.Status]int, error) {
	if !cfg.Security.Audit.Enabled {
		return map[domainexecution.Status]int{}, nil
	}
	repo, err := executionpersistence.NewJSONLRepository(cfg.Security.Audit.Path)
	if err != nil {
		return nil, err
	}
	return repo.CountByStatus(context.Background())
}

func loadEvidenceSummary(cfg *config.Config) (map[string]map[string]int, error) {
	if !cfg.Security.Audit.Enabled {
		return map[string]map[string]int{
			"status": {
				"passed": 0,
				"failed": 0,
				"other":  0,
			},
			"error_kind": {
				"apply":  0,
				"verify": 0,
				"repair": 0,
				"none":   0,
				"other":  0,
			},
		}, nil
	}
	store, err := executionpersistence.NewJSONLReportStore(cfg.Security.Audit.Path)
	if err != nil {
		return nil, err
	}
	return store.Summary(context.Background())
}

func buildHealthService(cfg *config.Config) *healthapp.HealthService {
	if cfg.LLMGateway.Enabled {
		apiKey := ""
		if cfg.LLMGateway.APIKeyEnv != "" {
			apiKey = strings.TrimSpace(os.Getenv(cfg.LLMGateway.APIKeyEnv))
		}
		timeout := time.Duration(cfg.LLMGateway.TimeoutSec) * time.Second
		checks := []domainhealth.Check{}
		for _, agentID := range []string{"mio", "worker", "shiro", "kuro", "midori"} {
			checks = append(checks, infrahealth.NewOpenAICompatibleChatCheck("gateway_"+agentID, cfg.LLMGateway.BaseURL, agentID, apiKey, timeout))
		}
		return healthapp.NewHealthService(checks...)
	}
	if cfg.LocalLLM.Enabled && cfg.LocalLLM.Provider == "local_openai" {
		checks := buildLocalLLMHealthChecks(cfg)
		return healthapp.NewHealthService(checks...)
	}

	checks := []domainhealth.Check{
		infrahealth.NewOllamaCheck(cfg.Ollama.BaseURL),
	}
	requirements := collectOllamaHealthRequirements(cfg)
	for _, req := range requirements {
		checks = append(checks, infrahealth.NewOllamaModelCheck(cfg.Ollama.BaseURL, req.Name))
	}

	// 常駐モデルのコンテキスト長チェック（max_context が設定されている場合のみ）
	if cfg.Ollama.MaxContext > 0 {
		checks = append(checks, infrahealth.NewOllamaModelsCheck(
			cfg.Ollama.BaseURL,
			requirements,
		))
	}

	return healthapp.NewHealthService(checks...)
}

func buildLocalLLMHealthChecks(cfg *config.Config) []domainhealth.Check {
	if cfg == nil {
		return nil
	}
	seen := map[string]struct{}{}
	add := func(checks []domainhealth.Check, role, baseURL, model string) []domainhealth.Check {
		role = strings.TrimSpace(role)
		baseURL = firstNonEmpty(baseURL, cfg.LocalLLM.BaseURL)
		model = strings.TrimSpace(model)
		if role == "" || baseURL == "" || model == "" {
			return checks
		}
		key := role + "\x00" + baseURL + "\x00" + model
		if _, ok := seen[key]; ok {
			return checks
		}
		seen[key] = struct{}{}
		timeout := modulellm.LocalTimeoutForAlias(localRuntimeConfigFromAppConfig(cfg), role)
		return append(checks, infrahealth.NewOpenAICompatibleChatCheck(role, baseURL, model, cfg.LocalLLM.APIKey, timeout))
	}

	checks := make([]domainhealth.Check, 0, 5)
	if cfg.RuntimeTopologyRoleEnabled("RenCraw_LLM", "chat") {
		checks = add(checks, "Chat", cfg.LocalLLM.ChatBaseURL, cfg.LocalLLM.ChatModel)
	}
	if cfg.RuntimeTopologyRoleEnabled("RenCraw_LLM", "worker") {
		checks = add(checks, "Worker", cfg.LocalLLM.WorkerBaseURL, cfg.LocalLLM.WorkerModel)
	}
	if cfg.RuntimeTopologyRoleEnabled("RenCraw_LLM", "chatworker") {
		checks = add(checks, "ChatWorker", firstNonEmpty(cfg.LocalLLM.ChatWorkerBaseURL, cfg.LocalLLM.WorkerBaseURL), modulellm.LocalModelForAlias(localRuntimeConfigFromAppConfig(cfg), "chatworker"))
	}
	if cfg.RuntimeTopologyRoleEnabled("RenCraw_LLM", "heavy") && strings.TrimSpace(cfg.LocalLLM.HeavyBaseURL) != "" {
		checks = add(checks, "Heavy", cfg.LocalLLM.HeavyBaseURL, modulellm.LocalModelForAlias(localRuntimeConfigFromAppConfig(cfg), "heavy"))
	}
	if cfg.RuntimeTopologyRoleEnabled("RenCraw_LLM", "wild") && cfg.LocalLLMWarmupEnabled() {
		checks = add(checks, "Wild", cfg.LocalLLM.WildBaseURL, cfg.LocalLLM.WildModel)
	}
	return checks
}

func collectOllamaHealthRequirements(cfg *config.Config) []infrahealth.ModelRequirement {
	if cfg == nil {
		return nil
	}
	seen := map[string]struct{}{}
	add := func(out []infrahealth.ModelRequirement, name string) []infrahealth.ModelRequirement {
		name = strings.TrimSpace(name)
		if name == "" {
			return out
		}
		if _, ok := seen[name]; ok {
			return out
		}
		seen[name] = struct{}{}
		return append(out, infrahealth.ModelRequirement{Name: name, MaxContext: cfg.Ollama.MaxContext})
	}

	out := make([]infrahealth.ModelRequirement, 0, 3)
	out = add(out, cfg.Ollama.Model)
	return out
}

func inferTTSDebugBaseURLFromConfig(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	if cfg.TTS.Irodori.Enabled && strings.TrimSpace(cfg.TTS.Irodori.BaseURL) != "" {
		return strings.TrimSpace(cfg.TTS.Irodori.BaseURL)
	}
	if cfg.TTS.SBV2.Enabled && strings.TrimSpace(cfg.TTS.SBV2.BaseURL) != "" {
		return strings.TrimSpace(cfg.TTS.SBV2.BaseURL)
	}
	return strings.TrimSpace(cfg.TTS.HTTPBaseURL)
}

func inferTTSDebugHealthPathFromConfig(cfg *config.Config) string {
	return ""
}
