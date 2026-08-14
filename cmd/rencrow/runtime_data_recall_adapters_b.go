package main

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	appstore "github.com/Nyukimin/RenCrow_CORE/internal/application/durablestore"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

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

func registerRuntimeDataRecallDurableStoreWorkflow(r *runtimeDataRecallRegistry, s appstore.Store) error {
	if r == nil || s == nil {
		return fmt.Errorf("durable recall unavailable")
	}
	return r.Register("durable_store_workflow", "exact_request", dataRecallAccessUser, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		v, err := s.FindByDedupeKey(ctx, q.Query)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		records := []map[string]any{}
		if v != nil {
			records = append(records, map[string]any{"status": string(v.Status), "owner_module": v.Classification.OwnerModule, "requested_outcome": string(v.Requirement.RequestedOutcome), "created_at": v.CreatedAt})
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, records), nil
	})
}
