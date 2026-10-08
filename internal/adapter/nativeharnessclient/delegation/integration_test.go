package delegation

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_Harness/pkg/client"
	"github.com/Nyukimin/RenCrow_Harness/pkg/protocol"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/advisor"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	actionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/testsupport/harnessdeploy"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// The integration tests run CORE's delegation against the real rencrow-harness
// binary, built from the Harness module this build resolves, in its production
// composition (the real Service, the real RenCrow_LLM client and the strict wire
// over loopback HTTP), with a Gateway double as the only replaced part. They are
// skipped under -short.

type integration struct {
	t       *testing.T
	deploy  *harnessdeploy.Deployment
	actions *actionmanager.Manager
	runtime *Runtime
	log     *logSink
}

func newIntegration(t *testing.T, mutate func(*Settings), wrap func(StartFunc) StartFunc) *integration {
	t.Helper()
	deploy := harnessdeploy.New(t)
	actionRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := actionpersistence.NewJSONLStore(filepath.Join(actionRoot, "actions"))
	if err != nil {
		t.Fatalf("open the Action store: %v", err)
	}
	actions := actionmanager.New(store)
	settings := Settings{
		HarnessBinary: deploy.Binary, HarnessConfig: deploy.Config, ExpectedBuildRevision: harnessdeploy.BuildRevision,
		Workspace: Workspace{Path: deploy.Work, PolicyRef: deploy.PolicyRef, ExecutionMode: protocol.ModeStructuredOnly},
		Binding:   Binding{Selector: deploy.Binding.Selector, ProfileRevision: deploy.Binding.ProfileRevision, ExecutionRole: *deploy.Binding.ExecutionRole},
		Limits:    protocol.Limits{MaxModelSteps: 10, MaxToolCallsPerStep: 8, DeadlineSeconds: 600, MaxCaptureBytes: 67108864, MaxGenerationAttempts: 32},
		// A child that cannot start fails fast; a real start takes about a second.
		StartTimeout: 60 * time.Second, StepTimeout: 60 * time.Second, CancelGrace: 30 * time.Second,
	}
	if mutate != nil {
		mutate(&settings)
	}
	log := &logSink{}
	opts := Options{Log: log.write}
	if wrap != nil {
		opts.Start = wrap(startHarness)
	}
	runtime, err := NewRuntime(settings, actions, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })
	return &integration{t: t, deploy: deploy, actions: actions, runtime: runtime, log: log}
}

func (i *integration) delegationActions() []domainaction.Action {
	i.t.Helper()
	actions, err := i.actions.ListActions(context.Background(), domainaction.Filter{})
	if err != nil {
		i.t.Fatal(err)
	}
	var out []domainaction.Action
	for _, action := range actions {
		if action.Kind == domainaction.KindDelegation {
			out = append(out, action)
		}
	}
	return out
}

