package backlog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// EvidenceVerificationRequest carries an EvidenceRef claim together with the
// authoritative Atlas lifecycle context that owns the claim.  The context is
// resolved from the current item/freeze by this application layer; callers
// cannot choose a different implementation unit or revision by editing Ref.
type EvidenceVerificationRequest struct {
	Ref                    domainbacklog.EvidenceRef `json:"ref"`
	BacklogItemID          string                    `json:"backlog_item_id"`
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

type QueueFreezeCancellationStore interface {
	CancelQueueFreeze(context.Context, string, domainworkstream.QueueFreezeCancellation) (domainworkstream.QueueFreeze, error)
}

var (
	ErrEvidenceVerifierUnavailable             = errors.New("atlas evidence verifier unavailable")
	ErrLifecycleStoreUnavailable               = errors.New("atlas lifecycle store unavailable")
	ErrLifecycleConflict                       = errors.New("atlas lifecycle idempotency conflict")
	ErrQueueFreezeCancellationStoreUnavailable = errors.New("atlas queue freeze cancellation store unavailable")
)

type runnerExecutionClaim struct {
	token         string
	itemID        string
	unitID        string
	revision      int
	workstreamID  string
	deliveryState string
}

// BeginRunnerExecution claims one exact Atlas selection before the heartbeat
// runner creates its Task or invokes the Worker. The claim is process-local:
// the existing lifecycle mutex orders it with cancellation and admission,
// while the durable lease remains the authority for implementation ownership.
func (s *Service) BeginRunnerExecution(ctx context.Context, selected domainbacklog.Item) (string, error) {
	if s == nil {
		return "", fmt.Errorf("%w: Atlas service unavailable", ErrLifecycleConflict)
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.runnerExecution != nil {
		return "", fmt.Errorf("%w: a runner execution is already active", ErrLifecycleConflict)
	}
	if ctx == nil {
		return "", errors.New("runner execution context is required")
	}

	selected = domainbacklog.ProjectLegacy(selected)
	itemID := strings.TrimSpace(string(selected.BacklogItemID))
	if itemID == "" {
		return "", fmt.Errorf("%w: selected runner item has no backlog id", ErrLifecycleConflict)
	}
	item, err := s.find(ctx, itemID)
	if err != nil {
		return "", fmt.Errorf("%w: selected runner item is unavailable: %v", ErrLifecycleConflict, err)
	}
	item = domainbacklog.ProjectLegacy(item)
	if !reflect.DeepEqual(item, selected) {
		return "", fmt.Errorf("%w: selected runner item changed before execution", ErrLifecycleConflict)
	}
	if item.SchemaVersion != domainbacklog.SchemaVersion2 || item.ConceptState != domainbacklog.ConceptAdopted || !runnerExecutionStage(item.DeliveryState) {
		return "", fmt.Errorf("%w: Atlas item %s is not runnable at delivery state %q", ErrLifecycleConflict, itemID, item.DeliveryState)
	}
	if strings.TrimSpace(item.ImplementationUnit) == "" || strings.TrimSpace(item.WorkstreamID) == "" || item.ImplementationRevision < 1 {
		return "", fmt.Errorf("%w: Atlas item %s has incomplete implementation binding", ErrLifecycleConflict, itemID)
	}
	if _, frozen, err := s.ActiveQueueFreeze(ctx); err != nil {
		return "", err
	} else if frozen {
		return "", fmt.Errorf("%w: Atlas queue is frozen", ErrLifecycleConflict)
	}
	if err := s.rejectCompetingTargetForPendingCancellation(ctx, item); err != nil {
		return "", err
	}
	lease, held, err := s.getLease(ctx, domainbacklog.ImplementationLeaseName)
	if err != nil {
		return "", err
	}
	if !held || lease.LeaseName != domainbacklog.ImplementationLeaseName ||
		lease.HolderUnitID != item.ImplementationUnit || lease.HolderWorkstreamID != item.WorkstreamID {
		return "", fmt.Errorf("%w: selected runner item does not own the implementation lease", ErrLifecycleConflict)
	}
	if lease.Revision != "" {
		leaseRevision, parseErr := strconv.Atoi(strings.TrimSpace(lease.Revision))
		if parseErr != nil || leaseRevision != item.ImplementationRevision {
			return "", fmt.Errorf("%w: selected runner item revision does not match the implementation lease", ErrLifecycleConflict)
		}
	}

	if s.runnerExecutionNonce == ^uint64(0) {
		return "", fmt.Errorf("%w: runner execution claim nonce exhausted", ErrLifecycleConflict)
	}
	s.runnerExecutionNonce++
	token := strconv.FormatUint(s.runnerExecutionNonce, 10)
	s.runnerExecution = &runnerExecutionClaim{
		token: token, itemID: itemID, unitID: item.ImplementationUnit,
		revision: item.ImplementationRevision, workstreamID: item.WorkstreamID,
		deliveryState: item.DeliveryState,
	}
	return token, nil
}

// EndRunnerExecution releases only the claim represented by token. A stale
// End cannot clear a newer execution claim.
func (s *Service) EndRunnerExecution(_ context.Context, token string) error {
	if s == nil {
		return ErrLifecycleConflict
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.runnerExecution == nil || strings.TrimSpace(token) == "" || s.runnerExecution.token != token {
		return fmt.Errorf("%w: runner execution claim token does not match", ErrLifecycleConflict)
	}
	s.runnerExecution = nil
	return nil
}

func runnerExecutionStage(stage string) bool {
	switch strings.ToUpper(strings.TrimSpace(stage)) {
	case domainbacklog.DeliveryQueued, domainbacklog.DeliverySpec, domainbacklog.DeliveryTDDRed,
		domainbacklog.DeliveryTDDGreen, domainbacklog.DeliveryRefactor, domainbacklog.DeliveryE2EPredeploy,
		domainbacklog.DeliveryBuild, domainbacklog.DeliveryDeploy, domainbacklog.DeliveryRestart,
		domainbacklog.DeliveryPostDeployVerify:
		return true
	default:
		return false
	}
}

// ResolveQueueFreezeRequest is the owner request used to replace a blocked
// unit. ActionID is the canonical resolution action identity and idempotency key.
type ResolveQueueFreezeRequest struct {
	Disposition                       string                      `json:"disposition,omitempty"`
	ActionID                          modulecore.ActionID         `json:"action_id"`
	ExpectedFreezeRevision            int                         `json:"expected_freeze_revision"`
	BlockedUnitID                     string                      `json:"blocked_unit_id,omitempty"`
	BlockedRevision                   int                         `json:"blocked_revision,omitempty"`
	Reason                            string                      `json:"reason,omitempty"`
	ExpectedMissingTransitionEventIDs []modulecore.EventID        `json:"expected_missing_transition_event_ids,omitempty"`
	ReplacementUnitID                 string                      `json:"replacement_unit_id"`
	SupersedesUnitID                  string                      `json:"supersedes_unit_id"`
	BlockerResolutionRefs             []domainbacklog.EvidenceRef `json:"blocker_resolution_refs"`
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
	if strings.TrimSpace(request.BacklogItemID) == "" || strings.TrimSpace(request.ImplementationUnitID) == "" || request.ImplementationRevision < 1 || strings.TrimSpace(request.TargetDeliveryState) == "" {
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

func queueFreezeID(unitID string, revision int) string {
	return fmt.Sprintf("atlas-freeze:%s:%d", strings.TrimSpace(unitID), revision)
}

// receiptActionID binds a stage or closure receipt to the Action owner pair
// already attached to the execution context. A replay reuses the persisted
// ActionID and never treats a transport RequestID as an Action identity.
func receiptActionID(ctx context.Context, existing modulecore.ActionID) (modulecore.ActionID, error) {
	if existing != "" {
		if err := existing.Validate(); err != nil {
			return "", fmt.Errorf("existing action_id is invalid: %w", err)
		}
		return existing, nil
	}
	actionID, _, ok := domainexecution.BoundActionAttemptFromContext(ctx)
	if !ok {
		return "", errors.New("canonical Action/Attempt context is required")
	}
	return actionID, nil
}

func transitionEventID(existing modulecore.EventID) modulecore.EventID {
	if existing != "" {
		return existing
	}
	return modulecore.NewEventID()
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

// validateClosureActionBinding verifies that every persisted receipt for the
// unit/revision uses one valid action identity.  Receipts are owner state, so
// callers must not repair or replace a conflicting binding here.
func (s *Service) validateClosureActionBinding(ctx context.Context, unitID string, revision int) (modulecore.ActionID, error) {
	type binding struct {
		kind   string
		key    string
		action modulecore.ActionID
	}

	keys := []binding{
		{kind: "LIVE_VERIFIED stage", key: stageRunKey(unitID, revision, domainbacklog.DeliveryLiveVerified)},
		{kind: "DONE stage", key: stageRunKey(unitID, revision, domainbacklog.DeliveryDone)},
		{kind: "DONE closure", key: stageRunKey(unitID, revision, domainbacklog.DeliveryDone)},
	}
	found := make([]binding, 0, len(keys))
	for index := range keys {
		entry := keys[index]
		if index < 2 {
			receipt, ok, err := s.findStageReceipt(ctx, entry.key)
			if err != nil {
				return "", err
			}
			if !ok {
				continue
			}
			entry.action = receipt.ActionID
		} else {
			receipt, ok, err := s.findClosureReceipt(ctx, entry.key)
			if err != nil {
				return "", err
			}
			if !ok {
				continue
			}
			entry.action = receipt.ActionID
		}
		found = append(found, entry)
	}
	if len(found) == 0 {
		return "", nil
	}
	for _, entry := range found {
		if err := entry.action.Validate(); err != nil {
			return "", fmt.Errorf("%w: %s %s has invalid action_id", ErrLifecycleConflict, entry.kind, entry.key)
		}
	}
	actionID := found[0].action
	for _, entry := range found[1:] {
		if entry.action != actionID {
			return "", fmt.Errorf("%w: persisted closure action_id mismatch for unit %q revision %d: %s=%q, %s=%q", ErrLifecycleConflict, strings.TrimSpace(unitID), revision, found[0].kind, actionID, entry.kind, entry.action)
		}
	}
	return actionID, nil
}

func (s *Service) saveClosureReceipt(ctx context.Context, receipt domainworkstream.ClosureReceipt) error {
	store, ok := s.workstream.(LifecycleStore)
	if !ok {
		return ErrLifecycleStoreUnavailable
	}
	return store.SaveClosureReceipt(ctx, receipt)
}

type rejectedCancellationResourceReader interface {
	ListWorkstreams(context.Context, int) ([]domainworkstream.Workstream, error)
	FindGoalByID(context.Context, string) (domainworkstream.Goal, bool, error)
	ListArtifacts(context.Context, int) ([]domainworkstream.Artifact, error)
}

const rejectedCancellationResourceReadLimit = 1 << 20

func validateRejectedStageItemBinding(stage domainworkstream.StageRunReceipt, item domainbacklog.Item) error {
	if stage.TargetStage != domainbacklog.DeliveryRejected || stage.BacklogItemID != item.BacklogItemID ||
		stage.UnitID != item.ImplementationUnit || stage.ImplementationRevision != item.ImplementationRevision {
		return fmt.Errorf("%w: rejected stage %q does not belong to backlog item %q", ErrLifecycleConflict, stage.IdempotencyKey, item.BacklogItemID)
	}
	return nil
}

func (s *Service) rejectCompetingTargetForPendingCancellation(ctx context.Context, item domainbacklog.Item) error {
	unitID := strings.TrimSpace(item.ImplementationUnit)
	if unitID == "" {
		return nil
	}
	revision := item.ImplementationRevision
	if revision < 1 {
		revision = 1
	}
	key := stageRunKey(unitID, revision, domainbacklog.DeliveryRejected)
	stage, found, err := s.findStageReceipt(ctx, key)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if _, err := validateRejectedStageReceipt(stage); err != nil {
		return err
	}
	if err := validateRejectedStageItemBinding(stage, item); err != nil {
		return err
	}
	return fmt.Errorf("%w: rejected cancellation stage %q already fences this implementation revision", ErrLifecycleConflict, key)
}

func (s *Service) preflightRejectedCancellationLease(ctx context.Context, item domainbacklog.Item) error {
	unitID := strings.TrimSpace(item.ImplementationUnit)
	workstreamID := strings.TrimSpace(item.WorkstreamID)
	if unitID == "" || workstreamID == "" {
		return fmt.Errorf("%w: rejected cancellation has no implementation unit/workstream binding", ErrLifecycleConflict)
	}
	lease, held, err := s.getLease(ctx, domainbacklog.ImplementationLeaseName)
	if err != nil {
		return err
	}
	if held {
		if err := validateRejectedLeaseHolder(lease, item); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) preflightLegacyRejectedCancellationLease(ctx context.Context, item domainbacklog.Item) (bool, error) {
	lease, held, err := s.getLease(ctx, domainbacklog.ImplementationLeaseName)
	if err != nil {
		return false, err
	}
	return s.validateLegacyRejectedCancellationLease(ctx, item, lease, held)
}

// validateLegacyRejectedCancellationLease returns true only when the lease is
// still held by the legacy cancellation's own unit and must be released. A
// different, independently bound unit/workstream pair belongs to a newer
// active operation and must be preserved.
func (s *Service) validateLegacyRejectedCancellationLease(ctx context.Context, item domainbacklog.Item, lease domainworkstream.ImplementationLease, held bool) (bool, error) {
	if !held {
		return false, nil
	}
	if lease.LeaseName != domainbacklog.ImplementationLeaseName {
		return false, fmt.Errorf("%w: legacy rejected cancellation found an unexpected lease name", ErrLifecycleConflict)
	}
	itemUnit := strings.TrimSpace(item.ImplementationUnit)
	itemWorkstream := strings.TrimSpace(item.WorkstreamID)
	leaseUnit := strings.TrimSpace(lease.HolderUnitID)
	leaseWorkstream := strings.TrimSpace(lease.HolderWorkstreamID)
	sameUnit := leaseUnit == itemUnit
	sameWorkstream := leaseWorkstream == itemWorkstream
	if sameUnit && sameWorkstream {
		return true, nil
	}
	if sameUnit || sameWorkstream || leaseUnit == "" || leaseWorkstream == "" {
		return false, fmt.Errorf("%w: legacy rejected cancellation lease partially matches its implementation unit/workstream", ErrLifecycleConflict)
	}
	items, err := s.listAll(ctx)
	if err != nil {
		return false, err
	}
	var holder domainbacklog.Item
	matches := 0
	for _, candidate := range items {
		if candidate.ImplementationUnit == leaseUnit {
			holder = candidate
			matches++
		}
	}
	if matches != 1 || holder.ConceptState != domainbacklog.ConceptAdopted ||
		holder.ImplementationUnit != leaseUnit || holder.WorkstreamID != leaseWorkstream {
		return false, fmt.Errorf("%w: foreign legacy-cancellation lease is not bound to one adopted item", ErrLifecycleConflict)
	}
	return false, nil
}

func validateRejectedLeaseHolder(lease domainworkstream.ImplementationLease, item domainbacklog.Item) error {
	if lease.LeaseName != domainbacklog.ImplementationLeaseName || lease.HolderUnitID != strings.TrimSpace(item.ImplementationUnit) ||
		lease.HolderWorkstreamID != strings.TrimSpace(item.WorkstreamID) {
		return fmt.Errorf("%w: rejected cancellation lease does not match implementation unit/workstream", ErrLifecycleConflict)
	}
	return nil
}

func validateRejectedStageReceipt(stage domainworkstream.StageRunReceipt) (domainbacklog.Item, error) {
	return validateRejectedStageReceiptWithEmptyImplementation(stage, false)
}

func validateRejectedStageReceiptForRecovery(stage domainworkstream.StageRunReceipt) (domainbacklog.Item, error) {
	if stage.Status == domainworkstream.StageRunPrepared || stage.Status == domainworkstream.StageRunCompleted {
		// Pre-change REJECTED receipts could have an empty reason, which was
		// persisted as an empty Implementation string. Recovery permits decoding
		// it here, then requires an exact recognized write-boundary state below.
		return validateRejectedStageReceiptWithEmptyImplementation(stage, true)
	}
	return validateRejectedStageReceipt(stage)
}

func exactLegacyPreparedQueuedRejectedResult(current, result domainbacklog.Item, payloadHash string) bool {
	if current.DeliveryState != domainbacklog.DeliveryQueued || current.CheckOK ||
		result.DeliveryState != domainbacklog.DeliveryRejected || strings.TrimSpace(result.Implementation) != "" ||
		current.BacklogItemID != result.BacklogItemID || current.ConceptState != result.ConceptState ||
		current.ImplementationUnit != result.ImplementationUnit || current.ImplementationRevision != result.ImplementationRevision ||
		current.WorkstreamID != result.WorkstreamID || len(current.EvidenceRefs) > len(result.EvidenceRefs) {
		return false
	}
	for index, ref := range current.EvidenceRefs {
		if ref != result.EvidenceRefs[index] {
			return false
		}
	}
	request := ReviseRequest{
		TargetDeliveryState: domainbacklog.DeliveryRejected,
		EvidenceRefs:        append([]domainbacklog.EvidenceRef(nil), result.EvidenceRefs[len(current.EvidenceRefs):]...),
	}
	payloadMatches := false
	// Before the reason/revision gate was added, Revise accepted an omitted
	// ExpectedRevision (zero) as well as the current revision. A positive
	// mismatch could not reach persistence because the old service rejected it.
	for _, expectedRevision := range []int{0, current.ImplementationRevision} {
		request.ExpectedRevision = expectedRevision
		if stagePayloadHash(request) == payloadHash {
			payloadMatches = true
			break
		}
	}
	if !payloadMatches {
		return false
	}
	// TransitionDelivery changes only these projection fields. Requiring every
	// other field to match prevents a stale prepared receipt from replacing a
	// newer edit to the item while reconciling the old QUEUED write boundary.
	expected := current
	expected.SchemaVersion = result.SchemaVersion
	expected.DeliveryState = result.DeliveryState
	expected.ImplementationRevision = result.ImplementationRevision
	expected.EvidenceRefs = result.EvidenceRefs
	expected.Status = result.Status
	expected.CheckOK = result.CheckOK
	return reflect.DeepEqual(expected, result)
}

func validateRejectedStageReceiptWithEmptyImplementation(stage domainworkstream.StageRunReceipt, allowEmptyImplementation bool) (domainbacklog.Item, error) {
	key := stageRunKey(stage.UnitID, stage.ImplementationRevision, domainbacklog.DeliveryRejected)
	if stage.IdempotencyKey != key || stage.TargetStage != domainbacklog.DeliveryRejected ||
		stage.DeliveryState != domainbacklog.DeliveryRejected || stage.UnitID == "" || stage.BacklogItemID == "" ||
		stage.ImplementationRevision < 1 || !validSHA256(stage.PayloadHash) || stage.ActionID.Validate() != nil {
		return domainbacklog.Item{}, fmt.Errorf("%w: rejected stage receipt %q has inconsistent identity", ErrLifecycleConflict, stage.IdempotencyKey)
	}
	if stage.Status != domainworkstream.StageRunPrepared && stage.Status != domainworkstream.StageRunCompleted {
		return domainbacklog.Item{}, fmt.Errorf("%w: rejected stage receipt %q has status %q", ErrLifecycleConflict, key, stage.Status)
	}
	if stage.Status == domainworkstream.StageRunCompleted && stage.CompletedAt.IsZero() {
		return domainbacklog.Item{}, fmt.Errorf("%w: completed rejected stage receipt %q has no completion time", ErrLifecycleConflict, key)
	}
	var item domainbacklog.Item
	if err := json.Unmarshal([]byte(stage.ResultJSON), &item); err != nil {
		return domainbacklog.Item{}, fmt.Errorf("decode rejected stage receipt result: %w", err)
	}
	if item.BacklogItemID != stage.BacklogItemID || item.ImplementationUnit != stage.UnitID ||
		item.ImplementationRevision != stage.ImplementationRevision || item.ConceptState != domainbacklog.ConceptAdopted ||
		item.DeliveryState != domainbacklog.DeliveryRejected || item.CheckOK || (!allowEmptyImplementation && strings.TrimSpace(item.Implementation) == "") {
		return domainbacklog.Item{}, fmt.Errorf("%w: rejected stage receipt %q result is inconsistent", ErrLifecycleConflict, key)
	}
	for _, ref := range item.EvidenceRefs {
		if err := domainbacklog.ValidateEvidenceRef(ref); err != nil {
			return domainbacklog.Item{}, fmt.Errorf("%w: rejected stage receipt %q has invalid evidence: %v", ErrLifecycleConflict, key, err)
		}
	}
	return item, nil
}

func validateRejectedTerminalItem(stage domainworkstream.StageRunReceipt, stageItem, current domainbacklog.Item) error {
	if current.BacklogItemID != stage.BacklogItemID || current.ConceptState != domainbacklog.ConceptAdopted ||
		current.DeliveryState != domainbacklog.DeliveryRejected || current.ImplementationUnit != stage.UnitID ||
		current.ImplementationRevision != stage.ImplementationRevision || current.WorkstreamID != stageItem.WorkstreamID ||
		current.CheckOK || current.Implementation != stageItem.Implementation || !sameEvidenceRefs(current.EvidenceRefs, stageItem.EvidenceRefs) {
		return fmt.Errorf("%w: rejected stage %q does not match the terminal item", ErrLifecycleConflict, stage.IdempotencyKey)
	}
	return nil
}

// preparedRejectedTerminalItemMatches tolerates the backlog owner's
// NormalizeForSave timestamp update while keeping every other persisted field
// bound to the prepared stage result. Status and CheckOK are already canonical
// for this v2 transition and remain part of the exact comparison.
func preparedRejectedTerminalItemMatches(current, prepared domainbacklog.Item) bool {
	current.UpdatedAt = prepared.UpdatedAt
	return reflect.DeepEqual(current, prepared)
}

func validateRejectedClosureReceipt(stage domainworkstream.StageRunReceipt, item domainbacklog.Item, receipt domainworkstream.ClosureReceipt) error {
	key := stageRunKey(stage.UnitID, stage.ImplementationRevision, domainbacklog.DeliveryRejected)
	goalID := "goal_atlas_" + safeSegment(string(item.BacklogItemID))
	artifactID := "artifact_atlas_" + safeSegment(string(item.BacklogItemID))
	if err := receipt.ReceiptID.Validate(); err != nil || receipt.IdempotencyKey != key || receipt.ActionID.Validate() != nil || receipt.CreatedAt.IsZero() ||
		receipt.ActionID != stage.ActionID || receipt.UnitID != stage.UnitID || receipt.BacklogItemID != stage.BacklogItemID ||
		receipt.ImplementationRevision != stage.ImplementationRevision || receipt.TargetDeliveryState != domainbacklog.DeliveryRejected ||
		receipt.WorkstreamID == "" || receipt.WorkstreamID != item.WorkstreamID || receipt.GoalID != goalID ||
		receipt.ArtifactID != artifactID || receipt.LeaseName != domainbacklog.ImplementationLeaseName {
		return fmt.Errorf("%w: rejected closure receipt %q has inconsistent identity", ErrLifecycleConflict, key)
	}
	switch receipt.Phase {
	case domainworkstream.ClosurePhasePrepared:
		if receipt.LeaseReleased {
			return fmt.Errorf("%w: prepared rejected closure %q claims a released lease", ErrLifecycleConflict, key)
		}
	case domainworkstream.ClosurePhaseResources:
		if receipt.LeaseReleased {
			return fmt.Errorf("%w: resources-complete rejected closure %q claims a released lease", ErrLifecycleConflict, key)
		}
	case domainworkstream.ClosurePhaseLease, domainworkstream.ClosurePhaseDone:
		if !receipt.LeaseReleased {
			return fmt.Errorf("%w: rejected closure %q advanced without releasing its lease", ErrLifecycleConflict, key)
		}
	default:
		return fmt.Errorf("%w: rejected closure %q has phase %q", ErrLifecycleConflict, key, receipt.Phase)
	}
	if receipt.Status == domainworkstream.ClosureStatusCompleted {
		if receipt.Phase != domainworkstream.ClosurePhaseDone || !receipt.LeaseReleased || receipt.Error != "" || receipt.CompletedAt.IsZero() {
			return fmt.Errorf("%w: completed rejected closure %q is incomplete", ErrLifecycleConflict, key)
		}
		return nil
	}
	if receipt.Status != domainworkstream.ClosureStatusPrepared && receipt.Status != domainworkstream.ClosureStatusFailed {
		return fmt.Errorf("%w: rejected closure %q has status %q", ErrLifecycleConflict, key, receipt.Status)
	}
	if receipt.Phase == domainworkstream.ClosurePhaseDone {
		return fmt.Errorf("%w: unfinished rejected closure %q is already in done phase", ErrLifecycleConflict, key)
	}
	return nil
}

func (s *Service) readRejectedCancellationResources(ctx context.Context, receipt domainworkstream.ClosureReceipt, item domainbacklog.Item) (domainworkstream.Workstream, domainworkstream.Goal, domainworkstream.Artifact, error) {
	reader, ok := s.workstream.(rejectedCancellationResourceReader)
	if !ok {
		return domainworkstream.Workstream{}, domainworkstream.Goal{}, domainworkstream.Artifact{}, errors.New("Atlas rejected cancellation resource readers unavailable")
	}
	if strings.TrimSpace(item.WorkstreamID) == "" || receipt.WorkstreamID != item.WorkstreamID {
		return domainworkstream.Workstream{}, domainworkstream.Goal{}, domainworkstream.Artifact{}, fmt.Errorf("%w: rejected cancellation workstream binding is inconsistent", ErrLifecycleConflict)
	}
	workstreams, err := reader.ListWorkstreams(ctx, rejectedCancellationResourceReadLimit)
	if err != nil {
		return domainworkstream.Workstream{}, domainworkstream.Goal{}, domainworkstream.Artifact{}, fmt.Errorf("read rejected cancellation workstream: %w", err)
	}
	var workstream domainworkstream.Workstream
	workstreamFound := false
	for _, candidate := range workstreams {
		if candidate.WorkstreamID == receipt.WorkstreamID {
			workstream, workstreamFound = candidate, true
			break
		}
	}
	if !workstreamFound || workstream.WorkstreamID != item.WorkstreamID {
		return domainworkstream.Workstream{}, domainworkstream.Goal{}, domainworkstream.Artifact{}, fmt.Errorf("%w: rejected cancellation workstream %q not found", ErrLifecycleConflict, receipt.WorkstreamID)
	}
	goal, found, err := reader.FindGoalByID(ctx, receipt.GoalID)
	if err != nil {
		return domainworkstream.Workstream{}, domainworkstream.Goal{}, domainworkstream.Artifact{}, fmt.Errorf("read rejected cancellation goal: %w", err)
	}
	if !found || goal.GoalID != receipt.GoalID || goal.WorkstreamID != item.WorkstreamID {
		return domainworkstream.Workstream{}, domainworkstream.Goal{}, domainworkstream.Artifact{}, fmt.Errorf("%w: rejected cancellation goal %q missing or associated with another workstream", ErrLifecycleConflict, receipt.GoalID)
	}
	artifacts, err := reader.ListArtifacts(ctx, rejectedCancellationResourceReadLimit)
	if err != nil {
		return domainworkstream.Workstream{}, domainworkstream.Goal{}, domainworkstream.Artifact{}, fmt.Errorf("read rejected cancellation artifact: %w", err)
	}
	var artifact domainworkstream.Artifact
	artifactFound := false
	for _, candidate := range artifacts {
		if candidate.ArtifactID == receipt.ArtifactID {
			artifact, artifactFound = candidate, true
			break
		}
	}
	if !artifactFound || artifact.ArtifactID != receipt.ArtifactID || artifact.WorkstreamID != item.WorkstreamID {
		return domainworkstream.Workstream{}, domainworkstream.Goal{}, domainworkstream.Artifact{}, fmt.Errorf("%w: rejected cancellation artifact %q missing or associated with another workstream", ErrLifecycleConflict, receipt.ArtifactID)
	}
	return workstream, goal, artifact, nil
}

func (s *Service) archiveRejectedCancellationResources(ctx context.Context, receipt domainworkstream.ClosureReceipt, item domainbacklog.Item) error {
	workstream, goal, artifact, err := s.readRejectedCancellationResources(ctx, receipt, item)
	if err != nil {
		return err
	}
	now := s.now()
	workstream.Status = domainworkstream.StatusArchived
	workstream.UpdatedAt = now
	goal.Status = domainworkstream.StatusArchived
	artifact.Status = domainworkstream.StatusArchived
	artifact.UpdatedAt = now
	if err := s.workstream.SaveWorkstream(ctx, workstream); err != nil {
		return fmt.Errorf("archive rejected cancellation workstream: %w", err)
	}
	if err := s.workstream.SaveGoal(ctx, goal); err != nil {
		return fmt.Errorf("archive rejected cancellation goal: %w", err)
	}
	if err := s.workstream.SaveArtifact(ctx, artifact); err != nil {
		return fmt.Errorf("archive rejected cancellation artifact: %w", err)
	}
	return nil
}

func (s *Service) resumeRejectedCancellationStage(ctx context.Context, stage domainworkstream.StageRunReceipt) (domainbacklog.Item, error) {
	next, err := validateRejectedStageReceiptForRecovery(stage)
	if err != nil {
		return domainbacklog.Item{}, err
	}
	key := stageRunKey(stage.UnitID, stage.ImplementationRevision, domainbacklog.DeliveryRejected)
	current, err := s.find(ctx, string(stage.BacklogItemID))
	if err != nil {
		return domainbacklog.Item{}, err
	}
	if current.BacklogItemID != stage.BacklogItemID || current.ConceptState != domainbacklog.ConceptAdopted || current.ImplementationUnit != stage.UnitID ||
		current.ImplementationRevision != stage.ImplementationRevision || current.WorkstreamID != next.WorkstreamID || current.DeliveryState == domainbacklog.DeliveryDone ||
		current.DeliveryState == domainbacklog.DeliveryBlocked || current.DeliveryState == domainbacklog.DeliveryLiveVerified {
		return domainbacklog.Item{}, fmt.Errorf("%w: rejected stage %q no longer matches its item", ErrLifecycleConflict, key)
	}
	legacyPreparedQueued := stage.Status == domainworkstream.StageRunPrepared && current.DeliveryState == domainbacklog.DeliveryQueued && strings.TrimSpace(next.Implementation) == ""
	legacyPreparedTerminal := stage.Status == domainworkstream.StageRunPrepared && current.DeliveryState == domainbacklog.DeliveryRejected
	if legacyPreparedQueued && !exactLegacyPreparedQueuedRejectedResult(current, next, stage.PayloadHash) {
		return domainbacklog.Item{}, fmt.Errorf("%w: prepared legacy rejected stage %q does not match its queued item", ErrLifecycleConflict, key)
	}
	if legacyPreparedTerminal && !preparedRejectedTerminalItemMatches(current, next) {
		return domainbacklog.Item{}, fmt.Errorf("%w: prepared legacy rejected stage %q does not match its terminal item", ErrLifecycleConflict, key)
	}
	if stage.Status == domainworkstream.StageRunPrepared && strings.TrimSpace(next.Implementation) == "" && !legacyPreparedQueued && !legacyPreparedTerminal {
		return domainbacklog.Item{}, fmt.Errorf("%w: empty-implementation prepared rejected stage %q is outside a recognized legacy write boundary", ErrLifecycleConflict, key)
	}
	if current.DeliveryState == domainbacklog.DeliveryRejected &&
		(current.CheckOK || current.Implementation != next.Implementation || !sameEvidenceRefs(current.EvidenceRefs, next.EvidenceRefs)) {
		return domainbacklog.Item{}, fmt.Errorf("%w: rejected item %q differs from its stage result", ErrLifecycleConflict, key)
	}

	receipt, found, err := s.findClosureReceipt(ctx, key)
	if err != nil {
		return domainbacklog.Item{}, err
	}
	legacyCleanup := legacyPreparedQueued || current.DeliveryState == domainbacklog.DeliveryRejected &&
		(stage.Status == domainworkstream.StageRunCompleted || legacyPreparedTerminal)
	legacyReconciliation := legacyCleanup || legacyPreparedQueued
	if !found {
		if legacyCleanup {
			if _, err := s.preflightLegacyRejectedCancellationLease(ctx, current); err != nil {
				return domainbacklog.Item{}, err
			}
			if _, _, _, err := s.readRejectedCancellationResources(ctx, domainworkstream.ClosureReceipt{
				WorkstreamID: next.WorkstreamID,
				GoalID:       "goal_atlas_" + safeSegment(string(next.BacklogItemID)),
				ArtifactID:   "artifact_atlas_" + safeSegment(string(next.BacklogItemID)),
			}, next); err != nil {
				return domainbacklog.Item{}, err
			}
		} else if err := s.preflightRejectedCancellationLease(ctx, current); err != nil {
			return domainbacklog.Item{}, err
		}
		receipt = domainworkstream.ClosureReceipt{
			ReceiptID: modulecore.NewReceiptID(), IdempotencyKey: key, ActionID: stage.ActionID,
			TransitionEventID: transitionEventID(""), UnitID: stage.UnitID, BacklogItemID: stage.BacklogItemID,
			ImplementationRevision: stage.ImplementationRevision, TargetDeliveryState: domainbacklog.DeliveryRejected,
			Phase: domainworkstream.ClosurePhasePrepared, Status: domainworkstream.ClosureStatusPrepared,
			WorkstreamID: next.WorkstreamID, GoalID: "goal_atlas_" + safeSegment(string(next.BacklogItemID)),
			ArtifactID: "artifact_atlas_" + safeSegment(string(next.BacklogItemID)), LeaseName: domainbacklog.ImplementationLeaseName,
			CreatedAt: s.now(), UpdatedAt: s.now(),
		}
		eventFields := map[string]any{
			"idempotency_key": key, "payload_hash": stage.PayloadHash,
		}
		if legacyReconciliation {
			eventFields["legacy_reconciliation"] = true
			eventFields["historical_stage_receipt_id"] = string(stage.ReceiptID)
			eventFields["historical_stage_idempotency_key"] = stage.IdempotencyKey
			switch {
			case legacyPreparedQueued:
				eventFields["legacy_reconciliation_boundary"] = "prepared_before_item_save"
			case legacyPreparedTerminal:
				eventFields["legacy_reconciliation_boundary"] = "prepared_after_item_save"
			default:
				eventFields["legacy_reconciliation_boundary"] = "completed_stage_without_closure"
			}
		}
		if err := s.appendTransitionCompanionEvent(ctx, DevelopmentEventClosureTransition, receipt.TransitionEventID, next, domainbacklog.DeliveryRejected, key, "", eventFields); err != nil {
			return domainbacklog.Item{}, err
		}
		if err := s.saveClosureReceipt(ctx, receipt); err != nil {
			return domainbacklog.Item{}, err
		}
	}
	if err := validateRejectedClosureReceipt(stage, next, receipt); err != nil {
		return domainbacklog.Item{}, err
	}
	if receipt.Status == domainworkstream.ClosureStatusCompleted {
		if current.DeliveryState != domainbacklog.DeliveryRejected {
			return domainbacklog.Item{}, fmt.Errorf("%w: completed rejected closure %q has no rejected item", ErrLifecycleConflict, key)
		}
		if stage.Status == domainworkstream.StageRunPrepared {
			stage.Status = domainworkstream.StageRunCompleted
			stage.DeliveryState = domainbacklog.DeliveryRejected
			stage.CompletedAt = s.now()
			if err := s.saveStageReceipt(ctx, stage); err != nil {
				return domainbacklog.Item{}, err
			}
		}
		return next, nil
	}
	if legacyCleanup {
		if _, err := s.preflightLegacyRejectedCancellationLease(ctx, current); err != nil {
			return domainbacklog.Item{}, err
		}
	} else if err := s.preflightRejectedCancellationLease(ctx, current); err != nil {
		return domainbacklog.Item{}, err
	}
	if legacyCleanup {
		if _, _, _, err := s.readRejectedCancellationResources(ctx, receipt, next); err != nil {
			return domainbacklog.Item{}, err
		}
	}
	if stage.Status == domainworkstream.StageRunCompleted && !legacyCleanup {
		return domainbacklog.Item{}, fmt.Errorf("%w: completed rejected stage %q has unfinished closure", ErrLifecycleConflict, key)
	}
	if receipt.Status == domainworkstream.ClosureStatusFailed {
		receipt.Status = domainworkstream.ClosureStatusPrepared
		receipt.Error = ""
		receipt.UpdatedAt = s.now()
		if err := s.saveClosureReceipt(ctx, receipt); err != nil {
			return domainbacklog.Item{}, err
		}
	}
	if receipt.Phase == domainworkstream.ClosurePhasePrepared {
		if err := s.archiveRejectedCancellationResources(ctx, receipt, next); err != nil {
			return domainbacklog.Item{}, s.failClosure(ctx, receipt, err)
		}
		receipt.Phase = domainworkstream.ClosurePhaseResources
		receipt.Status = domainworkstream.ClosureStatusPrepared
		receipt.Error = ""
		receipt.UpdatedAt = s.now()
		if err := s.saveClosureReceipt(ctx, receipt); err != nil {
			return domainbacklog.Item{}, err
		}
	}
	lease, held, err := s.getLease(ctx, receipt.LeaseName)
	if err != nil {
		return domainbacklog.Item{}, err
	}
	shouldRelease := held
	if legacyCleanup {
		shouldRelease, err = s.validateLegacyRejectedCancellationLease(ctx, current, lease, held)
		if err != nil {
			return domainbacklog.Item{}, err
		}
	} else if held {
		if lease.LeaseName != receipt.LeaseName || lease.HolderUnitID != receipt.UnitID || lease.HolderWorkstreamID != receipt.WorkstreamID {
			return domainbacklog.Item{}, fmt.Errorf("%w: rejected closure %q found a different lease holder", ErrLifecycleConflict, key)
		}
	}
	if shouldRelease {
		if err := s.releaseLease(ctx, receipt.LeaseName, receipt.UnitID); err != nil {
			return domainbacklog.Item{}, s.failClosure(ctx, receipt, err)
		}
	}
	receipt.LeaseReleased = true
	receipt.Phase = domainworkstream.ClosurePhaseLease
	receipt.Status = domainworkstream.ClosureStatusPrepared
	receipt.Error = ""
	receipt.UpdatedAt = s.now()
	if err := s.saveClosureReceipt(ctx, receipt); err != nil {
		return domainbacklog.Item{}, err
	}
	if current.DeliveryState != domainbacklog.DeliveryRejected {
		if err := s.save(ctx, next); err != nil {
			return domainbacklog.Item{}, s.failClosure(ctx, receipt, err)
		}
	}
	receipt.Phase = domainworkstream.ClosurePhaseDone
	receipt.Status = domainworkstream.ClosureStatusCompleted
	receipt.Error = ""
	receipt.CompletedAt = s.now()
	receipt.UpdatedAt = receipt.CompletedAt
	if err := s.saveClosureReceipt(ctx, receipt); err != nil {
		return domainbacklog.Item{}, err
	}
	if stage.Status == domainworkstream.StageRunPrepared {
		stage.Status = domainworkstream.StageRunCompleted
		stage.DeliveryState = domainbacklog.DeliveryRejected
		stage.CompletedAt = s.now()
		if err := s.saveStageReceipt(ctx, stage); err != nil {
			return domainbacklog.Item{}, err
		}
	}
	return next, nil
}

func (s *Service) resumeRejectedCancellationForItem(ctx context.Context, item domainbacklog.Item) (domainbacklog.Item, bool, error) {
	if item.DeliveryState != domainbacklog.DeliveryRejected {
		return domainbacklog.Item{}, false, nil
	}
	if item.ConceptState != domainbacklog.ConceptAdopted || strings.TrimSpace(item.ImplementationUnit) == "" {
		return domainbacklog.Item{}, false, fmt.Errorf("%w: terminal rejected item has no adopted implementation identity", ErrLifecycleConflict)
	}
	key := stageRunKey(item.ImplementationUnit, item.ImplementationRevision, domainbacklog.DeliveryRejected)
	stage, found, err := s.findStageReceipt(ctx, key)
	if err != nil {
		return domainbacklog.Item{}, false, err
	}
	if !found {
		return domainbacklog.Item{}, false, fmt.Errorf("%w: terminal rejected item %q has no stage receipt", ErrLifecycleConflict, key)
	}
	result, err := s.resumeRejectedCancellationStage(ctx, stage)
	return result, true, err
}

func (s *Service) resumeRejectedCancellation(ctx context.Context) (domainbacklog.Item, bool, error) {
	items, err := s.listAll(ctx)
	if err != nil {
		return domainbacklog.Item{}, false, err
	}
	for _, item := range items {
		if item.ConceptState == domainbacklog.ConceptRejected {
			continue
		}
		if item.DeliveryState != domainbacklog.DeliveryRejected {
			continue
		}
		if item.ConceptState != domainbacklog.ConceptAdopted || strings.TrimSpace(item.ImplementationUnit) == "" || item.ImplementationRevision < 1 {
			return domainbacklog.Item{}, false, fmt.Errorf("%w: terminal rejected item has no valid adopted implementation identity", ErrLifecycleConflict)
		}
	}
	store, ok := s.workstream.(LifecycleStore)
	if !ok {
		for _, item := range items {
			if item.ConceptState != domainbacklog.ConceptRejected && item.DeliveryState == domainbacklog.DeliveryRejected {
				return domainbacklog.Item{}, false, fmt.Errorf("%w: terminal rejected item has no lifecycle store", ErrLifecycleStoreUnavailable)
			}
		}
		return domainbacklog.Item{}, false, nil
	}
	const stageRunReceiptScanLimit = 1 << 20
	stages, err := store.ListStageRunReceipts(ctx, stageRunReceiptScanLimit)
	if err != nil {
		return domainbacklog.Item{}, false, err
	}
	if len(stages) >= stageRunReceiptScanLimit {
		return domainbacklog.Item{}, false, fmt.Errorf("%w: rejected cancellation stage receipt scan reached the %d-row limit and may be truncated", ErrLifecycleConflict, stageRunReceiptScanLimit)
	}
	byKey := make(map[string]domainworkstream.StageRunReceipt)
	preparedPending := make([]domainworkstream.StageRunReceipt, 0, 1)
	legacyPending := make([]domainworkstream.StageRunReceipt, 0, 1)
	for _, stage := range stages {
		if stage.TargetStage != domainbacklog.DeliveryRejected && stage.DeliveryState != domainbacklog.DeliveryRejected {
			continue
		}
		stageItem, validateErr := validateRejectedStageReceiptForRecovery(stage)
		if validateErr != nil {
			return domainbacklog.Item{}, false, validateErr
		}
		key := stageRunKey(stage.UnitID, stage.ImplementationRevision, domainbacklog.DeliveryRejected)
		byKey[key] = stage
		current, findErr := s.find(ctx, string(stage.BacklogItemID))
		if findErr != nil {
			return domainbacklog.Item{}, false, findErr
		}
		if current.BacklogItemID != stage.BacklogItemID || current.ConceptState != domainbacklog.ConceptAdopted || current.ImplementationUnit != stage.UnitID ||
			current.ImplementationRevision != stage.ImplementationRevision || current.WorkstreamID != stageItem.WorkstreamID || current.DeliveryState == domainbacklog.DeliveryDone ||
			current.DeliveryState == domainbacklog.DeliveryBlocked || current.DeliveryState == domainbacklog.DeliveryLiveVerified {
			return domainbacklog.Item{}, false, fmt.Errorf("%w: rejected stage %q no longer matches its item", ErrLifecycleConflict, key)
		}
		if stage.Status == domainworkstream.StageRunPrepared {
			closure, found, closureErr := s.findClosureReceipt(ctx, key)
			if closureErr != nil {
				return domainbacklog.Item{}, false, closureErr
			}
			if found {
				if err := validateRejectedClosureReceipt(stage, stageItem, closure); err != nil {
					return domainbacklog.Item{}, false, err
				}
			}
			if current.DeliveryState == domainbacklog.DeliveryRejected {
				if err := validateRejectedTerminalItem(stage, stageItem, current); err != nil || !preparedRejectedTerminalItemMatches(current, stageItem) {
					if err != nil {
						return domainbacklog.Item{}, false, err
					}
					return domainbacklog.Item{}, false, fmt.Errorf("%w: prepared rejected stage %q does not match its terminal item", ErrLifecycleConflict, key)
				}
				legacyPending = append(legacyPending, stage)
				continue
			}
			if strings.TrimSpace(stageItem.Implementation) == "" && !exactLegacyPreparedQueuedRejectedResult(current, stageItem, stage.PayloadHash) {
				return domainbacklog.Item{}, false, fmt.Errorf("%w: empty-implementation prepared rejected stage %q is outside a recognized legacy write boundary", ErrLifecycleConflict, key)
			}
			if strings.TrimSpace(stageItem.Implementation) == "" {
				// This is a fully bound legacy prepared result at the old QUEUED
				// write boundary. Drain it with terminal historical cancellations;
				// it is not a competing active intent from the current protocol.
				legacyPending = append(legacyPending, stage)
				continue
			}
			preparedPending = append(preparedPending, stage)
			continue
		}
		closure, found, closureErr := s.findClosureReceipt(ctx, key)
		if closureErr != nil {
			return domainbacklog.Item{}, false, closureErr
		}
		if !found {
			if stage.Status != domainworkstream.StageRunCompleted {
				return domainbacklog.Item{}, false, fmt.Errorf("%w: rejected stage %q has no closure receipt", ErrLifecycleConflict, key)
			}
			if err := validateRejectedTerminalItem(stage, stageItem, current); err != nil {
				return domainbacklog.Item{}, false, err
			}
			legacyPending = append(legacyPending, stage)
			continue
		}
		if err := validateRejectedClosureReceipt(stage, stageItem, closure); err != nil {
			return domainbacklog.Item{}, false, err
		}
		if err := validateRejectedTerminalItem(stage, stageItem, current); err != nil {
			return domainbacklog.Item{}, false, err
		}
		if closure.Status != domainworkstream.ClosureStatusCompleted {
			if stage.Status != domainworkstream.StageRunCompleted {
				return domainbacklog.Item{}, false, fmt.Errorf("%w: rejected stage %q has unfinished closure", ErrLifecycleConflict, key)
			}
			// A completed legacy stage can be reconciled only when its exact
			// rejected Item and the new cleanup receipt remain bound above.
			legacyPending = append(legacyPending, stage)
		}
	}
	for _, item := range items {
		if item.ConceptState == domainbacklog.ConceptRejected {
			continue
		}
		if item.DeliveryState != domainbacklog.DeliveryRejected {
			continue
		}
		key := stageRunKey(item.ImplementationUnit, item.ImplementationRevision, domainbacklog.DeliveryRejected)
		stage, found := byKey[key]
		if !found {
			return domainbacklog.Item{}, false, fmt.Errorf("%w: terminal rejected item %q has no stage receipt", ErrLifecycleConflict, key)
		}
		if stage.BacklogItemID != item.BacklogItemID || stage.UnitID != item.ImplementationUnit || stage.ImplementationRevision != item.ImplementationRevision {
			return domainbacklog.Item{}, false, fmt.Errorf("%w: terminal rejected item %q does not match its stage receipt binding", ErrLifecycleConflict, key)
		}
	}
	if len(preparedPending) > 1 {
		return domainbacklog.Item{}, false, fmt.Errorf("%w: multiple prepared rejected cancellations found", ErrLifecycleConflict)
	}
	if len(legacyPending) == 0 && len(preparedPending) == 0 {
		return domainbacklog.Item{}, false, nil
	}
	var result domainbacklog.Item
	for _, stage := range legacyPending {
		result, err = s.resumeRejectedCancellationStage(ctx, stage)
		if err != nil {
			return domainbacklog.Item{}, true, err
		}
	}
	for _, stage := range preparedPending {
		result, err = s.resumeRejectedCancellationStage(ctx, stage)
		if err != nil {
			return domainbacklog.Item{}, true, err
		}
	}
	return result, true, nil
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
		unitID = string(next.BacklogItemID)
	}
	actionID, err := s.validateClosureActionBinding(ctx, unitID, next.ImplementationRevision)
	if err != nil {
		return err
	}
	receipt, found, err := s.findClosureReceipt(ctx, key)
	if err != nil {
		return err
	}
	now := s.now()
	if !found {
		operationActionID, actionErr := receiptActionID(ctx, actionID)
		if actionErr != nil {
			return actionErr
		}
		closureEventID := transitionEventID("")
		if err := s.appendTransitionCompanionEvent(ctx, DevelopmentEventClosureTransition, closureEventID, next, string(domainbacklog.DeliveryDone), key, request.RequestID, map[string]any{
			"idempotency_key": key, "payload_hash": payloadHash,
		}); err != nil {
			return err
		}
		receipt = domainworkstream.ClosureReceipt{
			ReceiptID: modulecore.NewReceiptID(), IdempotencyKey: key, ActionID: operationActionID,
			TransitionEventID: closureEventID,
			UnitID:            unitID, BacklogItemID: next.BacklogItemID, ImplementationRevision: next.ImplementationRevision,
			Phase: domainworkstream.ClosurePhasePrepared, Status: domainworkstream.ClosureStatusPrepared,
			WorkstreamID: next.WorkstreamID, GoalID: "goal_atlas_" + safeSegment(string(next.BacklogItemID)),
			ArtifactID: "artifact_atlas_" + safeSegment(string(next.BacklogItemID)), LeaseName: domainbacklog.ImplementationLeaseName,
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
				Description: "Implementation Unit " + unitID + " for Atlas item " + string(next.BacklogItemID),
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
		unitID = string(live.BacklogItemID)
	}
	actionID, err := s.validateClosureActionBinding(ctx, unitID, live.ImplementationRevision)
	if err != nil {
		return domainbacklog.Item{}, err
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
		operationActionID, actionErr := receiptActionID(ctx, actionID)
		if actionErr != nil {
			return domainbacklog.Item{}, actionErr
		}
		doneStageEventID := transitionEventID("")
		if err := s.appendTransitionCompanionEvent(ctx, DevelopmentEventStageRunTransition, doneStageEventID, live, string(domainbacklog.DeliveryDone), doneKey, doneRequest.RequestID, map[string]any{
			"idempotency_key": doneKey, "payload_hash": donePayloadHash,
		}); err != nil {
			return domainbacklog.Item{}, err
		}
		doneReceipt = domainworkstream.StageRunReceipt{
			ReceiptID: modulecore.NewReceiptID(), IdempotencyKey: doneKey,
			ActionID: operationActionID, TransitionEventID: doneStageEventID,
			UnitID: unitID, BacklogItemID: live.BacklogItemID,
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
	// Recovery may enter directly at DONE; its prepared receipt is the owner
	// of the action subsequently recorded by completeDone.
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
	freezeEventID := transitionEventID("")
	if err := s.appendTransitionCompanionEvent(ctx, DevelopmentEventQueueFreezeTransition, freezeEventID, next, string(domainbacklog.DeliveryBlocked), queueFreezeID(next.ImplementationUnit, next.ImplementationRevision), request.RequestID, map[string]any{
		"reason_code":            firstNonEmpty(strings.TrimSpace(request.Reason), "stage_failed"),
		"invalidated_from_stage": next.InvalidatedFromStage,
	}); err != nil {
		return err
	}
	freeze := domainworkstream.QueueFreeze{
		FreezeID:      queueFreezeID(next.ImplementationUnit, next.ImplementationRevision),
		BlockedUnitID: next.ImplementationUnit, BlockedRevision: next.ImplementationRevision, FreezeRevision: 1,
		TransitionEventID:    freezeEventID,
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
