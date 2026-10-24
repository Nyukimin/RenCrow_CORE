package l1sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func ownerScopeForRecall(ctx context.Context, requestID, ownerID string) (domaintool.ToolExecutionScope, error) {
	scope, ok := domaintool.ToolExecutionScopeFromContext(ctx)
	if !ok || scope.Validate() != nil {
		return domaintool.ToolExecutionScope{}, domainmemory.ErrUserMemoryOwnerForbidden
	}
	if strings.TrimSpace(requestID) != "" && scope.RequestID != strings.TrimSpace(requestID) {
		return domaintool.ToolExecutionScope{}, domainmemory.ErrUserMemoryOwnerForbidden
	}
	if strings.TrimSpace(ownerID) == "" || scope.AuthenticatedUserID != strings.TrimSpace(ownerID) || !scope.Allows(domaintool.DataScopeUser) {
		return domaintool.ToolExecutionScope{}, domainmemory.ErrUserMemoryOwnerForbidden
	}
	return scope, nil
}

func chatOwnerScopeForRecall(ctx context.Context, ownerID string) error {
	scope, ok := domaintool.ToolExecutionScopeFromContext(ctx)
	if !ok || scope.Validate() != nil || scope.AuthenticatedUserID != ownerID || !scope.Allows(domaintool.DataScopeUser) || scope.Allows(domaintool.DataScopeInternal) {
		return domainmemory.ErrUserMemoryOwnerForbidden
	}
	userProof, ok := domaintool.AuthenticatedHTTPUserScopeFromContext(ctx)
	if !ok || userProof.Validate() != nil || userProof.ActorKind != domaintool.ActorKindUser || userProof.AuthenticationSource != domaintool.AuthenticationSourceHTTP || userProof.ActorID != ownerID || userProof.AuthenticatedUserID != ownerID || !userProof.Allows(domaintool.DataScopeUser) {
		return domainmemory.ErrUserMemoryOwnerForbidden
	}
	switch scope.ActorKind {
	case domaintool.ActorKindUser:
		if scope.AuthenticationSource != domaintool.AuthenticationSourceHTTP || scope.ActorID != ownerID {
			return domainmemory.ErrUserMemoryOwnerForbidden
		}
	case domaintool.ActorKindAgent:
		if scope.AuthenticationSource != domaintool.AuthenticationSourceAgentOrchestrator || scope.ActorID != "mio" || (scope.AgentRole != "chat" && scope.AgentRole != "conversation") {
			return domainmemory.ErrUserMemoryOwnerForbidden
		}
	default:
		return domainmemory.ErrUserMemoryOwnerForbidden
	}
	return nil
}

func normalizeUserMemoryChatReadRequest(request domainmemory.UserMemoryChatReadRequest) (domainmemory.UserMemoryChatReadRequest, error) {
	if strings.TrimSpace(request.OwnerID) != request.OwnerID {
		return domainmemory.UserMemoryChatReadRequest{}, domainmemory.ErrUserMemoryOwnerInvalid
	}
	request.OwnerID = strings.TrimSpace(request.OwnerID)
	request.Query = strings.TrimSpace(request.Query)
	request.Persona = strings.ToLower(strings.TrimSpace(request.Persona))
	if request.Persona == "" {
		request.Persona = "mio"
	}
	if request.OwnerID == "" || !utf8.ValidString(request.Query) || request.Query == "" || len([]rune(request.Query)) > 512 || request.Persona != "mio" {
		return domainmemory.UserMemoryChatReadRequest{}, domainmemory.ErrUserMemoryOwnerInvalid
	}
	if _, err := BuildL1Namespace(NamespaceKindUser, request.OwnerID); err != nil {
		return domainmemory.UserMemoryChatReadRequest{}, fmt.Errorf("%w: %v", domainmemory.ErrUserMemoryOwnerInvalid, err)
	}
	if err := request.SessionID.Validate(); err != nil {
		return domainmemory.UserMemoryChatReadRequest{}, fmt.Errorf("%w: invalid chat session ID", domainmemory.ErrUserMemoryOwnerInvalid)
	}
	if err := request.TurnID.Validate(); err != nil {
		return domainmemory.UserMemoryChatReadRequest{}, fmt.Errorf("%w: invalid chat turn ID", domainmemory.ErrUserMemoryOwnerInvalid)
	}
	if err := request.TraceID.Validate(); err != nil {
		return domainmemory.UserMemoryChatReadRequest{}, fmt.Errorf("%w: invalid chat trace ID", domainmemory.ErrUserMemoryOwnerInvalid)
	}
	if err := request.RootTaskID.Validate(); err != nil {
		return domainmemory.UserMemoryChatReadRequest{}, fmt.Errorf("%w: invalid chat root task ID", domainmemory.ErrUserMemoryOwnerInvalid)
	}
	if string(request.TurnID) == string(request.TraceID) || string(request.TurnID) == string(request.RootTaskID) || string(request.TraceID) == string(request.RootTaskID) {
		return domainmemory.UserMemoryChatReadRequest{}, domainmemory.ErrUserMemoryOwnerInvalid
	}
	if request.Limit <= 0 {
		request.Limit = domainmemory.UserMemoryRecallDefaultLimit
	}
	if request.Limit > domainmemory.UserMemoryRecallMaxLimit || len(request.References) > domainmemory.UserMemoryRecallMaxReferences {
		return domainmemory.UserMemoryChatReadRequest{}, domainmemory.ErrUserMemoryOwnerInvalid
	}
	if len(request.References) > 0 {
		return domainmemory.UserMemoryChatReadRequest{}, domainmemory.ErrUserMemoryRecallReferencesUnsupported
	}
	return request, nil
}

