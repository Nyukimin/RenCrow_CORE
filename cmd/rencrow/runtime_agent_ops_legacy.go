package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/subagent"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// The legacy branch serves POST /v1/agent/ops while the native coding profile
// is disabled (the production default). Like every other Shiro execution path
// it runs on a Task and Run issued by the Task owner and, when the SuperAgent
// ledger is configured, on a recorded Lead Agent run that the Subagent Manager
// can attach its records to. Only fixed text is stored; the request message is
// never written to the Task, the ledger or the log.
const (
	agentOpsLegacyTaskTitle  = "Local Agent OPS request"
	agentOpsLegacyTaskIntent = "Local Agent OPS request"
	agentOpsLegacyLeadGoal   = "Run an authenticated local Agent OPS request"
	// agentOpsLegacyFinalizationTimeout is the budget of one terminal write.
	// It is deliberately detached from the request context (a client cancel
	// must still close the Run) and large enough for a slow Task store: a
	// terminal write that expires leaves the Run active and holds the single
	// operations slot.
	agentOpsLegacyFinalizationTimeout = 60 * time.Second
	agentOpsLegacyLogErrorBytes       = 512
)

var errAgentOpsLegacyEmptyOutput = errors.New("executor returned an empty output")

// agentOpsLegacyOutcome is the decided result of one admitted request. It is
// written to the Task owner exactly once and then projected to the response.
type agentOpsLegacyOutcome struct {
	output     string
	taskStatus domaintask.Status
	summary    string
	httpStatus int
	errorCode  string
}

func agentOpsLegacyFailure(status domaintask.Status, summary string, httpStatus int, errorCode string) agentOpsLegacyOutcome {
	return agentOpsLegacyOutcome{taskStatus: status, summary: summary, httpStatus: httpStatus, errorCode: errorCode}
}

// agentOpsLegacyRequest is the per-request state shared by the execution and
// the finalization of one admitted legacy OPS request.
type agentOpsLegacyRequest struct {
	requestID string
	task      domaintask.Task
	run       domaintask.Run
	turn      conversation.TurnInput
	leadInput orchestrator.LeadAgentRunInput
	lead      orchestrator.LeadAgentRunRecord
	// leadStarted is set once the Lead Agent run was recorded as started, which
	// obliges the finalization to record it as finished.
	leadStarted bool
}

func (h *agentOpsHandler) serveLegacyOPS(w http.ResponseWriter, parentContext context.Context, requestID, message string) {
	shiroContext, err := deriveAgentOpsShiroContext(parentContext, requestID)
	if err != nil {
		writeAgentOpsError(w, http.StatusInternalServerError, "runtime_unavailable")
		return
	}
	releaseWorkerBusy := h.acquireWorkerBusyLease()
	defer releaseWorkerBusy()

	// The input is pure validation, so it is built before any state is created.
	taskID := modulecore.NewTaskID()
	address, err := conversation.NewChannelAddress(agentOpsTaskChannel, agentOpsTaskChatID)
	if err != nil {
		writeAgentOpsError(w, http.StatusInternalServerError, "execution_failed")
		return
	}
	turn, err := conversation.NewTurnInput(taskID, message, address)
	if err != nil {
		writeAgentOpsError(w, http.StatusInternalServerError, "execution_failed")
		return
	}
	turn = turn.
		WithSessionID(string(modulecore.NewSessionID())).
		WithRoute(routing.RouteOPS)

	task, run, err := h.admitLegacyOPS(shiroContext, taskID)
	if err != nil {
		logAgentOpsLegacy("admission failed", requestID, taskID, err)
		if errors.Is(err, taskmanager.ErrParallelLimit) {
			writeAgentOpsError(w, http.StatusServiceUnavailable, "runtime_unavailable")
		} else {
			writeAgentOpsError(w, http.StatusInternalServerError, "runtime_unavailable")
		}
		return
	}
	state := &agentOpsLegacyRequest{requestID: requestID, task: task, run: run, turn: turn}

	// A panic below would otherwise leave the Run active and hold the single
	// operations slot until the next restart.
	finalizing := false
	defer func() {
		if !finalizing {
			_ = h.completeLegacyOPSRun(shiroContext, state, domaintask.StatusFailed, "OPS request ended without a terminal state")
		}
	}()

	outcome := h.executeLegacyOPS(shiroContext, state)
	finalizing = true
	outcome = h.finalizeLegacyOPS(shiroContext, state, outcome)
	if outcome.errorCode != "" {
		writeAgentOpsError(w, outcome.httpStatus, outcome.errorCode)
		return
	}
	writeJSONStatus(w, http.StatusOK, agentOpsResponse{
		RequestID: requestID,
		TaskID:    task.TaskID.String(),
		AgentID:   "shiro",
		Role:      "worker",
		Route:     routing.RouteOPS.String(),
		Output:    outcome.output,
	})
}

