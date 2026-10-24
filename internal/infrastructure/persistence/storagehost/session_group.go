package storagehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	domainsession "github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	// GroupSession exposes only the existing canonical session owner contract.
	GroupSession = "session"

	// The protocol envelope is capped at 32 MiB. Session records use a lower
	// bound to leave room for that envelope and the recorded operation result.
	maxSessionPayloadBytes    = 24 << 20
	maxSessionHistoryEntries  = 100_000
	maxSessionMemoryEntries   = 100_000
	maxSessionAttachmentsTurn = 256
)

// SessionGroupOwner is the existing CORE session owner plus its canonical
// lookup operation. The storage host delegates all state decisions to it.
type SessionGroupOwner interface {
	domainsession.SessionRepository
	LoadCanonical(context.Context, string, conversation.ChannelAddress) (*domainsession.Session, bool, error)
	LoadCanonicalForOwner(context.Context, string, string, conversation.ChannelAddress) (domainsession.CanonicalSessionOwnerResolution, error)
	LoadCanonicalWithCreationIntent(context.Context, string, conversation.ChannelAddress) (*domainsession.Session, *domainsession.CreationIntent, bool, error)
	LoadOrCreateCanonical(context.Context, string, conversation.ChannelAddress, time.Time) (*domainsession.Session, bool, error)
	LoadOrCreateCanonicalForOwner(context.Context, string, conversation.ChannelAddress, string, time.Time) (domainsession.CanonicalSessionOwnerResolution, error)
	ReserveCanonicalV2(context.Context, string, conversation.ChannelAddress, string, time.Time) (*domainsession.Session, bool, error)
	IsCanonicalV2Reserved(context.Context, string, string) (bool, error)
}

type sessionAddressDTO struct {
	ChannelType            string `json:"channel_type"`
	ExternalConversationID string `json:"external_conversation_id"`
}

type sessionAttachmentDTO struct {
	ID                  string          `json:"id"`
	Kind                attachment.Kind `json:"kind"`
	Filename            string          `json:"filename"`
	ContentType         string          `json:"content_type"`
	SizeBytes           int64           `json:"size_bytes"`
	Path                string          `json:"path"`
	SHA256              string          `json:"sha256"`
	ExtractedText       string          `json:"extracted_text,omitempty"`
	ExtractionError     string          `json:"extraction_error,omitempty"`
	ExtractionTruncated bool            `json:"extraction_truncated,omitempty"`
	SecurityWarnings    []string        `json:"security_warnings,omitempty"`
}

type sessionTurnDTO struct {
	RootTaskID      string                 `json:"root_task_id"`
	TurnID          string                 `json:"turn_id"`
	TraceID         string                 `json:"trace_id"`
	UserMessageID   string                 `json:"user_message_id"`
	AgentMessageID  string                 `json:"agent_message_id"`
	MessageText     string                 `json:"message_text"`
	ChannelAddress  *sessionAddressDTO     `json:"channel_address"`
	Attachments     []sessionAttachmentDTO `json:"attachments"`
	ViewerRecipient string                 `json:"viewer_recipient,omitempty"`
	ForcedRoute     string                 `json:"forced_route,omitempty"`
	Route           string                 `json:"route,omitempty"`
}

// sessionRecordDTO carries only explicit canonical session fields. Memory
// values remain bounded JSON values because the domain API is map[string]any.
type sessionRecordDTO struct {
	ID             string                     `json:"id"`
	OwnerID        string                     `json:"owner_id,omitempty"`
	LogicalDate    string                     `json:"logical_date"`
	ChannelAddress *sessionAddressDTO         `json:"channel_address"`
	History        []sessionTurnDTO           `json:"history"`
	Memory         map[string]json.RawMessage `json:"memory"`
	CreatedAt      time.Time                  `json:"created_at"`
	UpdatedAt      time.Time                  `json:"updated_at"`
}

type sessionSavePayload struct {
	Session *sessionRecordDTO `json:"session"`
}

type sessionIDPayload struct {
	ID string `json:"id"`
}

type sessionCanonicalLookupPayload struct {
	LogicalDate    string             `json:"logical_date"`
	ChannelAddress *sessionAddressDTO `json:"channel_address"`
	CreatedAt      time.Time          `json:"created_at"`
}

type sessionCanonicalReadPayload struct {
	LogicalDate    string             `json:"logical_date"`
	ChannelAddress *sessionAddressDTO `json:"channel_address"`
}

type sessionCanonicalReservePayload struct {
	LogicalDate    string             `json:"logical_date"`
	ChannelAddress *sessionAddressDTO `json:"channel_address"`
	OwnerID        string             `json:"owner_id"`
	CreatedAt      time.Time          `json:"created_at"`
}

type sessionCanonicalOwnerReadPayload struct {
	ID             string             `json:"id"`
	OwnerID        string             `json:"owner_id"`
	ChannelAddress *sessionAddressDTO `json:"channel_address"`
}

type sessionCanonicalOwnerLookupPayload struct {
	LogicalDate    string             `json:"logical_date"`
	ChannelAddress *sessionAddressDTO `json:"channel_address"`
	OwnerID        string             `json:"owner_id"`
	CreatedAt      time.Time          `json:"created_at"`
}

type sessionReservationIDPayload struct {
	ID      string `json:"id"`
	OwnerID string `json:"owner_id"`
}

type sessionCanonicalReservationResult struct {
	Found          bool                          `json:"found"`
	Session        *sessionRecordDTO             `json:"session,omitempty"`
	CreationIntent *domainsession.CreationIntent `json:"creation_intent,omitempty"`
}

type sessionLoadResult struct {
	Found   bool              `json:"found"`
	Created bool              `json:"created"`
	Session *sessionRecordDTO `json:"session,omitempty"`
}

type sessionExistsResult struct {
	Exists bool `json:"exists"`
}

type sessionReservationResult struct {
	Reserved bool `json:"reserved"`
}

type sessionCanonicalOwnerResolutionResult struct {
	Status       domainsession.CanonicalSessionOwnerStatus `json:"status"`
	BoundOwnerID string                                    `json:"bound_owner_id,omitempty"`
	Session      *sessionRecordDTO                         `json:"session,omitempty"`
}

// sessionLoadResultDecoder validates and reconstructs a typed domain result
// inside Client.Call's decode boundary. For mutating canonical lookup, any
// failure here remains outcome-unknown with the same op_id.
type sessionLoadResultDecoder struct {
	found                  bool
	created                bool
	session                *domainsession.Session
	requireFound           bool
	allowCreated           bool
	expectedID             string
	expectedLogicalDate    string
	expectedChannelAddress *conversation.ChannelAddress
}

