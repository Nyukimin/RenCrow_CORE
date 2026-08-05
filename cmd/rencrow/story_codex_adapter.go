package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

type idleChatCodexExeGenerator struct {
	runner *tools.CodexExecRunner
}

// storyCodexExeGenerator remains as a compatibility alias for existing story
// adapter tests; production Topic, Dialogue, and Story share one boundary.
type storyCodexExeGenerator = idleChatCodexExeGenerator

func (g idleChatCodexExeGenerator) Generate(ctx context.Context, prompt string) (string, error) {
	if g.runner == nil {
		return "", errors.New("IdleChat CodexExe runner is not configured")
	}
	response, err := g.runner.Run(ctx, tools.CodexRunRequest{
		Prompt:    prompt,
		Sandbox:   "read-only",
		Ephemeral: true,
	})
	if err != nil {
		return "", err
	}
	if response.ExitCode != 0 {
		return "", fmt.Errorf("CodexExe exited with code %d: %s", response.ExitCode, strings.TrimSpace(response.ErrorMessage))
	}
	text := strings.TrimSpace(response.FinalText)
	if text == "" {
		return "", errors.New("CodexExe returned empty final text")
	}
	return text, nil
}
