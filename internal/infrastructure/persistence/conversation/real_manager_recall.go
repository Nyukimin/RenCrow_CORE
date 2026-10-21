package conversation

import (
	"context"
	"errors"
	"fmt"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	"log"
	"sort"
	"strings"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

// Recall は会話記憶を3層から取得（短期→中期→長期）
func (r *RealConversationManager) Recall(ctx context.Context, sessionID string, query string, topK int) ([]domconv.Message, error) {
	// 1. 短期記憶（Redis: ActiveThread）
	thread, err := r.GetActiveThread(ctx, sessionID)
	if err == nil && thread != nil && len(thread.Turns) > 0 {
		messages := append([]domconv.Message(nil), thread.Turns...)
		if store, ok := r.l1Store.(conversationTurnL1Store); ok {
			// Prompt Recall has a one-closed-thread raw fallback window. The L1
			// query orders by closed-thread sequence descending, so the immediately
			// preceding full User/Agent thread remains available while its summary
			// follower is pending without replaying an unbounded archive backlog.
			pending, pendingErr := store.ListPendingConversationTurnThreadFollowers(ctx, sessionID, 1)
			if pendingErr != nil {
				return nil, pendingErr
			}
			seenThreads := make(map[modulecore.ThreadID]struct{}, len(pending))
			for _, follower := range pending {
				if _, exists := seenThreads[follower.ClosedThreadID]; exists {
					continue
				}
				seenThreads[follower.ClosedThreadID] = struct{}{}
				events, loadErr := store.LoadConversationThreadProjection(ctx, sessionID, follower.ClosedThreadID)
				if loadErr != nil {
					return nil, loadErr
				}
				messages = append(messages, l1ProjectionEventsToMessages(events)...)
			}
			if len(seenThreads) > 0 {
				messages = dedupeAndOrderConversationRecall(messages)
			}
		}
		return messages, nil
	}

	// 1.5. L1 hot store（SQLite: 再起動後の当日会話）
	if r.l1Store != nil {
		if store, ok := r.l1Store.(conversationTurnL1Store); ok && modulecore.SessionID(sessionID).Validate() == nil {
			messages, found, hasTypedTurns, err := recallFromL1ConversationProjections(ctx, store, sessionID)
			if err != nil {
				return nil, err
			}
			if found {
				return messages, nil
			}
			if !hasTypedTurns {
				// Legacy L1 rows have no active-thread or pending-follower ledger.
				// Read them only when the session has no canonical turn receipt.
				events, err := r.l1Store.RecentBySession(ctx, sessionID, topK*4)
				if err != nil {
					return nil, fmt.Errorf("Recall: L1 SQLite search failed for session %q: %w", sessionID, err)
				} else if len(events) > 0 {
					messages := l1EventsToMessages(events)
					if len(messages) > 0 {
						return messages, nil
					}
				}
			}
		} else {
			// Typed turn owners require canonical SessionIDs. Historical L1 rows
			// may predate that contract, so keep their original session key intact.
			events, err := r.l1Store.RecentBySession(ctx, sessionID, topK*4)
			if err != nil {
				return nil, fmt.Errorf("Recall: L1 SQLite search failed for session %q: %w", sessionID, err)
			} else if len(events) > 0 {
				messages := l1EventsToMessages(events)
				if len(messages) > 0 {
					return messages, nil
				}
			}
		}
	}

	// 2. 中期記憶（SQLite archive: Session履歴）
	if r.archiveStore != nil {
		summaries, err := r.archiveStore.GetSessionHistory(ctx, sessionID, topK)
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

func recallFromL1ConversationProjections(ctx context.Context, store conversationTurnL1Store, sessionID string) ([]domconv.Message, bool, bool, error) {
	activeEvents, err := store.LoadActiveConversationThreadProjection(ctx, sessionID)
	if err != nil && !errors.Is(err, domconv.ErrThreadNotFound) {
		return nil, false, false, fmt.Errorf("Recall: load active L1 conversation projection for session %q: %w", sessionID, err)
	}
	messages := l1ProjectionEventsToMessages(activeEvents)
	found := len(activeEvents) > 0

	// Match the normal Redis-backed Recall window: include no more than the
	// newest pending closed thread while its summary follower is outstanding.
	pending, err := store.ListPendingConversationTurnThreadFollowers(ctx, sessionID, 1)
	if err != nil {
		return nil, false, false, fmt.Errorf("Recall: list pending L1 conversation followers for session %q: %w", sessionID, err)
	}
	seenThreads := make(map[modulecore.ThreadID]struct{}, len(pending))
	for _, follower := range pending {
		if _, exists := seenThreads[follower.ClosedThreadID]; exists {
			continue
		}
		seenThreads[follower.ClosedThreadID] = struct{}{}
		events, loadErr := store.LoadConversationThreadProjection(ctx, sessionID, follower.ClosedThreadID)
		if loadErr != nil {
			return nil, false, false, fmt.Errorf("Recall: load pending closed L1 thread %q for session %q: %w", follower.ClosedThreadID, sessionID, loadErr)
		}
		if len(events) == 0 {
			return nil, false, false, fmt.Errorf("Recall: pending closed L1 thread %q has no messages", follower.ClosedThreadID)
		}
		messages = append(messages, l1ProjectionEventsToMessages(events)...)
		found = true
	}
	if !found {
		hasTypedTurns, err := store.HasConversationTurnReceiptsForSession(ctx, sessionID)
		if err != nil {
			return nil, false, false, fmt.Errorf("Recall: check typed L1 conversation rows for session %q: %w", sessionID, err)
		}
		if hasTypedTurns {
			return nil, false, true, nil
		}
		return nil, false, false, nil
	}
	return dedupeAndOrderConversationRecall(messages), true, true, nil
}

func dedupeAndOrderConversationRecall(messages []domconv.Message) []domconv.Message {
	seen := make(map[string]struct{}, len(messages))
	result := make([]domconv.Message, 0, len(messages))
	for _, message := range messages {
		if id, ok := message.Meta["message_id"].(string); ok && strings.TrimSpace(id) != "" {
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
		}
		result = append(result, message)
	}
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].Timestamp.Before(result[j].Timestamp)
	})
	return result
}

func l1EventsToMessages(events []l1sqlite.L1MemoryEvent) []domconv.Message {
	// RecentBySession returns newest first; restore insertion order for equal-time events.
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
	return l1EventsInCanonicalOrderToMessages(events)
}

// l1ProjectionEventsToMessages consumes LoadConversationThreadProjection's
// rowid-ascending order. Stable timestamp ordering preserves user/agent order
// even when their CreatedAt values are equal.
func l1ProjectionEventsToMessages(events []l1sqlite.L1MemoryEvent) []domconv.Message {
	return l1EventsInCanonicalOrderToMessages(events)
}

func l1EventsInCanonicalOrderToMessages(events []l1sqlite.L1MemoryEvent) []domconv.Message {
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
		speaker := ev.Speaker
		if canonical, ok := domconv.CanonicalChatAgentSpeaker(speaker); ok {
			speaker = canonical
		}
		messages = append(messages, domconv.Message{
			Speaker:   speaker,
			Msg:       ev.Message,
			Timestamp: ev.CreatedAt,
			Meta:      meta,
		})
	}
	return messages
}
