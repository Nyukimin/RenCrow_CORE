package conversation

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

var (
	ErrPreparedRecallRuntimeUnavailable = errors.New("prepared recall runtime unavailable")
	ErrPreparedRecallRuntimeClosed      = errors.New("prepared recall runtime closed")
	ErrPreparedRecallRuntimeOwner       = errors.New("prepared recall runtime owner is invalid")
)

const (
	preparedRecallInitialWarmRetryAttempts  = 5
	preparedRecallInitialWarmAttemptTimeout = 5 * time.Second
	preparedRecallInitialWarmRetryBackoff   = 250 * time.Millisecond
	preparedRecallInitialWarmMaxBackoff     = 4 * time.Second
)

// PreparedRecallUserMemorySource is the narrow, explicit-owner L1 source used
// by the runtime. Implementations return a bounded snapshot and its coverage.
type PreparedRecallUserMemorySource interface {
	ReadPreparedRecallUserMemories(context.Context, string) ([]domainmemory.UserMemory, string, error)
}

// PreparedRecallRuntimeCapability is a copied view of the current projection
// binding. Its getters do not alter the runtime; source versions are equality
// tokens and callers must not derive ordering from their contents.
type PreparedRecallRuntimeCapability struct {
	ownerID    string
	scope      string
	version    string
	projection *PreparedRecallProjectionOwner
}

func (c PreparedRecallRuntimeCapability) OwnerID() string { return c.ownerID }

func (c PreparedRecallRuntimeCapability) UserScope() string { return c.scope }

func (c PreparedRecallRuntimeCapability) SourceVersion() string { return c.version }

// ReadyOption converts the read-only binding into the existing turn-scoped
// engine option. The returned option is a snapshot; it does not change the
// runtime's current owner or source version.
func (c PreparedRecallRuntimeCapability) ReadyOption(preparation *domainconversation.ConversationTurnPreparation) (PreparedRecallReadyOption, bool) {
	if c.projection == nil || c.ownerID == "" || c.scope == "" || c.version == "" {
		return PreparedRecallReadyOption{}, false
	}
	return PreparedRecallReadyOption{
		ProjectionOwner:       c.projection,
		ExpectedSourceVersion: c.version,
		Preparation:           preparation,
	}, true
}

// PreparedRecallRuntime owns one owner's in-memory prepared-recall lifecycle.
// Mutation notices serialize only revocation and generation updates; source
// I/O and immutable view construction run in the single background worker.
type PreparedRecallRuntime struct {
	ownerID    string
	scope      string
	source     PreparedRecallUserMemorySource
	projection *PreparedRecallProjectionOwner

	mu                sync.Mutex
	revision          uint64
	versionSequence   uint64
	publishedRevision uint64
	currentVersion    string
	closed            bool
	buildMu           sync.Mutex
	warmMu            sync.Mutex
	warmReady         chan struct{}
	warmReadyOnce     sync.Once
	initialWarmDone   bool
	initialWarmRetry  bool
	rebuildSignal     chan struct{}
	ctx               context.Context
	cancel            context.CancelFunc
	done              chan struct{}
}

