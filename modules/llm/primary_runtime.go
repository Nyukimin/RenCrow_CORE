package llm

import "time"

const (
	PrimaryRoleChat   = RoleChat
	PrimaryRoleWorker = RoleWorker
	PrimaryRoleHeavy  = RoleHeavy
	PrimaryRoleWild   = RoleWild

	PrimaryModeLocal        = "local"
	PrimaryModeLegacyOllama = "legacy_ollama"
)

type LegacyOllamaRuntimeConfig struct {
	BaseURL     string
	ChatModel   string
	WorkerModel string
}

type PrimaryRuntimeConfig struct {
	LocalEnabled bool
	Local        LocalRuntimeConfig
	LegacyOllama LegacyOllamaRuntimeConfig
}

type PrimaryProviderPlan struct {
	Mode          string
	Roles         map[string]LocalAliasConfig
	WarmupTimeout time.Duration
}

func BuildPrimaryProviderPlan(cfg PrimaryRuntimeConfig) PrimaryProviderPlan {
	if cfg.LocalEnabled {
		roles := map[string]LocalAliasConfig{
			PrimaryRoleChat:   BuildLocalAliasConfig(cfg.Local, PrimaryRoleChat),
			PrimaryRoleWorker: BuildLocalAliasConfig(cfg.Local, PrimaryRoleWorker),
			PrimaryRoleHeavy:  BuildLocalAliasConfig(cfg.Local, PrimaryRoleHeavy),
			PrimaryRoleWild:   BuildLocalAliasConfig(cfg.Local, PrimaryRoleWild),
		}
		return PrimaryProviderPlan{
			Mode:  PrimaryModeLocal,
			Roles: roles,
			WarmupTimeout: MaxDuration(
				roles[PrimaryRoleChat].Timeout,
				roles[PrimaryRoleWorker].Timeout,
				roles[PrimaryRoleHeavy].Timeout,
				roles[PrimaryRoleWild].Timeout,
			),
		}
	}

	workerModel := FirstNonEmpty(cfg.LegacyOllama.WorkerModel, cfg.LegacyOllama.ChatModel)
	roles := map[string]LocalAliasConfig{
		PrimaryRoleChat: {
			Alias:    PrimaryRoleChat,
			Provider: LocalProviderOllama,
			BaseURL:  cfg.LegacyOllama.BaseURL,
			Model:    cfg.LegacyOllama.ChatModel,
			NumCtx:   LegacyOllamaChatNumCtx,
		},
		PrimaryRoleWorker: {
			Alias:    PrimaryRoleWorker,
			Provider: LocalProviderOllama,
			BaseURL:  cfg.LegacyOllama.BaseURL,
			Model:    workerModel,
			NumCtx:   LegacyOllamaWorkerNumCtx,
		},
		PrimaryRoleHeavy: {
			Alias:    PrimaryRoleHeavy,
			Provider: LocalProviderOllama,
			BaseURL:  cfg.LegacyOllama.BaseURL,
			Model:    workerModel,
			NumCtx:   LegacyOllamaWorkerNumCtx,
		},
		PrimaryRoleWild: {
			Alias:    PrimaryRoleWild,
			Provider: LocalProviderOllama,
			BaseURL:  cfg.LegacyOllama.BaseURL,
			Model:    workerModel,
			NumCtx:   LegacyOllamaWorkerNumCtx,
		},
	}
	return PrimaryProviderPlan{Mode: PrimaryModeLegacyOllama, Roles: roles}
}
