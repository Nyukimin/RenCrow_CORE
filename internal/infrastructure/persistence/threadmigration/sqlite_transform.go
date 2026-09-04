package threadmigration

// This file contains the pure Step 05 SQLite payload transform.  It accepts
// already inventoried legacy values and produces canonical JSON for the
// current conversation-turn contract.  It deliberately has no database,
// filesystem, clock, random, or process dependencies; materialization owns
// those concerns and must call this boundary only after the source row has
// been read and validated.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// sqliteCanonicalThreadTuple is the canonical identity tuple emitted by the
// transform.  A zero tuple is used only for an optional, unthreaded source
// row; a positive tuple always contains all four canonical values.
type sqliteCanonicalThreadTuple struct {
	SessionID  modulecore.SessionID
	ThreadID   modulecore.ThreadID
	ThreadSeq  modulecore.ThreadSeq
	ThreadKind modulecore.ThreadKind
}

// sqliteTransformIndex is the immutable, O(1) lookup boundary shared by all
// rows in one materialization pass.  Plan.Validate is intentionally performed
// only by newSQLiteTransformIndex; repeatedly validating a plan for every
// SQLite row would turn a bounded transform into an avoidable O(rows*mappings)
// operation.
type sqliteTransformIndex struct {
	planSHA256 string
	generic    map[genericGroupKey]ThreadMapping
	chatGPT    map[legacyTuple]ThreadMapping
	ready      bool
}

func newSQLiteTransformIndex(plan Plan) (sqliteTransformIndex, error) {
	if err := plan.Validate(); err != nil {
		return sqliteTransformIndex{}, fmt.Errorf("validate SQLite transform plan: %w", err)
	}
	index := sqliteTransformIndex{
		planSHA256: plan.MappingSHA256,
		generic:    make(map[genericGroupKey]ThreadMapping, len(plan.Generic)),
		chatGPT:    make(map[legacyTuple]ThreadMapping, len(plan.ChatGPT)),
		ready:      true,
	}
	for _, mapping := range plan.Generic {
		key := genericGroupKey{sessionID: string(mapping.SessionID), legacyThreadID: mapping.LegacyThreadID}
		if _, exists := index.generic[key]; exists {
			return sqliteTransformIndex{}, fmt.Errorf("duplicate generic transform tuple %q/%d", key.sessionID, key.legacyThreadID)
		}
		index.generic[key] = cloneMapping(mapping)
	}
	for _, mapping := range plan.ChatGPT {
		sessionID, threadID, err := chatGPTLegacyTuple(mapping.ChatGPTConversationID)
		if err != nil {
			return sqliteTransformIndex{}, fmt.Errorf("ChatGPT mapping %q legacy tuple: %w", mapping.SemanticKey, err)
		}
		key := legacyTuple{sessionID: sessionID, threadID: threadID}
		if previous, exists := index.chatGPT[key]; exists {
			return sqliteTransformIndex{}, fmt.Errorf("legacy ChatGPT tuple %q/%d maps to both %q and %q", sessionID, threadID, previous.SemanticKey, mapping.SemanticKey)
		}
		canonicalGenericSession, err := canonicalGenericSessionID(sessionID)
		if err != nil {
			return sqliteTransformIndex{}, fmt.Errorf("canonicalize ChatGPT legacy session %q: %w", sessionID, err)
		}
		if generic, exists := index.generic[genericGroupKey{sessionID: canonicalGenericSession, legacyThreadID: threadID}]; exists {
			return sqliteTransformIndex{}, fmt.Errorf("legacy tuple %q/%d is classified as both generic %q and ChatGPT %q", sessionID, threadID, generic.SemanticKey, mapping.SemanticKey)
		}
		index.chatGPT[key] = cloneMapping(mapping)
	}
	return index, nil
}

func (tuple sqliteCanonicalThreadTuple) validate() error {
	if err := tuple.SessionID.Validate(); err != nil {
		return fmt.Errorf("canonical session ID %q: %w", tuple.SessionID, err)
	}
	if err := tuple.ThreadID.Validate(); err != nil {
		return fmt.Errorf("canonical thread ID %q: %w", tuple.ThreadID, err)
	}
	if err := tuple.ThreadSeq.Validate(); err != nil {
		return fmt.Errorf("canonical thread sequence: %w", err)
	}
	if err := tuple.ThreadKind.Validate(); err != nil {
		return fmt.Errorf("canonical thread kind: %w", err)
	}
	return nil
}

