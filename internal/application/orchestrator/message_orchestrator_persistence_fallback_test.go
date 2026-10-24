package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	appdurablestore "github.com/Nyukimin/RenCrow_CORE/internal/application/durablestore"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/durablestore"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	infrarouting "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/routing"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func newEmptyFallbackContext(t *testing.T) (context.Context, *persistencequeue.Admission, *atomic.Int32, *atomic.Bool) {
	t.Helper()
	queue, err := persistencequeue.NewPersistenceQueue(4, 64)
	if err != nil {
		t.Fatalf("NewPersistenceQueue() error = %v", err)
	}
	admission, err := persistencequeue.Admit(queue, 1, 16)
	if err != nil {
		t.Fatalf("Admit() error = %v", err)
	}
	calls := &atomic.Int32{}
	completed := &atomic.Bool{}
	ctx := persistencequeue.WithAdmission(context.Background(), admission)
	ctx = persistencequeue.WithSynchronousFallback(ctx, func(ctx context.Context) (context.Context, error) {
		calls.Add(1)
		if err := admission.Barrier(ctx); err != nil {
			return nil, err
		}
		if err := admission.Release(); err != nil {
			return nil, err
		}
		completed.Store(true)
		return persistencequeue.WithAdmission(ctx, nil), nil
	})
	t.Cleanup(func() {
		_ = admission.Release()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := queue.Shutdown(shutdownCtx); err != nil {
			t.Errorf("queue shutdown: %v", err)
		}
	})
	return ctx, admission, calls, completed
}

type noIntentDurableStoreWorkflow struct {
	calls *atomic.Int32
}

func (noIntentDurableStoreWorkflow) CanHandle(appdurablestore.Input) bool { return false }

func (w noIntentDurableStoreWorkflow) Handle(context.Context, appdurablestore.Input) (durablestore.WorkflowResult, bool, error) {
	if w.calls != nil {
		w.calls.Add(1)
	}
	return durablestore.WorkflowResult{}, false, nil
}

func TestMessageOrchestratorCombinedChatDraftKeepsRAMAdmission(t *testing.T) {
	ctx, _, fallbackCalls, _ := newEmptyFallbackContext(t)
	durableHandlerCalls := &atomic.Int32{}
	provider := &sequentialChatReplyProvider{responses: []string{`{"route":"CHAT","reply":"combined answer"}`}}
	manager := newRecordingTaskLifecycleManager()
	engine := &chatReplyConversationEngine{manager: manager}
	mio := agent.NewMioAgent(
		provider,
		infrarouting.NewLLMClassifier(provider, "current Mio classifier route policy"),
		chatReplyRuleDictionary{}, nil, nil, engine,
	).WithSystemPrompt("canonical Mio persona")
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	orch.SetTaskLifecycleManager(manager)
	orch.SetDurableStoreWorkflow(noIntentDurableStoreWorkflow{calls: durableHandlerCalls})

	req := defaultReq()
	resp, err := orch.ProcessMessage(ctx, req)
	if err != nil {
		t.Fatalf("ProcessMessage() error = %v", err)
	}
	if resp.Route != routing.RouteCHAT || resp.Response != "combined answer" {
		t.Fatalf("response = %+v, want combined Mio CHAT reply", resp)
	}
	if got := fallbackCalls.Load(); got != 0 {
		t.Fatalf("synchronous fallback calls = %d, want none for a valid combined CHAT draft", got)
	}
	if got := durableHandlerCalls.Load(); got != 0 {
		t.Fatalf("durable-store handler calls = %d, want none when the pure matcher rejects the message", got)
	}
}

