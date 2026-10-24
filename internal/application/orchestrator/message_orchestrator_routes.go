package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	domainagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/speech"
	"github.com/Nyukimin/RenCrow_CORE/internal/turntiming"
	modulechat "github.com/Nyukimin/RenCrow_CORE/modules/chat"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

// messageStreamHook binds the internal LLM token stream of one turn to TTS and
// VTuber forwarding. speaker is the Agent whose model generates the tokens and
// is also the from of the final public agent.response (docs/06_Public_API仕様.md).
type messageStreamHook func(ctx context.Context, route routing.Route, speaker, taskID, sessionID, channel, chatID, ttsSessionID string) (context.Context, *streamBundle)

// messageTTSPusher voices one finished utterance. speaker is the Agent that
// produced the text (the turn's responding Agent), never inferred from route.
type messageTTSPusher func(ctx context.Context, sessionID string, route routing.Route, speaker, eventType, text string)

// messageSpeechPlanPusher hands one normalized CHAT plan to its TTS owner.
// Conversation session, TTS session, and canonical turn IDs remain explicit.
type messageSpeechPlanPusher func(ctx context.Context, ttsSessionID, conversationSessionID, turnID, speakerID string, plan speech.SpeechPlan) error

// MioRetrievalPort delegates each semantic read to its existing source owner.
// Implementations must use the authenticated turn scope from ctx/input and
// return only content that passed the source owner's policy and freshness
// checks. Web retrieval remains behind the existing policy-checked web owner.
type MioRetrievalPort interface {
	Retrieve(ctx context.Context, input domainconversation.TurnInput, request domainagent.MioRetrievalRequest) (domainagent.MioRetrievalResult, error)
}

type speechPlanRouteObservation struct {
	Event            string `json:"event"`
	TurnID           string `json:"turn_id"`
	SpeakerID        string `json:"speaker_id"`
	SpeechPlanValid  bool   `json:"speech_plan_valid"`
	UtteranceCount   int    `json:"utterance_count"`
	CueCount         int    `json:"cue_count"`
	LLMGenerationMS  *int64 `json:"llm_generation_ms"`
	PlanValidationMS *int64 `json:"plan_validation_ms"`
	FallbackReason   string `json:"fallback_reason"`
}

const (
	speechPlanHandoffUnavailable  = "speech_plan_tts_handoff_unavailable"
	speechPlanHandoffFailed       = "speech_plan_tts_handoff_failed"
	speechPlanDisplayUnavailable  = "speech_plan_display_unavailable"
	speechPlanCallbackUnavailable = "speech_plan_callback_unavailable"
)

var (
	ErrMioRetrievalUnavailable              = errors.New("Mio retrieval source is unavailable")
	ErrMioRetrievalDenied                   = errors.New("Mio retrieval request was denied")
	ErrMioRetrievalBudgetExceeded           = domainagent.ErrMioRetrievalBudgetExceeded
	ErrMioAnswerDeltaSinkFull               = errors.New("Mio answer delta sink is full")
	ErrMioAnswerDeltaTooLarge               = errors.New("Mio answer delta exceeds the in-memory sink limit")
	ErrMioFinalResponsePublisherUnavailable = errors.New("guarded Mio final response publisher is unavailable")
)

type mioFinalResponsePublisher func(context.Context, string, modulecore.TaskID, modulecore.RunID, domainconversation.PreparedChatTurnGuard) (domainconversation.PreparedChatTurnPublicationGuard, error)

type mioFinalResponsePublisherContextKey struct{}

func withMioFinalResponsePublisher(ctx context.Context, publisher mioFinalResponsePublisher) context.Context {
	if ctx == nil || publisher == nil {
		return ctx
	}
	return context.WithValue(ctx, mioFinalResponsePublisherContextKey{}, publisher)
}

func mioFinalResponsePublisherFromContext(ctx context.Context) mioFinalResponsePublisher {
	if ctx == nil {
		return nil
	}
	publisher, _ := ctx.Value(mioFinalResponsePublisherContextKey{}).(mioFinalResponsePublisher)
	return publisher
}

const (
	maxMioRetrievalRounds       = 2
	maxMioRetrievalRequests     = 2
	maxMioRetrievalItems        = 8
	maxMioRetrievalEnvelopeSize = 64 * 1024
	mioRetrievalSequenceLimit   = 10 * time.Second
	mioRetrievalDatabaseLimit   = 2 * time.Second
	mioRetrievalWebLimit        = 8 * time.Second
)

const (
	mioAnswerDeltaQueueCapacity = 64
	mioAnswerDeltaMaxBytes      = 16 * 1024
)

// mioAnswerDelta is an internal, turn-local projection of one already
// validated logical answer_delta. It is not a public event or a durable record.
type mioAnswerDelta struct {
	TurnID    string
	TraceID   string
	TaskID    string
	RunID     string
	SessionID string
	MessageID string
	Text      string
}

// MioAnswerDelta is the bounded, turn-local projection exposed to an adapter
// that opts into authenticated private response streaming. It is transient
// transport data, not a canonical event or durable record.
type MioAnswerDelta struct {
	TurnID    string
	TraceID   string
	TaskID    string
	RunID     string
	SessionID string
	MessageID string
	Text      string
}

// WithMioAnswerDeltaAdmission opts one ProcessMessage request into a narrow
// delta callback. The orchestrator still owns publication qualification and
// invokes the callback only while the exact prepared turn and Session binding
// are guarded. Adapters should make this callback a bounded nonblocking RAM
// admission only.
func WithMioAnswerDeltaAdmission(req ProcessMessageRequest, admit func(MioAnswerDelta, domainconversation.PreparedChatTurnPublicationGuard) (func(bool), error), drain func() error) ProcessMessageRequest {
	if admit == nil {
		return req
	}
	req.mioAnswerDeltaSink = &mioAnswerDeltaSink{admit: admit, drain: drain}
	return req
}

