package deepseek

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
)

const defaultBaseURL = "https://api.deepseek.com"

// DeepSeekProvider はDeepSeek APIプロバイダーの実装
// DeepSeek APIはOpenAI互換のため、実装はOpenAIProviderと類似
type DeepSeekProvider struct {
	apiKey  string
	model   string
	baseURL string
	client  *http.Client
}

// NewDeepSeekProvider は新しいDeepSeekProviderを作成
func NewDeepSeekProvider(apiKey, model string) *DeepSeekProvider {
	return &DeepSeekProvider{
		apiKey:  apiKey,
		model:   model,
		baseURL: defaultBaseURL,
		client: &http.Client{
			Timeout: 300 * time.Second,
		},
	}
}

// SetBaseURL はベースURLを設定（テスト用）
func (p *DeepSeekProvider) SetBaseURL(url string) {
	p.baseURL = url
}

// Generate はLLM生成を実行
func (p *DeepSeekProvider) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	// DeepSeek APIリクエスト構築（OpenAI互換）
	deepseekReq := map[string]interface{}{
		"model":    p.model,
		"messages": p.convertMessages(req),
	}

	// MaxTokens
	if req.MaxTokens > 0 {
		deepseekReq["max_tokens"] = req.MaxTokens
	}

	// Temperature
	if req.Temperature > 0 {
		deepseekReq["temperature"] = req.Temperature
	}

	reqBody, err := json.Marshal(deepseekReq)
	if err != nil {
		return llm.GenerateResponse{}, fmt.Errorf("failed to marshal request: %w", err)
	}

	// HTTPリクエスト作成
	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/v1/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return llm.GenerateResponse{}, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	log.Printf("[DeepSeek] request start model=%s messages=%d max_tokens=%v temperature=%v", p.model, len(deepseekReq["messages"].([]map[string]interface{})), deepseekReq["max_tokens"], deepseekReq["temperature"])

	// リクエスト実行
	resp, err := p.client.Do(httpReq)
	if err != nil {
		log.Printf("[DeepSeek] request error model=%s err=%v", p.model, err)
		return llm.GenerateResponse{}, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("[DeepSeek] request bad_status model=%s status=%d", p.model, resp.StatusCode)
		return llm.GenerateResponse{}, fmt.Errorf("deepseek API error: status=%d, body=%s", resp.StatusCode, string(body))
	}
	log.Printf("[DeepSeek] response headers model=%s status=%d", p.model, resp.StatusCode)

	// レスポンスパース（OpenAI互換）
	var deepseekResp struct {
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&deepseekResp); err != nil {
		log.Printf("[DeepSeek] decode error model=%s err=%v", p.model, err)
		return llm.GenerateResponse{}, fmt.Errorf("failed to decode response: %w", err)
	}

	// コンテンツ抽出
	var content string
	var finishReason string
	if len(deepseekResp.Choices) > 0 {
		content = deepseekResp.Choices[0].Message.Content
		finishReason = deepseekResp.Choices[0].FinishReason
	}
	log.Printf("[DeepSeek] response complete model=%s choices=%d finish=%s content_len=%d tokens=%d", p.model, len(deepseekResp.Choices), finishReason, len(content), deepseekResp.Usage.TotalTokens)

	return llm.GenerateResponse{
		Content:      content,
		TokensUsed:   deepseekResp.Usage.TotalTokens,
		FinishReason: finishReason,
	}, nil
}

// Name はプロバイダー名を返す
func (p *DeepSeekProvider) Name() string {
	return fmt.Sprintf("deepseek-%s", p.model)
}

// Chat はtool calling対応のチャットを実行（DeepSeek /v1/chat/completions + tools、OpenAI互換）
func (p *DeepSeekProvider) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	model := req.Model
	if model == "" {
		model = p.model
	}

	messages := p.convertChatMessages(req.Messages)

	dsReq := map[string]interface{}{
		"model":    model,
		"messages": messages,
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]interface{}, 0, len(req.Tools))
		for _, td := range req.Tools {
			tools = append(tools, map[string]interface{}{
				"type": "function",
				"function": map[string]interface{}{
					"name":        td.Function.Name,
					"description": td.Function.Description,
					"parameters":  td.Function.Parameters,
				},
			})
		}
		dsReq["tools"] = tools
	}
	if req.Temperature > 0 {
		dsReq["temperature"] = req.Temperature
	}

	reqBody, err := json.Marshal(dsReq)
	if err != nil {
		return llm.ChatResponse{}, fmt.Errorf("failed to marshal chat request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/v1/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return llm.ChatResponse{}, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return llm.ChatResponse{}, fmt.Errorf("failed to execute chat request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return llm.ChatResponse{}, fmt.Errorf("deepseek chat API error: status=%d, body=%s", resp.StatusCode, string(body))
	}

	return p.parseChatResponse(resp.Body)
}

// convertChatMessages はChatMessageをOpenAI互換フォーマットに変換
func (p *DeepSeekProvider) convertChatMessages(msgs []llm.ChatMessage) []map[string]interface{} {
	messages := make([]map[string]interface{}, 0, len(msgs))
	for _, m := range msgs {
		msg := map[string]interface{}{
			"role": m.Role,
		}
		if m.Content != "" {
			msg["content"] = m.Content
		}
		if len(m.ToolCalls) > 0 {
			tcs := make([]map[string]interface{}, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				argsJSON, _ := json.Marshal(tc.Function.Arguments)
				tcs = append(tcs, map[string]interface{}{
					"id":   tc.ID,
					"type": "function",
					"function": map[string]interface{}{
						"name":      tc.Function.Name,
						"arguments": string(argsJSON),
					},
				})
			}
			msg["tool_calls"] = tcs
		}
		if m.ToolCallID != "" {
			msg["tool_call_id"] = m.ToolCallID
		}
		messages = append(messages, msg)
	}
	return messages
}

// parseChatResponse はOpenAI互換レスポンスをパースする
func (p *DeepSeekProvider) parseChatResponse(body io.Reader) (llm.ChatResponse, error) {
	var dsResp struct {
		Choices []struct {
			Message struct {
				Role      string `json:"role"`
				Content   string `json:"content"`
				ToolCalls []struct {
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

	if err := json.NewDecoder(body).Decode(&dsResp); err != nil {
		return llm.ChatResponse{}, fmt.Errorf("failed to decode chat response: %w", err)
	}

	if len(dsResp.Choices) == 0 {
		return llm.ChatResponse{}, fmt.Errorf("empty choices in response")
	}

	choice := dsResp.Choices[0]
	result := llm.ChatResponse{
		Message: llm.ChatMessage{
			Role:    choice.Message.Role,
			Content: choice.Message.Content,
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

// convertMessages はドメインメッセージをDeepSeek APIフォーマットに変換
func (p *DeepSeekProvider) convertMessages(req llm.GenerateRequest) []map[string]interface{} {
	messages := make([]map[string]interface{}, 0)

	// システムプロンプトを最初に追加
	if req.SystemPrompt != "" {
		messages = append(messages, map[string]interface{}{
			"role":    "system",
			"content": req.SystemPrompt,
		})
	}

	// ユーザーメッセージを追加
	for _, msg := range req.Messages {
		messages = append(messages, map[string]interface{}{
			"role":    msg.Role,
			"content": msg.Content,
		})
	}

	return messages
}
