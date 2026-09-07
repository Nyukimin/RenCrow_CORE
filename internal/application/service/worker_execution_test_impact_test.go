package service

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/coderloop"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/patch"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/proposal"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/testsupport/testimpact"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestMain(m *testing.M) {
	testimpact.RunIfRequested()
	os.Exit(m.Run())
}

func testImpactWorkerConfig(workspace, binary string) config.WorkerConfig {
	return config.WorkerConfig{
		Workspace:                workspace,
		TestImpactBinary:         binary,
		TestImpactTimeoutSeconds: 30,
		CommandTimeout:           5,
		StopOnError:              true,
		ProtectedPatterns:        []string{},
		ActionOnProtected:        "error",
	}
}

func testImpactPatch(workspace string) *proposal.Proposal {
	target := filepath.Join(workspace, "changed.txt")
	patchJSON := fmt.Sprintf(`[{"type":"file_edit","action":"create","target":%q,"content":"changed"}]`, target)
	return proposal.NewProposal("test impact integration", patchJSON, "", "")
}

func testImpactPlanReplacement(workspace string) *proposal.Proposal {
	target := filepath.Join(workspace, "scripts", "test-local.plan.json")
	patchJSON := fmt.Sprintf(`[{"type":"file_edit","action":"update","target":%q,"content":%q}]`, target, `{"name":"replacement"}`)
	return proposal.NewProposal("replace canonical plan", patchJSON, "", "")
}

func testImpactRunnerReplacement(workspace string) *proposal.Proposal {
	target := filepath.Join(workspace, "scripts", "test-local.ps1")
	patchJSON := fmt.Sprintf(`[{"type":"file_edit","action":"update","target":%q,"content":%q}]`, target, "runner replacement")
	return proposal.NewProposal("replace canonical runner", patchJSON, "", "")
}

func makeCanonicalImpactWorkspace(t *testing.T) string {
	t.Helper()
	workspace := t.TempDir()
	return testimpact.InitWorkspace(t, workspace)
}

func TestExecuteProposalRunsAndBindsOwnerTestImpactReceipt(t *testing.T) {
	t.Setenv(testimpact.HelperModeEnv, "passed")
	workspace := makeCanonicalImpactWorkspace(t)
	worker := newTestWorkerExecutionService(t, testImpactWorkerConfig(workspace, os.Args[0]))

	result, err := executeOwnedProposal(t, worker, context.Background(), modulecore.NewTaskID(), testImpactPatch(workspace))
	if err != nil {
		t.Fatalf("ExecuteProposal failed: %v", err)
	}
	if !result.Success || result.TestStatus != "passed" {
		t.Fatalf("expected checked success, got %#v", result)
	}
	if result.TestReceipt == "" {
		t.Fatal("expected owner receipt path")
	}
	if _, err := os.Stat(result.TestReceipt); err != nil {
		t.Fatalf("owner receipt missing: %v", err)
	}
	if !strings.Contains(result.Summary, "test-impact=passed") {
		t.Fatalf("summary does not include finalized test status: %q", result.Summary)
	}
}

func TestImpactTimeoutUsesDedicatedPositiveDefault(t *testing.T) {
	if got := testImpactTimeout(0); got != time.Hour {
		t.Fatalf("default test-impact timeout = %s, want 1h", got)
	}
	if got := testImpactTimeout(-1); got != time.Hour {
		t.Fatalf("negative test-impact timeout = %s, want 1h default", got)
	}
	if got := testImpactTimeout(17); got != 17*time.Second {
		t.Fatalf("configured test-impact timeout = %s, want 17s", got)
	}
}

func TestExecuteProposalRequiresFullFallbackWhenCanonicalPlanChanges(t *testing.T) {
	t.Setenv(testimpact.HelperModeEnv, "passed")
	workspace := makeCanonicalImpactWorkspace(t)
	worker := newTestWorkerExecutionService(t, testImpactWorkerConfig(workspace, os.Args[0]))

	result, err := executeOwnedProposal(t, worker, context.Background(), modulecore.NewTaskID(), testImpactPlanReplacement(workspace))
	if err != nil {
		t.Fatalf("ExecuteProposal should return a blocked result for an unbounded plan replacement: %v", err)
	}
	if result.Success || result.TestStatus != "blocked" || !strings.Contains(result.FailureReason, "full fallback") {
		t.Fatalf("expected full fallback gate, got %#v", result)
	}
}

func TestExecuteProposalAllowsCanonicalPlanChangeWithOwnerFullFallback(t *testing.T) {
	t.Setenv(testimpact.HelperModeEnv, "full_fallback")
	workspace := makeCanonicalImpactWorkspace(t)
	worker := newTestWorkerExecutionService(t, testImpactWorkerConfig(workspace, os.Args[0]))

	result, err := executeOwnedProposal(t, worker, context.Background(), modulecore.NewTaskID(), testImpactPlanReplacement(workspace))
	if err != nil {
		t.Fatalf("ExecuteProposal failed: %v", err)
	}
	if !result.Success || result.TestStatus != "passed" {
		t.Fatalf("expected owner full fallback success, got %#v", result)
	}
}

