package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

// FileStore はファイルベースのメモリ永続化実装
// - 長期記憶: memory/MEMORY.md
// - 日次ノート: memory/YYYYMM/YYYYMMDD.md
type FileStore struct {
	memoryDir  string
	memoryFile string
	now        func() time.Time // テスト用に注入可能
}

// NewFileStore は新しいFileStoreを作成する
func NewFileStore(workspaceDir string) *FileStore {
	memoryDir := filepath.Join(workspaceDir, "memory")
	return NewFileStoreAt(memoryDir)
}

// NewFileStoreAt は指定ディレクトリをメモリ永続化先としてFileStoreを作成する
func NewFileStoreAt(memoryDir string) *FileStore {
	memoryFile := filepath.Join(memoryDir, "MEMORY.md")

	// メモリディレクトリを確保
	os.MkdirAll(memoryDir, 0755)

	return &FileStore{
		memoryDir:  memoryDir,
		memoryFile: memoryFile,
		now:        time.Now,
	}
}

// WithClock はテスト用に時刻関数を差し替える
func (fs *FileStore) WithClock(now func() time.Time) *FileStore {
	fs.now = now
	return fs
}

// getTodayFile は今日の日次ノートファイルパスを返す (memory/YYYYMM/YYYYMMDD.md)
func (fs *FileStore) getTodayFile() string {
	today := fs.now().Format("20060102") // YYYYMMDD
	monthDir := today[:6]                // YYYYMM
	return filepath.Join(fs.memoryDir, monthDir, today+".md")
}

// ReadLongTerm は長期記憶（MEMORY.md）を読み込む
func (fs *FileStore) ReadLongTerm() string {
	if data, err := os.ReadFile(fs.memoryFile); err == nil {
		return string(data)
	}
	return ""
}

// WriteLongTerm は長期記憶に書き込む
func (fs *FileStore) WriteLongTerm(content string) error {
	return os.WriteFile(fs.memoryFile, []byte(content), 0644)
}

// ReadToday は今日の日次ノートを読み込む
func (fs *FileStore) ReadToday() string {
	todayFile := fs.getTodayFile()
	if data, err := os.ReadFile(todayFile); err == nil {
		return string(data)
	}
	return ""
}

// AppendToday は今日の日次ノートに追記する
func (fs *FileStore) AppendToday(content string) error {
	todayFile := fs.getTodayFile()

	// 月ディレクトリを確保
	monthDir := filepath.Dir(todayFile)
	os.MkdirAll(monthDir, 0755)

	var existingContent string
	if data, err := os.ReadFile(todayFile); err == nil {
		existingContent = string(data)
	}

	var newContent string
	if existingContent == "" {
		header := fmt.Sprintf("# %s\n\n", fs.now().Format("2006-01-02"))
		newContent = header + content
	} else {
		newContent = existingContent + "\n" + content
	}

	return os.WriteFile(todayFile, []byte(newContent), 0644)
}

// GetRecentDailyNotes は直近N日分の日次ノートを返す
func (fs *FileStore) GetRecentDailyNotes(days int) string {
	var notes []string

	for i := range days {
		date := fs.now().AddDate(0, 0, -i)
		dateStr := date.Format("20060102") // YYYYMMDD
		monthDir := dateStr[:6]            // YYYYMM
		filePath := filepath.Join(fs.memoryDir, monthDir, dateStr+".md")

		if data, err := os.ReadFile(filePath); err == nil {
			notes = append(notes, string(data))
		}
	}

	if len(notes) == 0 {
		return ""
	}

	return strings.Join(notes, "\n\n---\n\n")
}

// SaveDailyNoteForDate は指定日の日次ノートに書き込む
func (fs *FileStore) SaveDailyNoteForDate(date time.Time, content string) error {
	dateStr := date.Format("20060102")
	monthDir := dateStr[:6]
	dirPath := filepath.Join(fs.memoryDir, monthDir)
	os.MkdirAll(dirPath, 0755)

	filePath := filepath.Join(dirPath, dateStr+".md")

	var existingContent string
	if data, err := os.ReadFile(filePath); err == nil {
		existingContent = string(data)
	}

	var newContent string
	if existingContent == "" {
		header := fmt.Sprintf("# %s\n\n", date.Format("2006-01-02"))
		newContent = header + content
	} else {
		newContent = existingContent + "\n\n" + content
	}

	return os.WriteFile(filePath, []byte(newContent), 0644)
}

// GetMemoryContext はエージェントプロンプト用のメモリコンテキストを返す
func (fs *FileStore) GetMemoryContext() string {
	var parts []string

	longTerm := fs.ReadLongTerm()
	if longTerm != "" {
		parts = append(parts, "## Long-term Memory\n\n"+longTerm)
	}

	recentNotes := fs.GetRecentDailyNotes(3)
	if recentNotes != "" {
		parts = append(parts, "## Recent Daily Notes\n\n"+recentNotes)
	}

	if len(parts) == 0 {
		return ""
	}

	return fmt.Sprintf("# Memory\n\n%s", strings.Join(parts, "\n\n---\n\n"))
}

// インターフェース準拠の静的チェック
var _ domainmemory.Store = (*FileStore)(nil)
