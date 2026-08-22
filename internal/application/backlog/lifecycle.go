package backlog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
)

// EvidenceVerificationRequest carries an EvidenceRef claim together with the
// authoritative Atlas lifecycle context that owns the claim.  The context is
// resolved from the current item/freeze by this application layer; callers
// cannot choose a different implementation unit or revision by editing Ref.
type EvidenceVerificationRequest struct {
	Ref                    domainbacklog.EvidenceRef `json:"ref"`
	ItemID                 string                    `json:"item_id"`
	ImplementationUnitID   string                    `json:"implementation_unit_id"`
	ImplementationRevision int                       `json:"implementation_revision"`
	TargetDeliveryState    string                    `json:"target_delivery_state"`
	Purpose                string                    `json:"purpose,omitempty"`
}

// EvidenceVerifier is the CORE-owned boundary for turning an EvidenceRef
// claim into a persisted verification result. The service clears all
// request-provided verification fields before calling it and supplies the
// authoritative item/unit/revision/stage context in the typed request.
type EvidenceVerifier interface {
	Verify(context.Context, EvidenceVerificationRequest) (bool, error)
}

// LifecycleStore is the optional durable extension of the existing
// Workstream store.  Implementations persist lifecycle records in the same
// physical store as leases; the service continues to work with older stores
// that only implement WorkstreamCreator/ImplementationLeaseStore.
type LifecycleStore interface {
	SaveQueueFreeze(context.Context, domainworkstream.QueueFreeze) error
	GetQueueFreeze(context.Context, string) (domainworkstream.QueueFreeze, bool, error)
	ListQueueFreezes(context.Context, int) ([]domainworkstream.QueueFreeze, error)
	SaveStageRunReceipt(context.Context, domainworkstream.StageRunReceipt) error
	FindStageRunReceipt(context.Context, string) (domainworkstream.StageRunReceipt, bool, error)
	ListStageRunReceipts(context.Context, int) ([]domainworkstream.StageRunReceipt, error)
	SaveClosureReceipt(context.Context, domainworkstream.ClosureReceipt) error
	FindClosureReceipt(context.Context, string) (domainworkstream.ClosureReceipt, bool, error)
	ListClosureReceipts(context.Context, int) ([]domainworkstream.ClosureReceipt, error)
	// AcquireImplementationLeaseIfUnfrozen performs the queue-freeze and
	// singleton-lease check as one owner-store operation.
	AcquireImplementationLeaseIfUnfrozen(context.Context, domainworkstream.ImplementationLease) (bool, string, error)
	// ResolveQueueFreezeAndAcquireLease resolves one exact active freeze and
	// acquires its replacement lease in the same owner-store operation.  The
	// complete resolution payload is passed to the store so it is persisted
	// atomically with the lease.
	ResolveQueueFreezeAndAcquireLease(context.Context, string, domainworkstream.QueueFreezeResolution, domainworkstream.ImplementationLease) (domainworkstream.QueueFreeze, domainworkstream.ImplementationLease, bool, error)
}

var (
	ErrEvidenceVerifierUnavailable = errors.New("atlas evidence verifier unavailable")
	ErrLifecycleStoreUnavailable   = errors.New("atlas lifecycle store unavailable")
	ErrLifecycleConflict           = errors.New("atlas lifecycle idempotency conflict")
)

// ResolveQueueFreezeRequest is the owner request used to replace a blocked
// unit. RequestID is the idempotency key for the resolution operation.
type ResolveQueueFreezeRequest struct {
	RequestID              string                      `json:"request_id"`
	ExpectedFreezeRevision int                         `json:"expected_freeze_revision"`
	ReplacementUnitID      string                      `json:"replacement_unit_id"`
	SupersedesUnitID       string                      `json:"supersedes_unit_id"`
	BlockerResolutionRefs  []domainbacklog.EvidenceRef `json:"blocker_resolution_refs"`
}

