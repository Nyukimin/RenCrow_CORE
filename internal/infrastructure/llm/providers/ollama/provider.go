package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

const preflightTTL = 30 * time.Second

// OllamaProvider はOllama APIプロバイダーの実装
type OllamaProvider struct {
	baseURL string
	model   string
	numCtx  int
	client  *http.Client

	readyCacheMu sync.Mutex
	readyCache   map[string]time.Time // model -> 最後に ready 確認した時刻
}

// NewOllamaProvider は新しいOllamaProviderを作成
func NewOllamaProvider(baseURL, model string) *OllamaProvider {
	return NewOllamaProviderWithNumCtx(baseURL, model, 0)
}

// NewOllamaProviderWithNumCtx は num_ctx を明示した OllamaProvider を作成
func NewOllamaProviderWithNumCtx(baseURL, model string, numCtx int) *OllamaProvider {
	return &OllamaProvider{
		baseURL:    baseURL,
		model:      model,
		numCtx:     numCtx,
		readyCache: make(map[string]time.Time),
		client: &http.Client{
			Timeout: 120 * time.Second, // Ollamaは遅い場合があるため長めに設定
		},
	}
}

// Generate はLLM生成を実行（OnToken が設定されていればストリーミング）
func (p *OllamaProvider) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	// プロンプト構築
	prompt := p.buildPrompt(req)

	streaming := req.OnToken != nil

	if err := p.ensureModelReady(ctx, p.model); err != nil {
		return llm.GenerateResponse{}, err
	}

	// Ollama APIリクエスト
	ollamaReq := map[string]interface{}{
		"model":      p.model,
		"prompt":     prompt,
		"stream":     streaming,
		"keep_alive": -1,
		"think":      false, // thinking モデル（gemma4等）の思考タグ出力を抑制
		"options": map[string]interface{}{
			"temperature": req.Temperature,
			"num_predict": req.MaxTokens,
			"stop":        []string{},
		},
	}
	if p.numCtx > 0 {
		ollamaReq["options"].(map[string]interface{})["num_ctx"] = p.numCtx
	}

	reqBody, err := json.Marshal(ollamaReq)
	if err != nil {
		return llm.GenerateResponse{}, fmt.Errorf("failed to marshal request: %w", err)
	}

	// HTTPリクエスト作成
	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/api/generate", bytes.NewReader(reqBody))
	if err != nil {
		return llm.GenerateResponse{}, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	// ストリーミング時はタイムアウトなしの別クライアントを使用
	client := p.client
	if streaming {
		client = &http.Client{} // no timeout for streaming
	}

	// リクエスト実行
	resp, err := client.Do(httpReq)
	if err != nil {
		return llm.GenerateResponse{}, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return llm.GenerateResponse{}, fmt.Errorf("ollama API error: status=%d, body=%s", resp.StatusCode, string(body))
	}

	// ストリーミング
	if streaming {
		return p.readStream(resp.Body, req.OnToken)
	}

	// 非ストリーミング（従来通り）
	var ollamaResp struct {
		Response string `json:"response"`
		Done     bool   `json:"done"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&ollamaResp); err != nil {
		return llm.GenerateResponse{}, fmt.Errorf("failed to decode response: %w", err)
	}

	return llm.GenerateResponse{
		Content:      ollamaResp.Response,
		TokensUsed:   0,
		FinishReason: "stop",
	}, nil
}

// Name はプロバイダー名を返す
func (p *OllamaProvider) Name() string {
	return fmt.Sprintf("ollama-%s", p.model)
}

// Chat はtool calling対応のチャットを実行（Ollama /api/chat）
func (p *OllamaProvider) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	model := req.Model
	if model == "" {
		model = p.model
	}

	if err := p.ensureModelReady(ctx, model); err != nil {
		return llm.ChatResponse{}, err
	}

	// メッセージ変換
	messages := make([]ollamaChatMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		msg := ollamaChatMessage{
			Role:    m.Role,
			Content: m.Content,
		}
		if len(m.ToolCalls) > 0 {
			for _, tc := range m.ToolCalls {
				msg.ToolCalls = append(msg.ToolCalls, ollamaToolCall{
					Function: ollamaToolCallFunction{
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					},
				})
			}
		}
		messages = append(messages, msg)
	}

	// ツール定義変換
	var tools []ollamaToolDef
	for _, td := range req.Tools {
		tools = append(tools, ollamaToolDef{
			Type: td.Type,
			Function: ollamaFunctionDef{
				Name:        td.Function.Name,
				Description: td.Function.Description,
				Parameters:  td.Function.Parameters,
			},
		})
	}

	chatReq := ollamaChatRequest{
		Model:     model,
		Messages:  messages,
		Tools:     tools,
		Stream:    false,
		KeepAlive: -1,
	}
	if req.Temperature > 0 {
		chatReq.Options = &ollamaChatOptions{Temperature: req.Temperature}
	}
	if p.numCtx > 0 {
		if chatReq.Options == nil {
			chatReq.Options = &ollamaChatOptions{}
		}
		chatReq.Options.NumCtx = p.numCtx
	}

	reqBody, err := json.Marshal(chatReq)
	if err != nil {
		return llm.ChatResponse{}, fmt.Errorf("failed to marshal chat request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/api/chat", bytes.NewReader(reqBody))
	if err != nil {
		return llm.ChatResponse{}, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return llm.ChatResponse{}, fmt.Errorf("failed to execute chat request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return llm.ChatResponse{}, fmt.Errorf("ollama chat API error: status=%d, body=%s", resp.StatusCode, string(body))
	}

	var chatResp ollamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return llm.ChatResponse{}, fmt.Errorf("failed to decode chat response: %w", err)
	}

	// レスポンス変換
	result := llm.ChatResponse{
		Message: llm.ChatMessage{
			Role:    chatResp.Message.Role,
			Content: chatResp.Message.Content,
		},
		Done: chatResp.Done,
	}

	if len(chatResp.Message.ToolCalls) > 0 {
		result.FinishReason = "tool_calls"
		for i, tc := range chatResp.Message.ToolCalls {
			id := tc.Function.Name
			if id == "" {
				id = fmt.Sprintf("call_%d", i)
			} else {
				id = fmt.Sprintf("call_%s_%d", tc.Function.Name, i)
			}
			result.Message.ToolCalls = append(result.Message.ToolCalls, llm.ToolCall{
				ID: id,
				Function: llm.ToolCallFunction{
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				},
			})
		}
	} else {
		result.FinishReason = "stop"
	}

	return result, nil
}

// --- Ollama /api/chat 用の内部型 ---
