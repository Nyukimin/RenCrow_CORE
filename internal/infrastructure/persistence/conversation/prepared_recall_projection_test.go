package conversation

import (
	"errors"
	"sync"
	"testing"
	"time"

	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

const (
	projectionTestOwner = "owner-1"
	projectionTestScope = "mio_only"
)

func projectionTestInput(version string) domainconversation.PreparedRecallViewInput {
	return domainconversation.PreparedRecallViewInput{
		AuthenticatedOwnerID: projectionTestOwner,
		UserScope:            projectionTestScope,
		SourceVersion:        version,
		MemoryBudget: domainconversation.PreparedRecallMemoryBudget{
			AggregateLimitBytes: domainconversation.PreparedRecallMaxAggregateBytes,
		},
		Coverage: domainconversation.PreparedRecallCoverage{
			UserMemory: domainconversation.PreparedRecallCoverageFull,
			Knowledge:  domainconversation.PreparedRecallCoverageFull,
		},
	}
}

func projectionTestEligibility(owner, scope, version string) (PreparedRecallEligibilityRelease, bool) {
	if owner != projectionTestOwner || scope != projectionTestScope || version == "" {
		return nil, false
	}
	return func() {}, true
}

func newProjectionTestOwner(t *testing.T, budget uint64) *PreparedRecallProjectionOwner {
	t.Helper()
	owner, err := NewPreparedRecallProjectionOwner(projectionTestOwner, projectionTestScope, budget)
	if err != nil {
		t.Fatalf("NewPreparedRecallProjectionOwner() error = %v", err)
	}
	return owner
}

func TestPreparedRecallProjectionBuildAcquireSelectAndScopeBinding(t *testing.T) {
	owner := newProjectionTestOwner(t, domainconversation.PreparedRecallMaxAggregateBytes)
	if status, err := owner.BuildAndPublish(func() domainconversation.PreparedRecallViewInput {
		input := projectionTestInput("wrong-owner")
		input.AuthenticatedOwnerID = "different-owner"
		return input
	}(), projectionTestEligibility); status != domainconversation.PreparedRecallPreparationDenied || !errors.Is(err, ErrPreparedRecallProjectionDenied) {
		t.Fatalf("mismatched owner BuildAndPublish() = (%q, %v), want denied", status, err)
	}
	if status, err := owner.BuildAndPublish(func() domainconversation.PreparedRecallViewInput {
		input := projectionTestInput("wrong-scope")
		input.UserScope = "different-scope"
		return input
	}(), projectionTestEligibility); status != domainconversation.PreparedRecallPreparationDenied || !errors.Is(err, ErrPreparedRecallProjectionDenied) {
		t.Fatalf("mismatched scope BuildAndPublish() = (%q, %v), want denied", status, err)
	}
	status, err := owner.BuildAndPublish(projectionTestInput("opaque-v1"), projectionTestEligibility)
	if err != nil || status != domainconversation.PreparedRecallPreparationReady {
		t.Fatalf("BuildAndPublish() = (%q, %v), want ready", status, err)
	}

	if lease, status, err := owner.Acquire("different-owner", projectionTestScope, "opaque-v1"); !errors.Is(err, ErrPreparedRecallProjectionDenied) || lease != nil || status != domainconversation.PreparedRecallPreparationDenied {
		t.Fatalf("mismatched owner Acquire() = (%v, %q, %v)", lease, status, err)
	}
	if lease, status, err := owner.Acquire(projectionTestOwner, "different-scope", "opaque-v1"); !errors.Is(err, ErrPreparedRecallProjectionDenied) || lease != nil || status != domainconversation.PreparedRecallPreparationDenied {
		t.Fatalf("mismatched scope Acquire() = (%v, %q, %v)", lease, status, err)
	}
	if lease, status, err := owner.Acquire(projectionTestOwner, projectionTestScope, "opaque-v2"); !errors.Is(err, ErrPreparedRecallProjectionStale) || lease != nil || status != domainconversation.PreparedRecallPreparationStale {
		t.Fatalf("mismatched version Acquire() = (%v, %q, %v)", lease, status, err)
	}

	lease, status, err := owner.Acquire(projectionTestOwner, projectionTestScope, "opaque-v1")
	if err != nil || status != domainconversation.PreparedRecallPreparationReady {
		t.Fatalf("Acquire() = (%v, %q, %v), want ready", lease, status, err)
	}
	defer lease.Release()
	selection, err := lease.Select(domainconversation.PreparedRecallSelectInput{
		AuthenticatedOwnerID:  projectionTestOwner,
		UserScope:             projectionTestScope,
		ExpectedSourceVersion: "opaque-v1",
		Persona:               "mio",
		Now:                   time.Now(),
	})
	if err != nil || selection.PreparationStatus != domainconversation.PreparedRecallPreparationReady || selection.SourceVersion != "opaque-v1" {
		t.Fatalf("Select() = (%+v, %v), want ready v1", selection, err)
	}
}

func TestPreparedRecallProjectionConcurrentBuildReservationsStayWithinBudget(t *testing.T) {
	budget := uint64(domainconversation.PreparedRecallMaxViewBytes) + (domainconversation.PreparedRecallMaxAggregateBytes - 2*uint64(domainconversation.PreparedRecallMaxViewBytes))
	owner := newProjectionTestOwner(t, budget)
	entered := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		status, err := owner.BuildAndPublish(projectionTestInput("v1"), func(owner, scope, version string) (PreparedRecallEligibilityRelease, bool) {
			close(entered)
			<-release
			return func() {}, true
		})
		if err == nil && status != domainconversation.PreparedRecallPreparationReady {
			err = errors.New("first build did not become ready")
		}
		firstDone <- err
	}()
	<-entered

	status, err := owner.BuildAndPublish(projectionTestInput("v2"), projectionTestEligibility)
	if status != domainconversation.PreparedRecallPreparationUnavailable || !errors.Is(err, domainconversation.ErrPreparedRecallCapacity) {
		t.Fatalf("overlapping BuildAndPublish() = (%q, %v), want unavailable capacity", status, err)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
}

func TestPreparedRecallProjectionReaderPinnedGenerationConsumesBudget(t *testing.T) {
	budget := uint64(domainconversation.PreparedRecallMaxViewBytes) + (domainconversation.PreparedRecallMaxAggregateBytes - 2*uint64(domainconversation.PreparedRecallMaxViewBytes))
	owner := newProjectionTestOwner(t, budget)
	if status, err := owner.BuildAndPublish(projectionTestInput("v1"), projectionTestEligibility); err != nil || status != domainconversation.PreparedRecallPreparationReady {
		t.Fatalf("initial build = (%q, %v), want ready", status, err)
	}
	lease, _, err := owner.Acquire(projectionTestOwner, projectionTestScope, "v1")
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Revoke(projectionTestOwner, projectionTestScope); err != nil {
		t.Fatal(err)
	}
	status, err := owner.BuildAndPublish(projectionTestInput("v2"), projectionTestEligibility)
	if status != domainconversation.PreparedRecallPreparationUnavailable || !errors.Is(err, domainconversation.ErrPreparedRecallCapacity) {
		t.Fatalf("build with revoked reader pin = (%q, %v), want unavailable capacity", status, err)
	}
	if _, err := lease.Select(domainconversation.PreparedRecallSelectInput{
		AuthenticatedOwnerID:  projectionTestOwner,
		UserScope:             projectionTestScope,
		ExpectedSourceVersion: "v1",
		Persona:               "mio",
		Now:                   time.Now(),
	}); !errors.Is(err, ErrPreparedRecallProjectionStale) {
		t.Fatalf("revoked lease Select() error = %v, want stale", err)
	}
	lease.Release()
	if status, err := owner.BuildAndPublish(projectionTestInput("v2"), projectionTestEligibility); err != nil || status != domainconversation.PreparedRecallPreparationReady {
		t.Fatalf("build after releasing old generation = (%q, %v), want ready", status, err)
	}
}

func TestPreparedRecallProjectionRevokeBeatsLateBuilder(t *testing.T) {
	owner := newProjectionTestOwner(t, domainconversation.PreparedRecallMaxAggregateBytes)
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct {
		status string
		err    error
	}, 1)
	go func() {
		status, err := owner.BuildAndPublish(projectionTestInput("late-v1"), func(owner, scope, version string) (PreparedRecallEligibilityRelease, bool) {
			close(entered)
			<-release
			return func() {}, true
		})
		done <- struct {
			status string
			err    error
		}{status, err}
	}()
	<-entered
	if err := owner.Revoke(projectionTestOwner, projectionTestScope); err != nil {
		t.Fatal(err)
	}
	close(release)
	result := <-done
	if result.status != domainconversation.PreparedRecallPreparationStale || !errors.Is(result.err, ErrPreparedRecallProjectionStale) {
		t.Fatalf("late build = (%q, %v), want stale", result.status, result.err)
	}
	if lease, status, err := owner.Acquire(projectionTestOwner, projectionTestScope, "late-v1"); lease != nil || status != domainconversation.PreparedRecallPreparationUnavailable || !errors.Is(err, ErrPreparedRecallProjectionUnavailable) {
		t.Fatalf("Acquire after revoke = (%v, %q, %v), want unavailable", lease, status, err)
	}
}

