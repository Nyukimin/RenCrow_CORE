package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	domainkm "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgememory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	knowledgememorypersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/knowledgememory"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

const runtimeKnowledgeMemoryCandidateIDPrefix = "knowledge-candidate/sha256:"

type runtimeKnowledgeMemoryCandidateStore interface {
	SaveCreativeCandidateWithReceipt(context.Context, domainkm.CreativeKnowledgeItem, knowledgememorypersistence.KnowledgeMemoryRequestReceipt) (bool, error)
}

type runtimeKnowledgeMemoryCandidateWritePayload struct {
	Title        string   `json:"title"`
	CreatorNames []string `json:"creator_names,omitempty"`
	WorkType     string   `json:"work_type,omitempty"`
	RelatedWorks []string `json:"related_works,omitempty"`
	ContentHints []string `json:"content_hints,omitempty"`
}

type runtimeKnowledgeMemoryCandidateWriter struct {
	mu    sync.Mutex
	store runtimeKnowledgeMemoryCandidateStore
}

func registerRuntimeDataWriteKnowledgeMemory(r *runtimeDataWriteRegistry, store runtimeKnowledgeMemoryCandidateStore) error {
	if r == nil || store == nil {
		return fmt.Errorf("knowledge memory candidate data write unavailable")
	}
	writer := &runtimeKnowledgeMemoryCandidateWriter{store: store}
	return r.RegisterWithContract("knowledge_memory", "propose_creative_candidate", dataRecallAccessUser, runtimeDataWriteContract{
		RequiredPayloadFields: []string{"title"},
		OptionalPayloadFields: []string{"content_hints", "creator_names", "related_works", "work_type"},
	}, writer.write)
}

func (w *runtimeKnowledgeMemoryCandidateWriter) write(ctx context.Context, request tools.DataWriteRequest) (runtimeDataWriteOwnerResult, error) {
	if w == nil || w.store == nil {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("knowledge memory candidate store unavailable")
	}
	scope, err := runtimeDataWriteOwnerScope(ctx)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	userID := strings.TrimSpace(scope.AuthenticatedUserID)
	if userID == "" || !scope.Allows(domaintool.DataScopeUser) {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("knowledge memory candidate requires authenticated user scope")
	}
	payload, payloadHash, err := decodeRuntimeKnowledgeMemoryCandidatePayload(request.Payload)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	now := time.Now().UTC()
	item := domainkm.CreativeKnowledgeItem{
		ItemID:       runtimeDataWriteDerivedID(runtimeKnowledgeMemoryCandidateIDPrefix, scope.RequestID),
		UserID:       userID,
		Title:        payload.Title,
		CreatorNames: payload.CreatorNames,
		WorkType:     payload.WorkType,
		RelatedWorks: payload.RelatedWorks,
		ContentHints: payload.ContentHints,
		Status:       "candidate",
		Visibility:   "private",
		CreatedAt:    now,
	}
	if err := domainkm.ValidateCreativeCandidate(item); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	receipt := knowledgememorypersistence.KnowledgeMemoryRequestReceipt{
		RequestID:   scope.RequestID,
		UserID:      userID,
		ActorID:     strings.TrimSpace(scope.ActorID),
		PayloadHash: payloadHash,
		ItemID:      item.ItemID,
		CreatedAt:   now,
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	replay, err := w.store.SaveCreativeCandidateWithReceipt(ctx, item, receipt)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	return runtimeKnowledgeMemoryCandidateWriteReceipt(item.ItemID, scope.RequestID, replay), nil
}

func decodeRuntimeKnowledgeMemoryCandidatePayload(payload map[string]any) (runtimeKnowledgeMemoryCandidateWritePayload, string, error) {
	if err := validateRuntimeDataWritePayloadKeys(payload, map[string]struct{}{
		"title": {}, "creator_names": {}, "work_type": {}, "related_works": {}, "content_hints": {},
	}); err != nil {
		return runtimeKnowledgeMemoryCandidateWritePayload{}, "", err
	}
	var decoded runtimeKnowledgeMemoryCandidateWritePayload
	if err := decodeRuntimeDataWritePayload(payload, &decoded); err != nil {
		return runtimeKnowledgeMemoryCandidateWritePayload{}, "", err
	}
	decoded.Title = strings.TrimSpace(decoded.Title)
	decoded.WorkType = strings.TrimSpace(decoded.WorkType)
	decoded.CreatorNames = trimRuntimeKnowledgeMemoryCandidateEntries(decoded.CreatorNames)
	decoded.RelatedWorks = trimRuntimeKnowledgeMemoryCandidateEntries(decoded.RelatedWorks)
	decoded.ContentHints = trimRuntimeKnowledgeMemoryCandidateEntries(decoded.ContentHints)
	if decoded.Title == "" {
		return runtimeKnowledgeMemoryCandidateWritePayload{}, "", fmt.Errorf("title is required")
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return runtimeKnowledgeMemoryCandidateWritePayload{}, "", fmt.Errorf("hash creative candidate payload: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return decoded, hex.EncodeToString(digest[:]), nil
}

func trimRuntimeKnowledgeMemoryCandidateEntries(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	trimmed := make([]string, len(values))
	for i, value := range values {
		trimmed[i] = strings.TrimSpace(value)
	}
	return trimmed
}

func runtimeKnowledgeMemoryCandidateWriteReceipt(auditRef, requestID string, replay bool) runtimeDataWriteOwnerResult {
	return runtimeDataWriteOwnerResult{
		SchemaVersion:    "knowledge-memory-creative-candidate/v1",
		MigrationState:   "embedded_current",
		ValidationState:  "owner_validated",
		AuditRef:         auditRef,
		IdempotencyKey:   requestID,
		IdempotentReplay: replay,
		PolicyRevision:   runtimeDataWritePolicyRevision,
	}
}
