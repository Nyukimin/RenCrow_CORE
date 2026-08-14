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
type advisorAdoptionRecallFinder interface {
	FindAdvisorAdoptionByID(context.Context, string) (domainadvisor.AdvisorAdoptionRecord, bool, error)
}
type sandboxRecallLister interface {
	ListSandboxes(context.Context, int) ([]domainsandbox.SandboxRecord, error)
}
type sandboxPromotionRecallFinder interface {
	FindPromotionRequestByID(context.Context, string) (domainsandbox.PromotionRequest, bool, error)
	FindPromotionGateLogByID(context.Context, string) (domainsandbox.PromotionGateLog, bool, error)
}
type dciRecallLister interface {
	ListRecent(int) ([]domaindci.SearchTrace, error)
}
type dciSearchTraceRecallFinder interface {
	FindSearchTraceByID(context.Context, string) (domaindci.SearchTrace, bool, error)
}
type skillRecallLister interface {
	ListSkillManifests(context.Context, int) ([]domainskill.SkillManifest, error)
}
type skillContributionGateRecallFinder interface {
	FindContributionGateByID(context.Context, string) (domainskill.ContributionGateLog, bool, error)
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
	return r.Register("advisor", "advice_runs", dataRecallAccessInternal, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		items, err := s.ListAdviceRuns(ctx, q.Limit)
		if err != nil {
			return runtimeDataRecallResult{}, err
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

func registerRuntimeDataRecallAdvisorAdoptions(r *runtimeDataRecallRegistry, s advisorAdoptionRecallFinder) error {
	if r == nil || s == nil {
		return fmt.Errorf("advisor adoption recall unavailable")
	}
	return r.Register("advisor", "adoptions", dataRecallAccessInternal, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		item, found, err := s.FindAdvisorAdoptionByID(ctx, q.Query)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		records := []map[string]any{}
		if found {
			records = append(records, map[string]any{
				"adoption_id": item.AdoptionID, "run_id": item.RunID, "task_id": item.TaskID,
				"advisor_id": string(item.AdvisorID), "adopted_by_agent": item.AdoptedByAgent,
				"adopted": item.Adopted, "outcome": item.Outcome, "revision_count": item.RevisionCount,
				"reason": item.Reason, "created_at": item.CreatedAt,
			})
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, records), nil
	})
}

func registerRuntimeDataRecallSandbox(r *runtimeDataRecallRegistry, s sandboxRecallLister) error {
	if r == nil || s == nil {
		return fmt.Errorf("sandbox recall unavailable")
	}
	return r.Register("sandbox", "sandboxes", dataRecallAccessInternal, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		items, err := s.ListSandboxes(ctx, q.Limit)
		if err != nil {
			return runtimeDataRecallResult{}, err
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

func registerRuntimeDataRecallSandboxPromotionGates(r *runtimeDataRecallRegistry, s sandboxPromotionRecallFinder) error {
	if r == nil || s == nil {
		return fmt.Errorf("sandbox promotion gate recall unavailable")
	}
	return r.Register("sandbox", "promotion_gates", dataRecallAccessInternal, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		gate, found, err := s.FindPromotionGateLogByID(ctx, q.Query)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		records := []map[string]any{}
		if found {
			promotion, promotionFound, err := s.FindPromotionRequestByID(ctx, gate.PromotionID)
			if err != nil {
				return runtimeDataRecallResult{}, err
			}
			if !promotionFound {
				return runtimeDataRecallResult{}, fmt.Errorf("sandbox promotion gate references missing promotion")
			}
			records = append(records, map[string]any{
				"event_id": gate.EventID, "promotion_id": promotion.PromotionID, "sandbox_id": promotion.SandboxID,
				"workstream_id": promotion.WorkstreamID, "goal_id": promotion.GoalID,
				"requested_by": promotion.RequestedBy, "risk_level": promotion.RiskLevel,
				"promotion_reason": promotion.Reason, "gate_status": gate.GateStatus,
				"gate_reason": gate.Reason, "created_at": gate.CreatedAt,
			})
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, records), nil
	})
}

func registerRuntimeDataRecallDCI(r *runtimeDataRecallRegistry, s dciRecallLister) error {
	if r == nil || s == nil {
		return fmt.Errorf("dci recall unavailable")
	}
	return r.Register("dci", "search_traces", dataRecallAccessInternal, func(_ context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		items, err := s.ListRecent(q.Limit)
		if err != nil {
			return runtimeDataRecallResult{}, err
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

func registerRuntimeDataRecallDCISearchTrace(r *runtimeDataRecallRegistry, s dciSearchTraceRecallFinder) error {
	if r == nil || s == nil {
		return fmt.Errorf("dci exact search trace recall unavailable")
	}
	return r.Register("dci", "search_trace", dataRecallAccessInternal, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		trace, found, err := s.FindSearchTraceByID(ctx, q.Query)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		records := []map[string]any{}
		if found {
			records = append(records, map[string]any{
				"trace_id": trace.EventID, "query": trace.UserQuery, "actor": trace.Actor,
				"mode": trace.Mode, "scope": append([]string(nil), trace.CorpusScope...),
				"steps":          append([]domaindci.SearchStep(nil), trace.Steps...),
				"evidence_count": trace.FinalEvidenceCount, "status": trace.Status,
				"error_message": trace.ErrorMessage, "started_at": trace.StartedAt, "ended_at": trace.EndedAt,
			})
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, records), nil
	})
}

func registerRuntimeDataRecallSkillGovernance(r *runtimeDataRecallRegistry, s skillRecallLister) error {
	if r == nil || s == nil {
		return fmt.Errorf("skill recall unavailable")
	}
	return r.Register("skill_governance", "skill_manifests", dataRecallAccessInternal, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		items, err := s.ListSkillManifests(ctx, q.Limit)
		if err != nil {
			return runtimeDataRecallResult{}, err
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

func registerRuntimeDataRecallSkillContributionGates(r *runtimeDataRecallRegistry, s skillContributionGateRecallFinder) error {
	if r == nil || s == nil {
		return fmt.Errorf("skill contribution gate recall unavailable")
	}
	return r.Register("skill_governance", "contribution_gates", dataRecallAccessInternal, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		item, found, err := s.FindContributionGateByID(ctx, q.Query)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		records := []map[string]any{}
		if found {
			records = append(records, map[string]any{
				"event_id": item.EventID, "repo": item.Repo, "target_branch": item.TargetBranch,
				"problem_statement": item.ProblemStatement, "existing_prs_checked": item.ExistingPRsChecked,
				"real_problem_verified": item.RealProblemVerified, "core_change_verified": item.CoreChangeVerified,
				"diff_reviewed": item.DiffReviewed, "test_result": item.TestResult,
				"gate_status": item.GateStatus, "created_at": item.CreatedAt,
			})
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, records), nil
	})
}

func registerRuntimeDataRecallWorkstream(r *runtimeDataRecallRegistry, s workstreamRecallLister) error {
	if r == nil || s == nil {
		return fmt.Errorf("workstream recall unavailable")
	}
	return r.Register("workstream", "goals", dataRecallAccessUser, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		items, err := s.ListGoals(ctx, q.Limit)
		if err != nil {
			return runtimeDataRecallResult{}, err
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
	return r.Register("revenue", "opportunities", dataRecallAccessInternal, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		items, err := s.ListOpportunities(ctx, q.Limit)
		if err != nil {
			return runtimeDataRecallResult{}, err
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
