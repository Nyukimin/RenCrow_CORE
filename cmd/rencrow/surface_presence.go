package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	idlechat "github.com/Nyukimin/RenCrow_CORE/internal/application/idlechat"
)

const (
	surfacePresenceTTL               = 30 * time.Second
	surfacePresenceMaxRegistrations  = 4096
	surfacePresenceMaxClientIDBytes  = 128
	surfacePresenceReceiptBytes      = 32
	surfacePresenceRetiredCacheTTL   = 30 * time.Second
	surfacePresenceProtocolVersionV2 = 2
)

var errChatSurfacePresent = errors.New("chat surface is visible")

type surfaceLifecycleRuntime interface {
	surfacePresenceQuickStopper
}

// surfacePresenceQuickStopper is deliberately narrower than the IdleChat
// runtime. The presence mutex is never held while this method runs.
type surfacePresenceQuickStopper interface {
	StopPlaybackForForeground(context.Context, idlechat.PlaybackStopRequest) (idlechat.PlaybackStopReceipt, error)
}

type surfacePresenceRuntimeActivity interface {
	IsChatActive() bool
	IsManualMode() bool
}

type surfacePresencePlaybackControlRuntime interface {
	PlaybackControlSnapshot() idlechat.PlaybackControlSnapshot
}

type surfacePresencePlaybackControlNotifier interface {
	SetPlaybackControlSnapshotChangedHandler(func())
}

type surfacePresenceKey struct {
	principal      string
	clientType     string
	viewerClientID string
	surface        string
}

type surfacePresenceIdentity struct {
	principal      string
	clientType     string
	viewerClientID string
}

type surfacePresenceV2Request struct {
	ProtocolVersion     int    `json:"protocol_version"`
	ViewerClientID      string `json:"viewer_client_id"`
	RuntimeEpoch        string `json:"runtime_epoch"`
	RegistrationReceipt string `json:"registration_receipt"`
	Surface             string `json:"surface"`
	Action              string `json:"action"`
	UpdateSeq           uint64 `json:"update_seq"`
	ClaimSeq            uint64 `json:"claim_seq,omitempty"`
}

type surfacePresenceV2Response struct {
	OK                      bool       `json:"ok"`
	ProtocolVersion         int        `json:"protocol_version"`
	RuntimeEpoch            string     `json:"runtime_epoch"`
	RegistrationReceipt     string     `json:"registration_receipt,omitempty"`
	RegistrationExpiresAt   *time.Time `json:"registration_expires_at,omitempty"`
	LastUpdateSeq           uint64     `json:"last_update_seq"`
	AcceptedUpdateSeq       uint64     `json:"accepted_update_seq,omitempty"`
	CurrentClaimSeq         uint64     `json:"current_claim_seq,omitempty"`
	StateRevision           uint64     `json:"state_revision"`
	EffectiveMode           string     `json:"effective_mode"`
	PlaybackState           string     `json:"playback_state"`
	PlaybackAllowed         bool       `json:"playback_allowed"`
	PlaybackActive          bool       `json:"playback_active"`
	PlaybackGeneration      uint64     `json:"playback_generation"`
	ForegroundReady         bool       `json:"foreground_ready"`
	ForegroundPresenceCount int        `json:"foreground_presence_count"`
	ForegroundTurnCount     int        `json:"foreground_turn_count"`
	IdleChatPresenceCount   int        `json:"idlechat_presence_count"`
	CleanupPending          bool       `json:"cleanup_pending"`
	GenerationState         string     `json:"generation_state"`
	GenerationQuiesced      bool       `json:"generation_quiesced"`
	BackendResourceState    string     `json:"backend_resource_state"`
	LeaseExpiresAt          *time.Time `json:"lease_expires_at,omitempty"`
	IdleChatActive          bool       `json:"idlechat_active"`
	ChatPresenceCount       int        `json:"chat_presence_count"`
}

type surfacePresenceRegistration struct {
	receipt       string
	expiresAt     time.Time
	retiredAt     time.Time
	activeSurface string
	claimSeq      uint64
	lastUpdateSeq uint64
	lastAction    string
	lastSurface   string
	lastClaimSeq  uint64
}

type surfacePresencePin struct {
	controller *surfacePresenceController
	once       sync.Once
	err        error
}

func (p *surfacePresencePin) Release() error {
	if p == nil || p.controller == nil {
		return nil
	}
	p.once.Do(func() { p.err = p.controller.releaseForegroundPin(p) })
	return p.err
}

type surfacePresencePolicyPlan struct {
	revision     uint64
	runtimeEpoch string
	target       string
	idleVisible  bool
	foreground   bool
}

type surfacePresenceAPIError struct {
	status  int
	code    string
	message string
}

func (e *surfacePresenceAPIError) Error() string { return e.message }

func newSurfacePresenceAPIError(status int, code, message string) *surfacePresenceAPIError {
	return &surfacePresenceAPIError{status: status, code: code, message: message}
}

var (
	errPresenceRegistrationRequired = newSurfacePresenceAPIError(409, "registration_required", "register again before updating presence")
	errPresenceSequenceConflict     = newSurfacePresenceAPIError(409, "update_seq_conflict", "update_seq was already used with different content")
	errPresenceStaleSequence        = newSurfacePresenceAPIError(409, "stale_update_seq", "update_seq is older than the latest accepted update")
	errPresencePlaybackChanged      = newSurfacePresenceAPIError(409, "playback_state_changed", "playback policy changed; refresh presence before requesting playback")
	errPresenceManualStartDisabled  = newSurfacePresenceAPIError(409, "manual_start_disabled", "manual IdleChat start is disabled; use prepared playback")
	errPresenceForegroundNotReady   = newSurfacePresenceAPIError(503, "foreground_not_ready", "foreground playback stop is not confirmed")
	errPresenceCapacityExceeded     = newSurfacePresenceAPIError(503, "presence_capacity_exceeded", "surface presence registration capacity exceeded")
)

