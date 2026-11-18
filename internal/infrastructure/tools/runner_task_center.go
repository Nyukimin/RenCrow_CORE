package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskcenter"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

// TaskCenterService is the application boundary used by TaskCenter Tools.
type TaskCenterService interface {
	Submit(context.Context, taskcenter.SubmitInput) (taskcenter.MutationResult, error)
	Claim(context.Context, taskcenter.ClaimInput) (taskcenter.MutationResult, error)
	List(context.Context, taskcenter.ListQuery) (taskcenter.ListResult, error)
	Get(context.Context, string) (taskcenter.Item, error)
	Capabilities(context.Context) (taskcenter.Capabilities, error)
}

const (
	taskCenterListRemainingTool = "tasks.list_remaining"
	taskCenterListClaimableTool = "tasks.list_claimable"
	taskCenterGetTool           = "tasks.get"
	taskCenterCapabilitiesTool  = "tasks.capabilities"
	taskCenterAddTool           = "tasks.add"
	taskCenterClaimTool         = "tasks.claim"
	taskCenterExecuteTool       = "tasks.execute"
)

// WithTaskCenterService attaches the same application service to an existing
// startup-created runner. Chat receives only read tools; Worker can mutate.
func (r *ToolRunner) WithTaskCenterService(service TaskCenterService, includeMutations bool) *ToolRunner {
	if r == nil || service == nil {
		return r
	}
	r.registerTaskCenterReadTools(service)
	if includeMutations {
		r.registerTaskCenterMutationTools(service)
		if executionService, ok := service.(taskcenter.TaskCenterExecutionCoordinator); ok {
			r.registerTaskCenterExecutionTool(executionService)
		}
	}
	return r
}

func (r *ToolRunner) registerTaskCenterExecutionTool(service taskcenter.TaskCenterExecutionCoordinator) {
	r.toolsV2[taskCenterExecuteTool] = func(ctx context.Context, args map[string]any) (*tool.ToolResponse, error) {
		scope, err := taskCenterToolScope(ctx)
		if err != nil {
			return taskCenterToolScopeError(err), nil
		}
		if scope.ActorKind != tool.ActorKindAgent {
			return taskCenterToolError(tool.ErrPermissionDenied, "Only an authenticated Agent can execute its assigned TaskCenter Goal"), nil
		}
		identity, err := domainexecution.IdentityFromContext(ctx)
		if err != nil {
			return taskCenterToolError(tool.ErrPermissionDenied, "Trusted parent Task execution identity is required"), nil
		}
		var input taskcenter.ExecutionInput
		if err := decodeTaskCenterToolInput(args, &input); err != nil {
			return taskCenterToolError(tool.ErrValidationFailed, "TaskCenter execute input is invalid"), nil
		}
		input.CallerTaskID, input.CallerRunID = identity.TaskID, identity.RunID
		result, err := service.ExecuteTaskCenterExecution(ctx, input)
		if err != nil {
			return taskCenterToolServiceError(err), nil
		}
		return tool.NewSuccess(result), nil
	}
	r.metadata[taskCenterExecuteTool] = taskCenterMutationMetadata(taskCenterExecuteTool,
		"Execute only the current Agent's already prepared and assigned typed TaskCenter Goal.", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"goal_id":                   map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
				"expected_revision":         map[string]any{"type": "integer", "minimum": 1},
				"expected_assignment_epoch": map[string]any{"type": "integer", "minimum": 0},
				"expected_policy_revision":  map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
				"idempotency_key":           map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
			},
			"required":             []any{"goal_id", "expected_revision", "expected_assignment_epoch", "expected_policy_revision", "idempotency_key"},
			"additionalProperties": false,
		})
}

