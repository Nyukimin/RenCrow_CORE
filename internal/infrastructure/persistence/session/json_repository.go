package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// JSONSessionRepository はJSONファイルベースのSessionRepository実装
type JSONSessionRepository struct {
	baseDir string
	mu      sync.Mutex
	// bindingMu protects the warmed canonical RAM projection. It is kept
	// separate from mu because mu spans Session file reads and writes.
	bindingMu sync.RWMutex
	writeGate sync.RWMutex
	writeMu   sync.Mutex
	overlay   map[string]*pendingSessionWrite

	canonicalIndex            map[canonicalSessionKey]map[string]struct{}
	canonicalIndexBySessionID map[string]canonicalSessionKey
	canonicalOwnerProjection  map[string]canonicalSessionOwnerProjection
	canonicalIndexWarmed      bool
	readCanonicalDirectory    func(string) ([]os.DirEntry, error)
	readCanonicalIdentityFile func(string) ([]byte, error)
	readSessionFile           func(string) ([]byte, error)
	beforeWriteSessionBytes   func(context.Context, string, []byte) error
}

var canonicalSessionBindingGenerationSequence atomic.Uint64

func nextCanonicalSessionBindingGeneration() (conversation.BindingGeneration, error) {
	for {
		current := canonicalSessionBindingGenerationSequence.Load()
		if current == ^uint64(0) {
			return conversation.BindingGenerationUnavailable, fmt.Errorf("canonical Session binding generation exhausted")
		}
		next := current + 1
		if canonicalSessionBindingGenerationSequence.CompareAndSwap(current, next) {
			return conversation.BindingGeneration(next), nil
		}
	}
}

type pendingSessionWrite struct {
	data      []byte
	done      chan struct{}
	err       error
	admission *persistencequeue.Admission
}

type canonicalSessionKey struct {
	logicalDate            string
	channelType            string
	externalConversationID string
}

// canonicalSessionOwnerProjection is derived from the canonical Session
// record. Its Session value is repository-owned and never returned directly.
type canonicalSessionOwnerProjection struct {
	creationIntent    *session.CreationIntent
	value             *session.Session
	bindingGeneration conversation.BindingGeneration
}

type sessionIdentityProbe struct {
	ID             string             `json:"id"`
	LogicalDate    string             `json:"logical_date"`
	ChannelAddress *channelAddressDTO `json:"channel_address"`
}

// channelAddressDTO is the persistence projection for conversation's private
// ChannelAddress fields. It is deliberately not the domain value object.
type channelAddressDTO struct {
	ChannelType            string `json:"channel_type"`
	ExternalConversationID string `json:"external_conversation_id"`
}

func channelAddressDTOFromDomain(address conversation.ChannelAddress) channelAddressDTO {
	return channelAddressDTO{
		ChannelType:            address.ChannelType(),
		ExternalConversationID: address.ExternalConversationID(),
	}
}

func channelAddressFromDTO(dto *channelAddressDTO) (conversation.ChannelAddress, error) {
	if dto == nil {
		return conversation.ChannelAddress{}, fmt.Errorf("channel_address is required")
	}
	address, err := conversation.NewChannelAddress(dto.ChannelType, dto.ExternalConversationID)
	if err != nil {
		return conversation.ChannelAddress{}, fmt.Errorf("invalid channel_address: %w", err)
	}
	if dto.ChannelType != address.ChannelType() || dto.ExternalConversationID != address.ExternalConversationID() {
		return conversation.ChannelAddress{}, fmt.Errorf("channel_address is not normalized")
	}
	return address, nil
}

// LoadCanonical resolves an existing daily conversation using explicit lookup
// attributes without creating or changing any session state.
func (r *JSONSessionRepository) LoadCanonical(ctx context.Context, logicalDate string, address conversation.ChannelAddress) (*session.Session, bool, error) {
	value, _, found, err := r.LoadCanonicalWithCreationIntent(ctx, logicalDate, address)
	return value, found, err
}

// BindingGeneration returns the current RAM-owned generation for a binding
// identity that has already passed canonical resolution. The supplied binding
// must be unresolved (generation zero); this method never reads the Session
// file and fails closed when the canonical projection is unavailable.
func (r *JSONSessionRepository) BindingGeneration(ctx context.Context, binding conversation.SessionBinding) (conversation.BindingGeneration, error) {
	if err := ctx.Err(); err != nil {
		return conversation.BindingGenerationUnavailable, err
	}
	if err := binding.ValidateForPublication(); err != nil {
		return conversation.BindingGenerationUnavailable, fmt.Errorf("invalid SessionBinding identity: %w", err)
	}
	if binding.BindingGeneration() != conversation.BindingGenerationUnavailable {
		return conversation.BindingGenerationUnavailable, fmt.Errorf("SessionBinding generation lookup requires an unresolved binding")
	}
	r.bindingMu.RLock()
	defer r.bindingMu.RUnlock()
	projection, exists := r.canonicalOwnerProjection[string(binding.SessionID())]
	key, indexed := r.canonicalIndexBySessionID[string(binding.SessionID())]
	if !exists {
		return conversation.BindingGenerationUnavailable, fmt.Errorf("canonical Session binding projection is unavailable")
	}
	if err := r.validateCanonicalSessionBindingIdentity(binding, projection, key, indexed); err != nil {
		return conversation.BindingGenerationUnavailable, err
	}
	if projection.bindingGeneration == conversation.BindingGenerationUnavailable {
		return conversation.BindingGenerationUnavailable, fmt.Errorf("canonical Session binding generation is unavailable")
	}
	return projection.bindingGeneration, nil
}

// CurrentPublicationBinding reads the canonical owner, audience, route, and
// publication incarnation from the warmed RAM projection. It never falls back
// to per-request Session file I/O.
func (r *JSONSessionRepository) CurrentPublicationBinding(ctx context.Context, id string) (session.CanonicalSessionPublicationBinding, error) {
	if ctx == nil {
		return session.CanonicalSessionPublicationBinding{}, fmt.Errorf("canonical publication lookup context is required")
	}
	if err := ctx.Err(); err != nil {
		return session.CanonicalSessionPublicationBinding{}, err
	}
	if err := modulecore.SessionID(id).Validate(); err != nil {
		return session.CanonicalSessionPublicationBinding{}, fmt.Errorf("invalid canonical publication session_id: %w", err)
	}
	r.bindingMu.RLock()
	defer r.bindingMu.RUnlock()
	if !r.canonicalIndexWarmed {
		return session.CanonicalSessionPublicationBinding{}, fmt.Errorf("canonical Session publication projection is not warmed")
	}
	projection, exists := r.canonicalOwnerProjection[id]
	if !exists || projection.value == nil {
		return session.CanonicalSessionPublicationBinding{}, fmt.Errorf("canonical Session publication projection is unavailable")
	}
	key, indexed := r.canonicalIndexBySessionID[id]
	if !indexed || key != canonicalSessionKeyFor(projection.value.LogicalDate(), projection.value.ChannelAddress()) {
		return session.CanonicalSessionPublicationBinding{}, fmt.Errorf("canonical Session publication identity index is unavailable")
	}
	if projection.creationIntent != nil {
		return session.CanonicalSessionPublicationBinding{}, fmt.Errorf("response-first reserved Session has no public publication binding")
	}
	binding, err := session.NewCanonicalSessionPublicationBinding(projection.value)
	if err != nil {
		return session.CanonicalSessionPublicationBinding{}, fmt.Errorf("canonical Session publication binding is invalid: %w", err)
	}
	return binding, nil
}

// CurrentReservedV2ErrorPublicationBinding resolves publication identity for
// one reserved v2 terminal error from the warmed canonical projection.
func (r *JSONSessionRepository) CurrentReservedV2ErrorPublicationBinding(ctx context.Context, sessionID, ownerID string, address conversation.ChannelAddress) (session.CanonicalSessionPublicationBinding, error) {
	if ctx == nil {
		return session.CanonicalSessionPublicationBinding{}, fmt.Errorf("reserved v2 error publication lookup context is required")
	}
	if err := ctx.Err(); err != nil {
		return session.CanonicalSessionPublicationBinding{}, err
	}
	if err := modulecore.SessionID(sessionID).Validate(); err != nil {
		return session.CanonicalSessionPublicationBinding{}, fmt.Errorf("invalid reserved v2 error publication session_id: %w", err)
	}
	if err := session.ValidateOwnerID(ownerID); err != nil {
		return session.CanonicalSessionPublicationBinding{}, fmt.Errorf("invalid reserved v2 error publication owner: %w", err)
	}
	if err := address.Validate(); err != nil {
		return session.CanonicalSessionPublicationBinding{}, fmt.Errorf("invalid reserved v2 error publication ChannelAddress: %w", err)
	}

	r.bindingMu.RLock()
	defer r.bindingMu.RUnlock()
	if err := ctx.Err(); err != nil {
		return session.CanonicalSessionPublicationBinding{}, err
	}
	return r.reservedV2ErrorPublicationBindingLocked(sessionID, ownerID, address)
}

