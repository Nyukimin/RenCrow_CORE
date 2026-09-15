package session

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

func TestNewJSONSessionRepository(t *testing.T) {
	tmpDir := t.TempDir()
	repo := NewJSONSessionRepository(tmpDir)

	if repo == nil {
		t.Fatal("NewJSONSessionRepository should not return nil")
	}
}

func TestJSONSessionRepository_SaveAndLoad(t *testing.T) {
	tmpDir := t.TempDir()
	repo := NewJSONSessionRepository(tmpDir)

	// セッション作成
	sess := session.NewSession("20260301-line-U123", "line", "U123")
	taskID := task.NewTaskID()
	testTask := task.NewTask(taskID, "テストメッセージ", "line", "U123")
	sess.AddTask(testTask)
	sess.SetMemory("key1", "value1")

	// 保存
	err := repo.Save(context.Background(), sess)
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// ロード
	loaded, err := repo.Load(context.Background(), "20260301-line-U123")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if loaded.ID() != sess.ID() {
		t.Errorf("Expected ID '%s', got '%s'", sess.ID(), loaded.ID())
	}

	if loaded.Channel() != sess.Channel() {
		t.Errorf("Expected channel '%s', got '%s'", sess.Channel(), loaded.Channel())
	}

	if loaded.ChatID() != sess.ChatID() {
		t.Errorf("Expected chatID '%s', got '%s'", sess.ChatID(), loaded.ChatID())
	}

	if loaded.HistoryCount() != 1 {
		t.Errorf("Expected 1 task in history, got %d", loaded.HistoryCount())
	}

	value, ok := loaded.GetMemory("key1")
	if !ok {
		t.Error("Expected key1 to exist in memory")
	}
	if value != "value1" {
		t.Errorf("Expected memory value 'value1', got '%v'", value)
	}
}