type surfacePresenceController struct {
	mu        sync.Mutex
	controlMu sync.Mutex

	runtime               surfaceLifecycleRuntime
	ttl                   time.Duration
	runtimeEpoch          string
	initErr               error
	now                   func() time.Time
	registrations         map[surfacePresenceIdentity]*surfacePresenceRegistration
	legacyLeases          map[surfacePresenceKey]time.Time
	pins                  map[*surfacePresencePin]struct{}
	timer                 *time.Timer
	timerGeneration       uint64
	closed                bool
	policyIdleVisible     bool
	policyForeground      bool
	policyInitialized     bool
	stateRevision         uint64
	appliedRevision       uint64
	runtimeActive         bool
	playbackStopConfirmed bool
	controlPublisher      func(viewer.IdleChatControlSnapshot)
	lastControlSnapshot   viewer.IdleChatControlSnapshot
	hasControlSnapshot    bool
}

func newSurfacePresenceController(runtime surfaceLifecycleRuntime, ttl time.Duration) *surfacePresenceController {
	if ttl <= 0 {
		ttl = surfacePresenceTTL
	}
	epoch, err := newSurfacePresenceToken(surfacePresenceReceiptBytes)
	c := &surfacePresenceController{
		runtime:               runtime,
		ttl:                   ttl,
		runtimeEpoch:          epoch,
		initErr:               err,
		now:                   time.Now,
		registrations:         make(map[surfacePresenceIdentity]*surfacePresenceRegistration),
		legacyLeases:          make(map[surfacePresenceKey]time.Time),
		pins:                  make(map[*surfacePresencePin]struct{}),
		stateRevision:         1,
		playbackStopConfirmed: false,
	}
	c.policyInitialized = true
	return c
}

func newSurfacePresenceToken(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate surface presence token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

// Update is the V1 compatibility path used by older in-process callers. The
// HTTP handler limits V1 to conservative foreground blockers.
func (c *surfacePresenceController) Update(viewerClientID, surface, action string) (surfacePresenceV2Response, error) {
	identity := surfacePresenceIdentity{principal: "local", clientType: "RenCrow_PORTAL", viewerClientID: viewerClientID}
	return c.updateLegacy(identity, surface, action)
}

func (c *surfacePresenceController) UpdateLegacy(identity surfacePresenceIdentity, surface, action string) (surfacePresenceV2Response, error) {
	return c.updateLegacy(identity, surface, action)
}

func (c *surfacePresenceController) updateLegacy(identity surfacePresenceIdentity, surface, action string) (response surfacePresenceV2Response, resultErr error) {
	defer func() { c.projectRuntimeActivity(&response) }()
	if c == nil {
		return surfacePresenceV2Response{}, fmt.Errorf("surface presence is unavailable")
	}
	if err := validateSurfacePresenceIdentity(identity); err != nil {
		return surfacePresenceV2Response{}, err
	}
	if (surface != "chat" && surface != "idlechat") || (action != "claim" && action != "heartbeat" && action != "release") {
		return surfacePresenceV2Response{}, fmt.Errorf("invalid surface presence identity")
	}
	key := surfacePresenceKey{principal: identity.principal, clientType: identity.clientType, viewerClientID: identity.viewerClientID, surface: surface}
	now := c.now()
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return surfacePresenceV2Response{}, fmt.Errorf("surface presence is closed")
	}
	changed := c.expireLocked(now)
	if _, exists := c.legacyLeases[key]; !exists && action != "release" && len(c.legacyLeases) >= surfacePresenceMaxRegistrations {
		plan := c.policyPlanLocked(now)
		c.scheduleExpiryLocked(now)
		c.mu.Unlock()
		if changed {
			_ = c.applyPolicy(context.Background(), plan)
		}
		return surfacePresenceV2Response{}, errPresenceCapacityExceeded
	}
	switch action {
	case "claim", "heartbeat":
		c.legacyLeases[key] = now.Add(c.ttl)
	case "release":
		delete(c.legacyLeases, key)
	default:
		plan := c.policyPlanLocked(now)
		c.scheduleExpiryLocked(now)
		c.mu.Unlock()
		if changed {
			_ = c.applyPolicy(context.Background(), plan)
		}
		return surfacePresenceV2Response{}, fmt.Errorf("unsupported surface action %q", action)
	}
	policyChangedNow := c.refreshPolicyLocked()
	changed = policyChangedNow || changed
	plan := c.policyPlanLocked(now)
	c.scheduleExpiryLocked(now)
	response = c.snapshotLocked(now, nil, nil)
	c.mu.Unlock()
	if changed {
		if err := c.applyPolicy(context.Background(), plan); err != nil {
			return response, err
		}
		response = c.snapshotForLegacy()
	}
	return response, nil
}

func (c *surfacePresenceController) RegisterV2(identity surfacePresenceIdentity) (response surfacePresenceV2Response, resultErr error) {
	defer func() { c.projectRuntimeActivity(&response) }()
	if c == nil {
		return surfacePresenceV2Response{}, fmt.Errorf("surface presence is unavailable")
	}
	if c.initErr != nil {
		return surfacePresenceV2Response{}, newSurfacePresenceAPIError(503, "presence_unavailable", c.initErr.Error())
	}
	if err := validateSurfacePresenceIdentity(identity); err != nil {
		return surfacePresenceV2Response{}, err
	}
	receipt, err := newSurfacePresenceToken(surfacePresenceReceiptBytes)
	if err != nil {
		return surfacePresenceV2Response{}, newSurfacePresenceAPIError(503, "presence_unavailable", err.Error())
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return surfacePresenceV2Response{}, fmt.Errorf("surface presence is closed")
	}
	now := c.now()
	policyChanged := c.expireLocked(now)
	if registration := c.registrations[identity]; registration != nil && registration.retiredAt.IsZero() && registration.expiresAt.After(now) {
		response = c.registrationResponseLocked(now, identity, registration)
		plan := c.policyPlanLocked(now)
		c.scheduleExpiryLocked(now)
		c.mu.Unlock()
		if policyChanged {
			if err := c.applyPolicy(context.Background(), plan); err != nil {
				return response, err
			}
			response = c.snapshotForRegistration(identity)
		}
		return response, nil
	}
	if _, hasIdentityRecord := c.registrations[identity]; !hasIdentityRecord && len(c.registrations) >= surfacePresenceMaxRegistrations {
		c.pruneRetiredLocked(now)
		if _, hasIdentityRecord = c.registrations[identity]; !hasIdentityRecord && len(c.registrations) >= surfacePresenceMaxRegistrations {
			plan := c.policyPlanLocked(now)
			c.scheduleExpiryLocked(now)
			c.mu.Unlock()
			if policyChanged {
				_ = c.applyPolicy(context.Background(), plan)
			}
			return surfacePresenceV2Response{}, errPresenceCapacityExceeded
		}
	}
	registration := &surfacePresenceRegistration{receipt: receipt, expiresAt: now.Add(c.ttl)}
	c.registrations[identity] = registration
	plan := c.policyPlanLocked(now)
	response = c.registrationResponseLocked(now, identity, registration)
	c.scheduleExpiryLocked(now)
	c.mu.Unlock()
	if policyChanged {
		if err := c.applyPolicy(context.Background(), plan); err != nil {
			return response, err
		}
		response = c.snapshotForRegistration(identity)
	}
	return response, nil
}