func (s *L1SQLiteStore) readAndRankUserMemories(ctx context.Context, ownerID, query, persona string, limit int) ([]domainmemory.UserMemoryRecallDecision, error) {
	memories, err := s.OwnerListUserMemories(ctx, ownerID, "", true, domainmemory.UserMemoryRecallMaxScan)
	if err != nil {
		return nil, err
	}
	for _, item := range memories {
		if item.UserID != ownerID || item.Namespace != NamespaceKindUser+":"+ownerID {
			return nil, domainmemory.ErrUserMemoryOwnerForbidden
		}
	}
	if persona == "" {
		return domainmemory.RankUserMemoriesForRecall(query, memories, limit), nil
	}
	return domainmemory.RankUserMemoriesForRecallForPersona(query, memories, limit, persona), nil
}

// readAndRankChatUserMemories applies Mio's recall eligibility in SQLite
// before the bounded candidate LIMIT. The owner-facing list/recall path keeps
// its existing newest-row window; chat must not let newer ineligible rows
// hide an older confirmed memory.
func (s *L1SQLiteStore) readAndRankChatUserMemories(ctx context.Context, ownerID, query string, limit int) ([]domainmemory.UserMemoryRecallDecision, bool, error) {
	memories, hasMore, err := s.listEligibleChatUserMemories(ctx, ownerID, domainmemory.UserMemoryRecallMaxScan)
	if err != nil {
		return nil, false, err
	}
	decisions := domainmemory.RankUserMemoriesForRecallForPersona(query, memories, limit, "mio")
	for _, decision := range decisions {
		if decision.Status != domainmemory.UserMemoryRecallStatusInjected && decision.Status != domainmemory.UserMemoryRecallStatusBudgetDropped {
			return nil, false, fmt.Errorf("%w: chat UserMemory eligibility query returned an ineligible row", domainmemory.ErrUserMemoryOwnerUnavailable)
		}
	}
	return decisions, hasMore, nil
}