// mioAnswerDeltaSink keeps the opt-in stream seam bounded and nonblocking.
// Production adapters use admit, which is invoked only after the orchestrator
// proves the private Session binding. The package-local channel exists only
// for lifecycle unit tests and is not an external publication surface.
type mioAnswerDeltaSink struct {
	deltas chan mioAnswerDelta
	admit  func(MioAnswerDelta, domainconversation.PreparedChatTurnPublicationGuard) (func(bool), error)
	drain  func() error
}

func newMioAnswerDeltaSink() *mioAnswerDeltaSink {
	return &mioAnswerDeltaSink{deltas: make(chan mioAnswerDelta, mioAnswerDeltaQueueCapacity)}
}

func (s *mioAnswerDeltaSink) offer(delta mioAnswerDelta) (func(bool), error) {
	if s == nil {
		return nil, errors.New("Mio answer delta sink is unavailable")
	}
	if len(delta.Text) > mioAnswerDeltaMaxBytes {
		return nil, ErrMioAnswerDeltaTooLarge
	}
	if s.admit != nil {
		return nil, errors.New("private Mio answer delta admission must use the canonical Session binding owner")
	}
	if s.deltas == nil {
		return nil, errors.New("Mio answer delta sink is unavailable")
	}
	select {
	case s.deltas <- delta:
		return nil, nil
	default:
		return nil, ErrMioAnswerDeltaSinkFull
	}
}

type mioEarlyStreamLifecycleContextKey struct{}

type mioEarlyStreamLifecycle struct {
	mu                 sync.Mutex
	policyApplied      bool
	policyDecision     routing.Decision
	policyContext      context.Context
	setupErr           error
	deltasSuppressed   bool
	prepared           bool
	actor              string
	taskID             modulecore.TaskID
	runID              modulecore.RunID
	runRecord          LeadAgentRunRecord
	ttsSessionID       string
	ttsStarted         bool
	ownsTTS            bool
	finishRunAttempted bool
	finishTTSAttempted bool
	unregisterRun      func()
	cancelProvider     context.CancelFunc
	stopProviderCancel func() bool
}

type mioEarlyStreamLifecycleSnapshot struct {
	policyApplied      bool
	policyDecision     routing.Decision
	policyContext      context.Context
	setupErr           error
	deltasSuppressed   bool
	prepared           bool
	actor              string
	taskID             modulecore.TaskID
	runID              modulecore.RunID
	runRecord          LeadAgentRunRecord
	ttsSessionID       string
	ttsStarted         bool
	ownsTTS            bool
	finishRunAttempted bool
	finishTTSAttempted bool
	unregisterRun      func()
	cancelProvider     context.CancelFunc
	stopProviderCancel func() bool
}

func withMioEarlyStreamLifecycle(ctx context.Context, lifecycle *mioEarlyStreamLifecycle) context.Context {
	if ctx == nil || lifecycle == nil {
		return ctx
	}
	return context.WithValue(ctx, mioEarlyStreamLifecycleContextKey{}, lifecycle)
}

func mioEarlyStreamLifecycleFromContext(ctx context.Context) *mioEarlyStreamLifecycle {
	if ctx == nil {
		return nil
	}
	lifecycle, _ := ctx.Value(mioEarlyStreamLifecycleContextKey{}).(*mioEarlyStreamLifecycle)
	return lifecycle
}

func (l *mioEarlyStreamLifecycle) snapshot() mioEarlyStreamLifecycleSnapshot {
	if l == nil {
		return mioEarlyStreamLifecycleSnapshot{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return mioEarlyStreamLifecycleSnapshot{
		policyApplied:      l.policyApplied,
		policyDecision:     l.policyDecision,
		policyContext:      l.policyContext,
		setupErr:           l.setupErr,
		deltasSuppressed:   l.deltasSuppressed,
		prepared:           l.prepared,
		actor:              l.actor,
		taskID:             l.taskID,
		runID:              l.runID,
		runRecord:          l.runRecord,
		ttsSessionID:       l.ttsSessionID,
		ttsStarted:         l.ttsStarted,
		ownsTTS:            l.ownsTTS,
		finishRunAttempted: l.finishRunAttempted,
		finishTTSAttempted: l.finishTTSAttempted,
		unregisterRun:      l.unregisterRun,
		cancelProvider:     l.cancelProvider,
		stopProviderCancel: l.stopProviderCancel,
	}
}

func (l *mioEarlyStreamLifecycle) cachePolicy(decision routing.Decision, ctx context.Context) {
	if l == nil {
		return
	}
	decision.Evidence = append([]routing.DecisionEvidence(nil), decision.Evidence...)
	l.mu.Lock()
	l.policyApplied = true
	l.policyDecision = decision
	l.policyContext = ctx
	l.deltasSuppressed = decision.Route != routing.RouteCHAT
	l.mu.Unlock()
}

func (l *mioEarlyStreamLifecycle) fail(err error) {
	if l == nil || err == nil {
		return
	}
	l.mu.Lock()
	if l.setupErr == nil {
		l.setupErr = err
	}
	l.mu.Unlock()
}

func (l *mioEarlyStreamLifecycle) markPrepared(actor string, taskID modulecore.TaskID, runID modulecore.RunID, runRecord LeadAgentRunRecord, ctx context.Context, ttsSessionID string, ttsStarted, ownsTTS bool, unregisterRun func()) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.prepared = true
	l.actor = actor
	l.taskID = taskID
	l.runID = runID
	l.runRecord = runRecord
	l.policyContext = ctx
	l.ttsSessionID = ttsSessionID
	l.ttsStarted = ttsStarted
	l.ownsTTS = ownsTTS
	l.unregisterRun = unregisterRun
	l.mu.Unlock()
}

func (l *mioEarlyStreamLifecycle) markRunFinishAttempted() {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.finishRunAttempted = true
	l.mu.Unlock()
}

func (l *mioEarlyStreamLifecycle) markTTSFinishAttempted() {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.finishTTSAttempted = true
	l.mu.Unlock()
}

func (l *mioEarlyStreamLifecycle) setProviderCancellation(cancel context.CancelFunc, stop func() bool) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.cancelProvider = cancel
	l.stopProviderCancel = stop
	l.mu.Unlock()
}

