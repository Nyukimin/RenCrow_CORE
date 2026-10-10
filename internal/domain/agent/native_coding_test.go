package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/advisor"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

type recordingNativeDelegate struct {
	calls   int
	request NativeCodingRequest
	result  NativeCodingResult
	err     error
}

func (d *recordingNativeDelegate) DelegateNativeCoding(_ context.Context, request NativeCodingRequest) (NativeCodingResult, error) {
	d.calls++
	d.request = request
	return d.result, d.err
}

func completedResult(text string, verification NativeCodingVerification) NativeCodingResult {
	return NativeCodingResult{
		Status:         NativeRunCompleted,
		FinalText:      text,
		Verification:   verification,
		HarnessTask:    ExternalRef{Owner: HarnessOwner, ID: "tsk_x"},
		HarnessRun:     ExternalRef{Owner: HarnessOwner, ID: "run_x"},
		HarnessReceipt: ExternalRef{Owner: HarnessOwner, ID: "rcp_x"},
	}
}

func selectNative(t *testing.T, input conversation.TurnInput) conversation.TurnInput {
	t.Helper()
	selected, err := input.WithBackendSelection(conversation.BackendShiroNativeCodingV1)
	if err != nil {
		t.Fatalf("WithBackendSelection: %v", err)
	}
	return selected
}

// legacyTripwires builds a Shiro whose every old route fails the test when it
// is touched: the CodexWorkPath (codex.run, advisor), the SubagentManager
// (toolloop) and the plain Generate path.
type legacyTripwires struct {
	llmCalls, toolCalls, listCalls, advisorCalls, subagentCalls int
}

func (w *legacyTripwires) shiro(withSubagent bool) *ShiroAgent {
	provider := &mockLLMProvider{generateFunc: func(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
		w.llmCalls++
		return llm.GenerateResponse{Content: "legacy plain generate"}, nil
	}}
	runner := &mockToolRunner{
		listFunc: func(context.Context) ([]tool.ToolMetadata, error) {
			w.listCalls++
			return []tool.ToolMetadata{{ToolID: "codex.run"}}, nil
		},
		executeV2Func: func(context.Context, string, map[string]any) (*tool.ToolResponse, error) {
			w.toolCalls++
			return tool.NewSuccess("legacy codex output"), nil
		},
	}
	var subagents SubagentManager
	if withSubagent {
		subagents = &mockSubagentManager{runSyncFunc: func(context.Context, SubagentTask) (SubagentResult, error) {
			w.subagentCalls++
			return SubagentResult{Output: "legacy toolloop"}, nil
		}}
	}
	return NewShiroAgent(provider, runner, &mockMCPClient{}, "test prompt", subagents).
		WithAdvisorService(&countingAdvisor{calls: &w.advisorCalls})
}

func (w *legacyTripwires) untouched() bool {
	return w.llmCalls == 0 && w.toolCalls == 0 && w.listCalls == 0 && w.advisorCalls == 0 && w.subagentCalls == 0
}

type countingAdvisor struct{ calls *int }

func (a *countingAdvisor) RequestAdvice(context.Context, advisor.AdviceRequest) (advisor.AdviceResult, error) {
	*a.calls++
	return advisor.AdviceResult{}, errors.New("legacy advisor path")
}

func (a *countingAdvisor) RecordAdoption(context.Context, advisor.AdvisorAdoptionRecord) error {
	return nil
}

func TestShiroNativeCoding_SelectedTurnGoesToTheDelegateAndToNoOldRoute(t *testing.T) {
	cases := []struct {
		name         string
		message      string
		withSubagent bool
	}{
		{"keyword matches the CodexWorkPath, subagent configured", "この場面を描画して", true},
		{"keyword matches the CodexWorkPath, no subagent", "この場面を描画して", false},
		{"no keyword, subagent configured", "テストを直して", true},
		{"no keyword, no subagent", "テストを直して", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var tripwires legacyTripwires
			delegate := &recordingNativeDelegate{result: completedResult("native done", NativeVerificationPassed)}
			shiro := tripwires.shiro(tc.withSubagent).WithNativeCodingDelegate(delegate)

			got, err := shiro.Execute(context.Background(), selectNative(t, newAgentTurnInput(t, tc.message, "line", "U123")))
			if err != nil || got != "native done" {
				t.Fatalf("Execute() = %q, %v", got, err)
			}
			if delegate.calls != 1 {
				t.Fatalf("the delegate must be called exactly once, got %d", delegate.calls)
			}
			if !tripwires.untouched() {
				t.Fatalf("a selected turn entered an old route: %+v", tripwires)
			}
		})
	}
}