func TestMessageOrchestratorFailedCombinedChatDraftStaysTerminalWithoutFallback(t *testing.T) {
	ctx, _, fallbackCalls, _ := newEmptyFallbackContext(t)
	provider := &sequentialChatReplyProvider{responses: []string{`{"route":"CHAT","reply":`}}
	manager := newRecordingTaskLifecycleManager()
	engine := &chatReplyConversationEngine{manager: manager}
	mio := agent.NewMioAgent(
		provider,
		infrarouting.NewLLMClassifier(provider, "current Mio classifier route policy"),
		chatReplyRuleDictionary{}, nil, nil, engine,
	).WithSystemPrompt("canonical Mio persona")
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	orch.SetTaskLifecycleManager(manager)

	_, err := orch.ProcessMessage(ctx, defaultReq())
	if err == nil || !strings.Contains(err.Error(), "combined Mio CHAT reply is unavailable") {
		t.Fatalf("ProcessMessage() error = %v, want terminal combined-draft error", err)
	}
	if got := fallbackCalls.Load(); got != 0 {
		t.Fatalf("synchronous fallback calls = %d, want none for an already-failed combined draft", got)
	}
	if got := len(provider.requests); got != 1 {
		t.Fatalf("provider Generate calls = %d, want the single failed combined generation", got)
	}
}

func TestMessageOrchestratorChatWithoutCombinedDraftFallsBackBeforeHandler(t *testing.T) {
	ctx, _, fallbackCalls, fallbackCompleted := newEmptyFallbackContext(t)
	manager := newRecordingTaskLifecycleManager()
	callsAtClassification := -1
	fallback, ok := persistencequeue.SynchronousFallbackFromContext(ctx)
	if !ok {
		t.Fatal("test context has no synchronous fallback capability")
	}
	ctx = persistencequeue.WithSynchronousFallback(ctx, func(ctx context.Context) (context.Context, error) {
		if callsAtClassification < 0 {
			return ctx, errors.New("CHAT fallback ran before classification")
		}
		if got := len(manager.calls); got != callsAtClassification {
			return ctx, fmt.Errorf("CHAT fallback ran after task activation: calls=%v", manager.calls)
		}
		return fallback(ctx)
	})
	mio := &mockMioAgent{
		decideFunc: func(ctx context.Context, _ conversation.TurnInput) (routing.Decision, error) {
			if _, ok := persistencequeue.AdmissionFromContext(ctx); !ok {
				return routing.Decision{}, errors.New("CHAT classifier lost the RAM admission")
			}
			callsAtClassification = len(manager.calls)
			return routing.NewDecision(routing.RouteCHAT, 1, "chat without a combined draft"), nil
		},
		chatFunc: func(ctx context.Context, _ conversation.TurnInput) (string, error) {
			if !fallbackCompleted.Load() {
				return "", errors.New("Mio CHAT handler began before persistence fallback")
			}
			if _, ok := persistencequeue.AdmissionFromContext(ctx); ok {
				return "", errors.New("Mio CHAT handler received the released RAM admission")
			}
			return "synchronous chat", nil
		},
	}
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	orch.SetTaskLifecycleManager(manager)
	resp, err := orch.ProcessMessage(ctx, defaultReq())
	if err != nil {
		t.Fatalf("ProcessMessage() error = %v", err)
	}
	if resp.Route != routing.RouteCHAT || resp.Response != "synchronous chat" {
		t.Fatalf("response = %+v, want completed synchronous CHAT", resp)
	}
	if got := fallbackCalls.Load(); got != 1 {
		t.Fatalf("synchronous fallback calls = %d, want one for CHAT without a combined draft", got)
	}
}

func TestMessageOrchestratorChatFallbackFailureStopsActivationAndHandler(t *testing.T) {
	ctx, _, _, _ := newEmptyFallbackContext(t)
	fallbackErr := errors.New("CHAT persistence barrier failed")
	var fallbackCalls atomic.Int32
	ctx = persistencequeue.WithSynchronousFallback(ctx, func(ctx context.Context) (context.Context, error) {
		fallbackCalls.Add(1)
		return ctx, fallbackErr
	})
	manager := newRecordingTaskLifecycleManager()
	callsAtClassification := -1
	chatCalls := atomic.Int32{}
	listener := &recordingEventListener{}
	mio := &mockMioAgent{
		decideFunc: func(context.Context, conversation.TurnInput) (routing.Decision, error) {
			callsAtClassification = len(manager.calls)
			return routing.NewDecision(routing.RouteCHAT, 1, "chat without a combined draft"), nil
		},
		chatFunc: func(context.Context, conversation.TurnInput) (string, error) {
			chatCalls.Add(1)
			return "must not run", nil
		},
	}
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	orch.SetTaskLifecycleManager(manager)
	orch.SetEventListener(listener)

	_, err := orch.ProcessMessage(ctx, defaultReq())
	if !errors.Is(err, fallbackErr) {
		t.Fatalf("ProcessMessage() error = %v, want fallback error", err)
	}
	if got := fallbackCalls.Load(); got != 1 {
		t.Fatalf("synchronous fallback calls = %d, want one", got)
	}
	if got := chatCalls.Load(); got != 0 {
		t.Fatalf("Mio CHAT handler calls = %d, want none after fallback failure", got)
	}
	for _, call := range manager.calls[callsAtClassification:] {
		if call == "Start" || call == "RecordRouting" || call == "RecordAssignment" {
			t.Fatalf("task activation proceeded after fallback failure: calls=%v", manager.calls)
		}
	}
	for _, event := range listener.events {
		if event.Type == "agent.assignment" || event.Type == "agent.start" || event.Type == "agent.response" {
			t.Fatalf("external route event %q published after fallback failure: %#v", event.Type, listener.events)
		}
	}
}

