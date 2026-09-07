package aiworkflow

import (
	"errors"
	"fmt"
	"strings"
)

func ValidateProjectMemoryIndex(item ProjectMemoryIndex) error {
	if err := item.MemoryID.Validate(); err != nil {
		return fmt.Errorf("memory_id: %w", err)
	}
	if strings.TrimSpace(item.Repo) == "" {
		return errors.New("repo is required")
	}
	if strings.TrimSpace(item.FilePath) == "" {
		return errors.New("file_path is required")
	}
	if strings.TrimSpace(item.MemoryType) == "" {
		return errors.New("memory_type is required")
	}
	if item.UpdatedAt.IsZero() {
		return errors.New("updated_at is required")
	}
	return nil
}

func ValidateWorktreeRegistry(item WorktreeRegistry) error {
	if strings.TrimSpace(item.WorktreeID) == "" {
		return errors.New("worktree_id is required")
	}
	if strings.TrimSpace(item.Repo) == "" {
		return errors.New("repo is required")
	}
	if strings.TrimSpace(item.Path) == "" {
		return errors.New("path is required")
	}
	if strings.TrimSpace(item.Branch) == "" {
		return errors.New("branch is required")
	}
	if strings.TrimSpace(item.Status) == "" {
		return errors.New("status is required")
	}
	if item.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	return nil
}

func ValidateCommandRegistry(item CommandRegistry) error {
	if strings.TrimSpace(item.CommandName) == "" {
		return errors.New("command_name is required")
	}
	if strings.TrimSpace(item.FilePath) == "" {
		return errors.New("file_path is required")
	}
	if item.UpdatedAt.IsZero() {
		return errors.New("updated_at is required")
	}
	return nil
}

func ValidateContextUsage(item ContextUsage) error {
	if strings.TrimSpace(item.EventID) == "" {
		return errors.New("event_id is required")
	}
	if strings.TrimSpace(item.Agent) == "" {
		return errors.New("agent is required")
	}
	if item.InputTokens < 0 || item.OutputTokens < 0 || item.ContextTokens < 0 ||
		item.ToolCallCount < 0 || item.DCICallCount < 0 || item.RepairCount < 0 || item.LatencyMS < 0 {
		return errors.New("counts must be >= 0")
	}
	if item.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	taskSet := !item.TaskID.IsZero()
	runSet := item.RunID != ""
	if taskSet != runSet {
		return errors.New("task_id and run_id must both be set when either is set")
	}
	if taskSet {
		if err := item.TaskID.Validate(); err != nil {
			return err
		}
		if err := item.RunID.Validate(); err != nil {
			return err
		}
	}
	return nil
}