func (c *surfacePresenceController) UpdateV2(identity surfacePresenceIdentity, req surfacePresenceV2Request) (response surfacePresenceV2Response, resultErr error) {
	defer func() { c.projectRuntimeActivity(&response) }()
	if c == nil {
		return surfacePresenceV2Response{}, fmt.Errorf("surface presence is unavailable")
	}
	if err := validateSurfacePresenceIdentity(identity); err != nil {
		return surfacePresenceV2Response{}, err
	}
	if req.ProtocolVersion != surfacePresenceProtocolVersionV2 || req.ViewerClientID != identity.viewerClientID {
		return surfacePresenceV2Response{}, newSurfacePresenceAPIError(400, "invalid_request", "protocol_version or viewer_client_id is invalid")
	}
	if req.RuntimeEpoch != c.runtimeEpoch {
		return surfacePresenceV2Response{}, newSurfacePresenceAPIError(409, "runtime_epoch_mismatch", "register against the current CORE runtime")
	}
	if req.RegistrationReceipt == "" || req.UpdateSeq == 0 || req.Surface == "" {
		return surfacePresenceV2Response{}, newSurfacePresenceAPIError(400, "invalid_request", "registration_receipt, surface, and positive update_seq are required")
	}
	if req.Action != "claim" && req.Action != "heartbeat" && req.Action != "release" {
		return surfacePresenceV2Response{}, newSurfacePresenceAPIError(400, "invalid_request", "action must be claim, heartbeat, or release")
	}
	if req.Surface != "idlechat" && req.Surface != "chat" && req.Surface != "games" && req.Surface != "other" && req.Surface != "cmd" {
		return surfacePresenceV2Response{}, newSurfacePresenceAPIError(400, "invalid_request", "surface is not supported")
	}
	if req.Action == "claim" && req.ClaimSeq != 0 {
		return surfacePresenceV2Response{}, newSurfacePresenceAPIError(400, "invalid_request", "claim must not include claim_seq")
	}
	if req.Action == "heartbeat" && (req.ClaimSeq == 0 || req.ClaimSeq > req.UpdateSeq) {
		return surfacePresenceV2Response{}, newSurfacePresenceAPIError(400, "invalid_request", "heartbeat requires a valid claim_seq")
	}
	if req.Action == "release" && req.ClaimSeq > req.UpdateSeq {
		return surfacePresenceV2Response{}, newSurfacePresenceAPIError(400, "invalid_request", "release claim_seq cannot exceed update_seq")
	}

	now := c.now()
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return surfacePresenceV2Response{}, fmt.Errorf("surface presence is closed")
	}
	policyChanged := c.expireLocked(now)
	registration := c.registrations[identity]
	if registration == nil || registration.receipt != req.RegistrationReceipt {
		plan := c.policyPlanLocked(now)
		c.scheduleExpiryLocked(now)
		c.mu.Unlock()
		if policyChanged {
			_ = c.applyPolicy(context.Background(), plan)
		}
		return surfacePresenceV2Response{}, errPresenceRegistrationRequired
	}
	if !registration.retiredAt.IsZero() && req.Action == "release" && req.UpdateSeq == registration.lastUpdateSeq && req.Action == registration.lastAction && req.Surface == registration.lastSurface && req.ClaimSeq == registration.lastClaimSeq {
		response = c.snapshotLocked(now, &identity, registration)
		response.AcceptedUpdateSeq = registration.lastUpdateSeq
		c.scheduleExpiryLocked(now)
		plan := c.policyPlanLocked(now)
		c.mu.Unlock()
		if policyChanged {
			_ = c.applyPolicy(context.Background(), plan)
		}
		return response, nil
	}
	if !registration.retiredAt.IsZero() || !registration.expiresAt.After(now) {
		plan := c.policyPlanLocked(now)
		c.scheduleExpiryLocked(now)
		c.mu.Unlock()
		if policyChanged {
			_ = c.applyPolicy(context.Background(), plan)
		}
		return surfacePresenceV2Response{}, errPresenceRegistrationRequired
	}
	if req.UpdateSeq == registration.lastUpdateSeq {
		if req.Action != registration.lastAction || req.Surface != registration.lastSurface || req.ClaimSeq != registration.lastClaimSeq {
			plan := c.policyPlanLocked(now)
			c.scheduleExpiryLocked(now)
			c.mu.Unlock()
			if policyChanged {
				_ = c.applyPolicy(context.Background(), plan)
			}
			return surfacePresenceV2Response{}, errPresenceSequenceConflict
		}
		response = c.snapshotLocked(now, &identity, registration)
		response.AcceptedUpdateSeq = registration.lastUpdateSeq
		c.scheduleExpiryLocked(now)
		plan := c.policyPlanLocked(now)
		c.mu.Unlock()
		if policyChanged {
			_ = c.applyPolicy(context.Background(), plan)
			return c.snapshotForRegistration(identity), nil
		}
		return response, nil
	}
	if req.UpdateSeq < registration.lastUpdateSeq {
		plan := c.policyPlanLocked(now)
		c.scheduleExpiryLocked(now)
		c.mu.Unlock()
		if policyChanged {
			_ = c.applyPolicy(context.Background(), plan)
		}
		return surfacePresenceV2Response{}, errPresenceStaleSequence
	}
	if registration.retiredAt.IsZero() == false {
		c.mu.Unlock()
		return surfacePresenceV2Response{}, errPresenceRegistrationRequired
	}
	if req.Action == "heartbeat" && (registration.activeSurface != req.Surface || registration.claimSeq != req.ClaimSeq) {
		plan := c.policyPlanLocked(now)
		c.scheduleExpiryLocked(now)
		c.mu.Unlock()
		if policyChanged {
			_ = c.applyPolicy(context.Background(), plan)
		}
		return surfacePresenceV2Response{}, newSurfacePresenceAPIError(409, "claim_mismatch", "heartbeat does not match the active claim")
	}
	if req.Action == "release" && registration.claimSeq > req.ClaimSeq {
		plan := c.policyPlanLocked(now)
		c.scheduleExpiryLocked(now)
		c.mu.Unlock()
		if policyChanged {
			_ = c.applyPolicy(context.Background(), plan)
		}
		return surfacePresenceV2Response{}, newSurfacePresenceAPIError(409, "stale_claim", "release refers to an older claim")
	}
	if req.Action == "release" && registration.claimSeq == req.ClaimSeq && registration.activeSurface != "" && registration.activeSurface != req.Surface {
		plan := c.policyPlanLocked(now)
		c.scheduleExpiryLocked(now)
		c.mu.Unlock()
		if policyChanged {
			_ = c.applyPolicy(context.Background(), plan)
		}
		return surfacePresenceV2Response{}, newSurfacePresenceAPIError(409, "stale_claim", "release surface does not match the active claim")
	}

	registration.lastUpdateSeq = req.UpdateSeq
	registration.lastAction = req.Action
	registration.lastSurface = req.Surface
	registration.lastClaimSeq = req.ClaimSeq
	switch req.Action {
	case "claim":
		registration.activeSurface = req.Surface
		registration.claimSeq = req.UpdateSeq
		registration.expiresAt = now.Add(c.ttl)
	case "heartbeat":
		registration.expiresAt = now.Add(c.ttl)
	case "release":
		registration.activeSurface = ""
		registration.expiresAt = time.Time{}
		registration.retiredAt = now
	}
	policyChangedNow := c.refreshPolicyLocked()
	policyChanged = policyChangedNow || policyChanged
	plan := c.policyPlanLocked(now)
	needsApply := policyChanged || c.appliedRevision < c.stateRevision
	c.scheduleExpiryLocked(now)
	response = c.snapshotLocked(now, &identity, registration)
	response.AcceptedUpdateSeq = registration.lastUpdateSeq
	c.mu.Unlock()
	if needsApply {
		if err := c.applyPolicy(context.Background(), plan); err != nil {
			return response, err
		}
		return c.snapshotForRegistration(identity), nil
	}
	return response, nil
}