func TestIntegrationRealHarnessRunsTheDelegationAndTheToolEffectIsInTheWorkspace(t *testing.T) {
	it := newIntegration(t, nil, nil)
	createArgs, _ := json.Marshal(map[string]any{"path": "hello.txt", "text": "hello from the Harness", "expected_absent": true})
	it.deploy.Gateway.SetScript(
		harnessdeploy.Reply{Name: "file.create", Args: string(createArgs)},
		harnessdeploy.Reply{Text: "hello.txt を作成しました"},
	)
	ctx, input, messages := newTurn(t, "hello.txt を作成して")
	identity, _ := domainexecution.IdentityFromContext(ctx)

	result, err := it.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages})
	if err != nil {
		t.Fatalf("DelegateNativeCoding: %v", err)
	}
	if result.Status != agent.NativeRunCompleted || result.FinalText != "hello.txt を作成しました" {
		t.Fatalf("the Run must complete with the model's final answer: %+v", result)
	}
	if result.Accepted() {
		t.Fatalf("a Run that ran no verification must not be an accepted Task: %+v", result)
	}
	content, err := os.ReadFile(filepath.Join(it.deploy.Work, "hello.txt"))
	if err != nil || string(content) != "hello from the Harness" {
		t.Fatalf("the Tool effect must be in the allowed workspace: %q %v", content, err)
	}

	// CORE's record: one delegation Action in the parent Task Run, with the Harness
	// IDs by owner in its summary.
	delegations := it.delegationActions()
	if len(delegations) != 1 {
		t.Fatalf("exactly one delegation Action expected, got %d", len(delegations))
	}
	action := delegations[0]
	if action.TaskID != identity.TaskID || action.RunID != identity.RunID || action.Status != domainaction.StatusSucceeded {
		t.Fatalf("the Action must be the parent Task Run's and succeeded: %+v", action)
	}
	mustContain(t, "Action summary", action.Summary, "status=completed", "harness_task=RenCrow_Harness/"+result.HarnessTask.ID, "harness_run=RenCrow_Harness/"+result.HarnessRun.ID)
	attempts, err := it.actions.ListAttempts(context.Background(), domainaction.AttemptFilter{ActionID: action.ActionID})
	if err != nil || len(attempts) != 1 || attempts[0].AttemptID != result.AttemptID || attempts[0].Status != domainaction.AttemptStatusSucceeded {
		t.Fatalf("one Attempt, succeeded: %+v %v", attempts, err)
	}

	// The Model received CORE's typed context and the user's request.
	generations := it.deploy.Gateway.Generations()
	if len(generations) != 2 {
		t.Fatalf("the model is asked once per step (tool call, then final): %d", len(generations))
	}
	var prompt strings.Builder
	for _, message := range generations[0].Messages {
		prompt.WriteString(message.Role + ": " + message.Content + "\n")
	}
	mustContain(t, "the model's first prompt", prompt.String(), "character prompt", "stable context", "recall memory", "variable context", "hello.txt を作成して")

	// The Harness's own facts reached CORE's log as references, without content.
	waitForLog(t, it.log, eventHarnessEvent, 2)
	for _, line := range it.log.all() {
		mustNotContain(t, "log", line, "hello from the Harness", it.deploy.Binary, it.deploy.Config)
	}
	deadline := time.Now().Add(10 * time.Second)
	terminal := false
	for !terminal && time.Now().Before(deadline) {
		for _, entry := range it.log.events(t, eventHarnessEvent) {
			terminal = terminal || entry["event_type"] == protocol.EventRunTerminal
		}
		if !terminal {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if !terminal {
		t.Fatal("run.terminal must be recorded as a reference line")
	}

	// Orderly stop: the child leaves.
	if err := it.runtime.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// lossyStart wraps the real client so that the first turn/start is delivered,
// answered by the real Harness, and then reported to CORE as an unknown outcome:
// the answer was lost.
func lossyStart(real StartFunc) StartFunc {
	return func(ctx context.Context, cfg client.Config) (Client, error) {
		c, err := real(ctx, cfg)
		if err != nil {
			return nil, err
		}
		return &lossyClient{Client: c}, nil
	}
}

type lossyClient struct {
	Client
	mu     sync.Mutex
	starts []protocol.StartInput
}

func (c *lossyClient) TurnStart(ctx context.Context, in protocol.StartInput) (protocol.StartResult, error) {
	c.mu.Lock()
	c.starts = append(c.starts, in)
	first := len(c.starts) == 1
	c.mu.Unlock()
	result, err := c.Client.TurnStart(ctx, in)
	if first && err == nil {
		return protocol.StartResult{}, errors.Join(client.ErrOutcomeUnknown, context.DeadlineExceeded)
	}
	return result, err
}

func TestIntegrationALostStartAnswerIsRepeatedWithTheSameKeyAndTheHarnessStartsOneRun(t *testing.T) {
	var wrapped *lossyClient
	it := newIntegration(t, nil, func(real StartFunc) StartFunc {
		start := lossyStart(real)
		return func(ctx context.Context, cfg client.Config) (Client, error) {
			c, err := start(ctx, cfg)
			if lc, ok := c.(*lossyClient); ok {
				wrapped = lc
			}
			return c, err
		}
	})
	it.deploy.Gateway.SetScript(harnessdeploy.Reply{Text: "done once"})
	ctx, input, messages := newTurn(t, "テストを直して")

	result, err := it.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages})
	if err != nil {
		t.Fatalf("DelegateNativeCoding: %v", err)
	}
	if result.Status != agent.NativeRunCompleted || result.FinalText != "done once" {
		t.Fatalf("result = %+v", result)
	}
	if wrapped == nil || len(wrapped.starts) != 2 {
		t.Fatalf("the start must be sent twice (original and repeat), got %v", wrapped)
	}
	first, second := wrapped.starts[0], wrapped.starts[1]
	if first.IdempotencyKey != second.IdempotencyKey || first.Input != second.Input || first.ThreadID != second.ThreadID {
		t.Fatalf("the repeat must be the first payload with the same key:\n%+v\n%+v", first, second)
	}
	if got := len(it.deploy.Gateway.Generations()); got != 1 {
		t.Fatalf("the real Harness answers the repeat from its receipt: one Run, one generation, got %d", got)
	}
	if len(it.delegationActions()) != 1 {
		t.Fatal("a repeat must not record a second delegation Action")
	}
}

