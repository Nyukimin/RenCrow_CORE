package backlog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
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
	request.ActionID = modulecore.ActionID(strings.TrimSpace(string(request.ActionID)))
	request.BlockedUnitID = strings.TrimSpace(request.BlockedUnitID)
	request.Reason = strings.TrimSpace(request.Reason)
	request.ExpectedMissingTransitionEventIDs = append([]modulecore.EventID(nil), request.ExpectedMissingTransitionEventIDs...)
	request.ReplacementUnitID = strings.TrimSpace(request.ReplacementUnitID)
	request.SupersedesUnitID = strings.TrimSpace(request.SupersedesUnitID)
	request.BlockerResolutionRefs = append([]domainbacklog.EvidenceRef(nil), request.BlockerResolutionRefs...)
	if request.Disposition != "" && request.Disposition != domainworkstream.QueueFreezeDispositionCancelled {
		return domainworkstream.QueueFreeze{}, domainworkstream.ImplementationLease{}, false, errors.New("unknown queue freeze resolution disposition")
	}
	if request.Disposition == domainworkstream.QueueFreezeDispositionCancelled {
		return s.cancelQueueFreeze(ctx, store, freezeID, request)
	}
	if request.BlockedUnitID != "" || request.BlockedRevision != 0 || request.Reason != "" || len(request.ExpectedMissingTransitionEventIDs) != 0 {
		return domainworkstream.QueueFreeze{}, domainworkstream.ImplementationLease{}, false, errors.New("cancellation fields require disposition=cancelled")
	}
	if freezeID == "" || request.ActionID.Validate() != nil || request.ExpectedFreezeRevision < 1 || request.ReplacementUnitID == "" || request.SupersedesUnitID == "" || len(request.BlockerResolutionRefs) == 0 {
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
			ActionID:               request.ActionID,
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
		if freeze.ActionID != request.ActionID || !freeze.MatchesResolved(resolution) {
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
		byID[string(item.BacklogItemID)] = item
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
			BacklogItemID:          string(blocked.BacklogItemID),
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
		ActionID:               request.ActionID,
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

func (s *Service) cancelQueueFreeze(ctx context.Context, lifecycleStore LifecycleStore, freezeID string, request ResolveQueueFreezeRequest) (domainworkstream.QueueFreeze, domainworkstream.ImplementationLease, bool, error) {
	if freezeID == "" || request.ActionID.Validate() != nil || request.ExpectedFreezeRevision < 1 || request.BlockedUnitID == "" || request.BlockedRevision < 1 || request.Reason == "" {
		return domainworkstream.QueueFreeze{}, domainworkstream.ImplementationLease{}, false, errors.New("cancelled freeze request fields are required")
	}
	if request.ReplacementUnitID != "" || request.SupersedesUnitID != "" || len(request.BlockerResolutionRefs) != 0 {
		return domainworkstream.QueueFreeze{}, domainworkstream.ImplementationLease{}, false, errors.New("cancelled freeze request cannot include replacement fields or blocker evidence")
	}
	if err := validateCanonicalSortedEventIDs(request.ExpectedMissingTransitionEventIDs); err != nil {
		return domainworkstream.QueueFreeze{}, domainworkstream.ImplementationLease{}, false, err
	}
	cancellationStore, ok := s.workstream.(QueueFreezeCancellationStore)
	if !ok {
		return domainworkstream.QueueFreeze{}, domainworkstream.ImplementationLease{}, false, ErrQueueFreezeCancellationStoreUnavailable
	}
	freeze, found, err := lifecycleStore.GetQueueFreeze(ctx, freezeID)
	if err != nil {
		return domainworkstream.QueueFreeze{}, domainworkstream.ImplementationLease{}, false, err
	}
	if !found {
		return domainworkstream.QueueFreeze{}, domainworkstream.ImplementationLease{}, false, domainworkstream.ErrQueueFreezeNotFound
	}
	cancellation := domainworkstream.QueueFreezeCancellation{
		ActionID: request.ActionID, ExpectedFreezeRevision: request.ExpectedFreezeRevision,
		BlockedUnitID: request.BlockedUnitID, BlockedRevision: request.BlockedRevision,
		Reason: request.Reason, ExpectedMissingTransitionEventIDs: append([]modulecore.EventID(nil), request.ExpectedMissingTransitionEventIDs...),
		ResolutionPayloadHash: cancelQueueFreezePayloadHash(request),
	}
	if freeze.Status == domainworkstream.QueueFreezeResolved {
		resolved, cancelErr := cancellationStore.CancelQueueFreeze(ctx, freezeID, cancellation)
		if cancelErr != nil {
			return freeze, domainworkstream.ImplementationLease{}, false, cancelErr
		}
		return resolved, domainworkstream.ImplementationLease{}, false, nil
	}
	if (freeze.Status != domainworkstream.QueueFreezeActive && strings.TrimSpace(freeze.Status) != "") || freeze.FreezeRevision != request.ExpectedFreezeRevision ||
		freeze.BlockedUnitID != request.BlockedUnitID || freeze.BlockedRevision != request.BlockedRevision ||
		!strings.EqualFold(strings.TrimSpace(freeze.InvalidatedFromStage), domainbacklog.DeliveryQueued) {
		return freeze, domainworkstream.ImplementationLease{}, false, domainworkstream.ErrQueueFreezeResolutionConflict
	}
	items, err := s.list(ctx)
	if err != nil {
		return freeze, domainworkstream.ImplementationLease{}, false, err
	}
	var blocked domainbacklog.Item
	blockedFound := false
	for _, item := range items {
		if item.ImplementationUnit != request.BlockedUnitID {
			continue
		}
		if blockedFound {
			return freeze, domainworkstream.ImplementationLease{}, false, errors.New("blocked unit identity is not unique")
		}
		blocked = domainbacklog.ProjectLegacy(item)
		blockedFound = true
	}
	if !blockedFound || blocked.ConceptState != domainbacklog.ConceptAdopted || blocked.DeliveryState != domainbacklog.DeliveryBlocked ||
		blocked.ImplementationUnit != request.BlockedUnitID || blocked.ImplementationRevision != request.BlockedRevision ||
		blocked.InvalidatedFromStage != freeze.InvalidatedFromStage {
		return freeze, domainworkstream.ImplementationLease{}, false, errors.New("queue freeze item is not the expected ADOPTED BLOCKED revision")
	}
	stageKey := stageRunKey(request.BlockedUnitID, request.BlockedRevision, domainbacklog.DeliveryBlocked)
	stageReceipt, stageFound, err := lifecycleStore.FindStageRunReceipt(ctx, stageKey)
	if err != nil {
		return freeze, domainworkstream.ImplementationLease{}, false, err
	}
	if !stageFound || stageReceipt.IdempotencyKey != stageKey || stageReceipt.Status != domainworkstream.StageRunCompleted ||
		stageReceipt.UnitID != request.BlockedUnitID || stageReceipt.BacklogItemID != blocked.BacklogItemID ||
		stageReceipt.ImplementationRevision != request.BlockedRevision || stageReceipt.TargetStage != domainbacklog.DeliveryBlocked ||
		stageReceipt.DeliveryState != domainbacklog.DeliveryBlocked || !validSHA256(stageReceipt.PayloadHash) ||
		stageReceipt.TransitionEventID == "" {
		return freeze, domainworkstream.ImplementationLease{}, false, errors.New("completed BLOCKED stage receipt is missing or inconsistent")
	}
	if stageReceipt.TransitionEventID == freeze.TransitionEventID {
		return freeze, domainworkstream.ImplementationLease{}, false, errors.New("BLOCKED stage and queue freeze must reference distinct canonical events")
	}
	if err := validateExpectedMissingEventTargets(request.ExpectedMissingTransitionEventIDs, []modulecore.EventID{
		freeze.TransitionEventID, stageReceipt.TransitionEventID,
	}); err != nil {
		return freeze, domainworkstream.ImplementationLease{}, false, err
	}
	var recordedResult domainbacklog.Item
	if err := json.Unmarshal([]byte(stageReceipt.ResultJSON), &recordedResult); err != nil ||
		recordedResult.BacklogItemID != blocked.BacklogItemID || recordedResult.ImplementationUnit != blocked.ImplementationUnit ||
		recordedResult.ImplementationRevision != request.BlockedRevision || recordedResult.ConceptState != domainbacklog.ConceptAdopted ||
		recordedResult.DeliveryState != domainbacklog.DeliveryBlocked ||
		!sameEvidenceRefs(recordedResult.EvidenceRefs, blocked.EvidenceRefs) ||
		!sameEvidenceRefsForStage(freeze.EvidenceRefs, recordedResult.EvidenceRefs, domainbacklog.DeliveryBlocked) ||
		!recordedBlockedFailureMatches(recordedResult, blocked, freeze) {
		return freeze, domainworkstream.ImplementationLease{}, false, errors.New("BLOCKED stage receipt does not retain the item's failure evidence")
	}
	freezeEventMissing, err := s.verifyQueueFreezeEvent(ctx, freeze, blocked)
	if err != nil {
		return freeze, domainworkstream.ImplementationLease{}, false, err
	}
	stageEventMissing, err := s.verifyStageRunEvent(ctx, stageReceipt, blocked)
	if err != nil {
		return freeze, domainworkstream.ImplementationLease{}, false, err
	}
	observedMissing := make([]modulecore.EventID, 0, len(request.ExpectedMissingTransitionEventIDs))
	if freezeEventMissing {
		observedMissing = append(observedMissing, freeze.TransitionEventID)
	}
	if stageEventMissing {
		observedMissing = append(observedMissing, stageReceipt.TransitionEventID)
	}
	sort.Slice(observedMissing, func(left, right int) bool { return observedMissing[left] < observedMissing[right] })
	if !sameTransitionEventIDs(request.ExpectedMissingTransitionEventIDs, observedMissing) {
		return freeze, domainworkstream.ImplementationLease{}, false, errors.New("observed missing transition events do not match the cancellation request")
	}
	resolved, err := cancellationStore.CancelQueueFreeze(ctx, freezeID, cancellation)
	if err != nil {
		return freeze, domainworkstream.ImplementationLease{}, false, err
	}
	return resolved, domainworkstream.ImplementationLease{}, false, nil
}

// AcquireRunnable first resumes the existing implementation lease holder.
// Only when no lease exists does it select a dependency-eligible queued item.
// Queue freezes are a normal no-item result, not a fallback to another item.
func (s *Service) AcquireRunnable(ctx context.Context) (result AcquireRunnableResult, resultErr error) {
	ownedCtx, finish, err := s.beginAction(ctx, "atlas_acquire_runnable")
	if err != nil {
		return AcquireRunnableResult{}, err
	}
	result, resultErr = s.acquireRunnable(ownedCtx)
	resultErr = finishAction(resultErr, finish)
	return result, resultErr
}

func (s *Service) acquireRunnable(ctx context.Context) (AcquireRunnableResult, error) {
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
		ActionID               modulecore.ActionID         `json:"action_id"`
		ExpectedFreezeRevision int                         `json:"expected_freeze_revision"`
		ReplacementUnitID      string                      `json:"replacement_unit_id"`
		SupersedesUnitID       string                      `json:"supersedes_unit_id"`
		BlockerResolutionRefs  []domainbacklog.EvidenceRef `json:"blocker_resolution_refs"`
	}{request.ActionID, request.ExpectedFreezeRevision, request.ReplacementUnitID, request.SupersedesUnitID, refs})
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:])
}

