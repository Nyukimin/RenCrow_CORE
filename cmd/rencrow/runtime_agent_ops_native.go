package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/nativeharnessclient"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const agentOpsNativeFinalizationTimeout = 60 * time.Second

// agentOpsNativeResponse contains only durable claim and lifecycle projection.
// Final Harness text is returned only to the creator after verified success;
// replay responses never synthesize cached output or verification.
type agentOpsNativeResponse struct {
	RequestID   string `json:"request_id"`
	TaskID      string `json:"task_id,omitempty"`
	RunID       string `json:"run_id,omitempty"`
	ClaimStatus string `json:"claim_status"`
	TaskStatus  string `json:"task_status,omitempty"`
	RunStatus   string `json:"run_status,omitempty"`
	Error       string `json:"error,omitempty"`
	Output      string `json:"output,omitempty"`
}

func deriveAgentOpsShiroContext(parentContext context.Context, requestID string) (context.Context, error) {
	return domaintool.DeriveAgentToolExecutionScope(
		parentContext,
		requestID,
		"shiro",
		"worker",
		"ops",
		true,
	)
}

type agentOpsNativeResumeRuntime interface {
	PrepareNativeOPSResumeSource(context.Context, domaintask.Task, modulecore.RunID) (domaintask.NativeOPSResumeSource, error)
	ExecuteNativeOPSResume(context.Context, domaintask.Task, domaintask.Run, domaintask.NativeOPSResumeClaim) (domainagent.NativeCodingResult, error)
	ReconcileNativeOPSResume(context.Context, domaintask.Task, domaintask.Run, domaintask.NativeOPSResumeClaim) (domainagent.NativeCodingResult, bool, error)
}

