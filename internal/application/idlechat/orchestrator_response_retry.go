package idlechat

import (
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

func buildIdleCompactRetryMessages(speaker, topic, latestOther, purpose string) []llm.Message {
	topic = strings.TrimSpace(topic)
	if topic == "" {
		topic = "この会話の現在のお題"
	}
	other := strings.TrimSpace(latestOther)
	if other == "" {
		other = "-"
	}
	style := "自然な日本語で1-2文。英語だけの応答、英語の見出し、英語での説明は禁止。表示される会話本文だけを返す。具体物か小さな問いを一つ入れる。"
	if strings.EqualFold(strings.TrimSpace(speaker), "mio") {
		style += " Mioとしてタメ口で、明るく好奇心のある入口にする。"
	} else if strings.EqualFold(strings.TrimSpace(speaker), "shiro") {
		style += " Shiroとして落ち着いた常体寄りで、整理だけで終えず小さな未決点を残す。"
	}
	content := fmt.Sprintf("%sとして、話題「%s」について会話本文を作ってください。\n直前の相手発言: %s\n%s", speaker, topic, other, style)
	if strings.TrimSpace(purpose) != "" {
		content += "\n狙い: " + strings.TrimSpace(purpose)
	}
	return []llm.Message{
		{Role: "system", Content: "/no_think\n最終回答の本文だけを自然な日本語で返す。英語だけの応答、英語の見出し、英語での説明は禁止。"},
		{Role: "user", Content: content},
	}
}

func unusableIdleResponse(raw, sanitized string) bool {
	return invalidIdleResponse(sanitized) ||
		englishDominantIdleText(sanitized) ||
		((hasPromptLeak(raw) || hasInternalReasoningLeak(raw)) && !hasIdleSentenceEnd(sanitized)) ||
		hasPromptLeak(sanitized) ||
		hasInternalReasoningLeak(sanitized)
}

func finishReasonLooksTruncated(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "length", "max_tokens", "max_output_tokens":
		return true
	default:
		return false
	}
}