type messageRouteDispatcher struct {
	mio               MioAgent
	shiroChat         MioAgent
	shiro             ShiroAgent
	wild              WildAgent
	heavy             HeavyAgent
	speechPlanEnabled bool
	codeExecutor      CodeExecutor
	emit              messageEventEmitter
	withStreamHooks   messageStreamHook
	pushTTS           messageTTSPusher
	pushSpeechPlan    messageSpeechPlanPusher
	mioRetrieval      MioRetrievalPort
	executeAutonomous autonomousRouteExecutor
	canonicalEvents   CanonicalEventRecorder
	nativeCoding      NativeCodingAdmission
}

func newMessageRouteDispatcher(
	mio MioAgent,
	shiro ShiroAgent,
	codeExecutor CodeExecutor,
	emit messageEventEmitter,
	withStreamHooks messageStreamHook,
	pushTTS messageTTSPusher,
) *messageRouteDispatcher {
	return &messageRouteDispatcher{
		mio:             mio,
		shiro:           shiro,
		codeExecutor:    codeExecutor,
		emit:            emit,
		withStreamHooks: withStreamHooks,
		pushTTS:         pushTTS,
	}
}

func (d *messageRouteDispatcher) SetWildAgent(wild WildAgent) {
	d.wild = wild
}

func (d *messageRouteDispatcher) SetHeavyAgent(heavy HeavyAgent) {
	d.heavy = heavy
}

func (d *messageRouteDispatcher) SetShiroChatAgent(chat MioAgent) {
	d.shiroChat = chat
}

// SetSpeechPlanPusher installs the typed CHAT-to-TTS handoff. The message
// orchestrator lifecycle owns wiring this seam to its active TTS bridge.
func (d *messageRouteDispatcher) SetSpeechPlanPusher(pusher messageSpeechPlanPusher) {
	d.pushSpeechPlan = pusher
}

// SetMioRetrievalPort installs the owner-mediated read seam for Mio's
// same-turn CHAT continuation.
func (d *messageRouteDispatcher) SetMioRetrievalPort(port MioRetrievalPort) {
	d.mioRetrieval = port
}

// SetMioRetrievalPort configures the owner-mediated read seam before the
// orchestrator begins processing turns.
func (o *MessageOrchestrator) SetMioRetrievalPort(port MioRetrievalPort) {
	if o == nil || o.routeDispatcher == nil {
		return
	}
	o.routeDispatcher.SetMioRetrievalPort(port)
}

func (d *messageRouteDispatcher) SetSpeechPlanEnabled(enabled bool) {
	d.speechPlanEnabled = enabled
}

func (d *messageRouteDispatcher) SetAutonomousExecutor(execute autonomousRouteExecutor) {
	d.executeAutonomous = execute
}

func (d *messageRouteDispatcher) SetCanonicalEventRecorder(recorder CanonicalEventRecorder) {
	d.canonicalEvents = recorder
}

func turnInputMetadata(input domainconversation.TurnInput) (sessionID, channel, chatID string) {
	address := input.ChannelAddress()
	return input.SessionID(), address.ChannelType(), address.ExternalConversationID()
}

func (d *messageRouteDispatcher) ExecuteTurnInput(ctx context.Context, input domainconversation.TurnInput, route routing.Route, taskID modulecore.TaskID, runID modulecore.RunID, ttsSessionID string) (string, error) {
	return d.ExecuteTurnInputWithChatDraft(ctx, input, route, taskID, runID, ttsSessionID, nil)
}

func (d *messageRouteDispatcher) ExecuteTurnInputWithChatDraft(ctx context.Context, input domainconversation.TurnInput, route routing.Route, taskID modulecore.TaskID, runID modulecore.RunID, ttsSessionID string, chatDraft *domainagent.MioChatDraft) (string, error) {
	var err error
	ctx, err = bindRouteExecutionContext(ctx, input, route, taskID, runID)
	if err != nil {
		return "", err
	}
	input = input.WithRoute(route)
	if route != routing.RouteCHAT {
		// shiro_native_coding_v1: the admission selects a new OPS turn before it
		// enters the autonomous coordinator, so the selection travels with the
		// input through every attempt. A refusal ends the turn here.
		input, err = AdmitNativeCoding(ctx, d.nativeCoding, input, route)
		if err != nil {
			return "", err
		}
		if shouldTraceShiroDelegation(route) {
			sessionID, channel, chatID := turnInputMetadata(input)
			d.emit("agent.delegate", "mio", "shiro", formatMioToShiroInstruction(input, route, taskID.String()), route.String(), taskID.String(), sessionID, channel, chatID)
			d.emit("agent.acknowledge", "shiro", "mio", formatShiroReadbackToMio(input, route, taskID.String()), route.String(), taskID.String(), sessionID, channel, chatID)
		}
		return d.executeAutonomous(ctx, input, route, taskID, ttsSessionID)
	}

	return d.executeChatRouteWithDraft(ctx, input, taskID, ttsSessionID, chatDraft)
}