func (h *agentOpsHandler) serveNativeOPSResume(w http.ResponseWriter, parentContext context.Context, requestID string, target agentOpsNativeResumeTarget) {
	if h.taskOwner == nil || h.nativeCoding == nil {
		writeAgentOpsError(w, http.StatusServiceUnavailable, "runtime_unavailable")
		return
	}
	runtime, ok := h.nativeCoding.(agentOpsNativeResumeRuntime)
	if !ok {
		writeAgentOpsError(w, http.StatusServiceUnavailable, "runtime_unavailable")
		return
	}
	releaseWorkerBusy := h.acquireWorkerBusyLease()
	defer releaseWorkerBusy()

	task, err := h.taskOwner.Get(parentContext, target.TaskID)
	if err != nil || task.TaskID != target.TaskID {
		writeAgentOpsError(w, http.StatusConflict, "resume_target_unavailable")
		return
	}
	var source domaintask.NativeOPSResumeSource
	if saved, found := savedNativeOPSResumeClaim(task, requestID); found {
		source = saved.Source
	} else {
		source, err = runtime.PrepareNativeOPSResumeSource(parentContext, task, target.ExpectedCoreRunID)
		if err != nil {
			writeAgentOpsError(w, http.StatusConflict, "resume_source_unavailable")
			return
		}
	}
	claim, err := h.taskOwner.AdmitNativeOPSResume(parentContext, taskmanager.NativeOPSResumeInput{
		TaskID: target.TaskID, ExpectedCoreRunID: target.ExpectedCoreRunID, Source: source,
	})
	if err != nil {
		if errors.Is(err, taskmanager.ErrNativeOPSResumeRejected) {
			writeAgentOpsError(w, http.StatusConflict, "resume_request_rejected")
		} else {
			writeAgentOpsError(w, http.StatusServiceUnavailable, "runtime_unavailable")
		}
		return
	}
	if claim.Task.TaskID != target.TaskID || claim.Run.TaskID != target.TaskID || claim.Run.RunID != claim.Claim.NewCoreRunID ||
		claim.Run.TraceID != claim.Claim.TraceID || claim.Claim.ExpectedCoreRunID != target.ExpectedCoreRunID ||
		claim.Claim.OwnerUserID != h.userID || claim.Claim.Validate(claim.Task) != nil {
		writeAgentOpsError(w, http.StatusServiceUnavailable, "runtime_unavailable")
		return
	}
	if claim.MayExecute && (claim.Status != taskmanager.NativeOPSResumeClaimed || claim.Run.Status != domaintask.RunStatusRunning || claim.Task.Status != domaintask.StatusRunning) {
		writeAgentOpsError(w, http.StatusServiceUnavailable, "runtime_unavailable")
		return
	}
	if !claim.MayExecute && claim.Status != taskmanager.NativeOPSResumeOutcomeUnknown {
		writeAgentOpsError(w, http.StatusServiceUnavailable, "runtime_unavailable")
		return
	}
	shiroContext, err := deriveAgentOpsShiroContext(parentContext, requestID)
	if err != nil {
		writeAgentOpsError(w, http.StatusInternalServerError, "runtime_unavailable")
		return
	}
	executionContext, err := domainexecution.WithIdentity(shiroContext, claim.Task.TaskID, claim.Run.RunID, claim.Run.TraceID)
	if err != nil {
		writeAgentOpsError(w, http.StatusInternalServerError, "runtime_unavailable")
		return
	}

	var result domainagent.NativeCodingResult
	var done bool
	if claim.MayExecute {
		result, err = runtime.ExecuteNativeOPSResume(executionContext, claim.Task, claim.Run, claim.Claim)
		done = err == nil
	} else {
		result, done, err = runtime.ReconcileNativeOPSResume(executionContext, claim.Task, claim.Run, claim.Claim)
	}
	if errors.Is(err, domainagent.ErrNativeCodingOutcomeUnknown) || !claim.MayExecute && !done {
		writeNativeOPSResumeUnresolved(w, http.StatusAccepted, requestID, string(taskmanager.NativeOPSResumeOutcomeUnknown), claim.Task, claim.Run)
		return
	}
	if err != nil && !errors.Is(err, domainagent.ErrNativeCodingRejected) {
		writeNativeOPSResumeUnresolved(w, http.StatusServiceUnavailable, requestID, "blocked", claim.Task, claim.Run)
		return
	}
	if err != nil {
		result = domainagent.NativeCodingResult{}
	}
	status := domaintask.StatusFailed
	summary := "native OPS Resume was not accepted"
	if err == nil && result.Status == domainagent.NativeRunCancelled {
		status = domaintask.StatusCancelled
		summary = "native OPS Resume was cancelled"
	}
	if err == nil && result.Accepted() {
		status = domaintask.StatusSucceeded
		summary = "native OPS Resume completed and verification passed"
	}
	completed, run, finishErr := h.finishNativeOPSResume(executionContext, claim.Task, claim.Run, claim.Claim, status, summary)
	if finishErr != nil {
		writeNativeOPSResumeUnresolved(w, http.StatusServiceUnavailable, requestID, "blocked", claim.Task, claim.Run)
		return
	}
	if status != domaintask.StatusSucceeded {
		writeNativeOPSFinalResponse(w, http.StatusInternalServerError, requestID, "claimed", completed, run)
		return
	}
	writeJSONStatus(w, http.StatusOK, agentOpsNativeResponse{
		RequestID: requestID, TaskID: completed.TaskID.String(), RunID: string(run.RunID),
		ClaimStatus: string(taskmanager.NativeOPSResumeClaimed), TaskStatus: string(completed.Status),
		RunStatus: string(run.Status), Output: result.FinalText,
	})
}

func savedNativeOPSResumeClaim(task domaintask.Task, requestID string) (domaintask.NativeOPSResumeClaim, bool) {
	for _, claim := range task.NativeResumeClaims {
		if claim.RequestID == requestID {
			return claim, true
		}
	}
	return domaintask.NativeOPSResumeClaim{}, false
}

func writeNativeOPSResumeUnresolved(w http.ResponseWriter, status int, requestID, claimStatus string, task domaintask.Task, run domaintask.Run) {
	writeJSONStatus(w, status, agentOpsNativeResponse{
		RequestID: requestID, TaskID: task.TaskID.String(), RunID: string(run.RunID),
		ClaimStatus: claimStatus, TaskStatus: string(task.Status), RunStatus: string(run.Status),
	})
}

