package orchestrator

import (
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
)

func displayAgentName(agentID string) string {
	switch strings.ToLower(agentID) {
	case "mio":
		return "みお"
	case "shiro":
		return "しろ"
	case "coder1":
		return "あか"
	case "coder2":
		return "あお"
	case "coder3":
		return "ぎん"
	default:
		return agentID
	}
}

func routeNoticeText(route routing.Route, userMessage string) string {
	switch route {
	case routing.RouteCHAT:
		return "みおが会話として対応するよ。"
	case routing.RouteOPS:
		return "しろに運用作業をお願いしたよ。"
	case routing.RoutePLAN:
		return "計画モードで整理するよ。"
	case routing.RouteANALYZE:
		return "分析として進めるよ。"
	case routing.RouteRESEARCH:
		return "調査タスクとして進めるよ。"
	case routing.RouteCODE, routing.RouteCODE1, routing.RouteCODE2, routing.RouteCODE3:
		return fmt.Sprintf("しろ経由でコーディング依頼に回したよ（依頼: %s）。", truncateForNote(userMessage, 32))
	default:
		return "処理経路を決めて進めるよ。"
	}
}

func truncateForNote(s string, max int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= max {
		return string(r)
	}
	return string(r[:max]) + "..."
}
