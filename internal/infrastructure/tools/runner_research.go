package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	coreresearch "github.com/Nyukimin/RenCrow_CORE/internal/application/research"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	contract "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	engine "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
)

const (
	researchRunToolName    = "research.run"
	researchStatusToolName = "research.status"
	researchRecallToolName = "research.recall"
	researchWatchToolName  = "research.watch"
	// ResearchPolicyRevision identifies this CORE adapter's compiled policy.
	// It is not the revision of the caller's overall Task policy.
	ResearchPolicyRevision = "core.research.pending-intake.v1"
)

type ResearchResultRecorder interface {
	Record(context.Context, contract.Envelope, contract.Result) error
}

type ResearchResultPreflighter interface {
	Preflight(context.Context, contract.Envelope) error
}

// ResearchResultStatusReconciler verifies the readonly Engine journal against
// its Task projection and the latest Task/Run closure before exposing status.
type ResearchResultStatusReconciler interface {
	Reconcile(context.Context, contract.Envelope, contract.Result) (contract.Result, error)
}

func (r *ToolRunner) WithResearchResultRecorder(recorder ResearchResultRecorder) *ToolRunner {
	if r != nil {
		r.researchResultRecorder = recorder
	}
	return r
}

// WithResearchEngine attaches the Research owner engine to an existing runner.
func (r *ToolRunner) WithResearchEngine(researchEngine *engine.Engine) *ToolRunner {
	if r == nil || researchEngine == nil {
		return r
	}
	r.registerResearchTools(researchEngine)
	return r
}