// AcquireRunnableResult is returned by the dispatcher-facing service method.
// Acquired=false is a normal result when the queue is frozen or another
// implementation lease is occupied; Item is zero when no runnable item was
// selected.
type AcquireRunnableResult struct {
	Item     domainbacklog.Item                   `json:"item"`
	Lease    domainworkstream.ImplementationLease `json:"lease"`
	Acquired bool                                 `json:"acquired"`
	Reason   string                               `json:"reason,omitempty"`
}

// WithEvidenceVerifier installs the owner verifier.
func (s *Service) WithEvidenceVerifier(verifier EvidenceVerifier) *Service {
	if s != nil {
		s.verifier = verifier
	}
	return s
}

func clearVerification(ref domainbacklog.EvidenceRef) domainbacklog.EvidenceRef {
	ref.Verified = false
	ref.VerificationResult = ""
	ref.VerifiedAt = ""
	ref.Verifier = ""
	return ref
}

func normalizeEvidenceRefStage(ref domainbacklog.EvidenceRef, target string) (domainbacklog.EvidenceRef, error) {
	target = strings.ToUpper(strings.TrimSpace(target))
	if target == "" {
		return ref, errors.New("evidence target delivery state is required")
	}
	stage := strings.TrimSpace(ref.Stage)
	if stage != "" && !strings.EqualFold(stage, target) {
		return ref, fmt.Errorf("evidence stage %q does not match target delivery state %q", ref.Stage, target)
	}
	// Empty stages are bound to the current owner operation. Canonicalizing a
	// supplied equivalent stage also makes the persisted evidence deterministic.
	ref.Stage = target
	return ref, nil
}

func (s *Service) verifyEvidence(ctx context.Context, request EvidenceVerificationRequest) (domainbacklog.EvidenceRef, error) {
	clean := clearVerification(request.Ref)
	normalized, normalizeErr := normalizeEvidenceRefStage(clean, request.TargetDeliveryState)
	if normalizeErr != nil {
		return clean, normalizeErr
	}
	request.Ref = normalized
	if strings.TrimSpace(request.ItemID) == "" || strings.TrimSpace(request.ImplementationUnitID) == "" || request.ImplementationRevision < 1 || strings.TrimSpace(request.TargetDeliveryState) == "" {
		return clean, errors.New("authoritative Atlas evidence context is incomplete")
	}
	if s == nil || s.verifier == nil {
		return clean, ErrEvidenceVerifierUnavailable
	}
	ok, err := s.verifier.Verify(ctx, request)
	if err != nil {
		return clean, err
	}
	if !ok {
		return clean, domainbacklog.ErrEvidenceRequired
	}
	normalized.Verified = true
	normalized.VerificationResult = domainbacklog.EvidenceVerificationVerified
	normalized.VerifiedAt = s.now().Format(time.RFC3339Nano)
	return normalized, nil
}

type stagePayload struct {
	TargetDeliveryState string                      `json:"delivery_state"`
	EvidenceRefs        []domainbacklog.EvidenceRef `json:"evidence_refs"`
	Reason              string                      `json:"reason,omitempty"`
	ExpectedRevision    int                         `json:"expected_revision,omitempty"`
}

func stageRunKey(unitID string, revision int, target string) string {
	return fmt.Sprintf("%s:%d:%s", strings.TrimSpace(unitID), revision, strings.ToUpper(strings.TrimSpace(target)))
}

func stagePayloadHash(request ReviseRequest) string {
	refs := make([]domainbacklog.EvidenceRef, len(request.EvidenceRefs))
	copy(refs, request.EvidenceRefs)
	for index := range refs {
		// Verification fields are an owner-produced result, not part of the
		// caller's idempotency claim.  A replay with forged or stale verifier
		// metadata must hash identically to the same unverified claim.
		refs[index] = clearVerification(refs[index])
	}
	payload, _ := json.Marshal(stagePayload{
		TargetDeliveryState: strings.ToUpper(strings.TrimSpace(request.TargetDeliveryState)),
		EvidenceRefs:        refs,
		Reason:              strings.TrimSpace(request.Reason),
		ExpectedRevision:    request.ExpectedRevision,
	})
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:])
}