func (h *agentOpsHandler) finishNativeOPSResume(
	ctx context.Context,
	task domaintask.Task,
	run domaintask.Run,
	claim domaintask.NativeOPSResumeClaim,
	status domaintask.Status,
	summary string,
) (domaintask.Task, domaintask.Run, error) {
	if h.taskOwner == nil || task.TaskID != run.TaskID || run.RunID != claim.NewCoreRunID || run.TraceID != claim.TraceID ||
		run.WriterGeneration != claim.WriterGeneration || run.Assignee != "shiro" || task.Assignee != "shiro" {
		return domaintask.Task{}, domaintask.Run{}, errors.New("native OPS Resume finalization identity mismatch")
	}
	terminalContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), agentOpsNativeFinalizationTimeout)
	defer cancel()
	completed, completeErr := h.taskOwner.CompleteNativeOPSResumeRun(
		terminalContext, task.TaskID, run.RunID, "shiro", claim, status, summary, "",
	)
	if completeErr != nil {
		if verifyErr := h.taskOwner.VerifyNativeOPSResumeReplay(terminalContext, task.TaskID, run.RunID, "shiro", claim, status); verifyErr != nil {
			return domaintask.Task{}, domaintask.Run{}, completeErr
		}
		completed, completeErr = h.taskOwner.Get(terminalContext, task.TaskID)
		if completeErr != nil {
			return domaintask.Task{}, domaintask.Run{}, completeErr
		}
	}
	storedRun, err := h.taskOwner.GetRun(terminalContext, run.RunID)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, err
	}
	expectedRunStatus := domaintask.RunStatusFailed
	switch status {
	case domaintask.StatusSucceeded:
		expectedRunStatus = domaintask.RunStatusSucceeded
	case domaintask.StatusCancelled:
		expectedRunStatus = domaintask.RunStatusCancelled
	case domaintask.StatusWaiting:
		expectedRunStatus = domaintask.RunStatusWaiting
	}
	if completed.TaskID != task.TaskID || completed.Status != status || storedRun.TaskID != task.TaskID || storedRun.RunID != run.RunID ||
		storedRun.TraceID != claim.TraceID || storedRun.WriterGeneration != claim.WriterGeneration || storedRun.Status != expectedRunStatus {
		return domaintask.Task{}, domaintask.Run{}, errors.New("native OPS Resume finalization did not persist the expected result")
	}
	if err := h.taskOwner.VerifyNativeOPSResumeReplay(terminalContext, task.TaskID, run.RunID, "shiro", claim, status); err != nil {
		return domaintask.Task{}, domaintask.Run{}, err
	}
	return completed, storedRun, nil
}

