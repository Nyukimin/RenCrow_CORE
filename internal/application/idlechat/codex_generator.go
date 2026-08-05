package idlechat

import (
	"context"
	"errors"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

// IdleChatCodexGenerator is the narrow application boundary for a read-only,
// ephemeral CodexExe execution. CodexExe is an execution mechanism, not an
// Agent identity.
type IdleChatCodexGenerator interface {
	Generate(ctx context.Context, prompt string) (string, error)
}

type idleChatCodexLLMProvider struct {
	generator IdleChatCodexGenerator
}

func newIdleChatCodexLLMProvider(generator IdleChatCodexGenerator) llm.LLMProvider {
	if generator == nil {
		return nil
	}
	return idleChatCodexLLMProvider{generator: generator}
}

func (p idleChatCodexLLMProvider) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	if p.generator == nil {
		return llm.GenerateResponse{}, errors.New("CodexExe generator is not configured")
	}
	var prompt strings.Builder
	for _, message := range req.Messages {
		content := strings.TrimSpace(message.Content)
		if content == "" {
			continue
		}
		prompt.WriteString("[")
		prompt.WriteString(strings.ToLower(strings.TrimSpace(message.Role)))
		prompt.WriteString("]\n")
		prompt.WriteString(content)
		prompt.WriteString("\n\n")
	}
	ctx = llm.WithExecutionObservation(ctx, llm.ExecutionObservation{
		Initiator: "shiro",
		Caller:    "idlechat.codex_exe",
		Purpose:   "topic_generation_or_judge",
	})
	text, err := p.generator.Generate(ctx, strings.TrimSpace(prompt.String()))
	if err != nil {
		return llm.GenerateResponse{}, err
	}
	return llm.GenerateResponse{Content: strings.TrimSpace(text), FinishReason: "stop"}, nil
}

func (idleChatCodexLLMProvider) Name() string { return "CodexExe" }
