package main

import (
	"context"
	"fmt"
	"strings"
	"sync"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

type runtimeConversationL1UserMemoryStore interface {
	CreateUserMemoryCandidateWithRequest(context.Context, string, string, domainmemory.CreateUserMemoryInput) (*domainmemory.UserMemory, bool, error)
}

type runtimeConversationL1UserMemoryWritePayload struct {
	Type             string   `json:"type"`
	Statement        string   `json:"statement"`
	EvidenceEventIDs []string `json:"evidence_event_ids,omitempty"`
	Confidence       float64  `json:"confidence,omitempty"`
	Sensitivity      string   `json:"sensitivity,omitempty"`
	PersonaScope     string   `json:"persona_scope,omitempty"`
}

type runtimeConversationL1UserMemoryWriter struct {
	mu    sync.Mutex
	store runtimeConversationL1UserMemoryStore
}

func registerRuntimeDataWriteConversationL1(r *runtimeDataWriteRegistry, store runtimeConversationL1UserMemoryStore) error {
	if r == nil || store == nil {
		return fmt.Errorf("conversation l1 user memory data write unavailable")
	}
	writer := &runtimeConversationL1UserMemoryWriter{store: store}
	return r.RegisterWithContract("conversation_l1", "propose_user_memory", dataRecallAccessUser, runtimeDataWriteContract{
		RequiredPayloadFields: []string{"statement", "type"},
		OptionalPayloadFields: []string{"confidence", "evidence_event_ids", "persona_scope", "sensitivity"},
	}, writer.write)
}

func (w *runtimeConversationL1UserMemoryWriter) write(ctx context.Context, request tools.DataWriteRequest) (runtimeDataWriteOwnerResult, error) {
	scope, err := runtimeDataWriteOwnerScope(ctx)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	userID := strings.TrimSpace(scope.AuthenticatedUserID)
	if userID == "" {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("conversation l1 user memory requires authenticated user scope")
	}
	payload, err := decodeRuntimeConversationL1UserMemoryPayload(request.Payload)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	memory, replay, err := w.store.CreateUserMemoryCandidateWithRequest(ctx, scope.RequestID, scope.ActorID, domainmemory.CreateUserMemoryInput{
		UserID:           userID,
		Type:             payload.Type,
		Statement:        payload.Statement,
		EvidenceEventIDs: payload.EvidenceEventIDs,
		Confidence:       payload.Confidence,
		Sensitivity:      payload.Sensitivity,
		Scope:            payload.PersonaScope,
	})
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	return runtimeConversationL1UserMemoryResult(memory.ID, scope.RequestID, replay), nil
}

func runtimeConversationL1UserMemoryResult(memoryID, requestID string, replay bool) runtimeDataWriteOwnerResult {
	return runtimeDataWriteOwnerResult{
		SchemaVersion:    "conversation-l1-user-memory-candidate/v1",
		MigrationState:   "embedded_current",
		ValidationState:  "owner_validated",
		AuditRef:         memoryID,
		IdempotencyKey:   requestID,
		IdempotentReplay: replay,
		PolicyRevision:   runtimeDataWritePolicyRevision,
	}
}

func decodeRuntimeConversationL1UserMemoryPayload(payload map[string]any) (runtimeConversationL1UserMemoryWritePayload, error) {
	if err := validateRuntimeDataWritePayloadKeys(payload, map[string]struct{}{
		"type": {}, "statement": {}, "evidence_event_ids": {}, "confidence": {}, "sensitivity": {}, "persona_scope": {},
	}); err != nil {
		return runtimeConversationL1UserMemoryWritePayload{}, err
	}
	var decoded runtimeConversationL1UserMemoryWritePayload
	if err := decodeRuntimeDataWritePayload(payload, &decoded); err != nil {
		return runtimeConversationL1UserMemoryWritePayload{}, err
	}
	decoded.Type = strings.TrimSpace(decoded.Type)
	decoded.Statement = strings.TrimSpace(decoded.Statement)
	decoded.Sensitivity = strings.TrimSpace(decoded.Sensitivity)
	decoded.PersonaScope = strings.TrimSpace(decoded.PersonaScope)
	if decoded.Type == "" {
		return runtimeConversationL1UserMemoryWritePayload{}, fmt.Errorf("type is required")
	}
	if decoded.Statement == "" {
		return runtimeConversationL1UserMemoryWritePayload{}, fmt.Errorf("statement is required")
	}
	if decoded.Confidence < 0 || decoded.Confidence > 1 {
		return runtimeConversationL1UserMemoryWritePayload{}, fmt.Errorf("confidence must be between 0 and 1")
	}
	if decoded.EvidenceEventIDs != nil {
		trimmed := make([]string, len(decoded.EvidenceEventIDs))
		for i, evidenceID := range decoded.EvidenceEventIDs {
			trimmed[i] = strings.TrimSpace(evidenceID)
			if trimmed[i] == "" {
				return runtimeConversationL1UserMemoryWritePayload{}, fmt.Errorf("evidence_event_ids[%d] is required", i)
			}
		}
		decoded.EvidenceEventIDs = trimmed
	}
	return decoded, nil
}