func (result *sessionLoadResultDecoder) UnmarshalJSON(raw []byte) error {
	var wire sessionLoadResult
	if err := decodeSessionMessage(raw, &wire); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return errors.New("session result is not JSON")
	}
	if _, ok := fields["found"]; !ok {
		return errors.New("session result is missing found")
	}
	if _, ok := fields["created"]; !ok {
		return errors.New("session result is missing created")
	}
	if wire.Created && !result.allowCreated {
		return errors.New("session result claims creation for a read operation")
	}
	if !wire.Found {
		if result.requireFound || wire.Session != nil || wire.Created {
			return errors.New("session result is incomplete")
		}
		result.found = false
		result.created = false
		result.session = nil
		return nil
	}
	if _, ok := fields["session"]; !ok || wire.Session == nil {
		return errors.New("session result is missing its session")
	}
	value, err := wire.Session.toDomain()
	if err != nil {
		return errors.New("session result cannot be reconstructed")
	}
	if result.expectedID != "" && value.ID() != result.expectedID {
		return errors.New("session result identity does not match the request")
	}
	if result.expectedLogicalDate != "" && value.LogicalDate() != result.expectedLogicalDate {
		return errors.New("canonical session date does not match the request")
	}
	if result.expectedChannelAddress != nil && value.ChannelAddress() != *result.expectedChannelAddress {
		return errors.New("canonical session address does not match the request")
	}
	result.found = true
	result.created = wire.Created
	result.session = value
	return nil
}

type sessionExistsResultDecoder struct{ exists bool }

func (result *sessionExistsResultDecoder) UnmarshalJSON(raw []byte) error {
	var wire sessionExistsResult
	if err := decodeSessionMessage(raw, &wire); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return errors.New("session exists result is not JSON")
	}
	if _, ok := fields["exists"]; !ok {
		return errors.New("session exists result is missing exists")
	}
	result.exists = wire.Exists
	return nil
}

type sessionReservationResultDecoder struct{ reserved bool }

func (result *sessionReservationResultDecoder) UnmarshalJSON(raw []byte) error {
	var wire sessionReservationResult
	if err := decodeSessionMessage(raw, &wire); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return errors.New("session reservation result is not JSON")
	}
	if _, ok := fields["reserved"]; !ok {
		return errors.New("session reservation result is missing reserved")
	}
	result.reserved = wire.Reserved
	return nil
}

type sessionCanonicalReservationResultDecoder struct {
	found                  bool
	session                *domainsession.Session
	creationIntent         *domainsession.CreationIntent
	expectedLogicalDate    string
	expectedChannelAddress *conversation.ChannelAddress
}

type sessionCanonicalOwnerResolutionDecoder struct {
	resolution             domainsession.CanonicalSessionOwnerResolution
	expectedOwnerID        string
	expectedSessionID      string
	expectedLogicalDate    string
	expectedChannelAddress *conversation.ChannelAddress
	allowCreated           bool
}

func (result *sessionCanonicalOwnerResolutionDecoder) UnmarshalJSON(raw []byte) error {
	var wire sessionCanonicalOwnerResolutionResult
	if err := decodeSessionMessage(raw, &wire); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return errors.New("canonical session owner result is not JSON")
	}
	if _, ok := fields["status"]; !ok {
		return errors.New("canonical session owner result is missing status")
	}
	status := wire.Status
	switch status {
	case domainsession.CanonicalSessionOwnerMatched, domainsession.CanonicalSessionOwnerCreated:
		if status == domainsession.CanonicalSessionOwnerCreated && !result.allowCreated {
			return errors.New("canonical session owner read claims creation")
		}
		if _, ok := fields["session"]; !ok || wire.Session == nil {
			return errors.New("canonical session owner result is missing its session")
		}
		if _, ok := fields["bound_owner_id"]; !ok {
			return errors.New("canonical session owner result is missing its bound owner")
		}
		if domainsession.ValidateOwnerID(wire.BoundOwnerID) != nil || wire.BoundOwnerID != result.expectedOwnerID {
			return errors.New("canonical session owner result has a mismatched owner")
		}
		value, err := wire.Session.toDomain()
		if err != nil || value.OwnerID() != result.expectedOwnerID || value.OwnerID() != wire.BoundOwnerID {
			return errors.New("canonical session owner result has an invalid owner-bound session")
		}
		if result.expectedSessionID != "" && value.ID() != result.expectedSessionID {
			return errors.New("canonical session owner result identity does not match the request")
		}
		if result.expectedLogicalDate != "" && value.LogicalDate() != result.expectedLogicalDate {
			return errors.New("canonical session owner result date does not match the request")
		}
		if result.expectedChannelAddress != nil && value.ChannelAddress() != *result.expectedChannelAddress {
			return errors.New("canonical session owner result address does not match the request")
		}
		result.resolution = domainsession.CanonicalSessionOwnerResolution{
			Session: value, BoundOwnerID: wire.BoundOwnerID, Status: status,
		}
	case domainsession.CanonicalSessionOwnerUnavailable, domainsession.CanonicalSessionOwnerConflict, domainsession.CanonicalSessionOwnerNotFound:
		if _, ok := fields["session"]; ok || wire.Session != nil {
			return errors.New("unresolved canonical session owner result contains a session")
		}
		if _, ok := fields["bound_owner_id"]; ok || wire.BoundOwnerID != "" {
			return errors.New("unresolved canonical session owner result contains an owner")
		}
		if status == domainsession.CanonicalSessionOwnerNotFound && result.expectedSessionID == "" {
			return errors.New("canonical natural-key owner lookup cannot be not_found")
		}
		result.resolution = domainsession.CanonicalSessionOwnerResolution{Status: status}
	default:
		return errors.New("canonical session owner result has an unknown status")
	}
	return nil
}

