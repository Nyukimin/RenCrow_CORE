package orchestrator

import (
	"context"
	"fmt"
	"log"
	"time"

	moduleapp "github.com/Nyukimin/RenCrow_CORE/internal/application/moduleregistry"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/service"
	appsubagent "github.com/Nyukimin/RenCrow_CORE/internal/application/subagent"
	appverification "github.com/Nyukimin/RenCrow_CORE/internal/application/verification"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
	domainpersona "github.com/Nyukimin/RenCrow_CORE/internal/domain/persona"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/proposal"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domainskill "github.com/Nyukimin/RenCrow_CORE/internal/domain/skillgovernance"
	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domainverification "github.com/Nyukimin/RenCrow_CORE/internal/domain/verification"
)

// ProcessMessageRequest はメッセージ処理リクエスト
type ProcessMessageRequest struct {
	SessionID   string
	Channel     string
	ChatID      string
	UserMessage string
	To          string
	Attachments []attachment.Attachment
}

// ProcessMessageResponse はメッセージ処理レスポンス
type ProcessMessageResponse struct {
	Response     string
	Route        routing.Route
	Confidence   float64
	JobID        string
	Verification *domainverification.VerificationReport `json:"verification,omitempty"`
}

// Orchestrator は MessageOrchestrator と DistributedOrchestrator の共通インターフェース。
// 各アダプター（LINE / Slack / Telegram / Discord）はこのインターフェースに依存する。
type Orchestrator interface {
	ProcessMessage(ctx context.Context, req ProcessMessageRequest) (ProcessMessageResponse, error)
}

// SessionRepository はセッション永続化のインターフェース
type SessionRepository interface {
	Save(ctx context.Context, sess *session.Session) error
	Load(ctx context.Context, id string) (*session.Session, error)
	Exists(ctx context.Context, id string) (bool, error)
	Delete(ctx context.Context, id string) error
}

// MioAgent はルーティング・会話を担当
type MioAgent interface {
	DecideAction(ctx context.Context, t task.Task) (routing.Decision, error)
	Chat(ctx context.Context, t task.Task) (string, error)
	HandleChatCommand(ctx context.Context, sessionID string, message string) (agent.ChatCommandResult, error)
}

// ShiroAgent は実行を担当
type ShiroAgent interface {
	Execute(ctx context.Context, t task.Task) (string, error)
}

// CoderAgent はコード生成を担当
type CoderAgent interface {
	Generate(ctx context.Context, t task.Task, systemPrompt string) (string, error)
}

// WildAgent は創作Wildを担当
type WildAgent interface {
	Generate(ctx context.Context, t task.Task) (string, error)
}

// HeavyAgent は深い分析・診断を担当
type HeavyAgent interface {
	Generate(ctx context.Context, t task.Task) (string, error)
}

type ResponseVerifier interface {
	VerifyResponse(ctx context.Context, req appverification.Request) (appverification.Result, error)
}

// DCISearcher は明示的な直接コーパス探索を担当する。
type DCISearcher interface {
	ShouldTrigger(query string) bool
	Search(ctx context.Context, query string) (domaindci.SearchResult, error)
}

type RecallTraceStore interface {
	SaveRecallTrace(ctx context.Context, trace domainconversation.RecallTrace) error
}

type SkillBootstrapRecorder interface {
	Record(ctx context.Context, task domainskill.TaskContext, usedSkillIDs []string) ([]domainskill.SkillTriggerLog, error)
}

type CoderProposalEvidenceRecorder interface {
	SaveCoderProposalEvidence(ctx context.Context, evidence domainskill.CoderProposalEvidence) (domainskill.CoderProposalEvidencePaths, error)
}

type WorkflowEventRecorder interface {
	SaveWorkflowEvent(ctx context.Context, item domainai.WorkflowEvent) error
}

type CommandRegistryLister interface {
	ListCommandRegistries(ctx context.Context, limit int) ([]domainai.CommandRegistry, error)
}

