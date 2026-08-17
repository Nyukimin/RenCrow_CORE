package conversation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"sort"
	"strings"
	"unicode/utf8"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
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
		minTurns: 3,
		// The Worker target is a reasoning model whose analysis channel
		// consumes output tokens before the final JSON. 256 tokens starved
		// the final channel (EMPTY_FINAL_CONTENT) or truncated the JSON.
		// CORE still validates the response against the 64KiB exact-JSON
		// contract after generation.
		maxTokens:   4096,
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

	// 既知情報テキスト。serviceがbounded owner projectionだけを渡す。
	existingText := ""
	if len(existing.Preferences) > 0 || len(existing.Facts) > 0 {
		existingText = "既知情報:\n"
		preferenceKeys := make([]string, 0, len(existing.Preferences))
		for k := range existing.Preferences {
			preferenceKeys = append(preferenceKeys, k)
		}
		sort.Strings(preferenceKeys)
		for _, k := range preferenceKeys {
			v := existing.Preferences[k]
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
preferences の各キーと値、facts の各要素は必ず JSON の文字列（string）で返してください。数値、オブジェクト、配列、真偽値、null は使わないでください。
preferences と facts を合わせて最大16件までにしてください。特に重要なものだけを選んでください。
各文字列は200文字以内の一文とし、文字列の中に改行を入れないでください。
JSONオブジェクトの前後に説明文、マークダウン、コードフェンスを付けないでください。

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
		log.Printf("[ProfileExtractor] LLM call failed: profile_extractor_unavailable")
		return nil, fmt.Errorf("profile extractor LLM call failed: %w", err)
	}
	if len(resp.Content) > domainmemory.ProfilePromotionResponseBytesMax {
		return nil, fmt.Errorf("profile extractor response exceeds %d bytes", domainmemory.ProfilePromotionResponseBytesMax)
	}

	// JSONはresponse全体でなければならない。prefix/suffixからJSONを抜き出さない。
	result, err := decodeProfileExtractionResult(resp.Content)
	if err != nil {
		log.Printf("[ProfileExtractor] JSON parse failed: profile_extractor_invalid")
		return nil, fmt.Errorf("profile extractor JSON parse failed: %w", err)
	}

	return result, nil
}

type profileExtractionPayload struct {
	Preferences map[string]json.RawMessage `json:"preferences"`
	Facts       []string                   `json:"facts"`
}

func decodeProfileExtractionResult(content string) (*domconv.ProfileExtractionResult, error) {
	if len(content) > domainmemory.ProfilePromotionResponseBytesMax {
		return nil, fmt.Errorf("profile extractor response exceeds %d bytes", domainmemory.ProfilePromotionResponseBytesMax)
	}
	trimmed := bytes.TrimSpace([]byte(content))
	if !utf8.Valid(trimmed) || len(trimmed) == 0 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return nil, fmt.Errorf("profile extractor response must be one JSON object")
	}
	fields, err := decodeUniqueJSONObject(trimmed)
	if err != nil {
		return nil, err
	}
	if fields == nil || len(fields) != 2 {
		return nil, fmt.Errorf("profile extractor response must contain exactly preferences and facts")
	}
	preferencesRaw, ok := fields["preferences"]
	if !ok {
		return nil, fmt.Errorf("profile extractor response is missing preferences")
	}
	factsRaw, ok := fields["facts"]
	if !ok {
		return nil, fmt.Errorf("profile extractor response is missing facts")
	}
	rawPreferences, err := decodeUniqueJSONObject(bytes.TrimSpace(preferencesRaw))
	if err != nil {
		return nil, fmt.Errorf("preferences must be a JSON object")
	}
	if bytes.Equal(bytes.TrimSpace(factsRaw), []byte("null")) {
		return nil, fmt.Errorf("facts must be a JSON array")
	}
	var facts []string
	if err := json.Unmarshal(factsRaw, &facts); err != nil || facts == nil {
		return nil, fmt.Errorf("facts must be a JSON array of strings")
	}
	if len(rawPreferences)+len(facts) > domainmemory.ProfilePromotionRawCandidateLimit {
		return nil, fmt.Errorf("profile extractor raw candidate count exceeds %d", domainmemory.ProfilePromotionRawCandidateLimit)
	}
	var payload profileExtractionPayload
	payload.Preferences = rawPreferences
	payload.Facts = facts
	result := &domconv.ProfileExtractionResult{
		NewPreferences: make(map[string]string, len(payload.Preferences)),
		NewFacts:       payload.Facts,
	}
	for key, raw := range payload.Preferences {
		if strings.TrimSpace(key) == "" || strings.ContainsAny(key, "\r\n\x00") || len([]rune(key)) > domainmemory.ProfilePromotionPreferenceKeyMax {
			return nil, fmt.Errorf("preference key %q is invalid", key)
		}
		var stringValue string
		if err := json.Unmarshal(raw, &stringValue); err == nil {
			if strings.TrimSpace(stringValue) == "" || strings.ContainsAny(stringValue, "\r\n\x00") || len([]rune(stringValue)) > domainmemory.ProfilePromotionPreferenceValueMax {
				return nil, fmt.Errorf("preference %q value is invalid", key)
			}
			result.NewPreferences[key] = stringValue
			continue
		}
		return nil, fmt.Errorf("preference %q must be a JSON string", key)
	}
	for i, fact := range result.NewFacts {
		if strings.TrimSpace(fact) == "" || strings.ContainsAny(fact, "\r\n\x00") || len([]rune(fact)) > domainmemory.ProfilePromotionProjectionStatementMax {
			return nil, fmt.Errorf("fact[%d] is invalid", i)
		}
	}
	return result, nil
}

func decodeUniqueJSONObject(raw []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	first, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := first.(json.Delim)
	if !ok || delim != '{' {
		return nil, fmt.Errorf("JSON value must be an object")
	}
	values := make(map[string]json.RawMessage)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, fmt.Errorf("JSON object key must be a string")
		}
		if _, exists := values[key]; exists {
			return nil, fmt.Errorf("duplicate JSON object key %q", key)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		values[key] = value
	}
	last, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := last.(json.Delim); !ok || delim != '}' {
		return nil, fmt.Errorf("JSON object is not closed")
	}
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("JSON response contains more than one value")
		}
		return nil, err
	}
	return values, nil
}
