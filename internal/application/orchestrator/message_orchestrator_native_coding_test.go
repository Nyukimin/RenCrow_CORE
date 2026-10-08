package orchestrator

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/advisor"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

// This file is the Trace test of the execution profile shiro_native_coding_v1
// from the real entry (ProcessMessage): the real ShiroAgent runs behind the real
// orchestrator, and every old route (CodexWorkPath tools and advisor, the
// SubagentManager (toolloop), the plain Generate path) is a tripwire.

type ncTripwires struct {
	llm, tools, listTools, advisor, subagent atomic.Int32
}

func (w *ncTripwires) touched() bool {
	return w.llm.Load()+w.tools.Load()+w.listTools.Load()+w.advisor.Load()+w.subagent.Load() != 0
}

type ncLLM struct{ w *ncTripwires }

func (p ncLLM) Generate(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.w.llm.Add(1)
	return llm.GenerateResponse{Content: "legacy plain generate"}, nil
}
func (ncLLM) Name() string { return "nc-llm" }

type ncTools struct{ w *ncTripwires }

func (t ncTools) ExecuteV2(context.Context, string, map[string]any) (*tool.ToolResponse, error) {
	t.w.tools.Add(1)
	return tool.NewSuccess("legacy codex output"), nil
}
func (t ncTools) ListTools(context.Context) ([]tool.ToolMetadata, error) {
	t.w.listTools.Add(1)
	return []tool.ToolMetadata{{ToolID: "codex.run"}}, nil
}

type ncMCP struct{}

func (ncMCP) CallTool(context.Context, string, string, map[string]interface{}) (string, error) {
	return "", errors.New("mcp must not be called")
}
func (ncMCP) ListTools(context.Context, string) ([]string, error) { return nil, nil }

type ncAdvisor struct{ w *ncTripwires }

func (a ncAdvisor) RequestAdvice(context.Context, advisor.AdviceRequest) (advisor.AdviceResult, error) {
	a.w.advisor.Add(1)
	return advisor.AdviceResult{}, errors.New("legacy advisor path")
}

type ncSubagents struct{ w *ncTripwires }

func (s ncSubagents) RunSync(context.Context, agent.SubagentTask) (agent.SubagentResult, error) {
	s.w.subagent.Add(1)
	return agent.SubagentResult{Output: "legacy toolloop"}, nil
}

type ncAdmission struct {
	calls atomic.Int32
	err   error
}

func (a *ncAdmission) AdmitNativeCoding(context.Context, conversation.TurnInput) error {
	a.calls.Add(1)
	return a.err
}

type ncDelegate struct {
	calls  atomic.Int32
	result agent.NativeCodingResult
	err    error
	inputs []conversation.TurnInput
}

func (d *ncDelegate) DelegateNativeCoding(_ context.Context, request agent.NativeCodingRequest) (agent.NativeCodingResult, error) {
	d.calls.Add(1)
	d.inputs = append(d.inputs, request.Input)
	return d.result, d.err
}

func ncRealShiro(w *ncTripwires, withSubagent bool, delegate agent.NativeCodingDelegate) *agent.ShiroAgent {
	var subagents agent.SubagentManager
	if withSubagent {
		subagents = ncSubagents{w: w}
	}
	shiro := agent.NewShiroAgent(ncLLM{w: w}, ncTools{w: w}, ncMCP{}, "test prompt", subagents).
		WithAdvisorService(ncAdvisor{w: w})
	if delegate != nil {
		shiro.WithNativeCodingDelegate(delegate)
	}
	return shiro
}

func ncOrchestrator(t *testing.T, route routing.Route, shiro ShiroAgent) *MessageOrchestrator {
	t.Helper()
	mio := &mockMioAgent{decision: routing.NewDecision(route, 0.9, "nc"), response: "mio chat"}
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, shiro, nil, nil, nil, nil, nil)
	attachCanonicalTestTaskOwner(t, orch)
	orch.SetMaxRepair(3)
	return orch
}

func ncRequest(message string) ProcessMessageRequest {
	return ProcessMessageRequest{SessionID: "20261008-line-U1", Channel: "line", ChatID: "U1", UserMessage: message}
}

