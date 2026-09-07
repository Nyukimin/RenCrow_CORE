package orchestrator

import (
	"context"
	"errors"
	"fmt"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/coderloop"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/patch"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/proposal"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type impactLoopCoder struct {
	responses []string
	index     int
	calls     int
}

func (c *impactLoopCoder) Generate(context.Context, conversation.TurnInput, string) (string, error) {
	return "", errors.New("unused Generate path")
}

func (c *impactLoopCoder) GenerateWithContext(_ context.Context, _ []llm.Message) (string, error) {
	c.calls++
	if c.index >= len(c.responses) {
		return `{"type":"final_report","summary":"no response","changed_files":[],"tests_run":[],"remaining_risks":[]}`, nil
	}
	response := c.responses[c.index]
	c.index++
	return response, nil
}

type impactLoopWorker struct {
	patchResults []*patch.PatchExecutionResult
	observations [][]coderloop.ObservationActionResult
	patchCalls   int
	observeCalls int
}

func (w *impactLoopWorker) ExecuteObservation(context.Context, []coderloop.ObservationAction) ([]coderloop.ObservationActionResult, error) {
	index := w.observeCalls
	w.observeCalls++
	if index >= len(w.observations) {
		return nil, nil
	}
	return w.observations[index], nil
}

func (w *impactLoopWorker) ExecuteProposal(context.Context, modulecore.TaskID, *proposal.Proposal) (*patch.PatchExecutionResult, error) {
	index := w.patchCalls
	w.patchCalls++
	if index >= len(w.patchResults) {
		return nil, errors.New("unexpected patch proposal")
	}
	return w.patchResults[index], nil
}

func newImpactLoopRequest(t *testing.T) CodeExecutionRequest {
	t.Helper()
	taskID := modulecore.NewTaskID()
	return newOrchestratorTestCodeExecutionRequest(t, taskID, "apply change", routing.RouteCODE1, "session-1", "test", "chat-1")
}

func TestCoderLoopBlocksFinalReportAfterFailedPatchUntilCheckedRevision(t *testing.T) {
	coder := &impactLoopCoder{responses: []string{
		`{"type":"patch_proposal","intent":"first","patch":"[]","tests":[]}`,
		`{"type":"read_request","actions":[{"action":"read_file","target":"changed.txt"}]}`,
		`{"type":"final_report","summary":"premature","changed_files":[],"tests_run":[],"remaining_risks":[]}`,
		`{"type":"patch_proposal","intent":"revised","patch":"[]","tests":[]}`,
		`{"type":"final_report","summary":"checked","changed_files":["changed.txt"],"tests_run":[],"remaining_risks":[]}`,
	}}
	failed := patch.NewPatchExecutionResult().WithFailureMetadata("test_impact_failed", "owner receipt failed", false)
	failed.Success = false
	failed.TestStatus = "failed"
	passed := patch.NewPatchExecutionResult().WithSummary("checked")
	passed.TestStatus = "passed"
	worker := &impactLoopWorker{patchResults: []*patch.PatchExecutionResult{failed, passed}}
	executor := NewCoderLoopExecutor(coder, worker, "coder", "prompt", nil).WithMaxTurns(5)

	result, err := executor.runLoop(context.Background(), newImpactLoopRequest(t))
	if err != nil {
		t.Fatalf("runLoop failed: %v", err)
	}
	if result.Partial || result.FinalReport == nil || result.Summary != "checked" {
		t.Fatalf("expected final report only after checked revision, got %#v", result)
	}
	if worker.patchCalls != 2 {
		t.Fatalf("patch calls=%d, want 2", worker.patchCalls)
	}
}

