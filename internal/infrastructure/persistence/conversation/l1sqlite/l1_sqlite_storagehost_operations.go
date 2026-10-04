package l1sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// ErrConversationL1OperationNeedsSingleSQLiteStore is retained for callers
// but now reports an attached archive owner that cannot provide an atomic,
// exactly reconcilable promotion receipt.
var ErrConversationL1OperationNeedsSingleSQLiteStore = errors.New("conversation L1 promotion requires a receipt-capable archive owner")

// MatchesStorageHostSaveRequest confirms that a recovered cache result carries
// the exact deterministic fields derived from its original save request.
func (entry *L1SearchCacheEntry) MatchesStorageHostSaveRequest(provider, rawQuery, resultsJSON string, sourceURLs []string, ttl time.Duration) bool {
	if entry == nil {
		return false
	}
	if provider == "" {
		provider = "default"
	}
	if resultsJSON == "" {
		resultsJSON = "[]"
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	normalizedQuery := normalizeSearchQuery(rawQuery)
	return entry.QueryHash == searchQueryHash(provider, normalizedQuery) && entry.NormalizedQuery == normalizedQuery &&
		entry.Provider == provider && entry.RawQuery == rawQuery && entry.ResultsJSON == resultsJSON &&
		slices.Equal(entry.SourceURLs, sourceURLs) && !entry.RetrievedAt.IsZero() &&
		entry.CreatedAt.Equal(entry.RetrievedAt) && entry.UpdatedAt.Equal(entry.RetrievedAt) &&
		entry.ExpiresAt.Equal(entry.RetrievedAt.Add(ttl))
}

// VerifySearchCacheInvalidationResult ties a recovered affected count to the
// invalidation event committed immediately before its operation receipt.
func (s *L1SQLiteStore) VerifySearchCacheInvalidationResult(ctx context.Context, identity ConversationL1OperationIdentity, provider, rawQuery, eventID string, affected int64) error {
	if s == nil || s.db == nil {
		return errors.New("conversation L1 operation owner is unavailable")
	}
	if identity.Operation != "invalidate_search_cache" {
		return errors.New("conversation L1 invalidation result identity mismatch")
	}
	if err := identity.validate(); err != nil {
		return err
	}
	if _, found, err := s.LookupConversationL1OperationReceipt(ctx, identity); err != nil {
		return err
	} else if !found {
		return errors.New("conversation L1 invalidation receipt is missing")
	}
	if affected < 0 {
		return errors.New("conversation L1 invalidation result is negative")
	}
	if strings.TrimSpace(eventID) == "" {
		return errors.New("conversation L1 invalidation event id is missing")
	}
	normalizedQuery := normalizeSearchQuery(rawQuery)
	if normalizedQuery == "" {
		return errors.New("search cache query is required")
	}
	if provider == "" {
		provider = "default"
	}
	hash := searchQueryHash(provider, normalizedQuery)
	namespace, err := BuildL1Namespace(NamespaceKindKnowledge, provider)
	if err != nil {
		return err
	}
	var eventPayload string
	if err := s.db.QueryRowContext(ctx, `
SELECT e.payload_json
FROM l1_event_log AS e
JOIN conversation_l1_operation_receipt AS r ON r.op_id = ?
WHERE r.operation = ? AND r.payload_sha256 = ? AND r.writer_generation = ?
	AND e.id = ? AND e.event_type = 'search.cache_invalidated' AND e.namespace = ?
	AND json_extract(e.payload_json, '$.query_hash') = ?
	AND json_extract(e.payload_json, '$.storage_host_operation.op_id') = r.op_id
	AND json_extract(e.payload_json, '$.storage_host_operation.operation') = r.operation
	AND json_extract(e.payload_json, '$.storage_host_operation.payload_sha256') = r.payload_sha256
	AND json_extract(e.payload_json, '$.storage_host_operation.writer_generation') = r.writer_generation
	LIMIT 1`, identity.OpID, identity.Operation, identity.PayloadSHA256, identity.WriterGeneration, eventID, namespace, hash).Scan(&eventPayload); err != nil {
		return fmt.Errorf("failed to read conversation L1 invalidation effect: %w", err)
	}
	var evidence struct {
		QueryHash            string `json:"query_hash"`
		NormalizedQuery      string `json:"normalized_query"`
		RawQuery             string `json:"raw_query"`
		Provider             string `json:"provider"`
		Affected             int64  `json:"affected"`
		StorageHostOperation struct {
			OpID             string `json:"op_id"`
			Operation        string `json:"operation"`
			PayloadSHA256    string `json:"payload_sha256"`
			WriterGeneration int64  `json:"writer_generation"`
		} `json:"storage_host_operation"`
	}
	if err := json.Unmarshal([]byte(eventPayload), &evidence); err != nil {
		return fmt.Errorf("failed to decode conversation L1 invalidation effect: %w", err)
	}
	if evidence.QueryHash != hash || evidence.NormalizedQuery != normalizedQuery || evidence.RawQuery != rawQuery || evidence.Provider != provider || evidence.Affected != affected ||
		evidence.StorageHostOperation.OpID != identity.OpID || evidence.StorageHostOperation.Operation != identity.Operation ||
		evidence.StorageHostOperation.PayloadSHA256 != identity.PayloadSHA256 || evidence.StorageHostOperation.WriterGeneration != identity.WriterGeneration {
		return errors.New("conversation L1 invalidation result does not match committed effect")
	}
	return nil
}

func (s *L1SQLiteStore) SaveMessageForOperation(ctx context.Context, identity ConversationL1OperationIdentity, sessionID string, threadID modulecore.ThreadID, threadSeq modulecore.ThreadSeq, threadKind modulecore.ThreadKind, namespace string, msg domconv.Message, memoryState string) error {
	if identity.Operation != "save_message" {
		return errors.New("conversation L1 save message operation identity mismatch")
	}
	if err := validateL1MessageSaveInput(sessionID, threadID, threadSeq, threadKind, msg); err != nil {
		return err
	}
	if namespace == "" {
		var err error
		namespace, err = BuildL1Namespace(NamespaceKindConversation, string(threadID))
		if err != nil {
			return err
		}
	}
	if err := ValidateL1Namespace(namespace); err != nil {
		return err
	}
	if memoryState == "" {
		memoryState = MemoryStateObserved
	}
	if err := validateMemoryState(memoryState); err != nil {
		return err
	}
	tx, prior, found, err := s.beginConversationL1Operation(ctx, identity)
	if err != nil {
		return err
	}
	if found {
		return validateConversationL1VoidResult(prior)
	}
	defer tx.Rollback()

	layer := MemoryLayerL1
	now := time.Now().UTC()
	createdAt := msg.Timestamp
	if createdAt.IsZero() {
		createdAt = now
	}
	createdAt = createdAt.UTC()
	meta := msg.Meta
	if meta == nil {
		meta = map[string]interface{}{}
	}
	metaJSON, err := marshalL1MetaJSON(meta, "failed to marshal l1 memory meta")
	if err != nil {
		return rollbackL1Tx(tx, err)
	}
	id := fmt.Sprintf("%s:%s:%d:%s:%d", sessionID, threadID, createdAt.UnixNano(), msg.Speaker, l1IDSequence.Add(1))
	event := L1MemoryEvent{
		ID: id, Namespace: namespace, SessionID: sessionID, ThreadID: threadID,
		ThreadSeq: threadSeq, ThreadKind: threadKind, Speaker: msg.Speaker,
		Message: msg.Msg, Meta: meta, MemoryState: memoryState,
		Layer: layer, Source: "conversation", CreatedAt: createdAt, UpdatedAt: now,
	}
	if err := validateL1MemoryEvent(event); err != nil {
		return rollbackL1Tx(tx, err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO l1_memory_event (
	id, namespace, session_id, thread_id, thread_seq, thread_kind, speaker, message, meta_json,
	memory_state, layer, source, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
	message = excluded.message,
	meta_json = excluded.meta_json,
	memory_state = excluded.memory_state,
	updated_at = excluded.updated_at
`, event.ID, event.Namespace, event.SessionID, event.ThreadID, event.ThreadSeq, event.ThreadKind, string(event.Speaker), event.Message, metaJSON,
		event.MemoryState, event.Layer, event.Source, event.CreatedAt, event.UpdatedAt); err != nil {
		return rollbackL1Tx(tx, fmt.Errorf("failed to save l1 memory event: %w", err))
	}
	if _, err := appendL1EventLog(ctx, tx, "memory.message_saved", namespace, sessionID, threadID, threadSeq, threadKind, map[string]interface{}{
		"memory_id": id, "speaker": string(msg.Speaker), "memory_state": memoryState, "layer": layer,
	}, "conversation"); err != nil {
		return rollbackL1Tx(tx, fmt.Errorf("failed to append l1 message event log: %w", err))
	}
	if msg.Speaker == domconv.SpeakerUser && memoryState == MemoryStateObserved && strings.HasPrefix(namespace, "conv:") {
		if _, err := execInsertProfilePromotionJob(ctx, tx, true, id, sessionID, threadID, threadSeq, threadKind, domainmemory.ProfilePromotionPending, createdAt, now); err != nil {
			return rollbackL1Tx(tx, fmt.Errorf("failed to enqueue profile promotion job: %w", err))
		}
	}
	return commitConversationL1Operation(ctx, tx, identity, nil)
}

func (s *L1SQLiteStore) SaveSearchCacheForOperation(ctx context.Context, identity ConversationL1OperationIdentity, provider, rawQuery, resultsJSON string, sourceURLs []string, ttl time.Duration) (*L1SearchCacheEntry, error) {
	if identity.Operation != "save_search_cache" {
		return nil, errors.New("conversation L1 save search cache operation identity mismatch")
	}
	normalizedQuery := normalizeSearchQuery(rawQuery)
	if normalizedQuery == "" {
		return nil, errors.New("search cache query is required")
	}
	if provider == "" {
		provider = "default"
	}
	if resultsJSON == "" {
		resultsJSON = "[]"
	}
	if !json.Valid([]byte(resultsJSON)) {
		return nil, errors.New("search cache results_json must be valid JSON")
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	searchNamespace, err := BuildL1Namespace(NamespaceKindKnowledge, provider)
	if err != nil {
		return nil, err
	}
	tx, prior, found, err := s.beginConversationL1Operation(ctx, identity)
	if err != nil {
		return nil, err
	}
	if found {
		var entry *L1SearchCacheEntry
		if err := decodeConversationL1OperationResult(prior, &entry); err != nil {
			return nil, err
		}
		if entry == nil {
			return nil, errors.New("conversation L1 search cache receipt result is empty")
		}
		return entry, nil
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	entry := &L1SearchCacheEntry{
		QueryHash: searchQueryHash(provider, normalizedQuery), NormalizedQuery: normalizedQuery,
		Provider: provider, RawQuery: rawQuery, ResultsJSON: resultsJSON, SourceURLs: sourceURLs,
		RetrievedAt: now, ExpiresAt: now.Add(ttl), CreatedAt: now, UpdatedAt: now,
	}
	sourceURLsJSON, err := json.Marshal(sourceURLs)
	if err != nil {
		return nil, rollbackL1Tx(tx, fmt.Errorf("failed to marshal search cache source urls: %w", err))
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO l1_search_cache (
	query_hash, normalized_query, provider, raw_query, results_json, source_urls_json,
	retrieved_at, expires_at, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(query_hash) DO UPDATE SET
	raw_query = excluded.raw_query,
	results_json = excluded.results_json,
	source_urls_json = excluded.source_urls_json,
	retrieved_at = excluded.retrieved_at,
	expires_at = excluded.expires_at,
	updated_at = excluded.updated_at
`, entry.QueryHash, entry.NormalizedQuery, entry.Provider, entry.RawQuery, entry.ResultsJSON, string(sourceURLsJSON),
		entry.RetrievedAt, entry.ExpiresAt, entry.CreatedAt, entry.UpdatedAt); err != nil {
		return nil, rollbackL1Tx(tx, fmt.Errorf("failed to save l1 search cache: %w", err))
	}
	if _, err := appendL1EventLog(ctx, tx, "search.cache_saved", searchNamespace, "", "", 0, "", map[string]interface{}{
		"query_hash": entry.QueryHash, "normalized_query": entry.NormalizedQuery, "raw_query": entry.RawQuery,
		"provider": entry.Provider, "expires_at": entry.ExpiresAt.Format(time.RFC3339), "source_urls": entry.SourceURLs,
	}, "search_cache"); err != nil {
		return nil, rollbackL1Tx(tx, fmt.Errorf("failed to append l1 search cache event log: %w", err))
	}
	if err := commitConversationL1Operation(ctx, tx, identity, entry); err != nil {
		return nil, err
	}
	return entry, nil
}

func (s *L1SQLiteStore) InvalidateSearchCacheForOperation(ctx context.Context, identity ConversationL1OperationIdentity, provider, rawQuery string) (int64, error) {
	if identity.Operation != "invalidate_search_cache" {
		return 0, errors.New("conversation L1 invalidate search cache operation identity mismatch")
	}
	normalizedQuery := normalizeSearchQuery(rawQuery)
	if normalizedQuery == "" {
		return 0, errors.New("search cache query is required")
	}
	if provider == "" {
		provider = "default"
	}
	hash := searchQueryHash(provider, normalizedQuery)
	searchNamespace, err := BuildL1Namespace(NamespaceKindKnowledge, provider)
	if err != nil {
		return 0, err
	}
	tx, prior, found, err := s.beginConversationL1Operation(ctx, identity)
	if err != nil {
		return 0, err
	}
	if found {
		var previous ConversationL1SearchCacheInvalidationResult
		if err := decodeConversationL1OperationResult(prior, &previous); err != nil {
			return 0, err
		}
		if err := s.VerifySearchCacheInvalidationResult(ctx, identity, provider, rawQuery, previous.EventID, previous.Affected); err != nil {
			return 0, err
		}
		return previous.Affected, nil
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `DELETE FROM l1_search_cache WHERE query_hash = ?`, hash)
	if err != nil {
		return 0, rollbackL1Tx(tx, fmt.Errorf("failed to invalidate l1 search cache: %w", err))
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, rollbackL1Tx(tx, fmt.Errorf("failed to inspect l1 search cache invalidation: %w", err))
	}
	event, err := appendL1EventLog(ctx, tx, "search.cache_invalidated", searchNamespace, "", "", 0, "", map[string]interface{}{
		"query_hash": hash, "normalized_query": normalizedQuery, "raw_query": rawQuery,
		"provider": provider, "affected": affected,
		"storage_host_operation": map[string]interface{}{
			"op_id": identity.OpID, "operation": identity.Operation,
			"payload_sha256": identity.PayloadSHA256, "writer_generation": identity.WriterGeneration,
		},
	}, "search_cache")
	if err != nil {
		return 0, rollbackL1Tx(tx, fmt.Errorf("failed to append l1 search cache invalidated event log: %w", err))
	}
	receiptResult := ConversationL1SearchCacheInvalidationResult{Affected: affected, EventID: event.ID}
	if err := commitConversationL1Operation(ctx, tx, identity, receiptResult); err != nil {
		return 0, err
	}
	if err := s.VerifySearchCacheInvalidationResult(ctx, identity, provider, rawQuery, event.ID, affected); err != nil {
		return 0, err
	}
	return affected, nil
}

func (s *L1SQLiteStore) AppendEventForOperation(ctx context.Context, identity ConversationL1OperationIdentity, eventType, namespace, sessionID string, threadID modulecore.ThreadID, threadSeq modulecore.ThreadSeq, threadKind modulecore.ThreadKind, payload map[string]interface{}, source string) (*L1EventLogEntry, error) {
	if identity.Operation != "append_event" {
		return nil, errors.New("conversation L1 append event operation identity mismatch")
	}
	tx, prior, found, err := s.beginConversationL1Operation(ctx, identity)
	if err != nil {
		return nil, err
	}
	if found {
		var entry *L1EventLogEntry
		if err := decodeConversationL1OperationResult(prior, &entry); err != nil {
			return nil, err
		}
		if entry == nil {
			return nil, errors.New("conversation L1 event receipt result is empty")
		}
		return entry, nil
	}
	defer tx.Rollback()
	entry, err := appendL1EventLog(ctx, tx, eventType, namespace, sessionID, threadID, threadSeq, threadKind, payload, source)
	if err != nil {
		return nil, rollbackL1Tx(tx, err)
	}
	if err := commitConversationL1Operation(ctx, tx, identity, entry); err != nil {
		return nil, err
	}
	return entry, nil
}

func (s *L1SQLiteStore) UpdateMemoryStateForOperation(ctx context.Context, identity ConversationL1OperationIdentity, id, memoryState string) error {
	if identity.Operation != "update_memory_state" {
		return errors.New("conversation L1 update memory state operation identity mismatch")
	}
	if id == "" {
		return errors.New("l1 memory event id is required")
	}
	if err := validateMemoryState(memoryState); err != nil {
		return err
	}
	tx, prior, found, err := s.beginConversationL1Operation(ctx, identity)
	if err != nil {
		return err
	}
	if found {
		return validateConversationL1VoidResult(prior)
	}
	defer tx.Rollback()

	var namespace, sessionID, threadIDRaw, threadKindRaw, previousState string
	var threadSeqRaw int64
	if err := tx.QueryRowContext(ctx, `
SELECT namespace, session_id, thread_id, thread_seq, thread_kind, memory_state
FROM l1_memory_event WHERE id = ?`, id).Scan(&namespace, &sessionID, &threadIDRaw, &threadSeqRaw, &threadKindRaw, &previousState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return rollbackL1Tx(tx, sql.ErrNoRows)
		}
		return rollbackL1Tx(tx, fmt.Errorf("failed to load l1 memory event before state update: %w", err))
	}
	threadID := modulecore.ThreadID(threadIDRaw)
	threadSeq := modulecore.ThreadSeq(threadSeqRaw)
	threadKind := modulecore.ThreadKind(threadKindRaw)
	if err := validateL1SessionThreadTuple(sessionID, threadID, threadSeq, threadKind); err != nil {
		return rollbackL1Tx(tx, fmt.Errorf("failed to validate l1 memory event before state update: %w", err))
	}
	result, err := tx.ExecContext(ctx, `UPDATE l1_memory_event SET memory_state = ?, updated_at = ? WHERE id = ?`, memoryState, time.Now().UTC(), id)
	if err != nil {
		return rollbackL1Tx(tx, fmt.Errorf("failed to update l1 memory state: %w", err))
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return rollbackL1Tx(tx, fmt.Errorf("failed to inspect l1 memory state update: %w", err))
	}
	if affected == 0 {
		return rollbackL1Tx(tx, sql.ErrNoRows)
	}
	entry, err := appendL1EventLog(ctx, tx, "memory.state_updated", namespace, sessionID, threadID, threadSeq, threadKind, map[string]interface{}{
		"memory_id": id, "previous_state": previousState, "memory_state": memoryState,
	}, "memory")
	if err != nil {
		return rollbackL1Tx(tx, fmt.Errorf("failed to append l1 memory state event log: %w", err))
	}
	if strings.HasPrefix(namespace, "user:") {
		var metaJSON string
		if err := tx.QueryRowContext(ctx, `SELECT meta_json FROM l1_memory_event WHERE id = ?`, id).Scan(&metaJSON); err != nil {
			return rollbackL1Tx(tx, fmt.Errorf("failed to load user memory meta for event link update: %w", err))
		}
		meta := map[string]interface{}{}
		if strings.TrimSpace(metaJSON) != "" {
			if err := json.Unmarshal([]byte(metaJSON), &meta); err != nil {
				return rollbackL1Tx(tx, fmt.Errorf("failed to decode user memory meta for event link update: %w", err))
			}
		}
		writeUserMemoryEventLinkMeta(meta, "", auditEventID(entry))
		encodedMeta, err := marshalL1MetaJSON(meta, "failed to marshal memory meta")
		if err != nil {
			return rollbackL1Tx(tx, err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE l1_memory_event SET meta_json = ?, updated_at = ? WHERE id = ?`, encodedMeta, time.Now().UTC(), id); err != nil {
			return rollbackL1Tx(tx, fmt.Errorf("failed to persist user memory updated_by_event_id: %w", err))
		}
	}
	return commitConversationL1Operation(ctx, tx, identity, nil)
}

func (s *L1SQLiteStore) PromoteMemoryToNamespaceForOperation(ctx context.Context, identity ConversationL1OperationIdentity, id, targetNamespace, promotedBy string) (*L1MemoryEvent, error) {
	if identity.Operation != "promote_memory_to_namespace" {
		return nil, errors.New("conversation L1 promotion operation identity mismatch")
	}
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("l1 memory event id is required")
	}
	if err := ValidateL1Namespace(targetNamespace); err != nil {
		return nil, err
	}
	if s.archiveStore != nil {
		if archive, ok := s.archiveStore.(L1PromotionArchiveStore); !ok || archive == nil {
			return nil, ErrConversationL1OperationNeedsSingleSQLiteStore
		}
	}
	tx, prior, found, err := s.beginConversationL1Operation(ctx, identity)
	if err != nil {
		return nil, err
	}
	if found {
		var event *L1MemoryEvent
		if err := decodeConversationL1OperationResult(prior, &event); err != nil {
			return nil, err
		}
		if event == nil || event.Namespace != targetNamespace || event.MemoryState != MemoryStateConfirmed {
			return nil, errors.New("conversation L1 promotion receipt result does not match request")
		}
		if s.archiveStore != nil {
			if err := s.completeL1PromotionArchive(ctx, identity, *event); err != nil {
				return nil, err
			}
		}
		return event, nil
	}
	defer tx.Rollback()

	sourceRows, err := scanL1EventRows(tx.QueryRowContext(ctx, `
SELECT id, namespace, session_id, thread_id, thread_seq, thread_kind, speaker, message, meta_json,
       memory_state, layer, source, created_at, updated_at
FROM l1_memory_event WHERE id = ?`, id))
	if err != nil {
		return nil, rollbackL1Tx(tx, err)
	}
	source := sourceRows[0]
	now := time.Now().UTC()
	meta := make(map[string]interface{}, len(source.Meta)+2)
	for key, value := range source.Meta {
		meta[key] = value
	}
	meta["promoted_from"] = source.ID
	meta["promoted_by"] = promotedBy
	metaJSON, err := marshalL1MetaJSON(meta, "failed to marshal promoted l1 memory meta")
	if err != nil {
		return nil, rollbackL1Tx(tx, err)
	}
	promoted := &L1MemoryEvent{
		ID:        fmt.Sprintf("%s:%s:%d:%d", targetNamespace, source.ID, now.UnixNano(), l1IDSequence.Add(1)),
		Namespace: targetNamespace, SessionID: source.SessionID, ThreadID: source.ThreadID,
		ThreadSeq: source.ThreadSeq, ThreadKind: source.ThreadKind, Speaker: source.Speaker,
		Message: source.Message, Meta: meta, MemoryState: MemoryStateConfirmed,
		Layer: source.Layer, Source: "promoter", CreatedAt: now, UpdatedAt: now,
	}
	if err := validateL1MemoryEvent(*promoted); err != nil {
		return nil, rollbackL1Tx(tx, err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO l1_memory_event (
	id, namespace, session_id, thread_id, thread_seq, thread_kind, speaker, message, meta_json,
	memory_state, layer, source, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, promoted.ID, promoted.Namespace, promoted.SessionID, string(promoted.ThreadID), promoted.ThreadSeq, promoted.ThreadKind, string(promoted.Speaker), promoted.Message, metaJSON,
		promoted.MemoryState, promoted.Layer, promoted.Source, promoted.CreatedAt, promoted.UpdatedAt); err != nil {
		return nil, rollbackL1Tx(tx, fmt.Errorf("failed to promote l1 memory: %w", err))
	}
	if _, err := appendL1EventLog(ctx, tx, "memory.promoted", targetNamespace, source.SessionID, source.ThreadID, source.ThreadSeq, source.ThreadKind, map[string]interface{}{
		"source_memory_id": source.ID, "promoted_memory_id": promoted.ID,
		"promoted_by": promotedBy, "memory_state": promoted.MemoryState,
	}, "promoter"); err != nil {
		return nil, rollbackL1Tx(tx, fmt.Errorf("failed to append l1 memory promoted event log: %w", err))
	}
	if s.archiveStore != nil {
		if err := insertL1PromotionArchiveOutbox(ctx, tx, identity, *promoted); err != nil {
			return nil, rollbackL1Tx(tx, err)
		}
	}
	if err := commitConversationL1Operation(ctx, tx, identity, promoted); err != nil {
		return nil, err
	}
	if s.archiveStore != nil {
		if err := s.completeL1PromotionArchive(ctx, identity, *promoted); err != nil {
			return nil, err
		}
	}
	return promoted, nil
}

func (s *L1SQLiteStore) SaveRecallTraceForOperation(ctx context.Context, identity ConversationL1OperationIdentity, trace domconv.RecallTrace) error {
	if identity.Operation != "save_recall_trace" {
		return errors.New("conversation L1 recall trace operation identity mismatch")
	}
	if strings.TrimSpace(trace.SessionID) == "" {
		return errors.New("session_id is required")
	}
	if err := validateL1SessionThreadTuple(trace.SessionID, "", 0, ""); err != nil {
		return fmt.Errorf("invalid recall trace session identity: %w", err)
	}
	if trace.TraceID.Validate() != nil || trace.TurnID.Validate() != nil || trace.RootTaskID.Validate() != nil {
		return errors.New("canonical recall trace identity is required")
	}
	tx, prior, found, err := s.beginConversationL1Operation(ctx, identity)
	if err != nil {
		return err
	}
	if found {
		return validateConversationL1VoidResult(prior)
	}
	defer tx.Rollback()
	if trace.CreatedAt.IsZero() {
		trace.CreatedAt = time.Now().UTC()
	}
	traceID := string(trace.TraceID)
	queryText := ""
	for _, item := range trace.Items {
		if candidate := strings.TrimSpace(item.Query); candidate != "" {
			queryText = candidate
			break
		}
	}
	records := TraceItemRecordsFromPack(traceID, trace.Items)
	injectedCount := 0
	totalTokens := 0
	for _, item := range records {
		if item.Injected {
			injectedCount++
			totalTokens += item.TokenCount
		}
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO recall_trace (
	trace_id, owner_id, turn_id, root_task_id, chat_id, persona, route, user_message_hash, query_text_redacted,
	created_at, model_id, prompt_version, recall_policy_version, total_candidates,
	injected_count, total_injected_tokens, status
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, trace.TraceID, strings.TrimSpace(trace.OwnerID), trace.TurnID, trace.RootTaskID, trace.SessionID,
		firstNonEmptyString(trace.Role, "mio"), "", HashRecallText(queryText), RedactedRecallQuery(queryText),
		trace.CreatedAt.UTC(), "", "", "memory-lifecycle-v1", len(records), injectedCount, totalTokens, "started"); err != nil {
		return rollbackL1Tx(tx, fmt.Errorf("failed to start recall trace: %w", err))
	}
	for i, item := range records {
		if strings.TrimSpace(item.ItemID) == "" {
			item.ItemID = fmt.Sprintf("%s:item:%04d", traceID, i)
		}
		if strings.TrimSpace(item.TraceID) == "" {
			item.TraceID = traceID
		}
		if modulecore.TraceID(item.TraceID).Validate() != nil || item.TraceID != traceID {
			return rollbackL1Tx(tx, fmt.Errorf("trace item %s belongs to different trace %s", item.ItemID, item.TraceID))
		}
		injected := 0
		if item.Injected {
			injected = 1
		}
		var retrievedAt any
		if !item.RetrievedAt.IsZero() {
			retrievedAt = item.RetrievedAt.UTC()
		}
		var publishedAt any
		if !item.PublishedAt.IsZero() {
			publishedAt = item.PublishedAt.UTC()
		}
		if _, err := tx.ExecContext(ctx, `
INSERT OR REPLACE INTO recall_trace_item (
	item_id, trace_id, layer, memory_id, source_id, source_url, source_type, status,
	score, relevance, recency, confidence, source_trust, reason, injected,
	prompt_section, token_count, sensitivity, memory_state, is_raw_or_summary, retrieved_at,
	published_at, event_id, summary, kind
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, item.ItemID, item.TraceID, item.Layer, item.MemoryID, item.SourceID, item.SourceURL, item.SourceType, item.Status,
			item.Score, item.Relevance, item.Recency, item.Confidence, item.SourceTrust, item.Reason, injected,
			item.PromptSection, item.TokenCount, item.Sensitivity, item.MemoryState, item.IsRawOrSummary, retrievedAt,
			publishedAt, item.EventID, item.Summary, item.Kind); err != nil {
			return rollbackL1Tx(tx, fmt.Errorf("failed to insert recall trace item: %w", err))
		}
	}
	for i, event := range PromptInjectionEventsFromItems(traceID, records, trace.CreatedAt) {
		if strings.TrimSpace(event.InjectionID) == "" {
			event.InjectionID = fmt.Sprintf("%s:injection:%04d", traceID, i)
		}
		if strings.TrimSpace(event.TraceID) == "" {
			event.TraceID = traceID
		}
		if modulecore.TraceID(event.TraceID).Validate() != nil || event.TraceID != traceID {
			return rollbackL1Tx(tx, fmt.Errorf("prompt injection event %s belongs to different trace %s", event.InjectionID, event.TraceID))
		}
		if event.CreatedAt.IsZero() {
			event.CreatedAt = time.Now().UTC()
		}
		itemIDs, err := json.Marshal(event.ItemIDs)
		if err != nil {
			return rollbackL1Tx(tx, fmt.Errorf("failed to marshal prompt injection item ids: %w", err))
		}
		if _, err := tx.ExecContext(ctx, `
INSERT OR REPLACE INTO prompt_injection_event (
	injection_id, trace_id, prompt_section, order_index, item_ids, token_count, redaction_level, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
`, event.InjectionID, event.TraceID, event.PromptSection, event.OrderIndex, string(itemIDs), event.TokenCount, event.RedactionLevel, event.CreatedAt.UTC()); err != nil {
			return rollbackL1Tx(tx, fmt.Errorf("failed to insert prompt injection event: %w", err))
		}
	}
	result, err := tx.ExecContext(ctx, `
UPDATE recall_trace SET status = ?, injected_count = ?, total_injected_tokens = ? WHERE trace_id = ?
`, "completed", injectedCount, totalTokens, traceID)
	if err != nil {
		return rollbackL1Tx(tx, fmt.Errorf("failed to finish recall trace: %w", err))
	}
	if affected, err := result.RowsAffected(); err != nil || affected == 0 {
		if err != nil {
			return rollbackL1Tx(tx, err)
		}
		return rollbackL1Tx(tx, sql.ErrNoRows)
	}
	if _, err := appendL1EventLog(ctx, tx, "recall.trace", "conv:"+trace.SessionID, trace.SessionID, "", 0, "", map[string]interface{}{
		"trace_id": trace.TraceID, "turn_id": trace.TurnID, "root_task_id": trace.RootTaskID,
		"session_id": trace.SessionID, "role": trace.Role, "items": trace.Items,
		"created_at": trace.CreatedAt.UTC().Format(time.RFC3339),
	}, "recall"); err != nil {
		return rollbackL1Tx(tx, err)
	}
	return commitConversationL1Operation(ctx, tx, identity, nil)
}
