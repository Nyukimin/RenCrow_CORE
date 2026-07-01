package aiworkflow

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	domainai "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/aiworkflow"
)

type CommandRegistryStore interface {
	SaveCommandRegistry(ctx context.Context, item domainai.CommandRegistry) error
}

type CommandRegistryScanOptions struct {
	RepoRoot string
	Now      func() time.Time
}

func RegisterCommandFiles(ctx context.Context, store CommandRegistryStore, opts CommandRegistryScanOptions) ([]domainai.CommandRegistry, error) {
	if store == nil {
		return nil, fmt.Errorf("command registry store is required")
	}
	repoRoot := strings.TrimSpace(opts.RepoRoot)
	if repoRoot == "" {
		repoRoot = "."
	}
	absRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil, err
	}
	commandsDir := filepath.Join(absRoot, "commands")
	entries, err := os.ReadDir(commandsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []domainai.CommandRegistry{}, nil
		}
		return nil, err
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	updatedAt := now().UTC()
	var commands []domainai.CommandRegistry
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		absPath := filepath.Join(commandsDir, entry.Name())
		relPath, err := filepath.Rel(absRoot, absPath)
		if err != nil {
			return nil, err
		}
		command, err := parseCommandFile(relPath, absPath, updatedAt)
		if err != nil {
			return nil, err
		}
		if err := store.SaveCommandRegistry(ctx, command); err != nil {
			return nil, err
		}
		commands = append(commands, command)
	}
	return commands, nil
}

func parseCommandFile(relPath, absPath string, updatedAt time.Time) (domainai.CommandRegistry, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return domainai.CommandRegistry{}, err
	}
	defer f.Close()
	item := domainai.CommandRegistry{
		CommandName:  "/" + strings.TrimSuffix(filepath.Base(relPath), filepath.Ext(relPath)),
		FilePath:     filepath.ToSlash(relPath),
		DefaultAgent: "Chat",
		UpdatedAt:    updatedAt,
	}
	section := ""
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "# ") && strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(line, "# ")), "/") {
			item.CommandName = strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "# ")))[0]
			continue
		}
		if strings.HasPrefix(line, "## ") {
			section = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, "## ")))
			continue
		}
		if line == "" || strings.HasPrefix(line, "-") || strings.HasPrefix(line, "```") {
			continue
		}
		switch section {
		case "purpose":
			if item.Description == "" {
				item.Description = line
			}
		case "agent":
			if item.DefaultAgent == "" || item.DefaultAgent == "Chat" {
				item.DefaultAgent = line
			}
		case "required skill", "required skills":
			if item.RequiredSkill == "" {
				item.RequiredSkill = line
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return domainai.CommandRegistry{}, err
	}
	if err := domainai.ValidateCommandRegistry(item); err != nil {
		return domainai.CommandRegistry{}, err
	}
	return item, nil
}