func TestExecuteProposalRejectsFailedOwnerTestImpactStep(t *testing.T) {
	t.Setenv(testimpact.HelperModeEnv, "failed_step")
	workspace := makeCanonicalImpactWorkspace(t)
	worker := newTestWorkerExecutionService(t, testImpactWorkerConfig(workspace, os.Args[0]))

	result, err := executeOwnedProposal(t, worker, context.Background(), modulecore.NewTaskID(), testImpactPatch(workspace))
	if err != nil {
		t.Fatalf("ExecuteProposal should return a failed result for an owner step failure: %v", err)
	}
	if result.Success || result.TestStatus != "failed" || result.FailureKind != "test_impact_failed" {
		t.Fatalf("expected failed owner step to gate completion, got %#v", result)
	}
	if !strings.Contains(result.FailureReason, "result is \"failed\"") {
		t.Fatalf("expected owner step failure reason, got %q", result.FailureReason)
	}
}

func TestExecuteProposalRequiresFullFallbackWhenCanonicalRunnerChanges(t *testing.T) {
	t.Setenv(testimpact.HelperModeEnv, "passed")
	workspace := makeCanonicalImpactWorkspace(t)
	worker := newTestWorkerExecutionService(t, testImpactWorkerConfig(workspace, os.Args[0]))

	result, err := executeOwnedProposal(t, worker, context.Background(), modulecore.NewTaskID(), testImpactRunnerReplacement(workspace))
	if err != nil {
		t.Fatalf("ExecuteProposal should return a blocked result for an unbounded runner replacement: %v", err)
	}
	if result.Success || result.TestStatus != "blocked" || !strings.Contains(result.FailureReason, "full fallback") {
		t.Fatalf("expected runner full fallback gate, got %#v", result)
	}
}

