package storagehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	LoadOrCreateCanonical(context.Context, string, conversation.ChannelAddress, time.Time) (*domainsession.Session, error)
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

type sessionLoadResult struct {
	Found   bool              `json:"found"`
	Session *sessionRecordDTO `json:"session,omitempty"`
}

type sessionExistsResult struct {
	Exists bool `json:"exists"`
}

// sessionLoadResultDecoder validates and reconstructs a typed domain result
// inside Client.Call's decode boundary. For mutating canonical lookup, any
// failure here remains outcome-unknown with the same op_id.
type sessionLoadResultDecoder struct {
	found                  bool
	session                *domainsession.Session
	requireFound           bool
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
	if !wire.Found {
		if result.requireFound || wire.Session != nil {
			return errors.New("session result is incomplete")
		}
		result.found = false
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
	if err := h.Register(GroupSession, "load_or_create_canonical", true, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload sessionCanonicalLookupPayload
		if err := decodeSessionMessage(raw, &payload); err != nil {
			return nil, sessionPayloadRejected("canonical session lookup payload rejected")
		}
		address, err := payload.ChannelAddress.toDomain()
		if err != nil || domainsession.ValidateLogicalDate(payload.LogicalDate) != nil || payload.CreatedAt.IsZero() {
			return nil, sessionPayloadRejected("canonical session lookup payload rejected")
		}
		value, err := store.LoadOrCreateCanonical(ctx, payload.LogicalDate, address, payload.CreatedAt)
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
		return sessionLoadResult{Found: true, Session: dto}, nil
	}); err != nil {
		return fmt.Errorf("register canonical session lookup: %w", err)
	}
	return nil
}

// SessionRepositoryClient implements the existing session repository and
// canonical lookup contracts over the storage host protocol.
type SessionRepositoryClient struct{ client *Client }

func NewSessionRepositoryClient(c *Client) *SessionRepositoryClient {
	return &SessionRepositoryClient{client: c}
}

var (
	_ domainsession.SessionRepository = (*SessionRepositoryClient)(nil)
	_ interface {
		LoadOrCreateCanonical(context.Context, string, conversation.ChannelAddress, time.Time) (*domainsession.Session, error)
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

// LoadOrCreateCanonical resolves the owner's existing natural lookup and
// returns its opaque, storage-assigned SessionID.
func (s *SessionRepositoryClient) LoadOrCreateCanonical(ctx context.Context, logicalDate string, address conversation.ChannelAddress, createdAt time.Time) (*domainsession.Session, error) {
	if domainsession.ValidateLogicalDate(logicalDate) != nil || address.Validate() != nil || createdAt.IsZero() {
		return nil, NewError(ErrorCodeSchemaRejected, "canonical session lookup rejected")
	}
	addressDTO := sessionAddressFromDomain(address)
	request := sessionCanonicalLookupPayload{LogicalDate: logicalDate, ChannelAddress: &addressDTO, CreatedAt: createdAt}
	result := sessionLoadResultDecoder{
		requireFound:           true,
		expectedLogicalDate:    logicalDate,
		expectedChannelAddress: &address,
	}
	if err := s.call(ctx, "load_or_create_canonical", request, &result); err != nil {
		return nil, err
	}
	return result.session, nil
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
	value, err := domainsession.ReconstructCanonicalSession(
		modulecore.SessionID(dto.ID),
		dto.LogicalDate,
		address,
		history,
		memory,
		dto.CreatedAt,
		dto.UpdatedAt,
	)
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
