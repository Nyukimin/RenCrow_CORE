package main

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/service"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/llm/providers/ollama"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/mcp"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

// initHandler はagentTypeに応じたハンドラを初期化
func initHandler(agentType string, cfg *config.Config) (AgentHandler, error) {
	switch agentType {
	case "worker":
		return initWorkerHandler(cfg)
	case "coder1":
		return initCoderHandler("coder1", cfg)
	case "coder2":
		return initCoderHandler("coder2", cfg)
	case "coder3":
		return initCoderHandler("coder3", cfg)
	case "coder4":
		return initCoderHandler("coder4", cfg)
	default:
		return nil, fmt.Errorf("unknown agent type: %s (supported: worker, coder1, coder2, coder3, coder4, audio_router)", agentType)
	}
}

// initWorkerHandler はWorkerハンドラを初期化
func initWorkerHandler(cfg *config.Config) (*workerHandler, error) {
	model := strings.TrimSpace(cfg.Ollama.Model)
	if model == "" {
		model = cfg.Ollama.Model
	}
	ollamaProvider := ollama.NewOllamaProviderWithNumCtx(cfg.Ollama.BaseURL, model, 16384)
	toolRunnerCfg := tools.ToolRunnerConfig{
		GoogleAPIKey:         os.Getenv("GOOGLE_API_KEY_WORKER"),
		GoogleSearchEngineID: os.Getenv("GOOGLE_SEARCH_ENGINE_ID_WORKER"),
	}
	toolRunner := tools.NewToolRunner(toolRunnerCfg)
	mcpClient := mcp.NewMCPClient()
	shiroAgent := agent.NewShiroAgent(ollamaProvider, toolRunner, mcpClient, cfg.Prompts.Worker, nil)
	executionService := service.NewWorkerExecutionService(cfg.Worker)

	log.Printf("[rencrow-agent] Worker initialized (workspace=%s)", cfg.Worker.Workspace)

	return &workerHandler{
		shiroAgent:       shiroAgent,
		executionService: executionService,
	}, nil
}

// initCoderHandler はCoderハンドラを初期化
func initCoderHandler(agentName string, cfg *config.Config) (*coderHandler, error) {
	// v4.1: Unified CoderConfig を使用
	var coderCfg config.CoderConfig
	switch agentName {
	case "coder1":
		coderCfg = cfg.Coder1
	case "coder2":
		coderCfg = cfg.Coder2
	case "coder3":
		coderCfg = cfg.Coder3
	case "coder4":
		coderCfg = cfg.Coder4
	default:
		return nil, fmt.Errorf("unknown coder: %s", agentName)
	}

	if !coderCfg.Enabled {
		return nil, fmt.Errorf("%s is not enabled in config", agentName)
	}

	// CoderConfig から Provider を作成
	provider, err := createProviderFromConfig(coderCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create provider for %s: %w", agentName, err)
	}

	// CoderAgent 作成
	coderAgent := agent.NewCoderAgent(provider, nil, nil, cfg.Prompts.CoderProposal)

	// Persona 適用
	if personality := coderPersonalityFromPrompts(coderCfg, cfg.Prompts); personality != "" {
		persona := agent.AgentPersona{
			Name:        coderCfg.Name,
			Personality: personality,
			Tone:        coderCfg.Tone,
		}
		coderAgent.WithPersona(persona)
		log.Printf("[rencrow-agent] %s: Applied Persona '%s'", agentName, coderCfg.Name)
	}

	// LightMemory 適用
	if coderCfg.LightMemory.Enabled {
		memory := agent.NewLightMemory(coderCfg.LightMemory.MaxTurns)
		coderAgent.WithLightMemory(memory)
		log.Printf("[rencrow-agent] %s: Applied LightMemory (max_turns=%d)", agentName, coderCfg.LightMemory.MaxTurns)
	}

	log.Printf("[rencrow-agent] %s initialized: provider=%s, model=%s", agentName, coderCfg.Provider, coderCfg.Model)

	return &coderHandler{
		agentName:        agentName,
		coderAgent:       coderAgent,
		proposalPrompt:   cfg.Prompts.CoderProposal,
		characterPrompts: cfg.Prompts.CharacterPrompts,
	}, nil
}

func coderPersonalityFromPrompts(coderCfg config.CoderConfig, prompts *config.LoadedPrompts) string {
	if strings.TrimSpace(coderCfg.Personality) != "" {
		return coderCfg.Personality
	}
	if prompts == nil {
		return ""
	}
	return characterPromptForCoder(coderCfg.Name, prompts.CharacterPrompts)
}

func coderPersonalityFromCharacters(coderCfg config.CoderConfig, characterPrompts map[string]string) string {
	if strings.TrimSpace(coderCfg.Personality) != "" {
		return coderCfg.Personality
	}
	return characterPromptForCoder(coderCfg.Name, characterPrompts)
}

func characterPromptForCoder(name string, characterPrompts map[string]string) string {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		return ""
	}
	return strings.TrimSpace(characterPrompts[key])
}
