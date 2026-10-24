package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	idlechat "github.com/Nyukimin/RenCrow_CORE/internal/application/idlechat"
)

type fakeSurfacePresenceRuntime struct {
	mu             sync.Mutex
	manual         bool
	active         bool
	disabled       bool
	starts         int
	stops          int
	startErr       error
	stopErr        error
	policyRevision uint64
}

type snapshotSurfacePresenceRuntime struct {
	*fakeSurfacePresenceRuntime
	snapshotMu sync.Mutex
	snapshot   idlechat.PlaybackControlSnapshot
}

func (r *snapshotSurfacePresenceRuntime) PlaybackControlSnapshot() idlechat.PlaybackControlSnapshot {
	r.snapshotMu.Lock()
	defer r.snapshotMu.Unlock()
	return r.snapshot
}

func (r *snapshotSurfacePresenceRuntime) setPlaybackControlSnapshot(snapshot idlechat.PlaybackControlSnapshot) {
	r.snapshotMu.Lock()
	r.snapshot = snapshot
	r.snapshotMu.Unlock()
}

func (f *fakeSurfacePresenceRuntime) StartManualMode() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return f.startErr
	}
	f.starts++
	f.manual = true
	f.disabled = false
	return nil
}

func (f *fakeSurfacePresenceRuntime) StopManualMode() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
	f.manual = false
	f.active = false
	f.disabled = true
}

func (f *fakeSurfacePresenceRuntime) StopPlaybackForForeground(_ context.Context, req idlechat.PlaybackStopRequest) (idlechat.PlaybackStopReceipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stopErr != nil {
		return idlechat.PlaybackStopReceipt{}, f.stopErr
	}
	if req.StateRevision < f.policyRevision {
		return idlechat.PlaybackStopReceipt{}, fmt.Errorf("stop revision %d is older than %d", req.StateRevision, f.policyRevision)
	}
	f.policyRevision = req.StateRevision
	wasActive := f.manual || f.active
	f.stops++
	f.manual = false
	f.active = false
	f.disabled = true
	return idlechat.PlaybackStopReceipt{
		RuntimeEpoch:         req.RuntimeEpoch,
		AppliedStateRevision: req.StateRevision,
		Generation:           uint64(f.stops),
		PlaybackStopped:      wasActive,
		ResumeBlocked:        true,
		CleanupPending:       true,
	}, nil
}

func TestSurfacePresenceIdleClaimDoesNotStartAndForegroundClaimQuickStops(t *testing.T) {
	runtime := &fakeSurfacePresenceRuntime{disabled: true}
	controller := newSurfacePresenceController(runtime, time.Minute)
	t.Cleanup(controller.Close)

	idleIdentity := surfacePresenceIdentity{principal: "local", clientType: "RenCrow_PORTAL", viewerClientID: "idle-tab"}
	idleRegistration, err := controller.RegisterV2(idleIdentity)
	if err != nil {
		t.Fatal(err)
	}
	idleClaim := surfacePresenceV2Request{ProtocolVersion: 2, ViewerClientID: idleIdentity.viewerClientID, RuntimeEpoch: idleRegistration.RuntimeEpoch, RegistrationReceipt: idleRegistration.RegistrationReceipt, Surface: "idlechat", Action: "claim", UpdateSeq: 1}
	idleSnapshot, err := controller.UpdateV2(idleIdentity, idleClaim)
	if err != nil {
		t.Fatal(err)
	}
	if !idleSnapshot.PlaybackAllowed || idleSnapshot.IdleChatActive {
		t.Fatalf("IdleChat eligibility snapshot = %+v", idleSnapshot)
	}
	if starts, _ := runtime.counts(); starts != 0 {
		t.Fatalf("IdleChat claim started manual mode %d times", starts)
	}

	cmdIdentity := surfacePresenceIdentity{principal: "local", clientType: "RenCrow_CMD", viewerClientID: "cmd-instance"}
	cmdRegistration, err := controller.RegisterV2(cmdIdentity)
	if err != nil {
		t.Fatal(err)
	}
	cmdClaim := surfacePresenceV2Request{ProtocolVersion: 2, ViewerClientID: cmdIdentity.viewerClientID, RuntimeEpoch: cmdRegistration.RuntimeEpoch, RegistrationReceipt: cmdRegistration.RegistrationReceipt, Surface: "cmd", Action: "claim", UpdateSeq: 1}
	cmdDone := make(chan error, 1)
	go func() {
		_, updateErr := controller.UpdateV2(cmdIdentity, cmdClaim)
		cmdDone <- updateErr
	}()
	select {
	case cmdErr := <-cmdDone:
		if cmdErr != nil {
			t.Fatalf("foreground claim failed: %v", cmdErr)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("foreground claim did not complete its quick-stop promptly")
	}
	if manual := runtime.IsManualMode(); manual {
		t.Fatal("foreground claim left manual mode active")
	}
	if snapshot := controller.snapshotForRegistration(cmdIdentity); snapshot.EffectiveMode != "chat" || snapshot.PlaybackAllowed || !snapshot.ForegroundReady {
		t.Fatalf("foreground snapshot after quick-stop=%+v", snapshot)
	}
}

func TestSurfacePresenceForegroundClaimBetweenPlaybackGuardAndMarkInvalidatesActivation(t *testing.T) {
	runtime := &fakeSurfacePresenceRuntime{disabled: true}
	controller := newSurfacePresenceController(runtime, time.Minute)
	t.Cleanup(controller.Close)

	idleIdentity := surfacePresenceIdentity{principal: "local", clientType: "RenCrow_PORTAL", viewerClientID: "idle-tab"}
	registration, err := controller.RegisterV2(idleIdentity)
	if err != nil {
		t.Fatal(err)
	}
	idleClaim := surfacePresenceV2Request{
		ProtocolVersion: 2, ViewerClientID: idleIdentity.viewerClientID,
		RuntimeEpoch: registration.RuntimeEpoch, RegistrationReceipt: registration.RegistrationReceipt,
		Surface: "idlechat", Action: "claim", UpdateSeq: 1,
	}
	ready, err := controller.UpdateV2(idleIdentity, idleClaim)
	if err != nil {
		t.Fatal(err)
	}
	guardChecked := make(chan error, 1)
	allowPostLaunchMark := make(chan struct{})
	markDone := make(chan error, 1)
	go func() {
		_, guardErr := controller.ValidatePlayback(idleIdentity, registration.RuntimeEpoch, registration.RegistrationReceipt, 1, ready.StateRevision)
		guardChecked <- guardErr
		<-allowPostLaunchMark
		markDone <- controller.MarkPlaybackStarted(idleIdentity, registration.RuntimeEpoch, registration.RegistrationReceipt, 1, ready.StateRevision)
	}()
	if err := <-guardChecked; err != nil {
		t.Fatalf("pre-activation guard rejected current policy: %v", err)
	}

	cmdIdentity := surfacePresenceIdentity{principal: "local", clientType: "RenCrow_CMD", viewerClientID: "cmd-instance"}
	cmdRegistration, err := controller.RegisterV2(cmdIdentity)
	if err != nil {
		t.Fatal(err)
	}
	cmdClaim := surfacePresenceV2Request{
		ProtocolVersion: 2, ViewerClientID: cmdIdentity.viewerClientID,
		RuntimeEpoch: cmdRegistration.RuntimeEpoch, RegistrationReceipt: cmdRegistration.RegistrationReceipt,
		Surface: "cmd", Action: "claim", UpdateSeq: 1,
	}
	if _, err := controller.UpdateV2(cmdIdentity, cmdClaim); err != nil {
		t.Fatalf("foreground claim in guard/activation window failed: %v", err)
	}
	close(allowPostLaunchMark)
	if err := <-markDone; !errors.Is(err, errPresencePlaybackChanged) {
		t.Fatalf("post-launch playback mark error=%v, want stale policy rejection", err)
	}
	if snapshot := controller.snapshotForRegistration(idleIdentity); snapshot.PlaybackActive || snapshot.PlaybackAllowed {
		t.Fatalf("foreground claim allowed stale activation: %+v", snapshot)
	}
	if runtime.IsManualMode() {
		t.Fatal("foreground claim left IdleChat playback active")
	}
}

func (f *fakeSurfacePresenceRuntime) IsManualMode() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.manual
}

func (f *fakeSurfacePresenceRuntime) IsChatActive() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active
}

func (f *fakeSurfacePresenceRuntime) IsDisabled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.disabled
}

func (f *fakeSurfacePresenceRuntime) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts, f.stops
}

type gatedSurfacePresenceRuntime struct {
	base                  *fakeSurfacePresenceRuntime
	blockNextActivityRead atomic.Bool
	activityReadStarted   chan struct{}
	releaseActivityRead   chan struct{}
}