func TestMessageOrchestratorPreRoutingChatCommandFallsBackBeforeHandler(t *testing.T) {
	ctx, _, fallbackCalls, fallbackCompleted := newEmptyFallbackContext(t)
	mio := &mockMioAgent{
		cmdFunc: func(ctx context.Context, _, _ string) (agent.ChatCommandResult, error) {
			if !fallbackCompleted.Load() {
				t.Error("chat command handler ran before the persistence fallback completed")
			}
			if _, ok := persistencequeue.AdmissionFromContext(ctx); ok {
				t.Error("chat command handler received the released RAM admission")
			}
			return agent.ChatCommandResult{Handled: true, Response: "stopped"}, nil
		},
	}
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	req := defaultReq()
	req.UserMessage = "/stop"

	if _, err := orch.ProcessMessage(ctx, req); err != nil {
		t.Fatalf("ProcessMessage() error = %v", err)
	}
	if got := fallbackCalls.Load(); got != 1 {
		t.Fatalf("synchronous fallback calls = %d, want one for a pre-routing command", got)
	}
}

type fallbackCheckingCommandRegistry struct {
	completed *atomic.Bool
	commands  []domainai.CommandRegistry
}

func (r fallbackCheckingCommandRegistry) ListCommandRegistries(context.Context, int) ([]domainai.CommandRegistry, error) {
	if !r.completed.Load() {
		return nil, errors.New("slash command registry read began before persistence fallback")
	}
	return append([]domainai.CommandRegistry(nil), r.commands...), nil
}

type fallbackCheckingCanonicalRecorder struct {
	completed *atomic.Bool
	items     []modulecore.EventEnvelope
}

func (r *fallbackCheckingCanonicalRecorder) Append(_ context.Context, item modulecore.EventEnvelope) error {
	if !r.completed.Load() {
		return errors.New("slash command event write began before persistence fallback")
	}
	r.items = append(r.items, item)
	return nil
}

func TestMessageOrchestratorRegisteredSlashCommandFallsBackBeforeExpansion(t *testing.T) {
	ctx, _, fallbackCalls, fallbackCompleted := newEmptyFallbackContext(t)
	t.Chdir(t.TempDir())
	if err := os.Mkdir("commands", 0o755); err != nil {
		t.Fatalf("create commands directory: %v", err)
	}
	if err := os.WriteFile("commands/review-architecture.md", []byte("# review\n"), 0o600); err != nil {
		t.Fatalf("write slash command file: %v", err)
	}
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCHAT, 1, "chat"),
		response: "reviewed",
	}
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	orch.SetCommandRegistry(fallbackCheckingCommandRegistry{
		completed: fallbackCompleted,
		commands: []domainai.CommandRegistry{{
			CommandName: "/review-architecture",
			FilePath:    "commands/review-architecture.md",
		}},
	})
	canonical := &fallbackCheckingCanonicalRecorder{completed: fallbackCompleted}
	orch.SetCanonicalEventRecorder(canonical)
	attachCanonicalTestTaskOwner(t, orch)
	req := defaultReq()
	req.UserMessage = "/review-architecture route fallback behavior"

	if _, err := orch.ProcessMessage(ctx, req); err != nil {
		t.Fatalf("ProcessMessage() error = %v", err)
	}
	if got := fallbackCalls.Load(); got != 1 {
		t.Fatalf("synchronous fallback calls = %d, want one for a registered slash command", got)
	}
	if len(canonical.items) != 1 || canonical.items[0].EventType != "command.invoked" {
		t.Fatalf("registered slash command event = %#v, want one command.invoked event", canonical.items)
	}
}