func cancelQueueFreezePayloadHash(request ResolveQueueFreezeRequest) string {
	payload, _ := json.Marshal(struct {
		ActionID                          modulecore.ActionID  `json:"action_id"`
		ExpectedFreezeRevision            int                  `json:"expected_freeze_revision"`
		Disposition                       string               `json:"disposition"`
		BlockedUnitID                     string               `json:"blocked_unit_id"`
		BlockedRevision                   int                  `json:"blocked_revision"`
		Reason                            string               `json:"reason"`
		ExpectedMissingTransitionEventIDs []modulecore.EventID `json:"expected_missing_transition_event_ids,omitempty"`
	}{
		ActionID: request.ActionID, ExpectedFreezeRevision: request.ExpectedFreezeRevision,
		Disposition: request.Disposition, BlockedUnitID: request.BlockedUnitID,
		BlockedRevision: request.BlockedRevision, Reason: request.Reason,
		ExpectedMissingTransitionEventIDs: request.ExpectedMissingTransitionEventIDs,
	})
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:])
}

func validSHA256(value string) bool {
	decoded, err := hex.DecodeString(strings.TrimSpace(value))
	return err == nil && len(decoded) == sha256.Size
}

func recordedBlockedFailureMatches(recorded, current domainbacklog.Item, freeze domainworkstream.QueueFreeze) bool {
	reason := strings.TrimSpace(freeze.ReasonCode)
	if reason == "" || strings.TrimSpace(current.Implementation) != reason {
		return false
	}
	if reason == "stage_failed" {
		return strings.TrimSpace(recorded.Implementation) == "" || strings.TrimSpace(recorded.Implementation) == reason
	}
	return strings.TrimSpace(recorded.Implementation) == reason
}

