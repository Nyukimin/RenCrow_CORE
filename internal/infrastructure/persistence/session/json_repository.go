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
	r.mu.Lock()
	defer r.mu.Unlock()

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
	r.mu.Lock()
	defer r.mu.Unlock()

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
	r.mu.Lock()
	defer r.mu.Unlock()

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
	r.mu.Lock()
	defer r.mu.Unlock()
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

func (r *JSONSessionRepository) findCanonicalSessionID(ctx context.Context, logicalDate string, address conversation.ChannelAddress) (string, error) {
	entries, err := os.ReadDir(r.baseDir)
	if err != nil {
		return "", fmt.Errorf("read session directory: %w", err)
	}
	matchedID := ""
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, readErr := os.ReadFile(filepath.Join(r.baseDir, entry.Name()))
		if readErr != nil {
			return "", fmt.Errorf("read session identity: %w", readErr)
		}
		if err := rejectLegacySessionIdentityFields(raw); err != nil {
			return "", err
		}
		var probe sessionIdentityProbe
		if err := json.Unmarshal(raw, &probe); err != nil {
			return "", fmt.Errorf("decode session identity: %w", err)
		}
		if probe.ID == "" && probe.LogicalDate == "" && probe.ChannelAddress == nil {
			continue
		}
		if probe.ID == "" || probe.LogicalDate == "" || probe.ChannelAddress == nil {
			return "", fmt.Errorf("canonical session identity is incomplete")
		}
		candidateAddress, err := channelAddressFromDTO(probe.ChannelAddress)
		if err != nil {
			return "", fmt.Errorf("canonical ChannelAddress is invalid: %w", err)
		}
		if err := session.ValidateLogicalDate(probe.LogicalDate); err != nil {
			return "", fmt.Errorf("canonical logical_date is invalid: %w", err)
		}
		if err := modulecore.SessionID(probe.ID).Validate(); err != nil {
			return "", fmt.Errorf("canonical session_id is invalid: %w", err)
		}
		if probe.LogicalDate != logicalDate || candidateAddress != address {
			continue
		}
		if matchedID != "" && matchedID != probe.ID {
			return "", fmt.Errorf("multiple canonical sessions match lookup attributes")
		}
		matchedID = probe.ID
	}
	return matchedID, nil
}

// NewJSONSessionRepository は新しいJSONSessionRepositoryを作成
func NewJSONSessionRepository(baseDir string) *JSONSessionRepository {
	return &JSONSessionRepository{
		baseDir: baseDir,
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
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.save(ctx, sess)
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
	return r.saveDTO(ctx, sess, r.toDTO(sess))
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

	data, err := json.MarshalIndent(dto, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal session: %w", err)
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
	if err := os.Rename(temporaryPath, r.getFilePath(sess.ID())); err != nil {
		return fmt.Errorf("failed to commit session file: %w", err)
	}
	committed = true
	return nil
}

// Load はセッションをロード
func (r *JSONSessionRepository) Load(ctx context.Context, id string) (*session.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.load(ctx, id)
}

func (r *JSONSessionRepository) load(ctx context.Context, id string) (*session.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := modulecore.SessionID(id).Validate(); err != nil {
		return nil, fmt.Errorf("invalid canonical session_id: %w", err)
	}
	filePath := r.getFilePath(id)

	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("session %s: %w", id, session.ErrSessionNotFound)
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
	data, err := os.ReadFile(r.getFilePath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("session %s: %w", id, session.ErrSessionNotFound)
		}
		return nil, fmt.Errorf("failed to read session creation intent: %w", err)
	}
	return creationIntentFromJSON(data)
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
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := modulecore.SessionID(id).Validate(); err != nil {
		return false, fmt.Errorf("invalid canonical session_id: %w", err)
	}
	filePath := r.getFilePath(id)
	_, err := os.Stat(filePath)
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
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := modulecore.SessionID(id).Validate(); err != nil {
		return fmt.Errorf("invalid canonical session_id: %w", err)
	}
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
