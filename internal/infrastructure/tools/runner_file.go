package tools

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/tool"
)

// executeFileRead はファイルを読み込む（limit + offset 行制限対応）
func (r *ToolRunner) executeFileRead(ctx context.Context, args map[string]interface{}) (string, error) {
	path, ok := args["path"].(string)
	if !ok {
		return "", fmt.Errorf("'path' argument is required and must be a string")
	}

	_, hasLimit := args["limit"]
	if !hasLimit {
		// ページング指定なし: ファイル全体を読む
		content, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("failed to read file: %w", err)
		}
		return string(content), nil
	}

	// ページング: 行単位でストリーム読み込みし、対象範囲だけ取得する
	limit := intArg(args, "limit", 100)
	offset := intArg(args, "offset", 0)
	if limit > 10000 {
		limit = 10000
	}
	if offset < 0 {
		offset = 0
	}

	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}
	defer f.Close()

	const maxLineBytes = 16 * 1024 * 1024 // 16 MiB: minified JSON や長いログ行に対応
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, bufio.MaxScanTokenSize), maxLineBytes)
	var collected []string
	lineNum := 0
	for scanner.Scan() {
		if lineNum >= offset {
			if len(collected) >= limit {
				break
			}
			collected = append(collected, scanner.Text())
		}
		lineNum++
	}
	if err := scanner.Err(); err != nil {
		if err == bufio.ErrTooLong {
			return "", fmt.Errorf("failed to read file: line exceeds %d MiB limit", maxLineBytes/(1024*1024))
		}
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	result := strings.Join(collected, "\n")
	// 次の行が存在する場合のみフッターを出力する
	if len(collected) >= limit {
		hasMore := scanner.Scan()
		if err := scanner.Err(); err != nil {
			if err == bufio.ErrTooLong {
				return "", fmt.Errorf("failed to read file: line exceeds %d MiB limit", maxLineBytes/(1024*1024))
			}
			return "", fmt.Errorf("failed to read file: %w", err)
		}
		if hasMore {
			result += fmt.Sprintf("\n--- showing lines %d-%d (limit reached) ---", offset+1, offset+limit)
		}
	}
	return result, nil
}

// executeFileWrite はファイルに書き込む（mode=plan で dry-run 対応）
func (r *ToolRunner) executeFileWrite(ctx context.Context, args map[string]interface{}) (string, error) {
	path, ok := args["path"].(string)
	if !ok {
		return "", fmt.Errorf("'path' argument is required and must be a string")
	}
	if !r.isFileWritePathAllowed(path) {
		return "", &tool.ToolError{
			Code:    tool.ErrPermissionDenied,
			Message: "file path not in allowed write list",
			Details: map[string]any{"path": path},
		}
	}

	content, ok := args["content"].(string)
	if !ok {
		return "", fmt.Errorf("'content' argument is required and must be a string")
	}

	// dry-run モード: ファイル存在チェック + プレビューのみ
	mode, _ := args["mode"].(string)
	if mode == "plan" {
		return r.fileWriteDryRun(path, content), nil
	}

	// ディレクトリが存在しない場合は作成
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create directory: %w", err)
	}

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("failed to write file: %w", err)
	}

	return fmt.Sprintf("Successfully wrote %d bytes to %s", len(content), path), nil
}

// fileWriteDryRun はファイル書き込みの dry-run を実行
func (r *ToolRunner) fileWriteDryRun(path, content string) string {
	var result strings.Builder
	result.WriteString("[DRY-RUN] file_write\n")
	result.WriteString(fmt.Sprintf("path: %s\n", path))
	result.WriteString(fmt.Sprintf("content_size: %d bytes\n", len(content)))

	if info, err := os.Stat(path); err == nil {
		result.WriteString(fmt.Sprintf("exists: true (current size: %d bytes)\n", info.Size()))
		result.WriteString("action: overwrite\n")
	} else {
		result.WriteString("exists: false\n")
		result.WriteString("action: create\n")
	}

	// プレビュー（最大5行）
	lines := strings.SplitN(content, "\n", 6)
	if len(lines) > 5 {
		lines = lines[:5]
		result.WriteString("preview (first 5 lines):\n")
	} else {
		result.WriteString("preview:\n")
	}
	for _, line := range lines {
		if len(line) > 120 {
			line = line[:120] + "..."
		}
		result.WriteString("  " + line + "\n")
	}

	return result.String()
}

// isFileWritePathAllowed は file_write の対象パスが許可されているか判定する
func (r *ToolRunner) isFileWritePathAllowed(path string) bool {
	if len(r.config.AllowedWritePaths) == 0 {
		return true // 制限なし
	}

	targetAbs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return false
	}

	for _, allowed := range r.config.AllowedWritePaths {
		allowedAbs, err := filepath.Abs(filepath.Clean(allowed))
		if err != nil {
			continue
		}
		if targetAbs == allowedAbs {
			return true
		}
	}
	return false
}

// executeFileList はディレクトリ内のファイル一覧を取得（limit + offset 対応）
func (r *ToolRunner) executeFileList(ctx context.Context, args map[string]interface{}) (string, error) {
	path, ok := args["path"].(string)
	if !ok {
		return "", fmt.Errorf("'path' argument is required and must be a string")
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return "", fmt.Errorf("failed to read directory: %w", err)
	}

	// ページングパラメータ（デフォルト: limit=100, offset=0）
	limit := intArg(args, "limit", 100)
	offset := intArg(args, "offset", 0)

	// 上限制約
	if limit > 1000 {
		limit = 1000
	}
	if limit < 1 {
		limit = 1
	}
	if offset < 0 {
		offset = 0
	}

	total := len(entries)
	start := offset
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}

	var result strings.Builder
	for _, entry := range entries[start:end] {
		if entry.IsDir() {
			fmt.Fprintf(&result, "%s/\n", entry.Name())
		} else {
			fmt.Fprintf(&result, "%s\n", entry.Name())
		}
	}

	// ページング情報
	if end < total {
		fmt.Fprintf(&result, "--- showing %d-%d of %d (next offset: %d) ---\n", start+1, end, total, end)
	}

	return result.String(), nil
}

// intArg は args から int 値を取得する（float64 からの変換対応、JSON 由来）
func intArg(args map[string]interface{}, key string, defaultVal int) int {
	v, ok := args[key]
	if !ok {
		return defaultVal
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return defaultVal
	}
}
