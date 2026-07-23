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
	pending := make([]string, 0, 4)
	streamDirectly := !p.thinkingBridge
	var finishReason string
	var completionTokens int
	var tokensPerSecond float64
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
			Usage struct {
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
			Timings struct {
				PredictedPerSecond float64 `json:"predicted_per_second"`
			} `json:"timings"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return llm.GenerateResponse{}, fmt.Errorf("failed to decode stream chunk: %w", err)
		}
		if chunk.Error != nil {
			return llm.GenerateResponse{}, fmt.Errorf("gateway stream error %s: %s", chunk.Error.Code, chunk.Error.Message)
		}
		if chunk.Usage.CompletionTokens > 0 {
			completionTokens = chunk.Usage.CompletionTokens
		}
		if chunk.Timings.PredictedPerSecond > 0 {
			tokensPerSecond = chunk.Timings.PredictedPerSecond
		}
		for _, choice := range chunk.Choices {
			if choice.FinishReason != "" {
				finishReason = choice.FinishReason
			}
			if choice.Delta.Content == "" {
				continue
			}
			full.WriteString(choice.Delta.Content)
			if streamDirectly {
				onToken(choice.Delta.Content)
				continue
			}
			pending = append(pending, choice.Delta.Content)
			if potentialUntaggedReasoningPrefix(full.String()) {
				continue
			}
			streamDirectly = true
			for _, buffered := range pending {
				onToken(buffered)
			}
			pending = nil
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
			Content:         content,
			TokensUsed:      completionTokens,
			TokensPerSecond: tokensPerSecond,
			FinishReason:    finishReason,
		}, nil
	}
	if !streamDirectly {
		for _, buffered := range pending {
			onToken(buffered)
		}
	}
	return llm.GenerateResponse{
		Content:         content,
		TokensUsed:      completionTokens,
		TokensPerSecond: tokensPerSecond,
		FinishReason:    finishReason,
	}, nil
}

func potentialUntaggedReasoningPrefix(content string) bool {
	lower := strings.ToLower(strings.TrimSpace(content))
	if lower == "" {
		return true
	}
	for _, prefix := range []string{"okay,", "ok,", "let me ", "we need ", "i need ", "i should ", "the user "} {
		if strings.HasPrefix(prefix, lower) || strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}