// UseReservedV2ErrorPublicationBinding admits a bounded publication claim only
// while the same reserved v2 identity remains current in RAM.
func (r *JSONSessionRepository) UseReservedV2ErrorPublicationBinding(ctx context.Context, expected session.CanonicalSessionPublicationBinding, claim func() error) error {
	if claim == nil {
		return fmt.Errorf("reserved v2 error publication claim callback is required")
	}
	if ctx == nil {
		return fmt.Errorf("reserved v2 error publication claim context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := expected.Validate(); err != nil {
		return fmt.Errorf("invalid expected reserved v2 error publication binding: %w", err)
	}
	if err := session.ValidateOwnerID(expected.OwnerID()); err != nil {
		return fmt.Errorf("invalid expected reserved v2 error publication owner: %w", err)
	}
	if err := expected.ChannelAddress().Validate(); err != nil {
		return fmt.Errorf("invalid expected reserved v2 error publication ChannelAddress: %w", err)
	}

	r.bindingMu.RLock()
	defer r.bindingMu.RUnlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := r.reservedV2ErrorPublicationBindingLocked(string(expected.SessionID()), expected.OwnerID(), expected.ChannelAddress())
	if err != nil {
		return err
	}
	if !current.SameIdentity(expected) {
		return fmt.Errorf("reserved v2 error publication binding is stale")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return claim()
}

// reservedV2ErrorPublicationBindingLocked resolves the complete reservation
// tuple from the warmed RAM projection. The caller must hold bindingMu.
func (r *JSONSessionRepository) reservedV2ErrorPublicationBindingLocked(sessionID, ownerID string, address conversation.ChannelAddress) (session.CanonicalSessionPublicationBinding, error) {
	if !r.canonicalIndexWarmed {
		return session.CanonicalSessionPublicationBinding{}, fmt.Errorf("canonical Session publication projection is not warmed")
	}
	projection, exists := r.canonicalOwnerProjection[sessionID]
	if !exists || projection.value == nil || projection.value.ID() != sessionID {
		return session.CanonicalSessionPublicationBinding{}, fmt.Errorf("reserved v2 canonical Session publication projection is unavailable")
	}
	key, indexed := r.canonicalIndexBySessionID[sessionID]
	expectedKey := canonicalSessionKeyFor(projection.value.LogicalDate(), projection.value.ChannelAddress())
	ids := r.canonicalIndex[expectedKey]
	if !indexed || key != expectedKey || len(ids) != 1 {
		return session.CanonicalSessionPublicationBinding{}, fmt.Errorf("reserved v2 canonical Session publication identity index is unavailable")
	}
	if _, present := ids[sessionID]; !present {
		return session.CanonicalSessionPublicationBinding{}, fmt.Errorf("reserved v2 canonical Session publication identity index is unavailable")
	}
	if projection.creationIntent == nil {
		return session.CanonicalSessionPublicationBinding{}, fmt.Errorf("canonical Session has no response-first v2 creation intent")
	}
	if err := session.ValidateOwnerID(projection.creationIntent.OwnerID); err != nil {
		return session.CanonicalSessionPublicationBinding{}, fmt.Errorf("canonical Session response-first v2 owner is invalid: %w", err)
	}
	if projection.creationIntent.Runtime != "v2" || projection.creationIntent.OwnerID != ownerID {
		return session.CanonicalSessionPublicationBinding{}, fmt.Errorf("canonical Session response-first v2 owner or runtime conflicts with the request")
	}
	if projection.value.ChannelAddress() != address {
		return session.CanonicalSessionPublicationBinding{}, fmt.Errorf("reserved v2 canonical Session ChannelAddress conflicts with the request")
	}
	binding, err := session.NewReservedV2ErrorPublicationBinding(projection.value, ownerID)
	if err != nil {
		return session.CanonicalSessionPublicationBinding{}, fmt.Errorf("reserved v2 canonical Session publication binding is invalid: %w", err)
	}
	return binding, nil
}

// UsePublicationBinding claims a short publication action only while the
// expected owner, audience, route, and incarnation remain current in RAM. The
// callback must be bounded and must not perform I/O or call this repository.
func (r *JSONSessionRepository) UsePublicationBinding(ctx context.Context, expected session.CanonicalSessionPublicationBinding, claim func() error) error {
	if claim == nil {
		return fmt.Errorf("canonical Session publication claim callback is required")
	}
	if ctx == nil {
		return fmt.Errorf("canonical Session publication claim context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := expected.Validate(); err != nil {
		return fmt.Errorf("invalid expected canonical Session publication binding: %w", err)
	}
	r.bindingMu.RLock()
	defer r.bindingMu.RUnlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !r.canonicalIndexWarmed {
		return fmt.Errorf("canonical Session publication projection is not warmed")
	}
	projection, exists := r.canonicalOwnerProjection[string(expected.SessionID())]
	if !exists || projection.value == nil {
		return fmt.Errorf("canonical Session publication projection is unavailable")
	}
	if projection.creationIntent != nil {
		return fmt.Errorf("response-first reserved Session has no public publication binding")
	}
	key, indexed := r.canonicalIndexBySessionID[string(expected.SessionID())]
	if !indexed || key != canonicalSessionKeyFor(projection.value.LogicalDate(), projection.value.ChannelAddress()) {
		return fmt.Errorf("canonical Session publication identity index is unavailable")
	}
	current, err := session.NewCanonicalSessionPublicationBinding(projection.value)
	if err != nil {
		return fmt.Errorf("canonical Session publication binding is invalid: %w", err)
	}
	if !current.SameIdentity(expected) {
		return fmt.Errorf("canonical Session publication binding is stale")
	}
	return claim()
}

// UseBinding serializes a short bounded publication enqueue with Session
// binding revocation. The callback must not perform database, filesystem,
// network, HTTP, or LLM work and must not call back into this repository.
func (r *JSONSessionRepository) UseBinding(ctx context.Context, binding conversation.SessionBinding, boundedEnqueue func() error) error {
	if boundedEnqueue == nil {
		return fmt.Errorf("SessionBinding enqueue callback is required")
	}
	if err := binding.ValidateForPublication(); err != nil {
		return fmt.Errorf("invalid SessionBinding: %w", err)
	}
	if binding.BindingGeneration() == conversation.BindingGenerationUnavailable {
		return fmt.Errorf("SessionBinding generation is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.bindingMu.RLock()
	defer r.bindingMu.RUnlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	projection, exists := r.canonicalOwnerProjection[string(binding.SessionID())]
	if !exists {
		return fmt.Errorf("canonical Session binding projection is unavailable")
	}
	key, indexed := r.canonicalIndexBySessionID[string(binding.SessionID())]
	if err := r.validateCanonicalSessionBindingIdentity(binding, projection, key, indexed); err != nil {
		return err
	}
	if binding.BindingGeneration() != projection.bindingGeneration {
		return fmt.Errorf("SessionBinding generation is stale")
	}
	return boundedEnqueue()
}

func (r *JSONSessionRepository) validateCanonicalSessionBindingIdentity(binding conversation.SessionBinding, projection canonicalSessionOwnerProjection, key canonicalSessionKey, indexed bool) error {
	if projection.value == nil || projection.value.ID() != string(binding.SessionID()) {
		return fmt.Errorf("SessionBinding does not match the canonical Session identity")
	}
	if projection.value.ChannelAddress() != binding.ChannelAddress() {
		return fmt.Errorf("SessionBinding ChannelAddress does not match the canonical Session")
	}
	expectedKey := canonicalSessionKeyFor(projection.value.LogicalDate(), projection.value.ChannelAddress())
	if !indexed || key != expectedKey {
		return fmt.Errorf("SessionBinding canonical identity index is unavailable")
	}
	if projection.value.PublicationIncarnation() != binding.PublicationIncarnation() {
		return fmt.Errorf("SessionBinding publication incarnation does not match the canonical Session")
	}
	if projection.creationIntent != nil {
		return fmt.Errorf("SessionBinding cannot use a response-first reserved Session")
	}
	switch binding.Audience() {
	case conversation.SessionAudiencePublic:
		if binding.OwnerID() != "" || projection.value.OwnerID() != "" {
			return fmt.Errorf("public SessionBinding does not match the canonical Session audience")
		}
	case conversation.SessionAudiencePrivate:
		if err := session.ValidateOwnerID(binding.OwnerID()); err != nil || projection.value.OwnerID() != binding.OwnerID() {
			return fmt.Errorf("private SessionBinding owner does not match the canonical Session")
		}
	default:
		return fmt.Errorf("invalid SessionBinding audience %q", binding.Audience())
	}
	return nil
}

// LoadCanonicalForOwner resolves an explicit opaque SessionID only when its
// persisted owner provenance and ChannelAddress match the authenticated
// request. An unbound legacy Session is returned as unavailable.
func (r *JSONSessionRepository) LoadCanonicalForOwner(ctx context.Context, id, ownerID string, address conversation.ChannelAddress) (session.CanonicalSessionOwnerResolution, error) {
	if err := ctx.Err(); err != nil {
		return session.CanonicalSessionOwnerResolution{}, err
	}
	if err := modulecore.SessionID(id).Validate(); err != nil {
		return session.CanonicalSessionOwnerResolution{}, fmt.Errorf("invalid canonical session_id: %w", err)
	}
	if err := session.ValidateOwnerID(ownerID); err != nil {
		return session.CanonicalSessionOwnerResolution{}, fmt.Errorf("invalid canonical Session owner lookup: %w", err)
	}
	if err := address.Validate(); err != nil {
		return session.CanonicalSessionOwnerResolution{}, fmt.Errorf("invalid canonical Session owner lookup ChannelAddress: %w", err)
	}
	if projection, key, indexed, warmed, exists := r.canonicalOwnerProjectionSnapshot(id); warmed {
		if !exists {
			return session.CanonicalSessionOwnerResolution{Status: session.CanonicalSessionOwnerNotFound}, nil
		}
		if !indexed {
			return session.CanonicalSessionOwnerResolution{}, fmt.Errorf("canonical owner projection for Session %q has no identity index", id)
		}
		return r.resolveOwnerProjection(ctx, projection, key, id, ownerID, address)
	}
	unlock, err := r.lockForContext(ctx)
	if err != nil {
		return session.CanonicalSessionOwnerResolution{}, err
	}
	defer unlock()

	return r.resolveCanonicalOwnerByID(ctx, id, ownerID, address)
}

// LoadCanonicalWithCreationIntent is the read-only natural-key owner lookup
// used to reconcile an uncertain v2 reservation. The intent is read from the
// same JSON record as the canonical identity.
func (r *JSONSessionRepository) LoadCanonicalWithCreationIntent(ctx context.Context, logicalDate string, address conversation.ChannelAddress) (*session.Session, *session.CreationIntent, bool, error) {
	unlock, err := r.lockForContext(ctx)
	if err != nil {
		return nil, nil, false, err
	}
	defer unlock()

	if err := validateCanonicalLookup(ctx, logicalDate, address); err != nil {
		return nil, nil, false, err
	}
	matchedID, err := r.findCanonicalSessionID(ctx, logicalDate, address)
	if err != nil {
		return nil, nil, false, err
	}
	if matchedID == "" {
		return nil, nil, false, nil
	}
	value, err := r.load(ctx, matchedID)
	if err != nil {
		return nil, nil, false, err
	}
	intentDTO, err := r.creationIntent(ctx, matchedID)
	if err != nil {
		return nil, nil, false, err
	}
	var intent *session.CreationIntent
	if intentDTO != nil {
		intent = intentDTO
	}
	return value, intent, true, nil
}

// LoadOrCreateCanonical resolves a daily conversation using explicit lookup
// attributes. SessionID remains opaque and is generated only when no matching
// canonical session exists. The returned created flag is decided under the
// same repository lock as the lookup and write.
func (r *JSONSessionRepository) LoadOrCreateCanonical(ctx context.Context, logicalDate string, address conversation.ChannelAddress, createdAt time.Time) (*session.Session, bool, error) {
	unlock, err := r.lockForContext(ctx)
	if err != nil {
		return nil, false, err
	}
	defer unlock()

	if err := validateCanonicalLookup(ctx, logicalDate, address); err != nil {
		return nil, false, err
	}
	if createdAt.IsZero() {
		return nil, false, fmt.Errorf("invalid canonical session lookup: created_at is required")
	}
	matchedID, err := r.findCanonicalSessionID(ctx, logicalDate, address)
	if err != nil {
		return nil, false, err
	}
	if matchedID != "" {
		intent, err := r.creationIntent(ctx, matchedID)
		if err != nil {
			return nil, false, err
		}
		if intent != nil {
			return nil, false, fmt.Errorf("canonical session is reserved for response-first v2")
		}
		value, err := r.load(ctx, matchedID)
		if err != nil {
			return nil, false, err
		}
		return value, false, nil
	}
	created, err := session.NewCanonicalSession(modulecore.NewSessionID(), logicalDate, address, createdAt)
	if err != nil {
		return nil, false, err
	}
	if err := r.save(ctx, created); err != nil {
		return nil, false, err
	}
	return created, true, nil
}

// LoadOrCreateCanonicalForOwner binds a newly-created Session to the
// authenticated owner in the same RAM-admitted persistence write. Existing
// Sessions are reusable only when their canonical owner provenance and
// ChannelAddress match; unbound legacy Sessions remain unavailable.
func (r *JSONSessionRepository) LoadOrCreateCanonicalForOwner(ctx context.Context, logicalDate string, address conversation.ChannelAddress, ownerID string, createdAt time.Time) (session.CanonicalSessionOwnerResolution, error) {
	return r.loadOrCreateCanonicalForOwner(ctx, logicalDate, address, ownerID, createdAt, false)
}

// LoadOrCreateCanonicalForOwnerSynchronously creates an owner-bound canonical
// Session with a synchronous durable write when no persistence admission is
// available. Callers must establish the authenticated owner and route policy
// before using this path; it does not bypass existing owner or address checks.
func (r *JSONSessionRepository) LoadOrCreateCanonicalForOwnerSynchronously(ctx context.Context, logicalDate string, address conversation.ChannelAddress, ownerID string, createdAt time.Time) (session.CanonicalSessionOwnerResolution, error) {
	if _, admitted := persistencequeue.AdmissionFromContext(ctx); admitted {
		return session.CanonicalSessionOwnerResolution{}, fmt.Errorf("synchronous owner-bound canonical Session creation cannot bypass persistence admission")
	}
	return r.loadOrCreateCanonicalForOwner(ctx, logicalDate, address, ownerID, createdAt, true)
}

func (r *JSONSessionRepository) loadOrCreateCanonicalForOwner(ctx context.Context, logicalDate string, address conversation.ChannelAddress, ownerID string, createdAt time.Time, synchronous bool) (session.CanonicalSessionOwnerResolution, error) {
	if err := validateCanonicalLookup(ctx, logicalDate, address); err != nil {
		return session.CanonicalSessionOwnerResolution{}, err
	}
	if err := session.ValidateOwnerID(ownerID); err != nil {
		return session.CanonicalSessionOwnerResolution{}, fmt.Errorf("invalid canonical Session owner lookup: %w", err)
	}
	if createdAt.IsZero() {
		return session.CanonicalSessionOwnerResolution{}, fmt.Errorf("invalid canonical session lookup: created_at is required")
	}
	if id, warmed, snapshotErr := r.canonicalSessionIDSnapshot(canonicalSessionKeyFor(logicalDate, address)); snapshotErr != nil {
		return session.CanonicalSessionOwnerResolution{}, snapshotErr
	} else if warmed && id != "" {
		projection, key, indexed, _, exists := r.canonicalOwnerProjectionSnapshot(id)
		if !exists {
			return session.CanonicalSessionOwnerResolution{}, fmt.Errorf("indexed canonical Session %q has no owner projection", id)
		}
		if !indexed {
			return session.CanonicalSessionOwnerResolution{}, fmt.Errorf("canonical owner projection for Session %q has no identity index", id)
		}
		resolved, err := r.resolveOwnerProjection(ctx, projection, key, id, ownerID, address)
		if err != nil {
			return session.CanonicalSessionOwnerResolution{}, err
		}
		if resolved.Session != nil && resolved.Session.LogicalDate() != logicalDate {
			return session.CanonicalSessionOwnerResolution{Status: session.CanonicalSessionOwnerConflict}, nil
		}
		return resolved, nil
	}
	var unlock func()
	var err error
	if synchronous {
		unlock, err = r.lockForSynchronousWrite(ctx)
	} else {
		unlock, err = r.lockForContext(ctx)
	}
	if err != nil {
		return session.CanonicalSessionOwnerResolution{}, err
	}
	defer unlock()
	if err := validateCanonicalLookup(ctx, logicalDate, address); err != nil {
		return session.CanonicalSessionOwnerResolution{}, err
	}
	if err := session.ValidateOwnerID(ownerID); err != nil {
		return session.CanonicalSessionOwnerResolution{}, fmt.Errorf("invalid canonical Session owner lookup: %w", err)
	}
	if createdAt.IsZero() {
		return session.CanonicalSessionOwnerResolution{}, fmt.Errorf("invalid canonical session lookup: created_at is required")
	}

	matchedID, err := r.findCanonicalSessionID(ctx, logicalDate, address)
	if err != nil {
		return session.CanonicalSessionOwnerResolution{}, err
	}
	if matchedID != "" {
		resolved, err := r.resolveCanonicalOwnerByID(ctx, matchedID, ownerID, address)
		if err != nil {
			return session.CanonicalSessionOwnerResolution{}, err
		}
		if resolved.Status == session.CanonicalSessionOwnerNotFound {
			return session.CanonicalSessionOwnerResolution{}, fmt.Errorf("indexed canonical Session %q has no owner projection", matchedID)
		}
		if resolved.Session != nil && resolved.Session.LogicalDate() != logicalDate {
			return session.CanonicalSessionOwnerResolution{Status: session.CanonicalSessionOwnerConflict}, nil
		}
		return resolved, nil
	}
	if !synchronous {
		if _, admitted := persistencequeue.AdmissionFromContext(ctx); !admitted {
			return session.CanonicalSessionOwnerResolution{}, fmt.Errorf("owner-bound canonical Session creation requires persistence admission")
		}
	}

	created, err := session.NewCanonicalSessionWithOwner(modulecore.NewSessionID(), ownerID, logicalDate, address, createdAt)
	if err != nil {
		return session.CanonicalSessionOwnerResolution{}, err
	}
	if err := r.save(ctx, created); err != nil {
		return session.CanonicalSessionOwnerResolution{}, err
	}
	return session.CanonicalSessionOwnerResolution{Session: created, BoundOwnerID: ownerID, Status: session.CanonicalSessionOwnerCreated}, nil
}

func (r *JSONSessionRepository) resolveCanonicalOwnerByID(ctx context.Context, id, requestedOwner string, requestedAddress conversation.ChannelAddress) (session.CanonicalSessionOwnerResolution, error) {
	if err := ctx.Err(); err != nil {
		return session.CanonicalSessionOwnerResolution{}, err
	}
	if projection, key, indexed, warmed, exists := r.canonicalOwnerProjectionSnapshot(id); warmed {
		if !exists {
			return session.CanonicalSessionOwnerResolution{Status: session.CanonicalSessionOwnerNotFound}, nil
		}
		if !indexed {
			return session.CanonicalSessionOwnerResolution{}, fmt.Errorf("canonical owner projection for Session %q has no identity index", id)
		}
		return r.resolveOwnerProjection(ctx, projection, key, id, requestedOwner, requestedAddress)
	}

	data, err := r.readSessionBytes(ctx, id)
	if errors.Is(err, session.ErrSessionNotFound) {
		return session.CanonicalSessionOwnerResolution{Status: session.CanonicalSessionOwnerNotFound}, nil
	}
	if err != nil {
		return session.CanonicalSessionOwnerResolution{}, err
	}
	value, err := r.sessionFromBytes(ctx, id, data)
	if err != nil {
		return session.CanonicalSessionOwnerResolution{}, err
	}
	if value.ID() != id {
		return session.CanonicalSessionOwnerResolution{Status: session.CanonicalSessionOwnerConflict}, nil
	}
	status, boundOwnerID, err := matchCanonicalSessionOwner(value, data, requestedOwner, requestedAddress)
	if err != nil {
		return session.CanonicalSessionOwnerResolution{}, err
	}
	if status != session.CanonicalSessionOwnerMatched {
		return session.CanonicalSessionOwnerResolution{Status: status}, nil
	}
	return session.CanonicalSessionOwnerResolution{Session: value, BoundOwnerID: boundOwnerID, Status: status}, nil
}

func (r *JSONSessionRepository) canonicalOwnerProjectionSnapshot(id string) (canonicalSessionOwnerProjection, canonicalSessionKey, bool, bool, bool) {
	r.bindingMu.RLock()
	defer r.bindingMu.RUnlock()
	projection, exists := r.canonicalOwnerProjection[id]
	key, indexed := r.canonicalIndexBySessionID[id]
	return projection, key, indexed, r.canonicalIndexWarmed, exists
}

func (r *JSONSessionRepository) canonicalSessionIDSnapshot(key canonicalSessionKey) (string, bool, error) {
	r.bindingMu.RLock()
	defer r.bindingMu.RUnlock()
	if !r.canonicalIndexWarmed {
		return "", false, nil
	}
	ids := r.canonicalIndex[key]
	switch len(ids) {
	case 0:
		return "", true, nil
	case 1:
		for id := range ids {
			return id, true, nil
		}
	default:
		return "", true, fmt.Errorf("multiple canonical sessions match lookup attributes")
	}
	return "", true, nil
}

func (r *JSONSessionRepository) resolveOwnerProjection(ctx context.Context, projection canonicalSessionOwnerProjection, key canonicalSessionKey, id, requestedOwner string, requestedAddress conversation.ChannelAddress) (session.CanonicalSessionOwnerResolution, error) {
	boundOwnerID, ownerBound := projection.ownerID()
	if !ownerBound {
		return session.CanonicalSessionOwnerResolution{Status: session.CanonicalSessionOwnerUnavailable}, nil
	}
	if boundOwnerID != requestedOwner || key.channelType != requestedAddress.ChannelType() || key.externalConversationID != requestedAddress.ExternalConversationID() {
		return session.CanonicalSessionOwnerResolution{Status: session.CanonicalSessionOwnerConflict}, nil
	}
	if projection.value == nil || projection.value.ID() != id {
		return session.CanonicalSessionOwnerResolution{}, fmt.Errorf("canonical owner projection for Session %q is incomplete", id)
	}
	value, err := r.cloneSession(ctx, projection.value)
	if err != nil {
		return session.CanonicalSessionOwnerResolution{}, err
	}
	return session.CanonicalSessionOwnerResolution{Session: value, BoundOwnerID: boundOwnerID, Status: session.CanonicalSessionOwnerMatched}, nil
}

func (projection canonicalSessionOwnerProjection) ownerID() (string, bool) {
	if projection.creationIntent != nil {
		return projection.creationIntent.OwnerID, true
	}
	if projection.value == nil || projection.value.OwnerID() == "" {
		return "", false
	}
	return projection.value.OwnerID(), true
}

func matchCanonicalSessionOwner(value *session.Session, data []byte, requestedOwner string, requestedAddress conversation.ChannelAddress) (session.CanonicalSessionOwnerStatus, string, error) {
	intent, err := creationIntentFromJSON(data)
	if err != nil {
		return "", "", err
	}
	ownerID, hasOwner, err := canonicalOwnerBindingFromJSON(data)
	if err != nil {
		return "", "", err
	}
	if intent != nil {
		ownerID = intent.OwnerID
		hasOwner = true
	}
	if !hasOwner {
		return session.CanonicalSessionOwnerUnavailable, "", nil
	}
	if ownerID != requestedOwner || value.ChannelAddress() != requestedAddress {
		return session.CanonicalSessionOwnerConflict, "", nil
	}
	return session.CanonicalSessionOwnerMatched, ownerID, nil
}

// ReserveCanonicalV2 creates the canonical JSON identity and its immutable
// response-first intent in one repository critical section and atomic file
// replacement. An existing same-owner reservation is reusable after retry or
// restart. Any pre-existing unreserved JSON session remains legacy and cannot
// be promoted.
func (r *JSONSessionRepository) ReserveCanonicalV2(ctx context.Context, logicalDate string, address conversation.ChannelAddress, ownerID string, createdAt time.Time) (*session.Session, bool, error) {
	unlock, err := r.lockForSynchronousWrite(ctx)
	if err != nil {
		return nil, false, err
	}
	defer unlock()

	if err := validateCanonicalLookup(ctx, logicalDate, address); err != nil {
		return nil, false, err
	}
	if strings.TrimSpace(ownerID) == "" || ownerID != strings.TrimSpace(ownerID) {
		return nil, false, fmt.Errorf("invalid response-first v2 reservation: owner_id is required and normalized")
	}
	if createdAt.IsZero() {
		return nil, false, fmt.Errorf("invalid response-first v2 reservation: created_at is required")
	}

	matchedID, err := r.findCanonicalSessionID(ctx, logicalDate, address)
	if err != nil {
		return nil, false, err
	}
	if matchedID != "" {
		intent, err := r.creationIntent(ctx, matchedID)
		if err != nil {
			return nil, false, err
		}
		if intent == nil {
			return nil, false, fmt.Errorf("canonical session already exists without a response-first v2 reservation")
		}
		if intent.Runtime != "v2" || intent.OwnerID != ownerID {
			return nil, false, fmt.Errorf("canonical response-first v2 reservation owner does not match")
		}
		value, err := r.load(ctx, matchedID)
		if err != nil {
			return nil, false, err
		}
		return value, false, nil
	}

	created, err := session.NewCanonicalSession(modulecore.NewSessionID(), logicalDate, address, createdAt)
	if err != nil {
		return nil, false, err
	}
	if _, err := os.Stat(r.getFilePath(created.ID())); err == nil {
		return nil, false, fmt.Errorf("canonical response-first v2 session_id collision")
	} else if !os.IsNotExist(err) {
		return nil, false, fmt.Errorf("check canonical response-first v2 session_id: %w", err)
	}
	dto := r.toDTO(created)
	dto.CreationIntent = &session.CreationIntent{Runtime: "v2", OwnerID: ownerID}
	if err := r.saveDTO(ctx, created, dto); err != nil {
		return nil, false, err
	}
	return created, true, nil
}

// IsCanonicalV2Reserved verifies a reservation by opaque SessionID and owner.
// It is read-only and deliberately returns false for missing, legacy, or
// differently-owned sessions.
func (r *JSONSessionRepository) IsCanonicalV2Reserved(ctx context.Context, id, ownerID string) (bool, error) {
	unlock, err := r.lockForContext(ctx)
	if err != nil {
		return false, err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := modulecore.SessionID(id).Validate(); err != nil {
		return false, fmt.Errorf("invalid canonical session_id: %w", err)
	}
	if strings.TrimSpace(ownerID) == "" || ownerID != strings.TrimSpace(ownerID) {
		return false, fmt.Errorf("invalid response-first v2 reservation lookup: owner_id is required and normalized")
	}
	if projection, _, _, warmed, exists := r.canonicalOwnerProjectionSnapshot(id); warmed {
		if !exists || projection.creationIntent == nil {
			return false, nil
		}
		return projection.creationIntent.Runtime == "v2" && projection.creationIntent.OwnerID == ownerID, nil
	}
	intent, err := r.creationIntent(ctx, id)
	if err != nil {
		if errors.Is(err, session.ErrSessionNotFound) {
			return false, nil
		}
		return false, err
	}
	return intent != nil && intent.Runtime == "v2" && intent.OwnerID == ownerID, nil
}

func validateCanonicalLookup(ctx context.Context, logicalDate string, address conversation.ChannelAddress) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := session.ValidateLogicalDate(logicalDate); err != nil {
		return fmt.Errorf("invalid canonical session lookup: %w", err)
	}
	if err := address.Validate(); err != nil {
		return fmt.Errorf("invalid canonical session lookup: %w", err)
	}
	return nil
}

func (r *JSONSessionRepository) lockForContext(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, admitted := persistencequeue.AdmissionFromContext(ctx); admitted {
		if !r.writeGate.TryRLock() {
			return nil, fmt.Errorf("session repository is busy with a synchronous write")
		}
		r.mu.Lock()
		if err := ctx.Err(); err != nil {
			r.mu.Unlock()
			r.writeGate.RUnlock()
			return nil, err
		}
		return func() {
			r.mu.Unlock()
			r.writeGate.RUnlock()
		}, nil
	}
	r.writeGate.Lock()
	r.mu.Lock()
	if err := ctx.Err(); err != nil {
		r.mu.Unlock()
		r.writeGate.Unlock()
		return nil, err
	}
	return func() {
		r.mu.Unlock()
		r.writeGate.Unlock()
	}, nil
}

func (r *JSONSessionRepository) lockForSynchronousWrite(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.writeGate.Lock()
	r.mu.Lock()
	if err := ctx.Err(); err != nil {
		r.mu.Unlock()
		r.writeGate.Unlock()
		return nil, err
	}
	return func() {
		r.mu.Unlock()
		r.writeGate.Unlock()
	}, nil
}

func (r *JSONSessionRepository) findCanonicalSessionID(ctx context.Context, logicalDate string, address conversation.ChannelAddress) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	key := canonicalSessionKeyFor(logicalDate, address)
	if id, warmed, err := r.canonicalSessionIDSnapshot(key); warmed {
		return id, err
	}
	return r.scanCanonicalSessionID(ctx, logicalDate, address)
}

func (r *JSONSessionRepository) scanCanonicalSessionID(ctx context.Context, logicalDate string, address conversation.ChannelAddress) (string, error) {
	entries, err := r.readCanonicalDir(r.baseDir)
	if err != nil {
		return "", fmt.Errorf("read session directory: %w", err)
	}
	matchedID := ""
	inspect := func(raw []byte) error {
		id, key, present, err := parseCanonicalSessionIdentity(raw)
		if err != nil {
			return err
		}
		if !present {
			return nil
		}
		if key != canonicalSessionKeyFor(logicalDate, address) {
			return nil
		}
		if matchedID != "" && matchedID != id {
			return fmt.Errorf("multiple canonical sessions match lookup attributes")
		}
		matchedID = id
		return nil
	}

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		fileID := strings.TrimSuffix(entry.Name(), ".json")
		if r.overlay[fileID] != nil {
			continue
		}
		raw, readErr := r.readCanonicalFile(filepath.Join(r.baseDir, entry.Name()))
		if readErr != nil {
			return "", fmt.Errorf("read session identity: %w", readErr)
		}
		if err := inspect(raw); err != nil {
			return "", err
		}
	}
	for _, pending := range r.overlay {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if err := inspect(pending.data); err != nil {
			return "", err
		}
	}
	return matchedID, nil
}

// WarmCanonicalIndex scans and validates persisted session identities once for
// this repository process. Canonical lookup calls use the in-memory index
// after this succeeds. A failed scan leaves the index unwarmed and unchanged.
func (r *JSONSessionRepository) WarmCanonicalIndex(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	r.bindingMu.RLock()
	warmed := r.canonicalIndexWarmed
	r.bindingMu.RUnlock()
	if warmed {
		return nil
	}

	entries, err := r.readCanonicalDir(r.baseDir)
	if err != nil {
		return fmt.Errorf("read session directory while warming canonical index: %w", err)
	}
	index := make(map[canonicalSessionKey]map[string]struct{})
	byID := make(map[string]canonicalSessionKey)
	ownerProjection := make(map[string]canonicalSessionOwnerProjection)
	add := func(id string, key canonicalSessionKey) error {
		if existingKey, exists := byID[id]; exists && existingKey != key {
			return fmt.Errorf("canonical session %q has conflicting identities", id)
		}
		if index[key] == nil {
			index[key] = make(map[string]struct{})
		}
		index[key][id] = struct{}{}
		byID[id] = key
		return nil
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		fileID := strings.TrimSuffix(entry.Name(), ".json")
		if r.overlay[fileID] != nil {
			continue
		}
		raw, readErr := r.readCanonicalFile(filepath.Join(r.baseDir, entry.Name()))
		if readErr != nil {
			return fmt.Errorf("read session identity %q while warming canonical index: %w", entry.Name(), readErr)
		}
		id, key, present, err := parseCanonicalSessionIdentity(raw)
		if err != nil {
			return fmt.Errorf("invalid canonical identity in %q: %w", entry.Name(), err)
		}
		if !present {
			continue
		}
		if id != fileID {
			return fmt.Errorf("canonical session id %q does not match filename %q", id, entry.Name())
		}
		migrated, changed, err := ensurePublicationIncarnationInJSON(raw)
		if err != nil {
			return fmt.Errorf("invalid canonical publication incarnation in %q: %w", entry.Name(), err)
		}
		if changed {
			if err := r.writeSessionBytes(ctx, fileID, migrated); err != nil {
				return fmt.Errorf("persist canonical publication incarnation migration for %q: %w", entry.Name(), err)
			}
			raw = migrated
		}
		if err := add(id, key); err != nil {
			return err
		}
		projection, err := r.canonicalOwnerProjectionFromRecord(ctx, id, key, raw)
		if err != nil {
			return fmt.Errorf("build canonical owner projection for %q: %w", entry.Name(), err)
		}
		projection, err = withFreshCanonicalSessionBindingGeneration(projection)
		if err != nil {
			return fmt.Errorf("assign canonical Session binding generation for %q: %w", entry.Name(), err)
		}
		ownerProjection[id] = projection
	}
	for id, pending := range r.overlay {
		if err := ctx.Err(); err != nil {
			return err
		}
		pendingID, key, present, err := parseCanonicalSessionIdentity(pending.data)
		if err != nil {
			return fmt.Errorf("invalid canonical identity in pending session %q: %w", id, err)
		}
		if !present || pendingID != id {
			return fmt.Errorf("pending session %q has no matching canonical identity", id)
		}
		if oldKey, exists := byID[id]; exists && oldKey != key {
			removeCanonicalSessionIndexID(index, byID, id)
		}
		if err := add(id, key); err != nil {
			return err
		}
		projection, err := r.canonicalOwnerProjectionFromRecord(ctx, id, key, pending.data)
		if err != nil {
			return fmt.Errorf("build canonical owner projection for pending session %q: %w", id, err)
		}
		projection, err = withFreshCanonicalSessionBindingGeneration(projection)
		if err != nil {
			return fmt.Errorf("assign canonical Session binding generation for pending session %q: %w", id, err)
		}
		ownerProjection[id] = projection
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.bindingMu.Lock()
	r.canonicalIndex = index
	r.canonicalIndexBySessionID = byID
	r.canonicalOwnerProjection = ownerProjection
	r.canonicalIndexWarmed = true
	r.bindingMu.Unlock()
	return nil
}

// CanonicalIndexWarmed reports whether startup successfully built the
// process-local canonical index. Before warming, lookups retain the legacy
// synchronous scan path for callers that have not yet adopted startup wiring.
func (r *JSONSessionRepository) CanonicalIndexWarmed() bool {
	r.bindingMu.RLock()
	defer r.bindingMu.RUnlock()
	return r.canonicalIndexWarmed
}

func (r *JSONSessionRepository) readCanonicalDir(path string) ([]os.DirEntry, error) {
	if r.readCanonicalDirectory != nil {
		return r.readCanonicalDirectory(path)
	}
	return os.ReadDir(path)
}

func (r *JSONSessionRepository) readCanonicalFile(path string) ([]byte, error) {
	if r.readCanonicalIdentityFile != nil {
		return r.readCanonicalIdentityFile(path)
	}
	return os.ReadFile(path)
}

func parseCanonicalSessionIdentity(raw []byte) (string, canonicalSessionKey, bool, error) {
	if err := rejectLegacySessionIdentityFields(raw); err != nil {
		return "", canonicalSessionKey{}, false, err
	}
	if _, _, err := canonicalOwnerBindingFromJSON(raw); err != nil {
		return "", canonicalSessionKey{}, false, err
	}
	var probe sessionIdentityProbe
	if err := json.Unmarshal(raw, &probe); err != nil {
		return "", canonicalSessionKey{}, false, fmt.Errorf("decode session identity: %w", err)
	}
	if probe.ID == "" && probe.LogicalDate == "" && probe.ChannelAddress == nil {
		return "", canonicalSessionKey{}, false, nil
	}
	if probe.ID == "" || probe.LogicalDate == "" || probe.ChannelAddress == nil {
		return "", canonicalSessionKey{}, false, fmt.Errorf("canonical session identity is incomplete")
	}
	address, err := channelAddressFromDTO(probe.ChannelAddress)
	if err != nil {
		return "", canonicalSessionKey{}, false, fmt.Errorf("canonical ChannelAddress is invalid: %w", err)
	}
	if err := session.ValidateLogicalDate(probe.LogicalDate); err != nil {
		return "", canonicalSessionKey{}, false, fmt.Errorf("canonical logical_date is invalid: %w", err)
	}
	if err := modulecore.SessionID(probe.ID).Validate(); err != nil {
		return "", canonicalSessionKey{}, false, fmt.Errorf("canonical session_id is invalid: %w", err)
	}
	return probe.ID, canonicalSessionKeyFor(probe.LogicalDate, address), true, nil
}

func (r *JSONSessionRepository) canonicalOwnerProjectionFromRecord(ctx context.Context, expectedID string, expectedKey canonicalSessionKey, data []byte) (canonicalSessionOwnerProjection, error) {
	id, key, present, err := parseCanonicalSessionIdentity(data)
	if err != nil {
		return canonicalSessionOwnerProjection{}, err
	}
	if !present || id != expectedID || key != expectedKey {
		return canonicalSessionOwnerProjection{}, fmt.Errorf("canonical Session identity does not match its projection key")
	}
	_, ownerBound, err := canonicalOwnerBindingFromJSON(data)
	if err != nil {
		return canonicalSessionOwnerProjection{}, err
	}
	intent, err := creationIntentFromJSON(data)
	if err != nil {
		return canonicalSessionOwnerProjection{}, err
	}
	if intent != nil {
		if ownerBound {
			return canonicalSessionOwnerProjection{}, fmt.Errorf("canonical Session duplicates owner provenance")
		}
	}

	value, err := r.sessionFromBytes(ctx, expectedID, data)
	if err != nil {
		return canonicalSessionOwnerProjection{}, err
	}
	if value.ID() != expectedID || canonicalSessionKeyFor(value.LogicalDate(), value.ChannelAddress()) != key {
		return canonicalSessionOwnerProjection{}, fmt.Errorf("decoded canonical Session identity does not match its record")
	}
	projection := canonicalSessionOwnerProjection{
		creationIntent: intent,
	}
	projection.value = value
	return projection, nil
}

func withFreshCanonicalSessionBindingGeneration(projection canonicalSessionOwnerProjection) (canonicalSessionOwnerProjection, error) {
	generation, err := nextCanonicalSessionBindingGeneration()
	if err != nil {
		return canonicalSessionOwnerProjection{}, err
	}
	projection.bindingGeneration = generation
	return projection, nil
}

func sameCanonicalSessionBindingIdentity(existing canonicalSessionOwnerProjection, existingKey canonicalSessionKey, next canonicalSessionOwnerProjection, nextKey canonicalSessionKey) bool {
	if !sameCanonicalSessionRouteIdentity(existing, existingKey, next, nextKey) {
		return false
	}
	return existing.value.PublicationIncarnation() == next.value.PublicationIncarnation()
}

func sameCanonicalSessionRouteIdentity(existing canonicalSessionOwnerProjection, existingKey canonicalSessionKey, next canonicalSessionOwnerProjection, nextKey canonicalSessionKey) bool {
	if existing.value == nil || next.value == nil || existing.value.ID() != next.value.ID() {
		return false
	}
	if existing.value.OwnerID() != next.value.OwnerID() || existing.value.ChannelAddress() != next.value.ChannelAddress() || existingKey != nextKey {
		return false
	}
	if (existing.creationIntent == nil) != (next.creationIntent == nil) {
		return false
	}
	if existing.creationIntent != nil && *existing.creationIntent != *next.creationIntent {
		return false
	}
	return true
}

// invalidateCanonicalProjectionBeforeSave revokes an existing binding before
// a save reads or writes its Session file when the canonical identity changes.
// Owner and address changes that the legacy Save contract rejects are left for
// that contract to reject without revoking a valid existing binding.
func (r *JSONSessionRepository) invalidateCanonicalProjectionBeforeSave(sess *session.Session) error {
	key := canonicalSessionKeyFor(sess.LogicalDate(), sess.ChannelAddress())
	r.bindingMu.Lock()
	defer r.bindingMu.Unlock()
	if err := r.validateCanonicalIndexUpdateLocked(sess.ID(), key); err != nil {
		return err
	}
	existing, exists := r.canonicalOwnerProjection[sess.ID()]
	if !exists || existing.value == nil || existing.creationIntent != nil {
		return nil
	}
	if existing.value.OwnerID() != sess.OwnerID() {
		return nil
	}
	if existing.value.OwnerID() != "" && existing.value.ChannelAddress() != sess.ChannelAddress() {
		return nil
	}
	existingKey := r.canonicalIndexBySessionID[sess.ID()]
	next := canonicalSessionOwnerProjection{value: sess}
	if !sameCanonicalSessionRouteIdentity(existing, existingKey, next, key) {
		r.removeCanonicalIndexEntryLocked(sess.ID())
	}
	return nil
}

// prepareCanonicalProjectionForWrite chooses a stable generation for ordinary
// saves and a new generation for a newly admitted or rekeyed canonical Session.
// Its identity transition is atomic with the in-memory lookup projection.
func (r *JSONSessionRepository) prepareCanonicalProjectionForWrite(id string, key canonicalSessionKey, projection canonicalSessionOwnerProjection) (canonicalSessionOwnerProjection, error) {
	r.bindingMu.Lock()
	defer r.bindingMu.Unlock()
	if err := r.validateCanonicalIndexUpdateLocked(id, key); err != nil {
		return canonicalSessionOwnerProjection{}, err
	}
	if existing, exists := r.canonicalOwnerProjection[id]; exists {
		existingKey := r.canonicalIndexBySessionID[id]
		if sameCanonicalSessionBindingIdentity(existing, existingKey, projection, key) {
			projection.bindingGeneration = existing.bindingGeneration
			return projection, nil
		}
		r.removeCanonicalIndexEntryLocked(id)
	}
	generation, err := nextCanonicalSessionBindingGeneration()
	if err != nil {
		return canonicalSessionOwnerProjection{}, err
	}
	projection.bindingGeneration = generation
	return projection, nil
}

func canonicalSessionKeyFor(logicalDate string, address conversation.ChannelAddress) canonicalSessionKey {
	return canonicalSessionKey{
		logicalDate:            logicalDate,
		channelType:            address.ChannelType(),
		externalConversationID: address.ExternalConversationID(),
	}
}

func (r *JSONSessionRepository) validateCanonicalIndexUpdate(id string, key canonicalSessionKey) error {
	r.bindingMu.RLock()
	defer r.bindingMu.RUnlock()
	return r.validateCanonicalIndexUpdateLocked(id, key)
}

func (r *JSONSessionRepository) validateCanonicalIndexUpdateLocked(id string, key canonicalSessionKey) error {
	if !r.canonicalIndexWarmed {
		return nil
	}
	if existingIDs := r.canonicalIndex[key]; len(existingIDs) > 0 {
		if _, updatingExisting := existingIDs[id]; updatingExisting {
			return nil
		}
		return fmt.Errorf("multiple canonical sessions match lookup attributes")
	}
	return nil
}

func (r *JSONSessionRepository) updateCanonicalIndexLocked(id string, key canonicalSessionKey) {
	if r.canonicalIndex == nil {
		r.canonicalIndex = make(map[canonicalSessionKey]map[string]struct{})
	}
	if r.canonicalIndexBySessionID == nil {
		r.canonicalIndexBySessionID = make(map[string]canonicalSessionKey)
	}
	if oldKey, exists := r.canonicalIndexBySessionID[id]; exists && oldKey != key {
		removeCanonicalSessionIndexID(r.canonicalIndex, r.canonicalIndexBySessionID, id)
	}
	if r.canonicalIndex[key] == nil {
		r.canonicalIndex[key] = make(map[string]struct{})
	}
	r.canonicalIndex[key][id] = struct{}{}
	r.canonicalIndexBySessionID[id] = key
}

func (r *JSONSessionRepository) updateCanonicalOwnerProjectionLocked(id string, projection canonicalSessionOwnerProjection) {
	if r.canonicalOwnerProjection == nil {
		r.canonicalOwnerProjection = make(map[string]canonicalSessionOwnerProjection)
	}
	r.canonicalOwnerProjection[id] = projection
}

func (r *JSONSessionRepository) removeCanonicalIndexEntry(id string) {
	r.bindingMu.Lock()
	defer r.bindingMu.Unlock()
	r.removeCanonicalIndexEntryLocked(id)
}

func (r *JSONSessionRepository) removeCanonicalIndexEntryLocked(id string) {
	removeCanonicalSessionIndexID(r.canonicalIndex, r.canonicalIndexBySessionID, id)
	delete(r.canonicalOwnerProjection, id)
}

func (r *JSONSessionRepository) installCanonicalProjection(id string, key canonicalSessionKey, projection canonicalSessionOwnerProjection) {
	r.bindingMu.Lock()
	defer r.bindingMu.Unlock()
	r.updateCanonicalIndexLocked(id, key)
	r.updateCanonicalOwnerProjectionLocked(id, projection)
}

func removeCanonicalSessionIndexID(index map[canonicalSessionKey]map[string]struct{}, byID map[string]canonicalSessionKey, id string) {
	key, exists := byID[id]
	if !exists {
		return
	}
	delete(index[key], id)
	if len(index[key]) == 0 {
		delete(index, key)
	}
	delete(byID, id)
}

// NewJSONSessionRepository は新しいJSONSessionRepositoryを作成
func NewJSONSessionRepository(baseDir string) *JSONSessionRepository {
	return &JSONSessionRepository{
		baseDir:                   baseDir,
		overlay:                   make(map[string]*pendingSessionWrite),
		canonicalIndex:            make(map[canonicalSessionKey]map[string]struct{}),
		canonicalIndexBySessionID: make(map[string]canonicalSessionKey),
		canonicalOwnerProjection:  make(map[string]canonicalSessionOwnerProjection),
	}
}

// sessionDTO はJSONシリアライズ用のDTO
type sessionDTO struct {
	ID                     string                  `json:"id"`
	OwnerID                string                  `json:"owner_id,omitempty"`
	PublicationIncarnation string                  `json:"publication_incarnation"`
	LogicalDate            string                  `json:"logical_date"`
	ChannelAddress         *channelAddressDTO      `json:"channel_address"`
	CreationIntent         *session.CreationIntent `json:"creation_intent,omitempty"`
	History                []turnInputDTO          `json:"history"`
	Memory                 map[string]interface{}  `json:"memory"`
	CreatedAt              time.Time               `json:"created_at"`
	UpdatedAt              time.Time               `json:"updated_at"`
}

// turnInputDTO is the JSON projection for one canonical conversation input.
// SessionID is intentionally absent: the parent Session owns that boundary.
type turnInputDTO struct {
	RootTaskID      string                  `json:"root_task_id"`
	TurnID          string                  `json:"turn_id"`
	TraceID         string                  `json:"trace_id"`
	UserMessageID   string                  `json:"user_message_id"`
	AgentMessageID  string                  `json:"agent_message_id"`
	MessageText     string                  `json:"message_text"`
	ChannelAddress  *channelAddressDTO      `json:"channel_address"`
	Attachments     []attachment.Attachment `json:"attachments"`
	ViewerRecipient string                  `json:"viewer_recipient,omitempty"`
	ForcedRoute     string                  `json:"forced_route,omitempty"`
	Route           string                  `json:"route,omitempty"`
}

// Save はセッションを保存
func (r *JSONSessionRepository) Save(ctx context.Context, sess *session.Session) error {
	if _, admitted := persistencequeue.AdmissionFromContext(ctx); !admitted {
		return r.saveAfterPendingWrites(ctx, sess)
	}
	unlock, err := r.lockForContext(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	return r.save(ctx, sess)
}

func (r *JSONSessionRepository) saveAfterPendingWrites(ctx context.Context, sess *session.Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateCanonicalSession(sess); err != nil {
		return err
	}
	for {
		pending, saveErr := func() (*pendingSessionWrite, error) {
			unlock, err := r.lockForContext(ctx)
			if err != nil {
				return nil, err
			}
			defer unlock()
			if pending := r.overlay[sess.ID()]; pending != nil {
				return pending, nil
			}
			return nil, r.save(ctx, sess)
		}()
		if saveErr != nil {
			return saveErr
		}
		if pending == nil {
			return nil
		}
		if err := pending.waitForDurability(ctx); err != nil {
			return fmt.Errorf("wait for earlier admitted session write: %w", err)
		}
	}
}

func (pending *pendingSessionWrite) waitForDurability(ctx context.Context) error {
	if pending == nil {
		return nil
	}
	if pending.done == nil || pending.admission == nil {
		return fmt.Errorf("pending session write has no durability ticket")
	}

	barrierCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	barrierDone := make(chan error, 1)
	// The command-local completion is the durability barrier for this session.
	// Watch the admission barrier only to surface a prior queue failure that
	// prevents this command from ever reaching Apply; cancel it as soon as this
	// specific command completes so later sessions do not extend the wait.
	go func() {
		barrierDone <- pending.admission.Barrier(barrierCtx)
	}()

	select {
	case <-pending.done:
		return pending.err
	case err := <-barrierDone:
		select {
		case <-pending.done:
			return pending.err
		default:
			return err
		}
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *JSONSessionRepository) save(ctx context.Context, sess *session.Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateCanonicalSession(sess); err != nil {
		return err
	}
	if err := r.invalidateCanonicalProjectionBeforeSave(sess); err != nil {
		return err
	}
	existingData, readErr := r.readSessionBytes(ctx, sess.ID())
	if readErr != nil && !errors.Is(readErr, session.ErrSessionNotFound) {
		return readErr
	}
	if readErr == nil {
		intent, err := creationIntentFromJSON(existingData)
		if err != nil {
			return err
		}
		if intent != nil {
			return fmt.Errorf("cannot save a response-first v2 reserved session through the legacy session contract")
		}
		existingOwnerID, hasOwner, err := canonicalOwnerBindingFromJSON(existingData)
		if err != nil {
			return err
		}
		if hasOwner != (sess.OwnerID() != "") || hasOwner && existingOwnerID != sess.OwnerID() {
			return fmt.Errorf("canonical Session owner binding cannot be changed or removed")
		}
		if hasOwner {
			existingID, existingKey, present, err := parseCanonicalSessionIdentity(existingData)
			if err != nil {
				return err
			}
			address := sess.ChannelAddress()
			if !present || existingID != sess.ID() || existingKey.channelType != address.ChannelType() || existingKey.externalConversationID != address.ExternalConversationID() {
				return fmt.Errorf("canonical Session owner binding does not match the persisted ChannelAddress")
			}
		}
	}
	if err := r.preparePublicationIncarnationForSave(sess, existingData, readErr); err != nil {
		return err
	}
	dto := r.toDTO(sess)
	if admission, ok := persistencequeue.AdmissionFromContext(ctx); ok {
		return r.enqueueSessionWrite(ctx, admission, sess.ID(), dto)
	}
	return r.saveDTO(ctx, sess, dto)
}

func (r *JSONSessionRepository) preparePublicationIncarnationForSave(sess *session.Session, existingData []byte, readErr error) error {
	if sess == nil {
		return fmt.Errorf("canonical Session is required")
	}
	if errors.Is(readErr, session.ErrSessionNotFound) {
		return sess.RenewPublicationIncarnation()
	}
	if readErr != nil {
		return readErr
	}

	existingID, existingKey, present, err := parseCanonicalSessionIdentity(existingData)
	if err != nil {
		return fmt.Errorf("read existing canonical Session identity: %w", err)
	}
	if !present || existingID != sess.ID() {
		return fmt.Errorf("existing canonical Session identity does not match the save target")
	}
	ownerID, ownerBound, err := canonicalOwnerBindingFromJSON(existingData)
	if err != nil {
		return err
	}
	currentIncarnation, hasIncarnation, err := canonicalPublicationIncarnationFromJSON(existingData)
	if err != nil {
		return err
	}
	identityMatches := existingKey == canonicalSessionKeyFor(sess.LogicalDate(), sess.ChannelAddress()) &&
		ownerBound == (sess.OwnerID() != "") && (!ownerBound || ownerID == sess.OwnerID())
	if identityMatches && hasIncarnation {
		return sess.RestorePublicationIncarnation(currentIncarnation)
	}
	// A same-ID rekey or a legacy record being migrated starts a fresh
	// publication lifecycle. The canonical identity change is separately
	// revoked from RAM before this durable replacement is attempted.
	return sess.RenewPublicationIncarnation()
}

func (r *JSONSessionRepository) saveDTO(ctx context.Context, sess *session.Session, dto *sessionDTO) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateCanonicalSession(sess); err != nil {
		return err
	}
	if dto == nil || dto.ID != sess.ID() || dto.LogicalDate != sess.LogicalDate() {
		return fmt.Errorf("session DTO identity does not match the canonical Session")
	}
	if err := session.ValidatePublicationIncarnation(dto.PublicationIncarnation); err != nil || dto.PublicationIncarnation != sess.PublicationIncarnation() {
		return fmt.Errorf("session DTO publication incarnation does not match the canonical Session")
	}
	address, err := channelAddressFromDTO(dto.ChannelAddress)
	if err != nil || address != sess.ChannelAddress() {
		return fmt.Errorf("session DTO channel address does not match the canonical Session")
	}
	if err := validateCreationIntent(dto.CreationIntent); err != nil {
		return err
	}
	if dto.OwnerID != sess.OwnerID() {
		return fmt.Errorf("session DTO would change or remove the canonical Session owner binding")
	}
	if dto.OwnerID != "" {
		if err := session.ValidateOwnerID(dto.OwnerID); err != nil {
			return fmt.Errorf("invalid canonical Session owner: %w", err)
		}
		if dto.CreationIntent != nil {
			return fmt.Errorf("canonical Session cannot duplicate owner provenance")
		}
	}
	key := canonicalSessionKeyFor(dto.LogicalDate, address)
	if err := r.validateCanonicalIndexUpdate(sess.ID(), key); err != nil {
		return err
	}

	data, err := marshalSessionDTO(dto)
	if err != nil {
		return err
	}
	projection, err := r.canonicalOwnerProjectionFromRecord(ctx, sess.ID(), key, data)
	if err != nil {
		return fmt.Errorf("prepare canonical Session owner projection: %w", err)
	}
	projection, err = r.prepareCanonicalProjectionForWrite(sess.ID(), key, projection)
	if err != nil {
		return fmt.Errorf("prepare canonical Session binding generation: %w", err)
	}
	if err := r.writeSessionBytes(ctx, sess.ID(), data); err != nil {
		return err
	}
	r.installCanonicalProjection(sess.ID(), key, projection)
	return nil
}

func marshalSessionDTO(dto *sessionDTO) ([]byte, error) {
	data, err := json.MarshalIndent(dto, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal session: %w", err)
	}
	return data, nil
}

func (r *JSONSessionRepository) enqueueSessionWrite(ctx context.Context, admission *persistencequeue.Admission, id string, dto *sessionDTO) error {
	data, err := marshalSessionDTO(dto)
	if err != nil {
		return err
	}
	identityID, key, present, err := parseCanonicalSessionIdentity(data)
	if err != nil {
		return err
	}
	if !present || identityID != id {
		return fmt.Errorf("session write does not contain matching canonical identity")
	}
	if err := r.validateCanonicalIndexUpdate(id, key); err != nil {
		return err
	}
	projection, err := r.canonicalOwnerProjectionFromRecord(ctx, id, key, data)
	if err != nil {
		return fmt.Errorf("prepare admitted canonical Session owner projection: %w", err)
	}
	projection, err = r.prepareCanonicalProjectionForWrite(id, key, projection)
	if err != nil {
		return fmt.Errorf("prepare admitted Session binding generation: %w", err)
	}
	entry := &pendingSessionWrite{
		data:      data,
		done:      make(chan struct{}),
		admission: admission,
	}
	_, err = admission.Enqueue(persistencequeue.PersistenceCommand{
		Owner:     persistencequeue.OwnerSession,
		Kind:      "session.json.write",
		SizeBytes: int64(len(data)),
		Apply: func(ctx context.Context) (applyErr error) {
			defer func() {
				entry.err = applyErr
				close(entry.done)
			}()
			r.mu.Lock()
			if r.overlay[id] != entry {
				r.mu.Unlock()
				return nil
			}
			r.writeMu.Lock()
			r.mu.Unlock()
			err := r.writeSessionBytesLocked(ctx, id, entry.data)
			r.writeMu.Unlock()
			if err != nil {
				return err
			}
			r.mu.Lock()
			if r.overlay[id] == entry {
				delete(r.overlay, id)
			}
			r.mu.Unlock()
			return nil
		},
	})
	if err != nil {
		return err
	}
	if r.overlay == nil {
		r.overlay = make(map[string]*pendingSessionWrite)
	}
	r.overlay[id] = entry
	r.installCanonicalProjection(id, key, projection)
	return nil
}

func (r *JSONSessionRepository) writeSessionBytes(ctx context.Context, id string, data []byte) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	return r.writeSessionBytesLocked(ctx, id, data)
}

func (r *JSONSessionRepository) writeSessionBytesLocked(ctx context.Context, id string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.beforeWriteSessionBytes != nil {
		if err := r.beforeWriteSessionBytes(ctx, id, data); err != nil {
			return err
		}
	}
	temporary, err := os.CreateTemp(r.baseDir, ".session-*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create session temp file: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0600); err != nil {
		return fmt.Errorf("failed to secure session temp file: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("failed to write session temp file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("failed to sync session temp file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("failed to close session temp file: %w", err)
	}
	if err := os.Rename(temporaryPath, r.getFilePath(id)); err != nil {
		return fmt.Errorf("failed to commit session file: %w", err)
	}
	committed = true
	return nil
}

// Load はセッションをロード
func (r *JSONSessionRepository) Load(ctx context.Context, id string) (*session.Session, error) {
	unlock, err := r.lockForContext(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return r.load(ctx, id)
}

func (r *JSONSessionRepository) load(ctx context.Context, id string) (*session.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := modulecore.SessionID(id).Validate(); err != nil {
		return nil, fmt.Errorf("invalid canonical session_id: %w", err)
	}
	if projection, _, _, warmed, exists := r.canonicalOwnerProjectionSnapshot(id); warmed {
		if !exists {
			return nil, fmt.Errorf("session %s: %w", id, session.ErrSessionNotFound)
		}
		return r.cloneSession(ctx, projection.value)
	}
	data, err := r.readSessionBytes(ctx, id)
	if err != nil {
		if errors.Is(err, session.ErrSessionNotFound) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("failed to read session file: %w", err)
	}
	return r.sessionFromBytes(ctx, id, data)
}

func (r *JSONSessionRepository) sessionFromBytes(ctx context.Context, id string, data []byte) (*session.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := modulecore.SessionID(id).Validate(); err != nil {
		return nil, fmt.Errorf("invalid canonical session_id: %w", err)
	}
	if err := rejectLegacySessionIdentityFields(data); err != nil {
		return nil, err
	}

	var dto sessionDTO
	if err := json.Unmarshal(data, &dto); err != nil {
		return nil, fmt.Errorf("failed to unmarshal session: %w", err)
	}
	ownerID, _, err := canonicalOwnerBindingFromJSON(data)
	if err != nil {
		return nil, err
	}
	dto.OwnerID = ownerID
	intent, err := creationIntentFromJSON(data)
	if err != nil {
		return nil, err
	}
	dto.CreationIntent = intent

	result, err := r.fromDTO(&dto)
	if err != nil {
		return nil, fmt.Errorf("failed to reconstruct session: %w", err)
	}
	return result, nil
}

func (r *JSONSessionRepository) cloneSession(ctx context.Context, value *session.Session) (*session.Session, error) {
	if value == nil {
		return nil, fmt.Errorf("canonical Session clone source is required")
	}
	data, err := marshalSessionDTO(r.toDTO(value))
	if err != nil {
		return nil, err
	}
	return r.sessionFromBytes(ctx, value.ID(), data)
}

func cloneCreationIntent(intent *session.CreationIntent) *session.CreationIntent {
	if intent == nil {
		return nil
	}
	clone := *intent
	return &clone
}

func (r *JSONSessionRepository) creationIntent(ctx context.Context, id string) (*session.CreationIntent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if projection, _, _, warmed, exists := r.canonicalOwnerProjectionSnapshot(id); warmed {
		if !exists {
			return nil, fmt.Errorf("session %s: %w", id, session.ErrSessionNotFound)
		}
		return cloneCreationIntent(projection.creationIntent), nil
	}
	data, err := r.readSessionBytes(ctx, id)
	if err != nil {
		if errors.Is(err, session.ErrSessionNotFound) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("failed to read session creation intent: %w", err)
	}
	return creationIntentFromJSON(data)
}

func (r *JSONSessionRepository) readSessionBytes(ctx context.Context, id string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if pending := r.overlay[id]; pending != nil {
		return pending.data, nil
	}
	read := os.ReadFile
	if r.readSessionFile != nil {
		read = r.readSessionFile
	}
	data, err := read(r.getFilePath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("session %s: %w", id, session.ErrSessionNotFound)
		}
		return nil, err
	}
	return data, nil
}

func creationIntentFromJSON(data []byte) (*session.CreationIntent, error) {
	if err := validateSensitiveSessionJSONKeys(data); err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, fmt.Errorf("decode session creation intent: %w", err)
	}
	raw, exists := fields["creation_intent"]
	if !exists {
		return nil, nil
	}
	var intentFields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &intentFields); err != nil || len(intentFields) != 2 {
		return nil, fmt.Errorf("session creation_intent is invalid")
	}
	if _, ok := intentFields["runtime"]; !ok {
		return nil, fmt.Errorf("session creation_intent.runtime is required")
	}
	if _, ok := intentFields["owner_id"]; !ok {
		return nil, fmt.Errorf("session creation_intent.owner_id is required")
	}
	var intent session.CreationIntent
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&intent); err != nil {
		return nil, fmt.Errorf("session creation_intent is invalid: %w", err)
	}
	if err := validateCreationIntent(&intent); err != nil {
		return nil, err
	}
	return &intent, nil
}

func canonicalOwnerBindingFromJSON(data []byte) (string, bool, error) {
	if err := validateSensitiveSessionJSONKeys(data); err != nil {
		return "", false, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return "", false, fmt.Errorf("decode session owner binding: %w", err)
	}
	raw, exists := fields["owner_id"]
	if !exists {
		return "", false, nil
	}
	if _, hasCreationIntent := fields["creation_intent"]; hasCreationIntent {
		return "", false, fmt.Errorf("canonical Session duplicates owner provenance")
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false, fmt.Errorf("session owner_id is invalid: %w", err)
	}
	ownerID, ok := value.(string)
	if !ok {
		return "", false, fmt.Errorf("session owner_id must be a string")
	}
	if err := session.ValidateOwnerID(ownerID); err != nil {
		return "", false, fmt.Errorf("session owner_id is invalid: %w", err)
	}
	return ownerID, true, nil
}

func canonicalPublicationIncarnationFromJSON(data []byte) (string, bool, error) {
	if err := validateSensitiveSessionJSONKeys(data); err != nil {
		return "", false, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return "", false, fmt.Errorf("decode session publication incarnation: %w", err)
	}
	raw, exists := fields["publication_incarnation"]
	if !exists {
		return "", false, nil
	}
	var incarnation string
	if err := json.Unmarshal(raw, &incarnation); err != nil {
		return "", false, fmt.Errorf("session publication_incarnation must be a string: %w", err)
	}
	if err := session.ValidatePublicationIncarnation(incarnation); err != nil {
		return "", false, fmt.Errorf("session publication_incarnation is invalid: %w", err)
	}
	return incarnation, true, nil
}

func ensurePublicationIncarnationInJSON(data []byte) ([]byte, bool, error) {
	_, exists, err := canonicalPublicationIncarnationFromJSON(data)
	if err != nil || exists {
		return data, false, err
	}
	incarnation, err := session.NewPublicationIncarnation()
	if err != nil {
		return nil, false, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, false, fmt.Errorf("decode legacy Session for publication migration: %w", err)
	}
	encoded, err := json.Marshal(incarnation)
	if err != nil {
		return nil, false, fmt.Errorf("encode Session publication incarnation: %w", err)
	}
	fields["publication_incarnation"] = encoded
	migrated, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return nil, false, fmt.Errorf("marshal migrated Session publication incarnation: %w", err)
	}
	return migrated, true, nil
}

// validateSensitiveSessionJSONKeys prevents encoding/json's case-insensitive
// struct-field matching from accepting alternate or duplicate spellings for
// persisted owner provenance. Those keys are security-sensitive because the
// raw owner helpers intentionally use exact JSON names.
func validateSensitiveSessionJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	opening, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode session record keys: %w", err)
	}
	if opening != json.Delim('{') {
		return fmt.Errorf("session record must be a JSON object")
	}
	seen := make(map[string]struct{}, 2)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("decode session record key: %w", err)
		}
		key, ok := token.(string)
		if !ok {
			return fmt.Errorf("session record key must be a string")
		}
		canonicalKey, sensitive := canonicalSensitiveSessionJSONKey(key)
		if sensitive {
			if key != canonicalKey {
				return fmt.Errorf("session record key %q must use canonical spelling %q", key, canonicalKey)
			}
			if _, duplicate := seen[canonicalKey]; duplicate {
				return fmt.Errorf("session record key %q is duplicated", canonicalKey)
			}
			seen[canonicalKey] = struct{}{}
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return fmt.Errorf("decode session record value for key %q: %w", key, err)
		}
	}
	closing, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode session record end: %w", err)
	}
	if closing != json.Delim('}') {
		return fmt.Errorf("session record must end with a JSON object")
	}
	return nil
}