func TestIntegrationCancellingTheTurnStopsTheRealRunAndKeepsTheCancelledResult(t *testing.T) {
	it := newIntegration(t, nil, nil)
	release := make(chan struct{})
	it.deploy.Gateway.Hold(release)
	defer close(release)
	ctx, input, messages := newTurn(t, "時間のかかる作業")
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type outcome struct {
		result agent.NativeCodingResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := it.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages})
		done <- outcome{result, err}
	}()
	select {
	case <-it.deploy.Gateway.Measuring():
	case <-time.After(60 * time.Second):
		t.Fatal("the Run never reached the model")
	}
	cancel()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("a Run that the Harness cancelled is a typed result: %v", got.err)
		}
		if got.result.Status != agent.NativeRunCancelled {
			t.Fatalf("status = %s (code %s), want cancelled", got.result.Status, got.result.Code)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("the delegation did not end after the cancellation")
	}
	delegations := it.delegationActions()
	if len(delegations) != 1 || delegations[0].Status != domainaction.StatusCancelled {
		t.Fatalf("the Action must close cancelled: %+v", delegations)
	}
}

func TestIntegrationAnotherBuildOfTheHarnessIsNotUsed(t *testing.T) {
	it := newIntegration(t, func(s *Settings) { s.ExpectedBuildRevision = "a-build-that-was-not-pinned" }, nil)
	_, input, _ := newTurn(t, "x")
	err := it.runtime.AdmitNativeCoding(context.Background(), input)
	if !errors.Is(err, agent.ErrNativeCodingBlocked) {
		t.Fatalf("a Harness of another build must block the turn: %v", err)
	}
	mustContain(t, "refusal", err.Error(), codeBuildMismatch)
	if got := len(it.deploy.Gateway.Generations()); got != 0 {
		t.Fatalf("no work may reach the model of a Harness that was refused: %d", got)
	}
}

func TestIntegrationAMissingBinaryFailsClosedWithoutLeakingItsPath(t *testing.T) {
	it := newIntegration(t, func(s *Settings) { s.HarnessBinary = filepath.Join(filepath.Dir(s.HarnessBinary), "missing-harness") }, nil)
	_, input, _ := newTurn(t, "x")
	err := it.runtime.AdmitNativeCoding(context.Background(), input)
	if !errors.Is(err, agent.ErrNativeCodingBlocked) {
		t.Fatalf("a binary that cannot start must block the turn: %v", err)
	}
	mustNotContain(t, "refusal", err.Error(), "missing-harness")
}

// --- the real entry: ProcessMessage with the real orchestrator and the real Shiro ---

type entrySessions struct {
	mu sync.Mutex
	m  map[string]*session.Session
}

func (s *entrySessions) Save(_ context.Context, sess *session.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[sess.ID()] = sess
	return nil
}

func (s *entrySessions) Load(_ context.Context, id string) (*session.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.m[id]; ok {
		return sess, nil
	}
	address, err := conversation.NewChannelAddress("test", "test")
	if err != nil {
		return nil, err
	}
	sess, err := session.NewCanonicalSession(modulecore.NewSessionID(), "2026-10-08", address, time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC))
	if err != nil {
		return nil, err
	}
	s.m[id] = sess
	return sess, nil
}

func (s *entrySessions) Exists(_ context.Context, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.m[id]
	return ok, nil
}

func (s *entrySessions) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, id)
	return nil
}

type entryMio struct{ route routing.Route }

func (m entryMio) DecideAction(context.Context, conversation.TurnInput) (routing.Decision, error) {
	return routing.NewDecision(m.route, 0.9, "integration"), nil
}
func (entryMio) Chat(context.Context, conversation.TurnInput) (string, error) { return "mio chat", nil }
func (entryMio) HandleChatCommand(context.Context, string, string) (agent.ChatCommandResult, error) {
	return agent.ChatCommandResult{}, nil
}

