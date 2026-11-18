package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskcenter"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const nativeTaskPlanningPrefix = "/tasks "

type nativeTaskPlanningService interface {
	List(context.Context, taskcenter.ListQuery) (taskcenter.ListResult, error)
	Get(context.Context, string) (taskcenter.Item, error)
	Capabilities(context.Context) (taskcenter.Capabilities, error)
	Submit(context.Context, taskcenter.SubmitInput) (taskcenter.MutationResult, error)
	Claim(context.Context, taskcenter.ClaimInput) (taskcenter.MutationResult, error)
}

type nativeTaskPlanningExecution interface {
	ExecuteTaskCenterExecution(context.Context, taskcenter.ExecutionInput) (taskcenter.ExecutionResult, error)
}

type nativeTaskPlanningDispatcher struct {
	service  nativeTaskPlanningService
	executor nativeTaskPlanningExecution
	tasks    *taskmanager.Manager
}

func attachTaskCenterPlanningPorts(cfg *config.Config, deps *Dependencies, agents agentRuntime) {
	if deps == nil || deps.taskCenter == nil || deps.taskManager == nil {
		return
	}
	var executor nativeTaskPlanningExecution
	if deps.taskCenter.NativeExecutionAvailable() {
		executor = deps.taskCenter
	}
	dispatcher := &nativeTaskPlanningDispatcher{service: deps.taskCenter, executor: executor, tasks: deps.taskManager}
	if agents.Mio != nil {
		agents.Mio.WithNativePlanningPort(dispatcher, "mio")
	}
	if agents.ShiroChat != nil {
		agents.ShiroChat.WithNativePlanningPort(dispatcher, "shiro")
	}
	if agents.Shiro != nil {
		agents.Shiro.WithNativePlanningPort(dispatcher, "shiro")
	}
	if agents.Heavy != nil {
		agents.Heavy.WithNativePlanningPort(dispatcher, "kuro")
	}
	if agents.Wild != nil {
		agents.Wild.WithNativePlanningPort(dispatcher, "midori")
	}
}

func attachCoderTaskPlanningPorts(cfg *config.Config, deps *Dependencies, coders ...*coderAdapter) {
	if cfg == nil || deps == nil || deps.taskCenter == nil || deps.taskManager == nil {
		return
	}
	configured := []struct {
		slot, actor string
		value       config.CoderConfig
	}{
		{slot: "coder1", actor: "aka", value: cfg.Coder1},
		{slot: "coder2", actor: "ao", value: cfg.Coder2},
		{slot: "coder3", actor: "kin", value: cfg.Coder3},
		{slot: "coder4", actor: "gin", value: cfg.Coder4},
	}
	bySlot := map[string]*coderAdapter{"coder1": nil, "coder2": nil, "coder3": nil, "coder4": nil}
	for index, coder := range coders {
		if index < len(configured) {
			bySlot[configured[index].slot] = coder
		}
	}
	for _, binding := range configured {
		if bySlot[binding.slot] == nil || !binding.value.Enabled || strings.ToLower(strings.TrimSpace(binding.value.Name)) != binding.actor {
			continue
		}
		var executor nativeTaskPlanningExecution
		if deps.taskCenter.NativeExecutionAvailable() {
			executor = deps.taskCenter
		}
		bySlot[binding.slot].WithNativePlanningPort(&nativeTaskPlanningDispatcher{service: deps.taskCenter, executor: executor, tasks: deps.taskManager}, binding.actor)
	}
}

func renderNativeTaskPlanningCapability(cfg *config.Config, deps *Dependencies, agents agentRuntime, coders ...*coderAdapter) string {
	if cfg == nil || deps == nil || deps.taskCenter == nil || deps.taskManager == nil {
		return ""
	}
	connected := make([]string, 0, 8)
	if agents.Mio != nil {
		connected = append(connected, "mio")
	}
	if agents.Shiro != nil || agents.ShiroChat != nil {
		connected = append(connected, "shiro")
	}
	if agents.Heavy != nil {
		connected = append(connected, "kuro")
	}
	if agents.Wild != nil {
		connected = append(connected, "midori")
	}
	bindings := []struct {
		actor string
		value config.CoderConfig
	}{
		{"aka", cfg.Coder1}, {"ao", cfg.Coder2}, {"kin", cfg.Coder3}, {"gin", cfg.Coder4},
	}
	for index, binding := range bindings {
		if index >= len(coders) || coders[index] == nil || coders[index].planning == nil || !binding.value.Enabled || strings.ToLower(strings.TrimSpace(binding.value.Name)) != binding.actor {
			continue
		}
		connected = append(connected, binding.actor+" (read-only proposal planning)")
	}
	if len(connected) == 0 {
		return ""
	}
	operations := "tasks.list_remaining, tasks.list_claimable, tasks.get, tasks.capabilities; tasks.add and tasks.claim require a writable admitted Task"
	if deps.taskCenter.NativeExecutionAvailable() {
		operations += "; tasks.execute requires a writable admitted Task"
	}
	return "Native TaskCenter planning is connected for " + strings.Join(connected, ", ") + ". Use `/tasks {\"operation\":\"...\",\"input\":{...}}` for " + operations + ". Read-only Tasks can only read; configured coders remain proposal-only."
}

