package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// EvidenceLister provides recent execution reports.
type EvidenceLister interface {
	ListRecent(ctx context.Context, limit int) ([]domainexecution.ExecutionReport, error)
	ListRecentUnique(ctx context.Context, limit int) ([]domainexecution.ExecutionReport, error)
	GetByTaskID(ctx context.Context, taskID modulecore.TaskID) (domainexecution.ExecutionReport, error)
	Summary(ctx context.Context) (map[string]map[string]int, error)
	SummaryUnique(ctx context.Context) (map[string]map[string]int, error)
}

// EvidenceTaskReader resolves the canonical Task that owns an execution report.
type EvidenceTaskReader interface {
	GetTask(context.Context, modulecore.TaskID) (domaintask.Task, error)
}

// HandleEvidenceRecent returns recent execution reports as JSON.
func HandleEvidenceRecent(store EvidenceLister) http.HandlerFunc {
	return HandleEvidenceRecentWithPublicVisibility(store, nil, nil)
}

// HandleEvidenceRecentWithPublicVisibility returns reports only when their
// canonical Task is visible under the trusted public visibility policy.
func HandleEvidenceRecentWithPublicVisibility(store EvidenceLister, tasks EvidenceTaskReader, visibility PublicTaskVisibility) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		limit := 20
		if raw := r.URL.Query().Get("limit"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n <= 0 {
				http.Error(w, "invalid limit", http.StatusBadRequest)
				return
			}
			if n > 100 {
				n = 100
			}
			limit = n
		}
		if store == nil {
			http.Error(w, "evidence unavailable", http.StatusServiceUnavailable)
			return
		}
		if tasks == nil || visibility == nil {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []domainexecution.ExecutionReport{}})
			return
		}

		// Use ListRecentUnique by default to avoid showing duplicate tasks (retry/repair).
		items, err := store.ListRecentUnique(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load evidence", http.StatusInternalServerError)
			return
		}
		type visibleEvidenceCandidate struct {
			item domainexecution.ExecutionReport
			task domaintask.Task
		}
		candidates := make([]visibleEvidenceCandidate, 0, len(items))
		for _, item := range items {
			if err := item.TaskID.Validate(); err != nil {
				continue
			}
			task, err := tasks.GetTask(r.Context(), item.TaskID)
			if err != nil || task.TaskID != item.TaskID || !publicTaskVisible(r.Context(), visibility, task) {
				continue
			}
			candidates = append(candidates, visibleEvidenceCandidate{item: item, task: task})
		}

		// A later Task lookup or visibility check may block while an earlier
		// origin Session is revoked or recreated. Re-read every candidate's
		// canonical Task and revalidate its exact origin before response admission.
		visibleItems := make([]domainexecution.ExecutionReport, 0, len(candidates))
		for _, candidate := range candidates {
			current, err := tasks.GetTask(r.Context(), candidate.task.TaskID)
			if err != nil || current.TaskID != candidate.task.TaskID || !sameTaskOriginProof(candidate.task, current) {
				continue
			}
			if !publicTaskVisible(r.Context(), visibility, current) {
				continue
			}
			visibleItems = append(visibleItems, candidate.item)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": visibleItems,
		})
	}
}

// HandleEvidenceDetail returns one execution report by task_id.
func HandleEvidenceDetail(store EvidenceLister) http.HandlerFunc {
	return HandleEvidenceDetailWithPublicVisibility(store, nil, nil)
}

// HandleEvidenceDetailWithPublicVisibility returns a report only when its
// canonical Task is visible under the trusted public visibility policy.
func HandleEvidenceDetailWithPublicVisibility(store EvidenceLister, tasks EvidenceTaskReader, visibility PublicTaskVisibility) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		taskID := modulecore.TaskID(r.URL.Query().Get("task_id"))
		if err := taskID.Validate(); err != nil {
			http.Error(w, "valid task_id is required", http.StatusBadRequest)
			return
		}
		if tasks == nil || visibility == nil {
			http.Error(w, "report not found", http.StatusNotFound)
			return
		}
		task, err := tasks.GetTask(r.Context(), taskID)
		if err != nil || task.TaskID != taskID || !publicTaskVisible(r.Context(), visibility, task) {
			http.Error(w, "report not found", http.StatusNotFound)
			return
		}
		if store == nil {
			http.Error(w, "evidence unavailable", http.StatusServiceUnavailable)
			return
		}

		item, err := store.GetByTaskID(r.Context(), taskID)
		if err != nil || item.TaskID != taskID {
			http.Error(w, "report not found", http.StatusNotFound)
			return
		}
		current, err := tasks.GetTask(r.Context(), taskID)
		if err != nil || current.TaskID != taskID || !sameTaskOriginProof(task, current) || !publicTaskVisible(r.Context(), visibility, current) {
			http.Error(w, "report not found", http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"item": item,
		})
	}
}

func sameTaskOriginProof(left, right domaintask.Task) bool {
	if left.TaskID != right.TaskID ||
		left.OriginSessionID != right.OriginSessionID ||
		left.OriginThreadID != right.OriginThreadID ||
		left.OriginTurnID != right.OriginTurnID ||
		left.OriginMessageID != right.OriginMessageID {
		return false
	}
	if left.OriginPublication == nil || right.OriginPublication == nil {
		return left.OriginPublication == nil && right.OriginPublication == nil
	}
	return *left.OriginPublication == *right.OriginPublication
}

// HandleEvidenceSummary returns evidence summary counts.
func HandleEvidenceSummary(store EvidenceLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// SummaryUnique is global and cannot prove that private Tasks have been
		// removed. Keep the public endpoint unavailable until the owner exposes
		// an exact visibility-scoped aggregate.
		http.Error(w, "scoped evidence summary unavailable", http.StatusServiceUnavailable)
	}
}
