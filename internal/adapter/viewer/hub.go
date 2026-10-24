package viewer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	replayPageLimit                   = 1000
	replayTimeout                     = 30 * time.Second
	privateMioDeltaClientCapacity     = 64
	privateMioDeltaMaxClientsPerOwner = 8
	privateMioDeltaMaxContentBytes    = 16 * 1024
	guardedOwnerScopedFrameMarker     = "_rencrow_guarded_owner_publication"
)

// PreparedPrivateMioDelta is encoded outside authority guards so guarded
// publication only performs bounded nonblocking RAM fanout.
type PreparedPrivateMioDelta struct {
	ownerID string
	eventID modulecore.EventID
	data    []byte
}

// guardedOwnerScopedLiveFrame is flattened into the event JSON queued only on
// the authenticated owner's shared SSE lane. Its marker survives removal of
// the Hub's qualification map entry while a buffered frame is still draining.
// The SSE handler decodes the event and re-encodes it, so this internal marker
// is never sent on the wire.
type guardedOwnerScopedLiveFrame struct {
	orchestrator.OrchestratorEvent
	Guarded bool `json:"_rencrow_guarded_owner_publication"`
}

// PublicationCleanup releases qualification handles after a guarded RAM
// publication has left its authority boundary. The returned function is
// idempotent and must be called after turn/session guards and relay ordering
// locks are released.
type PublicationCleanup func()

// EventStream is the shared subscription and replay contract used by Viewer
// and Chrome bridge consumers.
type EventStream interface {
	Subscribe() (chan []byte, []orchestrator.OrchestratorEvent)
	Unsubscribe(chan []byte)
	Replay(context.Context, modulecore.EventSeq, []orchestrator.OrchestratorEvent, func(orchestrator.OrchestratorEvent) error) (modulecore.EventSeq, error)
}

// EventHub broadcasts already-persisted orchestrator events to connected SSE
// clients. Canonical publication is owned by the runtime event relay.
type EventHub struct {
	mu                     sync.RWMutex
	clients                map[chan []byte]struct{}
	privateMioDeltaClients map[string]map[chan []byte]struct{}
	privateMioDeltaOwners  map[chan []byte]string
	privateMioLiveClaims   map[chan []byte]map[modulecore.EventID]domainconversation.PreparedChatTurnPublicationGuard
	controlClients         map[chan []byte]chan IdleChatControlSnapshot
	controlSnapshot        IdleChatControlSnapshot
	hasControlSnapshot     bool
	controlSnapshotRead    IdleChatControlSnapshotProvider
	history                []orchestrator.OrchestratorEvent
	acceptedTail           []orchestrator.OrchestratorEvent
	acceptedGap            *EventReplayGap
	maxHist                int
	replayReader           EventReplayReader
	sessionReplayReader    SessionPublicEventReplayReader
	clientCountListener    func(int)
	lastLiveEventSeq       modulecore.EventSeq
	lastLiveEventID        modulecore.EventID
}

// EventReplaySnapshot is captured atomically with subscription. Events holds
// the bounded live history plus any admitted events retained until durable
// replay confirms their commit. AcceptedGap identifies the most recent
// admitted event evicted from that retained tail.
type EventReplaySnapshot struct {
	Events      []orchestrator.OrchestratorEvent
	AcceptedGap *EventReplayGap
}

// IdleChatControlSnapshot is the latest RAM-only playback-control projection
// shared by SSE clients. It is not a canonical event and carries no
// EventSeq/EventID, receipt, claim, or client-specific readiness data.
type IdleChatControlSnapshot struct {
	RuntimeEpoch         string `json:"runtime_epoch"`
	StateRevision        uint64 `json:"state_revision"`
	PlaybackState        string `json:"playback_state"`
	PlaybackAllowed      bool   `json:"playback_allowed"`
	PlaybackActive       bool   `json:"playback_active"`
	PlaybackGeneration   uint64 `json:"playback_generation"`
	CleanupPending       bool   `json:"cleanup_pending"`
	GenerationState      string `json:"generation_state"`
	GenerationQuiesced   bool   `json:"generation_quiesced"`
	BackendResourceState string `json:"backend_resource_state"`
}

type IdleChatControlSnapshotProvider func() (IdleChatControlSnapshot, bool)

// EventReplayGap describes accepted RAM-only history that was evicted before
// a durable replay confirmed its commit.
type EventReplayGap struct {
	EventID  modulecore.EventID
	EventSeq modulecore.EventSeq
}

// ReplayCoverageError means a captured RAM suffix cannot be proven complete
// from the durable prefix. SSE callers must send an explicit reset instead of
// silently continuing from a partial replay.
type ReplayCoverageError struct {
	After          modulecore.EventSeq
	Missing        EventReplayGap
	DurableThrough modulecore.EventSeq
}

func (e *ReplayCoverageError) Error() string {
	if e == nil {
		return "event replay coverage is unavailable"
	}
	return fmt.Sprintf("event replay after %d cannot cover admitted event %s at EventSeq %d (durable through %d)", e.After, e.Missing.EventID, e.Missing.EventSeq, e.DurableThrough)
}

var errEventHubLiveSequenceNotIncreasing = errors.New("live EventSeq is not increasing")

// NewEventHub creates a new EventHub with the given history capacity.
func NewEventHub(maxHistory int) *EventHub {
	return &EventHub{
		clients:                make(map[chan []byte]struct{}),
		privateMioDeltaClients: make(map[string]map[chan []byte]struct{}),
		privateMioDeltaOwners:  make(map[chan []byte]string),
		privateMioLiveClaims:   make(map[chan []byte]map[modulecore.EventID]domainconversation.PreparedChatTurnPublicationGuard),
		controlClients:         make(map[chan []byte]chan IdleChatControlSnapshot),
		maxHist:                maxHistory,
	}
}

// SubscribePrivateMioDeltas registers a live-only lane for one authenticated
// private Session owner. It has no history and is never part of canonical
// replay. A nil return means the owner identity was invalid.
func (h *EventHub) SubscribePrivateMioDeltas(ownerID string) chan []byte {
	if h == nil || strings.TrimSpace(ownerID) == "" || strings.TrimSpace(ownerID) != ownerID {
		return nil
	}
	ch := make(chan []byte, privateMioDeltaClientCapacity)
	h.mu.Lock()
	if !h.registerPrivateMioDeltaSubscriberLocked(ownerID, ch) {
		h.mu.Unlock()
		return nil
	}
	h.mu.Unlock()
	return ch
}