func (r *ToolRunner) registerResearchTools(researchEngine *engine.Engine) {
	r.toolsV2[researchRunToolName] = func(ctx context.Context, args map[string]any) (*tool.ToolResponse, error) {
		env, err := researchEnvelopeFromContext(ctx)
		if err != nil {
			return researchScopeError(err)
		}
		if researchEngine.PolicyRevision != "" {
			env.PolicyRevision = researchEngine.PolicyRevision
		}
		var input struct {
			Request json.RawMessage `json:"request"`
		}
		if err := decodeResearchToolArgs(args, &input); err != nil || len(input.Request) == 0 || bytes.Equal(bytes.TrimSpace(input.Request), []byte("null")) {
			return tool.NewError(tool.ErrValidationFailed, "research.run request is invalid", nil), nil
		}
		request, err := engine.DecodeRequest(input.Request)
		if err != nil {
			return tool.NewError(tool.ErrValidationFailed, "research.run request is invalid", nil), nil
		}
		if unsupported := researchUnsupportedSaveTargets(request); len(unsupported) > 0 {
			return tool.NewError(tool.ErrPermissionDenied, "Research save target is not connected", map[string]any{"unavailable_targets": unsupported}), nil
		}
		if r.researchResultRecorder == nil {
			return tool.NewError(tool.ErrNotFound, "Research Task result owner is unavailable", nil), nil
		}
		preflighter, ok := r.researchResultRecorder.(ResearchResultPreflighter)
		if !ok || preflighter.Preflight(ctx, env) != nil {
			return tool.NewError(tool.ErrNotFound, "Research Task result owner is unavailable", nil), nil
		}
		result, err := researchEngine.Run(ctx, env, request)
		if err != nil {
			return researchEngineError(err, result), nil
		}
		if err := r.researchResultRecorder.Record(ctx, env, result); err != nil {
			return tool.NewError(tool.ErrInternalError, "Research result Task projection is unavailable", map[string]any{"research_result": result}), nil
		}
		return researchResultResponse(result), nil
	}
	r.toolsV2[researchStatusToolName] = func(ctx context.Context, args map[string]any) (*tool.ToolResponse, error) {
		env, err := researchEnvelopeFromContext(ctx)
		if err != nil {
			return researchScopeError(err)
		}
		if researchEngine.PolicyRevision != "" {
			env.PolicyRevision = researchEngine.PolicyRevision
		}
		var input struct {
			RunID   string `json:"run_id"`
			WatchID string `json:"watch_id"`
		}
		if err := decodeResearchToolArgs(args, &input); err != nil || (input.RunID == "") == (input.WatchID == "") || strings.TrimSpace(input.RunID) != input.RunID || strings.TrimSpace(input.WatchID) != input.WatchID {
			return tool.NewError(tool.ErrValidationFailed, "research.status requires exactly one run_id or watch_id", nil), nil
		}
		if input.WatchID != "" {
			management, ok := researchEngine.Ports.Watch.(coreresearch.WatchManagementPort)
			if !ok {
				return tool.NewError(tool.ErrNotFound, "Research watch status owner is unavailable", nil), nil
			}
			item, readErr := management.Read(ctx, env, input.WatchID)
			expectedOwner := env.OwnerID
			if env.DataScope == "public" {
				expectedOwner = env.Actor
			}
			if readErr != nil || item.ValidateFor(expectedOwner, env.DataScope) != nil {
				return tool.NewError(tool.ErrNotFound, "Research watch status is unavailable", nil), nil
			}
			return tool.NewSuccess(item), nil
		}
		if strings.TrimSpace(input.RunID) == "" {
			return tool.NewError(tool.ErrValidationFailed, "research.status run_id is invalid", nil), nil
		}
		result, err := researchEngine.Status(ctx, env, input.RunID)
		if err != nil {
			return researchEngineError(err, result), nil
		}
		reconciler, ok := r.researchResultRecorder.(ResearchResultStatusReconciler)
		if !ok {
			return tool.NewError(tool.ErrNotFound, "Research Task result status owner is unavailable", nil), nil
		}
		result, err = reconciler.Reconcile(ctx, env, result)
		if err != nil {
			return tool.NewError(tool.ErrNotFound, "Research Task result status is unavailable", nil), nil
		}
		return researchResultResponse(result), nil
	}
	r.toolsV2[researchRecallToolName] = func(ctx context.Context, args map[string]any) (*tool.ToolResponse, error) {
		env, err := researchEnvelopeFromContext(ctx)
		if err != nil {
			return researchScopeError(err)
		}
		if researchEngine.PolicyRevision != "" {
			env.PolicyRevision = researchEngine.PolicyRevision
		}
		var input struct {
			Request json.RawMessage `json:"request"`
		}
		if err := decodeResearchToolArgs(args, &input); err != nil || len(input.Request) == 0 || bytes.Equal(bytes.TrimSpace(input.Request), []byte("null")) {
			return tool.NewError(tool.ErrValidationFailed, "research.recall request is invalid", nil), nil
		}
		var request contract.RecallRequest
		if err := decodeResearchRequest(input.Request, &request); err != nil {
			return tool.NewError(tool.ErrValidationFailed, "research.recall request is invalid", nil), nil
		}
		result, err := researchEngine.Recall(ctx, env, request)
		if err != nil {
			return researchEngineError(err, result), nil
		}
		if len(result.Missing) > 0 {
			return tool.NewError(tool.ErrNotFound, "one or more Research recall targets are unavailable", map[string]any{"recall_result": result}), nil
		}
		return tool.NewSuccess(result), nil
	}
	r.toolsV2[researchWatchToolName] = func(ctx context.Context, args map[string]any) (*tool.ToolResponse, error) {
		env, err := researchEnvelopeFromContext(ctx)
		if err != nil {
			return researchScopeError(err)
		}
		if researchEngine.PolicyRevision != "" {
			env.PolicyRevision = researchEngine.PolicyRevision
		}
		var input struct {
			Request json.RawMessage `json:"request"`
		}
		if err := decodeResearchToolArgs(args, &input); err != nil || len(input.Request) == 0 || bytes.Equal(bytes.TrimSpace(input.Request), []byte("null")) {
			return tool.NewError(tool.ErrValidationFailed, "research.watch request is invalid", nil), nil
		}
		var request contract.WatchRequest
		if err := decodeResearchRequest(input.Request, &request); err != nil {
			return tool.NewError(tool.ErrValidationFailed, "research.watch request is invalid", nil), nil
		}
		result, err := researchEngine.Watch(ctx, env, request)
		if err != nil {
			return researchEngineError(err, result), nil
		}
		return tool.NewSuccess(result), nil
	}

	r.metadata[researchRunToolName] = researchMetadata(researchRunToolName, "query", "Run bounded Research and retain results in pending intake.", researchRunSchema())
	r.metadata[researchStatusToolName] = researchMetadata(researchStatusToolName, "query", "Read Research run status under its owner scope.", researchStatusSchema())
	r.metadata[researchRecallToolName] = researchMetadata(researchRecallToolName, "query", "Recall evidence from connected Research targets.", researchRecallSchema())
	r.metadata[researchWatchToolName] = researchMetadata(researchWatchToolName, "mutation", "Register a source watch through the existing owner.", researchWatchSchema())
}

