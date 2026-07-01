package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domainmemory "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/memory"
)

func jst() *time.Location {
	loc, _ := time.LoadLocation("Asia/Tokyo")
	return loc
}

func TestFileStore_ImplementsStore(t *testing.T) {
	var _ domainmemory.Store = (*FileStore)(nil)
}

func TestReadWriteLongTerm(t *testing.T) {
	dir := t.TempDir()
	fs := NewFileStore(dir)

	// 初期状態は空
	if got := fs.ReadLongTerm(); got != "" {
		t.Errorf("expected empty, got %q", got)
	}

	// 書き込み→読み込み
	if err := fs.WriteLongTerm("important fact"); err != nil {
		t.Fatalf("WriteLongTerm: %v", err)
	}
	if got := fs.ReadLongTerm(); got != "important fact" {
		t.Errorf("expected 'important fact', got %q", got)
	}

	// 上書き
	fs.WriteLongTerm("updated fact")
	if got := fs.ReadLongTerm(); got != "updated fact" {
		t.Errorf("expected 'updated fact', got %q", got)
	}
}

func TestReadToday_AppendToday(t *testing.T) {
	dir := t.TempDir()
	fixedTime := time.Date(2026, 3, 5, 14, 0, 0, 0, jst())
	fs := NewFileStore(dir).WithClock(func() time.Time { return fixedTime })

	// 初期状態は空
	if got := fs.ReadToday(); got != "" {
		t.Errorf("expected empty, got %q", got)
	}

	// 追記（新規作成）
	if err := fs.AppendToday("First entry"); err != nil {
		t.Fatalf("AppendToday: %v", err)
	}

	got := fs.ReadToday()
	if !strings.Contains(got, "# 2026-03-05") {
		t.Error("expected date header")
	}
	if !strings.Contains(got, "First entry") {
		t.Error("expected first entry")
	}

	// 追記（既存ファイル）
	fs.AppendToday("Second entry")
	got = fs.ReadToday()
	if !strings.Contains(got, "First entry") {
		t.Error("should still contain first entry")
	}
	if !strings.Contains(got, "Second entry") {
		t.Error("should contain second entry")
	}
}

func TestGetRecentDailyNotes(t *testing.T) {
	dir := t.TempDir()
	fixedTime := time.Date(2026, 3, 5, 14, 0, 0, 0, jst())
	fs := NewFileStore(dir).WithClock(func() time.Time { return fixedTime })

	// 空の場合
	if got := fs.GetRecentDailyNotes(3); got != "" {
		t.Errorf("expected empty, got %q", got)
	}

	// 数日分のノートを作成
	for i := range 3 {
		date := fixedTime.AddDate(0, 0, -i)
		fs.SaveDailyNoteForDate(date, "Note for day "+date.Format("2006-01-02"))
	}

	got := fs.GetRecentDailyNotes(3)
	if !strings.Contains(got, "Note for day 2026-03-05") {
		t.Error("should contain today's note")
	}
	if !strings.Contains(got, "Note for day 2026-03-03") {
		t.Error("should contain note from 2 days ago")
	}
	if !strings.Contains(got, "---") {
		t.Error("should contain separator between notes")
	}
}

func TestSaveDailyNoteForDate(t *testing.T) {
	dir := t.TempDir()
	fs := NewFileStore(dir)

	date := time.Date(2026, 2, 21, 0, 0, 0, 0, jst())
	content := "## Session Summary\n\nHad a good conversation."

	if err := fs.SaveDailyNoteForDate(date, content); err != nil {
		t.Fatalf("SaveDailyNoteForDate: %v", err)
	}

	expectedFile := filepath.Join(dir, "memory", "202602", "20260221.md")
	data, err := os.ReadFile(expectedFile)
	if err != nil {
		t.Fatalf("expected file to exist: %v", err)
	}

	got := string(data)
	if !strings.Contains(got, "2026-02-21") {
		t.Error("should contain date header")
	}
	if !strings.Contains(got, "Session Summary") {
		t.Error("should contain session summary")
	}
}

func TestSaveDailyNoteForDate_Append(t *testing.T) {
	dir := t.TempDir()
	fs := NewFileStore(dir)

	date := time.Date(2026, 2, 21, 0, 0, 0, 0, jst())

	fs.SaveDailyNoteForDate(date, "First entry.")
	fs.SaveDailyNoteForDate(date, "Second entry.")

	expectedFile := filepath.Join(dir, "memory", "202602", "20260221.md")
	data, _ := os.ReadFile(expectedFile)
	got := string(data)

	if !strings.Contains(got, "First entry.") {
		t.Error("should contain first entry")
	}
	if !strings.Contains(got, "Second entry.") {
		t.Error("should contain second entry")
	}
}

func TestGetMemoryContext(t *testing.T) {
	dir := t.TempDir()
	fixedTime := time.Date(2026, 3, 5, 14, 0, 0, 0, jst())
	fs := NewFileStore(dir).WithClock(func() time.Time { return fixedTime })

	// 空の場合
	if got := fs.GetMemoryContext(); got != "" {
		t.Errorf("expected empty, got %q", got)
	}

	// 長期記憶 + 日次ノート
	fs.WriteLongTerm("User likes rabbits")
	fs.SaveDailyNoteForDate(fixedTime, "Today's discussion")

	got := fs.GetMemoryContext()
	if !strings.Contains(got, "# Memory") {
		t.Error("should have Memory header")
	}
	if !strings.Contains(got, "Long-term Memory") {
		t.Error("should contain long-term section")
	}
	if !strings.Contains(got, "User likes rabbits") {
		t.Error("should contain long-term content")
	}
	if !strings.Contains(got, "Recent Daily Notes") {
		t.Error("should contain recent notes section")
	}
	if !strings.Contains(got, "Today's discussion") {
		t.Error("should contain daily note content")
	}
}

func TestGetMemoryContext_LongTermOnly(t *testing.T) {
	dir := t.TempDir()
	fs := NewFileStore(dir)

	fs.WriteLongTerm("Only long-term")

	got := fs.GetMemoryContext()
	if !strings.Contains(got, "Long-term Memory") {
		t.Error("should contain long-term section")
	}
	if strings.Contains(got, "Recent Daily Notes") {
		t.Error("should NOT contain recent notes section")
	}
}

func TestDirectoryStructure(t *testing.T) {
	dir := t.TempDir()
	fs := NewFileStore(dir)

	// memory/ ディレクトリが作成されること
	memDir := filepath.Join(dir, "memory")
	if _, err := os.Stat(memDir); os.IsNotExist(err) {
		t.Error("memory directory should be created")
	}

	// 月ディレクトリが自動作成されること
	date := time.Date(2026, 3, 5, 0, 0, 0, 0, jst())
	fs.SaveDailyNoteForDate(date, "test")

	monthDir := filepath.Join(dir, "memory", "202603")
	if _, err := os.Stat(monthDir); os.IsNotExist(err) {
		t.Error("month directory should be created")
	}
}
