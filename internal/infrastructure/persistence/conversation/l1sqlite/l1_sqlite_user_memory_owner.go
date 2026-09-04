package l1sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

const ownerMemorySourcePrefix = "operator:"

type ownerMemoryReceiptRow struct {
	RequestID      string
	Operation      string
	OwnerID        string
	ActorID        string
	PayloadHash    string
	MemoryID       string
	AuditReference string
	ResultJSON     string
	CreatedAt      time.Time
}

func (s *L1SQLiteStore) OwnerListUserMemories(ctx context.Context, userID, state string, includeInactive bool, limit int) ([]domainmemory.UserMemory, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, domainmemory.ErrUserMemoryOwnerInvalid
	}
	if _, err := BuildL1Namespace(NamespaceKindUser, userID); err != nil {
		return nil, fmt.Errorf("%w: %v", domainmemory.ErrUserMemoryOwnerInvalid, err)
	}
	state = strings.TrimSpace(state)
	if state != "" {
		if err := domainmemory.ValidateMemoryState(state); err != nil {
			return nil, fmt.Errorf("%w: %v", domainmemory.ErrUserMemoryOwnerInvalid, err)
		}
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	namespace := NamespaceKindUser + ":" + userID
	query := `
	SELECT id, namespace, session_id, thread_id, thread_seq, thread_kind, speaker, message, meta_json,
       memory_state, layer, source, created_at, updated_at
FROM l1_memory_event
WHERE namespace = ? AND speaker = ? AND layer = ?`
	args := []interface{}{namespace, string(domconv.SpeakerMemory), MemoryLayerL1}
	if !includeInactive {
		// Apply the active projection before LIMIT. Filtering only after the
		// query can hide older active memories behind newer inactive rows.
		query += " AND COALESCE(json_extract(meta_json, '$.active'), 1) = 1"
	}
	if state != "" {
		query += " AND memory_state = ?"
		args = append(args, state)
	}
	query += " ORDER BY created_at DESC, rowid DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query owner user memories: %w", err)
	}
	defer rows.Close()
	events, err := scanL1Events(rows)
	if err != nil {
		return nil, err
	}
	items := make([]domainmemory.UserMemory, 0, len(events))
	for _, event := range events {
		if event.Speaker != domconv.SpeakerMemory || event.Layer != MemoryLayerL1 {
			continue
		}
		item, strictErr := strictUserMemoryFromEvent(event)
		if strictErr != nil {
			// Non-user-memory rows in the owner namespace are not a safe
			// projection and must not leak into the owner API.
			continue
		}
		if !includeInactive && !item.Active {
			continue
		}
		items = append(items, *item)
	}
	return items, nil
}

func (s *L1SQLiteStore) OwnerFindUserMemory(ctx context.Context, userID, id string) (domainmemory.UserMemory, error) {
	userID = strings.TrimSpace(userID)
	id = strings.TrimSpace(id)
	if userID == "" || id == "" {
		return domainmemory.UserMemory{}, domainmemory.ErrUserMemoryOwnerInvalid
	}
	item, found, err := s.FindUserMemoryByID(ctx, id)
	if err != nil {
		return domainmemory.UserMemory{}, err
	}
	if !found || item.UserID != userID || item.Namespace != NamespaceKindUser+":"+userID {
		return domainmemory.UserMemory{}, domainmemory.ErrUserMemoryOwnerNotFound
	}
	return item, nil
}

