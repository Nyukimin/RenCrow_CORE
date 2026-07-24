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

// WithCurrentJSTTime appends JST time to the canonical system prompt without
// mutating the caller's message slice.
func WithCurrentJSTTime(req GenerateRequest, now time.Time) GenerateRequest {
	if strings.TrimSpace(req.SystemPrompt) != "" {
		req.SystemPrompt = AppendCurrentJSTTime(req.SystemPrompt, now)
		return req
	}

	for i, message := range req.Messages {
		if strings.EqualFold(strings.TrimSpace(message.Role), "system") {
			req.Messages = append([]Message(nil), req.Messages...)
			req.Messages[i].Content = AppendCurrentJSTTime(message.Content, now)
			break
		}
	}
	return req
}

// WithCurrentJSTTimeNow appends the current JST time to the canonical system prompt.
func WithCurrentJSTTimeNow(req GenerateRequest) GenerateRequest {
	return WithCurrentJSTTime(req, time.Now())
}
