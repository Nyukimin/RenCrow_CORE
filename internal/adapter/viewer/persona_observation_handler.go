package viewer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	domainpersona "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/persona"
)

type PersonaObservationLister interface {
	ListDiscomfortLogs(ctx context.Context, limit int) ([]domainpersona.DiscomfortLog, error)
	ListTriggerLogs(ctx context.Context, limit int) ([]domainpersona.TriggerLog, error)
	ListCanonicalResponseLogs(ctx context.Context, limit int) ([]domainpersona.CanonicalResponseLog, error)
	ListObservationLogs(ctx context.Context, limit int) ([]domainpersona.ObservationLog, error)
	ListMetaProfileUpdates(ctx context.Context, limit int) ([]domainpersona.MetaProfileUpdate, error)
	ListInterfaceSessions(ctx context.Context, limit int) ([]domainpersona.InterfaceSession, error)
}

type PersonaObservationStore interface {
	PersonaObservationLister
	SaveDiscomfortLog(ctx context.Context, item domainpersona.DiscomfortLog) error
	SaveTriggerLog(ctx context.Context, item domainpersona.TriggerLog) error
	SaveCanonicalResponseLog(ctx context.Context, item domainpersona.CanonicalResponseLog) error
	SaveObservationLog(ctx context.Context, item domainpersona.ObservationLog) error
	SaveMetaProfileUpdate(ctx context.Context, item domainpersona.MetaProfileUpdate) error
	SaveInterfaceSession(ctx context.Context, item domainpersona.InterfaceSession) error
}

type PersonaMetaProfileUpdateApplier interface {
	ApplyMetaProfileUpdate(ctx context.Context, item domainpersona.MetaProfileUpdate) (string, error)
}

func HandlePersonaObservationStatus(store PersonaObservationLister, characters ...map[string]domainpersona.CharacterProfile) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "persona observation store unavailable", http.StatusServiceUnavailable)
			return
		}
		limit, err := parseViewerLimit(r.URL.Query().Get("limit"), 20, 100)
		if err != nil {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		discomforts, err := store.ListDiscomfortLogs(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load persona discomfort logs", http.StatusInternalServerError)
			return
		}
		triggers, err := store.ListTriggerLogs(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load persona trigger logs", http.StatusInternalServerError)
			return
		}
		canonicals, err := store.ListCanonicalResponseLogs(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load canonical response logs", http.StatusInternalServerError)
			return
		}
		observations, err := store.ListObservationLogs(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load observation logs", http.StatusInternalServerError)
			return
		}
		metaUpdates, err := store.ListMetaProfileUpdates(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load meta profile updates", http.StatusInternalServerError)
			return
		}
		sessions, err := store.ListInterfaceSessions(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load persona interface sessions", http.StatusInternalServerError)
			return
		}
		if discomforts == nil {
			discomforts = []domainpersona.DiscomfortLog{}
		}
		if triggers == nil {
			triggers = []domainpersona.TriggerLog{}
		}
		if canonicals == nil {
			canonicals = []domainpersona.CanonicalResponseLog{}
		}
		if observations == nil {
			observations = []domainpersona.ObservationLog{}
		}
		if metaUpdates == nil {
			metaUpdates = []domainpersona.MetaProfileUpdate{}
		}
		if sessions == nil {
			sessions = []domainpersona.InterfaceSession{}
		}
		characterProfiles := map[string]domainpersona.CharacterProfile{}
		if len(characters) > 0 && characters[0] != nil {
			characterProfiles = characters[0]
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"discomfort_logs":         discomforts,
			"trigger_logs":            triggers,
			"canonical_response_logs": canonicals,
			"observation_logs":        observations,
			"meta_profile_updates":    metaUpdates,
			"interface_sessions":      sessions,
			"characters":              characterProfiles,
		})
	}
}

func HandlePersonaDiscomfortCreate(store PersonaObservationStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var item domainpersona.DiscomfortLog
		if !decodePersonaObservationPost(w, r, &item, store) {
			return
		}
		if item.Status == "" {
			item.Status = "candidate"
		}
		if item.CreatedAt.IsZero() {
			item.CreatedAt = time.Now().UTC()
		}
		if err := store.SaveDiscomfortLog(r.Context(), item); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"discomfort_log": item})
	}
}

