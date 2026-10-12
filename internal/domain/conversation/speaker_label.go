package conversation

import (
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

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
// （例 `[midori] `、`[midori] [shiro]: `）と、その直後の区切りを取り除く。自分以外の
// Agent 名も対象とする。前後の空白も取り除く。
//
// ラベルを取り除くのは、直後に区切り（半角空白・全角空白・タブ・改行・コロン（半角／全角））が
// ある場合と、本文がそのラベルだけの場合に限る。履歴の形式 `[speaker] msg` は空白で区切るため、
// modelが真似たラベルはこの条件を満たす。区切りが無いもの（`[shiro]からの報告です`、
// `[mio]は…`）は、他Agentの報告を中継する言い方などの本文として残す。本文の途中にある
// ラベルと、既知でない角括弧（`[注]`）も変えない。
func StripLeadingSpeakerLabels(content string) string {
	rest := strings.TrimLeftFunc(content, unicode.IsSpace)
	for {
		n := leadingLabelLen(rest)
		if n == 0 {
			break
		}
		after := rest[n:]
		if after != "" {
			r, _ := utf8.DecodeRuneInString(after)
			if !isLabelSeparator(r) {
				break
			}
		}
		rest = strings.TrimLeftFunc(after, isLabelSeparator)
	}
	return strings.TrimSpace(rest)
}

// isLabelSeparator はラベルの直後に置かれる区切り。半角空白・全角空白・タブ・改行・
// コロン（半角／全角）。
func isLabelSeparator(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '\u3000', ':', '：':
		return true
	}
	return false
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
// StripLeadingSpeakerLabels と同じ規則で、ラベルの直後に区切りがある場合と、ラベルだけで終わる
// 場合に限って取り除く。
// 保留するのは、先頭が `[` で始まり、既知ラベルの先頭部分である間と、既知ラベルが閉じた直後の
// 1文字（区切りかどうかが決まるまで）だけ。区切りならラベルを捨て、区切りでなければ保留分を
// ラベルごと流す。既知ラベルでないと分かった時点でも保留分をまとめて流す。確定後の delta は
// 保留しない。flush は、判定の途中で stream が終わった場合に処理する（冪等）。ラベルで終わった
// stream はラベルだけの応答として捨て、ラベルの先頭部分で終わった stream は流す。
// next が nil のときは nil と何もしない flush を返し、呼び出し側の非streamの経路を保つ。
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
//
// 先頭が `[` で既知ラベルとして閉じた後は、次の1文字で区切りかどうかが決まる。区切りなら
// ラベルを捨てて続きを評価し、区切りでなければラベルごと流す。ラベルで終わった stream
// （final）は、ラベルだけの応答として捨てる。
func (s *speakerLabelStream) decide(final bool) {
	for !s.passthrough {
		s.head = strings.TrimLeftFunc(s.head, s.isSkippable)
		if s.head == "" {
			return
		}
		if s.head[0] == '[' {
			if n := leadingLabelLen(s.head); n > 0 {
				rest := s.head[n:]
				if rest == "" {
					if final {
						s.head = ""
						return
					}
					return // 閉じたラベル。次の delta で区切りかどうかが決まる
				}
				if !final && !utf8.FullRuneInString(rest) {
					return // 区切りの先頭 rune が delta をまたいでいる
				}
				if r, _ := utf8.DecodeRuneInString(rest); isLabelSeparator(r) {
					s.head = rest
					s.afterLabel = true
					continue
				}
			} else if !final && isLeadingLabelPrefix(s.head) {
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
