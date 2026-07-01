package idlechat

import (
	"fmt"
	"log"
	"regexp"
	"strings"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
)

const maxPromptGuides = 5

var promptFixLineRe = regexp.MustCompile(`(?i)^\s*(?:[-*・]\s*)?(?:PROMPT_FIX|prompt_fix|プロンプト補正|プロンプト修正)\s*[:：]\s*(.+)$`)
var interestHookLineRe = regexp.MustCompile(`(?i)^\s*(?:[-*・]\s*)?(?:INTEREST_HOOK|interest_hook|面白さの芽)\s*[:：]\s*(.+)$`)
var missedTurnLineRe = regexp.MustCompile(`(?i)^\s*(?:[-*・]\s*)?(?:MISSED_TURN|missed_turn|逃した分岐)\s*[:：]\s*(.+)$`)
var lengthControlLineRe = regexp.MustCompile(`(?i)^\s*(?:[-*・]\s*)?(?:LENGTH_CONTROL|length_control|長さ制御)\s*[:：]\s*(.+)$`)

func (o *IdleChatOrchestrator) reviewSessionEnd(topic, mode string, transcript []string, summary, loopReason string) (string, string) {
	body := strings.TrimSpace(strings.Join(transcript, "\n"))
	if body == "" {
		return "", ""
	}

	fallbackReview, fallbackGuide := heuristicQualityReview(topic, mode, transcript, summary, loopReason)
	loopReasonForPrompt := strings.TrimSpace(loopReason)
	if loopReasonForPrompt == "" {
		loopReasonForPrompt = "なし（会話は規定ターンまで完了。打ち切りとして扱わない）"
	}
	messages := []llm.Message{
		{Role: "system", Content: "あなたはIdleChatの聞き手体験を編集する脚本編集者です。退屈さを検出するだけでなく、会話に残っていた面白さの芽を言語化し、次回は短く効くように補正してください。"},
		{Role: "user", Content: fmt.Sprintf(`次のIdleChat終了ログを評価してください。

観点:
- その話は聞き手にとって面白かったか。面白くなかった場合、何が足りなかったか
- 会話内に一瞬でもあった「面白さの芽」は何か
- どの発話で、面白い方向へ曲がれたのに抽象論・説明・反復へ逃げたか
- 長くせず面白くするには、どんな型にすべきか
- 注記や打ち切り理由がある場合は、必ず原因を推定して再発防止プロンプトを出す
- 打ち切り理由が「なし」の場合、BORING_CAUSE や MISSED_TURN に「打ち切り」と書かない

出力形式:
QUALITY: pass または fail
BORING_CAUSE: 退屈だった主因を1文
INTEREST_HOOK: 会話内にあった面白さの芽を1つ。具体物・秘密・損得・選択・感情のどれかを含める
MISSED_TURN: 面白くできたのに逃した分岐を1文
PROMPT_FIX: 次回以降のsystem promptに追加すべき具体的な一文。必ず「INTEREST_HOOK」という語を含め、説明を増やす指示ではなく、面白さの芽を場面・選択・秘密へ変換する指示にする
LENGTH_CONTROL: 2文以内、または最大120字など、短くする制約を1文

モード: %s
話題: %s
打ち切り理由: %s
要約:
%s

会話ログ:
%s`, mode, topic, loopReasonForPrompt, strings.TrimSpace(summary), body)},
	}

	resp, err := o.providerForSpeaker("shiro").Generate(o.idleRunContext(), llm.GenerateRequest{
		Messages:    messages,
		MaxTokens:   idleChatQualityReviewMaxTokens,
		Temperature: 0.2,
	})
	if err != nil || strings.TrimSpace(resp.Content) == "" {
		if err != nil {
			log.Printf("[IdleChat] quality review failed: %v", err)
		}
		if err == nil {
			logIdleRaw("quality_review.generate", resp.Content)
		}
		return fallbackReview, fallbackGuide
	}
	logIdleRaw("quality_review.generate", resp.Content)

	review := strings.TrimSpace(resp.Content)
	review = normalizeQualityReview(review)
	if review == "" {
		return fallbackReview, fallbackGuide
	}
	if qualityReviewContradictsCompletion(review, loopReason) {
		log.Printf("[IdleChat] quality review contradicted completed session, using heuristic review")
		return fallbackReview, fallbackGuide
	}
	guide := extractPromptGuidance(review)
	if guide == "" {
		guide = fallbackGuide
	}
	if fallbackGuide != "" && !strings.Contains(guide, fallbackGuide) {
		guide = joinPromptGuides(guide, fallbackGuide)
	}
	return review, guide
}