func TestPreparedRecallProjectionBuildDoesNotHoldReaderControlLock(t *testing.T) {
	owner := newProjectionTestOwner(t, domainconversation.PreparedRecallMaxAggregateBytes)
	if _, err := owner.BuildAndPublish(projectionTestInput("v1"), projectionTestEligibility); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := owner.BuildAndPublish(projectionTestInput("v2"), func(owner, scope, version string) (PreparedRecallEligibilityRelease, bool) {
			close(entered)
			<-release
			return func() {}, true
		})
		done <- err
	}()
	<-entered
	acquired := make(chan error, 1)
	go func() {
		lease, _, err := owner.Acquire(projectionTestOwner, projectionTestScope, "v1")
		if lease != nil {
			lease.Release()
		}
		acquired <- err
	}()
	select {
	case err := <-acquired:
		if err != nil {
			t.Fatalf("Acquire during blocked build: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Acquire blocked behind build eligibility callback")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPreparedRecallProjectionCandidateIsNotReadableDuringBlockedEligibility(t *testing.T) {
	owner := newProjectionTestOwner(t, domainconversation.PreparedRecallMaxAggregateBytes)
	if _, err := owner.BuildAndPublish(projectionTestInput("v1"), projectionTestEligibility); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct {
		status string
		err    error
	}, 1)
	go func() {
		status, err := owner.BuildAndPublish(projectionTestInput("candidate-v2"), func(owner, scope, version string) (PreparedRecallEligibilityRelease, bool) {
			close(entered)
			<-release
			return nil, false
		})
		done <- struct {
			status string
			err    error
		}{status, err}
	}()
	<-entered
	if lease, status, err := owner.Acquire(projectionTestOwner, projectionTestScope, "candidate-v2"); lease != nil || status != domainconversation.PreparedRecallPreparationStale || !errors.Is(err, ErrPreparedRecallProjectionStale) {
		t.Fatalf("Acquire candidate during eligibility = (%v, %q, %v), want old generation only", lease, status, err)
	}
	lease, status, err := owner.Acquire(projectionTestOwner, projectionTestScope, "v1")
	if err != nil || status != domainconversation.PreparedRecallPreparationReady {
		t.Fatalf("Acquire prior generation during eligibility = (%v, %q, %v), want ready", lease, status, err)
	}
	selection, err := lease.Select(domainconversation.PreparedRecallSelectInput{
		AuthenticatedOwnerID:  projectionTestOwner,
		UserScope:             projectionTestScope,
		ExpectedSourceVersion: "v1",
		Persona:               "mio",
		Now:                   time.Now(),
	})
	lease.Release()
	if err != nil || selection.PreparationStatus != domainconversation.PreparedRecallPreparationReady || selection.SourceVersion != "v1" {
		t.Fatalf("Select prior generation during eligibility = (%+v, %v), want ready v1", selection, err)
	}
	close(release)
	result := <-done
	if result.status != domainconversation.PreparedRecallPreparationStale || !errors.Is(result.err, ErrPreparedRecallProjectionStale) {
		t.Fatalf("rejected candidate = (%q, %v), want stale", result.status, result.err)
	}
}

func TestPreparedRecallProjectionEligibilityRejectsChangedOwnerVersion(t *testing.T) {
	owner := newProjectionTestOwner(t, domainconversation.PreparedRecallMaxAggregateBytes)
	currentVersion := "new-version"
	status, err := owner.BuildAndPublish(projectionTestInput("old-version"), func(owner, scope, version string) (PreparedRecallEligibilityRelease, bool) {
		if version != currentVersion {
			return nil, false
		}
		return func() {}, true
	})
	if status != domainconversation.PreparedRecallPreparationStale || !errors.Is(err, ErrPreparedRecallProjectionStale) {
		t.Fatalf("stale owner version build = (%q, %v), want stale", status, err)
	}
	if lease, _, err := owner.Acquire(projectionTestOwner, projectionTestScope, "old-version"); lease != nil || !errors.Is(err, ErrPreparedRecallProjectionUnavailable) {
		t.Fatalf("Acquire after stale build = (%v, %v), want unavailable", lease, err)
	}
}

func TestPreparedRecallProjectionFalseEligibilityRollsBackOnlyItsPublication(t *testing.T) {
	owner := newProjectionTestOwner(t, domainconversation.PreparedRecallMaxAggregateBytes)
	if status, err := owner.BuildAndPublish(projectionTestInput("existing-v1"), projectionTestEligibility); err != nil || status != domainconversation.PreparedRecallPreparationReady {
		t.Fatalf("initial build = (%q, %v), want ready", status, err)
	}
	status, err := owner.BuildAndPublish(projectionTestInput("rejected-v2"), func(owner, scope, version string) (PreparedRecallEligibilityRelease, bool) {
		return nil, false
	})
	if status != domainconversation.PreparedRecallPreparationStale || !errors.Is(err, ErrPreparedRecallProjectionStale) {
		t.Fatalf("false eligibility build = (%q, %v), want stale", status, err)
	}
	lease, status, err := owner.Acquire(projectionTestOwner, projectionTestScope, "existing-v1")
	if err != nil || status != domainconversation.PreparedRecallPreparationReady {
		t.Fatalf("Acquire restored generation = (%v, %q, %v), want existing v1", lease, status, err)
	}
	lease.Release()
	if lease, _, err := owner.Acquire(projectionTestOwner, projectionTestScope, "rejected-v2"); lease != nil || !errors.Is(err, ErrPreparedRecallProjectionStale) {
		t.Fatalf("Acquire rejected generation = (%v, %v), want stale", lease, err)
	}
}

func TestPreparedRecallProjectionEligibilityGuardCoversAtomicPublication(t *testing.T) {
	owner := newProjectionTestOwner(t, domainconversation.PreparedRecallMaxAggregateBytes)
	var sourceMu sync.Mutex
	currentVersion := "guarded-v1"
	guardHeld := make(chan struct{})
	publicationObserved := make(chan bool, 1)
	eligibility := func(ownerID, scope, version string) (PreparedRecallEligibilityRelease, bool) {
		sourceMu.Lock()
		if ownerID != projectionTestOwner || scope != projectionTestScope || version != currentVersion {
			sourceMu.Unlock()
			return nil, false
		}
		close(guardHeld)
		return func() {
			lease, status, err := owner.Acquire(ownerID, scope, version)
			publicationObserved <- err == nil && status == domainconversation.PreparedRecallPreparationReady && lease != nil
			if lease != nil {
				lease.Release()
			}
			sourceMu.Unlock()
		}, true
	}
	done := make(chan error, 1)
	go func() {
		status, err := owner.BuildAndPublish(projectionTestInput("guarded-v1"), eligibility)
		if err == nil && status != domainconversation.PreparedRecallPreparationReady {
			err = errors.New("guarded build did not become ready")
		}
		done <- err
	}()
	<-guardHeld
	mutationAttempting := make(chan struct{})
	mutationDone := make(chan struct{})
	go func() {
		close(mutationAttempting)
		sourceMu.Lock()
		currentVersion = "guarded-v2"
		_ = owner.Revoke(projectionTestOwner, projectionTestScope)
		sourceMu.Unlock()
		close(mutationDone)
	}()
	<-mutationAttempting
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !<-publicationObserved {
		t.Fatal("owner eligibility guard was released before the generation became current")
	}
	<-mutationDone
	if lease, status, err := owner.Acquire(projectionTestOwner, projectionTestScope, "guarded-v1"); lease != nil || status != domainconversation.PreparedRecallPreparationUnavailable || !errors.Is(err, ErrPreparedRecallProjectionUnavailable) {
		t.Fatalf("Acquire after guarded source mutation = (%v, %q, %v), want unavailable", lease, status, err)
	}
}
