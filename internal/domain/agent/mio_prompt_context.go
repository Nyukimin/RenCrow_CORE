package agent

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

// MioExpressionHistory is the small, non-semantic style memory used to avoid
// nearby repetition. It stores wording hints only; it must not replace the
// conversation memory or alter factual context.
type MioExpressionHistory struct {
	Openings    []string
	Evaluations []string
	Connectors  []string
	Closings    []string
}

const mioExpressionHistoryLimit = 3

// Prompt renders a bounded prompt fragment suitable for runtime injection.
func (h MioExpressionHistory) Prompt() string {
	h = h.normalized()
	if len(h.Openings) == 0 && len(h.Evaluations) == 0 && len(h.Connectors) == 0 && len(h.Closings) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("## 最近の表現履歴\n")
	b.WriteString("意味と正確さを優先しつつ、近接ターンでは次の書き出し・評価語・接続・締め方をそのまま繰り返さない。履歴にない表現を無理に作らず、自然な言い換えを選ぶ。\n")
	writeMioHistoryList(&b, "最近の冒頭", h.Openings)
	writeMioHistoryList(&b, "最近の評価語", h.Evaluations)
	writeMioHistoryList(&b, "最近の接続表現", h.Connectors)
	writeMioHistoryList(&b, "最近の締め方", h.Closings)
	return strings.TrimSpace(b.String())
}

func (h MioExpressionHistory) normalized() MioExpressionHistory {
	return MioExpressionHistory{
		Openings:    normalizeMioHistoryItems(h.Openings),
		Evaluations: normalizeMioHistoryItems(h.Evaluations),
		Connectors:  normalizeMioHistoryItems(h.Connectors),
		Closings:    normalizeMioHistoryItems(h.Closings),
	}
}

func normalizeMioHistoryItems(items []string) []string {
	out := make([]string, 0, mioExpressionHistoryLimit)
	seen := make(map[string]struct{}, mioExpressionHistoryLimit)
	for i := len(items) - 1; i >= 0 && len(out) < mioExpressionHistoryLimit; i-- {
		item := truncateMioExpression(strings.TrimSpace(items[i]), 80)
		if item == "" {
			continue
		}
		key := strings.ToLower(item)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	// The newest item is useful first, but stable ordering makes snapshots
	// deterministic when this prompt is inspected.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func writeMioHistoryList(b *strings.Builder, label string, values []string) {
	if len(values) == 0 {
		return
	}
	fmt.Fprintf(b, "- %s: %s\n", label, strings.Join(values, " / "))
}

func truncateMioExpression(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "…"
}

func (m *MioAgent) runtimeMioPromptContext(t task.Task) string {
	recipient := strings.ToLower(strings.TrimSpace(t.ViewerRecipient()))
	if recipient != "" && recipient != "mio" {
		return ""
	}

	mode := mioModeForTask(t)
	tone := mioToneForTask(t)
	route := strings.TrimSpace(string(t.Route()))
	if route == "" {
		route = "未確定（Mioの判断対象）"
	}
	searchPolicy := "明示的な検索・調査依頼がある場合だけ、利用可能なCORE経路を使う。"
	if mode == "IdleChat" {
		searchPolicy = "外部検索は禁止。既存の内部文脈だけを使う。"
	}

	parts := []string{
		"# Runtime-injected Mio context",
		"このブロックは現在ターンだけの情報です。固定人格や会話記憶を上書きせず、事実として与えられた項目だけを使ってください。",
		"- mode: " + mode,
		"- tone: " + tone,
		"- route: " + route,
		"- requested_agent: " + valueOrDefault(recipient, "mio"),
		"- external_search_policy: " + searchPolicy,
		"- integrity: 実行していない処理を完了と表現しない。提案・部分成功・失敗・未確認を区別する。",
	}
	m.expressionHistoryMu.RLock()
	historyPrompt := m.expressionHistory.Prompt()
	m.expressionHistoryMu.RUnlock()
	if historyPrompt != "" {
		parts = append(parts, historyPrompt)
	}
	return strings.Join(parts, "\n\n")
}

func (m *MioAgent) stableMioPromptContext(t task.Task) string {
	recipient := strings.ToLower(strings.TrimSpace(t.ViewerRecipient()))
	if recipient != "" && recipient != "mio" {
		return ""
	}
	return strings.TrimSpace(m.agentContractsPrompt)
}

func (m *MioAgent) rememberExpression(response string) {
	response = strings.TrimSpace(response)
	if response == "" {
		return
	}
	sentences := mioSentences(response)
	if len(sentences) == 0 {
		return
	}

	history := MioExpressionHistory{}
	m.expressionHistoryMu.RLock()
	history = m.expressionHistory
	m.expressionHistoryMu.RUnlock()
	history.Openings = append(history.Openings, sentences[0])
	history.Closings = append(history.Closings, sentences[len(sentences)-1])
	for _, connector := range []string{"そのうえで", "とはいえ", "一方で", "まず", "なので", "ちなみに", "ここから", "ただ"} {
		if strings.Contains(response, connector) {
			history.Connectors = append(history.Connectors, connector)
		}
	}
	for _, evaluation := range []string{"筋は通っています", "自然です", "実装しやすいです", "引っかかります", "危険です", "確認が必要です", "狙いどおりです"} {
		if strings.Contains(response, evaluation) {
			history.Evaluations = append(history.Evaluations, evaluation)
		}
	}
	history = history.normalized()
	m.expressionHistoryMu.Lock()
	m.expressionHistory = history
	m.expressionHistoryMu.Unlock()
}

func mioSentences(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == '。' || r == '！' || r == '？' || r == '!' || r == '?' || r == '\n'
	})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimFunc(strings.TrimSpace(part), func(r rune) bool {
			return unicode.IsSpace(r) || r == '「' || r == '」' || r == '『' || r == '』'
		})
		if part != "" {
			out = append(out, truncateMioExpression(part, 80))
		}
	}
	return out
}

func mioModeForTask(t task.Task) string {
	if strings.EqualFold(strings.TrimSpace(t.Channel()), "idlechat") || strings.HasPrefix(strings.ToLower(strings.TrimSpace(t.ChatID())), "idle") {
		return "IdleChat"
	}
	switch strings.ToUpper(strings.TrimSpace(string(t.Route()))) {
	case "PLAN":
		return "PLAN"
	case "ANALYZE":
		return "ANALYZE"
	case "RESEARCH":
		return "RESEARCH"
	case "OPS":
		return "OPS"
	case "CODE", "CODE1", "CODE2", "CODE3", "CODE4":
		return "CODE"
	default:
		return "Chat"
	}
}

func mioToneForTask(t task.Task) string {
	mode := mioModeForTask(t)
	if mode == "IdleChat" {
		return "HIGH"
	}
	message := strings.ToLower(t.UserMessage())
	for _, keyword := range []string{
		"security", "セキュリティ", "認証", "秘密鍵", "token", "本番", "production",
		"法律", "契約", "医療", "削除", "破壊", "危険", "個人情報", "credential",
	} {
		if strings.Contains(message, strings.ToLower(keyword)) {
			return "LOW"
		}
	}
	switch mode {
	case "OPS", "CODE", "ANALYZE":
		return "LOW"
	default:
		return "MEDIUM"
	}
}

func valueOrDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