func researchUnsupportedSaveTargets(request contract.Request) []string {
	unsupported := []string{}
	for _, target := range request.Outputs.Save {
		switch target.Target {
		case "knowledge_staging", "artifact":
		default:
			unsupported = append(unsupported, target.Target)
		}
	}
	return unsupported
}

func researchMetadata(name, category, description string, schema map[string]any) tool.ToolMetadata {
	return tool.ToolMetadata{
		ToolID: name, Version: "1.0.0", Category: category, DryRun: false,
		Origin: tool.OriginCoreRuntime, Description: description,
		Invariants: []string{
			"execution envelope is built only from trusted CORE execution identity and ToolExecutionScope",
			"arguments cannot select task, run, trace, actor, owner, data scope, or policy revision",
			"unknown request fields and trailing JSON are rejected",
		},
		Parameters: schema,
	}
}

func researchEnvelopeFromContext(ctx context.Context) (contract.Envelope, error) {
	if ctx == nil {
		return contract.Envelope{}, errors.New("missing context")
	}
	identity, err := domainexecution.IdentityFromContext(ctx)
	if err != nil {
		return contract.Envelope{}, err
	}
	if err := identity.Validate(); err != nil || identity.TraceID == "" {
		return contract.Envelope{}, errors.New("trusted task, run, and trace identity are required")
	}
	scope, found := tool.ToolExecutionScopeFromContext(ctx)
	if !found {
		return contract.Envelope{}, errors.New("trusted tool execution scope is required")
	}
	if err := scope.Validate(); err != nil {
		return contract.Envelope{}, err
	}
	if scope.RequestID == "" || scope.ActorID == "" {
		return contract.Envelope{}, errors.New("authenticated scope actor is required")
	}
	dataScope, _, err := scope.SearchScope()
	if err != nil {
		return contract.Envelope{}, err
	}
	if dataScope != tool.DataScopePublic && dataScope != tool.DataScopeUser {
		return contract.Envelope{}, errors.New("unsupported research data scope")
	}
	ownerID := ""
	if dataScope == tool.DataScopeUser {
		if scope.AuthenticatedUserID == "" {
			return contract.Envelope{}, errors.New("authenticated user scope is required")
		}
		ownerID = scope.AuthenticatedUserID
	}
	return contract.Envelope{
		TaskID: string(identity.TaskID), RunID: string(identity.RunID), TraceID: string(identity.TraceID),
		RequestID: scope.RequestID, Actor: scope.ActorID,
		OwnerID: ownerID, DataScope: dataScope, PolicyRevision: ResearchPolicyRevision,
	}, nil
}

func researchScopeError(err error) (*tool.ToolResponse, error) {
	code := tool.ErrToolExecutionScopeInvalid
	if strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "missing") {
		code = tool.ErrToolExecutionScopeMissing
	}
	return tool.NewError(code, "trusted Research execution identity and scope are required", nil), nil
}

func decodeResearchToolArgs(args map[string]any, dst any) error {
	if args == nil {
		args = map[string]any{}
	}
	b, err := json.Marshal(args)
	if err != nil {
		return err
	}
	return decodeResearchRequest(b, dst)
}

func decodeResearchRequest(data []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}

func researchResultResponse(result contract.Result) *tool.ToolResponse {
	if result.Status == "failed" || result.Status == "partial" {
		message := "research run did not complete successfully"
		if result.Status == "partial" {
			message = "research run completed with missing required outputs"
		}
		return tool.NewError(tool.ErrInternalError, message, map[string]any{"research_result": result, "research_result_recorded": true})
	}
	return tool.NewSuccess(result)
}

