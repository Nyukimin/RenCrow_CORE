package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/modulebridge"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/orchestrator"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/service"
	domainai "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/aiworkflow"
	capdomain "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/capability"
	domainsession "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/session"
	logginginfra "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/logging"
)

func buildOrchestratorRuntime(
	cfg *config.Config,
	deps *Dependencies,
	sessionRepo orchestrator.SessionRepository,
	agents agentRuntime,
	llmRuntime llmRuntimeProviders,
	workerExecutionService service.WorkerExecutionService,
	nodeCaps capdomain.NodeCapabilities,
	centralMemory *domainsession.CentralMemory,
	ttsBridge orchestrator.TTSBridge,
	vtuberBridge orchestrator.VTuberBridge,
	bridges viewerBridgeFactories,
	verificationRuntime verificationRuntime,
) {
	if cfg.Distributed.Enabled {
		log.Println("=== v4 Distributed Mode ===")
		deps.buildDistributedMode(
			cfg,
			sessionRepo,
			agents.Mio,
			agents.Shiro,
			agents.Heavy,
			agents.Wild,
			llmRuntime.Coder1,
			llmRuntime.Coder2,
			llmRuntime.Coder3,
			llmRuntime.Coder4,
			workerExecutionService,
			llmRuntime.Chat,
			centralMemory,
			ttsBridge,
			vtuberBridge,
			nodeCaps,
		)
		deps.moduleChatService = modulebridge.NewRuntimeChatService(deps.distOrch, agents.Mio)
		deps.live2DChatResponder = &live2DOrchestratorResponder{orch: deps.distOrch}
		deps.viewerSend = bridges.ViewerSendFromOrch(deps.distOrch)
		deps.repairRunner = newAsyncRepairJobRunner(deps.distOrch, deps.eventRelay)
		deps.entryHandler = bridges.EntryFromOrch(deps.distOrch)
		deps.chromeBridge, deps.chromeBridgeStatus, deps.chromeBridgeEvents = bridges.ChromeBridgeFromOrch(deps.distOrch)
		startSuperAgentRunQueueScheduler(cfg, deps.superAgentStore, deps.distOrch, newBackgroundJobFailureReporter(deps.eventRelay))
		return
	}

	log.Println("=== v3 Local Mode ===")
	orch := orchestrator.NewMessageOrchestrator(
		sessionRepo,
		agents.Mio,
		agents.Shiro,
		llmRuntime.Coder1,
		llmRuntime.Coder2,
		llmRuntime.Coder3,
		llmRuntime.Coder4,
		workerExecutionService,
	)
	if coderCaps := buildCoderCapabilities(nodeCaps, cfg); coderCaps != nil {
		orch.SetCoderCapabilities(coderCaps)
		log.Printf("Coder capability metadata loaded (%d coders); CODE uses only local coder1 unless an explicit CODE route is requested", len(coderCaps))
	}
	orch.SetExternalCoderPolicy(buildExternalCoderPolicyFromRuntime(cfg))
	// 自己認識コンテキストをプロンプトに注入
	if cfg.SelfSourceDir != "" {
		injectSelfContext(cfg)
		log.Printf("Self-source context injected (SelfSourceDir=%s)", cfg.SelfSourceDir)
	}

	if cfg.Prompts != nil && cfg.Prompts.CoderLoop != "" {
		orch.SetCoderLoopPrompt(cfg.Prompts.CoderLoop)
		log.Printf("CoderLoop prompt loaded (%d bytes); all coder slots use multi-turn loop", len(cfg.Prompts.CoderLoop))
	}

	// セッション会話ターンロガー: ~/.picoclaw/logs/sessions/ に記録
	sessionLogDir := filepath.Join(os.Getenv("HOME"), ".picoclaw", "logs", "sessions")
	orch.SetSessionTurnLogger(logginginfra.NewSessionLogWriter(sessionLogDir))
	log.Printf("Session turn logger initialized: %s", sessionLogDir)
	orch.SetEventListener(deps.eventRelay)
	if deps.reportStore != nil {
		orch.SetReportStore(deps.reportStore)
	}
	orch.SetMaxRepair(cfg.Worker.MaxRepair)
	orch.SetWildAgent(agents.Wild)
	orch.SetHeavyAgent(agents.Heavy)
	orch.SetHeavyWorkerPolicy(domainai.HeavyWorkerPolicy{
		Enabled:                 cfg.AIWorkflow.HeavyWorkerEnabled,
		RequireReason:           cfg.AIWorkflow.HeavyWorkerRequireReason,
		FileCountThreshold:      cfg.AIWorkflow.HeavyWorkerFileThreshold,
		SpecCountThreshold:      cfg.AIWorkflow.HeavyWorkerSpecThreshold,
		FailedAttemptsThreshold: cfg.AIWorkflow.HeavyWorkerRetryThreshold,
	})
	if deps.dciSearcher != nil {
		orch.SetDCISearcher(deps.dciSearcher)
		log.Println("DCI explicit trigger integrated with MessageOrchestrator")
	}
	if deps.recallTraceStore != nil {
		orch.SetRecallTraceStore(deps.recallTraceStore)
		log.Println("Recall trace store integrated with MessageOrchestrator")
	}
	if deps.skillBootstrap != nil {
		orch.SetSkillBootstrapRecorder(deps.skillBootstrap)
		log.Println("Skill bootstrap integrated with MessageOrchestrator routes")
	}
	if deps.coderProposalEvidence != nil {
		orch.SetCoderProposalEvidenceRecorder(deps.coderProposalEvidence)
		log.Println("Coder proposal evidence recorder integrated with MessageOrchestrator")
	}
	if deps.aiWorkflowStore != nil {
		orch.SetWorkflowEventRecorder(deps.aiWorkflowStore)
		orch.SetCommandRegistry(deps.aiWorkflowStore)
		log.Println("AI Workflow event recorder integrated with MessageOrchestrator")
	}
	if deps.superAgentStore != nil {
		orch.SetSuperAgentRuntimeRecorder(deps.superAgentStore)
		orch.SetSuperAgentRunController(deps.superAgentRunController)
		log.Println("SuperAgent runtime recorder integrated with MessageOrchestrator")
	}
	if deps.personaRuntimeStore != nil {
		orch.SetPersonaRuntimeRecorder(deps.personaRuntimeStore, deps.personaTriggerDefinitions)
		orch.SetPersonaCanonicalResponses(deps.personaCanonicalResponses)
		log.Printf("Persona runtime recorder integrated with MessageOrchestrator (%d trigger definitions, %d canonical responses)", len(deps.personaTriggerDefinitions), len(deps.personaCanonicalResponses))
	}
	orch.SetTTSBridge(ttsBridge)
	orch.SetVTuberBridge(vtuberBridge)
	if verificationRuntime.Pipeline != nil {
		orch.SetVerificationPipeline(verificationRuntime.Pipeline)
		log.Println("Verification pipeline integrated with MessageOrchestrator")
	}
	if deps.idleChatOrch != nil {
		orch.SetIdleNotifier(deps.idleChatOrch)
		log.Printf("IdleChat integrated with MessageOrchestrator")
	}
	buildChannelRuntimeHandlers(cfg, deps, orch)
	deps.moduleChatService = modulebridge.NewRuntimeChatService(orch, agents.Mio)
	deps.live2DChatResponder = &live2DOrchestratorResponder{orch: orch}
	deps.viewerSend = bridges.ViewerSendFromOrch(orch)
	deps.repairRunner = newAsyncRepairJobRunner(orch, deps.eventRelay)
	deps.entryHandler = bridges.EntryFromOrch(orch)
	deps.chromeBridge, deps.chromeBridgeStatus, deps.chromeBridgeEvents = bridges.ChromeBridgeFromOrch(orch)
	deps.voiceDirectHandler = orch
	startSuperAgentRunQueueScheduler(cfg, deps.superAgentStore, orch, newBackgroundJobFailureReporter(deps.eventRelay))
}

