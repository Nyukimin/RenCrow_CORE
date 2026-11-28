package toolloop

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	coreresearch "github.com/Nyukimin/RenCrow_CORE/internal/application/research"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
	contract "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	engine "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
)

const researchRunTool = "research.run"

var (
	researchExplicitMode       = regexp.MustCompile(`(?i)(?:^|\s)mode\s*[:=]\s*(search_only|collect|investigate)\b`)
	researchExplicitURLs       = regexp.MustCompile(`https?://[^\s<>"']+`)
	researchRequiredConditions = regexp.MustCompile(`(?i)\b(board|db|database|youtube|instagram|tiktok|reddit|github|arxiv|huggingface)\b|Xリサーチ|Xの|直近[0-9０-９]+|過去[0-9０-９]+|[0-9]{4}-[0-9]{2}-[0-9]{2}|今朝|昨日|今日|最新|保存先|登録先`)
)

func researchRunCapabilityRegistered(metadata []tool.ToolMetadata, allowed []string) bool {
	for _, item := range metadata {
		if item.ToolID == researchRunTool && item.Category == "query" && !item.Deprecated && len(item.Parameters) > 0 && webQueryAllowed(researchRunTool, allowed) {
			return true
		}
	}
	return false
}

func (r *WebResearcher) gatherResearchEngine(ctx context.Context, request llm.GenerateRequest) ([]llm.Message, error) {
	objective := researchObjective(request.Messages)
	if objective == "" {
		return nil, fmt.Errorf("research.run requires a user request")
	}
	versionedRequest := IsVersionedResearchRequest(objective)
	researchRequest, err := researchRequestFromUser(objective)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(researchRequest)
	if err != nil {
		return nil, fmt.Errorf("encode research.run request")
	}
	var requestObject map[string]any
	if err := json.Unmarshal(encoded, &requestObject); err != nil {
		return nil, fmt.Errorf("encode research.run request")
	}
	args := map[string]any{"request": requestObject}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	execCtx := ctx
	var cfg Config
	if r.actions != nil {
		identity, err := researchLoopIdentity(ctx)
		if err != nil {
			return nil, err
		}
		cfg = Config{TaskID: identity.TaskID, RunID: identity.RunID, Actions: r.actions}
		execCtx, err = bindToolActionAttempt(ctx, cfg, researchRunTool)
		if err != nil {
			return nil, fmt.Errorf("bind research.run Tool attempt: %w", err)
		}
	}
	response, runErr := r.runner.ExecuteV2(execCtx, researchRunTool, args)
	result, typedResult := researchTypedResult(response)
	var projection *domaintask.ResearchResultProjection
	if versionedRequest && typedResult && result.Status == "succeeded" && response.Error == nil {
		recorded, projectionErr := recordedResearchProjection(ctx, response, result)
		if projectionErr != nil {
			response = tool.NewError(tool.ErrInternalError, "Research Task completion facts are unavailable", nil)
		} else {
			projection = &recorded
		}
	}
	if runErr == nil && (response == nil || response.Error == nil) && (!typedResult || result.Status == "partial" || result.Status == "failed") {
		details := map[string]any{}
		message := "research.run returned no valid completed result"
		if typedResult {
			details["research_result"] = result
			message = "research.run returned a non-success result"
		}
		response = tool.NewError(tool.ErrInternalError, message, details)
	}
	if r.actions != nil {
		actionID, attemptID, bound := boundResearchAttempt(execCtx)
		if !bound {
			return nil, fmt.Errorf("research.run Tool attempt binding is unavailable")
		}
		if err := r.actions.CompleteToolAttempt(execCtx, actionID, attemptID, response, runErr); err != nil {
			return nil, fmt.Errorf("complete research.run Tool attempt: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if runErr != nil {
		return nil, fmt.Errorf("research.run could not complete")
	}
	if !typedResult {
		if response != nil && response.Error != nil {
			return nil, fmt.Errorf("research.run failed: %s", safeResearchToolError(response.Error.Code))
		}
		return nil, fmt.Errorf("research.run returned no typed result")
	}
	if response.Error != nil && result.Status != "partial" && result.Status != "failed" {
		return nil, fmt.Errorf("research.run failed: %s", safeResearchToolError(response.Error.Code))
	}
	data, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encode typed research result")
	}
	metadata := map[string]string{"runtime_context_kind": "web_research_evidence", "research_status": result.Status}
	if versionedRequest {
		metadata["research_request_schema"] = contract.RequestSchema
		metadata["research_mode"] = researchRequest.Mode
		if projection != nil {
			metadata["research_task_summary"] = projection.TaskSummary()
		}
	}
	return []llm.Message{{
		Role: "user", Type: llm.PromptContextVariable,
		Metadata: metadata,
		Content:  string(data),
	}}, nil
}

func recordedResearchProjection(ctx context.Context, response *tool.ToolResponse, result contract.Result) (domaintask.ResearchResultProjection, error) {
	if response == nil || response.Metadata == nil {
		return domaintask.ResearchResultProjection{}, fmt.Errorf("Research Task projection metadata is missing")
	}
	projection, ok := response.Metadata["research_result_projection"].(domaintask.ResearchResultProjection)
	if !ok {
		return domaintask.ResearchResultProjection{}, fmt.Errorf("Research Task projection metadata has an invalid type")
	}
	artifactRef, ok := response.Metadata["research_result_artifact_ref"].(string)
	if !ok || artifactRef == "" || artifactRef != projection.ArtifactRef {
		return domaintask.ResearchResultProjection{}, fmt.Errorf("Research Task projection CAS reference is missing or changed")
	}
	identity, err := researchLoopIdentity(ctx)
	if err != nil {
		return domaintask.ResearchResultProjection{}, err
	}
	scope, ok := tool.ToolExecutionScopeFromContext(ctx)
	if !ok || scope.Validate() != nil {
		return domaintask.ResearchResultProjection{}, fmt.Errorf("Research Task projection scope is unavailable")
	}
	dataScope, ownerID, err := scope.SearchScope()
	if err != nil {
		return domaintask.ResearchResultProjection{}, err
	}
	env := contract.Envelope{
		TaskID: string(identity.TaskID), RunID: string(identity.RunID), TraceID: string(identity.TraceID),
		RequestID: scope.RequestID, Actor: scope.ActorID, OwnerID: ownerID, DataScope: dataScope,
	}
	if err := coreresearch.ValidateResearchResultProjection(env, result, projection, artifactRef, projection.RequiredReceiptsComplete); err != nil {
		return domaintask.ResearchResultProjection{}, fmt.Errorf("Research Task projection differs: %w", err)
	}
	return projection, nil
}

func researchLoopIdentity(ctx context.Context) (identity domainexecution.Identity, err error) {
	identity, err = domainexecution.IdentityFromContext(ctx)
	if err != nil {
		return identity, fmt.Errorf("research.run execution identity is required")
	}
	if err := identity.Validate(); err != nil || identity.TraceID == "" {
		return identity, fmt.Errorf("research.run task, run, and trace identity are required")
	}
	return identity, nil
}

func boundResearchAttempt(ctx context.Context) (modulecore.ActionID, modulecore.AttemptID, bool) {
	return domainexecution.BoundActionAttemptFromContext(ctx)
}

func researchTypedResult(response *tool.ToolResponse) (contract.Result, bool) {
	if response == nil {
		return contract.Result{}, false
	}
	var value any = response.Result
	if response.Error != nil {
		value = response.Error.Details["research_result"]
	}
	result, ok := value.(contract.Result)
	if !ok {
		if pointer, pointerOK := value.(*contract.Result); pointerOK && pointer != nil {
			result, ok = *pointer, true
		}
	}
	if !ok || result.SchemaVersion != contract.ResultSchema || result.RunID == "" || !researchRunStatus(result.Status) {
		return contract.Result{}, false
	}
	return result, true
}

func researchRunStatus(status string) bool {
	switch status {
	case "accepted", "running", "waiting_input", "succeeded", "partial", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func safeResearchToolError(code tool.ErrorCode) string {
	switch code {
	case tool.ErrValidationFailed:
		return "request_invalid"
	case tool.ErrPermissionDenied:
		return "not_authorized"
	case tool.ErrNotFound:
		return "run_not_found"
	case tool.ErrConflict:
		return "run_conflict"
	case tool.ErrTimeout:
		return "timed_out"
	default:
		return "owner_unavailable"
	}
}

func researchObjective(messages []llm.Message) string {
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if !strings.EqualFold(strings.TrimSpace(message.Role), "user") || message.Type == llm.PromptContextRecall || message.Type == llm.PromptContextVariable {
			continue
		}
		objective := strings.TrimSpace(message.Content)
		if objective == "" {
			continue
		}
		return objective
	}
	return ""
}

// This entry handles a complete versioned contract or a simple Web objective.
// Conditions that need typed source/time/destination fields must be supplied
// through the existing structured Tool/CLI, rather than silently discarded.
func researchRequestFromUser(objective string) (contract.Request, error) {
	if structured, ok := structuredResearchRequestPayload(objective); ok {
		return engine.DecodeRequest([]byte(structured))
	}
	raw := strings.TrimSpace(objective)
	if utf8.RuneCountInString(raw) > 2000 {
		return contract.Request{}, fmt.Errorf("research objective exceeds 2000 characters; provide a bounded structured Research request")
	}
	if researchRequiredConditions.MatchString(raw) {
		return contract.Request{}, fmt.Errorf("source, time or destination conditions require a structured Research request; no reduced-scope research was started")
	}
	return engine.Normalize(contract.Request{SchemaVersion: contract.RequestSchema, Mode: researchMode(raw), Objective: raw, SourceKinds: []string{"web"}, SeedURLs: researchSeedURLs(raw), CostProfile: "zero_incremental_cost"})
}

// IsVersionedResearchRequest recognizes only the existing envelope identifier.
// Full field validation remains owned by NormalizeResearchRequest.
func IsVersionedResearchRequest(objective string) bool {
	structured, ok := structuredResearchRequestPayload(objective)
	if !ok {
		return false
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(structured), &envelope); err != nil {
		return false
	}
	var schemaVersion string
	if err := json.Unmarshal(envelope["schema_version"], &schemaVersion); err != nil {
		return false
	}
	return schemaVersion == contract.RequestSchema
}

func structuredResearchRequestPayload(objective string) (string, bool) {
	raw := strings.TrimSpace(objective)
	if strings.HasPrefix(raw, "```json\n") && strings.HasSuffix(raw, "\n```") {
		raw = strings.TrimSuffix(strings.TrimPrefix(raw, "```json\n"), "\n```")
	}
	return raw, strings.HasPrefix(raw, "{")
}

// NormalizeResearchRequest is the shared normalization entry for typed
// Research callers outside the legacy evidence ToolLoop.
func NormalizeResearchRequest(objective string) (contract.Request, error) {
	return researchRequestFromUser(objective)
}

func researchMode(objective string) string {
	if match := researchExplicitMode.FindStringSubmatch(objective); len(match) == 2 {
		return strings.ToLower(match[1])
	}
	lower := strings.ToLower(objective)
	if strings.Contains(lower, "search_only") || strings.Contains(objective, "検索のみ") || strings.Contains(objective, "検索だけ") {
		return "search_only"
	}
	if strings.Contains(lower, "collect only") || strings.Contains(objective, "収集のみ") || strings.Contains(objective, "収集だけ") {
		return "collect"
	}
	return "investigate"
}

func researchSeedURLs(objective string) []string {
	seeds := make([]string, 0, 4)
	seen := make(map[string]struct{})
	for _, candidate := range researchExplicitURLs.FindAllString(objective, 20) {
		candidate = strings.TrimRight(candidate, ".,;:!?)]}。、，；：！？」』")
		normalized, err := modulewebgather.NormalizeURL(candidate, false)
		if err != nil {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		seeds = append(seeds, normalized)
	}
	return seeds
}
