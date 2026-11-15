package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	appsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/application/superagent"
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
	tasks      *taskmanager.Manager
	ownerID    string
	actors     map[string]privateTaskWorker
	autonomous *autonomousTaskRuntime
}

func newPrivateTaskRunQueueRunner(tasks *taskmanager.Manager, ownerID string, mio, shiroChat *agent.MioAgent, midori *agent.WildAgent, privateTextWorkers map[string]privateTaskWorker) *privateTaskRunQueueRunner {
	actors := make(map[string]privateTaskWorker, 3+len(privateTextWorkers))
	if mio != nil {
		actors["mio"] = mio
	}
	if shiroChat != nil {
		actors["shiro"] = shiroChat
	}
	if midori != nil {
		actors["midori"] = midori
	}
	for actorID, worker := range privateTextWorkers {
		if !isPrivateTextWorkerActor(actorID) || worker == nil {
			continue
		}
		actors[actorID] = worker
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
	privateFirst := item.IdempotencyKey == domainsuperagent.RunQueueIntentPrivateFirstChat &&
		item.RunStartReason == domaintask.RunStartReasonFirst
	privateResume := domainsuperagent.IsPrivateTextResumeQueueItem(item)
	if (!privateFirst && !privateResume) || strings.TrimSpace(item.Action) != "chat" ||
		item.Goal != domainsuperagent.RunQueueGoalPrivateFirstChat || item.AttemptCount <= 0 {
		return "", errors.New("private task queue intent is not an exact first or checkpoint-resume run")
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
		run.StartReason != item.RunStartReason || run.Assignee != task.Assignee || run.TraceID != "" && run.TraceID != traceID {
		return "", errors.New("private task queue item does not match its active canonical Task and Run")
	}
	if privateFirst {
		if run.StartReason != domaintask.RunStartReasonFirst || item.PredecessorRunID != "" || item.CheckpointDigest != "" {
			return "", errors.New("private first-chat queue intent does not match its exact first Run")
		}
	} else {
		predecessor, err := r.tasks.GetRun(ctx, item.PredecessorRunID)
		if err != nil {
			return "", fmt.Errorf("read exact private text predecessor Run: %w", err)
		}
		if predecessor.RunID != item.PredecessorRunID || predecessor.TaskID != item.TaskID || predecessor.Status != domaintask.RunStatusWaiting || predecessor.CompletedAt == nil || predecessor.Validate() != nil {
			return "", errors.New("private checkpoint-resume Task does not match its exact waiting predecessor Run")
		}
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
	inputText, err := r.privateTaskInputText(ctx, task)
	if err != nil {
		return "", fmt.Errorf("build private task instruction: %w", err)
	}
	input, err := conversation.ReconstructTurnInput(
		task.TaskID, modulecore.NewTurnID(), traceID, modulecore.NewMessageID(), modulecore.NewMessageID(), inputText, address,
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
	checkpoint, found, err := r.tasks.GetPrivateTextCheckpoint(ctx, item.TaskID)
	if err != nil {
		return "", fmt.Errorf("read private task cursor: %w", err)
	}
	if !found || checkpoint.TaskID != item.TaskID || checkpoint.RunID != run.RunID || checkpoint.QueueItemID != item.QueueItemID ||
		checkpoint.ActorID != actorID || checkpoint.InputSHA256 != privateTaskInputSHA256(task.Title) {
		return "", errors.New("private task cursor does not match its canonical Task, Run, queue item, actor, and input")
	}
	if task.Autonomous != nil && (r.autonomous == nil || !r.autonomous.autonomousWindow(actorCtx, actorID, r.autonomous.now())) {
		return "", r.pausePrivateTaskRun(actorCtx, task, run, actorID)
	}
	if privateResume && run.StartCheckpointSHA256 != item.CheckpointDigest {
		return "", errors.New("private checkpoint-resume Run does not retain its exact predecessor digest")
	}
	if checkpoint.Output != "" {
		output := strings.TrimSpace(checkpoint.Output)
		if !validPrivateTaskOutput(output) {
			return "", r.closePrivateTaskFailure(ctx, task, run, actorID, errPrivateTaskExecutionFailed)
		}
		var decision domaintask.AutonomousDecision
		if task.Autonomous != nil && task.Autonomous.Kind == domaintask.AutonomousTaskKindPlan {
			if r.autonomous == nil {
				return "", errors.New("autonomous plan owner is unavailable")
			}
			if appsuperagent.RunQueueStopRequested(actorCtx) || ctx.Err() != nil || !r.autonomous.autonomousWindow(actorCtx, actorID, r.autonomous.now()) {
				return "", r.pausePrivateTaskRun(actorCtx, task, run, actorID)
			}
			decision, err = r.autonomous.preparePlanDecision(actorCtx, task, output)
			if err != nil {
				if _, invalid := err.(autonomousInvalidDecisionError); invalid {
					return "", r.closePrivateTaskFailure(ctx, task, run, actorID, err)
				}
				return "", r.pausePrivateTaskRun(actorCtx, task, run, actorID)
			}
			if err := r.saveAutonomousPlanDecision(actorCtx, task, run, actorID, decision); err != nil {
				return "", r.pausePrivateTaskRun(actorCtx, task, run, actorID)
			}
		}
		if err := r.completePrivateTaskRun(actorCtx, task, run, actorID, false); err != nil {
			if task.Autonomous != nil {
				return "", r.pausePrivateTaskRun(actorCtx, task, run, actorID)
			}
			return "", err
		}
		if err := r.afterPrivateTaskSuccess(actorCtx, task); err != nil {
			return "", err
		}
		return privateTaskQueueProjectionSummary, nil
	}
	if appsuperagent.RunQueueStopRequested(actorCtx) {
		return "", r.pausePrivateTaskRun(actorCtx, task, run, actorID)
	}
	providerParent := actorCtx
	var cancelDeadline context.CancelFunc
	if task.Autonomous != nil {
		if r.autonomous == nil {
			return "", errors.New("autonomous task owner is unavailable")
		}
		providerParent, cancelDeadline = context.WithTimeout(actorCtx, autonomousProviderTimeout(r.autonomous.now(), r.autonomous.location()))
		defer cancelDeadline()
	}
	providerCtx, cancelProvider := context.WithCancel(providerParent)
	stopSignal := appsuperagent.RunQueueStopSignal(actorCtx)
	watcherDone := make(chan struct{})
	go func() {
		select {
		case <-stopSignal:
			cancelProvider()
		case <-watcherDone:
		case <-actorCtx.Done():
			cancelProvider()
		}
	}()
	output, workErr := worker.ExecutePrivateWork(providerCtx, input, []string{})
	close(watcherDone)
	cancelProvider()
	pauseRequested := appsuperagent.RunQueueStopRequested(actorCtx) || actorCtx.Err() != nil ||
		(task.Autonomous != nil && !r.autonomous.autonomousWindow(actorCtx, actorID, r.autonomous.now()))
	if workErr != nil {
		if pauseRequested {
			return "", r.pausePrivateTaskRun(actorCtx, task, run, actorID)
		}
		return "", r.closePrivateTaskFailure(ctx, task, run, actorID, workErr)
	}
	output = strings.TrimSpace(output)
	if !validPrivateTaskOutput(output) {
		return "", r.closePrivateTaskFailure(ctx, task, run, actorID, errPrivateTaskExecutionFailed)
	}
	persistCtx, cancelPersist := privateTaskPersistenceContext(actorCtx)
	defer cancelPersist()
	if err := r.tasks.SavePrivateTextOutput(persistCtx, task.TaskID, run.RunID, actorID, output); err != nil {
		return "", err
	}
	if pauseRequested {
		return "", r.pausePrivateTaskRun(actorCtx, task, run, actorID)
	}
	if task.Autonomous != nil && task.Autonomous.Kind == domaintask.AutonomousTaskKindPlan {
		if r.autonomous == nil {
			return "", errors.New("autonomous plan owner is unavailable")
		}
		decision, err := r.autonomous.preparePlanDecision(actorCtx, task, output)
		if err != nil {
			if _, invalid := err.(autonomousInvalidDecisionError); invalid {
				return "", r.closePrivateTaskFailure(ctx, task, run, actorID, err)
			}
			return "", r.pausePrivateTaskRun(actorCtx, task, run, actorID)
		}
		if err := r.saveAutonomousPlanDecision(actorCtx, task, run, actorID, decision); err != nil {
			return "", r.pausePrivateTaskRun(actorCtx, task, run, actorID)
		}
	}
	if err := r.tasks.CompletePrivateTextRun(persistCtx, task.TaskID, run.RunID, actorID, false); err != nil {
		if task.Autonomous != nil {
			return "", r.pausePrivateTaskRun(actorCtx, task, run, actorID)
		}
		return "", err
	}
	if err := r.afterPrivateTaskSuccess(persistCtx, task); err != nil {
		return "", err
	}
	return privateTaskQueueProjectionSummary, nil
}

func (r *privateTaskRunQueueRunner) privateTaskInputText(ctx context.Context, task domaintask.Task) (string, error) {
	if task.Autonomous == nil {
		return task.Title, nil
	}
	if r.autonomous == nil {
		return "", errors.New("autonomous instruction owner is unavailable")
	}
	if task.Autonomous.Kind == domaintask.AutonomousTaskKindPlan {
		shared, err := r.tasks.Context(ctx, task.TaskID)
		if err != nil {
			return "", err
		}
		if shared.UserIntent == "" || len(shared.UserIntent) > autonomousEvaluationPromptMax || shared.CurrentPlan == "" {
			return "", errors.New("autonomous plan prompt is missing or exceeds its bounded context")
		}
		return shared.UserIntent, nil
	}
	if task.Autonomous.Kind != domaintask.AutonomousTaskKindWork {
		return "", errors.New("autonomous Task kind is unsupported")
	}
	plan, err := r.tasks.Get(ctx, task.Autonomous.SourceTaskID)
	if err != nil {
		return "", err
	}
	sharedPlan, err := r.tasks.Context(ctx, plan.TaskID)
	if err != nil {
		return "", err
	}
	if plan.Autonomous == nil || plan.Autonomous.Kind != domaintask.AutonomousTaskKindPlan || plan.Status != domaintask.StatusSucceeded ||
		sharedPlan.AutonomousDecision == nil || sharedPlan.AutonomousDecision.Decision != domaintask.AutonomousDecisionContinue ||
		sharedPlan.AutonomousDecision.Title != task.Title {
		return "", errors.New("autonomous work does not match its saved source plan decision")
	}
	shared, err := r.tasks.Context(ctx, task.TaskID)
	if err != nil {
		return "", err
	}
	decision := sharedPlan.AutonomousDecision
	if shared.UserIntent != decision.Objective || shared.CurrentPlan != decision.Completion {
		return "", errors.New("autonomous work instruction differs from its saved objective and completion condition")
	}
	return fmt.Sprintf("Task: %s\nObjective: %s\nCompletion condition: %s\n\nProduce only the private CHAT text result. Do not use or request tools, external actions, network access, file writes, shell commands, purchases, messages, or state changes.", task.Title, shared.UserIntent, shared.CurrentPlan), nil
}

func (r *privateTaskRunQueueRunner) afterPrivateTaskSuccess(ctx context.Context, task domaintask.Task) error {
	if r == nil || r.autonomous == nil || task.Autonomous == nil {
		return nil
	}
	callbackCtx, cancel := privateTaskPersistenceContext(ctx)
	defer cancel()
	if err := r.autonomous.afterPrivateTaskSuccess(callbackCtx, task); err != nil {
		log.Printf("autonomous successor registration pending actor=%s task_id=%s kind=%s error=%v", task.Assignee, task.TaskID, task.Autonomous.Kind, err)
	}
	return nil
}

func (r *privateTaskRunQueueRunner) saveAutonomousPlanDecision(ctx context.Context, task domaintask.Task, run domaintask.Run, actorID string, decision domaintask.AutonomousDecision) error {
	if r == nil || r.autonomous == nil {
		return errors.New("autonomous plan owner is unavailable")
	}
	if err := ctx.Err(); err != nil || appsuperagent.RunQueueStopRequested(ctx) || !r.autonomous.autonomousWindow(ctx, actorID, r.autonomous.now()) {
		if err != nil {
			return err
		}
		return context.Canceled
	}
	persistCtx, cancel := privateTaskPersistenceContext(ctx)
	defer cancel()
	return r.autonomous.savePlanDecision(persistCtx, task, run, actorID, decision)
}

func (r *privateTaskRunQueueRunner) pausePrivateTaskRun(ctx context.Context, task domaintask.Task, run domaintask.Run, actorID string) error {
	persistCtx, cancel := privateTaskPersistenceContext(ctx)
	defer cancel()
	if err := r.tasks.CompletePrivateTextRun(persistCtx, task.TaskID, run.RunID, actorID, true); err != nil {
		if task.Autonomous != nil {
			// Keep the queue scheduler out of its generic failure path. Its pause
			// owner check decides whether the Task is waiting or still needs exact
			// startup recovery; the Task itself remains canonical either way.
			return errors.Join(appsuperagent.RunQueuePauseOutcome{}, err)
		}
		return err
	}
	return appsuperagent.RunQueuePauseOutcome{}
}

func (r *privateTaskRunQueueRunner) completePrivateTaskRun(ctx context.Context, task domaintask.Task, run domaintask.Run, actorID string, pause bool) error {
	persistCtx, cancel := privateTaskPersistenceContext(ctx)
	defer cancel()
	return r.tasks.CompletePrivateTextRun(persistCtx, task.TaskID, run.RunID, actorID, pause)
}

func privateTaskPersistenceContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
}

func privateTaskInputSHA256(input string) string {
	digest := sha256.Sum256([]byte(input))
	return hex.EncodeToString(digest[:])
}

func validPrivateTaskOutput(output string) bool {
	return output != "" && utf8.ValidString(output) && len(output) <= conversation.MaxReportResultBytes
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