func (d *messageRouteDispatcher) ExecuteDirect(ctx context.Context, input domainconversation.TurnInput, route routing.Route, taskID modulecore.TaskID, runID modulecore.RunID, ttsSessionID string) (string, error) {
	var err error
	ctx, err = bindRouteExecutionContext(ctx, input, route, taskID, runID)
	if err != nil {
		return "", err
	}
	input = input.WithRoute(route)
	switch route {
	case routing.RouteOPS:
		return d.executeOPSRoute(ctx, input, taskID, ttsSessionID)
	case routing.RouteCODE, routing.RouteCODE1, routing.RouteCODE2, routing.RouteCODE3, routing.RouteCODE4:
		return d.executeCodeRoute(ctx, input, route, taskID, ttsSessionID)
	case routing.RouteWILD:
		return d.executeWildRoute(ctx, input, taskID, ttsSessionID)
	case routing.RoutePLAN:
		return d.executePlanRoute(ctx, input, taskID, ttsSessionID)
	case routing.RouteANALYZE:
		return d.executeAnalyzeRoute(ctx, input, taskID, ttsSessionID)
	case routing.RouteRESEARCH:
		return d.executeResearchRoute(ctx, input, taskID, ttsSessionID)
	default:
		return "", fmt.Errorf("unsupported autonomous route: %s", route)
	}
}

// executeDirectForContext is the adapter used by the bounded autonomous
// coordinator. The coordinator retries with the same context, so it must
// recover the exact owner-selected RunID rather than issue or derive one.
func (d *messageRouteDispatcher) executeDirectForContext(ctx context.Context, input domainconversation.TurnInput, route routing.Route, taskID modulecore.TaskID, ttsSessionID string) (string, error) {
	identity, err := domainexecution.IdentityFromContext(ctx)
	if err != nil {
		return "", err
	}
	if identity.TaskID != taskID {
		return "", fmt.Errorf("execution task identity mismatch: context=%s argument=%s", identity.TaskID, taskID)
	}
	return d.ExecuteDirect(ctx, input, route, taskID, identity.RunID, ttsSessionID)
}

func (d *messageRouteDispatcher) executeChatRoute(ctx context.Context, input domainconversation.TurnInput, taskID modulecore.TaskID, ttsSessionID string) (string, error) {
	return d.executeChatRouteWithDraft(ctx, input, taskID, ttsSessionID, nil)
}

func (d *messageRouteDispatcher) executeChatRouteWithDraft(ctx context.Context, input domainconversation.TurnInput, taskID modulecore.TaskID, ttsSessionID string, chatDraft *domainagent.MioChatDraft) (string, error) {
	sessionID, channel, chatID := turnInputMetadata(input)
	taskIDText := taskID.String()
	speaker := chatSpeakerForTurnInput(input)
	finalPublisher := mioFinalResponsePublisherFromContext(ctx)
	guardedMioFinal := finalPublisher != nil && chatDraft != nil && speaker == "mio"
	abortUnpublished := func(stream *streamBundle) {
		if stream == nil {
			return
		}
		if guardedMioFinal {
			stream.Abort()
			return
		}
		stream.Finalize(ctx, "")
	}
	turntiming.Mark(ctx, turntiming.PointAgentStartPublicationStarted)
	d.emit("agent.start", speaker, "user", "考え中...", "CHAT", taskIDText, sessionID, channel, chatID)
	turntiming.Mark(ctx, turntiming.PointAgentStartPublicationEnded)
	streamHookCtx := ctx
	if d.speechPlanEnabled {
		streamHookCtx = withSpeechPlanStreamMode(streamHookCtx)
	}
	streamCtx, ttsStream := d.withStreamHooks(streamHookCtx, routing.RouteCHAT, speaker, taskIDText, sessionID, channel, chatID, ttsSessionID)
	var speechPlanResult *speech.Result
	if d.speechPlanEnabled {
		streamCtx = domainagent.ContextWithSpeechPlanCallback(streamCtx, func(result speech.Result) {
			captured := result
			speechPlanResult = &captured
		})
	}
	var resp string
	var err error
	if chatDraft != nil && speaker == "mio" && !d.speechPlanEnabled {
		chatAgent, ok := d.mio.(interface {
			ChatWithDraft(context.Context, domainconversation.TurnInput, *domainagent.MioChatDraft) (string, error)
		})
		if !ok {
			abortUnpublished(ttsStream)
			return "", fmt.Errorf("Mio cannot adopt the prepared CHAT reply")
		}
		if err := d.completeMioRetrieval(ctx, input, chatDraft); err != nil {
			abortUnpublished(ttsStream)
			return "", err
		}
		resp, err = chatAgent.ChatWithDraft(streamCtx, input, chatDraft)
	} else {
		resp, err = d.generateChatResponse(streamCtx, input, speaker)
	}
	if err != nil {
		if speechPlanResult != nil {
			d.logSpeechPlanObservation(string(input.TurnID()), speaker, *speechPlanResult, "")
		} else if d.speechPlanEnabled {
			abortUnpublished(ttsStream)
			d.logSpeechPlanObservation(string(input.TurnID()), speaker, speech.Result{FallbackReason: speechPlanCallbackUnavailable}, "")
		}
		abortUnpublished(ttsStream)
		return resp, err
	}
	if guardedMioFinal {
		preparedGuard := chatDraft.TransferPreparedChatTurnGuard()
		if preparedGuard == nil {
			abortUnpublished(ttsStream)
			return resp, fmt.Errorf("%w: prepared Mio turn guard was not transferable", ErrMioFinalResponsePublisherUnavailable)
		}
		defer preparedGuard.Release()
		identity, identityErr := domainexecution.IdentityFromContext(ctx)
		if identityErr != nil || identity.TaskID != taskID || identity.TraceID != input.TraceID() {
			abortUnpublished(ttsStream)
			if identityErr != nil {
				return resp, fmt.Errorf("%w: read final execution identity: %v", ErrMioFinalResponsePublisherUnavailable, identityErr)
			}
			return resp, fmt.Errorf("%w: final execution identity does not match the adopted turn", ErrMioFinalResponsePublisherUnavailable)
		}
		qualification, publishErr := finalPublisher(ctx, resp, taskID, identity.RunID, preparedGuard)
		if publishErr != nil {
			if qualification != nil {
				qualification.Release()
			}
			abortUnpublished(ttsStream)
			return resp, publishErr
		}
		if qualification != nil {
			defer qualification.Release()
		}
		var validateTTS func(func() error) error
		if qualification != nil {
			validateTTS = qualification.Use
		}
		ttsStream.FinalizeWithAdmission(ctx, resp, validateTTS)
		if streamErr := ttsStream.Err(); streamErr != nil {
			return resp, fmt.Errorf("Mio final response TTS handoff failed: %w", streamErr)
		}
		return resp, nil
	}
	d.emit("agent.response", speaker, "user", resp, "CHAT", taskIDText, sessionID, channel, chatID)
	if speechPlanResult == nil {
		if d.speechPlanEnabled {
			ttsStream.Finalize(ctx, "")
			d.logSpeechPlanObservation(string(input.TurnID()), speaker, speech.Result{FallbackReason: speechPlanCallbackUnavailable}, "")
			return resp, nil
		}
		// SpeechPlan is disabled, so deterministic shortcuts keep the existing
		// text-only stream behavior.
		ttsStream.Finalize(ctx, resp)
		return resp, nil
	}

	// Close and drain legacy forwarders without sending the JSON plan or its
	// display text through the old TTS/Avatar text path.
	ttsStream.Finalize(ctx, "")
	result := *speechPlanResult
	routeFallbackReason := ""
	if result.Valid {
		if strings.TrimSpace(result.DisplayText) == "" {
			routeFallbackReason = speechPlanDisplayUnavailable
		} else if d.pushSpeechPlan == nil {
			routeFallbackReason = speechPlanHandoffUnavailable
		} else if pushErr := d.pushSpeechPlan(ctx, ttsSessionID, sessionID, string(input.TurnID()), speaker, result.Plan); pushErr != nil {
			routeFallbackReason = speechPlanHandoffFailed
		}
	}
	d.logSpeechPlanObservation(string(input.TurnID()), speaker, result, routeFallbackReason)
	return resp, nil
}

