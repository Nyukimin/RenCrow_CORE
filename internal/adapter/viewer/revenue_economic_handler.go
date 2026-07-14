package viewer

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	revenueapp "github.com/Nyukimin/RenCrow_CORE/internal/application/revenue"
	domainrevenue "github.com/Nyukimin/RenCrow_CORE/internal/domain/revenue"
)

type RevenueOpportunityWorkstreamGoalRequest struct {
	OpportunityID string `json:"opportunity_id"`
	WorkstreamID  string `json:"workstream_id"`
}

type RevenueReflectionFromEventRequest struct {
	ReflectionID   string   `json:"reflection_id"`
	OpportunityID  string   `json:"opportunity_id"`
	RevenueEventID string   `json:"revenue_event_id"`
	Outcome        string   `json:"outcome"`
	Lessons        []string `json:"lessons,omitempty"`
	NextActions    []string `json:"next_actions,omitempty"`
}

func HandleRevenueOpportunities(store RevenueStore) http.HandlerFunc {
	service := revenueapp.NewEconomicService(store, time.Now)
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			http.Error(w, "revenue store unavailable", http.StatusServiceUnavailable)
			return
		}
		switch r.Method {
		case http.MethodGet:
			limit, err := parseViewerLimit(r.URL.Query().Get("limit"), 50, 100)
			if err != nil {
				http.Error(w, "invalid limit", http.StatusBadRequest)
				return
			}
			items, err := store.ListOpportunities(r.Context(), limit)
			if err != nil {
				http.Error(w, "failed to load opportunities", http.StatusInternalServerError)
				return
			}
			if items == nil {
				items = []domainrevenue.Opportunity{}
			}
			writeJSON(w, http.StatusOK, map[string]any{"opportunities": items, "opportunity_count": len(items)})
		case http.MethodPost:
			var item domainrevenue.Opportunity
			if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
				http.Error(w, "invalid opportunity payload", http.StatusBadRequest)
				return
			}
			created, err := service.DraftOpportunity(r.Context(), item)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, http.StatusCreated, map[string]any{"opportunity": created, "human_approval_required_for_publish": true})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func HandleRevenueEconomicTasks(store RevenueStore) http.HandlerFunc {
	service := revenueapp.NewEconomicService(store, time.Now)
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			http.Error(w, "revenue store unavailable", http.StatusServiceUnavailable)
			return
		}
		switch r.Method {
		case http.MethodGet:
			limit, err := parseViewerLimit(r.URL.Query().Get("limit"), 50, 100)
			if err != nil {
				http.Error(w, "invalid limit", http.StatusBadRequest)
				return
			}
			items, err := store.ListEconomicTasks(r.Context(), limit)
			if err != nil {
				http.Error(w, "failed to load economic tasks", http.StatusInternalServerError)
				return
			}
			if items == nil {
				items = []domainrevenue.EconomicTask{}
			}
			writeJSON(w, http.StatusOK, map[string]any{"economic_tasks": items, "task_count": len(items)})
		case http.MethodPost:
			var item domainrevenue.EconomicTask
			if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
				http.Error(w, "invalid economic task payload", http.StatusBadRequest)
				return
			}
			created, err := service.DraftEconomicTask(r.Context(), item)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, http.StatusCreated, map[string]any{
				"economic_task": created, "human_approval_required": domainrevenue.RequiresHumanApproval(created.TaskKind),
				"auto_execution_allowed": !domainrevenue.RequiresHumanApproval(created.TaskKind),
			})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func HandleRevenueEconomicReflections(store RevenueStore) http.HandlerFunc {
	service := revenueapp.NewEconomicService(store, time.Now)
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			http.Error(w, "revenue store unavailable", http.StatusServiceUnavailable)
			return
		}
		switch r.Method {
		case http.MethodGet:
			limit, err := parseViewerLimit(r.URL.Query().Get("limit"), 50, 100)
			if err != nil {
				http.Error(w, "invalid limit", http.StatusBadRequest)
				return
			}
			items, err := store.ListEconomicReflections(r.Context(), limit)
			if err != nil {
				http.Error(w, "failed to load economic reflections", http.StatusInternalServerError)
				return
			}
			if items == nil {
				items = []domainrevenue.EconomicReflection{}
			}
			writeJSON(w, http.StatusOK, map[string]any{"economic_reflections": items, "reflection_count": len(items)})
		case http.MethodPost:
			var item domainrevenue.EconomicReflection
			if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
				http.Error(w, "invalid economic reflection payload", http.StatusBadRequest)
				return
			}
			created, err := service.DraftReflection(r.Context(), item)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, http.StatusCreated, map[string]any{"economic_reflection": created})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func HandleRevenueReflectionFromEvent(store RevenueStore) http.HandlerFunc {
	service := revenueapp.NewEconomicService(store, time.Now)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "revenue store unavailable", http.StatusServiceUnavailable)
			return
		}
		var req RevenueReflectionFromEventRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid reflection from revenue event payload", http.StatusBadRequest)
			return
		}
		created, err := service.ReflectRevenueEvent(r.Context(), revenueapp.ReflectionFromRevenueEventRequest{
			ReflectionID: req.ReflectionID, OpportunityID: req.OpportunityID, RevenueEventID: req.RevenueEventID,
			Outcome: req.Outcome, Lessons: req.Lessons, NextActions: req.NextActions,
		})
		if errors.Is(err, revenueapp.ErrOpportunityNotFound) || errors.Is(err, revenueapp.ErrRevenueEventNotFound) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"economic_reflection": created})
	}
}

func HandleRevenueOpportunityWorkstreamGoal(store RevenueStore, goalStore revenueapp.WorkstreamGoalStore) http.HandlerFunc {
	service := revenueapp.NewEconomicService(store, time.Now).WithWorkstreamGoalStore(goalStore)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil || goalStore == nil {
			http.Error(w, "economic goal service unavailable", http.StatusServiceUnavailable)
			return
		}
		var req RevenueOpportunityWorkstreamGoalRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid opportunity workstream goal payload", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.OpportunityID) == "" || strings.TrimSpace(req.WorkstreamID) == "" {
			http.Error(w, "opportunity_id and workstream_id are required", http.StatusBadRequest)
			return
		}
		goal, err := service.CreateWorkstreamGoal(r.Context(), req.OpportunityID, req.WorkstreamID)
		if errors.Is(err, revenueapp.ErrOpportunityNotFound) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"goal": goal, "status": "draft", "external_actions_applied": false})
	}
}
