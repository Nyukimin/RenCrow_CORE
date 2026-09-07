package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/patch"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	defaultTestImpactBinary = "rencrow-test-impact"
	testImpactPlanSchema    = "rencrow.test-impact-plan.v1"
	testImpactReceiptSchema = "rencrow.test-receipt.v1"
	maxTestImpactDiagnostic = 1024
)

type testImpactPlan struct {
	Schema            string `json:"schema"`
	Repo              string `json:"repo"`
	Worktree          bool   `json:"worktree"`
	CanonicalPlanHash string `json:"canonicalPlanHash"`
	SourceHash        string `json:"sourceHash"`
	PlanHash          string `json:"planHash"`
	ExecutionMode     string `json:"executionMode"`
	FullFallback      bool   `json:"fullFallback"`
}

type testImpactReceipt struct {
	Schema            string                  `json:"schema"`
	Repo              string                  `json:"repo"`
	ImpactPlan        string                  `json:"impactPlan"`
	CanonicalPlanHash string                  `json:"canonicalPlanHash"`
	SourceHash        string                  `json:"sourceHash"`
	Result            string                  `json:"result"`
	Steps             []testImpactReceiptStep `json:"steps"`
}

type testImpactReceiptStep struct {
	Name   string `json:"name"`
	Result string `json:"result"`
}

type testImpactScope struct {
	status                  string
	workspace               string
	planPath                string
	runnerPath              string
	beforeCanonicalPlanHash string
	beforeRunnerHash        string
	reason                  string
}

func (w *workerExecutionService) inspectTestImpactScope() (testImpactScope, error) {
	workspace := strings.TrimSpace(w.config.Workspace)
	if workspace == "" {
		workspace = "."
	}
	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return testImpactScope{}, fmt.Errorf("test-impact workspace is invalid: %w", err)
	}
	planPath := filepath.Join(absWorkspace, "scripts", "test-local.plan.json")
	runnerPath := filepath.Join(absWorkspace, "scripts", "test-local.ps1")
	switch repositoryWorkspaceKind(absWorkspace) {
	case "scratch":
		return testImpactScope{status: "not_applicable", workspace: absWorkspace, planPath: planPath, runnerPath: runnerPath}, nil
	case "nested":
		return testImpactScope{
			status:     "blocked",
			workspace:  absWorkspace,
			planPath:   planPath,
			runnerPath: runnerPath,
			reason:     "configured workspace is nested inside a Git repository; configure the repository root",
		}, nil
	}
	if !isRegularFile(planPath) {
		return testImpactScope{
			status:     "blocked",
			workspace:  absWorkspace,
			planPath:   planPath,
			runnerPath: runnerPath,
			reason:     "Git workspace is missing canonical scripts/test-local.plan.json",
		}, nil
	}
	planBytes, err := os.ReadFile(planPath)
	if err != nil {
		return testImpactScope{}, fmt.Errorf("canonical test plan is unreadable: %w", err)
	}
	scope := testImpactScope{
		status:                  "applicable",
		workspace:               absWorkspace,
		planPath:                planPath,
		runnerPath:              runnerPath,
		beforeCanonicalPlanHash: fmt.Sprintf("%x", sha256.Sum256(planBytes)),
	}
	if isRegularFile(runnerPath) {
		runnerBytes, readErr := os.ReadFile(runnerPath)
		if readErr != nil {
			return testImpactScope{}, fmt.Errorf("canonical owner runner is unreadable: %w", readErr)
		}
		scope.beforeRunnerHash = fmt.Sprintf("%x", sha256.Sum256(runnerBytes))
	}
	return scope, nil
}

func (w *workerExecutionService) executeTestImpact(
	ctx context.Context,
	taskID modulecore.TaskID,
	result *patch.PatchExecutionResult,
	scope testImpactScope,
) *patch.PatchExecutionResult {
	status, receiptPath, reason := w.runTestImpact(ctx, taskID, scope)
	result.TestStatus = status
	result.TestReceipt = receiptPath
	if status != "passed" && status != "not_applicable" {
		result.Success = false
		result.WithFailureMetadata(testImpactFailureKind(status), reason, false)
	}
	return result
}

// finalizeTestImpactResult appends verification evidence after the existing
// patch finalization has established the command summary.
func (w *workerExecutionService) finalizeTestImpactResult(result *patch.PatchExecutionResult) *patch.PatchExecutionResult {
	if result == nil || strings.TrimSpace(result.TestStatus) == "" {
		return result
	}
	detail := "test-impact=" + result.TestStatus
	if strings.TrimSpace(result.TestReceipt) != "" {
		detail += " receipt=" + result.TestReceipt
	}
	if strings.TrimSpace(result.Summary) == "" {
		result.Summary = detail
	} else {
		result.Summary = strings.TrimSpace(result.Summary) + "; " + detail
	}
	return result
}