func (result *sessionCanonicalReservationResultDecoder) UnmarshalJSON(raw []byte) error {
	var wire sessionCanonicalReservationResult
	if err := decodeSessionMessage(raw, &wire); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return errors.New("session reservation lookup result is not JSON")
	}
	if _, ok := fields["found"]; !ok {
		return errors.New("session reservation lookup result is missing found")
	}
	if !wire.Found {
		if wire.Session != nil || wire.CreationIntent != nil {
			return errors.New("absent session reservation lookup contains a session or intent")
		}
		result.found = false
		result.session = nil
		result.creationIntent = nil
		return nil
	}
	if _, ok := fields["session"]; !ok || wire.Session == nil {
		return errors.New("session reservation lookup result is missing its session")
	}
	value, err := wire.Session.toDomain()
	if err != nil {
		return errors.New("session reservation lookup result cannot be reconstructed")
	}
	if result.expectedLogicalDate != "" && value.LogicalDate() != result.expectedLogicalDate {
		return errors.New("canonical session date does not match the reservation lookup")
	}
	if result.expectedChannelAddress != nil && value.ChannelAddress() != *result.expectedChannelAddress {
		return errors.New("canonical session address does not match the reservation lookup")
	}
	if wire.CreationIntent != nil {
		if wire.CreationIntent.Runtime != "v2" || wire.CreationIntent.OwnerID == "" || strings.TrimSpace(wire.CreationIntent.OwnerID) != wire.CreationIntent.OwnerID {
			return errors.New("session reservation lookup returned an invalid creation intent")
		}
	}
	result.found = true
	result.session = value
	result.creationIntent = wire.CreationIntent
	return nil
}

// RegisterSessionGroup registers a closed set of operations against the
// existing session owner. Owner errors are left unwrapped: without explicit
// rollback proof the protocol keeps mutating operation IDs outcome-unknown.
func RegisterSessionGroup(h *Handler, store SessionGroupOwner) error {
	if h == nil || store == nil {
		return errors.New("storagehost: session group needs a handler and an owner store")
	}
	if err := h.Register(GroupSession, "save", true, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload sessionSavePayload
		if err := decodeSessionMessage(raw, &payload); err != nil {
			return nil, sessionPayloadRejected("session save payload rejected")
		}
		value, err := payload.Session.toDomain()
		if err != nil {
			return nil, sessionPayloadRejected("session save payload rejected")
		}
		if err := store.Save(ctx, value); err != nil {
			return nil, uncertainSessionOwnerError("save")
		}
		return nil, nil
	}); err != nil {
		return fmt.Errorf("register session save: %w", err)
	}
	if err := h.Register(GroupSession, "load", false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload sessionIDPayload
		if err := decodeSessionMessage(raw, &payload); err != nil || validateSessionID(payload.ID) != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "session load payload rejected")
		}
		value, err := store.Load(ctx, payload.ID)
		if errors.Is(err, domainsession.ErrSessionNotFound) {
			return sessionLoadResult{Found: false}, nil
		}
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "session owner load failed")
		}
		if value == nil || value.ID() != payload.ID {
			return nil, NewError(ErrorCodeStoreUnavailable, "session owner returned a mismatched session")
		}
		dto, err := sessionRecordFromDomain(value)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "session owner returned an invalid session")
		}
		return sessionLoadResult{Found: true, Session: dto}, nil
	}); err != nil {
		return fmt.Errorf("register session load: %w", err)
	}
	if err := h.Register(GroupSession, "exists", false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload sessionIDPayload
		if err := decodeSessionMessage(raw, &payload); err != nil || validateSessionID(payload.ID) != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "session exists payload rejected")
		}
		exists, err := store.Exists(ctx, payload.ID)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "session owner exists check failed")
		}
		return sessionExistsResult{Exists: exists}, nil
	}); err != nil {
		return fmt.Errorf("register session exists: %w", err)
	}
	if err := h.Register(GroupSession, "delete", true, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload sessionIDPayload
		if err := decodeSessionMessage(raw, &payload); err != nil || validateSessionID(payload.ID) != nil {
			return nil, sessionPayloadRejected("session delete payload rejected")
		}
		if err := store.Delete(ctx, payload.ID); err != nil {
			return nil, uncertainSessionOwnerError("delete")
		}
		return nil, nil
	}); err != nil {
		return fmt.Errorf("register session delete: %w", err)
	}
	if err := h.Register(GroupSession, "load_canonical", false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload sessionCanonicalReadPayload
		if err := decodeSessionMessage(raw, &payload); err != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "canonical session read payload rejected")
		}
		address, err := payload.ChannelAddress.toDomain()
		if err != nil || domainsession.ValidateLogicalDate(payload.LogicalDate) != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "canonical session read payload rejected")
		}
		value, found, err := store.LoadCanonical(ctx, payload.LogicalDate, address)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "session owner canonical read failed")
		}
		if !found {
			if value != nil {
				return nil, NewError(ErrorCodeStoreUnavailable, "session owner returned a session for an absent canonical lookup")
			}
			return sessionLoadResult{Found: false}, nil
		}
		if value == nil || value.LogicalDate() != payload.LogicalDate || value.ChannelAddress() != address {
			return nil, NewError(ErrorCodeStoreUnavailable, "session owner returned a mismatched canonical session")
		}
		dto, err := sessionRecordFromDomain(value)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "session owner returned an invalid session")
		}
		return sessionLoadResult{Found: true, Session: dto}, nil
	}); err != nil {
		return fmt.Errorf("register canonical session read: %w", err)
	}
	if err := h.Register(GroupSession, "load_canonical_for_owner", false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload sessionCanonicalOwnerReadPayload
		if err := decodeSessionMessage(raw, &payload); err != nil || validateSessionID(payload.ID) != nil || domainsession.ValidateOwnerID(payload.OwnerID) != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "canonical session owner read payload rejected")
		}
		address, err := payload.ChannelAddress.toDomain()
		if err != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "canonical session owner read payload rejected")
		}
		resolution, err := store.LoadCanonicalForOwner(ctx, payload.ID, payload.OwnerID, address)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "session owner canonical owner read failed")
		}
		result, err := sessionOwnerResolutionFromDomain(resolution, payload.OwnerID, payload.ID, "", address, false)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "session owner returned an invalid canonical owner resolution")
		}
		return result, nil
	}); err != nil {
		return fmt.Errorf("register canonical session owner read: %w", err)
	}
	if err := h.Register(GroupSession, "load_canonical_reservation", false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload sessionCanonicalReadPayload
		if err := decodeSessionMessage(raw, &payload); err != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "canonical reservation read payload rejected")
		}
		address, err := payload.ChannelAddress.toDomain()
		if err != nil || domainsession.ValidateLogicalDate(payload.LogicalDate) != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "canonical reservation read payload rejected")
		}
		value, intent, found, err := store.LoadCanonicalWithCreationIntent(ctx, payload.LogicalDate, address)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "session owner canonical reservation read failed")
		}
		if !found {
			if value != nil || intent != nil {
				return nil, NewError(ErrorCodeStoreUnavailable, "session owner returned a reservation for an absent canonical lookup")
			}
			return sessionCanonicalReservationResult{Found: false}, nil
		}
		if value == nil || value.LogicalDate() != payload.LogicalDate || value.ChannelAddress() != address {
			return nil, NewError(ErrorCodeStoreUnavailable, "session owner returned a mismatched canonical reservation")
		}
		if intent != nil && (intent.Runtime != "v2" || intent.OwnerID == "" || strings.TrimSpace(intent.OwnerID) != intent.OwnerID) {
			return nil, NewError(ErrorCodeStoreUnavailable, "session owner returned an invalid canonical reservation intent")
		}
		dto, err := sessionRecordFromDomain(value)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "session owner returned an invalid canonical reservation session")
		}
		return sessionCanonicalReservationResult{Found: true, Session: dto, CreationIntent: intent}, nil
	}); err != nil {
		return fmt.Errorf("register canonical reservation read: %w", err)
	}
	if err := h.Register(GroupSession, "is_v2_reserved", false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload sessionReservationIDPayload
		if err := decodeSessionMessage(raw, &payload); err != nil || validateSessionID(payload.ID) != nil || payload.OwnerID == "" || strings.TrimSpace(payload.OwnerID) != payload.OwnerID {
			return nil, NewError(ErrorCodeSchemaRejected, "session reservation lookup payload rejected")
		}
		reserved, err := store.IsCanonicalV2Reserved(ctx, payload.ID, payload.OwnerID)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "session owner reservation lookup failed")
		}
		return sessionReservationResult{Reserved: reserved}, nil
	}); err != nil {
		return fmt.Errorf("register session reservation lookup: %w", err)
	}
	if err := h.Register(GroupSession, "reserve_canonical_v2", true, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload sessionCanonicalReservePayload
		if err := decodeSessionMessage(raw, &payload); err != nil {
			return nil, sessionPayloadRejected("canonical v2 reservation payload rejected")
		}
		address, err := payload.ChannelAddress.toDomain()
		if err != nil || domainsession.ValidateLogicalDate(payload.LogicalDate) != nil || payload.CreatedAt.IsZero() || payload.OwnerID == "" || strings.TrimSpace(payload.OwnerID) != payload.OwnerID {
			return nil, sessionPayloadRejected("canonical v2 reservation payload rejected")
		}
		value, created, err := store.ReserveCanonicalV2(ctx, payload.LogicalDate, address, payload.OwnerID, payload.CreatedAt)
		if err != nil {
			return nil, uncertainSessionOwnerError("canonical v2 reservation")
		}
		if value == nil || value.LogicalDate() != payload.LogicalDate || value.ChannelAddress() != address {
			return nil, NewError(ErrorCodeStoreUnavailable, "session owner returned a mismatched canonical v2 reservation")
		}
		dto, err := sessionRecordFromDomain(value)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "session owner returned an invalid canonical v2 reservation")
		}
		return sessionLoadResult{Found: true, Created: created, Session: dto}, nil
	}); err != nil {
		return fmt.Errorf("register canonical v2 reservation: %w", err)
	}
	if err := h.Register(GroupSession, "load_or_create_canonical", true, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload sessionCanonicalLookupPayload
		if err := decodeSessionMessage(raw, &payload); err != nil {
			return nil, sessionPayloadRejected("canonical session lookup payload rejected")
		}
		address, err := payload.ChannelAddress.toDomain()
		if err != nil || domainsession.ValidateLogicalDate(payload.LogicalDate) != nil || payload.CreatedAt.IsZero() {
			return nil, sessionPayloadRejected("canonical session lookup payload rejected")
		}
		value, created, err := store.LoadOrCreateCanonical(ctx, payload.LogicalDate, address, payload.CreatedAt)
		if err != nil {
			return nil, uncertainSessionOwnerError("canonical lookup")
		}
		if value == nil || value.LogicalDate() != payload.LogicalDate || value.ChannelAddress() != address {
			return nil, NewError(ErrorCodeStoreUnavailable, "session owner returned a mismatched canonical session")
		}
		dto, err := sessionRecordFromDomain(value)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "session owner returned an invalid session")
		}
		return sessionLoadResult{Found: true, Created: created, Session: dto}, nil
	}); err != nil {
		return fmt.Errorf("register canonical session lookup: %w", err)
	}
	if err := h.Register(GroupSession, "load_or_create_canonical_for_owner", true, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload sessionCanonicalOwnerLookupPayload
		if err := decodeSessionMessage(raw, &payload); err != nil {
			return nil, sessionPayloadRejected("canonical session owner lookup payload rejected")
		}
		address, err := payload.ChannelAddress.toDomain()
		if err != nil || domainsession.ValidateLogicalDate(payload.LogicalDate) != nil || domainsession.ValidateOwnerID(payload.OwnerID) != nil || payload.CreatedAt.IsZero() {
			return nil, sessionPayloadRejected("canonical session owner lookup payload rejected")
		}
		resolution, err := store.LoadOrCreateCanonicalForOwner(ctx, payload.LogicalDate, address, payload.OwnerID, payload.CreatedAt)
		if err != nil {
			return nil, uncertainSessionOwnerError("canonical owner lookup")
		}
		result, err := sessionOwnerResolutionFromDomain(resolution, payload.OwnerID, "", payload.LogicalDate, address, true)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "session owner returned an invalid canonical owner resolution")
		}
		return result, nil
	}); err != nil {
		return fmt.Errorf("register canonical session owner lookup: %w", err)
	}
	return nil
}

