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
// Promotion was deferred until the next restart.
//
// Whether every failure path after a start closes the Run cannot be decided by
// a syntactic test: closing goes through helpers, deferred functions, and
// callers. This test therefore enforces something weaker and checkable. Every
// non-test function that starts a Run (CreateAndStartRun, StartRunWithReason,
// StartRunFromCheckpoint) is listed below with a reviewed classification, so a
// new Run-starting path cannot be added without a review of how its failure
// paths close the Run, and a known gap stays visible until it is fixed. A stale
// entry fails the test. The behavioral guarantee is held by each owner's own
// tests (for Memory Promotion: memory_promotion_action_runner_test.go).

// startedRunClosure is the reviewed state of one Run-starting function.
type startedRunClosure string

const (
	// Every failure path after the start closes the Run through
	// Manager.CloseStartedRun. The file of the function must call it.
	startedRunClosesViaSharedAPI startedRunClosure = "closes_via_shared_api"
	// Every failure path after the start closes the Run (or the Task) by the
	// function's own means, or the Task owner re-issues the Run on its own.
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
	// Memory Promotion: fixed together with this test, and moved onto the shared
	// API. The behavioral tests are the TestActionMemoryPromotionRunner* group in
	// cmd/rencrow.
	"cmd/rencrow/memory_promotion_action_runner.go:RunOne": {startedRunClosesViaSharedAPI, "AbortStartedRun before the Action exists; CloseAttempt then CloseStartedRun after"},

	// Closed: classified by reading the code; only Memory Promotion has a
	// behavioral test. The reason says what was and was not checked.
	"internal/application/heartbeat/worker_lifecycle.go:beginWorker":          {startedRunCloses, "its own failures go through failBeforeWorker -> finishWorker; after it returns, its callers call finish (not reviewed here)"},
	"cmd/rencrow/runtime_agent_ops_legacy.go:admitLegacyOPS":                  {startedRunClosedByCaller, "the handler closes through finalizeLegacyOPS, with a deferred close for panics"},
	"cmd/rencrow/runtime_agent_ops_dci_identity.go:admitAgentOpsDCIExecution": {startedRunCloses, "closes the Run when WithIdentity fails; the caller closes after execution"},
	"internal/adapter/viewer/dci_handler.go:ServeHTTP":                        {startedRunCloses, "every path calls completeDCIRun; its 10s budget is below the observed Task store latency, see the failure knowledge"},
	"internal/application/superagent/run_queue_scheduler.go:startRun":         {startedRunClosedByCaller, "RunOnce interrupts the issued Run when the lease attach fails; a later failure (the claimed trace write) leaves it until the lease expires and the Run is re-issued, so the hold is bounded by the lease"},

	// Known gaps: same defect as Memory Promotion. CreateAndStartRun is followed
	// by WithIdentity, CreateAction, or the receipt-owner binding, and a failure
	// there returns without closing the Run. Fix with a test per entry, then
	// change the entry to "closes".
	"cmd/rencrow/playback_action_receipt.go:Record":                           {startedRunKnownGap, "CHAT; CreateAction failure leaves the Run open"},
	"cmd/rencrow/tts_action_bridge.go:executionContext":                       {startedRunKnownGap, "CHAT; StartSession: WithIdentity (here), CreateAction, and binding failures leave the Run open"},
	"internal/application/backlog/action_context.go:beginAction":              {startedRunKnownGap, "OPERATIONS, the single operations slot; WithIdentity and CreateAction failures"},
	"internal/application/sandbox/promotion_diff_apply.go:executePatchAction": {startedRunKnownGap, "CODE; WithIdentity and CreateAction failures"},
	"internal/infrastructure/llm/middleware/action.go:begin":                  {startedRunKnownGap, "GENERAL; WithIdentity, CreateAction, and binding failures"},
	"internal/infrastructure/vision/action_analyzer.go:executionContext":      {startedRunKnownGap, "CHAT; Analyze: WithIdentity (here), CreateAction, and binding failures leave the Run open"},
	"internal/infrastructure/stt/action_provider.go:executionContext":         {startedRunKnownGap, "CHAT; Transcribe: WithIdentity (here), CreateAction, and binding failures leave the Run open"},
	// Not reachable with the real Task owner, which validates the Run it returns,
	// but the returns exist: an invalid or mismatched Run from the owner is
	// returned without closing it.
	"internal/application/idlechat/run_identity.go:issueIdleChatRun":                      {startedRunKnownGap, "existing-Task path only; a new Task is cancelled. The owner-returned Run is not closed when its identity is invalid"},
	"internal/application/idlechat/generation_run_checkpoint.go:startGenerationSuccessor": {startedRunKnownGap, "closes the successor only when the request was cancelled after the owner committed; owner-returned Run mismatches are returned unclosed"},
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