func TestJSONSessionRepositoryWritesCanonicalTaskID(t *testing.T) {
	tmpDir := t.TempDir()
	repo := NewJSONSessionRepository(tmpDir)
	sess := session.NewSession("canonical-task-session", "line", "U123")
	sess.AddTask(task.NewTask(task.NewTaskID(), "hello", "line", "U123"))

	if err := repo.Save(context.Background(), sess); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(tmpDir, "canonical-task-session.json"))
	if err != nil {
		t.Fatalf("read saved session: %v", err)
	}
	var decoded struct {
		History []map[string]any `json:"history"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode saved session: %v", err)
	}
	if len(decoded.History) != 1 {
		t.Fatalf("saved history length = %d, want 1", len(decoded.History))
	}
	raw, ok := decoded.History[0]["task_id"].(string)
	if !ok || !strings.HasPrefix(raw, "tsk_") {
		t.Fatalf("saved task_id = %#v, want canonical tsk_ ID", decoded.History[0]["task_id"])
	}
	if _, exists := decoded.History[0]["job_id"]; exists {
		t.Fatal("saved session contains legacy job_id")
	}
	if _, err := task.ParseTaskID(raw); err != nil {
		t.Fatalf("saved task_id is invalid: %v", err)
	}
}

func TestJSONSessionRepositoryRejectsInvalidTaskIDOnWrite(t *testing.T) {
	repo := NewJSONSessionRepository(t.TempDir())
	sess := session.NewSession("invalid-task-session", "line", "U123")
	sess.AddTask(task.NewTask(task.TaskID("job-legacy"), "hello", "line", "U123"))

	if err := repo.Save(context.Background(), sess); err == nil {
		t.Fatal("Save accepted a non-canonical TaskID")
	}
}

func TestJSONSessionRepositoryMigratesLegacySessionJobIDOnRead(t *testing.T) {
	tmpDir := t.TempDir()
	repo := NewJSONSessionRepository(tmpDir)
	legacy := "20260301-120000-abcd1234"
	fixture := `{"id":"legacy-session","channel":"line","chat_id":"U123","history":[{"job_id":"` + legacy + `","user_message":"old","channel":"line","chat_id":"U123"}],"memory":{}}`
	if err := os.WriteFile(filepath.Join(tmpDir, "legacy-session.json"), []byte(fixture), 0644); err != nil {
		t.Fatalf("write legacy session: %v", err)
	}

	loaded, err := repo.Load(context.Background(), "legacy-session")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	history := loaded.GetHistory()
	if len(history) != 1 {
		t.Fatalf("loaded history length = %d, want 1", len(history))
	}
	want, err := task.MigrateLegacySessionTaskID(legacy)
	if err != nil {
		t.Fatalf("derive expected migration ID: %v", err)
	}
	if history[0].TaskID() != want {
		t.Fatalf("migrated task_id = %q, want %q", history[0].TaskID(), want)
	}
}

func TestJSONSessionRepository_LoadNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	repo := NewJSONSessionRepository(tmpDir)

	_, err := repo.Load(context.Background(), "nonexistent")
	if err == nil {
		t.Error("Expected error when loading non-existent session")
	}
}

func TestJSONSessionRepository_Exists(t *testing.T) {
	tmpDir := t.TempDir()
	repo := NewJSONSessionRepository(tmpDir)

	sess := session.NewSession("test-session", "line", "U123")
	repo.Save(context.Background(), sess)

	exists, err := repo.Exists(context.Background(), "test-session")
	if err != nil {
		t.Fatalf("Exists failed: %v", err)
	}

	if !exists {
		t.Error("Session should exist")
	}

	exists, err = repo.Exists(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("Exists failed: %v", err)
	}

	if exists {
		t.Error("Session should not exist")
	}
}

func TestJSONSessionRepository_Delete(t *testing.T) {
	tmpDir := t.TempDir()
	repo := NewJSONSessionRepository(tmpDir)

	sess := session.NewSession("test-session", "line", "U123")
	repo.Save(context.Background(), sess)

	// 削除前に存在確認
	exists, _ := repo.Exists(context.Background(), "test-session")
	if !exists {
		t.Error("Session should exist before deletion")
	}

	// 削除
	err := repo.Delete(context.Background(), "test-session")
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// 削除後に存在確認
	exists, _ = repo.Exists(context.Background(), "test-session")
	if exists {
		t.Error("Session should not exist after deletion")
	}
}

func TestJSONSessionRepository_FileStructure(t *testing.T) {
	tmpDir := t.TempDir()
	repo := NewJSONSessionRepository(tmpDir)

	sess := session.NewSession("20260301-line-U123", "line", "U123")
	repo.Save(context.Background(), sess)

	// ファイルが正しい場所に作成されているか確認
	expectedPath := filepath.Join(tmpDir, "20260301-line-U123.json")
	if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
		t.Errorf("Expected file to exist at %s", expectedPath)
	}

	// ファイルの内容がJSONとして読めるか確認
	data, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}

	if len(data) == 0 {
		t.Error("File should not be empty")
	}
}

func TestJSONSessionRepository_MultipleHistoryItems(t *testing.T) {
	tmpDir := t.TempDir()
	repo := NewJSONSessionRepository(tmpDir)

	sess := session.NewSession("test-session", "line", "U123")

	// 複数のタスクを追加
	for i := 0; i < 5; i++ {
		taskID := task.NewTaskID()
		testTask := task.NewTask(taskID, "Message "+string(rune('A'+i)), "line", "U123")
		sess.AddTask(testTask)
	}

	// 保存してロード
	repo.Save(context.Background(), sess)
	loaded, err := repo.Load(context.Background(), "test-session")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if loaded.HistoryCount() != 5 {
		t.Errorf("Expected 5 tasks in history, got %d", loaded.HistoryCount())
	}

	history := loaded.GetHistory()
	if history[0].UserMessage() != "Message A" {
		t.Errorf("Expected first message 'Message A', got '%s'", history[0].UserMessage())
	}
}

func TestJSONSessionRepository_MemoryPreservation(t *testing.T) {
	tmpDir := t.TempDir()
	repo := NewJSONSessionRepository(tmpDir)

	sess := session.NewSession("test-session", "line", "U123")
	sess.SetMemory("string", "value")
	sess.SetMemory("number", 42)
	sess.SetMemory("bool", true)

	repo.Save(context.Background(), sess)
	loaded, err := repo.Load(context.Background(), "test-session")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// メモリが正しく保存・復元されているか確認
	if val, ok := loaded.GetMemory("string"); !ok || val != "value" {
		t.Errorf("Expected string memory 'value', got '%v'", val)
	}

	if val, ok := loaded.GetMemory("number"); !ok || val.(float64) != 42 { // JSONは数値をfloat64にする
		t.Errorf("Expected number memory 42, got '%v'", val)
	}

	if val, ok := loaded.GetMemory("bool"); !ok || val != true {
		t.Errorf("Expected bool memory true, got '%v'", val)
	}
}
