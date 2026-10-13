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
// Agent 名も対象とする。前後の空白も取り除く。self は応答を生成した Agent。
//
// ラベルごとに、先頭から順に次のとおり判定する。
//   - 生成した Agent 自身の名前のラベル（self）: 区切りが無くても取り除く。直後の空白・コロンも
//     取り除く（`[mio]こんにちは` は `こんにちは`）。
//   - 他の既知の Agent 名のラベル: 直後に区切り（半角空白・全角空白・タブ・改行・コロン（半角／
//     全角））がある場合と、本文がそのラベルだけの場合に限って取り除く。履歴の形式 `[speaker] msg`
//     は空白で区切るため、modelが真似たラベルはこの条件を満たす。区切りが無いもの
//     （`[shiro]からの報告です`）は、他Agentの報告を中継する言い方などの本文として残し、
//     そこで判定を止める。
//
// 本文の途中にあるラベルと、既知でない角括弧（`[注]`）は変えない。
func StripLeadingSpeakerLabels(content string, self Speaker) string {
	selfName := selfLabelName(self)
	rest := strings.TrimLeftFunc(content, unicode.IsSpace)
	for {
		n, isSelf := leadingLabel(rest, selfName)
		if n == 0 {
			break
		}
		after := rest[n:]
		if !isSelf && after != "" {
			r, _ := utf8.DecodeRuneInString(after)
			if !isLabelSeparator(r) {
				break
			}
		}
		rest = strings.TrimLeftFunc(after, isLabelSeparator)
	}
	return strings.TrimSpace(rest)
}

// selfLabelName は、生成した Agent の speaker から、自分のラベルに使う小文字の名前を返す。
// 旧別名（heavy／wild）は Agent identity（kuro／midori）に直す。
func selfLabelName(speaker Speaker) string {
	if canonical, ok := CanonicalChatAgentSpeaker(speaker); ok {
		speaker = canonical
	}
	return strings.ToLower(strings.TrimSpace(string(speaker)))
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

// leadingLabel は text の先頭が既知ラベル（または self のラベル）ならそのバイト長と、
// それが self のラベルかを返す。先頭がラベルでなければ0を返す。selfName は小文字（空は無し）。
func leadingLabel(text, selfName string) (n int, isSelf bool) {
	if selfName != "" {
		if label := "[" + selfName + "]"; hasLabelPrefix(text, label) {
			return len(label), true
		}
	}
	for _, label := range agentLabels {
		if hasLabelPrefix(text, label) {
			return len(label), false
		}
	}
	return 0, false
}

// isLeadingLabelPrefix は text が、まだ完結していない既知ラベル（または self のラベル）の
// 先頭部分かを返す。
func isLeadingLabelPrefix(text, selfName string) bool {
	if selfName != "" {
		if label := "[" + selfName + "]"; len(text) < len(label) && equalFoldASCII(text, label[:len(text)]) {
			return true
		}
	}
	for _, label := range agentLabels {
		if len(text) < len(label) && equalFoldASCII(text, label[:len(text)]) {
			return true
		}
	}
	return false
}

// hasLabelPrefix は text が label（小文字の `[name]`）で始まるかを、ASCII の大文字小文字を
// 無視して返す。
func hasLabelPrefix(text, label string) bool {
	return len(text) >= len(label) && equalFoldASCII(text[:len(label)], label)
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
	mu       sync.Mutex
	next     llm.StreamCallback
	selfName string
	// head は、まだ流すか捨てるかを決めていない先頭部分。
	head string
	// afterLabel は、ラベルを1つ以上捨てた後であること（直後のコロンも捨てる）。
	afterLabel bool
	// passthrough は、先頭の判定が済み、以降の delta をそのまま流すこと。
	passthrough bool
}

// NewSpeakerLabelStreamFilter は next を包み、stream の先頭にある既知の Agent 名ラベルの
// 連続（delta に分割されて届いてもよい）を取り除く callback と、完了時に呼ぶ flush を返す。
// self は応答を生成した Agent。StripLeadingSpeakerLabels と同じ規則で、self のラベルは区切りが
// 無くても取り除き、他のラベルは直後に区切りがある場合と、ラベルだけで終わる場合に限って
// 取り除く。
// 保留するのは、先頭が `[` で始まり、既知ラベルの先頭部分である間と、self 以外の既知ラベルが
// 閉じた直後の1文字（区切りかどうかが決まるまで）だけ。区切りならラベルを捨て、区切りでなければ
// 保留分をラベルごと流す。既知ラベルでないと分かった時点でも保留分をまとめて流す。確定後の delta は
// 保留しない。flush は、判定の途中で stream が終わった場合に処理する（冪等）。ラベルで終わった
// stream はラベルだけの応答として捨て、ラベルの先頭部分で終わった stream は流す。
// next が nil のときは nil と何もしない flush を返し、呼び出し側の非streamの経路を保つ。
func NewSpeakerLabelStreamFilter(next llm.StreamCallback, self Speaker) (llm.StreamCallback, func()) {
	if next == nil {
		return nil, func() {}
	}
	s := &speakerLabelStream{next: next, selfName: selfLabelName(self)}
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
// self のラベルは閉じた時点で捨てる。self 以外の既知ラベルが閉じた後は、次の1文字で区切りか
// どうかが決まる。区切りならラベルを捨てて続きを評価し、区切りでなければラベルごと流す。
// ラベルで終わった stream（final）は、ラベルだけの応答として捨てる。
func (s *speakerLabelStream) decide(final bool) {
	for !s.passthrough {
		s.head = strings.TrimLeftFunc(s.head, s.isSkippable)
		if s.head == "" {
			return
		}
		if !final && !utf8.FullRuneInString(s.head) {
			return // 先頭の rune が delta をまたいでいる
		}
		if s.head[0] == '[' {
			if n, isSelf := leadingLabel(s.head, s.selfName); n > 0 {
				rest := s.head[n:]
				if isSelf {
					s.head = rest
					s.afterLabel = true
					continue
				}
				if rest == "" {
					if final {
						s.head = ""
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
			} else if !final && isLeadingLabelPrefix(s.head, s.selfName) {
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