func testImpactFailureKind(status string) string {
	if status == "blocked" {
		return "test_impact_blocked"
	}
	return "test_impact_failed"
}

func (w *workerExecutionService) runTestImpact(ctx context.Context, taskID modulecore.TaskID, scope testImpactScope) (string, string, string) {
	if scope.status == "not_applicable" {
		return "not_applicable", "", ""
	}
	if scope.status != "applicable" {
		return "blocked", "", scope.reason
	}
	if repositoryWorkspaceKind(scope.workspace) != "repository" || !isRegularFile(scope.planPath) {
		return "blocked", "", "Git workspace or canonical test plan disappeared after patch execution"
	}
	runnerChanged := false
	if scope.beforeRunnerHash != "" {
		if !isRegularFile(scope.runnerPath) {
			return "blocked", "", "canonical owner runner disappeared after patch execution"
		}
		runnerBytes, err := os.ReadFile(scope.runnerPath)
		if err != nil {
			return "blocked", "", fmt.Sprintf("canonical owner runner changed or became unreadable: %v", err)
		}
		currentRunnerHash := fmt.Sprintf("%x", sha256.Sum256(runnerBytes))
		if currentRunnerHash != scope.beforeRunnerHash {
			// The owner resolver must widen a runner change to Full, just as it
			// does for a canonical plan change.
			runnerChanged = true
		}
	}
	if strings.TrimSpace(scope.beforeCanonicalPlanHash) == "" {
		return "blocked", "", "canonical test plan was not bound before patch execution"
	}
	runID, err := safeTestImpactRunID(taskID)
	if err != nil {
		return "blocked", "", err.Error()
	}
	tmpRoot, err := ensureRepoLocalTestTmp(scope.workspace)
	if err != nil {
		return "blocked", "", err.Error()
	}
	planBase := filepath.Join(tmpRoot, "test-impact")
	receiptBase := filepath.Join(tmpRoot, "test-results")
	if err := ensureNoSymlinkPath(scope.workspace, planBase); err != nil {
		return "blocked", "", err.Error()
	}
	if err := ensureNoSymlinkPath(scope.workspace, receiptBase); err != nil {
		return "blocked", "", err.Error()
	}
	if err := os.MkdirAll(planBase, 0o755); err != nil {
		return "blocked", "", fmt.Sprintf("test-impact plan directory: %v", err)
	}
	if err := ensureNoSymlinkPath(scope.workspace, planBase); err != nil {
		return "blocked", "", err.Error()
	}
	if err := os.MkdirAll(receiptBase, 0o755); err != nil {
		return "blocked", "", fmt.Sprintf("test-impact receipt directory: %v", err)
	}
	if err := ensureNoSymlinkPath(scope.workspace, receiptBase); err != nil {
		return "blocked", "", err.Error()
	}
	planDir, err := os.MkdirTemp(planBase, runID+"-")
	if err != nil {
		return "blocked", "", fmt.Sprintf("test-impact plan run directory: %v", err)
	}
	receiptDir, err := os.MkdirTemp(receiptBase, runID+"-")
	if err != nil {
		return "blocked", "", fmt.Sprintf("test-impact receipt run directory: %v", err)
	}
	if err := ensureNoSymlinkPath(scope.workspace, planDir); err != nil {
		return "blocked", "", err.Error()
	}
	if err := ensureNoSymlinkPath(scope.workspace, receiptDir); err != nil {
		return "blocked", "", err.Error()
	}

	planOutput := filepath.Join(planDir, "plan.json")
	receiptOutput := filepath.Join(receiptDir, "receipt.json")
	binary := strings.TrimSpace(w.config.TestImpactBinary)
	if binary == "" {
		binary = defaultTestImpactBinary
	}
	testCtx, cancel := context.WithTimeout(ctx, testImpactTimeout(w.config.TestImpactTimeoutSeconds))
	defer cancel()

	_, stderr, err := runWorkerCommand(testCtx, scope.workspace, binary,
		"resolve", "--repo", scope.workspace, "--base", "HEAD", "--worktree", "--output", planOutput)
	if err != nil {
		return "blocked", "", testImpactCommandFailure("resolve", err, stderr)
	}
	plan, err := readTestImpactPlan(planOutput, scope.workspace, scope.planPath)
	if err != nil {
		return "blocked", "", err.Error()
	}
	canonicalChanged := plan.CanonicalPlanHash != scope.beforeCanonicalPlanHash
	if (canonicalChanged || runnerChanged) &&
		(plan.ExecutionMode != "full" || !plan.FullFallback) {
		return "blocked", "", "canonical plan or owner runner changed after patch without an owner full fallback"
	}

	_, runStderr, runErr := runWorkerCommand(testCtx, scope.workspace, binary,
		"run", "--repo", scope.workspace, "--execution-plan", planOutput, "--output", receiptOutput)
	receipt, receiptReadErr := readTestImpactReceipt(receiptOutput)
	if receiptReadErr != nil {
		if runErr != nil {
			return "blocked", existingReceiptPath(receiptOutput), testImpactCommandFailure("run", runErr, runStderr) + ": " + receiptReadErr.Error()
		}
		return "blocked", existingReceiptPath(receiptOutput), receiptReadErr.Error()
	}
	status, validationErr := validateTestImpactReceipt(receipt, plan, scope.workspace)
	if runErr != nil {
		if validationErr == nil && status == "passed" {
			status = "blocked"
			validationErr = fmt.Errorf("run command failed after a passed receipt: %s", testImpactCommandFailure("run", runErr, runStderr))
		} else if validationErr == nil {
			validationErr = fmt.Errorf("%s", testImpactCommandFailure("run", runErr, runStderr))
		}
	}
	if validationErr != nil {
		return status, receiptOutput, validationErr.Error()
	}
	return "passed", receiptOutput, ""
}

