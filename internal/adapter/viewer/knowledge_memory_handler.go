package viewer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	kmapp "github.com/Nyukimin/RenCrow_CORE/internal/application/knowledgememory"
	domainkm "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgememory"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type KnowledgeMemoryLister interface {
	ListPersonalArchiveEntries(ctx context.Context, limit int) ([]domainkm.PersonalArchiveEntry, error)
	ListCreativeKnowledgeItems(ctx context.Context, limit int) ([]domainkm.CreativeKnowledgeItem, error)
	ListNewsKnowledgeItems(ctx context.Context, limit int) ([]domainkm.NewsKnowledgeItem, error)
	ListDailyIntakeRules(ctx context.Context, limit int) ([]domainkm.DailyIntakeRule, error)
	ListTemporalMemoryMarkers(ctx context.Context, limit int) ([]domainkm.TemporalMemoryMarker, error)
	ListDreamConsolidationRuns(ctx context.Context, limit int) ([]domainkm.DreamConsolidationRun, error)
}

type KnowledgeMemoryStore interface {
	KnowledgeMemoryLister
	SavePersonalArchiveEntry(ctx context.Context, item domainkm.PersonalArchiveEntry) error
	SaveCreativeKnowledgeItem(ctx context.Context, item domainkm.CreativeKnowledgeItem) error
	SaveNewsKnowledgeItem(ctx context.Context, item domainkm.NewsKnowledgeItem) error
	SaveDailyIntakeRule(ctx context.Context, item domainkm.DailyIntakeRule) error
	SaveTemporalMemoryMarker(ctx context.Context, item domainkm.TemporalMemoryMarker) error
	SaveDreamConsolidationRun(ctx context.Context, item domainkm.DreamConsolidationRun) error
}

type DreamConsolidationReviewRequest struct {
	RunID        modulecore.RunID `json:"run_id"`
	ReviewStatus string           `json:"review_status"`
	Promote      bool             `json:"promote,omitempty"`
}

type KnowledgeMemoryReviewRequest struct {
	DetailType   string `json:"detail_type"`
	ID           string `json:"id"`
	ReviewStatus string `json:"review_status"`
	Promote      bool   `json:"promote,omitempty"`
	ReviewedBy   string `json:"reviewed_by,omitempty"`
}

func HandleKnowledgeMemoryStatus(store KnowledgeMemoryLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "knowledge memory store unavailable", http.StatusServiceUnavailable)
			return
		}
		limit, err := parseViewerLimit(r.URL.Query().Get("limit"), 20, 100)
		if err != nil {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		if detailType := strings.TrimSpace(r.URL.Query().Get("detail_type")); detailType != "" {
			handleKnowledgeMemoryDetail(w, r, store, detailType, limit)
			return
		}
		personal, _ := store.ListPersonalArchiveEntries(r.Context(), limit)
		creative, _ := store.ListCreativeKnowledgeItems(r.Context(), limit)
		news, _ := store.ListNewsKnowledgeItems(r.Context(), limit)
		intake, _ := store.ListDailyIntakeRules(r.Context(), limit)
		temporal, _ := store.ListTemporalMemoryMarkers(r.Context(), limit)
		dream, _ := store.ListDreamConsolidationRuns(r.Context(), limit)
		writeJSON(w, http.StatusOK, map[string]any{
			"personal_archive":   nonNilPersonalArchive(personal),
			"creative_knowledge": nonNilCreativeKnowledge(creative),
			"news_knowledge":     nonNilNewsKnowledge(news),
			"daily_intake_rules": nonNilDailyIntakeRules(intake),
			"temporal_markers":   nonNilTemporalMarkers(temporal),
			"dream_runs":         nonNilDreamRuns(dream),
		})
	}
}

func HandleKnowledgeMemoryReview(store KnowledgeMemoryStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "knowledge memory store unavailable", http.StatusServiceUnavailable)
			return
		}
		var req KnowledgeMemoryReviewRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid knowledge memory review payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		result, err := reviewKnowledgeMemoryItem(r.Context(), store, req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, result)
	}
}

