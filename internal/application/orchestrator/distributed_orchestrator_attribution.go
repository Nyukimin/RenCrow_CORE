package orchestrator

import (
	"fmt"
	"strings"

	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
)

type distributedAttributionGuard struct {
	memory *session.CentralMemory
}

func newDistributedAttributionGuard(memory *session.CentralMemory) *distributedAttributionGuard {
	return &distributedAttributionGuard{memory: memory}
}

func (g *distributedAttributionGuard) Apply(input domainconversation.TurnInput, targetAgent string) domainconversation.TurnInput {
	if targetAgent == "" || isCodeRoute(input.Route()) || strings.Contains(input.MessageText(), "【発言帰属ガード】") {
		return input
	}
	guarded := g.BuildMessage(input.MessageText(), targetAgent, input.SessionID())
	if guarded == input.MessageText() {
		return input
	}
	return input.WithMessageText(guarded)
}

func (g *distributedAttributionGuard) BuildMessage(userMessage, targetAgent, sessionID string) string {
	entries := g.memory.GetUnifiedView(120)
	selfLines := make([]string, 0, 3)
	otherLines := make([]string, 0, 3)

	for i := len(entries) - 1; i >= 0 && (len(selfLines) < 3 || len(otherLines) < 3); i-- {
		m := entries[i].Message
		if m.SessionID != sessionID || strings.TrimSpace(m.Content) == "" {
			continue
		}
		if m.Type == domaintransport.MessageTypeIdleChat || strings.HasPrefix(strings.ToLower(m.SessionID), "idle-") {
			continue
		}
		line := truncateForNote(strings.TrimSpace(m.Content), 90)
		if strings.EqualFold(m.From, targetAgent) {
			if len(selfLines) < 3 {
				selfLines = append(selfLines, line)
			}
			continue
		}
		if len(otherLines) < 3 {
			otherLines = append(otherLines, fmt.Sprintf("%s: %s", m.From, line))
		}
	}

	if len(selfLines) == 0 && len(otherLines) == 0 {
		return userMessage
	}
	if len(selfLines) == 0 {
		selfLines = append(selfLines, "なし")
	}
	if len(otherLines) == 0 {
		otherLines = append(otherLines, "なし")
	}

	guard := fmt.Sprintf(
		"【発言帰属ガード】\nあなたは %s。\n自分の過去発言: %s\n他者の発言: %s\n要件: 他者の発言や既出案を自分の新規アイデアとして言い換えない。参照時は発言者を明示する。",
		targetAgent,
		strings.Join(selfLines, " / "),
		strings.Join(otherLines, " / "),
	)
	return guard + "\n\n【ユーザー依頼】\n" + userMessage
}