func stageRunReceiptID(key string) string { return "atlas-stage:" + key }
func closureReceiptID(unitID string, revision int) string {
	return fmt.Sprintf("atlas-closure:%s:%d", strings.TrimSpace(unitID), revision)
}
func queueFreezeID(unitID string, revision int) string {
	return fmt.Sprintf("atlas-freeze:%s:%d", strings.TrimSpace(unitID), revision)
}

func (s *Service) findStageReceipt(ctx context.Context, key string) (domainworkstream.StageRunReceipt, bool, error) {
	store, ok := s.workstream.(LifecycleStore)
	if !ok {
		return domainworkstream.StageRunReceipt{}, false, ErrLifecycleStoreUnavailable
	}
	return store.FindStageRunReceipt(ctx, key)
}

func (s *Service) saveStageReceipt(ctx context.Context, receipt domainworkstream.StageRunReceipt) error {
	store, ok := s.workstream.(LifecycleStore)
	if !ok {
		return ErrLifecycleStoreUnavailable
	}
	return store.SaveStageRunReceipt(ctx, receipt)
}

func (s *Service) findClosureReceipt(ctx context.Context, key string) (domainworkstream.ClosureReceipt, bool, error) {
	store, ok := s.workstream.(LifecycleStore)
	if !ok {
		return domainworkstream.ClosureReceipt{}, false, ErrLifecycleStoreUnavailable
	}
	return store.FindClosureReceipt(ctx, key)
}

func (s *Service) saveClosureReceipt(ctx context.Context, receipt domainworkstream.ClosureReceipt) error {
	store, ok := s.workstream.(LifecycleStore)
	if !ok {
		return ErrLifecycleStoreUnavailable
	}
	return store.SaveClosureReceipt(ctx, receipt)
}

func (s *Service) saveFreeze(ctx context.Context, freeze domainworkstream.QueueFreeze) error {
	store, ok := s.workstream.(LifecycleStore)
	if !ok {
		return ErrLifecycleStoreUnavailable
	}
	return store.SaveQueueFreeze(ctx, freeze)
}

func (s *Service) findFreeze(ctx context.Context, id string) (domainworkstream.QueueFreeze, bool, error) {
	store, ok := s.workstream.(LifecycleStore)
	if !ok {
		return domainworkstream.QueueFreeze{}, false, ErrLifecycleStoreUnavailable
	}
	return store.GetQueueFreeze(ctx, id)
}

