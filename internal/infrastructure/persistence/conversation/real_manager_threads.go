package conversation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Store はメッセージをActiveThreadに追加
func (r *RealConversationManager) Store(ctx context.Context, sessionID string, msg domconv.Message) error {
	r.threadMu.Lock()
	defer r.threadMu.Unlock()

	var preparedSession *domconv.SessionConversation
	thread, err := r.GetActiveThread(ctx, sessionID)
	if err == domconv.ErrThreadNotFound {
		thread, preparedSession, err = r.prepareThreadLocked(ctx, sessionID, "general")
		if err != nil {
			return fmt.Errorf("failed to create thread: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("failed to get active thread: %w", err)
	}

	if err := validateStoreThread(thread, sessionID); err != nil {
		return err
	}
	if err := r.validateLatestStoreThreadReference(ctx, sessionID, thread); err != nil {
		return err
	}
	thread = cloneThreadForStore(thread)
	if err := thread.AddMessage(msg); err != nil {
		return fmt.Errorf("failed to append message to thread: %w", err)
	}

	if len(thread.Turns) >= 12 {
		oldThreadID := thread.ID
		newThread, newSession, err := r.prepareThreadLocked(ctx, sessionID, thread.Domain)
		if err != nil {
			return fmt.Errorf("failed to create new thread before background flush: %w", err)
		}
		if err := validateStoreThread(newThread, sessionID); err != nil {
			return err
		}
		newThread = cloneThreadForStore(newThread)
		if err := newThread.AddMessage(msg); err != nil {
			return fmt.Errorf("failed to append message to new thread: %w", err)
		}
		if err := r.saveObservedMessage(ctx, sessionID, newThread.ID, newThread.ThreadSeq, newThread.ThreadKind, msg); err != nil {
			return fmt.Errorf("failed to save message to L1 SQLite: %w", err)
		}
		if err := r.persistPreparedThreadLocked(ctx, newThread, newSession); err != nil {
			return fmt.Errorf("failed to persist rolled thread: %w", err)
		}
		r.enqueueThreadFlush(ctx, oldThreadID)
		return nil
	}

	if err := r.saveObservedMessage(ctx, sessionID, thread.ID, thread.ThreadSeq, thread.ThreadKind, msg); err != nil {
		return fmt.Errorf("failed to save message to L1 SQLite: %w", err)
	}

	if preparedSession != nil {
		if err := r.persistPreparedThreadLocked(ctx, thread, preparedSession); err != nil {
			return fmt.Errorf("failed to persist created thread: %w", err)
		}
		return nil
	}
	if err := r.redisStore.SaveThread(ctx, thread); err != nil {
		return fmt.Errorf("failed to save thread to redis: %w", err)
	}
	return nil
}

func (r *RealConversationManager) saveObservedMessage(ctx context.Context, sessionID string, threadID modulecore.ThreadID, threadSeq modulecore.ThreadSeq, threadKind modulecore.ThreadKind, msg domconv.Message) error {
	if r.l1Store == nil {
		return nil
	}
	namespace := fmt.Sprintf("conv:%s", threadID)
	return r.l1Store.SaveMessage(ctx, sessionID, threadID, threadSeq, threadKind, namespace, msg, l1sqlite.MemoryStateObserved)
}

func (r *RealConversationManager) enqueueThreadFlush(parent context.Context, threadID modulecore.ThreadID) {
	r.backgroundMu.Lock()
	if r.backgroundClosed {
		r.backgroundMu.Unlock()
		log.Printf("Thread %s background flush skipped: manager is closing", threadID)
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
		_, err := r.FlushThread(ctx, threadID)
		if err != nil {
			log.Printf("Thread %s background flush failed: thread_summary_archive_failed", threadID)
			return
		}
		log.Printf("Thread %s background flushed: thread_summary_persisted", threadID)
	}()
}

func (r *RealConversationManager) waitForBackgroundJobs() {
	r.backgroundWG.Wait()
}

// FlushThread はThreadを要約してSQLite archive/VectorDBに保存する。
func (r *RealConversationManager) FlushThread(ctx context.Context, threadID modulecore.ThreadID) (*domconv.ThreadSummary, error) {
	thread, err := r.redisStore.GetThread(ctx, threadID)
	if err != nil {
		return nil, fmt.Errorf("failed to get thread from redis: %w", err)
	}

	residual, generationMode, failureCode := r.generateSummaryResidual(ctx, thread)

	var embedding []float32
	if r.embedder != nil {
		emb, err := r.embedder.Embed(ctx, residual.Summary)
		if err != nil {
			log.Printf("Failed to generate embedding (skipping VectorDB): embedding_unavailable")
		} else {
			embedding = emb
		}
	}

	return r.archiveThreadSummary(ctx, thread, residual, generationMode, failureCode, embedding)
}

func (r *RealConversationManager) archiveThreadSummary(ctx context.Context, thread *domconv.Thread, residual domconv.SummaryResidual, generationMode string, failureCode string, embedding []float32) (*domconv.ThreadSummary, error) {
	roles, evidenceSHA256, err := deriveThreadSummaryEvidence(thread)
	if err != nil {
		return nil, fmt.Errorf("thread summary evidence unavailable")
	}
	archiveAt, err := stableThreadSummaryTime(thread)
	if err != nil {
		return nil, err
	}
	archiveAt = archiveAt.UTC()
	provider := strings.TrimSpace(residual.Provider)
	receipt := &domconv.ThreadSummaryReceipt{
		SchemaVersion:   domconv.ThreadSummaryReceiptSchemaVersion,
		GenerationMode:  generationMode,
		Provider:        provider,
		FailureCode:     failureCode,
		EvidenceSHA256:  evidenceSHA256,
		SourceTurnCount: len(thread.Turns),
		Roles:           roles,
		CreatedAt:       archiveAt,
	}
	if err := receipt.ValidateForWrite(); err != nil {
		return nil, fmt.Errorf("thread summary receipt invalid")
	}
	if err := domconv.ValidateSummaryResidual(residual); err != nil {
		return nil, fmt.Errorf("thread summary residual invalid")
	}
	summary := &domconv.ThreadSummary{
		ThreadID:   thread.ID,
		ThreadSeq:  thread.ThreadSeq,
		ThreadKind: thread.ThreadKind,
		SessionID:  thread.SessionID,
		Domain:     thread.Domain,
		Summary:    residual.Summary,
		Keywords:   residual.Keywords,
		Roles:      append([]string(nil), roles...),
		Receipt:    receipt,
		Embedding:  embedding,
		StartTime:  thread.StartTime,
		EndTime:    archiveAt,
		IsNovel:    false,
	}

	if r.archiveStore != nil {
		if err := r.archiveStore.SaveThreadSummaryWithReceipt(ctx, summary, receipt); err != nil {
			return nil, fmt.Errorf("failed to save summary to archive sqlite: %w", err)
		}
	}

	if len(summary.Embedding) > 0 {
		if err := r.vectordbStore.SaveThreadSummary(ctx, summary); err != nil {
			log.Printf("Failed to save summary to vectordb: vector_summary_unavailable")
		}
	}

	if err := r.redisStore.DeleteThread(ctx, thread.ID); err != nil {
		log.Printf("Failed to delete thread from redis: thread_delete_failed")
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
	if err := validateSessionThreadReference(sess); err != nil {
		return nil, fmt.Errorf("invalid session active thread: %w", err)
	}
	if sess.LastThreadID == "" {
		return nil, domconv.ErrThreadNotFound
	}
	thread, err := r.redisStore.GetThread(ctx, sess.LastThreadID)
	if err != nil {
		return nil, err
	}
	if thread == nil || thread.ID != sess.LastThreadID || thread.ThreadSeq != sess.LastThreadSeq || thread.ThreadKind != sess.LastThreadKind || thread.SessionID != sess.ID {
		return nil, fmt.Errorf("active thread does not match session reference")
	}
	return thread, nil
}

// CreateThread は新規 Thread を作成
func (r *RealConversationManager) CreateThread(ctx context.Context, sessionID string, domain string) (*domconv.Thread, error) {
	r.threadMu.Lock()
	defer r.threadMu.Unlock()
	return r.createThreadLocked(ctx, sessionID, domain)
}

func (r *RealConversationManager) createThreadLocked(ctx context.Context, sessionID string, domain string) (*domconv.Thread, error) {
	thread, sess, err := r.prepareThreadLocked(ctx, sessionID, domain)
	if err != nil {
		return nil, err
	}
	if err := r.persistPreparedThreadLocked(ctx, thread, sess); err != nil {
		return nil, err
	}
	return thread, nil
}

func (r *RealConversationManager) prepareThreadLocked(ctx context.Context, sessionID string, domain string) (*domconv.Thread, *domconv.SessionConversation, error) {
	sess, err := r.redisStore.GetSession(ctx, sessionID)
	if err == domconv.ErrSessionNotFound {
		sess = domconv.NewSessionConversation(sessionID, "")
	} else if err != nil {
		return nil, nil, fmt.Errorf("failed to get session: %w", err)
	}
	if sess != nil {
		sessionCopy := *sess
		sess = &sessionCopy
	}
	if err := r.reconcileSessionThreadReference(ctx, sess); err != nil {
		return nil, nil, err
	}

	seq, err := nextSessionThreadSequence(sess)
	if err != nil {
		return nil, nil, err
	}
	thread, err := domconv.NewThread(sessionID, domain, domconv.ThreadKindUserConversation, seq)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create thread: %w", err)
	}
	sess.LastThreadID = thread.ID
	sess.LastThreadSeq = thread.ThreadSeq
	sess.LastThreadKind = thread.ThreadKind
	sess.UpdatedAt = time.Now().UTC()
	return thread, sess, nil
}

func (r *RealConversationManager) persistPreparedThreadLocked(ctx context.Context, thread *domconv.Thread, sess *domconv.SessionConversation) error {
	if err := r.redisStore.SaveThread(ctx, thread); err != nil {
		return fmt.Errorf("failed to save thread to redis: %w", err)
	}
	if err := r.redisStore.SaveSession(ctx, sess); err != nil {
		if rollbackErr := r.redisStore.DeleteThread(ctx, thread.ID); rollbackErr != nil {
			return fmt.Errorf("failed to save session to redis: %w", errors.Join(err, fmt.Errorf("failed to rollback created thread: %w", rollbackErr)))
		}
		return fmt.Errorf("failed to save session to redis: %w", err)
	}
	return nil
}

func validateStoreThread(thread *domconv.Thread, sessionID string) error {
	if thread == nil {
		return domconv.ErrConversationTurnInvalid
	}
	if thread.Status != domconv.ThreadActive {
		return domconv.ErrInvalidThreadStatus
	}
	if modulecore.SessionID(sessionID).Validate() != nil || thread.SessionID != sessionID || modulecore.SessionID(thread.SessionID).Validate() != nil || thread.ID.Validate() != nil || thread.ThreadSeq.Validate() != nil || thread.ThreadKind.Validate() != nil {
		return domconv.ErrConversationTurnInvalid
	}
	return nil
}

func (r *RealConversationManager) validateLatestStoreThreadReference(ctx context.Context, sessionID string, thread *domconv.Thread) error {
	if r.l1Store == nil {
		return nil
	}
	l1ThreadID, l1ThreadSeq, l1ThreadKind, found, err := r.l1Store.LatestConversationThreadReference(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("load latest L1 conversation thread: %w", err)
	}
	if !found {
		return nil
	}
	if l1ThreadID.Validate() != nil || l1ThreadSeq.Validate() != nil || l1ThreadKind.Validate() != nil {
		return fmt.Errorf("invalid latest L1 conversation thread: %w", domconv.ErrConversationTurnInvalid)
	}
	if l1ThreadSeq > thread.ThreadSeq || l1ThreadSeq == thread.ThreadSeq && (l1ThreadID != thread.ID || l1ThreadKind != thread.ThreadKind) {
		return fmt.Errorf("active thread conflicts with latest L1 conversation thread: %w", domconv.ErrConversationTurnConflict)
	}
	return nil
}

func cloneThreadForStore(thread *domconv.Thread) *domconv.Thread {
	if thread == nil {
		return nil
	}
	clone := *thread
	clone.Turns = make([]domconv.Message, len(thread.Turns))
	for index, turn := range thread.Turns {
		clone.Turns[index] = turn
		clone.Turns[index].Meta = cloneConversationTurnMeta(turn.Meta)
	}
	clone.Targets = append([]string(nil), thread.Targets...)
	if thread.Cooldown != nil {
		clone.Cooldown = make(map[string]int, len(thread.Cooldown))
		for key, value := range thread.Cooldown {
			clone.Cooldown[key] = value
		}
	}
	if thread.EndTime != nil {
		endTime := *thread.EndTime
		clone.EndTime = &endTime
	}
	return &clone
}

func (r *RealConversationManager) reconcileSessionThreadReference(ctx context.Context, sess *domconv.SessionConversation) error {
	if r == nil || r.l1Store == nil {
		return nil
	}
	if err := validateSessionThreadReference(sess); err != nil {
		return fmt.Errorf("invalid session active thread: %w", err)
	}
	l1ThreadID, l1ThreadSeq, l1ThreadKind, found, err := r.l1Store.LatestConversationThreadReference(ctx, sess.ID)
	if err != nil {
		return fmt.Errorf("load latest L1 conversation thread: %w", err)
	}
	if !found {
		return nil
	}
	l1Reference := &domconv.SessionConversation{
		LastThreadID:   l1ThreadID,
		LastThreadSeq:  l1ThreadSeq,
		LastThreadKind: l1ThreadKind,
	}
	if err := validateSessionThreadReference(l1Reference); err != nil {
		return fmt.Errorf("invalid latest L1 conversation thread: %w", err)
	}
	if sess.LastThreadID == "" || l1ThreadSeq > sess.LastThreadSeq {
		sess.LastThreadID = l1ThreadID
		sess.LastThreadSeq = l1ThreadSeq
		sess.LastThreadKind = l1ThreadKind
		return nil
	}
	if l1ThreadSeq == sess.LastThreadSeq && (l1ThreadID != sess.LastThreadID || l1ThreadKind != sess.LastThreadKind) {
		return errors.New("session active thread identity conflicts with latest L1 conversation thread")
	}
	return nil
}

func validateSessionThreadReference(sess *domconv.SessionConversation) error {
	if sess == nil {
		return errors.New("session is nil")
	}
	empty := sess.LastThreadID == "" && sess.LastThreadSeq == 0 && sess.LastThreadKind == ""
	if empty {
		return nil
	}
	if sess.LastThreadID == "" || sess.LastThreadSeq == 0 || sess.LastThreadKind == "" {
		return errors.New("session active thread reference must contain thread_id, thread_seq, and thread_kind")
	}
	if err := sess.LastThreadID.Validate(); err != nil {
		return fmt.Errorf("invalid thread ID: %w", err)
	}
	if err := sess.LastThreadSeq.Validate(); err != nil {
		return fmt.Errorf("invalid thread sequence: %w", err)
	}
	if err := sess.LastThreadKind.Validate(); err != nil {
		return fmt.Errorf("invalid thread kind: %w", err)
	}
	return nil
}

func nextSessionThreadSequence(sess *domconv.SessionConversation) (modulecore.ThreadSeq, error) {
	if err := validateSessionThreadReference(sess); err != nil {
		return 0, err
	}
	if sess.LastThreadID == "" {
		return 1, nil
	}
	if sess.LastThreadSeq == modulecore.ThreadSeq(1<<63-1) {
		return 0, errors.New("thread sequence overflow")
	}
	return sess.LastThreadSeq + 1, nil
}

func (r *RealConversationManager) generateSummaryResidual(ctx context.Context, thread *domconv.Thread) (domconv.SummaryResidual, string, string) {
	if r.summarizer == nil {
		return fallbackSummaryResidual(thread, domconv.ThreadSummaryProviderNotConfigured), domconv.ThreadSummaryGenerationDeterministicFallback, domconv.ThreadSummaryFailureNotConfigured
	}
	residual, err := r.summarizer.Summarize(ctx, thread)
	if err == nil {
		if normalized, normalizeErr := domconv.NormalizeSummaryResidual(residual); normalizeErr == nil {
			if strings.TrimSpace(normalized.Provider) != "" {
				return normalized, domconv.ThreadSummaryGenerationLLM, ""
			}
			return fallbackSummaryResidual(thread, domconv.ThreadSummaryProviderNotConfigured), domconv.ThreadSummaryGenerationDeterministicFallback, domconv.ThreadSummaryFailureNotConfigured
		}
		provider := strings.TrimSpace(residual.Provider)
		if provider == "" {
			provider = domconv.ThreadSummaryProviderNotConfigured
		}
		return fallbackSummaryResidual(thread, provider), domconv.ThreadSummaryGenerationDeterministicFallback, domconv.ThreadSummaryFailureInvalid
	}
	provider := strings.TrimSpace(residual.Provider)
	if provider == "" {
		provider = domconv.ThreadSummaryProviderNotConfigured
		return fallbackSummaryResidual(thread, provider), domconv.ThreadSummaryGenerationDeterministicFallback, domconv.ThreadSummaryFailureNotConfigured
	}
	return fallbackSummaryResidual(thread, provider), domconv.ThreadSummaryGenerationDeterministicFallback, classifySummaryFailure(err)
}

func generateSimpleSummary(thread *domconv.Thread) string {
	if thread == nil || len(thread.Turns) == 0 {
		return "Empty thread"
	}
	firstMessage := thread.Turns[0]
	lastMessage := thread.Turns[len(thread.Turns)-1]
	first := truncateSummaryFragment(firstMessage.Msg, 50)
	last := truncateSummaryFragment(lastMessage.Msg, 50)
	return fmt.Sprintf("Start [%s]: %s ... End [%s]: %s (%d turns)", firstMessage.Speaker, first, lastMessage.Speaker, last, len(thread.Turns))
}

func fallbackSummaryResidual(thread *domconv.Thread, provider string) domconv.SummaryResidual {
	residual := domconv.SummaryResidual{
		Summary:  generateSimpleSummary(thread),
		Keywords: fallbackSummaryKeywords(thread),
		Provider: strings.TrimSpace(provider),
	}
	if normalized, err := domconv.NormalizeSummaryResidual(residual); err == nil {
		return normalized
	}
	return domconv.SummaryResidual{
		Summary:  "Conversation thread summary",
		Keywords: []string{"conversation", "thread", "summary"},
		Provider: strings.TrimSpace(provider),
	}
}

func fallbackSummaryKeywords(thread *domconv.Thread) []string {
	candidates := make([]string, 0, 8)
	if thread != nil {
		candidates = append(candidates, thread.Domain)
		for _, turn := range thread.Turns {
			candidates = append(candidates, string(turn.Speaker))
		}
	}
	candidates = append(candidates, "conversation", "thread", "summary")
	keywords := make([]string, 0, 5)
	seen := make(map[string]struct{}, 5)
	for _, candidate := range candidates {
		candidate = normalizeFallbackKeyword(candidate)
		if candidate == "" || len([]rune(candidate)) > 64 {
			continue
		}
		folded := strings.ToLower(candidate)
		if _, ok := seen[folded]; ok {
			continue
		}
		seen[folded] = struct{}{}
		keywords = append(keywords, candidate)
		if len(keywords) == 5 {
			break
		}
	}
	for _, candidate := range []string{"conversation", "thread", "summary"} {
		if len(keywords) == 5 {
			break
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		keywords = append(keywords, candidate)
	}
	return keywords
}

func normalizeFallbackKeyword(value string) string {
	value = strings.Map(func(r rune) rune {
		switch r {
		case '\r', '\n', '\x00':
			return ' '
		default:
			return r
		}
	}, value)
	return strings.Join(strings.Fields(value), " ")
}

func truncateSummaryFragment(value string, max int) string {
	value = strings.Map(func(r rune) rune {
		if r == '\x00' {
			return -1
		}
		if r == '\r' || r == '\n' {
			return ' '
		}
		return r
	}, value)
	runes := []rune(value)
	if len(runes) > max {
		return string(runes[:max]) + "..."
	}
	return string(runes)
}

func classifySummaryFailure(err error) string {
	switch {
	case errors.Is(err, domconv.ErrThreadSummarizerUnavailable):
		return domconv.ThreadSummaryFailureUnavailable
	case errors.Is(err, domconv.ErrThreadSummarizerNotConfigured):
		return domconv.ThreadSummaryFailureNotConfigured
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return domconv.ThreadSummaryFailureUnavailable
	default:
		return domconv.ThreadSummaryFailureInvalid
	}
}

type threadSummaryEvidence struct {
	SchemaVersion   string                      `json:"schema_version"`
	ThreadID        modulecore.ThreadID         `json:"thread_id"`
	ThreadSeq       modulecore.ThreadSeq        `json:"thread_seq"`
	ThreadKind      modulecore.ThreadKind       `json:"thread_kind"`
	SessionID       string                      `json:"session_id"`
	SourceTurnCount int                         `json:"source_turn_count"`
	Turns           []threadSummaryEvidenceTurn `json:"turns"`
}

type threadSummaryEvidenceTurn struct {
	Index   int    `json:"index"`
	Speaker string `json:"speaker"`
	Body    string `json:"body"`
}

func deriveThreadSummaryEvidence(thread *domconv.Thread) ([]string, string, error) {
	if thread == nil {
		return nil, "", fmt.Errorf("thread summary evidence source is required")
	}
	if thread.ID.Validate() != nil || thread.ThreadSeq.Validate() != nil || thread.ThreadKind.Validate() != nil || strings.TrimSpace(thread.SessionID) == "" || !utf8.ValidString(thread.SessionID) || len(thread.Turns) == 0 {
		return nil, "", fmt.Errorf("thread summary evidence source identity is invalid")
	}
	roles := make([]string, 0, len(thread.Turns))
	seenRoles := make(map[string]struct{}, len(thread.Turns))
	evidence := threadSummaryEvidence{
		SchemaVersion:   "conversation.thread_summary_evidence.v1",
		ThreadID:        thread.ID,
		ThreadSeq:       thread.ThreadSeq,
		ThreadKind:      thread.ThreadKind,
		SessionID:       thread.SessionID,
		SourceTurnCount: len(thread.Turns),
		Turns:           make([]threadSummaryEvidenceTurn, 0, len(thread.Turns)),
	}
	for index, turn := range thread.Turns {
		speaker := string(turn.Speaker)
		if !utf8.ValidString(speaker) || !utf8.ValidString(turn.Msg) || strings.TrimSpace(speaker) == "" || strings.ContainsAny(speaker, "\r\n\x00") {
			return nil, "", fmt.Errorf("thread summary evidence contains invalid UTF-8")
		}
		if _, ok := seenRoles[speaker]; !ok {
			seenRoles[speaker] = struct{}{}
			roles = append(roles, speaker)
		}
		evidence.Turns = append(evidence.Turns, threadSummaryEvidenceTurn{Index: index, Speaker: speaker, Body: turn.Msg})
	}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		return nil, "", fmt.Errorf("thread summary evidence encoding failed")
	}
	digest := sha256.Sum256(encoded)
	return roles, hex.EncodeToString(digest[:]), nil
}

func stableThreadSummaryTime(thread *domconv.Thread) (time.Time, error) {
	if thread == nil {
		return time.Time{}, fmt.Errorf("thread summary time source is required")
	}
	if thread.EndTime != nil && !thread.EndTime.IsZero() {
		return *thread.EndTime, nil
	}
	if len(thread.Turns) > 0 && !thread.Turns[len(thread.Turns)-1].Timestamp.IsZero() {
		return thread.Turns[len(thread.Turns)-1].Timestamp, nil
	}
	if !thread.StartTime.IsZero() {
		return thread.StartTime, nil
	}
	return time.Time{}, fmt.Errorf("thread summary time source is unavailable")
}