type nativeTaskPlanningEnvelope struct {
	Operation string          `json:"operation"`
	Input     json.RawMessage `json:"input,omitempty"`
}

func (d *nativeTaskPlanningDispatcher) Dispatch(ctx context.Context, actorID, message string) (string, bool, error) {
	message = strings.TrimSpace(message)
	if message != strings.TrimSuffix(nativeTaskPlanningPrefix, " ") && !strings.HasPrefix(message, nativeTaskPlanningPrefix) && !strings.HasPrefix(message, strings.TrimSuffix(nativeTaskPlanningPrefix, " ")+"\t") {
		return "", false, nil
	}
	if d == nil || d.service == nil || d.tasks == nil {
		return "", true, errors.New("native TaskCenter planning is unavailable")
	}
	scope, ok := tool.ToolExecutionScopeFromContext(ctx)
	allowedParent := scope.ActorID == actorID || scope.ActorID == "shiro" && (actorID == "aka" || actorID == "ao" || actorID == "kin" || actorID == "gin")
	if !ok || scope.Validate() != nil || scope.ActorKind != tool.ActorKindAgent || !allowedParent || scope.AuthenticationSource != tool.AuthenticationSourceAgentOrchestrator {
		return "", true, errors.New("native TaskCenter planning requires the startup-bound Agent execution scope")
	}
	identity, err := domainexecution.IdentityFromContext(ctx)
	if err != nil {
		return "", true, fmt.Errorf("native TaskCenter planning requires an owner-issued Task/Run: %w", err)
	}
	var envelope nativeTaskPlanningEnvelope
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(strings.TrimPrefix(message, strings.TrimSuffix(nativeTaskPlanningPrefix, " ")))))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return "", true, errors.New("native TaskCenter envelope is invalid")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return "", true, errors.New("native TaskCenter envelope must contain one JSON object")
	}
	if envelope.Operation == "" || len(envelope.Input) > 8<<10 {
		return "", true, errors.New("native TaskCenter operation or input is invalid")
	}
	requestKey := modulecore.RequestID(scope.RequestID)
	child, childRun, _, created, err := d.tasks.EnsureNativePlanningInvocation(ctx, identity.TaskID, identity.RunID, scope.ActorID, actorID, requestKey, envelopeBytes(message))
	if err != nil {
		return "", true, fmt.Errorf("issue native TaskCenter planning context: %w", err)
	}
	if child.Status == domaintask.StatusSucceeded && childRun.Status == domaintask.RunStatusSucceeded {
		return child.Summary, true, nil
	}
	if !created {
		return "", true, errors.New("native TaskCenter planning invocation is not complete; owner reconciliation is required")
	}
	childCtx, err := taskmanager.BindNativePlanningInvocationContext(ctx, child, childRun)
	if err != nil {
		_ = d.tasks.FailNativePlanningInvocation(ctx, child.TaskID, childRun.RunID, identity.TaskID, identity.RunID, scope.ActorID, actorID, `{"error":"native_context_binding_failed"}`)
		return "", true, err
	}
	if _, err := d.tasks.ValidateNativePlanningParent(ctx, identity.TaskID, identity.RunID, scope.ActorID); err != nil {
		_ = d.tasks.FailNativePlanningInvocation(ctx, child.TaskID, childRun.RunID, identity.TaskID, identity.RunID, scope.ActorID, actorID, `{"error":"native_parent_admission_closed"}`)
		return "", true, fmt.Errorf("native TaskCenter parent admission is closed: %w", err)
	}
	readOnly := child.ReadOnly
	reject := func(err error) (string, bool, error) {
		_ = d.tasks.FailNativePlanningInvocation(ctx, child.TaskID, childRun.RunID, identity.TaskID, identity.RunID, scope.ActorID, actorID, `{"error":"native_planning_input_rejected"}`)
		return "", true, err
	}
	var result any
	switch envelope.Operation {
	case "tasks.list_remaining", "tasks.list_claimable":
		var input struct {
			Cursor string `json:"cursor,omitempty"`
			Limit  int    `json:"limit"`
		}
		if err := strictPlanningInput(envelope.Input, &input); err != nil || input.Limit < 1 || input.Limit > 10 {
			return reject(errors.New("TaskCenter list input is invalid"))
		}
		view := taskcenter.ViewRemaining
		query := taskcenter.ListQuery{View: view, Cursor: input.Cursor, Limit: input.Limit}
		if envelope.Operation == "tasks.list_claimable" {
			query.View, query.Assignee = taskcenter.ViewClaimable, "unassigned"
		}
		result, err = d.service.List(childCtx, query)
	case "tasks.get":
		var input struct {
			GoalID string `json:"goal_id"`
		}
		if err := strictPlanningInput(envelope.Input, &input); err != nil || strings.TrimSpace(input.GoalID) != input.GoalID || input.GoalID == "" {
			return reject(errors.New("TaskCenter get input is invalid"))
		}
		result, err = d.service.Get(childCtx, input.GoalID)
	case "tasks.capabilities":
		if len(envelope.Input) != 0 && !bytes.Equal(bytes.TrimSpace(envelope.Input), []byte("{}")) {
			return reject(errors.New("TaskCenter capabilities accepts no input"))
		}
		result, err = d.service.Capabilities(childCtx)
	case "tasks.add":
		if readOnly {
			return reject(errors.New("TaskCenter mutation denied for read-only Task"))
		}
		var input taskcenter.SubmitInput
		if err := strictPlanningInput(envelope.Input, &input); err != nil {
			return reject(errors.New("TaskCenter add input is invalid"))
		}
		if input.Assignee != "" || input.WorkstreamID != "" || input.DataScope != "" || len(input.AllowedAgents) != 0 {
			return reject(errors.New("TaskCenter add cannot set actor, scope, or allowlist"))
		}
		result, err = d.service.Submit(childCtx, input)
	case "tasks.claim":
		if readOnly {
			return reject(errors.New("TaskCenter mutation denied for read-only Task"))
		}
		var input taskcenter.ClaimInput
		if err := strictPlanningInput(envelope.Input, &input); err != nil {
			return reject(errors.New("TaskCenter claim input is invalid"))
		}
		result, err = d.service.Claim(childCtx, input)
	case "tasks.execute":
		if readOnly {
			return reject(errors.New("TaskCenter execution denied for read-only Task"))
		}
		if d.executor == nil {
			return reject(errors.New("TaskCenter execution coordinator is unavailable"))
		}
		var input taskcenter.ExecutionInput
		if err := strictPlanningInput(envelope.Input, &input); err != nil || input.GoalID == "" || input.ExpectedRevision == 0 || input.ExpectedPolicyRevision == "" || input.IdempotencyKey == "" {
			return reject(errors.New("TaskCenter execute input is invalid"))
		}
		childIdentity, identityErr := domainexecution.IdentityFromContext(childCtx)
		if identityErr != nil {
			return reject(errors.New("TaskCenter execution requires the issued planning Task/Run"))
		}
		input.CallerTaskID, input.CallerRunID = childIdentity.TaskID, childIdentity.RunID
		result, err = d.executor.ExecuteTaskCenterExecution(childCtx, input)
	default:
		err = errors.New("native TaskCenter operation is not supported")
	}
	if err != nil {
		_ = d.tasks.FailNativePlanningInvocation(ctx, child.TaskID, childRun.RunID, identity.TaskID, identity.RunID, scope.ActorID, actorID, `{"error":"native_planning_operation_failed"}`)
		return "", true, err
	}
	encoded, err := encodeNativeTaskPlanningResult(result)
	if err != nil {
		_ = d.tasks.FailNativePlanningInvocation(ctx, child.TaskID, childRun.RunID, identity.TaskID, identity.RunID, scope.ActorID, actorID, `{"error":"native_planning_result_encoding_failed"}`)
		return "", true, err
	}
	if err := d.tasks.CompleteNativePlanningInvocation(ctx, child.TaskID, childRun.RunID, identity.TaskID, identity.RunID, scope.ActorID, actorID, string(encoded)); err != nil {
		return "", true, err
	}
	return string(encoded), true, nil
}

