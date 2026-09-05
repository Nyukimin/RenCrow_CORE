package worker

import (
	"fmt"
	"strings"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type ProposalPatchArgs struct {
	Plan     string
	Patch    string
	Risk     string
	CostHint string
}

func NormalizeToolName(tool string) string {
	return strings.TrimSpace(tool)
}

func IsSupportedTool(tool string) bool {
	return NormalizeToolName(tool) == ToolProposalPatch
}

// Validate enforces the canonical correlation identity at the Worker boundary.
func (action Action) Validate() error {
	if err := action.TaskID.Validate(); err != nil {
		return fmt.Errorf("worker action task_id is invalid: %w", err)
	}
	return nil
}

// Validate enforces the canonical correlation identity on a Worker result.
func (result Result) Validate() error {
	if err := result.TaskID.Validate(); err != nil {
		return fmt.Errorf("worker result task_id is invalid: %w", err)
	}
	return nil
}

func ExtractProposalPatchArgs(action Action) (ProposalPatchArgs, error) {
	if err := action.Validate(); err != nil {
		return ProposalPatchArgs{}, err
	}
	if !IsSupportedTool(action.Tool) {
		return ProposalPatchArgs{}, fmt.Errorf("unsupported worker tool: %s", action.Tool)
	}
	args := ProposalPatchArgs{
		Plan:     StringArg(action.Arguments, "plan"),
		Patch:    StringArg(action.Arguments, "patch"),
		Risk:     StringArg(action.Arguments, "risk"),
		CostHint: StringArg(action.Arguments, "cost_hint"),
	}
	if args.Plan == "" || args.Patch == "" {
		return ProposalPatchArgs{}, fmt.Errorf("proposal action requires plan and patch")
	}
	return args, nil
}

func StringArg(args map[string]any, name string) string {
	if args == nil {
		return ""
	}
	value, ok := args[name]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func ResultStatusFromPatchExecution(summary *PatchExecutionSummary) Status {
	if summary == nil || !summary.Success {
		return StatusFailed
	}
	return StatusSucceeded
}

func OutputFromPatchExecution(summary *PatchExecutionSummary) string {
	if summary == nil {
		return ""
	}
	return summary.Summary
}

func MetadataFromPatchExecution(summary *PatchExecutionSummary) map[string]any {
	if summary == nil {
		return nil
	}
	return map[string]any{
		"success":        summary.Success,
		"executed_cmds":  summary.ExecutedCmds,
		"failed_cmds":    summary.FailedCmds,
		"git_commit":     summary.GitCommit,
		"failure_kind":   summary.FailureKind,
		"failure_reason": summary.FailureReason,
		"retryable":      summary.Retryable,
		"failed_index":   summary.FailedIndex,
	}
}

func BuildFailedResult(taskID modulecore.TaskID, status Status, message string, startedAt time.Time, finishedAt time.Time) Result {
	if status == "" {
		status = StatusFailed
	}
	if finishedAt.IsZero() {
		finishedAt = time.Now().UTC()
	}
	return Result{
		TaskID:     taskID,
		Status:     status,
		Error:      message,
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
	}
}

func BuildPatchExecutionResult(taskID modulecore.TaskID, summary *PatchExecutionSummary, startedAt time.Time, finishedAt time.Time) Result {
	if finishedAt.IsZero() {
		finishedAt = time.Now().UTC()
	}
	return Result{
		TaskID:     taskID,
		Status:     ResultStatusFromPatchExecution(summary),
		Output:     OutputFromPatchExecution(summary),
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
		Metadata:   MetadataFromPatchExecution(summary),
	}
}

func BuildActionErrorResult(action Action, err error, startedAt time.Time, finishedAt time.Time) Result {
	status := StatusFailed
	if !IsSupportedTool(action.Tool) {
		status = StatusDenied
	}
	message := ""
	if err != nil {
		message = err.Error()
	}
	taskID := action.TaskID
	if taskID.Validate() != nil {
		// An invalid inbound identifier cannot become result correlation state.
		taskID = ""
	}
	return BuildFailedResult(taskID, status, message, startedAt, finishedAt)
}
