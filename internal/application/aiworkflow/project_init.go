package aiworkflow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
)

type ProjectInitStore interface {
	SaveWorkflowEvent(ctx context.Context, item domainai.WorkflowEvent) error
	SaveProjectMemoryIndex(ctx context.Context, item domainai.ProjectMemoryIndex) error
}

type ProjectInitOptions struct {
	RepoRoot          string
	ProjectMemoryRoot string
	RepoName          string
	Now               func() time.Time
}

type ProjectInitResult struct {
	RepoRoot       string                        `json:"repo_root"`
	RepoName       string                        `json:"repo_name"`
	GeneratedFiles []string                      `json:"generated_files"`
	MemoryIndexes  []domainai.ProjectMemoryIndex `json:"memory_indexes"`
	WorkflowEvent  domainai.WorkflowEvent        `json:"workflow_event"`
}

type ProjectScanner struct {
	store ProjectInitStore
}

func NewProjectScanner(store ProjectInitStore) *ProjectScanner {
	return &ProjectScanner{store: store}
}

func (s *ProjectScanner) Run(ctx context.Context, opts ProjectInitOptions) (ProjectInitResult, error) {
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	repoRoot := strings.TrimSpace(opts.RepoRoot)
	if repoRoot == "" {
		repoRoot = "."
	}
	absRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return ProjectInitResult{}, err
	}
	if err := rejectProjectInitUnsafeRoot(absRoot); err != nil {
		return ProjectInitResult{}, err
	}
	info, err := os.Stat(absRoot)
	if err != nil {
		return ProjectInitResult{}, err
	}
	if !info.IsDir() {
		return ProjectInitResult{}, fmt.Errorf("repo_root must be a directory")
	}
	repoName := strings.TrimSpace(opts.RepoName)
	if repoName == "" {
		repoName = filepath.Base(absRoot)
	}
	memoryRoot := strings.TrimSpace(opts.ProjectMemoryRoot)
	if memoryRoot == "" {
		memoryRoot = ".ai"
	}
	if filepath.IsAbs(memoryRoot) || strings.Contains(memoryRoot, "..") {
		return ProjectInitResult{}, fmt.Errorf("project_memory_root must be a relative path inside repo")
	}
	outputRoot := filepath.Join(absRoot, memoryRoot)
	profile, err := buildProjectProfile(absRoot, repoName)
	if err != nil {
		return ProjectInitResult{}, err
	}
	files := map[string]string{
		"project_profile.md":    profile,
		"source_map.md":         buildSourceMap(absRoot),
		"test_commands.md":      buildCommandDoc(absRoot, "test"),
		"build_commands.md":     buildCommandDoc(absRoot, "build"),
		"coding_conventions.md": buildCodingConventions(absRoot),
		"risk_notes.md":         buildRiskNotes(absRoot),
	}
	generated := make([]string, 0, len(files))
	indexes := make([]domainai.ProjectMemoryIndex, 0, len(files))
	at := now().UTC()
	for name, body := range files {
		path := filepath.Join(outputRoot, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return ProjectInitResult{}, err
		}
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			return ProjectInitResult{}, err
		}
		rel, _ := filepath.Rel(absRoot, path)
		generated = append(generated, rel)
		idx := domainai.ProjectMemoryIndex{
			ID:         "project_init:" + rel,
			Repo:       repoName,
			FilePath:   rel,
			MemoryType: strings.TrimSuffix(name, filepath.Ext(name)),
			Title:      strings.TrimSuffix(name, ".md"),
			Summary:    "Project Init Pack generated file",
			UpdatedAt:  at,
		}
		indexes = append(indexes, idx)
		if s.store != nil {
			if err := s.store.SaveProjectMemoryIndex(ctx, idx); err != nil {
				return ProjectInitResult{}, err
			}
		}
	}
	sort.Strings(generated)
	sort.Slice(indexes, func(i, j int) bool { return indexes[i].FilePath < indexes[j].FilePath })
	event := domainai.WorkflowEvent{
		EventID:   "project_init:" + repoName + ":" + at.Format("20060102T150405Z"),
		EventType: "project_init_completed",
		Agent:     "Worker",
		Repo:      repoName,
		Status:    "completed",
		CreatedAt: at,
		Summary:   fmt.Sprintf("generated %d project init files", len(generated)),
	}
	if s.store != nil {
		if err := s.store.SaveWorkflowEvent(ctx, event); err != nil {
			return ProjectInitResult{}, err
		}
	}
	return ProjectInitResult{
		RepoRoot:       absRoot,
		RepoName:       repoName,
		GeneratedFiles: generated,
		MemoryIndexes:  indexes,
		WorkflowEvent:  event,
	}, nil
}

