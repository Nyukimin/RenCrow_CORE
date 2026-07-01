package viewer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	domconv "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/conversation"
	conversationpersistence "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/conversation"
)

type MemoryLayerHotStore interface {
	RecentBySession(ctx context.Context, sessionID string, limit int) ([]conversationpersistence.L1MemoryEvent, error)
	RecentByNamespace(ctx context.Context, namespace string, limit int) ([]conversationpersistence.L1MemoryEvent, error)
	RecentByState(ctx context.Context, memoryState string, limit int) ([]conversationpersistence.L1MemoryEvent, error)
}

type MemoryLayerColdStore interface {
	GetSessionHistory(ctx context.Context, sessionID string, limit int) ([]*domconv.ThreadSummary, error)
	SearchByDomain(ctx context.Context, domain string, limit int) ([]*domconv.ThreadSummary, error)
	ListKBDocuments(ctx context.Context, domain string, limit int) ([]*domconv.Document, error)
}

func HandleMemoryLayers(hot MemoryLayerHotStore, cold MemoryLayerColdStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if hot == nil {
			http.Error(w, "memory layers unavailable", http.StatusServiceUnavailable)
			return
		}
		limit, err := parseViewerLimit(r.URL.Query().Get("limit"), 12, 50)
		if err != nil {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
		namespace := strings.TrimSpace(r.URL.Query().Get("namespace"))
		domain := strings.TrimSpace(r.URL.Query().Get("domain"))

		out := map[string]any{
			"session_id": sessionID,
			"namespace":  namespace,
			"domain":     domain,
			"l0":         []conversationpersistence.L1MemoryEvent{},
			"l1":         []conversationpersistence.L1MemoryEvent{},
			"l2":         []*domconv.ThreadSummary{},
			"l3":         []conversationpersistence.L1MemoryEvent{},
			"l3_qdrant":  []*domconv.Document{},
		}
		if sessionID != "" {
			l0, err := hot.RecentBySession(r.Context(), sessionID, limit)
			if err != nil {
				http.Error(w, "failed to load l0 memory", http.StatusInternalServerError)
				return
			}
			out["l0"] = l0
		}
		if namespace != "" {
			l1, err := hot.RecentByNamespace(r.Context(), namespace, limit)
			if err != nil {
				http.Error(w, "failed to load l1 memory", http.StatusInternalServerError)
				return
			}
			out["l1"] = l1
		}
		if cold != nil {
			var l2 []*domconv.ThreadSummary
			if sessionID != "" {
				history, err := cold.GetSessionHistory(r.Context(), sessionID, limit)
				if err != nil {
					http.Error(w, "failed to load l2 session memory", http.StatusInternalServerError)
					return
				}
				l2 = append(l2, history...)
			}
			if domain != "" {
				byDomain, err := cold.SearchByDomain(r.Context(), domain, limit)
				if err != nil {
					http.Error(w, "failed to load l2 domain memory", http.StatusInternalServerError)
					return
				}
				l2 = append(l2, byDomain...)
				kbDocs, err := cold.ListKBDocuments(r.Context(), domain, limit)
				if err != nil {
					http.Error(w, "failed to load l3 qdrant memory", http.StatusInternalServerError)
					return
				}
				out["l3_qdrant"] = kbDocs
			}
			out["l2"] = l2
		}
		l3, err := hot.RecentByState(r.Context(), conversationpersistence.MemoryStateConfirmed, limit)
		if err != nil {
			http.Error(w, "failed to load l3 memory", http.StatusInternalServerError)
			return
		}
		out["l3"] = l3
		if err := validateMemoryLayersSnapshot(out); err != nil {
			http.Error(w, "invalid memory layers snapshot: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}
}

func validateMemoryLayersSnapshot(snapshot map[string]any) error {
	for _, layer := range []string{"l0", "l1", "l3"} {
		items, ok := snapshot[layer].([]conversationpersistence.L1MemoryEvent)
		if !ok {
			return fmt.Errorf("%s memory snapshot has invalid type", layer)
		}
		for _, item := range items {
			if err := validateMemoryLayerEventSnapshot(layer, item); err != nil {
				return err
			}
		}
	}
	summaries, ok := snapshot["l2"].([]*domconv.ThreadSummary)
	if !ok {
		return fmt.Errorf("l2 memory snapshot has invalid type")
	}
	for _, item := range summaries {
		if item == nil {
			return fmt.Errorf("l2 summary is nil")
		}
		if item.ThreadID <= 0 {
			return fmt.Errorf("l2 summary missing thread_id")
		}
		if strings.TrimSpace(item.Summary) == "" {
			return fmt.Errorf("l2 summary missing summary")
		}
	}
	docs, ok := snapshot["l3_qdrant"].([]*domconv.Document)
	if !ok {
		return fmt.Errorf("l3_qdrant snapshot has invalid type")
	}
	for _, item := range docs {
		if item == nil {
			return fmt.Errorf("l3_qdrant document is nil")
		}
		if strings.TrimSpace(item.ID) == "" {
			return fmt.Errorf("l3_qdrant document missing id")
		}
		if strings.TrimSpace(item.Domain) == "" {
			return fmt.Errorf("l3_qdrant document missing domain")
		}
		if strings.TrimSpace(item.Content) == "" {
			return fmt.Errorf("l3_qdrant document missing content")
		}
		if item.CreatedAt.IsZero() {
			return fmt.Errorf("l3_qdrant document missing created_at")
		}
		if item.UpdatedAt.IsZero() {
			return fmt.Errorf("l3_qdrant document missing updated_at")
		}
	}
	return nil
}

func validateMemoryLayerEventSnapshot(layer string, item conversationpersistence.L1MemoryEvent) error {
	if strings.TrimSpace(item.ID) == "" {
		return fmt.Errorf("%s memory missing id", layer)
	}
	if strings.TrimSpace(item.Message) == "" {
		return fmt.Errorf("%s memory missing message", layer)
	}
	if strings.TrimSpace(item.Layer) == "" {
		return fmt.Errorf("%s memory missing layer", layer)
	}
	if item.CreatedAt.IsZero() {
		return fmt.Errorf("%s memory missing created_at", layer)
	}
	return nil
}
