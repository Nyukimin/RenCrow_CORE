package main

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/service"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/patch"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/proposal"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func newAgentTurnInputForTest(t *testing.T) conversation.TurnInput {
	t.Helper()

	address, err := conversation.NewChannelAddress("line", "U-agent-h2")
	if err != nil {
		t.Fatalf("NewChannelAddress() error = %v", err)
	}
	input, err := conversation.ReconstructTurnInput(
		modulecore.NewTaskID(),
		modulecore.NewTurnID(),
		modulecore.NewTraceID(),
		modulecore.NewMessageID(),
		modulecore.NewMessageID(),
		"canonical agent message",
		address,
	)
	if err != nil {
		t.Fatalf("ReconstructTurnInput() error = %v", err)
	}

	return input.
		WithSessionID(string(modulecore.NewSessionID())).
		WithAttachments([]attachment.Attachment{{
			ID:                  "att-h2",
			Kind:                attachment.KindDocument,
			Filename:            "context.md",
			ContentType:         "text/markdown",
			SizeBytes:           42,
			Path:                "workspace/context.md",
			SHA256:              "sha256-h2",
			ExtractedText:       "attachment metadata",
			ExtractionError:     "",
			ExtractionTruncated: true,
			SecurityWarnings:    []string{"warning-h2"},
		}}).
		WithViewerRecipient("mio").
		WithForcedRoute(routing.RouteCODE3).
		WithRoute(routing.RouteCODE2)
}

func TestTurnInputFromAgentMessagePreservesCanonicalProjectionAfterJSONRoundTrip(t *testing.T) {
	want := newAgentTurnInputForTest(t)
	executionTaskID := modulecore.NewTaskID()
	message, err := domaintransport.NewTurnInputMessage("mio", "shiro", executionTaskID, want)
	if err != nil {
		t.Fatalf("NewTurnInputMessage() error = %v", err)
	}

	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var decoded domaintransport.Message
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	secondExecutionTaskID := modulecore.NewTaskID()
	decoded.TaskID = secondExecutionTaskID

	got, err := turnInputFromAgentMessage(decoded)
	if err != nil {
		t.Fatalf("turnInputFromAgentMessage() error = %v", err)
	}
	if got.RootTaskID() != want.RootTaskID() || got.TurnID() != want.TurnID() || got.TraceID() != want.TraceID() || got.UserMessageID() != want.UserMessageID() || got.AgentMessageID() != want.AgentMessageID() {
		t.Fatalf("canonical identities changed: got root=%q turn=%q trace=%q user=%q agent=%q, want root=%q turn=%q trace=%q user=%q agent=%q", got.RootTaskID(), got.TurnID(), got.TraceID(), got.UserMessageID(), got.AgentMessageID(), want.RootTaskID(), want.TurnID(), want.TraceID(), want.UserMessageID(), want.AgentMessageID())
	}
	if decoded.TaskID != secondExecutionTaskID || decoded.TaskID == executionTaskID {
		t.Fatalf("execution TaskID was not independently replaced: got=%q initial=%q", decoded.TaskID, executionTaskID)
	}
	if got.SessionID() != want.SessionID() || got.MessageText() != want.MessageText() {
		t.Fatalf("session/message changed: got session=%q message=%q, want session=%q message=%q", got.SessionID(), got.MessageText(), want.SessionID(), want.MessageText())
	}
	if got.ChannelAddress().ChannelType() != want.ChannelAddress().ChannelType() || got.ChannelAddress().ExternalConversationID() != want.ChannelAddress().ExternalConversationID() {
		t.Fatalf("channel address changed: got=%q/%q, want=%q/%q", got.ChannelAddress().ChannelType(), got.ChannelAddress().ExternalConversationID(), want.ChannelAddress().ChannelType(), want.ChannelAddress().ExternalConversationID())
	}
	if !reflect.DeepEqual(got.Attachments(), want.Attachments()) {
		t.Fatalf("attachments changed: got=%#v, want=%#v", got.Attachments(), want.Attachments())
	}
	if got.ViewerRecipient() != want.ViewerRecipient() || got.ForcedRoute() != want.ForcedRoute() || got.Route() != want.Route() || !got.HasForcedRoute() {
		t.Fatalf("recipient/routes changed: got recipient=%q forced=%q route=%q, want recipient=%q forced=%q route=%q", got.ViewerRecipient(), got.ForcedRoute(), got.Route(), want.ViewerRecipient(), want.ForcedRoute(), want.Route())
	}
}

func TestTurnInputFromAgentMessageRejectsMissingOrMalformedProjection(t *testing.T) {
	messageWithoutProjection := domaintransport.NewMessage("mio", "shiro", "session", modulecore.NewTaskID(), "message without projection")
	if _, err := turnInputFromAgentMessage(messageWithoutProjection); err == nil || !strings.Contains(err.Error(), "invalid agent turn input projection") {
		t.Fatalf("missing projection error = %v, want bounded projection error", err)
	}

	message, err := domaintransport.NewTurnInputMessage("mio", "shiro", modulecore.NewTaskID(), newAgentTurnInputForTest(t))
	if err != nil {
		t.Fatalf("NewTurnInputMessage() error = %v", err)
	}
	projection := *message.TurnInput
	projection.TurnID = modulecore.TurnID("malformed-turn-id")
	message.TurnInput = &projection
	if _, err := turnInputFromAgentMessage(message); err == nil {
		t.Fatal("malformed projection was accepted")
	}
}