func rejectProjectInitUnsafeRoot(absRoot string) error {
	clean := filepath.Clean(absRoot)
	switch clean {
	case "/", "/home", "/tmp", "/var", "/etc", "/usr", "/System", "/Applications":
		return fmt.Errorf("refusing to project-init broad or system root: %s", clean)
	}
	if strings.HasSuffix(clean, string(filepath.Separator)+".git") {
		return fmt.Errorf("repo_root must not be .git")
	}
	return nil
}

func buildProjectProfile(root, repoName string) (string, error) {
	files, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, f.Name())
	}
	sort.Strings(names)
	return fmt.Sprintf(`# Project Profile

## Overview
Repository: %s

## Detected Files
%s

## Languages
%s
`, repoName, markdownList(names), detectLanguages(root)), nil
}

func buildSourceMap(root string) string {
	dirs := []string{"cmd", "internal", "pkg", "docs", "rules", "config", "test", "tests", "scripts"}
	var present []string
	for _, dir := range dirs {
		if info, err := os.Stat(filepath.Join(root, dir)); err == nil && info.IsDir() {
			present = append(present, dir+"/")
		}
	}
	return "# Source Map\n\n" + markdownList(present)
}

func buildCommandDoc(root, kind string) string {
	var commands []string
	if fileExists(filepath.Join(root, "go.mod")) {
		if kind == "test" {
			commands = append(commands, "`GOCACHE=/tmp/rencrow-gocache go test ./...`")
		} else {
			commands = append(commands, "`go build ./...`")
		}
	}
	if fileExists(filepath.Join(root, "package.json")) {
		if kind == "test" {
			commands = append(commands, "`npm test`")
		} else {
			commands = append(commands, "`npm run build`")
		}
	}
	if len(commands) == 0 {
		commands = append(commands, "未検出")
	}
	title := "Build Commands"
	if kind == "test" {
		title = "Test Commands"
	}
	return "# " + title + "\n\n" + markdownList(commands)
}

func buildCodingConventions(root string) string {
	var notes []string
	if fileExists(filepath.Join(root, "AGENTS.md")) {
		notes = append(notes, "`AGENTS.md` を作業ルールの入口とする。")
	}
	if fileExists(filepath.Join(root, "CLAUDE.md")) {
		notes = append(notes, "`CLAUDE.md` をプロジェクト概要の補助参照とする。")
	}
	if len(notes) == 0 {
		notes = append(notes, "プロジェクト固有の規約ファイルは未検出。")
	}
	return "# Coding Conventions\n\n" + markdownList(notes)
}

func buildRiskNotes(root string) string {
	risks := []string{"`.env`、秘密鍵、認証情報は読み取らない。"}
	if fileExists(filepath.Join(root, "go.mod")) {
		risks = append(risks, "Go 依存関係変更は明示承認後に行う。")
	}
	if fileExists(filepath.Join(root, "package.json")) {
		risks = append(risks, "Node 依存関係 install / update は明示承認後に行う。")
	}
	return "# Risk Notes\n\n" + markdownList(risks)
}

func detectLanguages(root string) string {
	var langs []string
	if fileExists(filepath.Join(root, "go.mod")) {
		langs = append(langs, "Go")
	}
	if fileExists(filepath.Join(root, "package.json")) {
		langs = append(langs, "JavaScript / TypeScript")
	}
	if fileExists(filepath.Join(root, "pyproject.toml")) || fileExists(filepath.Join(root, "requirements.txt")) {
		langs = append(langs, "Python")
	}
	if len(langs) == 0 {
		langs = append(langs, "未検出")
	}
	return markdownList(langs)
}

func markdownList(items []string) string {
	if len(items) == 0 {
		return "- なし\n"
	}
	var b strings.Builder
	for _, item := range items {
		b.WriteString("- ")
		b.WriteString(item)
		b.WriteByte('\n')
	}
	return b.String()
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