func completedNativeResult(text string) agent.NativeCodingResult {
	return agent.NativeCodingResult{
		Status: agent.NativeRunCompleted, FinalText: text, Verification: agent.NativeVerificationPassed,
		HarnessTask: agent.ExternalRef{Owner: agent.HarnessOwner, ID: "tsk_x"},
	}
}

func TestNativeCodingTrace_SelectedOPSTurnReachesOnlyTheDelegate(t *testing.T) {
	// Both a message that matches a CodexWorkPath keyword and one that does not,
	// with and without a SubagentManager.
	for _, message := range []string{"この場面を描画して", "テストを直して"} {
		for _, withSubagent := range []bool{true, false} {
			var w ncTripwires
			delegate := &ncDelegate{result: completedNativeResult("native answer")}
			admission := &ncAdmission{}
			orch := ncOrchestrator(t, routing.RouteOPS, ncRealShiro(&w, withSubagent, delegate))
			orch.SetNativeCodingAdmission(admission)

			resp, err := orch.ProcessMessage(context.Background(), ncRequest(message))
			if err != nil {
				t.Fatalf("message=%q subagent=%v: ProcessMessage: %v", message, withSubagent, err)
			}
			if resp.Response != "native answer" {
				t.Fatalf("message=%q subagent=%v: response = %q", message, withSubagent, resp.Response)
			}
			if admission.calls.Load() != 1 || delegate.calls.Load() != 1 {
				t.Fatalf("message=%q subagent=%v: admission=%d delegate=%d, want one each (no repair loop, no re-delegation)", message, withSubagent, admission.calls.Load(), delegate.calls.Load())
			}
			if delegate.inputs[0].BackendSelection() != conversation.BackendShiroNativeCodingV1 || delegate.inputs[0].Route() != routing.RouteOPS {
				t.Fatalf("the delegate must receive the selected OPS input: %+v", delegate.inputs[0])
			}
			if w.touched() {
				t.Fatalf("message=%q subagent=%v: an old route was entered: llm=%d tools=%d list=%d advisor=%d subagent=%d",
					message, withSubagent, w.llm.Load(), w.tools.Load(), w.listTools.Load(), w.advisor.Load(), w.subagent.Load())
			}
		}
	}
}

func TestNativeCodingTrace_NoAdmissionKeepsTheOldRoutes(t *testing.T) {
	var w ncTripwires
	delegate := &ncDelegate{result: completedNativeResult("must not be used")}
	orch := ncOrchestrator(t, routing.RouteOPS, ncRealShiro(&w, false, delegate))
	// The delegate is wired into Shiro, but without the admission no turn is
	// ever selected: configuration, not wiring, selects the profile.
	resp, err := orch.ProcessMessage(context.Background(), ncRequest("整理して"))
	if err != nil {
		t.Fatalf("ProcessMessage: %v", err)
	}
	if resp.Response != "legacy plain generate" || delegate.calls.Load() != 0 || w.llm.Load() != 1 {
		t.Fatalf("an unselected turn must run the existing path only: %q delegate=%d llm=%d", resp.Response, delegate.calls.Load(), w.llm.Load())
	}
}

func TestNativeCodingTrace_OnlyOPSTurnsAreAdmitted(t *testing.T) {
	var w ncTripwires
	delegate := &ncDelegate{result: completedNativeResult("must not be used")}
	admission := &ncAdmission{}
	orch := ncOrchestrator(t, routing.RouteCHAT, ncRealShiro(&w, false, delegate))
	orch.SetNativeCodingAdmission(admission)
	resp, err := orch.ProcessMessage(context.Background(), ncRequest("こんにちは"))
	if err != nil || resp.Response != "mio chat" {
		t.Fatalf("ProcessMessage = %q, %v", resp.Response, err)
	}
	if admission.calls.Load() != 0 || delegate.calls.Load() != 0 {
		t.Fatalf("a CHAT turn must not be admitted: admission=%d delegate=%d", admission.calls.Load(), delegate.calls.Load())
	}
}

