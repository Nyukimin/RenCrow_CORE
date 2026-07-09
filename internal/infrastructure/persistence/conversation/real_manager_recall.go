package conversation

import (
	"context"
	"fmt"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"log"
	"sort"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

// Recall は会話記憶を3層から取得（短期→中期→長期）
func (r *RealConversationManager) Recall(ctx context.Context, sessionID string, query string, topK int) ([]domconv.Message, error) {
	// 1. 短期記憶（Redis: ActiveThread）
	thread, err := r.GetActiveThread(ctx, sessionID)
	if err == nil && thread != nil && len(thread.Turns) > 0 {
		return thread.Turns, nil
	}

	// 1.5. L1 hot store（SQLite: 再起動後の当日会話）
	if r.l1Store != nil {
		events, err := r.l1Store.RecentBySession(ctx, sessionID, topK*4)
		if err != nil {
			log.Printf("Recall: L1 SQLite search failed for session %q: %v", sessionID, err)
		} else if len(events) > 0 {
			messages := l1EventsToMessages(events)
			if len(messages) > 0 {
				return messages, nil
			}
		}
	}

	// 2. 中期記憶（DuckDB: Session履歴）
	if r.duckdbStore != nil {
		summaries, err := r.duckdbStore.GetSessionHistory(ctx, sessionID, topK)
		if err == nil && len(summaries) > 0 {
			messages := make([]domconv.Message, 0, len(summaries))
			for _, s := range summaries {
				msg := domconv.NewMessage(
					domconv.SpeakerSystem,
					fmt.Sprintf("[Summary] %s (domain: %s)", s.Summary, s.Domain),
					map[string]interface{}{"thread_id": s.ThreadID, "keywords": s.Keywords},
				)
				messages = append(messages, msg)
			}
			return messages, nil
		}
	}

	// 3. 長期記憶（VectorDB: 類似度検索）
	if r.embedder == nil {
		log.Printf("Recall: Embedder not configured, skipping long-term memory search")
		return []domconv.Message{}, nil
	}
	embedding, err := r.embedder.Embed(ctx, query)
	if err != nil {
		log.Printf("Recall: Failed to embed query %q: %v", query, err)
		return []domconv.Message{}, nil
	}

	// VectorDB検索をリトライ付きで実行
	var vdbResults []*domconv.ThreadSummary
	err = withRetry(ctx, DefaultRetryConfig, func() error {
		var searchErr error
		vdbResults, searchErr = r.vectordbStore.SearchSimilar(ctx, embedding, topK)
		return searchErr
	})
	if err != nil {
		log.Printf("Recall: VectorDB search failed after retries for query %q: %v", query, err)
		return []domconv.Message{}, nil
	}
	if len(vdbResults) == 0 {
		return []domconv.Message{}, nil
	}
	messages := make([]domconv.Message, 0, len(vdbResults))
	for _, s := range vdbResults {
		msg := domconv.NewMessage(
			domconv.SpeakerSystem,
			fmt.Sprintf("[LongTermMemory] %s (score: %.2f)", s.Summary, s.Score),
			map[string]interface{}{"thread_id": s.ThreadID, "score": s.Score},
		)
		messages = append(messages, msg)
	}
	return messages, nil
}

func l1EventsToMessages(events []l1sqlite.L1MemoryEvent) []domconv.Message {
	sort.SliceStable(events, func(i, j int) bool {
		return events[i].CreatedAt.Before(events[j].CreatedAt)
	})
	messages := make([]domconv.Message, 0, len(events))
	for _, ev := range events {
		if ev.Message == "" {
			continue
		}
		meta := map[string]interface{}{
			"namespace":    ev.Namespace,
			"thread_id":    ev.ThreadID,
			"memory_state": ev.MemoryState,
			"layer":        ev.Layer,
			"source":       ev.Source,
		}
		for k, v := range ev.Meta {
			if _, exists := meta[k]; !exists {
				meta[k] = v
			}
		}
		messages = append(messages, domconv.Message{
			Speaker:   ev.Speaker,
			Msg:       ev.Message,
			Timestamp: ev.CreatedAt,
			Meta:      meta,
		})
	}
	return messages
}
