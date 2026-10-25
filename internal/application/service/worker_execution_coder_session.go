package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/patch"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/proposal"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// CoderTaskExecutionSession binds one CoderLoop run to the Task owner, its
// canonical module root, and evidence gathered during that run.
type CoderTaskExecutionSession interface {
	RecordLoopFailure(ctx context.Context, reason string) error
	ExecuteObservation(ctx context.Context, actions []ObservationAction) ([]ObservationActionResult, error)
	ExecuteProposal(ctx context.Context, p *proposal.Proposal) (*patch.PatchExecutionResult, error)
	CompletionEvidence(ctx context.Context) (CoderTaskCompletionEvidence, error)
}

// CoderTaskExecutionSessionProvider is implemented by the CORE Worker owner.
type CoderTaskExecutionSessionProvider interface {
	BeginCoderTaskExecution(ctx context.Context, taskID modulecore.TaskID) (CoderTaskExecutionSession, error)
}

// CoderTaskCompletionEvidence contains only Worker-owned facts. It never
// copies changed-file or test claims from a CoderMessage.
type CoderTaskCompletionEvidence struct {
	TaskID                modulecore.TaskID
	ChangedFiles          []string
	ReadOnly              bool
	SuccessfulObservation bool
	SuccessfulPatch       bool
	TestStatus            string
	TestReceipt           string
	Ready                 bool
	FailureReason         string
}

type workerCoderTaskExecutionSession struct {
	mu sync.Mutex

	owner       *taskmanager.Manager
	worker      *workerExecutionService
	taskID      modulecore.TaskID
	identity    domainexecution.Identity
	scope       domaintool.ToolExecutionScope
	moduleRoot  string
	scratchRoot bool
	readOnly    bool
	baseline    map[string]coderWorkspaceEntry

	successfulObservation     bool
	observationFailures       map[string]string
	patchFailure              string
	verificationFailure       string
	loopFailure               string
	successfulPatch           bool
	testStatus                string
	testReceipt               string
	verificationSnapshot      map[string]coderWorkspaceEntry
	verificationReceiptDigest string
}

type coderWorkspaceSnapshotCapture func(context.Context, string, bool, int) (map[string]coderWorkspaceEntry, error)

func (w *workerExecutionService) BeginCoderTaskExecution(
	ctx context.Context,
	taskID modulecore.TaskID,
) (CoderTaskExecutionSession, error) {
	if w == nil || w.owner == nil {
		return nil, errors.New("coder task execution owner is unavailable")
	}
	identity, err := w.owner.ValidateExecutionContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("coder task execution admission denied: %w", err)
	}
	if identity.TaskID != taskID {
		return nil, fmt.Errorf("coder task execution identity mismatch: context=%s argument=%s", identity.TaskID, taskID)
	}
	task, err := w.owner.Get(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("load coder task %s: %w", taskID, err)
	}
	if task.TaskID != taskID {
		return nil, fmt.Errorf("coder task owner returned identity %s for %s", task.TaskID, taskID)
	}
	moduleRoot, scratchRoot, err := canonicalCoderTaskRoot(task.ModuleRoot, ctx)
	if err != nil {
		return nil, fmt.Errorf("coder task %s module root: %w", taskID, err)
	}
	scope, found := domaintool.ToolExecutionScopeFromContext(ctx)
	if !found {
		return nil, errors.New("coder task execution scope is required")
	}
	scope.AllowedDataScopes = append([]string(nil), scope.AllowedDataScopes...)
	clone := *w
	clone.config = w.config
	clone.config.Workspace = moduleRoot
	baseline, err := captureCoderWorkspaceSnapshot(ctx, moduleRoot, scratchRoot, clone.config.CommandTimeout)
	if err != nil {
		return nil, fmt.Errorf("capture coder task baseline: %w", err)
	}
	if _, err := w.owner.ValidateExecutionContext(ctx); err != nil {
		return nil, fmt.Errorf("coder task execution admission expired before session start: %w", err)
	}
	return &workerCoderTaskExecutionSession{
		owner:               w.owner,
		worker:              &clone,
		taskID:              taskID,
		identity:            identity,
		scope:               scope,
		moduleRoot:          moduleRoot,
		scratchRoot:         scratchRoot,
		readOnly:            task.ReadOnly,
		baseline:            baseline,
		observationFailures: make(map[string]string),
	}, nil
}