func TestNativeCodingTrace_RefusedAdmissionEndsTheTurnWithoutFallbackOrRepair(t *testing.T) {
	for _, refusal := range []error{agent.ErrNativeCodingBlocked, agent.ErrNativeCodingRejected} {
		var w ncTripwires
		delegate := &ncDelegate{result: completedNativeResult("must not be used")}
		admission := &ncAdmission{err: refusal}
		orch := ncOrchestrator(t, routing.RouteOPS, ncRealShiro(&w, true, delegate))
		orch.SetNativeCodingAdmission(admission)

		resp, err := orch.ProcessMessage(context.Background(), ncRequest("この場面を描画して"))
		if !errors.Is(err, refusal) || resp.Response != "" {
			t.Fatalf("a refused admission must fail the turn with %v, got %q, %v", refusal, resp.Response, err)
		}
		if delegate.calls.Load() != 0 || w.touched() {
			t.Fatalf("refusal %v reached a collaborator: delegate=%d tripwires=%+v", refusal, delegate.calls.Load(), &w)
		}
		if admission.calls.Load() != 1 {
			t.Fatalf("admission is asked once, got %d", admission.calls.Load())
		}
	}
}

func TestNativeCodingTrace_DelegateFailureIsNeverRedelegatedOrRerouted(t *testing.T) {
	failures := map[string]struct {
		result agent.NativeCodingResult
		err    error
	}{
		"blocked run":      {result: agent.NativeCodingResult{Status: agent.NativeRunBlocked, Code: "MODEL_GENERATION_OUTCOME_UNKNOWN", Resumable: true}},
		"failed run":       {result: agent.NativeCodingResult{Status: agent.NativeRunFailed, Code: "TOOL_FAILED"}},
		"cancelled run":    {result: agent.NativeCodingResult{Status: agent.NativeRunCancelled, Code: "CANCELLED", Resumable: true}},
		"client failure":   {err: agent.ErrNativeCodingBlocked},
		"outcome unknown":  {err: agent.ErrNativeCodingOutcomeUnknown},
		"failed check":     {result: agent.NativeCodingResult{Status: agent.NativeRunCompleted, FinalText: "x", Verification: agent.NativeVerificationFailed}},
		"verification run": {result: agent.NativeCodingResult{Status: agent.NativeRunIncomplete, Code: "DEADLINE_EXCEEDED", Resumable: true}},
	}
	for name, failure := range failures {
		t.Run(name, func(t *testing.T) {
			var w ncTripwires
			delegate := &ncDelegate{result: failure.result, err: failure.err}
			admission := &ncAdmission{}
			orch := ncOrchestrator(t, routing.RouteOPS, ncRealShiro(&w, true, delegate))
			orch.SetNativeCodingAdmission(admission)

			_, err := orch.ProcessMessage(context.Background(), ncRequest("この場面を描画して"))
			if err == nil {
				t.Fatal("a delegation that is not a verified success must fail the turn")
			}
			if !agent.IsNativeCodingFailure(err) {
				t.Fatalf("the typed failure must survive the orchestrator wrapping: %v", err)
			}
			if failure.err == nil {
				var runErr *agent.NativeCodingError
				if !errors.As(err, &runErr) || runErr.Result.Status != failure.result.Status || runErr.Result.Code != failure.result.Code {
					t.Fatalf("the typed Run result must be reachable with errors.As: %v", err)
				}
			}
			if delegate.calls.Load() != 1 {
				t.Fatalf("a failure must not be repaired by delegating again (maxRepair=3): delegate calls = %d", delegate.calls.Load())
			}
			if w.touched() {
				t.Fatalf("a failure must not fall back to an old route: %+v", &w)
			}
		})
	}
}

