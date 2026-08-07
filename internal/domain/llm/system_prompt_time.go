package llm

import (
	"fmt"
	"strings"
	"time"
)

const (
	// CurrentJSTTimePrefix identifies the canonical current-time line in a system prompt.
	CurrentJSTTimePrefix = "現在時刻（JST）: "
	currentJSTTimeFormat = "2006-01-02 15:04:05 JST"
)

var jstLocation = time.FixedZone("JST", 9*60*60)

// AppendCurrentJSTTime appends the supplied time, converted to JST, to a system prompt.
func AppendCurrentJSTTime(prompt string, now time.Time) string {
	timeLine := fmt.Sprintf("%s%s", CurrentJSTTimePrefix, now.In(jstLocation).Format(currentJSTTimeFormat))
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return timeLine
	}
	return prompt + "\n\n" + timeLine
}

// AppendNowJST appends the current JST time to a system prompt.
func AppendNowJST(prompt string) string {
	return AppendCurrentJSTTime(prompt, time.Now())
}

// WithCurrentJSTTime adds JST time as Variable RuntimeContext without mutating
// the fixed Character SystemPrompt or the caller's message slice.
func WithCurrentJSTTime(req GenerateRequest, now time.Time) GenerateRequest {
	for _, message := range req.Messages {
		if message.Type == PromptContextVariable && strings.Contains(message.Content, CurrentJSTTimePrefix) {
			return req
		}
	}
	timeMessage := Message{
		Role:    "system",
		Content: AppendCurrentJSTTime("", now),
		Type:    PromptContextVariable,
		Metadata: map[string]string{
			"runtime_context_kind": "time",
		},
	}
	insertAt := len(req.Messages)
	for index := len(req.Messages) - 1; index >= 0; index-- {
		if req.Messages[index].Type == PromptContextUser || strings.EqualFold(strings.TrimSpace(req.Messages[index].Role), "user") {
			insertAt = index
			break
		}
	}
	result := make([]Message, 0, len(req.Messages)+1)
	result = append(result, req.Messages[:insertAt]...)
	result = append(result, timeMessage)
	result = append(result, req.Messages[insertAt:]...)
	req.Messages = result
	return req
}

// WithCurrentJSTTimeNow adds current JST time as Variable RuntimeContext.
func WithCurrentJSTTimeNow(req GenerateRequest) GenerateRequest {
	return WithCurrentJSTTime(req, time.Now())
}