// resolveSQLiteThreadTuple resolves one positive legacy (session, numeric
// thread) tuple against the validated inventory Plan.  ChatGPT's synthetic
// source tuple is checked first because its session is not a generic
// session_files value.  The generic fallback canonicalizes the source
// session byte-for-byte before performing the plan lookup.
func resolveSQLiteThreadTuple(index sqliteTransformIndex, sourceSessionID string, legacyThreadID int64) (sqliteCanonicalThreadTuple, error) {
	if err := index.validate(); err != nil {
		return sqliteCanonicalThreadTuple{}, err
	}
	if legacyThreadID <= 0 {
		return sqliteCanonicalThreadTuple{}, fmt.Errorf("legacy thread ID must be positive, got %d", legacyThreadID)
	}
	if strings.TrimSpace(sourceSessionID) == "" {
		return sqliteCanonicalThreadTuple{}, errors.New("positive legacy thread tuple has an empty session ID")
	}

	if mapping, ok := index.chatGPT[legacyTuple{sessionID: sourceSessionID, threadID: legacyThreadID}]; ok {
		return canonicalTupleFromMapping(mapping, sourceSessionID, legacyThreadID)
	}

	canonicalSessionID, err := canonicalGenericSessionID(sourceSessionID)
	if err != nil {
		return sqliteCanonicalThreadTuple{}, fmt.Errorf("canonicalize legacy session %q: %w", sourceSessionID, err)
	}
	mapping, ok := index.generic[genericGroupKey{sessionID: canonicalSessionID, legacyThreadID: legacyThreadID}]
	if !ok {
		return sqliteCanonicalThreadTuple{}, fmt.Errorf("no generic mapping for legacy tuple %q/%d", sourceSessionID, legacyThreadID)
	}
	return canonicalTupleFromMapping(mapping, sourceSessionID, legacyThreadID)
}

// resolveSQLiteOptionalThreadTuple preserves an unthreaded zero tuple.  A
// nonempty parent session is still canonicalized even when no thread exists;
// this is required for L1 event/archive rows whose optional thread reference
// is zero.  Empty parent plus zero thread remains the all-zero tuple.
func resolveSQLiteOptionalThreadTuple(index sqliteTransformIndex, sourceSessionID string, legacyThreadID int64) (sqliteCanonicalThreadTuple, error) {
	if err := index.validate(); err != nil {
		return sqliteCanonicalThreadTuple{}, err
	}
	if legacyThreadID < 0 {
		return sqliteCanonicalThreadTuple{}, fmt.Errorf("optional legacy thread ID must be zero or positive, got %d", legacyThreadID)
	}
	if legacyThreadID == 0 {
		if sourceSessionID == "" {
			return sqliteCanonicalThreadTuple{}, nil
		}
		canonicalSessionID, err := canonicalGenericSessionID(sourceSessionID)
		if err != nil {
			return sqliteCanonicalThreadTuple{}, fmt.Errorf("canonicalize optional legacy session %q: %w", sourceSessionID, err)
		}
		return sqliteCanonicalThreadTuple{SessionID: modulecore.SessionID(canonicalSessionID)}, nil
	}
	return resolveSQLiteThreadTuple(index, sourceSessionID, legacyThreadID)
}

