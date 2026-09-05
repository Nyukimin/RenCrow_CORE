package session

import (
	"context"
	"encoding/json"
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

// LoadOrCreateCanonical resolves a daily conversation using explicit lookup
// attributes. SessionID remains opaque and is generated only when no matching
// canonical session exists.
func (r *JSONSessionRepository) LoadOrCreateCanonical(ctx context.Context, logicalDate string, address conversation.ChannelAddress, createdAt time.Time) (*session.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := session.ValidateLogicalDate(logicalDate); err != nil {
		return nil, fmt.Errorf("invalid canonical session lookup: %w", err)
	}
	if err := address.Validate(); err != nil {
		return nil, fmt.Errorf("invalid canonical session lookup: %w", err)
	}
	if createdAt.IsZero() {
		return nil, fmt.Errorf("invalid canonical session lookup: created_at is required")
	}
	entries, err := os.ReadDir(r.baseDir)
	if err != nil {
		return nil, fmt.Errorf("read session directory: %w", err)
	}
	matchedID := ""
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, readErr := os.ReadFile(filepath.Join(r.baseDir, entry.Name()))
		if readErr != nil {
			return nil, fmt.Errorf("read session identity: %w", readErr)
		}
		if err := rejectLegacySessionIdentityFields(raw); err != nil {
			return nil, err
		}
		var probe sessionIdentityProbe
		if err := json.Unmarshal(raw, &probe); err != nil {
			return nil, fmt.Errorf("decode session identity: %w", err)
		}
		if probe.ID == "" && probe.LogicalDate == "" && probe.ChannelAddress == nil {
			continue
		}
		if probe.ID == "" || probe.LogicalDate == "" || probe.ChannelAddress == nil {
			return nil, fmt.Errorf("canonical session identity is incomplete")
		}
		candidateAddress, err := channelAddressFromDTO(probe.ChannelAddress)
		if err != nil {
			return nil, fmt.Errorf("canonical ChannelAddress is invalid: %w", err)
		}
		if err := session.ValidateLogicalDate(probe.LogicalDate); err != nil {
			return nil, fmt.Errorf("canonical logical_date is invalid: %w", err)
		}
		if err := modulecore.SessionID(probe.ID).Validate(); err != nil {
			return nil, fmt.Errorf("canonical session_id is invalid: %w", err)
		}
		if probe.LogicalDate != logicalDate || candidateAddress != address {
			continue
		}
		if matchedID != "" && matchedID != probe.ID {
			return nil, fmt.Errorf("multiple canonical sessions match lookup attributes")
		}
		matchedID = probe.ID
	}
	if matchedID != "" {
		return r.load(ctx, matchedID)
	}
	created, err := session.NewCanonicalSession(modulecore.NewSessionID(), logicalDate, address, createdAt)
	if err != nil {
		return nil, err
	}
	if err := r.save(ctx, created); err != nil {
		return nil, err
	}
	return created, nil
}

// NewJSONSessionRepository は新しいJSONSessionRepositoryを作成
func NewJSONSessionRepository(baseDir string) *JSONSessionRepository {
	return &JSONSessionRepository{
		baseDir: baseDir,
	}
}

// sessionDTO はJSONシリアライズ用のDTO
type sessionDTO struct {
	ID             string                 `json:"id"`
	LogicalDate    string                 `json:"logical_date"`
	ChannelAddress *channelAddressDTO     `json:"channel_address"`
	History        []turnInputDTO         `json:"history"`
	Memory         map[string]interface{} `json:"memory"`
	CreatedAt      time.Time              `json:"created_at"`
	UpdatedAt      time.Time              `json:"updated_at"`
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
	dto := r.toDTO(sess)

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

	result, err := r.fromDTO(&dto)
	if err != nil {
		return nil, fmt.Errorf("failed to reconstruct session: %w", err)
	}
	return result, nil
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
