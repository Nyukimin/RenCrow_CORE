package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	domainworkstream "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/workstream"
)

type WorkstreamLister interface {
	ListWorkstreams(ctx context.Context, limit int) ([]domainworkstream.Workstream, error)
	ListGoals(ctx context.Context, limit int) ([]domainworkstream.Goal, error)
	ListArtifacts(ctx context.Context, limit int) ([]domainworkstream.Artifact, error)
	ListArtifactAnnotations(ctx context.Context, limit int) ([]domainworkstream.ArtifactAnnotation, error)
	ListSteeringItems(ctx context.Context, limit int) ([]domainworkstream.SteeringItem, error)
	ListHeartbeatSchedules(ctx context.Context, limit int) ([]domainworkstream.HeartbeatSchedule, error)
	ListVaultUpdateLogs(ctx context.Context, limit int) ([]domainworkstream.VaultUpdateLog, error)
}

type WorkstreamSaver interface {
	SaveWorkstream(ctx context.Context, item domainworkstream.Workstream) error
}

type WorkstreamStore interface {
	WorkstreamLister
	WorkstreamSaver
	SaveGoal(ctx context.Context, goal domainworkstream.Goal) error
	SaveArtifact(ctx context.Context, item domainworkstream.Artifact) error
	SaveArtifactAnnotation(ctx context.Context, item domainworkstream.ArtifactAnnotation) error
	SaveSteeringItem(ctx context.Context, item domainworkstream.SteeringItem) error
	SaveHeartbeatSchedule(ctx context.Context, item domainworkstream.HeartbeatSchedule) error
	SaveVaultUpdateLog(ctx context.Context, item domainworkstream.VaultUpdateLog) error
}

type WorkstreamVaultUpdateApplier interface {
	ApplyVaultUpdate(ctx context.Context, item domainworkstream.VaultUpdateLog) (string, error)
}

type WorkstreamVaultUpdatePreviewer interface {
	PreviewVaultUpdate(ctx context.Context, item domainworkstream.VaultUpdateLog) (*domainworkstream.VaultUpdatePreview, error)
}

func HandleWorkstreamStatus(store WorkstreamLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			handleWorkstreamCreate(w, r, store)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "workstream store unavailable", http.StatusServiceUnavailable)
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
		workstreams, err := store.ListWorkstreams(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load workstreams", http.StatusInternalServerError)
			return
		}
		goals, err := store.ListGoals(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load goals", http.StatusInternalServerError)
			return
		}
		artifacts, err := store.ListArtifacts(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load artifacts", http.StatusInternalServerError)
			return
		}
		annotations, err := store.ListArtifactAnnotations(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load artifact annotations", http.StatusInternalServerError)
			return
		}
		steering, err := store.ListSteeringItems(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load steering queue", http.StatusInternalServerError)
			return
		}
		heartbeats, err := store.ListHeartbeatSchedules(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load heartbeat schedules", http.StatusInternalServerError)
			return
		}
		vaultUpdates, err := store.ListVaultUpdateLogs(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load vault update logs", http.StatusInternalServerError)
			return
		}
		if workstreams == nil {
			workstreams = []domainworkstream.Workstream{}
		}
		if goals == nil {
			goals = []domainworkstream.Goal{}
		}
		if artifacts == nil {
			artifacts = []domainworkstream.Artifact{}
		}
		if annotations == nil {
			annotations = []domainworkstream.ArtifactAnnotation{}
		}
		if steering == nil {
			steering = []domainworkstream.SteeringItem{}
		}
		if heartbeats == nil {
			heartbeats = []domainworkstream.HeartbeatSchedule{}
		}
		if vaultUpdates == nil {
			vaultUpdates = []domainworkstream.VaultUpdateLog{}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"workstreams":   workstreams,
			"goals":         goals,
			"artifacts":     artifacts,
			"annotations":   annotations,
			"steering":      steering,
			"heartbeats":    heartbeats,
			"vault_updates": vaultUpdates,
		})
	}
}

func handleWorkstreamCreate(w http.ResponseWriter, r *http.Request, store WorkstreamLister) {
	saver, ok := store.(WorkstreamSaver)
	if !ok {
		http.Error(w, "workstream create unavailable", http.StatusServiceUnavailable)
		return
	}
	var item domainworkstream.Workstream
	if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
		http.Error(w, "invalid workstream payload", http.StatusBadRequest)
		return
	}
	if item.Status == "" {
		item.Status = domainworkstream.StatusDraft
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now().UTC()
	}
	if err := saver.SaveWorkstream(r.Context(), item); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"workstream": item,
	})
}

func HandleWorkstreamGoalCreate(store WorkstreamStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var item domainworkstream.Goal
		if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
			http.Error(w, "invalid workstream goal payload", http.StatusBadRequest)
			return
		}
		if item.Status == "" {
			item.Status = domainworkstream.StatusDraft
		}
		if item.CreatedAt.IsZero() {
			item.CreatedAt = time.Now().UTC()
		}
		if err := store.SaveGoal(r.Context(), item); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"goal": item})
	}
}

func HandleWorkstreamArtifactCreate(store WorkstreamStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var item domainworkstream.Artifact
		if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
			http.Error(w, "invalid workstream artifact payload", http.StatusBadRequest)
			return
		}
		if item.Status == "" {
			item.Status = "draft"
		}
		if item.CreatedAt.IsZero() {
			item.CreatedAt = time.Now().UTC()
		}
		if err := store.SaveArtifact(r.Context(), item); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"artifact": item})
	}
}

