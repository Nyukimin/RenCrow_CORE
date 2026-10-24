package conversation

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

var (
	ErrPreparedRecallProjectionDenied      = errors.New("prepared recall projection owner or scope denied")
	ErrPreparedRecallProjectionUnavailable = errors.New("prepared recall projection unavailable")
	ErrPreparedRecallProjectionStale       = errors.New("prepared recall projection source is stale")
	ErrPreparedRecallProjectionReleased    = errors.New("prepared recall projection lease released")
)

// PreparedRecallEligibilityRelease releases the owner's source-version
// serialization acquired by a successful eligibility callback.
type PreparedRecallEligibilityRelease func()

// PreparedRecallCurrentEligibility verifies sourceVersion and, when eligible,
// returns a release function while keeping source changes serialized. The
// manager publishes the staged candidate before calling release. A false
// result must not retain a lock. Revoke must still be called before the owner
// reports successful forget, supersede, or scope-reduction receipts.
type PreparedRecallCurrentEligibility func(ownerID, scope, sourceVersion string) (PreparedRecallEligibilityRelease, bool)

// PreparedRecallProjectionOwner accounts for all views and in-flight builds
// for one owner/scope pair. SourceVersion remains opaque and owner-owned; the
// counters here are only RAM projection accounting and invalidation state.
type PreparedRecallProjectionOwner struct {
	mu sync.Mutex
	// Serializes the owner eligibility callback and its commit/finalize pair.
	// Ready acquisition and selection never take this lock.
	publishMu sync.Mutex

	ownerID             string
	scope               string
	aggregateLimitBytes uint64

	current      *preparedRecallProjectionGeneration
	generations  map[*preparedRecallProjectionGeneration]struct{}
	candidates   map[uint64]*preparedRecallProjectionGeneration
	reservations map[uint64]uint64
	nextBuildID  uint64
	revokeEpoch  uint64
}

type preparedRecallProjectionGeneration struct {
	view          *domainconversation.PreparedRecallView
	sourceVersion string
	bytes         uint64
	readers       atomic.Uint64
	revokeEpoch   uint64
	buildID       uint64
	candidate     bool
	current       bool

	mu      sync.RWMutex
	revoked bool
}

// PreparedRecallProjectionReadLease pins a generation until Release. It does
// not expose the mutable manager or the underlying view.
type PreparedRecallProjectionReadLease struct {
	manager    *PreparedRecallProjectionOwner
	generation *preparedRecallProjectionGeneration

	mu       sync.RWMutex
	released bool
}

// NewPreparedRecallProjectionOwner creates a projection RAM owner bound to a
// single authenticated owner and scope. AggregateLimitBytes may be reduced
// for deployments/tests, but cannot exceed the domain's fixed aggregate cap.
func NewPreparedRecallProjectionOwner(ownerID, scope string, aggregateLimitBytes uint64) (*PreparedRecallProjectionOwner, error) {
	if strings.TrimSpace(ownerID) == "" || strings.TrimSpace(scope) == "" {
		return nil, ErrPreparedRecallProjectionDenied
	}
	if aggregateLimitBytes == 0 || aggregateLimitBytes > domainconversation.PreparedRecallMaxAggregateBytes {
		return nil, domainconversation.ErrPreparedRecallCapacity
	}
	return &PreparedRecallProjectionOwner{
		ownerID:             ownerID,
		scope:               scope,
		aggregateLimitBytes: aggregateLimitBytes,
		generations:         make(map[*preparedRecallProjectionGeneration]struct{}),
		candidates:          make(map[uint64]*preparedRecallProjectionGeneration),
		reservations:        make(map[uint64]uint64),
	}, nil
}