func sessionOwnerResolutionFromDomain(resolution domainsession.CanonicalSessionOwnerResolution, requestedOwnerID, expectedSessionID, expectedLogicalDate string, expectedAddress conversation.ChannelAddress, allowCreated bool) (sessionCanonicalOwnerResolutionResult, error) {
	result := sessionCanonicalOwnerResolutionResult{Status: resolution.Status}
	switch resolution.Status {
	case domainsession.CanonicalSessionOwnerMatched:
	case domainsession.CanonicalSessionOwnerCreated:
		if !allowCreated {
			return sessionCanonicalOwnerResolutionResult{}, errors.New("read-only canonical owner lookup returned created")
		}
	case domainsession.CanonicalSessionOwnerUnavailable, domainsession.CanonicalSessionOwnerConflict:
	case domainsession.CanonicalSessionOwnerNotFound:
		if expectedSessionID == "" {
			return sessionCanonicalOwnerResolutionResult{}, errors.New("natural-key canonical owner lookup returned not_found")
		}
	default:
		return sessionCanonicalOwnerResolutionResult{}, errors.New("canonical owner lookup returned an unknown status")
	}
	if resolution.Status != domainsession.CanonicalSessionOwnerMatched && resolution.Status != domainsession.CanonicalSessionOwnerCreated {
		if resolution.Session != nil || resolution.BoundOwnerID != "" {
			return sessionCanonicalOwnerResolutionResult{}, errors.New("unresolved canonical owner result contains owner-bound data")
		}
		return result, nil
	}
	value := resolution.Session
	if value == nil || domainsession.ValidateOwnerID(resolution.BoundOwnerID) != nil || resolution.BoundOwnerID != requestedOwnerID || value.OwnerID() != requestedOwnerID || value.ChannelAddress() != expectedAddress {
		return sessionCanonicalOwnerResolutionResult{}, errors.New("canonical owner result identity or provenance does not match the request")
	}
	if expectedSessionID != "" && value.ID() != expectedSessionID || expectedLogicalDate != "" && value.LogicalDate() != expectedLogicalDate {
		return sessionCanonicalOwnerResolutionResult{}, errors.New("canonical owner result does not match the requested identity")
	}
	dto, err := sessionRecordFromDomain(value)
	if err != nil {
		return sessionCanonicalOwnerResolutionResult{}, err
	}
	result.BoundOwnerID = resolution.BoundOwnerID
	result.Session = dto
	return result, nil
}

