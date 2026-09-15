package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

// JSONSessionRepository はJSONファイルベースのSessionRepository実装
type JSONSessionRepository struct {
	baseDir string
}

// NewJSONSessionRepository は新しいJSONSessionRepositoryを作成
func NewJSONSessionRepository(baseDir string) *JSONSessionRepository {
	return &JSONSessionRepository{
		baseDir: baseDir,
	}
}

// sessionDTO はJSONシリアライズ用のDTO
type sessionDTO struct {
	ID        string                 `json:"id"`
	Channel   string                 `json:"channel"`
	ChatID    string                 `json:"chat_id"`
	History   []taskDTO              `json:"history"`
	Memory    map[string]interface{} `json:"memory"`
	CreatedAt time.Time              `json:"created_at"`
	UpdatedAt time.Time              `json:"updated_at"`
}

// taskDTO はJSONシリアライズ用のDTO
type taskDTO struct {
	TaskID string `json:"task_id"`
	// LegacyJobID is read-only migration input. It is never populated by
	// toDTO, so newly written sessions contain task_id only.
	LegacyJobID string `json:"job_id,omitempty"`
	UserMessage string `json:"user_message"`
	Channel     string `json:"channel"`
	ChatID      string `json:"chat_id"`
	ForcedRoute string `json:"forced_route,omitempty"`
	Route       string `json:"route,omitempty"`
}

// Save はセッションを保存
func (r *JSONSessionRepository) Save(ctx context.Context, sess *session.Session) error {
	dto, err := r.toDTO(sess)
	if err != nil {
		return fmt.Errorf("failed to prepare session: %w", err)
	}

	data, err := json.MarshalIndent(dto, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal session: %w", err)
	}

	filePath := r.getFilePath(sess.ID())
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write session file: %w", err)
	}

	return nil
}

// Load はセッションをロード
func (r *JSONSessionRepository) Load(ctx context.Context, id string) (*session.Session, error) {
	filePath := r.getFilePath(id)

	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("session %s: %w", id, session.ErrSessionNotFound)
		}
		return nil, fmt.Errorf("failed to read session file: %w", err)
	}

	var dto sessionDTO
	if err := json.Unmarshal(data, &dto); err != nil {
		return nil, fmt.Errorf("failed to unmarshal session: %w", err)
	}

	loaded, err := r.fromDTO(&dto)
	if err != nil {
		return nil, fmt.Errorf("failed to restore session: %w", err)
	}
	return loaded, nil
}

// Exists はセッションが存在するか確認
func (r *JSONSessionRepository) Exists(ctx context.Context, id string) (bool, error) {
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
func (r *JSONSessionRepository) toDTO(sess *session.Session) (*sessionDTO, error) {
	history := make([]taskDTO, 0, sess.HistoryCount())
	for _, t := range sess.GetHistory() {
		if err := t.TaskID().Validate(); err != nil {
			return nil, fmt.Errorf("task %q has invalid task_id: %w", t.TaskID(), err)
		}
		history = append(history, taskDTO{
			TaskID:      t.TaskID().String(),
			UserMessage: t.UserMessage(),
			Channel:     t.Channel(),
			ChatID:      t.ChatID(),
			ForcedRoute: string(t.ForcedRoute()),
			Route:       string(t.Route()),
		})
	}

	return &sessionDTO{
		ID:        sess.ID(),
		Channel:   sess.Channel(),
		ChatID:    sess.ChatID(),
		History:   history,
		Memory:    sess.GetAllMemory(),
		CreatedAt: sess.CreatedAt(),
		UpdatedAt: sess.UpdatedAt(),
	}, nil
}

// fromDTO はDTOからSessionを生成
func (r *JSONSessionRepository) fromDTO(dto *sessionDTO) (*session.Session, error) {
	sess := session.ReconstructSession(dto.ID, dto.Channel, dto.ChatID, dto.CreatedAt, dto.UpdatedAt)

	// 履歴を復元
	for _, taskDTO := range dto.History {
		if taskDTO.TaskID != "" && taskDTO.LegacyJobID != "" {
			return nil, fmt.Errorf("task history item has both task_id and legacy job_id")
		}
		var taskID task.TaskID
		var err error
		switch {
		case taskDTO.TaskID != "":
			taskID, err = task.ParseTaskID(taskDTO.TaskID)
		case taskDTO.LegacyJobID != "":
			taskID, err = task.MigrateLegacySessionTaskID(taskDTO.LegacyJobID)
		default:
			err = fmt.Errorf("task history item is missing task_id")
		}
		if err != nil {
			return nil, fmt.Errorf("restore task identity: %w", err)
		}
		t := task.NewTask(taskID, taskDTO.UserMessage, taskDTO.Channel, taskDTO.ChatID)

		if taskDTO.ForcedRoute != "" {
			t = t.WithForcedRoute(routing.Route(taskDTO.ForcedRoute))
		}
		if taskDTO.Route != "" {
			t = t.WithRoute(routing.Route(taskDTO.Route))
		}

		sess.AddTask(t)
	}

	// メモリを復元
	for key, value := range dto.Memory {
		sess.SetMemory(key, value)
	}

	return sess, nil
}
