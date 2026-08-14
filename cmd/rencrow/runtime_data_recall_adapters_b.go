package main

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	appstore "github.com/Nyukimin/RenCrow_CORE/internal/application/durablestore"
	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	domainbrowser "github.com/Nyukimin/RenCrow_CORE/internal/domain/browsertrace"
	domaincomplexity "github.com/Nyukimin/RenCrow_CORE/internal/domain/complexity"
	domaindurable "github.com/Nyukimin/RenCrow_CORE/internal/domain/durablestore"
	domainpersona "github.com/Nyukimin/RenCrow_CORE/internal/domain/persona"
	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

type runtimePersonaObservationExactFinder interface {
	FindObservationLogByID(context.Context, string) (domainpersona.ObservationLog, bool, error)
}

type runtimeBrowserTraceValidationExactFinder interface {
	FindAPICandidateValidationResultByID(context.Context, string) (domainbrowser.APICandidateValidationResult, bool, error)
}

type runtimeComplexityReportExactFinder interface {
	FindReportArtifactByID(context.Context, string) (domaincomplexity.ReportArtifact, bool, error)
}

type runtimeSuperAgentTraceExactFinder interface {
	FindTraceEventByID(context.Context, string) (domainsuperagent.TraceEvent, bool, error)
}

type runtimeAIWorkflowEventExactFinder interface {
	FindWorkflowEventByID(context.Context, string) (domainai.WorkflowEvent, bool, error)
}

func registerRuntimeDataRecallPersonaArchitecture(r *runtimeDataRecallRegistry, s viewer.PersonaObservationLister) error {
	if r == nil || s == nil {
		return fmt.Errorf("persona recall unavailable")
	}
	return r.Register("persona_architecture", "canonical_responses", dataRecallAccessUser, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		items, err := s.ListCanonicalResponseLogs(ctx, q.Limit)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		records := []map[string]any{}
		for _, v := range items {
			status := "unused"
			if v.Used {
				status = "used"
			}
			if v.Rewritten {
				status = "rewritten"
			}
			if dataRecallMatches(q.Query, v.ResponseID, v.CharacterID, status) {
				records = append(records, map[string]any{"response_id": v.ResponseID, "trigger": v.CharacterID, "status": status, "created_at": v.CreatedAt})
			}
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, records), nil
	})
}

func registerRuntimeDataRecallBrowserTraceToAPI(r *runtimeDataRecallRegistry, s viewer.BrowserTraceAPILister) error {
	if r == nil || s == nil {
		return fmt.Errorf("browser recall unavailable")
	}
	return r.Register("browser_trace_to_api", "validated_candidates", dataRecallAccessInternal, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		candidates, err := s.ListAPICandidates(ctx, q.Limit)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		validations, err := s.ListAPICandidateValidationResults(ctx, q.Limit)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		passed := map[string]bool{}
		for _, v := range validations {
			if v.Passed && strings.EqualFold(v.Status, "validated") {
				passed[v.CandidateID] = true
			}
		}
		records := []map[string]any{}
		for _, v := range candidates {
			if !passed[v.CandidateID] {
				continue
			}
			parsed, e := url.Parse(v.ObservedURL)
			if e != nil || parsed.Hostname() == "" {
				continue
			}
			host := parsed.Hostname()
			if dataRecallMatches(q.Query, v.CandidateID, v.Method, host) {
				records = append(records, map[string]any{"candidate_id": v.CandidateID, "host": host, "method": v.Method, "status": "validated"})
			}
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, records), nil
	})
}

