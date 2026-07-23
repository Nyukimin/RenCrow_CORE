package conversation

import (
	"context"
	"fmt"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"log"
	"strings"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

// Store はメッセージをActiveThreadに追加
func (r *RealConversationManager) Store(ctx context.Context, sessionID string, msg domconv.Message) error {
	thread, err := r.GetActiveThread(ctx, sessionID)
	if err == domconv.ErrThreadNotFound {
		thread, err = r.CreateThread(ctx, sessionID, "general")
		if err != nil {
			return fmt.Errorf("failed to create thread: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("failed to get active thread: %w", err)
	}

	thread.AddMessage(msg)

	if len(thread.Turns) >= 12 {
		oldThreadID := thread.ID
		newThread, err := r.CreateThread(ctx, sessionID, thread.Domain)
		if err != nil {
			return fmt.Errorf("failed to create new thread before background flush: %w", err)
		}
		newThread.AddMessage(msg)
		if err := r.saveObservedMessage(ctx, sessionID, newThread.ID, msg); err != nil {
			log.Printf("Failed to save message to L1 SQLite: %v", err)
		}
		if err := r.redisStore.SaveThread(ctx, newThread); err != nil {
			return fmt.Errorf("failed to save rolled thread to redis: %w", err)
		}
		r.enqueueThreadFlush(ctx, oldThreadID)
		return nil
	}

	if err := r.saveObservedMessage(ctx, sessionID, thread.ID, msg); err != nil {
		log.Printf("Failed to save message to L1 SQLite: %v", err)
	}

	if err := r.redisStore.SaveThread(ctx, thread); err != nil {
		return fmt.Errorf("failed to save thread to redis: %w", err)
	}
	return nil
}

func (r *RealConversationManager) saveObservedMessage(ctx context.Context, sessionID string, threadID int64, msg domconv.Message) error {
	if r.l1Store == nil {
		return nil
	}
	namespace := fmt.Sprintf("conv:%d", threadID)
	return r.l1Store.SaveMessage(ctx, sessionID, threadID, namespace, msg, l1sqlite.MemoryStateObserved)
}

func (r *RealConversationManager) enqueueThreadFlush(parent context.Context, threadID int64) {
	r.backgroundMu.Lock()
	if r.backgroundClosed {
		r.backgroundMu.Unlock()
		log.Printf("Thread #%d background flush skipped: manager is closing", threadID)
		return
	}
	r.backgroundWG.Add(1)
	r.backgroundMu.Unlock()

	go func() {
		defer r.backgroundWG.Done()
		timeout := r.backgroundFlushTimeout
		if timeout <= 0 {
			timeout = 45 * time.Second
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), timeout)
		defer cancel()
		summary, err := r.FlushThread(ctx, threadID)
		if err != nil {
			log.Printf("Thread #%d LLM background flush failed, using simple summary: %v", threadID, err)
			persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
			defer persistCancel()
			summary, err = r.flushThreadWithSimpleSummary(persistCtx, threadID)
			if err != nil {
				log.Printf("Thread #%d simple background flush failed: %v", threadID, err)
				return
			}
		}
		log.Printf("Thread #%d background flushed: %s", threadID, summary.Summary)
	}()
}

func (r *RealConversationManager) waitForBackgroundJobs() {
	r.backgroundWG.Wait()
}

// FlushThread はThreadを要約してSQLite archive/VectorDBに保存する。
func (r *RealConversationManager) FlushThread(ctx context.Context, threadID int64) (*domconv.ThreadSummary, error) {
	thread, err := r.redisStore.GetThread(ctx, threadID)
	if err != nil {
		return nil, fmt.Errorf("failed to get thread from redis: %w", err)
	}

	summaryText, keywords := r.generateSummaryAndKeywords(ctx, thread)
	if strings.TrimSpace(summaryText) == "" {
		summaryText = generateSimpleSummary(thread)
	}

	var embedding []float32
	if r.embedder != nil {
		emb, err := r.embedder.Embed(ctx, summaryText)
		if err != nil {
			log.Printf("Failed to generate embedding (skipping VectorDB): %v", err)
		} else {
			embedding = emb
		}
	}

	return r.archiveThreadSummary(ctx, thread, summaryText, keywords, embedding)
}

func (r *RealConversationManager) flushThreadWithSimpleSummary(ctx context.Context, threadID int64) (*domconv.ThreadSummary, error) {
	thread, err := r.redisStore.GetThread(ctx, threadID)
	if err != nil {
		return nil, fmt.Errorf("failed to get thread for simple summary: %w", err)
	}
	return r.archiveThreadSummary(ctx, thread, generateSimpleSummary(thread), []string{thread.Domain}, nil)
}

func (r *RealConversationManager) archiveThreadSummary(ctx context.Context, thread *domconv.Thread, summaryText string, keywords []string, embedding []float32) (*domconv.ThreadSummary, error) {
	summary := &domconv.ThreadSummary{
		ThreadID:  thread.ID,
		SessionID: thread.SessionID,
		Domain:    thread.Domain,
		Summary:   summaryText,
		Keywords:  keywords,
		Embedding: embedding,
		StartTime: thread.StartTime,
		EndTime:   time.Now(),
		IsNovel:   false,
	}

	if r.archiveStore != nil {
		if err := r.archiveStore.SaveThreadSummary(ctx, summary); err != nil {
			return nil, fmt.Errorf("failed to save summary to archive sqlite: %w", err)
		}
	}

	if len(summary.Embedding) > 0 {
		if err := r.vectordbStore.SaveThreadSummary(ctx, summary); err != nil {
			log.Printf("Failed to save summary to vectordb: %v", err)
		}
	}

	if err := r.redisStore.DeleteThread(ctx, thread.ID); err != nil {
		log.Printf("Failed to delete thread from redis: %v", err)
	}
	return summary, nil
}

// IsNovelInformation は情報が新規かを判定
func (r *RealConversationManager) IsNovelInformation(ctx context.Context, msg domconv.Message) (bool, float32, error) {
	if r.embedder == nil {
		return false, 0.0, nil
	}
	embedding, err := r.embedder.Embed(ctx, msg.Msg)
	if err != nil {
		return false, 0.0, fmt.Errorf("failed to embed message: %w", err)
	}
	isNovel, score, err := r.vectordbStore.IsNovelQuery(ctx, embedding, noveltyThreshold)
	if err != nil {
		return false, 0.0, fmt.Errorf("failed to query vectordb: %w", err)
	}
	return isNovel, score, nil
}

// GetActiveThread は SessionID に紐づく ActiveThread を取得
func (r *RealConversationManager) GetActiveThread(ctx context.Context, sessionID string) (*domconv.Thread, error) {
	sess, err := r.redisStore.GetSession(ctx, sessionID)
	if err == domconv.ErrSessionNotFound {
		return nil, domconv.ErrThreadNotFound
	} else if err != nil {
		return nil, fmt.Errorf("failed to get session: %w", err)
	}
	if sess.LastThreadID == 0 {
		return nil, domconv.ErrThreadNotFound
	}
	return r.redisStore.GetThread(ctx, sess.LastThreadID)
}

// CreateThread は新規 Thread を作成
func (r *RealConversationManager) CreateThread(ctx context.Context, sessionID string, domain string) (*domconv.Thread, error) {
	sess, err := r.redisStore.GetSession(ctx, sessionID)
	if err == domconv.ErrSessionNotFound {
		sess = domconv.NewSessionConversation(sessionID, "")
	} else if err != nil {
		return nil, fmt.Errorf("failed to get session: %w", err)
	}

	thread := domconv.NewThread(sessionID, domain)
	sess.LastThreadID = thread.ID
	sess.UpdatedAt = time.Now()

	if err := r.redisStore.SaveThread(ctx, thread); err != nil {
		return nil, fmt.Errorf("failed to save thread to redis: %w", err)
	}
	if err := r.redisStore.SaveSession(ctx, sess); err != nil {
		return nil, fmt.Errorf("failed to save session to redis: %w", err)
	}
	return thread, nil
}

func (r *RealConversationManager) generateSummaryAndKeywords(ctx context.Context, thread *domconv.Thread) (string, []string) {
	if r.summarizer != nil {
		summary, err := r.summarizer.Summarize(ctx, thread)
		if err != nil {
			log.Printf("Summarizer failed, falling back to simple: %v", err)
		} else {
			keywords, err := r.summarizer.ExtractKeywords(ctx, thread)
			if err != nil {
				log.Printf("ExtractKeywords failed, using domain: %v", err)
				keywords = []string{thread.Domain}
			}
			return summary, keywords
		}
	}
	return generateSimpleSummary(thread), []string{thread.Domain}
}

func generateSimpleSummary(thread *domconv.Thread) string {
	if len(thread.Turns) == 0 {
		return "Empty thread"
	}
	first := thread.Turns[0].Msg
	last := thread.Turns[len(thread.Turns)-1].Msg
	if len(first) > 50 {
		first = first[:50] + "..."
	}
	if len(last) > 50 {
		last = last[:50] + "..."
	}
	return fmt.Sprintf("Start: %s ... End: %s (%d turns)", first, last, len(thread.Turns))
}