func (r *ToolRunner) registerTaskCenterReadTools(service TaskCenterService) {
	r.toolsV2[taskCenterListRemainingTool] = func(ctx context.Context, args map[string]any) (*tool.ToolResponse, error) {
		if _, err := taskCenterToolScope(ctx); err != nil {
			return taskCenterToolScopeError(err), nil
		}
		query, err := taskCenterListToolQuery(args, taskcenter.ViewRemaining)
		if err != nil {
			return taskCenterToolError(tool.ErrValidationFailed, "TaskCenter list input is invalid"), nil
		}
		result, err := service.List(ctx, query)
		if err != nil {
			return taskCenterToolServiceError(err), nil
		}
		return tool.NewSuccess(result), nil
	}
	r.toolsV2[taskCenterListClaimableTool] = func(ctx context.Context, args map[string]any) (*tool.ToolResponse, error) {
		if _, err := taskCenterToolScope(ctx); err != nil {
			return taskCenterToolScopeError(err), nil
		}
		query, err := taskCenterListToolQuery(args, taskcenter.ViewClaimable)
		if err != nil {
			return taskCenterToolError(tool.ErrValidationFailed, "TaskCenter list input is invalid"), nil
		}
		query.Assignee = "unassigned"
		result, err := service.List(ctx, query)
		if err != nil {
			return taskCenterToolServiceError(err), nil
		}
		return tool.NewSuccess(result), nil
	}
	r.toolsV2[taskCenterGetTool] = func(ctx context.Context, args map[string]any) (*tool.ToolResponse, error) {
		if _, err := taskCenterToolScope(ctx); err != nil {
			return taskCenterToolScopeError(err), nil
		}
		var input struct {
			GoalID string `json:"goal_id"`
		}
		if err := decodeTaskCenterToolInput(args, &input); err != nil || input.GoalID == "" ||
			len(input.GoalID) > 256 || strings.TrimSpace(input.GoalID) != input.GoalID {
			return taskCenterToolError(tool.ErrValidationFailed, "TaskCenter get input is invalid"), nil
		}
		result, err := service.Get(ctx, input.GoalID)
		if err != nil {
			return taskCenterToolServiceError(err), nil
		}
		return tool.NewSuccess(result), nil
	}
	r.toolsV2[taskCenterCapabilitiesTool] = func(ctx context.Context, args map[string]any) (*tool.ToolResponse, error) {
		if !taskCenterNoToolArguments(args) {
			return taskCenterToolError(tool.ErrValidationFailed, "TaskCenter capabilities does not accept arguments"), nil
		}
		if _, err := taskCenterToolScope(ctx); err != nil {
			return taskCenterToolScopeError(err), nil
		}
		result, err := service.Capabilities(ctx)
		if err != nil {
			return taskCenterToolServiceError(err), nil
		}
		return tool.NewSuccess(result), nil
	}
	r.metadata[taskCenterListRemainingTool] = taskCenterListMetadata(taskCenterListRemainingTool, "List remaining tasks visible to the trusted execution scope.")
	r.metadata[taskCenterListClaimableTool] = taskCenterListMetadata(taskCenterListClaimableTool, "List unassigned tasks claimable by the trusted Agent scope.")
	r.metadata[taskCenterGetTool] = taskCenterGetMetadata()
	r.metadata[taskCenterCapabilitiesTool] = taskCenterReadMetadata(taskCenterCapabilitiesTool, "Read TaskCenter task kinds, registered Agents, and policy revision.")
}

func taskCenterGetMetadata() tool.ToolMetadata {
	metadata := taskCenterReadMetadata(taskCenterGetTool, "Read full details for one visible task by Goal ID.")
	metadata.Parameters = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"goal_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
		},
		"required":             []any{"goal_id"},
		"additionalProperties": false,
	}
	return metadata
}