func registerRuntimeDataRecallPersonaArchitectureObservations(r *runtimeDataRecallRegistry, s runtimePersonaObservationExactFinder) error {
	if r == nil || s == nil {
		return fmt.Errorf("persona observation recall unavailable")
	}
	return r.Register("persona_architecture", "observations", dataRecallAccessUser, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		scope, ok := domaintool.ToolExecutionScopeFromContext(ctx)
		if !ok || strings.TrimSpace(scope.AuthenticatedUserID) == "" {
			return runtimeDataRecallResult{}, fmt.Errorf("persona observation recall scope unavailable")
		}
		item, found, err := s.FindObservationLogByID(ctx, q.Query)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		records := []map[string]any{}
		if found && strings.TrimSpace(item.TargetID) == strings.TrimSpace(scope.AuthenticatedUserID) {
			records = append(records, map[string]any{
				"event_id":         item.EventID,
				"observation_type": item.ObservationType,
				"summary":          item.Summary,
				"evidence_refs":    append([]string(nil), item.EvidenceRefs...),
				"sensitivity":      item.Sensitivity,
				"review_status":    item.ReviewStatus,
				"created_at":       item.CreatedAt,
			})
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, records), nil
	})
}

func registerRuntimeDataRecallBrowserTraceValidationReviews(r *runtimeDataRecallRegistry, s runtimeBrowserTraceValidationExactFinder) error {
	if r == nil || s == nil {
		return fmt.Errorf("browser validation review recall unavailable")
	}
	return r.Register("browser_trace_to_api", "validation_reviews", dataRecallAccessInternal, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		item, found, err := s.FindAPICandidateValidationResultByID(ctx, q.Query)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		records := []map[string]any{}
		if found {
			records = append(records, map[string]any{
				"validation_id": item.ValidationID,
				"candidate_id":  item.CandidateID,
				"trace_run_id":  item.TraceRunID,
				"passed":        item.Passed,
				"status":        item.Status,
				"issues":        append([]domainbrowser.APIValidationIssue(nil), item.Issues...),
				"reviewer":      item.Reviewer,
				"review_note":   item.ReviewNote,
				"created_at":    item.CreatedAt,
			})
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, records), nil
	})
}

func registerRuntimeDataRecallComplexityHotspot(r *runtimeDataRecallRegistry, s viewer.ComplexityHotspotLister) error {
	if r == nil || s == nil {
		return fmt.Errorf("complexity recall unavailable")
	}
	return r.Register("complexity_hotspot", "hotspots", dataRecallAccessInternal, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		items, err := s.ListHotspots(ctx, q.Limit)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		records := []map[string]any{}
		for _, v := range items {
			clean := filepath.Clean(v.FilePath)
			if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
				continue
			}
			if dataRecallMatches(q.Query, v.HotspotID, v.HotspotType, v.RiskLevel, clean) {
				records = append(records, map[string]any{"hotspot_id": v.HotspotID, "file_path": filepath.ToSlash(clean), "category": v.HotspotType, "severity": v.RiskLevel, "status": "recorded"})
			}
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, records), nil
	})
}

func registerRuntimeDataRecallComplexityReviews(r *runtimeDataRecallRegistry, s runtimeComplexityReportExactFinder) error {
	if r == nil || s == nil {
		return fmt.Errorf("complexity review recall unavailable")
	}
	return r.Register("complexity_hotspot", "concrete_diff_reviews", dataRecallAccessInternal, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		item, found, err := s.FindReportArtifactByID(ctx, q.Query)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		records := []map[string]any{}
		if found {
			records = append(records, map[string]any{
				"artifact_id": item.ArtifactID, "scan_id": item.ScanID, "workstream_id": item.WorkstreamID,
				"artifact_type": item.Type, "title": item.Title, "status": item.Status,
				"content": item.Content, "created_at": item.CreatedAt,
			})
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, records), nil
	})
}

func registerRuntimeDataRecallSuperAgentHarness(r *runtimeDataRecallRegistry, s viewer.SuperAgentLister) error {
	if r == nil || s == nil {
		return fmt.Errorf("superagent recall unavailable")
	}
	return r.Register("super_agent_harness", "agent_runs", dataRecallAccessInternal, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		items, err := s.ListAgentRuns(ctx, q.Limit)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		records := []map[string]any{}
		for _, v := range items {
			if dataRecallMatches(q.Query, v.RunID, v.AgentType, v.Status) {
				records = append(records, map[string]any{"run_id": v.RunID, "agent": v.AgentType, "status": v.Status, "started_at": v.StartedAt, "completed_at": v.CompletedAt})
			}
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, records), nil
	})
}

