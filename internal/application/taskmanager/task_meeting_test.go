package taskmanager

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func taskMeetingRootRequest() domaintask.TaskMeetingInvocationRequest {
	audience := []string{"aka", "ao", "gin", "kin", "kuro", "midori", "mio", "shiro"}
	return domaintask.TaskMeetingInvocationRequest{
		OccurrenceKey: "schedule-20261118T0900+0900", Purpose: domaintask.TaskMeetingPurposeRoot,
		CoordinatorActorID: "shiro", ActorID: "shiro", ConfiguredOwnerUserID: "owner_user",
		ConfigRevision: 3, RegistrationEvidenceID: modulecore.NewEvidenceID(),
		FrozenConfig: []byte(`{"actors":["aka","ao","gin","kin","kuro","midori","mio","shiro"],"owner":"owner_user"}`),
		Audience:     audience, FrozenInput: []byte(`{"operation":"register_occurrence"}`), Timing: modulecore.TaskTiming{Timezone: "Asia/Tokyo"},
	}
}

func TestEnsureTaskMeetingRootReceiptReplayAndFrozenConflict(t *testing.T) {
	ctx := context.Background()
	path, err := os.MkdirTemp(".", ".taskmeeting-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(path) })
	store, err := taskpersistence.NewJSONLStore(path)
	if err != nil {
		t.Fatal(err)
	}
	manager := New(store, DefaultParallelLimits())
	admissions := 0
	admit := func(context.Context, domaintask.TaskMeetingInvocationRequest, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext) error {
		admissions++
		return nil
	}
	receiptAuth := func(context.Context) error { return nil }
	stateAuth := func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext) error { return nil }
	request := taskMeetingRootRequest()
	first, firstRun, firstProof, err := manager.EnsureTaskMeetingRootInvocation(ctx, request, admit, receiptAuth, stateAuth)
	if err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	if first.TaskID == "" || firstRun.RunID == "" || firstProof.TaskID != first.TaskID || firstProof.RunID != firstRun.RunID || firstProof.AdmissionEvidenceID == firstProof.RegistrationEvidenceID {
		t.Fatalf("root proof did not separate persisted registration and manager admission evidence: %+v", firstProof)
	}
	if _, err := manager.Get(ctx, first.TaskID); !errors.Is(err, domaintask.ErrTypedTaskRequiresOwner) {
		t.Fatalf("generic Task get did not reject TaskMeeting root through its owner: %v", err)
	}
	if _, _, err := manager.RecoverRunAfterProcessRestart(ctx, first.TaskID, firstRun.RunID, "shiro", domaintask.StatusFailed, "generic recovery", ""); !errors.Is(err, domaintask.ErrTypedTaskRequiresOwner) {
		t.Fatalf("generic restart recovery accepted TaskMeeting control Run: %v", err)
	}
	if err := store.WithTaskExecutionFence(ctx, first.TaskID, func() error { return nil }); !errors.Is(err, domaintask.ErrTypedTaskRequiresOwner) {
		t.Fatalf("generic execution fence accepted TaskMeeting control Task: %v", err)
	}
	second, secondRun, secondProof, err := manager.EnsureTaskMeetingRootInvocation(ctx, request, admit, receiptAuth, stateAuth)
	if err != nil {
		t.Fatalf("replay root: %v", err)
	}
	if second.TaskID != first.TaskID || secondRun.RunID != firstRun.RunID || secondProof.AdmissionEvidenceID != firstProof.AdmissionEvidenceID || admissions != 1 {
		t.Fatalf("root replay changed identity/admission: Task=%s Run=%s proof=%+v admissions=%d", second.TaskID, secondRun.RunID, secondProof, admissions)
	}
	contextRequest := request
	contextRequest.Purpose, contextRequest.ActorID = domaintask.TaskMeetingPurposeRecipientContext, "mio"
	contextRequest.ParentTaskID, contextRequest.ParentRunID = first.TaskID, firstRun.RunID
	contextRequest.FrozenInput = []byte(`{"purpose":"recipient_context"}`)
	recipient, recipientRun, _, err := manager.EnsureTaskMeetingRecipientContext(ctx, contextRequest, firstProof, admit, stateAuth, receiptAuth, stateAuth)
	if err != nil {
		t.Fatalf("ensure recipient context: %v", err)
	}
	aggregateChecks := 0
	verifyAggregate := func(_ context.Context, task domaintask.Task, run domaintask.Run, _ domaintask.SharedRoleContext, outcome domaintask.Status) error {
		aggregateChecks++
		contextTask, contextCurrentRun, _, contextErr := manager.readTaskMeetingState(ctx, recipient.TaskID, recipientRun.RunID, stateAuth)
		if contextErr != nil || contextTask.Status != domaintask.StatusSucceeded || contextCurrentRun.Status != domaintask.RunStatusSucceeded || outcome != domaintask.StatusSucceeded ||
			task.TaskID != first.TaskID || run.RunID != firstRun.RunID || task.Status != domaintask.StatusRunning && task.Status != domaintask.StatusSucceeded {
			return domaintask.ErrTaskMeetingConflict
		}
		return nil
	}
	if err := manager.CompleteTaskMeetingRecipientContext(ctx, recipient.TaskID, recipientRun.RunID, stateAuth, func(_ context.Context, task domaintask.Task, run domaintask.Run, _ domaintask.SharedRoleContext, outcome domaintask.Status) error {
		if (task.Status != domaintask.StatusRunning && task.Status != domaintask.StatusSucceeded) || (run.Status != domaintask.RunStatusRunning && run.Status != domaintask.RunStatusSucceeded) || outcome != domaintask.StatusSucceeded {
			return domaintask.ErrTaskMeetingConflict
		}
		return nil
	}); err != nil {
		t.Fatalf("close recipient context: %v", err)
	}
	if err := manager.CompleteTaskMeetingRootInvocation(ctx, first.TaskID, firstRun.RunID, stateAuth, verifyAggregate); err != nil {
		t.Fatalf("complete root only after aggregate proof: %v", err)
	}
	completed, completedRun, _, err := manager.readTaskMeetingState(ctx, first.TaskID, firstRun.RunID, stateAuth)
	if err != nil || completed.Status != domaintask.StatusSucceeded || completedRun.Status != domaintask.RunStatusSucceeded || aggregateChecks != 2 {
		t.Fatalf("root did not close after verified aggregate: Task=%+v Run=%+v checks=%d err=%v", completed, completedRun, aggregateChecks, err)
	}
	if err := manager.CompleteTaskMeetingRootInvocation(ctx, first.TaskID, firstRun.RunID, stateAuth, func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext, domaintask.Status) error {
		return nil
	}); err != nil {
		t.Fatalf("root completion replay: %v", err)
	}
	changed := request
	changed.ConfigRevision++
	if _, _, _, err := manager.EnsureTaskMeetingRootInvocation(ctx, changed, admit, receiptAuth, stateAuth); !errors.Is(err, domaintask.ErrTaskMeetingConflict) {
		t.Fatalf("same occurrence with changed config revision error=%v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTaskMeetingConcurrentRootEnsureReturnsOneOwnerReceipt(t *testing.T) {
	ctx := context.Background()
	path, err := os.MkdirTemp(".", ".taskmeeting-concurrent-root-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(path) })
	store, err := taskpersistence.NewJSONLStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager := New(store, DefaultParallelLimits())
	var admissionsMu sync.Mutex
	admit := func(context.Context, domaintask.TaskMeetingInvocationRequest, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext) error {
		admissionsMu.Lock()
		defer admissionsMu.Unlock()
		return nil
	}
	request := taskMeetingRootRequest()
	start := make(chan struct{})
	type result struct {
		task  domaintask.Task
		run   domaintask.Run
		proof domaintask.TaskMeetingRootProof
		err   error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			<-start
			task, run, proof, callErr := manager.EnsureTaskMeetingRootInvocation(ctx, request, admit, func(context.Context) error { return nil }, func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext) error { return nil })
			results <- result{task: task, run: run, proof: proof, err: callErr}
		}()
	}
	close(start)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.task.TaskID == "" || first.task.TaskID != second.task.TaskID || first.run.RunID != second.run.RunID || first.proof.AdmissionEvidenceID != second.proof.AdmissionEvidenceID {
		t.Fatalf("concurrent ensures did not converge to one typed receipt: first=%+v second=%+v", first, second)
	}
	all, err := store.ListTasks(ctx, domaintask.Filter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Fatalf("generic Task list exposed %d owner-managed meeting Tasks", len(all))
	}
}

