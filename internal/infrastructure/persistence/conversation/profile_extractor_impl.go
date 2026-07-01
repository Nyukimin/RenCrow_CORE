package conversation

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	domconv "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/conversation"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
)

// LLMProfileExtractor は LLM を使ってユーザープロファイルを抽出する
type LLMProfileExtractor struct {
	provider     llm.LLMProvider
	minTurns     int // 最低ターン数（デフォルト: 3）
	maxTokens    int
	temperature  float64
}

// NewLLMProfileExtractor は新しい LLMProfileExtractor を作成
func NewLLMProfileExtractor(provider llm.LLMProvider) *LLMProfileExtractor {
	return &LLMProfileExtractor{
		provider:    provider,
		minTurns:    3,
		maxTokens:   256,
		temperature: 0.1,
	}
}

// Extract はスレッド内の会話からユーザープロファイルを抽出する
func (e *LLMProfileExtractor) Extract(ctx context.Context, thread *domconv.Thread, existing domconv.UserProfile) (*domconv.ProfileExtractionResult, error) {
	if thread == nil || len(thread.Turns) < e.minTurns {
		return &domconv.ProfileExtractionResult{}, nil
	}

	// ユーザーメッセージを収集
	var userMessages []string
	for _, turn := range thread.Turns {
		if turn.Speaker == domconv.SpeakerUser {
			userMessages = append(userMessages, turn.Msg)
		}
	}
	if len(userMessages) < e.minTurns {
		return &domconv.ProfileExtractionResult{}, nil
	}

	// 既知情報テキスト
	existingText := ""
	if len(existing.Preferences) > 0 || len(existing.Facts) > 0 {
		existingText = "既知情報:\n"
		for k, v := range existing.Preferences {
			existingText += fmt.Sprintf("- %s: %s\n", k, v)
		}
		for _, f := range existing.Facts {
			existingText += fmt.Sprintf("- %s\n", f)
		}
	}

	// プロンプト構築
	prompt := fmt.Sprintf(`以下の会話からユーザーに関する新しい情報を抽出してください。
既知情報と重複するものは除外してください。
JSON形式で出力してください。

%s

会話:
%s

出力形式:
{"preferences": {"カテゴリ": "値"}, "facts": ["事実1", "事実2"]}

新しい情報がない場合は空のJSONを返してください:
{"preferences": {}, "facts": []}`,
		existingText,
		strings.Join(userMessages, "\n"),
	)

	req := llm.GenerateRequest{
		Messages: []llm.Message{
			{Role: "user", Content: prompt},
		},
		MaxTokens:   e.maxTokens,
		Temperature: e.temperature,
	}

	resp, err := e.provider.Generate(ctx, req)
	if err != nil {
		log.Printf("[ProfileExtractor] LLM call failed: %v", err)
		return &domconv.ProfileExtractionResult{}, nil
	}

	// JSON パース（best-effort）
	result := &domconv.ProfileExtractionResult{}
	content := extractJSON(resp.Content)
	if err := json.Unmarshal([]byte(content), result); err != nil {
		log.Printf("[ProfileExtractor] JSON parse failed: %v (content: %s)", err, resp.Content)
		return &domconv.ProfileExtractionResult{}, nil
	}

	return result, nil
}

// extractJSON はレスポンスからJSON部分を抽出する
func extractJSON(s string) string {
	// JSON ブロックを探す
	start := strings.Index(s, "{")
	if start < 0 {
		return "{}"
	}
	end := strings.LastIndex(s, "}")
	if end < 0 || end < start {
		return "{}"
	}
	return s[start : end+1]
}
