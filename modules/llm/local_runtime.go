package llm

import (
	"strings"
	"time"
)

const (
	LocalProviderOllama = "ollama"
	LocalProviderOpenAI = "openai"

	LocalDefaultTimeout = 120 * time.Second
	LocalChatTimeout    = 10 * time.Second
	LocalWildTimeout    = 15 * time.Second
	LocalHeavyTimeout   = 30 * time.Second

	LocalChatQueueTimeout       = 1 * time.Second
	LocalChatWorkerQueueTimeout = 2 * time.Second
	LocalWorkerQueueTimeout     = 5 * time.Second
	LocalHeavyQueueTimeout      = 5 * time.Second
	LocalWildQueueTimeout       = 2 * time.Second
	LocalDefaultQueueTimeout    = 5 * time.Second

	LocalQueuePolicyWait   = "wait"
	LocalQueuePolicyReject = "reject"
	LocalQueuePolicyLatest = "latest"

	LocalOllamaDefaultNumCtx = 131072
	LegacyOllamaChatNumCtx   = 131072
	LegacyOllamaWorkerNumCtx = 131072
)

type LocalRuntimeConfig struct {
	Provider          string
	BaseURL           string
	ChatBaseURL       string
	WorkerBaseURL     string
	ChatWorkerBaseURL string
	HeavyBaseURL      string
	WildBaseURL       string
	ChatModel         string
	WorkerModel       string
	ChatWorkerModel   string
	HeavyModel        string
	WildModel         string
	TimeoutSec        int
	ModelConcurrency  int
	ModelContext      int
}

type LocalAliasConfig struct {
	Alias        string
	Provider     string
	BaseURL      string
	Model        string
	Timeout      time.Duration
	QueueTimeout time.Duration
	QueuePolicy  string
	Concurrency  int
	NumCtx       int
}

func BuildLocalAliasConfig(cfg LocalRuntimeConfig, alias string) LocalAliasConfig {
	return LocalAliasConfig{
		Alias:        strings.TrimSpace(alias),
		Provider:     NormalizeLocalProvider(cfg.Provider),
		BaseURL:      LocalBaseURLForAlias(cfg, alias),
		Model:        LocalModelForAlias(cfg, alias),
		Timeout:      LocalTimeoutForAlias(cfg, alias),
		QueueTimeout: LocalQueueTimeoutForAlias(alias),
		QueuePolicy:  LocalQueuePolicyWait,
		Concurrency:  cfg.ModelConcurrency,
		NumCtx:       LocalModelContextForAlias(cfg, alias),
	}
}

func NormalizeLocalProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case LocalProviderOllama:
		return LocalProviderOllama
	default:
		return LocalProviderOpenAI
	}
}

func LocalOllamaNumCtxForAlias(_ string) int {
	return LocalOllamaDefaultNumCtx
}

func LocalModelContextForAlias(cfg LocalRuntimeConfig, alias string) int {
	if cfg.ModelContext > 0 {
		return cfg.ModelContext
	}
	return LocalOllamaNumCtxForAlias(alias)
}

func LocalTimeoutForAlias(cfg LocalRuntimeConfig, alias string) time.Duration {
	switch strings.ToLower(strings.TrimSpace(alias)) {
	case RoleChat:
		if cfg.TimeoutSec > 0 {
			return time.Duration(cfg.TimeoutSec) * time.Second
		}
		return LocalChatTimeout
	case RoleWorker:
		if cfg.TimeoutSec <= 0 {
			return LocalDefaultTimeout
		}
		return time.Duration(cfg.TimeoutSec) * time.Second
	case "chatworker":
		if cfg.TimeoutSec <= 0 {
			return LocalDefaultTimeout
		}
		return time.Duration(cfg.TimeoutSec) * time.Second
	case RoleWild:
		if cfg.TimeoutSec > 0 {
			return time.Duration(cfg.TimeoutSec) * time.Second
		}
		return LocalWildTimeout
	case RoleHeavy:
		if cfg.TimeoutSec > 0 {
			return time.Duration(cfg.TimeoutSec) * time.Second
		}
		return LocalHeavyTimeout
	}
	if cfg.TimeoutSec <= 0 {
		return LocalDefaultTimeout
	}
	return time.Duration(cfg.TimeoutSec) * time.Second
}

func LocalQueueTimeoutForAlias(alias string) time.Duration {
	switch strings.ToLower(strings.TrimSpace(alias)) {
	case RoleChat:
		return LocalChatQueueTimeout
	case RoleWorker:
		return LocalWorkerQueueTimeout
	case "chatworker":
		return LocalChatWorkerQueueTimeout
	case RoleWild:
		return LocalWildQueueTimeout
	case RoleHeavy:
		return LocalHeavyQueueTimeout
	default:
		return LocalDefaultQueueTimeout
	}
}

func LocalBaseURLForAlias(cfg LocalRuntimeConfig, alias string) string {
	switch strings.ToLower(strings.TrimSpace(alias)) {
	case RoleChat:
		return FirstNonEmpty(cfg.ChatBaseURL, cfg.BaseURL)
	case RoleWorker:
		return FirstNonEmpty(cfg.WorkerBaseURL, cfg.BaseURL)
	case "chatworker":
		return FirstNonEmpty(cfg.ChatWorkerBaseURL, cfg.WorkerBaseURL, cfg.BaseURL)
	case RoleHeavy:
		return FirstNonEmpty(cfg.HeavyBaseURL, cfg.WorkerBaseURL, cfg.BaseURL)
	case RoleWild:
		return FirstNonEmpty(cfg.WildBaseURL, cfg.BaseURL)
	default:
		return strings.TrimSpace(cfg.BaseURL)
	}
}

func LocalModelForAlias(cfg LocalRuntimeConfig, alias string) string {
	switch strings.ToLower(strings.TrimSpace(alias)) {
	case RoleChat:
		return cfg.ChatModel
	case RoleWorker:
		return cfg.WorkerModel
	case "chatworker":
		return FirstNonEmpty(cfg.ChatWorkerModel, cfg.WorkerModel)
	case RoleHeavy:
		if strings.TrimSpace(cfg.HeavyBaseURL) == "" && strings.TrimSpace(cfg.WorkerBaseURL) != "" {
			return cfg.WorkerModel
		}
		return cfg.HeavyModel
	case RoleWild:
		return cfg.WildModel
	default:
		return ""
	}
}

func FirstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func MaxDuration(values ...time.Duration) time.Duration {
	var max time.Duration
	for _, v := range values {
		if v > max {
			max = v
		}
	}
	return max
}
