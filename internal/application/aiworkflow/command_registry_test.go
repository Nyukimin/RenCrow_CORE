package aiworkflow

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
)

type memoryCommandRegistryStore struct {
	commands []domainai.CommandRegistry
}

func (s *memoryCommandRegistryStore) SaveCommandRegistry(_ context.Context, item domainai.CommandRegistry) error {
	s.commands = append(s.commands, item)
	return nil
}

func TestRegisterCommandFilesScansCommandsMarkdown(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "commands"), 0755); err != nil {
		t.Fatal(err)
	}
	body := `# /review-architecture

## Purpose
設計変更が既存アーキテクチャと矛盾しないか確認する。

## Agent
Coder

## Required Skill
core.architecture-review
`
	if err := os.WriteFile(filepath.Join(root, "commands", "review-architecture.md"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	store := &memoryCommandRegistryStore{}
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	commands, err := RegisterCommandFiles(context.Background(), store, CommandRegistryScanOptions{
		RepoRoot: root,
		Now:      func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("RegisterCommandFiles failed: %v", err)
	}
	if len(commands) != 1 || len(store.commands) != 1 {
		t.Fatalf("expected one command, got commands=%+v store=%+v", commands, store.commands)
	}
	got := commands[0]
	if got.CommandName != "/review-architecture" ||
		got.FilePath != "commands/review-architecture.md" ||
		got.DefaultAgent != "Coder" ||
		got.RequiredSkill != "core.architecture-review" ||
		got.Description == "" ||
		!got.UpdatedAt.Equal(now) {
		t.Fatalf("unexpected command registry: %+v", got)
	}
}

func TestRegisterCommandFilesMissingCommandsDirIsEmpty(t *testing.T) {
	store := &memoryCommandRegistryStore{}
	commands, err := RegisterCommandFiles(context.Background(), store, CommandRegistryScanOptions{RepoRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("RegisterCommandFiles failed: %v", err)
	}
	if len(commands) != 0 || len(store.commands) != 0 {
		t.Fatalf("expected no commands, got commands=%+v store=%+v", commands, store.commands)
	}
}
