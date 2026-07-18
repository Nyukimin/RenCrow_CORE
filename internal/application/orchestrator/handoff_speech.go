package orchestrator

import (
	"fmt"
	"strings"
	"unicode"
)

const handoffSpeechTextLimit = 900

// formatAgentHandoffSpeech は、移譲元が移譲先を指名し、作業と会話文脈を口頭で渡す文面を返す。
func formatAgentHandoffSpeech(delegator, recipient, work, conversation string) string {
	return fmt.Sprintf("%s、%sから作業を移譲します。移譲内容: %s。会話内容: %s",
		handoffAgentName(recipient),
		handoffAgentName(delegator),
		handoffSpeechText(work, "指定された作業"),
		handoffSpeechText(conversation, "直前までの共通会話を参照"),
	)
}

// formatAgentHandoffReadbackSpeech は、移譲先が移譲元を指名して作業と会話文脈を復唱する文面を返す。
func formatAgentHandoffReadbackSpeech(delegator, recipient, work, conversation string) string {
	return fmt.Sprintf("%s、%sです。復唱します。移譲内容: %s。会話内容: %s",
		handoffAgentName(delegator),
		handoffAgentName(recipient),
		handoffSpeechText(work, "指定された作業"),
		handoffSpeechText(conversation, "直前までの共通会話を参照"),
	)
}

// formatAgentHandoffCompletionSpeech は、移譲先が移譲元を指名してから完了結果を報告する文面を返す。
func formatAgentHandoffCompletionSpeech(delegator, recipient, report string) string {
	return fmt.Sprintf("%s、%sです。移譲された作業が終わりました。報告: %s",
		handoffAgentName(delegator),
		handoffAgentName(recipient),
		handoffSpeechText(report, "結果なし"),
	)
}

func handoffAgentName(agentID string) string {
	raw := strings.TrimSpace(agentID)
	switch strings.ToLower(raw) {
	case "mio":
		return "Mio"
	case "shiro":
		return "Shiro"
	case "wild", "midori":
		return "Midori"
	case "heavy", "kuro":
		return "Kuro"
	case "coder1":
		return "Coder1"
	case "coder2":
		return "Coder2"
	case "coder3":
		return "Coder3"
	case "coder4":
		return "Coder4"
	case "coder_loop":
		return "CoderLoop"
	}
	if raw == "" {
		return "Agent"
	}
	runes := []rune(raw)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

func handoffSpeechText(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	runes := []rune(value)
	if len(runes) <= handoffSpeechTextLimit {
		return value
	}
	return strings.TrimSpace(string(runes[:handoffSpeechTextLimit])) + "..."
}