func reviewKnowledgeMemoryItem(ctx context.Context, store KnowledgeMemoryStore, req KnowledgeMemoryReviewRequest) (map[string]any, error) {
	detailType := strings.TrimSpace(req.DetailType)
	id := strings.TrimSpace(req.ID)
	reviewStatus := strings.TrimSpace(req.ReviewStatus)
	if detailType == "" || id == "" || reviewStatus == "" {
		return nil, fmt.Errorf("detail_type, id and review_status are required")
	}
	if reviewStatus != "adopted" && reviewStatus != "rejected" {
		return nil, fmt.Errorf("review_status must be adopted or rejected")
	}
	if req.Promote && reviewStatus != "adopted" {
		return nil, fmt.Errorf("promote requires adopted review_status")
	}
	current, ok, err := findKnowledgeMemoryDetail(ctx, store, detailType, id, 100)
	if err != nil {
		return nil, fmt.Errorf("failed to load knowledge memory item")
	}
	if !ok {
		return nil, fmt.Errorf("knowledge memory item not found")
	}
	targetStatus := targetKnowledgeMemoryStatus(reviewStatus, req.Promote, detailType)
	if targetStatus == "" {
		return nil, fmt.Errorf("unsupported knowledge memory review type")
	}
	target, err := saveKnowledgeMemoryReviewTarget(ctx, store, detailType, current, targetStatus)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"status":        "reviewed",
		"detail_type":   detailType,
		"id":            id,
		"review_status": reviewStatus,
		"promoted":      req.Promote,
		"auto_promote":  false,
		"reviewed_by":   strings.TrimSpace(req.ReviewedBy),
		"comparison": map[string]any{
			"current_status": knowledgeMemoryStatusValue(current),
			"target_status":  targetStatus,
			"current_item":   current,
			"target_item":    target,
			"formal_target":  knowledgeMemoryFormalTarget(detailType, targetStatus),
		},
	}, nil
}

func targetKnowledgeMemoryStatus(reviewStatus string, promote bool, detailType string) string {
	if reviewStatus == "rejected" {
		return "rejected"
	}
	if promote {
		if detailType == "daily_intake_rule" {
			return "enabled"
		}
		return "promoted"
	}
	return "reviewed"
}

func saveKnowledgeMemoryReviewTarget(ctx context.Context, store KnowledgeMemoryStore, detailType string, current any, targetStatus string) (any, error) {
	switch detailType {
	case "creative_knowledge":
		item, ok := current.(domainkm.CreativeKnowledgeItem)
		if !ok {
			return nil, fmt.Errorf("invalid creative knowledge item")
		}
		item.Status = targetStatus
		if err := store.SaveCreativeKnowledgeItem(ctx, item); err != nil {
			return nil, fmt.Errorf("failed to save creative knowledge review: %w", err)
		}
		return item, nil
	case "news_knowledge":
		item, ok := current.(domainkm.NewsKnowledgeItem)
		if !ok {
			return nil, fmt.Errorf("invalid news knowledge item")
		}
		item.Status = targetStatus
		if err := store.SaveNewsKnowledgeItem(ctx, item); err != nil {
			return nil, fmt.Errorf("failed to save news knowledge review: %w", err)
		}
		return item, nil
	case "daily_intake_rule":
		item, ok := current.(domainkm.DailyIntakeRule)
		if !ok {
			return nil, fmt.Errorf("invalid daily intake rule")
		}
		item.Status = targetStatus
		if err := store.SaveDailyIntakeRule(ctx, item); err != nil {
			return nil, fmt.Errorf("failed to save daily intake rule review: %w", err)
		}
		return item, nil
	default:
		return nil, fmt.Errorf("unsupported knowledge memory review type")
	}
}

func knowledgeMemoryStatusValue(item any) string {
	switch v := item.(type) {
	case domainkm.CreativeKnowledgeItem:
		return v.Status
	case domainkm.NewsKnowledgeItem:
		return v.Status
	case domainkm.DailyIntakeRule:
		return v.Status
	default:
		return ""
	}
}