func (h *EventHub) registerPrivateMioDeltaSubscriberLocked(ownerID string, ch chan []byte) bool {
	if h.privateMioDeltaClients == nil {
		h.privateMioDeltaClients = make(map[string]map[chan []byte]struct{})
	}
	if h.privateMioDeltaOwners == nil {
		h.privateMioDeltaOwners = make(map[chan []byte]string)
	}
	if h.privateMioDeltaClients[ownerID] == nil {
		h.privateMioDeltaClients[ownerID] = make(map[chan []byte]struct{})
	}
	if len(h.privateMioDeltaClients[ownerID]) >= privateMioDeltaMaxClientsPerOwner {
		return false
	}
	h.privateMioDeltaClients[ownerID][ch] = struct{}{}
	h.privateMioDeltaOwners[ch] = ownerID
	return true
}

// UnsubscribePrivateMioDeltas removes and closes one owner-specific live lane.
func (h *EventHub) UnsubscribePrivateMioDeltas(ownerID string, ch chan []byte) {
	if h == nil || ch == nil {
		return
	}
	h.mu.Lock()
	if h.privateMioDeltaOwners[ch] != ownerID {
		h.mu.Unlock()
		return
	}
	claims := h.removeSubscriberLocked(ch)
	h.mu.Unlock()
	releasePublicationClaims(claims)
}

// PreparePrivateMioDelta validates and encodes one live-only delta outside
// Session and prepared-turn authority guards.
func (h *EventHub) PreparePrivateMioDelta(ownerID string, event orchestrator.OrchestratorEvent) (PreparedPrivateMioDelta, error) {
	if h == nil {
		return PreparedPrivateMioDelta{}, errors.New("event hub is required")
	}
	if strings.TrimSpace(ownerID) == "" || strings.TrimSpace(ownerID) != ownerID {
		return PreparedPrivateMioDelta{}, errors.New("authenticated private Session owner is required")
	}
	if event.Type != "agent.response_delta" || event.From != "mio" || event.To != "user" || event.Route != "CHAT" ||
		event.EventSeq != 0 || event.Channel != "viewer" || event.ChannelAddress != "cmd:"+ownerID ||
		event.ActorKind != "agent" || event.ActorID != "mio" || len(event.Content) > privateMioDeltaMaxContentBytes ||
		event.EventID.Validate() != nil || event.TraceID.Validate() != nil || event.TaskID.Validate() != nil ||
		event.RunID.Validate() != nil || event.SessionID.Validate() != nil || event.TurnID.Validate() != nil || event.MessageID.Validate() != nil || event.Content == "" {
		return PreparedPrivateMioDelta{}, errors.New("private Mio answer delta identity is invalid")
	}
	data, err := marshalGuardedOwnerScopedLiveFrame(event)
	if err != nil {
		return PreparedPrivateMioDelta{}, fmt.Errorf("marshal private Mio answer delta: %w", err)
	}
	return PreparedPrivateMioDelta{ownerID: ownerID, eventID: event.EventID, data: data}, nil
}

// PublishPreparedPrivateMioDelta performs the final RAM-only, nonblocking
// fanout into the owner's shared canonical/live FIFO. The qualification is
// retained per subscriber and checked at the writer claim point. This method
// consumes qualification on success, failure, and zero-subscriber paths.
func (h *EventHub) PublishPreparedPrivateMioDelta(ownerID string, prepared PreparedPrivateMioDelta, qualification domainconversation.PreparedChatTurnPublicationGuard) error {
	if qualification != nil {
		defer qualification.Release()
	}
	if h == nil {
		return errors.New("event hub is required")
	}
	if strings.TrimSpace(ownerID) == "" || strings.TrimSpace(ownerID) != ownerID || prepared.ownerID != ownerID || prepared.eventID.Validate() != nil || len(prepared.data) == 0 || len(prepared.data) > 128*1024 || qualification == nil {
		return errors.New("prepared private Mio delta owner is invalid")
	}
	var claims []domainconversation.PreparedChatTurnPublicationGuard
	h.mu.Lock()
	for ch := range h.privateMioDeltaClients[ownerID] {
		claim := qualification.Retain()
		if claim == nil {
			continue
		}
		select {
		case ch <- prepared.data:
			if !h.addPrivateMioLiveClaimLocked(ch, prepared.eventID, claim) {
				claims = append(claims, claim)
				claims = append(claims, h.removeSubscriberLocked(ch)...)
			}
		default:
			claims = append(claims, claim)
			claims = append(claims, h.removeSubscriberLocked(ch)...)
		}
	}
	if len(h.privateMioDeltaClients[ownerID]) == 0 {
		delete(h.privateMioDeltaClients, ownerID)
	}
	h.mu.Unlock()
	releasePublicationClaims(claims)
	return nil
}