func sameEvidenceRefs(left, right []domainbacklog.EvidenceRef) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sameEvidenceRefsForStage(expected, actual []domainbacklog.EvidenceRef, stage string) bool {
	for _, ref := range expected {
		if err := domainbacklog.ValidateEvidenceRef(ref); err != nil || !strings.EqualFold(strings.TrimSpace(ref.Stage), stage) {
			return false
		}
	}
	actualForStage := make([]domainbacklog.EvidenceRef, 0, len(actual))
	for _, ref := range actual {
		if strings.EqualFold(strings.TrimSpace(ref.Stage), stage) {
			actualForStage = append(actualForStage, ref)
		}
	}
	return len(expected) > 0 && sameEvidenceRefs(expected, actualForStage)
}

func developmentEventString(fields map[string]any, key string) string {
	value, found := fields[key]
	if !found || value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func developmentEventRevision(fields map[string]any) (int, bool) {
	value, found := fields["implementation_revision"]
	if !found || value == nil {
		return 0, false
	}
	switch typed := value.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float64:
		if typed != float64(int(typed)) {
			return 0, false
		}
		return int(typed), true
	case json.Number:
		revision, err := typed.Int64()
		return int(revision), err == nil
	case string:
		revision, err := strconv.Atoi(strings.TrimSpace(typed))
		return revision, err == nil
	default:
		return 0, false
	}
}