type fallbackCheckingDurableStoreWorkflow struct {
	completed *atomic.Bool
	calls     int
}

func (w *fallbackCheckingDurableStoreWorkflow) CanHandle(appdurablestore.Input) bool { return true }

func (w *fallbackCheckingDurableStoreWorkflow) Handle(context.Context, appdurablestore.Input) (durablestore.WorkflowResult, bool, error) {
	w.calls++
	if !w.completed.Load() {
		return durablestore.WorkflowResult{}, true, errors.New("durable store handler began before persistence fallback")
	}
	return durablestore.WorkflowResult{}, true, errors.New("expected durable store handler test stop")
}

func TestMessageOrchestratorDurableStoreCandidateFallsBackBeforeHandler(t *testing.T) {
	ctx, _, fallbackCalls, fallbackCompleted := newEmptyFallbackContext(t)
	mio := &mockMioAgent{response: "ordinary chat"}
	workflow := &fallbackCheckingDurableStoreWorkflow{completed: fallbackCompleted}
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	orch.SetDurableStoreWorkflow(workflow)
	req := defaultReq()
	req.UserMessage = "ゲームDBを実装して"

	_, err := orch.ProcessMessage(ctx, req)
	if err == nil || !strings.Contains(err.Error(), "expected durable store handler test stop") {
		t.Fatalf("ProcessMessage() error = %v, want the deliberate durable-store handler stop", err)
	}
	if workflow.calls != 1 || fallbackCalls.Load() != 1 {
		t.Fatalf("durable handler calls=%d fallback calls=%d, want one each", workflow.calls, fallbackCalls.Load())
	}
}

type fallbackCheckingDCISearcher struct {
	completed *atomic.Bool
	calls     int
}

func (s *fallbackCheckingDCISearcher) ShouldTrigger(string) bool { return true }

func (s *fallbackCheckingDCISearcher) Search(context.Context, string) (domaindci.SearchResult, error) {
	s.calls++
	if !s.completed.Load() {
		return domaindci.SearchResult{}, errors.New("DCI search began before persistence fallback")
	}
	return domaindci.SearchResult{}, errors.New("expected DCI search test stop")
}