type SuperAgentRuntimeRecorder interface {
	SaveAgentRun(ctx context.Context, item domainsuperagent.AgentRun) error
	SaveContextPack(ctx context.Context, item domainsuperagent.ContextPack) error
	SaveTraceEvent(ctx context.Context, item domainsuperagent.TraceEvent) error
}

type SuperAgentRunController interface {
	RegisterRun(ctx context.Context, runID string) (context.Context, func())
	IsPauseRequested(runID string) bool
}

type PersonaRuntimeRecorder interface {
	SaveTriggerLog(ctx context.Context, item domainpersona.TriggerLog) error
	SaveCanonicalResponseLog(ctx context.Context, item domainpersona.CanonicalResponseLog) error
	ListCanonicalResponseLogs(ctx context.Context, limit int) ([]domainpersona.CanonicalResponseLog, error)
	SaveObservationLog(ctx context.Context, item domainpersona.ObservationLog) error
	SaveMetaProfileUpdate(ctx context.Context, item domainpersona.MetaProfileUpdate) error
	SaveInterfaceSession(ctx context.Context, item domainpersona.InterfaceSession) error
}

// CoderAgentWithProposal はProposal生成機能を持つCoderAgent
type CoderAgentWithProposal interface {
	CoderAgent
	GenerateProposal(ctx context.Context, t task.Task) (*proposal.Proposal, error)
}

// SessionTurnLogger はセッション単位の会話ターンを記録するインターフェース
type SessionTurnLogger interface {
	WriteUser(sessionID, channel, content string)
	WriteAssistant(sessionID, channel, route, jobID, content string)
}

// MessageOrchestrator はメッセージ処理を統括
type MessageOrchestrator struct {
	sessionRepo               SessionRepository
	mio                       MioAgent
	shiro                     ShiroAgent
	coder1                    CoderAgent // Slot 1
	coder2                    CoderAgent // Slot 2
	coder3                    CoderAgent // Slot 3
	coder4                    CoderAgent // Slot 4 (v4.1)
	wild                      WildAgent
	heavy                     HeavyAgent
	workerExecution           service.WorkerExecutionService
	coderStatus               *CoderStatus
	codeExecutor              CodeExecutor // Phase 1リファクタリング: コード実行を委譲
	listener                  EventListener
	reporter                  ReportStore
	idleNotifier              IdleNotifier
	ttsBridge                 TTSBridge
	vtuberBridge              VTuberBridge
	verifier                  ResponseVerifier
	dciSearcher               DCISearcher
	recallTrace               RecallTraceStore
	skillBootstrap            SkillBootstrapRecorder
	coderProposalEvidence     CoderProposalEvidenceRecorder
	workflowEvents            WorkflowEventRecorder
	commandRegistry           CommandRegistryLister
	superAgentRuns            SuperAgentRuntimeRecorder
	superAgentRunController   SuperAgentRunController
	personaRuntime            PersonaRuntimeRecorder
	personaTriggers           []domainpersona.TriggerDefinition
	personaCanonicalResponses []domainpersona.CanonicalResponseDefinition
	maxRepair                 int // 0以下は1とみなす
	sessionTurnLogger         SessionTurnLogger

	sessions             *messageSessionLifecycle
	responses            messageResponseAssembler
	preRoutingCommands   *preRoutingCommandHandler
	routeDecisions       *routeDecisionCoordinator
	idleBusyGuards       *idleBusyGuardFactory
	autonomousExecutions *autonomousExecutionCoordinator
	routeDispatcher      *messageRouteDispatcher
	ttsLifecycle         *messageTTSLifecycle
	events               *messageEventPort
	taskContexts         *messageTaskContextBuilder
}

// SetMaxRepair は自律実行のリペア上限を設定する（デフォルト: 1）
func (o *MessageOrchestrator) SetMaxRepair(n int) {
	if n > 0 {
		o.maxRepair = n
	}
}