// injectSelfContext は RenCrow 自身のソースディレクトリに関する自己認識コンテキストを
// 各エージェントのプロンプト先頭に付加する。
func injectSelfContext(cfg *config.Config) {
	if cfg.Prompts == nil || cfg.SelfSourceDir == "" {
		return
	}
	ctx := buildSelfContextBlock(cfg.SelfSourceDir)
	cfg.Prompts.SelfContext = ctx

	// CoderLoop: 既存の Project Context を上書きせず先頭に追記
	if cfg.Prompts.CoderLoop != "" {
		cfg.Prompts.CoderLoop = ctx + "\n\n" + cfg.Prompts.CoderLoop
	}
	// CoderProposal
	if cfg.Prompts.CoderProposal != "" {
		cfg.Prompts.CoderProposal = ctx + "\n\n" + cfg.Prompts.CoderProposal
	}
	// Worker
	if cfg.Prompts.Worker != "" {
		cfg.Prompts.Worker = ctx + "\n\n" + cfg.Prompts.Worker
	}
	// Mio（Chat）
	if cfg.Prompts.MioPersona != "" {
		cfg.Prompts.MioPersona = cfg.Prompts.MioPersona + "\n\n" + ctx
	}
}

// buildSelfContextBlock は自己認識コンテキストブロック文字列を生成する。
func buildSelfContextBlock(selfSourceDir string) string {
	return fmt.Sprintf(`## Self-Knowledge: RenCrow Source

You are running as part of the RenCrow system.
Your own source code is located at: %s

Key facts:
- This directory IS the RenCrow Go codebase (github.com/Nyukimin/picoclaw_multiLLM)
- You can read, search, and modify this codebase via Worker actions
- Use mcp_tool (Serena LSP) or shell_command (git grep / cat) to explore it
- Changes to this directory affect RenCrow itself — apply caution and always run go build ./... before final_report
- The binary you are running from was compiled from this source`, selfSourceDir)
}