// OwnerProposeUserMemory persists the operator evidence, candidate projection,
// audit event and idempotency receipt in one SQLite transaction.
func (s *L1SQLiteStore) OwnerProposeUserMemory(ctx context.Context, requestID, ownerID, actorID, memoryType, statement, reason string) (domainmemory.UserMemoryOwnerResult, error) {
	requestID = strings.TrimSpace(requestID)
	ownerID = strings.TrimSpace(ownerID)
	actorID = strings.TrimSpace(actorID)
	memoryType = strings.TrimSpace(memoryType)
	statement = strings.TrimSpace(statement)
	reason = strings.TrimSpace(reason)
	if requestID == "" || ownerID == "" || actorID == "" || statement == "" || reason == "" {
		return domainmemory.UserMemoryOwnerResult{}, domainmemory.ErrUserMemoryOwnerInvalid
	}
	if err := domainmemory.ValidateUserMemoryType(memoryType); err != nil {
		return domainmemory.UserMemoryOwnerResult{}, fmt.Errorf("%w: %v", domainmemory.ErrUserMemoryOwnerInvalid, err)
	}
	namespace, err := BuildL1Namespace(NamespaceKindUser, ownerID)
	if err != nil {
		return domainmemory.UserMemoryOwnerResult{}, fmt.Errorf("%w: %v", domainmemory.ErrUserMemoryOwnerInvalid, err)
	}
	payloadHash := ownerMemoryPayloadHash(domainmemory.UserMemoryOwnerOperationPropose, ownerID, actorID, "", "", memoryType, statement, reason)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domainmemory.UserMemoryOwnerResult{}, err
	}
	if replay, ok, err := ownerMemoryReplay(ctx, tx, requestID, domainmemory.UserMemoryOwnerOperationPropose, ownerID, actorID, payloadHash); err != nil {
		return domainmemory.UserMemoryOwnerResult{}, rollbackL1Tx(tx, err)
	} else if ok {
		if err := tx.Commit(); err != nil {
			return domainmemory.UserMemoryOwnerResult{}, err
		}
		return replay, nil
	}

	now := time.Now().UTC()
	candidateID := userMemoryCandidateID(requestID)
	evidenceID := ownerMemoryEvidenceID(requestID)
	evidenceMeta := map[string]interface{}{
		"type":        "operator_evidence",
		"user_id":     ownerID,
		"statement":   statement,
		"request_id":  requestID,
		"actor_id":    actorID,
		"reason":      reason,
		"active":      true,
		"source_kind": "operator",
	}
	candidateMeta := map[string]interface{}{
		"type":               memoryType,
		"user_id":            ownerID,
		"statement":          statement,
		"evidence_event_ids": []string{evidenceID},
		"confidence":         0.5,
		"sensitivity":        "normal",
		"scope":              "all_personas",
		"active":             true,
		"actor_id":           actorID,
		"request_id":         requestID,
	}
	evidenceMetaJSON, err := marshalL1MetaJSON(evidenceMeta, "failed to marshal owner evidence meta")
	if err != nil {
		return domainmemory.UserMemoryOwnerResult{}, rollbackL1Tx(tx, err)
	}
	candidateMetaJSON, err := marshalL1MetaJSON(candidateMeta, "failed to marshal owner candidate meta")
	if err != nil {
		return domainmemory.UserMemoryOwnerResult{}, rollbackL1Tx(tx, err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO l1_memory_event (
	id, namespace, session_id, thread_id, thread_seq, thread_kind, speaker, message, meta_json,
	memory_state, layer, source, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, evidenceID, namespace, "", "", 0, "", string(domconv.SpeakerUser), statement, evidenceMetaJSON,
		MemoryStateObserved, MemoryLayerL1, ownerMemorySourcePrefix+actorID, now, now); err != nil {
		return domainmemory.UserMemoryOwnerResult{}, rollbackL1Tx(tx, fmt.Errorf("failed to create owner evidence: %w", err))
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO l1_memory_event (
	id, namespace, session_id, thread_id, thread_seq, thread_kind, speaker, message, meta_json,
	memory_state, layer, source, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, candidateID, namespace, "", "", 0, "", string(domconv.SpeakerMemory), statement, candidateMetaJSON,
		MemoryStateCandidate, MemoryLayerL1, ownerMemorySourcePrefix+actorID, now, now); err != nil {
		return domainmemory.UserMemoryOwnerResult{}, rollbackL1Tx(tx, fmt.Errorf("failed to create owner candidate: %w", err))
	}
	_, err = appendL1EventLog(ctx, tx, "memory.user_owner_proposed", namespace, "", "", 0, "", map[string]interface{}{
		"memory_id":         candidateID,
		"evidence_event_id": evidenceID,
		"request_id":        requestID,
		"actor_id":          actorID,
		"user_id":           ownerID,
		"type":              memoryType,
		"memory_state":      MemoryStateCandidate,
		"reason":            reason,
	}, ownerMemorySourcePrefix+actorID)
	if err != nil {
		return domainmemory.UserMemoryOwnerResult{}, rollbackL1Tx(tx, err)
	}
	item := domainmemory.UserMemory{
		ID:               candidateID,
		Namespace:        namespace,
		UserID:           ownerID,
		Type:             memoryType,
		Statement:        statement,
		EvidenceEventIDs: []string{evidenceID},
		Confidence:       0.5,
		Sensitivity:      "normal",
		State:            domainmemory.MemoryStateCandidate,
		Scope:            "all_personas",
		Active:           true,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	result := newOwnerMemoryResult(item, requestID, domainmemory.UserMemoryOwnerOperationPropose, candidateID, now, false)
	if err := insertOwnerMemoryReceipt(ctx, tx, requestID, domainmemory.UserMemoryOwnerOperationPropose, ownerID, actorID, payloadHash, candidateID, candidateID, result); err != nil {
		return domainmemory.UserMemoryOwnerResult{}, rollbackL1Tx(tx, err)
	}
	if err := tx.Commit(); err != nil {
		return domainmemory.UserMemoryOwnerResult{}, err
	}
	return result, nil
}

// OwnerTransitionUserMemory performs one owner-scoped state/lifecycle action
// and its audit/receipt write in one SQLite transaction.
func (s *L1SQLiteStore) OwnerTransitionUserMemory(ctx context.Context, requestID, ownerID, actorID, id, operation, replacementID, reason string) (domainmemory.UserMemoryOwnerResult, error) {
	requestID = strings.TrimSpace(requestID)
	ownerID = strings.TrimSpace(ownerID)
	actorID = strings.TrimSpace(actorID)
	id = strings.TrimSpace(id)
	operation = strings.TrimSpace(operation)
	replacementID = strings.TrimSpace(replacementID)
	reason = strings.TrimSpace(reason)
	if requestID == "" || ownerID == "" || actorID == "" || id == "" || reason == "" {
		return domainmemory.UserMemoryOwnerResult{}, domainmemory.ErrUserMemoryOwnerInvalid
	}
	switch operation {
	case domainmemory.UserMemoryOwnerOperationConfirm, domainmemory.UserMemoryOwnerOperationPin, domainmemory.UserMemoryOwnerOperationForget, domainmemory.UserMemoryOwnerOperationSupersede:
	default:
		return domainmemory.UserMemoryOwnerResult{}, domainmemory.ErrUserMemoryOwnerInvalid
	}
	if operation == domainmemory.UserMemoryOwnerOperationSupersede && (replacementID == "" || replacementID == id) {
		return domainmemory.UserMemoryOwnerResult{}, domainmemory.ErrUserMemoryOwnerInvalid
	}
	if operation != domainmemory.UserMemoryOwnerOperationSupersede && replacementID != "" {
		return domainmemory.UserMemoryOwnerResult{}, domainmemory.ErrUserMemoryOwnerInvalid
	}
	payloadHash := ownerMemoryPayloadHash(operation, ownerID, actorID, id, replacementID, "", "", reason)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domainmemory.UserMemoryOwnerResult{}, err
	}
	if replay, ok, err := ownerMemoryReplay(ctx, tx, requestID, operation, ownerID, actorID, payloadHash); err != nil {
		return domainmemory.UserMemoryOwnerResult{}, rollbackL1Tx(tx, err)
	} else if ok {
		if err := tx.Commit(); err != nil {
			return domainmemory.UserMemoryOwnerResult{}, err
		}
		return replay, nil
	}

	event, found, err := findL1MemoryEventByID(ctx, tx, id)
	if err != nil {
		return domainmemory.UserMemoryOwnerResult{}, rollbackL1Tx(tx, err)
	}
	if !found {
		return domainmemory.UserMemoryOwnerResult{}, rollbackL1Tx(tx, domainmemory.ErrUserMemoryOwnerNotFound)
	}
	item, err := strictUserMemoryFromEvent(event)
	if err != nil {
		return domainmemory.UserMemoryOwnerResult{}, rollbackL1Tx(tx, err)
	}
	if item.UserID != ownerID || event.Namespace != NamespaceKindUser+":"+ownerID {
		return domainmemory.UserMemoryOwnerResult{}, rollbackL1Tx(tx, domainmemory.ErrUserMemoryOwnerForbidden)
	}
	if !item.Active {
		return domainmemory.UserMemoryOwnerResult{}, rollbackL1Tx(tx, domainmemory.ErrUserMemoryOwnerConflict)
	}
	if operation == domainmemory.UserMemoryOwnerOperationConfirm && item.State != domainmemory.MemoryStateCandidate {
		return domainmemory.UserMemoryOwnerResult{}, rollbackL1Tx(tx, domainmemory.ErrUserMemoryOwnerConflict)
	}
	if operation == domainmemory.UserMemoryOwnerOperationPin && item.State != domainmemory.MemoryStateConfirmed {
		return domainmemory.UserMemoryOwnerResult{}, rollbackL1Tx(tx, domainmemory.ErrUserMemoryOwnerConflict)
	}
	if operation == domainmemory.UserMemoryOwnerOperationConfirm {
		if err := domainmemory.CanPromoteUserMemory(domainmemory.MemoryStateConfirmed, item.EvidenceEventIDs, item.Sensitivity, reason); err != nil {
			return domainmemory.UserMemoryOwnerResult{}, rollbackL1Tx(tx, fmt.Errorf("%w: %v", domainmemory.ErrUserMemoryOwnerConflict, err))
		}
	}
	if operation == domainmemory.UserMemoryOwnerOperationPin {
		if err := domainmemory.CanPromoteUserMemory(domainmemory.MemoryStatePinned, item.EvidenceEventIDs, item.Sensitivity, reason); err != nil {
			return domainmemory.UserMemoryOwnerResult{}, rollbackL1Tx(tx, fmt.Errorf("%w: %v", domainmemory.ErrUserMemoryOwnerConflict, err))
		}
	}
	if operation == domainmemory.UserMemoryOwnerOperationSupersede {
		replacement, replacementFound, findErr := findL1MemoryEventByID(ctx, tx, replacementID)
		if findErr != nil {
			return domainmemory.UserMemoryOwnerResult{}, rollbackL1Tx(tx, findErr)
		}
		if !replacementFound {
			return domainmemory.UserMemoryOwnerResult{}, rollbackL1Tx(tx, domainmemory.ErrUserMemoryOwnerNotFound)
		}
		replacementMemory, strictErr := strictUserMemoryFromEvent(replacement)
		if strictErr != nil {
			return domainmemory.UserMemoryOwnerResult{}, rollbackL1Tx(tx, strictErr)
		}
		if replacementMemory.UserID != ownerID || replacement.Namespace != event.Namespace {
			return domainmemory.UserMemoryOwnerResult{}, rollbackL1Tx(tx, domainmemory.ErrUserMemoryOwnerForbidden)
		}
		if !replacementMemory.Active {
			return domainmemory.UserMemoryOwnerResult{}, rollbackL1Tx(tx, domainmemory.ErrUserMemoryOwnerConflict)
		}
	}

	now := time.Now().UTC()
	updatedMeta := cloneOwnerMemoryMeta(event.Meta)
	updatedState := item.State
	eventType := "memory.user_owner_" + operation
	switch operation {
	case domainmemory.UserMemoryOwnerOperationConfirm:
		updatedState = domainmemory.MemoryStateConfirmed
	case domainmemory.UserMemoryOwnerOperationPin:
		updatedState = domainmemory.MemoryStatePinned
	case domainmemory.UserMemoryOwnerOperationForget:
		updatedMeta["active"] = false
		updatedMeta["forget_reason"] = reason
		updatedMeta["forgot_at"] = now.Format(time.RFC3339Nano)
	case domainmemory.UserMemoryOwnerOperationSupersede:
		updatedMeta["active"] = false
		updatedMeta["superseded_by"] = replacementID
		updatedMeta["supersede_reason"] = reason
		updatedMeta["superseded_at"] = now.Format(time.RFC3339Nano)
	}
	metaJSON, err := marshalL1MetaJSON(updatedMeta, "failed to marshal owner mutation meta")
	if err != nil {
		return domainmemory.UserMemoryOwnerResult{}, rollbackL1Tx(tx, err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE l1_memory_event
SET memory_state = ?, meta_json = ?, updated_at = ?
WHERE id = ? AND namespace = ?
`, updatedState, metaJSON, now, id, event.Namespace); err != nil {
		return domainmemory.UserMemoryOwnerResult{}, rollbackL1Tx(tx, fmt.Errorf("failed to apply owner mutation: %w", err))
	}
	event.MemoryState = updatedState
	event.Meta = updatedMeta
	event.UpdatedAt = now
	updatedItem := l1EventToUserMemory(event)
	if updatedItem == nil {
		return domainmemory.UserMemoryOwnerResult{}, rollbackL1Tx(tx, errors.New("owner mutation produced invalid user memory"))
	}
	_, err = appendL1EventLog(ctx, tx, eventType, event.Namespace, event.SessionID, event.ThreadID, event.ThreadSeq, event.ThreadKind, map[string]interface{}{
		"memory_id":      id,
		"request_id":     requestID,
		"actor_id":       actorID,
		"user_id":        ownerID,
		"operation":      operation,
		"previous_state": item.State,
		"memory_state":   updatedState,
		"replacement_id": replacementID,
		"reason":         reason,
	}, ownerMemorySourcePrefix+actorID)
	if err != nil {
		return domainmemory.UserMemoryOwnerResult{}, rollbackL1Tx(tx, err)
	}
	result := newOwnerMemoryResult(*updatedItem, requestID, operation, id, now, false)
	if err := insertOwnerMemoryReceipt(ctx, tx, requestID, operation, ownerID, actorID, payloadHash, id, id, result); err != nil {
		return domainmemory.UserMemoryOwnerResult{}, rollbackL1Tx(tx, err)
	}
	if err := tx.Commit(); err != nil {
		return domainmemory.UserMemoryOwnerResult{}, err
	}
	return result, nil
}

func newOwnerMemoryResult(item domainmemory.UserMemory, requestID, operation, auditReference string, completedAt time.Time, replay bool) domainmemory.UserMemoryOwnerResult {
	return domainmemory.UserMemoryOwnerResult{
		Item: domainmemory.UserMemoryOwnerViewFromMemory(item),
		Receipt: domainmemory.UserMemoryOwnerReceipt{
			RequestID:        requestID,
			Operation:        operation,
			Status:           "completed",
			OwnerRoute:       "conversation_l1/user_memory/" + operation,
			PolicyRevision:   domainmemory.UserMemoryOwnerPolicyRevision,
			IdempotencyKey:   requestID,
			IdempotentReplay: replay,
			InputCount:       1,
			OutputCount:      1,
			Warnings:         []string{},
			AuditReference:   auditReference,
			CompletedAt:      completedAt,
		},
	}
}

func ownerMemoryReplay(ctx context.Context, tx *sql.Tx, requestID, operation, ownerID, actorID, payloadHash string) (domainmemory.UserMemoryOwnerResult, bool, error) {
	row, found, err := findOwnerMemoryReceipt(ctx, tx, requestID)
	if err != nil || !found {
		return domainmemory.UserMemoryOwnerResult{}, false, err
	}
	if row.Operation != operation || row.OwnerID != ownerID || row.ActorID != actorID || row.PayloadHash != payloadHash {
		return domainmemory.UserMemoryOwnerResult{}, false, domainmemory.ErrUserMemoryOwnerConflict
	}
	var result domainmemory.UserMemoryOwnerResult
	if err := json.Unmarshal([]byte(row.ResultJSON), &result); err != nil {
		return domainmemory.UserMemoryOwnerResult{}, false, fmt.Errorf("failed to decode owner receipt result: %w", err)
	}
	result.Receipt.IdempotentReplay = true
	if result.Receipt.Warnings == nil {
		result.Receipt.Warnings = []string{}
	}
	return result, true, nil
}

func insertOwnerMemoryReceipt(ctx context.Context, tx *sql.Tx, requestID, operation, ownerID, actorID, payloadHash, memoryID, auditReference string, result domainmemory.UserMemoryOwnerResult) error {
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("failed to marshal owner receipt result: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO l1_memory_owner_receipt (
	request_id, operation, owner_id, actor_id, payload_hash, memory_id,
	audit_reference, result_json, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
`, requestID, operation, ownerID, actorID, payloadHash, memoryID, auditReference, string(resultJSON), result.Receipt.CompletedAt)
	if err != nil {
		return fmt.Errorf("failed to persist owner receipt: %w", err)
	}
	return nil
}

func findOwnerMemoryReceipt(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}, requestID string) (ownerMemoryReceiptRow, bool, error) {
	var row ownerMemoryReceiptRow
	err := queryer.QueryRowContext(ctx, `
SELECT request_id, operation, owner_id, actor_id, payload_hash, memory_id,
       audit_reference, result_json, created_at
FROM l1_memory_owner_receipt
WHERE request_id = ?
`, requestID).Scan(&row.RequestID, &row.Operation, &row.OwnerID, &row.ActorID, &row.PayloadHash, &row.MemoryID, &row.AuditReference, &row.ResultJSON, &row.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ownerMemoryReceiptRow{}, false, nil
	}
	if err != nil {
		return ownerMemoryReceiptRow{}, false, fmt.Errorf("failed to read owner receipt: %w", err)
	}
	return row, true, nil
}

func ownerMemoryPayloadHash(operation, ownerID, actorID, id, replacementID, memoryType, statement, reason string) string {
	payload, _ := json.Marshal(struct {
		Operation     string `json:"operation"`
		OwnerID       string `json:"owner_id"`
		ActorID       string `json:"actor_id"`
		ID            string `json:"id"`
		ReplacementID string `json:"replacement_id"`
		Type          string `json:"type"`
		Statement     string `json:"statement"`
		Reason        string `json:"reason"`
	}{operation, ownerID, actorID, id, replacementID, memoryType, statement, reason})
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func ownerMemoryEvidenceID(requestID string) string {
	digest := sha256.Sum256([]byte("operator-evidence:" + requestID))
	return "user-memory-owner-evidence/sha256:" + hex.EncodeToString(digest[:])
}

func cloneOwnerMemoryMeta(meta map[string]interface{}) map[string]interface{} {
	cloned := make(map[string]interface{}, len(meta)+4)
	for key, value := range meta {
		cloned[key] = value
	}
	return cloned
}
