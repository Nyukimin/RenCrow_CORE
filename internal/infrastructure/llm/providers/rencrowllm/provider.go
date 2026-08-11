package rencrowllm

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

const defaultBaseURL = "http://127.0.0.1:8090"

// GatewayProvider はRenCrow_LLM Gateway APIプロバイダーの実装
type GatewayProvider struct {
	apiKey         string
	model          string
	baseURL        string
	thinkingBridge bool
	modelContext   int
	client         *http.Client
	agentID        string
	executionRole  string
	executionAlias string
}

// WithRenCrowExecution adds the explicit Agent/Role meaning of a Gateway
// execution alias. Physical RenCrow_LLM Gateway-compatible targets never receive this
// metadata because RenCrow_LLM removes the rencrow field before proxying.
func (p *GatewayProvider) WithRenCrowExecution(agentID, executionRole, executionAlias string) *GatewayProvider {
	p.agentID = strings.TrimSpace(agentID)
	p.executionRole = strings.ToLower(strings.TrimSpace(executionRole))
	p.executionAlias = strings.TrimSpace(executionAlias)
	return p
}

// NewGatewayProvider は新しいGatewayProviderを作成
func NewGatewayProvider(apiKey, model string) *GatewayProvider {
	return NewGatewayProviderWithOptions(apiKey, model, defaultBaseURL, 120*time.Second)
}

// NewGatewayProviderWithOptions creates an RenCrow_LLM Gateway-compatible provider with custom endpoint and timeout.
func NewGatewayProviderWithOptions(apiKey, model, baseURL string, timeout time.Duration) *GatewayProvider {
	return NewGatewayProviderWithModelContext(apiKey, model, baseURL, timeout, 0)
}

// NewGatewayProviderWithModelContext creates an RenCrow_LLM Gateway-compatible provider with a default
// Gateway model context option.
func NewGatewayProviderWithModelContext(apiKey, model, baseURL string, timeout time.Duration, modelContext int) *GatewayProvider {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	provider := &GatewayProvider{
		apiKey:         apiKey,
		model:          model,
		baseURL:        baseURL,
		thinkingBridge: true,
		modelContext:   modelContext,
		client: &http.Client{
			Timeout: timeout,
		},
	}
	if agentID, executionRole, ok := canonicalExecutionForAlias(model); ok {
		provider.WithRenCrowExecution(agentID, executionRole, strings.TrimSpace(model))
	}
	return provider
}

func canonicalExecutionForAlias(alias string) (agentID, executionRole string, ok bool) {
	switch normalized := strings.ToLower(strings.TrimSpace(alias)); normalized {
	case "mio":
		return "mio", "chat", true
	case "shiro":
		return "shiro", "chatworker", true
	case "worker":
		return "shiro", "worker", true
	case "midori":
		return "midori", "wild", true
	case "kuro":
		return "kuro", "heavy", true
	case "coder1", "coder2", "coder3", "coder4":
		return normalized, "coder", true
	default:
		return "", "", false
	}
}

// SetBaseURL はベースURLを設定（テスト用）
func (p *GatewayProvider) SetBaseURL(url string) {
	p.baseURL = url
}

// Generate はLLM生成を実行
func (p *GatewayProvider) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	streaming := req.OnToken != nil

	// RenCrow_LLM Gateway APIリクエスト構築
	gatewayReq := map[string]interface{}{
		"model":    p.model,
		"messages": p.convertMessages(req),
	}
	p.addThinkingBridgeFields(gatewayReq, streaming)
	p.addProviderOptions(gatewayReq, req.ProviderOptions)
	p.addModelContextOption(gatewayReq)
	p.addRenCrowExecutionMetadata(ctx, gatewayReq, promptContextBlockMetadata(req))
	if err := addResponseFormat(gatewayReq, req.ResponseFormat); err != nil {
		return llm.GenerateResponse{}, err
	}
	if streaming {
		gatewayReq["stream_options"] = map[string]any{"include_usage": true}
	}

	// MaxTokens（RenCrow_LLM Gatewayではmax_tokens）
	if req.MaxTokens > 0 {
		gatewayReq["max_tokens"] = req.MaxTokens
	}

	// Temperature
	if req.Temperature > 0 {
		gatewayReq["temperature"] = req.Temperature
	}

	reqBody, err := json.Marshal(gatewayReq)
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
		return llm.GenerateResponse{}, fmt.Errorf("gateway API error: status=%d, body=%s", resp.StatusCode, string(body))
	}

	if streaming {
		return p.readChatCompletionsStream(resp.Body, req.OnToken)
	}

	// レスポンスパース
	var gatewayResp struct {
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

	if err := json.NewDecoder(resp.Body).Decode(&gatewayResp); err != nil {
		return llm.GenerateResponse{}, fmt.Errorf("failed to decode response: %w", err)
	}

	// コンテンツ抽出
	var content string
	var finishReason string
	if len(gatewayResp.Choices) > 0 {
		msg := gatewayResp.Choices[0].Message
		content = p.sanitizeThinkingBridgeContent(msg.Content, msg.ParseStatus, msg.ParserName)
		finishReason = gatewayResp.Choices[0].FinishReason
	}

	return llm.GenerateResponse{
		Content:      content,
		TokensUsed:   gatewayResp.Usage.TotalTokens,
		FinishReason: finishReason,
	}, nil
}

