package conversation

import (
	"strings"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

// RealThreadBoundaryDetector classifies only explicit, unambiguous language
// cues. It is intentionally independent of embeddings, elapsed time, and
// route/domain labels.
type RealThreadBoundaryDetector struct{}

func NewThreadBoundaryDetector() *RealThreadBoundaryDetector {
	return &RealThreadBoundaryDetector{}
}

func (d *RealThreadBoundaryDetector) Detect(currentThread *domconv.Thread, newMessage string) domconv.ThreadBoundaryResult {
	if currentThread == nil || strings.TrimSpace(newMessage) == "" {
		return domconv.ThreadBoundaryResult{Proposal: domconv.ThreadBoundaryUnknown}
	}
	message := stripQuotedAndCode(strings.TrimSpace(newMessage))
	if message == "" {
		return domconv.ThreadBoundaryResult{Proposal: domconv.ThreadBoundaryUnknown}
	}
	newTopic := hasUnquotedInstruction(message, []string{
		"別件です", "別件ですが", "別件で", "別の話です", "別の話ですが",
		"話題を変えます", "話題を変えて", "話を変えます", "話変わるけど",
		"質問を変えます", "質問を変えて", "質問変えて", "質問を変えたい", "別の話にします", "別の質問です",
		"前の話は置いて", "前の話は一旦置いて", "前の話はいったん置いて",
	})
	continueTopic := hasUnquotedInstruction(message, []string{
		"続けて", "続けます", "続きを", "前の話の続き", "前の相談の続き", "前回の話の続き", "先ほど", "さっきの", "その件", "その話", "それについて",
		"この件", "これについて", "前の回答", "前の質問", "前の内容", "先ほどの質問", "もう少し詳しく",
	})
	if newTopic && continueTopic {
		return domconv.ThreadBoundaryResult{Proposal: domconv.ThreadBoundaryKeep}
	}
	if continueTopic {
		return domconv.ThreadBoundaryResult{Proposal: domconv.ThreadBoundaryKeep}
	}
	if newTopic {
		return domconv.ThreadBoundaryResult{
			Proposal: domconv.ThreadBoundaryNew,
			Reason:   domconv.BoundaryExplicitTopicChange,
		}
	}
	return domconv.ThreadBoundaryResult{Proposal: domconv.ThreadBoundaryUnknown}
}

func hasUnquotedInstruction(message string, cues []string) bool {
	for _, cue := range cues {
		for offset := 0; offset < len(message); {
			index := strings.Index(message[offset:], cue)
			if index < 0 {
				break
			}
			start := offset + index
			end := start + len(cue)
			if !isNegatedOrMentionedCue(message, end) {
				return true
			}
			offset = end
		}
	}
	return false
}

func isNegatedOrMentionedCue(message string, end int) bool {
	clauseEnd := len(message)
	if relative := strings.IndexAny(message[end:], "。.!?！？\n"); relative >= 0 {
		clauseEnd = end + relative
	}
	after := message[end:clauseEnd]
	if len(after) > 48 {
		after = after[:48]
	}
	for _, negation := range []string{
		"ではありません", "ではなく", "じゃありません", "じゃない", "わけではない", "わけではなく", "のではなく", "のではない",
		"はありません", "はなく", "はない", "ありません", "いませんでした", "いなかった", "いません", "いない",
		"ほしくありません", "ほしくない", "欲しくありません", "欲しくない", "はいけません", "はいけない", "はだめ", "は駄目",
	} {
		if strings.Contains(after, negation) {
			return true
		}
	}
	for _, mention := range []string{
		"という言葉", "という表現", "という意味", "というフレーズ", "という", "と書かれ", "と書いて",
		"と言った", "と言って", "と言いました", "と言われ", "と説明した", "と説明して", "と出てくる",
	} {
		if strings.Contains(after, mention) {
			return true
		}
	}
	return false
}

func stripQuotedAndCode(message string) string {
	var builder strings.Builder
	var closing rune
	var closingBackticks int
	runes := []rune(message)
	for index := 0; index < len(runes); {
		current := runes[index]
		if current == '\n' && (closing == 0 || closing == '`') {
			builder.WriteRune(current)
		}
		if closing != 0 {
			if current == closing {
				if closing == '`' {
					count := 1
					for index+count < len(runes) && runes[index+count] == '`' {
						count++
					}
					if count >= closingBackticks {
						closing = 0
						closingBackticks = 0
						index += count
						continue
					}
				} else {
					closing = 0
				}
			}
			index++
			continue
		}
		if current == '>' && (index == 0 || runes[index-1] == '\n') {
			for index < len(runes) && runes[index] != '\n' {
				index++
			}
			continue
		}
		switch current {
		case '「':
			closing = '」'
		case '『':
			closing = '』'
		case '“':
			closing = '”'
		case '"':
			closing = '"'
		case '`':
			closing = '`'
			closingBackticks = 1
			for index+closingBackticks < len(runes) && runes[index+closingBackticks] == '`' {
				closingBackticks++
			}
		default:
			builder.WriteRune(current)
		}
		if closing != 0 {
			builder.WriteRune(' ')
		}
		index++
	}
	return builder.String()
}
