package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"

	domainagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domainrouting "github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	infrarouting "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/routing"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type sequentialChatReplyProvider struct {
	responses []string
	requests  []llm.GenerateRequest
}

func (p *sequentialChatReplyProvider) Name() string { return "sequential-chat-reply-test" }

func (p *sequentialChatReplyProvider) Generate(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.requests = append(p.requests, req)
	index := len(p.requests) - 1
	if index >= len(p.responses) {
		return llm.GenerateResponse{Content: "unexpected extra generation"}, nil
	}
	return llm.GenerateResponse{Content: p.responses[index], TokensUsed: 12}, nil
}

type chatReplyRuleDictionary struct{}

func (chatReplyRuleDictionary) Match(domainconversation.TurnInput) (domainrouting.Route, float64, bool) {
	return "", 0, false
}

type failingChatReplyTaskLifecycleManager struct {
	*recordingTaskLifecycleManager
	err error
}

func (m *failingChatReplyTaskLifecycleManager) Start(_ context.Context, _ modulecore.TaskID) (domaintask.Task, error) {
	m.calls = append(m.calls, "Start")
	return domaintask.Task{}, m.err
}

type chatReplyConversationEngine struct {
	manager           *recordingTaskLifecycleManager
	commitCalls       int
	commitStarted     bool
	commitRunStarted  bool
	request           domainconversation.ConversationTurnRequest
	preparationActive bool
	preparedSession   string
	releaseCalls      int
}

func (e *chatReplyConversationEngine) BeginTurn(context.Context, string, string) (*domainconversation.RecallPack, error) {
	return &domainconversation.RecallPack{
		RollingSummary: "RecallPack prior conversation detail",
	}, nil
}

func (e *chatReplyConversationEngine) GetPersona() domainconversation.PersonaState {
	return domainconversation.PersonaState{}
}
func (e *chatReplyConversationEngine) FlushCurrentThread(context.Context, string) error { return nil }
func (e *chatReplyConversationEngine) GetStatus(context.Context, string) (*domainconversation.ConversationStatus, error) {
	return &domainconversation.ConversationStatus{}, nil
}
func (e *chatReplyConversationEngine) ResetSession(context.Context, string) error { return nil }
func (e *chatReplyConversationEngine) PrepareConversationTurn(_ context.Context, turnID, sessionID string) (*domainconversation.ConversationTurnPreparation, error) {
	if e.preparationActive {
		return nil, domainconversation.ErrConversationTurnConflict
	}
	e.preparationActive = true
	e.preparedSession = sessionID
	return &domainconversation.ConversationTurnPreparation{
		TurnID: modulecore.TurnID(turnID), SessionID: sessionID, OwnerID: "test-owner",
	}, nil
}
func (e *chatReplyConversationEngine) ReleaseConversationTurnPreparation(_ context.Context, preparation *domainconversation.ConversationTurnPreparation) error {
	if preparation == nil || !e.preparationActive || preparation.SessionID != e.preparedSession {
		return nil
	}
	e.preparationActive = false
	e.releaseCalls++
	return nil
}
func (e *chatReplyConversationEngine) CommitConversationTurn(_ context.Context, req domainconversation.ConversationTurnRequest) (domainconversation.ConversationTurnResult, error) {
	e.commitCalls++
	e.request = req
	for _, call := range e.manager.calls {
		if call == "Start" {
			e.commitStarted = true
		}
	}
	for _, run := range e.manager.runs {
		if run.Status == domaintask.RunStatusRunning {
			e.commitRunStarted = true
		}
	}
	return domainconversation.ConversationTurnResult{
		TurnID: req.TurnID, SessionID: req.SessionID, Status: domainconversation.ConversationTurnCompleted,
	}, nil
}