func qualityReviewContradictsCompletion(review, loopReason string) bool {
	if strings.TrimSpace(loopReason) != "" {
		return false
	}
	review = strings.TrimSpace(review)
	if review == "" {
		return false
	}
	return strings.Contains(review, "打ち切り") || strings.Contains(strings.ToLower(review), "loop")
}

func normalizeQualityReview(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	start := -1
	for i, line := range lines {
		lower := strings.ToLower(strings.TrimSpace(line))
		if strings.HasPrefix(lower, "quality:") {
			start = i
			break
		}
	}
	if start < 0 {
		return ""
	}
	s = strings.TrimSpace(strings.Join(lines[start:], "\n"))
	required := []string{"QUALITY:", "BORING_CAUSE:", "INTEREST_HOOK:", "MISSED_TURN:", "PROMPT_FIX:", "LENGTH_CONTROL:"}
	for _, key := range required {
		if !strings.Contains(s, key) {
			return ""
		}
	}
	if hasInternalReasoningLeak(s) || summaryLooksLikeEnglishMetaReasoning(s) {
		return ""
	}
	return s
}

func heuristicQualityReview(topic, mode string, transcript []string, summary, loopReason string) (string, string) {
	var issues []string
	var fixes []string
	if note := loopReasonLabel(loopReason); note != "" {
		issues = append(issues, "打ち切り注記: "+note)
		switch loopReason {
		case "short_template_repeat", "template_repeat":
			fixes = append(fixes, "同じ受け方で広げず、INTEREST_HOOKを1つ選んで、誰かが損をする選択・隠し事が露出する瞬間・感情が反転する場面のどれかへ変える。")
		case "what_if_repeat":
			fixes = append(fixes, "「もし」「だったら」「なら」で仮定を重ねず、INTEREST_HOOKを現実の行動か結果に落とし、次が気になる余白を残す。")
		case "short_high_similarity", "high_similarity", "exact_repeat", "pre_emit_similarity", "alternating_repeat", "short_alternating_repeat":
			fixes = append(fixes, "相手の語句を言い換えず、INTEREST_HOOKに新しい利害・秘密・誤解のどれかを一つだけ足して会話を前に進める。")
		}
	}
	if hasRedundantTranscript(transcript) {
		issues = append(issues, "直近発話の語彙や構文が近く、聞き手には停滞して聞こえる可能性がある。")
		fixes = append(fixes, "説明を足さず、INTEREST_HOOKを短い感情・具体物・問いのどれか一つに絞る。")
	}
	if len(issues) == 0 {
		return "QUALITY: pass\nBORING_CAUSE: 大きな損耗は検出されませんでした。\nINTEREST_HOOK: \nMISSED_TURN: \nPROMPT_FIX: \nLENGTH_CONTROL: ", ""
	}
	interestHook := inferInterestHook(topic, transcript)
	missedTurn := inferMissedTurn(transcript, loopReason)
	lengthControl := "2文以内。説明を足さず、最後に小さな疑問か余白を残す。"
	promptFix := strings.Join(dedupeNonEmpty(fixes), " ")
	review := fmt.Sprintf("QUALITY: fail\nBORING_CAUSE: %s\nINTEREST_HOOK: %s\nMISSED_TURN: %s\nPROMPT_FIX: %s\nLENGTH_CONTROL: %s",
		strings.Join(issues, " / "), interestHook, missedTurn, promptFix, lengthControl)
	return review, joinPromptGuides(promptFix, lengthControl)
}

func hasRedundantTranscript(transcript []string) bool {
	if len(transcript) < 4 {
		return false
	}
	start := len(transcript) - 4
	if start < 0 {
		start = 0
	}
	repeatedStarts := map[string]int{}
	for _, line := range transcript[start:] {
		text := stripSpeakerPrefix(line)
		runes := []rune(text)
		if len(runes) > 14 {
			runes = runes[:14]
		}
		key := strings.TrimSpace(string(runes))
		if key != "" {
			repeatedStarts[key]++
		}
	}
	for _, n := range repeatedStarts {
		if n >= 2 {
			return true
		}
	}
	return false
}