func (c *surfacePresenceController) PinForeground(identity surfacePresenceIdentity, runtimeEpoch, receipt string, claimSeq uint64) (*surfacePresencePin, error) {
	if c == nil {
		return nil, errPresenceRegistrationRequired
	}
	now := c.now()
	c.mu.Lock()
	changed := c.expireLocked(now)
	registration := c.registrations[identity]
	invalidLease := c.closed || runtimeEpoch != c.runtimeEpoch || registration == nil || registration.receipt != receipt || !registration.retiredAt.IsZero() || !registration.expiresAt.After(now) || registration.activeSurface == "" || registration.activeSurface == "idlechat" || registration.claimSeq != claimSeq
	if invalidLease || (c.runtime != nil && !c.playbackStopConfirmed) || c.appliedRevision < c.stateRevision {
		plan := c.policyPlanLocked(now)
		c.scheduleExpiryLocked(now)
		c.mu.Unlock()
		if changed {
			_ = c.applyPolicy(context.Background(), plan)
		}
		if invalidLease {
			return nil, errPresenceRegistrationRequired
		}
		return nil, errPresenceForegroundNotReady
	}
	pin := &surfacePresencePin{controller: c}
	c.pins[pin] = struct{}{}
	policyChangedNow := c.refreshPolicyLocked()
	changed = policyChangedNow || changed
	plan := c.policyPlanLocked(now)
	c.scheduleExpiryLocked(now)
	c.mu.Unlock()
	if changed {
		if err := c.applyPolicy(context.Background(), plan); err != nil {
			_ = pin.Release()
			return nil, err
		}
		c.publishControlSnapshotChanged()
	}
	return pin, nil
}

// PinForegroundRequest reserves a short-lived foreground turn for a trusted
// server-side request that has no client-owned presence registration. The
// caller must establish that trust before invoking this method.
func (c *surfacePresenceController) PinForegroundRequest() (*surfacePresencePin, error) {
	if c == nil {
		return nil, errPresenceForegroundNotReady
	}
	if c.initErr != nil {
		return nil, newSurfacePresenceAPIError(503, "presence_unavailable", c.initErr.Error())
	}

	now := c.now()
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errPresenceForegroundNotReady
	}
	changed := c.expireLocked(now)
	pin := &surfacePresencePin{controller: c}
	c.pins[pin] = struct{}{}
	changed = c.refreshPolicyLocked() || changed
	plan := c.policyPlanLocked(now)
	c.scheduleExpiryLocked(now)
	c.mu.Unlock()

	if changed {
		if err := c.applyPolicy(context.Background(), plan); err != nil {
			_ = pin.Release()
			return nil, err
		}
		c.publishControlSnapshotChanged()
	}

	c.mu.Lock()
	ready := !c.closed && c.appliedRevision >= c.stateRevision && (c.runtime == nil || c.playbackStopConfirmed)
	c.mu.Unlock()
	if !ready {
		_ = pin.Release()
		return nil, errPresenceForegroundNotReady
	}
	return pin, nil
}

