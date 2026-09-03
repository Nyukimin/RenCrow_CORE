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

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// JSONSessionRepository はJSONファイルベースのSessionRepository実装
type JSONSessionRepository struct {
	baseDir string
	mu      sync.Mutex
}

type sessionIdentityProbe struct {
	ID             string                  `json:"id"`
	LogicalDate    string                  `json:"logical_date"`
	ChannelAddress *session.ChannelAddress `json:"channel_address"`
}

// LoadOrCreateCanonical resolves a daily conversation using explicit lookup
// attributes. SessionID remains opaque and is generated only when no matching
// canonical session exists.
func (r *JSONSessionRepository) LoadOrCreateCanonical(ctx context.Context, logicalDate string, address session.ChannelAddress, createdAt time.Time) (*session.Session, error) {
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
		if err := probe.ChannelAddress.Validate(); err != nil {
			return nil, fmt.Errorf("canonical ChannelAddress is invalid: %w", err)
		}
		if err := session.ValidateLogicalDate(probe.LogicalDate); err != nil {
			return nil, fmt.Errorf("canonical logical_date is invalid: %w", err)
		}
		if err := modulecore.SessionID(probe.ID).Validate(); err != nil {
			return nil, fmt.Errorf("canonical session_id is invalid: %w", err)
		}
		if probe.LogicalDate != logicalDate || *probe.ChannelAddress != address {
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
	ID             string                  `json:"id"`
	LogicalDate    string                  `json:"logical_date"`
	ChannelAddress *session.ChannelAddress `json:"channel_address"`
	History        []taskDTO               `json:"history"`
	Memory         map[string]interface{}  `json:"memory"`
	CreatedAt      time.Time               `json:"created_at"`
	UpdatedAt      time.Time               `json:"updated_at"`
}

// taskDTO はJSONシリアライズ用のDTO
type taskDTO struct {
	JobID       string `json:"job_id"`
	UserMessage string `json:"user_message"`
	Channel     string `json:"channel"`
	ChatID      string `json:"chat_id"`
	ForcedRoute string `json:"forced_route,omitempty"`
	Route       string `json:"route,omitempty"`
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
	history := make([]taskDTO, 0, sess.HistoryCount())
	for _, t := range sess.GetHistory() {
		history = append(history, taskDTO{
			JobID:       t.JobID().String(),
			UserMessage: t.UserMessage(),
			Channel:     t.Channel(),
			ChatID:      t.ChatID(),
			ForcedRoute: string(t.ForcedRoute()),
			Route:       string(t.Route()),
		})
	}

	address := sess.ChannelAddress()
	dto := &sessionDTO{
		ID:             sess.ID(),
		LogicalDate:    sess.LogicalDate(),
		ChannelAddress: &address,
		History:        history,
		Memory:         sess.GetAllMemory(),
		CreatedAt:      sess.CreatedAt(),
		UpdatedAt:      sess.UpdatedAt(),
	}
	return dto
}

// fromDTO はDTOからSessionを生成
func (r *JSONSessionRepository) fromDTO(dto *sessionDTO) (*session.Session, error) {
	history := make([]task.Task, 0, len(dto.History))
	for _, taskDTO := range dto.History {
		jobID := task.JobIDFromString(taskDTO.JobID)
		t := task.NewTask(jobID, taskDTO.UserMessage, taskDTO.Channel, taskDTO.ChatID)
		if taskDTO.ForcedRoute != "" {
			t = t.WithForcedRoute(routing.Route(taskDTO.ForcedRoute))
		}
		if taskDTO.Route != "" {
			t = t.WithRoute(routing.Route(taskDTO.Route))
		}
		history = append(history, t)
	}

	if dto.ChannelAddress == nil {
		return nil, fmt.Errorf("channel_address is required")
	}
	return session.ReconstructCanonicalSession(modulecore.SessionID(dto.ID), dto.LogicalDate, *dto.ChannelAddress, history, dto.Memory, dto.CreatedAt, dto.UpdatedAt)
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
	return nil
}
