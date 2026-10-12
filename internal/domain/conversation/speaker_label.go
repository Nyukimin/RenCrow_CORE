package conversation

import (
	"strings"
	"sync"
	"unicode"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

// RecallPack.ToPromptMessages は履歴の Agent 発言を `[speaker] msg` の形でモデルへ渡す。
// モデルはこの形を真似て、応答の頭に Agent 名のラベル（自分以外の名前や、重なった
// `[midori] [midori] ` を含む）を付けることがある。ラベルは発話者の情報であり本文ではない
// （発話者は event の from と会話履歴の speaker が持つ）。そのため、応答の本文・stream・
// 読み上げ文・保存する履歴には入れない。履歴の形式を作る側と、その逆変換を同じ package が持つ。

// agentLabelNames は、先頭ラベルとして除去する既知の Agent 名（小文字）。CORE の Agent
// identity（application/agentprofile の Catalog が持つ ID）と一致する。履歴に入る
// ラベルは CanonicalChatAgentSpeaker が返す4名だが、モデルは他の Agent 名も真似得るため、
// 全 Agent identity を対象にする。一致は agentprofile の test が強制する。
var agentLabelNames = []string{
	string(SpeakerMio),
	string(SpeakerShiro),
	string(SpeakerKuro),
	string(SpeakerMidori),
	string(SpeakerAka),
	string(SpeakerAo),
	string(SpeakerKin),
	string(SpeakerGin),
}

// agentLabels は agentLabelNames の `[name]` 形（小文字）。
var agentLabels = func() []string {
	labels := make([]string, 0, len(agentLabelNames))
	for _, name := range agentLabelNames {
		labels = append(labels, "["+name+"]")
	}
	return labels
}()

// AgentLabelNames は、先頭ラベルとして除去する既知の Agent 名（小文字）の一覧を返す。
func AgentLabelNames() []string {
	return append([]string(nil), agentLabelNames...)
}

// StripLeadingSpeakerLabels は、応答の先頭にある既知の Agent 名の角括弧ラベルの連続
// （例 `[midori] `、`[midori] [shiro]: `）と、その直後の空白・コロンを取り除く。
// 自分以外の Agent 名も対象とする。本文の途中にあるラベルと、既知でない角括弧（`[注]`）は
// 変えない。前後の空白も取り除く。
func StripLeadingSpeakerLabels(content string) string {
	rest := strings.TrimLeftFunc(content, unicode.IsSpace)
	for {
		n := leadingLabelLen(rest)
		if n == 0 {
			break
		}
		rest = strings.TrimLeftFunc(rest[n:], isLabelSeparator)
	}
	return strings.TrimSpace(rest)
}

// isLabelSeparator はラベルの直後に置かれる区切り（空白とコロン）。
func isLabelSeparator(r rune) bool {
	return unicode.IsSpace(r) || r == ':' || r == '：'
}

// leadingLabelLen は text の先頭が既知ラベルならそのバイト長、そうでなければ0を返す。
func leadingLabelLen(text string) int {
	for _, label := range agentLabels {
		if len(text) >= len(label) && equalFoldASCII(text[:len(label)], label) {
			return len(label)
		}
	}
	return 0
}

// isLeadingLabelPrefix は text が、まだ完結していない既知ラベルの先頭部分かを返す。
func isLeadingLabelPrefix(text string) bool {
	for _, label := range agentLabels {
		if len(text) < len(label) && equalFoldASCII(text, label[:len(text)]) {
			return true
		}
	}
	return false
}

// equalFoldASCII は ASCII の大文字小文字だけを無視して比較する。Unicode の
// case folding（例 KELVIN SIGN と k）は同一視しない。b は小文字の ASCII。
func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		c := a[i]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != b[i] {
			return false
		}
	}
	return true
}

// speakerLabelStream は stream の先頭にある Agent 名ラベルだけを取り除く。
type speakerLabelStream struct {
	mu   sync.Mutex
	next llm.StreamCallback
	// head は、まだ流すか捨てるかを決めていない先頭部分。
	head string
	// afterLabel は、ラベルを1つ以上捨てた後であること（直後のコロンも捨てる）。
	afterLabel bool
	// passthrough は、先頭の判定が済み、以降の delta をそのまま流すこと。
	passthrough bool
}

// NewSpeakerLabelStreamFilter は next を包み、stream の先頭にある既知の Agent 名ラベルの
// 連続（delta に分割されて届いてもよい）を取り除く callback と、完了時に呼ぶ flush を返す。
// 保留するのは、先頭が `[` で始まり、かつ既知ラベルの先頭部分である間だけ（最長でラベル1つ分）。
// ラベルが確定したら捨て、既知ラベルでないと分かった時点で保留分をまとめて流す。
// 先頭以降の delta は保留しない。flush は、判定の途中で stream が終わった場合に保留分を流す
// （冪等）。next が nil のときは nil と何もしない flush を返し、呼び出し側の非streamの経路を保つ。
func NewSpeakerLabelStreamFilter(next llm.StreamCallback) (llm.StreamCallback, func()) {
	if next == nil {
		return nil, func() {}
	}
	s := &speakerLabelStream{next: next}
	return s.write, s.flush
}

func (s *speakerLabelStream) write(token string) {
	if token == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.passthrough {
		s.next(token)
		return
	}
	s.head += token
	s.decide(false)
}

func (s *speakerLabelStream) flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.passthrough {
		return
	}
	s.decide(true)
	s.passthrough = true
}

// decide は先頭部分を評価する。final は stream の終了後であること。
func (s *speakerLabelStream) decide(final bool) {
	for !s.passthrough {
		s.head = strings.TrimLeftFunc(s.head, s.isSkippable)
		if s.head == "" {
			return
		}
		if s.head[0] == '[' {
			if n := leadingLabelLen(s.head); n > 0 {
				s.head = s.head[n:]
				s.afterLabel = true
				continue
			}
			if !final && isLeadingLabelPrefix(s.head) {
				return
			}
		}
		s.passthrough = true
		head := s.head
		s.head = ""
		s.next(head)
	}
}

func (s *speakerLabelStream) isSkippable(r rune) bool {
	if s.afterLabel {
		return isLabelSeparator(r)
	}
	return unicode.IsSpace(r)
}
