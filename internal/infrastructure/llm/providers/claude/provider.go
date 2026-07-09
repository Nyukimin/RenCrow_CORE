package claude

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

const defaultBaseURL = "https://api.anthropic.com"
const anthropicVersion = "2023-06-01"

// ClaudeProvider はClaude APIプロバイダーの実装
type ClaudeProvider struct {
	apiKey  string
	model   string
	baseURL string
	client  *http.Client
}

// NewClaudeProvider は新しいClaudeProviderを作成
func NewClaudeProvider(apiKey, model string) *ClaudeProvider {
	return &ClaudeProvider{
		apiKey:  apiKey,
		model:   model,
		baseURL: defaultBaseURL,
		client: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

// SetBaseURL はベースURLを設定（テスト用）
func (p *ClaudeProvider) SetBaseURL(url string) {
	p.baseURL = url
}

// Generate はLLM生成を実行
func (p *ClaudeProvider) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	// Messages内のsystemロールからシステムプロンプトを抽出
	systemPrompt := req.SystemPrompt
	if systemPrompt == "" {
		for _, msg := range req.Messages {
			if msg.Role == "system" {
				systemPrompt = msg.Content
				break
			}
		}
	}

	// Claude APIリクエスト構築
	claudeReq := map[string]interface{}{
		"model":      p.model,
		"messages":   p.convertMessages(req.Messages),
		"max_tokens": req.MaxTokens,
	}

	// システムプロンプト
	if systemPrompt != "" {
		claudeReq["system"] = systemPrompt
	}

	// Temperature（0.0-1.0の範囲）
	if req.Temperature > 0 {
		claudeReq["temperature"] = req.Temperature
	}

	reqBody, err := json.Marshal(claudeReq)
	if err != nil {
		return llm.GenerateResponse{}, fmt.Errorf("failed to marshal request: %w", err)
	}

	// HTTPリクエスト作成
	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/v1/messages", bytes.NewReader(reqBody))
	if err != nil {
		return llm.GenerateResponse{}, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", anthropicVersion)

	// リクエスト実行
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return llm.GenerateResponse{}, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return llm.GenerateResponse{}, fmt.Errorf("claude API error: status=%d, body=%s", resp.StatusCode, string(body))
	}

	// レスポンスパース
	var claudeResp struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&claudeResp); err != nil {
		return llm.GenerateResponse{}, fmt.Errorf("failed to decode response: %w", err)
	}

	// コンテンツ抽出
	var content string
	if len(claudeResp.Content) > 0 && claudeResp.Content[0].Type == "text" {
		content = claudeResp.Content[0].Text
	}

	return llm.GenerateResponse{
		Content:      content,
		TokensUsed:   claudeResp.Usage.InputTokens + claudeResp.Usage.OutputTokens,
		FinishReason: claudeResp.StopReason,
	}, nil
}

// Name はプロバイダー名を返す
func (p *ClaudeProvider) Name() string {
	return fmt.Sprintf("claude-%s", p.model)
}

// Chat はtool calling対応のチャットを実行（Anthropic Messages API + tools）
func (p *ClaudeProvider) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	model := req.Model
	if model == "" {
		model = p.model
	}

	// system メッセージを抽出し、残りを変換
	var systemPrompt string
	messages := p.convertChatMessages(req.Messages, &systemPrompt)

	claudeReq := map[string]interface{}{
		"model":      model,
		"messages":   messages,
		"max_tokens": 4096,
	}
	if systemPrompt != "" {
		claudeReq["system"] = systemPrompt
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]interface{}, 0, len(req.Tools))
		for _, td := range req.Tools {
			tools = append(tools, map[string]interface{}{
				"name":         td.Function.Name,
				"description":  td.Function.Description,
				"input_schema": td.Function.Parameters,
			})
		}
		claudeReq["tools"] = tools
	}
	if req.Temperature > 0 {
		claudeReq["temperature"] = req.Temperature
	}

	reqBody, err := json.Marshal(claudeReq)
	if err != nil {
		return llm.ChatResponse{}, fmt.Errorf("failed to marshal chat request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/v1/messages", bytes.NewReader(reqBody))
	if err != nil {
		return llm.ChatResponse{}, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", anthropicVersion)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return llm.ChatResponse{}, fmt.Errorf("failed to execute chat request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return llm.ChatResponse{}, fmt.Errorf("claude chat API error: status=%d, body=%s", resp.StatusCode, string(body))
	}

	return p.parseChatResponse(resp.Body)
}

