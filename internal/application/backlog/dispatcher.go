package backlog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
)

// ListQueueFreezes returns the latest durable freeze record for each freeze
// ID. The store owns append-only/latest-record projection semantics.
func (s *Service) ListQueueFreezes(ctx context.Context, limit int) ([]domainworkstream.QueueFreeze, error) {
	store, ok := s.workstream.(LifecycleStore)
	if !ok {
		return nil, ErrLifecycleStoreUnavailable
	}
	return store.ListQueueFreezes(ctx, limit)
}

// ActiveQueueFreeze returns the current queue-stop record, if one exists.
func (s *Service) ActiveQueueFreeze(ctx context.Context) (domainworkstream.QueueFreeze, bool, error) {
	freezes, err := s.ListQueueFreezes(ctx, 0)
	if err != nil {
		return domainworkstream.QueueFreeze{}, false, err
	}
	for _, freeze := range freezes {
		if freeze.Status == domainworkstream.QueueFreezeActive || strings.TrimSpace(freeze.Status) == "" {
			return freeze, true, nil
		}
	}
	return domainworkstream.QueueFreeze{}, false, nil
}

// ResolveQueueFreeze validates the blocked/replacement pair and evidence in
// CORE before delegating the freeze resolution and replacement lease check to
// one atomic owner-store operation.
func (s *Service) ResolveQueueFreeze(ctx context.Context, freezeID string, request ResolveQueueFreezeRequest) (domainworkstream.QueueFreeze, domainworkstream.ImplementationLease, bool, error) {
	store, ok := s.workstream.(LifecycleStore)
	if !ok {
		return domainworkstream.QueueFreeze{}, domainworkstream.ImplementationLease{}, false, ErrLifecycleStoreUnavailable
	}
	freezeID = strings.TrimSpace(freezeID)
	request.RequestID = strings.TrimSpace(request.RequestID)
	request.ReplacementUnitID = strings.TrimSpace(request.ReplacementUnitID)
	request.SupersedesUnitID = strings.TrimSpace(request.SupersedesUnitID)
	request.BlockerResolutionRefs = append([]domainbacklog.EvidenceRef(nil), request.BlockerResolutionRefs...)
	if freezeID == "" || request.RequestID == "" || request.ExpectedFreezeRevision < 1 || request.ReplacementUnitID == "" || request.SupersedesUnitID == "" || len(request.BlockerResolutionRefs) == 0 {
		return domainworkstream.QueueFreeze{}, domainworkstream.ImplementationLease{}, false, errors.New("freeze resolution request fields are required")
	}
	for index, ref := range request.BlockerResolutionRefs {
		if err := domainbacklog.ValidateEvidenceRef(ref); err != nil {
			return domainworkstream.QueueFreeze{}, domainworkstream.ImplementationLease{}, false, err
		}
		normalized, normalizeErr := normalizeEvidenceRefStage(ref, domainbacklog.DeliveryBlocked)
		if normalizeErr != nil {
			return domainworkstream.QueueFreeze{}, domainworkstream.ImplementationLease{}, false, normalizeErr
		}
		request.BlockerResolutionRefs[index] = normalized
	}
	freeze, found, err := store.GetQueueFreeze(ctx, freezeID)
	if err != nil {
		return domainworkstream.QueueFreeze{}, domainworkstream.ImplementationLease{}, false, err
	}
	if !found {
		return domainworkstream.QueueFreeze{}, domainworkstream.ImplementationLease{}, false, domainworkstream.ErrQueueFreezeNotFound
	}
	payloadHash := resolveQueueFreezePayloadHash(request)
	if freeze.Status == domainworkstream.QueueFreezeResolved {
		resolution := domainworkstream.QueueFreezeResolution{
			ExpectedFreezeRevision: request.ExpectedFreezeRevision,
			ResolutionRequestID:    request.RequestID,
			ReplacementUnitID:      request.ReplacementUnitID,
			SupersedesUnitID:       request.SupersedesUnitID,
			ResolutionPayloadHash:  payloadHash,
		}
		// A replay must carry the same verified evidence as the completed
		// operation.  The hash covers the request payload while the persisted
		// refs are the authoritative verification result.
		if len(freeze.BlockerResolutionRefs) == 0 {
			return freeze, domainworkstream.ImplementationLease{}, false, domainworkstream.ErrQueueFreezeResolutionConflict
		}
		for _, ref := range freeze.BlockerResolutionRefs {
			if err := domainbacklog.ValidateEvidenceRef(ref); err != nil || !ref.IsVerified() {
				return freeze, domainworkstream.ImplementationLease{}, false, domainworkstream.ErrQueueFreezeResolutionConflict
			}
		}
		resolution.BlockerResolutionRefs = append([]domainbacklog.EvidenceRef(nil), freeze.BlockerResolutionRefs...)
		if freeze.ResolutionRequestID != request.RequestID || !freeze.MatchesResolved(resolution) {
			return freeze, domainworkstream.ImplementationLease{}, false, domainworkstream.ErrQueueFreezeResolutionConflict
		}
		return freeze, freeze.ReplacementLease, freeze.ResolutionAcquired, nil
	}
	if freeze.Status != domainworkstream.QueueFreezeActive && strings.TrimSpace(freeze.Status) != "" {
		return freeze, domainworkstream.ImplementationLease{}, false, domainworkstream.ErrQueueFreezeResolutionConflict
	}
	if freeze.FreezeRevision != request.ExpectedFreezeRevision {
		return freeze, domainworkstream.ImplementationLease{}, false, fmt.Errorf("%w: expected %d current %d", domainworkstream.ErrQueueFreezeRevisionConflict, request.ExpectedFreezeRevision, freeze.FreezeRevision)
	}
	if request.SupersedesUnitID != freeze.BlockedUnitID {
		return freeze, domainworkstream.ImplementationLease{}, false, fmt.Errorf("replacement supersedes %q, want blocked unit %q", request.SupersedesUnitID, freeze.BlockedUnitID)
	}

	items, err := s.list(ctx)
	if err != nil {
		return freeze, domainworkstream.ImplementationLease{}, false, err
	}
	byID := make(map[string]domainbacklog.Item, len(items))
	var blocked, replacement domainbacklog.Item
	blockedFound, replacementFound := false, false
	for _, item := range items {
		byID[item.ItemID] = item
		if item.ImplementationUnit == freeze.BlockedUnitID {
			blocked = item
			blockedFound = true
		}
		if item.ImplementationUnit == request.ReplacementUnitID {
			replacement = item
			replacementFound = true
		}
	}
	if !blockedFound || blocked.DeliveryState != domainbacklog.DeliveryBlocked || freeze.BlockedRevision < 1 || blocked.ImplementationRevision != freeze.BlockedRevision {
		return freeze, domainworkstream.ImplementationLease{}, false, errors.New("queue freeze blocked unit is not the expected BLOCKED revision")
	}
	if !replacementFound || replacement.ConceptState != domainbacklog.ConceptAdopted || replacement.DeliveryState != domainbacklog.DeliveryQueued {
		return freeze, domainworkstream.ImplementationLease{}, false, errors.New("replacement unit must be ADOPTED and QUEUED")
	}
	if replacement.SupersedesUnitID != freeze.BlockedUnitID {
		return freeze, domainworkstream.ImplementationLease{}, false, fmt.Errorf("replacement unit supersedes %q, want blocked unit %q", replacement.SupersedesUnitID, freeze.BlockedUnitID)
	}
	if !dependenciesDone(replacement, byID, map[string]bool{}) {
		return freeze, domainworkstream.ImplementationLease{}, false, errors.New("replacement unit dependencies are not DONE")
	}
	verifiedRefs := make([]domainbacklog.EvidenceRef, len(request.BlockerResolutionRefs))
	for index, ref := range request.BlockerResolutionRefs {
		verified, verifyErr := s.verifyEvidence(ctx, EvidenceVerificationRequest{
			Ref:                    ref,
			ItemID:                 blocked.ItemID,
			ImplementationUnitID:   freeze.BlockedUnitID,
			ImplementationRevision: freeze.BlockedRevision,
			TargetDeliveryState:    domainbacklog.DeliveryBlocked,
			Purpose:                "blocker_resolution",
		})
		if verifyErr != nil {
			return freeze, domainworkstream.ImplementationLease{}, false, verifyErr
		}
		verifiedRefs[index] = verified
	}
	now := s.now()
	lease := domainworkstream.ImplementationLease{
		LeaseName:          domainbacklog.ImplementationLeaseName,
		HolderUnitID:       replacement.ImplementationUnit,
		HolderWorkstreamID: replacement.WorkstreamID,
		Stage:              domainbacklog.DeliveryQueued,
		Revision:           strconv.Itoa(replacement.ImplementationRevision),
		AcquiredAt:         now,
		HeartbeatAt:        now,
	}
	resolution := domainworkstream.QueueFreezeResolution{
		ExpectedFreezeRevision: request.ExpectedFreezeRevision,
		ResolutionRequestID:    request.RequestID,
		ReplacementUnitID:      request.ReplacementUnitID,
		SupersedesUnitID:       request.SupersedesUnitID,
		BlockerResolutionRefs:  verifiedRefs,
		ResolutionPayloadHash:  payloadHash,
	}
	resolvedFreeze, acquiredLease, acquired, err := store.ResolveQueueFreezeAndAcquireLease(ctx, freezeID, resolution, lease)
	if err != nil || !acquired {
		return resolvedFreeze, acquiredLease, acquired, err
	}
	// Replacement item evidence remains a separate backlog projection.  The
	// QueueFreeze itself is already complete before this write is attempted.
	replacement.BlockerResolutionRefs = verifiedRefs
	if err := s.save(ctx, replacement); err != nil {
		return resolvedFreeze, acquiredLease, acquired, err
	}
	return resolvedFreeze, acquiredLease, acquired, nil
}