func HandlePersonaTriggerLogCreate(store PersonaObservationStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var item domainpersona.TriggerLog
		if !decodePersonaObservationPost(w, r, &item, store) {
			return
		}
		if item.CreatedAt.IsZero() {
			item.CreatedAt = time.Now().UTC()
		}
		if err := store.SaveTriggerLog(r.Context(), item); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"trigger_log": item})
	}
}

func HandlePersonaCanonicalResponseLogCreate(store PersonaObservationStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var item domainpersona.CanonicalResponseLog
		if !decodePersonaObservationPost(w, r, &item, store) {
			return
		}
		if item.CreatedAt.IsZero() {
			item.CreatedAt = time.Now().UTC()
		}
		if err := store.SaveCanonicalResponseLog(r.Context(), item); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"canonical_response_log": item})
	}
}

func HandlePersonaObservationLogCreate(store PersonaObservationStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var item domainpersona.ObservationLog
		if !decodePersonaObservationPost(w, r, &item, store) {
			return
		}
		if item.Sensitivity == "" {
			item.Sensitivity = "normal"
		}
		if item.ReviewStatus == "" {
			item.ReviewStatus = "pending"
		}
		if item.CreatedAt.IsZero() {
			item.CreatedAt = time.Now().UTC()
		}
		if err := store.SaveObservationLog(r.Context(), item); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"observation_log": item})
	}
}

func HandlePersonaMetaProfileUpdateCreate(store PersonaObservationStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var item domainpersona.MetaProfileUpdate
		if !decodePersonaObservationPost(w, r, &item, store) {
			return
		}
		if item.Sensitivity == "" {
			item.Sensitivity = "normal"
		}
		item.ReviewStatus = "pending"
		if item.CreatedAt.IsZero() {
			item.CreatedAt = time.Now().UTC()
		}
		if err := store.SaveMetaProfileUpdate(r.Context(), item); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"meta_profile_update": item})
	}
}

func HandlePersonaMetaProfileUpdateReview(store PersonaObservationStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var item domainpersona.MetaProfileUpdate
		if !decodePersonaObservationPost(w, r, &item, store) {
			return
		}
		if item.Sensitivity == "" {
			item.Sensitivity = "normal"
		}
		if item.CreatedAt.IsZero() {
			item.CreatedAt = time.Now().UTC()
		}
		if item.ReviewedAt.IsZero() {
			item.ReviewedAt = time.Now().UTC()
		}
		if err := domainpersona.ValidateMetaProfileUpdateReview(item); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		applied := false
		appliedPath := ""
		if item.ReviewStatus == "approved" {
			applier, ok := store.(PersonaMetaProfileUpdateApplier)
			if !ok {
				http.Error(w, "persona meta profile apply unavailable", http.StatusServiceUnavailable)
				return
			}
			path, err := applier.ApplyMetaProfileUpdate(r.Context(), item)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			applied = true
			appliedPath = path
		}
		if err := store.SaveMetaProfileUpdate(r.Context(), item); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"meta_profile_update": item,
			"applied":             applied,
			"applied_path":        appliedPath,
		})
	}
}

func HandlePersonaInterfaceSessionCreate(store PersonaObservationStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var item domainpersona.InterfaceSession
		if !decodePersonaObservationPost(w, r, &item, store) {
			return
		}
		if item.CreatedAt.IsZero() {
			item.CreatedAt = time.Now().UTC()
		}
		if err := store.SaveInterfaceSession(r.Context(), item); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"interface_session": item})
	}
}