// SessionRepositoryClient implements the existing session repository and
// canonical lookup contracts over the storage host protocol.
type SessionRepositoryClient struct {
	client *Client

	reservationMu  sync.Mutex
	reservationOps map[string]*Operation
}

func NewSessionRepositoryClient(c *Client) *SessionRepositoryClient {
	return &SessionRepositoryClient{client: c, reservationOps: make(map[string]*Operation)}
}

var (
	_ domainsession.SessionRepository = (*SessionRepositoryClient)(nil)
	_ interface {
		LoadCanonical(context.Context, string, conversation.ChannelAddress) (*domainsession.Session, bool, error)
		LoadCanonicalForOwner(context.Context, string, string, conversation.ChannelAddress) (domainsession.CanonicalSessionOwnerResolution, error)
		LoadCanonicalWithCreationIntent(context.Context, string, conversation.ChannelAddress) (*domainsession.Session, *domainsession.CreationIntent, bool, error)
		LoadOrCreateCanonical(context.Context, string, conversation.ChannelAddress, time.Time) (*domainsession.Session, bool, error)
		LoadOrCreateCanonicalForOwner(context.Context, string, conversation.ChannelAddress, string, time.Time) (domainsession.CanonicalSessionOwnerResolution, error)
		ReserveCanonicalV2(context.Context, string, conversation.ChannelAddress, string, time.Time) (*domainsession.Session, bool, error)
		IsCanonicalV2Reserved(context.Context, string, string) (bool, error)
	} = (*SessionRepositoryClient)(nil)
)

// Save persists one canonical session through the storage owner.
func (s *SessionRepositoryClient) Save(ctx context.Context, value *domainsession.Session) error {
	dto, err := sessionRecordFromDomain(value)
	if err != nil {
		return NewError(ErrorCodeSchemaRejected, "session rejected")
	}
	return s.call(ctx, "save", sessionSavePayload{Session: dto}, nil)
}

// Load returns the canonical session identified by its opaque SessionID.
func (s *SessionRepositoryClient) Load(ctx context.Context, id string) (*domainsession.Session, error) {
	if err := validateSessionID(id); err != nil {
		return nil, NewError(ErrorCodeSchemaRejected, "session id rejected")
	}
	result := sessionLoadResultDecoder{expectedID: id}
	if err := s.call(ctx, "load", sessionIDPayload{ID: id}, &result); err != nil {
		return nil, err
	}
	if !result.found {
		return nil, domainsession.ErrSessionNotFound
	}
	return result.session, nil
}

// Exists reports whether a canonical session is present at the owner.
func (s *SessionRepositoryClient) Exists(ctx context.Context, id string) (bool, error) {
	if err := validateSessionID(id); err != nil {
		return false, NewError(ErrorCodeSchemaRejected, "session id rejected")
	}
	var result sessionExistsResultDecoder
	if err := s.call(ctx, "exists", sessionIDPayload{ID: id}, &result); err != nil {
		return false, err
	}
	return result.exists, nil
}

// Delete removes one canonical session. The owner defines missing deletion as
// an idempotent success, matching the existing repository contract.
func (s *SessionRepositoryClient) Delete(ctx context.Context, id string) error {
	if err := validateSessionID(id); err != nil {
		return NewError(ErrorCodeSchemaRejected, "session id rejected")
	}
	return s.call(ctx, "delete", sessionIDPayload{ID: id}, nil)
}

// LoadCanonical resolves the owner's existing natural lookup without causing
// the owner to create or mutate a session.
func (s *SessionRepositoryClient) LoadCanonical(ctx context.Context, logicalDate string, address conversation.ChannelAddress) (*domainsession.Session, bool, error) {
	if domainsession.ValidateLogicalDate(logicalDate) != nil || address.Validate() != nil {
		return nil, false, NewError(ErrorCodeSchemaRejected, "canonical session read rejected")
	}
	addressDTO := sessionAddressFromDomain(address)
	request := sessionCanonicalReadPayload{LogicalDate: logicalDate, ChannelAddress: &addressDTO}
	result := sessionLoadResultDecoder{
		expectedLogicalDate:    logicalDate,
		expectedChannelAddress: &address,
	}
	if err := s.call(ctx, "load_canonical", request, &result); err != nil {
		return nil, false, err
	}
	return result.session, result.found, nil
}

// LoadCanonicalForOwner resolves an opaque SessionID only when the canonical
// owner and ChannelAddress recorded by the owner match the request.
func (s *SessionRepositoryClient) LoadCanonicalForOwner(ctx context.Context, id, ownerID string, address conversation.ChannelAddress) (domainsession.CanonicalSessionOwnerResolution, error) {
	if validateSessionID(id) != nil || domainsession.ValidateOwnerID(ownerID) != nil || address.Validate() != nil {
		return domainsession.CanonicalSessionOwnerResolution{}, NewError(ErrorCodeSchemaRejected, "canonical session owner read rejected")
	}
	addressDTO := sessionAddressFromDomain(address)
	request := sessionCanonicalOwnerReadPayload{ID: id, OwnerID: ownerID, ChannelAddress: &addressDTO}
	result := sessionCanonicalOwnerResolutionDecoder{
		expectedOwnerID: ownerID, expectedSessionID: id, expectedChannelAddress: &address,
	}
	if err := s.call(ctx, "load_canonical_for_owner", request, &result); err != nil {
		return domainsession.CanonicalSessionOwnerResolution{}, err
	}
	return result.resolution, nil
}