func (h *agentOpsHandler) serveAcceptedNativeOPS(w http.ResponseWriter, parentContext context.Context, requestID, rawMessage string, origin conversation.AcceptedOPSInputOrigin) {
	if h.acceptedOPSInputStore == nil || h.taskOwner == nil || h.nativeCoding == nil || h.executor == nil {
		writeAgentOpsError(w, http.StatusInternalServerError, "runtime_unavailable")
		return
	}
	candidate := conversation.AcceptedOPSInputRequest{
		RequestID:      requestID,
		OwnerID:        h.userID,
		ActorID:        h.userID,
		SessionID:      modulecore.NewSessionID(),
		FirstThreadID:  modulecore.NewThreadID(),
		TaskID:         modulecore.NewTaskID(),
		TurnID:         modulecore.NewTurnID(),
		TraceID:        modulecore.NewTraceID(),
		UserMessageID:  modulecore.NewMessageID(),
		AgentMessageID: modulecore.NewMessageID(),
		RawMessage:     rawMessage,
		DeclaredOrigin: origin,
	}

	accepted, err := h.acceptedOPSInputStore.AcceptOPSInput(parentContext, candidate)
	if err != nil {
		writeAcceptedOPSOwnerError(w, err)
		return
	}
	if err := validateAcceptedAgentOPSReceipt(accepted, candidate, accepted.IdempotentReplay); err != nil {
		writeAgentOpsError(w, http.StatusConflict, "accepted_input_conflict")
		return
	}

	read, err := h.acceptedOPSInputStore.ReadAcceptedOPSInput(parentContext, conversation.AcceptedOPSInputReadRequest{
		RequestID: requestID,
		OwnerID:   h.userID,
	})
	if err != nil {
		writeAcceptedOPSOwnerError(w, err)
		return
	}
	if !sameAcceptedOPSReceipt(accepted, read.Receipt) || read.RawMessage != rawMessage || validateAcceptedAgentOPSReceipt(read.Receipt, candidate, accepted.IdempotentReplay) != nil {
		writeAgentOpsError(w, http.StatusConflict, "accepted_input_conflict")
		return
	}
	acceptedInputReader, err := newAgentOpsAcceptedInputReader(parentContext, h.acceptedOPSInputStore, read)
	if err != nil {
		writeAcceptedOPSOwnerError(w, err)
		return
	}

	claim, err := h.taskOwner.AdmitAcceptedOPS(parentContext, read.Receipt, conversation.BackendShiroNativeCodingV1)
	if err != nil {
		status, code := http.StatusServiceUnavailable, "runtime_unavailable"
		if errors.Is(err, taskmanager.ErrAcceptedOPSClaimRejected) {
			status, code = http.StatusConflict, "accepted_task_conflict"
		}
		writeAgentOpsError(w, status, code)
		return
	}
	if !claim.MayExecute {
		response := nativeOPSClaimResponse(requestID, claim)
		status := http.StatusOK
		if claim.Status == taskmanager.AcceptedOPSAlreadyRunning || claim.Status == taskmanager.AcceptedOPSOutcomeUnknown {
			status = http.StatusAccepted
		} else if claim.Status == taskmanager.AcceptedOPSBlocked {
			status = http.StatusServiceUnavailable
		}
		writeJSONStatus(w, status, response)
		return
	}
	if claim.Status != taskmanager.AcceptedOPSClaimed || claim.Task.TaskID != read.Receipt.TaskID || claim.Run.TaskID != read.Receipt.TaskID || claim.Run.Status != domaintask.RunStatusRunning {
		writeAgentOpsError(w, http.StatusInternalServerError, "runtime_unavailable")
		return
	}

	address, err := conversation.NewChannelAddress(agentOpsTaskChannel, agentOpsTaskChatID)
	if err != nil {
		h.finishNativeOPSFailure(parentContext, requestID, read.Receipt, claim, domaintask.StatusFailed, "native coding input reconstruction failed")
		writeAgentOpsError(w, http.StatusInternalServerError, "runtime_unavailable")
		return
	}
	input, err := conversation.ReconstructTurnInput(
		read.Receipt.TaskID,
		read.Receipt.TurnID,
		read.Receipt.TraceID,
		read.Receipt.UserMessageID,
		read.Receipt.AgentMessageID,
		rawMessage,
		address,
	)
	if err != nil {
		h.finishNativeOPSFailure(parentContext, requestID, read.Receipt, claim, domaintask.StatusFailed, "native coding input reconstruction failed")
		writeAgentOpsError(w, http.StatusInternalServerError, "runtime_unavailable")
		return
	}
	input = input.WithSessionID(string(read.Receipt.SessionID)).WithRoute(routing.RouteOPS)
	readerContext := nativeharnessclient.WithAcceptedInputReader(parentContext, acceptedInputReader)
	shiroContext, err := deriveAgentOpsShiroContext(readerContext, requestID)
	if err != nil {
		h.finishNativeOPSFailure(parentContext, requestID, read.Receipt, claim, domaintask.StatusFailed, "native coding execution scope failed")
		writeAgentOpsError(w, http.StatusInternalServerError, "runtime_unavailable")
		return
	}
	executionContext, err := domainexecution.WithIdentity(shiroContext, claim.Task.TaskID, claim.Run.RunID, read.Receipt.TraceID)
	if err != nil {
		h.finishNativeOPSFailure(parentContext, requestID, read.Receipt, claim, domaintask.StatusFailed, "native coding execution identity failed")
		writeAgentOpsError(w, http.StatusInternalServerError, "runtime_unavailable")
		return
	}

	releaseWorkerBusy := h.acquireWorkerBusyLease()
	defer releaseWorkerBusy()
	input, err = orchestrator.AdmitNativeCoding(executionContext, h.nativeCoding, input, routing.RouteOPS)
	if err != nil {
		if errors.Is(err, domainagent.ErrNativeCodingOutcomeUnknown) {
			writeJSONStatus(w, http.StatusAccepted, agentOpsNativeResponse{
				RequestID: requestID, TaskID: claim.Task.TaskID.String(), RunID: string(claim.Run.RunID),
				ClaimStatus: string(taskmanager.AcceptedOPSOutcomeUnknown), TaskStatus: string(claim.Task.Status), RunStatus: string(claim.Run.Status),
			})
			return
		}
		completed, run, finishErr := h.finishNativeOPS(executionContext, read.Receipt, claim, nativeOPSFailureTaskStatus(err), nativeOPSTaskSummary(err))
		if finishErr != nil {
			writeAgentOpsError(w, http.StatusInternalServerError, "runtime_unavailable")
			return
		}
		writeNativeOPSFinalResponse(w, http.StatusInternalServerError, requestID, "claimed", completed, run)
		return
	}
	output, executeErr := h.executor.Execute(executionContext, input)
	if errors.Is(executeErr, domainagent.ErrNativeCodingOutcomeUnknown) {
		writeJSONStatus(w, http.StatusAccepted, agentOpsNativeResponse{
			RequestID:   requestID,
			TaskID:      claim.Task.TaskID.String(),
			RunID:       string(claim.Run.RunID),
			ClaimStatus: string(taskmanager.AcceptedOPSOutcomeUnknown),
			TaskStatus:  string(claim.Task.Status),
			RunStatus:   string(claim.Run.Status),
		})
		return
	}
	if executeErr == nil && strings.TrimSpace(output) != "" {
		completed, run, finishErr := h.finishNativeOPS(executionContext, read.Receipt, claim, domaintask.StatusSucceeded, "native coding completed and verification passed")
		if finishErr != nil {
			writeAgentOpsError(w, http.StatusInternalServerError, "runtime_unavailable")
			return
		}
		writeJSONStatus(w, http.StatusOK, agentOpsNativeResponse{
			RequestID:   requestID,
			TaskID:      completed.TaskID.String(),
			RunID:       string(run.RunID),
			ClaimStatus: string(taskmanager.AcceptedOPSClaimed),
			TaskStatus:  string(completed.Status),
			RunStatus:   string(run.Status),
			Output:      output,
		})
		return
	}
	status := nativeOPSFailureTaskStatus(executeErr)
	completed, run, finishErr := h.finishNativeOPS(executionContext, read.Receipt, claim, status, nativeOPSTaskSummary(executeErr))
	if finishErr != nil {
		writeAgentOpsError(w, http.StatusInternalServerError, "runtime_unavailable")
		return
	}
	writeNativeOPSFinalResponse(w, http.StatusInternalServerError, requestID, string(taskmanager.AcceptedOPSClaimed), completed, run)
}