func canonicalTupleFromMapping(mapping ThreadMapping, sourceSessionID string, legacyThreadID int64) (sqliteCanonicalThreadTuple, error) {
	if mapping.ChatGPTConversationID != "" {
		expectedSessionID, expectedLegacyThreadID, err := chatGPTLegacyTuple(mapping.ChatGPTConversationID)
		if err != nil {
			return sqliteCanonicalThreadTuple{}, fmt.Errorf("ChatGPT mapping %q: %w", mapping.SemanticKey, err)
		}
		if sourceSessionID != expectedSessionID || legacyThreadID != expectedLegacyThreadID {
			return sqliteCanonicalThreadTuple{}, fmt.Errorf("legacy tuple %q/%d contradicts ChatGPT mapping %q", sourceSessionID, legacyThreadID, mapping.SemanticKey)
		}
		if string(mapping.SessionID) == "" || mapping.ThreadSeq != 1 {
			return sqliteCanonicalThreadTuple{}, fmt.Errorf("ChatGPT mapping %q has an invalid canonical tuple", mapping.SemanticKey)
		}
	} else {
		canonicalSessionID, err := canonicalGenericSessionID(sourceSessionID)
		if err != nil {
			return sqliteCanonicalThreadTuple{}, fmt.Errorf("canonicalize generic source session %q: %w", sourceSessionID, err)
		}
		if string(mapping.SessionID) != canonicalSessionID || mapping.LegacyThreadID != legacyThreadID || mapping.ThreadSeq != modulecore.ThreadSeq(legacyThreadID) {
			return sqliteCanonicalThreadTuple{}, fmt.Errorf("generic mapping %q does not preserve legacy tuple %q/%d", mapping.SemanticKey, sourceSessionID, legacyThreadID)
		}
	}
	tuple := sqliteCanonicalThreadTuple{
		SessionID:  mapping.SessionID,
		ThreadID:   mapping.ThreadID,
		ThreadSeq:  mapping.ThreadSeq,
		ThreadKind: mapping.ThreadKind,
	}
	if err := tuple.validate(); err != nil {
		return sqliteCanonicalThreadTuple{}, fmt.Errorf("mapping %q: %w", mapping.SemanticKey, err)
	}
	return tuple, nil
}

// transformLegacyTurnResult converts a validated legacy conversation turn
// receipt result_json value.  row is the SQL-side receipt identity; the
// legacy JSON is checked against it before any canonical output is built.
func transformLegacyTurnResult(index sqliteTransformIndex, row legacyReceiptRow, encoded string) ([]byte, error) {
	if err := index.validate(); err != nil {
		return nil, err
	}
	if err := validateLegacyReceiptRowForTransform(row); err != nil {
		return nil, err
	}
	if len(encoded) == 0 || len(encoded) > maxLegacyResultJSONBytes {
		return nil, fmt.Errorf("legacy %s row %q result_json exceeds legacy bound", turnReceiptSurface, row.turnID)
	}
	if err := auditTypedTurnJSON(encoded, turnReceiptSurface, row.turnID); err != nil {
		return nil, err
	}
	payload, err := decodeLegacyTurnResult(encoded)
	if err != nil {
		return nil, fmt.Errorf("legacy %s row %q result_json: %w", turnReceiptSurface, row.turnID, err)
	}
	if err := payload.validate(row); err != nil {
		return nil, fmt.Errorf("legacy %s row %q result_json identity: %w", turnReceiptSurface, row.turnID, err)
	}

	threadTuple, err := resolveSQLiteThreadTuple(index, row.sessionID, row.threadID)
	if err != nil {
		return nil, fmt.Errorf("legacy %s row %q thread identity: %w", turnReceiptSurface, row.turnID, err)
	}
	var closedTuple sqliteCanonicalThreadTuple
	if row.closed {
		closedTuple, err = resolveSQLiteThreadTuple(index, row.sessionID, row.closedID)
		if err != nil {
			return nil, fmt.Errorf("legacy %s row %q closed thread identity: %w", turnReceiptSurface, row.turnID, err)
		}
	}

	canonical := domconv.ConversationTurnResult{
		TurnID:           payload.TurnID,
		TraceID:          payload.TraceID,
		SessionID:        threadTupleSessionString(threadTuple),
		ThreadID:         threadTuple.ThreadID,
		ThreadSeq:        domconv.ThreadSeq(threadTuple.ThreadSeq),
		ThreadKind:       domconv.ThreadKind(threadTuple.ThreadKind),
		UserMessageID:    payload.UserMessageID,
		AgentMessageID:   payload.AgentMessageID,
		MessageIDs:       append([]string(nil), payload.MessageIDs...),
		PayloadSHA256:    payload.PayloadSHA256,
		Status:           domconv.ConversationTurnStatus(payload.Status),
		ErrorCode:        domconv.ConversationTurnErrorCode(payload.ErrorCode),
		RequestedTargets: append([]string(nil), payload.RequestedTargets...),
		PendingTargets:   append([]string(nil), payload.PendingTargets...),
		CompletedTargets: append([]string(nil), payload.CompletedTargets...),
		IdempotentReplay: payload.IdempotentReplay,
	}
	if row.closed {
		canonical.ClosedThreadID = closedTuple.ThreadID
		canonical.ClosedThreadSeq = domconv.ThreadSeq(closedTuple.ThreadSeq)
		canonical.ClosedThreadKind = domconv.ThreadKind(closedTuple.ThreadKind)
	}
	return marshalCanonicalTurnResult(canonical)
}

