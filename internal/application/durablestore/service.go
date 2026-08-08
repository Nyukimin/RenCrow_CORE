package durablestore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	domain "github.com/Nyukimin/RenCrow_CORE/internal/domain/durablestore"
)

type Store interface {
	FindByDedupeKey(context.Context, string) (*domain.WorkflowResult, error)
	Save(context.Context, domain.WorkflowResult) error
}

type Implementer interface {
	Implement(context.Context, domain.StorageRequirement, domain.Classification) (*domain.StorageProposal, domain.ActivationEvidence, error)
}

type Input struct {
	RequestID, TraceID, RequestedBy, UserScope, Message string
}

type Service struct {
	manifests   []domain.Manifest
	store       Store
	implementer Implementer
	now         func() time.Time
}

func NewService(manifests []domain.Manifest, store Store, implementer Implementer) *Service {
	return &Service{manifests: append([]domain.Manifest(nil), manifests...), store: store, implementer: implementer, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) Handle(ctx context.Context, in Input) (domain.WorkflowResult, bool, error) {
	req, handled := domain.NormalizeStorageIntent(in.Message)
	if !handled {
		return domain.WorkflowResult{}, false, nil
	}
	if err := domain.ValidateRegistry(s.manifests); err != nil {
		return domain.WorkflowResult{}, true, fmt.Errorf("durable store registry: %w", err)
	}
	req.RequestID, req.TraceID, req.RequestedBy, req.UserScope = in.RequestID, in.TraceID, in.RequestedBy, in.UserScope
	classification := domain.Classify(req, s.manifests)
	req.OwnerModule = classification.OwnerModule
	req.DedupeKey = digest(strings.Join([]string{string(req.RequestedOutcome), req.OwnerModule, req.UserScope, normalizeDedupeText(in.Message), "contract:v1"}, "\x00"))
	req.RequirementID = "sr-" + digest(in.RequestID + "\x00" + req.DedupeKey)[:20]
	if s.store != nil {
		prior, err := s.store.FindByDedupeKey(ctx, req.DedupeKey)
		if err != nil {
			return domain.WorkflowResult{}, true, err
		}
		if prior != nil {
			prior.Deduplicated = true
			return *prior, true, nil
		}
	}
	now := s.now()
	result := domain.WorkflowResult{Requirement: req, Classification: classification, Lifecycle: domain.LifecycleProposed, CreatedAt: now, UpdatedAt: now}
	switch {
	case classification.Status == domain.StatusBlocked:
		result.Status, result.Reason, result.ReasonCode = domain.StatusBlocked, classification.Reason, "owner_unresolved"
	case req.RequestedOutcome == domain.OutcomeAssess:
		result.Status, result.Lifecycle, result.Reason = domain.StatusCompleted, domain.LifecycleValidated, classification.Reason
	case classification.Class != domain.ClassNewStore:
		result.Status, result.Lifecycle = domain.StatusCompleted, domain.LifecycleValidated
		result.Reason = "registered storage contract selected; ingestion changes, if any, require a separate validated implementation"
	case s.implementer == nil:
		result.Status = domain.StatusBlocked
		result.Reason, result.ReasonCode = "validated durable-store implementer is not configured", "implementer_unavailable"
	default:
		proposal, evidence, err := s.implementer.Implement(ctx, req, classification)
		if err != nil {
			result.Status, result.Reason, result.ReasonCode = domain.StatusRejected, err.Error(), "proposal_rejected"
			break
		}
		result.Proposal, result.Evidence = proposal, evidence
		if proposal == nil || !proposal.ValidationPassed {
			result.Status, result.Reason, result.ReasonCode = domain.StatusRejected, "proposal did not pass deterministic validation", "proposal_validation_failed"
			break
		}
		result.Lifecycle = domain.LifecycleImplemented
		if evidence.Complete() {
			result.Status, result.Lifecycle, result.Reason = domain.StatusCompleted, domain.LifecycleActive, "store activated with complete evidence"
		} else {
			result.Status, result.Reason, result.ReasonCode = domain.StatusBlocked, "implementation is not active: provisioning, backup, restore, integrity, or health evidence is incomplete", "activation_evidence_incomplete"
		}
	}
	if s.store != nil {
		if err := s.store.Save(ctx, result); err != nil {
			return domain.WorkflowResult{}, true, err
		}
	}
	return result, true, nil
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func normalizeDedupeText(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), " ")
}