func (c *surfacePresenceController) releaseForegroundPin(pin *surfacePresencePin) error {
	if c == nil {
		return nil
	}
	now := c.now()
	c.mu.Lock()
	if c.closed {
		delete(c.pins, pin)
		c.mu.Unlock()
		return nil
	}
	changed := c.expireLocked(now)
	if _, exists := c.pins[pin]; !exists {
		c.mu.Unlock()
		return nil
	}
	delete(c.pins, pin)
	policyChangedNow := c.refreshPolicyLocked()
	changed = policyChangedNow || changed
	plan := c.policyPlanLocked(now)
	c.scheduleExpiryLocked(now)
	c.mu.Unlock()
	if changed {
		err := c.applyPolicy(context.Background(), plan)
		c.publishControlSnapshotChanged()
		return err
	}
	return nil
}

func (c *surfacePresenceController) ValidatePlayback(identity surfacePresenceIdentity, runtimeEpoch, receipt string, claimSeq, expectedStateRevision uint64) (response surfacePresenceV2Response, resultErr error) {
	defer func() { c.projectRuntimeActivity(&response) }()
	if c == nil {
		return surfacePresenceV2Response{}, errPresenceRegistrationRequired
	}
	now := c.now()
	c.mu.Lock()
	changed := c.expireLocked(now)
	registration := c.registrations[identity]
	if c.closed || runtimeEpoch != c.runtimeEpoch || registration == nil || registration.receipt != receipt || !registration.retiredAt.IsZero() || !registration.expiresAt.After(now) || registration.activeSurface != "idlechat" || registration.claimSeq != claimSeq {
		plan := c.policyPlanLocked(now)
		c.scheduleExpiryLocked(now)
		c.mu.Unlock()
		if changed {
			_ = c.applyPolicy(context.Background(), plan)
		}
		return surfacePresenceV2Response{}, errPresenceRegistrationRequired
	}
	response = c.snapshotLocked(now, &identity, registration)
	plan := c.policyPlanLocked(now)
	c.scheduleExpiryLocked(now)
	c.mu.Unlock()
	if changed {
		if err := c.applyPolicy(context.Background(), plan); err != nil {
			return response, err
		}
		response = c.snapshotForRegistration(identity)
	}
	if expectedStateRevision != response.StateRevision || !response.PlaybackAllowed {
		return response, errPresencePlaybackChanged
	}
	return response, nil
}

func (c *surfacePresenceController) MarkPlaybackStarted(identity surfacePresenceIdentity, runtimeEpoch, receipt string, claimSeq, expectedStateRevision uint64) error {
	if c == nil {
		return errPresenceRegistrationRequired
	}
	now := c.now()
	c.mu.Lock()
	changed := c.expireLocked(now)
	registration := c.registrations[identity]
	valid := !c.closed && runtimeEpoch == c.runtimeEpoch && registration != nil && registration.receipt == receipt && registration.retiredAt.IsZero() && registration.expiresAt.After(now) && registration.activeSurface == "idlechat" && registration.claimSeq == claimSeq
	response := c.snapshotLocked(now, &identity, registration)
	plan := c.policyPlanLocked(now)
	c.scheduleExpiryLocked(now)
	if !valid || expectedStateRevision != response.StateRevision || !response.PlaybackAllowed {
		c.mu.Unlock()
		if err := c.stopPlaybackForPlan(context.Background(), plan, "stale_playback_activation"); err == nil {
			c.markPolicyApplied(plan, false)
		} else if changed {
			_ = c.applyPolicy(context.Background(), plan)
		}
		return errPresencePlaybackChanged
	}
	c.runtimeActive = true
	c.mu.Unlock()
	if changed {
		if err := c.applyPolicy(context.Background(), plan); err != nil {
			return err
		}
	}
	c.publishControlSnapshotChanged()
	return nil
}

func (c *surfacePresenceController) StartExplicit() error {
	if c == nil || c.runtime == nil {
		return fmt.Errorf("idlechat surface presence is unavailable")
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return fmt.Errorf("surface presence is closed")
	}
	_, foregroundCount, v2IdleCount := c.countsLocked(c.now())
	if foregroundCount > 0 {
		c.mu.Unlock()
		return errChatSurfacePresent
	}
	if v2IdleCount == 0 {
		c.mu.Unlock()
		return errPresenceRegistrationRequired
	}
	c.mu.Unlock()
	return errPresenceManualStartDisabled
}

func (c *surfacePresenceController) StopExplicit() {
	defer func() { c.publishControlSnapshotChanged() }()
	if c == nil || c.runtime == nil {
		return
	}
	c.mu.Lock()
	if c.closed || c.runtime == nil {
		c.mu.Unlock()
		return
	}
	c.stateRevision++
	plan := c.policyPlanLocked(c.now())
	plan.revision = c.stateRevision
	c.runtimeActive = false
	c.playbackStopConfirmed = false
	c.mu.Unlock()
	if err := c.stopPlaybackForPlan(context.Background(), plan, "explicit_stop"); err != nil {
		log.Printf("[presence] explicit IdleChat stop was not confirmed: %v", err)
		return
	}
	c.markPolicyApplied(plan, false)
}

func (c *surfacePresenceController) Close() {
	defer func() { c.publishControlSnapshotChanged() }()
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.timerGeneration++
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	c.runtimeActive = false
	c.pins = make(map[*surfacePresencePin]struct{})
	c.mu.Unlock()
}

