package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/service"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/coderloop"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/patch"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/proposal"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/testsupport/testimpact"
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

func (w *impactLoopWorker) BeginCoderTaskExecution(_ context.Context, taskID modulecore.TaskID) (service.CoderTaskExecutionSession, error) {
	return &impactLoopSession{worker: w, taskID: taskID}, nil
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

type impactLoopSession struct {
	worker                *impactLoopWorker
	taskID                modulecore.TaskID
	successfulObservation bool
	successfulPatch       bool
	changedFiles          []string
	testStatus            string
	testReceipt           string
	patchFailure          bool
	verificationFailure   bool
	loopFailure           string
}

func (s *impactLoopSession) RecordLoopFailure(_ context.Context, reason string) error {
	s.loopFailure = reason
	return nil
}

func (s *impactLoopSession) ExecuteObservation(ctx context.Context, actions []service.ObservationAction) ([]service.ObservationActionResult, error) {
	results, err := s.worker.ExecuteObservation(ctx, actions)
	for index, result := range results {
		if index < len(actions) && actions[index].Action == "shell_command" && strings.HasPrefix(actions[index].Target, "go ") {
			if result.Status == "ok" {
				if result.TestStatus == "" {
					result.TestStatus = "passed"
					results[index].TestStatus = "passed"
				}
				s.testStatus, s.testReceipt = result.TestStatus, result.TestReceipt
				s.verificationFailure = result.TestStatus != "passed" && result.TestStatus != "not_applicable"
			} else {
				s.testStatus = result.TestStatus
				s.verificationFailure = true
			}
		} else if result.Status == "ok" {
			s.successfulObservation = true
		}
	}
	return results, err
}

func (s *impactLoopSession) ExecuteProposal(ctx context.Context, p *proposal.Proposal) (*patch.PatchExecutionResult, error) {
	result, err := s.worker.ExecuteProposal(ctx, s.taskID, p)
	if err != nil || result == nil {
		s.patchFailure = true
		s.successfulPatch = false
		return result, err
	}
	s.testStatus, s.testReceipt = result.TestStatus, result.TestReceipt
	if result.Success && (result.TestStatus == "passed" || result.TestStatus == "not_applicable") {
		s.successfulPatch = true
		s.patchFailure = false
		s.verificationFailure = false
		s.loopFailure = ""
		s.changedFiles = []string{"changed.txt"}
		return result, nil
	}
	s.successfulPatch = false
	s.patchFailure = true
	s.verificationFailure = result.TestStatus != "passed" && result.TestStatus != "not_applicable"
	return result, errors.New("mock patch did not pass owner verification")
}

func (s *impactLoopSession) CompletionEvidence(context.Context) (service.CoderTaskCompletionEvidence, error) {
	ready := s.successfulPatch && !s.patchFailure && !s.verificationFailure && s.loopFailure == "" && len(s.changedFiles) > 0 && (s.testStatus == "passed" || s.testStatus == "not_applicable")
	return service.CoderTaskCompletionEvidence{
		TaskID:          s.taskID,
		ChangedFiles:    append([]string(nil), s.changedFiles...),
		SuccessfulPatch: s.successfulPatch,
		TestStatus:      s.testStatus,
		TestReceipt:     s.testReceipt,
		Ready:           ready,
		FailureReason:   "mock owner evidence incomplete: " + s.loopFailure,
	}, nil
}

func newImpactLoopRequest(t *testing.T) CodeExecutionRequest {
	t.Helper()
	taskID := modulecore.NewTaskID()
	return newOrchestratorTestCodeExecutionRequest(t, taskID, "apply change", routing.RouteCODE1, "session-1", "test", "chat-1")
}

func TestCoderLoopBlocksFinalReportAfterFailedPatchUntilCheckedRevision(t *testing.T) {
	coder := &impactLoopCoder{responses: []string{
		`{"type":"patch_proposal","intent":"first","patch":"[{\"type\":\"file_edit\",\"action\":\"create\",\"target\":\"changed.txt\",\"content\":\"first\"}]","tests":[]}`,
		`{"type":"read_request","actions":[{"action":"shell_command","target":"cat changed.txt"}]}`,
		`{"type":"final_report","summary":"premature","changed_files":[],"tests_run":[],"remaining_risks":[]}`,
		`{"type":"patch_proposal","intent":"revised","patch":"[{\"type\":\"file_edit\",\"action\":\"update\",\"target\":\"changed.txt\",\"content\":\"revised\"}]","tests":[]}`,
		`{"type":"final_report","summary":"checked","changed_files":["changed.txt"],"tests_run":[],"remaining_risks":[]}`,
	}}
	failed := patch.NewPatchExecutionResult().WithFailureMetadata("test_impact_failed", "owner receipt failed", false)
	failed.Success = false
	failed.TestStatus = "failed"
	passed := patch.NewPatchExecutionResult().WithSummary("checked")
	passed.AddResult(patch.CommandResult{Success: true, Output: "applied"})
	passed.Success = true
	passed.TestStatus = "passed"
	passed.TestReceipt = "owner-receipt"
	worker := &impactLoopWorker{
		patchResults: []*patch.PatchExecutionResult{failed, passed},
		observations: [][]coderloop.ObservationActionResult{{coderloop.NewObservationActionResult("shell_command", "cat changed.txt", "old content", nil)}},
	}
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
	final := `{"type":"final_report","summary":"checked","changed_files":[],"tests_run":["untrusted test claim"],"remaining_risks":[]}`
	testRequest := `{"type":"test_request","actions":[{"action":"shell_command","target":"go test ./..."}]}`
	patchRequest := `{"type":"patch_proposal","intent":"create a file","patch":"[{\"type\":\"file_edit\",\"action\":\"create\",\"target\":\"changed.txt\",\"content\":\"checked\"}]","tests":[]}`
	for _, tc := range []struct {
		name    string
		replies []string
		failed  bool
	}{
		{"parse failures", []string{"bad", "bad"}, true},
		{"turn limit", []string{`{"type":"plan","task_summary":"work","steps":["inspect"],"risk":[]}`}, true},
		{"failed verification", []string{testRequest, final}, true},
		{"recovered verification", []string{patchRequest, final}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			coder := &impactLoopCoder{responses: tc.replies}
			worker := &impactLoopWorker{
				observations: [][]coderloop.ObservationActionResult{{coderloop.NewObservationActionResult("shell_command", "go test ./...", "", errors.New("failed"))}, {coderloop.NewObservationActionResult("shell_command", "go test ./...", "passed", nil)}},
				patchResults: []*patch.PatchExecutionResult{func() *patch.PatchExecutionResult {
					result := patch.NewPatchExecutionResult()
					result.AddResult(patch.CommandResult{Success: true, Output: "applied"})
					result.Success = true
					result.TestStatus = "passed"
					result.TestReceipt = "owner-receipt"
					return result
				}()},
			}
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
	workspace, worker, owner := newOrchestratorWorker(t)
	rootTaskID := modulecore.NewTaskID()
	createdTask, run, err := owner.CreateAndStartRun(context.Background(), domaintask.Task{
		TaskID:     rootTaskID,
		Title:      "bounded CoderLoop terminal fixture",
		OwnerID:    "mio",
		Assignee:   "shiro",
		Route:      domaintask.RouteCODE3,
		ModuleID:   "RenCrow_CORE",
		ModuleRoot: workspace,
	}, domaintask.SharedRoleContext{TaskID: rootTaskID, UserIntent: "implement"})
	if err != nil {
		t.Fatalf("create scoped running task: %v", err)
	}
	if createdTask.TaskID != rootTaskID || run.TaskID != rootTaskID {
		t.Fatalf("scoped task/run identity mismatch: task=%#v run=%#v", createdTask, run)
	}
	coder := &impactLoopCoder{responses: []string{"invalid provider proposal", "invalid provider proposal"}}
	mio := &mockMioAgent{decision: routing.NewDecision(routing.RouteCODE3, 1, "test route"), response: "chat"}
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{response: "unused"}, nil, nil, coder, nil, worker)
	orch.SetTaskLifecycleManager(owner)
	orch.SetCoderLoopPrompt("test loop")
	_, err = orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		RootTaskID: rootTaskID.String(), CanonicalRunID: run.RunID,
		SessionID: newMockCanonicalSessionID("viewer", "user"), Channel: "viewer", ChatID: "user", UserMessage: "/code3 implement",
	})
	if err == nil {
		t.Fatal("incomplete loop accepted as route success")
	}
	if coder.calls != 2 {
		t.Fatalf("outer executor restarted terminated loop: calls=%d error=%v", coder.calls, err)
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

type isolatedHistoryLoopCoder struct {
	responses []string
	index     int
	histories [][]llm.Message
}

func (c *isolatedHistoryLoopCoder) Generate(context.Context, conversation.TurnInput, string) (string, error) {
	return "", errors.New("unused Generate path")
}

func (c *isolatedHistoryLoopCoder) GenerateWithContext(_ context.Context, messages []llm.Message) (string, error) {
	c.histories = append(c.histories, append([]llm.Message(nil), messages...))
	if c.index >= len(c.responses) {
		if len(messages) > 0 {
			return "", fmt.Errorf("unexpected extra CoderLoop turn after: %s", messages[len(messages)-1].Content)
		}
		return "", errors.New("unexpected extra CoderLoop turn")
	}
	response := c.responses[c.index]
	c.index++
	return response, nil
}

func TestCoderLoopsKeepFourConversationHistoriesAndRealOwnerEvidenceIndependent(t *testing.T) {
	t.Setenv(testimpact.HelperModeEnv, "passed")
	roots := make([]string, 4)
	for index := range roots {
		roots[index] = makeOrchestratorImpactWorkspace(t)
		if err := os.WriteFile(filepath.Join(roots[index], "README.md"), []byte("shared fixture read\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	limits := taskmanager.DefaultParallelLimits()
	limits.Global, limits.PerModule, limits.CodingTasks = 4, 1, 4
	owner := taskmanager.New(store, limits)
	t.Cleanup(func() {
		if err := owner.Close(); err != nil {
			t.Error(err)
		}
	})
	worker := service.NewWorkerExecutionService(config.WorkerConfig{
		Workspace:                roots[0],
		ProtectedPatterns:        []string{".env*"},
		ActionOnProtected:        "error",
		CommandTimeout:           10,
		GitTimeout:               10,
		TestImpactBinary:         os.Args[0],
		TestImpactTimeoutSeconds: 30,
	}, owner)
	routes := []routing.Route{routing.RouteCODE1, routing.RouteCODE2, routing.RouteCODE3, routing.RouteCODE4}
	domainRoutes := []domaintask.Route{domaintask.RouteCODE1, domaintask.RouteCODE2, domaintask.RouteCODE3, domaintask.RouteCODE4}
	taskIDs := make([]modulecore.TaskID, len(roots))
	contexts := make([]context.Context, len(roots))
	coders := make([]*isolatedHistoryLoopCoder, len(roots))
	executors := make([]*CoderLoopExecutor, len(roots))
	requests := make([]CodeExecutionRequest, len(roots))
	filenames := make([]string, len(roots))
	sharedMessage := "inspect and make an isolated task change"
	for index, root := range roots {
		taskIDs[index] = modulecore.NewTaskID()
		ownerTask, run, err := owner.CreateAndStartRun(context.Background(), domaintask.Task{
			TaskID:     taskIDs[index],
			Title:      fmt.Sprintf("independent coder task %d", index+1),
			OwnerID:    "mio",
			Assignee:   "shiro",
			Route:      domainRoutes[index],
			ModuleID:   fmt.Sprintf("isolated-module-%d", index+1),
			ModuleRoot: root,
		}, domaintask.SharedRoleContext{TaskID: taskIDs[index], UserIntent: sharedMessage})
		if err != nil {
			t.Fatalf("admit task %d: %v", index+1, err)
		}
		if ownerTask.TaskID != taskIDs[index] || run.TaskID != taskIDs[index] {
			t.Fatalf("task %d owner identity mismatch: task=%#v run=%#v", index+1, ownerTask, run)
		}
		ctx, err := domainexecution.WithIdentity(context.Background(), taskIDs[index], run.RunID, "")
		if err != nil {
			t.Fatal(err)
		}
		scope, err := domaintool.NewToolExecutionScope(
			string(modulecore.NewRequestID()), domaintool.ActorKindAgent, "shiro", "",
			[]string{domaintool.DataScopePublic}, domaintool.AuthenticationSourceAgentOrchestrator,
		)
		if err != nil {
			t.Fatal(err)
		}
		contexts[index] = domaintool.WithToolExecutionScope(ctx, scope)

		filenames[index] = fmt.Sprintf("isolated-task-%d.txt", index+1)
		patchJSON, err := json.Marshal([]map[string]any{{
			"type": "file_edit", "action": "create", "target": filepath.Join(root, filenames[index]), "content": fmt.Sprintf("task-%d", index+1),
		}})
		if err != nil {
			t.Fatal(err)
		}
		patchResponse, err := json.Marshal(map[string]any{
			"type": "patch_proposal", "intent": "create this task's isolated file", "patch": string(patchJSON), "tests": []string{},
		})
		if err != nil {
			t.Fatal(err)
		}
		finalResponse, err := json.Marshal(map[string]any{
			"type": "final_report", "summary": "isolated task complete", "changed_files": []string{"untrusted-other-file"},
			"tests_run": []string{"untrusted test claim"}, "remaining_risks": []string{},
		})
		if err != nil {
			t.Fatal(err)
		}
		coders[index] = &isolatedHistoryLoopCoder{responses: []string{
			`{"type":"read_request","actions":[{"action":"shell_command","target":"cat README.md"}]}`,
			string(patchResponse),
			string(finalResponse),
		}}
		executors[index] = NewCoderLoopExecutor(coders[index], worker, "coder", "shared task prompt", nil)
		requests[index] = newOrchestratorTestCodeExecutionRequest(t, taskIDs[index], sharedMessage, routes[index], "same-conversation-history", "test", "same-chat")
	}

	type loopRun struct {
		response CodeExecutionResponse
		err      error
	}
	results := make([]loopRun, len(roots))
	var wait sync.WaitGroup
	for index := range roots {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			results[index].response, results[index].err = executors[index].Execute(contexts[index], requests[index])
		}(index)
	}
	wait.Wait()

	for index, result := range results {
		if result.err != nil || !result.response.Handled {
			t.Fatalf("task %d CoderLoop failed: response=%#v err=%v", index+1, result.response, result.err)
		}
		if !strings.Contains(result.response.Response, "- "+filenames[index]) || !strings.Contains(result.response.Response, filepath.Join(roots[index], "Tmp", "test-results")) {
			t.Fatalf("task %d response omitted its owner-derived file or receipt: %q", index+1, result.response.Response)
		}
		if strings.Contains(result.response.Response, "untrusted-other-file") || strings.Contains(result.response.Response, "untrusted test claim") {
			t.Fatalf("task %d displayed Coder-owned evidence claims: %q", index+1, result.response.Response)
		}
		if len(coders[index].histories) != 3 {
			t.Fatalf("task %d history turn count=%d, want 3", index+1, len(coders[index].histories))
		}
		if index > 0 && !reflect.DeepEqual(coders[0].histories[0], coders[index].histories[0]) {
			t.Fatalf("task %d did not receive the shared initial conversation history: %#v", index+1, coders[index].histories[0])
		}
		for historyIndex, history := range coders[index].histories {
			for messageIndex, message := range history {
				for otherIndex, otherRoot := range roots {
					if otherIndex != index && strings.Contains(message.Content, otherRoot) {
						t.Fatalf("task %d history turn %d message %d contains task %d workspace evidence", index+1, historyIndex+1, messageIndex+1, otherIndex+1)
					}
				}
			}
		}
	}
}
