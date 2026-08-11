package rencrowllm

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

type streamedToolCall struct {
	id        strings.Builder
	name      strings.Builder
	arguments strings.Builder
}

func readToolChatCompletionsStream(body io.Reader) (llm.ChatResponse, error) {
	var content strings.Builder
	role := ""
	finishReason := ""
	toolCalls := make(map[int]*streamedToolCall)

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}

		var chunk struct {
			Error *struct {
				Code    json.RawMessage `json:"code"`
				Message string          `json:"message"`
			} `json:"error,omitempty"`
			Choices []struct {
				Delta struct {
					Role      string `json:"role"`
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return llm.ChatResponse{}, fmt.Errorf("failed to decode tool chat stream chunk: %w", err)
		}
		if chunk.Error != nil {
			return llm.ChatResponse{}, fmt.Errorf("gateway tool chat stream error %s: %s", streamErrorCodeText(chunk.Error.Code), chunk.Error.Message)
		}
		if len(chunk.Choices) > 0 {
			choice := chunk.Choices[0]
			if role == "" && choice.Delta.Role != "" {
				role = choice.Delta.Role
			}
			content.WriteString(choice.Delta.Content)
			if choice.FinishReason != "" {
				finishReason = choice.FinishReason
			}
			for _, delta := range choice.Delta.ToolCalls {
				if delta.Index < 0 {
					return llm.ChatResponse{}, fmt.Errorf("invalid negative tool call index %d", delta.Index)
				}
				accumulator := toolCalls[delta.Index]
				if accumulator == nil {
					accumulator = &streamedToolCall{}
					toolCalls[delta.Index] = accumulator
				}
				accumulator.id.WriteString(delta.ID)
				accumulator.name.WriteString(delta.Function.Name)
				accumulator.arguments.WriteString(delta.Function.Arguments)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return llm.ChatResponse{}, fmt.Errorf("failed to read tool chat stream: %w", err)
	}

	if role == "" {
		role = "assistant"
	}
	result := llm.ChatResponse{Message: llm.ChatMessage{Role: role, Content: content.String()}, Done: true, FinishReason: finishReason}
	indices := make([]int, 0, len(toolCalls))
	for index := range toolCalls {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		call := toolCalls[index]
		rawArguments := call.arguments.String()
		arguments := make(map[string]any)
		if err := json.Unmarshal([]byte(rawArguments), &arguments); err != nil {
			arguments = map[string]any{"_raw": rawArguments}
		}
		result.Message.ToolCalls = append(result.Message.ToolCalls, llm.ToolCall{
			ID: call.id.String(),
			Function: llm.ToolCallFunction{
				Name:      call.name.String(),
				Arguments: arguments,
			},
		})
	}
	if result.FinishReason == "" {
		if len(result.Message.ToolCalls) > 0 {
			result.FinishReason = "tool_calls"
		} else {
			result.FinishReason = "stop"
		}
	}
	if strings.TrimSpace(result.Message.Content) == "" && len(result.Message.ToolCalls) == 0 {
		return llm.ChatResponse{}, fmt.Errorf("tool chat stream completed without content or tool calls")
	}
	return result, nil
}