// LoadCanonicalWithCreationIntent reads the natural-key session and immutable
// reservation metadata from the owner without creating session state.
func (s *SessionRepositoryClient) LoadCanonicalWithCreationIntent(ctx context.Context, logicalDate string, address conversation.ChannelAddress) (*domainsession.Session, *domainsession.CreationIntent, bool, error) {
	if domainsession.ValidateLogicalDate(logicalDate) != nil || address.Validate() != nil {
		return nil, nil, false, NewError(ErrorCodeSchemaRejected, "canonical reservation read rejected")
	}
	addressDTO := sessionAddressFromDomain(address)
	request := sessionCanonicalReadPayload{LogicalDate: logicalDate, ChannelAddress: &addressDTO}
	result := sessionCanonicalReservationResultDecoder{
		expectedLogicalDate: logicalDate, expectedChannelAddress: &address,
	}
	if err := s.call(ctx, "load_canonical_reservation", request, &result); err != nil {
		return nil, nil, false, err
	}
	return result.session, result.creationIntent, result.found, nil
}

// LoadOrCreateCanonical resolves the owner's existing natural lookup and
// returns its opaque, storage-assigned SessionID plus atomic creation provenance.
func (s *SessionRepositoryClient) LoadOrCreateCanonical(ctx context.Context, logicalDate string, address conversation.ChannelAddress, createdAt time.Time) (*domainsession.Session, bool, error) {
	if domainsession.ValidateLogicalDate(logicalDate) != nil || address.Validate() != nil || createdAt.IsZero() {
		return nil, false, NewError(ErrorCodeSchemaRejected, "canonical session lookup rejected")
	}
	addressDTO := sessionAddressFromDomain(address)
	request := sessionCanonicalLookupPayload{LogicalDate: logicalDate, ChannelAddress: &addressDTO, CreatedAt: createdAt}
	result := sessionLoadResultDecoder{
		requireFound:           true,
		allowCreated:           true,
		expectedLogicalDate:    logicalDate,
		expectedChannelAddress: &address,
	}
	if err := s.call(ctx, "load_or_create_canonical", request, &result); err != nil {
		return nil, false, err
	}
	return result.session, result.created, nil
}

// LoadOrCreateCanonicalForOwner resolves a canonical natural key using an
// explicit authenticated owner binding. Mutating retries retain the same
// operation ID through Client.Call until the recorded result is confirmed.
func (s *SessionRepositoryClient) LoadOrCreateCanonicalForOwner(ctx context.Context, logicalDate string, address conversation.ChannelAddress, ownerID string, createdAt time.Time) (domainsession.CanonicalSessionOwnerResolution, error) {
	if domainsession.ValidateLogicalDate(logicalDate) != nil || address.Validate() != nil || domainsession.ValidateOwnerID(ownerID) != nil || createdAt.IsZero() {
		return domainsession.CanonicalSessionOwnerResolution{}, NewError(ErrorCodeSchemaRejected, "canonical session owner lookup rejected")
	}
	addressDTO := sessionAddressFromDomain(address)
	request := sessionCanonicalOwnerLookupPayload{LogicalDate: logicalDate, ChannelAddress: &addressDTO, OwnerID: ownerID, CreatedAt: createdAt}
	result := sessionCanonicalOwnerResolutionDecoder{
		expectedOwnerID: ownerID, expectedLogicalDate: logicalDate, expectedChannelAddress: &address, allowCreated: true,
	}
	if err := s.call(ctx, "load_or_create_canonical_for_owner", request, &result); err != nil {
		return domainsession.CanonicalSessionOwnerResolution{}, err
	}
	return result.resolution, nil
}

// ReserveCanonicalV2 records a JSON-owner creation intent before EventStore
// acceptance. If the mutation's result is uncertain, it first queries that
// operation's own op_id, then trusts only an exact read-only owner reservation
// for the same natural key and owner. An absent or mismatched owner result is
// never treated as proof that the mutation did not execute.
func (s *SessionRepositoryClient) ReserveCanonicalV2(ctx context.Context, logicalDate string, address conversation.ChannelAddress, ownerID string, createdAt time.Time) (*domainsession.Session, bool, error) {
	if s == nil || s.client == nil || domainsession.ValidateLogicalDate(logicalDate) != nil || address.Validate() != nil || ownerID == "" || strings.TrimSpace(ownerID) != ownerID || createdAt.IsZero() {
		return nil, false, NewError(ErrorCodeSchemaRejected, "canonical v2 reservation rejected")
	}
	addressDTO := sessionAddressFromDomain(address)
	request := sessionCanonicalReservePayload{
		LogicalDate: logicalDate, ChannelAddress: &addressDTO, OwnerID: ownerID, CreatedAt: createdAt,
	}
	body, err := marshalSessionMessage(request)
	if err != nil {
		return nil, false, err
	}

	s.reservationMu.Lock()
	defer s.reservationMu.Unlock()
	key := string(body)
	op := s.reservationOps[key]
	if op == nil {
		op = s.client.NewOperation(GroupSession, "reserve_canonical_v2", request)
		s.reservationOps[key] = op
	}
	result := sessionLoadResultDecoder{
		requireFound: true, allowCreated: true,
		expectedLogicalDate: logicalDate, expectedChannelAddress: &address,
	}
	if err := s.client.Do(ctx, op, &result); err == nil {
		delete(s.reservationOps, key)
		return result.session, result.created, nil
	} else {
		op.mu.Lock()
		op.pending = true
		op.mu.Unlock()
		// A failed mutating exchange is not a retry authorization. Resolve this
		// exact op_id first; if its receipt remains uncertain, consult the
		// read-only canonical owner metadata below.
		if opErr := s.client.resolveOperation(ctx, op, &result); opErr == nil {
			delete(s.reservationOps, key)
			return result.session, result.created, nil
		}
		value, intent, found, readErr := s.LoadCanonicalWithCreationIntent(ctx, logicalDate, address)
		if readErr == nil && found && value != nil && intent != nil && intent.Runtime == "v2" && intent.OwnerID == ownerID {
			return value, false, nil
		}
		return nil, false, NewError(ErrorCodeOutcomeUnknown, "canonical v2 reservation outcome remains uncertain")
	}
}

// IsCanonicalV2Reserved performs the read-only owner check used by v1/v2
// admission and EventStore acceptance authorization.
func (s *SessionRepositoryClient) IsCanonicalV2Reserved(ctx context.Context, id, ownerID string) (bool, error) {
	if validateSessionID(id) != nil || ownerID == "" || strings.TrimSpace(ownerID) != ownerID {
		return false, NewError(ErrorCodeSchemaRejected, "session reservation lookup rejected")
	}
	var result sessionReservationResultDecoder
	if err := s.call(ctx, "is_v2_reserved", sessionReservationIDPayload{ID: id, OwnerID: ownerID}, &result); err != nil {
		return false, err
	}
	return result.reserved, nil
}

