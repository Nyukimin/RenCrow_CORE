package main

import (
	"context"
	"fmt"

	domainadvisor "github.com/Nyukimin/RenCrow_CORE/internal/domain/advisor"
	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
	domainrevenue "github.com/Nyukimin/RenCrow_CORE/internal/domain/revenue"
	domainsandbox "github.com/Nyukimin/RenCrow_CORE/internal/domain/sandbox"
	domainskill "github.com/Nyukimin/RenCrow_CORE/internal/domain/skillgovernance"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

type advisorRecallLister interface {
	ListAdviceRuns(context.Context, int) ([]domainadvisor.AdviceRunRecord, error)
}
type sandboxRecallLister interface {
	ListSandboxes(context.Context, int) ([]domainsandbox.SandboxRecord, error)
}
type dciRecallLister interface {
	ListRecent(int) ([]domaindci.SearchTrace, error)
}
type skillRecallLister interface {
	ListSkillManifests(context.Context, int) ([]domainskill.SkillManifest, error)
}
type workstreamRecallLister interface {
	ListGoals(context.Context, int) ([]domainworkstream.Goal, error)
}
type revenueRecallLister interface {
	ListOpportunities(context.Context, int) ([]domainrevenue.Opportunity, error)
}

func registerRuntimeDataRecallAdvisor(r *runtimeDataRecallRegistry, s advisorRecallLister) error {
	if r == nil || s == nil {
		return fmt.Errorf("advisor recall unavailable")
	}
	return r.Register("advisor", "advice_runs", dataRecallAccessInternal, func(ctx context.Context, q tools.DataRecallRequest) (any, error) {
		items, err := s.ListAdviceRuns(ctx, q.Limit)
		if err != nil {
			return nil, err
		}
		records := []map[string]any{}
		for _, v := range items {
			if dataRecallMatches(q.Query, v.RunID, string(v.AdvisorID), string(v.Status), v.Summary) {
				records = append(records, map[string]any{"run_id": v.RunID, "advisor_id": string(v.AdvisorID), "status": string(v.Status), "summary": v.Summary, "created_at": v.StartedAt})
			}
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, records), nil
	})
}

func registerRuntimeDataRecallSandbox(r *runtimeDataRecallRegistry, s sandboxRecallLister) error {
	if r == nil || s == nil {
		return fmt.Errorf("sandbox recall unavailable")
	}
	return r.Register("sandbox", "sandboxes", dataRecallAccessInternal, func(ctx context.Context, q tools.DataRecallRequest) (any, error) {
		items, err := s.ListSandboxes(ctx, q.Limit)
		if err != nil {
			return nil, err
		}
		records := []map[string]any{}
		for _, v := range items {
			if dataRecallMatches(q.Query, v.SandboxID, string(v.Status), v.BaseRef) {
				records = append(records, map[string]any{"sandbox_id": v.SandboxID, "status": string(v.Status), "base_branch": v.BaseRef, "created_at": v.CreatedAt})
			}
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, records), nil
	})
}

func registerRuntimeDataRecallDCI(r *runtimeDataRecallRegistry, s dciRecallLister) error {
	if r == nil || s == nil {
		return fmt.Errorf("dci recall unavailable")
	}
	return r.Register("dci", "search_traces", dataRecallAccessInternal, func(_ context.Context, q tools.DataRecallRequest) (any, error) {
		items, err := s.ListRecent(q.Limit)
		if err != nil {
			return nil, err
		}
		records := []map[string]any{}
		for _, v := range items {
			if dataRecallMatches(q.Query, v.EventID, v.UserQuery, v.Status) {
				records = append(records, map[string]any{"trace_id": v.EventID, "query": v.UserQuery, "scope": v.CorpusScope, "status": v.Status, "created_at": v.StartedAt})
			}
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, records), nil
	})
}

func registerRuntimeDataRecallSkillGovernance(r *runtimeDataRecallRegistry, s skillRecallLister) error {
	if r == nil || s == nil {
		return fmt.Errorf("skill recall unavailable")
	}
	return r.Register("skill_governance", "skill_manifests", dataRecallAccessInternal, func(ctx context.Context, q tools.DataRecallRequest) (any, error) {
		items, err := s.ListSkillManifests(ctx, q.Limit)
		if err != nil {
			return nil, err
		}
		records := []map[string]any{}
		for _, v := range items {
			status := "disabled"
			if v.Enabled {
				status = "enabled"
			}
			if dataRecallMatches(q.Query, v.SkillID, v.Name, v.Version, status) {
				records = append(records, map[string]any{"skill_id": v.SkillID, "name": v.Name, "version": v.Version, "status": status})
			}
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, records), nil
	})
}

func registerRuntimeDataRecallWorkstream(r *runtimeDataRecallRegistry, s workstreamRecallLister) error {
	if r == nil || s == nil {
		return fmt.Errorf("workstream recall unavailable")
	}
	return r.Register("workstream", "goals", dataRecallAccessUser, func(ctx context.Context, q tools.DataRecallRequest) (any, error) {
		items, err := s.ListGoals(ctx, q.Limit)
		if err != nil {
			return nil, err
		}
		records := []map[string]any{}
		for _, v := range items {
			if dataRecallMatches(q.Query, v.GoalID, v.WorkstreamID, v.Title, string(v.Status)) {
				records = append(records, map[string]any{"goal_id": v.GoalID, "title": v.Title, "status": string(v.Status), "workstream_id": v.WorkstreamID, "created_at": v.CreatedAt})
			}
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, records), nil
	})
}

func registerRuntimeDataRecallRevenue(r *runtimeDataRecallRegistry, s revenueRecallLister) error {
	if r == nil || s == nil {
		return fmt.Errorf("revenue recall unavailable")
	}
	return r.Register("revenue", "opportunities", dataRecallAccessInternal, func(ctx context.Context, q tools.DataRecallRequest) (any, error) {
		items, err := s.ListOpportunities(ctx, q.Limit)
		if err != nil {
			return nil, err
		}
		records := []map[string]any{}
		for _, v := range items {
			if dataRecallMatches(q.Query, v.OpportunityID, v.Title, v.Summary) {
				records = append(records, map[string]any{"opportunity_id": v.OpportunityID, "title": v.Title, "summary": v.Summary, "updated_at": v.UpdatedAt})
			}
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, records), nil
	})
}