func TestRunTestImpactCommandKillsOwnedProcessTreeOnTimeout(t *testing.T) {
	t.Setenv(testimpact.HelperModeEnv, "tree_timeout")
	workspace := t.TempDir()
	marker := filepath.Join(workspace, "tree-marker.log")
	t.Setenv("RENCROW_TEST_IMPACT_HELPER_MARKER", marker)
	output := filepath.Join(workspace, "Tmp", "test-results", "timeout.json")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, _, err := runWorkerCommand(ctx, workspace, os.Args[0],
		"resolve", "--repo", workspace, "--output", output)
	if err == nil {
		t.Fatal("runWorkerCommand returned nil after timeout")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		data, readErr := os.ReadFile(marker)
		if readErr == nil && len(data) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	before, _ := os.ReadFile(marker)
	if len(before) == 0 {
		t.Fatal("timeout helper never started its child")
	}
	time.Sleep(250 * time.Millisecond)
	after, _ := os.ReadFile(marker)
	if string(before) != string(after) {
		t.Fatalf("owned process tree continued after timeout: before=%d after=%d", len(before), len(after))
	}
}

func TestExecuteProposalReportsNotApplicableForScratchWorkspace(t *testing.T) {
	workspace := nonRepositoryScratchWorkspace(t)
	worker := newTestWorkerExecutionService(t, testImpactWorkerConfig(workspace, filepath.Join(workspace, "missing-impact")))

	scope, err := worker.inspectTestImpactScope()
	if err != nil {
		t.Fatalf("inspectTestImpactScope failed: %v", err)
	}
	if scope.status != "not_applicable" {
		t.Fatalf("expected scratch scope, got %#v", scope)
	}
	result := worker.executeTestImpact(context.Background(), modulecore.NewTaskID(), &patch.PatchExecutionResult{Success: true}, scope)
	result = worker.finalizeTestImpactResult(result)
	if !result.Success || result.TestStatus != "not_applicable" {
		t.Fatalf("expected not_applicable success, got %#v", result)
	}
	if !strings.Contains(result.Summary, "test-impact=not_applicable") {
		t.Fatalf("summary does not disclose not_applicable: %q", result.Summary)
	}
}

func nonRepositoryScratchWorkspace(t *testing.T) string {
	t.Helper()
	current, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if isRepositoryWorkspace(current) {
			for {
				parent := filepath.Dir(current)
				if parent == current || !isRepositoryWorkspace(parent) {
					name := strings.NewReplacer("/", "_", "\\", "_").Replace(t.Name())
					return filepath.Join(parent, "rencrow-test-scratch-"+name)
				}
				current = parent
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			t.Fatal("could not find a path outside the Git repository")
		}
		current = parent
	}
}

func TestExecuteProposalBlocksNestedGitWorkspaceBeforePatch(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "nested")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	worker := newTestWorkerExecutionService(t, testImpactWorkerConfig(workspace, filepath.Join(workspace, "missing-impact")))
	target := filepath.Join(workspace, "must-not-apply.txt")
	patchJSON := fmt.Sprintf(`[{"type":"file_edit","action":"create","target":%q,"content":"blocked"}]`, target)

	_, err := executeOwnedProposal(t, worker, context.Background(), modulecore.NewTaskID(), proposal.NewProposal("nested workspace", patchJSON, "", ""))
	if err == nil || !strings.Contains(err.Error(), "nested inside a Git repository") {
		t.Fatalf("expected nested Git workspace block, got %v", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("patch must not apply to nested Git workspace: %v", statErr)
	}
}

func TestExecuteProposalBlocksMissingOwnerForCanonicalRepository(t *testing.T) {
	workspace := makeCanonicalImpactWorkspace(t)
	worker := newTestWorkerExecutionService(t, testImpactWorkerConfig(workspace, filepath.Join(workspace, "missing-impact")))

	result, err := executeOwnedProposal(t, worker, context.Background(), modulecore.NewTaskID(), testImpactPatch(workspace))
	if err != nil {
		t.Fatalf("ExecuteProposal should return a result for owner failure: %v", err)
	}
	if result.Success || result.TestStatus != "blocked" || result.FailureKind != "test_impact_blocked" {
		t.Fatalf("expected blocked owner result, got %#v", result)
	}
	if result.ExecutedCmds != 1 || result.FailedCmds != 0 {
		t.Fatalf("applied command disclosure lost: %#v", result)
	}
	if !strings.Contains(result.Summary, "test-impact=blocked") {
		t.Fatalf("summary does not disclose blocked verification: %q", result.Summary)
	}
}

func TestExecuteProposalRejectsReceiptSourceMismatch(t *testing.T) {
	t.Setenv(testimpact.HelperModeEnv, "bad_source")
	workspace := makeCanonicalImpactWorkspace(t)
	worker := newTestWorkerExecutionService(t, testImpactWorkerConfig(workspace, os.Args[0]))

	result, err := executeOwnedProposal(t, worker, context.Background(), modulecore.NewTaskID(), testImpactPatch(workspace))
	if err != nil {
		t.Fatalf("ExecuteProposal should return a result for receipt failure: %v", err)
	}
	if result.Success || result.TestStatus != "blocked" {
		t.Fatalf("expected blocked receipt binding, got %#v", result)
	}
	if !strings.Contains(result.FailureReason, "sourceHash") {
		t.Fatalf("expected source binding failure, got %q", result.FailureReason)
	}
}

func TestExecuteProposalBlocksCanonicalPlanDeletionAfterPatch(t *testing.T) {
	workspace := makeCanonicalImpactWorkspace(t)
	worker := newTestWorkerExecutionService(t, testImpactWorkerConfig(workspace, filepath.Join(workspace, "missing-impact")))
	planPath := filepath.Join(workspace, "scripts", "test-local.plan.json")
	patchJSON := fmt.Sprintf(`[{"type":"file_edit","action":"delete","target":%q}]`, planPath)

	result, err := executeOwnedProposal(t, worker, context.Background(), modulecore.NewTaskID(), proposal.NewProposal("delete plan", patchJSON, "", ""))
	if err != nil {
		t.Fatalf("ExecuteProposal should return a blocked result after plan deletion: %v", err)
	}
	if result.Success || result.TestStatus != "blocked" {
		t.Fatalf("expected blocked post-patch applicability, got %#v", result)
	}
	if !strings.Contains(result.FailureReason, "disappeared") {
		t.Fatalf("expected canonical plan disappearance reason, got %q", result.FailureReason)
	}
	if _, err := os.Stat(planPath); !os.IsNotExist(err) {
		t.Fatalf("test patch should have applied plan deletion, stat error=%v", err)
	}
}

func TestExecuteProposalBlocksGitWorkspaceMissingCanonicalPlanBeforePatch(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	worker := newTestWorkerExecutionService(t, testImpactWorkerConfig(workspace, filepath.Join(workspace, "missing-impact")))
	target := filepath.Join(workspace, "must-not-apply.txt")
	patchJSON := fmt.Sprintf(`[{"type":"file_edit","action":"create","target":%q,"content":"blocked"}]`, target)

	_, err := executeOwnedProposal(t, worker, context.Background(), modulecore.NewTaskID(), proposal.NewProposal("missing plan", patchJSON, "", ""))
	if err == nil || !strings.Contains(err.Error(), "missing canonical scripts/test-local.plan.json") {
		t.Fatalf("expected missing canonical plan block, got %v", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("patch must not apply to an unplanned Git workspace: %v", statErr)
	}
}

// The owner lives outside the patch workspace so git operations cannot commit test state.
func newTestWorkerExecutionService(t *testing.T, cfg config.WorkerConfig) *workerExecutionService {
	t.Helper()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	owner := taskmanager.New(store, taskmanager.DefaultParallelLimits())
	t.Cleanup(func() {
		if err := owner.Close(); err != nil {
			t.Error(err)
		}
	})
	return NewWorkerExecutionService(cfg, owner)
}

// Every existing patch scenario enters through persisted Task/Run ownership.
func executeOwnedProposal(t *testing.T, worker *workerExecutionService, ctx context.Context, taskID modulecore.TaskID, p *proposal.Proposal) (*patch.PatchExecutionResult, error) {
	t.Helper()
	return worker.ExecuteProposal(bindOwnedWorkerContext(t, worker, ctx, taskID), taskID, p)
}

func bindOwnedWorkerContext(t *testing.T, worker *workerExecutionService, ctx context.Context, taskID modulecore.TaskID) context.Context {
	t.Helper()
	task, err := worker.owner.Create(ctx, domaintask.Task{TaskID: taskID, Title: "worker patch fixture", Assignee: "shiro"}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := worker.owner.StartRunWithReason(ctx, task.TaskID, domaintask.RunStartReasonFirst)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err = domainexecution.WithIdentity(ctx, task.TaskID, run.RunID, "")
	if err != nil {
		t.Fatal(err)
	}
	scope, err := domaintool.NewToolExecutionScope(string(modulecore.NewRequestID()), domaintool.ActorKindAgent, "shiro", "", []string{domaintool.DataScopePublic}, domaintool.AuthenticationSourceAgentOrchestrator)
	if err != nil {
		t.Fatal(err)
	}
	return domaintool.WithToolExecutionScope(ctx, scope)
}

type observationAdmissionCaller struct {
	calls  int
	ctx    context.Context
	during func()
}

func (c *observationAdmissionCaller) CallTool(ctx context.Context, _ string, _ map[string]any) (string, error) {
	c.calls++
	c.ctx = ctx
	if c.during != nil {
		c.during()
	}
	return "observed", nil
}

func TestWorkerObservationAdmission(t *testing.T) {
	for _, mode := range []string{"valid", "nil receiver", "nil owner", "nil context", "missing scope", "wrong task", "canceled", "terminal", "closed", "cancel during", "finish during"} {
		t.Run(mode, func(t *testing.T) {
			worker := newTestWorkerExecutionService(t, config.WorkerConfig{Workspace: t.TempDir()})
			owner := worker.owner
			task, err := owner.Create(context.Background(), domaintask.Task{TaskID: modulecore.NewTaskID(), Title: "observation", Assignee: "shiro"}, domaintask.SharedRoleContext{})
			if err != nil {
				t.Fatal(err)
			}
			run, err := owner.StartRunWithReason(context.Background(), task.TaskID, domaintask.RunStartReasonFirst)
			if err != nil {
				t.Fatal(err)
			}
			ctx, err := domainexecution.WithIdentity(context.Background(), task.TaskID, run.RunID, "")
			if err != nil {
				t.Fatal(err)
			}
			scope, err := domaintool.NewToolExecutionScope(string(modulecore.NewRequestID()), domaintool.ActorKindAgent, "shiro", "", []string{domaintool.DataScopePublic}, domaintool.AuthenticationSourceAgentOrchestrator)
			if err != nil {
				t.Fatal(err)
			}
			bare := ctx
			ctx = domaintool.WithToolExecutionScope(ctx, scope)
			ownedCtx := ctx
			caller := &observationAdmissionCaller{}
			worker.SetMCPToolCaller(caller)
			finish := func() {
				if _, err := owner.Succeed(ownedCtx, task.TaskID, "done"); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "nil receiver":
				worker = nil
			case "nil owner":
				worker.owner = nil
			case "nil context":
				ctx = nil
			case "missing scope":
				ctx = bare
			case "wrong task":
				ctx, err = domainexecution.WithIdentity(context.Background(), modulecore.NewTaskID(), run.RunID, "")
				if err != nil {
					t.Fatal(err)
				}
				ctx = domaintool.WithToolExecutionScope(ctx, scope)
			case "canceled", "cancel during":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
				if mode == "canceled" {
					cancel()
				} else {
					caller.during = cancel
				}
			case "terminal":
				finish()
			case "closed":
				if err := owner.Close(); err != nil {
					t.Fatal(err)
				}
			case "finish during":
				caller.during = finish
			}
			// Recover only to make a missing nil-receiver guard a readable regression.
			defer func() {
				if value := recover(); value != nil {
					t.Errorf("observation admission panicked: %v", value)
				}
			}()
			results, err := worker.ExecuteObservation(ctx, []ObservationAction{{Action: "mcp_tool", Target: "find_symbol"}, {Action: "mcp_tool", Target: "find_symbol"}})
			if mode == "valid" {
				original, _ := domainexecution.IdentityFromContext(ctx)
				received, _ := domainexecution.IdentityFromContext(caller.ctx)
				originalScope, _ := domaintool.ToolExecutionScopeFromContext(ctx)
				receivedScope, _ := domaintool.ToolExecutionScopeFromContext(caller.ctx)
				if original != received || !reflect.DeepEqual(originalScope, receivedScope) {
					t.Fatal("action deadline changed caller identity or scope")
				}
				if err != nil || caller.calls != 2 || len(results) != 2 || results[0].Status != "ok" || results[1].Status != "ok" {
					t.Fatalf("valid observation: err=%v calls=%d results=%#v", err, caller.calls, results)
				}
			} else if mode == "cancel during" || mode == "finish during" {
				if err == nil || caller.calls != 1 || len(results) != 1 || results[0].Status != "error" {
					t.Fatalf("late result accepted or next action started: err=%v calls=%d results=%#v", err, caller.calls, results)
				}
			} else {
				if err == nil || caller.calls != 0 || len(results) != 0 {
					t.Fatalf("invalid request executed: err=%v calls=%d results=%#v", err, caller.calls, results)
				}
			}
		})
	}
}

func TestWorkerObservationAdmissionEmptyStillRequiresOwner(t *testing.T) {
	worker := NewWorkerExecutionService(config.WorkerConfig{})
	if _, err := worker.ExecuteObservation(context.Background(), nil); err == nil {
		t.Fatal("empty actions bypassed owner admission")
	}
}

func TestObservationCommandRejectsShellSyntaxAndPrefixLookalikes(t *testing.T) {
	for _, command := range []string{"git status; echo unexpected", "git status && echo unexpected", "git status | cat", "git status > marker", "cat $(echo marker)", "cat `echo marker`", "cat $HOME", "git status\necho unexpected", "git status &", "cat < marker", "git status_suffix", "go testfake", "catalog file", "git status # comment", "cat *.go", "cat \"unclosed"} {
		if _, err := observationCommandArgs(command); err == nil {
			t.Errorf("unsafe command accepted: %q", command)
		}
	}
}

func TestObservationCommandLiteralArguments(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  []string
	}{
		{`git grep 'a; b | c'`, []string{"git", "grep", "a; b | c"}},
		{`cat 'file name.txt'`, []string{"cat", "file name.txt"}},
		{`cat 'C:\source\file.txt'`, []string{"cat", `C:\source\file.txt`}},
		{`cat "C:\source\file.txt"`, []string{"cat", `C:\source\file.txt`}},
		{`git grep '$HOME'`, []string{"git", "grep", "$HOME"}},
		{`git diff -- 'file name.go'`, []string{"git", "diff", "--", "file name.go"}},
	} {
		args, err := observationCommandArgs(tc.input)

		if err != nil || !reflect.DeepEqual(args, tc.want) {
			t.Errorf("%q args=%q err=%v want=%q", tc.input, args, err, tc.want)
		}
	}
}

func TestObservationCommandExecutesLiteralGitArgument(t *testing.T) {
	workspace := makeCanonicalImpactWorkspace(t)
	worker := newTestWorkerExecutionService(t, config.WorkerConfig{Workspace: workspace})
	// A literal nonexistent path is harmless; git must not parse its contents as a shell command.
	result := worker.executeSingleObservation(context.Background(), ObservationAction{Action: "shell_command", Target: `git status --porcelain -- 'not a path; echo injected'`})
	if result.Status != "ok" || result.Output != "" {
		t.Fatalf("literal native argv result=%#v", result)
	}
}

func TestObservationFindRejectsSideEffectsAndUnknownPredicates(t *testing.T) {
	for _, command := range []string{"find . -delete", "find . -exec echo marker '+'", "find . -execdir echo marker '+'", "find . -ok echo marker '+'", "find . -okdir echo marker '+'", "find . -fprint marker", "find . -fprintf marker format", "find . -fls marker", "find . -future-option", "find . -name", "find . -maxdepth", "find . -type", "find . -print extra-path", "find -L . -print"} {
		if _, err := observationCommandArgs(command); err == nil {
			t.Errorf("unsafe find accepted: %q", command)
		}
	}
}
func TestObservationFindAllowsReadPredicatesAndLiteralValues(t *testing.T) {
	for _, command := range []string{"find .", "find . -name '*.go' -type f -print", "find . -name '-delete'", "find . -iname '-exec'", "find . -maxdepth 3 -mindepth 1 -empty", "find src test -path '*cache*' -prune -o -type f -print0", "find . '(' -name '*.go' -o -name '*.md' ')' -print"} {
		if _, err := observationCommandArgs(command); err != nil {
			t.Errorf("read query rejected %q: %v", command, err)
		}
	}
}

func TestObservationGitRejectsSideEffectsAndUnknownOptions(t *testing.T) {
	for _, command := range []string{"git diff --output=marker", "git log --output marker", "git diff --ext-diff", "git show --textconv", "git grep --textconv value", "git log --show-signature", "git log --format='%G?'", "git status --future-option", "git -c alias.x=unknown status", "git log -n", "git grep -e"} {
		if _, err := observationCommandArgs(command); err == nil {
			t.Errorf("unsafe Git option accepted: %q", command)
		}
	}
}
func TestObservationGitAllowsReadOptions(t *testing.T) {
	for _, command := range []string{"git status --porcelain", "git diff --stat", "git diff -- '--output=filename'", "git log --oneline -n 5", "git show --name-only HEAD", "git grep -n -e '--output=literal' -- src", "git ls-files --cached"} {
		if _, err := observationCommandArgs(command); err != nil {
			t.Errorf("safe query rejected %q: %v", command, err)
		}
	}
}
func TestObservationGitDiffDisablesExternalDriver(t *testing.T) {
	workspace := makeCanonicalImpactWorkspace(t)
	cmd := exec.Command("git", "add", "--", "scripts/test-local.ps1")
	cmd.Dir = workspace
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("stage fixture: %v %s", err, output)
	}
	if err := os.WriteFile(filepath.Join(workspace, "scripts", "test-local.ps1"), []byte("param()\n# observation changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_EXTERNAL_DIFF", "rencrow-nonexistent-external-driver")
	worker := newTestWorkerExecutionService(t, config.WorkerConfig{Workspace: workspace})
	result := worker.executeSingleObservation(context.Background(), ObservationAction{Action: "shell_command", Target: "git diff"})
	if result.Status != "ok" || !strings.Contains(result.Output, "+# observation changed") {
		t.Fatalf("builtin diff was not used: %#v", result)
	}
}

func TestObservationCanonicalTestUsesOwnerReceipt(t *testing.T) {
	for _, mode := range []string{"passed", "failed_step", "bad_source", "missing_plan"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv(testimpact.HelperModeEnv, mode)
			workspace := makeCanonicalImpactWorkspace(t)
			if mode == "missing_plan" {
				if err := os.Remove(filepath.Join(workspace, "scripts", "test-local.plan.json")); err != nil {
					t.Fatal(err)
				}
			}
			worker := newTestWorkerExecutionService(t, testImpactWorkerConfig(workspace, os.Args[0]))
			ctx := bindOwnedWorkerContext(t, worker, context.Background(), modulecore.NewTaskID())
			results, err := worker.ExecuteObservation(ctx, []ObservationAction{{Action: "shell_command", Target: "go test ./..."}, {Action: "shell_command", Target: "go build ./..."}})
			if err != nil || len(results) != 2 {
				t.Fatalf("results=%#v error=%v", results, err)
			}
			if mode == "passed" {
				if results[0].Status != "ok" || !strings.Contains(results[0].Output, "test-impact=passed") || !strings.Contains(results[0].Output, "receipt=") {
					t.Fatalf("canonical pass receipt missing: %#v", results)
				}
			} else if results[0].Status != "error" || results[1].Status != "error" {
				t.Fatalf("owner failure became success: %#v", results)
			}

			wantStatus := "blocked"
			if mode == "passed" {
				wantStatus = "passed"
			} else if mode == "failed_step" {
				wantStatus = "failed"
			}
			if results[0].TestStatus != wantStatus || results[1].TestStatus != wantStatus || results[0].TestReceipt != results[1].TestReceipt {
				t.Fatalf("structured owner result lost: %#v", results)
			}
			receipts, err := filepath.Glob(filepath.Join(workspace, "Tmp", "test-results", "*", "receipt.json"))
			if err != nil {
				t.Fatal(err)
			}
			wantCount := 1
			if mode == "missing_plan" {
				wantCount = 0
			}
			if len(receipts) != wantCount {
				t.Fatalf("duplicate canonical execution: receipts=%v", receipts)
			}
			if wantCount == 1 && results[0].TestReceipt != receipts[0] {
				t.Fatalf("wrong receipt reference: %#v", results)
			}
			wire := coderloop.NewObservationResult(1, results).ToJSON()
			var decoded coderloop.ObservationResult
			if err := json.Unmarshal([]byte(wire), &decoded); err != nil || !reflect.DeepEqual(decoded.Results, results) {
				t.Fatalf("observation receipt roundtrip failed: %s %v", wire, err)
			}
			if mode == "passed" {
				next, err := worker.ExecuteObservation(ctx, []ObservationAction{{Action: "shell_command", Target: "go vet ./..."}})
				if err != nil || len(next) != 1 || next[0].TestReceipt == results[0].TestReceipt || next[0].TestStatus != "passed" {
					t.Fatalf("separate batch reused receipt: %#v %v", next, err)
				}
			}
		})
	}
}