func (s *workerCoderTaskExecutionSession) validate(ctx context.Context) error {
	if s == nil || s.owner == nil || s.worker == nil {
		return errors.New("coder task execution session is unavailable")
	}
	identity, err := s.owner.ValidateExecutionContext(ctx)
	if err != nil {
		return fmt.Errorf("coder task execution admission denied: %w", err)
	}
	if identity != s.identity || identity.TaskID != s.taskID {
		return fmt.Errorf("coder task execution identity changed during session")
	}
	scope, found := domaintool.ToolExecutionScopeFromContext(ctx)
	if !found || !reflect.DeepEqual(scope, s.scope) {
		return errors.New("coder task execution scope changed during session")
	}
	task, err := s.owner.Get(ctx, s.taskID)
	if err != nil {
		return fmt.Errorf("reload coder task %s: %w", s.taskID, err)
	}
	if task.TaskID != s.taskID || task.ReadOnly != s.readOnly {
		return errors.New("coder task identity or read-only state changed during session")
	}
	if strings.TrimSpace(task.ModuleRoot) == "" {
		return errors.New("authoritative Task.ModuleRoot disappeared during session")
	}
	currentRoot, err := filepath.EvalSymlinks(task.ModuleRoot)
	if err != nil || filepath.Clean(currentRoot) != s.moduleRoot {
		return errors.New("authoritative Task.ModuleRoot changed during session")
	}
	return nil
}

func (s *workerCoderTaskExecutionSession) RecordLoopFailure(ctx context.Context, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validate(ctx); err != nil {
		return err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errors.New("CoderLoop failure reason is required")
	}
	s.loopFailure = reason
	return nil
}

func (s *workerCoderTaskExecutionSession) ExecuteObservation(
	ctx context.Context,
	actions []ObservationAction,
) ([]ObservationActionResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validate(ctx); err != nil {
		return nil, err
	}
	if len(actions) == 0 {
		return nil, errors.New("coder observation requires at least one action")
	}
	for _, action := range actions {
		if action.Action == "mcp_tool" {
			return nil, errors.New("mcp_tool is unavailable in a task-bound CoderLoop session")
		}
		if s.readOnly && isCanonicalTestObservation(action) {
			return nil, errors.New("read-only task cannot execute test, build, or vet observations")
		}
	}
	results, callErr := s.worker.ExecuteObservation(ctx, actions)
	if err := s.validate(ctx); err != nil {
		return results, fmt.Errorf("coder observation result rejected after owner change: %w", err)
	}
	if len(results) == 0 {
		callErr = errors.Join(callErr, errors.New("worker returned no observation results"))
	}
	for index, result := range results {
		canonicalTest := index < len(actions) && isCanonicalTestObservation(actions[index])
		key := coderObservationKey(result.Action, result.Target)
		if canonicalTest {
			s.recordVerificationResult(ctx, result)
			if s.verificationFailure == "" {
				delete(s.observationFailures, key)
			} else {
				results[index].Status = "error"
				results[index].Output = s.verificationFailure
			}
			continue
		}
		if result.Status != "ok" {
			reason := strings.TrimSpace(result.Output)
			if reason == "" {
				reason = "worker observation failed"
			}
			s.observationFailures[key] = reason
		} else if result.TestStatus == "" {
			s.successfulObservation = true
			delete(s.observationFailures, key)
		}
	}
	if len(results) != len(actions) && callErr == nil {
		callErr = fmt.Errorf("worker returned %d observation results for %d actions", len(results), len(actions))
	}
	if callErr != nil {
		s.observationFailures["batch"] = callErr.Error()
		return results, callErr
	}
	for _, result := range results {
		if result.Status != "ok" {
			return results, fmt.Errorf("observation action %q for %q failed: %s", result.Action, result.Target, result.Output)
		}
	}
	if s.readOnly && s.successfulObservation {
		s.loopFailure = ""
	}
	return results, nil
}