// PublishAcceptedWithClaim admits a canonical private Mio response into the
// same EventSeq/history path as other accepted events, but queues its live
// frame only for the authenticated owner's private SSE subscribers. Each
// pending frame carries a retained writer-claim qualification. The caller must
// invoke the returned cleanup after leaving all authority and relay-order locks.
func (h *EventHub) PublishAcceptedWithClaim(ev orchestrator.OrchestratorEvent, ownerID string, qualification domainconversation.PreparedChatTurnPublicationGuard) (bool, PublicationCleanup, error) {
	cleanup := deferredPublicationCleanup(qualification, nil)
	if h == nil {
		return false, cleanup, errors.New("event hub is required")
	}
	if qualification == nil || !isPrivateMioFinalForOwner(ev, ownerID) {
		return false, cleanup, errors.New("private Mio final publication qualification or envelope is invalid")
	}
	data, err := marshalGuardedOwnerScopedLiveFrame(ev)
	if err != nil {
		return false, cleanup, fmt.Errorf("marshal accepted private Mio final event: %w", err)
	}
	var claims []domainconversation.PreparedChatTurnPublicationGuard
	h.mu.Lock()
	if ev.EventSeq < h.lastLiveEventSeq {
		last := h.lastLiveEventSeq
		h.mu.Unlock()
		return false, cleanup, fmt.Errorf("%w: got %d after %d", errEventHubLiveSequenceNotIncreasing, ev.EventSeq, last)
	}
	if ev.EventSeq == h.lastLiveEventSeq {
		lastID := h.lastLiveEventID
		h.mu.Unlock()
		if ev.EventID == lastID {
			return false, cleanup, nil
		}
		return false, cleanup, fmt.Errorf("%w: EventSeq %d already belongs to event %s", errEventHubLiveSequenceNotIncreasing, ev.EventSeq, lastID)
	}
	h.lastLiveEventSeq = ev.EventSeq
	h.lastLiveEventID = ev.EventID
	h.retainAcceptedLocked(ev)
	h.appendHistoryLocked(ev)
	for ch := range h.privateMioDeltaClients[ownerID] {
		claim := qualification.Retain()
		if claim == nil {
			continue
		}
		select {
		case ch <- data:
			if !h.addPrivateMioLiveClaimLocked(ch, ev.EventID, claim) {
				claims = append(claims, claim)
				claims = append(claims, h.removeSubscriberLocked(ch)...)
			}
		default:
			claims = append(claims, claim)
			claims = append(claims, h.removeSubscriberLocked(ch)...)
		}
	}
	clientCount := len(h.clients)
	h.mu.Unlock()
	cleanup = deferredPublicationCleanup(qualification, claims)
	var finish sync.Once
	return true, func() {
		finish.Do(func() {
			cleanup()
			h.logPublishedEvent(ev, len(claims) > 0, clientCount)
		})
	}, nil
}

func (h *EventHub) addPrivateMioLiveClaimLocked(ch chan []byte, eventID modulecore.EventID, claim domainconversation.PreparedChatTurnPublicationGuard) bool {
	if h.privateMioLiveClaims == nil {
		h.privateMioLiveClaims = make(map[chan []byte]map[modulecore.EventID]domainconversation.PreparedChatTurnPublicationGuard)
	}
	if h.privateMioLiveClaims[ch] == nil {
		h.privateMioLiveClaims[ch] = make(map[modulecore.EventID]domainconversation.PreparedChatTurnPublicationGuard)
	}
	if _, exists := h.privateMioLiveClaims[ch][eventID]; exists {
		return false
	}
	h.privateMioLiveClaims[ch][eventID] = claim
	return true
}

func releasePublicationClaims(claims []domainconversation.PreparedChatTurnPublicationGuard) {
	for _, claim := range claims {
		if claim != nil {
			claim.Release()
		}
	}
}

func marshalGuardedOwnerScopedLiveFrame(event orchestrator.OrchestratorEvent) ([]byte, error) {
	return json.Marshal(guardedOwnerScopedLiveFrame{OrchestratorEvent: event, Guarded: true})
}

func isGuardedOwnerScopedLiveFrame(data []byte) bool {
	var marker struct {
		Guarded bool `json:"_rencrow_guarded_owner_publication"`
	}
	return json.Unmarshal(data, &marker) == nil && marker.Guarded
}

func deferredPublicationCleanup(producer domainconversation.PreparedChatTurnPublicationGuard, claims []domainconversation.PreparedChatTurnPublicationGuard) PublicationCleanup {
	owned := make([]domainconversation.PreparedChatTurnPublicationGuard, 0, len(claims)+1)
	if producer != nil {
		owned = append(owned, producer)
	}
	owned = append(owned, claims...)
	var once sync.Once
	return func() {
		once.Do(func() { releasePublicationClaims(owned) })
	}
}

// ClaimOwnerScopedLiveEvent validates one queued owner-scoped Mio frame at
// the last point before socket write. The authority guard is released before
// this method returns, so callers must perform socket I/O only afterward.
func (h *EventHub) ClaimOwnerScopedLiveEvent(ownerID string, ch chan []byte, data []byte) bool {
	claimed, _ := h.ClaimOwnerScopedLiveEventStatus(ownerID, ch, data)
	return claimed
}

// ClaimOwnerScopedLiveEventStatus validates one owner-scoped canonical final
// or transient delta at the writer boundary. ticketFound distinguishes an
// unguarded canonical event (which still passed the route's owner/incarnation
// filter) from a guarded event whose retained qualification was revoked.
func (h *EventHub) ClaimOwnerScopedLiveEventStatus(ownerID string, ch chan []byte, data []byte) (claimed, ticketFound bool) {
	if h == nil || ch == nil || strings.TrimSpace(ownerID) == "" || strings.TrimSpace(ownerID) != ownerID {
		return false, false
	}
	guardedFrame := isGuardedOwnerScopedLiveFrame(data)
	var event orchestrator.OrchestratorEvent
	if err := json.Unmarshal(data, &event); err != nil || !isOwnerScopedMioLiveEventForOwner(event, ownerID) {
		h.DiscardOwnerScopedLiveEvent(ownerID, ch, data)
		return false, guardedFrame
	}
	h.mu.Lock()
	if h.privateMioDeltaOwners[ch] != ownerID {
		h.mu.Unlock()
		return false, guardedFrame
	}
	var claim domainconversation.PreparedChatTurnPublicationGuard
	claim = h.privateMioLiveClaims[ch][event.EventID]
	ticketFound = guardedFrame || claim != nil
	delete(h.privateMioLiveClaims[ch], event.EventID)
	if len(h.privateMioLiveClaims[ch]) == 0 {
		delete(h.privateMioLiveClaims, ch)
	}
	h.mu.Unlock()
	if claim == nil {
		return false, ticketFound
	}
	claimed = false
	err := claim.Use(func() error {
		claimed = true
		return nil
	})
	claim.Release()
	return err == nil && claimed, true
}

// DiscardOwnerScopedLiveEvent releases a pending ticket for a frame that the
// SSE policy or cursor suppresses before the writer claim point.
func (h *EventHub) DiscardOwnerScopedLiveEvent(ownerID string, ch chan []byte, data []byte) {
	if h == nil || ch == nil {
		return
	}
	var event struct {
		EventID modulecore.EventID `json:"event_id"`
	}
	if json.Unmarshal(data, &event) != nil || event.EventID.Validate() != nil {
		return
	}
	h.mu.Lock()
	if h.privateMioDeltaOwners[ch] != ownerID {
		h.mu.Unlock()
		return
	}
	claim := h.privateMioLiveClaims[ch][event.EventID]
	delete(h.privateMioLiveClaims[ch], event.EventID)
	if len(h.privateMioLiveClaims[ch]) == 0 {
		delete(h.privateMioLiveClaims, ch)
	}
	h.mu.Unlock()
	if claim != nil {
		claim.Release()
	}
}