func canonicalSensitiveSessionJSONKey(key string) (string, bool) {
	for _, canonical := range []string{"owner_id", "creation_intent", "publication_incarnation"} {
		if strings.EqualFold(key, canonical) {
			return canonical, true
		}
	}
	return "", false
}

func validateCreationIntent(intent *session.CreationIntent) error {
	if intent == nil {
		return nil
	}
	if intent.Runtime != "v2" || strings.TrimSpace(intent.OwnerID) == "" || intent.OwnerID != strings.TrimSpace(intent.OwnerID) {
		return fmt.Errorf("session creation_intent is not a canonical response-first v2 reservation")
	}
	return nil
}

func rejectLegacySessionIdentityFields(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return fmt.Errorf("decode session identity: %w", err)
	}
	if _, exists := fields["channel"]; exists {
		return fmt.Errorf("legacy session identity field channel is not supported")
	}
	if _, exists := fields["chat_id"]; exists {
		return fmt.Errorf("legacy session identity field chat_id is not supported")
	}
	if rawHistory, exists := fields["history"]; exists {
		var history []map[string]json.RawMessage
		if err := json.Unmarshal(rawHistory, &history); err != nil {
			return fmt.Errorf("decode history: %w", err)
		}
		for index, item := range history {
			for _, key := range []string{"job_id", "user_message", "channel", "chat_id"} {
				if _, exists := item[key]; exists {
					return fmt.Errorf("legacy history field %s at index %d is not supported", key, index)
				}
			}
		}
	}
	if rawAddress, exists := fields["channel_address"]; exists {
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(rawAddress, &nested); err != nil {
			return fmt.Errorf("decode channel_address: %w", err)
		}
		if _, exists := nested["channel"]; exists {
			return fmt.Errorf("legacy channel_address field channel is not supported")
		}
		if _, exists := nested["address"]; exists {
			return fmt.Errorf("legacy channel_address field address is not supported")
		}
	}
	return nil
}