func (c *surfacePresenceController) applyPolicy(ctx context.Context, plan surfacePresencePolicyPlan) error {
	c.mu.Lock()
	if c.closed || plan.revision != c.stateRevision {
		c.mu.Unlock()
		return nil
	}
	runtime := c.runtime
	c.mu.Unlock()
	if runtime == nil {
		c.markPolicyApplied(plan, false)
		return nil
	}

	active := false
	c.mu.Lock()
	needsStop := !c.playbackStopConfirmed || c.runtimeActive
	c.mu.Unlock()
	switch plan.target {
	case "foreground":
		if err := c.stopPlaybackForPlan(ctx, plan, "foreground_presence"); err != nil {
			return err
		}
		active = false
	case "idlechat":
		if needsStop {
			if err := c.stopPlaybackForPlan(ctx, plan, "idlechat_eligibility"); err != nil {
				return err
			}
		}
		active = false
	case "none":
		if needsStop {
			if err := c.stopPlaybackForPlan(ctx, plan, "presence_released"); err != nil {
				return err
			}
		}
		active = false
	}
	c.markPolicyApplied(plan, active)
	return nil
}

func (c *surfacePresenceController) stopPlaybackForPlan(ctx context.Context, plan surfacePresencePolicyPlan, reason string) error {
	c.mu.Lock()
	runtime := c.runtime
	c.mu.Unlock()
	if runtime == nil {
		c.markPolicyApplied(plan, false)
		return nil
	}
	receipt, err := runtime.StopPlaybackForForeground(ctx, idlechat.PlaybackStopRequest{
		RuntimeEpoch:  plan.runtimeEpoch,
		StateRevision: plan.revision,
		Reason:        reason,
	})
	if err != nil {
		return fmt.Errorf("stop IdleChat playback for %s: %w", reason, err)
	}
	if receipt.RuntimeEpoch != plan.runtimeEpoch || receipt.AppliedStateRevision < plan.revision || !receipt.ResumeBlocked {
		return newSurfacePresenceAPIError(503, "runtime_stop_unconfirmed", "IdleChat did not confirm the current playback policy")
	}
	c.mu.Lock()
	if c.policyPlanMatchesLocked(plan) {
		c.playbackStopConfirmed = true
		c.runtimeActive = false
		c.appliedRevision = c.stateRevision
	}
	c.mu.Unlock()
	return nil
}

func (c *surfacePresenceController) markPolicyApplied(plan surfacePresencePolicyPlan, active bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.policyPlanMatchesLocked(plan) {
		return
	}
	c.appliedRevision = c.stateRevision
	c.runtimeActive = active
}

func (c *surfacePresenceController) expireLeases(generation uint64) {
	c.mu.Lock()
	if c.closed || generation != c.timerGeneration {
		c.mu.Unlock()
		return
	}
	now := c.now()
	changed := c.expireLocked(now)
	plan := c.policyPlanLocked(now)
	c.scheduleExpiryLocked(now)
	c.mu.Unlock()
	if changed {
		if err := c.applyPolicy(context.Background(), plan); err != nil {
			log.Printf("[presence] surface lease reconciliation failed: %v", err)
		}
	}
}

func (c *surfacePresenceController) expireLocked(now time.Time) bool {
	for key, expiresAt := range c.legacyLeases {
		if !expiresAt.After(now) {
			delete(c.legacyLeases, key)
		}
	}
	for _, registration := range c.registrations {
		if registration.retiredAt.IsZero() && !registration.expiresAt.After(now) {
			registration.activeSurface = ""
			registration.expiresAt = time.Time{}
			registration.retiredAt = now
		}
	}
	c.pruneRetiredLocked(now)
	return c.refreshPolicyLocked()
}

func (c *surfacePresenceController) pruneRetiredLocked(now time.Time) {
	for identity, registration := range c.registrations {
		if !registration.retiredAt.IsZero() && now.Sub(registration.retiredAt) >= surfacePresenceRetiredCacheTTL {
			delete(c.registrations, identity)
		}
	}
}

func (c *surfacePresenceController) refreshPolicyLocked() bool {
	_, foregroundCount, v2IdleCount := c.countsLocked(c.now())
	idleVisible := v2IdleCount > 0
	foregroundPresent := foregroundCount > 0
	if c.policyInitialized && idleVisible == c.policyIdleVisible && foregroundPresent == c.policyForeground {
		return false
	}
	if foregroundPresent && !c.policyForeground {
		c.playbackStopConfirmed = false
	}
	c.policyInitialized = true
	c.policyIdleVisible = idleVisible
	c.policyForeground = foregroundPresent
	c.stateRevision++
	return true
}

func (c *surfacePresenceController) policyPlanLocked(now time.Time) surfacePresencePolicyPlan {
	_, foregroundCount, v2IdleCount := c.countsLocked(now)
	target := "none"
	if foregroundCount > 0 {
		target = "foreground"
	} else if v2IdleCount > 0 {
		target = "idlechat"
	}
	return surfacePresencePolicyPlan{
		revision:     c.stateRevision,
		runtimeEpoch: c.runtimeEpoch,
		target:       target,
		idleVisible:  v2IdleCount > 0,
		foreground:   foregroundCount > 0,
	}
}

func (c *surfacePresenceController) policyPlanMatchesLocked(plan surfacePresencePolicyPlan) bool {
	if c.closed || c.runtimeEpoch != plan.runtimeEpoch || plan.revision != c.stateRevision || c.policyIdleVisible != plan.idleVisible || c.policyForeground != plan.foreground {
		return false
	}
	currentTarget := "none"
	if c.policyForeground {
		currentTarget = "foreground"
	} else if c.policyIdleVisible {
		currentTarget = "idlechat"
	}
	return currentTarget == plan.target
}

func (c *surfacePresenceController) countsLocked(now time.Time) (idleChatCount, foregroundCount, v2IdleCount int) {
	for _, registration := range c.registrations {
		if !registration.retiredAt.IsZero() || !registration.expiresAt.After(now) || registration.activeSurface == "" {
			continue
		}
		if registration.activeSurface == "idlechat" {
			idleChatCount++
			v2IdleCount++
		} else {
			foregroundCount++
		}
	}
	for key, expiresAt := range c.legacyLeases {
		if !expiresAt.After(now) {
			continue
		}
		if key.surface == "idlechat" {
			idleChatCount++
		} else {
			foregroundCount++
		}
	}
	foregroundCount += len(c.pins)
	return idleChatCount, foregroundCount, v2IdleCount
}