func (s *L1SQLiteStore) listEligibleChatUserMemories(ctx context.Context, ownerID string, limit int) ([]domainmemory.UserMemory, bool, error) {
	if s == nil || s.db == nil {
		return nil, false, domainmemory.ErrUserMemoryOwnerUnavailable
	}
	if limit <= 0 || limit > domainmemory.UserMemoryRecallMaxScan {
		return nil, false, domainmemory.ErrUserMemoryOwnerInvalid
	}
	query := `
SELECT id, namespace, session_id, thread_id, thread_seq, thread_kind, speaker, message, meta_json,
       memory_state, layer, source, created_at, updated_at
FROM l1_memory_event
WHERE namespace = ? AND speaker = ? AND layer = ?
  AND memory_state IN (?, ?)
  AND COALESCE(json_extract(meta_json, '$.active'), 1) = 1
  AND trim(COALESCE(json_extract(meta_json, '$.superseded_by'), '')) = ''
  AND lower(trim(COALESCE(json_extract(meta_json, '$.lifecycle_status'), ''))) <> 'decayed'
  AND lower(trim(COALESCE(json_extract(meta_json, '$.sensitivity'), 'normal'))) = 'normal'
  AND lower(trim(COALESCE(json_extract(meta_json, '$.scope'), 'all_personas'))) IN ('', 'all', 'all_personas', 'global', 'mio', 'mio_only')
ORDER BY created_at DESC, rowid DESC
LIMIT ?`
	rows, err := s.db.QueryContext(ctx, query,
		NamespaceKindUser+":"+ownerID, string(domconv.SpeakerMemory), MemoryLayerL1,
		domainmemory.MemoryStateConfirmed, domainmemory.MemoryStatePinned, limit+1,
	)
	if err != nil {
		return nil, false, fmt.Errorf("failed to query eligible chat UserMemory candidates: %w", err)
	}
	defer rows.Close()
	events, err := scanL1Events(rows)
	if err != nil {
		return nil, false, err
	}
	hasMore := len(events) > limit
	if hasMore {
		events = events[:limit]
	}
	memories := make([]domainmemory.UserMemory, 0, len(events))
	for _, event := range events {
		item, strictErr := strictUserMemoryFromEvent(event)
		if strictErr != nil || item.UserID != ownerID || item.Namespace != NamespaceKindUser+":"+ownerID {
			return nil, false, fmt.Errorf("%w: selected chat UserMemory row is invalid", domainmemory.ErrUserMemoryOwnerUnavailable)
		}
		memories = append(memories, *item)
	}
	return memories, hasMore, nil
}