func (r *gatedSurfacePresenceRuntime) StopPlaybackForForeground(ctx context.Context, request idlechat.PlaybackStopRequest) (idlechat.PlaybackStopReceipt, error) {
	return r.base.StopPlaybackForForeground(ctx, request)
}

func (r *gatedSurfacePresenceRuntime) IsChatActive() bool {
	if r.blockNextActivityRead.CompareAndSwap(true, false) {
		close(r.activityReadStarted)
		<-r.releaseActivityRead
	}
	return r.base.IsChatActive()
}

func (r *gatedSurfacePresenceRuntime) IsManualMode() bool {
	return r.base.IsManualMode()
}

func TestSurfacePresenceRuntimeProjectionDoesNotMixActivityAcrossPolicyRevisions(t *testing.T) {
	base := &fakeSurfacePresenceRuntime{active: true}
	runtime := &gatedSurfacePresenceRuntime{
		base:                base,
		activityReadStarted: make(chan struct{}),
		releaseActivityRead: make(chan struct{}),
	}
	controller := newSurfacePresenceController(runtime, time.Minute)
	t.Cleanup(controller.Close)

	idleIdentity := surfacePresenceIdentity{principal: "local", clientType: "RenCrow_PORTAL", viewerClientID: "idle-tab"}
	idleRegistration, err := controller.RegisterV2(idleIdentity)
	if err != nil {
		t.Fatal(err)
	}
	idleClaim := surfacePresenceV2Request{
		ProtocolVersion: 2, ViewerClientID: idleIdentity.viewerClientID,
		RuntimeEpoch: idleRegistration.RuntimeEpoch, RegistrationReceipt: idleRegistration.RegistrationReceipt,
		Surface: "idlechat", Action: "claim", UpdateSeq: 1,
	}
	if _, err := controller.UpdateV2(idleIdentity, idleClaim); err != nil {
		t.Fatal(err)
	}

	runtime.blockNextActivityRead.Store(true)
	oldResponseDone := make(chan surfacePresenceV2Response, 1)
	go func() {
		oldResponseDone <- controller.snapshotForRegistration(idleIdentity)
	}()
	select {
	case <-runtime.activityReadStarted:
	case <-time.After(time.Second):
		t.Fatal("old response did not enter the gated runtime activity read")
	}

	cmdIdentity := surfacePresenceIdentity{principal: "local", clientType: "RenCrow_CMD", viewerClientID: "cmd-instance"}
	cmdRegistration, err := controller.RegisterV2(cmdIdentity)
	if err != nil {
		t.Fatal(err)
	}
	cmdClaim := surfacePresenceV2Request{
		ProtocolVersion: 2, ViewerClientID: cmdIdentity.viewerClientID,
		RuntimeEpoch: cmdRegistration.RuntimeEpoch, RegistrationReceipt: cmdRegistration.RegistrationReceipt,
		Surface: "cmd", Action: "claim", UpdateSeq: 1,
	}
	newResponseDone := make(chan surfacePresenceV2Response, 1)
	newErrorDone := make(chan error, 1)
	go func() {
		response, updateErr := controller.UpdateV2(cmdIdentity, cmdClaim)
		newResponseDone <- response
		newErrorDone <- updateErr
	}()
	var newResponse surfacePresenceV2Response
	select {
	case newResponse = <-newResponseDone:
	case <-time.After(time.Second):
		t.Fatal("new foreground policy waited on the blocked projection")
	}
	if err := <-newErrorDone; err != nil {
		t.Fatalf("foreground claim failed: %v", err)
	}
	if newResponse.PlaybackActive || newResponse.IdleChatActive {
		t.Fatalf("new policy revision projected stale active runtime state: %+v", newResponse)
	}

	close(runtime.releaseActivityRead)
	oldResponse := <-oldResponseDone
	if oldResponse.StateRevision >= newResponse.StateRevision {
		t.Fatalf("old response revision=%d, current response revision=%d", oldResponse.StateRevision, newResponse.StateRevision)
	}
	if oldResponse.EffectiveMode != "idlechat" || !oldResponse.PlaybackAllowed || oldResponse.ForegroundReady {
		t.Fatalf("old response mixed in the newer foreground policy: %+v", oldResponse)
	}
	if current := controller.snapshotForRegistration(idleIdentity); current.StateRevision != newResponse.StateRevision || current.PlaybackActive || current.PlaybackAllowed {
		t.Fatalf("current policy snapshot does not match its revision: %+v", current)
	}
}

func TestSurfacePresenceControlSnapshotUsesPolicyRevisionForPlaybackAndAdvancesForCleanup(t *testing.T) {
	runtime := &snapshotSurfacePresenceRuntime{
		fakeSurfacePresenceRuntime: &fakeSurfacePresenceRuntime{disabled: true},
		snapshot: idlechat.PlaybackControlSnapshot{
			GenerationState:      "quiesced",
			GenerationQuiesced:   true,
			BackendResourceState: "released",
		},
	}
	controller := newSurfacePresenceController(runtime, time.Minute)
	t.Cleanup(controller.Close)
	runtime.setPlaybackControlSnapshot(idlechat.PlaybackControlSnapshot{
		RuntimeEpoch:         controller.runtimeEpochValue(),
		GenerationState:      "quiesced",
		GenerationQuiesced:   true,
		BackendResourceState: "released",
	})
	initial, ok := controller.idleChatControlSnapshot()
	if !ok {
		t.Fatal("initial control snapshot was unavailable")
	}

	runtime.setPlaybackControlSnapshot(idlechat.PlaybackControlSnapshot{
		RuntimeEpoch:         initial.RuntimeEpoch,
		PlaybackGeneration:   4,
		PlaybackActive:       true,
		GenerationState:      "running",
		GenerationQuiesced:   false,
		BackendResourceState: "released",
	})
	controller.publishControlSnapshotChanged()
	playing, ok := controller.idleChatControlSnapshot()
	if !ok || playing.StateRevision != initial.StateRevision || !playing.PlaybackActive || playing.PlaybackGeneration != 4 {
		t.Fatalf("playing control snapshot = %+v, initial = %+v", playing, initial)
	}

	runtime.setPlaybackControlSnapshot(idlechat.PlaybackControlSnapshot{
		RuntimeEpoch:         playing.RuntimeEpoch,
		PlaybackGeneration:   4,
		CleanupPending:       true,
		GenerationState:      "quiesced",
		GenerationQuiesced:   true,
		BackendResourceState: "released",
	})
	controller.publishControlSnapshotChanged()
	cleanupPending, ok := controller.idleChatControlSnapshot()
	if !ok || cleanupPending.StateRevision <= playing.StateRevision || cleanupPending.PlaybackActive || !cleanupPending.CleanupPending {
		t.Fatalf("cleanup-pending snapshot = %+v, playing = %+v", cleanupPending, playing)
	}

	runtime.setPlaybackControlSnapshot(idlechat.PlaybackControlSnapshot{
		RuntimeEpoch:         cleanupPending.RuntimeEpoch,
		PlaybackGeneration:   4,
		GenerationState:      "quiesced",
		GenerationQuiesced:   true,
		BackendResourceState: "released",
	})
	controller.publishControlSnapshotChanged()
	cleaned, ok := controller.idleChatControlSnapshot()
	if !ok || cleaned.StateRevision <= cleanupPending.StateRevision || cleaned.CleanupPending {
		t.Fatalf("cleanup-complete snapshot = %+v, previous = %+v", cleaned, cleanupPending)
	}
	if cleaned.BackendResourceState != "unknown" {
		t.Fatalf("backend state = %q, want unknown without direct observation", cleaned.BackendResourceState)
	}
}

