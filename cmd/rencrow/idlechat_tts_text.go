package main

import (
	"github.com/Nyukimin/RenCrow_CORE/internal/application/idlechat"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

func formatIdleChatTTSText(ev idlechat.TimelineEvent) string {
	return moduletts.FormatIdleChatTTSText(idleChatSpeechInput(ev))
}

func formatIdleChatDisplayText(ev idlechat.TimelineEvent) string {
	return moduletts.FormatIdleChatDisplayText(idleChatSpeechInput(ev))
}

func isIdleChatTopicAnnouncement(ev idlechat.TimelineEvent) bool {
	return moduletts.IsIdleChatTopicAnnouncement(idleChatSpeechInput(ev))
}

func idleChatSpeechInput(ev idlechat.TimelineEvent) moduletts.IdleChatSpeechInput {
	return moduletts.IdleChatSpeechInput{
		Type:    ev.Type,
		From:    ev.From,
		To:      ev.To,
		Content: ev.Content,
	}
}