func (o *MessageOrchestrator) maxRepairOrDefault() int {
	if o.maxRepair > 0 {
		return o.maxRepair
	}
	return 1
}

// NewMessageOrchestrator は新しいMessageOrchestratorを作成
func NewMessageOrchestrator(
	sessionRepo SessionRepository,
	mio MioAgent,
	shiro ShiroAgent,
	coder1 CoderAgent,
	coder2 CoderAgent,
	coder3 CoderAgent,
	coder4 CoderAgent,
	workerExecution service.WorkerExecutionService,
) *MessageOrchestrator {
	coderStatus := NewCoderStatus()

	// CodeExecutorを初期化（イベント発火は後でSetEventListenerで設定）
	codeExecutor := NewDefaultCodeExecutor(
		coder1,
		coder2,
		coder3,
		coder4,
		workerExecution,
		coderStatus,
		nil, // eventEmitterは後でSetEventListenerで設定
	).WithModuleResolver(moduleapp.DefaultRegistry())
	// CoderLoop プロンプトは SetCoderLoopPrompt で後から注入する

	orch := &MessageOrchestrator{
		sessionRepo:     sessionRepo,
		mio:             mio,
		shiro:           shiro,
		coder1:          coder1,
		coder2:          coder2,
		coder3:          coder3,
		coder4:          coder4,
		workerExecution: workerExecution,
		coderStatus:     coderStatus,
		codeExecutor:    codeExecutor,
	}
	orch.events = newMessageEventPort(nil)
	orch.responses = messageResponseAssembler{}
	orch.sessions = newMessageSessionLifecycle(sessionRepo)
	orch.taskContexts = newMessageTaskContextBuilder(orch.events.Emit, orch.ttsEnabled)
	orch.preRoutingCommands = newPreRoutingCommandHandler(mio, orch.events.Emit, orch.responses)
	orch.routeDecisions = newRouteDecisionCoordinator(mio, orch.events.Emit)
	orch.idleBusyGuards = newIdleBusyGuardFactory(nil)
	orch.ttsLifecycle = newMessageTTSLifecycle(nil, nil, orch.events.Emit)
	orch.routeDispatcher = newMessageRouteDispatcher(
		mio,
		shiro,
		codeExecutor,
		orch.events.Emit,
		orch.ttsLifecycle.WithStreamHooks,
		orch.ttsLifecycle.Push,
	)
	orch.autonomousExecutions = newAutonomousExecutionCoordinator(nil, orch.maxRepairOrDefault, orch.events.Emit, orch.routeDispatcher.ExecuteDirect)
	orch.routeDispatcher.SetAutonomousExecutor(orch.autonomousExecutions.Execute)
	return orch
}

// SetEventListener sets an optional listener for monitoring events.
func (o *MessageOrchestrator) SetEventListener(l EventListener) {
	o.listener = l
	if o.events != nil {
		o.events.SetListener(l)
	}
	// CodeExecutorにもイベント発火関数を設定
	if executor, ok := o.codeExecutor.(*DefaultCodeExecutor); ok {
		executor.SetEventEmitter(o.events.Emit)
	}
}

// SetCoderLoopPrompt は全 Coder スロットに CoderLoop システムプロンプトを設定する。
// prompt が空の場合は何もしない。
func (o *MessageOrchestrator) SetCoderLoopPrompt(prompt string) {
	if prompt == "" {
		return
	}
	if executor, ok := o.codeExecutor.(*DefaultCodeExecutor); ok {
		executor.WithCoderLoopPrompts(map[string]string{
			"coder1": prompt,
			"coder2": prompt,
			"coder3": prompt,
			"coder4": prompt,
		})
	}
}

// SetSessionTurnLogger はセッション会話ターンロガーを設定する
func (o *MessageOrchestrator) SetSessionTurnLogger(l SessionTurnLogger) {
	o.sessionTurnLogger = l
}

