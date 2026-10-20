package speecheval

import (
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/speech"
)

const CaseCount = 20

var japaneseCueLabels = map[string]string{
	"pause":     "間を置く",
	"chuckle":   "笑う",
	"sigh":      "ため息をつく",
	"breath":    "息を吸う",
	"gasp":      "息をのむ",
	"whisper":   "ささやく",
	"cry":       "泣き声を出す",
	"shout":     "叫ぶ",
	"cough":     "咳をする",
	"yawn":      "あくびをする",
	"hum":       "鼻歌を歌う",
	"agreement": "同意の声",
	"surprise":  "驚きの声",
	"question":  "疑問の声",
	"relief":    "安堵の声",
}

type Case struct {
	ID            string
	Text          string
	Intent        string
	CueApplicable bool
	Plan          speech.SpeechPlan
}

type Pair struct {
	SessionID      string
	TurnID         string
	NeutralPlan    speech.SpeechPlan
	ExpressivePlan speech.SpeechPlan
}

func AllCases() []Case {
	base := fixedCases()
	cases := make([]Case, len(base))
	for i, item := range base {
		cases[i] = Case{
			ID:            item.ID,
			Text:          item.Text,
			Intent:        item.Intent,
			CueApplicable: item.CueApplicable,
			Plan:          clonePlan(item.Plan),
		}
	}
	return cases
}

func NeutralClone(plan speech.SpeechPlan) speech.SpeechPlan {
	neutral := speech.SpeechPlan{SchemaVersion: plan.SchemaVersion, Utterances: make([]speech.Utterance, len(plan.Utterances))}
	for i, utterance := range plan.Utterances {
		segments := make([]speech.Segment, len(utterance.Segments))
		for j, segment := range utterance.Segments {
			segments[j] = speech.Segment{Text: segment.Text}
		}
		neutral.Utterances[i] = speech.Utterance{
			Segments: segments,
			Delivery: speech.Delivery{Tone: "neutral", Attitude: "neutral", Energy: "medium", Pace: "normal", Mode: "dialogue"},
		}
	}
	return neutral
}

func BuildPair(item Case, sessionID, turnID string) Pair {
	return Pair{
		SessionID:      sessionID,
		TurnID:         turnID,
		NeutralPlan:    NeutralClone(item.Plan),
		ExpressivePlan: clonePlan(item.Plan),
	}
}

func ValidateCases(cases []Case) error {
	if len(cases) != CaseCount {
		return fmt.Errorf("speech evaluation requires exactly %d cases", CaseCount)
	}
	applicable := 0
	for i, item := range cases {
		wantID := fmt.Sprintf("%02d", i+1)
		if item.ID != wantID {
			return fmt.Errorf("speech evaluation case[%d] id must be %q", i, wantID)
		}
		if item.Text == "" || item.Intent == "" {
			return fmt.Errorf("speech evaluation case %s requires text and intent", item.ID)
		}
		result := speech.Normalize(item.Plan)
		if !result.Valid || result.DisplayText != item.Text {
			return fmt.Errorf("speech evaluation case %s has invalid plan text or structure", item.ID)
		}
		hasCue := planHasCue(item.Plan)
		if item.CueApplicable != hasCue {
			return fmt.Errorf("speech evaluation case %s cue applicability does not match its plan", item.ID)
		}
		if item.CueApplicable {
			applicable++
		}
	}
	if applicable == 0 || applicable == len(cases) {
		return fmt.Errorf("speech evaluation cases must include cue-applicable and non-applicable examples")
	}
	return nil
}