func TestSurfacePresenceCleanupCompletionKeepsAppliedForegroundPolicy(t *testing.T) {
	runtime := &snapshotSurfacePresenceRuntime{
		fakeSurfacePresenceRuntime: &fakeSurfacePresenceRuntime{disabled: true},
		snapshot: idlechat.PlaybackControlSnapshot{
			CleanupPending:       true,
			GenerationState:      "quiesced",
			GenerationQuiesced:   true,
			BackendResourceState: "unknown",
		},
	}
	controller := newSurfacePresenceController(runtime, time.Minute)
	t.Cleanup(controller.Close)
	runtime.setPlaybackControlSnapshot(idlechat.PlaybackControlSnapshot{
		RuntimeEpoch:         controller.runtimeEpochValue(),
		CleanupPending:       true,
		GenerationState:      "quiesced",
		GenerationQuiesced:   true,
		BackendResourceState: "unknown",
	})
	if _, ok := controller.idleChatControlSnapshot(); !ok {
		t.Fatal("initial control snapshot was unavailable")
	}

	identity := surfacePresenceIdentity{principal: "local", clientType: "RenCrow_PORTAL", viewerClientID: "chat-tab"}
	registration, err := controller.RegisterV2(identity)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := controller.UpdateV2(identity, surfacePresenceV2Request{
		ProtocolVersion: 2, ViewerClientID: identity.viewerClientID,
		RuntimeEpoch: registration.RuntimeEpoch, RegistrationReceipt: registration.RegistrationReceipt,
		Surface: "chat", Action: "claim", UpdateSeq: 1,
	})
	if err != nil || !claimed.ForegroundReady {
		t.Fatalf("foreground claim response=%+v err=%v", claimed, err)
	}

	// Cleanup completion changes owner activity while the foreground stop policy
	// remains in force. It must advance the revision without requiring a new
	// policy application or reopening the playback gate.
	runtime.setPlaybackControlSnapshot(idlechat.PlaybackControlSnapshot{
		RuntimeEpoch:         claimed.RuntimeEpoch,
		GenerationState:      "quiesced",
		GenerationQuiesced:   true,
		BackendResourceState: "unknown",
	})
	controller.publishControlSnapshotChanged()
	cleaned := controller.snapshotForRegistration(identity)
	controller.mu.Lock()
	appliedRevision := controller.appliedRevision
	controller.mu.Unlock()
	if cleaned.StateRevision <= claimed.StateRevision || appliedRevision != cleaned.StateRevision || !cleaned.ForegroundReady {
		t.Fatalf("cleanup completion invalidated an applied foreground policy: claimed=%+v cleaned=%+v", claimed, cleaned)
	}
	pin, err := controller.PinForeground(identity, registration.RuntimeEpoch, registration.RegistrationReceipt, claimed.CurrentClaimSeq)
	if err != nil {
		t.Fatalf("first foreground send pin after cleanup completion failed: %v", err)
	}
	if err := pin.Release(); err != nil {
		t.Fatalf("release foreground send pin: %v", err)
	}

	controller.mu.Lock()
	oldPlan := controller.policyPlanLocked(controller.now())
	controller.mu.Unlock()
	released, err := controller.UpdateV2(identity, surfacePresenceV2Request{
		ProtocolVersion: 2, ViewerClientID: identity.viewerClientID,
		RuntimeEpoch: registration.RuntimeEpoch, RegistrationReceipt: registration.RegistrationReceipt,
		Surface: "chat", Action: "release", UpdateSeq: 2, ClaimSeq: claimed.CurrentClaimSeq,
	})
	if err != nil {
		t.Fatal(err)
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	currentPlan := controller.policyPlanLocked(controller.now())
	if currentPlan.revision <= oldPlan.revision || controller.policyPlanMatchesLocked(oldPlan) || released.ForegroundReady {
		t.Fatalf("real foreground release did not fence the old policy: old=%+v current=%+v response=%+v", oldPlan, currentPlan, released)
	}
}

func TestSurfacePresencePlaybackPreparationDoesNotInvalidateItsAdmissionRevision(t *testing.T) {
	runtime := &snapshotSurfacePresenceRuntime{
		fakeSurfacePresenceRuntime: &fakeSurfacePresenceRuntime{disabled: true},
		snapshot:                   idlechat.PlaybackControlSnapshot{GenerationState: "quiesced", GenerationQuiesced: true, BackendResourceState: "unknown"},
	}
	controller := newSurfacePresenceController(runtime, time.Minute)
	t.Cleanup(controller.Close)
	runtime.setPlaybackControlSnapshot(idlechat.PlaybackControlSnapshot{
		RuntimeEpoch: controller.runtimeEpochValue(), GenerationState: "quiesced", GenerationQuiesced: true, BackendResourceState: "unknown",
	})
	identity := surfacePresenceIdentity{principal: "local", clientType: "RenCrow_PORTAL", viewerClientID: "idle-tab"}
	registration, err := controller.RegisterV2(identity)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := controller.UpdateV2(identity, surfacePresenceV2Request{
		ProtocolVersion: 2, ViewerClientID: identity.viewerClientID,
		RuntimeEpoch: registration.RuntimeEpoch, RegistrationReceipt: registration.RegistrationReceipt,
		Surface: "idlechat", Action: "claim", UpdateSeq: 1,
	})
	if err != nil || !ready.PlaybackAllowed {
		t.Fatalf("ready policy response=%+v err=%v", ready, err)
	}

	// Generation work admission and startIdleRun are owner-local preparation
	// updates. They must refresh the control payload without changing the policy
	// revision that the in-flight playback request is validating.
	runtime.setPlaybackControlSnapshot(idlechat.PlaybackControlSnapshot{
		RuntimeEpoch: ready.RuntimeEpoch, PlaybackGeneration: 4,
		GenerationState: "running", GenerationQuiesced: false, BackendResourceState: "unknown",
	})
	controller.publishControlSnapshotChanged()
	runtime.setPlaybackControlSnapshot(idlechat.PlaybackControlSnapshot{
		RuntimeEpoch: ready.RuntimeEpoch, PlaybackGeneration: 4, PlaybackActive: true,
		GenerationState: "running", GenerationQuiesced: false, BackendResourceState: "unknown",
	})
	controller.publishControlSnapshotChanged()

	prepared, ok := controller.idleChatControlSnapshot()
	if !ok || prepared.StateRevision != ready.StateRevision || !prepared.PlaybackActive || prepared.PlaybackGeneration != 4 {
		t.Fatalf("prepared playback control state=%+v ready=%+v", prepared, ready)
	}
	if _, err := controller.ValidatePlayback(identity, registration.RuntimeEpoch, registration.RegistrationReceipt, 1, ready.StateRevision); err != nil {
		t.Fatalf("owner-local playback preparation invalidated expected revision: %v", err)
	}
	if err := controller.MarkPlaybackStarted(identity, registration.RuntimeEpoch, registration.RegistrationReceipt, 1, ready.StateRevision); err != nil {
		t.Fatalf("successful playback mark rejected prepared revision: %v", err)
	}
}

func TestSurfacePresenceRepeatedForegroundPinsDoNotInvalidateAppliedPolicy(t *testing.T) {
	runtime := &fakeSurfacePresenceRuntime{disabled: true}
	controller := newSurfacePresenceController(runtime, time.Minute)
	t.Cleanup(controller.Close)
	identity := surfacePresenceIdentity{principal: "local", clientType: "RenCrow_PORTAL", viewerClientID: "chat-tab"}
	registration, err := controller.RegisterV2(identity)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := controller.UpdateV2(identity, surfacePresenceV2Request{
		ProtocolVersion: 2, ViewerClientID: identity.viewerClientID,
		RuntimeEpoch: registration.RuntimeEpoch, RegistrationReceipt: registration.RegistrationReceipt,
		Surface: "chat", Action: "claim", UpdateSeq: 1,
	})
	if err != nil || claimed.PlaybackAllowed || !claimed.ForegroundReady {
		t.Fatalf("foreground claim response=%+v err=%v", claimed, err)
	}
	before := controller.snapshotForRegistration(identity).StateRevision
	pin, err := controller.PinForeground(identity, registration.RuntimeEpoch, registration.RegistrationReceipt, 1)
	if err != nil {
		t.Fatalf("first send pin failed: %v", err)
	}
	if got := controller.snapshotForRegistration(identity).StateRevision; got != before {
		t.Fatalf("first send pin advanced policy revision from %d to %d", before, got)
	}
	if err := pin.Release(); err != nil {
		t.Fatalf("first send pin release failed: %v", err)
	}
	second, err := controller.PinForeground(identity, registration.RuntimeEpoch, registration.RegistrationReceipt, 1)
	if err != nil {
		t.Fatalf("second send pin failed after prior send: %v", err)
	}
	if got := controller.snapshotForRegistration(identity).StateRevision; got != before {
		t.Fatalf("repeated send pin advanced policy revision from %d to %d", before, got)
	}
	if err := second.Release(); err != nil {
		t.Fatalf("second send pin release failed: %v", err)
	}
}

func TestSurfacePresencePolicyPlanRejectsOldRevisionAfterPolicyABA(t *testing.T) {
	runtime := &fakeSurfacePresenceRuntime{disabled: true}
	controller := newSurfacePresenceController(runtime, time.Minute)
	t.Cleanup(controller.Close)

	idleIdentity := surfacePresenceIdentity{principal: "local", clientType: "RenCrow_PORTAL", viewerClientID: "idle-tab"}
	idleRegistration, err := controller.RegisterV2(idleIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.UpdateV2(idleIdentity, surfacePresenceV2Request{
		ProtocolVersion: 2, ViewerClientID: idleIdentity.viewerClientID,
		RuntimeEpoch: idleRegistration.RuntimeEpoch, RegistrationReceipt: idleRegistration.RegistrationReceipt,
		Surface: "idlechat", Action: "claim", UpdateSeq: 1,
	}); err != nil {
		t.Fatal(err)
	}

	otherIdentity := surfacePresenceIdentity{principal: "local", clientType: "RenCrow_CMD", viewerClientID: "cmd-instance"}
	otherRegistration, err := controller.RegisterV2(otherIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.UpdateV2(otherIdentity, surfacePresenceV2Request{
		ProtocolVersion: 2, ViewerClientID: otherIdentity.viewerClientID,
		RuntimeEpoch: otherRegistration.RuntimeEpoch, RegistrationReceipt: otherRegistration.RegistrationReceipt,
		Surface: "chat", Action: "claim", UpdateSeq: 1,
	}); err != nil {
		t.Fatal(err)
	}
	controller.mu.Lock()
	oldPlan := controller.policyPlanLocked(controller.now())
	controller.mu.Unlock()
	if oldPlan.target != "foreground" {
		t.Fatalf("initial policy target=%q, want foreground", oldPlan.target)
	}
	if _, err := controller.UpdateV2(otherIdentity, surfacePresenceV2Request{
		ProtocolVersion: 2, ViewerClientID: otherIdentity.viewerClientID,
		RuntimeEpoch: otherRegistration.RuntimeEpoch, RegistrationReceipt: otherRegistration.RegistrationReceipt,
		Surface: "chat", Action: "release", UpdateSeq: 2, ClaimSeq: 1,
	}); err != nil {
		t.Fatal(err)
	}
	otherRegistration, err = controller.RegisterV2(otherIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.UpdateV2(otherIdentity, surfacePresenceV2Request{
		ProtocolVersion: 2, ViewerClientID: otherIdentity.viewerClientID,
		RuntimeEpoch: otherRegistration.RuntimeEpoch, RegistrationReceipt: otherRegistration.RegistrationReceipt,
		Surface: "chat", Action: "claim", UpdateSeq: 3,
	}); err != nil {
		t.Fatal(err)
	}

	controller.mu.Lock()
	defer controller.mu.Unlock()
	currentPlan := controller.policyPlanLocked(controller.now())
	if currentPlan.target != oldPlan.target || currentPlan.idleVisible != oldPlan.idleVisible || currentPlan.foreground != oldPlan.foreground {
		t.Fatalf("test did not return to the old policy: old=%+v current=%+v", oldPlan, currentPlan)
	}
	if currentPlan.revision == oldPlan.revision {
		t.Fatalf("policy cycle did not advance state revision: old=%+v current=%+v", oldPlan, currentPlan)
	}
	if controller.policyPlanMatchesLocked(oldPlan) {
		t.Fatal("old stop receipt still matches after a Chat → IdleChat → Chat policy revision cycle")
	}
}

func TestSurfacePresenceV1IdleDoesNotStartPreparedPlayback(t *testing.T) {
	runtime := &fakeSurfacePresenceRuntime{disabled: true}
	controller := newSurfacePresenceController(runtime, time.Minute)
	t.Cleanup(controller.Close)

	idleSnapshot, err := controller.Update("idle-tab", "idlechat", "claim")
	if err != nil {
		t.Fatal(err)
	}
	if idleSnapshot.EffectiveMode != "none" || idleSnapshot.IdleChatActive || idleSnapshot.PlaybackAllowed || idleSnapshot.IdleChatPresenceCount != 1 {
		t.Fatalf("idle snapshot = %+v", idleSnapshot)
	}
	if err := controller.StartExplicit(); !errors.Is(err, errPresenceRegistrationRequired) {
		t.Fatalf("legacy V1 IdleChat start error=%v, want registration_required", err)
	}
	if _, err := controller.Update("idle-tab", "idlechat", "heartbeat"); err != nil {
		t.Fatal(err)
	}
	if starts, _ := runtime.counts(); starts != 0 {
		t.Fatalf("V1 IdleChat claim/heartbeat started runtime %d times", starts)
	}

	chatSnapshot, err := controller.Update("chat-tab", "chat", "claim")
	if err != nil {
		t.Fatal(err)
	}
	if chatSnapshot.EffectiveMode != "chat" || chatSnapshot.IdleChatActive || chatSnapshot.PlaybackAllowed || chatSnapshot.ChatPresenceCount != 1 || chatSnapshot.IdleChatPresenceCount != 1 {
		t.Fatalf("chat snapshot = %+v", chatSnapshot)
	}
	resumed, err := controller.Update("chat-tab", "chat", "release")
	if err != nil {
		t.Fatal(err)
	}
	if resumed.EffectiveMode != "none" || resumed.IdleChatActive || resumed.PlaybackAllowed {
		t.Fatalf("resumed snapshot = %+v", resumed)
	}

	stopped, err := controller.Update("idle-tab", "idlechat", "release")
	if err != nil {
		t.Fatal(err)
	}
	if stopped.EffectiveMode != "none" || stopped.IdleChatActive {
		t.Fatalf("stopped snapshot = %+v", stopped)
	}
	starts, stops := runtime.counts()
	if starts != 0 || stops != 1 {
		t.Fatalf("runtime starts=%d stops=%d, want 0/1", starts, stops)
	}
}

func TestSurfacePresenceV1IdleHeartbeatDoesNotStartRuntime(t *testing.T) {
	runtime := &fakeSurfacePresenceRuntime{disabled: true}
	controller := newSurfacePresenceController(runtime, time.Minute)
	t.Cleanup(controller.Close)

	if _, err := controller.Update("idle-tab", "idlechat", "claim"); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Update("idle-tab", "idlechat", "heartbeat"); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Update("idle-tab", "idlechat", "heartbeat"); err != nil {
		t.Fatal(err)
	}
	starts, _ := runtime.counts()
	if starts != 0 {
		t.Fatalf("V1 IdleChat heartbeat started runtime %d times", starts)
	}
}

func TestSurfacePresenceLeaseExpiryStopsPreparedPlayback(t *testing.T) {
	runtime := &fakeSurfacePresenceRuntime{disabled: true}
	controller := newSurfacePresenceController(runtime, 20*time.Millisecond)
	t.Cleanup(controller.Close)
	identity := surfacePresenceIdentity{principal: "local", clientType: "RenCrow_PORTAL", viewerClientID: "idle-tab"}
	registration, err := controller.RegisterV2(identity)
	if err != nil {
		t.Fatal(err)
	}
	request := surfacePresenceV2Request{ProtocolVersion: 2, ViewerClientID: identity.viewerClientID, RuntimeEpoch: registration.RuntimeEpoch, RegistrationReceipt: registration.RegistrationReceipt, Surface: "idlechat", Action: "claim", UpdateSeq: 1}
	ready, err := controller.UpdateV2(identity, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.MarkPlaybackStarted(identity, registration.RuntimeEpoch, registration.RegistrationReceipt, 1, ready.StateRevision); err != nil {
		t.Fatalf("mark explicit prepared playback active: %v", err)
	}
	runtime.mu.Lock()
	runtime.active = true
	runtime.disabled = false
	runtime.mu.Unlock()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		_, stops := runtime.counts()
		if stops >= 1 && !runtime.IsChatActive() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("expired IdleChat surface lease did not stop prepared playback")
}

func TestSurfacePresenceProjectsNaturalPlaybackCompletionFromRuntime(t *testing.T) {
	runtime := &fakeSurfacePresenceRuntime{disabled: true}
	controller := newSurfacePresenceController(runtime, time.Minute)
	t.Cleanup(controller.Close)
	identity := surfacePresenceIdentity{principal: "local", clientType: "RenCrow_PORTAL", viewerClientID: "idle-tab"}
	registration, err := controller.RegisterV2(identity)
	if err != nil {
		t.Fatal(err)
	}
	claim := surfacePresenceV2Request{
		ProtocolVersion: 2, ViewerClientID: identity.viewerClientID,
		RuntimeEpoch: registration.RuntimeEpoch, RegistrationReceipt: registration.RegistrationReceipt,
		Surface: "idlechat", Action: "claim", UpdateSeq: 1,
	}
	ready, err := controller.UpdateV2(identity, claim)
	if err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	runtime.active = true
	runtime.disabled = false
	runtime.mu.Unlock()
	if err := controller.MarkPlaybackStarted(identity, registration.RuntimeEpoch, registration.RegistrationReceipt, 1, ready.StateRevision); err != nil {
		t.Fatal(err)
	}
	playing := controller.snapshotForRegistration(identity)
	if !playing.PlaybackActive || !playing.IdleChatActive || playing.PlaybackState != "playing" {
		t.Fatalf("active playback projection=%+v", playing)
	}

	// Model the orchestrator naturally finishing without a presence transition.
	runtime.mu.Lock()
	runtime.active = false
	runtime.manual = false
	runtime.disabled = true
	runtime.mu.Unlock()
	heartbeat := claim
	heartbeat.Action = "heartbeat"
	heartbeat.UpdateSeq = 2
	heartbeat.ClaimSeq = 1
	stopped, err := controller.UpdateV2(identity, heartbeat)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.PlaybackActive || stopped.IdleChatActive || stopped.PlaybackState != "stopped" || !stopped.PlaybackAllowed {
		t.Fatalf("natural completion projection=%+v", stopped)
	}
	controller.mu.Lock()
	cachedActive := controller.runtimeActive
	controller.mu.Unlock()
	if !cachedActive {
		t.Fatal("runtimeActive stop-needed cache should remain conservative until a confirmed policy stop")
	}
}

func TestSurfacePresenceBlocksExplicitStartWhileChatIsVisible(t *testing.T) {
	runtime := &fakeSurfacePresenceRuntime{disabled: true}
	controller := newSurfacePresenceController(runtime, time.Minute)
	t.Cleanup(controller.Close)
	if _, err := controller.Update("chat-tab", "chat", "claim"); err != nil {
		t.Fatal(err)
	}
	if err := controller.StartExplicit(); !errors.Is(err, errChatSurfacePresent) {
		t.Fatalf("StartExplicit error = %v", err)
	}
}

func TestSurfacePresenceWithoutRuntimeAllowsChatReady(t *testing.T) {
	controller := newSurfacePresenceController(nil, time.Minute)
	t.Cleanup(controller.Close)

	snapshot, err := controller.Update("chat-tab", "chat", "claim")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.EffectiveMode != "chat" {
		t.Fatalf("effective_mode=%q want chat", snapshot.EffectiveMode)
	}
	if snapshot.IdleChatActive {
		t.Fatalf("idlechat_active=true want false (runtime detached)")
	}
	if snapshot.ChatPresenceCount != 1 {
		t.Fatalf("chat_presence_count=%d want 1", snapshot.ChatPresenceCount)
	}

	released, err := controller.Update("chat-tab", "chat", "release")
	if err != nil {
		t.Fatal(err)
	}
	if released.EffectiveMode != "none" {
		t.Fatalf("released effective_mode=%q want none", released.EffectiveMode)
	}
}

func TestSurfacePresenceExplicitStartRejectsWhenRuntimeDetached(t *testing.T) {
	controller := newSurfacePresenceController(nil, time.Minute)
	t.Cleanup(controller.Close)

	err := controller.StartExplicit()
	if err == nil {
		t.Fatal("StartExplicit returned nil; want error when runtime is nil")
	}
}

func TestHandleSurfacePresenceValidatesProfileAndReturnsAggregateState(t *testing.T) {
	runtime := &fakeSurfacePresenceRuntime{disabled: true}
	controller := newSurfacePresenceController(runtime, time.Minute)
	t.Cleanup(controller.Close)
	deps := &Dependencies{surfacePresence: controller}

	requestBody := []byte(`{"viewer_client_id":"idle-tab","surface":"idlechat","action":"claim"}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/surface-presence", bytes.NewReader(requestBody))
	req.Header.Set("X-RenCrow-Client", "RenCrow_PORTAL")
	req.Header.Set(interactionProfileHeader, "portal-idlechat")
	rec := httptest.NewRecorder()
	deps.handleSurfacePresence().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		EffectiveMode  string `json:"effective_mode"`
		IdleChatActive bool   `json:"idlechat_active"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.EffectiveMode != "none" || response.IdleChatActive {
		t.Fatalf("response = %+v", response)
	}

	mismatch := httptest.NewRequest(http.MethodPost, "/viewer/surface-presence", bytes.NewReader(requestBody))
	mismatch.Header.Set("X-RenCrow-Client", "RenCrow_PORTAL")
	mismatch.Header.Set(interactionProfileHeader, "portal-chat")
	mismatchRec := httptest.NewRecorder()
	deps.handleSurfacePresence().ServeHTTP(mismatchRec, mismatch)
	if mismatchRec.Code != http.StatusForbidden {
		t.Fatalf("profile mismatch status=%d, want 403", mismatchRec.Code)
	}
}

func TestHandleIdleChatStartDeniesV1Start(t *testing.T) {
	controller := newSurfacePresenceController(nil, time.Minute)
	t.Cleanup(controller.Close)
	identity := surfacePresenceIdentity{principal: "local", clientType: "RenCrow_PORTAL", viewerClientID: "idle-tab"}
	if _, err := controller.UpdateLegacy(identity, "idlechat", "claim"); err != nil {
		t.Fatal(err)
	}
	deps := &Dependencies{idleChatOrch: &idlechat.IdleChatOrchestrator{}, surfacePresence: controller}
	req := httptest.NewRequest(http.MethodPost, "/viewer/idlechat/start", nil)
	rec := httptest.NewRecorder()
	deps.handleIdleChatStart().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("legacy IdleChat start status=%d body=%q, want 409", rec.Code, rec.Body.String())
	}
}

func TestHandleSurfacePresenceEnablesChatReadyWithoutIdleChatRuntime(t *testing.T) {
	controller := newSurfacePresenceController(nil, time.Minute)
	t.Cleanup(controller.Close)
	deps := &Dependencies{surfacePresence: controller}

	body := []byte(`{"viewer_client_id":"chat-tab","surface":"chat","action":"claim"}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/surface-presence", bytes.NewReader(body))
	req.Header.Set("X-RenCrow-Client", "RenCrow_PORTAL")
	req.Header.Set(interactionProfileHeader, "portal-chat")
	rec := httptest.NewRecorder()
	deps.handleSurfacePresence().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		EffectiveMode  string `json:"effective_mode"`
		IdleChatActive bool   `json:"idlechat_active"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.EffectiveMode != "chat" {
		t.Fatalf("effective_mode=%q want chat", response.EffectiveMode)
	}
	if response.IdleChatActive {
		t.Fatalf("idlechat_active=true want false when runtime is detached")
	}
}

func TestHandleSurfacePresenceV2RegisterThenClaim(t *testing.T) {
	controller := newSurfacePresenceController(nil, time.Minute)
	t.Cleanup(controller.Close)
	deps := &Dependencies{surfacePresence: controller}

	register := httptest.NewRequest(http.MethodPost, "/viewer/surface-presence", bytes.NewBufferString(`{"protocol_version":2,"viewer_client_id":"chat-tab","action":"register"}`))
	register.Header.Set("X-RenCrow-Client", "RenCrow_PORTAL")
	register.Header.Set(interactionProfileHeader, "portal-chat")
	registerRec := httptest.NewRecorder()
	deps.handleSurfacePresence().ServeHTTP(registerRec, register)
	if registerRec.Code != http.StatusOK {
		t.Fatalf("register status=%d body=%s", registerRec.Code, registerRec.Body.String())
	}
	registerBody := append([]byte(nil), registerRec.Body.Bytes()...)
	var registered struct {
		OK                  bool   `json:"ok"`
		ProtocolVersion     int    `json:"protocol_version"`
		RuntimeEpoch        string `json:"runtime_epoch"`
		RegistrationReceipt string `json:"registration_receipt"`
		LastUpdateSeq       uint64 `json:"last_update_seq"`
	}
	if err := json.Unmarshal(registerBody, &registered); err != nil {
		t.Fatal(err)
	}
	if !registered.OK || registered.ProtocolVersion != 2 || registered.RuntimeEpoch == "" || registered.RegistrationReceipt == "" || registered.LastUpdateSeq != 0 {
		t.Fatalf("register response = %+v", registered)
	}
	var registerWire map[string]json.RawMessage
	if err := json.Unmarshal(registerBody, &registerWire); err != nil {
		t.Fatal(err)
	}
	if lastUpdateSeq, ok := registerWire["last_update_seq"]; !ok || !bytes.Equal(lastUpdateSeq, []byte("0")) {
		t.Errorf("register wire last_update_seq = %s (present=%t), want explicit JSON integer 0", lastUpdateSeq, ok)
	}

	claimBody, err := json.Marshal(map[string]any{
		"protocol_version": 2, "viewer_client_id": "chat-tab", "runtime_epoch": registered.RuntimeEpoch,
		"registration_receipt": registered.RegistrationReceipt, "surface": "chat", "action": "claim", "update_seq": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	claim := httptest.NewRequest(http.MethodPost, "/viewer/surface-presence", bytes.NewReader(claimBody))
	claim.Header.Set("X-RenCrow-Client", "RenCrow_PORTAL")
	claim.Header.Set(interactionProfileHeader, "portal-chat")
	claimRec := httptest.NewRecorder()
	deps.handleSurfacePresence().ServeHTTP(claimRec, claim)
	if claimRec.Code != http.StatusOK {
		t.Fatalf("claim status=%d body=%s", claimRec.Code, claimRec.Body.String())
	}
	var claimed struct {
		OK                bool   `json:"ok"`
		ProtocolVersion   int    `json:"protocol_version"`
		AcceptedUpdateSeq uint64 `json:"accepted_update_seq"`
		RuntimeEpoch      string `json:"runtime_epoch"`
		EffectiveMode     string `json:"effective_mode"`
		ForegroundReady   bool   `json:"foreground_ready"`
	}
	if err := json.NewDecoder(claimRec.Body).Decode(&claimed); err != nil {
		t.Fatal(err)
	}
	if !claimed.OK || claimed.ProtocolVersion != 2 || claimed.AcceptedUpdateSeq != 1 || claimed.RuntimeEpoch != registered.RuntimeEpoch || claimed.EffectiveMode != "chat" || !claimed.ForegroundReady {
		t.Fatalf("claim response = %+v", claimed)
	}
}

func TestHandleSurfacePresenceV2CoreViewerClaimsOtherOnly(t *testing.T) {
	controller := newSurfacePresenceController(nil, time.Minute)
	t.Cleanup(controller.Close)
	deps := &Dependencies{surfacePresence: controller}
	newRequest := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/viewer/surface-presence", bytes.NewBufferString(body))
		req.Header.Set("X-RenCrow-Client", "RenCrow_CORE")
		req.Header.Set(interactionProfileHeader, "core-viewer")
		rec := httptest.NewRecorder()
		deps.handleSurfacePresence().ServeHTTP(rec, req)
		return rec
	}
	registerRec := newRequest(`{"protocol_version":2,"viewer_client_id":"core-viewer-tab","action":"register"}`)
	if registerRec.Code != http.StatusOK {
		t.Fatalf("CORE viewer register status=%d body=%s", registerRec.Code, registerRec.Body.String())
	}
	var registered surfacePresenceV2Response
	if err := json.NewDecoder(registerRec.Body).Decode(&registered); err != nil {
		t.Fatal(err)
	}
	claim := func(surface string, seq uint64) *httptest.ResponseRecorder {
		body, err := json.Marshal(surfacePresenceV2Request{
			ProtocolVersion: 2, ViewerClientID: "core-viewer-tab", RuntimeEpoch: registered.RuntimeEpoch,
			RegistrationReceipt: registered.RegistrationReceipt, Surface: surface, Action: "claim", UpdateSeq: seq,
		})
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil {
			t.Fatal(err)
		}
		if _, exists := fields["claim_seq"]; exists {
			t.Fatalf("claim request included forbidden claim_seq: %s", body)
		}
		return newRequest(string(body))
	}
	otherRec := claim("other", 1)
	if otherRec.Code != http.StatusOK {
		t.Fatalf("CORE viewer other claim status=%d body=%s", otherRec.Code, otherRec.Body.String())
	}
	idleRec := claim("idlechat", 2)
	if idleRec.Code != http.StatusForbidden {
		t.Fatalf("CORE viewer idlechat claim status=%d body=%s, want 403", idleRec.Code, idleRec.Body.String())
	}
	if !surfacePresenceProfileAllowsClient("RenCrow_CORE", "core-viewer") ||
		!surfacePresenceProfileAllows("RenCrow_CORE", "core-viewer", "other") ||
		surfacePresenceProfileAllows("RenCrow_CORE", "core-viewer", "idlechat") ||
		surfacePresenceProfileAllowsClient("RenCrow_PORTAL", "core-viewer") {
		t.Fatal("CORE viewer profile must allow only the CORE client and other surface")
	}
}

func TestSurfacePresenceProfilesKeepCMDAndCOREViewerSurfacesNarrow(t *testing.T) {
	for _, profile := range []string{"cmd-chat", "cmd-idlechat", "cmd-diagnostics", "cmd-control"} {
		if !surfacePresenceProfileAllows("RenCrow_CMD", profile, "cmd") {
			t.Errorf("CMD profile %q does not allow its cmd surface", profile)
		}
		for _, surface := range []string{"chat", "idlechat", "games", "other"} {
			if surfacePresenceProfileAllows("RenCrow_CMD", profile, surface) {
				t.Errorf("CMD profile %q unexpectedly allows surface %q", profile, surface)
			}
		}
	}
	if surfacePresenceProfileAllowsClient("RenCrow_CMD", "agent-ops") ||
		surfacePresenceProfileAllows("RenCrow_CMD", "agent-ops", "cmd") {
		t.Fatal("agent-ops bearer profile must not expose the separate presence route")
	}
	if surface, ok := viewerForegroundPresenceSurface("core-viewer"); !ok || surface != "other" ||
		!surfacePresenceProfileAllows("RenCrow_CORE", "core-viewer", surface) {
		t.Fatalf("CORE viewer send must pin only its other surface, got surface=%q ok=%t", surface, ok)
	}
}

func TestHandleSurfacePresenceRejectsUnknownProtocolVersion(t *testing.T) {
	controller := newSurfacePresenceController(nil, time.Minute)
	t.Cleanup(controller.Close)
	deps := &Dependencies{surfacePresence: controller}

	for _, version := range []string{"3", "null"} {
		t.Run(version, func(t *testing.T) {
			body := fmt.Sprintf(`{"protocol_version":%s,"viewer_client_id":"chat-tab","action":"register"}`, version)
			req := httptest.NewRequest(http.MethodPost, "/viewer/surface-presence", bytes.NewBufferString(body))
			req.Header.Set("X-RenCrow-Client", "RenCrow_PORTAL")
			req.Header.Set(interactionProfileHeader, "portal-chat")
			rec := httptest.NewRecorder()
			deps.handleSurfacePresence().ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("unknown protocol status=%d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusBadRequest)
			}
		})
	}
}

func TestSurfacePresenceV2ReregisterRotatesRetiredReceipt(t *testing.T) {
	controller := newSurfacePresenceController(nil, time.Minute)
	t.Cleanup(controller.Close)
	identity := surfacePresenceIdentity{principal: "local", clientType: "RenCrow_PORTAL", viewerClientID: "chat-tab"}
	first, err := controller.RegisterV2(identity)
	if err != nil {
		t.Fatal(err)
	}
	claim := surfacePresenceV2Request{ProtocolVersion: 2, ViewerClientID: identity.viewerClientID, RuntimeEpoch: first.RuntimeEpoch, RegistrationReceipt: first.RegistrationReceipt, Surface: "chat", Action: "claim", UpdateSeq: 1}
	if _, err := controller.UpdateV2(identity, claim); err != nil {
		t.Fatal(err)
	}
	release := claim
	release.Action = "release"
	release.UpdateSeq = 2
	release.ClaimSeq = 1
	if _, err := controller.UpdateV2(identity, release); err != nil {
		t.Fatal(err)
	}
	second, err := controller.RegisterV2(identity)
	if err != nil {
		t.Fatal(err)
	}
	if second.RegistrationReceipt == first.RegistrationReceipt {
		t.Fatal("re-register reused retired receipt")
	}
	if _, err := controller.UpdateV2(identity, claim); !errors.Is(err, errPresenceRegistrationRequired) {
		t.Fatalf("old receipt claim error=%v, want registration_required", err)
	}
}

func TestSurfacePresenceV1ReleaseDoesNotClearAnotherPrincipal(t *testing.T) {
	controller := newSurfacePresenceController(nil, time.Minute)
	t.Cleanup(controller.Close)
	first := surfacePresenceIdentity{principal: "actor-a", clientType: "RenCrow_PORTAL", viewerClientID: "shared-id"}
	second := surfacePresenceIdentity{principal: "actor-b", clientType: "RenCrow_PORTAL", viewerClientID: "shared-id"}
	if _, err := controller.UpdateLegacy(first, "chat", "claim"); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.UpdateLegacy(second, "chat", "release"); err != nil {
		t.Fatal(err)
	}
	response := controller.snapshotForLegacy()
	if response.ForegroundPresenceCount != 1 {
		t.Fatalf("other principal released foreground blocker: %+v", response)
	}
}

func TestSurfacePresenceV2SameSequenceIsIdempotentAndDoesNotExtendLease(t *testing.T) {
	controller := newSurfacePresenceController(nil, time.Minute)
	t.Cleanup(controller.Close)
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	controller.now = func() time.Time { return now }
	identity := surfacePresenceIdentity{principal: "local", clientType: "RenCrow_PORTAL", viewerClientID: "chat-tab"}
	registered, err := controller.RegisterV2(identity)
	if err != nil {
		t.Fatal(err)
	}
	claim := surfacePresenceV2Request{
		ProtocolVersion:     2,
		ViewerClientID:      identity.viewerClientID,
		RuntimeEpoch:        registered.RuntimeEpoch,
		RegistrationReceipt: registered.RegistrationReceipt,
		Surface:             "chat",
		Action:              "claim",
		UpdateSeq:           1,
	}
	if _, err := controller.UpdateV2(identity, claim); err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Second)
	heartbeat := claim
	heartbeat.Action = "heartbeat"
	heartbeat.UpdateSeq = 2
	heartbeat.ClaimSeq = 1
	first, err := controller.UpdateV2(identity, heartbeat)
	if err != nil {
		t.Fatal(err)
	}
	second, err := controller.UpdateV2(identity, heartbeat)
	if err != nil {
		t.Fatal(err)
	}
	if first.LeaseExpiresAt == nil || second.LeaseExpiresAt == nil || !first.LeaseExpiresAt.Equal(*second.LeaseExpiresAt) {
		t.Fatalf("duplicate heartbeat extended lease: first=%v second=%v", first.LeaseExpiresAt, second.LeaseExpiresAt)
	}
}

func TestSurfacePresenceV2ReleaseBeforeClaimRetiresReceipt(t *testing.T) {
	controller := newSurfacePresenceController(nil, time.Minute)
	t.Cleanup(controller.Close)
	identity := surfacePresenceIdentity{principal: "local", clientType: "RenCrow_PORTAL", viewerClientID: "chat-tab"}
	registered, err := controller.RegisterV2(identity)
	if err != nil {
		t.Fatal(err)
	}
	release := surfacePresenceV2Request{
		ProtocolVersion:     2,
		ViewerClientID:      identity.viewerClientID,
		RuntimeEpoch:        registered.RuntimeEpoch,
		RegistrationReceipt: registered.RegistrationReceipt,
		Surface:             "chat",
		Action:              "release",
		UpdateSeq:           2,
		ClaimSeq:            1,
	}
	if _, err := controller.UpdateV2(identity, release); err != nil {
		t.Fatalf("release before claim failed: %v", err)
	}
	claim := release
	claim.Action = "claim"
	claim.UpdateSeq = 1
	claim.ClaimSeq = 0
	if _, err := controller.UpdateV2(identity, claim); !errors.Is(err, errPresenceRegistrationRequired) {
		t.Fatalf("late claim error=%v, want registration_required", err)
	}
	if _, err := controller.UpdateV2(identity, release); err != nil {
		t.Fatalf("same release replay should be idempotent: %v", err)
	}
}

func TestSurfacePresenceV2ClaimAtomicallyReplacesSurfaceAndAdvancesPolicy(t *testing.T) {
	runtime := &fakeSurfacePresenceRuntime{disabled: true}
	controller := newSurfacePresenceController(runtime, time.Minute)
	t.Cleanup(controller.Close)
	identity := surfacePresenceIdentity{principal: "local", clientType: "RenCrow_PORTAL", viewerClientID: "one-tab"}
	registered, err := controller.RegisterV2(identity)
	if err != nil {
		t.Fatal(err)
	}
	idleClaim := surfacePresenceV2Request{ProtocolVersion: 2, ViewerClientID: identity.viewerClientID, RuntimeEpoch: registered.RuntimeEpoch, RegistrationReceipt: registered.RegistrationReceipt, Surface: "idlechat", Action: "claim", UpdateSeq: 1}
	idle, err := controller.UpdateV2(identity, idleClaim)
	if err != nil {
		t.Fatal(err)
	}
	if !idle.PlaybackAllowed || idle.IdleChatPresenceCount != 1 {
		t.Fatalf("IdleChat claim response=%+v", idle)
	}
	frontClaim := idleClaim
	frontClaim.Surface = "games"
	frontClaim.UpdateSeq = 2
	front, err := controller.UpdateV2(identity, frontClaim)
	if err != nil {
		t.Fatal(err)
	}
	if front.EffectiveMode != "chat" || front.IdleChatPresenceCount != 0 || front.ForegroundPresenceCount != 1 || front.PlaybackAllowed || front.StateRevision <= idle.StateRevision {
		t.Fatalf("foreground replacement response=%+v after idle=%+v", front, idle)
	}
}

func TestSurfacePresenceV2DoesNotRestorePlaybackUntilQuickStopIsConfirmed(t *testing.T) {
	runtime := &fakeSurfacePresenceRuntime{disabled: true}
	controller := newSurfacePresenceController(runtime, time.Minute)
	t.Cleanup(controller.Close)
	idleIdentity := surfacePresenceIdentity{principal: "local", clientType: "RenCrow_PORTAL", viewerClientID: "idle-tab"}
	idleRegistration, err := controller.RegisterV2(idleIdentity)
	if err != nil {
		t.Fatal(err)
	}
	idleClaim := surfacePresenceV2Request{ProtocolVersion: 2, ViewerClientID: idleIdentity.viewerClientID, RuntimeEpoch: idleRegistration.RuntimeEpoch, RegistrationReceipt: idleRegistration.RegistrationReceipt, Surface: "idlechat", Action: "claim", UpdateSeq: 1}
	idleResponse, err := controller.UpdateV2(idleIdentity, idleClaim)
	if err != nil || !idleResponse.PlaybackAllowed {
		t.Fatalf("initial IdleChat claim response=%+v err=%v", idleResponse, err)
	}

	runtime.mu.Lock()
	runtime.stopErr = errors.New("quick stop unavailable")
	runtime.mu.Unlock()
	frontIdentity := surfacePresenceIdentity{principal: "local", clientType: "RenCrow_CMD", viewerClientID: "cmd-instance"}
	frontRegistration, err := controller.RegisterV2(frontIdentity)
	if err != nil {
		t.Fatal(err)
	}
	frontClaim := surfacePresenceV2Request{ProtocolVersion: 2, ViewerClientID: frontIdentity.viewerClientID, RuntimeEpoch: frontRegistration.RuntimeEpoch, RegistrationReceipt: frontRegistration.RegistrationReceipt, Surface: "cmd", Action: "claim", UpdateSeq: 1}
	if _, err := controller.UpdateV2(frontIdentity, frontClaim); err == nil {
		t.Fatal("foreground claim succeeded without a quick-stop receipt")
	}
	frontRelease := frontClaim
	frontRelease.Action = "release"
	frontRelease.UpdateSeq = 2
	frontRelease.ClaimSeq = 1
	if _, err := controller.UpdateV2(frontIdentity, frontRelease); err == nil {
		t.Fatal("release restored playback without confirming the failed quick stop")
	}
	if response := controller.snapshotForRegistration(idleIdentity); response.PlaybackAllowed {
		t.Fatalf("playback was allowed before quick-stop confirmation: %+v", response)
	}

	runtime.mu.Lock()
	runtime.stopErr = nil
	runtime.mu.Unlock()
	idleHeartbeat := idleClaim
	idleHeartbeat.Action = "heartbeat"
	idleHeartbeat.UpdateSeq = 2
	idleHeartbeat.ClaimSeq = 1
	response, err := controller.UpdateV2(idleIdentity, idleHeartbeat)
	if err != nil || !response.PlaybackAllowed {
		t.Fatalf("quick-stop reconciliation did not restore explicit playback readiness: response=%+v err=%v", response, err)
	}
}

func TestSurfacePresenceExplicitStopAdvancesPolicyRevisionWithoutManualStart(t *testing.T) {
	runtime := &fakeSurfacePresenceRuntime{disabled: true}
	controller := newSurfacePresenceController(runtime, time.Minute)
	t.Cleanup(controller.Close)
	identity := surfacePresenceIdentity{principal: "local", clientType: "RenCrow_PORTAL", viewerClientID: "idle-tab"}
	registration, err := controller.RegisterV2(identity)
	if err != nil {
		t.Fatal(err)
	}
	claim := surfacePresenceV2Request{ProtocolVersion: 2, ViewerClientID: identity.viewerClientID, RuntimeEpoch: registration.RuntimeEpoch, RegistrationReceipt: registration.RegistrationReceipt, Surface: "idlechat", Action: "claim", UpdateSeq: 1}
	ready, err := controller.UpdateV2(identity, claim)
	if err != nil || !ready.PlaybackAllowed {
		t.Fatalf("initial playback policy response=%+v err=%v", ready, err)
	}
	controller.StopExplicit()
	stopped := controller.snapshotForRegistration(identity)
	if stopped.StateRevision <= ready.StateRevision || !stopped.PlaybackAllowed || stopped.PlaybackActive {
		t.Fatalf("explicit stop did not invalidate the prior playback revision: ready=%+v stopped=%+v", ready, stopped)
	}
	if _, err := controller.ValidatePlayback(identity, registration.RuntimeEpoch, registration.RegistrationReceipt, 1, ready.StateRevision); !errors.Is(err, errPresencePlaybackChanged) {
		t.Fatalf("stale playback revision error=%v, want playback_state_changed", err)
	}
	if err := controller.StartExplicit(); !errors.Is(err, errPresenceManualStartDisabled) {
		t.Fatalf("manual IdleChat start error=%v, want manual_start_disabled", err)
	}
	if _, err := controller.ValidatePlayback(identity, registration.RuntimeEpoch, registration.RegistrationReceipt, 1, stopped.StateRevision); err != nil {
		t.Fatalf("current explicit-play eligibility rejected: %v", err)
	}
}

func TestSurfacePresenceV2ForegroundPinSurvivesLeaseExpiry(t *testing.T) {
	runtime := &fakeSurfacePresenceRuntime{disabled: true}
	controller := newSurfacePresenceController(runtime, 30*time.Second)
	t.Cleanup(controller.Close)
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	controller.now = func() time.Time { return now }

	idleIdentity := surfacePresenceIdentity{principal: "local", clientType: "RenCrow_PORTAL", viewerClientID: "idle-tab"}
	idleReg, err := controller.RegisterV2(idleIdentity)
	if err != nil {
		t.Fatal(err)
	}
	idleReq := surfacePresenceV2Request{ProtocolVersion: 2, ViewerClientID: idleIdentity.viewerClientID, RuntimeEpoch: idleReg.RuntimeEpoch, RegistrationReceipt: idleReg.RegistrationReceipt, Surface: "idlechat", Action: "claim", UpdateSeq: 1}
	if _, err := controller.UpdateV2(idleIdentity, idleReq); err != nil {
		t.Fatal(err)
	}

	cmdIdentity := surfacePresenceIdentity{principal: "local", clientType: "RenCrow_CMD", viewerClientID: "cmd-instance"}
	cmdReg, err := controller.RegisterV2(cmdIdentity)
	if err != nil {
		t.Fatal(err)
	}
	cmdReq := surfacePresenceV2Request{ProtocolVersion: 2, ViewerClientID: cmdIdentity.viewerClientID, RuntimeEpoch: cmdReg.RuntimeEpoch, RegistrationReceipt: cmdReg.RegistrationReceipt, Surface: "cmd", Action: "claim", UpdateSeq: 1}
	cmdClaim, err := controller.UpdateV2(cmdIdentity, cmdReq)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := controller.PinForeground(cmdIdentity, cmdReg.RuntimeEpoch, cmdReg.RegistrationReceipt, cmdClaim.CurrentClaimSeq)
	if err != nil {
		t.Fatal(err)
	}

	now = now.Add(31 * time.Second)
	controller.mu.Lock()
	generation := controller.timerGeneration
	controller.mu.Unlock()
	controller.expireLeases(generation)
	if response := controller.snapshotForRegistration(idleIdentity); response.PlaybackAllowed || response.ForegroundTurnCount != 1 {
		t.Fatalf("lease expiry released an accepted foreground turn: %+v", response)
	}
	if err := pin.Release(); err != nil {
		t.Fatal(err)
	}
	if response := controller.snapshotForRegistration(idleIdentity); response.PlaybackAllowed || response.ForegroundTurnCount != 0 {
		t.Fatalf("turn completion leaked a foreground pin or restored expired IdleChat: %+v", response)
	}
}

func TestSurfacePresenceServerRequestPinQuickStopsAndReleases(t *testing.T) {
	runtime := &fakeSurfacePresenceRuntime{manual: true, active: true}
	controller := newSurfacePresenceController(runtime, time.Minute)
	t.Cleanup(controller.Close)

	pin, err := controller.PinForegroundRequest()
	if err != nil {
		t.Fatalf("pin trusted server request: %v", err)
	}
	if !runtime.IsDisabled() || runtime.IsChatActive() || runtime.IsManualMode() {
		t.Fatal("server request pin returned before quick stop completed")
	}
	if starts, stops := runtime.counts(); starts != 0 || stops != 1 {
		t.Fatalf("quick-stop counts starts=%d stops=%d, want 0/1", starts, stops)
	}
	controller.mu.Lock()
	pinCount := len(controller.pins)
	ready := controller.playbackStopConfirmed && controller.appliedRevision >= controller.stateRevision
	controller.mu.Unlock()
	if pinCount != 1 || !ready {
		t.Fatalf("server request pin state pins=%d ready=%t, want one ready pin", pinCount, ready)
	}
	if err := pin.Release(); err != nil {
		t.Fatalf("release server request pin: %v", err)
	}
	controller.mu.Lock()
	pinCount = len(controller.pins)
	controller.mu.Unlock()
	if pinCount != 0 {
		t.Fatalf("server request pins after release=%d, want 0", pinCount)
	}
}

func TestSurfacePresenceServerRequestPinFailsClosedWhenQuickStopFails(t *testing.T) {
	runtime := &fakeSurfacePresenceRuntime{stopErr: errors.New("quick stop unavailable")}
	controller := newSurfacePresenceController(runtime, time.Minute)
	t.Cleanup(controller.Close)

	if pin, err := controller.PinForegroundRequest(); err == nil || pin != nil {
		if pin != nil {
			_ = pin.Release()
		}
		t.Fatalf("server request pin=(%v,%v), want no pin and quick-stop error", pin, err)
	}
	controller.mu.Lock()
	pinCount := len(controller.pins)
	controller.mu.Unlock()
	if pinCount != 0 {
		t.Fatalf("failed quick stop leaked %d server request pins", pinCount)
	}
}

type gatedSurfacePresenceQuickStopper struct {
	started chan struct{}
	release chan struct{}
}

func (r *gatedSurfacePresenceQuickStopper) StopPlaybackForForeground(_ context.Context, request idlechat.PlaybackStopRequest) (idlechat.PlaybackStopReceipt, error) {
	select {
	case r.started <- struct{}{}:
	default:
	}
	<-r.release
	return idlechat.PlaybackStopReceipt{
		RuntimeEpoch:         request.RuntimeEpoch,
		AppliedStateRevision: request.StateRevision,
		ResumeBlocked:        true,
	}, nil
}

func TestSurfacePresenceConcurrentServerRequestPinCannotPassPendingQuickStop(t *testing.T) {
	runtime := &gatedSurfacePresenceQuickStopper{started: make(chan struct{}, 1), release: make(chan struct{})}
	controller := newSurfacePresenceController(runtime, time.Minute)
	t.Cleanup(controller.Close)

	firstDone := make(chan struct {
		pin *surfacePresencePin
		err error
	}, 1)
	go func() {
		pin, err := controller.PinForegroundRequest()
		firstDone <- struct {
			pin *surfacePresencePin
			err error
		}{pin: pin, err: err}
	}()
	select {
	case <-runtime.started:
	case <-time.After(time.Second):
		t.Fatal("first server request did not enter quick stop")
	}

	secondPin, secondErr := controller.PinForegroundRequest()
	if secondPin != nil || !errors.Is(secondErr, errPresenceForegroundNotReady) {
		if secondPin != nil {
			_ = secondPin.Release()
		}
		t.Fatalf("concurrent request passed pending quick stop: pin=%v err=%v", secondPin, secondErr)
	}
	select {
	case result := <-firstDone:
		if result.pin != nil {
			_ = result.pin.Release()
		}
		t.Fatalf("first request returned before quick-stop receipt: pin=%v err=%v", result.pin, result.err)
	default:
	}
	close(runtime.release)
	select {
	case result := <-firstDone:
		if result.err != nil || result.pin == nil {
			t.Fatalf("first request did not become ready after quick-stop receipt: pin=%v err=%v", result.pin, result.err)
		}
		if err := result.pin.Release(); err != nil {
			t.Fatalf("release first request pin: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first request did not return after quick-stop receipt")
	}
}

func TestSurfacePresenceV2RegistrationCapacityDoesNotEvictExistingLease(t *testing.T) {
	controller := newSurfacePresenceController(nil, time.Minute)
	t.Cleanup(controller.Close)
	var activeIdentity surfacePresenceIdentity
	var activeRegistration surfacePresenceV2Response
	for index := 0; index < surfacePresenceMaxRegistrations; index++ {
		identity := surfacePresenceIdentity{principal: "local", clientType: "RenCrow_PORTAL", viewerClientID: fmt.Sprintf("tab-%d", index)}
		registered, err := controller.RegisterV2(identity)
		if err != nil {
			t.Fatalf("register %d: %v", index, err)
		}
		if index == 0 {
			activeIdentity, activeRegistration = identity, registered
			claim := surfacePresenceV2Request{ProtocolVersion: 2, ViewerClientID: identity.viewerClientID, RuntimeEpoch: registered.RuntimeEpoch, RegistrationReceipt: registered.RegistrationReceipt, Surface: "chat", Action: "claim", UpdateSeq: 1}
			if _, err := controller.UpdateV2(identity, claim); err != nil {
				t.Fatalf("claim active registration: %v", err)
			}
		}
	}
	if _, err := controller.RegisterV2(surfacePresenceIdentity{principal: "local", clientType: "RenCrow_PORTAL", viewerClientID: "overflow"}); !errors.Is(err, errPresenceCapacityExceeded) {
		t.Fatalf("capacity error=%v, want capacity exceeded", err)
	}
	if _, err := controller.UpdateV2(activeIdentity, surfacePresenceV2Request{ProtocolVersion: 2, ViewerClientID: activeIdentity.viewerClientID, RuntimeEpoch: activeRegistration.RuntimeEpoch, RegistrationReceipt: activeRegistration.RegistrationReceipt, Surface: "chat", Action: "heartbeat", UpdateSeq: 2, ClaimSeq: 1}); err != nil {
		t.Fatalf("active registration was evicted at capacity: %v", err)
	}
}
