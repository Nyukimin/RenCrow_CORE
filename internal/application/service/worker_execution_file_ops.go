package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/patch"
)

// executeFileEdit はファイル編集コマンドを実行
func (w *workerExecutionService) executeFileEdit(
	ctx context.Context,
	cmd patch.PatchCommand,
) (string, error) {
	_ = ctx
	target := cmd.Target

	// ワークスペース外書き込み禁止
	absTarget, err := w.absoluteWorkspaceTarget(target)
	if err != nil {
		return "", fmt.Errorf("security error: %w", err)
	}
	target = absTarget

	// 保護ファイルチェック
	if w.isProtectedFile(target) {
		switch w.config.ActionOnProtected {
		case "error":
			return "", fmt.Errorf("protected file: %s", target)
		case "skip":
			return "Skipped (protected file)", nil
		case "log":
			fmt.Printf("[Worker] Warning: accessing protected file: %s\n", target)
		}
	}

	// Action別処理
	switch cmd.Action {
	case patch.ActionCreate, patch.ActionUpdate:
		return w.writeFile(target, cmd.Content)
	case patch.ActionDelete:
		return w.deleteFile(target)
	case patch.ActionAppend:
		return w.appendFile(target, cmd.Content)
	case patch.ActionMkdir:
		return w.createDirectory(target)
	case patch.ActionRename:
		newName := cmd.Metadata["new_name"]
		if newName == "" {
			return "", fmt.Errorf("rename: metadata 'new_name' is required")
		}
		return w.renameFile(target, newName)
	case patch.ActionCopy:
		dest := cmd.Metadata["destination"]
		if dest == "" {
			return "", fmt.Errorf("copy: metadata 'destination' is required")
		}
		return w.copyFile(target, dest)
	default:
		return "", fmt.Errorf("unknown file_edit action: %s", cmd.Action)
	}
}

// writeFile はファイル書き込み
func (w *workerExecutionService) writeFile(path, content string) (string, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create directory failed: %w", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("write file failed: %w", err)
	}
	return fmt.Sprintf("File written: %s (%d bytes)", path, len(content)), nil
}

// deleteFile はファイル削除
func (w *workerExecutionService) deleteFile(path string) (string, error) {
	if err := os.Remove(path); err != nil {
		return "", fmt.Errorf("delete file failed: %w", err)
	}
	return fmt.Sprintf("File deleted: %s", path), nil
}

// appendFile はファイル末尾追記
func (w *workerExecutionService) appendFile(path, content string) (string, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return "", fmt.Errorf("open file failed: %w", err)
	}
	defer f.Close()

	if _, err := f.WriteString(content); err != nil {
		return "", fmt.Errorf("append file failed: %w", err)
	}
	return fmt.Sprintf("Content appended: %s (%d bytes)", path, len(content)), nil
}

// createDirectory はディレクトリ作成
func (w *workerExecutionService) createDirectory(path string) (string, error) {
	if err := os.MkdirAll(path, 0755); err != nil {
		return "", fmt.Errorf("create directory failed: %w", err)
	}
	return fmt.Sprintf("Directory created: %s", path), nil
}

// renameFile はファイル/ディレクトリリネーム
func (w *workerExecutionService) renameFile(oldPath, newPath string) (string, error) {
	if err := os.Rename(oldPath, newPath); err != nil {
		return "", fmt.Errorf("rename failed: %w", err)
	}
	return fmt.Sprintf("Renamed: %s -> %s", oldPath, newPath), nil
}

// copyFile はファイル/ディレクトリコピー
func (w *workerExecutionService) copyFile(src, dest string) (string, error) {
	data, err := os.ReadFile(src)
	if err != nil {
		return "", fmt.Errorf("read source file failed: %w", err)
	}

	if err := os.WriteFile(dest, data, 0644); err != nil {
		return "", fmt.Errorf("write destination file failed: %w", err)
	}

	return fmt.Sprintf("Copied: %s -> %s (%d bytes)", src, dest, len(data)), nil
}

// isProtectedFile は保護ファイルかチェック
func (w *workerExecutionService) isProtectedFile(path string) bool {
	basename := filepath.Base(path)
	for _, pattern := range w.config.ProtectedPatterns {
		matched, _ := filepath.Match(pattern, basename)
		if matched {
			return true
		}
	}
	return false
}
