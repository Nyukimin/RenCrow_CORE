package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	baseDir   string
	mu        sync.Mutex
	writeGate sync.RWMutex
	writeMu   sync.Mutex
	overlay   map[string]*pendingSessionWrite

	canonicalIndex            map[canonicalSessionKey]map[string]struct{}
	canonicalIndexBySessionID map[string]canonicalSessionKey
	canonicalIndexWarmed      bool
	readCanonicalDirectory    func(string) ([]os.DirEntry, error)
	readCanonicalIdentityFile func(string) ([]byte, error)
	beforeWriteSessionBytes   func(context.Context, string, []byte) error
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
	if r.canonicalIndexWarmed {
		ids := r.canonicalIndex[key]
		switch len(ids) {
		case 0:
			return "", nil
		case 1:
			for id := range ids {
				return id, nil
			}
		default:
			return "", fmt.Errorf("multiple canonical sessions match lookup attributes")
		}
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
	if r.canonicalIndexWarmed {
		return nil
	}

	entries, err := r.readCanonicalDir(r.baseDir)
	if err != nil {
		return fmt.Errorf("read session directory while warming canonical index: %w", err)
	}
	index := make(map[canonicalSessionKey]map[string]struct{})
	byID := make(map[string]canonicalSessionKey)
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
		if err := add(id, key); err != nil {
			return err
		}
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
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.canonicalIndex = index
	r.canonicalIndexBySessionID = byID
	r.canonicalIndexWarmed = true
	return nil
}

// CanonicalIndexWarmed reports whether startup successfully built the
// process-local canonical index. Before warming, lookups retain the legacy
// synchronous scan path for callers that have not yet adopted startup wiring.
func (r *JSONSessionRepository) CanonicalIndexWarmed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
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

func canonicalSessionKeyFor(logicalDate string, address conversation.ChannelAddress) canonicalSessionKey {
	return canonicalSessionKey{
		logicalDate:            logicalDate,
		channelType:            address.ChannelType(),
		externalConversationID: address.ExternalConversationID(),
	}
}

func (r *JSONSessionRepository) validateCanonicalIndexUpdate(id string, key canonicalSessionKey) error {
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

func (r *JSONSessionRepository) updateCanonicalIndex(id string, key canonicalSessionKey) {
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

func (r *JSONSessionRepository) removeCanonicalIndexEntry(id string) {
	removeCanonicalSessionIndexID(r.canonicalIndex, r.canonicalIndexBySessionID, id)
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
	}
}

// sessionDTO はJSONシリアライズ用のDTO
type sessionDTO struct {
	ID             string                  `json:"id"`
	LogicalDate    string                  `json:"logical_date"`
	ChannelAddress *channelAddressDTO      `json:"channel_address"`
	CreationIntent *session.CreationIntent `json:"creation_intent,omitempty"`
	History        []turnInputDTO          `json:"history"`
	Memory         map[string]interface{}  `json:"memory"`
	CreatedAt      time.Time               `json:"created_at"`
	UpdatedAt      time.Time               `json:"updated_at"`
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
	intent, err := r.creationIntent(ctx, sess.ID())
	if err != nil && !errors.Is(err, session.ErrSessionNotFound) {
		return err
	}
	if intent != nil {
		return fmt.Errorf("cannot save a response-first v2 reserved session through the legacy session contract")
	}
	dto := r.toDTO(sess)
	if admission, ok := persistencequeue.AdmissionFromContext(ctx); ok {
		return r.enqueueSessionWrite(admission, sess.ID(), dto)
	}
	return r.saveDTO(ctx, sess, dto)
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
	address, err := channelAddressFromDTO(dto.ChannelAddress)
	if err != nil || address != sess.ChannelAddress() {
		return fmt.Errorf("session DTO channel address does not match the canonical Session")
	}
	if err := validateCreationIntent(dto.CreationIntent); err != nil {
		return err
	}
	key := canonicalSessionKeyFor(dto.LogicalDate, address)
	if err := r.validateCanonicalIndexUpdate(sess.ID(), key); err != nil {
		return err
	}

	data, err := marshalSessionDTO(dto)
	if err != nil {
		return err
	}
	if err := r.writeSessionBytes(ctx, sess.ID(), data); err != nil {
		return err
	}
	r.updateCanonicalIndex(sess.ID(), key)
	return nil
}

func marshalSessionDTO(dto *sessionDTO) ([]byte, error) {
	data, err := json.MarshalIndent(dto, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal session: %w", err)
	}
	return data, nil
}

func (r *JSONSessionRepository) enqueueSessionWrite(admission *persistencequeue.Admission, id string, dto *sessionDTO) error {
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
	r.updateCanonicalIndex(id, key)
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
	data, err := r.readSessionBytes(ctx, id)
	if err != nil {
		if errors.Is(err, session.ErrSessionNotFound) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("failed to read session file: %w", err)
	}
	if err := rejectLegacySessionIdentityFields(data); err != nil {
		return nil, err
	}

	var dto sessionDTO
	if err := json.Unmarshal(data, &dto); err != nil {
		return nil, fmt.Errorf("failed to unmarshal session: %w", err)
	}
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

func (r *JSONSessionRepository) creationIntent(ctx context.Context, id string) (*session.CreationIntent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
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
	data, err := os.ReadFile(r.getFilePath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("session %s: %w", id, session.ErrSessionNotFound)
		}
		return nil, err
	}
	return data, nil
}

func creationIntentFromJSON(data []byte) (*session.CreationIntent, error) {
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
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	filePath := r.getFilePath(id)
	if err := os.Remove(filePath); err != nil {
		if os.IsNotExist(err) {
			r.removeCanonicalIndexEntry(id)
			return nil // 既に存在しない場合はエラーとしない
		}
		return fmt.Errorf("failed to delete session file: %w", err)
	}
	r.removeCanonicalIndexEntry(id)
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
		ID:             sess.ID(),
		LogicalDate:    sess.LogicalDate(),
		ChannelAddress: &addressDTO,
		History:        history,
		Memory:         sess.GetAllMemory(),
		CreatedAt:      sess.CreatedAt(),
		UpdatedAt:      sess.UpdatedAt(),
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

	result, err := session.ReconstructCanonicalSession(modulecore.SessionID(dto.ID), dto.LogicalDate, address, history, dto.Memory, dto.CreatedAt, dto.UpdatedAt)
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