func TestMessageOrchestratorExplicitDCIFallsBackBeforeSearch(t *testing.T) {
	ctx, _, fallbackCalls, fallbackCompleted := newEmptyFallbackContext(t)
	orch := NewMessageOrchestrator(newMockSessionRepository(), &mockMioAgent{}, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	searcher := &fallbackCheckingDCISearcher{completed: fallbackCompleted}
	orch.SetDCISearcher(searcher)
	req := defaultReq()
	req.UserMessage = "docsから仕様を探して"

	_, err := orch.ProcessMessage(ctx, req)
	if err == nil || !strings.Contains(err.Error(), "expected DCI search test stop") {
		t.Fatalf("ProcessMessage() error = %v, want the deliberate DCI search stop", err)
	}
	if searcher.calls != 1 || fallbackCalls.Load() != 1 {
		t.Fatalf("DCI search calls=%d fallback calls=%d, want one each", searcher.calls, fallbackCalls.Load())
	}
}

func TestMessageOrchestratorNonChatWaitsForAdmissionBarrierBeforeActivationAndTool(t *testing.T) {
	writerStarted := make(chan struct{})
	releaseWriter := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseWriter) }) }
	queue, err := persistencequeue.NewPersistenceQueue(2, 16)
	if err != nil {
		t.Fatalf("NewPersistenceQueue() error = %v", err)
	}
	admission, err := persistencequeue.Admit(queue, 1, 1)
	if err != nil {
		t.Fatalf("Admit() error = %v", err)
	}
	if _, err := admission.Enqueue(persistencequeue.PersistenceCommand{
		Owner: persistencequeue.OwnerSession, Kind: "session.turn", SizeBytes: 1,
		Apply: func(context.Context) error {
			close(writerStarted)
			<-releaseWriter
			return nil
		},
	}); err != nil {
		t.Fatalf("Admission.Enqueue() error = %v", err)
	}
	t.Cleanup(func() {
		release()
		_ = admission.Release()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := queue.Shutdown(shutdownCtx); err != nil {
			t.Errorf("queue shutdown: %v", err)
		}
	})
	select {
	case <-writerStarted:
	case <-time.After(time.Second):
		t.Fatal("persistence writer did not start")
	}

	manager := newRecordingTaskLifecycleManager()
	callsAtClassification := -1
	fallbackStarted := make(chan struct{})
	fallbackCalls := &atomic.Int32{}
	ctx := persistencequeue.WithAdmission(context.Background(), admission)
	ctx = persistencequeue.WithSynchronousFallback(ctx, func(ctx context.Context) (context.Context, error) {
		fallbackCalls.Add(1)
		callsAtFallback := len(manager.calls)
		if callsAtClassification < 0 {
			return nil, errors.New("fallback ran before route classification")
		}
		if callsAtFallback != callsAtClassification {
			return nil, fmt.Errorf("task lifecycle changed after classification and before fallback: calls=%v", manager.calls)
		}
		close(fallbackStarted)
		if err := admission.Barrier(ctx); err != nil {
			return nil, fmt.Errorf("test persistence barrier: %w", err)
		}
		if err := admission.Release(); err != nil {
			return nil, fmt.Errorf("release test admission: %w", err)
		}
		return persistencequeue.WithAdmission(ctx, nil), nil
	})
	workerCalled := make(chan struct{}, 1)
	mio := &mockMioAgent{
		decideFunc: func(ctx context.Context, _ conversation.TurnInput) (routing.Decision, error) {
			if _, ok := persistencequeue.AdmissionFromContext(ctx); !ok {
				return routing.Decision{}, errors.New("route classifier lost the RAM admission before classification")
			}
			callsAtClassification = len(manager.calls)
			return routing.NewDecision(routing.RouteOPS, 0.9, "explicit operation"), nil
		},
	}
	shiro := &mockShiroAgent{executeFunc: func(ctx context.Context, _ conversation.TurnInput) (string, error) {
		if _, ok := persistencequeue.AdmissionFromContext(ctx); ok {
			return "", errors.New("post-classification tool received the released RAM admission")
		}
		workerCalled <- struct{}{}
		return "done", nil
	}}
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, shiro, nil, nil, nil, nil, nil)
	orch.SetTaskLifecycleManager(manager)

	type result struct {
		response ProcessMessageResponse
		err      error
	}
	completed := make(chan result, 1)
	go func() {
		response, err := orch.ProcessMessage(ctx, defaultReq())
		completed <- result{response: response, err: err}
	}()
	select {
	case <-fallbackStarted:
	case <-time.After(time.Second):
		t.Fatal("non-CHAT classification did not start synchronous fallback")
	}
	select {
	case <-workerCalled:
		t.Fatal("post-classification tool ran before the persistence barrier completed")
	default:
	}
	release()
	select {
	case got := <-completed:
		if got.err != nil {
			t.Fatalf("ProcessMessage() error = %v", got.err)
		}
		if got.response.Route != routing.RouteOPS || got.response.Response != "done" {
			t.Fatalf("response = %+v, want completed OPS response", got.response)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ProcessMessage() did not finish after the persistence barrier")
	}
	if got := fallbackCalls.Load(); got != 1 {
		t.Fatalf("synchronous fallback calls = %d, want one", got)
	}
}