func TestMessageOrchestrator_UsesCombinedMioRouteAndAnswerAfterTaskActivation(t *testing.T) {
	bridge := &mockTTSBridge{}
	var streamedTokens []string
	provider := &sequentialChatReplyProvider{responses: []string{
		`{"schema_version":2,"route":"CHAT","outcome":{"kind":"answer","text":"Mio combined answer"}}`,
		"separate second answer must not be generated",
	}}
	classifier := infrarouting.NewLLMClassifier(provider, "current Mio classifier route policy")
	taskManager := newRecordingTaskLifecycleManager()
	engine := &chatReplyConversationEngine{manager: taskManager}
	mio := domainagent.NewMioAgent(provider, classifier, chatReplyRuleDictionary{}, nil, nil, engine).
		WithSystemPrompt("canonical Mio persona").
		WithGenerationOptions(domainagent.MioGenerationOptions{Stream: true, MaxTokens: 512, Temperature: 0.7})
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	orch.SetTTSBridge(bridge)
	orch.SetTaskLifecycleManager(taskManager)
	listener := &failOnEventListener{}
	orch.SetEventListener(listener)

	req := defaultReq()
	ctx := llm.ContextWithStreamCallback(context.Background(), func(token string) {
		streamedTokens = append(streamedTokens, token)
	})
	resp, err := orch.ProcessMessage(ctx, req)
	if err != nil {
		t.Fatalf("ProcessMessage() error = %v", err)
	}
	if resp.Response != "Mio combined answer" {
		t.Fatalf("response = %q, want combined Mio answer", resp.Response)
	}
	if got := len(provider.requests); got != 1 {
		t.Fatalf("provider Generate calls = %d, want 1", got)
	}
	combined := provider.requests[0]
	if combined.ResponseFormat != llm.ResponseFormatJSONObject {
		t.Fatalf("ResponseFormat = %q, want JSON object mode", combined.ResponseFormat)
	}
	if combined.SystemPrompt != "" {
		t.Fatalf("SystemPrompt = %q, want typed prompt messages to preserve canonical order", combined.SystemPrompt)
	}
	if combined.OnToken != nil {
		t.Fatal("combined JSON generation exposed tokens to stream hooks")
	}
	if len(streamedTokens) != 0 {
		t.Fatalf("JSON/reply generation leaked stream tokens: %#v", streamedTokens)
	}
	wantTypes := []llm.PromptContextType{
		llm.PromptContextCharacter,
		llm.PromptContextStable,
		llm.PromptContextRecall,
		llm.PromptContextRecall,
		llm.PromptContextVariable,
		llm.PromptContextVariable,
		llm.PromptContextVariable,
		llm.PromptContextVariable,
		llm.PromptContextUser,
	}
	if len(combined.Messages) != len(wantTypes) {
		t.Fatalf("combined message count = %d, want %d ordered Mio prompt messages: %#v", len(combined.Messages), len(wantTypes), combined.Messages)
	}
	for index, wantType := range wantTypes {
		if combined.Messages[index].Type != wantType {
			t.Fatalf("combined message[%d].Type = %q, want %q", index, combined.Messages[index].Type, wantType)
		}
	}
	if combined.Messages[0].Role != "system" || combined.Messages[0].Content != "canonical Mio persona" {
		t.Fatalf("character message = %#v", combined.Messages[0])
	}
	if combined.Messages[1].Metadata["runtime_context_kind"] != "classifier_mio_turn_outcome_contract" ||
		!strings.Contains(combined.Messages[1].Content, "current Mio classifier route policy") ||
		!strings.Contains(combined.Messages[1].Content, "MioTurnOutcome schema_version 3") {
		t.Fatalf("typed classifier policy/JSON contract = %#v", combined.Messages[1])
	}
	if !strings.Contains(combined.Messages[2].Content, "RecallPack contains the authoritative shared conversation history") {
		t.Fatalf("continuity message = %#v", combined.Messages[2])
	}
	if combined.Messages[3].Content != "【L0 現在会話 / rolling summary】\n- RecallPack prior conversation detail\n" {
		t.Fatalf("recall summary message = %#v", combined.Messages[3])
	}
	if !strings.Contains(combined.Messages[4].Content, "# Runtime-injected Mio context") || !strings.Contains(combined.Messages[5].Content, "発言帰属ガード") {
		t.Fatalf("runtime/attribution messages = %#v / %#v", combined.Messages[4], combined.Messages[5])
	}
	preparedBoundary := combined.Messages[len(combined.Messages)-3]
	if preparedBoundary.Metadata["runtime_context_kind"] != "prepared_conversation_boundary_state" ||
		!strings.HasPrefix(preparedBoundary.Content, "Prepared conversation head state (internal guidance for thread_boundary only):") ||
		!strings.Contains(preparedBoundary.Content, "current_thread_exists: false") ||
		!strings.Contains(preparedBoundary.Content, "continuity_state: no_prior_thread") {
		t.Fatalf("prepared conversation boundary state = %#v, want the prepared no-prior-thread hint", preparedBoundary)
	}
	if len(preparedBoundary.Content) > 512 {
		t.Fatalf("prepared conversation boundary state length = %d, want a bounded hint of at most 512 bytes", len(preparedBoundary.Content))
	}
	timeMessage := combined.Messages[len(combined.Messages)-2]
	if timeMessage.Metadata["runtime_context_kind"] != "time" || !strings.HasPrefix(timeMessage.Content, "現在時刻（JST）:") {
		t.Fatalf("JST time message = %#v", timeMessage)
	}
	last := combined.Messages[len(combined.Messages)-1]
	if last.Role != "user" || last.Content != req.UserMessage {
		t.Fatalf("final user message = %#v, want exact request text", last)
	}
	if engine.commitCalls != 1 || !engine.commitStarted || !engine.commitRunStarted {
		t.Fatalf("conversation commit calls=%d after task Start=%v and Run start=%v, want commit after activation", engine.commitCalls, engine.commitStarted, engine.commitRunStarted)
	}
	if engine.request.AgentMessage != "Mio combined answer" || engine.request.AgentSpeaker != domainconversation.SpeakerMio {
		t.Fatalf("committed turn = %#v, want one Mio answer", engine.request)
	}
	responses := 0
	for _, event := range listener.events {
		if event.Type == "agent.response" {
			responses++
			if engine.commitCalls != 1 {
				t.Fatal("agent.response was published before conversation commit")
			}
			if event.Content != "Mio combined answer" || event.From != "mio" {
				t.Fatalf("agent.response = %#v, want Mio combined answer", event)
			}
			if event.RunID == "" {
				t.Fatalf("agent.response has no activated Run identity: %#v", event)
			}
		}
	}
	if responses != 1 {
		t.Fatalf("agent.response count = %d, want 1", responses)
	}
	if len(bridge.pushes) != 1 || !strings.Contains(bridge.pushes[0], "combined answer") {
		t.Fatalf("TTS finalized text = %#v, want the completed Mio reply exactly once after normal speech filtering", bridge.pushes)
	}
	if len(bridge.startReqs) != 1 || len(bridge.ended) != 1 || bridge.ended[0] != bridge.startReqs[0].SessionID {
		t.Fatalf("successful TTS session lifecycle start=%#v end=%#v, want one close at the existing success point", bridge.startReqs, bridge.ended)
	}
}