func validateAcceptedAgentOPSReceipt(receipt conversation.AcceptedOPSInputReceipt, candidate conversation.AcceptedOPSInputRequest, useStoredIdentities bool) error {
	if err := receipt.Validate(); err != nil {
		return err
	}
	wantPayloadHash, err := conversation.AcceptedOPSInputPayloadSHA256(candidate)
	if err != nil {
		return err
	}
	rawHash := sha256.Sum256([]byte(candidate.RawMessage))
	if receipt.RequestID != candidate.RequestID || receipt.OwnerID != candidate.OwnerID || receipt.ActorID != candidate.ActorID ||
		receipt.DeclaredOrigin != candidate.DeclaredOrigin || receipt.PayloadSHA256 != wantPayloadHash ||
		receipt.RawSHA256 != hex.EncodeToString(rawHash[:]) {
		return conversation.ErrAcceptedOPSInputConflict
	}
	// An idempotent owner replay returns the original canonical IDs. A first
	// acceptance must echo every candidate ID; subsequent attempts must preserve
	// and execute the stored receipt's IDs instead of inventing replacements.
	if !useStoredIdentities && (receipt.SessionID != candidate.SessionID || receipt.ThreadID != candidate.FirstThreadID ||
		receipt.ThreadSeq != 1 || receipt.ThreadKind != modulecore.ThreadKindUserConversation || receipt.TaskID != candidate.TaskID ||
		receipt.TurnID != candidate.TurnID || receipt.TraceID != candidate.TraceID || receipt.UserMessageID != candidate.UserMessageID ||
		receipt.AgentMessageID != candidate.AgentMessageID) {
		return conversation.ErrAcceptedOPSInputConflict
	}
	return nil
}

func sameAcceptedOPSReceipt(left, right conversation.AcceptedOPSInputReceipt) bool {
	return left.AcceptanceSequence == right.AcceptanceSequence && left.RequestID == right.RequestID &&
		left.OwnerID == right.OwnerID && left.ActorID == right.ActorID && left.SessionID == right.SessionID &&
		left.ThreadID == right.ThreadID && left.ThreadSeq == right.ThreadSeq && left.ThreadKind == right.ThreadKind &&
		left.TaskID == right.TaskID && left.TurnID == right.TurnID && left.TraceID == right.TraceID &&
		left.UserMessageID == right.UserMessageID && left.AgentMessageID == right.AgentMessageID &&
		left.DeclaredOrigin == right.DeclaredOrigin && left.PayloadSHA256 == right.PayloadSHA256 &&
		left.RawRecordID == right.RawRecordID && left.ManifestID == right.ManifestID &&
		left.RawSHA256 == right.RawSHA256 && left.ManifestSHA256 == right.ManifestSHA256 && left.AcceptedAt.Equal(right.AcceptedAt)
}