func TestNativeCodingTrace_SelectionIsWrittenOnlyByTheOrchestratorAdmission(t *testing.T) {
	// The input given to the admission itself carries no selection yet: it is the
	// orchestrator that writes the selection, after the admission answered.
	var seen conversation.BackendSelection = "unset"
	admission := ncAdmissionFunc(func(_ context.Context, input conversation.TurnInput) error {
		seen = input.BackendSelection()
		return nil
	})
	var w ncTripwires
	delegate := &ncDelegate{result: completedNativeResult("ok")}
	orch := ncOrchestrator(t, routing.RouteOPS, ncRealShiro(&w, false, delegate))
	orch.SetNativeCodingAdmission(admission)
	if _, err := orch.ProcessMessage(context.Background(), ncRequest("テストを直して")); err != nil {
		t.Fatalf("ProcessMessage: %v", err)
	}
	if seen != conversation.BackendSelectionNone {
		t.Fatalf("the admission must see an unselected input, saw %q", seen)
	}
}

type ncAdmissionFunc func(context.Context, conversation.TurnInput) error

func (f ncAdmissionFunc) AdmitNativeCoding(ctx context.Context, input conversation.TurnInput) error {
	return f(ctx, input)
}

func TestNativeCodingTrace_ACompletedAnswerThatMentionsAnErrorIsNotRejectedByTheTextHeuristic(t *testing.T) {
	// The keyword check of the existing OPS verification rejects any answer that
	// contains "error", "失敗" or "エラー"; a coding answer says such words all the
	// time. A selected turn is judged from the typed RunResult instead.
	text := "エラー処理を追加し、error handling のテストが通ることを確認しました。"
	var w ncTripwires
	delegate := &ncDelegate{result: completedNativeResult(text)}
	orch := ncOrchestrator(t, routing.RouteOPS, ncRealShiro(&w, false, delegate))
	orch.SetNativeCodingAdmission(&ncAdmission{})
	resp, err := orch.ProcessMessage(context.Background(), ncRequest("エラー処理を追加して"))
	if err != nil || resp.Response != text {
		t.Fatalf("ProcessMessage = %q, %v", resp.Response, err)
	}
	if delegate.calls.Load() != 1 {
		t.Fatalf("delegate calls = %d", delegate.calls.Load())
	}

	// The same words in the answer of an unselected turn keep failing the existing check.
	failing := &mockShiroAgent{response: "error: something went wrong"}
	existing := NewMessageOrchestrator(newMockSessionRepository(), &mockMioAgent{decision: routing.NewDecision(routing.RouteOPS, 0.9, "nc"), response: "mio chat"}, failing, nil, nil, nil, nil, nil)
	attachCanonicalTestTaskOwner(t, existing)
	existing.SetNativeCodingAdmission(nil)
	if _, err := existing.ProcessMessage(context.Background(), ncRequest("実行して")); err == nil {
		t.Fatal("an unselected turn must keep the existing text verification")
	}
}

func TestNativeCodingTrace_AnEmptyAnswerStillFailsTheVerificationWithoutRepair(t *testing.T) {
	var w ncTripwires
	delegate := &ncDelegate{result: completedNativeResult("")}
	orch := ncOrchestrator(t, routing.RouteOPS, ncRealShiro(&w, false, delegate))
	orch.SetNativeCodingAdmission(&ncAdmission{})
	if _, err := orch.ProcessMessage(context.Background(), ncRequest("実行して")); err == nil {
		t.Fatal("an empty answer is not an answer")
	}
	if delegate.calls.Load() != 1 {
		t.Fatalf("an empty answer must not be repaired by delegating again, delegate calls = %d", delegate.calls.Load())
	}
}

func TestClassifyExecutorFailureTreatsNativeCodingFailuresAsNotRetryable(t *testing.T) {
	for _, err := range []error{
		&agent.NativeCodingError{Result: agent.NativeCodingResult{Status: agent.NativeRunBlocked, Code: "MODEL_GENERATION_OUTCOME_UNKNOWN"}},
		agent.ErrNativeCodingBlocked, agent.ErrNativeCodingRejected, agent.ErrNativeCodingOutcomeUnknown,
	} {
		// Their text may contain "model" or "provider", which the text classifier
		// reads as a retryable provider failure.
		kind := classifyExecutorFailure(err)
		if kind != "native_coding" {
			t.Fatalf("%v classified as %q", err, kind)
		}
	}
	if kind := classifyExecutorFailure(errors.New("provider down")); kind == "native_coding" {
		t.Fatal("other errors keep their classification")
	}
}