func (s *Service) verifyQueueFreezeEvent(ctx context.Context, freeze domainworkstream.QueueFreeze, item domainbacklog.Item) (bool, error) {
	event, found, err := s.readDevelopmentEvent(ctx, freeze.TransitionEventID)
	if err != nil {
		return false, err
	}
	if !found {
		return true, nil
	}
	revision, revisionOK := developmentEventRevision(event.Fields)
	if event.EventID != freeze.TransitionEventID || event.Type != DevelopmentEventQueueFreezeTransition ||
		event.UnitID != freeze.BlockedUnitID || event.ArtifactID != transitionCompanionArtifactID(freeze.BlockedUnitID, domainbacklog.DeliveryBlocked, freeze.FreezeID) || event.CreatedAt.IsZero() ||
		developmentEventString(event.Fields, "target_stage") != domainbacklog.DeliveryBlocked ||
		developmentEventString(event.Fields, "backlog_item_id") != string(item.BacklogItemID) || !revisionOK || revision != freeze.BlockedRevision ||
		developmentEventString(event.Fields, "reason_code") != freeze.ReasonCode ||
		developmentEventString(event.Fields, "invalidated_from_stage") != freeze.InvalidatedFromStage {
		return false, errors.New("queue freeze transition Event does not match the blocked unit")
	}
	return false, nil
}

func (s *Service) verifyStageRunEvent(ctx context.Context, receipt domainworkstream.StageRunReceipt, item domainbacklog.Item) (bool, error) {
	event, found, err := s.readDevelopmentEvent(ctx, receipt.TransitionEventID)
	if err != nil {
		return false, err
	}
	if !found {
		return true, nil
	}
	revision, revisionOK := developmentEventRevision(event.Fields)
	if event.EventID != receipt.TransitionEventID || event.Type != DevelopmentEventStageRunTransition ||
		event.UnitID != receipt.UnitID || event.ArtifactID != transitionCompanionArtifactID(receipt.UnitID, domainbacklog.DeliveryBlocked, receipt.IdempotencyKey) || event.CreatedAt.IsZero() ||
		developmentEventString(event.Fields, "target_stage") != domainbacklog.DeliveryBlocked ||
		developmentEventString(event.Fields, "backlog_item_id") != string(item.BacklogItemID) || !revisionOK || revision != receipt.ImplementationRevision ||
		developmentEventString(event.Fields, "idempotency_key") != receipt.IdempotencyKey ||
		developmentEventString(event.Fields, "payload_hash") != receipt.PayloadHash {
		return false, errors.New("BLOCKED stage transition Event does not match its receipt")
	}
	return false, nil
}

func (s *Service) readDevelopmentEvent(ctx context.Context, eventID modulecore.EventID) (DevelopmentEvent, bool, error) {
	if err := eventID.Validate(); err != nil {
		return DevelopmentEvent{}, false, fmt.Errorf("canonical transition EventID is invalid: %w", err)
	}
	reader, ok := s.developmentEvents.(DevelopmentEventReader)
	if !ok {
		return DevelopmentEvent{}, false, errors.New("canonical development event reader unavailable")
	}
	event, found, err := reader.GetDevelopmentEvent(ctx, eventID)
	if err != nil {
		return DevelopmentEvent{}, false, err
	}
	return event, found, nil
}

func validateCanonicalSortedEventIDs(eventIDs []modulecore.EventID) error {
	for index, eventID := range eventIDs {
		if err := eventID.Validate(); err != nil {
			return fmt.Errorf("expected missing transition EventID is invalid: %w", err)
		}
		if index > 0 && eventIDs[index-1] >= eventID {
			return errors.New("expected missing transition EventIDs must be unique and sorted")
		}
	}
	return nil
}

func validateExpectedMissingEventTargets(expected, allowed []modulecore.EventID) error {
	if len(expected) > len(allowed) {
		return errors.New("expected missing transition EventIDs exceed the cancellation's two canonical events")
	}
	for _, eventID := range expected {
		found := false
		for _, allowedID := range allowed {
			if eventID == allowedID {
				found = true
				break
			}
		}
		if !found {
			return errors.New("expected missing transition EventID is not referenced by this freeze or BLOCKED stage")
		}
	}
	return nil
}

func sameTransitionEventIDs(left, right []modulecore.EventID) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