type mioRetrievalBudget struct {
	cache   map[string]domainagent.MioRetrievalResult
	ordered []domainagent.MioRetrievalResult
	items   int
}

type mioRetrievalPending struct {
	key     string
	request domainagent.MioRetrievalRequest
	result  domainagent.MioRetrievalResult
	err     error
}

func (d *messageRouteDispatcher) completeMioRetrieval(ctx context.Context, input domainconversation.TurnInput, draft *domainagent.MioChatDraft) error {
	outcome, ok := draft.Outcome()
	if !ok || outcome.Kind != domainagent.MioTurnOutcomeRetrieve {
		return nil
	}
	if d.mioRetrieval == nil {
		return ErrMioRetrievalUnavailable
	}
	continuation, ok := d.mio.(interface {
		ContinueChatDraft(context.Context, domainconversation.TurnInput, *domainagent.MioChatDraft, []domainagent.MioRetrievalResult) (domainagent.MioTurnOutcome, error)
	})
	if !ok {
		return errors.New("Mio cannot continue a retrieved CHAT draft")
	}
	sequenceCtx, cancel := context.WithTimeout(ctx, mioRetrievalSequenceLimit)
	defer cancel()
	budget := mioRetrievalBudget{cache: make(map[string]domainagent.MioRetrievalResult)}
	for round := 0; outcome.Kind == domainagent.MioTurnOutcomeRetrieve; round++ {
		if round >= maxMioRetrievalRounds {
			return domainagent.ErrMioRetrievalRoundsExceeded
		}
		if err := sequenceCtx.Err(); err != nil {
			return err
		}
		results, err := d.retrieveMioResults(sequenceCtx, input, outcome.Requests, &budget)
		if err != nil {
			return err
		}
		outcome, err = continuation.ContinueChatDraft(sequenceCtx, input, draft, results)
		if err != nil {
			return err
		}
	}
	if outcome.Kind != domainagent.MioTurnOutcomeAnswer {
		return fmt.Errorf("%w: final CHAT continuation returned %q", domainagent.ErrInvalidMioTurnOutcome, outcome.Kind)
	}
	return nil
}