// sqliteLegacyOutboxRow is the SQL-side identity and payload needed to
// transform one legacy conversation_turn_outbox row.  The database writer
// owns scanning this value; the transform only validates and converts it.
type sqliteLegacyOutboxRow struct {
	TurnID      string
	Target      string
	SessionID   string
	ThreadID    int64
	ClosedID    legacyOptionalInt64
	PayloadHash string
	PayloadJSON string
	Receipt     legacyReceiptRow
}

// legacyOptionalInt64 is the database-neutral representation of one
// nullable source integer. The materializer converts the scanned nullable
// value to this representation before calling the pure transform.
type legacyOptionalInt64 struct {
	Value int64
	Valid bool
}

// transformLegacyOutboxPayload converts the legacy payload_json value.  It
// returns only the canonical payload JSON; the outer outbox row (status,
// lease, attempts, and timestamps) remains the materializer's responsibility.
func transformLegacyOutboxPayload(index sqliteTransformIndex, row sqliteLegacyOutboxRow) ([]byte, error) {
	if err := index.validate(); err != nil {
		return nil, err
	}
	if err := validateLegacyOutboxRowForTransform(row); err != nil {
		return nil, err
	}
	if len(row.PayloadJSON) == 0 || len(row.PayloadJSON) > maxLegacyOutboxPayloadBytes {
		return nil, fmt.Errorf("legacy %s row %q/%q payload_json exceeds legacy bound", turnOutboxSurface, row.TurnID, row.Target)
	}
	if err := auditTypedTurnJSON(row.PayloadJSON, turnOutboxSurface, outboxRecordKey(row.TurnID, row.Target)); err != nil {
		return nil, err
	}
	payload, err := decodeLegacyOutboxPayload(row.PayloadJSON)
	if err != nil {
		return nil, fmt.Errorf("legacy %s row %q/%q payload_json: %w", turnOutboxSurface, row.TurnID, row.Target, err)
	}
	if err := validateLegacyOutboxPayloadForTransform(payload, row); err != nil {
		return nil, fmt.Errorf("legacy %s row %q/%q payload_json identity: %w", turnOutboxSurface, row.TurnID, row.Target, err)
	}

	threadTuple, err := resolveSQLiteThreadTuple(index, row.SessionID, row.ThreadID)
	if err != nil {
		return nil, fmt.Errorf("legacy %s row %q/%q thread identity: %w", turnOutboxSurface, row.TurnID, row.Target, err)
	}
	var closedTuple sqliteCanonicalThreadTuple
	if row.ClosedID.Valid {
		closedTuple, err = resolveSQLiteThreadTuple(index, row.SessionID, row.ClosedID.Value)
		if err != nil {
			return nil, fmt.Errorf("legacy %s row %q/%q closed thread identity: %w", turnOutboxSurface, row.TurnID, row.Target, err)
		}
	}

	canonical := canonicalTurnOutboxPayload{
		Version:        payload.Version,
		TurnID:         payload.TurnID,
		TraceID:        payload.TraceID,
		SessionID:      threadTupleSessionString(threadTuple),
		OwnerID:        payload.OwnerID,
		ThreadID:       threadTuple.ThreadID,
		ThreadSeq:      threadTuple.ThreadSeq,
		ThreadKind:     threadTuple.ThreadKind,
		UserMessageID:  payload.UserMessageID,
		AgentMessageID: payload.AgentMessageID,
		Target:         payload.Target,
		PayloadSHA256:  payload.PayloadSHA256,
	}
	if row.ClosedID.Valid {
		canonical.ClosedThreadID = closedTuple.ThreadID
		canonical.ClosedThreadSeq = closedTuple.ThreadSeq
		canonical.ClosedThreadKind = closedTuple.ThreadKind
	}
	return marshalCanonicalOutboxPayload(canonical)
}