func isRepositoryWorkspace(workspace string) bool {
	return isRegularOrDirectory(filepath.Join(workspace, ".git"))
}

func repositoryWorkspaceKind(workspace string) string {
	if isRepositoryWorkspace(workspace) {
		return "repository"
	}
	for parent := filepath.Dir(workspace); ; {
		if isRepositoryWorkspace(parent) {
			return "nested"
		}
		next := filepath.Dir(parent)
		if next == parent {
			break
		}
		parent = next
	}
	return "scratch"
}

func isRegularOrDirectory(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func ensureRepoLocalTestTmp(workspace string) (string, error) {
	tmpRoot := filepath.Join(workspace, "Tmp")
	if err := ensureNoSymlinkPath(workspace, tmpRoot); err != nil {
		return "", err
	}
	if err := os.MkdirAll(tmpRoot, 0o755); err != nil {
		return "", fmt.Errorf("repo-local test Tmp: %w", err)
	}
	if err := ensureNoSymlinkPath(workspace, tmpRoot); err != nil {
		return "", err
	}
	return tmpRoot, nil
}

func ensureNoSymlinkPath(root, target string) error {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("test-impact artifact path escapes workspace Tmp: %s", target)
	}
	current := root
	if info, statErr := os.Lstat(current); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("test-impact workspace path is a symlink: %s", current)
	}
	if rel == "." {
		return nil
	}
	for _, component := range strings.Split(rel, string(os.PathSeparator)) {
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if os.IsNotExist(statErr) {
			break
		}
		if statErr != nil {
			return fmt.Errorf("test-impact artifact path cannot be inspected: %w", statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("test-impact artifact path contains symlink: %s", current)
		}
	}
	return nil
}

func safeTestImpactRunID(taskID modulecore.TaskID) (string, error) {
	runID := strings.TrimSpace(taskID.String())
	if runID == "" || runID == "." || runID == ".." || filepath.Base(runID) != runID || strings.ContainsAny(runID, `/\\`) {
		return "", fmt.Errorf("test-impact task ID cannot name a repository path: %q", runID)
	}
	return runID, nil
}

func testImpactTimeout(configuredSeconds int) time.Duration {
	if configuredSeconds <= 0 {
		configuredSeconds = config.DefaultTestImpactTimeoutSeconds
	}
	return time.Duration(configuredSeconds) * time.Second
}

func readTestImpactPlan(path, workspace, canonicalPath string) (testImpactPlan, error) {
	var plan testImpactPlan
	if err := readTestImpactJSON(path, &plan); err != nil {
		return plan, fmt.Errorf("test-impact plan unreadable: %w", err)
	}
	if plan.Schema != testImpactPlanSchema {
		return plan, fmt.Errorf("test-impact plan schema %q is not %q", plan.Schema, testImpactPlanSchema)
	}
	if !sameWorkspace(plan.Repo, workspace) {
		return plan, fmt.Errorf("test-impact plan repo %q does not bind workspace %q", plan.Repo, workspace)
	}
	if !plan.Worktree {
		return plan, fmt.Errorf("test-impact plan is not a worktree plan")
	}
	if strings.TrimSpace(plan.CanonicalPlanHash) == "" || strings.TrimSpace(plan.SourceHash) == "" || strings.TrimSpace(plan.PlanHash) == "" {
		return plan, fmt.Errorf("test-impact plan is missing canonicalPlanHash, sourceHash, or planHash")
	}
	for name, value := range map[string]string{
		"canonicalPlanHash": plan.CanonicalPlanHash,
		"sourceHash":        plan.SourceHash,
		"planHash":          plan.PlanHash,
	} {
		if !validTestImpactHash(value) {
			return plan, fmt.Errorf("test-impact plan %s is not a lowercase SHA-256 digest", name)
		}
	}
	canonicalBytes, err := os.ReadFile(canonicalPath)
	if err != nil {
		return plan, fmt.Errorf("canonical test plan changed or disappeared: %w", err)
	}
	canonicalHash := fmt.Sprintf("%x", sha256.Sum256(canonicalBytes))
	if plan.CanonicalPlanHash != canonicalHash {
		return plan, fmt.Errorf("test-impact plan canonicalPlanHash does not bind current canonical plan")
	}
	return plan, nil
}