func knowledgeMemoryFormalTarget(detailType string, targetStatus string) string {
	switch detailType {
	case "creative_knowledge":
		return "knowledge_memory.creative_knowledge:" + targetStatus
	case "news_knowledge":
		return "knowledge_memory.news_knowledge:" + targetStatus
	case "daily_intake_rule":
		return "source_registry.daily_intake_rule:" + targetStatus
	default:
		return "knowledge_memory:" + targetStatus
	}
}

func handleKnowledgeMemoryDetail(w http.ResponseWriter, r *http.Request, store KnowledgeMemoryLister, detailType string, limit int) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		http.Error(w, "knowledge memory detail id is required", http.StatusBadRequest)
		return
	}
	item, ok, err := findKnowledgeMemoryDetail(r.Context(), store, detailType, id, limit)
	if err != nil {
		http.Error(w, "failed to read knowledge memory detail", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "knowledge memory detail not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"detail_type": detailType, "id": id, "item": item})
}

func findKnowledgeMemoryDetail(ctx context.Context, store KnowledgeMemoryLister, detailType string, id string, limit int) (any, bool, error) {
	switch strings.TrimSpace(detailType) {
	case "personal_archive":
		items, err := store.ListPersonalArchiveEntries(ctx, limit)
		if err != nil {
			return nil, false, err
		}
		for _, item := range items {
			if item.EntryID == id {
				return item, true, nil
			}
		}
	case "creative_knowledge":
		items, err := store.ListCreativeKnowledgeItems(ctx, limit)
		if err != nil {
			return nil, false, err
		}
		for _, item := range items {
			if item.ItemID == id {
				return item, true, nil
			}
		}
	case "news_knowledge":
		items, err := store.ListNewsKnowledgeItems(ctx, limit)
		if err != nil {
			return nil, false, err
		}
		for _, item := range items {
			if item.ItemID == id {
				return item, true, nil
			}
		}
	case "daily_intake_rule":
		items, err := store.ListDailyIntakeRules(ctx, limit)
		if err != nil {
			return nil, false, err
		}
		for _, item := range items {
			if item.RuleID == id {
				return item, true, nil
			}
		}
	case "temporal_marker":
		items, err := store.ListTemporalMemoryMarkers(ctx, limit)
		if err != nil {
			return nil, false, err
		}
		for _, item := range items {
			if item.MarkerID == id {
				return item, true, nil
			}
		}
	case "dream_run":
		items, err := store.ListDreamConsolidationRuns(ctx, limit)
		if err != nil {
			return nil, false, err
		}
		for _, item := range items {
			if string(item.RunID) == id {
				return item, true, nil
			}
		}
	default:
		return nil, false, nil
	}
	return nil, false, nil
}

func HandlePersonalArchiveCreate(store KnowledgeMemoryStore) http.HandlerFunc {
	return saveKnowledgeMemoryItem(store, "personal archive", func(ctx context.Context, store KnowledgeMemoryStore, dec *json.Decoder) error {
		var item domainkm.PersonalArchiveEntry
		if err := dec.Decode(&item); err != nil {
			return err
		}
		if item.CreatedAt.IsZero() {
			item.CreatedAt = time.Now().UTC()
		}
		return store.SavePersonalArchiveEntry(ctx, item)
	})
}

func HandleCreativeKnowledgeCreate(store KnowledgeMemoryStore) http.HandlerFunc {
	return saveKnowledgeMemoryItem(store, "creative knowledge", func(ctx context.Context, store KnowledgeMemoryStore, dec *json.Decoder) error {
		var item domainkm.CreativeKnowledgeItem
		if err := dec.Decode(&item); err != nil {
			return err
		}
		if item.CreatedAt.IsZero() {
			item.CreatedAt = time.Now().UTC()
		}
		return store.SaveCreativeKnowledgeItem(ctx, item)
	})
}

func HandleNewsKnowledgeCreate(store KnowledgeMemoryStore) http.HandlerFunc {
	return saveKnowledgeMemoryItem(store, "news knowledge", func(ctx context.Context, store KnowledgeMemoryStore, dec *json.Decoder) error {
		var item domainkm.NewsKnowledgeItem
		if err := dec.Decode(&item); err != nil {
			return err
		}
		if item.CreatedAt.IsZero() {
			item.CreatedAt = time.Now().UTC()
		}
		return store.SaveNewsKnowledgeItem(ctx, item)
	})
}

func HandleDailyIntakeRuleCreate(store KnowledgeMemoryStore) http.HandlerFunc {
	return saveKnowledgeMemoryItem(store, "daily intake rule", func(ctx context.Context, store KnowledgeMemoryStore, dec *json.Decoder) error {
		var item domainkm.DailyIntakeRule
		if err := dec.Decode(&item); err != nil {
			return err
		}
		if item.CreatedAt.IsZero() {
			item.CreatedAt = time.Now().UTC()
		}
		return store.SaveDailyIntakeRule(ctx, item)
	})
}

func HandleTemporalMemoryMarkerCreate(store KnowledgeMemoryStore) http.HandlerFunc {
	return saveKnowledgeMemoryItem(store, "temporal memory marker", func(ctx context.Context, store KnowledgeMemoryStore, dec *json.Decoder) error {
		var item domainkm.TemporalMemoryMarker
		if err := dec.Decode(&item); err != nil {
			return err
		}
		if item.CreatedAt.IsZero() {
			item.CreatedAt = time.Now().UTC()
		}
		return store.SaveTemporalMemoryMarker(ctx, item)
	})
}

func HandleDreamConsolidationRunCreate(store KnowledgeMemoryStore, verifier BrowserTraceRunVerifier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil || verifier == nil {
			http.Error(w, "knowledge memory store unavailable", http.StatusServiceUnavailable)
			return
		}
		defer r.Body.Close()
		var item domainkm.DreamConsolidationRun
		if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
			http.Error(w, "invalid dream consolidation run payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		if item.ReviewStatus == "adopted" {
			http.Error(w, "dream consolidation cannot be created with adopted review_status; use review API", http.StatusBadRequest)
			return
		}
		if !verifyDreamRunOwner(w, r, verifier, item.TaskID, item.RunID, item.ActorID) {
			return
		}
		if item.CreatedAt.IsZero() {
			item.CreatedAt = time.Now().UTC()
		}
		if err := store.SaveDreamConsolidationRun(r.Context(), item); err != nil {
			http.Error(w, "invalid dream consolidation run payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"status": "created"})
	}
}

func HandleDreamConsolidationProposalCreate(store KnowledgeMemoryStore, verifier BrowserTraceRunVerifier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil || verifier == nil {
			http.Error(w, "knowledge memory store unavailable", http.StatusServiceUnavailable)
			return
		}
		var input kmapp.DreamProposalInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, "invalid dream consolidation proposal payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		if !verifyDreamRunOwner(w, r, verifier, input.TaskID, input.RunID, input.ActorID) {
			return
		}
		run, err := kmapp.BuildDreamConsolidationProposal(r.Context(), store, input)
		if err != nil {
			http.Error(w, "failed to create dream consolidation proposal: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"status": "created", "dream_run": run})
	}
}

// verifyDreamRunOwner keeps dream consolidation writes on the Task-owned
// identity: the caller must present an existing Task/Run pair assigned to the
// requesting CORE actor instead of minting its own run identifier.
func verifyDreamRunOwner(w http.ResponseWriter, r *http.Request, verifier BrowserTraceRunVerifier, taskID modulecore.TaskID, runID modulecore.RunID, actorID string) bool {
	if err := taskID.Validate(); err != nil {
		http.Error(w, "task_id is invalid", http.StatusBadRequest)
		return false
	}
	if err := runID.Validate(); err != nil {
		http.Error(w, "run_id is invalid", http.StatusBadRequest)
		return false
	}
	if !isCoreActorID(actorID) {
		http.Error(w, "actor_id is invalid", http.StatusBadRequest)
		return false
	}
	assignee, err := verifier.VerifyTaskRun(r.Context(), taskID, runID)
	if err != nil {
		http.Error(w, "dream consolidation task/run ownership verification failed", http.StatusForbidden)
		return false
	}
	if assignee != "" && assignee != actorID {
		http.Error(w, "dream consolidation actor does not own run", http.StatusForbidden)
		return false
	}
	return true
}

func HandleDreamConsolidationReview(store KnowledgeMemoryStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "knowledge memory store unavailable", http.StatusServiceUnavailable)
			return
		}
		var req DreamConsolidationReviewRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid dream consolidation review payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		req.RunID = modulecore.RunID(strings.TrimSpace(string(req.RunID)))
		req.ReviewStatus = strings.TrimSpace(req.ReviewStatus)
		if err := req.RunID.Validate(); err != nil {
			http.Error(w, "run_id is invalid", http.StatusBadRequest)
			return
		}
		if req.ReviewStatus == "" {
			http.Error(w, "review_status is required", http.StatusBadRequest)
			return
		}
		if req.ReviewStatus != "adopted" && req.ReviewStatus != "rejected" {
			http.Error(w, "review_status must be adopted or rejected", http.StatusBadRequest)
			return
		}
		run, ok, err := findDreamConsolidationRun(r.Context(), store, req.RunID, 100)
		if err != nil {
			http.Error(w, "failed to load dream consolidation run", http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, "dream consolidation run not found", http.StatusNotFound)
			return
		}
		if req.Promote && req.ReviewStatus != "adopted" {
			http.Error(w, "promote requires adopted review_status", http.StatusBadRequest)
			return
		}
		run.ReviewStatus = req.ReviewStatus
		if req.ReviewStatus == "rejected" {
			run.Status = "rejected"
		} else if req.Promote {
			run.Status = "promoted"
		} else {
			run.Status = "reviewed"
		}
		if err := store.SaveDreamConsolidationRun(r.Context(), run); err != nil {
			http.Error(w, "failed to save dream consolidation review: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"status":       "reviewed",
			"dream_run":    run,
			"promoted":     req.Promote,
			"auto_promote": false,
		})
	}
}