func TestObservationDeadlineBoundsAndRejectsLateResult(t *testing.T) {
	worker := newTestWorkerExecutionService(t, config.WorkerConfig{Workspace: t.TempDir(), CommandTimeout: 1})
	ctx := bindOwnedWorkerContext(t, worker, context.Background(), modulecore.NewTaskID())
	caller := &observationAdmissionCaller{}
	worker.SetMCPToolCaller(caller)
	bounded := false
	caller.during = func() {
		deadline, ok := caller.ctx.Deadline()
		bounded = ok
		if ok {
			if time.Until(deadline) > time.Second {
				t.Error("action deadline exceeds Worker timeout")
			}
			<-caller.ctx.Done()
		}
	}
	results, err := worker.ExecuteObservation(ctx, []ObservationAction{{Action: "mcp_tool", Target: "read_file"}, {Action: "mcp_tool", Target: "read_file"}})
	if !bounded || err == nil || caller.calls != 1 || len(results) != 1 || results[0].Status != "error" {
		t.Fatalf("late success accepted: bounded=%v err=%v calls=%d results=%#v", bounded, err, caller.calls, results)
	}
	if ctx.Err() != nil {
		t.Fatal("action timeout canceled parent request")
	}
}

func TestObservationDeadlineDefaultAndParentLimit(t *testing.T) {
	for _, parentLimited := range []bool{false, true} {
		worker := newTestWorkerExecutionService(t, config.WorkerConfig{Workspace: t.TempDir()})
		ctx := bindOwnedWorkerContext(t, worker, context.Background(), modulecore.NewTaskID())
		if parentLimited {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, time.Second)
			defer cancel()
		}
		caller := &observationAdmissionCaller{}
		worker.SetMCPToolCaller(caller)
		bounded := false
		caller.during = func() {
			deadline, ok := caller.ctx.Deadline()
			bounded = ok
			limit := 300 * time.Second
			if parentLimited {
				limit = time.Second
			}
			if ok && (time.Until(deadline) <= 0 || time.Until(deadline) > limit) {
				t.Errorf("invalid action deadline: %s", deadline)
			}
			original, _ := domainexecution.IdentityFromContext(ctx)
			received, _ := domainexecution.IdentityFromContext(caller.ctx)
			if original != received {
				t.Fatal("deadline changed execution identity")
			}
		}
		results, err := worker.ExecuteObservation(ctx, []ObservationAction{{Action: "mcp_tool", Target: "read_file"}})
		if !bounded || err != nil || len(results) != 1 || results[0].Status != "ok" {
			t.Fatalf("deadline absent or request failed: bounded=%v err=%v results=%#v", bounded, err, results)
		}
	}
}