func registerRuntimeDataRecallSuperAgentTraceEvents(r *runtimeDataRecallRegistry, s runtimeSuperAgentTraceExactFinder) error {
	if r == nil || s == nil {
		return fmt.Errorf("superagent trace recall unavailable")
	}
	return r.Register("super_agent_harness", "trace_events", dataRecallAccessInternal, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		item, found, err := s.FindTraceEventByID(ctx, q.Query)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		records := []map[string]any{}
		if found {
			records = append(records, map[string]any{
				"event_id": item.EventID, "parent_event_id": item.ParentEventID, "run_id": item.RunID,
				"event_type": item.EventType, "actor": item.Actor, "payload_summary": item.PayloadSummary,
				"status": item.Status, "created_at": item.CreatedAt,
			})
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, records), nil
	})
}

func registerRuntimeDataRecallAIWorkflow(r *runtimeDataRecallRegistry, s viewer.AIWorkflowLister) error {
	if r == nil || s == nil {
		return fmt.Errorf("ai workflow recall unavailable")
	}
	return r.Register("ai_workflow", "command_registry", dataRecallAccessInternal, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		items, err := s.ListCommandRegistries(ctx, q.Limit)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		records := []map[string]any{}
		for _, v := range items {
			if dataRecallMatches(q.Query, v.CommandName, v.Description) {
				records = append(records, map[string]any{"command": v.CommandName, "name": strings.TrimPrefix(v.CommandName, "/"), "description": v.Description, "status": "registered"})
			}
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, records), nil
	})
}

func registerRuntimeDataRecallAIWorkflowEvents(r *runtimeDataRecallRegistry, s runtimeAIWorkflowEventExactFinder) error {
	if r == nil || s == nil {
		return fmt.Errorf("ai workflow event recall unavailable")
	}
	return r.Register("ai_workflow", "workflow_events", dataRecallAccessInternal, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		item, found, err := s.FindWorkflowEventByID(ctx, q.Query)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		records := []map[string]any{}
		if found {
			records = append(records, map[string]any{
				"event_id": item.EventID, "parent_event_id": item.ParentEventID, "run_id": item.RunID,
				"workstream_id": item.WorkstreamID, "event_type": item.EventType, "agent": item.Agent,
				"repo": item.Repo, "worktree_id": item.WorktreeID, "command_name": item.CommandName,
				"skill_name": item.SkillName, "status": item.Status, "summary": item.Summary,
				"created_at": item.CreatedAt, "completed_at": item.CompletedAt,
			})
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, records), nil
	})
}