// admitLegacyOPS creates the Task and its first Run through the Task owner. A
// Task whose Run could not start is closed as failed so that no queued Task is
// left behind.
func (h *agentOpsHandler) admitLegacyOPS(ctx context.Context, taskID modulecore.TaskID) (domaintask.Task, domaintask.Run, error) {
	task, err := h.taskOwner.Create(ctx, domaintask.Task{
		TaskID:   taskID,
		Title:    agentOpsLegacyTaskTitle,
		Route:    domaintask.RouteOperations,
		OwnerID:  "shiro",
		Assignee: "shiro",
	}, domaintask.SharedRoleContext{TaskID: taskID, UserIntent: agentOpsLegacyTaskIntent})
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, err
	}
	run, err := h.taskOwner.StartRunWithReason(ctx, task.TaskID, domaintask.RunStartReasonFirst)
	if err != nil {
		summary := "OPS admission failed"
		if errors.Is(err, taskmanager.ErrParallelLimit) {
			summary = "Task execution capacity is unavailable"
		}
		terminalContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), agentOpsLegacyFinalizationTimeout)
		defer cancel()
		_, failErr := h.taskOwner.Fail(terminalContext, task.TaskID, summary, nil)
		return domaintask.Task{}, domaintask.Run{}, errors.Join(err, failErr)
	}
	return task, run, nil
}

// executeLegacyOPS runs Shiro on the admitted Run and decides the outcome. It
// records the Lead Agent run as started when the ledger is configured and gives
// Shiro (and through it the Subagent Manager) the runtime context of that run.
func (h *agentOpsHandler) executeLegacyOPS(parentContext context.Context, state *agentOpsLegacyRequest) agentOpsLegacyOutcome {
	traceID := modulecore.NewTraceID()
	executionContext, err := domainexecution.WithIdentity(parentContext, state.task.TaskID, state.run.RunID, traceID)
	if err != nil {
		logAgentOpsLegacy("execution identity failed", state.requestID, state.task.TaskID, err)
		return agentOpsLegacyFailure(domaintask.StatusFailed, "OPS execution identity failed", http.StatusInternalServerError, "runtime_unavailable")
	}
	if h.leadRuns != nil {
		state.leadInput = orchestrator.LeadAgentRunInput{
			TraceID:   string(traceID),
			SessionID: state.turn.SessionID(),
			Channel:   agentOpsTaskChannel,
			ChatID:    agentOpsTaskChatID,
			Goal:      agentOpsLegacyLeadGoal,
		}
		state.lead, err = orchestrator.RecordLeadAgentRunStarted(executionContext, h.leadRuns, state.leadInput, state.task.TaskID, state.run.RunID, "shiro", routing.RouteOPS)
		if err != nil {
			logAgentOpsLegacy("lead run start record failed", state.requestID, state.task.TaskID, err)
			return agentOpsLegacyFailure(domaintask.StatusFailed, "OPS lead run record failed", http.StatusInternalServerError, "runtime_unavailable")
		}
		state.leadStarted = true
		executionContext = subagent.WithSuperAgentRuntime(
			executionContext, state.task.TaskID, state.run.RunID, "shiro", state.lead.TraceID, state.lead.StartedEventID,
			[]string{"session:" + state.leadInput.SessionID, "route:" + routing.RouteOPS.String()}, nil,
			"return summary-only subagent result to Lead Agent",
		)
	}

	output, err := h.executor.Execute(executionContext, state.turn)
	if err == nil && strings.TrimSpace(output) == "" {
		err = errAgentOpsLegacyEmptyOutput
	}
	if err != nil {
		logAgentOpsLegacy("execution failed", state.requestID, state.task.TaskID, err)
		status, summary := domaintask.StatusFailed, "OPS execution failed"
		if errors.Is(err, context.Canceled) {
			status, summary = domaintask.StatusCancelled, "OPS execution cancelled"
		}
		return agentOpsLegacyFailure(status, summary, http.StatusInternalServerError, "execution_failed")
	}
	return agentOpsLegacyOutcome{output: output, taskStatus: domaintask.StatusSucceeded, summary: "OPS execution completed"}
}

