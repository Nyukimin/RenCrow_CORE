package delegation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_Harness/pkg/client"
	"github.com/Nyukimin/RenCrow_Harness/pkg/protocol"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/nativeharnessclient"
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
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
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
	tasks   *taskmanager.Manager
	runtime *Runtime
	log     *logSink
}

func newIntegration(t *testing.T, mutate func(*Settings), wrap func(StartFunc) StartFunc) *integration {
	return newIntegrationFixture(t, false, mutate, wrap)
}

func newIntegrationWithVerifier(t *testing.T) *integration {
	return newIntegrationFixture(t, true, nil, nil)
}

func newIntegrationFixture(t *testing.T, withVerifier bool, mutate func(*Settings), wrap func(StartFunc) StartFunc) *integration {
	t.Helper()
	var deploy *harnessdeploy.Deployment
	if withVerifier {
		deploy = harnessdeploy.NewWithVerification(t)
	} else {
		deploy = harnessdeploy.New(t)
	}
	criteriaRevision := strings.Repeat("a", 64)
	executionMode := protocol.ModeStructuredOnly
	if withVerifier {
		criteriaRevision = harnessCriteriaRevision(t, deploy)
		executionMode = protocol.ModeTrustedHost
		logHarnessFixtureIdentity(t, deploy.Binary)
	}
	actionRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := actionpersistence.NewJSONLStore(filepath.Join(actionRoot, "actions"))
	if err != nil {
		t.Fatalf("open the Action store: %v", err)
	}
	actions := actionmanager.New(store)
	taskStore, err := taskpersistence.NewJSONLStore(filepath.Join(actionRoot, "tasks"))
	if err != nil {
		t.Fatalf("open the Task store: %v", err)
	}
	tasks, err := taskmanager.NewWithExpectedCriteriaRevision(taskStore, taskmanager.DefaultParallelLimits(), criteriaRevision)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := taskStore.Close(); err != nil {
			t.Errorf("close Task store: %v", err)
		}
	})
	settings := Settings{
		HarnessBinary: deploy.Binary, HarnessConfig: deploy.Config, ExpectedBuildRevision: harnessdeploy.BuildRevision,
		ExpectedCriteriaRevision: criteriaRevision,
		Workspace:                Workspace{Path: deploy.Work, PolicyRef: deploy.PolicyRef, ExecutionMode: executionMode},
		Binding:                  Binding{Selector: deploy.Binding.Selector, ProfileRevision: deploy.Binding.ProfileRevision, ExecutionRole: *deploy.Binding.ExecutionRole},
		Limits:                   protocol.Limits{MaxModelSteps: 10, MaxToolCallsPerStep: 8, DeadlineSeconds: 600, MaxCaptureBytes: 67108864, MaxGenerationAttempts: 32},
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
	runtime, err := NewRuntime(settings, actions, tasks, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })
	return &integration{t: t, deploy: deploy, actions: actions, tasks: tasks, runtime: runtime, log: log}
}

func harnessCriteriaRevision(t *testing.T, deploy *harnessdeploy.Deployment) string {
	t.Helper()
	command := exec.Command(deploy.Binary, "verification-digest", "--config", deploy.Config, "--policy-ref", deploy.PolicyRef,
		"--workspace", deploy.Work, "--mode", string(protocol.ModeTrustedHost))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Harness verification-digest CLI failed: %v (%s)", err, strings.TrimSpace(string(output)))
	}
	var result struct {
		CriteriaRevision string `json:"criteria_revision"`
	}
	if err := json.Unmarshal(output, &result); err != nil || !domaintask.ValidCriteriaRevision(result.CriteriaRevision) {
		t.Fatalf("Harness verification-digest JSON is invalid: %v", err)
	}
	return result.CriteriaRevision
}