func (s *workerCoderTaskExecutionSession) recordVerificationResult(ctx context.Context, result ObservationActionResult) {
	s.testStatus = strings.TrimSpace(result.TestStatus)
	s.testReceipt = strings.TrimSpace(result.TestReceipt)
	if result.Status != "ok" || (s.testStatus != "passed" && s.testStatus != "not_applicable") {
		s.verificationFailure = "canonical verification did not pass"
		if result.Output != "" {
			s.verificationFailure = result.Output
		}
		s.verificationSnapshot = nil
		s.verificationReceiptDigest = ""
		return
	}
	digest, err := validateCoderWorkerTestEvidence(s.moduleRoot, s.testStatus, s.testReceipt)
	if err != nil {
		s.testStatus = "blocked"
		s.verificationFailure = err.Error()
		s.verificationSnapshot = nil
		s.verificationReceiptDigest = ""
		return
	}
	snapshot, err := captureCoderWorkspaceSnapshot(ctx, s.moduleRoot, s.scratchRoot, s.worker.config.CommandTimeout)
	if err != nil {
		s.testStatus = "blocked"
		s.verificationFailure = fmt.Sprintf("capture post-verification workspace: %v", err)
		s.verificationSnapshot = nil
		s.verificationReceiptDigest = ""
		return
	}
	s.verificationSnapshot = snapshot
	s.verificationFailure = ""
	s.verificationReceiptDigest = digest
}

func (s *workerCoderTaskExecutionSession) ExecuteProposal(
	ctx context.Context,
	p *proposal.Proposal,
) (*patch.PatchExecutionResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validate(ctx); err != nil {
		return nil, err
	}
	if s.readOnly {
		return nil, errors.New("read-only task cannot apply a patch proposal")
	}
	if p == nil || strings.TrimSpace(p.Patch()) == "" {
		s.patchFailure = "patch proposal is empty"
		s.successfulPatch = false
		return nil, errors.New(s.patchFailure)
	}
	s.patchFailure = "patch proposal is still awaiting owner execution"
	s.successfulPatch = false
	s.testStatus = ""
	s.testReceipt = ""
	s.verificationFailure = ""
	s.verificationSnapshot = nil
	s.verificationReceiptDigest = ""
	verificationCaptured := false
	result, executionErr := s.worker.executeProposal(ctx, s.taskID, p, func(result *patch.PatchExecutionResult) error {
		if err := s.validate(ctx); err != nil {
			s.verificationFailure = fmt.Sprintf("task admission changed before verification snapshot: %v", err)
			return err
		}
		status := strings.TrimSpace(result.TestStatus)
		receipt := strings.TrimSpace(result.TestReceipt)
		if status != "passed" && status != "not_applicable" {
			s.verificationFailure = "patch did not produce canonical verification evidence"
			return errors.New(s.verificationFailure)
		}
		digest, err := validateCoderWorkerTestEvidence(s.moduleRoot, status, receipt)
		if err != nil {
			s.verificationFailure = err.Error()
			return err
		}
		snapshot, err := captureCoderWorkspaceSnapshot(ctx, s.moduleRoot, s.scratchRoot, s.worker.config.CommandTimeout)
		if err != nil {
			s.verificationFailure = fmt.Sprintf("capture pre-autocommit verification workspace: %v", err)
			return errors.New(s.verificationFailure)
		}
		if err := s.validate(ctx); err != nil {
			s.verificationFailure = fmt.Sprintf("task admission changed during verification snapshot: %v", err)
			return err
		}
		s.testStatus = status
		s.testReceipt = receipt
		s.verificationSnapshot = snapshot
		s.verificationReceiptDigest = digest
		s.verificationFailure = ""
		verificationCaptured = true
		return nil
	})
	if ownerErr := s.validate(ctx); ownerErr != nil {
		s.patchFailure = "task owner changed during patch execution"
		s.successfulPatch = false
		s.verificationSnapshot = nil
		s.verificationReceiptDigest = ""
		return result, fmt.Errorf("coder patch result rejected after owner change: %w", ownerErr)
	}
	if executionErr != nil || result == nil {
		s.patchFailure = "patch execution did not return a result"
		s.successfulPatch = false
		if executionErr != nil {
			s.patchFailure = executionErr.Error()
		}
		return result, errors.Join(executionErr, errorIf(result == nil, "worker returned no patch execution result"))
	}
	commandsSucceeded := result.Success && successfulPatchCommands(result)
	s.successfulPatch = commandsSucceeded
	if !commandsSucceeded {
		s.patchFailure = "patch execution had no successful nonempty command set"
		if strings.TrimSpace(result.FailureReason) != "" {
			s.patchFailure = result.FailureReason
		}
	}
	s.testStatus = strings.TrimSpace(result.TestStatus)
	s.testReceipt = strings.TrimSpace(result.TestReceipt)
	verificationOK := verificationCaptured && s.verificationFailure == ""
	if !verificationOK {
		s.verificationFailure = "patch did not produce canonical verification evidence"
		if strings.TrimSpace(result.FailureReason) != "" && !verificationCaptured {
			s.verificationFailure = result.FailureReason
		}
		s.verificationSnapshot = nil
		s.verificationReceiptDigest = ""
	}
	if commandsSucceeded && verificationOK {
		s.patchFailure = ""
		s.loopFailure = ""
	} else if !commandsSucceeded && s.patchFailure == "" {
		s.patchFailure = "patch execution failed"
	}
	if !verificationOK {
		result.Success = false
		if result.FailureReason == "" {
			result.WithFailureMetadata(testImpactFailureKind(s.testStatus), s.verificationFailure, false)
		}
		return result, errors.New(s.verificationFailure)
	}
	if !commandsSucceeded {
		result.Success = false
		return result, errors.New(s.patchFailure)
	}
	return result, nil
}