// NewPreparedRecallRuntime binds the runtime to the authenticated owner
// supplied by its caller. It never infers an owner from process configuration.
func NewPreparedRecallRuntime(authenticatedOwnerID string, source PreparedRecallUserMemorySource) (*PreparedRecallRuntime, error) {
	if authenticatedOwnerID == "" || strings.TrimSpace(authenticatedOwnerID) != authenticatedOwnerID || source == nil {
		return nil, ErrPreparedRecallRuntimeOwner
	}
	scope := "user:" + authenticatedOwnerID
	projection, err := NewPreparedRecallProjectionOwner(authenticatedOwnerID, scope, domainconversation.PreparedRecallMaxAggregateBytes)
	if err != nil {
		return nil, fmt.Errorf("create prepared recall projection owner: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runtime := &PreparedRecallRuntime{
		ownerID:       authenticatedOwnerID,
		scope:         scope,
		source:        source,
		projection:    projection,
		revision:      1,
		warmReady:     make(chan struct{}),
		rebuildSignal: make(chan struct{}, 1),
		ctx:           ctx,
		cancel:        cancel,
		done:          make(chan struct{}),
	}
	go runtime.runWorker()
	return runtime, nil
}

// Warm builds the initial bounded source snapshot before exposing a current
// capability. If an owner mutation invalidates an in-flight snapshot, Warm
// rereads the current source until it publishes a current generation or ctx
// ends. It is safe to call again after an earlier failed warm.
func (r *PreparedRecallRuntime) Warm(ctx context.Context) error {
	if r == nil || ctx == nil {
		return ErrPreparedRecallRuntimeUnavailable
	}
	r.warmMu.Lock()
	defer r.warmMu.Unlock()
	defer r.warmReadyOnce.Do(func() { close(r.warmReady) })
	r.mu.Lock()
	initialWarm := !r.initialWarmDone
	r.mu.Unlock()
	if _, ok := r.CurrentCapability(); ok {
		if initialWarm {
			r.finishInitialWarm(false)
		}
		return nil
	}

	operationCtx, cancel := context.WithCancel(ctx)
	stopRuntimeCancel := context.AfterFunc(r.ctx, cancel)
	defer func() {
		stopRuntimeCancel()
		cancel()
	}()

	for {
		published, stale, err := r.rebuild(operationCtx)
		if err != nil {
			if initialWarm {
				retry := retryablePreparedRecallInitialWarm(err, operationCtx.Err())
				r.finishInitialWarm(retry)
				if retry {
					log.Printf("[PreparedRecallRuntime] initial warm retry scheduled cause=%s max_attempts=%d", preparedRecallWarmFailureKind(err, operationCtx.Err()), preparedRecallInitialWarmRetryAttempts)
				}
			}
			return err
		}
		if published {
			if initialWarm {
				r.finishInitialWarm(false)
			}
			return nil
		}
		if !stale {
			if initialWarm {
				r.finishInitialWarm(false)
			}
			return ErrPreparedRecallRuntimeUnavailable
		}
		if err := operationCtx.Err(); err != nil {
			warmErr := fmt.Errorf("warm prepared recall runtime: %w", err)
			if initialWarm {
				retry := retryablePreparedRecallInitialWarm(warmErr, err)
				r.finishInitialWarm(retry)
				if retry {
					log.Printf("[PreparedRecallRuntime] initial warm retry scheduled cause=%s max_attempts=%d", preparedRecallWarmFailureKind(warmErr, err), preparedRecallInitialWarmRetryAttempts)
				}
			}
			return warmErr
		}
	}
}

func (r *PreparedRecallRuntime) finishInitialWarm(retry bool) {
	r.mu.Lock()
	if !r.initialWarmDone {
		r.initialWarmDone = true
		r.initialWarmRetry = retry
	}
	r.mu.Unlock()
}

// HandleUserMemoryMutation synchronously revokes this owner's current view,
// then coalesces rebuild work onto the one bounded worker signal. ChangeID is
// intentionally ignored because it is opaque and has no ordering semantics.
func (r *PreparedRecallRuntime) HandleUserMemoryMutation(notice l1sqlite.UserMemoryMutationNotice) {
	if r == nil || notice.UserID != r.ownerID {
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	if r.revision == ^uint64(0) {
		r.currentVersion = ""
		r.publishedRevision = 0
		r.closed = true
		_ = r.projection.Revoke(r.ownerID, r.scope)
		r.mu.Unlock()
		r.cancel()
		return
	}
	r.revision++
	r.currentVersion = ""
	r.publishedRevision = 0
	revokeErr := r.projection.Revoke(r.ownerID, r.scope)
	r.mu.Unlock()
	if revokeErr != nil {
		// The manager was created for this exact owner/scope. Any impossible
		// mismatch is terminal and must leave the runtime unavailable.
		r.mu.Lock()
		r.closed = true
		r.mu.Unlock()
		r.cancel()
		return
	}
	select {
	case r.rebuildSignal <- struct{}{}:
	default:
	}
}

// CurrentCapability returns the current owner/version binding only while the
// corresponding immutable projection is still the latest published revision.
func (r *PreparedRecallRuntime) CurrentCapability() (PreparedRecallRuntimeCapability, bool) {
	if r == nil {
		return PreparedRecallRuntimeCapability{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.currentVersion == "" || r.publishedRevision != r.revision {
		return PreparedRecallRuntimeCapability{}, false
	}
	return PreparedRecallRuntimeCapability{
		ownerID:    r.ownerID,
		scope:      r.scope,
		version:    r.currentVersion,
		projection: r.projection,
	}, true
}

// Close revokes the current view, cancels source work, and waits for the
// coalescing worker to exit. It is safe to call more than once.
func (r *PreparedRecallRuntime) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		r.currentVersion = ""
		r.publishedRevision = 0
		_ = r.projection.Revoke(r.ownerID, r.scope)
	}
	r.mu.Unlock()
	r.cancel()
	r.warmReadyOnce.Do(func() { close(r.warmReady) })
	<-r.done
	return nil
}

func (r *PreparedRecallRuntime) runWorker() {
	defer close(r.done)
	select {
	case <-r.ctx.Done():
		return
	case <-r.warmReady:
	}
	if r.ctx.Err() != nil {
		return
	}
	r.mu.Lock()
	retryInitialWarm := r.initialWarmRetry && !r.closed
	r.initialWarmRetry = false
	r.mu.Unlock()
	if retryInitialWarm {
		r.retryInitialWarm()
	}
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-r.rebuildSignal:
			_, _, _ = r.rebuild(r.ctx)
		}
	}
}

