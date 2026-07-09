package ollama

import (
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

// buildPrompt はメッセージリストからプロンプトを構築
func (p *OllamaProvider) buildPrompt(req llm.GenerateRequest) string {
	var parts []string

	// システムプロンプト
	if req.SystemPrompt != "" {
		parts = append(parts, fmt.Sprintf("System: %s\n", req.SystemPrompt))
	}

	// メッセージ履歴
	for _, msg := range req.Messages {
		switch msg.Role {
		case "user":
			parts = append(parts, fmt.Sprintf("User: %s", msg.Content))
		case "assistant":
			parts = append(parts, fmt.Sprintf("Assistant: %s", msg.Content))
		case "system":
			parts = append(parts, fmt.Sprintf("System: %s", msg.Content))
		}
	}

	return strings.Join(parts, "\n")
}