func successfulPatchCommands(result *patch.PatchExecutionResult) bool {
	if result == nil || result.ExecutedCmds <= 0 || result.FailedCmds != 0 || len(result.Results) != result.ExecutedCmds {
		return false
	}
	for _, command := range result.Results {
		if !command.Success {
			return false
		}
	}
	return true
}

func (s *workerCoderTaskExecutionSession) CompletionEvidence(ctx context.Context) (CoderTaskCompletionEvidence, error) {
	return s.completionEvidence(ctx, captureCoderWorkspaceSnapshot)
}

func (s *workerCoderTaskExecutionSession) completionEvidence(
	ctx context.Context,
	captureSnapshot coderWorkspaceSnapshotCapture,
) (CoderTaskCompletionEvidence, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validate(ctx); err != nil {
		return CoderTaskCompletionEvidence{}, err
	}
	current, err := captureSnapshot(ctx, s.moduleRoot, s.scratchRoot, s.worker.config.CommandTimeout)
	if err != nil {
		return CoderTaskCompletionEvidence{}, fmt.Errorf("capture final coder workspace: %w", err)
	}
	changed := changedCoderWorkspaceFiles(s.baseline, current)
	if s.verificationSnapshot != nil && !equalCoderWorkspaceSnapshots(s.verificationSnapshot, current) {
		s.verificationFailure = "workspace changed after the latest canonical verification"
	}
	evidence := CoderTaskCompletionEvidence{
		TaskID:                s.taskID,
		ChangedFiles:          changed,
		ReadOnly:              s.readOnly,
		SuccessfulObservation: s.successfulObservation,
		SuccessfulPatch:       s.successfulPatch,
		TestStatus:            s.testStatus,
		TestReceipt:           s.testReceipt,
	}
	var failures []string
	for key, reason := range s.observationFailures {
		failures = append(failures, fmt.Sprintf("observation %s: %s", key, reason))
	}
	if s.loopFailure != "" {
		failures = append(failures, "CoderLoop protocol: "+s.loopFailure)
	}
	if s.patchFailure != "" {
		failures = append(failures, "patch: "+s.patchFailure)
	}
	if s.verificationFailure != "" {
		failures = append(failures, "verification: "+s.verificationFailure)
	}
	sort.Strings(failures)
	if s.readOnly {
		if !s.successfulObservation {
			failures = append(failures, "read-only completion requires a successful observation")
		}
		if len(changed) > 0 {
			failures = append(failures, "read-only task changed workspace files")
		}
	} else {
		if !s.successfulPatch {
			failures = append(failures, "write completion requires a successful nonempty patch")
		}
		if len(changed) == 0 {
			failures = append(failures, "write completion requires actual workspace changes")
		}
		if s.testStatus != "passed" && s.testStatus != "not_applicable" {
			failures = append(failures, "write completion requires canonical verification evidence")
		} else if receiptDigest, err := validateCoderWorkerTestEvidence(s.moduleRoot, s.testStatus, s.testReceipt); err != nil {
			failures = append(failures, "verification receipt: "+err.Error())
		} else if receiptDigest != s.verificationReceiptDigest {
			failures = append(failures, "Worker verification receipt changed after validation")
		}
		if s.verificationSnapshot == nil {
			failures = append(failures, "canonical verification is not bound to a workspace snapshot")
		}
	}
	if len(failures) > 0 {
		evidence.FailureReason = strings.Join(failures, "; ")
		return evidence, nil
	}
	if err := ctx.Err(); err != nil {
		return evidence, fmt.Errorf("coder completion context ended before owner evidence was finalized: %w", err)
	}
	if err := s.validate(ctx); err != nil {
		return evidence, fmt.Errorf("coder completion task admission expired before owner evidence was finalized: %w", err)
	}
	evidence.Ready = true
	return evidence, nil
}