func (r *PreparedRecallRuntime) retryInitialWarm() {
	delay := preparedRecallInitialWarmRetryBackoff
	var lastErr error
	var lastAttemptErr error
	for attempt := 1; attempt <= preparedRecallInitialWarmRetryAttempts; attempt++ {
		if !waitPreparedRecallRetry(r.ctx, delay) {
			return
		}
		if _, ok := r.CurrentCapability(); ok {
			log.Printf("[PreparedRecallRuntime] initial warm recovered before retry attempt=%d", attempt)
			return
		}
		if r.ctx.Err() != nil {
			return
		}

		attemptCtx, cancel := context.WithTimeout(r.ctx, preparedRecallInitialWarmAttemptTimeout)
		published, stale, err := r.rebuild(attemptCtx)
		attemptErr := attemptCtx.Err()
		cancel()
		if published {
			log.Printf("[PreparedRecallRuntime] initial warm recovered attempt=%d", attempt)
			return
		}
		if stale {
			// The mutation handler already queued the coalesced current-generation
			// rebuild. Let the regular worker loop consume that owner signal.
			return
		}
		if err == nil {
			err = ErrPreparedRecallRuntimeUnavailable
		}
		lastErr, lastAttemptErr = err, attemptErr
		if r.ctx.Err() != nil {
			return
		}
		if !retryablePreparedRecallInitialWarm(err, attemptErr) {
			log.Printf("[PreparedRecallRuntime] initial warm retry stopped attempt=%d cause=%s", attempt, preparedRecallWarmFailureKind(err, attemptErr))
			return
		}
		log.Printf("[PreparedRecallRuntime] initial warm retry failed attempt=%d/%d cause=%s", attempt, preparedRecallInitialWarmRetryAttempts, preparedRecallWarmFailureKind(err, attemptErr))
		if attempt < preparedRecallInitialWarmRetryAttempts {
			delay *= 2
			if delay > preparedRecallInitialWarmMaxBackoff {
				delay = preparedRecallInitialWarmMaxBackoff
			}
		}
	}
	log.Printf("[PreparedRecallRuntime] initial warm retries exhausted attempts=%d cause=%s", preparedRecallInitialWarmRetryAttempts, preparedRecallWarmFailureKind(lastErr, lastAttemptErr))
}

func waitPreparedRecallRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return ctx.Err() == nil
	}
}

func retryablePreparedRecallInitialWarm(err, attemptErr error) bool {
	if permanentPreparedRecallWarmFailure(err) {
		return false
	}
	return attemptErr == context.DeadlineExceeded || errors.Is(err, context.DeadlineExceeded)
}

func permanentPreparedRecallWarmFailure(err error) bool {
	kind := preparedRecallWarmFailureKind(err, nil)
	return kind == "capacity" || kind == "owner_or_validation"
}

func preparedRecallWarmFailureKind(err, attemptErr error) string {
	if errors.Is(err, domainconversation.ErrPreparedRecallCapacity) {
		return "capacity"
	}
	if errors.Is(err, ErrPreparedRecallRuntimeOwner) ||
		errors.Is(err, ErrPreparedRecallRuntimeClosed) ||
		errors.Is(err, ErrPreparedRecallProjectionDenied) ||
		errors.Is(err, domainconversation.ErrPreparedRecallMissingScope) ||
		errors.Is(err, domainmemory.ErrUserMemoryOwnerInvalid) ||
		errors.Is(err, domainmemory.ErrUserMemoryOwnerNotFound) ||
		errors.Is(err, domainmemory.ErrUserMemoryOwnerForbidden) ||
		errors.Is(err, domainmemory.ErrUserMemoryOwnerConflict) {
		return "owner_or_validation"
	}
	if attemptErr == context.DeadlineExceeded || errors.Is(err, context.DeadlineExceeded) {
		return "deadline"
	}
	if errors.Is(err, domainmemory.ErrUserMemoryOwnerUnavailable) {
		return "source_unavailable"
	}
	return "unavailable"
}

func (r *PreparedRecallRuntime) rebuild(ctx context.Context) (published bool, stale bool, err error) {
	r.buildMu.Lock()
	defer r.buildMu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, false, err
	}
	revision, version, err := r.nextBuildVersion()
	if err != nil {
		return false, false, err
	}

	memories, coverage, err := r.source.ReadPreparedRecallUserMemories(ctx, r.ownerID)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return false, false, err
		}
		return false, false, errors.Join(ErrPreparedRecallRuntimeUnavailable, fmt.Errorf("read owner prepared recall source: %w", err))
	}
	retainedBytes, err := preparedRecallRuntimeSnapshotBytes(r.ownerID, r.scope, memories, coverage)
	if err != nil {
		return false, false, fmt.Errorf("validate owner prepared recall source: %w", err)
	}
	input := domainconversation.PreparedRecallViewInput{
		AuthenticatedOwnerID: r.ownerID,
		UserScope:            r.scope,
		SourceVersion:        version,
		MemoryBudget: domainconversation.PreparedRecallMemoryBudget{
			AggregateLimitBytes: domainconversation.PreparedRecallMaxAggregateBytes,
			RetainedBytes:       retainedBytes,
		},
		Coverage: domainconversation.PreparedRecallCoverage{
			UserMemory: coverage,
			Knowledge:  domainconversation.PreparedRecallCoverageNone,
		},
		EligibleUserMemories: memories,
		// Knowledge coverage stays none until its owner has a real, synchronous
		// invalidation path. No Knowledge body enters this projection.
		ValidatedCategoryRecords: nil,
	}
	status, err := r.projection.BuildAndPublish(input, func(ownerID, scope, sourceVersion string) (PreparedRecallEligibilityRelease, bool) {
		if ownerID != r.ownerID || scope != r.scope || sourceVersion != version {
			return nil, false
		}
		r.mu.Lock()
		if r.closed || r.revision != revision {
			r.mu.Unlock()
			return nil, false
		}
		// BuildAndPublish holds publishMu and finalizes before invoking this
		// release. Mutation handlers hold only mu then the projection's RAM lock.
		return r.mu.Unlock, true
	})
	if err != nil {
		if errors.Is(err, ErrPreparedRecallProjectionStale) {
			return false, true, nil
		}
		return false, false, errors.Join(ErrPreparedRecallRuntimeUnavailable, fmt.Errorf("publish prepared recall view: %w", err))
	}
	if status != domainconversation.PreparedRecallPreparationReady {
		if status == domainconversation.PreparedRecallPreparationStale {
			return false, true, nil
		}
		return false, false, fmt.Errorf("%w: projection returned status %q", ErrPreparedRecallRuntimeUnavailable, status)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.revision != revision {
		return false, true, nil
	}
	r.currentVersion = version
	r.publishedRevision = revision
	return true, false, nil
}

