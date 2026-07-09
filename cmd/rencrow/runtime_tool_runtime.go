package main

import (
	"log"
	"path/filepath"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/subagent"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/toolloop"
	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	capdomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	browseractorinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/browseractor"
	executionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/execution"
	toolharnesspersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/toolharness"
	securityinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/security"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

type toolRuntime struct {
	ChatRunnerV2          *tools.ToolRunner
	WorkerRunnerV2        *tools.ToolRunner
	ChatRuntimeRunnerV2   domaintool.RunnerV2
	WorkerRuntimeRunnerV2 domaintool.RunnerV2
	SubagentMgr           *subagent.Manager
	ToolMediationRecorder *toolharnesspersistence.JSONLRecorder
}

func buildToolRuntime(
	cfg *config.Config,
	workerToolProvider llm.ToolCallingProvider,
	runtimeToolRegistry capdomain.ToolRegistry,
	contextBudgetRecorder tools.ContextBudgetUsageRecorder,
) toolRuntime {
	personaWritePaths := []string{
		filepath.Join(cfg.WorkspaceDir, "persona", "mio.md"),
		filepath.Join(cfg.WorkspaceDir, "persona", "shiro.md"),
		filepath.Join(cfg.WorkspaceDir, "persona", "aka.md"),
		filepath.Join(cfg.WorkspaceDir, "persona", "ao.md"),
		filepath.Join(cfg.WorkspaceDir, "persona", "gin.md"),
		filepath.Join(cfg.WorkspaceDir, "persona", "kin.md"),
	}
	toolMediationRecorder := buildToolMediationRecorder(cfg)
	chatToolRunnerCfg := tools.ToolRunnerConfig{
		GoogleAPIKey:         cfg.GoogleSearchChat.APIKey,
		GoogleSearchEngineID: cfg.GoogleSearchChat.SearchEngineID,
		AllowedWritePaths:    personaWritePaths,
		DisableToolHarness:   true,
	}
	workerToolRunnerCfg := tools.ToolRunnerConfig{
		GoogleAPIKey:         cfg.GoogleSearchWorker.APIKey,
		GoogleSearchEngineID: cfg.GoogleSearchWorker.SearchEngineID,
		ToolRegistry:         runtimeToolRegistry,
		WorkspaceDir:         cfg.WorkspaceDir,
		DisableToolHarness:   true,
	}
	if cfg.BrowserActor.Enabled {
		workerToolRunnerCfg.BrowserActorRunner = browseractorinfra.NewRunner(browserActorConfigFromRuntime(cfg.BrowserActor))
	}
	if cfg.Codex.Enabled {
		workingDir := cfg.Codex.WorkingDir
		if workingDir == "" {
			workingDir = cfg.SelfSourceDir
		}
		workerToolRunnerCfg.CodexRunner = tools.NewCodexExecRunner(
			cfg.Codex.Command,
			workingDir,
			cfg.Codex.Sandbox,
			cfg.Codex.Model,
			time.Duration(cfg.Codex.TimeoutMS)*time.Millisecond,
			cfg.Codex.MaxPromptBytes,
			cfg.Codex.MaxOutputBytes,
			cfg.Codex.EphemeralEnabled(),
		)
		log.Printf("Codex runner enabled (sandbox=%s working_dir=%s)", cfg.Codex.Sandbox, workingDir)
	}

	chatToolRunnerV2 := tools.NewToolRunner(chatToolRunnerCfg)
	workerToolRunnerV2 := tools.NewToolRunner(workerToolRunnerCfg)

	var chatRunnerV2 domaintool.RunnerV2 = chatToolRunnerV2
	var workerRunnerV2 domaintool.RunnerV2 = workerToolRunnerV2
	if runtimeToolRegistry != nil {
		workerRunnerV2 = tools.NewCompositeRunnerV2(workerRunnerV2, runtimeToolRegistry, cfg.WorkspaceDir)
		log.Printf("CompositeRunnerV2 enabled (ToolRegistry fallback for worker)")
	}

	if cfg.Security.Enabled {
		var execRepo domainexecution.Repository
		if cfg.Security.Audit.Enabled && cfg.Security.Audit.Backend == "jsonl" {
			repo, err := executionpersistence.NewJSONLRepository(cfg.Security.Audit.Path)
			if err != nil {
				log.Fatalf("Failed to initialize execution audit repository: %v", err)
			}
			execRepo = repo
		}

		policy := securityinfra.NewPolicyEngine(securityinfra.PolicyConfig{
			Mode:              cfg.Security.PolicyMode,
			NetworkScope:      cfg.Security.NetworkScope,
			NetworkAllowed:    cfg.Security.NetworkAllowlist,
			DenyCommands:      cfg.Security.DenyCommands,
			Workspace:         cfg.WorkspaceDir,
			WorkspaceEnforced: cfg.Security.WorkspaceEnforced,
			SandboxRoot:       filepath.Join(cfg.WorkspaceDir, cfg.Sandbox.Root),
			SandboxWriteOnly:  cfg.Sandbox.Enabled && cfg.Sandbox.DenyOutsideSandboxWrite,
		})

		securedChatRunner, err := securityinfra.NewPolicyRunner(chatToolRunnerV2, policy, execRepo, "chat")
		if err != nil {
			log.Fatalf("Failed to create chat policy runner: %v", err)
		}
		securedWorkerRunner, err := securityinfra.NewPolicyRunner(workerRunnerV2, policy, execRepo, "worker")
		if err != nil {
			log.Fatalf("Failed to create worker policy runner: %v", err)
		}
		chatRunnerV2 = securedChatRunner
		workerRunnerV2 = securedWorkerRunner
		log.Printf("Security policy runner enabled (mode=%s)", cfg.Security.PolicyMode)
	}

	if cfg.ToolHarness.IsEnabled() {
		harnessCfg := tools.ToolHarnessRunnerConfig{
			Mode: cfg.ToolHarness.Mode,
		}
		if toolMediationRecorder != nil {
			harnessCfg.Recorder = toolMediationRecorder
		}
		chatRunnerV2 = tools.NewToolHarnessRunnerWithConfig(chatRunnerV2, harnessCfg)
		workerRunnerV2 = tools.NewToolHarnessRunnerWithConfig(workerRunnerV2, harnessCfg)
		log.Printf("Tool Harness enabled (mode=%s record_events=%t)", cfg.ToolHarness.Mode, cfg.ToolHarness.ShouldRecordEvents())
	} else {
		log.Printf("Tool Harness disabled by config")
	}

	if cfg.AIWorkflow.ContextBudgetTokens > 0 {
		policy := domainai.ContextBudgetPolicy{
			MaxContextTokens: cfg.AIWorkflow.ContextBudgetTokens,
			WarnAtRatio:      cfg.AIWorkflow.ContextBudgetWarnRatio,
			StopAtRatio:      cfg.AIWorkflow.ContextBudgetStopRatio,
		}
		offloadDir := filepath.Join(cfg.WorkspaceDir, "logs", "tool_results")
		chatRunnerV2 = tools.NewContextBudgetRunner(chatRunnerV2, tools.ContextBudgetRunnerConfig{Agent: "Chat", Policy: policy, Recorder: contextBudgetRecorder, OffloadDir: offloadDir})
		workerRunnerV2 = tools.NewContextBudgetRunner(workerRunnerV2, tools.ContextBudgetRunnerConfig{Agent: "Worker", Policy: policy, Recorder: contextBudgetRecorder, OffloadDir: offloadDir})
		log.Printf("Tool context budget runner enabled (max_context_tokens=%d)", cfg.AIWorkflow.ContextBudgetTokens)
	}

	var subagentMgr *subagent.Manager
	if cfg.Subagent.Enabled {
		subagentProvider := resolveSubagentProvider(cfg, workerToolProvider)
		toolDefs := workerToolRunnerV2.ToolDefinitions()
		subagentOpts := []subagent.ManagerOption{}
		if runtimeToolRegistry != nil {
			subagentOpts = append(subagentOpts, subagent.WithToolRegistry(runtimeToolRegistry))
		}
		subagentMgr = subagent.NewManager(
			subagentProvider,
			workerRunnerV2,
			toolDefs,
			toolloop.Config{MaxIterations: cfg.Subagent.MaxIterations},
			subagentOpts...,
		)
		workerToolRunnerV2.RegisterSubagent("worker", tools.NewSubagentFuncFromManager(subagentMgr))
		log.Printf("Subagent enabled (provider: %s, max_iterations: %d)",
			subagentProvider.Name(), cfg.Subagent.MaxIterations)
	} else {
		log.Printf("Subagent disabled")
	}

	log.Printf("ToolRunner initialized: Chat=%d tools, Worker=%d tools",
		len(mustGetToolList(chatRunnerV2)), len(mustGetToolList(workerRunnerV2)))

	if chatToolRunnerCfg.GoogleAPIKey != "" && chatToolRunnerCfg.GoogleSearchEngineID != "" {
		log.Printf("Google Search API (Chat) configured")
	}
	if workerToolRunnerCfg.GoogleAPIKey != "" && workerToolRunnerCfg.GoogleSearchEngineID != "" {
		log.Printf("Google Search API (Worker) configured")
	}

	return toolRuntime{
		ChatRunnerV2:          chatToolRunnerV2,
		WorkerRunnerV2:        workerToolRunnerV2,
		ChatRuntimeRunnerV2:   chatRunnerV2,
		WorkerRuntimeRunnerV2: workerRunnerV2,
		SubagentMgr:           subagentMgr,
		ToolMediationRecorder: toolMediationRecorder,
	}
}

func buildToolMediationRecorder(cfg *config.Config) *toolharnesspersistence.JSONLRecorder {
	if cfg == nil || !cfg.ToolHarness.IsEnabled() || !cfg.ToolHarness.ShouldRecordEvents() {
		return nil
	}
	path := cfg.ToolHarness.LogPath
	if path == "" {
		path = filepath.Join(cfg.WorkspaceDir, "logs", "tool_mediation.jsonl")
	}
	recorder, err := toolharnesspersistence.NewJSONLRecorder(path)
	if err != nil {
		log.Printf("Tool Harness mediation recorder disabled: %v", err)
		return nil
	}
	log.Printf("Tool Harness mediation recorder initialized (%s)", path)
	return recorder
}