type projectingWorkerExecution struct {
	result patch.PatchExecutionResult
}

func (p *projectingWorkerExecution) ExecuteObservation(_ context.Context, _ []service.ObservationAction) ([]service.ObservationActionResult, error) {
	return nil, nil
}

func (p *projectingWorkerExecution) ExecuteProposal(_ context.Context, _ modulecore.TaskID, _ *proposal.Proposal) (*patch.PatchExecutionResult, error) {
	result := p.result
	return &result, nil
}

func TestWorkerResultProjectionRemote(t *testing.T) {
	for _, tc := range workerResultProjectionCasesRemote() {
		t.Run(tc.name, func(t *testing.T) {
			msg := workerResultProjectionMessageRemote()
			handler := &workerHandler{executionService: &projectingWorkerExecution{result: tc.result}}
			got, err := handler.HandleMessage(context.Background(), msg)
			if err != nil {
				t.Fatalf("worker handler failed: %v", err)
			}
			assertWorkerResultProjectionRemote(t, msg, got, tc.result)
		})
	}
}

type workerResultProjectionCaseRemote struct {
	name   string
	result patch.PatchExecutionResult
}

func workerResultProjectionCasesRemote() []workerResultProjectionCaseRemote {
	return []workerResultProjectionCaseRemote{
		{
			name: "passed",
			result: patch.PatchExecutionResult{
				Success: true, Summary: "passed summary", ExecutedCmds: 2, FailedCmds: 0,
				GitCommit: "commit-passed", FailedIndex: -1, TestStatus: "passed", TestReceipt: "Tmp/test-results/passed.json",
			},
		},
		{
			name: "failed_zero_commands",
			result: patch.PatchExecutionResult{
				Success: false, Summary: "failed without commands", ExecutedCmds: 0, FailedCmds: 0,
				FailureKind: "execution_failed", FailureReason: "owner reported failure", Retryable: true,
				FailedIndex: -1, TestStatus: "failed", TestReceipt: "Tmp/test-results/failed-zero.json",
			},
		},
		{
			name: "blocked_zero_commands",
			result: patch.PatchExecutionResult{
				Success: false, Summary: "blocked before commands", ExecutedCmds: 0, FailedCmds: 0,
				FailureKind: "test_impact_blocked", FailureReason: "canonical owner unavailable", Retryable: false,
				FailedIndex: -1, TestStatus: "blocked", TestReceipt: "Tmp/test-results/blocked.json",
			},
		},
		{
			name: "command_failure",
			result: patch.PatchExecutionResult{
				Success: false, Summary: "command failed", ExecutedCmds: 2, FailedCmds: 1,
				FailureKind: "command_failed", FailureReason: "shell exit", Retryable: true,
				FailedIndex: 1, TestStatus: "not_run", TestReceipt: "Tmp/test-results/command-failure.json",
			},
		},
	}
}

func workerResultProjectionMessageRemote() domaintransport.Message {
	msg := domaintransport.NewMessage("mio", "shiro", string(modulecore.NewSessionID()), modulecore.NewTaskID(), "Execute coder proposal")
	msg.Proposal = &domaintransport.ProposalPayload{Plan: "plan", Patch: "[]", Risk: "risk", CostHint: "cost"}
	return msg
}

func assertWorkerResultProjectionRemote(t *testing.T, input, got domaintransport.Message, want patch.PatchExecutionResult) {
	t.Helper()
	if got.Type != domaintransport.MessageTypeResult {
		t.Fatalf("response type=%q, want result", got.Type)
	}
	if got.From != input.To || got.To != input.From || got.SessionID != input.SessionID || got.TaskID != input.TaskID {
		t.Fatalf("response address/identity changed: got from=%q to=%q session=%q task=%q, want from=%q to=%q session=%q task=%q", got.From, got.To, got.SessionID, got.TaskID, input.To, input.From, input.SessionID, input.TaskID)
	}
	if got.Result == nil {
		t.Fatal("response result is nil")
	}
	if got.Result.Success != want.Success || got.Result.Summary != want.Summary || got.Result.ExecutedCmds != want.ExecutedCmds || got.Result.FailedCmds != want.FailedCmds || got.Result.GitCommit != want.GitCommit || got.Result.FailureKind != want.FailureKind || got.Result.FailureReason != want.FailureReason || got.Result.Retryable != want.Retryable || got.Result.FailedIndex != want.FailedIndex || got.Result.TestStatus != want.TestStatus || got.Result.TestReceipt != want.TestReceipt {
		t.Fatalf("result projection=%#v, want success=%t summary=%q executed=%d failed=%d commit=%q kind=%q reason=%q retryable=%t index=%d status=%q receipt=%q", got.Result, want.Success, want.Summary, want.ExecutedCmds, want.FailedCmds, want.GitCommit, want.FailureKind, want.FailureReason, want.Retryable, want.FailedIndex, want.TestStatus, want.TestReceipt)
	}
}