func (r *PreparedRecallRuntime) nextBuildVersion() (uint64, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, "", ErrPreparedRecallRuntimeClosed
	}
	if r.versionSequence == ^uint64(0) {
		return 0, "", ErrPreparedRecallRuntimeUnavailable
	}
	r.versionSequence++
	// The sequence is minted by this owner and never copied from audit IDs.
	// Consumers treat the resulting value as opaque and compare only equality.
	return r.revision, "prepared-recall/" + strconv.FormatUint(r.versionSequence, 36), nil
}

func preparedRecallRuntimeSnapshotBytes(ownerID, scope string, memories []domainmemory.UserMemory, coverage string) (uint64, error) {
	if len(memories) > domainconversation.PreparedRecallMaxRecordsPerSource {
		return 0, domainconversation.ErrPreparedRecallCapacity
	}
	switch coverage {
	case domainconversation.PreparedRecallCoverageFull, domainconversation.PreparedRecallCoveragePartial:
	case domainconversation.PreparedRecallCoverageNone:
		if len(memories) != 0 {
			return 0, fmt.Errorf("none coverage returned records")
		}
	default:
		return 0, fmt.Errorf("invalid user memory coverage %q", coverage)
	}

	total := uint64(512)
	for index, memory := range memories {
		if memory.UserID != ownerID || memory.Namespace != scope {
			return 0, fmt.Errorf("memory %d is outside the authenticated owner namespace", index)
		}
		if len(memory.EvidenceEventIDs) > domainconversation.PreparedRecallMaxIndexKeysPerRecord {
			return 0, domainconversation.ErrPreparedRecallCapacity
		}
		if !addPreparedRecallRuntimeBytes(&total, 512) {
			return 0, domainconversation.ErrPreparedRecallCapacity
		}
		for _, value := range []string{
			memory.ID, memory.Namespace, memory.UserID, memory.Type, memory.Statement,
			string(memory.CreatedByEventID), string(memory.UpdatedByEventID), memory.Sensitivity,
			memory.State, memory.Scope, memory.LifecycleStatus, memory.SupersededBy,
		} {
			if !addPreparedRecallRuntimeBytes(&total, uint64(len(value))) {
				return 0, domainconversation.ErrPreparedRecallCapacity
			}
		}
		for _, evidenceID := range memory.EvidenceEventIDs {
			if !addPreparedRecallRuntimeBytes(&total, uint64(24+len(evidenceID))) {
				return 0, domainconversation.ErrPreparedRecallCapacity
			}
		}
		if total > domainconversation.PreparedRecallMaxAggregateBytes {
			return 0, domainconversation.ErrPreparedRecallCapacity
		}
	}
	return total, nil
}

func addPreparedRecallRuntimeBytes(total *uint64, amount uint64) bool {
	if ^uint64(0)-*total < amount {
		return false
	}
	*total += amount
	return true
}
