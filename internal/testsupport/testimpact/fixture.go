// Package testimpact contains the shared test-only owner boundary used by
// Worker integration tests. It deliberately does not import the service
// package, so each test package can expose the same fake executable without an
// import cycle.
package testimpact

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"testing"
	"time"
)

const (
	// HelperModeEnv selects the current test binary's owner helper protocol.
	HelperModeEnv           = "RENCROW_TEST_IMPACT_HELPER_MODE"
	testImpactPlanSchema    = "rencrow.test-impact-plan.v1"
	testImpactReceiptSchema = "rencrow.test-receipt.v1"
)

type helperPlan struct {
	Schema            string `json:"schema"`
	Repo              string `json:"repo"`
	Worktree          bool   `json:"worktree"`
	CanonicalPlanHash string `json:"canonicalPlanHash"`
	SourceHash        string `json:"sourceHash"`
	PlanHash          string `json:"planHash"`
	ExecutionMode     string `json:"executionMode"`
	FullFallback      bool   `json:"fullFallback"`
}

// RunIfRequested runs the test-impact helper protocol when the current test
// binary is used as TestImpactBinary. With no helper mode it returns so the
// normal test suite can continue.
func RunIfRequested() {
	mode := os.Getenv(HelperModeEnv)
	if mode == "" {
		return
	}
	runTestImpactHelper(mode)
}

// InitWorkspace creates an isolated Git repository containing the canonical
// version-2 Test Impact plan and runner. All callers use this one fixture so
// the test wire contract and canonical plan cannot drift between packages.
func InitWorkspace(t testing.TB, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create test-impact workspace: %v", err)
	}
	for _, args := range [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("initialize test-impact workspace: %v, output: %s", err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("/Tmp/\n"), 0o644); err != nil {
		t.Fatalf("isolate canonical test artifacts: %v", err)
	}
	scriptsDir := filepath.Join(dir, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatalf("create canonical test scripts directory: %v", err)
	}
	plan := `{
  "version": 2,
  "steps": [{
    "name": "unit",
    "workingDirectory": ".",
    "filePath": "go",
    "arguments": ["test", "./..."],
    "tier": "fast",
    "impact": {"paths": [], "packages": [], "globalPaths": []},
    "resourceLocks": [],
    "timeoutSeconds": 30
  }],
  "impact": {"globalPaths": [], "ignore": []}
}`
	if err := os.WriteFile(filepath.Join(scriptsDir, "test-local.plan.json"), []byte(plan), 0o644); err != nil {
		t.Fatalf("write canonical test plan: %v", err)
	}
	if err := os.WriteFile(filepath.Join(scriptsDir, "test-local.ps1"), []byte("param()\n"), 0o644); err != nil {
		t.Fatalf("write canonical owner runner: %v", err)
	}
	return dir
}

func runTestImpactHelper(mode string) {
	if mode == "tree_child" {
		for {
			appendTestImpactHelperMarker(os.Getenv("RENCROW_TEST_IMPACT_HELPER_MARKER"), "tree-child\n")
			// Fixed delay is required to keep the owned child observable during
			// the bounded process-tree timeout test.
			time.Sleep(20 * time.Millisecond)
		}
	}
	if mode == "tree_timeout" {
		signal.Ignore(os.Interrupt)
		child := exec.Command(os.Args[0], "test-impact-tree-child")
		child.Env = append(os.Environ(), HelperModeEnv+"=tree_child")
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		for {
			// Fixed delay is required to keep the parent alive until the owner
			// command timeout can exercise process-tree termination.
			time.Sleep(20 * time.Millisecond)
		}
	}

	command := ""
	for _, arg := range os.Args[1:] {
		if arg == "resolve" || arg == "run" {
			command = arg
			break
		}
	}
	if command == "" {
		os.Exit(2)
	}
	repo := testImpactHelperArg("--repo")
	output := testImpactHelperArg("--output")
	if repo == "" || output == "" {
		os.Exit(2)
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		os.Exit(2)
	}

	var payload any
	if command == "resolve" {
		canonicalPlan, err := os.ReadFile(filepath.Join(repo, "scripts", "test-local.plan.json"))
		if err != nil {
			os.Exit(2)
		}
		canonicalHash := fmt.Sprintf("%x", sha256.Sum256(canonicalPlan))
		sourceHash := fmt.Sprintf("%x", sha256.Sum256(append([]byte("source-test-tree:"), canonicalPlan...)))
		planHash := fmt.Sprintf("%x", sha256.Sum256([]byte(canonicalHash)))
		executionMode := "related"
		fullFallback := false
		if mode == "full_fallback" {
			executionMode = "full"
			fullFallback = true
		}
		payload = map[string]any{
			"schema":            testImpactPlanSchema,
			"repo":              repo,
			"worktree":          true,
			"canonicalPlanHash": canonicalHash,
			"sourceHash":        sourceHash,
			"planHash":          planHash,
			"executionMode":     executionMode,
			"fullFallback":      fullFallback,
		}
	} else {
		planPath := testImpactHelperArg("--execution-plan")
		planData, err := os.ReadFile(planPath)
		if err != nil {
			os.Exit(2)
		}
		var plan helperPlan
		if err := json.Unmarshal(planData, &plan); err != nil {
			os.Exit(2)
		}
		sourceHash := plan.SourceHash
		result := "passed"
		stepResult := "passed"
		exitCode := 0
		if mode == "bad_source" {
			sourceHash = fmt.Sprintf("%x", sha256.Sum256([]byte("different-source")))
		}
		if mode == "failed_step" {
			result = "failed"
			stepResult = "failed"
			exitCode = 1
		}
		payload = map[string]any{
			"schema":            testImpactReceiptSchema,
			"repo":              repo,
			"impactPlan":        plan.PlanHash,
			"canonicalPlanHash": plan.CanonicalPlanHash,
			"sourceHash":        sourceHash,
			"result":            result,
			"steps": []map[string]any{{
				"name":   "unit",
				"result": stepResult,
			}},
		}
		if err := writeTestImpactHelperJSON(output, payload); err != nil {
			os.Exit(2)
		}
		os.Exit(exitCode)
	}
	if err := writeTestImpactHelperJSON(output, payload); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func appendTestImpactHelperMarker(path, value string) {
	if path == "" {
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	_, _ = file.WriteString(value)
	_ = file.Close()
}

func testImpactHelperArg(name string) string {
	for i := 1; i+1 < len(os.Args); i++ {
		if os.Args[i] == name {
			return os.Args[i+1]
		}
	}
	return ""
}

func writeTestImpactHelperJSON(path string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