// BuildAndPublish reserves the maximum possible new view before building so
// concurrent builders cannot overcommit aggregate RAM. Input.MemoryBudget's
// RetainedBytes is the owner's estimate of other source/build data that stays
// live during construction; manager-owned generations and other build
// reservations are always added independently.
func (o *PreparedRecallProjectionOwner) BuildAndPublish(input domainconversation.PreparedRecallViewInput, currentEligibility PreparedRecallCurrentEligibility) (string, error) {
	if o == nil || input.AuthenticatedOwnerID != o.ownerID || input.UserScope != o.scope {
		return domainconversation.PreparedRecallPreparationDenied, ErrPreparedRecallProjectionDenied
	}
	if currentEligibility == nil {
		return domainconversation.PreparedRecallPreparationUnavailable, ErrPreparedRecallProjectionUnavailable
	}
	ownerRetainedBytes := input.MemoryBudget.RetainedBytes
	buildID, buildEpoch, retainedBytes, err := o.reserveBuild(ownerRetainedBytes)
	if err != nil {
		return domainconversation.PreparedRecallPreparationUnavailable, err
	}
	defer o.releaseBuild(buildID)

	input.MemoryBudget = domainconversation.PreparedRecallMemoryBudget{
		AggregateLimitBytes: o.aggregateLimitBytes,
		RetainedBytes:       retainedBytes,
	}
	view, err := domainconversation.NewPreparedRecallView(input)
	if err != nil {
		if errors.Is(err, domainconversation.ErrPreparedRecallCapacity) {
			return domainconversation.PreparedRecallPreparationUnavailable, err
		}
		return domainconversation.PreparedRecallPreparationDenied, err
	}
	if !o.stageCandidate(buildID, buildEpoch, input.SourceVersion, ownerRetainedBytes, view) {
		return domainconversation.PreparedRecallPreparationStale, ErrPreparedRecallProjectionStale
	}

	o.publishMu.Lock()
	releaseEligibility, eligible := currentEligibility(input.AuthenticatedOwnerID, input.UserScope, input.SourceVersion)
	finalized := false
	if eligible && releaseEligibility != nil {
		finalized = o.finalizePublication(buildID, buildEpoch)
	}
	if releaseEligibility != nil {
		releaseEligibility()
	}
	o.publishMu.Unlock()
	if !eligible || releaseEligibility == nil || !finalized {
		return domainconversation.PreparedRecallPreparationStale, ErrPreparedRecallProjectionStale
	}
	return domainconversation.PreparedRecallPreparationReady, nil
}