func TestMessageOrchestratorNonChatRequiresFallbackCapability(t *testing.T) {
	queue, err := persistencequeue.NewPersistenceQueue(2, 8)
	if err != nil {
		t.Fatalf("NewPersistenceQueue() error = %v", err)
	}
	admission, err := persistencequeue.Admit(queue, 1, 8)
	if err != nil {
		t.Fatalf("Admit() error = %v", err)
	}
	t.Cleanup(func() {
		_ = admission.Release()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := queue.Shutdown(shutdownCtx); err != nil {
			t.Errorf("queue shutdown: %v", err)
		}
	})

	var workerCalls atomic.Int32
	ctx := persistencequeue.WithAdmission(context.Background(), admission)
	mio := &mockMioAgent{decision: routing.NewDecision(routing.RouteOPS, 0.9, "explicit operation")}
	shiro := &mockShiroAgent{executeFunc: func(context.Context, conversation.TurnInput) (string, error) {
		workerCalls.Add(1)
		return "done", nil
	}}
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, shiro, nil, nil, nil, nil, nil)
	attachCanonicalTestTaskOwner(t, orch)

	_, err = orch.ProcessMessage(ctx, defaultReq())
	if err == nil || !strings.Contains(err.Error(), "has RAM admission but no synchronous fallback capability") {
		t.Fatalf("ProcessMessage() error = %v, want fail-closed missing-fallback error", err)
	}
	if got := workerCalls.Load(); got != 0 {
		t.Fatalf("post-classification worker calls = %d, want none without a fallback capability", got)
	}
}

func TestMessageOrchestratorHeavyPolicyFallsBackBeforeCanonicalEvent(t *testing.T) {
	ctx, _, fallbackCalls, fallbackCompleted := newEmptyFallbackContext(t)
	canonical := &fallbackCheckingCanonicalRecorder{completed: fallbackCompleted}
	heavy := &mockHeavyAgent{response: "deep analysis"}
	mio := &mockMioAgent{decision: routing.NewDecision(routing.RouteCHAT, 0.7, "default chat")}
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	attachCanonicalTestTaskOwner(t, orch)
	orch.SetHeavyAgent(heavy)
	orch.SetHeavyWorkerPolicy(domainai.HeavyWorkerPolicy{Enabled: true, RequireReason: true})
	orch.SetCanonicalEventRecorder(canonical)
	req := defaultReq()
	req.UserMessage = "この件を深掘りして"

	resp, err := orch.ProcessMessage(ctx, req)
	if err != nil {
		t.Fatalf("ProcessMessage() error = %v", err)
	}
	if resp.Route != routing.RouteANALYZE || !heavy.called {
		t.Fatalf("response = %+v, heavy called = %v; want ANALYZE through the heavy agent", resp, heavy.called)
	}
	if got := fallbackCalls.Load(); got != 1 {
		t.Fatalf("synchronous fallback calls = %d, want one before canonical publication", got)
	}
	if len(canonical.items) != 3 || canonical.items[0].EventType != "heavy_worker.requested" {
		t.Fatalf("canonical heavy worker events = %#v, want requested + lifecycle events", canonical.items)
	}
}

func TestMessageOrchestratorHeavyPolicyFallbackFailureStopsCanonicalEvent(t *testing.T) {
	ctx, _, _, _ := newEmptyFallbackContext(t)
	fallbackErr := errors.New("persistence barrier failed")
	var fallbackCalls atomic.Int32
	ctx = persistencequeue.WithSynchronousFallback(ctx, func(ctx context.Context) (context.Context, error) {
		fallbackCalls.Add(1)
		return nil, fallbackErr
	})
	canonical := &mockCanonicalEventRecorder{}
	heavy := &mockHeavyAgent{response: "must not execute"}
	mio := &mockMioAgent{decision: routing.NewDecision(routing.RouteCHAT, 0.7, "default chat")}
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	attachCanonicalTestTaskOwner(t, orch)
	orch.SetHeavyAgent(heavy)
	orch.SetHeavyWorkerPolicy(domainai.HeavyWorkerPolicy{Enabled: true, RequireReason: true})
	orch.SetCanonicalEventRecorder(canonical)
	req := defaultReq()
	req.UserMessage = "この件を深掘りして"

	_, err := orch.ProcessMessage(ctx, req)
	if !errors.Is(err, fallbackErr) {
		t.Fatalf("ProcessMessage() error = %v, want persistence barrier error", err)
	}
	if got := fallbackCalls.Load(); got != 1 {
		t.Fatalf("synchronous fallback calls = %d, want one", got)
	}
	if len(canonical.events) != 0 || heavy.called {
		t.Fatalf("canonical events = %#v, heavy called = %v; neither should run after fallback failure", canonical.events, heavy.called)
	}
}