func TestCoderLoopBlocksFinalReportAfterIndividualTestFailure(t *testing.T) {
	coder := &impactLoopCoder{responses: []string{
		`{"type":"test_request","actions":[{"action":"shell_command","target":"go test ./..."}]}`,
		`{"type":"final_report","summary":"should not complete","changed_files":[],"tests_run":[],"remaining_risks":[]}`,
	}}
	worker := &impactLoopWorker{observations: [][]coderloop.ObservationActionResult{{
		coderloop.NewObservationActionResult("shell_command", "go test ./...", "failed", errors.New("exit status 1")),
	}}}
	executor := NewCoderLoopExecutor(coder, worker, "coder", "prompt", nil).WithMaxTurns(2)

	result, err := executor.runLoop(context.Background(), newImpactLoopRequest(t))
	if err != nil {
		t.Fatalf("runLoop failed: %v", err)
	}
	if !result.Partial {
		t.Fatalf("failed explicit test request must prevent completion: %#v", result)
	}
	if result.FinalReport == nil || result.FinalReport.Summary != "should not complete" {
		t.Fatalf("expected blocked report to remain diagnostic evidence: %#v", result)
	}
}

func TestCoderLoopTerminalResult(t *testing.T) {
	final := `{"type":"final_report","summary":"checked","changed_files":[],"tests_run":[],"remaining_risks":[]}`
	testRequest := `{"type":"test_request","actions":[{"action":"shell_command","target":"go test ./..."}]}`
	for _, tc := range []struct {
		name    string
		replies []string
		failed  bool
	}{
		{"parse failures", []string{"bad", "bad"}, true},
		{"turn limit", []string{`{"type":"plan","task_summary":"work","steps":["inspect"],"risk":[]}`}, true},
		{"failed verification", []string{testRequest, final}, true},
		{"recovered verification", []string{testRequest, testRequest, final}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			coder := &impactLoopCoder{responses: tc.replies}
			worker := &impactLoopWorker{observations: [][]coderloop.ObservationActionResult{{coderloop.NewObservationActionResult("shell_command", "test", "", errors.New("failed"))}, {coderloop.NewObservationActionResult("shell_command", "test", "passed", nil)}}}
			executor := NewCoderLoopExecutor(coder, worker, "coder1", "prompt", nil).WithMaxTurns(len(tc.replies))
			response, err := executor.Execute(context.Background(), newImpactLoopRequest(t))
			if tc.failed {
				if err == nil || response.Handled || classifyExecutorFailure(err) != "coder_loop_incomplete" {
					t.Fatalf("incomplete loop accepted: response=%#v error=%v classification=%s", response, err, classifyExecutorFailure(err))
				}
				if classifyExecutorFailure(fmt.Errorf("provider proposal validation: %w", err)) != "coder_loop_incomplete" {
					t.Fatal("wrapped terminal error reclassified")
				}
				if tc.name == "turn limit" && !strings.Contains(err.Error(), "上限ターン数") {
					t.Fatalf("did not reach turn limit: %v", err)
				}
			} else if err != nil || !response.Handled {
				t.Fatalf("bounded repair failed: %#v %v", response, err)
			}
		})
	}
}

func TestCoderLoopTerminalPersistsFailedTaskAndRun(t *testing.T) {
	_, worker, owner := newOrchestratorWorker(t)
	coder := &impactLoopCoder{responses: []string{"invalid provider proposal", "invalid provider proposal"}}
	mio := &mockMioAgent{decision: routing.NewDecision(routing.RouteCODE3, 1, "test route"), response: "chat"}
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{response: "unused"}, nil, nil, coder, nil, worker)
	orch.SetTaskLifecycleManager(owner)
	orch.SetCoderLoopPrompt("test loop")
	_, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{SessionID: "loop-terminal", Channel: "viewer", ChatID: "user", UserMessage: "/code3 implement"})
	if err == nil {
		t.Fatal("incomplete loop accepted as route success")
	}
	if coder.calls != 2 {
		t.Fatalf("outer executor restarted terminated loop: calls=%d", coder.calls)
	}
	tasks, err := owner.List(context.Background(), domaintask.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	runs, err := owner.ListRuns(context.Background(), domaintask.RunFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) == 0 || len(runs) == 0 {
		t.Fatal("missing persisted lifecycle")
	}
	for _, task := range tasks {
		if task.Status != domaintask.StatusFailed {
			t.Fatalf("task status=%s", task.Status)
		}
	}
	for _, run := range runs {
		if run.Status != domaintask.RunStatusFailed {
			t.Fatalf("run status=%s", run.Status)
		}
	}
}