func researchEngineError(err error, result any) *tool.ToolResponse {
	code := tool.ErrInternalError
	message := "Research owner is unavailable"
	value := strings.ToLower(err.Error())
	switch {
	case strings.Contains(value, "invalid_request"):
		code, message = tool.ErrValidationFailed, "Research request is invalid"
	case strings.Contains(value, "blocked_by_policy"):
		code, message = tool.ErrPermissionDenied, "Research operation is not authorized"
	case strings.Contains(value, "conflict"):
		code, message = tool.ErrConflict, "Research run conflicts with an existing run"
	case engine.IsNotFound(err), strings.Contains(value, "not found"), strings.Contains(value, "notexist"):
		code, message = tool.ErrNotFound, "Research run was not found"
	case strings.Contains(value, "timeout"), strings.Contains(value, "deadline"):
		code, message = tool.ErrTimeout, "Research operation timed out"
	}
	details := map[string]any{"research_result": result}
	return tool.NewError(code, message, details)
}

func researchRunSchema() map[string]any {
	return researchObjectSchema(map[string]any{
		"request": map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"schema_version": map[string]any{"type": "string", "const": contract.RequestSchema},
				"mode":           map[string]any{"type": "string", "enum": []any{"search_only", "collect", "investigate", "refresh"}},
				"watch_id":       map[string]any{"type": "string", "minLength": 8, "maxLength": 256, "pattern": `^rw_.{5,253}$`},
				"objective":      map[string]any{"type": "string", "maxLength": 2000},
				"query":          map[string]any{"type": "string"},
				"source_kinds":   researchStringArraySchema(), "seed_urls": researchStringArraySchema(),
				"provider": map[string]any{"type": "string"}, "retrieval_targets": researchStringArraySchema(),
				"document_refs": map[string]any{"type": "array", "minItems": 1, "maxItems": 20, "uniqueItems": true, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 512}},
				"repo_ref":      map[string]any{"type": "string", "minLength": 1, "maxLength": 512, "pattern": `^[A-Za-z0-9_.-]+$`, "not": map[string]any{"enum": []any{".", ".."}}},
				"revision":      map[string]any{"type": "string", "pattern": `^[0-9a-f]{40}$`},
				"time_window":   map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"since": map[string]any{"type": "string", "format": "date-time"}, "until": map[string]any{"type": "string", "format": "date-time"}}, "required": []any{"since", "until"}},
				"outputs": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
					"report":   map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"required": map[string]any{"type": "boolean"}, "format": map[string]any{"type": "string", "enum": []any{"", "markdown"}}}},
					"save":     map[string]any{"type": []any{"array", "null"}, "items": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"target": map[string]any{"type": "string", "enum": []any{"knowledge_staging", "news_owner", "domain_owner", "artifact", "board"}}, "required": map[string]any{"type": "boolean"}, "destination_ref": map[string]any{"type": "string"}}, "required": []any{"target", "required"}}},
					"evidence": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"min_documents": map[string]any{"type": "integer", "minimum": 0}, "min_independent_sources": map[string]any{"type": "integer", "minimum": 0}, "require_primary_for_material_claims": map[string]any{"type": "boolean"}, "required_source_kinds": researchStringArraySchema()}},
				}},
				"limits":   map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"search_calls": map[string]any{"type": "integer", "minimum": 0, "maximum": 6}, "candidates": map[string]any{"type": "integer", "minimum": 0, "maximum": 20}, "fetch_attempts": map[string]any{"type": "integer", "minimum": 0, "maximum": 12}, "max_documents": map[string]any{"type": "integer", "minimum": 0, "maximum": 8}, "deadline_seconds": map[string]any{"type": "integer", "minimum": 0, "maximum": 180}, "analysis_calls": map[string]any{"type": "integer", "minimum": 0, "maximum": 6}}},
				"language": map[string]any{"type": "string"}, "cost_profile": map[string]any{"type": "string", "enum": []any{"", "zero_incremental_cost"}},
			}, "required": []any{"schema_version", "mode"},
			"allOf": []any{
				map[string]any{
					"if":   map[string]any{"required": []any{"document_refs"}},
					"then": map[string]any{"properties": map[string]any{"mode": map[string]any{"enum": []any{"collect", "investigate"}}, "retrieval_targets": map[string]any{"type": "array", "contains": map[string]any{"const": "documents"}}}, "required": []any{"retrieval_targets"}},
				},
				map[string]any{
					"if":   map[string]any{"required": []any{"repo_ref"}},
					"then": map[string]any{"properties": map[string]any{"retrieval_targets": map[string]any{"type": "array", "contains": map[string]any{"const": "code"}}}, "required": []any{"revision", "retrieval_targets"}},
				},
				map[string]any{
					"if":   map[string]any{"required": []any{"revision"}},
					"then": map[string]any{"properties": map[string]any{"retrieval_targets": map[string]any{"type": "array", "contains": map[string]any{"const": "code"}}}, "required": []any{"repo_ref", "retrieval_targets"}},
				},
			},
			"oneOf": []any{
				map[string]any{"properties": map[string]any{"mode": map[string]any{"enum": []any{"search_only", "collect", "investigate"}}, "objective": map[string]any{"type": "string", "minLength": 1, "maxLength": 2000}}, "required": []any{"objective", "cost_profile"}},
				map[string]any{"properties": map[string]any{"mode": map[string]any{"const": "refresh"}}, "required": []any{"watch_id"}},
			},
		},
	}, []any{"request"})
}

