package context

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SkillsLoader は skills/ ディレクトリからスキルメタデータを読み込む
type SkillsLoader struct {
	skillsDir string
}

// NewSkillsLoader は新しい SkillsLoader を作成
func NewSkillsLoader(skillsDir string) *SkillsLoader {
	return &SkillsLoader{skillsDir: skillsDir}
}

// LoadAll はすべてのスキルメタデータを読み込む
func (l *SkillsLoader) LoadAll() ([]SkillMetadata, error) {
	return l.LoadAllFromDirs(l.skillsDir)
}

// LoadAllFromDirs は複数の skills/ ディレクトリを優先順に読み込む。
// 同名 skill は先に渡したディレクトリのものを採用する。
func (l *SkillsLoader) LoadAllFromDirs(skillDirs ...string) ([]SkillMetadata, error) {
	var skills []SkillMetadata
	seen := make(map[string]bool)
	for _, skillsDir := range skillDirs {
		if strings.TrimSpace(skillsDir) == "" {
			continue
		}
		// WalkDir visits directory entries in lexical order. Keeping the outer
		// loop ordered preserves configured root priority for duplicate names.
		err := filepath.WalkDir(skillsDir, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				// A broken root or file must not hide valid skills from later roots.
				return nil
			}
			if entry == nil || entry.IsDir() || entry.Name() != "SKILL.md" {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil || strings.TrimSpace(string(data)) == "" {
				return nil
			}

			dirName := filepath.Base(filepath.Dir(path))
			meta := parseSkillFile(string(data), dirName)
			if strings.TrimSpace(meta.Name) == "" || strings.TrimSpace(meta.BodyText) == "" {
				return nil
			}
			key := strings.ToLower(strings.TrimSpace(meta.Name))
			if seen[key] {
				return nil
			}
			meta.CanExecute = false
			seen[key] = true
			skills = append(skills, meta)
			return nil
		})
		if err != nil {
			// WalkDir errors are intentionally isolated to this configured root.
			continue
		}
	}
	return skills, nil
}

// FormatSummary はスキル一覧を人間可読なテキストに変換する
func (l *SkillsLoader) FormatSummary(skills []SkillMetadata) string {
	var lines []string
	for _, s := range skills {
		if s.Description != "" {
			lines = append(lines, fmt.Sprintf("- %s: %s", s.Name, s.Description))
		} else {
			lines = append(lines, fmt.Sprintf("- %s", s.Name))
		}
	}
	return strings.Join(lines, "\n")
}

// parseSkillFile は SKILL.md の内容をパースする
// YAML frontmatter（---区切り）のキーバリューを解析
func parseSkillFile(content string, dirName string) SkillMetadata {
	meta := SkillMetadata{DirName: dirName}

	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "---") {
		// frontmatter なし: 先頭行をタイトルとして使用
		firstLine, body, _ := strings.Cut(content, "\n")
		meta.Name = dirName
		meta.Description = strings.TrimPrefix(strings.TrimSpace(firstLine), "# ")
		meta.BodyText = strings.TrimSpace(body)
		if meta.BodyText == "" {
			// 旧形式の1行Skillでは見出し自体が唯一の指示本文。
			meta.BodyText = strings.TrimSpace(firstLine)
		}
		return meta
	}

	// frontmatter を抽出
	rest := content[3:] // "---" を除去
	endIdx := strings.Index(rest, "\n---")
	if endIdx < 0 {
		return meta
	}

	frontmatter := rest[:endIdx]
	meta.BodyText = strings.TrimSpace(rest[endIdx+4:]) // "---\n" を除去

	// key: value 行を解析（YAML list も簡易サポート）
	lines := strings.Split(frontmatter, "\n")
	var currentListKey string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			currentListKey = ""
			continue
		}

		// YAML list item: "  - item"
		if currentListKey != "" && strings.HasPrefix(trimmed, "- ") {
			item := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
			item = strings.Trim(item, "\"'")
			if currentListKey == "invariants" {
				meta.Invariants = append(meta.Invariants, item)
			}
			continue
		}

		key, value, found := strings.Cut(trimmed, ":")
		if !found {
			currentListKey = ""
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		// クォートを除去
		value = strings.Trim(value, "\"'")

		switch key {
		case "name":
			meta.Name = value
		case "description":
			meta.Description = value
		case "tool_id":
			meta.ToolID = value
		case "version":
			meta.Version = value
		case "category":
			meta.Category = value
		case "dry_run":
			meta.DryRun = value == "true"
		case "deprecated":
			meta.Deprecated = value == "true"
		case "invariants":
			// 次の行が "- " で始まるリストアイテム
			currentListKey = "invariants"
		default:
			currentListKey = ""
		}
	}

	if meta.Name == "" {
		meta.Name = dirName
	}

	return meta
}