func TestShiroNativeCoding_UnselectedTurnKeepsTheOldRoutes(t *testing.T) {
	t.Run("plain generate", func(t *testing.T) {
		var tripwires legacyTripwires
		delegate := &recordingNativeDelegate{result: completedResult("native", NativeVerificationPassed)}
		shiro := tripwires.shiro(false).WithNativeCodingDelegate(delegate)
		got, err := shiro.Execute(context.Background(), newAgentTurnInput(t, "整理して", "line", "U123"))
		if err != nil || got != "legacy plain generate" {
			t.Fatalf("Execute() = %q, %v", got, err)
		}
		if delegate.calls != 0 || tripwires.llmCalls != 1 {
			t.Fatalf("an unselected turn must run the existing path only: delegate=%d llm=%d", delegate.calls, tripwires.llmCalls)
		}
	})
	t.Run("subagent route", func(t *testing.T) {
		var tripwires legacyTripwires
		delegate := &recordingNativeDelegate{}
		shiro := tripwires.shiro(true).WithNativeCodingDelegate(delegate)
		got, err := shiro.Execute(context.Background(), newAgentTurnInput(t, "整理して", "line", "U123"))
		if err != nil || got != "legacy toolloop" || delegate.calls != 0 || tripwires.subagentCalls != 1 {
			t.Fatalf("Execute() = %q, %v (delegate=%d subagent=%d)", got, err, delegate.calls, tripwires.subagentCalls)
		}
	})
	t.Run("codex work path", func(t *testing.T) {
		var tripwires legacyTripwires
		delegate := &recordingNativeDelegate{}
		shiro := tripwires.shiro(false).WithNativeCodingDelegate(delegate)
		// The advisor is the first CodexWorkPath collaborator; its error is the
		// existing behavior and proves the old route was entered.
		_, err := shiro.Execute(context.Background(), newAgentTurnInput(t, "この場面を描画して", "line", "U123"))
		if delegate.calls != 0 || tripwires.advisorCalls != 1 {
			t.Fatalf("the CodexWorkPath must still run for an unselected turn: delegate=%d advisor=%d err=%v", delegate.calls, tripwires.advisorCalls, err)
		}
	})
}

func TestShiroNativeCoding_NoDelegateBlocksAndNeverFallsBack(t *testing.T) {
	var tripwires legacyTripwires
	shiro := tripwires.shiro(true) // no delegate configured
	_, err := shiro.Execute(context.Background(), selectNative(t, newAgentTurnInput(t, "テストを直して", "line", "U123")))
	if !errors.Is(err, ErrNativeCodingBlocked) {
		t.Fatalf("a selected turn without a delegate must be blocked, got %v", err)
	}
	if !IsNativeCodingFailure(err) {
		t.Fatal("a block is a native coding failure")
	}
	if !tripwires.untouched() {
		t.Fatalf("a blocked selected turn entered an old route: %+v", tripwires)
	}
}

func TestShiroNativeCoding_DelegateErrorsAreReturnedWithoutFallback(t *testing.T) {
	for _, sentinel := range []error{ErrNativeCodingBlocked, ErrNativeCodingRejected, ErrNativeCodingOutcomeUnknown, context.Canceled} {
		var tripwires legacyTripwires
		delegate := &recordingNativeDelegate{err: sentinel}
		shiro := tripwires.shiro(true).WithNativeCodingDelegate(delegate)
		got, err := shiro.Execute(context.Background(), selectNative(t, newAgentTurnInput(t, "この場面を描画して", "line", "U123")))
		if !errors.Is(err, sentinel) || got != "" {
			t.Fatalf("Execute() = %q, %v, want %v", got, err, sentinel)
		}
		if delegate.calls != 1 || !tripwires.untouched() {
			t.Fatalf("%v: delegate=%d tripwires=%+v", sentinel, delegate.calls, tripwires)
		}
	}
}

