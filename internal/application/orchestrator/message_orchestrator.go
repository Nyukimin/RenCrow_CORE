package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	moduleapp "github.com/Nyukimin/RenCrow_CORE/internal/application/moduleregistry"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
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
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domainllm "github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domainnews "github.com/Nyukimin/RenCrow_CORE/internal/domain/newsbrief"
	domainpersona "github.com/Nyukimin/RenCrow_CORE/internal/domain/persona"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/proposal"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domainskill "github.com/Nyukimin/RenCrow_CORE/internal/domain/skillgovernance"
	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domainverification "github.com/Nyukimin/RenCrow_CORE/internal/domain/verification"
	domainvision "github.com/Nyukimin/RenCrow_CORE/internal/domain/vision"
	"github.com/Nyukimin/RenCrow_CORE/internal/turntiming"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type AudioOutputIntent string

const (
	AudioOutputRequested AudioOutputIntent = "requested"
	AudioOutputDisabled  AudioOutputIntent = "disabled"
)

// ProcessMessageRequest はメッセージ処理リクエスト
type ProcessMessageRequest struct {
	MessageID                string
	AgentMessageID           string
	TurnID                   string
	TraceID                  string
	RootTaskID               string
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
	ResumeCheckpointID       modulecore.CheckpointID
	CanonicalRunID           modulecore.RunID `json:"-"`
	mioAnswerDeltaSink       *mioAnswerDeltaSink
	originalUserMessage      string
}

// ProcessMessageResponse はメッセージ処理レスポンス
type ProcessMessageResponse struct {
	Response        string
	SessionID       string `json:"session_id,omitempty"`
	Route           routing.Route
	Confidence      float64
	TaskID          string                                 `json:"task_id,omitempty"`
	MessageID       string                                 `json:"message_id,omitempty"`
	TurnID          string                                 `json:"turn_id,omitempty"`
	TraceID         string                                 `json:"trace_id,omitempty"`
	RootTaskID      string                                 `json:"root_task_id,omitempty"`
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
	DecideAction(ctx context.Context, t domainconversation.TurnInput) (routing.Decision, error)
	Chat(ctx context.Context, t domainconversation.TurnInput) (string, error)
	HandleChatCommand(ctx context.Context, sessionID string, message string) (agent.ChatCommandResult, error)
}

// ShiroAgent は実行を担当
type ShiroAgent interface {
	Execute(ctx context.Context, t domainconversation.TurnInput) (string, error)
}

// CoderAgent はコード生成を担当
type CoderAgent interface {
	Generate(ctx context.Context, t domainconversation.TurnInput, systemPrompt string) (string, error)
}

// WildAgent は創作Wildを担当
type WildAgent interface {
	Generate(ctx context.Context, t domainconversation.TurnInput) (string, error)
}

// HeavyAgent は深い分析・診断を担当
type HeavyAgent interface {
	Generate(ctx context.Context, t domainconversation.TurnInput) (string, error)
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
	GenerateProposal(ctx context.Context, t domainconversation.TurnInput) (*proposal.Proposal, error)
}

// SessionTurnLogger はセッション単位の会話ターンを記録するインターフェース
type SessionTurnLogger interface {
	WriteUser(sessionID, channel, content string)
	WriteAssistant(sessionID, channel, route, taskID, content string)
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
	taskLifecycle             *taskLifecycle

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
	turnInputs              *messageTurnInputBuilder
	visionRequests          *visionRequestProcessor
	durableStoreWorkflow    DurableStoreWorkflow
}

