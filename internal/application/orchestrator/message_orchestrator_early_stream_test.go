package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	appsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/application/superagent"
	domainagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	infrarouting "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/routing"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type earlyStreamProviderStep struct {
	events       []llm.LogicalOutcomeEvent
	response     string
	finishReason string
	err          error
}

type earlyStreamProviderObservation struct {
	starts     int
	runningRun int
	ttsStarts  int
	runID      string
	taskID     string
}

type earlyStreamProvider struct {
	steps    []earlyStreamProviderStep
	requests []llm.GenerateRequest
	manager  *recordingTaskLifecycleManager
	bridge   *mockTTSBridge
	started  chan earlyStreamProviderObservation
	release  <-chan struct{}
}

type earlyStreamPreparedChatEngine struct {
	*chatReplyConversationEngine
	lastGuard *earlyStreamPreparedGuard
}

func (e *earlyStreamPreparedChatEngine) PrepareChatTurn(ctx context.Context, turnID, sessionID, userMessage string) (*domainconversation.PreparedChatTurn, error) {
	preparation, err := e.PrepareConversationTurn(ctx, turnID, sessionID)
	if err != nil {
		return nil, err
	}
	guard := &earlyStreamPreparedGuard{engine: e.chatReplyConversationEngine, preparation: preparation}
	e.lastGuard = guard
	return &domainconversation.PreparedChatTurn{
		RecallPack:  &domainconversation.RecallPack{RollingSummary: "prepared early-stream test context"},
		Preparation: preparation,
		Guard:       guard,
	}, nil
}

type earlyStreamPreparedGuard struct {
	engine      *chatReplyConversationEngine
	preparation *domainconversation.ConversationTurnPreparation
	released    bool
}

func (g *earlyStreamPreparedGuard) OwnerID() string                                       { return g.preparation.OwnerID }
func (g *earlyStreamPreparedGuard) SourceVersion() string                                 { return "early-stream-test-v1" }
func (*earlyStreamPreparedGuard) BindingGeneration() domainconversation.BindingGeneration { return 1 }
func (g *earlyStreamPreparedGuard) MatchesPreparation(preparation *domainconversation.ConversationTurnPreparation) bool {
	return !g.released && preparation != nil && preparation.TurnID == g.preparation.TurnID && preparation.SessionID == g.preparation.SessionID
}
func (g *earlyStreamPreparedGuard) Validate() error {
	if g.released || !g.engine.preparationActive || !g.MatchesPreparation(g.preparation) {
		return domainconversation.ErrConversationTurnUnavailable
	}
	return nil
}
func (g *earlyStreamPreparedGuard) Use(enqueue func() error) error {
	if err := g.Validate(); err != nil {
		return err
	}
	return enqueue()
}
func (g *earlyStreamPreparedGuard) RetainPublication() (domainconversation.PreparedChatTurnPublicationGuard, error) {
	if err := g.Validate(); err != nil {
		return nil, err
	}
	return earlyStreamPublicationQualification{}, nil
}
func (g *earlyStreamPreparedGuard) Release() error {
	if g.released {
		return nil
	}
	g.released = true
	return g.engine.ReleaseConversationTurnPreparation(context.Background(), g.preparation)
}

type earlyStreamPublicationQualification struct{}

func (earlyStreamPublicationQualification) Use(claim func() error) error { return claim() }
func (q earlyStreamPublicationQualification) Retain() domainconversation.PreparedChatTurnPublicationGuard {
	return q
}
func (earlyStreamPublicationQualification) Release() {}

func (p *earlyStreamProvider) Name() string { return "early-stream-lifecycle-test" }