type canonicalTurnOutboxPayload struct {
	Version          string                `json:"version"`
	TurnID           string                `json:"turn_id"`
	TraceID          string                `json:"trace_id"`
	SessionID        string                `json:"session_id"`
	OwnerID          string                `json:"owner_id"`
	ThreadID         modulecore.ThreadID   `json:"thread_id"`
	ThreadSeq        modulecore.ThreadSeq  `json:"thread_seq"`
	ThreadKind       modulecore.ThreadKind `json:"thread_kind"`
	ClosedThreadID   modulecore.ThreadID   `json:"closed_thread_id,omitempty"`
	ClosedThreadSeq  modulecore.ThreadSeq  `json:"closed_thread_seq,omitempty"`
	ClosedThreadKind modulecore.ThreadKind `json:"closed_thread_kind,omitempty"`
	UserMessageID    string                `json:"user_message_id"`
	AgentMessageID   string                `json:"agent_message_id"`
	Target           string                `json:"target"`
	PayloadSHA256    string                `json:"payload_sha256"`
}

func (index sqliteTransformIndex) validate() error {
	if !index.ready || index.planSHA256 == "" || index.generic == nil || index.chatGPT == nil {
		return errors.New("SQLite transform index is not initialized")
	}
	return nil
}

func validateLegacyReceiptRowForTransform(row legacyReceiptRow) error {
	if row.turnID == "" || row.traceID == "" || row.traceID != row.turnID || row.sessionID == "" || row.userMessage == "" || row.agentMessage == "" {
		return fmt.Errorf("legacy %s row %q has an invalid required field", turnReceiptSurface, row.turnID)
	}
	if row.threadID <= 0 {
		return fmt.Errorf("legacy %s row %q thread_id must be positive", turnReceiptSurface, row.turnID)
	}
	if row.closed {
		if row.closedID <= 0 {
			return fmt.Errorf("legacy %s row %q closed_thread_id must be positive", turnReceiptSurface, row.turnID)
		}
	} else if row.closedID != 0 {
		return fmt.Errorf("legacy %s row %q has a closed thread ID without a closed tuple", turnReceiptSurface, row.turnID)
	}
	if !validTurnStatus(row.status) {
		return fmt.Errorf("legacy %s row %q has invalid status %q", turnReceiptSurface, row.turnID, row.status)
	}
	if err := validatePayloadHash(row.payloadHash); err != nil {
		return fmt.Errorf("legacy %s row %q: %w", turnReceiptSurface, row.turnID, err)
	}
	return nil
}

func validateLegacyOutboxRowForTransform(row sqliteLegacyOutboxRow) error {
	if row.TurnID == "" || row.SessionID == "" || row.PayloadHash == "" || row.PayloadJSON == "" {
		return fmt.Errorf("legacy %s row %q/%q has an invalid required field", turnOutboxSurface, row.TurnID, row.Target)
	}
	if !validOutboxTarget(row.Target) {
		return fmt.Errorf("legacy %s row %q/%q has invalid target %q", turnOutboxSurface, row.TurnID, row.Target, row.Target)
	}
	if row.ThreadID <= 0 {
		return fmt.Errorf("legacy %s row %q/%q thread_id must be positive", turnOutboxSurface, row.TurnID, row.Target)
	}
	if err := validatePayloadHash(row.PayloadHash); err != nil {
		return fmt.Errorf("legacy %s row %q/%q: %w", turnOutboxSurface, row.TurnID, row.Target, err)
	}
	if row.ClosedID.Valid {
		if row.ClosedID.Value <= 0 {
			return fmt.Errorf("legacy %s row %q/%q closed_thread_id must be positive", turnOutboxSurface, row.TurnID, row.Target)
		}
	} else if row.ClosedID.Value != 0 {
		return fmt.Errorf("legacy %s row %q/%q has a closed thread ID without a closed tuple", turnOutboxSurface, row.TurnID, row.Target)
	}
	if err := validateLegacyReceiptRowForTransform(row.Receipt); err != nil {
		return fmt.Errorf("legacy %s row %q/%q receipt: %w", turnOutboxSurface, row.TurnID, row.Target, err)
	}
	if row.Receipt.turnID != row.TurnID || row.Receipt.sessionID != row.SessionID || row.Receipt.threadID != row.ThreadID || row.Receipt.payloadHash != row.PayloadHash {
		return fmt.Errorf("legacy %s row %q/%q SQL identity does not match its receipt", turnOutboxSurface, row.TurnID, row.Target)
	}
	if row.Receipt.closed != row.ClosedID.Valid || (row.ClosedID.Valid && row.Receipt.closedID != row.ClosedID.Value) {
		return fmt.Errorf("legacy %s row %q/%q closed thread does not match its receipt", turnOutboxSurface, row.TurnID, row.Target)
	}
	return nil
}