// legacyTripwires counts every entry into an old Shiro route.
type legacyRoutes struct{ llm, tools, advisor, subagent atomic.Int32 }

type entryLLM struct{ r *legacyRoutes }

func (p entryLLM) Generate(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.r.llm.Add(1)
	return llm.GenerateResponse{Content: "legacy plain generate"}, nil
}
func (entryLLM) Name() string { return "entry-llm" }

type entryTools struct{ r *legacyRoutes }

func (t entryTools) ExecuteV2(context.Context, string, map[string]any) (*tool.ToolResponse, error) {
	t.r.tools.Add(1)
	return tool.NewSuccess("legacy codex output"), nil
}
func (t entryTools) ListTools(context.Context) ([]tool.ToolMetadata, error) {
	t.r.tools.Add(1)
	return []tool.ToolMetadata{{ToolID: "codex.run"}}, nil
}

type entryAdvisor struct{ r *legacyRoutes }

func (a entryAdvisor) RequestAdvice(context.Context, advisor.AdviceRequest) (advisor.AdviceResult, error) {
	a.r.advisor.Add(1)
	return advisor.AdviceResult{}, errors.New("legacy advisor path")
}

type entrySubagents struct{ r *legacyRoutes }

func (s entrySubagents) RunSync(context.Context, agent.SubagentTask) (agent.SubagentResult, error) {
	s.r.subagent.Add(1)
	return agent.SubagentResult{Output: "legacy toolloop"}, nil
}

type entryMCP struct{}

func (entryMCP) CallTool(context.Context, string, string, map[string]interface{}) (string, error) {
	return "", errors.New("mcp must not be called")
}
func (entryMCP) ListTools(context.Context, string) ([]string, error) { return nil, nil }

func newEntryOrchestrator(t *testing.T, it *integration, route routing.Route, withAdmission bool) (*orchestrator.MessageOrchestrator, *legacyRoutes) {
	t.Helper()
	legacy := &legacyRoutes{}
	shiro := agent.NewShiroAgent(entryLLM{legacy}, entryTools{legacy}, entryMCP{}, "character prompt", entrySubagents{legacy}).
		WithAdvisorService(entryAdvisor{legacy}).
		WithNativeCodingDelegate(it.runtime)
	orch := orchestrator.NewMessageOrchestrator(&entrySessions{m: map[string]*session.Session{}}, entryMio{route: route}, shiro, nil, nil, nil, nil, nil)
	taskRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	taskStore, err := taskpersistence.NewJSONLStore(filepath.Join(taskRoot, "tasks"))
	if err != nil {
		t.Fatalf("open the Task store: %v", err)
	}
	orch.SetTaskLifecycleManager(taskmanager.New(taskStore, taskmanager.ParallelLimits{}))
	orch.SetMaxRepair(3)
	if withAdmission {
		orch.SetNativeCodingAdmission(it.runtime)
	}
	return orch, legacy
}

func TestIntegrationRealEntryReachesTheRealHarnessAndNoOldRoute(t *testing.T) {
	// A message that matches the CodexWorkPath keyword, with a SubagentManager, an
	// advisor and a codex tool all available: none of them is reached.
	it := newIntegration(t, nil, nil)
	it.deploy.Gateway.SetScript(harnessdeploy.Reply{Text: "描画の作業を完了しました"})
	orch, legacy := newEntryOrchestrator(t, it, routing.RouteOPS, true)

	resp, err := orch.ProcessMessage(context.Background(), orchestrator.ProcessMessageRequest{
		SessionID: "20261008-line-U1", Channel: "line", ChatID: "U1", UserMessage: "この場面を描画して",
	})
	if err != nil {
		t.Fatalf("ProcessMessage: %v", err)
	}
	mustContain(t, "response", resp.Response, "描画の作業を完了しました", "検証されていません")
	if legacy.llm.Load()+legacy.tools.Load()+legacy.advisor.Load()+legacy.subagent.Load() != 0 {
		t.Fatalf("an old route was entered: llm=%d tools=%d advisor=%d subagent=%d", legacy.llm.Load(), legacy.tools.Load(), legacy.advisor.Load(), legacy.subagent.Load())
	}
	if got := len(it.deploy.Gateway.Generations()); got != 1 {
		t.Fatalf("exactly one Run reached the model, got %d", got)
	}
	delegations := it.delegationActions()
	if len(delegations) != 1 || delegations[0].TaskID.String() != resp.TaskID || delegations[0].Status != domainaction.StatusSucceeded {
		t.Fatalf("the delegation Action must belong to the Task of the turn: %+v (task %s)", delegations, resp.TaskID)
	}
}