func findDreamConsolidationRun(ctx context.Context, store KnowledgeMemoryLister, runID modulecore.RunID, limit int) (domainkm.DreamConsolidationRun, bool, error) {
	items, err := store.ListDreamConsolidationRuns(ctx, limit)
	if err != nil {
		return domainkm.DreamConsolidationRun{}, false, err
	}
	for _, item := range items {
		if item.RunID == runID {
			return item, true, nil
		}
	}
	return domainkm.DreamConsolidationRun{}, false, nil
}

func saveKnowledgeMemoryItem(store KnowledgeMemoryStore, name string, save func(context.Context, KnowledgeMemoryStore, *json.Decoder) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "knowledge memory store unavailable", http.StatusServiceUnavailable)
			return
		}
		if err := save(r.Context(), store, json.NewDecoder(r.Body)); err != nil {
			http.Error(w, "invalid "+name+" payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"status": "created"})
	}
}

func nonNilPersonalArchive(items []domainkm.PersonalArchiveEntry) []domainkm.PersonalArchiveEntry {
	if items == nil {
		return []domainkm.PersonalArchiveEntry{}
	}
	return items
}

func nonNilCreativeKnowledge(items []domainkm.CreativeKnowledgeItem) []domainkm.CreativeKnowledgeItem {
	if items == nil {
		return []domainkm.CreativeKnowledgeItem{}
	}
	return items
}

func nonNilNewsKnowledge(items []domainkm.NewsKnowledgeItem) []domainkm.NewsKnowledgeItem {
	if items == nil {
		return []domainkm.NewsKnowledgeItem{}
	}
	return items
}

func nonNilDailyIntakeRules(items []domainkm.DailyIntakeRule) []domainkm.DailyIntakeRule {
	if items == nil {
		return []domainkm.DailyIntakeRule{}
	}
	return items
}

func nonNilTemporalMarkers(items []domainkm.TemporalMemoryMarker) []domainkm.TemporalMemoryMarker {
	if items == nil {
		return []domainkm.TemporalMemoryMarker{}
	}
	return items
}

func nonNilDreamRuns(items []domainkm.DreamConsolidationRun) []domainkm.DreamConsolidationRun {
	if items == nil {
		return []domainkm.DreamConsolidationRun{}
	}
	return items
}