func HandleWorkstreamAnnotationCreate(store WorkstreamStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var item domainworkstream.ArtifactAnnotation
		if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
			http.Error(w, "invalid workstream annotation payload", http.StatusBadRequest)
			return
		}
		if item.Status == "" {
			item.Status = "open"
		}
		if item.CreatedAt.IsZero() {
			item.CreatedAt = time.Now().UTC()
		}
		steering, err := steeringItemFromAnnotation(r.Context(), store, item)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := store.SaveArtifactAnnotation(r.Context(), item); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if steering != nil {
			if err := store.SaveSteeringItem(r.Context(), *steering); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"annotation": item,
			"steering":   steering,
		})
	}
}

func steeringItemFromAnnotation(ctx context.Context, store WorkstreamStore, item domainworkstream.ArtifactAnnotation) (*domainworkstream.SteeringItem, error) {
	artifacts, err := store.ListArtifacts(ctx, 100)
	if err != nil {
		return nil, err
	}
	for _, artifact := range artifacts {
		if artifact.ArtifactID != item.ArtifactID {
			continue
		}
		if artifact.WorkstreamID == "" {
			return nil, nil
		}
		steering := domainworkstream.SteeringItem{
			SteeringID:       "stq_from_" + item.AnnotationID,
			WorkstreamID:     artifact.WorkstreamID,
			TargetArtifactID: item.ArtifactID,
			Instruction:      item.Comment,
			Priority:         "normal",
			Status:           "pending",
			CreatedAt:        item.CreatedAt,
		}
		return &steering, domainworkstream.ValidateSteeringItem(steering)
	}
	return nil, nil
}

func HandleWorkstreamSteeringCreate(store WorkstreamStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var item domainworkstream.SteeringItem
		if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
			http.Error(w, "invalid workstream steering payload", http.StatusBadRequest)
			return
		}
		if item.Status == "" {
			item.Status = "pending"
		}
		if item.CreatedAt.IsZero() {
			item.CreatedAt = time.Now().UTC()
		}
		if err := store.SaveSteeringItem(r.Context(), item); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"steering": item})
	}
}

func HandleWorkstreamHeartbeatCreate(store WorkstreamStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var item domainworkstream.HeartbeatSchedule
		if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
			http.Error(w, "invalid workstream heartbeat payload", http.StatusBadRequest)
			return
		}
		if item.Status == "" {
			item.Status = domainworkstream.StatusActive
		}
		if item.CreatedAt.IsZero() {
			item.CreatedAt = time.Now().UTC()
		}
		if err := store.SaveHeartbeatSchedule(r.Context(), item); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"heartbeat": item})
	}
}

func HandleWorkstreamVaultUpdateCreate(store WorkstreamStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var item domainworkstream.VaultUpdateLog
		if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
			http.Error(w, "invalid workstream vault update payload", http.StatusBadRequest)
			return
		}
		if item.ReviewStatus == "" {
			item.ReviewStatus = domainworkstream.VaultReviewPending
		}
		if item.CreatedAt.IsZero() {
			item.CreatedAt = time.Now().UTC()
		}
		if err := store.SaveVaultUpdateLog(r.Context(), item); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"vault_update": item})
	}
}

func HandleWorkstreamVaultUpdateReview(store WorkstreamStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var item domainworkstream.VaultUpdateLog
		if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
			http.Error(w, "invalid workstream vault update review payload", http.StatusBadRequest)
			return
		}
		if item.CreatedAt.IsZero() {
			item.CreatedAt = time.Now().UTC()
		}
		if err := domainworkstream.ValidateVaultUpdateReview(item); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		applied := false
		appliedPath := ""
		if item.ReviewStatus == domainworkstream.VaultReviewApproved && item.ProposedContent != "" {
			applier, ok := store.(WorkstreamVaultUpdateApplier)
			if !ok {
				http.Error(w, "vault update apply unavailable", http.StatusServiceUnavailable)
				return
			}
			path, err := applier.ApplyVaultUpdate(r.Context(), item)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			applied = true
			appliedPath = path
			item.Applied = true
			item.AppliedPath = path
		}
		if err := store.SaveVaultUpdateLog(r.Context(), item); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"vault_update": item,
			"applied":      applied,
			"applied_path": appliedPath,
		})
	}
}

func HandleWorkstreamVaultUpdatePreview(store WorkstreamStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		previewer, ok := store.(WorkstreamVaultUpdatePreviewer)
		if !ok {
			http.Error(w, "vault update preview unavailable", http.StatusServiceUnavailable)
			return
		}
		var item domainworkstream.VaultUpdateLog
		if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
			http.Error(w, "invalid workstream vault update preview payload", http.StatusBadRequest)
			return
		}
		if item.CreatedAt.IsZero() {
			item.CreatedAt = time.Now().UTC()
		}
		if err := domainworkstream.ValidateVaultUpdateLog(item); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		preview, err := previewer.PreviewVaultUpdate(r.Context(), item)
		if err != nil {
			http.Error(w, "failed to preview vault update: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"preview": preview})
	}
}