func HandlePersonaObservationAggregate(store PersonaObservationStore) http.HandlerFunc {
	type request struct {
		ObserverID string `json:"observer_id"`
		TargetID   string `json:"target_id"`
		Period     string `json:"period"`
		Limit      int    `json:"limit,omitempty"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "persona observation store unavailable", http.StatusServiceUnavailable)
			return
		}
		defer r.Body.Close()
		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid persona observation aggregate payload", http.StatusBadRequest)
			return
		}
		period := strings.ToLower(strings.TrimSpace(req.Period))
		if period != "daily" && period != "weekly" && period != "monthly" {
			http.Error(w, "period must be daily, weekly, or monthly", http.StatusBadRequest)
			return
		}
		req.ObserverID = strings.TrimSpace(req.ObserverID)
		req.TargetID = strings.TrimSpace(req.TargetID)
		if req.ObserverID == "" || req.TargetID == "" {
			http.Error(w, "observer_id and target_id are required", http.StatusBadRequest)
			return
		}
		limit := req.Limit
		if limit <= 0 {
			limit = 50
		}
		if limit > 200 {
			limit = 200
		}
		observations, err := store.ListObservationLogs(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load observation logs", http.StatusInternalServerError)
			return
		}
		source := filterPersonaObservations(observations, req.ObserverID, req.TargetID)
		now := time.Now().UTC()
		summary := buildPersonaObservationSummary(period, source)
		evidence := personaObservationEvidenceRefs(source)
		summaryLog := domainpersona.ObservationLog{
			EventID:         fmt.Sprintf("evt_persona_%s_summary_%d", period, now.UnixNano()),
			ObserverID:      req.ObserverID,
			TargetID:        req.TargetID,
			ObservationType: period + "_summary",
			Summary:         summary,
			EvidenceRefs:    evidence,
			Sensitivity:     "normal",
			ReviewStatus:    "pending",
			CreatedAt:       now,
		}
		if err := store.SaveObservationLog(r.Context(), summaryLog); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		section := "Recent Changes"
		if period == "weekly" || period == "monthly" {
			section = "Stable Traits"
		}
		metaUpdate := domainpersona.MetaProfileUpdate{
			UpdateID:        fmt.Sprintf("meta_%s_summary_%d", period, now.UnixNano()),
			ObserverID:      req.ObserverID,
			TargetID:        req.TargetID,
			Section:         section,
			ProposedContent: summary,
			EvidenceRefs:    evidence,
			Sensitivity:     "normal",
			ReviewStatus:    "pending",
			CreatedAt:       now,
		}
		if err := store.SaveMetaProfileUpdate(r.Context(), metaUpdate); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"summary_observation": summaryLog,
			"meta_profile_update": metaUpdate,
			"source_count":        len(source),
			"auto_approved":       false,
		})
	}
}

func decodePersonaObservationPost(w http.ResponseWriter, r *http.Request, out any, store PersonaObservationStore) bool {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	if store == nil {
		http.Error(w, "persona observation store unavailable", http.StatusServiceUnavailable)
		return false
	}
	if err := json.NewDecoder(r.Body).Decode(out); err != nil {
		http.Error(w, "invalid persona observation payload", http.StatusBadRequest)
		return false
	}
	return true
}

func filterPersonaObservations(items []domainpersona.ObservationLog, observerID string, targetID string) []domainpersona.ObservationLog {
	out := make([]domainpersona.ObservationLog, 0, len(items))
	for _, item := range items {
		if item.ObserverID != observerID || item.TargetID != targetID {
			continue
		}
		if strings.HasSuffix(item.ObservationType, "_summary") {
			continue
		}
		out = append(out, item)
	}
	return out
}

func buildPersonaObservationSummary(period string, items []domainpersona.ObservationLog) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s observation summary\n\n", period)
	if len(items) == 0 {
		fmt.Fprintf(&b, "No source observations were found for this target.\n")
		return b.String()
	}
	for i, item := range items {
		if i >= 12 {
			fmt.Fprintf(&b, "\n... %d more observations omitted from summary draft\n", len(items)-i)
			break
		}
		summary := strings.TrimSpace(item.Summary)
		if summary == "" {
			summary = "(summary empty)"
		}
		fmt.Fprintf(&b, "- [%s] %s\n", item.ObservationType, summary)
	}
	fmt.Fprintf(&b, "\nReview required before updating stable meta profile.\n")
	return b.String()
}

func personaObservationEvidenceRefs(items []domainpersona.ObservationLog) []string {
	refs := make([]string, 0, len(items))
	for _, item := range items {
		if item.EventID == "" {
			continue
		}
		refs = append(refs, item.EventID)
	}
	return refs
}
