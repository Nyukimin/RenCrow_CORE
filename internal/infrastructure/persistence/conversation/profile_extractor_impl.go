package conversation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

// LLMProfileExtractor は LLM を使ってユーザープロファイルを抽出する
type LLMProfileExtractor struct {
	provider    llm.LLMProvider
	minTurns    int // 最低ターン数（デフォルト: 3）
	maxTokens   int
	temperature float64
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

func (e *LLMProfileExtractor) WithMinimumUserMessages(minimum int) *LLMProfileExtractor {
	if minimum < 1 {
		minimum = 1
	}
	e.minTurns = minimum
	return e
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
preferences の各値は必ず JSON の文字列（string）で返してください。数値に見える値も引用符で囲んだ文字列として返してください。オブジェクト、配列、真偽値、null は preferences の値に使わないでください。

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
		MaxTokens:      e.maxTokens,
		Temperature:    e.temperature,
		ResponseFormat: llm.ResponseFormatJSONObject,
	}

	requestCtx := llm.WithExecutionObservation(ctx, llm.ExecutionObservation{
		SessionID: thread.SessionID, Initiator: "shiro", Caller: "memory.profile_promotion", Purpose: "extract_profile_candidates",
	})
	resp, err := e.provider.Generate(requestCtx, req)
	if err != nil {
		log.Printf("[ProfileExtractor] LLM call failed: %v", err)
		return nil, fmt.Errorf("profile extractor LLM call failed: %w", err)
	}

	// JSON パース（best-effort）
	content := extractJSON(resp.Content)
	result, err := decodeProfileExtractionResult(content)
	if err != nil {
		log.Printf("[ProfileExtractor] JSON parse failed: %v", err)
		return nil, fmt.Errorf("profile extractor JSON parse failed: %w", err)
	}

	return result, nil
}

type profileExtractionPayload struct {
	Preferences map[string]json.RawMessage `json:"preferences"`
	Facts       []string                   `json:"facts"`
}

func decodeProfileExtractionResult(content string) (*domconv.ProfileExtractionResult, error) {
	var payload profileExtractionPayload
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		return nil, err
	}
	result := &domconv.ProfileExtractionResult{
		NewPreferences: make(map[string]string, len(payload.Preferences)),
		NewFacts:       payload.Facts,
	}
	for key, raw := range payload.Preferences {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil, fmt.Errorf("preference %q must be a JSON string or number", key)
		}
		var stringValue string
		if err := json.Unmarshal(raw, &stringValue); err == nil {
			result.NewPreferences[key] = stringValue
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("preference %q must be a JSON string or number: %w", key, err)
		}
		if number, ok := value.(json.Number); ok {
			result.NewPreferences[key] = number.String()
			continue
		}
		return nil, fmt.Errorf("preference %q must be a JSON string or number", key)
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
