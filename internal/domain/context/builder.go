package context

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/memory"
)

// BootstrapFile は workspace/ から読み込むファイル定義
type BootstrapFile struct {
	Filename string
	Label    string
	ChatOnly bool // true の場合 CHAT ルートのみ読み込み
}

// DefaultBootstrapFiles はデフォルトの読み込みファイル群（読み込み順序を保持）
var DefaultBootstrapFiles = []BootstrapFile{
	// Chat専用（ペルソナ優先）
	{Filename: "persona/mio.md", Label: "CHAT_PERSONA", ChatOnly: true},
	{Filename: "SOUL.md", Label: "SOUL", ChatOnly: true},
	{Filename: "PrimerMessage.md", Label: "PrimerMessage", ChatOnly: true},
	// 共通（全ルート）
	{Filename: "AGENT.md", Label: "AGENT"},
	{Filename: "IDENTITY.md", Label: "IDENTITY"},
	{Filename: "USER.md", Label: "USER"},
}

// Builder は workspace/ のコンテキストを統合的に組み立てる
type Builder struct {
	workspaceDir string
	memoryStore  memory.Store // nilを許容
	skillDirs    []string
}

// NewBuilder は新しい Builder を作成する
func NewBuilder(workspaceDir string) *Builder {
	return &Builder{
		workspaceDir: workspaceDir,
	}
}

// WithMemoryStore はメモリストアを設定する（オプション）
func (b *Builder) WithMemoryStore(store memory.Store) *Builder {
	b.memoryStore = store
	return b
}

// WithSkillDirs は skill context を読み込むディレクトリを優先順に設定する。
func (b *Builder) WithSkillDirs(skillDirs ...string) *Builder {
	b.skillDirs = append([]string{}, skillDirs...)
	return b
}

// BuildContext はルートに応じた完全なコンテキストを組み立てる
func (b *Builder) BuildContext(route string) string {
	isChat := strings.EqualFold(strings.TrimSpace(route), "CHAT")

	var parts []string

	// Bootstrap files（ルートに応じたフィルタ）
	for _, bf := range DefaultBootstrapFiles {
		if bf.ChatOnly && !isChat {
			continue
		}
		content := b.readFile(bf.Filename)
		if content != "" {
			parts = append(parts, fmt.Sprintf("# %s\n%s", bf.Label, content))
		}
	}

	// メモリコンテキスト
	if b.memoryStore != nil {
		if memCtx := b.memoryStore.GetMemoryContext(); memCtx != "" {
			parts = append(parts, memCtx)
		}
	}

	// スキル概要
	if skills := b.BuildSkillsSummary(); skills != "" {
		parts = append(parts, fmt.Sprintf("# SKILLS\n%s", skills))
	}

	// FewShot（先頭1件のみ、コンテキスト節約）
	if fs := b.readFile("FewShot_01.md"); fs != "" {
		parts = append(parts, fmt.Sprintf("# FewShot Example\n%s", fs))
	}

	return strings.Join(parts, "\n\n---\n\n")
}

// BuildSkillsSummary は skills/ 配下の SKILL.md から概要一覧を生成する
func (b *Builder) BuildSkillsSummary() string {
	skillDirs := b.skillDirs
	if len(skillDirs) == 0 {
		skillDirs = []string{filepath.Join(b.workspaceDir, "skills")}
	}
	loader := NewSkillsLoader(skillDirs[0])
	skills, err := loader.LoadAllFromDirs(skillDirs...)
	if err != nil || len(skills) == 0 {
		return ""
	}
	return loader.FormatSummary(skills)
}

// BuildMessageWithTask はコンテキスト + タスクを1つのメッセージに組み立てる
func (b *Builder) BuildMessageWithTask(route string, taskLabel string, taskContent string) string {
	ctx := b.BuildContext(route)
	if ctx != "" {
		return ctx + "\n\n===\n\n# " + taskLabel + "\n" + taskContent
	}
	return taskContent
}

// readFile は workspace/ 配下のファイルを読み込む
func (b *Builder) readFile(relPath string) string {
	path := filepath.Join(b.workspaceDir, relPath)
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
