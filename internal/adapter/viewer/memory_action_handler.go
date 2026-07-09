package viewer

import (
	"context"
	"encoding/json"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"net/http"
	"strings"
)

type MemoryActionStore interface {
	UpdateMemoryState(ctx context.Context, id string, memoryState string) error
	PromoteMemoryToNamespace(ctx context.Context, id string, targetNamespace string, promotedBy string) (*l1sqlite.L1MemoryEvent, error)
}

func HandleMemoryState(store MemoryActionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireViewerMethod(w, r, http.MethodPost) {
			return
		}
		if !requireViewerStore(w, store == nil, "memory action unavailable") {
			return
		}
		var req struct {
			ID          string `json:"id"`
			MemoryState string `json:"memory_state"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		req.ID = strings.TrimSpace(req.ID)
		req.MemoryState = strings.TrimSpace(req.MemoryState)
		if req.ID == "" || req.MemoryState == "" {
			http.Error(w, "id and memory_state are required", http.StatusBadRequest)
			return
		}
		if err := store.UpdateMemoryState(r.Context(), req.ID, req.MemoryState); err != nil {
			http.Error(w, "failed to update memory state", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

func HandleMemoryPromote(store MemoryActionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireViewerMethod(w, r, http.MethodPost) {
			return
		}
		if !requireViewerStore(w, store == nil, "memory action unavailable") {
			return
		}
		var req struct {
			ID              string `json:"id"`
			TargetNamespace string `json:"target_namespace"`
			TargetKind      string `json:"target_kind"`
			TargetID        string `json:"target_id"`
			PromotedBy      string `json:"promoted_by"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		req.ID = strings.TrimSpace(req.ID)
		req.TargetNamespace = strings.TrimSpace(req.TargetNamespace)
		req.TargetKind = strings.TrimSpace(req.TargetKind)
		req.TargetID = strings.TrimSpace(req.TargetID)
		req.PromotedBy = strings.TrimSpace(req.PromotedBy)
		if req.PromotedBy == "" {
			req.PromotedBy = "viewer"
		}
		if req.TargetNamespace == "" && req.TargetKind != "" && req.TargetID != "" {
			namespace, err := l1sqlite.BuildL1Namespace(req.TargetKind, req.TargetID)
			if err != nil {
				http.Error(w, "invalid target namespace", http.StatusBadRequest)
				return
			}
			req.TargetNamespace = namespace
		}
		if req.ID == "" || req.TargetNamespace == "" {
			http.Error(w, "id and target namespace are required", http.StatusBadRequest)
			return
		}
		if err := l1sqlite.ValidateL1Namespace(req.TargetNamespace); err != nil {
			http.Error(w, "invalid target namespace", http.StatusBadRequest)
			return
		}
		item, err := store.PromoteMemoryToNamespace(r.Context(), req.ID, req.TargetNamespace, req.PromotedBy)
		if err != nil {
			http.Error(w, "failed to promote memory", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"item": memoryEventDTOFromL1Ptr(item)})
	}
}