func (s *Service) completeDone(ctx context.Context, before, next domainbacklog.Item, request ReviseRequest, key, payloadHash string) error {
	unitID := strings.TrimSpace(next.ImplementationUnit)
	if unitID == "" {
		unitID = next.ItemID
	}
	receiptID := closureReceiptID(unitID, next.ImplementationRevision)
	receipt, found, err := s.findClosureReceipt(ctx, key)
	if err != nil {
		return err
	}
	now := s.now()
	if !found {
		receipt = domainworkstream.ClosureReceipt{
			ReceiptID: receiptID, IdempotencyKey: key, RequestID: strings.TrimSpace(request.RequestID),
			UnitID: unitID, ItemID: next.ItemID, ImplementationRevision: next.ImplementationRevision,
			Phase: domainworkstream.ClosurePhasePrepared, Status: domainworkstream.ClosureStatusPrepared,
			WorkstreamID: next.WorkstreamID, GoalID: "goal_atlas_" + safeSegment(next.ItemID),
			ArtifactID: "artifact_atlas_" + safeSegment(next.ItemID), LeaseName: domainbacklog.ImplementationLeaseName,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := s.saveClosureReceipt(ctx, receipt); err != nil {
			return err
		}
	}

	// Workstream, Goal, and Artifact have stable IDs derived at adoption.  The
	// existing store API is append/upsert based, so saving completed projections
	// is also the compatible update path for older stores without read methods.
	if receipt.Phase == domainworkstream.ClosurePhasePrepared {
		if s.workstream != nil {
			completedAt := now
			if err := s.workstream.SaveWorkstream(ctx, domainworkstream.Workstream{
				WorkstreamID: next.WorkstreamID, Name: "Atlas: " + next.Title,
				Description: "Implementation Unit " + unitID + " for Atlas item " + next.ItemID,
				Status:      domainworkstream.StatusCompleted, PrimaryAgent: "Coder", CreatedAt: now, UpdatedAt: now,
			}); err != nil {
				return s.failClosure(ctx, receipt, err)
			}
			if err := s.workstream.SaveGoal(ctx, domainworkstream.Goal{
				GoalID: receipt.GoalID, WorkstreamID: next.WorkstreamID, Title: next.Title,
				Description: next.Body, SuccessCriteria: []string{"required Atlas evidence exists", "the unit reaches LIVE_VERIFIED before DONE"},
				Verification: []string{"owner API stage transitions", "post-deploy/readiness evidence"},
				Status:       domainworkstream.StatusCompleted, CreatedAt: now, CompletedAt: completedAt,
			}); err != nil {
				return s.failClosure(ctx, receipt, err)
			}
			if err := s.workstream.SaveArtifact(ctx, domainworkstream.Artifact{
				ArtifactID: receipt.ArtifactID, WorkstreamID: next.WorkstreamID,
				Type: "atlas_implementation_unit", Title: next.Title, Status: domainworkstream.StatusCompleted,
				CreatedAt: now, UpdatedAt: now,
			}); err != nil {
				return s.failClosure(ctx, receipt, err)
			}
		}
		receipt.Phase = domainworkstream.ClosurePhaseResources
		receipt.UpdatedAt = s.now()
		if err := s.saveClosureReceipt(ctx, receipt); err != nil {
			return err
		}
	}
	if !receipt.LeaseReleased {
		if err := s.releaseLease(ctx, domainbacklog.ImplementationLeaseName, next.ImplementationUnit); err != nil {
			return s.failClosure(ctx, receipt, err)
		}
		receipt.LeaseReleased = true
		receipt.Phase = domainworkstream.ClosurePhaseLease
		receipt.UpdatedAt = s.now()
		if err := s.saveClosureReceipt(ctx, receipt); err != nil {
			return err
		}
	}
	// Lease release is intentionally before the DONE item write.  A failure in
	// release therefore cannot be hidden by a terminal item record.
	if before.DeliveryState != domainbacklog.DeliveryDone {
		if err := s.save(ctx, next); err != nil {
			return err
		}
	}
	receipt.Phase = domainworkstream.ClosurePhaseDone
	receipt.Status = domainworkstream.ClosureStatusCompleted
	receipt.CompletedAt = s.now()
	receipt.UpdatedAt = receipt.CompletedAt
	return s.saveClosureReceipt(ctx, receipt)
}

// completeLiveVerifiedClosure turns a successful LIVE_VERIFIED stage into the
// owner-controlled DONE closure in the same service operation.  The LIVE
// stage receipt is completed by Revise before this helper is called; the DONE
// stage receipt is prepared here so a restart can resume closure without a
// second worker or human request.
func (s *Service) completeLiveVerifiedClosure(ctx context.Context, live domainbacklog.Item, request ReviseRequest) (domainbacklog.Item, error) {
	unitID := strings.TrimSpace(live.ImplementationUnit)
	if unitID == "" {
		unitID = live.ItemID
	}
	doneRequest := request
	doneRequest.TargetDeliveryState = domainbacklog.DeliveryDone
	doneKey := stageRunKey(unitID, live.ImplementationRevision, domainbacklog.DeliveryDone)
	donePayloadHash := stagePayloadHash(doneRequest)
	doneReceipt, found, err := s.findStageReceipt(ctx, doneKey)
	if err != nil {
		return domainbacklog.Item{}, err
	}
	if found && doneReceipt.PayloadHash != donePayloadHash {
		return domainbacklog.Item{}, fmt.Errorf("%w: stage %s", ErrLifecycleConflict, doneKey)
	}
	done := live
	done.DeliveryState = domainbacklog.DeliveryDone
	if found && strings.TrimSpace(doneReceipt.ResultJSON) != "" {
		if err := json.Unmarshal([]byte(doneReceipt.ResultJSON), &done); err != nil {
			return domainbacklog.Item{}, fmt.Errorf("decode done stage receipt result: %w", err)
		}
	}
	if found && doneReceipt.Status == domainworkstream.StageRunCompleted && strings.TrimSpace(doneReceipt.ResultJSON) != "" {
		return done, nil
	}
	if found && doneReceipt.Status != domainworkstream.StageRunPrepared {
		return domainbacklog.Item{}, fmt.Errorf("atlas stage receipt %s has status %q", doneKey, doneReceipt.Status)
	}
	if !found {
		resultJSON, marshalErr := json.Marshal(done)
		if marshalErr != nil {
			return domainbacklog.Item{}, marshalErr
		}
		doneReceipt = domainworkstream.StageRunReceipt{
			ReceiptID: stageRunReceiptID(doneKey), IdempotencyKey: doneKey,
			RequestID: strings.TrimSpace(doneRequest.RequestID), UnitID: unitID, ItemID: live.ItemID,
			ImplementationRevision: live.ImplementationRevision, TargetStage: domainbacklog.DeliveryDone,
			PayloadHash: donePayloadHash, Status: domainworkstream.StageRunPrepared,
			DeliveryState: domainbacklog.DeliveryDone, ResultJSON: string(resultJSON), CreatedAt: s.now(),
		}
	} else {
		doneReceipt.Status = domainworkstream.StageRunPrepared
		doneReceipt.DeliveryState = domainbacklog.DeliveryDone
		if strings.TrimSpace(doneReceipt.ResultJSON) == "" {
			resultJSON, marshalErr := json.Marshal(done)
			if marshalErr != nil {
				return domainbacklog.Item{}, marshalErr
			}
			doneReceipt.ResultJSON = string(resultJSON)
		}
	}
	if err := s.saveStageReceipt(ctx, doneReceipt); err != nil {
		return domainbacklog.Item{}, err
	}
	if err := s.completeDone(ctx, live, done, doneRequest, doneKey, donePayloadHash); err != nil {
		return domainbacklog.Item{}, err
	}
	doneReceipt.Status = domainworkstream.StageRunCompleted
	doneReceipt.DeliveryState = domainbacklog.DeliveryDone
	doneReceipt.CompletedAt = s.now()
	if err := s.saveStageReceipt(ctx, doneReceipt); err != nil {
		return domainbacklog.Item{}, err
	}
	return done, nil
}

func (s *Service) failClosure(ctx context.Context, receipt domainworkstream.ClosureReceipt, err error) error {
	receipt.Status = domainworkstream.ClosureStatusFailed
	receipt.Error = err.Error()
	receipt.UpdatedAt = s.now()
	_ = s.saveClosureReceipt(ctx, receipt)
	return err
}

func (s *Service) completeBlocked(ctx context.Context, before, next domainbacklog.Item, request ReviseRequest) error {
	next.InvalidatedFromStage = before.DeliveryState
	if strings.TrimSpace(request.Reason) == "" {
		next.Implementation = "stage_failed"
	} else {
		next.Implementation = strings.TrimSpace(request.Reason)
	}
	now := s.now()
	if err := s.save(ctx, next); err != nil {
		return err
	}
	freeze := domainworkstream.QueueFreeze{
		FreezeID:      queueFreezeID(next.ImplementationUnit, next.ImplementationRevision),
		BlockedUnitID: next.ImplementationUnit, BlockedRevision: next.ImplementationRevision, FreezeRevision: 1,
		ReasonCode:           firstNonEmpty(strings.TrimSpace(request.Reason), "stage_failed"),
		InvalidatedFromStage: next.InvalidatedFromStage,
		EvidenceRefs:         append([]domainbacklog.EvidenceRef(nil), request.EvidenceRefs...),
		Status:               domainworkstream.QueueFreezeActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.saveFreeze(ctx, freeze); err != nil {
		return err
	}
	if err := s.releaseLease(ctx, domainbacklog.ImplementationLeaseName, next.ImplementationUnit); err != nil {
		return err
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