func TestObservationDeadlineCommandOutputBound(t *testing.T) {
	var output workerCommandBuffer
	payload := []byte(strings.Repeat("x", maxWorkerCommandOutputBytes*2))
	n, err := output.Write(payload)
	if err != nil || n != len(payload) || output.buffer.Len() != maxWorkerCommandOutputBytes || !strings.HasSuffix(output.String(), "[output truncated]") {
		t.Fatalf("command output not bounded: written=%d len=%d err=%v", n, output.buffer.Len(), err)
	}
	if _, _, err := runWorkerCommand(nil, t.TempDir(), "must-not-start"); err == nil {
		t.Fatal("nil context accepted")
	}
}

func TestObservationFindWorkspaceRoots(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workspace, "escape")); err != nil {
		t.Fatal(err)
	}
	worker := newTestWorkerExecutionService(t, config.WorkerConfig{Workspace: workspace})
	for _, command := range []string{
		"find '" + outside + "' -maxdepth 0",
		"find . '" + outside + "' -maxdepth 0",
		"find escape -maxdepth 0",
		"find ../001 -maxdepth 0",
	} {
		result := worker.executeSingleObservation(context.Background(), ObservationAction{Action: "shell_command", Target: command})
		if result.Status != "error" || !strings.Contains(result.Output, "workspace") {
			t.Errorf("outside root accepted: %q %#v", command, result)
		}
	}
	for _, command := range []string{"find . -maxdepth 0", "find -maxdepth 0", "find . -name '/not-a-root'", "find . -name '-delete'"} {
		result := worker.executeSingleObservation(context.Background(), ObservationAction{Action: "shell_command", Target: command})
		if result.Status != "ok" {
			t.Errorf("valid find rejected: %q %#v", command, result)
		}
	}
}