func (p *earlyStreamProvider) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.requests = append(p.requests, req)
	step := p.steps[len(p.requests)-1]
	for _, event := range step.events {
		if req.OnLogicalOutcomeEvent == nil {
			return llm.GenerateResponse{}, errors.New("logical outcome callback was not enabled")
		}
		if err := req.OnLogicalOutcomeEvent(event); err != nil {
			return llm.GenerateResponse{}, err
		}
		if event.Kind == llm.LogicalOutcomeAnswerStart && p.started != nil {
			observation := earlyStreamProviderObservation{ttsStarts: len(p.bridge.startReqs)}
			for _, call := range p.manager.calls {
				if call == "Start" {
					observation.starts++
				}
			}
			for _, run := range p.manager.runs {
				if run.Status == domaintask.RunStatusRunning {
					observation.runningRun++
					observation.runID = string(run.RunID)
					observation.taskID = run.TaskID.String()
				}
			}
			p.started <- observation
			if p.release != nil {
				select {
				case <-p.release:
				case <-ctx.Done():
					return llm.GenerateResponse{}, ctx.Err()
				}
			}
		}
	}
	if step.err != nil {
		return llm.GenerateResponse{}, step.err
	}
	return llm.GenerateResponse{Content: step.response, FinishReason: step.finishReason, TokensUsed: 5}, nil
}