// ReadUserMemoriesForChat returns a bounded owner-scoped selection for the
// current Mio turn. It reuses the owner recall ranking, echoes canonical IDs,
// and returns source provenance for a later capability guard. It does not
// create recall traces, prompt-injection events, receipts, or IDs.
func (s *L1SQLiteStore) ReadUserMemoriesForChat(ctx context.Context, request domainmemory.UserMemoryChatReadRequest) (domainmemory.UserMemoryChatReadResult, error) {
	if ctx == nil {
		return domainmemory.UserMemoryChatReadResult{}, domainmemory.ErrUserMemoryOwnerInvalid
	}
	request, err := normalizeUserMemoryChatReadRequest(request)
	if err != nil {
		return domainmemory.UserMemoryChatReadResult{}, err
	}
	if err := chatOwnerScopeForRecall(ctx, request.OwnerID); err != nil {
		return domainmemory.UserMemoryChatReadResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return domainmemory.UserMemoryChatReadResult{}, fmt.Errorf("%w: chat UserMemory read cancelled: %w", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}

	decisions, hasMore, err := s.readAndRankChatUserMemories(ctx, request.OwnerID, request.Query, request.Limit)
	if err != nil {
		if errors.Is(err, domainmemory.ErrUserMemoryOwnerForbidden) {
			return domainmemory.UserMemoryChatReadResult{}, err
		}
		return domainmemory.UserMemoryChatReadResult{}, fmt.Errorf("%w: failed to read chat UserMemory candidates: %w", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	selected := make([]domainmemory.UserMemoryRecallDecision, 0, request.Limit)
	for _, decision := range decisions {
		if decision.Selected {
			selected = append(selected, decision)
		}
	}
	references, err := s.readChatUserMemorySourceReferences(ctx, request.OwnerID, request.SessionID, selected)
	if err != nil {
		return domainmemory.UserMemoryChatReadResult{}, err
	}
	result := domainmemory.UserMemoryChatReadResult{
		OwnerID: request.OwnerID, SessionID: request.SessionID, TurnID: request.TurnID,
		TraceID: request.TraceID, RootTaskID: request.RootTaskID, Persona: request.Persona,
		TotalCandidates: len(decisions), HasMore: hasMore, Items: make([]domainmemory.UserMemoryChatRecallItem, 0, len(selected)),
	}
	for _, decision := range selected {
		projection := domainmemory.UserMemoryOwnerRecallItem{
			UserMemoryOwnerView: domainmemory.UserMemoryOwnerViewFromMemory(decision.Item),
			Score:               decision.Score,
		}
		result.Items = append(result.Items, domainmemory.UserMemoryChatRecallItem{Item: projection, Reference: references[decision.Item.ID]})
	}
	return result, nil
}

func (s *L1SQLiteStore) readChatUserMemorySourceReferences(ctx context.Context, ownerID string, sessionID modulecore.SessionID, selected []domainmemory.UserMemoryRecallDecision) (map[string]domainmemory.UserMemoryChatRecallSourceReference, error) {
	references := make(map[string]domainmemory.UserMemoryChatRecallSourceReference, len(selected))
	if len(selected) == 0 {
		return references, nil
	}
	if len(selected) > domainmemory.UserMemoryRecallMaxLimit {
		return nil, domainmemory.ErrUserMemoryOwnerInvalid
	}
	queryDB := s.readDB
	if queryDB == nil {
		queryDB = s.db
	}
	if queryDB == nil {
		return nil, domainmemory.ErrUserMemoryOwnerUnavailable
	}
	ids := make(map[string]domainmemory.UserMemoryRecallDecision, len(selected))
	args := []interface{}{NamespaceKindUser + ":" + ownerID, string(domconv.SpeakerMemory), MemoryLayerL1}
	for _, decision := range selected {
		if decision.Item.ID == "" || len(decision.Item.EvidenceEventIDs) == 0 || len(decision.Item.EvidenceEventIDs) > domainmemory.UserMemoryRecallMaxEvidenceReferences {
			return nil, fmt.Errorf("%w: selected UserMemory has missing or excessive source references", domainmemory.ErrUserMemoryOwnerUnavailable)
		}
		ids[decision.Item.ID] = decision
		args = append(args, decision.Item.ID)
	}
	query := `
SELECT id, namespace, source, updated_at
FROM l1_memory_event
WHERE namespace = ? AND speaker = ? AND layer = ? AND id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(selected)), ",") + `)
ORDER BY id ASC
LIMIT ?`
	args = append(args, len(selected)+1)
	rows, err := queryDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to read chat UserMemory provenance: %w", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, namespace, source string
		var updatedAt time.Time
		if err := rows.Scan(&id, &namespace, &source, &updatedAt); err != nil {
			return nil, fmt.Errorf("%w: failed to scan chat UserMemory provenance: %w", domainmemory.ErrUserMemoryOwnerUnavailable, err)
		}
		decision, ok := ids[id]
		if !ok || namespace != NamespaceKindUser+":"+ownerID || strings.TrimSpace(source) == "" || !decision.Item.UpdatedAt.Equal(updatedAt) {
			return nil, fmt.Errorf("%w: chat UserMemory provenance no longer matches the selected owner projection", domainmemory.ErrUserMemoryOwnerUnavailable)
		}
		evidenceIDs := append([]string(nil), decision.Item.EvidenceEventIDs...)
		for _, evidenceID := range evidenceIDs {
			if strings.TrimSpace(evidenceID) == "" {
				return nil, fmt.Errorf("%w: selected UserMemory has an empty evidence reference", domainmemory.ErrUserMemoryOwnerUnavailable)
			}
		}
		references[id] = domainmemory.UserMemoryChatRecallSourceReference{
			OwnerID: ownerID, SessionID: sessionID, MemoryID: id, Namespace: namespace,
			SourceType: "user_memory", Source: source, EvidenceEventIDs: evidenceIDs,
			CreatedByEventID: decision.Item.CreatedByEventID, UpdatedByEventID: decision.Item.UpdatedByEventID,
			PersonaScope: decision.Item.Scope, State: decision.Item.State, Sensitivity: decision.Item.Sensitivity,
			Active: decision.Item.Active, SupersededBy: decision.Item.SupersededBy,
			UpdatedAt:    updatedAt.UTC(),
			OwnerVersion: updatedAt.UTC().Format(time.RFC3339Nano),
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: failed to iterate chat UserMemory provenance: %w", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("%w: failed to close chat UserMemory provenance: %w", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	if len(references) != len(selected) {
		return nil, fmt.Errorf("%w: chat UserMemory source reference is missing", domainmemory.ErrUserMemoryOwnerUnavailable)
	}
	return references, nil
}

// OwnerRecallUserMemories performs the owner-only deterministic recall and
// persists its bounded decision trace before returning the owner projection.
func (s *L1SQLiteStore) OwnerRecallUserMemories(ctx context.Context, requestID, ownerID, query string, limit int) (domainmemory.UserMemoryOwnerRecallResult, error) {
	requestID = strings.TrimSpace(requestID)
	ownerID = strings.TrimSpace(ownerID)
	query = strings.TrimSpace(query)
	if requestID == "" || ownerID == "" || query == "" || len([]rune(query)) > 512 {
		return domainmemory.UserMemoryOwnerRecallResult{}, domainmemory.ErrUserMemoryOwnerInvalid
	}
	if limit <= 0 {
		limit = domainmemory.UserMemoryRecallDefaultLimit
	}
	if limit > domainmemory.UserMemoryRecallMaxLimit {
		return domainmemory.UserMemoryOwnerRecallResult{}, domainmemory.ErrUserMemoryOwnerInvalid
	}
	if _, err := ownerScopeForRecall(ctx, requestID, ownerID); err != nil {
		return domainmemory.UserMemoryOwnerRecallResult{}, err
	}

	now := time.Now().UTC()
	traceID := modulecore.NewTraceID()
	traceIDText := string(traceID)
	trace := domconv.RecallTraceRecord{
		TraceID:             traceID,
		OwnerID:             ownerID,
		TurnID:              modulecore.NewTurnID(),
		RootTaskID:          modulecore.NewTaskID(),
		ChatID:              ownerID,
		Persona:             "owner",
		Route:               "conversation_l1/user_memory/recall",
		UserMessageHash:     HashRecallText(query),
		QueryTextRedacted:   RedactedRecallQuery(query),
		CreatedAt:           now,
		RecallPolicyVersion: domainmemory.UserMemoryOwnerPolicyRevision,
		Status:              "started",
	}
	if err := s.StartRecallTrace(ctx, trace); err != nil {
		return domainmemory.UserMemoryOwnerRecallResult{}, fmt.Errorf("%w: failed to start owner recall trace: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}

	decisions, err := s.readAndRankUserMemories(ctx, ownerID, query, "", limit)
	if err != nil {
		if errors.Is(err, domainmemory.ErrUserMemoryOwnerForbidden) {
			return domainmemory.UserMemoryOwnerRecallResult{}, err
		}
		failure := domconv.RecallTraceItemRecord{
			ItemID:        traceIDText + ":source-failure",
			TraceID:       traceIDText,
			Layer:         "L1",
			Kind:          "user_memory_source_failure",
			Status:        domainmemory.UserMemoryRecallStatusSourceFailure,
			Reason:        "user memory source unavailable",
			PromptSection: domconv.PromptSectionUserMemory,
		}
		if addErr := s.AddRecallTraceItems(ctx, traceIDText, []domconv.RecallTraceItemRecord{failure}); addErr == nil {
			_, _ = s.db.ExecContext(ctx, `UPDATE recall_trace SET total_candidates = 0 WHERE trace_id = ?`, traceID)
			_ = s.FinishRecallTrace(ctx, traceIDText, "partial", 0, 0)
		}
		return domainmemory.UserMemoryOwnerRecallResult{}, fmt.Errorf("%w: failed to read owner memories: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	selected := make([]domainmemory.UserMemoryOwnerRecallItem, 0, limit)
	traceItems := make([]domconv.RecallTraceItemRecord, 0, len(decisions))
	for i, decision := range decisions {
		traceItem := domconv.RecallTraceItemRecord{
			ItemID:         fmt.Sprintf("%s:item:%04d", traceIDText, i),
			TraceID:        traceIDText,
			Layer:          "L1",
			Kind:           "user_memory",
			MemoryID:       decision.Item.ID,
			SourceType:     "user_memory",
			Status:         decision.Status,
			Score:          decision.Score,
			Reason:         decision.Reason,
			Injected:       decision.Selected,
			PromptSection:  domconv.PromptSectionUserMemory,
			MemoryState:    decision.Item.State,
			Sensitivity:    decision.Item.Sensitivity,
			IsRawOrSummary: "summary",
		}
		if len(decision.Item.EvidenceEventIDs) > 0 {
			traceItem.SourceID = decision.Item.EvidenceEventIDs[0]
		}
		if decision.Selected {
			traceItem.Summary = redactOwnerTraceSummary(decision.Item.Statement, decision.Item.Sensitivity, traceItem.Kind)
			traceItem.TokenCount = ownerTraceTokenCount(traceItem.Summary)
			selected = append(selected, domainmemory.UserMemoryOwnerRecallItem{
				UserMemoryOwnerView: domainmemory.UserMemoryOwnerViewFromMemory(decision.Item),
				Score:               decision.Score,
			})
		}
		traceItems = append(traceItems, traceItem)
	}
	trace.TotalCandidates = len(decisions)
	selectedCount := 0
	for _, decision := range decisions {
		if decision.Selected {
			selectedCount++
		}
	}
	trace.InjectedCount = selectedCount
	if _, err := s.db.ExecContext(ctx, `UPDATE recall_trace SET total_candidates = ? WHERE trace_id = ?`, trace.TotalCandidates, traceID); err != nil {
		return domainmemory.UserMemoryOwnerRecallResult{}, fmt.Errorf("%w: failed to update owner recall trace totals: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	if err := s.AddRecallTraceItems(ctx, traceIDText, traceItems); err != nil {
		return domainmemory.UserMemoryOwnerRecallResult{}, fmt.Errorf("%w: failed to save owner recall trace items: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	if err := s.AddPromptInjectionEvents(ctx, traceIDText, PromptInjectionEventsFromItems(traceIDText, traceItems, now)); err != nil {
		return domainmemory.UserMemoryOwnerRecallResult{}, fmt.Errorf("%w: failed to save owner recall injection events: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	if err := s.FinishRecallTrace(ctx, traceIDText, "completed", selectedCount, sumTraceTokens(traceItems)); err != nil {
		return domainmemory.UserMemoryOwnerRecallResult{}, fmt.Errorf("%w: failed to finish owner recall trace: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}

	return domainmemory.UserMemoryOwnerRecallResult{
		Items: selected,
		Trace: domainmemory.UserMemoryRecallTrace{
			ID:                traceIDText,
			Status:            "completed",
			QueryTextRedacted: trace.QueryTextRedacted,
			TotalCandidates:   trace.TotalCandidates,
			SelectedCount:     selectedCount,
			CreatedAt:         now,
		},
		Receipt: newOwnerReadReceipt(requestID, domainmemory.UserMemoryOwnerOperationRecall, traceIDText, len(decisions), len(selected), now),
	}, nil
}

func (s *L1SQLiteStore) OwnerListRecallTraces(ctx context.Context, ownerID string, limit int) ([]domainmemory.UserMemoryTraceSummary, error) {
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return nil, domainmemory.ErrUserMemoryOwnerInvalid
	}
	if _, err := ownerScopeForRecall(ctx, "", ownerID); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = domainmemory.UserMemoryTraceDefaultLimit
	}
	if limit > domainmemory.UserMemoryTraceMaxLimit {
		return nil, domainmemory.ErrUserMemoryOwnerInvalid
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT trace_id, status, route, persona, total_candidates, injected_count,
       total_injected_tokens, created_at
FROM recall_trace
WHERE owner_id = ?
ORDER BY created_at DESC, trace_id ASC
LIMIT ?`, ownerID, limit)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to list owner recall traces: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	defer rows.Close()
	items := make([]domainmemory.UserMemoryTraceSummary, 0)
	for rows.Next() {
		var item domainmemory.UserMemoryTraceSummary
		if err := rows.Scan(&item.ID, &item.Status, &item.Route, &item.Persona, &item.TotalCandidates, &item.InjectedCount, &item.TotalInjectedTokens, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("%w: failed to scan owner recall trace: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: failed to iterate owner recall traces: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	return items, nil
}

func (s *L1SQLiteStore) OwnerFindRecallTrace(ctx context.Context, ownerID, id string) (domainmemory.UserMemoryTraceDetail, error) {
	ownerID = strings.TrimSpace(ownerID)
	id = strings.TrimSpace(id)
	if ownerID == "" || id == "" {
		return domainmemory.UserMemoryTraceDetail{}, domainmemory.ErrUserMemoryOwnerInvalid
	}
	if _, err := ownerScopeForRecall(ctx, "", ownerID); err != nil {
		return domainmemory.UserMemoryTraceDetail{}, err
	}
	var detail domainmemory.UserMemoryTraceDetail
	if err := s.db.QueryRowContext(ctx, `
SELECT trace_id, status, route, persona, total_candidates, injected_count,
       total_injected_tokens, created_at, query_text_redacted
FROM recall_trace
WHERE trace_id = ? AND owner_id = ?`, id, ownerID).Scan(
		&detail.ID, &detail.Status, &detail.Route, &detail.Persona, &detail.TotalCandidates,
		&detail.InjectedCount, &detail.TotalInjectedTokens, &detail.CreatedAt, &detail.QueryTextRedacted); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domainmemory.UserMemoryTraceDetail{}, domainmemory.ErrUserMemoryOwnerNotFound
		}
		return domainmemory.UserMemoryTraceDetail{}, fmt.Errorf("%w: failed to find owner recall trace: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT item_id, memory_id, kind, source_id, source_type, summary, score,
       status, reason, prompt_section, token_count, memory_state, sensitivity
FROM recall_trace_item
WHERE trace_id = ?
ORDER BY item_id ASC`, id)
	if err != nil {
		return domainmemory.UserMemoryTraceDetail{}, fmt.Errorf("%w: failed to read owner recall trace items: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	defer rows.Close()
	for rows.Next() {
		var item domainmemory.UserMemoryTraceItem
		if err := rows.Scan(&item.ItemID, &item.MemoryID, &item.Kind, &item.SourceID, &item.SourceType, &item.Summary, &item.Score, &item.Status, &item.Reason, &item.PromptSection, &item.TokenCount, &item.MemoryState, &item.Sensitivity); err != nil {
			return domainmemory.UserMemoryTraceDetail{}, fmt.Errorf("%w: failed to scan owner recall trace item: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
		}
		item.Summary = redactOwnerTraceSummary(item.Summary, item.Sensitivity, item.Kind)
		if item.Summary == "" {
			item.TokenCount = 0
		}
		detail.Items = append(detail.Items, item)
	}
	if err := rows.Err(); err != nil {
		return domainmemory.UserMemoryTraceDetail{}, fmt.Errorf("%w: failed to iterate owner recall trace items: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	if detail.Items == nil {
		detail.Items = []domainmemory.UserMemoryTraceItem{}
	}
	return detail, nil
}

func newOwnerReadReceipt(requestID, operation, auditReference string, inputCount, outputCount int, completedAt time.Time) domainmemory.UserMemoryOwnerReceipt {
	return domainmemory.UserMemoryOwnerReceipt{
		RequestID: requestID, Operation: operation, Status: "completed",
		OwnerRoute:     "conversation_l1/user_memory/" + operation,
		PolicyRevision: domainmemory.UserMemoryOwnerPolicyRevision,
		IdempotencyKey: requestID, Warnings: []string{}, InputCount: inputCount, OutputCount: outputCount,
		AuditReference: auditReference, CompletedAt: completedAt,
	}
}

func redactOwnerTraceSummary(summary, sensitivity, kind string) string {
	if strings.TrimSpace(sensitivity) != "" && !strings.EqualFold(strings.TrimSpace(sensitivity), "normal") {
		return ""
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "short_context" || kind == "rolling_summary" || kind == "full_transcript" || kind == "conversation_full_transcript" || kind == "transcript" {
		return ""
	}
	runes := []rune(summary)
	if len(runes) > 240 {
		runes = runes[:240]
	}
	return string(runes)
}

func ownerTraceTokenCount(summary string) int {
	if strings.TrimSpace(summary) == "" {
		return 0
	}
	return len([]rune(summary))/4 + 1
}

func sumTraceTokens(items []domconv.RecallTraceItemRecord) int {
	total := 0
	for _, item := range items {
		if item.Injected {
			total += item.TokenCount
		}
	}
	return total
}