func registerRuntimeDataRecallDurableStoreWorkflow(r *runtimeDataRecallRegistry, s appstore.Store) error {
	if r == nil || s == nil {
		return fmt.Errorf("durable recall unavailable")
	}
	if err := r.Register("durable_store_workflow", "exact_request", dataRecallAccessUser, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		scope, found := domaintool.ToolExecutionScopeFromContext(ctx)
		if !found || strings.TrimSpace(scope.AuthenticatedUserID) == "" {
			return runtimeDataRecallResult{}, fmt.Errorf("authenticated user scope is required")
		}
		receipt, err := s.FindByRequestID(ctx, q.Query)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		records := []map[string]any{}
		if receipt == nil || strings.TrimSpace(receipt.RequestID) != q.Query || strings.TrimSpace(receipt.UserScope) != strings.TrimSpace(scope.AuthenticatedUserID) {
			return newRuntimeDataRecallResult(q.Store, q.Operation, records), nil
		}
		v, err := s.FindByRequirementID(ctx, receipt.RequirementID)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		if v == nil || strings.TrimSpace(v.Requirement.UserScope) != strings.TrimSpace(scope.AuthenticatedUserID) {
			return newRuntimeDataRecallResult(q.Store, q.Operation, records), nil
		}
		records = append(records, map[string]any{
			"requirement_id":    v.Requirement.RequirementID,
			"status":            string(v.Status),
			"lifecycle":         string(v.Lifecycle),
			"owner_module":      v.Classification.OwnerModule,
			"requested_outcome": string(v.Requirement.RequestedOutcome),
			"reason_code":       v.ReasonCode,
			"deduplicated":      receipt.RequestID != v.Requirement.RequestID,
			"created_at":        v.CreatedAt,
		})
		return newRuntimeDataRecallResult(q.Store, q.Operation, records), nil
	}); err != nil {
		return err
	}
	return r.Register("durable_store_workflow", "requirement", dataRecallAccessUser, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		scope, found := domaintool.ToolExecutionScopeFromContext(ctx)
		if !found || strings.TrimSpace(scope.AuthenticatedUserID) == "" {
			return runtimeDataRecallResult{}, fmt.Errorf("authenticated user scope is required")
		}
		workflow, err := s.FindByRequirementID(ctx, q.Query)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		record, visible, err := durableStoreWorkflowRequirementProjection(workflow, q.Query, scope.AuthenticatedUserID)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		if !visible {
			return newRuntimeDataRecallResult(q.Store, q.Operation, []map[string]any{}), nil
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, []map[string]any{record}), nil
	})
}

func durableStoreWorkflowRequirementProjection(workflow *domaindurable.WorkflowResult, requirementID, userID string) (map[string]any, bool, error) {
	if workflow == nil {
		return nil, false, nil
	}
	requirement := workflow.Requirement
	if strings.TrimSpace(requirement.RequirementID) == "" || requirement.RequirementID != requirementID {
		return nil, false, fmt.Errorf("durable workflow requirement_id is invalid")
	}
	if strings.TrimSpace(requirement.RequestID) == "" || requirement.RequestID != strings.TrimSpace(requirement.RequestID) {
		return nil, false, fmt.Errorf("durable workflow request_id is invalid")
	}
	if strings.TrimSpace(requirement.DedupeKey) == "" || requirement.DedupeKey != strings.TrimSpace(requirement.DedupeKey) {
		return nil, false, fmt.Errorf("durable workflow dedupe_key is invalid")
	}
	if strings.TrimSpace(requirement.UserScope) == "" || requirement.UserScope != strings.TrimSpace(requirement.UserScope) {
		return nil, false, fmt.Errorf("durable workflow user scope is invalid")
	}
	switch workflow.Status {
	case domaindurable.StatusCompleted, domaindurable.StatusRejected, domaindurable.StatusBlocked:
	default:
		return nil, false, fmt.Errorf("durable workflow status is invalid")
	}
	switch workflow.Lifecycle {
	case domaindurable.LifecycleProposed, domaindurable.LifecycleValidated, domaindurable.LifecycleImplemented, domaindurable.LifecycleProvisioned, domaindurable.LifecycleActive:
	default:
		return nil, false, fmt.Errorf("durable workflow lifecycle is invalid")
	}
	if workflow.CreatedAt.IsZero() || workflow.UpdatedAt.IsZero() || workflow.UpdatedAt.Before(workflow.CreatedAt) {
		return nil, false, fmt.Errorf("durable workflow timestamps are invalid")
	}
	if requirement.UserScope != strings.TrimSpace(userID) {
		return nil, false, nil
	}
	return map[string]any{
		"request_id":     requirement.RequestID,
		"requirement_id": requirement.RequirementID,
		"dedupe_key":     requirement.DedupeKey,
		"status":         string(workflow.Status),
		"lifecycle":      string(workflow.Lifecycle),
		"created_at":     workflow.CreatedAt,
		"updated_at":     workflow.UpdatedAt,
	}, true, nil
}