func synchronousPersistenceBeforeEffect(ctx context.Context, effect string) (context.Context, error) {
	if _, admitted := persistencequeue.AdmissionFromContext(ctx); !admitted {
		return ctx, nil
	}
	fallback, ok := persistencequeue.SynchronousFallbackFromContext(ctx)
	if !ok {
		return ctx, fmt.Errorf("synchronous persistence fallback before %s: context has RAM admission but no synchronous fallback capability", effect)
	}
	fallbackCtx, err := fallback(ctx)
	if err != nil {
		return ctx, fmt.Errorf("synchronous persistence fallback before %s: %w", effect, err)
	}
	if fallbackCtx == nil {
		return ctx, fmt.Errorf("synchronous persistence fallback before %s returned a nil context", effect)
	}
	if _, ok := persistencequeue.AdmissionFromContext(fallbackCtx); ok {
		return ctx, fmt.Errorf("synchronous persistence fallback before %s returned a context with RAM admission", effect)
	}
	return fallbackCtx, nil
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
	orch.turnInputs = newMessageTurnInputBuilder(orch.events.Emit, orch.ttsEnabled)
	orch.preRoutingCommands = newPreRoutingCommandHandler(mio, orch.events.Emit, orch.responses)
	orch.routeDecisions = newRouteDecisionCoordinator(mio)
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
	orch.routeDispatcher.SetSpeechPlanPusher(orch.ttsLifecycle.PushSpeechPlan)
	orch.autonomousExecutions = newAutonomousExecutionCoordinator(nil, orch.maxRepairOrDefault, orch.events.Emit, orch.routeDispatcher.executeDirectForContext)
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

// SetTaskLifecycleManager installs the durable task lifecycle used by every
// accepted ProcessMessage branch. Nil leaves durable task persistence disabled.
func (o *MessageOrchestrator) SetTaskLifecycleManager(manager TaskLifecycleManager) {
	if manager == nil {
		o.taskLifecycle = nil
		return
	}
	o.taskLifecycle = newTaskLifecycle(manager)
}

// SetCoderCapabilities は enabled/probe-ready 候補情報を注入する。明示 route は指定Coderをpinし、汎用CODEはreadyなローカル候補をround-robin選択する。
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

// SetSpeechPlanEnabled opts CHAT into the typed SpeechPlan generation path.
func (o *MessageOrchestrator) SetSpeechPlanEnabled(enabled bool) {
	if o.routeDispatcher != nil {
		o.routeDispatcher.SetSpeechPlanEnabled(enabled)
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
	turntiming.Mark(ctx, turntiming.PointProcessStarted)
	latencyStartedAt := time.Now()
	ctx = contextWithLatencyTrace(ctx, latencyStartedAt)
	if err := ensureProcessRequestIdentity(&req); err != nil {
		return ProcessMessageResponse{}, err
	}
	turntiming.Mark(ctx, turntiming.PointProcessSessionStarted)
	sess, req, boundOwnerID, err := o.sessions.ResolveForRequestWithOwner(ctx, req, latencyStartedAt.UTC())
	turntiming.Mark(ctx, turntiming.PointProcessSessionEnded)
	if err != nil {
		return ProcessMessageResponse{}, err
	}
	binding, err := bindingForResolvedProcessSession(sess, req, boundOwnerID, domainconversation.BindingGenerationUnavailable)
	if err != nil {
		return ProcessMessageResponse{}, err
	}
	if generationRepository, ok := o.sessions.sessionRepo.(canonicalSessionBindingGenerationRepository); ok {
		generation, generationErr := generationRepository.BindingGeneration(ctx, binding)
		if generationErr == nil && generation != domainconversation.BindingGenerationUnavailable {
			binding, err = bindingForResolvedProcessSession(sess, req, boundOwnerID, generation)
			if err != nil {
				return ProcessMessageResponse{}, err
			}
		}
	}
	ctx = domainconversation.WithSessionBinding(ctx, binding)
	if binding.Audience() == domainconversation.SessionAudiencePrivate && boundOwnerID != "" {
		ctx = agent.ContextWithMioTurnGenerateRequestAdmissionFactory(ctx, func(admissionCtx context.Context, guard domainconversation.PreparedChatTurnGuard) (*domainllm.GenerateRequestAdmission, error) {
			return o.newPrivateMioGenerateRequestAdmission(admissionCtx, req, guard)
		})
	}
	defer func() {
		if err == nil {
			resp.SessionID = req.SessionID
		}
	}()
	preserveOriginalUserMessage(&req)
	rootTaskID := modulecore.TaskID(req.RootTaskID)
	taskID := rootTaskID
	executionTaskID := rootTaskID
	lifecycleCreated := false
	var attachedTask *domaintask.Task
	rejectAttachedMismatch := func(activationErr error) {
		if attachedTask != nil && errors.Is(activationErr, errAttachedTaskMismatch) {
			// Input identity/route drift is rejected before execution. Do not let
			// the normal error finalizer fail the already-owned canonical Run.
			lifecycleCreated = false
		}
	}
	rootAssignee := taskLifecycleMio
	if selected, direct := directViewerRootAssignee(req, o.durableStoreWorkflow, o.dciSearcher); direct {
		rootAssignee = selected
	}
	traceID := modulecore.TraceID(req.TraceID)
	ctx = contextWithCanonicalTrace(ctx, traceID)
	if timing := turntiming.FromContext(ctx); timing != nil {
		o.events.BindTiming(traceID, timing)
		defer o.events.ReleaseTiming(traceID)
	}
	ctx = withOrchestrationLLMObservation(ctx, rootTaskID, traceID, req.SessionID, "orchestrator.message")
	if err := o.events.BindConversationSessionBinding(rootTaskID.String(), modulecore.TurnID(req.TurnID), modulecore.TraceID(req.TraceID), binding); err != nil {
		return ProcessMessageResponse{}, fmt.Errorf("bind canonical Session publication identity to message events: %w", err)
	}
	o.events.BindResponseMessageID(rootTaskID.String(), modulecore.MessageID(req.AgentMessageID))
	defer func() {
		o.events.ReleaseTrace(rootTaskID.String())
		o.events.ReleaseResponseMessageID(rootTaskID.String())
	}()
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
	defer func() {
		if !lifecycleCreated || o.taskLifecycle == nil {
			return
		}
		lifecycleErr := err
		if publicationErr := publicationFailures.Current(traceID); publicationErr != nil {
			if lifecycleErr == nil {
				lifecycleErr = publicationErr
			} else {
				lifecycleErr = errors.Join(lifecycleErr, publicationErr)
			}
		}
		if finalizeErr := o.taskLifecycle.finish(context.WithoutCancel(ctx), rootTaskID, executionTaskID, resp.Response, lifecycleErr); finalizeErr != nil {
			if err == nil {
				err = finalizeErr
			} else {
				err = errors.Join(err, finalizeErr)
			}
			resp = ProcessMessageResponse{}
		}
	}()
	if req.CanonicalRunID != "" && o.taskLifecycle == nil {
		return ProcessMessageResponse{}, fmt.Errorf("canonical run resume requires configured task lifecycle")
	}
	if o.taskLifecycle != nil {
		turntiming.Mark(ctx, turntiming.PointRootTaskPreparationStarted)
		if req.CanonicalRunID != "" {
			attached, attachErr := o.taskLifecycle.attachExisting(ctx, req)
			turntiming.Mark(ctx, turntiming.PointRootTaskPreparationEnded)
			if attachErr != nil {
				return ProcessMessageResponse{}, attachErr
			}
			attachedTask = &attached
		} else {
			if _, createErr := o.taskLifecycle.createRootForAssignee(ctx, req, rootAssignee); createErr != nil {
				turntiming.Mark(ctx, turntiming.PointRootTaskPreparationEnded)
				return ProcessMessageResponse{}, createErr
			}
			turntiming.Mark(ctx, turntiming.PointRootTaskCreated)
			turntiming.Mark(ctx, turntiming.PointRootTaskPreparationEnded)
		}
		lifecycleCreated = true
	}
	taskActivation, cleanupTaskActivation := newTaskLifecycleActivation(o.taskLifecycle, o.events, req, attachedTask, rootAssignee)
	defer cleanupTaskActivation()
	activateConfiguredTask := func(route routing.Route, actor, content string) (modulecore.TaskID, error) {
		if o.taskLifecycle == nil {
			return rootTaskID, nil
		}
		activatedTaskID, activateErr := taskActivation.Activate(ctx, route, actor, content)
		rejectAttachedMismatch(activateErr)
		if activatedTaskID != "" {
			executionTaskID = activatedTaskID
			taskID = activatedTaskID
			ctx = withOrchestrationLLMTask(ctx, activatedTaskID)
		}
		return activatedTaskID, activateErr
	}
	log.Printf("[MessageOrch] ProcessMessage START: taskID=%s traceID=%s messageID=%s sessionID=%s channel=%s chatID=%s message=%q",
		taskID.String(), req.TraceID, req.MessageID, req.SessionID, req.Channel, req.ChatID, req.UserMessage)
	turntiming.Mark(ctx, turntiming.PointInitialEventsStarted)
	if err := o.events.EmitMessageReceived(req, taskID.String()); err != nil {
		turntiming.Mark(ctx, turntiming.PointInitialEventsEnded)
		return ProcessMessageResponse{}, err
	}
	turntiming.Mark(ctx, turntiming.PointMessageReceivedEventPublished)

	endChatBusy := o.idleBusyGuards.BeginChat()
	defer endChatBusy()

	emitLatencyMetric(o.events.Emit, "network", "server_received", latencyStartedAt, "", taskID.String(), req.SessionID, req.Channel, req.ChatID, "")
	if err := o.events.PublicationError(traceID); err != nil {
		turntiming.Mark(ctx, turntiming.PointInitialEventsEnded)
		return ProcessMessageResponse{}, err
	}
	turntiming.Mark(ctx, turntiming.PointServerReceivedMetricPublished)
	sessionLogErr := writeUserSessionTurn(ctx, o.sessionTurnLogger, req)
	turntiming.Mark(ctx, turntiming.PointSessionTurnLogAttemptEnded)
	turntiming.Mark(ctx, turntiming.PointInitialEventsEnded)
	if sessionLogErr != nil {
		return ProcessMessageResponse{}, sessionLogErr
	}
	turntiming.Mark(ctx, turntiming.PointPersonaObservationStarted)
	if err := o.recordPersonaRuntimeObservation(ctx, req); err != nil {
		turntiming.Mark(ctx, turntiming.PointPersonaObservationEnded)
		return ProcessMessageResponse{}, err
	}
	turntiming.Mark(ctx, turntiming.PointPersonaObservationEnded)
	turntiming.Mark(ctx, turntiming.PointPreRouteChecksStarted)
	preRouteChecksEnded := false
	endPreRouteChecks := func() {
		if preRouteChecksEnded {
			return
		}
		turntiming.Mark(ctx, turntiming.PointPreRouteChecksEnded)
		preRouteChecksEnded = true
	}
	if agent.IsChatCommandCandidate(req.UserMessage) {
		ctx, err = synchronousPersistenceBeforeEffect(ctx, "pre-routing Mio command")
		if err != nil {
			endPreRouteChecks()
			return ProcessMessageResponse{}, err
		}
	}
	commandResp, commandHandled, commandErr := o.preRoutingCommands.Handle(ctx, req)
	if commandHandled {
		endPreRouteChecks()
		if _, err := activateConfiguredTask(routing.RouteCHAT, taskLifecycleMio, "confidence 100% evidence=pre-routing command"); err != nil {
			return ProcessMessageResponse{}, err
		}
		if commandErr != nil {
			return ProcessMessageResponse{}, commandErr
		}
		o.preRoutingCommands.EmitResponse(req, commandResp.Response, taskID)
		if err := o.events.PublicationError(traceID); err != nil {
			return ProcessMessageResponse{}, err
		}
		resp = ensureProcessResponseIdentity(commandResp, taskID.String(), req.TurnID, req.TraceID, req.RootTaskID, req.AgentMessageID, o.events.TakeResponseMessageID)
		if err := writeAssistantSessionTurn(ctx, o.sessionTurnLogger, req, resp); err != nil {
			return ProcessMessageResponse{}, err
		}
		return resp, nil
	}
	if commandErr != nil {
		endPreRouteChecks()
		return ProcessMessageResponse{}, commandErr
	}
	if _, _, slashCommandCandidate := parseSlashCommandInvocation(req.UserMessage); slashCommandCandidate {
		ctx, err = synchronousPersistenceBeforeEffect(ctx, "slash command expansion")
		if err != nil {
			endPreRouteChecks()
			return ProcessMessageResponse{}, err
		}
	}
	if expandedReq, handled, err := o.expandRegisteredSlashCommand(ctx, req); err != nil {
		endPreRouteChecks()
		return ProcessMessageResponse{}, err
	} else if handled {
		req = expandedReq
	}
	if o.visionRequests != nil {
		processed, err := o.visionRequests.Process(ctx, req, o.events.Emit)
		if err != nil {
			endPreRouteChecks()
			return ProcessMessageResponse{}, err
		}
		req = processed
		if err := o.events.PublicationError(traceID); err != nil {
			endPreRouteChecks()
			return ProcessMessageResponse{}, err
		}
	}

	input, taskID, ttsSessionID, err := o.turnInputs.BuildWithTaskID(req, taskID)
	if err != nil {
		endPreRouteChecks()
		return ProcessMessageResponse{}, err
	}
	turntiming.Mark(ctx, turntiming.PointTurnInputBuilt)
	dailyBriefRequested := isDailyNewsBriefRequest(req.UserMessage)
	if dailyBriefRequested {
		ctx, err = synchronousPersistenceBeforeEffect(ctx, "daily news brief")
		if err != nil {
			endPreRouteChecks()
			return ProcessMessageResponse{}, err
		}
		endPreRouteChecks()
		turntiming.Mark(ctx, turntiming.PointRouteDecided)
		if _, err := activateConfiguredTask(routing.RouteCHAT, taskLifecycleMio, "confidence 100% evidence=daily news brief intent"); err != nil {
			return ProcessMessageResponse{}, err
		}
	}
	activateDailyShiro := func(activateCtx context.Context) (modulecore.TaskID, error) {
		if o.taskLifecycle == nil {
			return rootTaskID, nil
		}
		activatedTaskID, activateErr := taskActivation.Activate(activateCtx, routing.RouteCHAT, taskLifecycleShiro, "confidence 100% evidence=daily news brief intent")
		rejectAttachedMismatch(activateErr)
		if activatedTaskID != "" {
			executionTaskID = activatedTaskID
			taskID = activatedTaskID
			ctx = withOrchestrationLLMTask(activateCtx, activatedTaskID)
		}
		return activatedTaskID, activateErr
	}
	if dailyBriefRequested {
		turntiming.Mark(ctx, turntiming.PointDispatchStarted)
	}
	if resp, handled, err := o.handleDailyNewsBrief(ctx, req, sess, input, rootTaskID, ttsSessionID, activateDailyShiro); err != nil {
		endPreRouteChecks()
		return ProcessMessageResponse{}, err
	} else if handled {
		endPreRouteChecks()
		if err := o.events.PublicationError(traceID); err != nil {
			return ProcessMessageResponse{}, err
		}
		resp = ensureProcessResponseIdentity(resp, taskID.String(), req.TurnID, req.TraceID, req.RootTaskID, req.AgentMessageID, o.events.TakeResponseMessageID)
		if err := writeAssistantSessionTurn(ctx, o.sessionTurnLogger, req, resp); err != nil {
			return ProcessMessageResponse{}, err
		}
		return resp, nil
	}
	explicitDCI := shouldHandleExplicitDCI(o.dciSearcher, req.UserMessage)
	if explicitDCI {
		ctx, err = synchronousPersistenceBeforeEffect(ctx, "explicit DCI search")
		if err != nil {
			endPreRouteChecks()
			return ProcessMessageResponse{}, err
		}
		endPreRouteChecks()
		turntiming.Mark(ctx, turntiming.PointRouteDecided)
		if _, err := activateConfiguredTask(routing.RouteRESEARCH, taskLifecycleShiro, "confidence 100% evidence=explicit DCI trigger"); err != nil {
			return ProcessMessageResponse{}, err
		}
	}
	if explicitDCI {
		turntiming.Mark(ctx, turntiming.PointDispatchStarted)
	}
	if resp, handled, err := o.handleExplicitDCI(ctx, req, sess, input.WithRoute(routing.RouteRESEARCH), taskID, explicitDCI); err != nil {
		endPreRouteChecks()
		return ProcessMessageResponse{}, err
	} else if handled {
		endPreRouteChecks()
		if err := o.events.PublicationError(traceID); err != nil {
			return ProcessMessageResponse{}, err
		}
		resp = ensureProcessResponseIdentity(resp, taskID.String(), req.TurnID, req.TraceID, req.RootTaskID, req.AgentMessageID, o.events.TakeResponseMessageID)
		if err := writeAssistantSessionTurn(ctx, o.sessionTurnLogger, req, resp); err != nil {
			return ProcessMessageResponse{}, err
		}
		return resp, nil
	}
	durableStoreCandidate := o.durableStoreWorkflow != nil && !strings.HasPrefix(strings.TrimSpace(req.UserMessage), "/")
	if matcher, ok := o.durableStoreWorkflow.(durableStoreIntentMatcher); ok {
		durableStoreCandidate = durableStoreCandidate && matcher.CanHandle(durableStoreInput(req))
	}
	if durableStoreCandidate {
		ctx, err = synchronousPersistenceBeforeEffect(ctx, "durable store workflow")
		if err != nil {
			endPreRouteChecks()
			return ProcessMessageResponse{}, err
		}
	}
	var durableResult domaindurablestore.WorkflowResult
	var durableHandled bool
	var durableErr error
	if durableStoreCandidate {
		durableResult, durableHandled, durableErr = evaluateDurableStore(ctx, o.durableStoreWorkflow, req)
	}
	if durableHandled {
		endPreRouteChecks()
		turntiming.Mark(ctx, turntiming.PointRouteDecided)
		if _, err := activateConfiguredTask(routing.RouteCHAT, taskLifecycleMio, "confidence 100% evidence=durable store workflow"); err != nil {
			return ProcessMessageResponse{}, err
		}
		if durableErr != nil {
			return ProcessMessageResponse{}, durableErr
		}
		turntiming.Mark(ctx, turntiming.PointDispatchStarted)
		resp, err := o.completeDurableStore(ctx, req, sess, input, taskID, durableResult)
		if err != nil {
			return ProcessMessageResponse{}, err
		}
		if err := o.events.PublicationError(traceID); err != nil {
			return ProcessMessageResponse{}, err
		}
		resp = ensureProcessResponseIdentity(resp, taskID.String(), req.TurnID, req.TraceID, req.RootTaskID, req.AgentMessageID, o.events.TakeResponseMessageID)
		if err := writeAssistantSessionTurn(ctx, o.sessionTurnLogger, req, resp); err != nil {
			return ProcessMessageResponse{}, err
		}
		return resp, nil
	}
	if durableErr != nil {
		endPreRouteChecks()
		return ProcessMessageResponse{}, durableErr
	}
	endPreRouteChecks()

	combinedChatAllowed := o.routeDispatcher != nil && !o.routeDispatcher.speechPlanEnabled
	if combinedChatAllowed && req.mioAnswerDeltaSink != nil && req.mioAnswerDeltaSink.admit != nil &&
		req.Channel == "viewer" && req.To == "mio" && req.OperationSource == "RenCrow_CMD" {
		ctx = withMioFinalResponsePublisher(ctx, func(finalCtx context.Context, response string, finalTaskID modulecore.TaskID, finalRunID modulecore.RunID, guard domainconversation.PreparedChatTurnGuard) (domainconversation.PreparedChatTurnPublicationGuard, error) {
			// Finish every accepted transient delta before the canonical final
			// enters the relay's ordered live queue.
			if req.mioAnswerDeltaSink.drain != nil {
				if drainErr := req.mioAnswerDeltaSink.drain(); drainErr != nil {
					return nil, fmt.Errorf("drain private Mio answer deltas before final response: %w", drainErr)
				}
			}
			return o.publishPrivateMioFinalResponse(finalCtx, req, response, finalTaskID, finalRunID, guard)
		})
	}
	var earlyStream *mioEarlyStreamLifecycle
	var actor string
	var leadRunID modulecore.RunID
	var runStartedAt LeadAgentRunRecord
	leadRunInput := leadAgentRunInputFromRequest(req)
	if combinedChatAllowed && req.mioAnswerDeltaSink != nil && o.taskLifecycle != nil {
		earlyStream = &mioEarlyStreamLifecycle{}
		var cancelMioStream context.CancelFunc
		ctx, cancelMioStream = context.WithCancel(ctx)
		earlyStream.setProviderCancellation(cancelMioStream, nil)
		defer cancelMioStream()
		ctx = withMioEarlyStreamLifecycle(ctx, earlyStream)
		ctx = agent.ContextWithMioTurnStreamEventHandler(ctx, func(event domainllm.LogicalOutcomeEvent, guard agent.MioTurnStreamGuardUse) (func(bool), error) {
			switch event.Kind {
			case domainllm.LogicalOutcomeAnswerStart:
				state := earlyStream.snapshot()
				if state.policyApplied {
					if state.prepared || state.deltasSuppressed {
						return nil, nil
					}
					return nil, errors.New("Mio CHAT stream lifecycle was not prepared before answer_start")
				}
				if event.Route != routing.RouteCHAT.String() {
					err := fmt.Errorf("Mio answer_start route %q is not CHAT", event.Route)
					earlyStream.fail(err)
					return nil, err
				}
				reason := "LLM generated MioTurnOutcome v2: answer on CHAT"
				provisional := routing.NewDecisionWithEvidence(routing.RouteCHAT, 0.7, reason, routing.DecisionEvidence{
					Source: routing.EvidenceSourceClassifier, Matched: true, Route: routing.RouteCHAT,
					Confidence: 0.7, Reason: reason,
				})
				policyDecision, policyCtx, policyErr := o.routeDecisions.applyHeavyWorkerPolicy(ctx, provisional, req, taskID)
				if policyErr != nil {
					earlyStream.fail(policyErr)
					return nil, policyErr
				}
				earlyStream.cachePolicy(policyDecision, policyCtx)
				ctx = policyCtx
				if policyDecision.Route != routing.RouteCHAT {
					return nil, nil
				}

				turntiming.Mark(ctx, turntiming.PointTaskActivationStarted)
				actor, err = actualCoreActorForRequest(routing.RouteCHAT, req)
				if err != nil {
					earlyStream.fail(err)
					return nil, err
				}
				if directViewerExecutionActor(req, routing.RouteCHAT, actor) {
					ctx = withOrchestrationLLMInitiator(ctx, actor)
				}
				preparedTaskID, activateErr := taskActivation.Activate(ctx, routing.RouteCHAT, actor, fmt.Sprintf("confidence %.0f%% evidence=%s", policyDecision.Confidence*100, routeDecisionEvidenceSummary(policyDecision.Evidence)))
				turntiming.Mark(ctx, turntiming.PointTaskActivationEnded)
				rejectAttachedMismatch(activateErr)
				if preparedTaskID != "" {
					executionTaskID = preparedTaskID
					taskID = preparedTaskID
					ctx = withOrchestrationLLMTask(ctx, preparedTaskID)
				}
				if activateErr != nil {
					earlyStream.fail(activateErr)
					return nil, activateErr
				}
				turntiming.Mark(ctx, turntiming.PointLeadRunResolutionStarted)
				leadRunID, err = resolveLeadAgentRun(ctx, o.taskLifecycle, taskID, o.superAgentRuns, o.superAgentRunController)
				turntiming.Mark(ctx, turntiming.PointLeadRunResolutionEnded)
				if err != nil {
					earlyStream.fail(err)
					return nil, err
				}
				boundCtx, bindErr := domainexecution.WithIdentity(ctx, taskID, leadRunID, input.TraceID())
				if bindErr != nil {
					earlyStream.fail(bindErr)
					return nil, fmt.Errorf("bind early Mio CHAT execution identity: %w", bindErr)
				}
				ctx = boundCtx
				turntiming.Mark(ctx, turntiming.PointLeadRunPersistenceStarted)
				runStartedAt, err = RecordLeadAgentRunStarted(ctx, o.superAgentRuns, leadRunInput, taskID, leadRunID, actor, routing.RouteCHAT)
				turntiming.Mark(ctx, turntiming.PointLeadRunPersistenceEnded)
				if err != nil {
					earlyStream.fail(err)
					return nil, err
				}
				var unregisterRun func()
				if o.superAgentRunController != nil {
					ctx, unregisterRun = o.superAgentRunController.RegisterRun(ctx, string(leadRunID))
					stopProviderCancel := context.AfterFunc(ctx, cancelMioStream)
					earlyStream.setProviderCancellation(cancelMioStream, stopProviderCancel)
				}
				if leadRunID != "" {
					ctx = appsubagent.WithSuperAgentRuntime(ctx, taskID, leadRunID, actor, runStartedAt.TraceID, runStartedAt.StartedEventID, []string{"session:" + req.SessionID, "route:" + string(routing.RouteCHAT)}, nil, "return summary-only subagent result to Lead Agent")
					turntiming.Mark(ctx, turntiming.PointLeadRunContextReady)
				}
				streamTTSStarted := false
				if o.ttsBridge != nil && ttsSessionID != "" {
					turntiming.Mark(ctx, turntiming.PointTTSSessionSetupStarted)
					streamTTSStarted = o.ttsLifecycle.StartSessionForRoute(ctx, req, taskID, policyDecision, ttsSessionID)
					turntiming.Mark(ctx, turntiming.PointTTSSessionSetupEnded)
					if !streamTTSStarted {
						ttsSessionID = ""
					}
				}
				earlyStream.markPrepared(actor, taskID, leadRunID, runStartedAt, ctx, ttsSessionID, streamTTSStarted, true, unregisterRun)
				return nil, nil
			case domainllm.LogicalOutcomeAnswerDelta:
				state := earlyStream.snapshot()
				if state.deltasSuppressed {
					return nil, nil
				}
				if !state.prepared {
					err := errors.New("Mio answer_delta arrived before its Task and Lead Run were prepared")
					earlyStream.fail(err)
					return nil, err
				}
				if event.Text == "" {
					return nil, nil
				}
				turntiming.Mark(ctx, turntiming.PointFirstTextCallback)
				delta := mioAnswerDelta{
					TurnID: req.TurnID, TraceID: req.TraceID, TaskID: state.taskID.String(), RunID: string(state.runID),
					SessionID: req.SessionID, MessageID: req.AgentMessageID, Text: event.Text,
				}
				if req.mioAnswerDeltaSink.admit != nil {
					return o.admitPrivateMioAnswerDelta(ctx, req, delta, guard)
				}
				return req.mioAnswerDeltaSink.offer(delta)
			default:
				return nil, nil
			}
		})
		defer func() {
			state := earlyStream.snapshot()
			cleanupCtx := ctx
			if state.policyContext != nil {
				cleanupCtx = state.policyContext
			}
			cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(cleanupCtx), 5*time.Second)
			defer cleanupCancel()
			if !state.finishRunAttempted && !state.runRecord.StartedAt.IsZero() {
				earlyStream.markRunFinishAttempted()
				_ = RecordLeadAgentRunFinished(cleanupCtx, o.superAgentRuns, leadRunInput, state.taskID, state.runID, state.actor, routing.RouteCHAT, state.runRecord, "failed", "Mio logical answer stream ended before Lead Agent completion")
			}
			if state.ownsTTS && state.ttsStarted && !state.finishTTSAttempted {
				earlyStream.markTTSFinishAttempted()
				o.ttsLifecycle.EndSession(cleanupCtx, state.ttsSessionID)
			}
			if state.unregisterRun != nil {
				state.unregisterRun()
			}
			if state.stopProviderCancel != nil {
				state.stopProviderCancel()
			}
			if state.cancelProvider != nil {
				state.cancelProvider()
			}
		}()
	}
	turntiming.Mark(ctx, turntiming.PointRouteDecisionStarted)
	routeResult, err := o.routeDecisions.DecideWithChatDraft(ctx, input, req, taskID, combinedChatAllowed)
	if err != nil {
		return ProcessMessageResponse{}, err
	}
	if routeResult.Context != nil {
		ctx = routeResult.Context
	}
	defer func() {
		// Keep the prepared-turn guard alive until every queued transient delta
		// has either passed its final owner/generation check or been discarded.
		if req.mioAnswerDeltaSink != nil && req.mioAnswerDeltaSink.drain != nil {
			if drainErr := req.mioAnswerDeltaSink.drain(); err == nil && drainErr != nil {
				err = fmt.Errorf("drain private Mio answer deltas: %w", drainErr)
			}
		}
		o.routeDecisions.releaseChatDraft(ctx, routeResult.ChatDraft)
	}()
	decision := routeResult.Decision
	if decision.Route != routing.RouteCHAT || routeResult.ChatDraft == nil {
		effect := "non-CHAT route execution"
		if decision.Route == routing.RouteCHAT {
			effect = "CHAT route execution without a combined draft"
		}
		ctx, err = synchronousPersistenceBeforeEffect(ctx, effect)
		if err != nil {
			return ProcessMessageResponse{}, err
		}
	}
	turntiming.Mark(ctx, turntiming.PointRouteDecided)
	earlyState := earlyStream.snapshot()
	earlyPrepared := earlyState.prepared
	if earlyPrepared {
		actor = earlyState.actor
		taskID = earlyState.taskID
		leadRunID = earlyState.runID
		runStartedAt = earlyState.runRecord
		if earlyState.policyContext != nil {
			ctx = earlyState.policyContext
		}
	} else {
		actor, err = actualCoreActorForRequest(decision.Route, req)
		if err != nil {
			return ProcessMessageResponse{}, err
		}
		if directViewerExecutionActor(req, decision.Route, actor) {
			ctx = withOrchestrationLLMInitiator(ctx, actor)
		}
		turntiming.Mark(ctx, turntiming.PointTaskActivationStarted)
		preparedTaskID, activateErr := taskActivation.Activate(ctx, decision.Route, actor, fmt.Sprintf("confidence %.0f%% evidence=%s", decision.Confidence*100, routeDecisionEvidenceSummary(decision.Evidence)))
		turntiming.Mark(ctx, turntiming.PointTaskActivationEnded)
		rejectAttachedMismatch(activateErr)
		if preparedTaskID != "" {
			executionTaskID = preparedTaskID
			taskID = preparedTaskID
			ctx = withOrchestrationLLMTask(ctx, preparedTaskID)
		}
		if activateErr != nil {
			return ProcessMessageResponse{}, activateErr
		}
		turntiming.Mark(ctx, turntiming.PointLeadRunResolutionStarted)
		leadRunID, err = resolveLeadAgentRun(ctx, o.taskLifecycle, taskID, o.superAgentRuns, o.superAgentRunController)
		turntiming.Mark(ctx, turntiming.PointLeadRunResolutionEnded)
		if err != nil {
			return ProcessMessageResponse{}, err
		}
	}
	emitLatencyMetric(o.events.Emit, "llm", "route_decision", latencyStartedAt, string(decision.Route), taskID.String(), req.SessionID, req.Channel, req.ChatID, decision.Reason)
	if err := o.events.PublicationError(traceID); err != nil {
		return ProcessMessageResponse{}, err
	}
	turntiming.Mark(ctx, turntiming.PointRouteMetricPublished)

	input = input.WithRoute(decision.Route)
	turntiming.Mark(ctx, turntiming.PointSkillBootstrapStarted)
	err = o.recordRouteSkillBootstrap(ctx, req, decision.Route)
	turntiming.Mark(ctx, turntiming.PointSkillBootstrapEnded)
	if err != nil {
		return ProcessMessageResponse{}, err
	}
	if !earlyPrepared && o.ttsBridge != nil && ttsSessionID != "" {
		boundCtx, bindErr := domainexecution.WithIdentity(ctx, taskID, leadRunID, input.TraceID())
		if bindErr != nil {
			return ProcessMessageResponse{}, fmt.Errorf("bind TTS execution identity: %w", bindErr)
		}
		ctx = boundCtx
	}
	if ttsSessionID != "" {
		turntiming.Mark(ctx, turntiming.PointTTSSessionSetupStarted)
	}
	ttsSessionStarted := false
	if earlyPrepared {
		ttsSessionID = earlyState.ttsSessionID
		ttsSessionStarted = earlyState.ttsStarted
	} else {
		ttsSessionStarted = o.ttsLifecycle.StartSessionForRoute(ctx, req, taskID, decision, ttsSessionID)
	}
	if ttsSessionID != "" {
		turntiming.Mark(ctx, turntiming.PointTTSSessionSetupEnded)
	}
	ttsSessionEnded := false
	if !ttsSessionStarted {
		// A TTS session that did not start (unresolved speaker or start failure)
		// receives nothing: the bridge would otherwise open one with its default voice.
		ttsSessionID = ""
	} else {
		defer func() {
			if ttsSessionEnded {
				return
			}
			endCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			o.ttsLifecycle.EndSession(endCtx, ttsSessionID)
			if earlyStream != nil && earlyPrepared {
				earlyStream.markTTSFinishAttempted()
			}
		}()
	}

	endWorkerBusy := o.idleBusyGuards.BeginWorker(decision.Route)
	defer endWorkerBusy()

	if !earlyPrepared {
		turntiming.Mark(ctx, turntiming.PointLeadRunPersistenceStarted)
		runStartedAt, err = RecordLeadAgentRunStarted(ctx, o.superAgentRuns, leadRunInput, taskID, leadRunID, actor, decision.Route)
		turntiming.Mark(ctx, turntiming.PointLeadRunPersistenceEnded)
		if err != nil {
			return ProcessMessageResponse{}, err
		}
		if o.superAgentRunController != nil {
			var unregister func()
			ctx, unregister = o.superAgentRunController.RegisterRun(ctx, string(leadRunID))
			defer unregister()
			if earlyStream != nil && decision.Route == routing.RouteCHAT {
				stopProviderCancel := context.AfterFunc(ctx, earlyState.cancelProvider)
				earlyStream.setProviderCancellation(earlyState.cancelProvider, stopProviderCancel)
			}
		}
		if leadRunID != "" {
			ctx = appsubagent.WithSuperAgentRuntime(ctx, taskID, leadRunID, actor, runStartedAt.TraceID, runStartedAt.StartedEventID, []string{"session:" + req.SessionID, "route:" + string(decision.Route)}, nil, "return summary-only subagent result to Lead Agent")
			turntiming.Mark(ctx, turntiming.PointLeadRunContextReady)
		}
		if earlyStream != nil && decision.Route == routing.RouteCHAT && routeResult.ChatDraft != nil {
			earlyStream.markPrepared(actor, taskID, leadRunID, runStartedAt, ctx, ttsSessionID, ttsSessionStarted, false, nil)
		}
	}
	finishLeadRun := func(finishCtx context.Context, status, reason string) error {
		if earlyStream != nil {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(finishCtx), 5*time.Second)
			defer cancel()
			err := RecordLeadAgentRunFinished(cleanupCtx, o.superAgentRuns, leadRunInput, taskID, leadRunID, actor, decision.Route, runStartedAt, status, reason)
			if err == nil {
				earlyStream.markRunFinishAttempted()
			}
			return err
		}
		return RecordLeadAgentRunFinished(finishCtx, o.superAgentRuns, leadRunInput, taskID, leadRunID, actor, decision.Route, runStartedAt, status, reason)
	}

	// 4. ルートに応じて実行
	emitLatencyMetric(o.events.Emit, "llm", "dispatch_start", latencyStartedAt, string(decision.Route), taskID.String(), req.SessionID, req.Channel, req.ChatID, "")
	if err := o.events.PublicationError(traceID); err != nil {
		return ProcessMessageResponse{}, err
	}
	turntiming.Mark(ctx, turntiming.PointDispatchMetricPublished)
	turntiming.Mark(ctx, turntiming.PointDispatchStarted)
	response, err := o.routeDispatcher.ExecuteTurnInputWithChatDraft(ctx, input, decision.Route, taskID, leadRunID, ttsSessionID, routeResult.ChatDraft)
	if publicationErr := o.events.PublicationError(traceID); publicationErr != nil {
		if err != nil {
			return ProcessMessageResponse{}, errors.Join(err, publicationErr)
		}
		return ProcessMessageResponse{}, publicationErr
	}
	if err != nil {
		if o.superAgentRunController != nil && o.superAgentRunController.IsPauseRequested(string(leadRunID)) {
			_ = finishLeadRun(context.Background(), "paused", "pause requested; task execution canceled")
		} else {
			_ = finishLeadRun(ctx, "failed", err.Error())
		}
		return ProcessMessageResponse{}, fmt.Errorf("task execution failed: %w", err)
	}
	emitLatencyMetric(o.events.Emit, "llm", "response_complete", latencyStartedAt, string(decision.Route), taskID.String(), req.SessionID, req.Channel, req.ChatID, fmt.Sprintf("response_len=%d", len(response)))
	if err := o.events.PublicationError(traceID); err != nil {
		return ProcessMessageResponse{}, err
	}
	o.ttsLifecycle.EndSession(ctx, ttsSessionID)
	ttsSessionEnded = true
	if earlyStream != nil && earlyPrepared {
		earlyStream.markTTSFinishAttempted()
	}
	var verificationReport *domainverification.VerificationReport
	if o.verifier != nil {
		verification, err := o.verifier.VerifyResponse(ctx, appverification.Request{
			DraftResponse: response,
			UserMessage:   req.UserMessage,
			Route:         string(decision.Route),
			SessionID:     req.SessionID,
			Channel:       req.Channel,
			ChatID:        req.ChatID,
			TaskID:        taskID,
		})
		if err != nil {
			_ = finishLeadRun(ctx, "failed", err.Error())
			return ProcessMessageResponse{}, fmt.Errorf("response verification failed: %w", err)
		}
		response = verification.Response
		verificationReport = &verification.Report
		o.events.Emit("verification.report", "verification", "viewer", string(verification.Report.Status), string(decision.Route), taskID.String(), req.SessionID, req.Channel, req.ChatID)
		if err := o.events.PublicationError(traceID); err != nil {
			return ProcessMessageResponse{}, err
		}
	}

	if applied, err := o.applyPersonaCanonicalResponse(ctx, req, response); err != nil {
		_ = finishLeadRun(ctx, "failed", err.Error())
		return ProcessMessageResponse{}, err
	} else if applied != "" {
		response = applied
	}

	if err := o.sessions.SaveCompletedTurnInput(ctx, sess, input); err != nil {
		_ = finishLeadRun(ctx, "failed", err.Error())
		return ProcessMessageResponse{}, err
	}
	if err := finishLeadRun(ctx, "completed", "Lead Agent completed"); err != nil {
		return ProcessMessageResponse{}, err
	}

	resp = ensureProcessResponseIdentity(
		o.responses.BuildWithVerification(response, decision, taskID, verificationReport),
		taskID.String(),
		req.TurnID,
		req.TraceID,
		req.RootTaskID,
		req.AgentMessageID,
		o.events.TakeResponseMessageID,
	)
	log.Printf("[MessageOrch] ProcessMessage COMPLETE: taskID=%s traceID=%s messageID=%s route=%s response_len=%d",
		taskID.String(), resp.TraceID, resp.MessageID, decision.Route, len(response))
	if err := writeAssistantSessionTurn(ctx, o.sessionTurnLogger, req, resp); err != nil {
		return ProcessMessageResponse{}, err
	}
	return resp, nil
}