// validateLegacyOutboxPayloadForTransform mirrors the already-established
// legacy payload contract without importing database/sql into this pure
// package boundary.  The SQL-side nullable value has already been normalized
// into legacyOptionalInt64 by the caller.
func validateLegacyOutboxPayloadForTransform(payload legacyOutboxPayload, row sqliteLegacyOutboxRow) error {
	embeddedThreadID, err := parseRequiredJSONInteger(payload.ThreadID, "thread_id")
	if err != nil {
		return err
	}
	embeddedClosedID, embeddedClosed, err := parseOptionalJSONInteger(payload.ClosedThreadID, "closed_thread_id")
	if err != nil {
		return err
	}
	if payload.Version != turnPayloadVersion || payload.TurnID != row.TurnID || payload.TraceID != row.TurnID || payload.SessionID != row.SessionID || payload.OwnerID == "" || embeddedThreadID != row.ThreadID || payload.UserMessageID != row.Receipt.userMessage || payload.AgentMessageID != row.Receipt.agentMessage || payload.Target != row.Target || payload.PayloadSHA256 != row.PayloadHash {
		return errors.New("embedded outbox identity does not match SQL/receipt identity")
	}
	if embeddedClosed != row.ClosedID.Valid || (embeddedClosed && embeddedClosedID != row.ClosedID.Value) {
		return errors.New("embedded outbox closed thread does not match SQL identity")
	}
	if err := validatePayloadHash(payload.PayloadSHA256); err != nil {
		return err
	}
	return nil
}

func threadTupleSessionString(tuple sqliteCanonicalThreadTuple) string {
	return string(tuple.SessionID)
}

func marshalCanonicalTurnResult(result domconv.ConversationTurnResult) ([]byte, error) {
	if err := result.ThreadID.Validate(); err != nil {
		return nil, fmt.Errorf("canonical result thread ID: %w", err)
	}
	if err := result.ThreadSeq.Validate(); err != nil {
		return nil, fmt.Errorf("canonical result thread sequence: %w", err)
	}
	if err := result.ThreadKind.Validate(); err != nil {
		return nil, fmt.Errorf("canonical result thread kind: %w", err)
	}
	if result.ClosedThreadID == "" || result.ClosedThreadSeq == 0 || result.ClosedThreadKind == "" {
		if result.ClosedThreadID != "" || result.ClosedThreadSeq != 0 || result.ClosedThreadKind != "" {
			return nil, errors.New("canonical result closed tuple is incomplete")
		}
	} else {
		if err := result.ClosedThreadID.Validate(); err != nil {
			return nil, fmt.Errorf("canonical result closed thread ID: %w", err)
		}
		if err := result.ClosedThreadSeq.Validate(); err != nil {
			return nil, fmt.Errorf("canonical result closed thread sequence: %w", err)
		}
		if err := result.ClosedThreadKind.Validate(); err != nil {
			return nil, fmt.Errorf("canonical result closed thread kind: %w", err)
		}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("marshal canonical turn result: %w", err)
	}
	if len(bytes.TrimSpace(encoded)) == 0 {
		return nil, errors.New("canonical turn result is empty")
	}
	if len(encoded) > maxLegacyResultJSONBytes {
		return nil, fmt.Errorf("canonical turn result exceeds %d bytes", maxLegacyResultJSONBytes)
	}
	return encoded, nil
}

func marshalCanonicalOutboxPayload(payload canonicalTurnOutboxPayload) ([]byte, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal canonical turn outbox payload: %w", err)
	}
	if len(encoded) > maxLegacyOutboxPayloadBytes {
		return nil, fmt.Errorf("canonical turn outbox payload exceeds %d bytes", maxLegacyOutboxPayloadBytes)
	}
	return encoded, nil
}
