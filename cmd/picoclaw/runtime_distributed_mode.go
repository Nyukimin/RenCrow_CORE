package main

import (
	"log"
	"strings"

	discordadapter "github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/channels/discord"
	slackadapter "github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/channels/slack"
	telegramadapter "github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/channels/telegram"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/line"
	attachmentapp "github.com/Nyukimin/picoclaw_multiLLM/internal/application/attachment"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/orchestrator"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/service"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/agent"
	domainai "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/aiworkflow"
	capdomain "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/capability"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
	domainsession "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/session"
	domaintransport "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/transport"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/transport"
)

// buildDistributedMode はv4分散モードの依存関係を構築
func (d *Dependencies) buildDistributedMode(
	cfg *config.Config,
	sessionRepo orchestrator.SessionRepository,
	mioAgent *agent.MioAgent,
	shiroAgent *agent.ShiroAgent,
	heavyAgent *agent.HeavyAgent,
	wildAgent *agent.WildAgent,
	coder1Adapter *coderAdapter,
	coder2Adapter *coderAdapter,
	coder3Adapter *coderAdapter,
	coder4Adapter *coderAdapter,
	workerExecution service.WorkerExecutionService,
	ollamaProvider llm.LLMProvider,
	centralMemory *domainsession.CentralMemory,
	ttsBridge orchestrator.TTSBridge,
	vtuberBridge orchestrator.VTuberBridge,
	nodeCaps capdomain.NodeCapabilities,
) {
	// Transport Factory でAgent別Transport生成
	factory := transport.NewTransportFactory()
	transports, err := factory.CreateTransports(cfg.Distributed)
	if err != nil {
		log.Fatalf("Failed to create transports: %v", err)
	}

	// MessageRouter 構築（LocalTransport専用）
	router := transport.NewMessageRouter()
	sshTransports := make(map[string]domaintransport.Transport)
	localTransports := make(map[string]*transport.LocalTransport)

	for agentName, t := range transports {
		switch v := t.(type) {
		case *transport.LocalTransport:
			if !localAgentEnabled(agentName, coder1Adapter, coder2Adapter, coder3Adapter, coder4Adapter) {
				log.Printf("Skipped LocalTransport for agent '%s' (agent not enabled in this process)", agentName)
				continue
			}
			router.RegisterAgent(agentName, v)
			localTransports[agentName] = v
			log.Printf("Registered LocalTransport for agent '%s'", agentName)
		case *transport.SSHTransport:
			// SSH接続失敗は対象 coder の縮退として扱い、Chat/Worker の起動は継続する。
			if err := registerSSHTransport(agentName, v, v, sshTransports); err != nil {
				reason := formatAgentUnavailableReason("ssh connect failed", err)
				markAgentUnavailable(d.monitorStore, agentName, reason)
				if d.eventRelay != nil {
					d.eventRelay.OnEvent(orchestrator.NewEvent(
						"agent.unavailable",
						agentName,
						"system",
						reason,
						"",
						"",
						"system",
						"system",
						"system",
					))
				}
			}
		}
	}
	d.router = router
	d.localTransports = localTransports
	d.sshTransports = sshTransports

	mioTransport := d.ensureLocalTransport("mio")
	shiroTransport := d.ensureLocalTransport("shiro")
	d.startLocalWorkerAgent("shiro", shiroTransport, shiroAgent, workerExecution)

	if lt, ok := d.localTransports["coder1"]; ok && coder1Adapter != nil {
		d.startLocalCoderAgent("coder1", lt, coder1Adapter)
	}
	if lt, ok := d.localTransports["coder2"]; ok && coder2Adapter != nil {
		d.startLocalCoderAgent("coder2", lt, coder2Adapter)
	}
	if lt, ok := d.localTransports["coder3"]; ok && coder3Adapter != nil {
		d.startLocalCoderAgent("coder3", lt, coder3Adapter)
	}
	if lt, ok := d.localTransports["coder4"]; ok && coder4Adapter != nil {
		d.startLocalCoderAgent("coder4", lt, coder4Adapter)
	}
	_ = mioTransport

	// DistributedOrchestrator（Local + SSH transports）
	distOrch := orchestrator.NewDistributedOrchestrator(
		sessionRepo,
		mioAgent,
		router,
		centralMemory,
		sshTransports,
	)
	d.distOrch = distOrch
	if coderCaps := buildCoderCapabilities(nodeCaps, cfg); coderCaps != nil {
		distOrch.SetCoderCapabilities(coderCaps)
		log.Printf("Distributed coder capability routing enabled (%d coders)", len(coderCaps))
	}
	distOrch.SetHeavyAgent(heavyAgent)
	distOrch.SetWildAgent(wildAgent)
	distOrch.SetHeavyWorkerPolicy(domainai.HeavyWorkerPolicy{
		Enabled:                 cfg.AIWorkflow.HeavyWorkerEnabled,
		RequireReason:           cfg.AIWorkflow.HeavyWorkerRequireReason,
		FileCountThreshold:      cfg.AIWorkflow.HeavyWorkerFileThreshold,
		SpecCountThreshold:      cfg.AIWorkflow.HeavyWorkerSpecThreshold,
		FailedAttemptsThreshold: cfg.AIWorkflow.HeavyWorkerRetryThreshold,
	})
	if d.dciSearcher != nil {
		distOrch.SetDCISearcher(d.dciSearcher)
		log.Println("DCI explicit trigger integrated with DistributedOrchestrator")
	}
	if d.recallTraceStore != nil {
		distOrch.SetRecallTraceStore(d.recallTraceStore)
		log.Println("Recall trace store integrated with DistributedOrchestrator")
	}
	if d.skillBootstrap != nil {
		distOrch.SetSkillBootstrapRecorder(d.skillBootstrap)
		log.Println("Skill Bootstrap integrated with DistributedOrchestrator")
	}
	if d.coderProposalEvidence != nil {
		distOrch.SetCoderProposalEvidenceRecorder(d.coderProposalEvidence)
		log.Println("Coder proposal evidence recorder integrated with DistributedOrchestrator")
	}
	if d.aiWorkflowStore != nil {
		distOrch.SetWorkflowEventRecorder(d.aiWorkflowStore)
		distOrch.SetCommandRegistry(d.aiWorkflowStore)
		log.Println("AI Workflow event recorder integrated with DistributedOrchestrator")
	}
	if d.superAgentStore != nil {
		distOrch.SetSuperAgentRuntimeRecorder(d.superAgentStore)
		distOrch.SetSuperAgentRunController(d.superAgentRunController)
		log.Println("SuperAgent runtime recorder integrated with DistributedOrchestrator")
	}

	// v4.1: SSH 経由で CoderConfig を送信するための設定
	coderConfigs := make(map[string]interface{})
	if cfg.Coder1.Enabled && distributedAgentAvailable("coder1", localTransports, sshTransports) {
		coderConfigs["coder1"] = coderConfigWithRuntimePersonality(cfg, cfg.Coder1)
	}
	if cfg.Coder2.Enabled && distributedAgentAvailable("coder2", localTransports, sshTransports) {
		coderConfigs["coder2"] = coderConfigWithRuntimePersonality(cfg, cfg.Coder2)
	}
	if cfg.Coder3.Enabled && distributedAgentAvailable("coder3", localTransports, sshTransports) {
		coderConfigs["coder3"] = coderConfigWithRuntimePersonality(cfg, cfg.Coder3)
	}
	if cfg.Coder4.Enabled && distributedAgentAvailable("coder4", localTransports, sshTransports) {
		coderConfigs["coder4"] = coderConfigWithRuntimePersonality(cfg, cfg.Coder4)
	}
	distOrch.SetCoderConfigs(coderConfigs)

	distOrch.SetMaxRepair(cfg.Worker.MaxRepair)
	distOrch.SetDistributedTimeouts(cfg.Distributed.CoderTimeoutSec, cfg.Distributed.CoderRetryMax)
	distOrch.SetTTSBridge(ttsBridge)
	distOrch.SetVTuberBridge(vtuberBridge)
	if d.reportStore != nil {
		distOrch.SetReportStore(d.reportStore)
	}
	if d.eventRelay != nil {
		distOrch.SetEventListener(d.eventRelay)
	}
	lineHandler := line.NewHandler(distOrch, cfg.Line.ChannelSecret, cfg.Line.AccessToken)
	lineHandler.SetAttachmentSaver(attachmentapp.NewStore(cfg.WorkspaceDir))
	applyLineChannelPolicy(lineHandler, cfg.Line)
	d.lineHandler = lineHandler
	if strings.TrimSpace(cfg.Telegram.BotToken) != "" {
		tg := telegramadapter.NewAdapter(cfg.Telegram.BotToken, distOrch)
		tg.SetWebhookSecret(cfg.Telegram.WebhookSecret)
		tg.SetAttachmentSaver(attachmentapp.NewStore(cfg.WorkspaceDir))
		d.telegramHandler = tg
	}
	if strings.TrimSpace(cfg.Discord.BotToken) != "" {
		dc := discordadapter.NewAdapter(cfg.Discord.BotToken, distOrch)
		dc.SetPublicKeyHex(cfg.Discord.PublicKey)
		dc.SetAttachmentSaver(attachmentapp.NewStore(cfg.WorkspaceDir))
		d.discordHandler = dc
	}
	if strings.TrimSpace(cfg.Slack.BotToken) != "" {
		sl := slackadapter.NewAdapter(cfg.Slack.BotToken, cfg.Slack.SigningSecret, distOrch)
		sl.SetAttachmentSaver(attachmentapp.NewStore(cfg.WorkspaceDir))
		d.slackHandler = sl
	}

	// IdleChat統合（有効な場合）
	if d.idleChatOrch != nil {
		distOrch.SetIdleNotifier(d.idleChatOrch)
		log.Printf("IdleChat integrated with DistributedOrchestrator")
	}
}
