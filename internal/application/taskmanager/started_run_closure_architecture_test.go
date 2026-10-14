package taskmanager

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Failure knowledge: docs/調査/*Run開始後の早期失敗でRunを閉じない_FailureKnowledge.md.
//
// A Run that has been issued holds an execution slot until it is closed. The
// Memory Promotion runner issued a Run with CreateAndStartRun, then returned
// without closing it when binding the identity or creating the Action failed.
// The RESEARCH slot (LongResearchTasks=1) stayed held and every later Memory
// Promotion was deferred until the next restart. Seven other functions had the
// same defect; all of them now close the Run they start through
// Manager.CloseStartedRun, the one rule for it (detached from the caller,
// 60 seconds per write, cancelled or failed by how the work ended, a failed
// write returned with its cause).
//
// Whether every failure path after a start closes the Run cannot be decided by
// a syntactic test: closing goes through helpers, deferred functions, and
// callers. This test therefore enforces something weaker and checkable. Every
// non-test function that starts a Run (CreateAndStartRun, StartRunWithReason,
// StartRunFromCheckpoint) is listed below with a reviewed classification, so a
// new Run-starting path cannot be added without a review of how its failure
// paths close the Run, and a known gap stays visible until it is fixed. A stale
// entry fails the test. A function classified as closing through the shared API
// must live in a file that calls CloseStartedRun, so the entry cannot claim the
// shared rule while the file reimplements it. The behavioral guarantee is held
// by each owner's own tests, which run the scenarios of
// taskmanagertest.VerifyStartedRunClosure (for Memory Promotion:
// memory_promotion_action_runner_test.go).

// startedRunClosure is the reviewed state of one Run-starting function.
type startedRunClosure string

const (
	// Every failure path after the start closes the Run through
	// Manager.CloseStartedRun. The file of the function must call it.
	startedRunClosesViaSharedAPI startedRunClosure = "closes_via_shared_api"
	// Every failure path after the start closes the Run (or the Task) by the
	// function's own means, or the Task owner re-issues the Run on its own. The
	// reason says how and what was not checked.
	startedRunCloses startedRunClosure = "closes"
	// The function hands the Run to its caller, which closes it. The reason
	// names the closer.
	startedRunClosedByCaller startedRunClosure = "closed_by_caller"
	// Known defect: at least one failure path after the start returns without
	// closing the Run. Each entry is a fix that has not been made; moving it to
	// "closes" is the fix.
	startedRunKnownGap startedRunClosure = "known_gap"
)

type startedRunEntry struct {
	closure startedRunClosure
	reason  string
}

// runStartingMethods maps a Run-starting method name to its argument count, so
// an unrelated method of the same name is not mistaken for one.
var runStartingMethods = map[string]int{
	"CreateAndStartRun":      3,
	"StartRunWithReason":     3,
	"StartRunFromCheckpoint": 6,
}

// startedRunLedger key = "repo-relative path:function".
var startedRunLedger = map[string]startedRunEntry{
	// Closed through Manager.CloseStartedRun, with the Attempt and Action closed
	// first through actionmanager.Manager.CloseAttempt where the function creates
	// an Action. Each has the scenarios of taskmanagertest.VerifyStartedRunClosure
	// (the DCI search has its own, in dci_handler_closure_test.go).
	"cmd/rencrow/memory_promotion_action_runner.go:RunOne":                    {startedRunClosesViaSharedAPI, "RESEARCH; fixed first, 4a900edc; TestActionMemoryPromotionRunner*"},
	"cmd/rencrow/playback_action_receipt.go:Record":                           {startedRunClosesViaSharedAPI, "CHAT; TestPlaybackActionRecorderClosesTheRunItStarted"},
	"cmd/rencrow/tts_action_bridge.go:executionContext":                       {startedRunClosesViaSharedAPI, "CHAT; closes on WithIdentity failure; StartSession closes on CreateAction and binding failures; TestActionTTSBridgeClosesTheRunItStarted"},
	"internal/application/backlog/action_context.go:beginAction":              {startedRunClosesViaSharedAPI, "OPERATIONS, the single operations slot; TestBeginActionClosesTheRunItStarted"},
	"internal/application/sandbox/promotion_diff_apply.go:executePatchAction": {startedRunClosesViaSharedAPI, "CODE; TestPromotionDiffApplierClosesTheRunItStarted"},
	"internal/infrastructure/llm/middleware/action.go:begin":                  {startedRunClosesViaSharedAPI, "GENERAL; TestActionProviderClosesTheRunItStarted"},
	"internal/infrastructure/vision/action_analyzer.go:executionContext":      {startedRunClosesViaSharedAPI, "CHAT; Analyze closes on CreateAction and binding failures; TestActionAnalyzerClosesTheRunItStarted"},
	"internal/infrastructure/stt/action_provider.go:executionContext":         {startedRunClosesViaSharedAPI, "CHAT; Transcribe closes on CreateAction and binding failures; TestActionProviderClosesTheRunItStarted"},
	"internal/infrastructure/stt/utterance_recorder.go:Record":                {startedRunClosesViaSharedAPI, "CHAT; the post-hoc record of one relayed STT utterance closes on CreateAction failure and on every outcome; TestUtteranceRecorderClosesTheRunItStarted"},
	"internal/adapter/viewer/dci_handler.go:ServeHTTP":                        {startedRunClosesViaSharedAPI, "RESEARCH, the slot Memory Promotion holds; every path closes through closeDCIRun with the 60s budget (it was 10s, below the observed Task store latency); TestDCISearchHandler*"},

	// Closed by the function's own means: classified by reading the code, with no
	// behavioral test of the early-failure paths. They use their own bounded
	// finalization budgets, not the shared API.
	"internal/application/heartbeat/worker_lifecycle.go:beginWorker":          {startedRunCloses, "its own failures go through failBeforeWorker -> finishWorker; after it returns, its callers call finish (not reviewed here)"},
	"cmd/rencrow/runtime_agent_ops_legacy.go:admitLegacyOPS":                  {startedRunClosedByCaller, "the handler closes through finalizeLegacyOPS, with a deferred close for panics"},
	"cmd/rencrow/runtime_agent_ops_dci_identity.go:admitAgentOpsDCIExecution": {startedRunCloses, "closes the Run when WithIdentity fails; the caller closes after execution"},
	"internal/application/superagent/run_queue_scheduler.go:startRun":         {startedRunClosedByCaller, "RunOnce interrupts the issued Run when the lease attach fails; a later failure (the claimed trace write) leaves it until the lease expires and the Run is re-issued, so the hold is bounded by the lease"},

	// Known gaps. Neither is reachable with the real Task owner, which validates
	// the Run it returns before it returns it: they act only when the owner's
	// answer is inconsistent. They are left open on purpose, not forgotten.
	// Closing an answer that contradicts itself would write to a Run whose
	// identity cannot be trusted (another Task's Run, or no valid ID at all), a
	// worse failure than the held slot; and the port these functions use
	// (idlechatRunIssuer) has no operation to close a Run. Re-evaluate when the
	// owner's contract can no longer return a contradicting Run (a type or
	// validation that makes it unrepresentable), or when the port gains a close.
	"internal/application/idlechat/run_identity.go:issueIdleChatRun":                      {startedRunKnownGap, "existing-Task path only; a new Task is cancelled. The owner-returned Run is not closed when its identity is invalid or belongs to another Task"},
	"internal/application/idlechat/generation_run_checkpoint.go:startGenerationSuccessor": {startedRunKnownGap, "closes the successor only when the request was cancelled after the owner committed; an owner-returned Run that contradicts the persisted intent is returned unclosed"},
}

func TestRunStartingFunctionsAreReviewedForClosure(t *testing.T) {
	root := repositoryRoot(t)
	observed := map[string]struct{}{}
	callsSharedClose := map[string]bool{}
	var unreviewed []string

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "vendor", "node_modules", "tmp", "Tmp", "workspace", "docs", "logs", "data":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		// The Task owner defines these methods and its test helper uses one to
		// build a saturated store; neither is a caller that must close a Run.
		if strings.HasPrefix(relative, "internal/application/taskmanager/") {
			return nil
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		callsSharedClose[relative] = callsMethod(parsed, "CloseStartedRun")
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil || !startsRun(function.Body) {
				continue
			}
			key := relative + ":" + function.Name.Name
			observed[key] = struct{}{}
			if _, ok := startedRunLedger[key]; !ok {
				unreviewed = append(unreviewed, key)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	sort.Strings(unreviewed)
	if len(unreviewed) > 0 {
		t.Fatalf("these functions start a Run that holds an execution slot until it is closed; "+
			"make every failure path after the start close the Run (Task owner: Manager.CloseStartedRun), "+
			"add a test that fails when it does not, then add a ledger entry:\n  %s", strings.Join(unreviewed, "\n  "))
	}
	var stale []string
	for key := range startedRunLedger {
		if _, ok := observed[key]; !ok {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Fatalf("ledger entries no longer match any Run-starting function; remove them or update the name:\n  %s", strings.Join(stale, "\n  "))
	}
	for key, entry := range startedRunLedger {
		if strings.TrimSpace(entry.reason) == "" {
			t.Errorf("%s: a ledger entry needs a reason", key)
		}
		switch entry.closure {
		case startedRunCloses, startedRunClosedByCaller, startedRunKnownGap:
		case startedRunClosesViaSharedAPI:
			file := key[:strings.Index(key, ":")]
			if !callsSharedClose[file] {
				t.Errorf("%s: classified as closing through the shared API, but %s never calls CloseStartedRun", key, file)
			}
		default:
			t.Errorf("%s: unknown closure classification %q", key, entry.closure)
		}
	}
}

// callsMethod reports whether the file calls a method with the given name.
func callsMethod(file *ast.File, name string) bool {
	found := false
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return !found
		}
		if selector, ok := call.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == name {
			found = true
		}
		return !found
	})
	return found
}

// startsRun reports whether the body calls one of the Run-starting methods.
func startsRun(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return !found
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return !found
		}
		if want, isStarter := runStartingMethods[selector.Sel.Name]; isStarter && len(call.Args) == want {
			found = true
		}
		return !found
	})
	return found
}