func newEarlyStreamOrchestrator(t *testing.T, provider *earlyStreamProvider) (*MessageOrchestrator, ProcessMessageRequest, *recordingTaskLifecycleManager, *mockTTSBridge, *earlyStreamPreparedChatEngine) {
	t.Helper()
	manager := newRecordingTaskLifecycleManager()
	engine := &earlyStreamPreparedChatEngine{chatReplyConversationEngine: &chatReplyConversationEngine{manager: manager}}
	bridge := &mockTTSBridge{}
	provider.manager = manager
	provider.bridge = bridge
	mio := domainagent.NewMioAgent(
		provider,
		infrarouting.NewLLMClassifier(provider, "current Mio classifier route policy"),
		chatReplyRuleDictionary{}, nil, nil, engine,
	).WithSystemPrompt("canonical Mio persona").WithGenerationOptions(domainagent.MioGenerationOptions{Stream: true, MaxTokens: 512, Temperature: 0.7})
	repo := newMockSessionRepository()
	req := defaultReq()
	address, err := domainconversation.NewChannelAddress(req.Channel, req.ChatID)
	if err != nil {
		t.Fatalf("NewChannelAddress: %v", err)
	}
	sess, err := session.NewCanonicalSession(modulecore.NewSessionID(), "2026-10-24", address, time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewCanonicalSession: %v", err)
	}
	req.SessionID = sess.ID()
	repo.sessions[sess.ID()] = sess
	orch := NewMessageOrchestrator(repo, mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	orch.SetTTSBridge(bridge)
	orch.SetTaskLifecycleManager(manager)
	return orch, req, manager, bridge, engine
}

func TestMioAnswerStartPreparesRunBeforeProviderCompletes(t *testing.T) {
	release := make(chan struct{})
	started := make(chan earlyStreamProviderObservation, 1)
	provider := &earlyStreamProvider{
		started: started,
		release: release,
		steps: []earlyStreamProviderStep{{
			events: []llm.LogicalOutcomeEvent{
				{Kind: llm.LogicalOutcomeAnswerStart, SchemaVersion: 3, Route: "CHAT", PayloadKind: "text"},
				{Kind: llm.LogicalOutcomeAnswerDelta, Text: "validated partial"},
				{Kind: llm.LogicalOutcomeAnswerDone, FinishReason: "stop", ThreadBoundary: "new"},
			},
			response:     "validated partial",
			finishReason: "stop",
		}},
	}
	orch, req, manager, bridge, preparedEngine := newEarlyStreamOrchestrator(t, provider)
	req.mioAnswerDeltaSink = newMioAnswerDeltaSink()
	req.mioAnswerDeltaSink.drain = func() error {
		if preparedEngine.lastGuard == nil || preparedEngine.lastGuard.released {
			return errors.New("prepared-turn guard was released before transient delta drain")
		}
		return nil
	}
	type processResult struct {
		response ProcessMessageResponse
		err      error
	}
	done := make(chan processResult, 1)
	go func() {
		response, err := orch.ProcessMessage(context.Background(), req)
		done <- processResult{response: response, err: err}
	}()

	observation := <-started
	if observation.starts != 1 || observation.runningRun != 1 || observation.ttsStarts != 1 {
		t.Fatalf("at answer_start lifecycle starts=%d running runs=%d TTS starts=%d; want one of each before provider completion", observation.starts, observation.runningRun, observation.ttsStarts)
	}
	select {
	case delta := <-req.mioAnswerDeltaSink.deltas:
		t.Fatalf("answer_delta arrived before provider was released: %#v", delta)
	default:
	}
	close(release)
	result := <-done
	if result.err != nil {
		t.Fatalf("ProcessMessage: %v", result.err)
	}
	if len(provider.requests) != 1 || provider.requests[0].LogicalOutcomeStream != llm.LogicalOutcomeStreamV2 {
		t.Fatalf("provider stream opt-in requests = %#v, want one v2 generation", provider.requests)
	}
	if result.response.Response != "validated partial" {
		t.Fatalf("user-visible response = %q, want answer text without terminal boundary metadata", result.response.Response)
	}
	if got := countLifecycleCalls(manager.calls, "Start"); got != 1 {
		t.Fatalf("Task Start calls = %d, want exactly one", got)
	}
	if len(manager.runs) != 1 {
		t.Fatalf("Task Run count = %d, want one", len(manager.runs))
	}
	select {
	case delta := <-req.mioAnswerDeltaSink.deltas:
		if delta.Text != "validated partial" || delta.TaskID != observation.taskID || delta.RunID != observation.runID || delta.SessionID != req.SessionID || delta.MessageID == "" || delta.TraceID == "" {
			t.Fatalf("validated answer_delta identity = %#v", delta)
		}
	default:
		t.Fatal("validated answer_delta was not exposed through the internal sink")
	}
	if len(bridge.startReqs) != 1 || len(bridge.ended) != 1 {
		t.Fatalf("TTS start/end count = %d/%d, want one lifecycle", len(bridge.startReqs), len(bridge.ended))
	}
	if preparedEngine.lastGuard == nil || !preparedEngine.lastGuard.released {
		t.Fatal("prepared-turn guard was not released after transient delta drain")
	}
}

func TestMioHeavyPolicySuppressesEarlyAnswerActivationAndDeltas(t *testing.T) {
	started := make(chan earlyStreamProviderObservation, 1)
	release := make(chan struct{})
	provider := &earlyStreamProvider{
		started: started,
		release: release,
		steps: []earlyStreamProviderStep{{
			events: []llm.LogicalOutcomeEvent{
				{Kind: llm.LogicalOutcomeAnswerStart, SchemaVersion: 3, Route: "CHAT", PayloadKind: "text"},
				{Kind: llm.LogicalOutcomeAnswerDelta, Text: "must be suppressed"},
				{Kind: llm.LogicalOutcomeAnswerDone, FinishReason: "stop"},
			},
			response:     "must be suppressed",
			finishReason: "stop",
		}},
	}
	orch, req, manager, bridge, _ := newEarlyStreamOrchestrator(t, provider)
	req.UserMessage = "この件を深掘りして"
	req.mioAnswerDeltaSink = newMioAnswerDeltaSink()
	heavy := &mockHeavyAgent{response: "heavy analysis result"}
	orch.SetHeavyAgent(heavy)
	orch.SetHeavyWorkerPolicy(domainai.HeavyWorkerPolicy{Enabled: true})
	done := make(chan error, 1)
	go func() {
		_, err := orch.ProcessMessage(context.Background(), req)
		done <- err
	}()

	observation := <-started
	if observation.starts != 0 || observation.runningRun != 0 || observation.ttsStarts != 0 {
		t.Fatalf("heavy policy answer_start activated tentative CHAT: starts=%d runs=%d TTS=%d", observation.starts, observation.runningRun, observation.ttsStarts)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("ProcessMessage: %v", err)
	}
	if !heavy.called {
		t.Fatal("heavy ANALYZE route was not dispatched after suppressing tentative CHAT")
	}
	if len(req.mioAnswerDeltaSink.deltas) != 0 {
		t.Fatalf("heavy policy exposed %d tentative answer_delta events", len(req.mioAnswerDeltaSink.deltas))
	}
	if got := countLifecycleCalls(manager.calls, "Start"); got == 0 {
		t.Fatalf("Task Start calls after ANALYZE dispatch = %d, want later non-CHAT activation", got)
	}
	if len(bridge.startReqs) != 1 || bridge.startReqs[0].CharacterID != "kuro" {
		t.Fatalf("heavy policy TTS lifecycle = %#v, want only the later ANALYZE responder session", bridge.startReqs)
	}
}

func TestMioHeavyPolicyDoesNotDispatchAfterMalformedSuppressedAnswer(t *testing.T) {
	provider := &earlyStreamProvider{steps: []earlyStreamProviderStep{{
		events:   []llm.LogicalOutcomeEvent{{Kind: llm.LogicalOutcomeAnswerStart, SchemaVersion: 3, Route: "CHAT", PayloadKind: "text"}},
		response: "",
	}}}
	orch, req, manager, bridge, _ := newEarlyStreamOrchestrator(t, provider)
	req.UserMessage = "この件を深掘りして"
	req.mioAnswerDeltaSink = newMioAnswerDeltaSink()
	heavy := &mockHeavyAgent{response: "must not dispatch after malformed stream"}
	orch.SetHeavyAgent(heavy)
	orch.SetHeavyWorkerPolicy(domainai.HeavyWorkerPolicy{Enabled: true})
	if _, err := orch.ProcessMessage(context.Background(), req); err == nil {
		t.Fatal("ProcessMessage error = nil, want malformed stream failure")
	}
	if heavy.called {
		t.Fatal("heavy ANALYZE was dispatched from a malformed tentative CHAT answer")
	}
	if countLifecycleCalls(manager.calls, "Start") != 0 || len(bridge.startReqs) != 0 || len(req.mioAnswerDeltaSink.deltas) != 0 {
		t.Fatalf("malformed suppressed answer caused CHAT effects: starts=%v TTS=%#v deltas=%d", manager.calls, bridge.startReqs, len(req.mioAnswerDeltaSink.deltas))
	}
}

func TestMioRetrievalContinuationReusesPreparedLifecycleForAnswerStart(t *testing.T) {
	provider := &earlyStreamProvider{steps: []earlyStreamProviderStep{
		{
			events: []llm.LogicalOutcomeEvent{{
				Kind: llm.LogicalOutcomeRetrievalRequest, SchemaVersion: 3, Route: "CHAT",
				RetrievalRequests: []byte(`[{"source_kind":"user_memory","query":"departure","references":[],"information_needed":"departure time"}]`),
			}},
			response: "",
		},
		{
			events: []llm.LogicalOutcomeEvent{
				{Kind: llm.LogicalOutcomeAnswerStart, SchemaVersion: 3, Route: "CHAT", PayloadKind: "text"},
				{Kind: llm.LogicalOutcomeAnswerDelta, Text: "10時だよ。"},
				{Kind: llm.LogicalOutcomeAnswerDone, FinishReason: "stop"},
			},
			response:     "10時だよ。",
			finishReason: "stop",
		},
	}}
	orch, req, manager, bridge, _ := newEarlyStreamOrchestrator(t, provider)
	req.mioAnswerDeltaSink = newMioAnswerDeltaSink()
	orch.SetMioRetrievalPort(mioRetrievalPortFunc(func(_ context.Context, input domainconversation.TurnInput, _ domainagent.MioRetrievalRequest) (domainagent.MioRetrievalResult, error) {
		return validMioUserMemoryResultForOrchestratorTest(input, "departure is at 10"), nil
	}))
	if _, err := orch.ProcessMessage(context.Background(), req); err != nil {
		t.Fatalf("ProcessMessage: %v", err)
	}
	if len(provider.requests) != 2 || provider.requests[1].LogicalOutcomeStream != llm.LogicalOutcomeStreamV2 {
		t.Fatalf("retrieval/continuation generations = %#v, want initial request and opted-in fixed CHAT continuation", provider.requests)
	}
	if got := countLifecycleCalls(manager.calls, "Start"); got != 1 {
		t.Fatalf("Task Start calls = %d, want continuation to reuse the initial Run", got)
	}
	if len(manager.runs) != 1 {
		t.Fatalf("Task Run count = %d, want one reused Run", len(manager.runs))
	}
	if len(bridge.startReqs) != 1 {
		t.Fatalf("TTS starts = %d, want once after initial Run and before continuation", len(bridge.startReqs))
	}
	select {
	case delta := <-req.mioAnswerDeltaSink.deltas:
		if delta.Text != "10時だよ。" || delta.TaskID == "" || delta.RunID == "" {
			t.Fatalf("continuation delta = %#v", delta)
		}
	default:
		t.Fatal("continuation answer_delta was not exposed")
	}
}

func TestMioEarlyStreamFailureClosesStartedRunAndTTS(t *testing.T) {
	for _, tc := range []struct {
		name string
		step earlyStreamProviderStep
	}{
		{
			name: "malformed answer after start",
			step: earlyStreamProviderStep{
				events:   []llm.LogicalOutcomeEvent{{Kind: llm.LogicalOutcomeAnswerStart, SchemaVersion: 3, Route: "CHAT", PayloadKind: "text"}},
				response: "",
			},
		},
		{
			name: "provider abort after partial answer",
			step: earlyStreamProviderStep{
				events: []llm.LogicalOutcomeEvent{
					{Kind: llm.LogicalOutcomeAnswerStart, SchemaVersion: 3, Route: "CHAT", PayloadKind: "text"},
					{Kind: llm.LogicalOutcomeAnswerDelta, Text: "partial"},
				},
				err: errors.New("provider aborted"),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &earlyStreamProvider{steps: []earlyStreamProviderStep{tc.step}}
			orch, req, manager, bridge, _ := newEarlyStreamOrchestrator(t, provider)
			req.mioAnswerDeltaSink = newMioAnswerDeltaSink()
			if _, err := orch.ProcessMessage(context.Background(), req); err == nil {
				t.Fatal("ProcessMessage error = nil, want malformed/aborted combined answer failure")
			}
			if got := countLifecycleCalls(manager.calls, "Start"); got != 1 {
				t.Fatalf("Task Start calls = %d, want one early activation before failure", got)
			}
			if len(bridge.startReqs) != 1 || len(bridge.ended) != 1 || bridge.ended[0] != bridge.startReqs[0].SessionID {
				t.Fatalf("started TTS was not closed exactly once: starts=%#v ends=%#v", bridge.startReqs, bridge.ended)
			}
			for _, run := range manager.runs {
				if run.Status == domaintask.RunStatusRunning {
					t.Fatalf("Run remained active after stream failure: %#v", run)
				}
			}
		})
	}
}

func TestMioRunCancellationCancelsLiveProviderAndClosesTTS(t *testing.T) {
	started := make(chan earlyStreamProviderObservation, 1)
	release := make(chan struct{})
	provider := &earlyStreamProvider{
		started: started,
		release: release,
		steps: []earlyStreamProviderStep{{
			events: []llm.LogicalOutcomeEvent{{Kind: llm.LogicalOutcomeAnswerStart, SchemaVersion: 3, Route: "CHAT", PayloadKind: "text"}},
		}},
	}
	orch, req, manager, bridge, _ := newEarlyStreamOrchestrator(t, provider)
	req.mioAnswerDeltaSink = newMioAnswerDeltaSink()
	controller := appsuperagent.NewRunController()
	orch.SetSuperAgentRunController(controller)
	done := make(chan error, 1)
	go func() {
		_, err := orch.ProcessMessage(context.Background(), req)
		done <- err
	}()

	observation := <-started
	if observation.runID == "" {
		t.Fatal("answer_start did not bind a live Run before the provider blocked")
	}
	result := controller.PauseRun(observation.runID, "test cancellation")
	if !result.Applied {
		t.Fatalf("PauseRun result = %#v, want cancellation of the registered live Run", result)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("ProcessMessage error = nil, want provider cancellation to abort the partial answer")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run cancellation did not reach the live provider context")
	}
	if len(manager.runs) != 1 {
		t.Fatalf("Task Run count = %d, want one canceled Run", len(manager.runs))
	}
	for _, run := range manager.runs {
		if run.Status == domaintask.RunStatusRunning {
			t.Fatalf("Run remained active after provider cancellation: %#v", run)
		}
	}
	if len(bridge.startReqs) != 1 || len(bridge.ended) != 1 || bridge.ended[0] != bridge.startReqs[0].SessionID {
		t.Fatalf("canceled TTS session was not closed exactly once: starts=%#v ends=%#v", bridge.startReqs, bridge.ended)
	}
}

func countLifecycleCalls(calls []string, want string) int {
	count := 0
	for _, call := range calls {
		if call == want {
			count++
		}
	}
	return count
}

var _ llm.LLMProvider = (*earlyStreamProvider)(nil)