func TestShiroNativeCoding_AttachmentsAreRejectedBeforeTheDelegate(t *testing.T) {
	var tripwires legacyTripwires
	delegate := &recordingNativeDelegate{}
	shiro := tripwires.shiro(true).WithNativeCodingDelegate(delegate)
	input := selectNative(t, newAgentTurnInput(t, "これを直して", "line", "U123")).
		WithAttachments([]attachment.Attachment{{Kind: attachment.KindImage, Filename: "a.png"}})
	_, err := shiro.Execute(context.Background(), input)
	if !errors.Is(err, ErrNativeCodingRejected) {
		t.Fatalf("an attachment cannot be passed to the Harness: %v", err)
	}
	if delegate.calls != 0 || !tripwires.untouched() {
		t.Fatalf("rejected turn reached a collaborator: delegate=%d %+v", delegate.calls, tripwires)
	}
}

func TestShiroNativeCoding_DelegateReceivesTypedContextNotFlattenedText(t *testing.T) {
	delegate := &recordingNativeDelegate{result: completedResult("ok", NativeVerificationPassed)}
	stable := "# Shared Agent Control\nbody\n## Routing\nroute\n## Tools\ntools"
	engine := &mockConversationEngine{beginTurnFunc: func(ctx context.Context, _ string, _ string) (*conversation.RecallPack, error) {
		if !conversation.HasNativeRecallProvenanceIntent(ctx) {
			t.Error("native Worker BeginTurn must carry provenance intent without changing authorization")
		}
		return &conversation.RecallPack{MidSummaries: []conversation.ThreadSummary{{Summary: "worker memory", Roles: []string{"worker"}}}}, nil
	}}
	shiro := NewShiroAgent(&mockLLMProvider{}, &mockToolRunner{}, &mockMCPClient{}, "character prompt", nil).
		WithStableRuntimeContext(stable).
		WithConversationEngine(engine).
		WithNativeCodingDelegate(delegate)

	userMessage := " \tテストを直して\n  "
	input := selectNative(t, newAgentTurnInput(t, userMessage, "line", "U123"))
	if _, err := shiro.Execute(context.Background(), input); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	messages := delegate.request.Messages
	if len(messages) < 4 {
		t.Fatalf("expected typed Character, Stable, Recall, Variable and user messages: %#v", messages)
	}
	last := messages[len(messages)-1]
	if last.Type != llm.PromptContextUser || last.Content != userMessage {
		t.Fatalf("the user message must be last and carry the text: %#v", last)
	}
	rank := map[llm.PromptContextType]int{llm.PromptContextCharacter: 0, llm.PromptContextStable: 1, llm.PromptContextRecall: 2, llm.PromptContextVariable: 3}
	seen := map[llm.PromptContextType]bool{}
	previous := -1
	for _, message := range messages[:len(messages)-1] {
		position, ok := rank[message.Type]
		if !ok {
			t.Fatalf("a non-block type before the user message: %#v", message)
		}
		if position < previous {
			t.Fatalf("messages are out of the standard order: %#v", messages)
		}
		previous = position
		seen[message.Type] = true
	}
	for kind := range rank {
		if !seen[kind] {
			t.Fatalf("type %s is missing from the typed messages: %#v", kind, messages)
		}
	}
	if delegate.request.Input.BackendSelection() != conversation.BackendShiroNativeCodingV1 {
		t.Fatal("the delegate must receive the selected TurnInput")
	}
	if len(engine.commitRequests) != 1 || engine.commitRequests[0].AgentMessage != "ok" {
		t.Fatalf("a completed delegation commits the conversation turn once: %#v", engine.commitRequests)
	}
}