// convertChatMessages はChatMessageをClaude APIフォーマットに変換する
//
// Claude API の特殊ルール:
//   - system は messages に含めず、トップレベルで渡す
//   - assistant の tool_calls → content に type=tool_use ブロック
//   - tool メッセージ → user メッセージ内の type=tool_result ブロック
func (p *ClaudeProvider) convertChatMessages(msgs []llm.ChatMessage, systemPrompt *string) []map[string]interface{} {
	messages := make([]map[string]interface{}, 0, len(msgs))

	// 連続する tool メッセージをまとめるためのバッファ
	var toolResults []map[string]interface{}

	flushToolResults := func() {
		if len(toolResults) > 0 {
			messages = append(messages, map[string]interface{}{
				"role":    "user",
				"content": toolResults,
			})
			toolResults = nil
		}
	}

	for _, m := range msgs {
		switch m.Role {
		case "system":
			if systemPrompt != nil {
				*systemPrompt = m.Content
			}

		case "assistant":
			flushToolResults()
			content := make([]map[string]interface{}, 0)
			if m.Content != "" {
				content = append(content, map[string]interface{}{
					"type": "text",
					"text": m.Content,
				})
			}
			for _, tc := range m.ToolCalls {
				content = append(content, map[string]interface{}{
					"type":  "tool_use",
					"id":    tc.ID,
					"name":  tc.Function.Name,
					"input": tc.Function.Arguments,
				})
			}
			if len(content) == 0 {
				content = append(content, map[string]interface{}{
					"type": "text",
					"text": "",
				})
			}
			messages = append(messages, map[string]interface{}{
				"role":    "assistant",
				"content": content,
			})

		case "tool":
			toolResults = append(toolResults, map[string]interface{}{
				"type":        "tool_result",
				"tool_use_id": m.ToolCallID,
				"content":     m.Content,
			})

		default: // "user"
			flushToolResults()
			messages = append(messages, map[string]interface{}{
				"role": m.Role,
				"content": []map[string]interface{}{
					{"type": "text", "text": m.Content},
				},
			})
		}
	}

	flushToolResults()
	return messages
}

// parseChatResponse はClaude Messages APIレスポンスをパースする
func (p *ClaudeProvider) parseChatResponse(body io.Reader) (llm.ChatResponse, error) {
	var claudeResp struct {
		Content []struct {
			Type  string         `json:"type"`
			Text  string         `json:"text"`
			ID    string         `json:"id"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}

	if err := json.NewDecoder(body).Decode(&claudeResp); err != nil {
		return llm.ChatResponse{}, fmt.Errorf("failed to decode chat response: %w", err)
	}

	result := llm.ChatResponse{
		Message: llm.ChatMessage{
			Role: "assistant",
		},
		Done: true,
	}

	for _, block := range claudeResp.Content {
		switch block.Type {
		case "text":
			if result.Message.Content != "" {
				result.Message.Content += "\n"
			}
			result.Message.Content += block.Text
		case "tool_use":
			result.Message.ToolCalls = append(result.Message.ToolCalls, llm.ToolCall{
				ID: block.ID,
				Function: llm.ToolCallFunction{
					Name:      block.Name,
					Arguments: block.Input,
				},
			})
		}
	}

	// Claude: stop_reason "tool_use" → ドメインの "tool_calls" に統一
	if claudeResp.StopReason == "tool_use" {
		result.FinishReason = "tool_calls"
	} else {
		result.FinishReason = claudeResp.StopReason
	}

	return result, nil
}

// convertMessages はドメインメッセージをClaude APIフォーマットに変換
func (p *ClaudeProvider) convertMessages(messages []llm.Message) []map[string]interface{} {
	claudeMessages := make([]map[string]interface{}, 0, len(messages))

	for _, msg := range messages {
		// Claude APIはsystemロールをサポートしないため、systemはトップレベルで渡す
		if msg.Role == "system" {
			continue
		}

		content := []map[string]interface{}{{"type": "text", "text": msg.Content}}
		if len(msg.Parts) > 0 {
			content = make([]map[string]interface{}, 0, len(msg.Parts))
			for _, part := range msg.Parts {
				switch part.Type {
				case llm.MessagePartImage:
					if len(part.Data) == 0 || part.MimeType == "" {
						continue
					}
					content = append(content, map[string]interface{}{
						"type": "image",
						"source": map[string]interface{}{
							"type":       "base64",
							"media_type": part.MimeType,
							"data":       base64.StdEncoding.EncodeToString(part.Data),
						},
					})
				default:
					text := part.Text
					if text == "" {
						text = msg.Content
					}
					if text != "" {
						content = append(content, map[string]interface{}{"type": "text", "text": text})
					}
				}
			}
			if len(content) == 0 {
				content = []map[string]interface{}{{"type": "text", "text": msg.Content}}
			}
		}

		claudeMessages = append(claudeMessages, map[string]interface{}{"role": msg.Role, "content": content})
	}

	return claudeMessages
}
