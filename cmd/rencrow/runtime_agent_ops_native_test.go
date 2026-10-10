package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/nativeharnessclient"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type agentOpsAcceptedOPSInputStoreStub struct {
	mu                  sync.Mutex
	userID              string
	inputs              map[string]conversation.AcceptedOPSInput
	accepts             int
	reads               int
	acceptErr           error
	readErr             error
	acceptCtxs          []context.Context
	readCtxs            []context.Context
	mutateAcceptReceipt func(conversation.AcceptedOPSInputReceipt) conversation.AcceptedOPSInputReceipt
	mutateReadInput     func(conversation.AcceptedOPSInput) conversation.AcceptedOPSInput
}

type agentOpsNativeCountingBlockingExecutor struct {
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

type agentOpsNativeResumeRuntimeStub struct {
	*agentOpsNativeCodingAdmissionStub
	source         domaintask.NativeOPSResumeSource
	result         agent.NativeCodingResult
	prepareErr     error
	executeErr     error
	reconcileErr   error
	prepareCalls   int
	executeCalls   int
	reconcileCalls int
	lastTask       domaintask.Task
	lastRun        domaintask.Run
	lastClaim      domaintask.NativeOPSResumeClaim
	lastScope      domaintool.ToolExecutionScope
}

func (s *agentOpsNativeResumeRuntimeStub) PrepareNativeOPSResumeSource(_ context.Context, _ domaintask.Task, _ modulecore.RunID) (domaintask.NativeOPSResumeSource, error) {
	s.prepareCalls++
	return s.source, s.prepareErr
}

func (s *agentOpsNativeResumeRuntimeStub) ExecuteNativeOPSResume(ctx context.Context, task domaintask.Task, run domaintask.Run, claim domaintask.NativeOPSResumeClaim) (agent.NativeCodingResult, error) {
	s.executeCalls++
	s.lastTask, s.lastRun, s.lastClaim = task, run, claim
	s.lastScope, _ = domaintool.ToolExecutionScopeFromContext(ctx)
	return s.result, s.executeErr
}

func (s *agentOpsNativeResumeRuntimeStub) ReconcileNativeOPSResume(ctx context.Context, task domaintask.Task, run domaintask.Run, claim domaintask.NativeOPSResumeClaim) (agent.NativeCodingResult, bool, error) {
	s.reconcileCalls++
	s.lastTask, s.lastRun, s.lastClaim = task, run, claim
	s.lastScope, _ = domaintool.ToolExecutionScopeFromContext(ctx)
	return s.result, s.reconcileErr == nil || errors.Is(s.reconcileErr, agent.ErrNativeCodingRejected), s.reconcileErr
}

func (e *agentOpsNativeCountingBlockingExecutor) Execute(context.Context, conversation.TurnInput) (string, error) {
	e.calls.Add(1)
	e.entered <- struct{}{}
	<-e.release
	return "verified output", nil
}

func newAgentOpsAcceptedOPSInputStoreStub(userID string) *agentOpsAcceptedOPSInputStoreStub {
	return &agentOpsAcceptedOPSInputStoreStub{userID: userID, inputs: make(map[string]conversation.AcceptedOPSInput)}
}

func (s *agentOpsAcceptedOPSInputStoreStub) AcceptOPSInput(ctx context.Context, request conversation.AcceptedOPSInputRequest) (conversation.AcceptedOPSInputReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accepts++
	s.acceptCtxs = append(s.acceptCtxs, ctx)
	if err := s.validateUserContext(ctx, request.RequestID, request.OwnerID); err != nil {
		return conversation.AcceptedOPSInputReceipt{}, err
	}
	if s.acceptErr != nil {
		return conversation.AcceptedOPSInputReceipt{}, s.acceptErr
	}
	key := request.OwnerID + "/" + request.RequestID
	if existing, ok := s.inputs[key]; ok {
		payload, err := conversation.AcceptedOPSInputPayloadSHA256(request)
		if err != nil {
			return conversation.AcceptedOPSInputReceipt{}, err
		}
		if payload != existing.Receipt.PayloadSHA256 {
			return conversation.AcceptedOPSInputReceipt{}, conversation.ErrAcceptedOPSInputConflict
		}
		receipt := existing.Receipt
		receipt.IdempotentReplay = true
		return receipt, nil
	}
	normalized, err := conversation.NormalizeAcceptedOPSInputRequest(request)
	if err != nil {
		return conversation.AcceptedOPSInputReceipt{}, err
	}
	payloadHash, err := conversation.AcceptedOPSInputPayloadSHA256(normalized)
	if err != nil {
		return conversation.AcceptedOPSInputReceipt{}, err
	}
	rawHash := sha256.Sum256([]byte(normalized.RawMessage))
	sequence := int64(len(s.inputs) + 1)
	receipt := conversation.AcceptedOPSInputReceipt{
		AcceptanceSequence: sequence,
		RequestID:          normalized.RequestID,
		OwnerID:            normalized.OwnerID,
		ActorID:            normalized.ActorID,
		SessionID:          normalized.SessionID,
		ThreadID:           normalized.FirstThreadID,
		ThreadSeq:          1,
		ThreadKind:         modulecore.ThreadKindUserConversation,
		TaskID:             normalized.TaskID,
		TurnID:             normalized.TurnID,
		TraceID:            normalized.TraceID,
		UserMessageID:      normalized.UserMessageID,
		AgentMessageID:     normalized.AgentMessageID,
		DeclaredOrigin:     normalized.DeclaredOrigin,
		PayloadSHA256:      payloadHash,
		RawRecordID:        "raw-record-" + normalized.RequestID,
		ManifestID:         "manifest-" + normalized.RequestID,
		RawSHA256:          hex.EncodeToString(rawHash[:]),
		ManifestSHA256:     strings.Repeat("a", 64),
		AcceptedAt:         time.Now().UTC(),
	}
	s.inputs[key] = conversation.AcceptedOPSInput{Receipt: receipt, RawMessage: normalized.RawMessage}
	if s.mutateAcceptReceipt != nil {
		receipt = s.mutateAcceptReceipt(receipt)
	}
	return receipt, nil
}

func (s *agentOpsAcceptedOPSInputStoreStub) ReadAcceptedOPSInput(ctx context.Context, request conversation.AcceptedOPSInputReadRequest) (conversation.AcceptedOPSInput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	s.readCtxs = append(s.readCtxs, ctx)
	if err := s.validateUserContext(ctx, request.RequestID, request.OwnerID); err != nil {
		return conversation.AcceptedOPSInput{}, err
	}
	if s.readErr != nil {
		return conversation.AcceptedOPSInput{}, s.readErr
	}
	input, ok := s.inputs[request.OwnerID+"/"+request.RequestID]
	if !ok {
		return conversation.AcceptedOPSInput{}, conversation.ErrAcceptedOPSInputInvalid
	}
	if s.mutateReadInput != nil {
		input = s.mutateReadInput(input)
	}
	return input, nil
}

func (s *agentOpsAcceptedOPSInputStoreStub) validateUserContext(ctx context.Context, requestID, ownerID string) error {
	scope, ok := domaintool.ToolExecutionScopeFromContext(ctx)
	if !ok || scope.Validate() != nil || scope.ActorKind != domaintool.ActorKindUser || scope.ActorID != ownerID || scope.AuthenticatedUserID != s.userID || scope.RequestID != requestID || scope.AuthenticationSource != domaintool.AuthenticationSourceHTTP {
		return conversation.ErrAcceptedOPSInputForbidden
	}
	return nil
}

func (s *agentOpsAcceptedOPSInputStoreStub) counts() (accepts, reads int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.accepts, s.reads
}

func (s *agentOpsAcceptedOPSInputStoreStub) snapshot(requestID string) (conversation.AcceptedOPSInput, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	input, ok := s.inputs[s.userID+"/"+requestID]
	return input, ok
}

func TestAgentOpsNativeCanonicalReceiptBindsRawIDsUserScopeAndOriginCapability(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	message := "  Unicode é — 日本語\n "
	store := newAgentOpsAcceptedOPSInputStoreStub("ren")
	executor := &agentOpsExecutorStub{output: "verified output"}
	admission := &agentOpsNativeCodingAdmissionStub{}
	handler := newAgentOpsTestHandlerWithStore(t, token, executor, admission, store)
	rec := serveNativeOPSRequest(t, handler, token, "req-native-canonical", message)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	var response agentOpsNativeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	accepted, ok := store.snapshot("req-native-canonical")
	if !ok || accepted.RawMessage != message {
		t.Fatalf("accepted input=%#v found=%t want raw=%q", accepted, ok, message)
	}
	if response.TaskID != accepted.Receipt.TaskID.String() || response.RunID == "" || response.TaskStatus != string(domaintask.StatusSucceeded) || response.RunStatus != string(domaintask.RunStatusSucceeded) || response.Output != "verified output" {
		t.Fatalf("response=%+v receipt=%+v", response, accepted.Receipt)
	}
	if admission.calls != 1 || executor.calls != 1 || admission.inputs[0].RootTaskID() != accepted.Receipt.TaskID || admission.inputs[0].TraceID() != accepted.Receipt.TraceID ||
		admission.inputs[0].TurnID() != accepted.Receipt.TurnID || admission.inputs[0].UserMessageID() != accepted.Receipt.UserMessageID || admission.inputs[0].AgentMessageID() != accepted.Receipt.AgentMessageID ||
		admission.inputs[0].SessionID() != string(accepted.Receipt.SessionID) || admission.inputs[0].MessageText() != message {
		t.Fatalf("admission calls=%d input=%#v receipt=%+v", admission.calls, admission.inputs, accepted.Receipt)
	}
	identity, err := domainexecution.IdentityFromContext(executor.ctx)
	if err != nil || identity.TaskID != accepted.Receipt.TaskID || string(identity.RunID) != response.RunID || identity.TraceID != accepted.Receipt.TraceID {
		t.Fatalf("execution identity=%+v err=%v response=%+v receipt=%+v", identity, err, response, accepted.Receipt)
	}
	for _, ctx := range []context.Context{store.acceptCtxs[0], store.readCtxs[0]} {
		scope, ok := domaintool.ToolExecutionScopeFromContext(ctx)
		if !ok || scope.ActorKind != domaintool.ActorKindUser || scope.ActorID != "ren" || scope.AuthenticatedUserID != "ren" || scope.RequestID != "req-native-canonical" {
			t.Fatalf("owner scope=%+v found=%t", scope, ok)
		}
	}
	if admission.ctxs[0] == store.acceptCtxs[0] {
		t.Fatal("native admission reused user parent instead of derived Shiro scope")
	}
	shiroScope, ok := domaintool.ToolExecutionScopeFromContext(admission.ctxs[0])
	if !ok || shiroScope.ActorID != "shiro" || shiroScope.AuthenticatedUserID != "ren" || shiroScope.Purpose != "ops" {
		t.Fatalf("admission scope=%+v found=%t", shiroScope, ok)
	}
	if accepted.Receipt.DeclaredOrigin != conversation.AcceptedOPSInputOriginAutomation {
		t.Fatalf("absent input-origin header declared %q, want Automation", accepted.Receipt.DeclaredOrigin)
	}

	if _, err := newAgentOpsAcceptedInputReader(executor.ctx, store, accepted); !errors.Is(err, conversation.ErrAcceptedOPSInputForbidden) {
		t.Fatalf("constructing the owner reader from a Shiro execution context error=%v, want forbidden", err)
	}
	reader, ok := nativeharnessclient.AcceptedInputReaderFromContext(executor.ctx)
	if !ok {
		t.Fatal("native Shiro execution context lost its request-scoped accepted-input reader")
	}
	ownerRead, err := reader.ReadAcceptedInput(executor.ctx)
	if err != nil || ownerRead.RawMessage != message || ownerRead.Receipt != accepted.Receipt {
		t.Fatalf("bound owner reread=%+v err=%v", ownerRead, err)
	}
	if _, err := reader.ReadAcceptedInput(executor.ctx); !errors.Is(err, conversation.ErrAcceptedOPSInputUnavailable) {
		t.Fatalf("one-request reader second read error=%v, want unavailable", err)
	}
	ownerReadContext := store.readCtxs[len(store.readCtxs)-1]
	ownerReadScope, ok := domaintool.ToolExecutionScopeFromContext(ownerReadContext)
	if !ok || ownerReadScope.ActorKind != domaintool.ActorKindUser || ownerReadScope.ActorID != "ren" || ownerReadScope.AuthenticationSource != domaintool.AuthenticationSourceHTTP {
		t.Fatalf("bound reread did not use the captured authenticated user parent: scope=%+v found=%t", ownerReadScope, ok)
	}
	if _, ok := ownerReadContext.Deadline(); !ok {
		t.Fatal("bound owner reread must have a finite deadline")
	}
}

func TestAgentOpsNativeResumeAdmitsSameTaskAndReplaysOnlyThroughReconciliation(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	taskOwner := newAgentOpsResumeTaskOwner(t)
	store := newAgentOpsAcceptedOPSInputStoreStub("ren")
	originalReceipt, originalRun := createFailedAcceptedAgentOPSTask(t, taskOwner, store)
	original, found := store.snapshot(string(originalReceipt.RequestID))
	if !found {
		t.Fatal("original accepted OPS receipt was not saved")
	}

	source := domaintask.NativeOPSResumeSource{
		ActionID: modulecore.NewActionID(), AttemptID: modulecore.NewAttemptID(),
		HarnessTaskID: "harness-task-known", ThreadID: "harness-thread-known", RunID: "harness-run-known", ReceiptID: "harness-receipt-known",
		CheckpointID: stringPointer("harness-checkpoint-known"), RunResultSHA256: strings.Repeat("b", 64),
	}
	resumeRuntime := &agentOpsNativeResumeRuntimeStub{
		agentOpsNativeCodingAdmissionStub: &agentOpsNativeCodingAdmissionStub{}, source: source,
		result: agent.NativeCodingResult{Status: agent.NativeRunCompleted, Verification: agent.NativeVerificationPassed, FinalText: "verified resume"},
	}
	handler := newAgentOpsTestHandlerWithTaskOwnerAndNativeAdmission(t, token, &agentOpsExecutorStub{output: "unused"}, taskOwner, resumeRuntime)
	requestID := "req-native-resume-http"
	body := `{"operation":"native_resume","target":{"task_id":"` + originalReceipt.TaskID.String() + `","expected_core_run_id":"` + string(originalRun.RunID) + `"}}`
	serve := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/agent/ops", strings.NewReader(body))
		setAgentOpsHeaders(req, token, requestID)
		req.RemoteAddr = "127.0.0.1:18791"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	first := serve()
	if first.Code != http.StatusOK {
		t.Fatalf("first Resume status=%d body=%q", first.Code, first.Body.String())
	}
	var response agentOpsNativeResponse
	if err := json.Unmarshal(first.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.RequestID != requestID || response.TaskID != originalReceipt.TaskID.String() || response.RunID == string(originalRun.RunID) ||
		response.TaskStatus != string(domaintask.StatusSucceeded) || response.RunStatus != string(domaintask.RunStatusSucceeded) || response.Output != "verified resume" {
		t.Fatalf("first Resume response=%+v", response)
	}
	if resumeRuntime.prepareCalls != 1 || resumeRuntime.executeCalls != 1 || resumeRuntime.reconcileCalls != 0 ||
		resumeRuntime.lastClaim.ExpectedCoreRunID != originalRun.RunID || !reflect.DeepEqual(resumeRuntime.lastClaim.Source, source) ||
		resumeRuntime.lastRun.RunID != resumeRuntime.lastClaim.NewCoreRunID {
		t.Fatalf("Resume runtime calls or claim=%+v", resumeRuntime)
	}
	if resumeRuntime.lastScope.ActorID != "shiro" || resumeRuntime.lastScope.AuthenticatedUserID != "ren" ||
		resumeRuntime.lastScope.RequestID != requestID || resumeRuntime.lastScope.Purpose != "ops" {
		t.Fatalf("Resume did not use a fresh Shiro scope derived from the configured user: %+v", resumeRuntime.lastScope)
	}

	second := serve()
	if second.Code != http.StatusOK {
		t.Fatalf("idempotent Resume replay status=%d body=%q", second.Code, second.Body.String())
	}
	if resumeRuntime.prepareCalls != 1 || resumeRuntime.executeCalls != 1 || resumeRuntime.reconcileCalls != 1 {
		t.Fatalf("same-request replay repeated Resume execution: prepare=%d execute=%d reconcile=%d", resumeRuntime.prepareCalls, resumeRuntime.executeCalls, resumeRuntime.reconcileCalls)
	}
	unchanged, found := store.snapshot(string(originalReceipt.RequestID))
	if !found || unchanged.Receipt != original.Receipt || unchanged.RawMessage != original.RawMessage {
		t.Fatalf("Resume changed original accepted OPS receipt/raw input: before=%+v after=%+v", original, unchanged)
	}
}

func TestAgentOpsNativeResumeDefiniteReplayFailureStillCompletesTask(t *testing.T) {
	owner := newAgentOpsResumeTaskOwner(t)
	ctx, task, run, claim := admitNativeOPSResumeForFinalizationTest(t, owner)
	runtime := &agentOpsNativeResumeRuntimeStub{
		agentOpsNativeCodingAdmissionStub: &agentOpsNativeCodingAdmissionStub{},
		reconcileErr:                      agent.ErrNativeCodingRejected,
	}
	const token = "0123456789abcdef0123456789abcdef"
	handler := newAgentOpsTestHandlerWithTaskOwnerAndNativeAdmission(t, token, &agentOpsExecutorStub{output: "unused"}, owner, runtime)
	body := `{"operation":"native_resume","target":{"task_id":"` + task.TaskID.String() + `","expected_core_run_id":"` + string(claim.ExpectedCoreRunID) + `"}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/agent/ops", strings.NewReader(body))
	setAgentOpsHeaders(req, token, claim.RequestID)
	req.RemoteAddr = "127.0.0.1:18792"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("known persisted Resume refusal response status=%d body=%q", rec.Code, rec.Body.String())
	}
	if runtime.prepareCalls != 0 || runtime.executeCalls != 0 || runtime.reconcileCalls != 1 ||
		runtime.lastClaim != claim || runtime.lastRun.RunID != run.RunID {
		t.Fatalf("refusal replay must reconcile saved IDs only: runtime=%+v", runtime)
	}
	storedTask, err := owner.Get(ctx, task.TaskID)
	if err != nil || storedTask.Status != domaintask.StatusFailed {
		t.Fatalf("known saved refusal did not finalize the canonical Task: task=%+v err=%v", storedTask, err)
	}
	storedRun, err := owner.GetRun(ctx, run.RunID)
	if err != nil || storedRun.Status != domaintask.RunStatusFailed || storedRun.WriterGeneration != claim.WriterGeneration {
		t.Fatalf("known saved refusal did not finalize the exact Resume Run: run=%+v err=%v", storedRun, err)
	}
}

func TestFinishNativeOPSResumeIsIdempotentAfterTaskCompletion(t *testing.T) {
	owner := newAgentOpsResumeTaskOwner(t)
	ctx, task, run, claim := admitNativeOPSResumeForFinalizationTest(t, owner)
	handler := &agentOpsHandler{taskOwner: owner}
	completed, storedRun, err := handler.finishNativeOPSResume(ctx, task, run, claim, domaintask.StatusSucceeded, "verified Resume")
	if err != nil || completed.Status != domaintask.StatusSucceeded || storedRun.Status != domaintask.RunStatusSucceeded {
		t.Fatalf("current-generation Resume finalization: task=%+v run=%+v err=%v", completed, storedRun, err)
	}
	replayed, replayedRun, err := handler.finishNativeOPSResume(ctx, task, run, claim, domaintask.StatusSucceeded, "verified Resume")
	if err != nil || replayed.Status != domaintask.StatusSucceeded || replayedRun.RunID != claim.NewCoreRunID || replayedRun.WriterGeneration != claim.WriterGeneration {
		t.Fatalf("replay after Task persistence must verify the same exact terminal Run: task=%+v run=%+v err=%v", replayed, replayedRun, err)
	}
}

func TestFinishNativeOPSResumeRecoversOnlyTheExactStaleWriterGeneration(t *testing.T) {
	root := filepath.Join(t.TempDir(), "resume-task-store")
	firstStore, err := taskpersistence.NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	firstStoreClosed := false
	t.Cleanup(func() {
		if !firstStoreClosed {
			_ = firstStore.Close()
		}
	})
	firstOwner, err := taskmanager.NewWithExpectedCriteriaRevision(firstStore, taskmanager.DefaultParallelLimits(), strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	ctx, task, run, claim := admitNativeOPSResumeForFinalizationTest(t, firstOwner)
	if err := firstStore.Close(); err != nil {
		t.Fatal(err)
	}
	firstStoreClosed = true

	secondStore, err := taskpersistence.NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := secondStore.Close(); err != nil {
			t.Errorf("close restarted Resume Task store: %v", err)
		}
	})
	secondOwner, err := taskmanager.NewWithExpectedCriteriaRevision(secondStore, taskmanager.DefaultParallelLimits(), strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	handler := &agentOpsHandler{taskOwner: secondOwner}
	completed, storedRun, err := handler.finishNativeOPSResume(ctx, task, run, claim, domaintask.StatusSucceeded, "verified Resume")
	if err != nil || completed.Status != domaintask.StatusSucceeded || storedRun.Status != domaintask.RunStatusSucceeded ||
		storedRun.WriterGeneration != claim.WriterGeneration {
		t.Fatalf("restart recovery must close the exact stale Resume Run generation: task=%+v run=%+v claim=%+v err=%v", completed, storedRun, claim, err)
	}
}

func admitNativeOPSResumeForFinalizationTest(
	t *testing.T,
	owner *taskmanager.Manager,
) (context.Context, domaintask.Task, domaintask.Run, domaintask.NativeOPSResumeClaim) {
	t.Helper()
	store := newAgentOpsAcceptedOPSInputStoreStub("ren")
	receipt, sourceRun := createFailedAcceptedAgentOPSTask(t, owner, store)
	requestID := "req-native-resume-finalize"
	userScope, err := domaintool.NewToolExecutionScope(requestID, domaintool.ActorKindUser, "ren", "ren",
		[]string{domaintool.DataScopePublic, domaintool.DataScopeUser}, domaintool.AuthenticationSourceHTTP)
	if err != nil {
		t.Fatal(err)
	}
	userContext := domaintool.WithToolExecutionScope(context.Background(), userScope)
	source := domaintask.NativeOPSResumeSource{
		ActionID: modulecore.NewActionID(), AttemptID: modulecore.NewAttemptID(), HarnessTaskID: "harness-task-finalize",
		ThreadID: "harness-thread-finalize", RunID: "harness-run-finalize", ReceiptID: "harness-receipt-finalize",
		RunResultSHA256: strings.Repeat("b", 64),
	}
	admitted, err := owner.AdmitNativeOPSResume(userContext, taskmanager.NativeOPSResumeInput{
		TaskID: receipt.TaskID, ExpectedCoreRunID: sourceRun.RunID, Source: source,
	})
	if err != nil || !admitted.MayExecute || admitted.Claim.Validate(admitted.Task) != nil {
		t.Fatalf("admit Task-owned Resume finalization fixture: claim=%+v err=%v", admitted, err)
	}
	workerContext, err := deriveAgentOpsShiroContext(userContext, requestID)
	if err != nil {
		t.Fatal(err)
	}
	executionContext, err := domainexecution.WithIdentity(workerContext, admitted.Task.TaskID, admitted.Run.RunID, admitted.Run.TraceID)
	if err != nil {
		t.Fatal(err)
	}
	return executionContext, admitted.Task, admitted.Run, admitted.Claim
}

func createFailedAcceptedAgentOPSTask(t *testing.T, owner *taskmanager.Manager, store *agentOpsAcceptedOPSInputStoreStub) (conversation.AcceptedOPSInputReceipt, domaintask.Run) {
	t.Helper()
	requestID := "req-original-accepted-ops"
	scope, err := domaintool.NewToolExecutionScope(requestID, domaintool.ActorKindUser, "ren", "ren",
		[]string{domaintool.DataScopePublic, domaintool.DataScopeUser}, domaintool.AuthenticationSourceHTTP)
	if err != nil {
		t.Fatal(err)
	}
	ctx := domaintool.WithToolExecutionScope(context.Background(), scope)
	receipt, err := store.AcceptOPSInput(ctx, conversation.AcceptedOPSInputRequest{
		RequestID: requestID, OwnerID: "ren", ActorID: "ren", SessionID: modulecore.NewSessionID(), FirstThreadID: modulecore.NewThreadID(),
		TaskID: modulecore.NewTaskID(), TurnID: modulecore.NewTurnID(), TraceID: modulecore.NewTraceID(),
		UserMessageID: modulecore.NewMessageID(), AgentMessageID: modulecore.NewMessageID(), RawMessage: "original accepted user input",
		DeclaredOrigin: conversation.AcceptedOPSInputOriginAutomation,
	})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := owner.AdmitAcceptedOPS(ctx, receipt, conversation.BackendShiroNativeCodingV1)
	if err != nil || accepted.Status != taskmanager.AcceptedOPSClaimed {
		t.Fatalf("accepted OPS claim=%+v err=%v", accepted, err)
	}
	if _, err := owner.CompleteRun(ctx, receipt.TaskID, accepted.Run.RunID, domaintask.AcceptedOPSAgentAssignee, domaintask.StatusFailed, "source run failed", ""); err != nil {
		t.Fatalf("close source OPS Run: %v", err)
	}
	run, err := owner.GetRun(ctx, accepted.Run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	return receipt, run
}

func stringPointer(value string) *string { return &value }

func newAgentOpsResumeTaskOwner(t *testing.T) *taskmanager.Manager {
	t.Helper()
	store, err := taskpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "resume-tasks"))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := taskmanager.NewWithExpectedCriteriaRevision(store, taskmanager.DefaultParallelLimits(), strings.Repeat("a", 64))
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close Resume Task store: %v", err)
		}
	})
	return manager
}

func TestAgentOpsAcceptedInputReaderCancellationUsesOriginalUserParent(t *testing.T) {
	const requestID = "req-native-reader-cancel"
	userScope, err := domaintool.NewToolExecutionScope(
		requestID, domaintool.ActorKindUser, "ren", "ren",
		[]string{domaintool.DataScopePublic, domaintool.DataScopeUser}, domaintool.AuthenticationSourceHTTP,
	)
	if err != nil {
		t.Fatal(err)
	}
	userParent := domaintool.WithToolExecutionScope(context.Background(), userScope)
	taskID, turnID, traceID := modulecore.NewTaskID(), modulecore.NewTurnID(), modulecore.NewTraceID()
	request := conversation.AcceptedOPSInputRequest{
		RequestID: requestID, OwnerID: "ren", ActorID: "ren", SessionID: modulecore.NewSessionID(),
		FirstThreadID: modulecore.NewThreadID(), TaskID: taskID, TurnID: turnID, TraceID: traceID,
		UserMessageID: modulecore.NewMessageID(), AgentMessageID: modulecore.NewMessageID(),
		RawMessage: "read cancellation source", DeclaredOrigin: conversation.AcceptedOPSInputOriginHuman,
	}
	base := newAgentOpsAcceptedOPSInputStoreStub("ren")
	if _, err := base.AcceptOPSInput(userParent, request); err != nil {
		t.Fatal(err)
	}
	accepted, err := base.ReadAcceptedOPSInput(userParent, conversation.AcceptedOPSInputReadRequest{RequestID: requestID, OwnerID: "ren"})
	if err != nil {
		t.Fatal(err)
	}
	store := &agentOpsBlockingAcceptedOPSInputStore{agentOpsAcceptedOPSInputStoreStub: base, readContexts: make(chan context.Context, 1)}
	reader, err := newAgentOpsAcceptedInputReader(userParent, store, accepted)
	if err != nil {
		t.Fatal(err)
	}
	shiroContext, err := deriveAgentOpsShiroContext(nativeharnessclient.WithAcceptedInputReader(userParent, reader), requestID)
	if err != nil {
		t.Fatal(err)
	}
	executionContext, err := domainexecution.WithIdentity(shiroContext, taskID, modulecore.NewRunID(), traceID)
	if err != nil {
		t.Fatal(err)
	}
	canceledContext, cancel := context.WithCancel(executionContext)
	defer cancel()
	readDone := make(chan error, 1)
	go func() {
		_, readErr := reader.ReadAcceptedInput(canceledContext)
		readDone <- readErr
	}()
	var ownerContext context.Context
	select {
	case ownerContext = <-store.readContexts:
	case <-time.After(time.Second):
		t.Fatal("bound owner read did not begin")
	}
	cancel()
	select {
	case err := <-readDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("bound owner read cancellation error=%v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Shiro cancellation did not stop the bounded user-parent read")
	}
	if userParent.Err() != nil {
		t.Fatalf("read cancellation altered the original user parent: %v", userParent.Err())
	}
	ownerScope, ok := domaintool.ToolExecutionScopeFromContext(ownerContext)
	if !ok || ownerScope.ActorKind != domaintool.ActorKindUser || ownerScope.ActorID != "ren" || ownerScope.AuthenticationSource != domaintool.AuthenticationSourceHTTP {
		t.Fatalf("cancelled read used a relabelled context: scope=%+v found=%t", ownerScope, ok)
	}
	if _, ok := ownerContext.Deadline(); !ok {
		t.Fatal("cancelled owner read had no finite deadline")
	}
}

type agentOpsBlockingAcceptedOPSInputStore struct {
	*agentOpsAcceptedOPSInputStoreStub
	readContexts chan context.Context
}

func (s *agentOpsBlockingAcceptedOPSInputStore) ReadAcceptedOPSInput(ctx context.Context, _ conversation.AcceptedOPSInputReadRequest) (conversation.AcceptedOPSInput, error) {
	s.readContexts <- ctx
	<-ctx.Done()
	return conversation.AcceptedOPSInput{}, ctx.Err()
}

func TestAgentOpsInputOriginRejectsMalformedAndDuplicateHeadersBeforeOwnerEffects(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	for _, tc := range []struct {
		name   string
		values []string
	}{
		{name: "empty value", values: []string{""}},
		{name: "unknown value", values: []string{"Human"}},
		{name: "multiple values", values: []string{"human", "automation"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newAgentOpsAcceptedOPSInputStoreStub("ren")
			executor := &agentOpsExecutorStub{output: "verified output"}
			admission := &agentOpsNativeCodingAdmissionStub{}
			handler := newAgentOpsTestHandlerWithStore(t, token, executor, admission, store)
			req := httptest.NewRequest(http.MethodPost, "/v1/agent/ops", strings.NewReader(`{"message":"run"}`))
			setAgentOpsHeaders(req, token, "req-origin-invalid")
			for _, value := range tc.values {
				req.Header.Add("X-RenCrow-Input-Origin", value)
			}
			req.RemoteAddr = "127.0.0.1:18791"
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			accepts, reads := store.counts()
			if rec.Code != http.StatusBadRequest || accepts != 0 || reads != 0 || admission.calls != 0 || executor.calls != 0 {
				t.Fatalf("status=%d accepts=%d reads=%d admission=%d executor=%d body=%q", rec.Code, accepts, reads, admission.calls, executor.calls, rec.Body.String())
			}
		})
	}
}

func TestAgentOpsHumanOriginRequiresNativeMessageRoute(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	t.Run("native profile disabled", func(t *testing.T) {
		executor := &agentOpsExecutorStub{output: "must not run"}
		handler := newAgentOpsTestHandlerWithNativeAdmission(t, token, executor, nil)
		req := httptest.NewRequest(http.MethodPost, "/v1/agent/ops", strings.NewReader(`{"message":"run"}`))
		setAgentOpsHeaders(req, token, "req-origin-disabled")
		req.Header.Set("X-RenCrow-Input-Origin", "human")
		req.RemoteAddr = "127.0.0.1:18791"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || executor.calls != 0 {
			t.Fatalf("status=%d executor=%d body=%q", rec.Code, executor.calls, rec.Body.String())
		}
	})

	t.Run("DCI operation", func(t *testing.T) {
		executor := &agentOpsExecutorWithToolsStub{agentOpsExecutorStub: &agentOpsExecutorStub{output: "unused"}}
		handler := newAgentOpsTestHandlerWithNativeAdmission(t, token, executor, nil)
		req := httptest.NewRequest(http.MethodPost, "/v1/agent/ops", strings.NewReader(`{"operation":"dci_identity_acceptance","query":"identity"}`))
		setAgentOpsHeaders(req, token, "req-origin-dci")
		req.Header.Set("X-RenCrow-Input-Origin", "human")
		req.RemoteAddr = "127.0.0.1:18791"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || executor.toolCalls != 0 {
			t.Fatalf("status=%d tool_calls=%d body=%q", rec.Code, executor.toolCalls, rec.Body.String())
		}
	})
}

func TestAgentOpsNativeHumanOriginIsStoredOnTheExactAcceptedInput(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	const requestID = "req-native-human-origin"
	message := "  exact 人の原文 🐦\n\t"
	store := newAgentOpsAcceptedOPSInputStoreStub("ren")
	executor := &agentOpsExecutorStub{output: "verified output"}
	admission := &agentOpsNativeCodingAdmissionStub{}
	handler := newAgentOpsTestHandlerWithStore(t, token, executor, admission, store)
	req := httptest.NewRequest(http.MethodPost, "/v1/agent/ops", strings.NewReader(`{"message":"  exact 人の原文 🐦\n\t"}`))
	setAgentOpsHeaders(req, token, requestID)
	req.Header.Set("X-RenCrow-Input-Origin", "human")
	req.RemoteAddr = "127.0.0.1:18791"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	accepted, ok := store.snapshot(requestID)
	if !ok || accepted.Receipt.DeclaredOrigin != conversation.AcceptedOPSInputOrigin("human") || accepted.RawMessage != message {
		t.Fatalf("accepted input=%+v found=%t", accepted, ok)
	}
}

func TestAgentOpsDisabledNativeProfileUsesNormalOPSDespiteTypedNilRuntime(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	path := filepath.Join(t.TempDir(), "agent-ops.token")
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		LocalAgentOps: config.LocalAgentOpsConfig{Enabled: true, AuthTokenFile: path, UserID: "ren"},
	}
	runtime, err := buildNativeCodingRuntime(cfg, nil, nil)
	if err != nil || runtime != nil {
		t.Fatalf("disabled profile runtime=%v err=%v, want nil runtime", runtime, err)
	}
	admission, err := configuredNativeCodingAdmission(cfg, runtime)
	if err != nil || admission != nil {
		t.Fatalf("disabled profile admission=%#v err=%v, want nil interface", admission, err)
	}
	store := newAgentOpsAcceptedOPSInputStoreStub("ren")
	executor := &agentOpsExecutorStub{output: "normal OPS output"}
	handler, err := newConfiguredAgentOpsHandler(cfg, executor, nil, newAgentOpsTestTaskOwner(t), admission, store)
	if err != nil {
		t.Fatalf("newConfiguredAgentOpsHandler() error=%v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/v1/agent/ops", strings.NewReader(`{"message":"ordinary OPS message"}`))
	setAgentOpsHeaders(req, token, "req-disabled-profile")
	req.RemoteAddr = "127.0.0.1:18791"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || executor.calls != 1 {
		t.Fatalf("normal OPS status=%d executions=%d body=%q", rec.Code, executor.calls, rec.Body.String())
	}
	accepts, reads := store.counts()
	if accepts != 0 || reads != 0 {
		t.Fatalf("disabled profile called accepted-input owner: accepts=%d reads=%d", accepts, reads)
	}
}

func TestAgentOpsEnabledNativeProfileWithUnavailableRuntimeOrOwnerFailsClosed(t *testing.T) {
	cfg := &config.Config{NativeHarness: config.NativeHarnessConfig{Profile: config.NativeHarnessProfileConfig{Enabled: true}}}
	if _, err := configuredNativeCodingAdmission(cfg, nil); err == nil {
		t.Fatal("enabled profile with unavailable runtime was accepted")
	}
	const token = "0123456789abcdef0123456789abcdef"
	path := filepath.Join(t.TempDir(), "agent-ops.token")
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.LocalAgentOps = config.LocalAgentOpsConfig{Enabled: true, AuthTokenFile: path, UserID: "ren"}
	if _, err := newAgentOpsHandler(cfg, &agentOpsExecutorStub{output: "must not execute"}, nil, newAgentOpsTestTaskOwner(t), &agentOpsNativeCodingAdmissionStub{}, nil); err == nil {
		t.Fatal("enabled profile with unavailable accepted-input owner was accepted")
	}
}

func TestAgentOpsNativeSameRequestRetryClaimsOnceAndReplaySkipsAvailability(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	store := newAgentOpsAcceptedOPSInputStoreStub("ren")
	owner := newAgentOpsTestTaskOwner(t)
	gate := make(chan struct{})
	executor := &agentOpsNativeCountingBlockingExecutor{entered: make(chan struct{}, 2), release: gate}
	admission := &agentOpsNativeCodingAdmissionStub{}
	handler := newAgentOpsTestHandlerWithOwnerAndStore(t, token, executor, owner, admission, store)
	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { firstDone <- serveNativeOPSRequest(t, handler, token, "req-native-replay", "same raw") }()
	select {
	case <-executor.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("creator did not enter executor")
	}
	second := serveNativeOPSRequest(t, handler, token, "req-native-replay", "same raw")
	if second.Code != http.StatusAccepted {
		t.Fatalf("replay status=%d body=%q", second.Code, second.Body.String())
	}
	var replay agentOpsNativeResponse
	if err := json.Unmarshal(second.Body.Bytes(), &replay); err != nil {
		t.Fatal(err)
	}
	if replay.ClaimStatus != string(taskmanager.AcceptedOPSAlreadyRunning) || replay.RunID == "" || replay.Output != "" {
		t.Fatalf("replay projection=%+v", replay)
	}
	close(gate)
	first := <-firstDone
	if first.Code != http.StatusOK {
		t.Fatalf("creator status=%d body=%q", first.Code, first.Body.String())
	}
	admission.err = agent.ErrNativeCodingBlocked
	third := serveNativeOPSRequest(t, handler, token, "req-native-replay", "same raw")
	if third.Code != http.StatusOK {
		t.Fatalf("terminal replay status=%d body=%q", third.Code, third.Body.String())
	}
	var terminal agentOpsNativeResponse
	if err := json.Unmarshal(third.Body.Bytes(), &terminal); err != nil {
		t.Fatal(err)
	}
	if terminal.ClaimStatus != string(taskmanager.AcceptedOPSTerminal) || terminal.Output != "" || admission.calls != 1 || executor.calls.Load() != 1 {
		t.Fatalf("terminal replay=%+v admissions=%d executions=%d", terminal, admission.calls, executor.calls.Load())
	}
	accepts, reads := store.counts()
	if accepts != 3 || reads != 3 {
		t.Fatalf("accepts=%d reads=%d admissions=%d", accepts, reads, admission.calls)
	}
	taskID := modulecore.TaskID(replay.TaskID)
	task, err := owner.Get(context.Background(), taskID)
	if err != nil || task.Status != domaintask.StatusSucceeded {
		t.Fatalf("canonical task=%+v err=%v", task, err)
	}
	runs, err := owner.ListRuns(context.Background(), domaintask.RunFilter{TaskID: taskID})
	if err != nil || len(runs) != 1 || string(runs[0].RunID) == "" || runs[0].Status != domaintask.RunStatusSucceeded {
		t.Fatalf("canonical first runs=%+v err=%v", runs, err)
	}
}

func TestAgentOpsNativeChangedRawMessageConflictsBeforeClaim(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	store := newAgentOpsAcceptedOPSInputStoreStub("ren")
	executor := &agentOpsExecutorStub{output: "ok"}
	admission := &agentOpsNativeCodingAdmissionStub{}
	handler := newAgentOpsTestHandlerWithStore(t, token, executor, admission, store)
	for _, tc := range []struct {
		requestID string
		first     string
		changed   string
	}{
		{requestID: "req-native-lf-space", first: "hello\n", changed: "hello "},
		{requestID: "req-native-unicode", first: "  e\u0301 日本語\n", changed: "  é 日本語\n"},
	} {
		first := serveNativeOPSRequest(t, handler, token, tc.requestID, tc.first)
		second := serveNativeOPSRequest(t, handler, token, tc.requestID, tc.changed)
		if first.Code != http.StatusOK || second.Code != http.StatusConflict {
			t.Fatalf("request=%s first=%d second=%d body=%q", tc.requestID, first.Code, second.Code, second.Body.String())
		}
	}
	if executor.calls != 2 || admission.calls != 2 {
		t.Fatalf("executions=%d admissions=%d, want one per first request", executor.calls, admission.calls)
	}
}

func TestAgentOpsNativeOwnerReadFailureNeverClaimsOrExecutes(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	for _, tc := range []struct {
		name       string
		readErr    error
		wantStatus int
	}{
		{name: "transport unknown", readErr: errors.New("transport outcome unknown"), wantStatus: http.StatusServiceUnavailable},
		{name: "forgotten or missing", readErr: conversation.ErrAcceptedOPSInputInvalid, wantStatus: http.StatusConflict},
		{name: "owner unavailable", readErr: conversation.ErrAcceptedOPSInputUnavailable, wantStatus: http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newAgentOpsAcceptedOPSInputStoreStub("ren")
			store.readErr = tc.readErr
			executor := &agentOpsExecutorStub{output: "must not execute"}
			admission := &agentOpsNativeCodingAdmissionStub{}
			owner := newAgentOpsTestTaskOwner(t)
			handler := newAgentOpsTestHandlerWithOwnerAndStore(t, token, executor, owner, admission, store)
			rec := serveNativeOPSRequest(t, handler, token, "req-native-read-fail", "run")
			if rec.Code != tc.wantStatus || executor.calls != 0 || admission.calls != 0 {
				t.Fatalf("status=%d execution=%d admission=%d body=%q", rec.Code, executor.calls, admission.calls, rec.Body.String())
			}
			accepts, reads := store.counts()
			if accepts != 1 || reads != 1 {
				t.Fatalf("accepts=%d reads=%d", accepts, reads)
			}
			tasks, err := owner.List(context.Background(), domaintask.Filter{})
			if err != nil || len(tasks) != 0 {
				t.Fatalf("owner Tasks after failed receipt read=%v err=%v, want none", tasks, err)
			}
		})
	}
}

func TestAgentOpsNativeRejectsSubstitutedOrMalformedOwnerReceiptsBeforeClaim(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	for _, tc := range []struct {
		name                string
		mutateAcceptReceipt func(conversation.AcceptedOPSInputReceipt) conversation.AcceptedOPSInputReceipt
		mutateReadInput     func(conversation.AcceptedOPSInput) conversation.AcceptedOPSInput
		wantReads           int
	}{
		{
			name: "accept task id substitution",
			mutateAcceptReceipt: func(receipt conversation.AcceptedOPSInputReceipt) conversation.AcceptedOPSInputReceipt {
				receipt.TaskID = modulecore.NewTaskID()
				return receipt
			},
		},
		{
			name: "accept payload hash substitution",
			mutateAcceptReceipt: func(receipt conversation.AcceptedOPSInputReceipt) conversation.AcceptedOPSInputReceipt {
				receipt.PayloadSHA256 = strings.Repeat("b", 64)
				return receipt
			},
		},
		{
			name: "read task id substitution",
			mutateReadInput: func(input conversation.AcceptedOPSInput) conversation.AcceptedOPSInput {
				input.Receipt.TaskID = modulecore.NewTaskID()
				return input
			},
			wantReads: 1,
		},
		{
			name: "read payload hash substitution",
			mutateReadInput: func(input conversation.AcceptedOPSInput) conversation.AcceptedOPSInput {
				input.Receipt.PayloadSHA256 = strings.Repeat("c", 64)
				return input
			},
			wantReads: 1,
		},
		{
			name: "malformed read receipt",
			mutateReadInput: func(input conversation.AcceptedOPSInput) conversation.AcceptedOPSInput {
				input.Receipt.ManifestSHA256 = "malformed"
				return input
			},
			wantReads: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newAgentOpsAcceptedOPSInputStoreStub("ren")
			store.mutateAcceptReceipt = tc.mutateAcceptReceipt
			store.mutateReadInput = tc.mutateReadInput
			owner := newAgentOpsTestTaskOwner(t)
			executor := &agentOpsExecutorStub{output: "must not execute"}
			admission := &agentOpsNativeCodingAdmissionStub{}
			handler := newAgentOpsTestHandlerWithOwnerAndStore(t, token, executor, owner, admission, store)
			rec := serveNativeOPSRequest(t, handler, token, "req-native-tamper", "raw input")
			if rec.Code != http.StatusConflict || executor.calls != 0 || admission.calls != 0 {
				t.Fatalf("status=%d execution=%d admission=%d body=%q", rec.Code, executor.calls, admission.calls, rec.Body.String())
			}
			accepts, reads := store.counts()
			if accepts != 1 || reads != tc.wantReads {
				t.Fatalf("accepts=%d reads=%d, want 1/%d", accepts, reads, tc.wantReads)
			}
			tasks, err := owner.List(context.Background(), domaintask.Filter{})
			if err != nil || len(tasks) != 0 {
				t.Fatalf("owner Tasks after invalid receipt=%v err=%v, want none", tasks, err)
			}
		})
	}
}

func TestAgentOpsNativeTaskClaimFailureDoesNotExecute(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	baseStore, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	faultStore := &agentOpsFaultTaskStore{Store: baseStore}
	faultStore.failTransactions.Store(true)
	owner := taskmanager.New(faultStore, taskmanager.DefaultParallelLimits())
	t.Cleanup(func() {
		if err := owner.Close(); err != nil {
			t.Errorf("close Task owner: %v", err)
		}
	})
	acceptedStore := newAgentOpsAcceptedOPSInputStoreStub("ren")
	executor := &agentOpsExecutorStub{output: "must not execute"}
	admission := &agentOpsNativeCodingAdmissionStub{}
	handler := newAgentOpsTestHandlerWithOwnerAndStore(t, token, executor, owner, admission, acceptedStore)
	rec := serveNativeOPSRequest(t, handler, token, "req-native-claim-fail", "raw input")
	if rec.Code != http.StatusServiceUnavailable || executor.calls != 0 || admission.calls != 0 {
		t.Fatalf("status=%d execution=%d admission=%d body=%q", rec.Code, executor.calls, admission.calls, rec.Body.String())
	}
	accepts, reads := acceptedStore.counts()
	if accepts != 1 || reads != 1 {
		t.Fatalf("accepts=%d reads=%d, want accepted owner resolution before failed Task claim", accepts, reads)
	}
	tasks, err := baseStore.ListTasks(context.Background(), domaintask.Filter{})
	if err != nil || len(tasks) != 0 {
		t.Fatalf("Task owner after failed claim=%v err=%v, want no claim", tasks, err)
	}
}

func TestAgentOpsNativeFinalTaskPersistenceFailureDoesNotProjectSuccess(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	baseStore, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	faultStore := &agentOpsFaultTaskStore{Store: baseStore}
	owner := taskmanager.New(faultStore, taskmanager.DefaultParallelLimits())
	t.Cleanup(func() {
		if err := owner.Close(); err != nil {
			t.Errorf("close Task owner: %v", err)
		}
	})
	store := newAgentOpsAcceptedOPSInputStoreStub("ren")
	executor := &agentOpsFinalizationFailureExecutor{failWrites: &faultStore.failWrites}
	admission := &agentOpsNativeCodingAdmissionStub{}
	handler := newAgentOpsTestHandlerWithOwnerAndStore(t, token, executor, owner, admission, store)
	rec := serveNativeOPSRequest(t, handler, token, "req-native-finalize-fail", "raw input")
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "verified output") || strings.Contains(rec.Body.String(), "succeeded") {
		t.Fatalf("final persistence failure projected success: status=%d body=%q", rec.Code, rec.Body.String())
	}
	tasks, err := baseStore.ListTasks(context.Background(), domaintask.Filter{})
	if err != nil || len(tasks) != 1 || tasks[0].Status != domaintask.StatusRunning {
		t.Fatalf("Task state after failed final commit=%+v err=%v, want running claim", tasks, err)
	}
	runs, err := baseStore.ListRuns(context.Background(), domaintask.RunFilter{TaskID: tasks[0].TaskID})
	if err != nil || len(runs) != 1 || runs[0].Status != domaintask.RunStatusRunning {
		t.Fatalf("Run state after failed final commit=%+v err=%v, want running claim", runs, err)
	}
}

func TestAgentOpsNativeAcceptOutcomeUnknownNeverReadsOrClaims(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	store := newAgentOpsAcceptedOPSInputStoreStub("ren")
	store.acceptErr = errors.New("accept transport outcome unknown")
	owner := newAgentOpsTestTaskOwner(t)
	executor := &agentOpsExecutorStub{output: "must not execute"}
	admission := &agentOpsNativeCodingAdmissionStub{}
	handler := newAgentOpsTestHandlerWithOwnerAndStore(t, token, executor, owner, admission, store)
	rec := serveNativeOPSRequest(t, handler, token, "req-native-accept-unknown", "run")
	if rec.Code != http.StatusServiceUnavailable || executor.calls != 0 || admission.calls != 0 {
		t.Fatalf("status=%d execution=%d admission=%d body=%q", rec.Code, executor.calls, admission.calls, rec.Body.String())
	}
	accepts, reads := store.counts()
	if accepts != 1 || reads != 0 {
		t.Fatalf("accepts=%d reads=%d, want accept once and no read after ambiguous accept", accepts, reads)
	}
	tasks, err := owner.List(context.Background(), domaintask.Filter{})
	if err != nil || len(tasks) != 0 {
		t.Fatalf("owner Tasks after ambiguous accept=%v err=%v, want none", tasks, err)
	}
}

func TestAgentOpsNativeOutcomeUnknownKeepsClaimActiveAndExactReplayDoesNotExecute(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	store := newAgentOpsAcceptedOPSInputStoreStub("ren")
	owner := newAgentOpsTestTaskOwner(t)
	unknown := errors.Join(errors.New("wrapped"), agent.ErrNativeCodingOutcomeUnknown)
	executor := &agentOpsExecutorStub{err: unknown}
	admission := &agentOpsNativeCodingAdmissionStub{}
	handler := newAgentOpsTestHandlerWithOwnerAndStore(t, token, executor, owner, admission, store)
	first := serveNativeOPSRequest(t, handler, token, "req-native-unknown", "run")
	if first.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%q", first.Code, first.Body.String())
	}
	var firstResponse agentOpsNativeResponse
	if err := json.Unmarshal(first.Body.Bytes(), &firstResponse); err != nil {
		t.Fatal(err)
	}
	if firstResponse.ClaimStatus != string(taskmanager.AcceptedOPSOutcomeUnknown) || firstResponse.TaskStatus != string(domaintask.StatusRunning) || firstResponse.RunStatus != string(domaintask.RunStatusRunning) {
		t.Fatalf("unknown outcome projection=%+v", firstResponse)
	}
	second := serveNativeOPSRequest(t, handler, token, "req-native-unknown", "run")
	if second.Code != http.StatusAccepted || executor.calls != 1 || admission.calls != 1 {
		t.Fatalf("retry status=%d executor=%d admission=%d body=%q", second.Code, executor.calls, admission.calls, second.Body.String())
	}
	var response agentOpsNativeResponse
	if err := json.Unmarshal(second.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.ClaimStatus != string(taskmanager.AcceptedOPSAlreadyRunning) || response.TaskStatus != string(domaintask.StatusRunning) || response.RunStatus != string(domaintask.RunStatusRunning) {
		t.Fatalf("running replay=%+v", response)
	}
}

func TestAgentOpsNativeUnverifiedAndCancelledResultsCloseCanonicalLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name     string
		result   agent.NativeCodingResult
		wantTask domaintask.Status
		wantRun  domaintask.RunStatus
	}{
		{name: "completed not run", result: agent.NativeCodingResult{Status: agent.NativeRunCompleted, Verification: agent.NativeVerificationNotRun, FinalText: "unverified"}, wantTask: domaintask.StatusFailed, wantRun: domaintask.RunStatusFailed},
		{name: "completed unknown", result: agent.NativeCodingResult{Status: agent.NativeRunCompleted, Verification: agent.NativeVerificationUnknown, FinalText: "uncertain"}, wantTask: domaintask.StatusFailed, wantRun: domaintask.RunStatusFailed},
		{name: "cancelled", result: agent.NativeCodingResult{Status: agent.NativeRunCancelled}, wantTask: domaintask.StatusCancelled, wantRun: domaintask.RunStatusCancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const token = "0123456789abcdef0123456789abcdef"
			store := newAgentOpsAcceptedOPSInputStoreStub("ren")
			owner := newAgentOpsTestTaskOwner(t)
			executor := &agentOpsExecutorStub{err: &agent.NativeCodingError{Result: tc.result}}
			admission := &agentOpsNativeCodingAdmissionStub{}
			handler := newAgentOpsTestHandlerWithOwnerAndStore(t, token, executor, owner, admission, store)
			rec := serveNativeOPSRequest(t, handler, token, "req-native-result-"+strings.ReplaceAll(tc.name, " ", "-"), "run")
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
			}
			var response agentOpsNativeResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			task, err := owner.Get(context.Background(), modulecore.TaskID(response.TaskID))
			if err != nil {
				t.Fatal(err)
			}
			run, err := owner.GetRun(context.Background(), modulecore.RunID(response.RunID))
			if err != nil {
				t.Fatal(err)
			}
			if task.Status != tc.wantTask || run.Status != tc.wantRun || response.Output != "" {
				t.Fatalf("task=%s run=%s response=%+v", task.Status, run.Status, response)
			}
			if tc.result.Status == agent.NativeRunCompleted && task.Summary != "native coding completed but verification was "+string(tc.result.Verification) {
				t.Fatalf("summary=%q want completed verification state retained", task.Summary)
			}
		})
	}
}

func newAgentOpsTestHandlerWithStore(t *testing.T, token string, executor agentOpsExecutor, admission *agentOpsNativeCodingAdmissionStub, store conversation.AcceptedOPSInputStore) http.HandlerFunc {
	return newAgentOpsTestHandlerWithOwnerAndStore(t, token, executor, newAgentOpsTestTaskOwner(t), admission, store)
}

func newAgentOpsTestHandlerWithOwnerAndStore(t *testing.T, token string, executor agentOpsExecutor, owner *taskmanager.Manager, admission orchestrator.NativeCodingAdmission, store conversation.AcceptedOPSInputStore) http.HandlerFunc {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent-ops.token")
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{LocalAgentOps: config.LocalAgentOpsConfig{Enabled: true, AuthTokenFile: path, UserID: "ren"}}
	handler, err := newAgentOpsHandler(cfg, executor, nil, owner, admission, store)
	if err != nil {
		t.Fatalf("newAgentOpsHandler() error=%v", err)
	}
	return handler
}

func serveNativeOPSRequest(t *testing.T, handler http.Handler, token, requestID, message string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(agentOpsRequest{Message: message})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/agent/ops", strings.NewReader(string(body)))
	setAgentOpsHeaders(req, token, requestID)
	req.RemoteAddr = "127.0.0.1:18791"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

type agentOpsFaultTaskStore struct {
	domaintask.Store
	failTransactions atomic.Bool
	failWrites       atomic.Bool
}

func (s *agentOpsFaultTaskStore) TaskTransaction(ctx context.Context, taskID modulecore.TaskID, fn func(domaintask.Store) error) error {
	if s.failTransactions.Load() {
		return errors.New("injected Task claim persistence failure")
	}
	return s.Store.TaskTransaction(ctx, taskID, func(txStore domaintask.Store) error {
		return fn(&agentOpsFaultTaskStoreView{Store: txStore, failWrites: &s.failWrites})
	})
}

type agentOpsFaultTaskStoreView struct {
	domaintask.Store
	failWrites *atomic.Bool
}

func (s *agentOpsFaultTaskStoreView) SaveTask(ctx context.Context, task domaintask.Task) error {
	if s.failWrites.Load() {
		return errors.New("injected final Task persistence failure")
	}
	return s.Store.SaveTask(ctx, task)
}

func (s *agentOpsFaultTaskStoreView) SaveRun(ctx context.Context, run domaintask.Run) error {
	if s.failWrites.Load() {
		return errors.New("injected final Run persistence failure")
	}
	return s.Store.SaveRun(ctx, run)
}

type agentOpsFinalizationFailureExecutor struct {
	failWrites *atomic.Bool
}

func (e *agentOpsFinalizationFailureExecutor) Execute(context.Context, conversation.TurnInput) (string, error) {
	e.failWrites.Store(true)
	return "verified output", nil
}

var _ conversation.AcceptedOPSInputStore = (*agentOpsAcceptedOPSInputStoreStub)(nil)