func TestShiroNativeCoding_ResultProjectionToStringAndError(t *testing.T) {
	type outcome struct {
		name        string
		result      NativeCodingResult
		wantErr     bool
		wantText    string
		wantNote    bool
		wantCommits int
	}
	nonCompleted := func(status NativeCodingRunStatus, code string, resumable bool) NativeCodingResult {
		result := completedResult("partial text", NativeVerificationNotRun)
		result.Status, result.Code, result.Resumable = status, code, resumable
		return result
	}
	cases := []outcome{
		{name: "completed and passed", result: completedResult("done", NativeVerificationPassed), wantText: "done", wantCommits: 1},
		{name: "completed, not run", result: completedResult("done", NativeVerificationNotRun), wantErr: true, wantText: "done", wantNote: true},
		{name: "completed, unknown", result: completedResult("done", NativeVerificationUnknown), wantErr: true, wantText: "done", wantNote: true},
		{name: "completed but verification failed", result: completedResult("done", NativeVerificationFailed), wantErr: true, wantText: "done"},
		{name: "incomplete", result: nonCompleted(NativeRunIncomplete, "DEADLINE_EXCEEDED", true), wantErr: true, wantText: "partial text"},
		{name: "rejected", result: nonCompleted(NativeRunRejected, "POLICY", false), wantErr: true, wantText: "partial text"},
		{name: "blocked", result: nonCompleted(NativeRunBlocked, "MODEL_GENERATION_OUTCOME_UNKNOWN", true), wantErr: true, wantText: "partial text"},
		{name: "cancelled", result: nonCompleted(NativeRunCancelled, "CANCELLED", true), wantErr: true, wantText: "partial text"},
		{name: "failed", result: nonCompleted(NativeRunFailed, "TOOL_FAILED", false), wantErr: true, wantText: "partial text"},
		{name: "restart required", result: nonCompleted(NativeRunRestartRequired, "PERSISTENCE_UNCERTAIN", false), wantErr: true, wantText: "partial text"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine := &mockConversationEngine{}
			delegate := &recordingNativeDelegate{result: tc.result}
			shiro := NewShiroAgent(&mockLLMProvider{}, &mockToolRunner{}, &mockMCPClient{}, "p", nil).
				WithConversationEngine(engine).WithNativeCodingDelegate(delegate)
			got, err := shiro.Execute(context.Background(), selectNative(t, newAgentTurnInput(t, "テストを直して", "line", "U123")))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				var runErr *NativeCodingError
				if !errors.As(err, &runErr) || runErr.Result.Status != tc.result.Status || runErr.Result.Code != tc.result.Code ||
					runErr.Result.Verification != tc.result.Verification || runErr.Result.Resumable != tc.result.Resumable || runErr.Result.HarnessTask != tc.result.HarnessTask ||
					runErr.Result.HarnessRun.Owner != HarnessOwner {
					t.Fatalf("the typed result must travel with the error: %#v", err)
				}
				if !IsNativeCodingFailure(err) {
					t.Fatal("IsNativeCodingFailure must recognize the typed error")
				}
			}
			if !strings.HasPrefix(got, tc.wantText) {
				t.Fatalf("text = %q, want prefix %q", got, tc.wantText)
			}
			if hasNote := strings.Contains(got, "検証されていません"); hasNote != tc.wantNote {
				t.Fatalf("unverified note = %v, want %v (%q)", hasNote, tc.wantNote, got)
			}
			if len(engine.commitRequests) != tc.wantCommits {
				t.Fatalf("conversation commits = %d, want %d", len(engine.commitRequests), tc.wantCommits)
			}
		})
	}
}

func TestNativeCodingResultAcceptedNeedsCompletedAndPassed(t *testing.T) {
	if !completedResult("x", NativeVerificationPassed).Accepted() {
		t.Fatal("completed and passed is the accepted combination")
	}
	for _, verification := range []NativeCodingVerification{NativeVerificationFailed, NativeVerificationNotRun, NativeVerificationUnknown} {
		if completedResult("x", verification).Accepted() {
			t.Fatalf("completed with verification %s must not be accepted", verification)
		}
	}
	incomplete := completedResult("x", NativeVerificationPassed)
	incomplete.Status = NativeRunIncomplete
	if incomplete.Accepted() {
		t.Fatal("a Run that did not complete is never accepted")
	}
}

func TestNativeCodingFailureClassification(t *testing.T) {
	if IsNativeCodingFailure(nil) || IsNativeCodingFailure(errors.New("other")) || IsNativeCodingFailure(context.Canceled) {
		t.Fatal("unrelated errors are not native coding failures")
	}
	wrapped := errors.Join(errors.New("outer"), &NativeCodingError{Result: NativeCodingResult{Status: NativeRunFailed}})
	if !IsNativeCodingFailure(wrapped) {
		t.Fatal("a wrapped typed run error must be recognized")
	}
}