func logHarnessFixtureIdentity(t *testing.T, binaryPath string) {
	t.Helper()
	file, err := os.Open(binaryPath)
	if err != nil {
		t.Fatalf("open Harness fixture binary for identity: %v", err)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		_ = file.Close()
		t.Fatalf("hash Harness fixture binary: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := buildinfo.ReadFile(binaryPath)
	if err != nil {
		t.Fatalf("read Harness fixture build info: %v", err)
	}
	harnessModule := "unlisted"
	for _, dep := range info.Deps {
		if dep.Path == "github.com/Nyukimin/RenCrow_Harness" {
			harnessModule = dep.Version
			if dep.Replace != nil {
				harnessModule += " => " + dep.Replace.Version
			}
		}
	}
	t.Logf("Harness fixture binary SHA-256=%s Go=%s main=%s@%s module=%s", hex.EncodeToString(hash.Sum(nil)), info.GoVersion, info.Main.Path, info.Main.Version, harnessModule)
}

func (i *integration) newTurn(text string) (context.Context, conversation.TurnInput, []llm.Message) {
	i.t.Helper()
	ctx, baseInput, messages := newTurn(i.t, text)
	parentID := modulecore.NewTaskID()
	input, err := conversation.ReconstructTurnInput(
		parentID, baseInput.TurnID(), baseInput.TraceID(), baseInput.UserMessageID(), baseInput.AgentMessageID(),
		baseInput.MessageText(), baseInput.ChannelAddress(),
	)
	if err != nil {
		i.t.Fatal(err)
	}
	parent, err := i.tasks.Create(context.Background(), domaintask.Task{
		TaskID: parentID, Title: "native delegation test root", Route: domaintask.RouteOperations,
		OwnerID: shiroActorID, Assignee: shiroActorID, OriginTurnID: input.TurnID(),
	}, domaintask.SharedRoleContext{})
	if err != nil {
		i.t.Fatalf("create Task root: %v", err)
	}
	if _, err := i.tasks.Start(context.Background(), parent.TaskID); err != nil {
		i.t.Fatalf("start Task root: %v", err)
	}
	child, err := i.tasks.Create(context.Background(), domaintask.Task{
		Title: "native delegation test child", Route: domaintask.RouteOperations, ParentTaskID: parent.TaskID,
		OwnerID: shiroActorID, Assignee: shiroActorID, OriginTurnID: input.TurnID(),
	}, domaintask.SharedRoleContext{})
	if err != nil {
		i.t.Fatalf("create Task child: %v", err)
	}
	run, err := i.tasks.StartRunWithReason(context.Background(), child.TaskID, domaintask.RunStartReasonFirst)
	if err != nil {
		i.t.Fatalf("start Task child Run: %v", err)
	}
	scope, ok := tool.ToolExecutionScopeFromContext(ctx)
	if !ok {
		i.t.Fatal("test turn has no trusted Shiro scope")
	}
	ctx, err = domainexecution.WithIdentity(context.Background(), child.TaskID, run.RunID, input.TraceID())
	if err != nil {
		i.t.Fatal(err)
	}
	return tool.WithToolExecutionScope(ctx, scope), input, messages
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
	ctx, input, messages := it.newTurn("hello.txt を作成して")
	identity, _ := domainexecution.IdentityFromContext(ctx)

	result, err := it.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages})
	if err != nil {
		t.Fatalf("DelegateNativeCoding: %v", err)
	}
	if result.Status != agent.NativeRunCompleted || result.FinalText != "hello.txt を作成しました" {
		t.Fatalf("the Run must complete with the model's final answer: %+v", result)
	}
	if result.Accepted() || result.Verification != agent.NativeVerificationNotRun {
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
	if action.TaskID != identity.TaskID || action.RunID != identity.RunID || action.Status != domainaction.StatusFailed {
		t.Fatalf("the completed but unverified Run must keep its parent Action failed: %+v", action)
	}
	mustContain(t, "Action summary", action.Summary, "status=completed", "verification=not_run", "harness_task=RenCrow_Harness/"+result.HarnessTask.ID, "harness_run=RenCrow_Harness/"+result.HarnessRun.ID)
	attempts, err := it.actions.ListAttempts(context.Background(), domainaction.AttemptFilter{ActionID: action.ActionID})
	if err != nil || len(attempts) != 1 || attempts[0].AttemptID != result.AttemptID || attempts[0].Status != domainaction.AttemptStatusFailed {
		t.Fatalf("one failed Attempt for the non-accepted Run: %+v %v", attempts, err)
	}
	var storedRun protocol.RunResult
	if err := json.Unmarshal(attempts[0].NativeDelegation.RunResult, &storedRun); err != nil {
		t.Fatalf("the actual RunResult must remain persisted: %v", err)
	}
	if storedRun.Status != string(agent.NativeRunCompleted) || storedRun.Verification.Status != string(agent.NativeVerificationNotRun) || storedRun.FinalText != result.FinalText {
		t.Fatalf("persisted RunResult must retain its completed/not_run outcome: %+v", storedRun)
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

func TestIntegrationRealHarnessVerifierBindsCriteriaAndSealedEvidenceBeforeAdoption(t *testing.T) {
	it := newIntegrationWithVerifier(t)
	it.deploy.Gateway.SetScript(harnessdeploy.Reply{Text: "fixed verifier passed"})
	ctx, input, messages := it.newTurn("run the fixed verification fixture")
	identity, err := domainexecution.IdentityFromContext(ctx)
	if err != nil {
		t.Fatalf("test turn has no trusted task identity: %v", err)
	}

	result, err := it.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages})
	if err != nil {
		t.Fatalf("DelegateNativeCoding with the fixed owner verifier: %v", err)
	}
	if result.Status != agent.NativeRunCompleted || result.Verification != agent.NativeVerificationPassed || !result.Accepted() {
		t.Fatalf("the real Harness verifier must produce an adopted completed result: %+v", result)
	}
	if got := len(it.deploy.Gateway.Generations()); got != 1 {
		t.Fatalf("the Gateway double must receive one generation, got %d", got)
	}

	task, err := it.tasks.Get(context.Background(), identity.TaskID)
	if err != nil || !domaintask.ValidCriteriaRevision(task.ExpectedCriteriaRevision) {
		t.Fatalf("the accepted Task must keep its immutable expected criteria: task=%+v err=%v", task, err)
	}
	delegations := it.delegationActions()
	if len(delegations) != 1 || delegations[0].Status != domainaction.StatusSucceeded {
		t.Fatalf("the verified result must close one delegation Action as succeeded: %+v", delegations)
	}
	attempts, err := it.actions.ListAttempts(context.Background(), domainaction.AttemptFilter{ActionID: delegations[0].ActionID})
	if err != nil || len(attempts) != 1 {
		t.Fatalf("one adopted delegation Attempt expected: %+v err=%v", attempts, err)
	}
	attempt := attempts[0]
	if attempt.Status != domainaction.AttemptStatusSucceeded || attempt.NativeDelegation.Proof == nil {
		t.Fatalf("the Attempt must persist the adoption proof: %+v", attempt.NativeDelegation)
	}
	proof := attempt.NativeDelegation.Proof
	if proof.ExpectedCriteriaRevision != task.ExpectedCriteriaRevision || proof.RunResultSHA256 == "" || proof.TerminalEventID == "" || proof.TerminalEventSeq == 0 || len(proof.Evidence) == 0 {
		t.Fatalf("the compact proof must bind Task criteria, exact RunResult, terminal and verifier evidence: %+v", proof)
	}
	var stored protocol.RunResult
	if err := json.Unmarshal(attempt.NativeDelegation.RunResult, &stored); err != nil {
		t.Fatalf("the actual owner RunResult must remain persisted: %v", err)
	}
	if stored.Status != string(agent.NativeRunCompleted) || stored.Verification.Status != string(agent.NativeVerificationPassed) || stored.Verification.CriteriaRevision == nil || *stored.Verification.CriteriaRevision != task.ExpectedCriteriaRevision {
		t.Fatalf("the persisted RunResult must retain the exact accepted owner result: %+v", stored)
	}
}

type nativeResumeClientCapture struct {
	mu              sync.Mutex
	opens           []protocol.SessionOpenInput
	starts          []protocol.StartInput
	resumes         []protocol.ResumeInput
	readMode        string
	runGets         int
	receiptGets     int
	awaitRunEntered chan struct{}
	awaitRunOnce    sync.Once
}

type nativeResumeCaptureClient struct {
	Client
	capture *nativeResumeClientCapture
}

func (c *nativeResumeCaptureClient) SessionOpen(ctx context.Context, input protocol.SessionOpenInput) (protocol.SessionOpenResult, error) {
	c.capture.mu.Lock()
	c.capture.opens = append(c.capture.opens, input)
	c.capture.mu.Unlock()
	return c.Client.SessionOpen(ctx, input)
}

func (c *nativeResumeCaptureClient) TurnStart(ctx context.Context, input protocol.StartInput) (protocol.StartResult, error) {
	c.capture.mu.Lock()
	c.capture.starts = append(c.capture.starts, input)
	c.capture.mu.Unlock()
	return c.Client.TurnStart(ctx, input)
}

func (c *nativeResumeCaptureClient) RunResume(ctx context.Context, input protocol.ResumeInput) (protocol.ResumeResult, error) {
	c.capture.mu.Lock()
	c.capture.resumes = append(c.capture.resumes, input)
	c.capture.mu.Unlock()
	return c.Client.RunResume(ctx, input)
}

func (c *nativeResumeCaptureClient) AwaitRun(ctx context.Context, runID string, opts client.AwaitOptions) (protocol.RunResult, error) {
	c.capture.awaitRunOnce.Do(func() { close(c.capture.awaitRunEntered) })
	return c.Client.AwaitRun(ctx, runID, opts)
}

func (c *nativeResumeCaptureClient) ReceiptGet(ctx context.Context, input protocol.ReceiptGetInput) (protocol.ReceiptRecord, error) {
	c.capture.mu.Lock()
	mode := c.capture.readMode
	c.capture.receiptGets++
	c.capture.mu.Unlock()
	if mode == "receipt_error" {
		return protocol.ReceiptRecord{}, errors.New("receipt owner read unavailable")
	}
	receipt, err := c.Client.ReceiptGet(ctx, input)
	if err == nil && mode == "receipt_mismatch" {
		receipt.ReceiptID = "different-known-receipt"
	}
	return receipt, err
}

func (c *nativeResumeCaptureClient) SessionGet(ctx context.Context, input protocol.SessionGetInput) (protocol.SessionInfo, error) {
	c.capture.mu.Lock()
	mode := c.capture.readMode
	c.capture.mu.Unlock()
	session, err := c.Client.SessionGet(ctx, input)
	if err == nil && mode == "active_child" {
		activeRunID := input.ThreadID + ".active"
		session.ActiveRunID = &activeRunID
	}
	return session, err
}

func (c *nativeResumeCaptureClient) RunGet(ctx context.Context, input protocol.RunGetInput) (protocol.RunInfo, error) {
	c.capture.mu.Lock()
	mode := c.capture.readMode
	c.capture.runGets++
	c.capture.mu.Unlock()
	info, err := c.Client.RunGet(ctx, input)
	if err == nil && mode == "run_mismatch" {
		info.RunID = "different-known-run"
	}
	return info, err
}

type rejectNativeResumeActionEffect struct {
	TaskOwner
	err error
}

func (owner rejectNativeResumeActionEffect) ExecuteNativeOPSResumeActionEffect(context.Context, modulecore.TaskID, modulecore.RunID, string, domaintask.NativeOPSResumeClaim, func(context.Context) error) error {
	return owner.err
}

func captureNativeResumeClient(real StartFunc, capture *nativeResumeClientCapture) StartFunc {
	return func(ctx context.Context, cfg client.Config) (Client, error) {
		started, err := real(ctx, cfg)
		if err != nil {
			return nil, err
		}
		return &nativeResumeCaptureClient{Client: started, capture: capture}, nil
	}
}

func TestIntegrationRealHarnessResumesKnownRunOnSameThreadWithoutOpeningOrStarting(t *testing.T) {
	capture := &nativeResumeClientCapture{awaitRunEntered: make(chan struct{})}
	it := newIntegrationFixture(t, true, nil, func(real StartFunc) StartFunc {
		return captureNativeResumeClient(real, capture)
	})
	const ownerID = "resume-owner"
	const firstRequestID = "request-resume-source"
	const resumeRequestID = "request-resume-new-run"
	const sourceText = "resume the accepted work after the run is cancelled"
	userContext, receipt, initialClaim, accepted := newResumeAcceptedOPSContext(t, it, firstRequestID, ownerID, sourceText)
	workerContext, err := tool.DeriveAgentToolExecutionScope(userContext, firstRequestID, shiroActorID, "worker", "ops", true)
	if err != nil {
		t.Fatal(err)
	}
	executionContext, err := domainexecution.WithIdentity(workerContext, initialClaim.Task.TaskID, initialClaim.Run.RunID, receipt.TraceID)
	if err != nil {
		t.Fatal(err)
	}
	executionContext = nativeharnessclient.WithAcceptedInputReader(executionContext, acceptedOPSInputReaderFunc(func(context.Context) (conversation.AcceptedOPSInput, error) {
		return accepted, nil
	}))
	input, err := conversation.ReconstructTurnInput(
		receipt.TaskID, receipt.TurnID, receipt.TraceID, receipt.UserMessageID, receipt.AgentMessageID, sourceText,
		mustIntegrationChannel(t),
	)
	if err != nil {
		t.Fatal(err)
	}
	input = input.WithSessionID(string(receipt.SessionID)).WithRoute(routing.RouteOPS)
	messages := []llm.Message{
		{Role: "system", Content: "character prompt", Type: llm.PromptContextCharacter},
		{Role: "system", Content: "stable context", Type: llm.PromptContextStable},
		{Role: "system", Content: "recall memory", Type: llm.PromptContextRecall},
		{Role: "system", Content: "variable context", Type: llm.PromptContextVariable},
		{Role: "user", Content: sourceText, Type: llm.PromptContextUser},
	}

	releaseGateway := make(chan struct{})
	var releaseOnce sync.Once
	closeGateway := func() { releaseOnce.Do(func() { close(releaseGateway) }) }
	defer closeGateway()
	it.deploy.Gateway.Hold(releaseGateway)
	turnContext, cancelTurn := context.WithCancel(executionContext)
	type delegationOutcome struct {
		result agent.NativeCodingResult
		err    error
	}
	firstDone := make(chan delegationOutcome, 1)
	go func() {
		result, err := it.runtime.DelegateNativeCoding(turnContext, agent.NativeCodingRequest{Input: input, Messages: messages})
		firstDone <- delegationOutcome{result: result, err: err}
	}()
	startupDeadline := time.After(60 * time.Second)
	select {
	case <-it.deploy.Gateway.Measuring():
	case <-startupDeadline:
		cancelTurn()
		t.Fatal("source Run never reached the model")
	}
	// Gateway measurement can precede CORE acceptedStart persistence; AwaitRun entry proves Start completed and released its Task fence before cancellation.
	select {
	case <-capture.awaitRunEntered:
	case <-startupDeadline:
		cancelTurn()
		t.Fatal("source Run never entered AwaitRun after Start was accepted")
	}
	cancelTurn()
	var first delegationOutcome
	select {
	case first = <-firstDone:
	case <-time.After(60 * time.Second):
		t.Fatal("source Run did not finish after cancellation")
	}
	if first.err != nil || first.result.Status != agent.NativeRunCancelled || !first.result.Resumable {
		t.Fatalf("real Harness source must be terminal, cancelled, and resumable: result=%+v err=%v", first.result, first.err)
	}
	closeGateway()

	if _, err := it.tasks.Cancel(userContext, receipt.TaskID, "source Run cancelled"); err != nil {
		t.Fatalf("close source CORE Task: %v", err)
	}
	closedTask, err := it.tasks.Get(userContext, receipt.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	source, err := it.runtime.PrepareNativeOPSResumeSource(userContext, closedTask, initialClaim.Run.RunID)
	if err != nil {
		t.Fatalf("read known cancelled child source: %v", err)
	}
	oldActions, err := it.actions.ListActions(userContext, domainaction.Filter{TaskID: receipt.TaskID, RunID: initialClaim.Run.RunID})
	if err != nil || len(oldActions) != 1 {
		t.Fatalf("source Action=%+v err=%v", oldActions, err)
	}
	oldAttempts, err := it.actions.ListAttempts(userContext, domainaction.AttemptFilter{ActionID: oldActions[0].ActionID})
	if err != nil || len(oldAttempts) != 1 {
		t.Fatalf("source Attempt=%+v err=%v", oldAttempts, err)
	}
	originalSourceResult := append([]byte(nil), oldAttempts[0].NativeDelegation.RunResult...)
	originalInputReference := oldAttempts[0].NativeDelegation.InputReference
	if originalInputReference == nil || originalInputReference.RequestID != firstRequestID || originalInputReference.OwnerID != ownerID {
		t.Fatalf("source Action lost the original accepted input reference: %+v", originalInputReference)
	}

	// The Task pin is the proof expectation for this accepted work. A newer
	// runtime config revision cannot silently replace it on Resume.
	pinnedCriteria := closedTask.ExpectedCriteriaRevision
	it.runtime.settings.ExpectedCriteriaRevision = strings.Repeat("b", 64)
	resumeUserScope, err := tool.NewToolExecutionScope(resumeRequestID, tool.ActorKindUser, ownerID, ownerID,
		[]string{tool.DataScopePublic, tool.DataScopeUser}, tool.AuthenticationSourceHTTP)
	if err != nil {
		t.Fatal(err)
	}
	resumeUserContext := tool.WithToolExecutionScope(context.Background(), resumeUserScope)
	resumeClaim, err := it.tasks.AdmitNativeOPSResume(resumeUserContext, taskmanager.NativeOPSResumeInput{
		TaskID: receipt.TaskID, ExpectedCoreRunID: initialClaim.Run.RunID, Source: source,
	})
	if err != nil || !resumeClaim.MayExecute || resumeClaim.Task.ExpectedCriteriaRevision != pinnedCriteria {
		t.Fatalf("Task owner must admit one same-Task Resume with its frozen criteria pin: claim=%+v err=%v", resumeClaim, err)
	}
	it.deploy.Gateway.SetScript(harnessdeploy.Reply{Text: "resumed and verified"})
	resumeContext, err := tool.DeriveAgentToolExecutionScope(resumeUserContext, resumeRequestID, shiroActorID, "worker", "ops", true)
	if err != nil {
		t.Fatal(err)
	}
	resumeContext, err = domainexecution.WithIdentity(resumeContext, receipt.TaskID,
		resumeClaim.Run.RunID, resumeClaim.Run.TraceID)
	if err != nil {
		t.Fatal(err)
	}
	it.runtime.tasks = rejectNativeResumeActionEffect{TaskOwner: it.tasks, err: errors.New("simulated crash boundary before Action adoption")}
	_, err = it.runtime.ExecuteNativeOPSResume(resumeContext, resumeClaim.Task, resumeClaim.Run, resumeClaim.Claim)
	it.runtime.tasks = it.tasks
	if !errors.Is(err, agent.ErrNativeCodingOutcomeUnknown) {
		t.Fatalf("failed Task-owner Action adoption must remain unresolved after Harness completion: %v", err)
	}

	capture.mu.Lock()
	opens := append([]protocol.SessionOpenInput(nil), capture.opens...)
	starts := append([]protocol.StartInput(nil), capture.starts...)
	resumes := append([]protocol.ResumeInput(nil), capture.resumes...)
	capture.mu.Unlock()
	if len(opens) != 1 || len(starts) != 1 || len(resumes) != 1 {
		t.Fatalf("Resume must add only one run/resume mutation: opens=%d starts=%d resumes=%d", len(opens), len(starts), len(resumes))
	}
	resumeInput := resumes[0]
	if resumeInput.TaskID != source.HarnessTaskID || resumeInput.ExpectedLastRunID != source.RunID || resumeInput.CheckpointID == nil && source.CheckpointID != nil ||
		resumeInput.CheckpointID != nil && (source.CheckpointID == nil || *resumeInput.CheckpointID != *source.CheckpointID) ||
		resumeInput.ExpectedControlRevision != 1 || resumeInput.Binding != nil || resumeInput.Limits != it.runtime.settings.Limits || resumeInput.IdempotencyKey == "" {
		t.Fatalf("ResumeInput did not bind known source, current control revision, nil Binding, fixed limits, and one key: %+v", resumeInput)
	}
	newActions, err := it.actions.ListActions(userContext, domainaction.Filter{TaskID: receipt.TaskID, RunID: resumeClaim.Run.RunID})
	if err != nil || len(newActions) != 1 {
		t.Fatalf("Resume Action=%+v err=%v", newActions, err)
	}
	newAttempts, err := it.actions.ListAttempts(userContext, domainaction.AttemptFilter{ActionID: newActions[0].ActionID})
	if err != nil || len(newAttempts) != 1 {
		t.Fatalf("Resume Attempt=%+v err=%v", newAttempts, err)
	}
	newAttempt := newAttempts[0]
	if newAttempt.StartReason != domainaction.AttemptStartReasonExplicitResume || newAttempt.NativeDelegation.Mode != domainaction.NativeDelegationModeResume ||
		newAttempt.NativeDelegation.ResumeSource == nil || newAttempt.NativeDelegation.ResumeSource.RunID != source.RunID ||
		newAttempt.NativeDelegation.Proof != nil || len(newAttempt.NativeDelegation.RunResult) != 0 || newAttempt.Status != domainaction.AttemptStatusRunning {
		t.Fatalf("before reconciliation the Action must keep only the typed Resume source and accepted IDs: %+v", newAttempt.NativeDelegation)
	}
	frozenPayload, err := protocol.Encode(resumeInput)
	if err != nil || !bytes.Equal(frozenPayload, newAttempt.NativeDelegation.Resume.Payload) {
		t.Fatalf("the exact SDK Resume bytes must remain frozen: bytes=%q stored=%q err=%v", frozenPayload, newAttempt.NativeDelegation.Resume.Payload, err)
	}
	replayedClaim, err := it.tasks.AdmitNativeOPSResume(resumeUserContext, taskmanager.NativeOPSResumeInput{
		TaskID: receipt.TaskID, ExpectedCoreRunID: initialClaim.Run.RunID, Source: source,
	})
	if err != nil || replayedClaim.MayExecute || replayedClaim.Status != taskmanager.NativeOPSResumeOutcomeUnknown ||
		replayedClaim.Run.RunID != resumeClaim.Run.RunID || replayedClaim.Claim != resumeClaim.Claim {
		t.Fatalf("same-request replay must recover only the saved claim: claim=%+v err=%v", replayedClaim, err)
	}
	for _, mode := range []string{"receipt_error", "receipt_mismatch", "run_mismatch", "active_child"} {
		capture.mu.Lock()
		capture.readMode = mode
		capture.mu.Unlock()
		unresolved, done, err := it.runtime.ReconcileNativeOPSResume(resumeContext, replayedClaim.Task, replayedClaim.Run, replayedClaim.Claim)
		if !errors.Is(err, agent.ErrNativeCodingOutcomeUnknown) || done || unresolved.Accepted() {
			t.Fatalf("known-ID %s must remain unresolved without Action adoption: result=%+v done=%v err=%v", mode, unresolved, done, err)
		}
		newAttempts, readErr := it.actions.ListAttempts(userContext, domainaction.AttemptFilter{ActionID: newActions[0].ActionID})
		if readErr != nil || len(newAttempts) != 1 || len(newAttempts[0].NativeDelegation.RunResult) != 0 || newAttempts[0].NativeDelegation.Proof != nil {
			t.Fatalf("failed known-ID %s read changed Action result/proof: attempts=%+v err=%v", mode, newAttempts, readErr)
		}
	}
	capture.mu.Lock()
	capture.readMode = ""
	capture.mu.Unlock()
	reconciled, done, err := it.runtime.ReconcileNativeOPSResume(resumeContext, replayedClaim.Task, replayedClaim.Run, replayedClaim.Claim)
	if err != nil || !done || !reconciled.Accepted() || reconciled.FinalText != "resumed and verified" {
		t.Fatalf("saved-ID reconciliation must persist the exact terminal result and fresh verifier proof: result=%+v done=%v err=%v", reconciled, done, err)
	}
	newAttempts, err = it.actions.ListAttempts(userContext, domainaction.AttemptFilter{ActionID: newActions[0].ActionID})
	if err != nil || len(newAttempts) != 1 || newAttempts[0].NativeDelegation.Proof == nil ||
		newAttempts[0].NativeDelegation.Proof.ExpectedCriteriaRevision != pinnedCriteria || len(newAttempts[0].NativeDelegation.RunResult) == 0 {
		t.Fatalf("reconciliation must save a proof under Task's frozen revision: attempts=%+v err=%v", newAttempts, err)
	}
	capture.mu.Lock()
	resumeCountAfterReconcile := len(capture.resumes)
	runReadsAfterReconcile, receiptReadsAfterReconcile := capture.runGets, capture.receiptGets
	capture.readMode = "receipt_error"
	capture.mu.Unlock()
	if resumeCountAfterReconcile != 1 {
		t.Fatalf("known-ID reconciliation resent RunResume: calls=%d", resumeCountAfterReconcile)
	}
	replayedResult, done, err := it.runtime.ReconcileNativeOPSResume(resumeContext, replayedClaim.Task, replayedClaim.Run, replayedClaim.Claim)
	capture.mu.Lock()
	runReadsAfterReplay, receiptReadsAfterReplay := capture.runGets, capture.receiptGets
	capture.mu.Unlock()
	if err != nil || !done || !replayedResult.Accepted() || runReadsAfterReplay != runReadsAfterReconcile || receiptReadsAfterReplay != receiptReadsAfterReconcile {
		t.Fatalf("valid immutable terminal proof replay must not reread or reprove Harness state: result=%+v done=%v err=%v reads before=(%d,%d) after=(%d,%d)",
			replayedResult, done, err, runReadsAfterReconcile, receiptReadsAfterReconcile, runReadsAfterReplay, receiptReadsAfterReplay)
	}
	unchangedSourceActions, err := it.actions.ListActions(userContext, domainaction.Filter{TaskID: receipt.TaskID, RunID: initialClaim.Run.RunID})
	if err != nil || len(unchangedSourceActions) != 1 || unchangedSourceActions[0].ActionID != oldActions[0].ActionID {
		t.Fatalf("Resume changed source Action history: %+v err=%v", unchangedSourceActions, err)
	}
	unchangedSourceAttempts, err := it.actions.ListAttempts(userContext, domainaction.AttemptFilter{ActionID: oldActions[0].ActionID})
	if err != nil || len(unchangedSourceAttempts) != 1 || !bytes.Equal(unchangedSourceAttempts[0].NativeDelegation.RunResult, originalSourceResult) ||
		unchangedSourceAttempts[0].NativeDelegation.Proof != nil || !reflect.DeepEqual(unchangedSourceAttempts[0].NativeDelegation.InputReference, originalInputReference) {
		t.Fatalf("Resume must leave the old Action result, proof, and accepted input reference unchanged: %+v err=%v", unchangedSourceAttempts, err)
	}
	completed, err := it.tasks.CompleteNativeOPSResumeRun(resumeContext, receipt.TaskID, resumeClaim.Run.RunID, shiroActorID,
		resumeClaim.Claim, domaintask.StatusSucceeded, "native OPS Resume completed and verification passed", "")
	if err != nil || completed.Status != domaintask.StatusSucceeded || completed.ExpectedCriteriaRevision != pinnedCriteria ||
		len(completed.NativeResumeClaims) != 1 || completed.AcceptedOPSClaim == nil || *completed.AcceptedOPSClaim != *closedTask.AcceptedOPSClaim {
		t.Fatalf("same-Task result finalization must preserve accepted owner state and criteria pin: task=%+v err=%v", completed, err)
	}
}

func newResumeAcceptedOPSContext(
	t *testing.T,
	it *integration,
	requestID, ownerID, rawMessage string,
) (context.Context, conversation.AcceptedOPSInputReceipt, taskmanager.AcceptedOPSClaimResult, conversation.AcceptedOPSInput) {
	t.Helper()
	request := conversation.AcceptedOPSInputRequest{
		RequestID: requestID, OwnerID: ownerID, ActorID: ownerID, SessionID: modulecore.NewSessionID(),
		FirstThreadID: modulecore.NewThreadID(), TaskID: modulecore.NewTaskID(), TurnID: modulecore.NewTurnID(),
		TraceID: modulecore.NewTraceID(), UserMessageID: modulecore.NewMessageID(), AgentMessageID: modulecore.NewMessageID(),
		RawMessage: rawMessage, DeclaredOrigin: conversation.AcceptedOPSInputOriginAutomation,
	}
	payloadSHA256, err := conversation.AcceptedOPSInputPayloadSHA256(request)
	if err != nil {
		t.Fatal(err)
	}
	rawHash := sha256.Sum256([]byte(rawMessage))
	receipt := conversation.AcceptedOPSInputReceipt{
		AcceptanceSequence: 1, RequestID: requestID, OwnerID: ownerID, ActorID: ownerID,
		SessionID: request.SessionID, ThreadID: request.FirstThreadID, ThreadSeq: modulecore.ThreadSeq(1),
		ThreadKind: modulecore.ThreadKindUserConversation, TaskID: request.TaskID, TurnID: request.TurnID, TraceID: request.TraceID,
		UserMessageID: request.UserMessageID, AgentMessageID: request.AgentMessageID,
		DeclaredOrigin: request.DeclaredOrigin, PayloadSHA256: payloadSHA256, RawRecordID: "raw-resume-source",
		ManifestID: "manifest-resume-source", RawSHA256: hex.EncodeToString(rawHash[:]),
		ManifestSHA256: strings.Repeat("c", 64), AcceptedAt: time.Now().UTC(),
	}
	userScope, err := tool.NewToolExecutionScope(requestID, tool.ActorKindUser, ownerID, ownerID,
		[]string{tool.DataScopePublic, tool.DataScopeUser}, tool.AuthenticationSourceHTTP)
	if err != nil {
		t.Fatal(err)
	}
	userContext := tool.WithToolExecutionScope(context.Background(), userScope)
	acceptedClaim, err := it.tasks.AdmitAcceptedOPS(userContext, receipt, conversation.BackendShiroNativeCodingV1)
	if err != nil || !acceptedClaim.MayExecute || acceptedClaim.Status != taskmanager.AcceptedOPSClaimed {
		t.Fatalf("create the accepted OPS Task: claim=%+v err=%v", acceptedClaim, err)
	}
	return userContext, receipt, acceptedClaim, conversation.AcceptedOPSInput{Receipt: receipt, RawMessage: rawMessage}
}

func mustIntegrationChannel(t *testing.T) conversation.ChannelAddress {
	t.Helper()
	address, err := conversation.NewChannelAddress("line", "U-resume-test")
	if err != nil {
		t.Fatal(err)
	}
	return address
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
	ctx, input, messages := it.newTurn("テストを直して")

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
	ctx, input, messages := it.newTurn("時間のかかる作業")
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
	ctx, input, _ := it.newTurn("x")
	err := it.runtime.AdmitNativeCoding(ctx, input)
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
	ctx, input, _ := it.newTurn("x")
	err := it.runtime.AdmitNativeCoding(ctx, input)
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
	orch.SetTaskLifecycleManager(it.tasks)
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
	rootTaskID := modulecore.NewTaskID()

	_, err := orch.ProcessMessage(context.Background(), orchestrator.ProcessMessageRequest{
		RootTaskID: rootTaskID.String(),
		SessionID:  "20261008-line-U1", Channel: "line", ChatID: "U1", UserMessage: "この場面を描画して",
	})
	var nativeErr *agent.NativeCodingError
	if !errors.As(err, &nativeErr) {
		t.Fatalf("completed but unverified Run must return its typed native coding failure: %v", err)
	}
	if nativeErr.Result.Status != agent.NativeRunCompleted || nativeErr.Result.Verification != agent.NativeVerificationNotRun || nativeErr.Result.FinalText != "描画の作業を完了しました" {
		t.Fatalf("the typed error must preserve the actual completed/not_run RunResult: %+v", nativeErr.Result)
	}
	if legacy.llm.Load()+legacy.tools.Load()+legacy.advisor.Load()+legacy.subagent.Load() != 0 {
		t.Fatalf("an old route was entered: llm=%d tools=%d advisor=%d subagent=%d", legacy.llm.Load(), legacy.tools.Load(), legacy.advisor.Load(), legacy.subagent.Load())
	}
	if got := len(it.deploy.Gateway.Generations()); got != 1 {
		t.Fatalf("exactly one Run reached the model, got %d", got)
	}
	delegations := it.delegationActions()
	if len(delegations) != 1 || delegations[0].Status != domainaction.StatusFailed {
		t.Fatalf("the non-accepted delegation must retain one failed Action: %+v", delegations)
	}
	executionTask, err := it.tasks.Get(context.Background(), delegations[0].TaskID)
	if err != nil || executionTask.Route != domaintask.RouteOperations || executionTask.Assignee != shiroActorID || executionTask.Status != domaintask.StatusFailed {
		t.Fatalf("the genuine Shiro operations Task must fail after the unverified Run: task=%+v err=%v", executionTask, err)
	}
	rootTask, err := it.tasks.Get(context.Background(), rootTaskID)
	if err != nil || rootTask.Status != domaintask.StatusFailed {
		t.Fatalf("the parent Task must fail after the non-accepted Run: task=%+v err=%v", rootTask, err)
	}
	attempts, err := it.actions.ListAttempts(context.Background(), domainaction.AttemptFilter{ActionID: delegations[0].ActionID})
	if err != nil || len(attempts) != 1 || attempts[0].Status != domainaction.AttemptStatusFailed {
		t.Fatalf("the parent Task failure must retain the failed Attempt: %+v err=%v", attempts, err)
	}
	var storedRun protocol.RunResult
	if err := json.Unmarshal(attempts[0].NativeDelegation.RunResult, &storedRun); err != nil || storedRun.Status != string(agent.NativeRunCompleted) || storedRun.Verification.Status != string(agent.NativeVerificationNotRun) {
		t.Fatalf("the Action must keep the real completed/not_run RunResult: %+v err=%v", storedRun, err)
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
	ctx, input, messages := it.newTurn("同じ課題")

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
