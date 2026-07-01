package orchestrator

import (
	"fmt"
	"strings"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/session"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/task"
	domaintransport "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/transport"
)

type distributedAttributionGuard struct {
	memory *session.CentralMemory
}

func newDistributedAttributionGuard(memory *session.CentralMemory) *distributedAttributionGuard {
	return &distributedAttributionGuard{memory: memory}
}

func (g *distributedAttributionGuard) Apply(t task.Task, targetAgent, sessionID string) task.Task {
	if targetAgent == "" || isCodeRoute(t.Route()) || strings.Contains(t.UserMessage(), "【発言帰属ガード】") {
		return t
	}
	guarded := g.BuildMessage(t.UserMessage(), targetAgent, sessionID)
	if guarded == t.UserMessage() {
		return t
	}
	out := task.NewTask(t.JobID(), guarded, t.Channel(), t.ChatID())
	if t.HasForcedRoute() {
		out = out.WithForcedRoute(t.ForcedRoute())
	}
	if t.Route() != "" {
		out = out.WithRoute(t.Route())
	}
	return out
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