func isOwnerScopedMioLiveEventForOwner(event orchestrator.OrchestratorEvent, ownerID string) bool {
	if isPrivateMioDeltaForOwner(event, ownerID) {
		return true
	}
	return isPrivateMioFinalForOwner(event, ownerID)
}

func isPrivateMioFinalForOwner(event orchestrator.OrchestratorEvent, ownerID string) bool {
	return strings.TrimSpace(ownerID) != "" && strings.TrimSpace(ownerID) == ownerID &&
		event.Type == "agent.response" && event.From == "mio" && event.To == "user" && event.Route == "CHAT" &&
		event.EventSeq > 0 && event.Channel == "viewer" && event.ChannelAddress == "cmd:"+ownerID &&
		event.ActorKind == "agent" && event.ActorID == "mio" && event.Visibility == "private" &&
		strings.TrimSpace(event.PublicationIncarnation) != "" && strings.TrimSpace(event.PublicationIncarnation) == event.PublicationIncarnation &&
		event.EventID.Validate() == nil &&
		event.TraceID.Validate() == nil && event.TaskID.Validate() == nil && event.RunID.Validate() == nil &&
		event.SessionID.Validate() == nil && event.TurnID.Validate() == nil && event.MessageID.Validate() == nil && event.Content != ""
}

// OnEvent projects an event to the Viewer history and SSE clients.
func (h *EventHub) OnEvent(ev orchestrator.OrchestratorEvent) {
	if ev.Type == "agent.response_delta" {
		log.Printf("[EventHub] OnEvent: transient Mio answer delta requires owner-scoped publication")
		return
	}
	if ev.EventSeq > 0 {
		if _, err := h.PublishLive(ev); err != nil {
			log.Printf("[EventHub] OnEvent: live sequence rejected: %v", err)
		}
		return
	}
	data, err := json.Marshal(ev)
	if err != nil {
		log.Printf("[EventHub] OnEvent: marshal error: %v", err)
		return
	}

	h.mu.Lock()
	clientDropped, droppedClaims := h.appendEventLocked(ev, data)
	clientCount := len(h.clients)
	h.mu.Unlock()
	releasePublicationClaims(droppedClaims)
	h.logPublishedEvent(ev, clientDropped, clientCount)
}

// PublishIdleChatControlSnapshot replaces the latest RAM-only control state
// and non-blockingly fans it out. A slow subscriber keeps only the newest
// state; control updates never enter canonical history or consume EventSeq.
func (h *EventHub) PublishIdleChatControlSnapshot(snapshot IdleChatControlSnapshot) {
	if h == nil || snapshot.RuntimeEpoch == "" {
		return
	}
	h.mu.Lock()
	if h.hasControlSnapshot && h.controlSnapshot.RuntimeEpoch == snapshot.RuntimeEpoch {
		if !shouldReplaceIdleChatControlSnapshot(h.controlSnapshot, snapshot) {
			h.mu.Unlock()
			return
		}
	}
	h.publishIdleChatControlSnapshotLocked(snapshot)
	h.mu.Unlock()
}

func shouldReplaceIdleChatControlSnapshot(current, next IdleChatControlSnapshot) bool {
	if next.RuntimeEpoch != current.RuntimeEpoch || next.StateRevision < current.StateRevision || next.PlaybackGeneration < current.PlaybackGeneration || next == current {
		return false
	}
	if next.StateRevision == current.StateRevision && next.PlaybackGeneration == current.PlaybackGeneration {
		// A same-generation start can publish its inactive preparation snapshot
		// after the active state. Keep the active state until a newer policy
		// revision or generation proves that playback stopped.
		if current.PlaybackActive && !next.PlaybackActive && !next.CleanupPending {
			return false
		}
		if current.CleanupPending && !next.CleanupPending {
			return false
		}
	}
	return true
}

func (h *EventHub) publishIdleChatControlSnapshotLocked(snapshot IdleChatControlSnapshot) {
	h.controlSnapshot = snapshot
	h.hasControlSnapshot = true
	for _, ch := range h.controlClients {
		select {
		case ch <- snapshot:
		default:
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- snapshot:
			default:
			}
		}
	}
}

// SetIdleChatControlSnapshotProvider installs a short RAM-only refresh hook.
// It is called outside the EventHub lock when a control SSE client connects.
func (h *EventHub) SetIdleChatControlSnapshotProvider(provider IdleChatControlSnapshotProvider) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.controlSnapshotRead = provider
	h.mu.Unlock()
}

// PublishLive appends one canonical sequenced event and fans it out only when
// its sequence advances. An exact retry of the latest event is a successful
// no-op so recovery cannot enqueue it twice.
func (h *EventHub) PublishLive(ev orchestrator.OrchestratorEvent) (bool, error) {
	return h.publishLive(ev, false)
}

// PublishAccepted publishes an EventStore event after RAM admission and keeps
// it in a bounded tail until a later durable replay confirms the commit.
func (h *EventHub) PublishAccepted(ev orchestrator.OrchestratorEvent) (bool, error) {
	return h.publishLive(ev, true)
}

