package session

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func newCanonicalRepositoryTestSession(t *testing.T) *session.Session {
	t.Helper()
	address, err := conversation.NewChannelAddress("line", "U123")
	if err != nil {
		t.Fatal(err)
	}
	value, err := session.NewCanonicalSession(modulecore.NewSessionID(), "2026-03-01", address, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestJSONSessionRepositoryLoadOrCreateCanonicalUsesExplicitLookupAttributes(t *testing.T) {
	repo := NewJSONSessionRepository(t.TempDir())
	address, err := conversation.NewChannelAddress("line", "U123")
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	first, err := repo.LoadOrCreateCanonical(context.Background(), "2026-09-03", address, createdAt)
	if err != nil {
		t.Fatalf("first LoadOrCreateCanonical: %v", err)
	}
	second, err := repo.LoadOrCreateCanonical(context.Background(), "2026-09-03", address, createdAt.Add(time.Hour))
	if err != nil {
		t.Fatalf("second LoadOrCreateCanonical: %v", err)
	}
	if first.ID() != second.ID() {
		t.Fatalf("same lookup attributes produced %q and %q", first.ID(), second.ID())
	}
	if err := modulecore.SessionID(first.ID()).Validate(); err != nil {
		t.Fatalf("created SessionID: %v", err)
	}

	nextDate, err := repo.LoadOrCreateCanonical(context.Background(), "2026-09-04", address, createdAt.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("date boundary LoadOrCreateCanonical: %v", err)
	}
	if nextDate.ID() == first.ID() {
		t.Fatal("date boundary reused the prior SessionID")
	}
}

func TestJSONSessionRepositoryLoadOrCreateCanonicalIsConcurrentSafe(t *testing.T) {
	dir := t.TempDir()
	repo := NewJSONSessionRepository(dir)
	address, err := conversation.NewChannelAddress("viewer", "ren")
	if err != nil {
		t.Fatal(err)
	}
	const workers = 32
	ids := make(chan string, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			item, loadErr := repo.LoadOrCreateCanonical(context.Background(), "2026-09-03", address, time.Now().UTC())
			if loadErr != nil {
				errs <- loadErr
				return
			}
			ids <- item.ID()
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatalf("LoadOrCreateCanonical: %v", err)
	}
	var want string
	for id := range ids {
		if want == "" {
			want = id
		}
		if id != want {
			t.Fatalf("concurrent IDs include %q and %q", want, id)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("session files = %d, want 1", len(entries))
	}
}

func TestJSONSessionRepositoryCanonicalIdentityRoundTrip(t *testing.T) {
	dir := t.TempDir()
	repo := NewJSONSessionRepository(dir)
	address, err := conversation.NewChannelAddress("line", "U123")
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
	var channelAddress map[string]json.RawMessage
	if err := json.Unmarshal(fields["channel_address"], &channelAddress); err != nil {
		t.Fatalf("channel_address: %v", err)
	}
	if len(channelAddress) != 2 {
		t.Fatalf("channel_address keys = %v, want exactly channel_type and external_conversation_id", channelAddress)
	}
	for _, key := range []string{"channel_type", "external_conversation_id"} {
		if _, ok := channelAddress[key]; !ok {
			t.Fatalf("channel_address.%s is absent", key)
		}
	}
	for _, key := range []string{"channel", "address"} {
		if _, ok := channelAddress[key]; ok {
			t.Fatalf("legacy channel_address.%s must not be written", key)
		}
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

func TestJSONSessionRepositoryRejectsLegacySessionContract(t *testing.T) {
	dir := t.TempDir()
	legacy := []byte(`{"id":"20260301-line-U123","channel":"line","chat_id":"U123","history":[],"memory":{},"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`)
	if err := os.WriteFile(filepath.Join(dir, "20260301-line-U123.json"), legacy, 0600); err != nil {
		t.Fatal(err)
	}
	repo := NewJSONSessionRepository(dir)
	if _, err := repo.Load(context.Background(), "20260301-line-U123"); err == nil {
		t.Fatal("legacy Session was accepted by canonical repository Load")
	}
	address, err := conversation.NewChannelAddress("line", "U123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.LoadOrCreateCanonical(context.Background(), "2026-03-01", address, time.Now().UTC()); err == nil {
		t.Fatal("legacy Session was skipped by canonical lookup")
	}
}

func TestJSONSessionRepositoryRejectsCanonicalRecordWithLegacyIdentityFields(t *testing.T) {
	dir := t.TempDir()
	id := modulecore.NewSessionID()
	mixed := []byte(`{"id":"` + string(id) + `","logical_date":"2026-03-01","channel_address":{"channel":"line","address":"U123"},"history":[],"memory":{},"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`)
	if err := os.WriteFile(filepath.Join(dir, string(id)+".json"), mixed, 0600); err != nil {
		t.Fatal(err)
	}
	repo := NewJSONSessionRepository(dir)
	if _, err := repo.Load(context.Background(), string(id)); err == nil {
		t.Fatal("canonical Session with legacy identity fields was accepted")
	}
	address, err := conversation.NewChannelAddress("line", "U123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.LoadOrCreateCanonical(context.Background(), "2026-03-01", address, time.Now().UTC()); err == nil {
		t.Fatal("canonical lookup accepted legacy nested channel_address fields")
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
	sess := newCanonicalRepositoryTestSession(t)
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
	loaded, err := repo.Load(context.Background(), sess.ID())
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if loaded.ID() != sess.ID() {
		t.Errorf("Expected ID '%s', got '%s'", sess.ID(), loaded.ID())
	}

	if loaded.ChannelAddress() != sess.ChannelAddress() {
		t.Errorf("Expected ChannelAddress %#v, got %#v", sess.ChannelAddress(), loaded.ChannelAddress())
	}

	if loaded.HistoryCount() != 1 {
		t.Errorf("Expected 1 task in history, got %d", loaded.HistoryCount())
	}
	history := loaded.GetHistory()
	if len(history) != 1 || history[0].SessionID() != sess.ID() {
		t.Fatalf("loaded task SessionID=%q, want canonical session %q", history[0].SessionID(), sess.ID())
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

	_, err := repo.Load(context.Background(), string(modulecore.NewSessionID()))
	if err == nil {
		t.Error("Expected error when loading non-existent session")
	}
}

func TestJSONSessionRepository_Exists(t *testing.T) {
	tmpDir := t.TempDir()
	repo := NewJSONSessionRepository(tmpDir)

	sess := newCanonicalRepositoryTestSession(t)
	if err := repo.Save(context.Background(), sess); err != nil {
		t.Fatal(err)
	}

	exists, err := repo.Exists(context.Background(), sess.ID())
	if err != nil {
		t.Fatalf("Exists failed: %v", err)
	}

	if !exists {
		t.Error("Session should exist")
	}

	exists, err = repo.Exists(context.Background(), string(modulecore.NewSessionID()))
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

	sess := newCanonicalRepositoryTestSession(t)
	if err := repo.Save(context.Background(), sess); err != nil {
		t.Fatal(err)
	}

	// 削除前に存在確認
	exists, _ := repo.Exists(context.Background(), sess.ID())
	if !exists {
		t.Error("Session should exist before deletion")
	}

	// 削除
	err := repo.Delete(context.Background(), sess.ID())
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// 削除後に存在確認
	exists, _ = repo.Exists(context.Background(), sess.ID())
	if exists {
		t.Error("Session should not exist after deletion")
	}
}

func TestJSONSessionRepository_FileStructure(t *testing.T) {
	tmpDir := t.TempDir()
	repo := NewJSONSessionRepository(tmpDir)

	sess := newCanonicalRepositoryTestSession(t)
	if err := repo.Save(context.Background(), sess); err != nil {
		t.Fatal(err)
	}

	// ファイルが正しい場所に作成されているか確認
	expectedPath := filepath.Join(tmpDir, sess.ID()+".json")
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

	sess := newCanonicalRepositoryTestSession(t)

	// 複数のタスクを追加
	for i := 0; i < 5; i++ {
		jobID := task.NewJobID()
		testTask := task.NewTask(jobID, "Message "+string(rune('A'+i)), "line", "U123")
		sess.AddTask(testTask)
	}

	// 保存してロード
	if err := repo.Save(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	loaded, err := repo.Load(context.Background(), sess.ID())
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

	sess := newCanonicalRepositoryTestSession(t)
	sess.SetMemory("string", "value")
	sess.SetMemory("number", 42)
	sess.SetMemory("bool", true)

	if err := repo.Save(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	loaded, err := repo.Load(context.Background(), sess.ID())
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