func TestObservationReadWorkspaceFiles(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	for _, path := range []string{outside, filepath.Join(workspace, "inside.txt"), filepath.Join(workspace, "patterns.txt")} {
		if err := os.WriteFile(path, []byte("needle\nsecond\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "escape")); err != nil {
		t.Fatal(err)
	}
	worker := newTestWorkerExecutionService(t, config.WorkerConfig{Workspace: workspace})
	for _, command := range []string{"cat '" + outside + "'", "head -n 1 '" + outside + "'", "tail -n 1 '" + outside + "'", "wc -l '" + outside + "'", "grep needle '" + outside + "'", "grep -f '" + outside + "' inside.txt", "cat inside.txt escape", "cat ."} {
		result := worker.executeSingleObservation(context.Background(), ObservationAction{Action: "shell_command", Target: command})
		if result.Status != "error" || !strings.Contains(result.Output, "observation file") {
			t.Errorf("unconfined read: %q %#v", command, result)
		}
	}
	for _, command := range []string{"cat inside.txt", "head -n1 inside.txt", "tail --lines=1 inside.txt", "wc -lw inside.txt", "grep -nF needle inside.txt", "grep -fpatterns.txt inside.txt"} {
		result := worker.executeSingleObservation(context.Background(), ObservationAction{Action: "shell_command", Target: command})
		if result.Status != "ok" || result.Output == "" {
			t.Errorf("valid read: %q %#v", command, result)
		}
	}
}

func TestObservationReadRejectsImplicitAndIndirectInputs(t *testing.T) {
	for _, command := range []string{"cat", "cat -", "head -n 3", "tail -f file", "wc --files0-from=file", "grep -r needle .", "grep -R needle .", "grep needle", "grep -f - file", "grep --future-option needle file", "head -n nope file"} {
		if _, err := observationCommandArgs(command); err == nil {
			t.Errorf("unbounded/unknown read admitted: %s", command)
		}
	}
}

func TestObservationReadOperandRoles(t *testing.T) {
	for _, tc := range []struct {
		command string
		files   []string
	}{
		{"grep -e '/literal/pattern' -f patterns.txt data.txt", []string{"patterns.txt", "data.txt"}},
		{"grep -nFe'-f' -- -file", []string{"-file"}},
		{"grep --regexp=needle --file=patterns.txt data.txt", []string{"patterns.txt", "data.txt"}},
		{"head -qn2 data.txt", []string{"data.txt"}},
		{"tail -n +2 data.txt", []string{"data.txt"}},
		{"cat -- -file", []string{"-file"}},
	} {
		argv, err := observationCommandArgs(tc.command)
		if err != nil {
			t.Errorf("parse %s: %v", tc.command, err)
			continue
		}
		files, err := observationReadFiles(argv)
		if err != nil || !reflect.DeepEqual(files, tc.files) {
			t.Errorf("roles %s: %q %v", tc.command, files, err)
		}
	}
}

func TestObservationGitRepositoryBinding(t *testing.T) {
	for _, mode := range []string{"environment", "nested", "local-worktree"} {
		t.Run(mode, func(t *testing.T) {
			workspace := makeCanonicalImpactWorkspace(t)
			outside := makeCanonicalImpactWorkspace(t)
			if err := os.WriteFile(filepath.Join(workspace, "inside-marker"), []byte("inside"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(outside, "outside-marker"), []byte("outside"), 0600); err != nil {
				t.Fatal(err)
			}
			configured := workspace
			switch mode {
			case "environment":
				t.Setenv("GIT_DIR", filepath.Join(outside, ".git"))
				t.Setenv("GIT_WORK_TREE", outside)
				t.Setenv("GIT_INDEX_FILE", filepath.Join(outside, ".git", "index"))
				t.Setenv("GIT_CONFIG_COUNT", "1")
				t.Setenv("GIT_CONFIG_KEY_0", "core.worktree")
				t.Setenv("GIT_CONFIG_VALUE_0", outside)
			case "nested":
				configured = filepath.Join(workspace, "subdir")
				if err := os.Mkdir(configured, 0700); err != nil {
					t.Fatal(err)
				}
			case "local-worktree":
				cmd := exec.Command("git", "-C", workspace, "config", "core.worktree", outside)
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("fixture: %s %v", output, err)
				}
			}
			worker := newTestWorkerExecutionService(t, config.WorkerConfig{Workspace: configured})
			result := worker.executeSingleObservation(context.Background(), ObservationAction{Action: "shell_command", Target: "git status --porcelain"})
			if mode == "environment" {
				if result.Status != "ok" || !strings.Contains(result.Output, "inside-marker") || strings.Contains(result.Output, "outside-marker") {
					t.Fatalf("Git environment escaped workspace: %#v", result)
				}
			} else if result.Status != "error" || !strings.Contains(result.Output, "workspace") {
				t.Fatalf("Git workspace mismatch admitted: %#v", result)
			}
		})
	}
}

func TestObservationGitDiffOutsideOperands(t *testing.T) {
	workspace := makeCanonicalImpactWorkspace(t)
	outside := t.TempDir()
	for _, name := range []string{"left", "right"} {
		if err := os.WriteFile(filepath.Join(outside, name), []byte("same fixture\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	worker := newTestWorkerExecutionService(t, config.WorkerConfig{Workspace: workspace})
	for _, separator := range []string{"", "-- "} {
		command := "git diff " + separator + "'" + filepath.Join(outside, "left") + "' '" + filepath.Join(outside, "right") + "'"
		result := worker.executeSingleObservation(context.Background(), ObservationAction{Action: "shell_command", Target: command})
		if result.Status != "error" || !strings.Contains(result.Output, "workspace") {
			t.Errorf("outside comparison admitted: %#v", result)
		}
	}
}

func TestObservationGitDiffOperandRoles(t *testing.T) {
	invocation, err := observationGitArgs([]string{"git", "diff", "-U", "2", "HEAD", "--", "-file"})
	want := []observationGitOperand{{value: "HEAD", mayBeRevision: true}, {value: "-file"}}
	if err != nil || !reflect.DeepEqual(invocation.diffOperands, want) {
		t.Fatalf("diff roles: %#v %v", invocation, err)
	}
	for _, required := range []string{"--no-ext-diff", "--no-textconv", "--no-pager", "core.fsmonitor=false"} {
		found := false
		for _, arg := range invocation.argv {
			if arg == required {
				found = true
			}
		}
		if !found {
			t.Errorf("missing execution protection: %s", required)
		}
	}
	workspace := makeCanonicalImpactWorkspace(t)
	for _, args := range [][]string{{"add", "scripts/test-local.ps1"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "fixture"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = workspace
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture: %s %v", out, err)
		}
	}
	worker := newTestWorkerExecutionService(t, config.WorkerConfig{Workspace: workspace})
	for _, command := range []string{"git diff HEAD", "git diff HEAD HEAD", "git diff HEAD:scripts/test-local.ps1 HEAD:scripts/test-local.ps1", "git diff HEAD -- scripts/test-local.ps1"} {
		result := worker.executeSingleObservation(context.Background(), ObservationAction{Action: "shell_command", Target: command})
		if result.Status != "ok" {
			t.Errorf("valid repository diff rejected: %s %#v", command, result)
		}
	}
}
