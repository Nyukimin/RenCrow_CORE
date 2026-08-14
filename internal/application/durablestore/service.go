package durablestore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	domain "github.com/Nyukimin/RenCrow_CORE/internal/domain/durablestore"
)

type Store interface {
	FindByDedupeKey(context.Context, string) (*domain.WorkflowResult, error)
	FindByRequestID(context.Context, string) (*domain.RequestReceipt, error)
	FindByRequirementID(context.Context, string) (*domain.WorkflowResult, error)
	SaveWithReceipt(context.Context, *domain.WorkflowResult, domain.RequestReceipt) error
}

var ErrRequestConflict = errors.New("durable store request conflict")

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
	req.RequestID = strings.TrimSpace(in.RequestID)
	req.TraceID = strings.TrimSpace(in.TraceID)
	req.RequestedBy = strings.TrimSpace(in.RequestedBy)
	req.UserScope = strings.TrimSpace(in.UserScope)
	classification := domain.Classify(req, s.manifests)
	req.OwnerModule = classification.OwnerModule
	req.DedupeKey = digest(strings.Join([]string{string(req.RequestedOutcome), req.OwnerModule, req.UserScope, normalizeDedupeText(in.Message), "contract:v1"}, "\x00"))
	req.RequirementID = "sr-" + digest(req.RequestID + "\x00" + req.DedupeKey)[:20]
	payloadHash := domain.HashStorageRequirement(req)
	if s.store != nil {
		if req.RequestID != "" {
			receipt, err := s.store.FindByRequestID(ctx, req.RequestID)
			if err != nil {
				return domain.WorkflowResult{}, true, err
			}
			if receipt != nil {
				prior, replayErr := s.replayReceipt(ctx, req, payloadHash, *receipt)
				if replayErr != nil {
					return domain.WorkflowResult{}, true, replayErr
				}
				return prior, true, nil
			}
		}
		prior, err := s.store.FindByDedupeKey(ctx, req.DedupeKey)
		if err != nil {
			return domain.WorkflowResult{}, true, err
		}
		if prior != nil {
			receipt := requestReceipt(req, payloadHash, prior.Requirement.RequirementID, s.now())
			if err := s.store.SaveWithReceipt(ctx, nil, receipt); err != nil {
				resolved, ok, resolveErr := s.resolvePersistenceConflict(ctx, req, payloadHash, prior)
				if resolveErr != nil {
					return domain.WorkflowResult{}, true, resolveErr
				}
				if ok {
					return resolved, true, nil
				}
				return domain.WorkflowResult{}, true, err
			}
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
		receipt := requestReceipt(req, payloadHash, result.Requirement.RequirementID, result.CreatedAt)
		if err := s.store.SaveWithReceipt(ctx, &result, receipt); err != nil {
			resolved, ok, resolveErr := s.resolvePersistenceConflict(ctx, req, payloadHash, &result)
			if resolveErr != nil {
				return domain.WorkflowResult{}, true, resolveErr
			}
			if ok {
				return resolved, true, nil
			}
			return domain.WorkflowResult{}, true, err
		}
	}
	return result, true, nil
}

func (s *Service) replayReceipt(ctx context.Context, req domain.StorageRequirement, payloadHash string, receipt domain.RequestReceipt) (domain.WorkflowResult, error) {
	if strings.TrimSpace(receipt.UserScope) != req.UserScope || strings.TrimSpace(receipt.PayloadHash) != payloadHash {
		return domain.WorkflowResult{}, fmt.Errorf("%w: request_id %q has a different payload or user scope", ErrRequestConflict, req.RequestID)
	}
	prior, err := s.store.FindByRequirementID(ctx, receipt.RequirementID)
	if err != nil {
		return domain.WorkflowResult{}, err
	}
	if prior == nil {
		return domain.WorkflowResult{}, fmt.Errorf("durable request receipt %q references missing requirement %q", req.RequestID, receipt.RequirementID)
	}
	prior.Deduplicated = true
	prior.RequestReplay = true
	return *prior, nil
}

func (s *Service) resolvePersistenceConflict(ctx context.Context, req domain.StorageRequirement, payloadHash string, candidate *domain.WorkflowResult) (domain.WorkflowResult, bool, error) {
	if req.RequestID != "" {
		receipt, err := s.store.FindByRequestID(ctx, req.RequestID)
		if err != nil {
			return domain.WorkflowResult{}, false, err
		}
		if receipt != nil {
			prior, replayErr := s.replayReceipt(ctx, req, payloadHash, *receipt)
			if replayErr != nil {
				return domain.WorkflowResult{}, false, replayErr
			}
			return prior, true, nil
		}
	}
	prior, err := s.store.FindByDedupeKey(ctx, req.DedupeKey)
	if err != nil {
		return domain.WorkflowResult{}, false, err
	}
	if prior == nil || candidate == nil || prior.Requirement.RequirementID == candidate.Requirement.RequirementID {
		return domain.WorkflowResult{}, false, nil
	}
	receipt := requestReceipt(req, payloadHash, prior.Requirement.RequirementID, s.now())
	if err := s.store.SaveWithReceipt(ctx, nil, receipt); err != nil {
		return domain.WorkflowResult{}, false, err
	}
	prior.Deduplicated = true
	return *prior, true, nil
}

func requestReceipt(req domain.StorageRequirement, payloadHash, requirementID string, createdAt time.Time) domain.RequestReceipt {
	requestID := strings.TrimSpace(req.RequestID)
	if requestID == "" {
		// Existing direct callers may omit the new trusted request identity. Keep
		// those callers persistable without making the Owner route accept it.
		requestID = "legacy/" + digest(requirementID+"\x00"+req.DedupeKey)
	}
	return domain.RequestReceipt{
		RequestID: requestID, UserScope: req.UserScope, PayloadHash: payloadHash,
		RequirementID: requirementID, CreatedAt: createdAt,
	}
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func normalizeDedupeText(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), " ")
}
