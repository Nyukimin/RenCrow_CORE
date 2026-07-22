package openai

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

func (p *OpenAIProvider) readChatCompletionsStream(body io.Reader, onToken llm.StreamCallback) (llm.GenerateResponse, error) {
	var full strings.Builder
	chunks := make([]string, 0, 16)
	var finishReason string
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
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error,omitempty"`
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return llm.GenerateResponse{}, fmt.Errorf("failed to decode stream chunk: %w", err)
		}
		if chunk.Error != nil {
			return llm.GenerateResponse{}, fmt.Errorf("gateway stream error %s: %s", chunk.Error.Code, chunk.Error.Message)
		}
		for _, choice := range chunk.Choices {
			if choice.FinishReason != "" {
				finishReason = choice.FinishReason
			}
			if choice.Delta.Content == "" {
				continue
			}
			full.WriteString(choice.Delta.Content)
			chunks = append(chunks, choice.Delta.Content)
		}
	}
	if err := scanner.Err(); err != nil {
		return llm.GenerateResponse{}, fmt.Errorf("failed to read stream: %w", err)
	}
	if finishReason == "" {
		finishReason = "stop"
	}
	content := full.String()
	if strings.TrimSpace(content) == "" {
		return llm.GenerateResponse{}, fmt.Errorf("stream completed without final content")
	}
	if p.thinkingBridge && looksLikeUntaggedReasoning(content) {
		content = extractFinalAnswerFromUntaggedReasoning(content)
		if content != "" {
			onToken(content)
		}
		return llm.GenerateResponse{
			Content:      content,
			FinishReason: finishReason,
		}, nil
	}
	for _, chunk := range chunks {
		onToken(chunk)
	}
	return llm.GenerateResponse{
		Content:      content,
		FinishReason: finishReason,
	}, nil
}
