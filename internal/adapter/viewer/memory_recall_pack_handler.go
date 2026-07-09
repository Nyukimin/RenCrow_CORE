package viewer

import (
	"fmt"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/conversation/l1sqlite"
	"net/http"
	"strings"
	"time"

	domainmemory "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/memory"
)

func HandleMemoryRecallPack(hot MemoryLayerHotStore, cold MemoryLayerColdStore, users UserMemoryStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireViewerMethod(w, r, http.MethodGet) {
			return
		}
		if !requireViewerStore(w, hot == nil, "memory recall pack unavailable") {
			return
		}
		limit, err := parseViewerLimit(r.URL.Query().Get("limit"), 12, 50)
		if err != nil {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
		userID := strings.TrimSpace(r.URL.Query().Get("user_id"))
		if userID == "" {
			userID = "ren"
		}
		domain := strings.TrimSpace(r.URL.Query().Get("domain"))
		pack := domainmemory.UserMemoryRecallView{
			SessionID: sessionID,
			UserID:    userID,
			Items:     []domainmemory.UserMemoryRecallItem{},
			CreatedAt: time.Now().UTC(),
		}
		if sessionID != "" {
			l0, err := hot.RecentBySession(r.Context(), sessionID, limit)
			if err != nil {
				http.Error(w, "failed to load l0 recall", http.StatusInternalServerError)
				return
			}
			for _, ev := range l0 {
				pack.Items = append(pack.Items, recallItemFromL1Event("L0", "short_context", ev, 0.75))
			}
		}
		if users != nil {
			userMemories, err := users.ListUserMemories(r.Context(), userID, "", false, limit)
			if err != nil {
				http.Error(w, "failed to load user memory recall", http.StatusInternalServerError)
				return
			}
			for _, mem := range userMemories {
				if mem.Sensitivity == "sensitive" {
					continue
				}
				switch mem.State {
				case domainmemory.MemoryStateConfirmed, domainmemory.MemoryStatePinned:
					pack.Items = append(pack.Items, domainmemory.UserMemoryRecallItem{
						Layer:       "UserMemory",
						Namespace:   mem.Namespace,
						MemoryID:    mem.ID,
						Kind:        mem.Type,
						Summary:     mem.Statement,
						Score:       scoreForUserMemory(mem),
						State:       mem.State,
						EventIDs:    mem.EvidenceEventIDs,
						Sensitivity: mem.Sensitivity,
					})
				}
			}
		}
		if cold != nil {
			if sessionID != "" {
				history, err := cold.GetSessionHistory(r.Context(), sessionID, limit)
				if err != nil {
					http.Error(w, "failed to load l2 recall", http.StatusInternalServerError)
					return
				}
				for _, summary := range history {
					if summary == nil || strings.TrimSpace(summary.Summary) == "" {
						continue
					}
					pack.Items = append(pack.Items, domainmemory.UserMemoryRecallItem{
						Layer:     "L2",
						Namespace: "conv:" + sessionID,
						MemoryID:  fmt.Sprintf("thread:%d", summary.ThreadID),
						Kind:      "thread_summary",
						Summary:   summary.Summary,
						Score:     0.55,
						State:     domainmemory.MemoryStateConfirmed,
					})
				}
			}
			if domain != "" {
				docs, err := cold.ListKBDocuments(r.Context(), domain, limit)
				if err != nil {
					http.Error(w, "failed to load knowledge recall", http.StatusInternalServerError)
					return
				}
				for _, doc := range docs {
					if doc == nil || strings.TrimSpace(doc.Content) == "" {
						continue
					}
					pack.Items = append(pack.Items, domainmemory.UserMemoryRecallItem{
						Layer:     "Knowledge",
						Namespace: "kb:" + doc.Domain,
						MemoryID:  doc.ID,
						Kind:      "knowledge",
						Summary:   doc.Content,
						Score:     0.45,
						State:     domainmemory.MemoryStateConfirmed,
						SourceID:  doc.Source,
					})
				}
			}
		}
		writeJSON(w, http.StatusOK, pack)
	}
}

func recallItemFromL1Event(layer string, kind string, ev l1sqlite.L1MemoryEvent, score float64) domainmemory.UserMemoryRecallItem {
	return domainmemory.UserMemoryRecallItem{
		Layer:       layer,
		Namespace:   ev.Namespace,
		MemoryID:    ev.ID,
		Kind:        kind,
		Summary:     ev.Message,
		Score:       score,
		State:       ev.MemoryState,
		SourceID:    metaStringForRecall(ev.Meta, "source_id"),
		EventIDs:    metaStringSliceForRecall(ev.Meta, "evidence_event_ids"),
		Sensitivity: metaStringForRecall(ev.Meta, "sensitivity"),
	}
}

func scoreForUserMemory(mem domainmemory.UserMemory) float64 {
	if mem.State == domainmemory.MemoryStatePinned {
		return 1.0
	}
	if mem.Confidence > 0 {
		return mem.Confidence
	}
	return 0.8
}

func metaStringForRecall(meta map[string]interface{}, key string) string {
	if meta == nil {
		return ""
	}
	if raw, ok := meta[key]; ok {
		if s, ok := raw.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func metaStringSliceForRecall(meta map[string]interface{}, key string) []string {
	if meta == nil {
		return nil
	}
	raw, ok := meta[key]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		return append([]string(nil), v...)
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	default:
		return nil
	}
}