func validateCoderWorkerTestEvidence(workspace, status, receiptPath string) (string, error) {
	switch status {
	case "not_applicable":
		if strings.TrimSpace(receiptPath) != "" {
			return "", errors.New("not_applicable test status cannot carry a receipt")
		}
		return "", nil
	case "passed":
	default:
		return "", fmt.Errorf("test status %q is not a completion status", status)
	}
	if strings.TrimSpace(receiptPath) == "" {
		return "", errors.New("passed test status has no Worker receipt")
	}
	receiptPath, err := filepath.Abs(receiptPath)
	if err != nil {
		return "", fmt.Errorf("resolve Worker receipt path: %w", err)
	}
	receiptRoot := filepath.Join(workspace, "Tmp", "test-results")
	if !coderPathWithin(receiptPath, receiptRoot) {
		return "", errors.New("Worker receipt is outside the workspace test-results owner")
	}
	if err := ensureNoSymlinkPath(workspace, receiptPath); err != nil {
		return "", fmt.Errorf("Worker receipt path is not canonical: %w", err)
	}
	info, err := os.Lstat(receiptPath)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("Worker test receipt is missing or not a regular file")
	}
	data, err := os.ReadFile(receiptPath)
	if err != nil {
		return "", fmt.Errorf("test-impact receipt unreadable: %w", err)
	}
	var receipt testImpactReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		return "", fmt.Errorf("test-impact receipt unreadable: %w", err)
	}
	planPaths, err := filepath.Glob(filepath.Join(workspace, "Tmp", "test-impact", "*", "plan.json"))
	if err != nil {
		return "", fmt.Errorf("list Worker test plans: %w", err)
	}
	var lastPlanErr error
	canonicalPlanPath := filepath.Join(workspace, "scripts", "test-local.plan.json")
	for _, planPath := range planPaths {
		if err := ensureNoSymlinkPath(workspace, planPath); err != nil {
			lastPlanErr = err
			continue
		}
		plan, err := readTestImpactPlan(planPath, workspace, canonicalPlanPath)
		if err != nil {
			lastPlanErr = err
			continue
		}
		if plan.PlanHash != receipt.ImpactPlan {
			continue
		}
		if status, err := validateTestImpactReceipt(receipt, plan, workspace); err != nil || status != "passed" {
			if err != nil {
				lastPlanErr = err
			} else {
				lastPlanErr = fmt.Errorf("Worker receipt status is %q", status)
			}
			continue
		}
		digest := sha256.Sum256(data)
		return hex.EncodeToString(digest[:]), nil
	}
	if lastPlanErr != nil {
		return "", fmt.Errorf("Worker receipt does not bind a current canonical test plan: %w", lastPlanErr)
	}
	return "", errors.New("Worker receipt does not bind a current canonical test plan")
}

func errorIf(condition bool, message string) error {
	if condition {
		return errors.New(message)
	}
	return nil
}

func coderObservationKey(action, target string) string {
	return action + "\x00" + target
}