func fixedCases() []Case {
	return []Case{
		fixedCase("01", "大丈夫です。ここで一緒に整理していきましょう。", "安心させる", false, "warm", "gentle", "medium", "normal", "dialogue", 0, nil, nil),
		fixedCase("02", "本当に？それはすごくうれしいです！", "喜びを伝える", true, "cheerful", "confident", "high", "fast", "dialogue", 150, []speech.Cue{{Kind: "chuckle", Strength: "light"}}, nil),
		fixedCase("03", "えっ、そんなことがあったんですか？", "驚きを伝える", true, "surprised", "questioning", "high", "normal", "dialogue", 150, []speech.Cue{{Kind: "gasp", Strength: "light"}}, []speech.Cue{{Kind: "question", Strength: "light"}}),
		fixedCase("04", "無理しなくていいですよ。ゆっくりで大丈夫です。", "気遣う", false, "worried", "gentle", "low", "slow", "dialogue", 300, nil, nil),
		fixedCase("05", "よかった……ちゃんと間に合いましたね。", "安堵する", true, "relieved", "gentle", "low", "slow", "dialogue", 300, nil, []speech.Cue{{Kind: "sigh", Strength: "light"}}),
		fixedCase("06", "うーん、少し考える時間をください。", "ためらう", false, "tense", "timid", "low", "slow", "dialogue", 150, nil, nil),
		fixedCase("07", "それ、ちょっとだけ自慢してもいいですか？", "冗談めかす", true, "amused", "teasing", "medium", "normal", "dialogue", 150, nil, []speech.Cue{{Kind: "chuckle", Strength: "light"}}),
		fixedCase("08", "ごめんなさい。伝え方がよくなかったです。", "謝る", true, "sad", "gentle", "low", "slow", "dialogue", 300, nil, []speech.Cue{{Kind: "sigh", Strength: "normal"}}),
		fixedCase("09", "その案、私はかなりいいと思います。", "自信を持って賛成する", false, "warm", "confident", "medium", "normal", "dialogue", 0, nil, nil),
		fixedCase("10", "わあ、きれいな景色ですね！", "感嘆する", true, "cheerful", "gentle", "high", "normal", "dialogue", 150, []speech.Cue{{Kind: "gasp", Strength: "light"}}, nil),
		fixedCase("11", "もう少しだけ、頑張ってみましょう。", "励ます", false, "warm", "confident", "medium", "normal", "dialogue", 150, nil, nil),
		fixedCase("12", "そうなんですね。教えてくれてありがとうございます。", "相づちと感謝", false, "calm", "gentle", "low", "normal", "backchannel", 0, nil, nil),
		fixedCase("13", "やった、ついに完成しました！", "祝う", true, "cheerful", "confident", "high", "fast", "dialogue", 150, []speech.Cue{{Kind: "shout", Strength: "light"}}, []speech.Cue{{Kind: "chuckle", Strength: "light"}}),
		fixedCase("14", "それは少し心配ですね。いっしょに確認しましょう。", "心配する", false, "worried", "gentle", "low", "slow", "dialogue", 300, nil, nil),
		fixedCase("15", "えへへ、気づいてくれたんですね。", "照れながら喜ぶ", true, "embarrassed", "timid", "low", "slow", "dialogue", 150, nil, []speech.Cue{{Kind: "chuckle", Strength: "light"}}),
		fixedCase("16", "それは違います。ここは見直す必要があります。", "穏やかに訂正する", false, "serious", "confident", "medium", "normal", "dialogue", 150, nil, nil),
		fixedCase("17", "少し休憩しませんか？お茶を淹れてきますね。", "休憩を提案する", true, "warm", "gentle", "low", "slow", "dialogue", 300, nil, []speech.Cue{{Kind: "breath", Strength: "light"}}),
		fixedCase("18", "ああ、そういうことだったんですね。", "納得する", true, "relieved", "gentle", "medium", "normal", "dialogue", 150, []speech.Cue{{Kind: "agreement", Strength: "light"}}, nil),
		fixedCase("19", "そんなに急がなくても大丈夫ですよ。", "落ち着かせる", false, "calm", "gentle", "low", "slow", "dialogue", 300, nil, nil),
		fixedCase("20", "明日もまたお話しできるのを楽しみにしています。", "温かく別れを伝える", false, "warm", "gentle", "medium", "slow", "dialogue", 150, nil, nil),
	}
}

func fixedCase(id, text, intent string, cueApplicable bool, tone, attitude, energy, pace, mode string, pause int, before, after []speech.Cue) Case {
	return Case{
		ID:            id,
		Text:          text,
		Intent:        intent,
		CueApplicable: cueApplicable,
		Plan: speech.SpeechPlan{SchemaVersion: speech.SchemaVersion, Utterances: []speech.Utterance{{
			Segments:     []speech.Segment{{Text: text, Before: before, After: after}},
			Delivery:     speech.Delivery{Tone: tone, Attitude: attitude, Energy: energy, Pace: pace, Mode: mode},
			PauseAfterMS: pause,
		}}},
	}
}

func clonePlan(plan speech.SpeechPlan) speech.SpeechPlan {
	return speech.Normalize(plan).Plan
}

func planHasCue(plan speech.SpeechPlan) bool {
	for _, utterance := range plan.Utterances {
		for _, segment := range utterance.Segments {
			if len(segment.Before) > 0 || len(segment.After) > 0 {
				return true
			}
		}
	}
	return false
}

func cueHint(plan speech.SpeechPlan) string {
	normalized := speech.Normalize(plan)
	if !normalized.Valid {
		return ""
	}
	utterances := normalized.Plan.Utterances
	var hints []string
	for utteranceIndex, utterance := range utterances {
		for segmentIndex, segment := range utterance.Segments {
			prefix := ""
			if len(utterances) > 1 {
				prefix = fmt.Sprintf("%dつ目の発話の", utteranceIndex+1)
			}
			before := prefix + cuePosition(segment, segmentIndex, len(utterance.Segments), true)
			for _, cue := range segment.Before {
				if kind, ok := japaneseCueKind(cue.Kind); ok {
					hints = append(hints, before+kind)
				}
			}
			after := prefix + cuePosition(segment, segmentIndex, len(utterance.Segments), false)
			for _, cue := range segment.After {
				if kind, ok := japaneseCueKind(cue.Kind); ok {
					hints = append(hints, after+kind)
				}
			}
		}
	}
	return strings.Join(hints, "、")
}

func cuePosition(segment speech.Segment, segmentIndex, segmentCount int, before bool) string {
	if before {
		if segmentIndex == 0 {
			return "冒頭に"
		}
		return "「" + segment.Text + "」の前に"
	}
	if segmentIndex == segmentCount-1 {
		return "終わりに"
	}
	return "「" + segment.Text + "」の後に"
}

func japaneseCueKind(kind string) (string, bool) {
	label, ok := japaneseCueLabels[kind]
	return label, ok
}