func (h *EventHub) publishLive(ev orchestrator.OrchestratorEvent, accepted bool) (bool, error) {
	if h == nil {
		return false, errors.New("event hub is required")
	}
	if ev.Type == "agent.response_delta" {
		return false, errors.New("transient Mio answer delta cannot enter canonical live history")
	}
	if ev.EventSeq <= 0 {
		return false, errors.New("live event must have a positive EventSeq")
	}
	data, err := json.Marshal(ev)
	if err != nil {
		return false, fmt.Errorf("marshal live event: %w", err)
	}

	h.mu.Lock()
	if ev.EventSeq < h.lastLiveEventSeq {
		last := h.lastLiveEventSeq
		h.mu.Unlock()
		return false, fmt.Errorf("%w: got %d after %d", errEventHubLiveSequenceNotIncreasing, ev.EventSeq, last)
	}
	if ev.EventSeq == h.lastLiveEventSeq {
		lastID := h.lastLiveEventID
		h.mu.Unlock()
		if ev.EventID == lastID {
			return false, nil
		}
		return false, fmt.Errorf("%w: EventSeq %d already belongs to event %s", errEventHubLiveSequenceNotIncreasing, ev.EventSeq, lastID)
	}
	h.lastLiveEventSeq = ev.EventSeq
	h.lastLiveEventID = ev.EventID
	if accepted {
		h.retainAcceptedLocked(ev)
	}
	clientDropped, droppedClaims := h.appendEventLocked(ev, data)
	clientCount := len(h.clients)
	h.mu.Unlock()
	releasePublicationClaims(droppedClaims)
	h.logPublishedEvent(ev, clientDropped, clientCount)
	return true, nil
}

func (h *EventHub) retainAcceptedLocked(ev orchestrator.OrchestratorEvent) {
	if h.maxHist <= 0 {
		h.acceptedGap = &EventReplayGap{EventID: ev.EventID, EventSeq: ev.EventSeq}
		h.acceptedTail = nil
		return
	}
	h.acceptedTail = append(h.acceptedTail, ev)
	if len(h.acceptedTail) > h.maxHist {
		evicted := h.acceptedTail[0]
		h.acceptedTail = h.acceptedTail[1:]
		h.acceptedGap = &EventReplayGap{EventID: evicted.EventID, EventSeq: evicted.EventSeq}
	}
}