func (d *messageRouteDispatcher) retrieveMioResults(ctx context.Context, input domainconversation.TurnInput, requests []domainagent.MioRetrievalRequest, budget *mioRetrievalBudget) ([]domainagent.MioRetrievalResult, error) {
	if d.mioRetrieval == nil {
		return nil, ErrMioRetrievalUnavailable
	}
	if len(requests) == 0 || len(requests) > maxMioRetrievalRequests {
		return nil, fmt.Errorf("%w: retrieval round has %d requests", domainagent.ErrInvalidMioTurnOutcome, len(requests))
	}
	pendingByKey := make(map[string]*mioRetrievalPending, len(requests))
	newPending := make([]*mioRetrievalPending, 0, len(requests))
	for index, request := range requests {
		key, err := mioRetrievalRequestKey(request)
		if err != nil {
			return nil, fmt.Errorf("%w: encode request %d: %v", domainagent.ErrInvalidMioTurnOutcome, index+1, err)
		}
		if _, ok := budget.cache[key]; ok {
			continue
		}
		if _, ok := pendingByKey[key]; ok {
			continue
		}
		pending := &mioRetrievalPending{key: key, request: request}
		pendingByKey[key] = pending
		newPending = append(newPending, pending)
	}

	fetchCtx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	for _, pending := range newPending {
		pending := pending
		workers.Add(1)
		go func() {
			defer workers.Done()
			limit := mioRetrievalDatabaseLimit
			if pending.request.SourceKind == domainagent.MioRetrievalSourceWebSearch {
				limit = mioRetrievalWebLimit
			}
			requestCtx, requestCancel := context.WithTimeout(fetchCtx, limit)
			defer requestCancel()
			pending.result, pending.err = d.mioRetrieval.Retrieve(requestCtx, input, pending.request)
			if pending.err == nil {
				pending.err = validateMioRetrievalResponse(pending.request, pending.result)
			}
			if pending.err != nil {
				cancel()
			}
		}()
	}
	workers.Wait()
	cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var canceledErr error
	for _, pending := range newPending {
		if pending.err != nil {
			if errors.Is(pending.err, context.Canceled) {
				if canceledErr == nil {
					canceledErr = pending.err
				}
				continue
			}
			return nil, fmt.Errorf("Mio retrieval source %s: %w", pending.request.SourceKind, pending.err)
		}
	}
	if canceledErr != nil {
		return nil, fmt.Errorf("Mio retrieval canceled after a peer failure: %w", canceledErr)
	}
	for _, pending := range newPending {
		result := cloneMioRetrievalResult(pending.result)
		budget.cache[pending.key] = result
		budget.ordered = append(budget.ordered, cloneMioRetrievalResult(result))
		budget.items += len(result.Items)
		if budget.items > maxMioRetrievalItems {
			return nil, ErrMioRetrievalBudgetExceeded
		}
	}
	envelope, err := json.Marshal(budget.ordered)
	if err != nil {
		return nil, fmt.Errorf("encode Mio retrieval budget: %w", err)
	}
	if len(envelope) > maxMioRetrievalEnvelopeSize {
		return nil, ErrMioRetrievalBudgetExceeded
	}
	// A continuation only receives newly fetched evidence. Results already in
	// the retained draft prompt are not duplicated when Mio repeats a request.
	newResults := make([]domainagent.MioRetrievalResult, 0, len(newPending))
	for _, pending := range newPending {
		newResults = append(newResults, cloneMioRetrievalResult(pending.result))
	}
	return newResults, nil
}

func validateMioRetrievalResponse(request domainagent.MioRetrievalRequest, result domainagent.MioRetrievalResult) error {
	if result.SourceKind != request.SourceKind {
		return fmt.Errorf("%w: response source %q does not match request source %q", domainagent.ErrInvalidMioRetrievalResult, result.SourceKind, request.SourceKind)
	}
	switch result.Status {
	case domainagent.MioRetrievalStatusDenied, domainagent.MioRetrievalStatusBlocked:
		return fmt.Errorf("%w: source status %q", ErrMioRetrievalDenied, result.Status)
	case domainagent.MioRetrievalStatusFound, domainagent.MioRetrievalStatusPartial:
		if len(result.Items) == 0 {
			return fmt.Errorf("%w: status %q has no items", domainagent.ErrInvalidMioRetrievalResult, result.Status)
		}
		for index, item := range result.Items {
			if strings.TrimSpace(item.Text) == "" {
				return fmt.Errorf("%w: item %d has empty content", domainagent.ErrInvalidMioRetrievalResult, index+1)
			}
		}
	case domainagent.MioRetrievalStatusNotFound, domainagent.MioRetrievalStatusDeleted, domainagent.MioRetrievalStatusUnavailable:
		if len(result.Items) != 0 {
			return fmt.Errorf("%w: status %q carries items", domainagent.ErrInvalidMioRetrievalResult, result.Status)
		}
	default:
		return fmt.Errorf("%w: unknown status %q", domainagent.ErrInvalidMioRetrievalResult, result.Status)
	}
	return nil
}