func (c *surfacePresenceController) snapshotLocked(now time.Time, identity *surfacePresenceIdentity, registration *surfacePresenceRegistration) surfacePresenceV2Response {
	idleCount, foregroundCount, v2IdleCount := c.countsLocked(now)
	effectiveMode := "none"
	if foregroundCount > 0 {
		effectiveMode = "chat"
	} else if v2IdleCount > 0 {
		effectiveMode = "idlechat"
	}
	playbackAllowed := !c.closed && v2IdleCount > 0 && foregroundCount == 0 && c.runtime != nil && c.playbackStopConfirmed && c.appliedRevision >= c.stateRevision
	playbackState := "blocked"
	if c.runtime == nil {
		playbackState = "disabled"
	} else if playbackAllowed {
		playbackState = "stopped"
		if c.runtimeActive {
			playbackState = "playing"
		}
	}
	foregroundReady := c.runtime == nil
	if !c.closed && identity != nil && registration != nil && registration.retiredAt.IsZero() && registration.expiresAt.After(now) && registration.activeSurface != "" && registration.activeSurface != "idlechat" {
		foregroundReady = c.runtime == nil || (c.playbackStopConfirmed && c.appliedRevision >= c.stateRevision)
	} else {
		foregroundReady = false
	}
	response := surfacePresenceV2Response{
		OK:                      true,
		ProtocolVersion:         surfacePresenceProtocolVersionV2,
		RuntimeEpoch:            c.runtimeEpoch,
		StateRevision:           c.stateRevision,
		EffectiveMode:           effectiveMode,
		PlaybackState:           playbackState,
		PlaybackAllowed:         playbackAllowed,
		PlaybackActive:          c.runtimeActive,
		ForegroundReady:         foregroundReady,
		ForegroundPresenceCount: foregroundCount - len(c.pins),
		ForegroundTurnCount:     len(c.pins),
		IdleChatPresenceCount:   idleCount,
		GenerationState:         "unknown",
		BackendResourceState:    "unknown",
		IdleChatActive:          c.runtimeActive,
		ChatPresenceCount:       foregroundCount,
	}
	if identity != nil && registration != nil {
		response.RegistrationReceipt = registration.receipt
		response.AcceptedUpdateSeq = registration.lastUpdateSeq
		response.CurrentClaimSeq = registration.claimSeq
		leaseExpiry := registration.expiresAt
		if !leaseExpiry.IsZero() {
			response.LeaseExpiresAt = &leaseExpiry
		}
	}
	return response
}

func (c *surfacePresenceController) registrationResponseLocked(now time.Time, identity surfacePresenceIdentity, registration *surfacePresenceRegistration) surfacePresenceV2Response {
	response := c.snapshotLocked(now, &identity, registration)
	registrationExpiry := registration.expiresAt
	response.RegistrationExpiresAt = &registrationExpiry
	response.LastUpdateSeq = registration.lastUpdateSeq
	return response
}

func (c *surfacePresenceController) snapshotForRegistration(identity surfacePresenceIdentity) surfacePresenceV2Response {
	c.mu.Lock()
	registration := c.registrations[identity]
	if registration == nil {
		c.mu.Unlock()
		return surfacePresenceV2Response{}
	}
	response := c.snapshotLocked(c.now(), &identity, registration)
	response.AcceptedUpdateSeq = registration.lastUpdateSeq
	c.mu.Unlock()
	c.projectRuntimeActivity(&response)
	return response
}

func (c *surfacePresenceController) snapshotForLegacy() surfacePresenceV2Response {
	c.mu.Lock()
	response := c.snapshotLocked(c.now(), nil, nil)
	c.mu.Unlock()
	c.projectRuntimeActivity(&response)
	return response
}

func (c *surfacePresenceController) setControlSnapshotPublisher(publisher func(viewer.IdleChatControlSnapshot)) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.controlPublisher = publisher
	c.mu.Unlock()
	c.refreshControlSnapshot(true)
}

func (c *surfacePresenceController) idleChatControlSnapshot() (viewer.IdleChatControlSnapshot, bool) {
	return c.refreshControlSnapshot(false)
}

func (c *surfacePresenceController) publishControlSnapshotChanged() {
	if c == nil {
		return
	}
	_, _ = c.refreshControlSnapshot(false)
}

func (c *surfacePresenceController) refreshControlSnapshot(force bool) (viewer.IdleChatControlSnapshot, bool) {
	return c.refreshControlSnapshotAt(force, 0, false)
}