func writeAcceptedOPSOwnerError(w http.ResponseWriter, err error) {
	if errors.Is(err, conversation.ErrAcceptedOPSInputConflict) || errors.Is(err, conversation.ErrAcceptedOPSInputInvalid) || errors.Is(err, conversation.ErrAcceptedOPSInputForbidden) {
		writeAgentOpsError(w, http.StatusConflict, "accepted_input_conflict")
		return
	}
	writeAgentOpsError(w, http.StatusServiceUnavailable, "runtime_unavailable")
}

func nativeOPSClaimResponse(requestID string, claim taskmanager.AcceptedOPSClaimResult) agentOpsNativeResponse {
	response := agentOpsNativeResponse{RequestID: requestID, ClaimStatus: string(claim.Status)}
	if claim.Status == taskmanager.AcceptedOPSBlocked {
		response.Error = "runtime_unavailable"
	}
	if claim.Task.TaskID != "" {
		response.TaskID = claim.Task.TaskID.String()
		response.TaskStatus = string(claim.Task.Status)
	}
	if claim.Run.RunID != "" {
		response.RunID = string(claim.Run.RunID)
		response.RunStatus = string(claim.Run.Status)
	}
	return response
}

func (h *agentOpsHandler) finishNativeOPSFailure(parentContext context.Context, requestID string, receipt conversation.AcceptedOPSInputReceipt, claim taskmanager.AcceptedOPSClaimResult, status domaintask.Status, summary string) error {
	shiroContext, err := deriveAgentOpsShiroContext(parentContext, requestID)
	if err != nil {
		return err
	}
	executionContext, err := domainexecution.WithIdentity(shiroContext, claim.Task.TaskID, claim.Run.RunID, receipt.TraceID)
	if err != nil {
		return err
	}
	_, _, err = h.finishNativeOPS(executionContext, receipt, claim, status, summary)
	return err
}

func (h *agentOpsHandler) finishNativeOPS(ctx context.Context, receipt conversation.AcceptedOPSInputReceipt, claim taskmanager.AcceptedOPSClaimResult, status domaintask.Status, summary string) (domaintask.Task, domaintask.Run, error) {
	terminalContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), agentOpsNativeFinalizationTimeout)
	defer cancel()
	task, err := h.taskOwner.CompleteRun(terminalContext, claim.Task.TaskID, claim.Run.RunID, "shiro", status, summary, "")
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, err
	}
	run, err := h.taskOwner.GetRun(terminalContext, claim.Run.RunID)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, err
	}
	if task.TaskID != receipt.TaskID || run.TaskID != receipt.TaskID || run.RunID != claim.Run.RunID {
		return domaintask.Task{}, domaintask.Run{}, errors.New("native OPS finalization identity mismatch")
	}
	return task, run, nil
}

func writeNativeOPSFinalResponse(w http.ResponseWriter, status int, requestID, claimStatus string, task domaintask.Task, run domaintask.Run) {
	writeJSONStatus(w, status, agentOpsNativeResponse{
		RequestID: requestID, TaskID: task.TaskID.String(), RunID: string(run.RunID),
		ClaimStatus: claimStatus, TaskStatus: string(task.Status), RunStatus: string(run.Status), Error: "execution_failed",
	})
}

func nativeOPSFailureTaskStatus(err error) domaintask.Status {
	if errors.Is(err, context.Canceled) {
		return domaintask.StatusCancelled
	}
	var codingErr *domainagent.NativeCodingError
	if errors.As(err, &codingErr) && codingErr.Result.Status == domainagent.NativeRunCancelled {
		return domaintask.StatusCancelled
	}
	return domaintask.StatusFailed
}

func nativeOPSTaskSummary(err error) string {
	var codingErr *domainagent.NativeCodingError
	if errors.As(err, &codingErr) {
		if codingErr.Result.Status == domainagent.NativeRunCompleted {
			return "native coding completed but verification was " + string(codingErr.Result.Verification)
		}
		return "native coding ended " + string(codingErr.Result.Status)
	}
	if err == nil {
		return "native coding returned an empty result"
	}
	return "native coding execution failed"
}
