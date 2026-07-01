package gemini

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
)

const (
	geminiAPIEndpoint = "https://generativelanguage.googleapis.com/v1beta/models"
)

// Provider は Google Gemini API を使用する LLM Provider 実装
type Provider struct {
	apiKey string
	model  string
	client *http.Client
}

// NewProvider は新しい Gemini Provider を作成
func NewProvider(apiKey, model string) *Provider {
	return &Provider{
		apiKey: apiKey,
		model:  model,
		client: &http.Client{},
	}
}

// Generate は Gemini API を使用してテキストを生成
func (p *Provider) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	// Gemini API リクエスト構築
	geminiReq := geminiGenerateRequest{
		Contents: convertMessages(req.Messages),
		GenerationConfig: geminiGenerationConfig{
			Temperature:     req.Temperature,
			MaxOutputTokens: req.MaxTokens,
		},
	}

	// JSON エンコード
	body, err := json.Marshal(geminiReq)
	if err != nil {
		return llm.GenerateResponse{}, fmt.Errorf("marshal request: %w", err)
	}

	// API URL 構築
	url := fmt.Sprintf("%s/%s:generateContent?key=%s", geminiAPIEndpoint, p.model, p.apiKey)

	// HTTP リクエスト作成
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return llm.GenerateResponse{}, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	// API 呼び出し
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return llm.GenerateResponse{}, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	// ステータスコード確認
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return llm.GenerateResponse{}, fmt.Errorf("gemini API error: %s (status %d)", string(bodyBytes), resp.StatusCode)
	}

	// レスポンス解析
	var geminiResp geminiGenerateResponse
	if err := json.NewDecoder(resp.Body).Decode(&geminiResp); err != nil {
		return llm.GenerateResponse{}, fmt.Errorf("decode response: %w", err)
	}

	// コンテンツ抽出
	if len(geminiResp.Candidates) == 0 {
		return llm.GenerateResponse{}, fmt.Errorf("no candidates in response")
	}
	if len(geminiResp.Candidates[0].Content.Parts) == 0 {
		return llm.GenerateResponse{}, fmt.Errorf("no parts in candidate content")
	}

	content := geminiResp.Candidates[0].Content.Parts[0].Text

	return llm.GenerateResponse{
		Content: content,
	}, nil
}

// Chat は tool calling 対応チャットを実行する。Gemini はツール非対応のため Generate に委譲する。
func (p *Provider) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	msgs := make([]llm.Message, 0, len(req.Messages))
	for _, m := range req.Messages {
		msgs = append(msgs, llm.Message{Role: m.Role, Content: m.Content})
	}
	genReq := llm.GenerateRequest{
		Messages:    msgs,
		Temperature: req.Temperature,
	}
	resp, err := p.Generate(ctx, genReq)
	if err != nil {
		return llm.ChatResponse{}, err
	}
	return llm.ChatResponse{
		Message:      llm.ChatMessage{Role: "assistant", Content: resp.Content},
		Done:         true,
		FinishReason: "stop",
	}, nil
}

// Name はプロバイダ名を返す
func (p *Provider) Name() string {
	return "gemini"
}

// Gemini API リクエスト型
type geminiGenerateRequest struct {
	Contents         []geminiContent        `json:"contents"`
	GenerationConfig geminiGenerationConfig `json:"generationConfig"`
}

type geminiContent struct {
	Role  string       `json:"role"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text       string            `json:"text,omitempty"`
	InlineData *geminiInlineData `json:"inlineData,omitempty"`
}

type geminiInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type geminiGenerationConfig struct {
	Temperature     float64 `json:"temperature"`
	MaxOutputTokens int     `json:"maxOutputTokens"`
}

// Gemini API レスポンス型
type geminiGenerateResponse struct {
	Candidates []geminiCandidate `json:"candidates"`
}

type geminiCandidate struct {
	Content geminiContent `json:"content"`
}

// convertMessages は llm.Message を Gemini 形式に変換
func convertMessages(messages []llm.Message) []geminiContent {
	contents := make([]geminiContent, 0, len(messages))

	for _, msg := range messages {
		role := msg.Role
		// Gemini は system role を直接サポートしないため user に変換
		if role == "system" {
			role = "user"
		}
		// assistant は model に変換
		if role == "assistant" {
			role = "model"
		}

		parts := []geminiPart{{Text: msg.Content}}
		if len(msg.Parts) > 0 {
			parts = make([]geminiPart, 0, len(msg.Parts))
			for _, part := range msg.Parts {
				switch part.Type {
				case llm.MessagePartImage, llm.MessagePartVideo, llm.MessagePartAudio:
					if len(part.Data) == 0 || part.MimeType == "" {
						if part.Type != llm.MessagePartAudio {
							continue
						}
					}
					if part.Type == llm.MessagePartAudio {
						if len(part.Data) == 0 {
							continue
						}
						parts = append(parts, geminiPart{InlineData: &geminiInlineData{
							MimeType: part.MimeType,
							Data:     base64.StdEncoding.EncodeToString(part.Data),
						}})
						continue
					}
					parts = append(parts, geminiPart{InlineData: &geminiInlineData{
						MimeType: part.MimeType,
						Data:     base64.StdEncoding.EncodeToString(part.Data),
					}})
				default:
					text := part.Text
					if text == "" {
						text = msg.Content
					}
					if text != "" {
						parts = append(parts, geminiPart{Text: text})
					}
				}
			}
			if len(parts) == 0 {
				parts = []geminiPart{{Text: msg.Content}}
			}
		}

		contents = append(contents, geminiContent{Role: role, Parts: parts})
	}

	return contents
}