// SetCoderCapabilities は診断用の能力情報を注入する。Coder 選択は明示 route と Coder1 既定に限定する。
func (o *MessageOrchestrator) SetCoderCapabilities(caps []capability.CoderCapability) {
	if executor, ok := o.codeExecutor.(*DefaultCodeExecutor); ok {
		executor.WithCapabilities(caps)
	}
}

func (o *MessageOrchestrator) SetExternalCoderPolicy(external map[string]bool) {
	if executor, ok := o.codeExecutor.(*DefaultCodeExecutor); ok {
		executor.WithExternalCoderPolicy(external)
	}
}

func (o *MessageOrchestrator) SetWildAgent(wild WildAgent) {
	o.wild = wild
	if o.routeDispatcher != nil {
		o.routeDispatcher.SetWildAgent(wild)
	}
}

func (o *MessageOrchestrator) SetHeavyAgent(heavy HeavyAgent) {
	o.heavy = heavy
	if o.routeDispatcher != nil {
		o.routeDispatcher.SetHeavyAgent(heavy)
	}
}

func (o *MessageOrchestrator) SetHeavyWorkerPolicy(policy domainai.HeavyWorkerPolicy) {
	if o.routeDecisions != nil {
		o.routeDecisions.SetHeavyWorkerPolicy(policy)
	}
}

func (o *MessageOrchestrator) SetReportStore(store ReportStore) {
	o.reporter = store
	if o.autonomousExecutions != nil {
		o.autonomousExecutions.SetReportStore(store)
	}
}

func (o *MessageOrchestrator) SetVerificationPipeline(verifier ResponseVerifier) {
	o.verifier = verifier
}

func (o *MessageOrchestrator) SetDCISearcher(searcher DCISearcher) {
	o.dciSearcher = searcher
}

func (o *MessageOrchestrator) SetRecallTraceStore(store RecallTraceStore) {
	o.recallTrace = store
}

func (o *MessageOrchestrator) SetSkillBootstrapRecorder(recorder SkillBootstrapRecorder) {
	o.skillBootstrap = recorder
}

func (o *MessageOrchestrator) SetCoderProposalEvidenceRecorder(recorder CoderProposalEvidenceRecorder) {
	o.coderProposalEvidence = recorder
	if executor, ok := o.codeExecutor.(*DefaultCodeExecutor); ok {
		executor.WithCoderProposalEvidenceRecorder(recorder)
	}
}

func (o *MessageOrchestrator) SetWorkflowEventRecorder(recorder WorkflowEventRecorder) {
	o.workflowEvents = recorder
	if o.routeDecisions != nil {
		o.routeDecisions.SetWorkflowEventRecorder(recorder)
	}
	if o.routeDispatcher != nil {
		o.routeDispatcher.SetWorkflowEventRecorder(recorder)
	}
}

func (o *MessageOrchestrator) SetCommandRegistry(registry CommandRegistryLister) {
	o.commandRegistry = registry
}

func (o *MessageOrchestrator) SetSuperAgentRuntimeRecorder(recorder SuperAgentRuntimeRecorder) {
	o.superAgentRuns = recorder
}

func (o *MessageOrchestrator) SetSuperAgentRunController(controller SuperAgentRunController) {
	o.superAgentRunController = controller
}

func (o *MessageOrchestrator) SetPersonaRuntimeRecorder(recorder PersonaRuntimeRecorder, triggers []domainpersona.TriggerDefinition) {
	o.personaRuntime = recorder
	o.personaTriggers = append([]domainpersona.TriggerDefinition(nil), triggers...)
}

func (o *MessageOrchestrator) SetPersonaCanonicalResponses(definitions []domainpersona.CanonicalResponseDefinition) {
	o.personaCanonicalResponses = append([]domainpersona.CanonicalResponseDefinition(nil), definitions...)
}

