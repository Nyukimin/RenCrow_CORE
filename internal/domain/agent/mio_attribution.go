package agent

import (
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

func buildAttributionContextsFromShort(short []conversation.Message, self conversation.Speaker, limit int) ([]string, []string) {
	selfCtx := make([]string, 0, limit)
	otherCtx := make([]string, 0, limit)
	for i := len(short) - 1; i >= 0 && (len(selfCtx) < limit || len(otherCtx) < limit); i-- {
		msg := strings.TrimSpace(short[i].Msg)
		if msg == "" {
			continue
		}
		line := truncateLog(msg, 80)
		if short[i].Speaker == self {
			if len(selfCtx) < limit {
				selfCtx = append(selfCtx, line)
			}
			continue
		}
		if len(otherCtx) < limit {
			otherCtx = append(otherCtx, fmt.Sprintf("%s: %s", short[i].Speaker, line))
		}
	}
	if len(selfCtx) == 0 {
		selfCtx = append(selfCtx, "なし")
	}
	if len(otherCtx) == 0 {
		otherCtx = append(otherCtx, "なし")
	}
	return selfCtx, otherCtx
}

func latestOtherMessageFromShort(short []conversation.Message, self conversation.Speaker) string {
	for i := len(short) - 1; i >= 0; i-- {
		if short[i].Speaker == self {
			continue
		}
		return strings.TrimSpace(short[i].Msg)
	}
	return ""
}

func violatesAttributionInChat(response, latestOther string) bool {
	resp := normalizeAttributionText(response)
	other := normalizeAttributionText(latestOther)
	if resp == "" || other == "" || resp != other {
		return false
	}
	lower := strings.ToLower(response)
	return !strings.Contains(lower, "あなた") && !strings.Contains(lower, "君") && !strings.Contains(lower, "相手")
}

func normalizeAttributionText(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "　", "")
	s = strings.ReplaceAll(s, "。", "")
	s = strings.ReplaceAll(s, "、", "")
	s = strings.ReplaceAll(s, "！", "")
	s = strings.ReplaceAll(s, "？", "")
	return s
}