func addResponseFormat(payload map[string]interface{}, format llm.ResponseFormat) error {
	switch format {
	case llm.ResponseFormatText:
		return nil
	case llm.ResponseFormatJSONObject:
		payload["response_format"] = map[string]any{"type": string(format)}
		return nil
	default:
		return fmt.Errorf("unsupported response format %q", format)
	}
}

// Name はプロバイダー名を返す
func (p *GatewayProvider) Name() string {
	return fmt.Sprintf("rencrow_llm-%s", p.model)
}

// Chat はtool calling対応のチャットを実行（RenCrow_LLM Gateway /v1/chat/completions + tools）
func (p *GatewayProvider) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	model := req.Model
	if model == "" {
		model = p.model
	}

	messages := p.convertChatMessages(req.Messages)

	gatewayReq := map[string]interface{}{
		"model":    model,
		"messages": messages,
		"stream":   true,
	}
	p.addThinkingBridgeFields(gatewayReq, false)
	if err := addChatReasoningEffort(gatewayReq, req.ReasoningEffort); err != nil {
		return llm.ChatResponse{}, err
	}
	p.addModelContextOption(gatewayReq)
	p.addRenCrowExecutionMetadata(ctx, gatewayReq, promptContextBlockMetadataFromChat(req))
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
		gatewayReq["tools"] = tools
	}
	if req.Temperature > 0 {
		gatewayReq["temperature"] = req.Temperature
	}

	reqBody, err := json.Marshal(gatewayReq)
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
		return llm.ChatResponse{}, fmt.Errorf("gateway chat API error: status=%d, body=%s", resp.StatusCode, string(body))
	}

	return readToolChatCompletionsStream(resp.Body)
}

func addChatReasoningEffort(payload map[string]interface{}, effort llm.ReasoningEffort) error {
	switch effort {
	case llm.ReasoningEffortUnspecified:
		return nil
	case llm.ReasoningEffortLow:
		payload["think"] = "low"
		payload["reasoning_effort"] = "low"
		kwargs, _ := payload["chat_template_kwargs"].(map[string]any)
		if kwargs == nil {
			kwargs = map[string]any{}
			payload["chat_template_kwargs"] = kwargs
		}
		kwargs["enable_thinking"] = true
		kwargs["reasoning_effort"] = "low"
		return nil
	default:
		return fmt.Errorf("unsupported chat reasoning effort %q", effort)
	}
}

func (p *GatewayProvider) addRenCrowExecutionMetadata(ctx context.Context, payload map[string]interface{}, blocks []map[string]any) {
	metadata := map[string]any{}
	addNonEmptyMetadata(metadata, "agent_id", p.agentID)
	addNonEmptyMetadata(metadata, "execution_role", p.executionRole)
	addNonEmptyMetadata(metadata, "execution_alias", p.executionAlias)
	observationCtx := llm.WithExecutionObservationDefaults(ctx, llm.ExecutionObservation{
		Initiator: p.agentID,
		Caller:    "core.unattributed",
		Purpose:   "unattributed",
	})
	observation, _ := llm.ExecutionObservationFromContext(observationCtx)
	addNonEmptyMetadata(metadata, "request_id", observation.RequestID)
	addNonEmptyMetadata(metadata, "trace_id", observation.TraceID)
	addNonEmptyMetadata(metadata, "job_id", observation.JobID)
	addNonEmptyMetadata(metadata, "session_id", observation.SessionID)
	addNonEmptyMetadata(metadata, "initiator", observation.Initiator)
	addNonEmptyMetadata(metadata, "caller", observation.Caller)
	addNonEmptyMetadata(metadata, "purpose", observation.Purpose)
	if len(blocks) > 0 {
		metadata["prompt_context_blocks"] = blocks
	}
	if len(metadata) == 0 {
		return
	}
	payload["rencrow"] = metadata
}

func promptContextBlockMetadataFromChat(request llm.ChatRequest) []map[string]any {
	blocks := make([]map[string]any, 0, len(request.Messages))
	for messageIndex, message := range request.Messages {
		if message.Type == "" && len(message.Metadata) == 0 {
			continue
		}
		block := map[string]any{"message_index": messageIndex, "type": string(message.Type)}
		for key, value := range message.Metadata {
			block[key] = value
		}
		blocks = append(blocks, block)
	}
	return blocks
}

func promptContextBlockMetadata(request llm.GenerateRequest) []map[string]any {
	blocks := make([]map[string]any, 0, len(request.Messages))
	messageIndex := 0
	if request.SystemPrompt != "" {
		messageIndex++
	}
	for _, message := range request.Messages {
		if message.Type != "" || len(message.Metadata) > 0 {
			block := map[string]any{"message_index": messageIndex, "type": string(message.Type)}
			for key, value := range message.Metadata {
				block[key] = value
			}
			blocks = append(blocks, block)
		}
		messageIndex++
	}
	return blocks
}

func addNonEmptyMetadata(metadata map[string]any, key, value string) {
	if value = strings.TrimSpace(value); value != "" {
		metadata[key] = value
	}
}