func mioRetrievalRequestKey(request domainagent.MioRetrievalRequest) (string, error) {
	request.Query = strings.ToLower(strings.Join(strings.Fields(request.Query), " "))
	request.InformationNeeded = strings.ToLower(strings.Join(strings.Fields(request.InformationNeeded), " "))
	request.References = append([]string(nil), request.References...)
	for index := range request.References {
		request.References[index] = strings.TrimSpace(request.References[index])
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func cloneMioRetrievalResult(result domainagent.MioRetrievalResult) domainagent.MioRetrievalResult {
	result.Items = append([]domainagent.MioRetrievalItem(nil), result.Items...)
	result.Provenance = append([]domainagent.MioRetrievalProvenance(nil), result.Provenance...)
	for index := range result.Provenance {
		result.Provenance[index].Reference.EvidenceEventIDs = append([]string(nil), result.Provenance[index].Reference.EvidenceEventIDs...)
	}
	return result
}

func (d *messageRouteDispatcher) generateChatResponse(ctx context.Context, input domainconversation.TurnInput, speaker string) (string, error) {
	switch speaker {
	case string(modulechat.ViewerRecipientMio):
		return d.mio.Chat(ctx, input)
	case string(modulechat.ViewerRecipientShiro):
		if d.shiroChat == nil {
			return "", fmt.Errorf("no ChatWorker agent available for Shiro CHAT")
		}
		return d.shiroChat.Chat(ctx, input)
	case string(modulechat.ViewerRecipientMidori):
		if d.wild == nil {
			return "", fmt.Errorf("no Wild agent available for Midori CHAT")
		}
		return d.wild.Generate(ctx, input)
	case string(modulechat.ViewerRecipientKuro):
		if d.heavy == nil {
			return "", fmt.Errorf("no heavy agent available for Kuro CHAT")
		}
		return d.heavy.Generate(ctx, input)
	default:
		return "", fmt.Errorf("unsupported CHAT recipient %q", speaker)
	}
}

func chatSpeakerForTurnInput(input domainconversation.TurnInput) string {
	recipient := normalizeProcessViewerRecipient(input.ViewerRecipient())
	if recipient == "" {
		return string(modulechat.DefaultViewerRecipient)
	}
	return recipient
}

func (d *messageRouteDispatcher) logSpeechPlanObservation(turnID, speakerID string, result speech.Result, routeFallbackReason string) {
	fallbackReason := routeFallbackReason
	if fallbackReason == "" {
		fallbackReason = result.FallbackReason
	}
	fallbackReason = boundedSpeechPlanFallbackReason(fallbackReason, result.Valid)

	observation := speechPlanRouteObservation{
		Event:            "speech.plan.observed",
		TurnID:           turnID,
		SpeakerID:        speakerID,
		SpeechPlanValid:  result.Valid,
		UtteranceCount:   boundedSpeechPlanUtteranceCount(result.Plan),
		CueCount:         boundedSpeechPlanCueCount(result.Plan),
		LLMGenerationMS:  result.LLMGenerationMS,
		PlanValidationMS: result.PlanValidationMS,
		FallbackReason:   fallbackReason,
	}
	encoded, err := json.Marshal(observation)
	if err != nil {
		return
	}
	log.New(log.Writer(), "", 0).Print(string(encoded))
}

func boundedSpeechPlanFallbackReason(reason string, valid bool) string {
	switch reason {
	case "":
		if valid {
			return ""
		}
		return "speech_plan_unavailable"
	case speech.FallbackSpeechPlanLimitExceeded,
		speech.FallbackUnknownField,
		speech.FallbackInvalidJSON,
		speech.FallbackPlainText,
		speech.FallbackInvalidSchema,
		speech.FallbackInvalidStructure,
		speech.FallbackInvalidSegmentText,
		speech.FallbackUnsupportedEmoji,
		speech.FallbackGenerationFailed,
		speechPlanHandoffUnavailable,
		speechPlanHandoffFailed,
		speechPlanDisplayUnavailable,
		speechPlanCallbackUnavailable:
		return reason
	default:
		return "speech_plan_unavailable"
	}
}

func boundedSpeechPlanUtteranceCount(plan speech.SpeechPlan) int {
	if len(plan.Utterances) > speech.MaxUtterances {
		return speech.MaxUtterances
	}
	return len(plan.Utterances)
}

func boundedSpeechPlanCueCount(plan speech.SpeechPlan) int {
	count := 0
	for utteranceIndex, utterance := range plan.Utterances {
		if utteranceIndex == speech.MaxUtterances {
			break
		}
		for segmentIndex, segment := range utterance.Segments {
			if segmentIndex == speech.MaxSegmentsPerUtterance {
				break
			}
			count += min(len(segment.Before), speech.MaxCuesPerPosition)
			count += min(len(segment.After), speech.MaxCuesPerPosition)
		}
	}
	return count
}

func (d *messageRouteDispatcher) executeOPSRoute(ctx context.Context, input domainconversation.TurnInput, taskID modulecore.TaskID, ttsSessionID string) (string, error) {
	sessionID, channel, chatID := turnInputMetadata(input)
	taskIDText := taskID.String()
	d.emit("agent.start", "mio", "shiro", "タスクを実行依頼", "OPS", taskIDText, sessionID, channel, chatID)
	resp, err := d.shiro.Execute(ctx, input)
	if err == nil {
		d.emit("agent.response", "shiro", "mio", resp, "OPS", taskIDText, sessionID, channel, chatID)
		d.emit("agent.report", "shiro", "mio", formatShiroToMioReport(routing.RouteOPS, taskIDText, resp), "OPS", taskIDText, sessionID, channel, chatID)
		d.emit("agent.response", "mio", "user", resp, "OPS", taskIDText, sessionID, channel, chatID)
		d.pushTTS(ctx, ttsSessionID, routing.RouteOPS, moduletts.AgentShiro, "agent.response", resp)
	} else {
		d.emit("agent.report", "shiro", "mio", formatShiroToMioReport(routing.RouteOPS, taskIDText, "実行失敗: "+err.Error()), "OPS", taskIDText, sessionID, channel, chatID)
	}
	return resp, err
}

func (d *messageRouteDispatcher) executeCodeRoute(ctx context.Context, input domainconversation.TurnInput, route routing.Route, taskID modulecore.TaskID, ttsSessionID string) (string, error) {
	resp, err := d.executeCodeViaShiro(ctx, input, route, taskID)
	if err == nil {
		d.pushTTS(ctx, ttsSessionID, route, moduletts.AgentShiro, "agent.response", resp)
	}
	return resp, err
}

func (d *messageRouteDispatcher) executeWildRoute(ctx context.Context, input domainconversation.TurnInput, taskID modulecore.TaskID, ttsSessionID string) (string, error) {
	sessionID, channel, chatID := turnInputMetadata(input)
	if d.wild == nil {
		return "", fmt.Errorf("no wild agent available")
	}
	taskIDText := taskID.String()
	work := fmt.Sprintf("route=%s task=%s の創作", routing.RouteWILD.String(), taskIDText)
	d.emit("agent.delegate", "mio", "midori", formatAgentHandoffSpeech("mio", "midori", work, input.MessageText()), "WILD", taskIDText, sessionID, channel, chatID)
	d.emit("agent.acknowledge", "midori", "mio", formatAgentHandoffReadbackSpeech("mio", "midori", work, input.MessageText()), "WILD", taskIDText, sessionID, channel, chatID)
	d.emit("agent.start", "mio", "midori", "創作中...", "WILD", taskIDText, sessionID, channel, chatID)
	streamCtx, ttsStream := d.withStreamHooks(ctx, routing.RouteWILD, "midori", taskIDText, sessionID, channel, chatID, ttsSessionID)
	resp, err := d.wild.Generate(streamCtx, input)
	if err == nil {
		d.emit("agent.response", "midori", "mio", resp, "WILD", taskIDText, sessionID, channel, chatID)
		d.emit("agent.report", "midori", "mio", formatAgentHandoffCompletionSpeech("mio", "midori", resp), "WILD", taskIDText, sessionID, channel, chatID)
		ttsStream.Finalize(ctx, resp)
	} else {
		d.emit("agent.report", "midori", "mio", formatAgentHandoffCompletionSpeech("mio", "midori", "実行失敗: "+err.Error()), "WILD", taskIDText, sessionID, channel, chatID)
	}
	return resp, err
}

func (d *messageRouteDispatcher) executePlanRoute(ctx context.Context, input domainconversation.TurnInput, taskID modulecore.TaskID, ttsSessionID string) (string, error) {
	sessionID, channel, chatID := turnInputMetadata(input)
	taskIDText := taskID.String()
	d.emit("agent.start", "mio", "user", "計画を検討中...", "PLAN", taskIDText, sessionID, channel, chatID)
	planCtx, ttsStream := d.withStreamHooks(ctx, routing.RoutePLAN, "mio", taskIDText, sessionID, channel, chatID, ttsSessionID)
	resp, err := d.mio.Chat(planCtx, input)
	if err == nil {
		d.emit("agent.response", "mio", "user", resp, "PLAN", taskIDText, sessionID, channel, chatID)
		ttsStream.Finalize(ctx, resp)
	}
	return resp, err
}

func (d *messageRouteDispatcher) executeAnalyzeRoute(ctx context.Context, input domainconversation.TurnInput, taskID modulecore.TaskID, ttsSessionID string) (string, error) {
	sessionID, channel, chatID := turnInputMetadata(input)
	taskIDText := taskID.String()
	if d.heavy == nil {
		return "", fmt.Errorf("no heavy agent available")
	}
	work := fmt.Sprintf("route=%s task=%s の分析", routing.RouteANALYZE.String(), taskIDText)
	d.emit("agent.delegate", "mio", "kuro", formatAgentHandoffSpeech("mio", "kuro", work, input.MessageText()), "ANALYZE", taskIDText, sessionID, channel, chatID)
	d.emit("agent.acknowledge", "kuro", "mio", formatAgentHandoffReadbackSpeech("mio", "kuro", work, input.MessageText()), "ANALYZE", taskIDText, sessionID, channel, chatID)
	d.emit("agent.start", "mio", "kuro", "分析中...", "ANALYZE", taskIDText, sessionID, channel, chatID)
	recordHeavyCanonicalEvent(ctx, d.canonicalEvents, "started", "Heavy Worker started", taskIDText)
	analyzeCtx, ttsStream := d.withStreamHooks(ctx, routing.RouteANALYZE, "kuro", taskIDText, sessionID, channel, chatID, ttsSessionID)
	resp, err := d.heavy.Generate(analyzeCtx, input)
	if err == nil {
		d.emit("agent.response", "kuro", "mio", resp, "ANALYZE", taskIDText, sessionID, channel, chatID)
		d.emit("agent.report", "kuro", "mio", formatAgentHandoffCompletionSpeech("mio", "kuro", resp), "ANALYZE", taskIDText, sessionID, channel, chatID)
		d.emit("agent.response", "mio", "user", resp, "ANALYZE", taskIDText, sessionID, channel, chatID)
		ttsStream.Finalize(ctx, resp)
		recordHeavyCanonicalEvent(ctx, d.canonicalEvents, "completed", "Heavy Worker completed", taskIDText)
	} else {
		d.emit("agent.report", "kuro", "mio", formatAgentHandoffCompletionSpeech("mio", "kuro", "実行失敗: "+err.Error()), "ANALYZE", taskIDText, sessionID, channel, chatID)
		recordHeavyCanonicalEvent(ctx, d.canonicalEvents, "failed", err.Error(), taskIDText)
	}
	return resp, err
}

func (d *messageRouteDispatcher) executeResearchRoute(ctx context.Context, input domainconversation.TurnInput, taskID modulecore.TaskID, ttsSessionID string) (string, error) {
	sessionID, channel, chatID := turnInputMetadata(input)
	taskIDText := taskID.String()
	d.emit("agent.start", "mio", "user", "調査中...", "RESEARCH", taskIDText, sessionID, channel, chatID)
	researchCtx, ttsStream := d.withStreamHooks(ctx, routing.RouteRESEARCH, "mio", taskIDText, sessionID, channel, chatID, ttsSessionID)
	resp, err := d.mio.Chat(researchCtx, input)
	if err == nil {
		d.emit("agent.response", "mio", "user", resp, "RESEARCH", taskIDText, sessionID, channel, chatID)
		ttsStream.Finalize(ctx, resp)
	}
	return resp, err
}

func (d *messageRouteDispatcher) executeCodeViaShiro(ctx context.Context, input domainconversation.TurnInput, route routing.Route, taskID modulecore.TaskID) (string, error) {
	req := CodeExecutionRequest{
		Input:  input.WithRoute(route),
		TaskID: taskID,
	}
	resp, err := d.codeExecutor.ExecuteCode(ctx, req)
	return resp.Response, err
}
