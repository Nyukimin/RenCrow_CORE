package viewer

import (
	"context"
	"net/http"

	domainadvisor "github.com/Nyukimin/RenCrow_CORE/internal/domain/advisor"
	domainagentprofile "github.com/Nyukimin/RenCrow_CORE/internal/domain/agentprofile"
)

type AdvisorReadStore interface {
	ListAdviceRuns(ctx context.Context, limit int) ([]domainadvisor.AdviceRunRecord, error)
	ListAdvisorScoreSnapshots(ctx context.Context, limit int) ([]domainadvisor.AdvisorScoreSnapshot, error)
	ListAgentPolicyDecisions(ctx context.Context, limit int) ([]domainagentprofile.PolicyDecision, error)
}

type AdvisorStatusOptions struct {
	Store           AdvisorReadStore
	AdvisorProfiles []domainadvisor.Profile
	AgentProfiles   []domainagentprofile.Profile
}

func HandleAdvisorsStatus(options AdvisorStatusOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		limit, err := parseViewerLimit(r.URL.Query().Get("limit"), 20, 100)
		if err != nil {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		runs := []domainadvisor.AdviceRunRecord{}
		scores := []domainadvisor.AdvisorScoreSnapshot{}
		decisions := []domainagentprofile.PolicyDecision{}
		warnings := []string{}
		if options.Store == nil {
			warnings = append(warnings, "advisor store unavailable")
		} else {
			if items, loadErr := options.Store.ListAdviceRuns(r.Context(), limit); loadErr != nil {
				warnings = append(warnings, "advisor runs unavailable: "+loadErr.Error())
			} else if items != nil {
				runs = items
			}
			if items, loadErr := options.Store.ListAdvisorScoreSnapshots(r.Context(), limit); loadErr != nil {
				warnings = append(warnings, "advisor scores unavailable: "+loadErr.Error())
			} else if items != nil {
				scores = items
			}
			if items, loadErr := options.Store.ListAgentPolicyDecisions(r.Context(), limit); loadErr != nil {
				warnings = append(warnings, "agent policy decisions unavailable: "+loadErr.Error())
			} else if items != nil {
				decisions = items
			}
		}
		failed := 0
		for _, run := range runs {
			if run.Status == domainadvisor.AdviceStatus(domainadvisor.StatusFailed) || run.Status == domainadvisor.AdviceStatus(domainadvisor.StatusUnavailable) {
				failed++
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled":          len(options.AdvisorProfiles) > 0,
			"profiles":         nonNilAdvisorProfiles(options.AdvisorProfiles),
			"recent_runs":      runs,
			"score_snapshots":  scores,
			"agent_profiles":   nonNilAgentProfiles(options.AgentProfiles),
			"policy_decisions": decisions,
			"warnings":         warnings,
			"status":           availabilityStatus(warnings),
			"summary": map[string]int{
				"advisor_count":         len(options.AdvisorProfiles),
				"recent_run_count":      len(runs),
				"failed_run_count":      failed,
				"score_snapshot_count":  len(scores),
				"profile_count":         len(options.AgentProfiles),
				"policy_decision_count": len(decisions),
			},
		})
	}
}

func HandleAdvisorRuns(store AdvisorReadStore) http.HandlerFunc {
	return handleAdvisorList(store, func(ctx context.Context, limit int) (any, error) {
		if store == nil {
			return []domainadvisor.AdviceRunRecord{}, nil
		}
		return store.ListAdviceRuns(ctx, limit)
	}, "runs")
}

func HandleAdvisorScores(store AdvisorReadStore) http.HandlerFunc {
	return handleAdvisorList(store, func(ctx context.Context, limit int) (any, error) {
		if store == nil {
			return []domainadvisor.AdvisorScoreSnapshot{}, nil
		}
		return store.ListAdvisorScoreSnapshots(ctx, limit)
	}, "scores")
}

func HandleAgentProfiles(profiles []domainagentprofile.Profile) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		profiles = nonNilAgentProfiles(profiles)
		writeJSON(w, http.StatusOK, map[string]any{"profiles": profiles, "profile_count": len(profiles)})
	}
}

func HandleAgentPolicyDecisions(store AdvisorReadStore) http.HandlerFunc {
	return handleAdvisorList(store, func(ctx context.Context, limit int) (any, error) {
		if store == nil {
			return []domainagentprofile.PolicyDecision{}, nil
		}
		return store.ListAgentPolicyDecisions(ctx, limit)
	}, "policy_decisions")
}

func handleAdvisorList(store AdvisorReadStore, list func(context.Context, int) (any, error), key string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		limit, err := parseViewerLimit(r.URL.Query().Get("limit"), 50, 100)
		if err != nil {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		items, err := list(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load "+key, http.StatusInternalServerError)
			return
		}
		warnings := []string{}
		if store == nil {
			warnings = append(warnings, "advisor store unavailable")
		}
		writeJSON(w, http.StatusOK, map[string]any{key: items, "warnings": warnings, "status": availabilityStatus(warnings)})
	}
}

func availabilityStatus(warnings []string) string {
	if len(warnings) > 0 {
		return "unavailable"
	}
	return "ok"
}

func nonNilAdvisorProfiles(items []domainadvisor.Profile) []domainadvisor.Profile {
	if items == nil {
		return []domainadvisor.Profile{}
	}
	return items
}

func nonNilAgentProfiles(items []domainagentprofile.Profile) []domainagentprofile.Profile {
	if items == nil {
		return []domainagentprofile.Profile{}
	}
	return items
}