// AcquireRunnable first resumes the existing implementation lease holder.
// Only when no lease exists does it select a dependency-eligible queued item.
// Queue freezes are a normal no-item result, not a fallback to another item.
func (s *Service) AcquireRunnable(ctx context.Context) (AcquireRunnableResult, error) {
	if _, frozen, err := s.ActiveQueueFreeze(ctx); err != nil {
		return AcquireRunnableResult{}, err
	} else if frozen {
		return AcquireRunnableResult{Reason: domainworkstream.ErrQueueFrozen.Error()}, nil
	}
	store, ok := s.workstream.(LifecycleStore)
	if !ok {
		return AcquireRunnableResult{}, ErrLifecycleStoreUnavailable
	}
	if currentLease, held, err := s.getLease(ctx, domainbacklog.ImplementationLeaseName); err != nil {
		return AcquireRunnableResult{}, err
	} else if held {
		holder, findErr := s.findByUnit(ctx, currentLease.HolderUnitID)
		if findErr != nil {
			return AcquireRunnableResult{}, findErr
		}
		switch holder.DeliveryState {
		case domainbacklog.DeliveryBlocked:
			if err := s.ensureBlockedQueueFreeze(ctx, holder); err != nil {
				return AcquireRunnableResult{}, err
			}
			if err := s.releaseLease(ctx, currentLease.LeaseName, currentLease.HolderUnitID); err != nil {
				return AcquireRunnableResult{}, err
			}
			return AcquireRunnableResult{Reason: domainworkstream.ErrQueueFrozen.Error()}, nil
		case domainbacklog.DeliveryDone, domainbacklog.DeliveryRejected:
			if err := s.releaseLease(ctx, currentLease.LeaseName, currentLease.HolderUnitID); err != nil {
				return AcquireRunnableResult{}, err
			}
		case domainbacklog.DeliveryLiveVerified:
			// LIVE_VERIFIED is an owner closure boundary, not a worker stage.
			done, resumed, err := s.resumeLiveVerifiedClosureForItem(ctx, holder)
			if err != nil {
				return AcquireRunnableResult{}, err
			}
			if resumed {
				return AcquireRunnableResult{Item: done, Reason: "LIVE_VERIFIED closure resumed"}, nil
			}
			return AcquireRunnableResult{}, errors.New("LIVE_VERIFIED closure was not resumable")
		default:
			// A nonterminal holder owns the singleton lease and must continue its
			// current stage instead of allowing a second queued unit to start.
			return AcquireRunnableResult{Item: holder, Lease: currentLease, Acquired: true}, nil
		}
	}
	if done, resumed, err := s.resumeLiveVerifiedClosure(ctx); err != nil || resumed {
		if err != nil {
			return AcquireRunnableResult{}, err
		}
		return AcquireRunnableResult{Item: done, Reason: "LIVE_VERIFIED closure resumed"}, nil
	}
	items, err := s.list(ctx)
	if err != nil {
		return AcquireRunnableResult{}, err
	}
	queue := s.queue(items)
	if len(queue) == 0 {
		return AcquireRunnableResult{}, nil
	}
	item := queue[0]
	if item.ImplementationRevision < 1 {
		item.ImplementationRevision = 1
	}
	now := s.now()
	lease := domainworkstream.ImplementationLease{
		LeaseName:          domainbacklog.ImplementationLeaseName,
		HolderUnitID:       item.ImplementationUnit,
		HolderWorkstreamID: item.WorkstreamID,
		Stage:              item.DeliveryState,
		Revision:           strconv.Itoa(item.ImplementationRevision),
		AcquiredAt:         now,
		HeartbeatAt:        now,
	}
	acquired, reason, err := store.AcquireImplementationLeaseIfUnfrozen(ctx, lease)
	if err != nil {
		return AcquireRunnableResult{}, err
	}
	if !acquired && reason == domainworkstream.ErrQueueFrozen.Error() {
		return AcquireRunnableResult{Reason: reason}, nil
	}
	return AcquireRunnableResult{Item: item, Lease: lease, Acquired: acquired, Reason: reason}, nil
}

func resolveQueueFreezePayloadHash(request ResolveQueueFreezeRequest) string {
	refs := make([]domainbacklog.EvidenceRef, len(request.BlockerResolutionRefs))
	for index, ref := range request.BlockerResolutionRefs {
		refs[index] = clearVerification(ref)
	}
	payload, _ := json.Marshal(struct {
		RequestID              string                      `json:"request_id"`
		ExpectedFreezeRevision int                         `json:"expected_freeze_revision"`
		ReplacementUnitID      string                      `json:"replacement_unit_id"`
		SupersedesUnitID       string                      `json:"supersedes_unit_id"`
		BlockerResolutionRefs  []domainbacklog.EvidenceRef `json:"blocker_resolution_refs"`
	}{request.RequestID, request.ExpectedFreezeRevision, request.ReplacementUnitID, request.SupersedesUnitID, refs})
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:])
}