func validTestImpactHash(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func readTestImpactReceipt(path string) (testImpactReceipt, error) {
	var receipt testImpactReceipt
	if err := readTestImpactJSON(path, &receipt); err != nil {
		return receipt, fmt.Errorf("test-impact receipt unreadable: %w", err)
	}
	return receipt, nil
}

func readTestImpactJSON(path string, destination any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return fmt.Errorf("empty JSON artifact")
	}
	return json.Unmarshal(data, destination)
}

func validateTestImpactReceipt(receipt testImpactReceipt, plan testImpactPlan, workspace string) (string, error) {
	status := strings.ToLower(strings.TrimSpace(receipt.Result))
	if receipt.Schema != testImpactReceiptSchema {
		return "blocked", fmt.Errorf("test-impact receipt schema %q is not %q", receipt.Schema, testImpactReceiptSchema)
	}
	if !sameWorkspace(receipt.Repo, workspace) {
		return "blocked", fmt.Errorf("test-impact receipt repo %q does not bind workspace %q", receipt.Repo, workspace)
	}
	if receipt.ImpactPlan != plan.PlanHash {
		return "blocked", fmt.Errorf("test-impact receipt impactPlan %q does not bind planHash %q", receipt.ImpactPlan, plan.PlanHash)
	}
	if receipt.CanonicalPlanHash != plan.CanonicalPlanHash {
		return "blocked", fmt.Errorf("test-impact receipt canonicalPlanHash does not bind the resolved plan")
	}
	if receipt.SourceHash != plan.SourceHash {
		return "blocked", fmt.Errorf("test-impact receipt sourceHash does not bind the resolved source")
	}
	if status != "passed" && status != "failed" && status != "blocked" {
		return "blocked", fmt.Errorf("test-impact receipt result %q is not passed/failed/blocked", receipt.Result)
	}
	if status == "failed" || status == "blocked" {
		return status, fmt.Errorf("test-impact receipt result is %q", receipt.Result)
	}
	for _, step := range receipt.Steps {
		stepStatus := strings.ToLower(strings.TrimSpace(step.Result))
		if stepStatus == "failed" || stepStatus == "blocked" {
			return stepStatus, fmt.Errorf("test-impact step %q result is %q", step.Name, step.Result)
		}
		if stepStatus == "" {
			return "blocked", fmt.Errorf("test-impact step %q has no result", step.Name)
		}
	}
	return "passed", nil
}

func sameWorkspace(received, expected string) bool {
	received = strings.TrimSpace(received)
	if received == "" {
		return false
	}
	candidates := []string{received}
	if !filepath.IsAbs(received) {
		candidates = append(candidates, filepath.Join(expected, received))
	}
	for _, candidate := range candidates {
		receivedAbs, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		if runtime.GOOS == "windows" {
			if strings.EqualFold(filepath.Clean(receivedAbs), filepath.Clean(expected)) {
				return true
			}
			continue
		}
		if filepath.Clean(receivedAbs) == filepath.Clean(expected) {
			return true
		}
	}
	return false
}

func existingReceiptPath(path string) string {
	if _, err := os.Stat(path); err == nil {
		return path
	}
	return ""
}

func testImpactCommandFailure(stage string, err error, stderr string) string {
	diagnostic := strings.TrimSpace(stderr)
	if diagnostic == "" {
		diagnostic = strings.TrimSpace(err.Error())
	}
	diagnostic = boundedTestImpactDiagnostic(diagnostic)
	return fmt.Sprintf("test-impact %s failed: %s", stage, diagnostic)
}

func boundedTestImpactDiagnostic(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= maxTestImpactDiagnostic {
		return value
	}
	return value[:maxTestImpactDiagnostic] + "...[truncated]"
}
