package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

const defaultBaseURL = "https://api.openai.com"

// OpenAIProvider はOpenAI APIプロバイダーの実装
type OpenAIProvider struct {
	apiKey         string
	model          string
	baseURL        string
	thinkingBridge bool
	modelContext   int
	client         *http.Client
}

// NewOpenAIProvider は新しいOpenAIProviderを作成
func NewOpenAIProvider(apiKey, model string) *OpenAIProvider {
	return NewOpenAIProviderWithOptions(apiKey, model, defaultBaseURL, 120*time.Second)
}

// NewOpenAIProviderWithOptions creates an OpenAI-compatible provider with custom endpoint and timeout.
func NewOpenAIProviderWithOptions(apiKey, model, baseURL string, timeout time.Duration) *OpenAIProvider {
	return NewOpenAIProviderWithModelContext(apiKey, model, baseURL, timeout, 0)
}

// NewOpenAIProviderWithModelContext creates an OpenAI-compatible provider with a default
// Ollama-compatible options.num_ctx value for local endpoints.
func NewOpenAIProviderWithModelContext(apiKey, model, baseURL string, timeout time.Duration, modelContext int) *OpenAIProvider {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	return &OpenAIProvider{
		apiKey:         apiKey,
		model:          model,
		baseURL:        baseURL,
		thinkingBridge: strings.TrimRight(baseURL, "/") != defaultBaseURL,
		modelContext:   modelContext,
		client: &http.Client{
			Timeout: timeout,
		},
	}
}

// SetBaseURL はベースURLを設定（テスト用）
func (p *OpenAIProvider) SetBaseURL(url string) {
	p.baseURL = url
}

// Generate はLLM生成を実行
func (p *OpenAIProvider) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	streaming := req.OnToken != nil

	// OpenAI APIリクエスト構築
	openaiReq := map[string]interface{}{
		"model":    p.model,
		"messages": p.convertMessages(req),
	}
	p.addThinkingBridgeFields(openaiReq, streaming)
	p.addProviderOptions(openaiReq, req.ProviderOptions)
	p.addModelContextOption(openaiReq)
	if streaming {
		openaiReq["stream_options"] = map[string]any{"include_usage": true}
	}

	// MaxTokens（OpenAIではmax_tokens）
	if req.MaxTokens > 0 {
		openaiReq["max_tokens"] = req.MaxTokens
	}

	// Temperature
	if req.Temperature > 0 {
		openaiReq["temperature"] = req.Temperature
	}

	reqBody, err := json.Marshal(openaiReq)
	if err != nil {
		return llm.GenerateResponse{}, fmt.Errorf("failed to marshal request: %w", err)
	}

	// HTTPリクエスト作成
	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/v1/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return llm.GenerateResponse{}, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	// リクエスト実行
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return llm.GenerateResponse{}, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return llm.GenerateResponse{}, fmt.Errorf("openai API error: status=%d, body=%s", resp.StatusCode, string(body))
	}

	if streaming {
		return p.readChatCompletionsStream(resp.Body, req.OnToken)
	}

	// レスポンスパース
	var openaiResp struct {
		Choices []struct {
			Message struct {
				Role        string `json:"role"`
				Content     string `json:"content"`
				ParseStatus string `json:"parse_status"`
				ParserName  string `json:"parser_name"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&openaiResp); err != nil {
		return llm.GenerateResponse{}, fmt.Errorf("failed to decode response: %w", err)
	}

	// コンテンツ抽出
	var content string
	var finishReason string
	if len(openaiResp.Choices) > 0 {
		msg := openaiResp.Choices[0].Message
		content = p.sanitizeThinkingBridgeContent(msg.Content, msg.ParseStatus, msg.ParserName)
		finishReason = openaiResp.Choices[0].FinishReason
	}

	return llm.GenerateResponse{
		Content:      content,
		TokensUsed:   openaiResp.Usage.TotalTokens,
		FinishReason: finishReason,
	}, nil
}

// Name はプロバイダー名を返す
func (p *OpenAIProvider) Name() string {
	return fmt.Sprintf("openai-%s", p.model)
}

// Chat はtool calling対応のチャットを実行（OpenAI /v1/chat/completions + tools）
func (p *OpenAIProvider) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	model := req.Model
	if model == "" {
		model = p.model
	}

	messages := p.convertChatMessages(req.Messages)

	openaiReq := map[string]interface{}{
		"model":    model,
		"messages": messages,
	}
	p.addThinkingBridgeFields(openaiReq, false)
	p.addModelContextOption(openaiReq)
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
		openaiReq["tools"] = tools
	}
	if req.Temperature > 0 {
		openaiReq["temperature"] = req.Temperature
	}

	reqBody, err := json.Marshal(openaiReq)
	if err != nil {
		return llm.ChatResponse{}, fmt.Errorf("failed to marshal chat request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/v1/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return llm.ChatResponse{}, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return llm.ChatResponse{}, fmt.Errorf("failed to execute chat request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return llm.ChatResponse{}, fmt.Errorf("openai chat API error: status=%d, body=%s", resp.StatusCode, string(body))
	}

	return p.parseChatResponse(resp.Body)
}