func (s *SessionRepositoryClient) call(ctx context.Context, op string, payload any, out any) error {
	body, err := marshalSessionMessage(payload)
	if err != nil {
		return err
	}
	return s.client.Call(ctx, GroupSession, op, body, out)
}

func marshalSessionMessage(value any) (json.RawMessage, error) {
	body, err := json.Marshal(value)
	if err != nil || len(body) == 0 || len(body) > maxSessionPayloadBytes {
		return nil, NewError(ErrorCodeSchemaRejected, "session payload size or encoding rejected")
	}
	return body, nil
}

func decodeSessionMessage(raw json.RawMessage, value any) error {
	if len(raw) == 0 || len(raw) > maxSessionPayloadBytes || !json.Valid(raw) {
		return errors.New("session payload size or JSON rejected")
	}
	if err := rejectSessionJSONFieldAliases(raw, reflect.TypeOf(value)); err != nil {
		return errors.New("session payload contains duplicate or aliased fields")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return errors.New("session payload schema rejected")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("session payload trailing data rejected")
	}
	return nil
}

// rejectSessionJSONFieldAliases mirrors encoding/json's case-insensitive
// matching for known struct fields while requiring the exact canonical JSON
// names and rejecting duplicate keys. It follows typed struct fields but does
// not inspect map values such as Session memory, whose keys are user data.
func rejectSessionJSONFieldAliases(raw []byte, target reflect.Type) error {
	for target != nil && target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	if target == nil {
		return nil
	}
	switch target.Kind() {
	case reflect.Struct:
		return rejectSessionJSONObjectFieldAliases(raw, target)
	case reflect.Slice, reflect.Array:
		if target.Elem().Kind() == reflect.Uint8 {
			return nil
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		start, ok := token.(json.Delim)
		if !ok || start != '[' {
			return nil
		}
		for decoder.More() {
			var item json.RawMessage
			if err := decoder.Decode(&item); err != nil {
				return err
			}
			if err := rejectSessionJSONFieldAliases(item, target.Elem()); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return nil
	}
}

func rejectSessionJSONObjectFieldAliases(raw []byte, target reflect.Type) error {
	fieldsByFoldedName := make(map[string]string, target.NumField())
	fieldTypes := make(map[string]reflect.Type, target.NumField())
	for index := 0; index < target.NumField(); index++ {
		field := target.Field(index)
		if field.PkgPath != "" {
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		folded := foldSessionJSONFieldName(name)
		fieldsByFoldedName[folded] = name
		fieldTypes[name] = field.Type
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	start, ok := token.(json.Delim)
	if !ok || start != '{' {
		return nil
	}
	seen := make(map[string]struct{}, len(fieldsByFoldedName))
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok {
			return errors.New("session object key is not a string")
		}
		var fieldRaw json.RawMessage
		if err := decoder.Decode(&fieldRaw); err != nil {
			return err
		}
		canonicalName, known := fieldsByFoldedName[foldSessionJSONFieldName(key)]
		if !known {
			continue
		}
		if key != canonicalName {
			return errors.New("session JSON field uses a noncanonical alias")
		}
		if _, duplicate := seen[canonicalName]; duplicate {
			return errors.New("session JSON field is duplicated")
		}
		seen[canonicalName] = struct{}{}
		if err := rejectSessionJSONFieldAliases(fieldRaw, fieldTypes[canonicalName]); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}

func foldSessionJSONFieldName(value string) string {
	bytes := []byte(value)
	for index, char := range bytes {
		if char >= 'A' && char <= 'Z' {
			bytes[index] = char + ('a' - 'A')
		}
	}
	return string(bytes)
}

func sessionPayloadRejected(message string) error {
	return ownerRolledBack(NewError(ErrorCodeSchemaRejected, message))
}

// uncertainSessionOwnerError intentionally does not unwrap the owner error.
// Only validation completed before owner execution can prove that retrying a
// mutating operation is safe; owner errors may follow a committed write.
func uncertainSessionOwnerError(operation string) error {
	return NewError(ErrorCodeStoreUnavailable, "session owner "+operation+" failed")
}

func validateSessionID(id string) error {
	return modulecore.SessionID(id).Validate()
}

func sessionAddressFromDomain(value conversation.ChannelAddress) sessionAddressDTO {
	return sessionAddressDTO{
		ChannelType:            value.ChannelType(),
		ExternalConversationID: value.ExternalConversationID(),
	}
}

func (dto *sessionAddressDTO) toDomain() (conversation.ChannelAddress, error) {
	if dto == nil {
		return conversation.ChannelAddress{}, errors.New("channel_address is required")
	}
	address, err := conversation.NewChannelAddress(dto.ChannelType, dto.ExternalConversationID)
	if err != nil || dto.ChannelType != address.ChannelType() || dto.ExternalConversationID != address.ExternalConversationID() {
		return conversation.ChannelAddress{}, errors.New("channel_address is not canonical")
	}
	return address, nil
}

func sessionRecordFromDomain(value *domainsession.Session) (*sessionRecordDTO, error) {
	if err := validateStorageHostSession(value); err != nil {
		return nil, err
	}
	if value.HistoryCount() > maxSessionHistoryEntries {
		return nil, errors.New("session history limit exceeded")
	}
	memory := value.GetAllMemory()
	if len(memory) > maxSessionMemoryEntries {
		return nil, errors.New("session memory limit exceeded")
	}
	address := sessionAddressFromDomain(value.ChannelAddress())
	dto := &sessionRecordDTO{
		ID:             value.ID(),
		OwnerID:        value.OwnerID(),
		LogicalDate:    value.LogicalDate(),
		ChannelAddress: &address,
		History:        make([]sessionTurnDTO, 0, value.HistoryCount()),
		Memory:         make(map[string]json.RawMessage),
		CreatedAt:      value.CreatedAt(),
		UpdatedAt:      value.UpdatedAt(),
	}
	for _, item := range value.GetHistory() {
		itemAddress := sessionAddressFromDomain(item.ChannelAddress())
		attachments := item.Attachments()
		if len(attachments) > maxSessionAttachmentsTurn {
			return nil, errors.New("session attachment limit exceeded")
		}
		attachmentDTOs := make([]sessionAttachmentDTO, 0, len(attachments))
		for _, attached := range attachments {
			attachmentDTOs = append(attachmentDTOs, sessionAttachmentDTO{
				ID:                  attached.ID,
				Kind:                attached.Kind,
				Filename:            attached.Filename,
				ContentType:         attached.ContentType,
				SizeBytes:           attached.SizeBytes,
				Path:                attached.Path,
				SHA256:              attached.SHA256,
				ExtractedText:       attached.ExtractedText,
				ExtractionError:     attached.ExtractionError,
				ExtractionTruncated: attached.ExtractionTruncated,
				SecurityWarnings:    append([]string(nil), attached.SecurityWarnings...),
			})
		}
		dto.History = append(dto.History, sessionTurnDTO{
			RootTaskID:      string(item.RootTaskID()),
			TurnID:          string(item.TurnID()),
			TraceID:         string(item.TraceID()),
			UserMessageID:   string(item.UserMessageID()),
			AgentMessageID:  string(item.AgentMessageID()),
			MessageText:     item.MessageText(),
			ChannelAddress:  &itemAddress,
			Attachments:     attachmentDTOs,
			ViewerRecipient: item.ViewerRecipient(),
			ForcedRoute:     string(item.ForcedRoute()),
			Route:           string(item.Route()),
		})
	}
	for key, item := range memory {
		raw, err := json.Marshal(item)
		if err != nil {
			return nil, fmt.Errorf("memory value %q is not JSON serializable", key)
		}
		dto.Memory[key] = raw
	}
	if _, err := marshalSessionMessage(dto); err != nil {
		return nil, err
	}
	return dto, nil
}

func (dto *sessionRecordDTO) toDomain() (*domainsession.Session, error) {
	if dto == nil {
		return nil, errors.New("session is required")
	}
	if validateSessionID(dto.ID) != nil || domainsession.ValidateLogicalDate(dto.LogicalDate) != nil {
		return nil, errors.New("session identity is not canonical")
	}
	address, err := dto.ChannelAddress.toDomain()
	if err != nil {
		return nil, err
	}
	if len(dto.History) > maxSessionHistoryEntries || len(dto.Memory) > maxSessionMemoryEntries {
		return nil, errors.New("session record limit exceeded")
	}
	history := make([]conversation.TurnInput, 0, len(dto.History))
	for index, item := range dto.History {
		itemAddress, err := item.ChannelAddress.toDomain()
		if err != nil {
			return nil, fmt.Errorf("history[%d] channel address rejected", index)
		}
		turn, err := conversation.ReconstructTurnInput(
			modulecore.TaskID(item.RootTaskID),
			modulecore.TurnID(item.TurnID),
			modulecore.TraceID(item.TraceID),
			modulecore.MessageID(item.UserMessageID),
			modulecore.MessageID(item.AgentMessageID),
			item.MessageText,
			itemAddress,
		)
		if err != nil || itemAddress != address {
			return nil, fmt.Errorf("history[%d] turn input rejected", index)
		}
		if len(item.Attachments) > maxSessionAttachmentsTurn {
			return nil, fmt.Errorf("history[%d] attachment limit exceeded", index)
		}
		attachments := make([]attachment.Attachment, 0, len(item.Attachments))
		for _, attached := range item.Attachments {
			attachments = append(attachments, attachment.Attachment{
				ID:                  attached.ID,
				Kind:                attached.Kind,
				Filename:            attached.Filename,
				ContentType:         attached.ContentType,
				SizeBytes:           attached.SizeBytes,
				Path:                attached.Path,
				SHA256:              attached.SHA256,
				ExtractedText:       attached.ExtractedText,
				ExtractionError:     attached.ExtractionError,
				ExtractionTruncated: attached.ExtractionTruncated,
				SecurityWarnings:    append([]string(nil), attached.SecurityWarnings...),
			})
		}
		turn = turn.WithSessionID(dto.ID).WithAttachments(attachments)
		if item.ViewerRecipient != "" {
			turn = turn.WithViewerRecipient(item.ViewerRecipient)
		}
		if item.ForcedRoute != "" {
			turn = turn.WithForcedRoute(routing.Route(item.ForcedRoute))
		}
		if item.Route != "" {
			turn = turn.WithRoute(routing.Route(item.Route))
		}
		history = append(history, turn)
	}
	memory := make(map[string]interface{}, len(dto.Memory))
	for key, raw := range dto.Memory {
		if len(raw) == 0 || !json.Valid(raw) {
			return nil, errors.New("session memory value rejected")
		}
		var value interface{}
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, errors.New("session memory value rejected")
		}
		memory[key] = value
	}
	var value *domainsession.Session
	if dto.OwnerID == "" {
		value, err = domainsession.ReconstructCanonicalSession(
			modulecore.SessionID(dto.ID), dto.LogicalDate, address, history, memory, dto.CreatedAt, dto.UpdatedAt,
		)
	} else {
		if err := domainsession.ValidateOwnerID(dto.OwnerID); err != nil {
			return nil, errors.New("session owner binding is not canonical")
		}
		value, err = domainsession.ReconstructCanonicalSessionWithOwner(
			modulecore.SessionID(dto.ID), dto.OwnerID, dto.LogicalDate, address, history, memory, dto.CreatedAt, dto.UpdatedAt,
		)
	}
	if err != nil {
		return nil, errors.New("session reconstruction rejected")
	}
	if err := validateStorageHostSession(value); err != nil {
		return nil, err
	}
	return value, nil
}

func validateStorageHostSession(value *domainsession.Session) error {
	if value == nil || validateSessionID(value.ID()) != nil || domainsession.ValidateLogicalDate(value.LogicalDate()) != nil || value.ChannelAddress().Validate() != nil || value.CreatedAt().IsZero() || value.UpdatedAt().Before(value.CreatedAt()) {
		return errors.New("session identity or timestamps rejected")
	}
	if value.OwnerID() != "" && domainsession.ValidateOwnerID(value.OwnerID()) != nil {
		return errors.New("session owner binding rejected")
	}
	if value.HistoryCount() > maxSessionHistoryEntries {
		return errors.New("session history limit exceeded")
	}
	for index, item := range value.GetHistory() {
		if err := item.Validate(); err != nil || item.SessionID() != value.ID() || item.ChannelAddress() != value.ChannelAddress() {
			return fmt.Errorf("session history[%d] rejected", index)
		}
		if len(item.Attachments()) > maxSessionAttachmentsTurn {
			return fmt.Errorf("session history[%d] attachment limit exceeded", index)
		}
	}
	if len(value.GetAllMemory()) > maxSessionMemoryEntries {
		return errors.New("session memory limit exceeded")
	}
	return nil
}
