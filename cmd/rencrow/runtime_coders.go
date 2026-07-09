package main

import (
	"context"
	"log"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	capdomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/proposal"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	llmfactory "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/llm/factory"
	moduleworker "github.com/Nyukimin/RenCrow_CORE/modules/worker"
)

type coderAdapter struct {
	domainCoder *agent.CoderAgent
}

func (a *coderAdapter) Generate(ctx context.Context, t task.Task, systemPrompt string) (string, error) {
	return a.domainCoder.GenerateWithPrompt(ctx, t, systemPrompt)
}

func (a *coderAdapter) GenerateProposal(ctx context.Context, t task.Task) (*proposal.Proposal, error) {
	return a.domainCoder.GenerateProposal(ctx, t)
}

func (a *coderAdapter) GenerateWithContext(ctx context.Context, messages []llm.Message) (string, error) {
	return a.domainCoder.GenerateWithContext(ctx, messages)
}

// buildCoderCapabilities は NodeCapabilities と config から []CoderCapability を構築する（Phase 3）
func buildCoderCapabilities(nodeCaps capdomain.NodeCapabilities, cfg *config.Config) []capdomain.CoderCapability {
	if cfg == nil {
		return nil
	}
	llms := make([]moduleworker.LLMCapability, 0, len(nodeCaps.LLMs))
	for _, l := range nodeCaps.LLMs {
		llms = append(llms, moduleworker.LLMCapability{
			ProviderName: l.ProviderName,
			ModelName:    l.ModelName,
			Available:    l.Available,
			Quality:      l.Quality,
		})
	}

	plans := moduleworker.BuildCoderCapabilityPlans(llms, coderSlotConfigsFromRuntime(cfg), cfg.Capability.LLMQualityOverrides)
	if plans == nil {
		return nil // 品質情報なし → 静的チェーンにフォールバック
	}
	caps := make([]capdomain.CoderCapability, 0, len(plans))
	for _, plan := range plans {
		caps = append(caps, capdomain.CoderCapability{
			Name:      plan.Name,
			Quality:   plan.Quality,
			Available: plan.Available,
		})
	}
	return caps
}

func coderSlotConfigFromAppConfig(name string, cfg config.CoderConfig) moduleworker.CoderSlotConfig {
	return moduleworker.CoderSlotConfig{
		Name:                name,
		DisplayName:         cfg.DisplayName,
		Provider:            cfg.Provider,
		Model:               cfg.Model,
		APIKey:              cfg.APIKey,
		Enabled:             cfg.Enabled,
		LightMemoryEnabled:  cfg.LightMemory.Enabled,
		LightMemoryMaxTurns: cfg.LightMemory.MaxTurns,
	}
}

func coderSlotConfigsFromRuntime(cfg *config.Config) []moduleworker.CoderSlotConfig {
	if cfg == nil {
		return nil
	}
	return []moduleworker.CoderSlotConfig{
		coderSlotConfigFromAppConfig("coder1", cfg.Coder1),
		coderSlotConfigFromAppConfig("coder2", cfg.Coder2),
		coderSlotConfigFromAppConfig("coder3", cfg.Coder3),
		coderSlotConfigFromAppConfig("coder4", cfg.Coder4),
	}
}

func buildExternalCoderPolicyFromRuntime(cfg *config.Config) map[string]bool {
	return moduleworker.BuildExternalCoderPolicy(coderSlotConfigsFromRuntime(cfg))
}

func coderConfigBySlotName(cfg *config.Config, name string) config.CoderConfig {
	if cfg == nil {
		return config.CoderConfig{}
	}
	switch moduleworker.CoderSlotIndex(name) {
	case 0:
		return cfg.Coder1
	case 1:
		return cfg.Coder2
	case 2:
		return cfg.Coder3
	case 3:
		return cfg.Coder4
	default:
		return config.CoderConfig{}
	}
}

func coderOutputBySlotName(name string, coder1, coder2, coder3, coder4 **coderAdapter) **coderAdapter {
	switch moduleworker.CoderSlotIndex(name) {
	case 0:
		return coder1
	case 1:
		return coder2
	case 2:
		return coder3
	case 3:
		return coder4
	default:
		return nil
	}
}