func TestMessageOrchestrator_DoesNotRetryInvalidOrEmptyCombinedReply(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response string
	}{
		{name: "invalid JSON", response: `{"schema_version":2,"route":"CHAT","outcome":`},
		{name: "empty CHAT answer", response: `{"schema_version":2,"route":"CHAT","outcome":{"kind":"answer","text":"  "}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &sequentialChatReplyProvider{responses: []string{tc.response, "must not repair with a second generation"}}
			classifier := infrarouting.NewLLMClassifier(provider, "current Mio classifier route policy")
			taskManager := newRecordingTaskLifecycleManager()
			engine := &chatReplyConversationEngine{manager: taskManager}
			mio := domainagent.NewMioAgent(provider, classifier, chatReplyRuleDictionary{}, nil, nil, engine).
				WithSystemPrompt("canonical Mio persona").
				WithGenerationOptions(domainagent.MioGenerationOptions{Stream: true, MaxTokens: 512, Temperature: 0.7})
			orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
			bridge := &mockTTSBridge{}
			heavy := &mockHeavyAgent{response: "unexpected deep-dive promotion"}
			orch.SetHeavyAgent(heavy)
			orch.SetHeavyWorkerPolicy(domainai.HeavyWorkerPolicy{Enabled: true})
			orch.SetTTSBridge(bridge)
			orch.SetTaskLifecycleManager(taskManager)
			listener := &failOnEventListener{}
			orch.SetEventListener(listener)

			req := defaultReq()
			req.UserMessage = "この件を深掘りして"
			if _, err := orch.ProcessMessage(context.Background(), req); err == nil {
				t.Fatal("ProcessMessage() error = nil, want invalid combined-reply error")
			}
			if heavy.called {
				t.Fatal("invalid combined reply was promoted to heavy-worker ANALYZE")
			}
			if got := len(provider.requests); got != 1 {
				t.Fatalf("provider Generate calls = %d, want no repair generation", got)
			}
			if engine.commitCalls != 0 {
				t.Fatalf("conversation commits = %d, want none for invalid draft", engine.commitCalls)
			}
			for _, event := range listener.events {
				if event.Type == "agent.response" {
					t.Fatalf("agent.response published for invalid draft: %#v", event)
				}
			}
			if len(bridge.startReqs) != 1 || len(bridge.ended) != 1 || bridge.ended[0] != bridge.startReqs[0].SessionID {
				t.Fatalf("TTS session lifecycle start=%#v end=%#v, want one started session ended on invalid draft", bridge.startReqs, bridge.ended)
			}
			if len(bridge.pushes) != 0 {
				t.Fatalf("invalid draft produced TTS text: %#v", bridge.pushes)
			}
		})
	}
}

func TestMessageRouteDispatcherFinalizesDraftStreamWhenMioCannotAdopt(t *testing.T) {
	provider := &sequentialChatReplyProvider{responses: []string{`{"schema_version":2,"route":"CHAT","outcome":{"kind":"answer","text":"prepared answer"}}`}}
	classifier := infrarouting.NewLLMClassifier(provider, "current Mio classifier route policy")
	taskManager := newRecordingTaskLifecycleManager()
	preparedMio := domainagent.NewMioAgent(provider, classifier, chatReplyRuleDictionary{}, nil, nil, &chatReplyConversationEngine{manager: taskManager}).
		WithSystemPrompt("canonical Mio persona")
	req := defaultReq()
	if err := ensureProcessRequestIdentity(&req); err != nil {
		t.Fatalf("ensureProcessRequestIdentity: %v", err)
	}
	input, err := buildTurnInputFromProcessRequest(req)
	if err != nil {
		t.Fatalf("buildTurnInputFromProcessRequest: %v", err)
	}
	decision, draft, err := preparedMio.DecideActionWithChatDraft(context.Background(), input, true)
	if err != nil || draft == nil || decision.Route != domainrouting.RouteCHAT {
		t.Fatalf("DecideActionWithChatDraft = route %s draft=%v err=%v", decision.Route, draft != nil, err)
	}

	bridge := &mockTTSBridge{}
	var stream *ttsStreamForwarder
	chatCalls := 0
	var events []OrchestratorEvent
	chatOnlyMio := &mockMioAgent{chatFunc: func(context.Context, domainconversation.TurnInput) (string, error) {
		chatCalls++
		return "unexpected fallback response", nil
	}}
	dispatcher := newMessageRouteDispatcher(
		chatOnlyMio,
		&mockShiroAgent{},
		nil,
		func(eventType, from, to, content, route, taskID, sessionID, channel, chatID string) {
			events = append(events, NewEvent(eventType, from, to, content, route, taskID, sessionID, channel, chatID))
		},
		func(ctx context.Context, route domainrouting.Route, speaker, taskID, sessionID, channel, chatID, ttsSessionID string) (context.Context, *streamBundle) {
			stream = newTTSStreamForwarder(bridge, ttsSessionID, route, speaker, "agent.response", "[test]", context.Background())
			return ctx, &streamBundle{tts: stream}
		},
		func(context.Context, string, domainrouting.Route, string, string, string) {},
	)
	if _, err := dispatcher.executeChatRouteWithDraft(context.Background(), input, modulecore.NewTaskID(), "tts-test", draft); err == nil {
		t.Fatal("executeChatRouteWithDraft() error = nil, want missing draft-adoption capability")
	}
	if chatCalls != 0 {
		t.Fatalf("legacy Chat calls = %d, want no fallback generation", chatCalls)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("provider Generate calls = %d, want the single route/reply generation only", len(provider.requests))
	}
	stream.mu.Lock()
	closed := stream.closed
	stream.mu.Unlock()
	if !closed {
		t.Fatal("TTS stream forwarder remained open after draft-adoption error")
	}
	if len(bridge.pushes) != 0 {
		t.Fatalf("draft-adoption error produced TTS text: %#v", bridge.pushes)
	}
	for _, event := range events {
		if event.Type == "agent.response" {
			t.Fatalf("agent.response published for unadopted draft: %#v", event)
		}
	}
}

func TestRouteDecisionCoordinatorKeepsNonChatRouteAndDiscardsDraft(t *testing.T) {
	provider := &sequentialChatReplyProvider{responses: []string{`{"schema_version":2,"route":"CODE2","outcome":{"kind":"handoff"}}`}}
	classifier := infrarouting.NewLLMClassifier(provider, "current Mio classifier route policy")
	taskManager := newRecordingTaskLifecycleManager()
	mio := domainagent.NewMioAgent(provider, classifier, chatReplyRuleDictionary{}, nil, nil, &chatReplyConversationEngine{manager: taskManager}).
		WithSystemPrompt("canonical Mio persona")
	req := defaultReq()
	if err := ensureProcessRequestIdentity(&req); err != nil {
		t.Fatalf("ensureProcessRequestIdentity: %v", err)
	}
	input, err := buildTurnInputFromProcessRequest(req)
	if err != nil {
		t.Fatalf("buildTurnInputFromProcessRequest: %v", err)
	}
	result, err := newRouteDecisionCoordinator(mio).DecideWithChatDraft(context.Background(), input, req, modulecore.TaskID(req.RootTaskID), true)
	if err != nil {
		t.Fatalf("DecideWithChatDraft: %v", err)
	}
	if result.Decision.Route != domainrouting.RouteCODE2 {
		t.Fatalf("route = %s, want CODE2", result.Decision.Route)
	}
	if result.ChatDraft != nil {
		t.Fatal("non-CHAT route retained a candidate reply")
	}
	if len(provider.requests) != 1 {
		t.Fatalf("provider Generate calls = %d, want one combined route decision", len(provider.requests))
	}
}

func TestMessageOrchestrator_CombinedHandoffActivatesNonChatRoute(t *testing.T) {
	provider := &sequentialChatReplyProvider{responses: []string{
		`{"schema_version":2,"route":"CODE2","outcome":{"kind":"handoff"}}`,
	}}
	taskManager := newRecordingTaskLifecycleManager()
	engine := &chatReplyConversationEngine{manager: taskManager}
	mio := domainagent.NewMioAgent(
		provider,
		infrarouting.NewLLMClassifier(provider, "current Mio classifier route policy"),
		chatReplyRuleDictionary{}, nil, nil, engine,
	).WithSystemPrompt("canonical Mio persona")
	coder2 := &mockCoderAgent{response: "impl ready\n```\npatch applied\n```"}
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{response: "unused"}, nil, coder2, nil, nil, nil)
	orch.SetTaskLifecycleManager(taskManager)
	recorder := &recordingEventListener{}
	orch.SetEventListener(recorder)
	req := defaultReq()
	req.UserMessage = "実装を進めて"
	req.AudioOutput = AudioOutputDisabled

	response, err := orch.ProcessMessage(context.Background(), req)
	if err != nil {
		t.Fatalf("ProcessMessage() error = %v, want CODE2 handoff to execute", err)
	}
	if response.Route != domainrouting.RouteCODE2 {
		t.Fatalf("response route = %s, want CODE2", response.Route)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("Mio provider Generate calls = %d, want one combined route-and-handoff generation", len(provider.requests))
	}
	code2Tasks := 0
	for _, task := range taskManager.tasks {
		if task.Route == domaintask.RouteCODE2 && task.Assignee == "shiro" && task.ParentTaskID != "" {
			code2Tasks++
		}
	}
	if code2Tasks != 1 {
		t.Fatalf("activated CODE2 child tasks = %d, want one non-CHAT Task assigned to Shiro; tasks=%#v", code2Tasks, taskManager.tasks)
	}
	if engine.commitCalls != 0 {
		t.Fatalf("Mio conversation commits = %d, want no Mio CHAT commit for handoff", engine.commitCalls)
	}
	if engine.preparationActive || engine.releaseCalls != 1 {
		t.Fatalf("conversation preparation active=%v releases=%d, want released exactly once for handoff", engine.preparationActive, engine.releaseCalls)
	}
	foundRoute := false
	for _, event := range recorder.events {
		if event.Type == "routing.decision" && event.Route == domainrouting.RouteCODE2.String() {
			foundRoute = true
		}
	}
	if !foundRoute {
		t.Fatalf("no CODE2 routing.decision event in %#v", recorder.events)
	}
}

func TestRouteDecisionCoordinatorDiscardsDraftWhenPolicyChangesRoute(t *testing.T) {
	provider := &sequentialChatReplyProvider{responses: []string{`{"schema_version":2,"route":"CHAT","outcome":{"kind":"answer","text":"candidate answer"}}`}}
	classifier := infrarouting.NewLLMClassifier(provider, "current Mio classifier route policy")
	taskManager := newRecordingTaskLifecycleManager()
	engine := &chatReplyConversationEngine{manager: taskManager}
	mio := domainagent.NewMioAgent(provider, classifier, chatReplyRuleDictionary{}, nil, nil, engine).
		WithSystemPrompt("canonical Mio persona")
	req := defaultReq()
	req.UserMessage = "この件を深掘りして"
	if err := ensureProcessRequestIdentity(&req); err != nil {
		t.Fatalf("ensureProcessRequestIdentity: %v", err)
	}
	input, err := buildTurnInputFromProcessRequest(req)
	if err != nil {
		t.Fatalf("buildTurnInputFromProcessRequest: %v", err)
	}
	coordinator := newRouteDecisionCoordinator(mio)
	coordinator.SetHeavyWorkerPolicy(domainai.HeavyWorkerPolicy{Enabled: true})
	result, err := coordinator.DecideWithChatDraft(context.Background(), input, req, modulecore.TaskID(req.RootTaskID), true)
	if err != nil {
		t.Fatalf("DecideWithChatDraft: %v", err)
	}
	if result.Decision.Route != domainrouting.RouteANALYZE {
		t.Fatalf("route = %s, want heavy-policy ANALYZE", result.Decision.Route)
	}
	if result.ChatDraft != nil {
		t.Fatal("policy-changed route retained the candidate CHAT reply")
	}
	if engine.preparationActive || engine.releaseCalls != 1 {
		t.Fatalf("policy-changed route lease active=%v releases=%d, want released exactly once", engine.preparationActive, engine.releaseCalls)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("provider Generate calls = %d, want one route-and-answer generation", len(provider.requests))
	}
}

func TestRouteDecisionCoordinatorDirectRecipientBypassesCombinedClassifier(t *testing.T) {
	provider := &sequentialChatReplyProvider{responses: []string{`{"schema_version":2,"route":"CHAT","outcome":{"kind":"answer","text":"must not generate"}}`}}
	classifier := infrarouting.NewLLMClassifier(provider, "current Mio classifier route policy")
	taskManager := newRecordingTaskLifecycleManager()
	mio := domainagent.NewMioAgent(provider, classifier, chatReplyRuleDictionary{}, nil, nil, &chatReplyConversationEngine{manager: taskManager}).
		WithSystemPrompt("canonical Mio persona")
	req := defaultReq()
	req.Channel = "viewer"
	req.To = "shiro"
	if err := ensureProcessRequestIdentity(&req); err != nil {
		t.Fatalf("ensureProcessRequestIdentity: %v", err)
	}
	input, err := buildTurnInputFromProcessRequest(req)
	if err != nil {
		t.Fatalf("buildTurnInputFromProcessRequest: %v", err)
	}
	result, err := newRouteDecisionCoordinator(mio).DecideWithChatDraft(context.Background(), input, req, modulecore.TaskID(req.RootTaskID), true)
	if err != nil {
		t.Fatalf("DecideWithChatDraft: %v", err)
	}
	if result.Decision.Route != domainrouting.RouteCHAT || result.ChatDraft != nil {
		t.Fatalf("result = %+v, want direct-recipient CHAT with no Mio draft", result)
	}
	if len(provider.requests) != 0 {
		t.Fatalf("provider Generate calls = %d, want direct recipient to bypass Mio classifier", len(provider.requests))
	}
}

func TestMessageOrchestrator_DoesNotAdoptCombinedReplyWhenTaskActivationFails(t *testing.T) {
	provider := &sequentialChatReplyProvider{responses: []string{
		`{"schema_version":2,"route":"CHAT","outcome":{"kind":"answer","text":"Mio combined answer"}}`,
		"unexpected second generation",
	}}
	classifier := infrarouting.NewLLMClassifier(provider, "current Mio classifier route policy")
	taskManager := newRecordingTaskLifecycleManager()
	engine := &chatReplyConversationEngine{manager: taskManager}
	mio := domainagent.NewMioAgent(provider, classifier, chatReplyRuleDictionary{}, nil, nil, engine).
		WithSystemPrompt("canonical Mio persona").
		WithGenerationOptions(domainagent.MioGenerationOptions{Stream: true, MaxTokens: 512, Temperature: 0.7})
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	orch.SetTaskLifecycleManager(&failingChatReplyTaskLifecycleManager{
		recordingTaskLifecycleManager: taskManager,
		err:                           errors.New("task activation failed"),
	})
	listener := &failOnEventListener{}
	orch.SetEventListener(listener)

	req := defaultReq()
	req.AudioOutput = AudioOutputDisabled
	if _, err := orch.ProcessMessage(context.Background(), req); err == nil || !strings.Contains(err.Error(), "task activation failed") {
		t.Fatalf("ProcessMessage() error = %v, want activation failure", err)
	}
	if got := len(provider.requests); got != 1 {
		t.Fatalf("provider Generate calls = %d, want one route-and-answer generation before activation", got)
	}
	if engine.commitCalls != 0 {
		t.Fatalf("conversation commits = %d, want no commit after failed activation", engine.commitCalls)
	}
	for _, event := range listener.events {
		if event.Type == "agent.response" {
			t.Fatalf("agent.response published after failed activation: %#v", event)
		}
	}
	if engine.preparationActive || engine.releaseCalls != 1 {
		t.Fatalf("activation failure lease active=%v releases=%d, want released exactly once", engine.preparationActive, engine.releaseCalls)
	}
	preparation, err := engine.PrepareConversationTurn(context.Background(), string(modulecore.NewTurnID()), engine.preparedSession)
	if err != nil || preparation == nil {
		t.Fatalf("same-session next PrepareConversationTurn() = %+v, %v; want successful lease", preparation, err)
	}
	if err := engine.ReleaseConversationTurnPreparation(context.Background(), preparation); err != nil {
		t.Fatalf("release same-session next preparation: %v", err)
	}
}

type cancelingChatReplyTaskLifecycleManager struct {
	*recordingTaskLifecycleManager
	cancel context.CancelFunc
}

func (m *cancelingChatReplyTaskLifecycleManager) Start(_ context.Context, _ modulecore.TaskID) (domaintask.Task, error) {
	m.calls = append(m.calls, "Start")
	if m.cancel != nil {
		m.cancel()
	}
	return domaintask.Task{}, context.Canceled
}

func TestMessageOrchestratorReleasesCombinedDraftWhenActivationIsCanceled(t *testing.T) {
	provider := &sequentialChatReplyProvider{responses: []string{`{"schema_version":2,"route":"CHAT","outcome":{"kind":"answer","text":"Mio combined answer"}}`}}
	taskManager := newRecordingTaskLifecycleManager()
	engine := &chatReplyConversationEngine{manager: taskManager}
	mio := domainagent.NewMioAgent(provider,
		infrarouting.NewLLMClassifier(provider, "current Mio classifier route policy"),
		chatReplyRuleDictionary{}, nil, nil, engine,
	).WithSystemPrompt("canonical Mio persona")
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	canceledManager := &cancelingChatReplyTaskLifecycleManager{recordingTaskLifecycleManager: taskManager}
	orch.SetTaskLifecycleManager(canceledManager)
	ctx, cancel := context.WithCancel(context.Background())
	canceledManager.cancel = cancel
	req := defaultReq()
	req.AudioOutput = AudioOutputDisabled
	_, err := orch.ProcessMessage(ctx, req)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ProcessMessage() error=%v, want cancellation during task activation", err)
	}
	if engine.preparationActive || engine.releaseCalls != 1 {
		t.Fatalf("canceled activation lease active=%v releases=%d, want released exactly once", engine.preparationActive, engine.releaseCalls)
	}
	preparation, err := engine.PrepareConversationTurn(context.Background(), string(modulecore.NewTurnID()), engine.preparedSession)
	if err != nil || preparation == nil {
		t.Fatalf("same-session next PrepareConversationTurn() = %+v, %v; want successful lease", preparation, err)
	}
	if err := engine.ReleaseConversationTurnPreparation(context.Background(), preparation); err != nil {
		t.Fatalf("release same-session next preparation: %v", err)
	}
}