func stripSpeakerPrefix(line string) string {
	if i := strings.Index(line, ":"); i >= 0 {
		return strings.TrimSpace(line[i+1:])
	}
	if i := strings.Index(line, "："); i >= 0 {
		return strings.TrimSpace(line[i+len("："):])
	}
	return strings.TrimSpace(line)
}

func extractPromptGuidance(review string) string {
	var guides []string
	for _, line := range strings.Split(review, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if m := promptFixLineRe.FindStringSubmatch(line); len(m) == 2 {
			guides = append(guides, strings.TrimSpace(m[1]))
			continue
		}
		if m := interestHookLineRe.FindStringSubmatch(line); len(m) == 2 {
			guides = append(guides, "INTEREST_HOOK: "+strings.TrimSpace(m[1]))
			continue
		}
		if m := missedTurnLineRe.FindStringSubmatch(line); len(m) == 2 {
			guides = append(guides, "MISSED_TURN: "+strings.TrimSpace(m[1]))
			continue
		}
		if m := lengthControlLineRe.FindStringSubmatch(line); len(m) == 2 {
			guides = append(guides, strings.TrimSpace(m[1]))
			continue
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "quality: pass") {
			return ""
		}
	}
	return strings.Join(dedupeNonEmpty(guides), " ")
}

func inferInterestHook(topic string, transcript []string) string {
	for i := len(transcript) - 1; i >= 0; i-- {
		text := stripSpeakerPrefix(transcript[i])
		if hasInterestingConcreteMarker(text) {
			return truncateRunes(text, 80)
		}
	}
	if strings.TrimSpace(topic) != "" {
		return truncateRunes(topic, 80)
	}
	return "会話の中で最初に出た具体物・秘密・損得の芽"
}

func inferMissedTurn(transcript []string, loopReason string) string {
	if len(transcript) == 0 {
		return "抽象論に広げる前に、具体的な場面か選択へ曲がる。"
	}
	text := stripSpeakerPrefix(transcript[len(transcript)-1])
	if note := loopReasonLabel(loopReason); note != "" {
		return truncateRunes(note+"の直前で、"+text, 96)
	}
	return truncateRunes("最後の発話で説明を増やさず、場面・選択・秘密のどれかへ曲がる: "+text, 96)
}

func hasInterestingConcreteMarker(s string) bool {
	markers := []string{"鍵", "秘密", "隠", "嘘", "損", "選", "失敗", "怖", "雨", "机", "駅", "階段", "手紙", "魚", "猫", "文化祭", "祝祭", "体育館"}
	for _, marker := range markers {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

func truncateRunes(s string, limit int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if limit <= 0 || len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "..."
}

func (o *IdleChatOrchestrator) addPromptGuideLocked(guide string) {
	guide = strings.TrimSpace(guide)
	if guide == "" {
		return
	}
	for _, existing := range o.promptGuides {
		if existing == guide {
			return
		}
	}
	o.promptGuides = append(o.promptGuides, guide)
	if len(o.promptGuides) > maxPromptGuides {
		o.promptGuides = o.promptGuides[len(o.promptGuides)-maxPromptGuides:]
	}
}

func promptGuidesFromHistory(history []SessionSummary, limit int) []string {
	if limit <= 0 {
		return nil
	}
	out := make([]string, 0, limit)
	for i := len(history) - 1; i >= 0 && len(out) < limit; i-- {
		guide := strings.TrimSpace(history[i].PromptGuidance)
		if guide == "" {
			continue
		}
		dup := false
		for _, existing := range out {
			if existing == guide {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, guide)
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func formatPromptGuidance(guides []string) string {
	if len(guides) == 0 {
		return ""
	}
	return "\n\n【前回までの聞き手体験レビューに基づくプロンプト補正】\n- " + strings.Join(guides, "\n- ")
}

func joinPromptGuides(parts ...string) string {
	return strings.Join(dedupeNonEmpty(parts), " ")
}

func dedupeNonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		dup := false
		for _, existing := range out {
			if existing == s {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, s)
		}
	}
	return out
}