func TestIntegrationRealEntryWithoutAdmissionKeepsTheExistingRoute(t *testing.T) {
	it := newIntegration(t, nil, nil)
	orch, legacy := newEntryOrchestrator(t, it, routing.RouteOPS, false)
	resp, err := orch.ProcessMessage(context.Background(), orchestrator.ProcessMessageRequest{
		SessionID: "20261008-line-U2", Channel: "line", ChatID: "U2", UserMessage: "整理して",
	})
	if err != nil {
		t.Fatalf("ProcessMessage: %v", err)
	}
	if legacy.subagent.Load() != 1 || resp.Response != "legacy toolloop" {
		t.Fatalf("without the profile the Shiro route is unchanged: %q subagent=%d", resp.Response, legacy.subagent.Load())
	}
	if got := len(it.deploy.Gateway.Generations()); got != 0 || len(it.delegationActions()) != 0 {
		t.Fatalf("the Harness must not be used: generations=%d actions=%d", got, len(it.delegationActions()))
	}
}

func TestIntegrationRealEntryWithAnUnavailableHarnessEndsBlockedWithoutFallback(t *testing.T) {
	it := newIntegration(t, func(s *Settings) { s.HarnessBinary = filepath.Join(filepath.Dir(s.HarnessBinary), "missing-harness") }, nil)
	orch, legacy := newEntryOrchestrator(t, it, routing.RouteOPS, true)
	_, err := orch.ProcessMessage(context.Background(), orchestrator.ProcessMessageRequest{
		SessionID: "20261008-line-U3", Channel: "line", ChatID: "U3", UserMessage: "この場面を描画して",
	})
	if !errors.Is(err, agent.ErrNativeCodingBlocked) {
		t.Fatalf("an unavailable Harness must end the turn blocked: %v", err)
	}
	if legacy.llm.Load()+legacy.tools.Load()+legacy.advisor.Load()+legacy.subagent.Load() != 0 {
		t.Fatal("a blocked profile turn must not fall back to an old route")
	}
}

func TestIntegrationTheSameHarnessAndStoreServeTheCLIWithoutCORE(t *testing.T) {
	it := newIntegration(t, nil, nil)
	it.deploy.Gateway.SetScript(harnessdeploy.Reply{Text: "answer through CORE"}, harnessdeploy.Reply{Text: "answer through the CLI"})
	ctx, input, messages := newTurn(t, "同じ課題")

	// CORE delegates a task to the Harness.
	viaCore, err := it.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages})
	if err != nil || viaCore.Status != agent.NativeRunCompleted || viaCore.FinalText != "answer through CORE" {
		t.Fatalf("via CORE: %+v %v", viaCore, err)
	}

	// CORE stops: the writer is released. The same binary, store, binding and model
	// serve the human CLI with its own caller profile, with no CORE process involved.
	if err := it.runtime.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	inputFile := filepath.Join(t.TempDir(), "task.txt")
	if err := os.WriteFile(inputFile, []byte("同じ課題"), 0o600); err != nil {
		t.Fatal(err)
	}
	cli := exec.Command(it.deploy.Binary, "exec", "--config", it.deploy.CLIConfig(t), "--workspace", it.deploy.Work,
		"--binding", harnessdeploy.BindingProfile, "--mode", protocol.ModeStructuredOnly, "--input-file", inputFile, "--json")
	cli.Dir = t.TempDir()
	var stderr strings.Builder
	cli.Stderr = &stderr
	out, err := cli.Output()
	if err != nil {
		t.Fatalf("the CLI must complete the run (exit 0 is completed): %v\nstdout: %s\nstderr: %s", err, out, stderr.String())
	}
	mustContain(t, "CLI output", string(out), "completed", "answer through the CLI")

	// Both runs reached the one model double, through the same Service and Kernel.
	if got := len(it.deploy.Gateway.Generations()); got != 2 {
		t.Fatalf("one generation per entrance, got %d", got)
	}
}