// SetIdleNotifier sets an optional notifier used to control idle chat.
func (o *MessageOrchestrator) SetIdleNotifier(n IdleNotifier) {
	o.idleNotifier = n
	if o.idleBusyGuards != nil {
		o.idleBusyGuards.SetNotifier(n)
	}
}

// SetTTSBridge sets an optional TTS bridge.
func (o *MessageOrchestrator) SetTTSBridge(b TTSBridge) {
	o.ttsBridge = b
	if o.ttsLifecycle != nil {
		o.ttsLifecycle.SetTTSBridge(b)
	}
}

// SetVTuberBridge sets an optional VTuber bridge.
func (o *MessageOrchestrator) SetVTuberBridge(b VTuberBridge) {
	o.vtuberBridge = b
	if o.ttsLifecycle != nil {
		o.ttsLifecycle.SetVTuberBridge(b)
	}
}

func (o *MessageOrchestrator) ttsEnabled() bool {
	return o.ttsBridge != nil
}

// ProcessMessage はメッセージを処理
func (o *MessageOrchestrator) ProcessMessage(ctx context.Context, req ProcessMessageRequest) (ProcessMessageResponse, error) {
	latencyStartedAt := time.Now()
	ctx = contextWithLatencyTrace(ctx, latencyStartedAt)
	log.Printf("[MessageOrch] ProcessMessage START: sessionID=%s channel=%s chatID=%s message=%q",
		req.SessionID, req.Channel, req.ChatID, req.UserMessage)

	endChatBusy := o.idleBusyGuards.BeginChat()
	defer endChatBusy()

	sess, err := o.sessions.LoadForRequest(ctx, req)
	if err != nil {
		return ProcessMessageResponse{}, err
	}

	emitLatencyMetric(o.events.Emit, "network", "server_received", latencyStartedAt, "", "", req.SessionID, req.Channel, req.ChatID, "")
	if o.sessionTurnLogger != nil {
		o.sessionTurnLogger.WriteUser(req.SessionID, req.Channel, req.UserMessage)
	}
	if err := o.recordPersonaRuntimeObservation(ctx, req); err != nil {
		return ProcessMessageResponse{}, err
	}
	if resp, handled, err := o.preRoutingCommands.Handle(ctx, req); err != nil {
		return ProcessMessageResponse{}, err
	} else if handled {
		return resp, nil
	}
	if expandedReq, handled, err := o.expandRegisteredSlashCommand(ctx, req); err != nil {
		return ProcessMessageResponse{}, err
	} else if handled {
		req = expandedReq
	}

	jobID := task.NewJobID()
	o.events.EmitMessageReceived(req, jobID.String())
	t, jobID, ttsSessionID := o.taskContexts.BuildWithJobID(req, jobID)
	if resp, handled, err := o.handleExplicitDCI(ctx, req, sess, t.WithRoute(routing.RouteRESEARCH), jobID); err != nil {
		return ProcessMessageResponse{}, err
	} else if handled {
		return resp, nil
	}

	decision, err := o.routeDecisions.Decide(ctx, t, req, jobID)
	if err != nil {
		return ProcessMessageResponse{}, err
	}
	emitLatencyMetric(o.events.Emit, "llm", "route_decision", latencyStartedAt, string(decision.Route), jobID.String(), req.SessionID, req.Channel, req.ChatID, decision.Reason)

	t = t.WithRoute(decision.Route)
	if err := o.recordRouteSkillBootstrap(ctx, req, decision.Route); err != nil {
		return ProcessMessageResponse{}, err
	}
	o.ttsLifecycle.StartSessionForRoute(ctx, req, jobID, decision, ttsSessionID)

	endWorkerBusy := o.idleBusyGuards.BeginWorker(decision.Route)
	defer endWorkerBusy()

	runStartedAt, err := recordLeadAgentRunStarted(ctx, o.superAgentRuns, req, jobID, decision.Route)
	if err != nil {
		return ProcessMessageResponse{}, err
	}
	leadRunID := leadAgentRunID(jobID)
	if o.superAgentRunController != nil {
		var unregister func()
		ctx, unregister = o.superAgentRunController.RegisterRun(ctx, leadRunID)
		defer unregister()
	}
	ctx = appsubagent.WithSuperAgentRuntime(ctx, leadRunID, []string{"session:" + req.SessionID, "route:" + string(decision.Route)}, nil, "return summary-only subagent result to Lead Agent")

	// 4. ルートに応じて実行
	emitLatencyMetric(o.events.Emit, "llm", "dispatch_start", latencyStartedAt, string(decision.Route), jobID.String(), req.SessionID, req.Channel, req.ChatID, "")
	response, err := o.routeDispatcher.ExecuteTask(ctx, t, decision.Route, req.SessionID, req.Channel, req.ChatID, ttsSessionID)
	if err != nil {
		if o.superAgentRunController != nil && o.superAgentRunController.IsPauseRequested(leadRunID) {
			_ = recordLeadAgentRunFinished(context.Background(), o.superAgentRuns, req, jobID, decision.Route, runStartedAt, "paused", "pause requested; task execution canceled")
		} else {
			_ = recordLeadAgentRunFinished(ctx, o.superAgentRuns, req, jobID, decision.Route, runStartedAt, "failed", err.Error())
		}
		return ProcessMessageResponse{}, fmt.Errorf("task execution failed: %w", err)
	}
	emitLatencyMetric(o.events.Emit, "llm", "response_complete", latencyStartedAt, string(decision.Route), jobID.String(), req.SessionID, req.Channel, req.ChatID, fmt.Sprintf("response_len=%d", len(response)))
	o.ttsLifecycle.EndSession(ctx, ttsSessionID)

	var verificationReport *domainverification.VerificationReport
	if o.verifier != nil {
		verification, err := o.verifier.VerifyResponse(ctx, appverification.Request{
			DraftResponse: response,
			UserMessage:   req.UserMessage,
			Route:         string(decision.Route),
			SessionID:     req.SessionID,
			Channel:       req.Channel,
			ChatID:        req.ChatID,
			JobID:         jobID.String(),
		})
		if err != nil {
			_ = recordLeadAgentRunFinished(ctx, o.superAgentRuns, req, jobID, decision.Route, runStartedAt, "failed", err.Error())
			return ProcessMessageResponse{}, fmt.Errorf("response verification failed: %w", err)
		}
		response = verification.Response
		verificationReport = &verification.Report
		o.events.Emit("verification.report", "verification", "viewer", string(verification.Report.Status), string(decision.Route), jobID.String(), req.SessionID, req.Channel, req.ChatID)
	}

	if applied, err := o.applyPersonaCanonicalResponse(ctx, req, response); err != nil {
		_ = recordLeadAgentRunFinished(ctx, o.superAgentRuns, req, jobID, decision.Route, runStartedAt, "failed", err.Error())
		return ProcessMessageResponse{}, err
	} else if applied != "" {
		response = applied
	}

	if err := o.sessions.SaveCompletedTask(ctx, sess, t); err != nil {
		_ = recordLeadAgentRunFinished(ctx, o.superAgentRuns, req, jobID, decision.Route, runStartedAt, "failed", err.Error())
		return ProcessMessageResponse{}, err
	}
	if err := recordLeadAgentRunFinished(ctx, o.superAgentRuns, req, jobID, decision.Route, runStartedAt, "completed", "Lead Agent completed"); err != nil {
		return ProcessMessageResponse{}, err
	}

	log.Printf("[MessageOrch] ProcessMessage COMPLETE: jobID=%s route=%s response_len=%d",
		jobID.String(), decision.Route, len(response))
	if o.sessionTurnLogger != nil {
		o.sessionTurnLogger.WriteAssistant(req.SessionID, req.Channel, string(decision.Route), jobID.String(), response)
	}

	return o.responses.BuildWithVerification(response, decision, jobID, verificationReport), nil
}