func (c *surfacePresenceController) refreshControlSnapshotAt(force bool, expectedRevision uint64, hasExpectedRevision bool) (viewer.IdleChatControlSnapshot, bool) {
	if c == nil {
		return viewer.IdleChatControlSnapshot{}, false
	}

	c.mu.Lock()
	runtime := c.runtime
	runtimeEpoch := c.runtimeEpoch
	publisher := c.controlPublisher
	readRevision := c.stateRevision
	c.mu.Unlock()
	if hasExpectedRevision && expectedRevision != readRevision {
		return viewer.IdleChatControlSnapshot{}, false
	}

	owner := idlechat.PlaybackControlSnapshot{
		RuntimeEpoch:         runtimeEpoch,
		GenerationState:      "unknown",
		BackendResourceState: "unknown",
	}
	activityObserved := false
	if runtime != nil {
		if reader, ok := runtime.(surfacePresencePlaybackControlRuntime); ok {
			owner = reader.PlaybackControlSnapshot()
			if owner.RuntimeEpoch == "" || owner.RuntimeEpoch != runtimeEpoch {
				return viewer.IdleChatControlSnapshot{}, false
			}
			activityObserved = true
		} else if activity, ok := runtime.(surfacePresenceRuntimeActivity); ok {
			owner.PlaybackActive = activity.IsChatActive() || activity.IsManualMode()
			activityObserved = true
		}
	}
	owner.BackendResourceState = "unknown"

	c.controlMu.Lock()
	c.mu.Lock()
	if c.stateRevision != readRevision {
		latest := c.lastControlSnapshot
		hasLatest := c.hasControlSnapshot
		c.mu.Unlock()
		c.controlMu.Unlock()
		return latest, hasLatest
	}
	policySettled := c.appliedRevision == 0 || c.appliedRevision >= c.stateRevision
	if c.hasControlSnapshot && c.lastControlSnapshot.CleanupPending != owner.CleanupPending && policySettled {
		previousRevision := c.stateRevision
		c.stateRevision++
		if c.playbackStopConfirmed && c.appliedRevision >= previousRevision {
			c.appliedRevision = c.stateRevision
		}
	}
	response := c.snapshotLocked(c.now(), nil, nil)
	snapshot := viewer.IdleChatControlSnapshot{
		RuntimeEpoch:         response.RuntimeEpoch,
		StateRevision:        response.StateRevision,
		PlaybackGeneration:   owner.PlaybackGeneration,
		PlaybackState:        response.PlaybackState,
		PlaybackAllowed:      response.PlaybackAllowed,
		PlaybackActive:       owner.PlaybackActive,
		CleanupPending:       owner.CleanupPending,
		GenerationState:      owner.GenerationState,
		GenerationQuiesced:   owner.GenerationQuiesced,
		BackendResourceState: "unknown",
	}
	if snapshot.PlaybackActive {
		snapshot.PlaybackState = "playing"
	} else if activityObserved {
		if response.PlaybackAllowed {
			snapshot.PlaybackState = "stopped"
		} else {
			snapshot.PlaybackState = "blocked"
		}
	}
	changed := !c.hasControlSnapshot || snapshot != c.lastControlSnapshot
	c.lastControlSnapshot = snapshot
	c.hasControlSnapshot = true
	c.mu.Unlock()
	c.controlMu.Unlock()

	if publisher != nil && (force || changed) {
		publisher(snapshot)
	}
	return snapshot, true
}

// projectRuntimeActivity reads the IdleChat owner's existing in-memory state
// after releasing the presence mutex. A concurrent policy revision invalidates
// this projection; callers use state_revision to discard such stale snapshots.
// The cached runtimeActive bit remains a conservative stop-needed hint only.
func (c *surfacePresenceController) projectRuntimeActivity(response *surfacePresenceV2Response) {
	if c == nil || response == nil {
		return
	}
	responseRevision := response.StateRevision
	snapshot, ok := c.refreshControlSnapshot(false)
	if !ok || snapshot.StateRevision != responseRevision {
		return
	}
	c.mu.Lock()
	if c.closed || snapshot.RuntimeEpoch != c.runtimeEpoch || snapshot.StateRevision != c.stateRevision {
		c.mu.Unlock()
		return
	}
	response.RuntimeEpoch = snapshot.RuntimeEpoch
	response.StateRevision = snapshot.StateRevision
	response.PlaybackState = snapshot.PlaybackState
	response.PlaybackAllowed = snapshot.PlaybackAllowed
	response.PlaybackActive = snapshot.PlaybackActive
	response.PlaybackGeneration = snapshot.PlaybackGeneration
	response.CleanupPending = snapshot.CleanupPending
	response.GenerationState = snapshot.GenerationState
	response.GenerationQuiesced = snapshot.GenerationQuiesced
	response.BackendResourceState = snapshot.BackendResourceState
	response.IdleChatActive = snapshot.PlaybackActive
	if response.ForegroundReady && c.runtime != nil {
		response.ForegroundReady = c.playbackStopConfirmed && c.appliedRevision >= c.stateRevision
	}
	c.mu.Unlock()
}

func (c *surfacePresenceController) scheduleExpiryLocked(now time.Time) {
	c.timerGeneration++
	generation := c.timerGeneration
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	if c.closed {
		return
	}
	var earliest time.Time
	for _, expiresAt := range c.legacyLeases {
		if earliest.IsZero() || expiresAt.Before(earliest) {
			earliest = expiresAt
		}
	}
	for _, registration := range c.registrations {
		expiresAt := registration.expiresAt
		if !registration.retiredAt.IsZero() {
			expiresAt = registration.retiredAt.Add(surfacePresenceRetiredCacheTTL)
		}
		if !expiresAt.IsZero() && (earliest.IsZero() || expiresAt.Before(earliest)) {
			earliest = expiresAt
		}
	}
	if earliest.IsZero() {
		return
	}
	delay := earliest.Sub(now)
	if delay < time.Millisecond {
		delay = time.Millisecond
	}
	c.timer = time.AfterFunc(delay, func() { c.expireLeases(generation) })
}

func (c *surfacePresenceController) runtimeEpochValue() string {
	if c == nil {
		return ""
	}
	return c.runtimeEpoch
}

func validateSurfacePresenceIdentity(identity surfacePresenceIdentity) error {
	if identity.clientType != "RenCrow_PORTAL" && identity.clientType != "RenCrow_CMD" && identity.clientType != "RenCrow_CORE" {
		return newSurfacePresenceAPIError(403, "client_not_allowed", "surface presence client is not allowed")
	}
	if identity.principal == "" || len(identity.principal) > 128 || !utf8.ValidString(identity.principal) || identity.viewerClientID == "" || len(identity.viewerClientID) > surfacePresenceMaxClientIDBytes || !utf8.ValidString(identity.viewerClientID) || strings.TrimSpace(identity.viewerClientID) != identity.viewerClientID {
		return newSurfacePresenceAPIError(400, "invalid_viewer_client_id", "viewer_client_id must be 1 to 128 bytes")
	}
	for _, character := range identity.principal + identity.viewerClientID {
		if character < 0x20 || character == 0x7f {
			return newSurfacePresenceAPIError(400, "invalid_identity", "surface presence identity contains control characters")
		}
	}
	return nil
}

func validSurfacePresenceViewerClientIDV2(value string) bool {
	if value == "" || len(value) > surfacePresenceMaxClientIDBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func validSurfacePresenceViewerClientID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 256 {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