// Acquire pins the current generation only when the authenticated owner,
// scope, and owner-provided expected source version all match.
func (o *PreparedRecallProjectionOwner) Acquire(ownerID, scope, expectedSourceVersion string) (*PreparedRecallProjectionReadLease, string, error) {
	if o == nil || ownerID != o.ownerID || scope != o.scope {
		return nil, domainconversation.PreparedRecallPreparationDenied, ErrPreparedRecallProjectionDenied
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.current == nil {
		return nil, domainconversation.PreparedRecallPreparationUnavailable, ErrPreparedRecallProjectionUnavailable
	}
	if expectedSourceVersion == "" || expectedSourceVersion != o.current.sourceVersion {
		return nil, domainconversation.PreparedRecallPreparationStale, ErrPreparedRecallProjectionStale
	}
	o.current.readers.Add(1)
	return &PreparedRecallProjectionReadLease{manager: o, generation: o.current}, domainconversation.PreparedRecallPreparationReady, nil
}

// Revoke synchronously invalidates every extant generation before returning.
// It is intended to be called on the owner's serialized mutation path before
// success receipts for forget, supersede, or scope reduction.
func (o *PreparedRecallProjectionOwner) Revoke(ownerID, scope string) error {
	if o == nil || ownerID != o.ownerID || scope != o.scope {
		return ErrPreparedRecallProjectionDenied
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.revokeEpoch++
	o.current = nil
	for generation := range o.generations {
		generation.mu.Lock()
		generation.revoked = true
		generation.mu.Unlock()
		generation.current = false
		if generation.readers.Load() == 0 {
			delete(o.generations, generation)
		}
	}
	clear(o.candidates)
	return nil
}

func (l *PreparedRecallProjectionReadLease) Select(input domainconversation.PreparedRecallSelectInput) (domainconversation.PreparedRecallSelection, error) {
	if l == nil || l.generation == nil {
		return domainconversation.PreparedRecallSelection{PreparationStatus: domainconversation.PreparedRecallPreparationUnavailable}, ErrPreparedRecallProjectionReleased
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.released {
		return domainconversation.PreparedRecallSelection{PreparationStatus: domainconversation.PreparedRecallPreparationUnavailable}, ErrPreparedRecallProjectionReleased
	}
	generation := l.generation
	generation.mu.RLock()
	defer generation.mu.RUnlock()
	if generation.revoked {
		return domainconversation.PreparedRecallSelection{PreparationStatus: domainconversation.PreparedRecallPreparationStale}, ErrPreparedRecallProjectionStale
	}
	selection := generation.view.Select(input)
	return selection, nil
}

// Release drops the generation pin. It is idempotent and waits for an
// in-progress Select on this lease to finish.
func (l *PreparedRecallProjectionReadLease) Release() {
	if l == nil || l.manager == nil || l.generation == nil {
		return
	}
	l.mu.Lock()
	if l.released {
		l.mu.Unlock()
		return
	}
	l.released = true
	manager := l.manager
	generation := l.generation
	l.mu.Unlock()
	manager.releaseGeneration(generation)
}

func (o *PreparedRecallProjectionOwner) reserveBuild(ownerRetainedBytes uint64) (uint64, uint64, uint64, error) {
	maxViewBytes := uint64(domainconversation.PreparedRecallMaxViewBytes)
	buildScratchBytes := domainconversation.PreparedRecallMaxAggregateBytes - 2*maxViewBytes
	reservationBytes := maxViewBytes + buildScratchBytes
	o.mu.Lock()
	defer o.mu.Unlock()
	usedBytes, err := o.usedBytesLocked(0)
	if err != nil || ownerRetainedBytes > o.aggregateLimitBytes || usedBytes > o.aggregateLimitBytes-ownerRetainedBytes || reservationBytes > o.aggregateLimitBytes-ownerRetainedBytes-usedBytes {
		return 0, 0, 0, domainconversation.ErrPreparedRecallCapacity
	}
	o.nextBuildID++
	if o.nextBuildID == 0 {
		o.nextBuildID++
	}
	buildID := o.nextBuildID
	o.reservations[buildID] = reservationBytes + ownerRetainedBytes
	retainedBytes, err := o.usedBytesLocked(buildID)
	if err != nil || ownerRetainedBytes > o.aggregateLimitBytes-retainedBytes {
		delete(o.reservations, buildID)
		return 0, 0, 0, domainconversation.ErrPreparedRecallCapacity
	}
	retainedBytes += ownerRetainedBytes
	return buildID, o.revokeEpoch, retainedBytes, nil
}

func (o *PreparedRecallProjectionOwner) releaseBuild(buildID uint64) {
	o.mu.Lock()
	delete(o.reservations, buildID)
	if candidate := o.candidates[buildID]; candidate != nil {
		delete(o.candidates, buildID)
		delete(o.generations, candidate)
	}
	o.mu.Unlock()
}

func (o *PreparedRecallProjectionOwner) stageCandidate(buildID, buildEpoch uint64, sourceVersion string, ownerRetainedBytes uint64, view *domainconversation.PreparedRecallView) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.reservations[buildID]; !ok || buildEpoch != o.revokeEpoch {
		return false
	}
	candidate := &preparedRecallProjectionGeneration{
		view:          view,
		sourceVersion: sourceVersion,
		bytes:         view.EstimatedBytes(),
		buildID:       buildID,
		revokeEpoch:   buildEpoch,
		candidate:     true,
	}
	if candidate.sourceVersion == "" {
		return false
	}
	o.candidates[buildID] = candidate
	o.generations[candidate] = struct{}{}
	// Building scratch has ended. Keep owner-held source/input buffers
	// reserved until eligibility returns, and account the immutable candidate.
	o.reservations[buildID] = ownerRetainedBytes
	return true
}

func (o *PreparedRecallProjectionOwner) finalizePublication(buildID, buildEpoch uint64) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	candidate := o.candidates[buildID]
	if candidate == nil || buildEpoch != o.revokeEpoch || !candidate.candidate {
		return false
	}
	delete(o.candidates, buildID)
	if old := o.current; old != nil {
		old.current = false
		if old.readers.Load() == 0 {
			delete(o.generations, old)
		}
	}
	candidate.candidate = false
	candidate.current = true
	o.current = candidate
	return true
}

func (o *PreparedRecallProjectionOwner) releaseGeneration(generation *preparedRecallProjectionGeneration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if generation.readers.Load() > 0 {
		generation.readers.Add(^uint64(0))
	}
	if generation.readers.Load() == 0 && !generation.current {
		delete(o.generations, generation)
	}
}

func (o *PreparedRecallProjectionOwner) usedBytesLocked(excludeReservation uint64) (uint64, error) {
	var used uint64
	for generation := range o.generations {
		if ^uint64(0)-used < generation.bytes {
			return 0, fmt.Errorf("prepared recall projection byte accounting overflow")
		}
		used += generation.bytes
	}
	for buildID, bytes := range o.reservations {
		if buildID == excludeReservation {
			continue
		}
		if ^uint64(0)-used < bytes {
			return 0, fmt.Errorf("prepared recall projection byte accounting overflow")
		}
		used += bytes
	}
	return used, nil
}
