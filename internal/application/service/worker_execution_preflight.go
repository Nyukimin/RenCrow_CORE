package service

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/patch"
)

var blockedSelfLifecycleCommandPatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{
		name: "picoclaw.service lifecycle change",
		re:   regexp.MustCompile(`(?i)\b(systemctl|service)\b[^\n;&|]*(restart|start|stop|reload|enable|disable)[^\n;&|]*\bpicoclaw(?:\.service)?\b`),
	},
	{
		name: "picoclaw.service lifecycle change",
		re:   regexp.MustCompile(`(?i)\bservice\b[^\n;&|]*\bpicoclaw(?:\.service)?\b[^\n;&|]*(restart|start|stop|reload|enable|disable)\b`),
	},
	{
		name: "picoclaw process kill",
		re:   regexp.MustCompile(`(?i)\b(pkill|killall)\b[^\n;&|]*\bpicoclaw\b`),
	},
	{
		name: "RenCrow live binary install",
		re:   regexp.MustCompile(`(?i)(\bmake\s+install\b|\.local/bin/picoclaw|~/.local/bin/picoclaw)`),
	},
}

func (w *workerExecutionService) validateCommandsBeforeExecution(commands []patch.PatchCommand) error {
	for index, cmd := range commands {
		switch cmd.Type {
		case patch.TypeShellCommand:
			if reason := blockedSelfLifecycleCommandReason(cmd.Target); reason != "" {
				return fmt.Errorf("approval required: command %d modifies RenCrow runtime lifecycle or live binary (%s): %s", index+1, reason, cmd.Target)
			}
		case patch.TypeFileEdit:
			if err := w.validateFileEditBeforeExecution(index, cmd); err != nil {
				return err
			}
		}
	}
	return nil
}

func (w *workerExecutionService) normalizeParsedCommands(commands []patch.PatchCommand) []patch.PatchCommand {
	normalized := make([]patch.PatchCommand, len(commands))
	copy(normalized, commands)
	for index, cmd := range normalized {
		if cmd.Type != patch.TypeFileEdit || cmd.Action != patch.ActionUpdate || !isMarkdownFileBlock(cmd) {
			continue
		}
		absTarget, err := w.absoluteWorkspaceTarget(cmd.Target)
		if err != nil {
			continue
		}
		if _, err := os.Stat(absTarget); os.IsNotExist(err) {
			cmd.Action = patch.ActionCreate
			normalized[index] = cmd
		}
	}
	return normalized
}

func isMarkdownFileBlock(cmd patch.PatchCommand) bool {
	format, ok := cmd.GetMetadata("format")
	return ok && format == "markdown"
}

func (w *workerExecutionService) validateFileEditBeforeExecution(index int, cmd patch.PatchCommand) error {
	if patch.IsPlaceholderTarget(cmd.Target) {
		return fmt.Errorf("invalid file edit command %d: placeholder target is not allowed: %s", index+1, cmd.Target)
	}
	absTarget, err := w.absoluteWorkspaceTarget(cmd.Target)
	if err != nil {
		return fmt.Errorf("invalid file edit command %d: %w", index+1, err)
	}
	switch cmd.Action {
	case patch.ActionCreate:
		if isGoFileTarget(absTarget) && isWorkspaceRootFile(w.config.Workspace, absTarget) {
			return fmt.Errorf("invalid file edit command %d: creating a Go file at workspace root requires an existing concrete target: %s", index+1, cmd.Target)
		}
	case patch.ActionUpdate:
		info, err := os.Stat(absTarget)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("invalid file edit command %d: update target does not exist: %s", index+1, cmd.Target)
			}
			return fmt.Errorf("invalid file edit command %d: stat update target: %w", index+1, err)
		}
		if info.IsDir() {
			return fmt.Errorf("invalid file edit command %d: update target is a directory: %s", index+1, cmd.Target)
		}
	}
	if isGoFileTarget(absTarget) && writesFileContent(cmd.Action) && !strings.HasPrefix(strings.TrimSpace(cmd.Content), "package ") {
		return fmt.Errorf("invalid file edit command %d: Go file content must start with package declaration: %s", index+1, cmd.Target)
	}
	return nil
}

func (w *workerExecutionService) absoluteWorkspaceTarget(target string) (string, error) {
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("invalid file path: %w", err)
	}
	absWorkspace, err := filepath.Abs(w.config.Workspace)
	if err != nil {
		return "", fmt.Errorf("invalid workspace path: %w", err)
	}
	rel, err := filepath.Rel(absWorkspace, absTarget)
	if err != nil {
		return "", fmt.Errorf("file path outside workspace: %s", target)
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("file path outside workspace: %s", target)
	}
	return absTarget, nil
}

func isGoFileTarget(target string) bool {
	return strings.EqualFold(filepath.Ext(target), ".go")
}

func isWorkspaceRootFile(workspace, target string) bool {
	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return false
	}
	return filepath.Dir(target) == absWorkspace
}

func writesFileContent(action patch.Action) bool {
	return action == patch.ActionCreate || action == patch.ActionUpdate || action == patch.ActionAppend
}

func blockedSelfLifecycleCommandReason(command string) string {
	normalized := strings.TrimSpace(command)
	if normalized == "" {
		return ""
	}
	for _, pattern := range blockedSelfLifecycleCommandPatterns {
		if pattern.re.MatchString(normalized) {
			return pattern.name
		}
	}
	return ""
}
