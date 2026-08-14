package main

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	domainglossary "github.com/Nyukimin/RenCrow_CORE/internal/glossary/domain/entity"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

const runtimeGlossaryCandidateIDPrefix = "glossary-candidate/sha256:"

type runtimeGlossaryCandidateStore interface {
	SaveCandidate(context.Context, domainglossary.GlossaryCandidate) error
	FindCandidateByID(context.Context, string) (domainglossary.GlossaryCandidate, bool, error)
}

type runtimeGlossaryCandidateWritePayload struct {
	Term        string `json:"term"`
	Explanation string `json:"explanation"`
	SourceURL   string `json:"source_url"`
	Category    string `json:"category"`
}

type runtimeGlossaryCandidateWriter struct {
	mu    sync.Mutex
	store runtimeGlossaryCandidateStore
}

func registerRuntimeDataWriteGlossary(r *runtimeDataWriteRegistry, store runtimeGlossaryCandidateStore) error {
	if r == nil || store == nil {
		return fmt.Errorf("glossary data write unavailable")
	}
	writer := &runtimeGlossaryCandidateWriter{store: store}
	return r.RegisterWithContract("glossary", "propose_term_candidate", dataRecallAccessInternal, runtimeDataWriteContract{
		RequiredPayloadFields: []string{"category", "explanation", "source_url", "term"},
	}, writer.write)
}

func (w *runtimeGlossaryCandidateWriter) write(ctx context.Context, request tools.DataWriteRequest) (runtimeDataWriteOwnerResult, error) {
	scope, err := runtimeDataWriteOwnerScope(ctx)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	payload, err := decodeRuntimeGlossaryCandidatePayload(request.Payload)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	now := time.Now().UTC()
	candidate := domainglossary.GlossaryCandidate{
		ID:          runtimeDataWriteDerivedID(runtimeGlossaryCandidateIDPrefix, scope.RequestID),
		Term:        payload.Term,
		Explanation: payload.Explanation,
		SourceURL:   payload.SourceURL,
		Category:    payload.Category,
		ProposedBy:  strings.TrimSpace(scope.ActorID),
		State:       domainglossary.GlossaryCandidateState,
		CreatedAt:   now,
	}
	if err := domainglossary.ValidateGlossaryCandidate(candidate); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	existing, found, err := w.store.FindCandidateByID(ctx, candidate.ID)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if found {
		if err := domainglossary.ValidateGlossaryCandidate(existing); err != nil {
			return runtimeDataWriteOwnerResult{}, fmt.Errorf("existing glossary candidate is invalid: %w", err)
		}
		if !runtimeGlossaryCandidatesEqual(existing, candidate) {
			return runtimeDataWriteOwnerResult{}, fmt.Errorf("glossary candidate idempotency payload mismatch")
		}
		return runtimeGlossaryCandidateWriteReceipt(existing.ID, scope.RequestID, true), nil
	}
	if err := w.store.SaveCandidate(ctx, candidate); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	return runtimeGlossaryCandidateWriteReceipt(candidate.ID, scope.RequestID, false), nil
}

func decodeRuntimeGlossaryCandidatePayload(payload map[string]any) (runtimeGlossaryCandidateWritePayload, error) {
	if err := validateRuntimeDataWritePayloadKeys(payload, map[string]struct{}{
		"term": {}, "explanation": {}, "source_url": {}, "category": {},
	}); err != nil {
		return runtimeGlossaryCandidateWritePayload{}, err
	}
	var decoded runtimeGlossaryCandidateWritePayload
	if err := decodeRuntimeDataWritePayload(payload, &decoded); err != nil {
		return runtimeGlossaryCandidateWritePayload{}, err
	}
	decoded.Term = strings.TrimSpace(decoded.Term)
	decoded.Explanation = strings.TrimSpace(decoded.Explanation)
	decoded.SourceURL = strings.TrimSpace(decoded.SourceURL)
	decoded.Category = strings.ToLower(strings.TrimSpace(decoded.Category))
	if decoded.Term == "" || decoded.Explanation == "" || decoded.SourceURL == "" || decoded.Category == "" {
		return runtimeGlossaryCandidateWritePayload{}, fmt.Errorf("term, explanation, source_url and category are required")
	}
	return decoded, nil
}

func runtimeGlossaryCandidatesEqual(left, right domainglossary.GlossaryCandidate) bool {
	left.CreatedAt = time.Time{}
	right.CreatedAt = time.Time{}
	return reflect.DeepEqual(left, right)
}

func runtimeGlossaryCandidateWriteReceipt(auditRef, requestID string, replay bool) runtimeDataWriteOwnerResult {
	return runtimeDataWriteOwnerResult{
		SchemaVersion:    "glossary-candidate/v1",
		MigrationState:   "embedded_current",
		ValidationState:  "owner_validated",
		AuditRef:         auditRef,
		IdempotencyKey:   requestID,
		IdempotentReplay: replay,
		PolicyRevision:   runtimeDataWritePolicyRevision,
	}
}