func researchStatusSchema() map[string]any {
	schema := researchObjectSchema(map[string]any{
		"run_id":   map[string]any{"type": "string", "minLength": 1},
		"watch_id": map[string]any{"type": "string", "minLength": 8, "maxLength": 256, "pattern": `^rw_[A-Za-z0-9_-]{5,253}$`},
	}, nil)
	schema["oneOf"] = []any{
		map[string]any{"required": []any{"run_id"}, "not": map[string]any{"required": []any{"watch_id"}}},
		map[string]any{"required": []any{"watch_id"}, "not": map[string]any{"required": []any{"run_id"}}},
	}
	return schema
}

func researchRecallSchema() map[string]any {
	return researchObjectSchema(map[string]any{"request": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
		"query": map[string]any{"type": "string", "minLength": 1}, "targets": researchStringArraySchema(), "document_refs": researchStringArraySchema(),
		"repo_ref": map[string]any{"type": "string"}, "revision": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer", "minimum": 0, "maximum": 20},
		"time_window": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"since": map[string]any{"type": "string", "format": "date-time"}, "until": map[string]any{"type": "string", "format": "date-time"}}, "required": []any{"since", "until"}},
	}, "required": []any{"query"}}}, []any{"request"})
}

func researchWatchSchema() map[string]any {
	return researchObjectSchema(map[string]any{"request": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
		"objective": map[string]any{"type": "string", "minLength": 1}, "source_refs": researchStringArraySchema(), "source_kinds": researchStringArraySchema(),
		"cadence": map[string]any{"type": "string", "minLength": 1}, "initial_run": map[string]any{"type": "boolean"},
		"output_contract": researchOutputsSchema(),
	}, "required": []any{"objective", "source_refs", "source_kinds", "cadence", "output_contract", "initial_run"}}}, []any{"request"})
}

func researchObjectSchema(properties map[string]any, required []any) map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": required}
}

func researchStringArraySchema() map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
}

func researchOutputsSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
		"report": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
			"required": map[string]any{"type": "boolean"}, "format": map[string]any{"type": "string", "enum": []any{"markdown"}},
		}},
		"save": map[string]any{"type": "array", "items": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
			"target":   map[string]any{"type": "string", "enum": []any{"knowledge_staging", "news_owner", "domain_owner", "artifact", "board"}},
			"required": map[string]any{"type": "boolean"}, "destination_ref": map[string]any{"type": "string"},
		}, "required": []any{"target", "required"}}},
		"evidence": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
			"min_documents":                       map[string]any{"type": "integer", "minimum": 0},
			"min_independent_sources":             map[string]any{"type": "integer", "minimum": 0},
			"require_primary_for_material_claims": map[string]any{"type": "boolean"},
			"required_source_kinds":               researchStringArraySchema(),
		}},
	}}
}