func encodeNativeTaskPlanningResult(result any) ([]byte, error) {
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if len(encoded) <= 48<<10 {
		return encoded, nil
	}
	// A Goal can contain legacy text with no total byte ceiling. Claim/add must
	// not succeed in the owner and then fail because its copied Item exceeds the
	// canonical Task summary bound. Return the exact owner receipt reference
	// and same-scope tasks.get key instead of truncating the Item.
	mutation, ok := result.(taskcenter.MutationResult)
	if !ok || mutation.Item.GoalID == "" {
		return nil, errors.New("TaskCenter result exceeds the bounded response; read the Goal through tasks.get")
	}
	return json.Marshal(struct {
		ReceiptRef string `json:"receipt_ref,omitempty"`
		GoalID     string `json:"goal_id"`
		Revision   uint64 `json:"revision"`
		Status     string `json:"status"`
		Readback   string `json:"readback_operation"`
	}{mutation.ReceiptRef, mutation.Item.GoalID, mutation.Item.Revision, mutation.Item.Status, "tasks.get"})
}

func envelopeBytes(message string) []byte {
	return []byte(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(message), nativeTaskPlanningPrefix)))
}

func strictPlanningInput(raw json.RawMessage, target any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return errors.New("input is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("input must contain one JSON value")
	}
	return nil
}

var _ agent.NativePlanningPort = (*nativeTaskPlanningDispatcher)(nil)