// setupCoders は Config から Coder1-4 を初期化（v4.1 Agent Persona 対応）
func setupCoders(cfg *config.Config, busyTracker *llmBusyTracker) (coder1, coder2, coder3, coder4 *coderAdapter) {
	// Shared LightMemory instances (セッション単位で共有)
	var globalLightMemory *agent.LightMemory

	for _, plan := range moduleworker.BuildCoderSetupPlans(coderSlotConfigsFromRuntime(cfg)) {
		cc := coderConfigBySlotName(cfg, plan.Name)
		out := coderOutputBySlotName(plan.Name, &coder1, &coder2, &coder3, &coder4)
		if out == nil {
			continue
		}
		if !plan.Enabled {
			log.Printf("[setupCoders] %s (%s) disabled", plan.Name, cc.Name)
			continue
		}

		// LLM Provider 生成
		provider, err := llmfactory.CreateProvider(cc)
		if err != nil {
			log.Printf("[setupCoders] %s (%s) provider creation failed: %v", plan.Name, cc.Name, err)
			continue
		}
		if provider == nil {
			log.Printf("[setupCoders] %s (%s) provider is nil (Enabled=false or error)", plan.Name, cc.Name)
			continue
		}
		provider = trackLLMProvider(strings.ToLower(plan.Name), provider, busyTracker)

		// CoderAgent 作成
		domainCoder := agent.NewCoderAgent(provider, nil, nil, cfg.Prompts.CoderProposal)

		// Agent Persona 設定（persona_file 優先、なければ personality、最後に characters/<name>）
		personality, source := resolveCoderPersonality(cfg, cc)
		if source != "" {
			log.Printf("[setupCoders] %s (%s) persona loaded from %s", plan.Name, cc.DisplayName, source)
		}
		if personality != "" {
			coderPersona := agent.AgentPersona{
				Name:        cc.Name,
				Personality: personality,
				Tone:        cc.Tone,
			}
			domainCoder.WithPersona(coderPersona)
			log.Printf("[setupCoders] %s (%s) persona enabled", plan.Name, cc.DisplayName)
		}

		// LightMemory 設定（全 Coder で共有）
		if plan.UseLightMemory {
			if plan.InitializeSharedLightMemory || globalLightMemory == nil {
				globalLightMemory = agent.NewLightMemory(plan.SharedLightMemoryMaxTurns)
				log.Printf("[setupCoders] LightMemory initialized with maxTurns=%d", plan.SharedLightMemoryMaxTurns)
			}
			domainCoder.WithLightMemory(globalLightMemory)
			log.Printf("[setupCoders] %s (%s) LightMemory enabled", plan.Name, cc.DisplayName)
		}

		// coderAdapter 作成
		*out = &coderAdapter{domainCoder: domainCoder}
		log.Printf("[setupCoders] %s (%s) enabled: provider=%s model=%s",
			plan.Name, cc.DisplayName, cc.Provider, cc.Model)
	}

	return
}

func coderConfigWithRuntimePersonality(cfg *config.Config, coderCfg config.CoderConfig) config.CoderConfig {
	personality, _ := resolveCoderPersonality(cfg, coderCfg)
	if personality != "" {
		coderCfg.Personality = personality
	}
	return coderCfg
}

func resolveCoderPersonality(cfg *config.Config, coderCfg config.CoderConfig) (string, string) {
	if cfg != nil && coderCfg.PersonaFile != "" {
		if content, ok := config.LoadPersonaFile(cfg.WorkspaceDir, coderCfg.PersonaFile); ok {
			return content, "file: " + coderCfg.PersonaFile
		}
	}
	if strings.TrimSpace(coderCfg.Personality) != "" {
		return coderCfg.Personality, "inline personality"
	}
	if cfg != nil && cfg.Prompts != nil {
		name := strings.ToLower(strings.TrimSpace(coderCfg.Name))
		if content := strings.TrimSpace(cfg.Prompts.CharacterPrompts[name]); content != "" {
			return content, "character bundle: " + name
		}
	}
	return "", ""
}