// finalizeLegacyOPS closes the Lead Agent run and then the Task Run. The Run
// is always closed, even when the ledger write fails, so that the operations
// slot is never leaked; a ledger failure turns a success into a failure.
func (h *agentOpsHandler) finalizeLegacyOPS(parentContext context.Context, state *agentOpsLegacyRequest, outcome agentOpsLegacyOutcome) agentOpsLegacyOutcome {
	terminalContext, cancel := context.WithTimeout(context.WithoutCancel(parentContext), agentOpsLegacyFinalizationTimeout)
	defer cancel()

	if state.leadStarted {
		leadStatus, leadSummary := agentOpsLegacyLeadStatus(outcome.taskStatus)
		if err := orchestrator.RecordLeadAgentRunFinished(terminalContext, h.leadRuns, state.leadInput, state.task.TaskID, state.run.RunID, "shiro", routing.RouteOPS, state.lead, leadStatus, leadSummary); err != nil {
			stage := "lead run finish record failed"
			if outcome.taskStatus == domaintask.StatusSucceeded {
				// Shiro already acted, so say so: the ledger is what is missing.
				stage += " after successful execution"
				outcome = agentOpsLegacyFailure(domaintask.StatusFailed, "OPS lead run record failed", http.StatusInternalServerError, "runtime_unavailable")
			}
			logAgentOpsLegacy(stage, state.requestID, state.task.TaskID, err)
		}
	}
	if err := h.completeLegacyOPSRunWith(terminalContext, state, outcome.taskStatus, outcome.summary); err != nil {
		return agentOpsLegacyFailure(outcome.taskStatus, outcome.summary, http.StatusInternalServerError, "runtime_unavailable")
	}
	return outcome
}

// completeLegacyOPSRun closes the Run on a context detached from the request.
func (h *agentOpsHandler) completeLegacyOPSRun(parentContext context.Context, state *agentOpsLegacyRequest, status domaintask.Status, summary string) error {
	terminalContext, cancel := context.WithTimeout(context.WithoutCancel(parentContext), agentOpsLegacyFinalizationTimeout)
	defer cancel()
	return h.completeLegacyOPSRunWith(terminalContext, state, status, summary)
}

func (h *agentOpsHandler) completeLegacyOPSRunWith(terminalContext context.Context, state *agentOpsLegacyRequest, status domaintask.Status, summary string) error {
	if _, err := h.taskOwner.CompleteRun(terminalContext, state.task.TaskID, state.run.RunID, "shiro", status, summary, ""); err != nil {
		logAgentOpsLegacy("terminal write failed", state.requestID, state.task.TaskID, err)
		return err
	}
	return nil
}

func agentOpsLegacyLeadStatus(status domaintask.Status) (string, string) {
	switch status {
	case domaintask.StatusSucceeded:
		return "completed", "Lead Agent completed"
	case domaintask.StatusCancelled:
		return "cancelled", "OPS request cancelled"
	default:
		return "failed", "OPS request failed"
	}
}

// logAgentOpsLegacy writes one diagnostic line. It carries identifiers and the
// bounded error text only; the request message is never logged.
func logAgentOpsLegacy(stage, requestID string, taskID modulecore.TaskID, err error) {
	log.Printf("[AgentOps] %s request_id=%s task_id=%s err=%q", stage, requestID, taskID, boundedAgentOpsLogText(err))
}

func boundedAgentOpsLogText(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	if len(text) > agentOpsLegacyLogErrorBytes {
		text = strings.ToValidUTF8(text[:agentOpsLegacyLogErrorBytes], "")
	}
	return text
}