func (r *ToolRunner) registerTaskCenterMutationTools(service TaskCenterService) {
	r.toolsV2[taskCenterAddTool] = func(ctx context.Context, args map[string]any) (*tool.ToolResponse, error) {
		if _, err := taskCenterToolScope(ctx); err != nil {
			return taskCenterToolScopeError(err), nil
		}
		var input taskcenter.SubmitInput
		if err := decodeTaskCenterToolInput(args, &input); err != nil {
			return taskCenterToolError(tool.ErrValidationFailed, "TaskCenter add input is invalid"), nil
		}
		result, err := service.Submit(ctx, input)
		if err != nil {
			return taskCenterToolServiceError(err), nil
		}
		return tool.NewSuccess(result), nil
	}
	r.toolsV2[taskCenterClaimTool] = func(ctx context.Context, args map[string]any) (*tool.ToolResponse, error) {
		scope, err := taskCenterToolScope(ctx)
		if err != nil {
			return taskCenterToolScopeError(err), nil
		}
		if scope.ActorKind != tool.ActorKindAgent {
			return taskCenterToolError(tool.ErrPermissionDenied, "Only an authenticated Agent can claim a task"), nil
		}
		var input taskcenter.ClaimInput
		if err := decodeTaskCenterToolInput(args, &input); err != nil {
			return taskCenterToolError(tool.ErrValidationFailed, "TaskCenter claim input is invalid"), nil
		}
		result, err := service.Claim(ctx, input)
		if err != nil {
			return taskCenterToolServiceError(err), nil
		}
		return tool.NewSuccess(result), nil
	}
	r.metadata[taskCenterAddTool] = taskCenterMutationMetadata(taskCenterAddTool, "Submit a task plan for a registered task kind.", taskCenterSubmitSchema())
	r.metadata[taskCenterClaimTool] = taskCenterMutationMetadata(taskCenterClaimTool, "Claim an unassigned task using the trusted Agent identity.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"goal_id":                  map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
			"expected_revision":        map[string]any{"type": "integer", "minimum": 1},
			"expected_policy_revision": map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
			"idempotency_key":          map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
		},
		"required":             []any{"goal_id", "expected_revision", "expected_policy_revision", "idempotency_key"},
		"additionalProperties": false,
	})
}

func taskCenterReadMetadata(id, description string) tool.ToolMetadata {
	return tool.ToolMetadata{
		ToolID: id, Version: "1.0.0", Category: "query", Origin: tool.OriginCoreRuntime,
		Description: description,
		Invariants:  []string{"requires_valid_trusted_tool_execution_scope", "identity_is_never_taken_from_arguments"},
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
	}
}

func taskCenterListMetadata(id, description string) tool.ToolMetadata {
	metadata := taskCenterReadMetadata(id, description)
	metadata.Parameters = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 200},
			"cursor": map[string]any{"type": "string", "maxLength": 2048},
		},
		"additionalProperties": false,
	}
	return metadata
}

func taskCenterMutationMetadata(id, description string, schema map[string]any) tool.ToolMetadata {
	return tool.ToolMetadata{
		ToolID: id, Version: "1.0.0", Category: "mutation", Origin: tool.OriginCoreRuntime,
		Description: description,
		Invariants:  []string{"requires_valid_trusted_tool_execution_scope", "identity_is_never_taken_from_arguments", "idempotency_key_is_required"},
		Parameters:  schema,
	}
}

func taskCenterSubmitSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"workstream_id": map[string]any{"type": "string", "maxLength": 256},
			"data_scope":    map[string]any{"type": "string", "enum": []any{"public", "user"}},
			"kind_id":       map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
			"kind_input": map[string]any{
				"type":                 "object",
				"description":          "Use the selected kind's input_schema from tasks.capabilities; the kind owner validates and canonicalizes this object.",
				"additionalProperties": true,
			},
			"title":                    map[string]any{"type": "string", "minLength": 1, "maxLength": 512},
			"description":              map[string]any{"type": "string", "maxLength": 8192},
			"success_criteria":         map[string]any{"type": "array", "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 2048}, "minItems": 1, "maxItems": 32},
			"verification":             map[string]any{"type": "array", "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 2048}, "minItems": 1, "maxItems": 32},
			"assignee":                 map[string]any{"type": "string", "maxLength": 128},
			"timing":                   taskCenterTimingSchema(),
			"allowed_agents":           map[string]any{"type": "array", "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}, "minItems": 1, "maxItems": 8},
			"idempotency_key":          map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
			"expected_policy_revision": map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
		},
		"required":             []any{"kind_id", "title", "success_criteria", "verification", "timing", "idempotency_key", "expected_policy_revision"},
		"additionalProperties": false,
	}
}

func taskCenterTimingSchema() map[string]any {
	dateTimeOrNull := map[string]any{"type": []any{"string", "null"}, "format": "date-time"}
	integerOrNull := map[string]any{"type": []any{"integer", "null"}}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"timezone":                   map[string]any{"type": "string"},
			"scheduled_start_at":         dateTimeOrNull,
			"not_before":                 dateTimeOrNull,
			"allowed_windows":            map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"start": map[string]any{"type": "string", "pattern": "^([01][0-9]|2[0-3]):[0-5][0-9]$"}, "end": map[string]any{"type": "string", "pattern": "^([01][0-9]|2[0-3]):[0-5][0-9]$"}}, "required": []any{"start", "end"}, "additionalProperties": false}},
			"deadline_at":                dateTimeOrNull,
			"recurrence":                 taskCenterRecurrenceSchema(),
			"estimated_duration_seconds": integerOrNull,
		},
		"required":             []any{"timezone", "scheduled_start_at", "not_before", "allowed_windows", "deadline_at", "recurrence", "estimated_duration_seconds"},
		"additionalProperties": false,
	}
}

