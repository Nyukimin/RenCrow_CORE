package orchestrator

import (
	"strings"
	"time"

	domaintransport "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/transport"
)

func transportMode(sshTransports map[string]domaintransport.Transport, targetAgent string) string {
	if _, ok := sshTransports[targetAgent]; ok {
		return "ssh"
	}
	return "local"
}

func routeAndChannelFromMessage(msg domaintransport.Message) (route, channel, chatID string) {
	if msg.Context == nil {
		return "", "", ""
	}
	route = stringContextValue(msg.Context, "route")
	channel = stringContextValue(msg.Context, "channel")
	chatID = stringContextValue(msg.Context, "chat_id")
	return route, channel, chatID
}

func stringContextValue(ctx map[string]interface{}, key string) string {
	raw, ok := ctx[key]
	if !ok || raw == nil {
		return ""
	}
	v, ok := raw.(string)
	if !ok {
		return ""
	}
	return v
}

// distributedWaitTimeout はエージェント種別とメッセージ内容に基づくタイムアウト時間を返す（パッケージレベル関数）。
// テストから直接呼べるよう、デフォルト定数を使う版。
func distributedWaitTimeout(targetAgent string, msg domaintransport.Message) time.Duration {
	switch {
	case isCoderAgent(targetAgent):
		return distributedCoderTimeout
	case targetAgent == "shiro" && msg.Proposal != nil:
		return distributedWorkerTimeout
	default:
		return distributedDefaultTimeout
	}
}

func isCoderAgent(targetAgent string) bool {
	return strings.HasPrefix(targetAgent, "coder")
}