func TestTaskMeetingRecipientContextPersistsExactConsumedReference(t *testing.T) {
	ctx := context.Background()
	path, err := os.MkdirTemp(".", ".taskmeeting-context-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(path) })
	store, err := taskpersistence.NewJSONLStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager := New(store, DefaultParallelLimits())
	admit := func(context.Context, domaintask.TaskMeetingInvocationRequest, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext) error {
		return nil
	}
	receiptAuth := func(context.Context) error { return nil }
	stateAuth := func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext) error { return nil }
	request := taskMeetingRootRequest()
	root, rootRun, proof, err := manager.EnsureTaskMeetingRootInvocation(ctx, request, admit, receiptAuth, stateAuth)
	if err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	contextRequest := request
	contextRequest.Purpose = domaintask.TaskMeetingPurposeRecipientContext
	contextRequest.ActorID = "mio"
	contextRequest.ParentTaskID = root.TaskID
	contextRequest.ParentRunID = rootRun.RunID
	contextRequest.FrozenInput = []byte(`{"purpose":"recipient_context"}`)
	recipient, recipientRun, _, err := manager.EnsureTaskMeetingRecipientContext(ctx, contextRequest, proof, admit, stateAuth, receiptAuth, stateAuth)
	if err != nil {
		t.Fatalf("ensure recipient context: %v", err)
	}
	body := "canonical meeting message"
	bodyDigest := sha256.Sum256([]byte(body))
	audienceHash, err := domaintask.TaskMeetingAudienceSHA256(contextRequest.Audience)
	if err != nil {
		t.Fatal(err)
	}
	ref := domaintask.TaskMeetingMessageReference{
		OccurrenceKey: contextRequest.OccurrenceKey, SessionID: modulecore.NewSessionID(), ThreadID: modulecore.NewThreadID(),
		MessageID: modulecore.NewMessageID(), EventID: modulecore.NewEventID(), BodySHA256: hex.EncodeToString(bodyDigest[:]),
		AudienceSHA256: audienceHash, ConfigRevision: contextRequest.ConfigRevision, ConfigSHA256: proof.ConfigSHA256,
		AttemptGeneration: 1, AttemptEpoch: 1, ScopeEvidenceID: modulecore.NewEvidenceID(),
	}
	messageAuth := func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext, domaintask.TaskMeetingMessageReference) error {
		return nil
	}
	consumed, err := manager.AppendTaskMeetingConsumedMessage(ctx, recipient.TaskID, recipientRun.RunID, ref, body, messageAuth)
	if err != nil {
		t.Fatalf("append consumed message: %v", err)
	}
	replayed, err := manager.AppendTaskMeetingConsumedMessage(ctx, recipient.TaskID, recipientRun.RunID, ref, body, messageAuth)
	if err != nil || replayed.EvidenceID != consumed.EvidenceID {
		t.Fatalf("append replay=%+v err=%v", replayed, err)
	}
	found, err := manager.FindTaskMeetingConsumedMessage(ctx, recipient.TaskID, recipientRun.RunID, ref, messageAuth)
	if err != nil || found.EvidenceID != consumed.EvidenceID {
		t.Fatalf("find consumption=%+v err=%v", found, err)
	}
	if _, err := manager.AppendTaskMeetingConsumedMessage(ctx, recipient.TaskID, recipientRun.RunID, ref, "different body", messageAuth); !errors.Is(err, domaintask.ErrTaskMeetingConflict) {
		t.Fatalf("changed canonical body error=%v", err)
	}
}

