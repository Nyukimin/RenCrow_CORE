package patch

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// ParsePatch は JSON または Markdown 形式の patch を解析
func ParsePatch(patchStr string) ([]PatchCommand, error) {
	// JSON 形式判定（先頭が '['）
	trimmed := strings.TrimSpace(patchStr)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		return parseJSONPatch(patchStr)
	}

	// Markdown 形式判定（コードブロック検出）
	if strings.Contains(patchStr, "```go:") || strings.Contains(patchStr, "```bash") || strings.Contains(patchStr, "```git") {
		return parseMarkdownPatch(patchStr)
	}

	return nil, fmt.Errorf("unknown patch format: must be JSON array or Markdown code blocks")
}

// parseJSONPatch は JSON 配列形式の patch を解析
func parseJSONPatch(patchStr string) ([]PatchCommand, error) {
	var commands []PatchCommand

	if err := json.Unmarshal([]byte(patchStr), &commands); err != nil {
		return nil, fmt.Errorf("JSON parse error: %w", err)
	}

	// バリデーション
	for i, cmd := range commands {
		if cmd.Type == "" {
			return nil, fmt.Errorf("command[%d]: type is required", i)
		}
		if cmd.Action == "" {
			return nil, fmt.Errorf("command[%d]: action is required", i)
		}
		if cmd.Target == "" {
			return nil, fmt.Errorf("command[%d]: target is required", i)
		}
		if cmd.Type == TypeFileEdit && IsPlaceholderTarget(cmd.Target) {
			return nil, fmt.Errorf("command[%d]: placeholder target is not allowed: %s", i, cmd.Target)
		}
	}

	return commands, nil
}

// parseMarkdownPatch は Markdown コードブロック形式の patch を解析
func parseMarkdownPatch(patchStr string) ([]PatchCommand, error) {
	// 位置情報付きコマンド
	type positionedCommand struct {
		pos int
		cmd PatchCommand
	}
	var positioned []positionedCommand

	// 正規表現: ```言語:ファイルパス\n内容\n```
	// 例: ```go:src/main.go\npackage main\n```
	// (?s) フラグ: DOTALL モード（.が改行にもマッチ）
	reCodeBlock := regexp.MustCompile(`(?s)` + "```" + `([a-z]+):([^\n]+)\n(.*?)` + "```")
	matches := reCodeBlock.FindAllStringSubmatchIndex(patchStr, -1)

	for _, match := range matches {
		lang := patchStr[match[2]:match[3]]
		filePath := strings.TrimSpace(patchStr[match[4]:match[5]])
		content := patchStr[match[6]:match[7]]
		if IsPlaceholderTarget(filePath) {
			return nil, fmt.Errorf("placeholder target is not allowed: %s", filePath)
		}

		// ファイル編集コマンドに変換
		cmd := PatchCommand{
			Type:    TypeFileEdit,
			Action:  ActionUpdate, // デフォルトはupdate（既存ファイル上書き）
			Target:  filePath,
			Content: content,
			Metadata: map[string]string{
				"language": lang,
				"format":   "markdown",
			},
		}
		positioned = append(positioned, positionedCommand{pos: match[0], cmd: cmd})
	}

	// 正規表現: ```bash\nコマンド\n```
	// 例: ```bash\ngo test ./...\n```
	// (?s) フラグ: DOTALL モード（複数行コマンド対応）
	reShellBlock := regexp.MustCompile(`(?s)` + "```bash\n(.*?)" + "```")
	shellMatches := reShellBlock.FindAllStringSubmatchIndex(patchStr, -1)

	for _, match := range shellMatches {
		command := strings.TrimSpace(patchStr[match[2]:match[3]])

		cmd := PatchCommand{
			Type:   TypeShellCommand,
			Action: ActionRun,
			Target: command,
		}
		positioned = append(positioned, positionedCommand{pos: match[0], cmd: cmd})
	}

	// 正規表現: ```git\nコマンド\n```
	// 例: ```git\nadd .\ncommit -m "message"\n```
	reGitBlock := regexp.MustCompile(`(?s)` + "```git\n(.*?)" + "```")
	gitMatches := reGitBlock.FindAllStringSubmatchIndex(patchStr, -1)

	for _, match := range gitMatches {
		gitCommand := strings.TrimSpace(patchStr[match[2]:match[3]])

		cmd := PatchCommand{
			Type:   TypeGitOperation,
			Action: ActionRun, // Git操作は詳細パースが必要
			Target: gitCommand,
		}
		positioned = append(positioned, positionedCommand{pos: match[0], cmd: cmd})
	}

	if len(positioned) == 0 {
		return nil, fmt.Errorf("no valid code blocks found in Markdown patch")
	}

	// 出現順にソート
	sort.Slice(positioned, func(i, j int) bool {
		return positioned[i].pos < positioned[j].pos
	})

	// コマンドリストに変換
	commands := make([]PatchCommand, len(positioned))
	for i, p := range positioned {
		commands[i] = p.cmd
	}

	return commands, nil
}
