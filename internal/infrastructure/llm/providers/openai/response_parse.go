package openai

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

// parseChatResponse はOpenAI chat completionsレスポンスをパースする
func (p *OpenAIProvider) parseChatResponse(body io.Reader) (llm.ChatResponse, error) {
	var openaiResp struct {
		Choices []struct {
			Message struct {
				Role        string `json:"role"`
				Content     string `json:"content"`
				ParseStatus string `json:"parse_status"`
				ParserName  string `json:"parser_name"`
				ToolCalls   []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}

	if err := json.NewDecoder(body).Decode(&openaiResp); err != nil {
		return llm.ChatResponse{}, fmt.Errorf("failed to decode chat response: %w", err)
	}

	if len(openaiResp.Choices) == 0 {
		return llm.ChatResponse{}, fmt.Errorf("empty choices in response")
	}

	choice := openaiResp.Choices[0]
	result := llm.ChatResponse{
		Message: llm.ChatMessage{
			Role:    choice.Message.Role,
			Content: p.sanitizeThinkingBridgeContent(choice.Message.Content, choice.Message.ParseStatus, choice.Message.ParserName),
		},
		Done:         true,
		FinishReason: choice.FinishReason,
	}

	for _, tc := range choice.Message.ToolCalls {
		var args map[string]any
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			args = map[string]any{"_raw": tc.Function.Arguments}
		}
		result.Message.ToolCalls = append(result.Message.ToolCalls, llm.ToolCall{
			ID: tc.ID,
			Function: llm.ToolCallFunction{
				Name:      tc.Function.Name,
				Arguments: args,
			},
		})
	}

	if len(result.Message.ToolCalls) > 0 && result.FinishReason == "" {
		result.FinishReason = "tool_calls"
	}

	return result, nil
}