func TestTaskMeetingGenerationUsesFrozenContextPrefixAndDistinctRequestReceipts(t *testing.T) {
	ctx := context.Background()
	path, err := os.MkdirTemp(".", ".taskmeeting-generation-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(path) })
	store, err := taskpersistence.NewJSONLStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager := New(store, DefaultParallelLimits())
	admissions := 0
	admit := func(context.Context, domaintask.TaskMeetingInvocationRequest, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext) error {
		admissions++
		return nil
	}
	receiptAuth := func(context.Context) error { return nil }
	stateAuth := func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext) error { return nil }
	messageAuth := func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext, domaintask.TaskMeetingMessageReference) error {
		return nil
	}
	rootRequest := taskMeetingRootRequest()
	root, rootRun, rootProof, err := manager.EnsureTaskMeetingRootInvocation(ctx, rootRequest, admit, receiptAuth, stateAuth)
	if err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	contextRequest := rootRequest
	contextRequest.Purpose, contextRequest.ActorID = domaintask.TaskMeetingPurposeRecipientContext, "mio"
	contextRequest.ParentTaskID, contextRequest.ParentRunID = root.TaskID, rootRun.RunID
	contextRequest.FrozenInput = []byte(`{"purpose":"recipient_context"}`)
	contextTask, contextRun, _, err := manager.EnsureTaskMeetingRecipientContext(ctx, contextRequest, rootProof, admit, stateAuth, receiptAuth, stateAuth)
	if err != nil {
		t.Fatalf("ensure recipient: %v", err)
	}
	appendMessage := func(message, body string) {
		digest := sha256.Sum256([]byte(body))
		audienceHash, _ := domaintask.TaskMeetingAudienceSHA256(contextRequest.Audience)
		ref := domaintask.TaskMeetingMessageReference{OccurrenceKey: contextRequest.OccurrenceKey, SessionID: modulecore.NewSessionID(), ThreadID: modulecore.NewThreadID(), MessageID: modulecore.MessageID(message), EventID: modulecore.NewEventID(), BodySHA256: hex.EncodeToString(digest[:]), AudienceSHA256: audienceHash, ConfigRevision: contextRequest.ConfigRevision, ConfigSHA256: rootProof.ConfigSHA256, AttemptGeneration: 1, AttemptEpoch: 1, ScopeEvidenceID: modulecore.NewEvidenceID()}
		if _, appendErr := manager.AppendTaskMeetingConsumedMessage(ctx, contextTask.TaskID, contextRun.RunID, ref, body, messageAuth); appendErr != nil {
			t.Fatalf("append context message: %v", appendErr)
		}
	}
	readContext := func() domaintask.SharedRoleContext {
		_, _, shared, readErr := manager.readTaskMeetingState(ctx, contextTask.TaskID, contextRun.RunID, stateAuth)
		if readErr != nil {
			t.Fatalf("read recipient context: %v", readErr)
		}
		return shared
	}
	appendMessage(string(modulecore.NewMessageID()), "first accepted message")
	firstContext := readContext()
	contextHash, err := domaintask.TaskMeetingContextSHA256(firstContext)
	if err != nil {
		t.Fatal(err)
	}
	requestID := modulecore.NewRequestID()
	firstRequest := contextRequest
	firstRequest.Purpose, firstRequest.GenerationRequestID = domaintask.TaskMeetingPurposeReport, requestID
	firstRequest.GenerationScope = taskMeetingTestScope(2)
	firstRequest.FrozenInput, err = taskMeetingTestInput(t, firstRequest.GenerationScope, `{"prompt":"report first context"}`)
	if err != nil {
		t.Fatal(err)
	}
	firstRequest.SourceContextSHA256 = contextHash
	firstRequest.FrozenSourceMessages = append([]domaintask.TaskMeetingConsumedMessage(nil), firstContext.TaskMeetingConsumedMessages...)
	firstRequest.ParentTaskID, firstRequest.ParentRunID = contextTask.TaskID, contextRun.RunID
	firstTask, firstRun, err := manager.EnsureTaskMeetingGeneration(ctx, firstRequest, contextTask.TaskID, contextRun.RunID, admit, stateAuth, receiptAuth, stateAuth)
	if err != nil {
		t.Fatalf("ensure first report: %v", err)
	}
	appendMessage(string(modulecore.NewMessageID()), "second accepted message")
	replayTask, replayRun, err := manager.EnsureTaskMeetingGeneration(ctx, firstRequest, contextTask.TaskID, contextRun.RunID, admit, stateAuth, receiptAuth, stateAuth)
	if err != nil || replayTask.TaskID != firstTask.TaskID || replayRun.RunID != firstRun.RunID {
		t.Fatalf("historical report replay=%s/%s err=%v, want %s/%s", replayTask.TaskID, replayRun.RunID, err, firstTask.TaskID, firstRun.RunID)
	}
	changed := firstRequest
	changed.FrozenInput, err = taskMeetingTestInput(t, changed.GenerationScope, `{"prompt":"changed report"}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.EnsureTaskMeetingGeneration(ctx, changed, contextTask.TaskID, contextRun.RunID, admit, stateAuth, receiptAuth, stateAuth); !errors.Is(err, domaintask.ErrTaskMeetingConflict) {
		t.Fatalf("same report request with changed bytes error=%v", err)
	}
	canonicalResult := json.RawMessage(`{"report":"Mio's report","concerns":[]}`)
	effectCalls, effectAdmissions := 0, 0
	effect := func(_ context.Context, task domaintask.Task, run domaintask.Run, _, _ domaintask.SharedRoleContext, input json.RawMessage) (json.RawMessage, error) {
		effectCalls++
		if task.TaskID != firstTask.TaskID || run.RunID != firstRun.RunID || !bytes.Equal(input, firstRequest.FrozenInput) {
			return nil, domaintask.ErrTaskMeetingConflict
		}
		if _, err := manager.SaveTaskMeetingGenerationOutput(ctx, task.TaskID, run.RunID, canonicalResult, stateAuth); err != nil {
			return nil, err
		}
		return nil, errors.New("simulated reply loss after canonical output commit")
	}
	authorizeEffect := func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext) error {
		effectAdmissions++
		return nil
	}
	for attempt := 0; attempt < 2; attempt++ {
		result, executeErr := manager.ExecuteTaskMeetingGeneration(ctx, firstTask.TaskID, firstRun.RunID, stateAuth, stateAuth, authorizeEffect, effect)
		if executeErr != nil || !bytes.Equal(result, canonicalResult) {
			t.Fatalf("execute generation attempt %d result=%s err=%v", attempt, result, executeErr)
		}
	}
	storedOutput, finishedReference, err := manager.GetTaskMeetingGenerationOutput(ctx, firstTask.TaskID, firstRun.RunID, stateAuth)
	if err != nil || !bytes.Equal(storedOutput, canonicalResult) || finishedReference.ValidateRun(firstTask, firstRun) != nil || effectCalls != 1 || effectAdmissions != 1 {
		t.Fatalf("effect fence/output receipt effects=%d admissions=%d ref=%+v output=%s err=%v", effectCalls, effectAdmissions, finishedReference, storedOutput, err)
	}
	if _, err := store.LookupTaskOperation(ctx, finishedReference.ReceiptID); !errors.Is(err, taskpersistence.ErrTaskOperationNotFound) {
		t.Fatalf("generic Task receipt lookup exposed typed generation output: %v", err)
	}
	resultChecks := 0
	verifyResult := func(_ context.Context, gotTask domaintask.Task, gotRun domaintask.Run, _ domaintask.SharedRoleContext, gotRef domaintask.TaskMeetingGenerationResultReference) error {
		resultChecks++
		body, persistedReference, readErr := manager.GetTaskMeetingGenerationOutput(ctx, gotTask.TaskID, gotRun.RunID, stateAuth)
		if readErr != nil || gotTask.TaskID != firstTask.TaskID || gotRun.RunID != firstRun.RunID || gotRef != finishedReference || persistedReference != gotRef || !bytes.Equal(body, canonicalResult) {
			return domaintask.ErrTaskMeetingConflict
		}
		return nil
	}
	if err := manager.FinishTaskMeetingInvocation(ctx, firstTask.TaskID, firstRun.RunID, finishedReference, stateAuth, verifyResult); err != nil {
		t.Fatalf("finish report with persisted result proof: %v", err)
	}
	finished, finishedRun, _, err := manager.readTaskMeetingState(ctx, firstTask.TaskID, firstRun.RunID, stateAuth)
	if err != nil || finished.Status != domaintask.StatusSucceeded || finishedRun.Status != domaintask.RunStatusSucceeded || finished.TaskMeetingResult == nil || *finished.TaskMeetingResult != finishedReference {
		t.Fatalf("finished Task lacks pinned result proof: Task=%+v Run=%+v err=%v", finished, finishedRun, err)
	}
	if err := manager.FinishTaskMeetingInvocation(ctx, firstTask.TaskID, firstRun.RunID, finishedReference, stateAuth, verifyResult); err != nil {
		t.Fatalf("exact finish replay: %v", err)
	}
	changedReference := finishedReference
	changedReference.ResultSHA256 = strings.Repeat("0", 64)
	if err := manager.FinishTaskMeetingInvocation(ctx, firstTask.TaskID, firstRun.RunID, changedReference, stateAuth, verifyResult); !errors.Is(err, domaintask.ErrTaskMeetingConflict) {
		t.Fatalf("changed result reference on finish replay error=%v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close Task store before restart recovery: %v", err)
	}
	store, err = taskpersistence.NewJSONLStore(path)
	if err != nil {
		t.Fatalf("reopen Task store: %v", err)
	}
	manager = New(store, DefaultParallelLimits())
	recoveredRoot, recoveredRootRun, recoveredProof, err := manager.FindTaskMeetingRootInvocation(ctx, rootRequest, receiptAuth, stateAuth)
	if err != nil || recoveredRoot.TaskID != root.TaskID || recoveredRootRun.RunID != rootRun.RunID || recoveredProof.AdmissionEvidenceID != rootProof.AdmissionEvidenceID {
		t.Fatalf("root receipt changed after writer reopen: Task=%s Run=%s proof=%+v err=%v", recoveredRoot.TaskID, recoveredRootRun.RunID, recoveredProof, err)
	}
	recoveredContext, recoveredContextRun, _, err := manager.FindTaskMeetingRecipientContext(ctx, contextRequest, recoveredProof, stateAuth, receiptAuth, stateAuth)
	if err != nil || recoveredContext.TaskID != contextTask.TaskID || recoveredContextRun.RunID != contextRun.RunID {
		t.Fatalf("recipient context changed after writer reopen: Task=%s Run=%s err=%v", recoveredContext.TaskID, recoveredContextRun.RunID, err)
	}
	appendMessage(string(modulecore.NewMessageID()), "third accepted message after restart")
	frozenGeneration, frozenGenerationRun, err := manager.FindTaskMeetingGenerationInvocation(ctx, firstRequest.OccurrenceKey, firstRequest.Purpose, firstRequest.ActorID, firstRequest.GenerationRequestID, receiptAuth, stateAuth, stateAuth)
	if err != nil || frozenGeneration.TaskID != firstTask.TaskID || frozenGenerationRun.RunID != firstRun.RunID || !bytes.Equal(frozenGeneration.TaskMeetingInvocation.FrozenInput, firstRequest.FrozenInput) {
		t.Fatalf("receipt-first generation lookup rebuilt or changed frozen input: Task=%s Run=%s err=%v", frozenGeneration.TaskID, frozenGenerationRun.RunID, err)
	}
	replayedAfterRestart, replayedAfterRestartRun, err := manager.EnsureTaskMeetingGeneration(ctx, firstRequest, contextTask.TaskID, contextRun.RunID, admit, stateAuth, receiptAuth, stateAuth)
	if err != nil || replayedAfterRestart.TaskID != firstTask.TaskID || replayedAfterRestartRun.RunID != firstRun.RunID || replayedAfterRestart.TaskMeetingResult == nil || *replayedAfterRestart.TaskMeetingResult != finishedReference {
		t.Fatalf("historical generation/result changed after context append and restart: Task=%s Run=%s err=%v", replayedAfterRestart.TaskID, replayedAfterRestartRun.RunID, err)
	}
	recoveredOutput, recoveredOutputReference, err := manager.ReadTaskMeetingGenerationResult(ctx, firstTask.TaskID, firstRun.RunID, stateAuth, stateAuth)
	if err != nil || !bytes.Equal(recoveredOutput, canonicalResult) || recoveredOutputReference != finishedReference {
		t.Fatalf("canonical Task-owner output receipt did not survive restart: ref=%+v output=%s err=%v", recoveredOutputReference, recoveredOutput, err)
	}
	if _, err := store.LookupTaskOperation(ctx, recoveredOutputReference.ReceiptID); !errors.Is(err, taskpersistence.ErrTaskOperationNotFound) {
		t.Fatalf("generic receipt lookup exposed recovered typed output: %v", err)
	}
	newHash, err := domaintask.TaskMeetingContextSHA256(readContext())
	if err != nil {
		t.Fatal(err)
	}
	latestContext := readContext()
	answer := firstRequest
	answer.Purpose, answer.GenerationRequestID = domaintask.TaskMeetingPurposeAnswer, modulecore.NewRequestID()
	answer.GenerationScope = taskMeetingTestScope(3)
	answer.FrozenInput, err = taskMeetingTestInput(t, answer.GenerationScope, `{"prompt":"answer current context"}`)
	if err != nil {
		t.Fatal(err)
	}
	answer.SourceContextSHA256 = newHash
	answer.FrozenSourceMessages = append([]domaintask.TaskMeetingConsumedMessage(nil), latestContext.TaskMeetingConsumedMessages...)
	answerTask, answerRun, err := manager.EnsureTaskMeetingGeneration(ctx, answer, contextTask.TaskID, contextRun.RunID, admit, stateAuth, receiptAuth, stateAuth)
	if err != nil {
		t.Fatalf("ensure answer generation: %v", err)
	}
	answerOutput := json.RawMessage(`{"answer":"Mio answered the new concern"}`)
	answerEffect := func(_ context.Context, task domaintask.Task, run domaintask.Run, _, _ domaintask.SharedRoleContext, input json.RawMessage) (json.RawMessage, error) {
		if task.TaskID != answerTask.TaskID || run.RunID != answerRun.RunID || !bytes.Equal(input, answer.FrozenInput) {
			return nil, domaintask.ErrTaskMeetingConflict
		}
		return append(json.RawMessage(nil), answerOutput...), nil
	}
	if result, executeErr := manager.ExecuteTaskMeetingGeneration(ctx, answerTask.TaskID, answerRun.RunID, stateAuth, stateAuth, authorizeEffect, answerEffect); executeErr != nil || !bytes.Equal(result, answerOutput) {
		t.Fatalf("execute answer generation result=%s err=%v", result, executeErr)
	}
	_, answerReference, err := manager.GetTaskMeetingGenerationOutput(ctx, answerTask.TaskID, answerRun.RunID, stateAuth)
	if err != nil {
		t.Fatalf("read answer output receipt: %v", err)
	}
	answerProof := func(_ context.Context, task domaintask.Task, run domaintask.Run, _ domaintask.SharedRoleContext, reference domaintask.TaskMeetingGenerationResultReference) error {
		body, stored, readErr := manager.GetTaskMeetingGenerationOutput(ctx, task.TaskID, run.RunID, stateAuth)
		if readErr != nil || stored != reference || !bytes.Equal(body, answerOutput) {
			return domaintask.ErrTaskMeetingConflict
		}
		return nil
	}
	if err := manager.FinishTaskMeetingInvocation(ctx, answerTask.TaskID, answerRun.RunID, answerReference, stateAuth, answerProof); err != nil {
		t.Fatalf("finish answer Task: %v", err)
	}
	if answerTask.TaskID == firstTask.TaskID || answerRun.RunID == firstRun.RunID || admissions != 4 {
		t.Fatalf("generation identities/admissions report=%s/%s answer=%s/%s admissions=%d", firstTask.TaskID, firstRun.RunID, answerTask.TaskID, answerRun.RunID, admissions)
	}
	if answerRun.WriterGeneration <= firstRun.WriterGeneration {
		t.Fatalf("new generation did not use the reopened writer generation: R1=%d R2=%d", firstRun.WriterGeneration, answerRun.WriterGeneration)
	}
	contextTerminalProof := func(_ context.Context, task domaintask.Task, run domaintask.Run, _ domaintask.SharedRoleContext, outcome domaintask.Status) error {
		r1, r1Run, _, r1Err := manager.readTaskMeetingState(ctx, firstTask.TaskID, firstRun.RunID, stateAuth)
		r2, r2Run, _, r2Err := manager.readTaskMeetingState(ctx, answerTask.TaskID, answerRun.RunID, stateAuth)
		if task.TaskID != contextTask.TaskID || run.RunID != contextRun.RunID || outcome != domaintask.StatusSucceeded || r1Err != nil || r2Err != nil ||
			r1.Status != domaintask.StatusSucceeded || r1Run.Status != domaintask.RunStatusSucceeded || r2.Status != domaintask.StatusSucceeded || r2Run.Status != domaintask.RunStatusSucceeded {
			return domaintask.ErrTaskMeetingConflict
		}
		return nil
	}
	if err := manager.CompleteTaskMeetingRecipientContext(ctx, contextTask.TaskID, contextRun.RunID, stateAuth, contextTerminalProof); err != nil {
		t.Fatalf("complete recipient context after reports: %v", err)
	}
	rootTerminalProof := func(_ context.Context, task domaintask.Task, run domaintask.Run, _ domaintask.SharedRoleContext, outcome domaintask.Status) error {
		closedContext, closedContextRun, _, readErr := manager.readTaskMeetingState(ctx, contextTask.TaskID, contextRun.RunID, stateAuth)
		if task.TaskID != root.TaskID || run.RunID != rootRun.RunID || outcome != domaintask.StatusSucceeded || readErr != nil || closedContext.Status != domaintask.StatusSucceeded || closedContextRun.Status != domaintask.RunStatusSucceeded {
			return domaintask.ErrTaskMeetingConflict
		}
		return nil
	}
	if err := manager.CompleteTaskMeetingRootInvocation(ctx, root.TaskID, rootRun.RunID, stateAuth, rootTerminalProof); err != nil {
		t.Fatalf("complete root after recipient cleanup: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close reopened Task store: %v", err)
	}
}

func TestTaskMeetingReturnedEffectErrorSettlesUnknownAndFreesCapacity(t *testing.T) {
	ctx := context.Background()
	path, err := os.MkdirTemp(".", ".taskmeeting-unknown-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(path) })
	store, err := taskpersistence.NewJSONLStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ownerStore := &taskMeetingUnknownFailOnceStore{JSONLStore: store, failuresRemaining: 1}
	manager := New(ownerStore, ParallelLimits{Global: 1})
	admit := func(context.Context, domaintask.TaskMeetingInvocationRequest, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext) error {
		return nil
	}
	receiptAuth := func(context.Context) error { return nil }
	stateAuth := func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext) error { return nil }
	rootRequest := taskMeetingRootRequest()
	root, rootRun, rootProof, err := manager.EnsureTaskMeetingRootInvocation(ctx, rootRequest, admit, receiptAuth, stateAuth)
	if err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	contextRequest := rootRequest
	contextRequest.Purpose, contextRequest.ActorID = domaintask.TaskMeetingPurposeRecipientContext, "mio"
	contextRequest.ParentTaskID, contextRequest.ParentRunID = root.TaskID, rootRun.RunID
	contextRequest.FrozenInput = []byte(`{"purpose":"recipient_context"}`)
	recipient, recipientRun, _, err := manager.EnsureTaskMeetingRecipientContext(ctx, contextRequest, rootProof, admit, stateAuth, receiptAuth, stateAuth)
	if err != nil {
		t.Fatalf("ensure recipient context: %v", err)
	}
	shared, err := manager.readTaskMeetingContext(ctx, recipient, recipientRun, stateAuth)
	if err != nil {
		t.Fatalf("read empty recipient context: %v", err)
	}
	contextHash, err := domaintask.TaskMeetingContextSHA256(shared)
	if err != nil {
		t.Fatal(err)
	}
	request := contextRequest
	request.Purpose, request.GenerationRequestID = domaintask.TaskMeetingPurposeReport, modulecore.NewRequestID()
	request.ParentTaskID, request.ParentRunID = recipient.TaskID, recipientRun.RunID
	request.GenerationScope = taskMeetingTestScope(1)
	request.FrozenInput, err = taskMeetingTestInput(t, request.GenerationScope, `{"prompt":"first report before utterances"}`)
	if err != nil {
		t.Fatal(err)
	}
	request.SourceContextSHA256 = contextHash
	generation, generationRun, err := manager.EnsureTaskMeetingGeneration(ctx, request, recipient.TaskID, recipientRun.RunID, admit, stateAuth, receiptAuth, stateAuth)
	if err != nil {
		t.Fatalf("ensure empty-context generation: %v", err)
	}
	effectCalls := 0
	authorizeEffect := func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext) error { return nil }
	effect := func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext, domaintask.SharedRoleContext, json.RawMessage) (json.RawMessage, error) {
		effectCalls++
		return nil, errors.New("remote request returned without a canonical result")
	}
	_, executeErr := manager.ExecuteTaskMeetingGeneration(ctx, generation.TaskID, generationRun.RunID, stateAuth, stateAuth, authorizeEffect, effect)
	if !errors.Is(executeErr, domaintask.ErrTaskMeetingExecutionUnknown) {
		t.Fatalf("returned effect error = %v, want durable unknown", executeErr)
	}
	if effectCalls != 1 {
		t.Fatalf("effect calls = %d, want one", effectCalls)
	}
	if ownerStore.unknownWrites != 2 {
		t.Fatalf("unknown receipt writes = %d, want one failed write plus one idempotent recovery write", ownerStore.unknownWrites)
	}
	failedGeneration, failedRun, _, err := manager.readTaskMeetingState(ctx, generation.TaskID, generationRun.RunID, stateAuth)
	if err != nil || failedGeneration.Status != domaintask.StatusFailed || failedRun.Status != domaintask.RunStatusFailed || failedGeneration.TaskMeetingResult != nil {
		t.Fatalf("unknown generation terminal state=(%s,%s,result=%v) executeErr=%v readErr=%v", failedGeneration.Status, failedRun.Status, failedGeneration.TaskMeetingResult, executeErr, err)
	}
	unknown, err := manager.GetTaskMeetingGenerationUnknown(ctx, generation.TaskID, generationRun.RunID, stateAuth)
	if err != nil || unknown.Reason != domaintask.TaskMeetingGenerationUnknownReasonEffectReturned || unknown.TaskID != generation.TaskID || unknown.RunID != generationRun.RunID {
		t.Fatalf("unknown receipt=%+v err=%v", unknown, err)
	}
	fenceEvidence, err := store.TaskMeetingExecutionFenceEvidence(ctx, generation.TaskID, generationRun.RunID, stateAuth)
	if err != nil || fenceEvidence.Active || fenceEvidence.FenceID == "" || fenceEvidence.ReleaseWriterGeneration == 0 {
		t.Fatalf("transient settlement recovery did not release durable fence: evidence=%+v err=%v", fenceEvidence, err)
	}
	active := -1
	err = store.ReadTransaction(ctx, func(reader domaintask.Store) error {
		counter, ok := reader.(domaintask.TaskMeetingActiveGenerationCounter)
		if !ok {
			return domaintask.ErrTaskMeetingOwnerRequired
		}
		var countErr error
		active, countErr = counter.CountTaskMeetingActiveGenerations(ctx)
		return countErr
	})
	if err != nil || active != 0 {
		t.Fatalf("active meeting generations after unknown settlement=%d err=%v, want zero", active, err)
	}
	ordinary, ordinaryRun, err := manager.CreateAndStartRun(ctx, domaintask.Task{TaskID: modulecore.NewTaskID(), Title: "ordinary task after unknown", Route: domaintask.RouteGeneral, Assignee: "mio", ReadOnly: true}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatalf("unknown generation retained global capacity: %v", err)
	}
	if _, err := manager.Fail(ctx, ordinary.TaskID, "capacity probe complete", nil); err != nil {
		t.Fatalf("close capacity probe Task %s/%s: %v", ordinary.TaskID, ordinaryRun.RunID, err)
	}
	if _, err := manager.ExecuteTaskMeetingGeneration(ctx, generation.TaskID, generationRun.RunID, stateAuth, stateAuth, authorizeEffect, effect); !errors.Is(err, domaintask.ErrTaskMeetingExecutionUnknown) || effectCalls != 1 {
		t.Fatalf("same request replay result error=%v effectCalls=%d, want unknown and no second effect", err, effectCalls)
	}
	contextFailureProof := func(_ context.Context, task domaintask.Task, run domaintask.Run, _ domaintask.SharedRoleContext, outcome domaintask.Status) error {
		current, currentRun, _, readErr := manager.readTaskMeetingState(ctx, generation.TaskID, generationRun.RunID, stateAuth)
		if task.TaskID != recipient.TaskID || run.RunID != recipientRun.RunID || outcome != domaintask.StatusFailed || readErr != nil || current.Status != domaintask.StatusFailed || currentRun.Status != domaintask.RunStatusFailed {
			return domaintask.ErrTaskMeetingConflict
		}
		return nil
	}
	if err := manager.FailTaskMeetingRecipientContext(ctx, recipient.TaskID, recipientRun.RunID, stateAuth, contextFailureProof); err != nil {
		t.Fatalf("fail recipient context after child settlement: %v", err)
	}
	rootFailureProof := func(_ context.Context, task domaintask.Task, run domaintask.Run, _ domaintask.SharedRoleContext, outcome domaintask.Status) error {
		closedContext, closedRun, _, readErr := manager.readTaskMeetingState(ctx, recipient.TaskID, recipientRun.RunID, stateAuth)
		if task.TaskID != root.TaskID || run.RunID != rootRun.RunID || outcome != domaintask.StatusFailed || readErr != nil || closedContext.Status != domaintask.StatusFailed || closedRun.Status != domaintask.RunStatusFailed {
			return domaintask.ErrTaskMeetingConflict
		}
		return nil
	}
	if err := manager.FailTaskMeetingRootInvocation(ctx, root.TaskID, rootRun.RunID, stateAuth, rootFailureProof); err != nil {
		t.Fatalf("fail root after recipient cleanup: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close before unknown replay: %v", err)
	}
	store, err = taskpersistence.NewJSONLStore(path)
	if err != nil {
		t.Fatalf("reopen unknown store: %v", err)
	}
	defer store.Close()
	manager = New(store, ParallelLimits{Global: 1})
	if _, err := manager.GetTaskMeetingGenerationUnknown(ctx, generation.TaskID, generationRun.RunID, stateAuth); err != nil {
		t.Fatalf("read unknown receipt after reopen: %v", err)
	}
	if _, err := manager.ExecuteTaskMeetingGeneration(ctx, generation.TaskID, generationRun.RunID, stateAuth, stateAuth, authorizeEffect, effect); !errors.Is(err, domaintask.ErrTaskMeetingExecutionUnknown) || effectCalls != 1 {
		t.Fatalf("reopened same request error=%v effectCalls=%d, want unknown and no second effect", err, effectCalls)
	}
}

type taskMeetingUnknownFailOnceStore struct {
	*taskpersistence.JSONLStore
	unknownWrites     int
	failuresRemaining int
}

func (s *taskMeetingUnknownFailOnceStore) RecordTaskMeetingGenerationUnknown(ctx context.Context, reference domaintask.TaskMeetingGenerationUnknownReference, authorize domaintask.TaskMeetingStateAuthorizationCheck) error {
	s.unknownWrites++
	if s.failuresRemaining > 0 {
		s.failuresRemaining--
		return errors.New("injected transient unknown receipt write failure")
	}
	return s.JSONLStore.RecordTaskMeetingGenerationUnknown(ctx, reference, authorize)
}

func TestTaskMeetingPersistentSettlementFailureKeepsStickyFenceAcrossRetryAndReopen(t *testing.T) {
	ctx := context.Background()
	path, err := os.MkdirTemp(".", ".taskmeeting-sticky-unknown-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(path) })
	base, err := taskpersistence.NewJSONLStore(path)
	if err != nil {
		t.Fatal(err)
	}
	store := &taskMeetingUnknownFailOnceStore{JSONLStore: base, failuresRemaining: 2}
	manager := New(store, ParallelLimits{Global: 1})
	allowAdmission := func(context.Context, domaintask.TaskMeetingInvocationRequest, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext) error {
		return nil
	}
	allowReceipt := func(context.Context) error { return nil }
	allowState := func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext) error { return nil }
	rootRequest := taskMeetingRootRequest()
	root, rootRun, rootProof, err := manager.EnsureTaskMeetingRootInvocation(ctx, rootRequest, allowAdmission, allowReceipt, allowState)
	if err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	contextRequest := rootRequest
	contextRequest.Purpose, contextRequest.ActorID = domaintask.TaskMeetingPurposeRecipientContext, "mio"
	contextRequest.ParentTaskID, contextRequest.ParentRunID = root.TaskID, rootRun.RunID
	contextRequest.FrozenInput = []byte(`{"purpose":"recipient_context"}`)
	recipient, recipientRun, _, err := manager.EnsureTaskMeetingRecipientContext(ctx, contextRequest, rootProof, allowAdmission, allowState, allowReceipt, allowState)
	if err != nil {
		t.Fatalf("ensure recipient context: %v", err)
	}
	shared, err := manager.readTaskMeetingContext(ctx, recipient, recipientRun, allowState)
	if err != nil {
		t.Fatalf("read recipient context: %v", err)
	}
	contextHash, err := domaintask.TaskMeetingContextSHA256(shared)
	if err != nil {
		t.Fatal(err)
	}
	request := contextRequest
	request.Purpose, request.GenerationRequestID = domaintask.TaskMeetingPurposeReport, modulecore.NewRequestID()
	request.ParentTaskID, request.ParentRunID = recipient.TaskID, recipientRun.RunID
	request.GenerationScope = taskMeetingTestScope(1)
	request.FrozenInput, err = taskMeetingTestInput(t, request.GenerationScope, `{"prompt":"sticky unknown"}`)
	if err != nil {
		t.Fatal(err)
	}
	request.SourceContextSHA256 = contextHash
	generation, generationRun, err := manager.EnsureTaskMeetingGeneration(ctx, request, recipient.TaskID, recipientRun.RunID, allowAdmission, allowState, allowReceipt, allowState)
	if err != nil {
		t.Fatalf("ensure generation: %v", err)
	}
	effectCalls := 0
	authorizeEffect := func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext) error { return nil }
	effect := func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext, domaintask.SharedRoleContext, json.RawMessage) (json.RawMessage, error) {
		effectCalls++
		return nil, errors.New("effect callback returned without output")
	}
	if _, err := manager.ExecuteTaskMeetingGeneration(ctx, generation.TaskID, generationRun.RunID, allowState, allowState, authorizeEffect, effect); !errors.Is(err, domaintask.ErrTaskMeetingExecutionUnknown) {
		t.Fatalf("first execute error=%v, want unknown after callback return", err)
	}
	if store.unknownWrites != 2 || effectCalls != 1 {
		t.Fatalf("settlement writes=%d effect calls=%d, want two writes and one effect", store.unknownWrites, effectCalls)
	}
	if _, _, _, err := manager.readTaskMeetingState(ctx, generation.TaskID, generationRun.RunID, allowState); err != nil {
		t.Fatalf("read running generation: %v", err)
	} else {
		current, currentRun, _, _ := manager.readTaskMeetingState(ctx, generation.TaskID, generationRun.RunID, allowState)
		if current.Status != domaintask.StatusRunning || currentRun.Status != domaintask.RunStatusRunning || current.TaskMeetingResult != nil {
			t.Fatalf("persistent settlement failure falsely completed generation: Task=%s Run=%s", current.Status, currentRun.Status)
		}
	}
	if _, err := manager.ExecuteTaskMeetingGeneration(ctx, generation.TaskID, generationRun.RunID, allowState, allowState, authorizeEffect, effect); err == nil || effectCalls != 1 {
		t.Fatalf("retry after failed settlement error=%v effect calls=%d; want blocked retry and no second effect", err, effectCalls)
	}
	fenceEvidence, err := base.TaskMeetingExecutionFenceEvidence(ctx, generation.TaskID, generationRun.RunID, allowState)
	if err != nil || !fenceEvidence.Active || fenceEvidence.FenceID == "" || fenceEvidence.AcquireWriterGeneration == 0 {
		t.Fatalf("persistent settlement failure did not retain durable fence: evidence=%+v err=%v", fenceEvidence, err)
	}
	if err := base.Close(); err != nil {
		t.Fatalf("close store with sticky fence: %v", err)
	}
	reopened, err := taskpersistence.NewJSONLStore(path)
	if err != nil {
		t.Fatalf("reopen sticky fence: %v", err)
	}
	defer reopened.Close()
	manager = New(reopened, ParallelLimits{Global: 1})
	if _, err := manager.ExecuteTaskMeetingGeneration(ctx, generation.TaskID, generationRun.RunID, allowState, allowState, authorizeEffect, effect); err == nil || effectCalls != 1 {
		t.Fatalf("reopened unknown retry error=%v effect calls=%d; want blocked retry and no second effect", err, effectCalls)
	}
	fenceEvidence, err = reopened.TaskMeetingExecutionFenceEvidence(ctx, generation.TaskID, generationRun.RunID, allowState)
	if err != nil || !fenceEvidence.Active || fenceEvidence.FenceID == "" || !fenceEvidence.RecoveryRequired {
		t.Fatalf("reopen lost durable sticky fence: evidence=%+v err=%v", fenceEvidence, err)
	}
	if _, _, _, err := manager.readTaskMeetingState(ctx, generation.TaskID, generationRun.RunID, allowState); err != nil {
		t.Fatalf("read reopened generation: %v", err)
	}
	ordinary := domaintask.Task{TaskID: modulecore.NewTaskID(), Title: "ordinary task stays blocked", Route: domaintask.RouteGeneral, Assignee: "mio", ReadOnly: true}
	if _, _, err := manager.CreateAndStartRun(ctx, ordinary, domaintask.SharedRoleContext{}); !errors.Is(err, ErrParallelLimit) {
		t.Fatalf("capacity released without durable unknown terminalization: %v", err)
	}
}

func TestTaskMeetingUnknownReceiptRecoversFenceAfterReleaseFailureAndReopen(t *testing.T) {
	ctx := context.Background()
	path, err := os.MkdirTemp(".", ".taskmeeting-unknown-release-recovery-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(path) })
	base, err := taskpersistence.NewJSONLStore(path)
	if err != nil {
		t.Fatal(err)
	}
	fencePath := filepath.Join(path, "task_execution_fences.jsonl")
	backupPath := filepath.Join(path, "task_execution_fences.saved")
	store := &taskMeetingUnknownReleaseFailureStore{JSONLStore: base, fencePath: fencePath, backupPath: backupPath}
	manager := New(store, ParallelLimits{Global: 1})
	allowAdmission := func(context.Context, domaintask.TaskMeetingInvocationRequest, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext) error {
		return nil
	}
	allowReceipt := func(context.Context) error { return nil }
	allowState := func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext) error { return nil }
	rootRequest := taskMeetingRootRequest()
	root, rootRun, rootProof, err := manager.EnsureTaskMeetingRootInvocation(ctx, rootRequest, allowAdmission, allowReceipt, allowState)
	if err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	contextRequest := rootRequest
	contextRequest.Purpose, contextRequest.ActorID = domaintask.TaskMeetingPurposeRecipientContext, "mio"
	contextRequest.ParentTaskID, contextRequest.ParentRunID = root.TaskID, rootRun.RunID
	contextRequest.FrozenInput = []byte(`{"purpose":"recipient_context"}`)
	recipient, recipientRun, _, err := manager.EnsureTaskMeetingRecipientContext(ctx, contextRequest, rootProof, allowAdmission, allowState, allowReceipt, allowState)
	if err != nil {
		t.Fatalf("ensure recipient context: %v", err)
	}
	shared, err := manager.readTaskMeetingContext(ctx, recipient, recipientRun, allowState)
	if err != nil {
		t.Fatalf("read recipient context: %v", err)
	}
	contextHash, err := domaintask.TaskMeetingContextSHA256(shared)
	if err != nil {
		t.Fatal(err)
	}
	request := contextRequest
	request.Purpose, request.GenerationRequestID = domaintask.TaskMeetingPurposeReport, modulecore.NewRequestID()
	request.ParentTaskID, request.ParentRunID = recipient.TaskID, recipientRun.RunID
	request.GenerationScope = taskMeetingTestScope(1)
	request.FrozenInput, err = taskMeetingTestInput(t, request.GenerationScope, `{"prompt":"release failure"}`)
	if err != nil {
		t.Fatal(err)
	}
	request.SourceContextSHA256 = contextHash
	generation, generationRun, err := manager.EnsureTaskMeetingGeneration(ctx, request, recipient.TaskID, recipientRun.RunID, allowAdmission, allowState, allowReceipt, allowState)
	if err != nil {
		t.Fatalf("ensure generation: %v", err)
	}
	effectCalls := 0
	effect := func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext, domaintask.SharedRoleContext, json.RawMessage) (json.RawMessage, error) {
		effectCalls++
		return nil, errors.New("effect returned without canonical output")
	}
	if _, err := manager.ExecuteTaskMeetingGeneration(ctx, generation.TaskID, generationRun.RunID, allowState, allowState, func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext) error { return nil }, effect); !errors.Is(err, domaintask.ErrTaskMeetingExecutionUnknown) {
		t.Fatalf("execution after release write failure = %v, want unknown", err)
	}
	if !store.fenceHidden {
		t.Fatal("fault did not hide the fence log before release")
	}
	if err := os.Remove(fencePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backupPath, fencePath); err != nil {
		t.Fatal(err)
	}
	store.fenceHidden = false
	evidence, err := base.TaskMeetingExecutionFenceEvidence(ctx, generation.TaskID, generationRun.RunID, allowState)
	if err != nil || !evidence.Active {
		t.Fatalf("fence after failed release: %+v err=%v", evidence, err)
	}
	unknown, err := manager.GetTaskMeetingGenerationUnknown(ctx, generation.TaskID, generationRun.RunID, allowState)
	if err != nil {
		t.Fatalf("get terminal unknown: %v", err)
	}
	failed, failedRun, _, err := manager.readTaskMeetingState(ctx, generation.TaskID, generationRun.RunID, allowState)
	if err != nil || failed.Status != domaintask.StatusFailed || failedRun.Status != domaintask.RunStatusFailed {
		t.Fatalf("failed state=(%s,%s) err=%v", failed.Status, failedRun.Status, err)
	}
	if err := base.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := taskpersistence.NewJSONLStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	manager = New(reopened, ParallelLimits{Global: 1})
	wrong := unknown
	wrong.ActorID = "shiro"
	if err := manager.RecoverTaskMeetingUnknownGenerationFence(ctx, generation.TaskID, generationRun.RunID, wrong, allowState); !errors.Is(err, domaintask.ErrTaskMeetingConflict) {
		t.Fatalf("wrong unknown receipt recovery = %v, want conflict", err)
	}
	if err := manager.RecoverTaskMeetingUnknownGenerationFence(ctx, generation.TaskID, generationRun.RunID, unknown, allowState); err != nil {
		t.Fatalf("recover exact ended unknown fence: %v", err)
	}
	evidence, err = reopened.TaskMeetingExecutionFenceEvidence(ctx, generation.TaskID, generationRun.RunID, allowState)
	if err != nil || evidence.Active || evidence.ReleaseWriterGeneration == 0 {
		t.Fatalf("recovered fence evidence=%+v err=%v", evidence, err)
	}
	if _, err := manager.ExecuteTaskMeetingGeneration(ctx, generation.TaskID, generationRun.RunID, allowState, allowState, func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext) error { return nil }, effect); !errors.Is(err, domaintask.ErrTaskMeetingExecutionUnknown) || effectCalls != 1 {
		t.Fatalf("replay after fence recovery error=%v effect calls=%d; want unknown and one effect", err, effectCalls)
	}
}

type taskMeetingUnknownReleaseFailureStore struct {
	*taskpersistence.JSONLStore
	fencePath, backupPath string
	fenceHidden           bool
}

func (s *taskMeetingUnknownReleaseFailureStore) RecordTaskMeetingGenerationUnknown(ctx context.Context, reference domaintask.TaskMeetingGenerationUnknownReference, authorize domaintask.TaskMeetingStateAuthorizationCheck) error {
	if err := s.JSONLStore.RecordTaskMeetingGenerationUnknown(ctx, reference, authorize); err != nil {
		return err
	}
	if !s.fenceHidden {
		if err := os.Rename(s.fencePath, s.backupPath); err != nil {
			return err
		}
		if err := os.Mkdir(s.fencePath, 0o700); err != nil {
			return err
		}
		s.fenceHidden = true
	}
	return nil
}

func TestTaskMeetingControlsDoNotConsumeButGenerationUsesGlobalCapacity(t *testing.T) {
	ctx := context.Background()
	path, err := os.MkdirTemp(".", ".taskmeeting-capacity-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(path) })
	store, err := taskpersistence.NewJSONLStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager := New(store, ParallelLimits{Global: 2})
	admit := func(context.Context, domaintask.TaskMeetingInvocationRequest, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext) error {
		return nil
	}
	receiptAuth := func(context.Context) error { return nil }
	stateAuth := func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext) error { return nil }
	rootRequest := taskMeetingRootRequest()
	root, rootRun, rootProof, err := manager.EnsureTaskMeetingRootInvocation(ctx, rootRequest, admit, receiptAuth, stateAuth)
	if err != nil {
		t.Fatalf("ensure root control: %v", err)
	}
	contextRequest := rootRequest
	contextRequest.Purpose, contextRequest.ActorID = domaintask.TaskMeetingPurposeRecipientContext, "mio"
	contextRequest.ParentTaskID, contextRequest.ParentRunID = root.TaskID, rootRun.RunID
	contextRequest.FrozenInput = []byte(`{"purpose":"recipient_context"}`)
	recipient, recipientRun, _, err := manager.EnsureTaskMeetingRecipientContext(ctx, contextRequest, rootProof, admit, stateAuth, receiptAuth, stateAuth)
	if err != nil {
		t.Fatalf("ensure recipient control: %v", err)
	}
	shared, err := manager.readTaskMeetingContext(ctx, recipient, recipientRun, stateAuth)
	if err != nil {
		t.Fatalf("read recipient context: %v", err)
	}
	contextHash, err := domaintask.TaskMeetingContextSHA256(shared)
	if err != nil {
		t.Fatal(err)
	}
	request := contextRequest
	request.Purpose, request.GenerationRequestID = domaintask.TaskMeetingPurposeReport, modulecore.NewRequestID()
	request.ParentTaskID, request.ParentRunID = recipient.TaskID, recipientRun.RunID
	request.GenerationScope = taskMeetingTestScope(1)
	request.FrozenInput, err = taskMeetingTestInput(t, request.GenerationScope, `{"prompt":"first report"}`)
	if err != nil {
		t.Fatal(err)
	}
	request.SourceContextSHA256 = contextHash
	first, firstRun, err := manager.EnsureTaskMeetingGeneration(ctx, request, recipient.TaskID, recipientRun.RunID, admit, stateAuth, receiptAuth, stateAuth)
	if err != nil {
		t.Fatalf("controls incorrectly consumed capacity: %v", err)
	}
	replayed, replayRun, err := manager.EnsureTaskMeetingGeneration(ctx, request, recipient.TaskID, recipientRun.RunID, admit, stateAuth, receiptAuth, stateAuth)
	if err != nil || replayed.TaskID != first.TaskID || replayRun.RunID != firstRun.RunID {
		t.Fatalf("exact generation replay changed receipt or failed: Task=%s Run=%s err=%v", replayed.TaskID, replayRun.RunID, err)
	}
	ordinary := domaintask.Task{TaskID: modulecore.NewTaskID(), Title: "ordinary Mio task", Route: domaintask.RouteGeneral, Assignee: "mio", ReadOnly: true}
	if _, _, err := manager.CreateAndStartRun(ctx, ordinary, domaintask.SharedRoleContext{}); err != nil {
		t.Fatalf("one ordinary run should fit alongside one meeting generation: %v", err)
	}
	nextRequest := request
	nextRequest.GenerationRequestID = modulecore.NewRequestID()
	nextRequest.GenerationScope = taskMeetingTestScope(2)
	nextRequest.FrozenInput, err = taskMeetingTestInput(t, nextRequest.GenerationScope, `{"prompt":"second report"}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.EnsureTaskMeetingGeneration(ctx, nextRequest, recipient.TaskID, recipientRun.RunID, admit, stateAuth, receiptAuth, stateAuth); !errors.Is(err, ErrParallelLimit) {
		t.Fatalf("meeting generation did not observe active ordinary Mio run: %v", err)
	}
	third := domaintask.Task{TaskID: modulecore.NewTaskID(), Title: "capacity boundary task", Route: domaintask.RouteGeneral, Assignee: "aka", ReadOnly: true}
	if _, _, err := manager.CreateAndStartRun(ctx, third, domaintask.SharedRoleContext{}); !errors.Is(err, ErrParallelLimit) {
		t.Fatalf("ordinary task did not observe active meeting generation capacity: %v", err)
	}
}

func taskMeetingTestScope(generation uint64) domaintask.TaskMeetingGenerationScope {
	return domaintask.TaskMeetingGenerationScope{ScopeEvidenceID: modulecore.NewEvidenceID(), Generation: generation, Epoch: 1}
}

func taskMeetingTestInput(t *testing.T, scope domaintask.TaskMeetingGenerationScope, payload string) ([]byte, error) {
	t.Helper()
	return domaintask.EncodeTaskMeetingGenerationInput(scope, []byte(payload))
}
