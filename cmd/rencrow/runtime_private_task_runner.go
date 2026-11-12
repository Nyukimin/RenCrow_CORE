package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domainrouting "github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const privateTaskQueueProjectionSummary = "private task completed"

var errPrivateTaskExecutionFailed = errors.New("private task execution failed")

type privateTaskWorker interface {
	ExecutePrivateWork(context.Context, conversation.TurnInput, []string) (string, error)
}

type privateTaskRunQueueRunner struct {
	tasks   *taskmanager.Manager
	ownerID string
	actors  map[string]privateTaskWorker
}

func newPrivateTaskRunQueueRunner(tasks *taskmanager.Manager, ownerID string, mio, shiroChat *agent.MioAgent, midori *agent.WildAgent) *privateTaskRunQueueRunner {
	actors := make(map[string]privateTaskWorker, 3)
	if mio != nil {
		actors["mio"] = mio
	}
	if shiroChat != nil {
		actors["shiro"] = shiroChat
	}
	if midori != nil {
		actors["midori"] = midori
	}
	return &privateTaskRunQueueRunner{tasks: tasks, ownerID: strings.TrimSpace(ownerID), actors: actors}
}

func (r *privateTaskRunQueueRunner) process(ctx context.Context, item domainsuperagent.RunQueueItem, traceID modulecore.TraceID) (string, error) {
	if r == nil || r.tasks == nil {
		return "", errors.New("private task owner is unavailable")
	}
	if err := traceID.Validate(); err != nil {
		return "", fmt.Errorf("private task trace_id is invalid: %w", err)
	}
	if err := item.TaskID.Validate(); err != nil {
		return "", fmt.Errorf("private task task_id is invalid: %w", err)
	}
	if err := item.RunID.Validate(); err != nil {
		return "", fmt.Errorf("private task run_id is invalid: %w", err)
	}
	if item.IdempotencyKey != domainsuperagent.RunQueueIntentPrivateFirstChat || strings.TrimSpace(item.Action) != "chat" ||
		item.Goal != domainsuperagent.RunQueueGoalPrivateFirstChat || item.RunStartReason != domaintask.RunStartReasonFirst || item.AttemptCount <= 0 {
		return "", errors.New("private task queue intent is not an exact first-chat run")
	}

	task, err := r.tasks.Get(ctx, item.TaskID)
	if err != nil {
		return "", fmt.Errorf("load private task: %w", err)
	}
	run, err := r.tasks.GetRun(ctx, item.RunID)
	if err != nil {
		return "", fmt.Errorf("load private task run: %w", err)
	}
	if task.TaskID != item.TaskID || run.RunID != item.RunID || run.TaskID != item.TaskID || task.Status != domaintask.StatusRunning || run.Status != domaintask.RunStatusRunning ||
		run.StartReason != domaintask.RunStartReasonFirst || run.Assignee != task.Assignee || run.TraceID != "" && run.TraceID != traceID {
		return "", errors.New("private task queue item does not match its active canonical Task and Run")
	}
	runs, err := r.tasks.ListRuns(ctx, domaintask.RunFilter{TaskID: item.TaskID, Limit: 2})
	if err != nil {
		return "", fmt.Errorf("read private task Run history: %w", err)
	}
	if len(runs) != 1 || runs[0].RunID != item.RunID {
		return "", errors.New("private first-chat Task does not have exactly its active first Run")
	}
	if err := validatePrivateFirstChatTask(task, r.ownerID, domaintask.StatusRunning); err != nil {
		return "", fmt.Errorf("private task is outside the first-chat execution scope: %w", err)
	}
	if item.WorkstreamID != string(task.WorkstreamID) {
		return "", errors.New("private task queue workstream does not match its canonical Task")
	}
	actorID := task.Assignee
	worker := r.actors[actorID]
	if worker == nil {
		return "", errors.New("private task actor is unavailable")
	}

	address, err := conversation.NewChannelAddress("superagent", item.TaskID.String())
	if err != nil {
		return "", fmt.Errorf("create private task channel address: %w", err)
	}
	input, err := conversation.ReconstructTurnInput(
		task.TaskID, modulecore.NewTurnID(), traceID, modulecore.NewMessageID(), modulecore.NewMessageID(), task.Title, address,
	)
	if err != nil {
		return "", fmt.Errorf("build private task input: %w", err)
	}
	input = input.WithViewerRecipient(actorID).WithRoute(domainrouting.RouteCHAT)
	actorCtx, err := execution.WithIdentity(ctx, task.TaskID, run.RunID, traceID)
	if err != nil {
		return "", fmt.Errorf("bind private task execution identity: %w", err)
	}
	scope, err := tool.NewToolExecutionScope(string(traceID), tool.ActorKindAgent, actorID, "", []string{tool.DataScopePublic}, tool.AuthenticationSourceAgentOrchestrator)
	if err != nil {
		return "", fmt.Errorf("create private task Agent scope: %w", err)
	}
	scope.AgentRole, scope.Purpose = "worker", "private_task_execution"
	if err := scope.Validate(); err != nil {
		return "", fmt.Errorf("validate private task Agent scope: %w", err)
	}
	actorCtx = tool.WithToolExecutionScope(actorCtx, scope)
	actorCtx = llm.WithExecutionObservationDefaults(actorCtx, llm.ExecutionObservation{
		TaskID: task.TaskID, TraceID: string(traceID), Initiator: actorID, Caller: "private_task_runner", Purpose: "private_task_execution",
	})
	if _, err := r.tasks.ValidateExecutionContext(actorCtx); err != nil {
		return "", fmt.Errorf("validate private task owner context: %w", err)
	}

	output, workErr := worker.ExecutePrivateWork(actorCtx, input, []string{})
	if workErr != nil {
		return "", r.closePrivateTaskFailure(ctx, task, run, actorID, workErr)
	}
	if err := ctx.Err(); err != nil {
		return "", r.closePrivateTaskFailure(ctx, task, run, actorID, err)
	}
	output = strings.TrimSpace(output)
	if output == "" || !utf8.ValidString(output) || len(output) > conversation.MaxReportResultBytes {
		return "", r.closePrivateTaskFailure(ctx, task, run, actorID, errPrivateTaskExecutionFailed)
	}
	if err := r.tasks.CloseStartedRun(ctx, task.TaskID, run.RunID, actorID, taskmanager.RunOutcome{
		Status: domaintask.StatusSucceeded, Summary: output,
	}, nil); err != nil {
		return "", err
	}
	return privateTaskQueueProjectionSummary, nil
}

func (r *privateTaskRunQueueRunner) closePrivateTaskFailure(ctx context.Context, task domaintask.Task, run domaintask.Run, actorID string, cause error) error {
	statusCause := errPrivateTaskExecutionFailed
	if ctx.Err() != nil && errors.Is(cause, context.Canceled) {
		statusCause = context.Canceled
	} else if ctx.Err() != nil && errors.Is(cause, context.DeadlineExceeded) {
		statusCause = context.DeadlineExceeded
	}
	return r.tasks.CloseStartedRun(ctx, task.TaskID, run.RunID, actorID, taskmanager.RunOutcomeOf(statusCause, ""), statusCause)
}
