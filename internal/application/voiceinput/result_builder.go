package voiceinput

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type BuildLLMRequest struct {
	UtteranceID  string
	SessionID    string
	Channel      string
	ChatID       string
	UserTextHint string
	FinalText    string
	StartedAt    time.Time
	CommitAt     time.Time
	FirstTokenAt time.Time
	FinalAt      time.Time
}

type BuildSTTRequest struct {
	UtteranceID string
	SessionID   string
	Channel     string
	ChatID      string
	UserText    string
	Reply       string
	StartedAt   time.Time
	CommitAt    time.Time
	FinalAt     time.Time
}

func BuildFromLLMFinal(req BuildLLMRequest) (Result, error) {
	raw := strings.TrimSpace(req.FinalText)
	if raw == "" {
		return Result{}, errors.New("llm final text is required")
	}
	if IsMetaNoAudioFinal(raw) {
		return Result{}, errors.New("llm final is non-conversational no-audio response")
	}
	userText := strings.TrimSpace(req.UserTextHint)
	reply, parsedUserText := SplitStructuredText(raw)
	if userText == "" {
		userText = parsedUserText
	}
	if strings.TrimSpace(reply) == "" {
		reply = raw
	}
	result := Result{
		Mode:        ModeLLM,
		UtteranceID: strings.TrimSpace(req.UtteranceID),
		SessionID:   strings.TrimSpace(req.SessionID),
		Channel:     strings.TrimSpace(req.Channel),
		ChatID:      strings.TrimSpace(req.ChatID),
		UserText:    strings.TrimSpace(userText),
		Reply:       strings.TrimSpace(reply),
		RawFinal:    raw,
		Source:      "RenCrow_LLM llm.final",
		Timings: Timings{
			StartedAt:    req.StartedAt,
			CommitAt:     req.CommitAt,
			FirstTokenAt: req.FirstTokenAt,
			FinalAt:      firstNonZeroTime(req.FinalAt, time.Now()),
		},
	}
	if err := result.Validate(); err != nil {
		return Result{}, err
	}
	return result, nil
}

func BuildFromSTTFinal(req BuildSTTRequest) (Result, error) {
	result := Result{
		Mode:        ModeSTT,
		UtteranceID: strings.TrimSpace(req.UtteranceID),
		SessionID:   strings.TrimSpace(req.SessionID),
		Channel:     strings.TrimSpace(req.Channel),
		ChatID:      strings.TrimSpace(req.ChatID),
		UserText:    strings.TrimSpace(req.UserText),
		Reply:       strings.TrimSpace(req.Reply),
		RawFinal:    strings.TrimSpace(req.UserText),
		Source:      "RenCrow_STT final",
		Timings: Timings{
			StartedAt: req.StartedAt,
			CommitAt:  req.CommitAt,
			FinalAt:   firstNonZeroTime(req.FinalAt, time.Now()),
		},
	}
	if err := result.Validate(); err != nil {
		return Result{}, err
	}
	return result, nil
}

func SplitStructuredText(text string) (reply string, userText string) {
	raw := strings.TrimSpace(text)
	if raw == "" {
		return "", ""
	}
	jsonText := ExtractJSONObject(raw)
	if jsonText == "" {
		return raw, ""
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(jsonText), &payload); err != nil {
		return raw, ""
	}
	userText = firstTextField(payload, "user_text", "transcript", "user_utterance", "recognized_text")
	reply = firstTextField(payload, "reply", "assistant_text", "mio_response", "response", "answer")
	if reply == "" {
		return raw, userText
	}
	return reply, userText
}

func ExtractJSONObject(text string) string {
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "```") {
		lines := strings.Split(trimmed, "\n")
		if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[0]), "```") {
			lines = lines[1:]
		}
		if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "```") {
			lines = lines[:len(lines)-1]
		}
		trimmed = strings.TrimSpace(strings.Join(lines, "\n"))
	}
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start >= 0 && end > start {
		return trimmed[start : end+1]
	}
	return ""
}

func IsMetaNoAudioFinal(text string) bool {
	normalized := strings.TrimSpace(text)
	if normalized == "" {
		return false
	}
	metaPhrases := []string{
		"音声内容を入力してください",
		"音声内容を提示してください",
		"音声ファイルをアップロード",
		"音声が提供されていない",
		"音声が入力されていない",
		"入力をお待ちしております",
	}
	for _, phrase := range metaPhrases {
		if strings.Contains(normalized, phrase) {
			return true
		}
	}
	return false
}

func firstTextField(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		value, _ := payload[key].(string)
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstNonZeroTime(values ...time.Time) time.Time {
	for _, value := range values {
		if !value.IsZero() {
			return value
		}
	}
	return time.Time{}
}
