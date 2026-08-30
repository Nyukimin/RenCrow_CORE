package orchestrator

import (
	"context"
	"errors"
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
	domaindurablestore "github.com/Nyukimin/RenCrow_CORE/internal/domain/durablestore"
	domainnews "github.com/Nyukimin/RenCrow_CORE/internal/domain/newsbrief"
	domainpersona "github.com/Nyukimin/RenCrow_CORE/internal/domain/persona"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/proposal"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domainskill "github.com/Nyukimin/RenCrow_CORE/internal/domain/skillgovernance"
	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domainverification "github.com/Nyukimin/RenCrow_CORE/internal/domain/verification"
	domainvision "github.com/Nyukimin/RenCrow_CORE/internal/domain/vision"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type AudioOutputIntent string

const (
	AudioOutputRequested AudioOutputIntent = "requested"
	AudioOutputDisabled  AudioOutputIntent = "disabled"
)

// ProcessMessageRequest はメッセージ処理リクエスト
type ProcessMessageRequest struct {
	JobID                    string
	MessageID                string
	TraceID                  string
	SessionID                string
	Channel                  string
	ChatID                   string
	UserMessage              string
	To                       string
	OperationSource          string
	AudioOutput              AudioOutputIntent
	Attachments              []attachment.Attachment
	ResumeCheckpointRevision int
	ResumeCheckpointSummary  string
	ResumeNextAction         string
	originalUserMessage      string
}

// ProcessMessageResponse はメッセージ処理レスポンス
type ProcessMessageResponse struct {
	Response        string
	Route           routing.Route
	Confidence      float64
	JobID           string
	MessageID       string                                 `json:"message_id,omitempty"`
	TraceID         string                                 `json:"trace_id,omitempty"`
	Verification    *domainverification.VerificationReport `json:"verification,omitempty"`
	Capability      string                                 `json:"capability,omitempty"`
	StorageWorkflow *domaindurablestore.WorkflowResult     `json:"storage_workflow,omitempty"`
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

type CanonicalEventRecorder interface {
	modulecore.EventAppender
}

type CommandRegistryLister interface {
	ListCommandRegistries(ctx context.Context, limit int) ([]domainai.CommandRegistry, error)
}

type SuperAgentRuntimeRecorder interface {
	SaveAgentRun(ctx context.Context, item domainsuperagent.AgentRun) error
	SaveContextPack(ctx context.Context, item domainsuperagent.ContextPack) error
	modulecore.EventAppender
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
	canonicalEvents           CanonicalEventRecorder
	commandRegistry           CommandRegistryLister
	superAgentRuns            SuperAgentRuntimeRecorder
	superAgentRunController   SuperAgentRunController
	personaRuntime            PersonaRuntimeRecorder
	personaTriggers           []domainpersona.TriggerDefinition
	personaCanonicalResponses []domainpersona.CanonicalResponseDefinition
	maxRepair                 int // 0以下は1とみなす
	sessionTurnLogger         SessionTurnLogger

	sessions                *messageSessionLifecycle
	responses               messageResponseAssembler
	preRoutingCommands      *preRoutingCommandHandler
	dailyNewsBriefReader    domainnews.DailyNewsBriefReader
	dailyNewsBriefCollector domainnews.DailyNewsBriefCollector
	shiroChat               MioAgent
	routeDecisions          *routeDecisionCoordinator
	idleBusyGuards          *idleBusyGuardFactory
	autonomousExecutions    *autonomousExecutionCoordinator
	routeDispatcher         *messageRouteDispatcher
	ttsLifecycle            *messageTTSLifecycle
	events                  *messageEventPort
	taskContexts            *messageTaskContextBuilder
	visionRequests          *visionRequestProcessor
	durableStoreWorkflow    DurableStoreWorkflow
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

// SetVisionAnalyzer installs the only raw image/video recognition path used by CORE.
func (o *MessageOrchestrator) SetVisionAnalyzer(analyzer domainvision.Analyzer, options VisionOptions) {
	o.visionRequests = newVisionRequestProcessor(analyzer, options)
}

func (o *MessageOrchestrator) SetHeavyAgent(heavy HeavyAgent) {
	o.heavy = heavy
	if o.routeDispatcher != nil {
		o.routeDispatcher.SetHeavyAgent(heavy)
	}
}

// SetShiroChatAgent selects the ChatWorker-backed conversational Shiro.
func (o *MessageOrchestrator) SetShiroChatAgent(chat MioAgent) {
	o.shiroChat = chat
	if o.routeDispatcher != nil {
		o.routeDispatcher.SetShiroChatAgent(chat)
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

func (o *MessageOrchestrator) SetCanonicalEventRecorder(recorder CanonicalEventRecorder) {
	o.canonicalEvents = recorder
	if o.routeDecisions != nil {
		o.routeDecisions.SetCanonicalEventRecorder(recorder)
	}
	if o.routeDispatcher != nil {
		o.routeDispatcher.SetCanonicalEventRecorder(recorder)
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
func (o *MessageOrchestrator) ProcessMessage(ctx context.Context, req ProcessMessageRequest) (resp ProcessMessageResponse, err error) {
	latencyStartedAt := time.Now()
	ctx = contextWithLatencyTrace(ctx, latencyStartedAt)
	jobID := resolveProcessMessageJobID(req.JobID)
	req.JobID = jobID.String()
	ensureProcessRequestIdentity(&req)
	traceID := modulecore.TraceID(req.TraceID)
	ctx = contextWithCanonicalTrace(ctx, traceID)
	o.events.BindTrace(jobID.String(), modulecore.TraceID(req.TraceID))
	defer o.events.ReleaseTrace(jobID.String())
	ctx, cancel := context.WithCancelCause(ctx)
	publicationFailures := o.events.publicationFail
	if publicationFailures != nil {
		publicationFailures.Begin(traceID, cancel)
	}
	defer func() {
		var publicationErr error
		if publicationFailures != nil {
			publicationErr = publicationFailures.End(traceID)
		}
		if publicationErr == nil {
			cancel(nil)
			return
		}
		cancel(publicationErr)
		wrapped := fmt.Errorf("canonical event publication failed: %w", publicationErr)
		resp = ProcessMessageResponse{}
		if err == nil {
			err = wrapped
		} else if !errors.Is(err, publicationErr) {
			err = errors.Join(err, wrapped)
		}
	}()
	preserveOriginalUserMessage(&req)
	log.Printf("[MessageOrch] ProcessMessage START: jobID=%s traceID=%s messageID=%s sessionID=%s channel=%s chatID=%s message=%q",
		jobID.String(), req.TraceID, req.MessageID, req.SessionID, req.Channel, req.ChatID, req.UserMessage)
	if err := o.events.EmitMessageReceived(req, jobID.String()); err != nil {
		return ProcessMessageResponse{}, err
	}

	endChatBusy := o.idleBusyGuards.BeginChat()
	defer endChatBusy()

	sess, err := o.sessions.LoadForRequest(ctx, req)
	if err != nil {
		return ProcessMessageResponse{}, err
	}

	emitLatencyMetric(o.events.Emit, "network", "server_received", latencyStartedAt, "", jobID.String(), req.SessionID, req.Channel, req.ChatID, "")
	if err := o.events.PublicationError(traceID); err != nil {
		return ProcessMessageResponse{}, err
	}
	writeUserSessionTurn(o.sessionTurnLogger, req)
	if err := o.recordPersonaRuntimeObservation(ctx, req); err != nil {
		return ProcessMessageResponse{}, err
	}
	if resp, handled, err := o.preRoutingCommands.Handle(ctx, req); err != nil {
		return ProcessMessageResponse{}, err
	} else if handled {
		if err := o.events.PublicationError(traceID); err != nil {
			return ProcessMessageResponse{}, err
		}
		resp = ensureProcessResponseIdentity(resp, jobID.String(), req.TraceID, o.events.TakeResponseMessageID)
		writeAssistantSessionTurn(o.sessionTurnLogger, req, resp)
		return resp, nil
	}
	if expandedReq, handled, err := o.expandRegisteredSlashCommand(ctx, req); err != nil {
		return ProcessMessageResponse{}, err
	} else if handled {
		req = expandedReq
	}
	if o.visionRequests != nil {
		processed, err := o.visionRequests.Process(ctx, req, o.events.Emit)
		if err != nil {
			return ProcessMessageResponse{}, err
		}
		req = processed
		if err := o.events.PublicationError(traceID); err != nil {
			return ProcessMessageResponse{}, err
		}
	}

	t, jobID, ttsSessionID := o.taskContexts.BuildWithJobID(req, jobID)
	if resp, handled, err := o.handleDailyNewsBrief(ctx, req, sess, t, jobID, ttsSessionID); err != nil {
		return ProcessMessageResponse{}, err
	} else if handled {
		if err := o.events.PublicationError(traceID); err != nil {
			return ProcessMessageResponse{}, err
		}
		resp = ensureProcessResponseIdentity(resp, jobID.String(), req.TraceID, o.events.TakeResponseMessageID)
		writeAssistantSessionTurn(o.sessionTurnLogger, req, resp)
		return resp, nil
	}
	if resp, handled, err := o.handleExplicitDCI(ctx, req, sess, t.WithRoute(routing.RouteRESEARCH), jobID); err != nil {
		return ProcessMessageResponse{}, err
	} else if handled {
		if err := o.events.PublicationError(traceID); err != nil {
			return ProcessMessageResponse{}, err
		}
		resp = ensureProcessResponseIdentity(resp, jobID.String(), req.TraceID, o.events.TakeResponseMessageID)
		writeAssistantSessionTurn(o.sessionTurnLogger, req, resp)
		return resp, nil
	}
	if resp, handled, err := o.handleDurableStore(ctx, req, sess, t, jobID); err != nil {
		return ProcessMessageResponse{}, err
	} else if handled {
		if err := o.events.PublicationError(traceID); err != nil {
			return ProcessMessageResponse{}, err
		}
		resp = ensureProcessResponseIdentity(resp, jobID.String(), req.TraceID, o.events.TakeResponseMessageID)
		writeAssistantSessionTurn(o.sessionTurnLogger, req, resp)
		return resp, nil
	}

	decision, err := o.routeDecisions.Decide(ctx, t, req, jobID)
	if err != nil {
		return ProcessMessageResponse{}, err
	}
	emitLatencyMetric(o.events.Emit, "llm", "route_decision", latencyStartedAt, string(decision.Route), jobID.String(), req.SessionID, req.Channel, req.ChatID, decision.Reason)
	if err := o.events.PublicationError(traceID); err != nil {
		return ProcessMessageResponse{}, err
	}

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
	if err := o.events.PublicationError(traceID); err != nil {
		return ProcessMessageResponse{}, err
	}
	response, err := o.routeDispatcher.ExecuteTask(ctx, t, decision.Route, req.SessionID, req.Channel, req.ChatID, ttsSessionID)
	if publicationErr := o.events.PublicationError(traceID); publicationErr != nil {
		if err != nil {
			return ProcessMessageResponse{}, errors.Join(err, publicationErr)
		}
		return ProcessMessageResponse{}, publicationErr
	}
	if err != nil {
		if o.superAgentRunController != nil && o.superAgentRunController.IsPauseRequested(leadRunID) {
			_ = recordLeadAgentRunFinished(context.Background(), o.superAgentRuns, req, jobID, decision.Route, runStartedAt, "paused", "pause requested; task execution canceled")
		} else {
			_ = recordLeadAgentRunFinished(ctx, o.superAgentRuns, req, jobID, decision.Route, runStartedAt, "failed", err.Error())
		}
		return ProcessMessageResponse{}, fmt.Errorf("task execution failed: %w", err)
	}
	emitLatencyMetric(o.events.Emit, "llm", "response_complete", latencyStartedAt, string(decision.Route), jobID.String(), req.SessionID, req.Channel, req.ChatID, fmt.Sprintf("response_len=%d", len(response)))
	if err := o.events.PublicationError(traceID); err != nil {
		return ProcessMessageResponse{}, err
	}
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
		if err := o.events.PublicationError(traceID); err != nil {
			return ProcessMessageResponse{}, err
		}
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

	resp = ensureProcessResponseIdentity(
		o.responses.BuildWithVerification(response, decision, jobID, verificationReport),
		jobID.String(),
		req.TraceID,
		o.events.TakeResponseMessageID,
	)
	log.Printf("[MessageOrch] ProcessMessage COMPLETE: jobID=%s traceID=%s messageID=%s route=%s response_len=%d",
		jobID.String(), resp.TraceID, resp.MessageID, decision.Route, len(response))
	writeAssistantSessionTurn(o.sessionTurnLogger, req, resp)
	return resp, nil
}