// MarkDurable retires accepted RAM tail entries through the matching durable
// EventSeq. FIFO EventStore commit order proves all earlier accepted entries
// have also committed. A reused sequence with a different identity fails
// closed.
func (h *EventHub) MarkDurable(ev orchestrator.OrchestratorEvent) error {
	if h == nil {
		return nil
	}
	if ev.EventSeq <= 0 || ev.EventID.Validate() != nil {
		return errors.New("durable event identity is invalid")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, accepted := range h.acceptedTail {
		if accepted.EventSeq == ev.EventSeq && accepted.EventID != ev.EventID {
			return fmt.Errorf("durable EventSeq %d belongs to %s, accepted tail has %s", ev.EventSeq, ev.EventID, accepted.EventID)
		}
	}
	if h.acceptedGap != nil && h.acceptedGap.EventSeq == ev.EventSeq && h.acceptedGap.EventID != ev.EventID {
		return fmt.Errorf("durable EventSeq %d belongs to %s, evicted accepted event has %s", ev.EventSeq, ev.EventID, h.acceptedGap.EventID)
	}
	firstRetained := 0
	for firstRetained < len(h.acceptedTail) && h.acceptedTail[firstRetained].EventSeq <= ev.EventSeq {
		firstRetained++
	}
	if firstRetained > 0 {
		copy(h.acceptedTail, h.acceptedTail[firstRetained:])
		h.acceptedTail = h.acceptedTail[:len(h.acceptedTail)-firstRetained]
	}
	if h.acceptedGap != nil && h.acceptedGap.EventSeq <= ev.EventSeq {
		h.acceptedGap = nil
	}
	return nil
}

func (h *EventHub) appendEventLocked(ev orchestrator.OrchestratorEvent, data []byte) (bool, []domainconversation.PreparedChatTurnPublicationGuard) {
	h.appendHistoryLocked(ev)
	clientDropped := false
	var droppedClaims []domainconversation.PreparedChatTurnPublicationGuard
	for ch := range h.clients {
		select {
		case ch <- data:
		default:
			droppedClaims = append(droppedClaims, h.removeSubscriberLocked(ch)...)
			clientDropped = true
		}
	}
	return clientDropped, droppedClaims
}

func (h *EventHub) appendHistoryLocked(ev orchestrator.OrchestratorEvent) {
	h.history = append(h.history, ev)
	if h.maxHist <= 0 {
		h.history = nil
	} else if len(h.history) > h.maxHist {
		h.history = h.history[len(h.history)-h.maxHist:]
	}
}

// removeSubscriberLocked atomically detaches and closes a channel from every
// lane and returns its pending publication references for release after the
// caller drops h.mu.
func (h *EventHub) removeSubscriberLocked(ch chan []byte) []domainconversation.PreparedChatTurnPublicationGuard {
	_, inClients := h.clients[ch]
	delete(h.clients, ch)
	if controlCh := h.controlClients[ch]; controlCh != nil {
		delete(h.controlClients, ch)
		close(controlCh)
	}
	if ownerID := h.privateMioDeltaOwners[ch]; ownerID != "" {
		delete(h.privateMioDeltaOwners, ch)
		if ownerClients := h.privateMioDeltaClients[ownerID]; ownerClients != nil {
			delete(ownerClients, ch)
			if len(ownerClients) == 0 {
				delete(h.privateMioDeltaClients, ownerID)
			}
		}
		inClients = true
	}
	claims := h.privateMioLiveClaims[ch]
	delete(h.privateMioLiveClaims, ch)
	if inClients {
		close(ch)
	}
	result := make([]domainconversation.PreparedChatTurnPublicationGuard, 0, len(claims))
	for _, claim := range claims {
		result = append(result, claim)
	}
	return result
}

func (h *EventHub) logPublishedEvent(ev orchestrator.OrchestratorEvent, clientDropped bool, clientCount int) {
	if clientDropped {
		log.Printf("[EventHub] disconnect: client too slow (remaining clients=%d)", clientCount)
		h.notifyClientCountChanged(clientCount)
	}
	log.Printf("[EventHub] OnEvent: eventType=%s from=%s to=%s clients=%d", ev.Type, ev.From, ev.To, clientCount)
}

// ClientCount returns the current number of connected SSE clients.
func (h *EventHub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// SetClientCountListener installs a callback invoked when the SSE client count changes.
func (h *EventHub) SetClientCountListener(listener func(int)) {
	h.mu.Lock()
	h.clientCountListener = listener
	h.mu.Unlock()
}

// SetReplayReader configures the canonical durable reader used for positive
// Last-Event-ID reconnects. A nil reader restores in-memory replay fallback.
func (h *EventHub) SetReplayReader(reader EventReplayReader) {
	h.mu.Lock()
	h.replayReader = reader
	h.mu.Unlock()
}

// SetSessionPublicReplayReader configures the durable public-message reader
// used by the owner/session-scoped response-first Viewer stream.
func (h *EventHub) SetSessionPublicReplayReader(reader SessionPublicEventReplayReader) {
	h.mu.Lock()
	h.sessionReplayReader = reader
	h.mu.Unlock()
}

func (h *EventHub) notifyClientCountChanged(count int) {
	h.mu.RLock()
	listener := h.clientCountListener
	h.mu.RUnlock()
	if listener != nil {
		listener(count)
	}
}

// Subscribe registers a new SSE client and returns its event channel and a
// consistent history snapshot captured at registration time.
func (h *EventHub) Subscribe() (chan []byte, []orchestrator.OrchestratorEvent) {
	ch, snapshot := h.SubscribeForReplay()
	return ch, snapshot.Events
}

// SubscribeForReplay atomically registers the subscriber and captures the
// bounded durable/live history plus retained admitted suffix metadata.
func (h *EventHub) SubscribeForReplay() (chan []byte, EventReplaySnapshot) {
	ch := make(chan []byte, 64)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	clientCount := len(h.clients)
	snapshot := h.replaySnapshotLocked()
	h.mu.Unlock()
	log.Printf("[EventHub] Subscribe: new client connected (total clients=%d)", clientCount)
	h.notifyClientCountChanged(clientCount)
	return ch, snapshot
}

// SubscribeForReplayWithControl atomically captures the latest control
// snapshot, registers both live lanes, and captures canonical replay history.
// The optional provider runs before taking the hub lock. Its sample only
// replaces the cached snapshot when its existing revision/generation is newer.
func (h *EventHub) SubscribeForReplayWithControl() (chan []byte, chan IdleChatControlSnapshot, EventReplaySnapshot, *IdleChatControlSnapshot) {
	return h.subscribeForReplayWithControl("")
}

// SubscribeForReplayWithControlAndPrivateMioDeltas atomically registers one
// channel in the canonical owner stream and the owner's transient Mio lane.
// Both event classes therefore share one FIFO and one bounded subscriber.
// A nil event channel means the owner subscription could not be admitted.
func (h *EventHub) SubscribeForReplayWithControlAndPrivateMioDeltas(ownerID string) (chan []byte, chan IdleChatControlSnapshot, EventReplaySnapshot, *IdleChatControlSnapshot) {
	if h == nil || strings.TrimSpace(ownerID) == "" || strings.TrimSpace(ownerID) != ownerID {
		return nil, nil, EventReplaySnapshot{}, nil
	}
	return h.subscribeForReplayWithControl(ownerID)
}

func (h *EventHub) subscribeForReplayWithControl(ownerID string) (chan []byte, chan IdleChatControlSnapshot, EventReplaySnapshot, *IdleChatControlSnapshot) {
	h.mu.RLock()
	provider := h.controlSnapshotRead
	h.mu.RUnlock()
	var refreshed IdleChatControlSnapshot
	var hasRefreshed bool
	if provider != nil {
		refreshed, hasRefreshed = provider()
	}

	ch := make(chan []byte, 64)
	controlCh := make(chan IdleChatControlSnapshot, 1)
	h.mu.Lock()
	if hasRefreshed && refreshed.RuntimeEpoch != "" {
		if !h.hasControlSnapshot || refreshed.RuntimeEpoch != h.controlSnapshot.RuntimeEpoch || shouldReplaceIdleChatControlSnapshot(h.controlSnapshot, refreshed) {
			h.publishIdleChatControlSnapshotLocked(refreshed)
		}
	}
	if ownerID != "" && len(h.privateMioDeltaClients[ownerID]) >= privateMioDeltaMaxClientsPerOwner {
		h.mu.Unlock()
		return nil, nil, EventReplaySnapshot{}, nil
	}
	h.clients[ch] = struct{}{}
	h.controlClients[ch] = controlCh
	if ownerID != "" && !h.registerPrivateMioDeltaSubscriberLocked(ownerID, ch) {
		delete(h.clients, ch)
		delete(h.controlClients, ch)
		h.mu.Unlock()
		return nil, nil, EventReplaySnapshot{}, nil
	}
	clientCount := len(h.clients)
	snapshot := h.replaySnapshotLocked()
	var controlSnapshot *IdleChatControlSnapshot
	if h.hasControlSnapshot {
		copy := h.controlSnapshot
		controlSnapshot = &copy
	}
	h.mu.Unlock()
	log.Printf("[EventHub] Subscribe: new client connected (total clients=%d)", clientCount)
	h.notifyClientCountChanged(clientCount)
	return ch, controlCh, snapshot, controlSnapshot
}

func (h *EventHub) replaySnapshotLocked() EventReplaySnapshot {
	events := make([]orchestrator.OrchestratorEvent, 0, len(h.history)+len(h.acceptedTail))
	events = append(events, h.history...)
	seen := make(map[modulecore.EventSeq]struct{}, len(events)+len(h.acceptedTail))
	for _, event := range events {
		if event.EventSeq > 0 {
			seen[event.EventSeq] = struct{}{}
		}
	}
	for _, event := range h.acceptedTail {
		if _, exists := seen[event.EventSeq]; exists {
			continue
		}
		events = append(events, event)
		seen[event.EventSeq] = struct{}{}
	}
	snapshot := EventReplaySnapshot{Events: events}
	if h.acceptedGap != nil {
		gap := *h.acceptedGap
		snapshot.AcceptedGap = &gap
	}
	return snapshot
}

// Unsubscribe removes a client and closes its channel.
func (h *EventHub) Unsubscribe(ch chan []byte) {
	h.mu.Lock()
	if _, inClients := h.clients[ch]; !inClients && h.privateMioDeltaOwners[ch] == "" {
		h.mu.Unlock()
		return
	}
	claims := h.removeSubscriberLocked(ch)
	clientCount := len(h.clients)
	h.mu.Unlock()
	releasePublicationClaims(claims)
	log.Printf("[EventHub] Unsubscribe: client disconnected (remaining clients=%d)", clientCount)
	h.notifyClientCountChanged(clientCount)
}

// History returns a copy of recent events.
func (h *EventHub) History() []orchestrator.OrchestratorEvent {
	h.mu.RLock()
	defer h.mu.RUnlock()

	result := make([]orchestrator.OrchestratorEvent, len(h.history))
	copy(result, h.history)
	return result
}

// Replay emits a bounded, ordered reconnect history and returns the last
// processed canonical EventSeq. A positive cursor uses the durable reader when
// configured; cursor zero intentionally preserves the existing in-memory
// initial snapshot behavior.
func (h *EventHub) Replay(ctx context.Context, after modulecore.EventSeq, snapshot []orchestrator.OrchestratorEvent, emit func(orchestrator.OrchestratorEvent) error) (modulecore.EventSeq, error) {
	return h.ReplayCaptured(ctx, after, EventReplaySnapshot{Events: snapshot}, emit)
}

// ReplayCaptured merges a durable prefix with the suffix atomically captured
// when the subscriber registered. Accepted RAM-only events are included after
// the fixed durable watermark, preventing a reconnect from missing events
// published before subscription.
func (h *EventHub) ReplayCaptured(ctx context.Context, after modulecore.EventSeq, snapshot EventReplaySnapshot, emit func(orchestrator.OrchestratorEvent) error) (modulecore.EventSeq, error) {
	if ctx == nil {
		return after, fmt.Errorf("event replay context is required")
	}
	if after < 0 {
		return after, fmt.Errorf("event replay cursor must not be negative")
	}
	if emit == nil {
		return after, fmt.Errorf("event replay emitter is required")
	}
	boundedContext, cancel := context.WithTimeout(ctx, replayTimeout)
	defer cancel()
	h.mu.RLock()
	reader := h.replayReader
	h.mu.RUnlock()
	return replayCaptured(boundedContext, reader, after, snapshot, emit, false)
}

// ReplayAvailable emits the retained canonical window when a durable reader is
// configured, or the current in-memory snapshot otherwise. It is used after a
// reconnect cursor can no longer be resolved, when replaying after its old
// EventSeq could silently skip a replacement event that reused that sequence.
func (h *EventHub) ReplayAvailable(ctx context.Context, snapshot []orchestrator.OrchestratorEvent, emit func(orchestrator.OrchestratorEvent) error) (modulecore.EventSeq, error) {
	return h.ReplayAvailableCaptured(ctx, EventReplaySnapshot{Events: snapshot}, emit)
}

// ReplayAvailableCaptured replays the available durable history and captured
// RAM suffix after a reset cursor. A missing accepted tail is reported as a
// typed coverage error if the durable prefix cannot prove it committed.
func (h *EventHub) ReplayAvailableCaptured(ctx context.Context, snapshot EventReplaySnapshot, emit func(orchestrator.OrchestratorEvent) error) (modulecore.EventSeq, error) {
	if ctx == nil {
		return 0, fmt.Errorf("event replay context is required")
	}
	if emit == nil {
		return 0, fmt.Errorf("event replay emitter is required")
	}
	boundedContext, cancel := context.WithTimeout(ctx, replayTimeout)
	defer cancel()
	h.mu.RLock()
	reader := h.replayReader
	h.mu.RUnlock()
	return replayCaptured(boundedContext, reader, 0, snapshot, emit, true)
}

func replayCaptured(ctx context.Context, reader EventReplayReader, after modulecore.EventSeq, snapshot EventReplaySnapshot, emit func(orchestrator.OrchestratorEvent) error, forceDurable bool) (modulecore.EventSeq, error) {
	if snapshot.AcceptedGap != nil && snapshot.AcceptedGap.EventSeq > after {
		if reader == nil {
			return after, &ReplayCoverageError{After: after, Missing: *snapshot.AcceptedGap}
		}
		proven, through, err := proveAcceptedGap(ctx, reader, *snapshot.AcceptedGap)
		if err != nil {
			return after, &ReplayCoverageError{After: after, Missing: *snapshot.AcceptedGap, DurableThrough: through}
		}
		if proven.EventSeq != snapshot.AcceptedGap.EventSeq || proven.EventID != snapshot.AcceptedGap.EventID {
			return after, errors.New("durable accepted gap proof identity does not match the captured gap")
		}
	}
	if reader == nil {
		return replaySnapshot(ctx, snapshot.Events, after, emit, after > 0)
	}
	if after == 0 && !forceDurable && snapshot.AcceptedGap == nil {
		return replaySnapshot(ctx, snapshot.Events, after, emit, false)
	}

	idsBySeq := make(map[modulecore.EventSeq]modulecore.EventID, len(snapshot.Events))
	for _, event := range snapshot.Events {
		if event.EventSeq <= 0 {
			continue
		}
		if prior, exists := idsBySeq[event.EventSeq]; exists && prior != event.EventID {
			return after, fmt.Errorf("captured replay EventSeq %d has conflicting EventIDs %s and %s", event.EventSeq, prior, event.EventID)
		}
		idsBySeq[event.EventSeq] = event.EventID
	}
	cursor, err := replayDurable(ctx, reader, after, func(event orchestrator.OrchestratorEvent) error {
		if capturedID, exists := idsBySeq[event.EventSeq]; exists && capturedID != event.EventID {
			return fmt.Errorf("durable EventSeq %d belongs to %s, captured live event has %s", event.EventSeq, event.EventID, capturedID)
		}
		return emit(event)
	})
	if err != nil {
		return cursor, err
	}
	suffix, err := orderedReplaySuffix(snapshot.Events, cursor)
	if err != nil {
		return cursor, err
	}
	for index, event := range suffix {
		if err := replayContextError(ctx); err != nil {
			return cursor, err
		}
		if event.EventSeq <= cursor {
			return cursor, fmt.Errorf("captured replay suffix %d has non-advancing EventSeq %d", index, event.EventSeq)
		}
		if err := emit(event); err != nil {
			return cursor, err
		}
		cursor = event.EventSeq
	}
	return cursor, nil
}

func proveAcceptedGap(ctx context.Context, reader EventReplayReader, gap EventReplayGap) (orchestrator.OrchestratorEvent, modulecore.EventSeq, error) {
	if gap.EventSeq <= 0 || gap.EventID.Validate() != nil {
		return orchestrator.OrchestratorEvent{}, 0, errors.New("accepted replay gap identity is invalid")
	}
	if err := replayContextError(ctx); err != nil {
		return orchestrator.OrchestratorEvent{}, 0, err
	}
	page, through, err := reader.ReplayPage(ctx, gap.EventSeq-1, 0, 1)
	if err != nil {
		return orchestrator.OrchestratorEvent{}, through, err
	}
	if through < gap.EventSeq || len(page) != 1 || page[0].EventSeq != gap.EventSeq || page[0].EventID != gap.EventID {
		return orchestrator.OrchestratorEvent{}, through, fmt.Errorf("durable replay does not prove evicted accepted event %s at EventSeq %d", gap.EventID, gap.EventSeq)
	}
	return page[0], through, nil
}

func orderedReplaySuffix(events []orchestrator.OrchestratorEvent, after modulecore.EventSeq) ([]orchestrator.OrchestratorEvent, error) {
	ordered := make([]orchestrator.OrchestratorEvent, 0, len(events))
	for _, event := range events {
		if event.EventSeq > after {
			ordered = append(ordered, event)
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].EventSeq < ordered[j].EventSeq })
	suffix := ordered[:0]
	for _, event := range ordered {
		if len(suffix) > 0 && suffix[len(suffix)-1].EventSeq == event.EventSeq {
			if suffix[len(suffix)-1].EventID != event.EventID {
				return nil, fmt.Errorf("captured replay EventSeq %d has conflicting EventIDs", event.EventSeq)
			}
			continue
		}
		suffix = append(suffix, event)
	}
	return suffix, nil
}

func replayDurable(ctx context.Context, reader EventReplayReader, after modulecore.EventSeq, emit func(orchestrator.OrchestratorEvent) error) (modulecore.EventSeq, error) {
	cursor := after
	through := modulecore.EventSeq(0)
	for {
		if err := replayContextError(ctx); err != nil {
			return cursor, err
		}
		page, watermark, err := reader.ReplayPage(ctx, cursor, through, replayPageLimit)
		if err != nil {
			return cursor, err
		}
		if through == 0 {
			through = watermark
			if through < after {
				return cursor, fmt.Errorf("event replay watermark %d precedes cursor %d", through, after)
			}
		} else if watermark != through {
			return cursor, fmt.Errorf("event replay watermark changed from %d to %d", through, watermark)
		}
		if len(page) > replayPageLimit {
			return cursor, fmt.Errorf("event replay page exceeds limit %d", replayPageLimit)
		}
		if len(page) == 0 {
			if cursor < through {
				return cursor, fmt.Errorf("event replay ended before fixed watermark %d", through)
			}
			return cursor, nil
		}
		for index, event := range page {
			if event.EventSeq <= cursor {
				return cursor, fmt.Errorf("event replay page %d has non-advancing event_seq %d", index, event.EventSeq)
			}
			if event.EventSeq <= 0 || (through > 0 && event.EventSeq > through) {
				return cursor, fmt.Errorf("event replay page %d has event_seq %d outside fixed window", index, event.EventSeq)
			}
			if err := replayContextError(ctx); err != nil {
				return cursor, err
			}
			if err := emit(event); err != nil {
				return cursor, err
			}
			cursor = event.EventSeq
		}
		if cursor >= through {
			return cursor, nil
		}
	}
}

func replaySnapshot(ctx context.Context, snapshot []orchestrator.OrchestratorEvent, after modulecore.EventSeq, emit func(orchestrator.OrchestratorEvent) error, validateRetention bool) (modulecore.EventSeq, error) {
	if err := replayContextError(ctx); err != nil {
		return after, err
	}
	if !validateRetention {
		cursor := after
		seen := make(map[modulecore.EventSeq]struct{})
		for _, event := range snapshot {
			if err := replayContextError(ctx); err != nil {
				return cursor, err
			}
			if event.EventSeq > 0 {
				if _, exists := seen[event.EventSeq]; exists {
					continue
				}
				seen[event.EventSeq] = struct{}{}
			}
			if err := emit(event); err != nil {
				return cursor, err
			}
			if event.EventSeq > cursor {
				cursor = event.EventSeq
			}
		}
		return cursor, nil
	}

	ordered := make([]orchestrator.OrchestratorEvent, 0, len(snapshot))
	oldest := modulecore.EventSeq(0)
	newest := modulecore.EventSeq(0)
	for _, event := range snapshot {
		if event.EventSeq <= 0 {
			continue
		}
		ordered = append(ordered, event)
		if oldest == 0 || event.EventSeq < oldest {
			oldest = event.EventSeq
		}
		if event.EventSeq > newest {
			newest = event.EventSeq
		}
	}
	if oldest == 0 {
		return after, fmt.Errorf("event replay cursor %d cannot be served without retained history", after)
	}
	if after < oldest-1 {
		return after, fmt.Errorf("event replay cursor %d is older than retained history starting at %d", after, oldest)
	}
	if after > newest {
		return after, fmt.Errorf("event replay cursor %d is beyond retained history ending at %d", after, newest)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].EventSeq < ordered[j].EventSeq
	})
	cursor := after
	seen := make(map[modulecore.EventSeq]struct{}, len(ordered))
	for _, event := range ordered {
		if event.EventSeq <= after {
			continue
		}
		if _, exists := seen[event.EventSeq]; exists {
			continue
		}
		seen[event.EventSeq] = struct{}{}
		if event.EventSeq <= cursor {
			return cursor, fmt.Errorf("retained replay history is not strictly advancing at %d", event.EventSeq)
		}
		if err := replayContextError(ctx); err != nil {
			return cursor, err
		}
		if err := emit(event); err != nil {
			return cursor, err
		}
		cursor = event.EventSeq
	}
	return cursor, nil
}

func replayContextError(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

// IsTransientReplayEvent identifies events that are live-only and must not be
// sent from durable or in-memory replay history.
func IsTransientReplayEvent(ev orchestrator.OrchestratorEvent) bool {
	switch ev.Type {
	case "agent.response_delta", "tts.audio_chunk", "tts.session_completed", "idlechat.message", "idlechat.summary":
		return true
	case "investment.refresh", "investment.market", "investment.macro", "investment.features", "investment.events", "investment.snapshot":
		return true
	default:
		return false
	}
}
