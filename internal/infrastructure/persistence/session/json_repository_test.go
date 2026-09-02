package session

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestJSONSessionRepositoryCanonicalIdentityRoundTrip(t *testing.T) {
	dir := t.TempDir()
	repo := NewJSONSessionRepository(dir)
	address, err := session.NewChannelAddress("line", "U123")
	if err != nil {
		t.Fatal(err)
	}
	id := modulecore.NewSessionID()
	sess, err := session.NewCanonicalSession(id, "2026-09-02", address, time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	sess.AddTask(task.NewTask(task.NewJobID(), "hello", "line", "U123"))
	sess.SetMemory("key", "value")
	if err := repo.Save(context.Background(), sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, string(id)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["logical_date"]; !ok {
		t.Fatal("logical_date is absent")
	}
	if _, ok := fields["channel_address"]; !ok {
		t.Fatal("channel_address is absent")
	}
	if _, ok := fields["channel"]; ok {
		t.Fatal("legacy channel must not be written for a canonical session")
	}
	if _, ok := fields["chat_id"]; ok {
		t.Fatal("legacy chat_id must not be written for a canonical session")
	}

	loaded, err := repo.Load(context.Background(), string(id))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.ID() != string(id) || loaded.LogicalDate() != "2026-09-02" || loaded.ChannelAddress() != address {
		t.Fatalf("loaded identity = id:%q date:%q address:%#v", loaded.ID(), loaded.LogicalDate(), loaded.ChannelAddress())
	}
	if !loaded.CreatedAt().Equal(sess.CreatedAt()) || !loaded.UpdatedAt().Equal(sess.UpdatedAt()) {
		t.Fatalf("loaded timestamps = %s/%s, want %s/%s", loaded.CreatedAt(), loaded.UpdatedAt(), sess.CreatedAt(), sess.UpdatedAt())
	}
}

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
	jobID := task.NewJobID()
	testTask := task.NewTask(jobID, "テストメッセージ", "line", "U123")
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
		jobID := task.NewJobID()
		testTask := task.NewTask(jobID, "Message "+string(rune('A'+i)), "line", "U123")
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