func taskCenterRecurrenceSchema() map[string]any {
	return map[string]any{
		"type": []any{"object", "null"},
		"properties": map[string]any{
			"frequency":        map[string]any{"type": "string", "enum": []any{"none", "daily", "weekly", "interval"}},
			"times":            map[string]any{"type": "array", "items": map[string]any{"type": "string", "pattern": "^([01][0-9]|2[0-3]):[0-5][0-9]$"}},
			"weekdays":         map[string]any{"type": "array", "items": map[string]any{"type": "integer", "minimum": 0, "maximum": 6}},
			"interval_seconds": map[string]any{"type": []any{"integer", "null"}, "minimum": 1},
		},
		"required":             []any{"frequency", "times", "weekdays", "interval_seconds"},
		"additionalProperties": false,
	}
}

func taskCenterToolScope(ctx context.Context) (tool.ToolExecutionScope, error) {
	scope, found := tool.ToolExecutionScopeFromContext(ctx)
	if !found {
		return tool.ToolExecutionScope{}, errors.New("scope missing")
	}
	if err := scope.Validate(); err != nil {
		return tool.ToolExecutionScope{}, fmt.Errorf("scope invalid: %w", err)
	}
	return scope, nil
}

func taskCenterToolScopeError(err error) *tool.ToolResponse {
	if strings.HasPrefix(err.Error(), "scope missing") {
		return taskCenterToolError(tool.ErrToolExecutionScopeMissing, "Trusted TaskCenter execution scope is required")
	}
	return taskCenterToolError(tool.ErrToolExecutionScopeInvalid, "Trusted TaskCenter execution scope is invalid")
}

func taskCenterNoToolArguments(args map[string]any) bool {
	return len(args) == 0
}

func taskCenterListToolQuery(args map[string]any, view string) (taskcenter.ListQuery, error) {
	if args == nil {
		args = map[string]any{}
	}
	var input struct {
		Limit  int    `json:"limit"`
		Cursor string `json:"cursor"`
	}
	if err := decodeTaskCenterToolInput(args, &input); err != nil {
		return taskcenter.ListQuery{}, err
	}
	if input.Limit == 0 {
		input.Limit = 50
	}
	if input.Limit < 1 || input.Limit > 200 || len(input.Cursor) > 2048 || strings.TrimSpace(input.Cursor) != input.Cursor {
		return taskcenter.ListQuery{}, errors.New("invalid list query")
	}
	return taskcenter.ListQuery{View: view, Limit: input.Limit, Cursor: input.Cursor}, nil
}

func decodeTaskCenterToolInput(args map[string]any, destination any) error {
	raw, err := json.Marshal(args)
	if err != nil || len(raw) == 0 || bytes.TrimSpace(raw)[0] != '{' {
		return errors.New("input must be an object")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("input must contain one object")
}

func taskCenterToolServiceError(err error) *tool.ToolResponse {
	switch {
	case errors.Is(err, taskcenter.ErrInvalid):
		return taskCenterToolError(tool.ErrValidationFailed, "TaskCenter input is invalid")
	case errors.Is(err, taskcenter.ErrForbidden):
		return taskCenterToolError(tool.ErrPermissionDenied, "TaskCenter operation is not permitted")
	case errors.Is(err, taskcenter.ErrNotFound):
		return taskCenterToolError(tool.ErrNotFound, "TaskCenter item was not found")
	case errors.Is(err, taskcenter.ErrConflict):
		return taskCenterToolError(tool.ErrConflict, "TaskCenter state changed; refresh and retry")
	default:
		return taskCenterToolError(tool.ErrInternalError, "TaskCenter is unavailable")
	}
}

func taskCenterToolError(code tool.ErrorCode, message string) *tool.ToolResponse {
	return tool.NewError(code, message, nil)
}