// Exists はセッションが存在するか確認
func (r *JSONSessionRepository) Exists(ctx context.Context, id string) (bool, error) {
	unlock, err := r.lockForContext(ctx)
	if err != nil {
		return false, err
	}
	defer unlock()
	if err := modulecore.SessionID(id).Validate(); err != nil {
		return false, fmt.Errorf("invalid canonical session_id: %w", err)
	}
	if r.overlay[id] != nil {
		return true, nil
	}
	filePath := r.getFilePath(id)
	_, err = os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Delete はセッションを削除
func (r *JSONSessionRepository) Delete(ctx context.Context, id string) error {
	unlock, err := r.lockForSynchronousWrite(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	if err := modulecore.SessionID(id).Validate(); err != nil {
		return fmt.Errorf("invalid canonical session_id: %w", err)
	}
	// Removing the current overlay invalidates every older queued command for
	// this session. Serialize the removal with file writes already in progress.
	delete(r.overlay, id)
	// Revoke the RAM binding before waiting for the file lock or removing the
	// durable record, so readers and publication callbacks fail closed promptly.
	r.removeCanonicalIndexEntry(id)
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	filePath := r.getFilePath(id)
	if err := os.Remove(filePath); err != nil {
		if os.IsNotExist(err) {
			return nil // 既に存在しない場合はエラーとしない
		}
		return fmt.Errorf("failed to delete session file: %w", err)
	}
	return nil
}

// getFilePath はセッションIDからファイルパスを生成
func (r *JSONSessionRepository) getFilePath(id string) string {
	return filepath.Join(r.baseDir, id+".json")
}

// toDTO はSessionをDTOに変換
func (r *JSONSessionRepository) toDTO(sess *session.Session) *sessionDTO {
	history := make([]turnInputDTO, 0, sess.HistoryCount())
	for _, input := range sess.GetHistory() {
		address := input.ChannelAddress()
		addressDTO := channelAddressDTOFromDomain(address)
		history = append(history, turnInputDTO{
			RootTaskID:      string(input.RootTaskID()),
			TurnID:          string(input.TurnID()),
			TraceID:         string(input.TraceID()),
			UserMessageID:   string(input.UserMessageID()),
			AgentMessageID:  string(input.AgentMessageID()),
			MessageText:     input.MessageText(),
			ChannelAddress:  &addressDTO,
			Attachments:     input.Attachments(),
			ViewerRecipient: input.ViewerRecipient(),
			ForcedRoute:     string(input.ForcedRoute()),
			Route:           string(input.Route()),
		})
	}

	address := sess.ChannelAddress()
	addressDTO := channelAddressDTOFromDomain(address)
	dto := &sessionDTO{
		ID:                     sess.ID(),
		OwnerID:                sess.OwnerID(),
		PublicationIncarnation: sess.PublicationIncarnation(),
		LogicalDate:            sess.LogicalDate(),
		ChannelAddress:         &addressDTO,
		History:                history,
		Memory:                 sess.GetAllMemory(),
		CreatedAt:              sess.CreatedAt(),
		UpdatedAt:              sess.UpdatedAt(),
	}
	return dto
}

// fromDTO はDTOからSessionを生成
func (r *JSONSessionRepository) fromDTO(dto *sessionDTO) (*session.Session, error) {
	address, err := channelAddressFromDTO(dto.ChannelAddress)
	if err != nil {
		return nil, err
	}
	history := make([]conversation.TurnInput, 0, len(dto.History))
	for index, inputDTO := range dto.History {
		address, err := channelAddressFromDTO(inputDTO.ChannelAddress)
		if err != nil {
			return nil, fmt.Errorf("history[%d] channel_address: %w", index, err)
		}
		input, err := conversation.ReconstructTurnInput(
			modulecore.TaskID(inputDTO.RootTaskID),
			modulecore.TurnID(inputDTO.TurnID),
			modulecore.TraceID(inputDTO.TraceID),
			modulecore.MessageID(inputDTO.UserMessageID),
			modulecore.MessageID(inputDTO.AgentMessageID),
			inputDTO.MessageText,
			address,
		)
		if err != nil {
			return nil, fmt.Errorf("history[%d] turn input: %w", index, err)
		}
		input = input.WithSessionID(dto.ID).WithAttachments(inputDTO.Attachments)
		if inputDTO.ViewerRecipient != "" {
			input = input.WithViewerRecipient(inputDTO.ViewerRecipient)
		}
		if inputDTO.ForcedRoute != "" {
			input = input.WithForcedRoute(routing.Route(inputDTO.ForcedRoute))
		}
		if inputDTO.Route != "" {
			input = input.WithRoute(routing.Route(inputDTO.Route))
		}
		history = append(history, input)
	}

	if err := session.ValidatePublicationIncarnation(dto.PublicationIncarnation); err != nil {
		return nil, fmt.Errorf("invalid Session publication incarnation: %w", err)
	}
	if dto.OwnerID != "" && dto.CreationIntent != nil {
		return nil, fmt.Errorf("canonical Session duplicates owner provenance")
	}
	var result *session.Session
	if dto.OwnerID != "" {
		result, err = session.ReconstructCanonicalSessionWithOwnerAndPublicationIncarnation(modulecore.SessionID(dto.ID), dto.OwnerID, dto.LogicalDate, address, history, dto.Memory, dto.CreatedAt, dto.UpdatedAt, dto.PublicationIncarnation)
	} else {
		result, err = session.ReconstructCanonicalSessionWithPublicationIncarnation(modulecore.SessionID(dto.ID), dto.LogicalDate, address, history, dto.Memory, dto.CreatedAt, dto.UpdatedAt, dto.PublicationIncarnation)
	}
	if err != nil {
		return nil, err
	}
	if err := validateCanonicalSession(result); err != nil {
		return nil, err
	}
	return result, nil
}

func validateCanonicalSession(sess *session.Session) error {
	if sess == nil {
		return fmt.Errorf("canonical Session is required")
	}
	if err := modulecore.SessionID(sess.ID()).Validate(); err != nil {
		return fmt.Errorf("invalid canonical session_id: %w", err)
	}
	if err := session.ValidateLogicalDate(sess.LogicalDate()); err != nil {
		return fmt.Errorf("invalid canonical logical_date: %w", err)
	}
	if err := sess.ChannelAddress().Validate(); err != nil {
		return fmt.Errorf("invalid canonical ChannelAddress: %w", err)
	}
	if err := session.ValidatePublicationIncarnation(sess.PublicationIncarnation()); err != nil {
		return fmt.Errorf("invalid canonical Session publication incarnation: %w", err)
	}
	if sess.OwnerID() != "" {
		if err := session.ValidateOwnerID(sess.OwnerID()); err != nil {
			return fmt.Errorf("invalid canonical Session owner: %w", err)
		}
	}
	for index, input := range sess.GetHistory() {
		if err := input.Validate(); err != nil {
			return fmt.Errorf("invalid canonical history[%d]: %w", index, err)
		}
		if input.SessionID() != sess.ID() {
			return fmt.Errorf("history[%d] session_id %q does not match parent session %q", index, input.SessionID(), sess.ID())
		}
		if input.ChannelAddress() != sess.ChannelAddress() {
			return fmt.Errorf("history[%d] channel_address does not match parent session", index)
		}
	}
	return nil
}
