package conversation

import (
	"fmt"
	"sort"
	"testing"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

func makeDetectorTestThread(domain string, turnsAgo time.Duration, messages ...string) *domconv.Thread {
	t := &domconv.Thread{
		SessionID: "test-session",
		Domain:    domain,
		Status:    domconv.ThreadActive,
	}
	for _, msg := range messages {
		m := domconv.NewMessage(domconv.SpeakerUser, msg, nil)
		m.Timestamp = time.Now().Add(-turnsAgo)
		t.Turns = append(t.Turns, m)
	}
	return t
}

func TestDetectExplicitTopicChanges(t *testing.T) {
	detector := NewThreadBoundaryDetector()
	thread := makeDetectorTestThread("general", time.Minute, "こんにちは", "元気？")
	for _, message := range []string{
		"別件です。今週の予定を確認したい",
		"話題を変えます。旅行の持ち物を教えて",
		"質問を変えます。別の件を相談します",
		"前の話は置いて、料理の作り方を教えて",
	} {
		result := detector.Detect(thread, message)
		if result.Proposal != domconv.ThreadBoundaryNew || result.Reason != domconv.BoundaryExplicitTopicChange {
			t.Errorf("Detect(%q) = %#v, want explicit new-topic proposal", message, result)
		}
	}
}

func TestDetectQuotedMentionAndAmbiguousTopicChange(t *testing.T) {
	detector := NewThreadBoundaryDetector()
	thread := makeDetectorTestThread("general", time.Minute, "前の相談")
	for _, message := range []string{
		"「別件です」と書かれた文の意味を教えて",
		"『話題を変える』という表現を解説して",
		"`別件です` と書かれた行を確認して",
		"ところで、先ほどの話を続けて",
		"ところで",
	} {
		result := detector.Detect(thread, message)
		if result.Proposal == domconv.ThreadBoundaryNew {
			t.Errorf("quoted or ambiguous message %q split the thread: %#v", message, result)
		}
	}
}

func TestDetectContinuationAndInactivity(t *testing.T) {
	detector := NewThreadBoundaryDetector()
	thread := makeDetectorTestThread("general", 2*time.Hour, "前の相談内容")
	for _, message := range []string{
		"さっきの続きを教えて",
		"先ほどの質問にもう一つ補足です",
		"その件をもう少し詳しく説明して",
		"前の回答について質問があります",
		"久しぶり。前の話の続きをお願い",
	} {
		result := detector.Detect(thread, message)
		if result.Proposal != domconv.ThreadBoundaryKeep {
			t.Errorf("Detect(%q) = %#v, want keep despite elapsed time", message, result)
		}
	}
	if result := detector.Detect(thread, "久しぶりに話します"); result.Proposal != domconv.ThreadBoundaryUnknown {
		t.Fatalf("inactivity-only result = %#v, want unknown", result)
	}
}

func TestDetectNegatedChangeAndConflictingSignalsKeep(t *testing.T) {
	detector := NewThreadBoundaryDetector()
	thread := makeDetectorTestThread("general", time.Minute, "前の相談")
	for _, message := range []string{
		"別件ではありません。先ほどの続きです",
		"話題を変えたいのではなく、さっきの質問の補足です",
		"質問を変えたいのではなく、前の回答を詳しくして",
		"別件です。前の回答の続きをお願いします",
	} {
		result := detector.Detect(thread, message)
		if result.Proposal != domconv.ThreadBoundaryKeep {
			t.Errorf("negated or conflicting message %q = %#v, want keep", message, result)
		}
	}
}

func TestDetectNegatedCuePrefixesDoNotProposeNewBoundary(t *testing.T) {
	detector := NewThreadBoundaryDetector()
	thread := makeDetectorTestThread("general", time.Minute, "前の相談")
	for _, tc := range []struct {
		message string
		want    domconv.ThreadBoundaryProposal
	}{
		{message: "別件ではありません。前の話の続きです。", want: domconv.ThreadBoundaryKeep},
		{message: "別件ではありません。", want: domconv.ThreadBoundaryUnknown},
		{message: "話題を変えていません。", want: domconv.ThreadBoundaryUnknown},
		{message: "質問を変えていません。前の回答の続きです。", want: domconv.ThreadBoundaryKeep},
		{message: "話題を変えてほしくないです。", want: domconv.ThreadBoundaryUnknown},
		{message: "質問を変えてはいけません。", want: domconv.ThreadBoundaryUnknown},
		{message: "質問を変えてはだめです。", want: domconv.ThreadBoundaryUnknown},
		{message: "話題を変えてほしくないです。さっきの話の続きを教えて。", want: domconv.ThreadBoundaryKeep},
		{message: "質問を変えてはいけません。前の回答の続きです。", want: domconv.ThreadBoundaryKeep},
	} {
		result := detector.Detect(thread, tc.message)
		if result.Proposal != tc.want {
			t.Errorf("Detect(%q) = %#v, want %q", tc.message, result, tc.want)
		}
	}
}

func TestDetectExplicitNewCueIsNotOverriddenByContinuationNouns(t *testing.T) {
	detector := NewThreadBoundaryDetector()
	thread := makeDetectorTestThread("general", time.Minute, "前の相談")
	for _, message := range []string{
		"別件です。追加料金はいくらですか",
		"別件です。補足資料を確認したいです",
		"別件です。詳しく知りたいです",
		"別件です。追記の方法を教えてください",
	} {
		result := detector.Detect(thread, message)
		if result.Proposal != domconv.ThreadBoundaryNew || result.Reason != domconv.BoundaryExplicitTopicChange {
			t.Errorf("Detect(%q) = %#v, want explicit new-topic proposal", message, result)
		}
	}
}

type threadBoundaryQualityCase struct {
	name    string
	message string
	age     time.Duration
	want    domconv.ThreadBoundaryProposal
}

func threadBoundaryQualityCases() []threadBoundaryQualityCase {
	cases := make([]threadBoundaryQualityCase, 0, 100)
	appendCases := func(group string, want domconv.ThreadBoundaryProposal, age time.Duration, messages ...string) {
		for index, message := range messages {
			cases = append(cases, threadBoundaryQualityCase{
				name:    fmt.Sprintf("%s-%02d", group, index+1),
				message: message,
				age:     age,
				want:    want,
			})
		}
	}

	appendCases("explicit-switch", domconv.ThreadBoundaryNew, time.Minute,
		"別件です。今週の予定を確認したいです",
		"別件ですが、旅行の持ち物を教えてください",
		"別件で相談があります。料理を始めたいです",
		"別の話です。新しい趣味を探しています",
		"別の話ですが、近所の図書館を調べたいです",
		"話題を変えます。明日の天気を教えてください",
		"話題を変えて、引っ越しの準備を相談したいです",
		"話を変えます。おすすめの映画はありますか",
		"話変わるけど、犬を飼う準備を知りたい",
		"質問を変えます。電車の乗り換えを教えて",
		"質問を変えて、英語の勉強法を知りたいです",
		"質問変えて、家計簿の付け方を教えて",
		"質問を変えたいです。週末の服装を相談したい",
		"別の話にします。簡単な夕食の案をください",
		"別の質問です。植物の水やり頻度を知りたい",
		"前の話は置いて、近場の温泉を探したいです",
		"前の話は一旦置いて、資格の選び方を教えて",
		"前の話はいったん置いて、健康診断の準備を聞きたい",
		"ところで別件です。新しい相談を始めます",
		"今日は話題を変えます。料理道具を選びたいです",
	)
	appendCases("continuation", domconv.ThreadBoundaryKeep, time.Minute,
		"さっきの続きを教えてください",
		"先ほどの質問にもう一つ補足です",
		"その件をもう少し詳しく説明して",
		"前の回答について質問があります",
		"これについて、例を追加してください",
		"この件の条件を一つ補足します",
		"先ほどの話を続けてください",
		"さっきの説明の続きをお願いします",
		"その件について具体例をください",
		"前の質問に関連して、もう一つ聞きたいです",
		"前の回答を少し詳しくして",
		"先ほどの相談の続きです",
		"それについて、別の例も見せてください",
		"これの手順を続けて説明して",
		"前の内容に追記があります",
		"その話をもう少し掘り下げてください",
		"前に聞いた件の続きをお願いします",
		"先ほどの質問を補足します",
		"さっきの件を続けたいです",
		"前の回答について、追加で確認させてください",
	)
	appendCases("quoted-cue", domconv.ThreadBoundaryUnknown, time.Minute,
		"「別件です」という言葉の意味を教えてください",
		"『話題を変えます』と書かれた文を翻訳してください",
		"`質問を変えます` というコメントを確認してください",
		"“別の話です”という表現は自然ですか",
		"\"別件ですが\" を文章で使う例を示して",
		"「前の話は置いて」と言う場面を説明してください",
		"『質問を変えて』の文法を解説してください",
		"コード内の `別件です` を検索する方法を教えて",
		"> 話題を変えます。\nこの引用の句読点を直して",
		"「別の質問です」と書いてある箇所を探したいです",
		"『別の話にします』という台詞を英訳して",
		"`話変わるけど` は砕けた表現ですか",
		"「質問を変えたいです」の敬語を考えてください",
		"『別件で』と『別件ですが』の違いを知りたい",
		"引用文の「前の話は一旦置いて」を校正して",
		"ログに `話題を変えて` とあります。意味は何ですか",
		"「別件です」と書いた見出しの長さを確認して",
		"『話を変えます』という文の主語を説明して",
		"\"質問変えて\" を丁寧に言い換えてください",
		"「別の話ですが」の後に続く文の例をください",
	)
	appendCases("negated-cue", domconv.ThreadBoundaryUnknown, time.Minute,
		"別件ではありません。",
		"話題を変えていません。",
		"質問を変えていません。",
		"質問を変えるわけではありません。",
		"別件じゃないです。",
		"話題を変えたいわけではないです。",
		"別件でありません。",
		"話題を変えてはいません。",
		"質問を変えてもいません。",
	)
	appendCases("negated-cue-continuation", domconv.ThreadBoundaryKeep, time.Minute,
		"別件ではありません。前の話の続きです。",
		"話題を変えていません。さっきの話の続きです。",
		"質問を変えていません。前の回答を詳しくして。",
		"別件ではなく、先ほどの続きです。",
		"別件ではなく、前の回答に補足があります。",
		"話題を変えるわけではなく、さっきの質問の補足です。",
		"質問を変えたいのではなく、前の回答を詳しくして。",
		"前の話は置いておくのではなく、その続きをお願いします。",
		"話題を変えるのではないです。前の相談を続けます。",
		"別件ではないので、前の話の続きをお願いします。",
		"話題を変えていないので、その件を続けてください。",
	)
	appendCases("ambiguous-and-return-unknown", domconv.ThreadBoundaryUnknown, 365*24*time.Hour,
		"ところで",
		"ところで、近況を報告します",
		"久しぶりに話します",
		"久しぶりに相談したいことがあります",
		"以前の話題に戻ります",
		"前に相談した趣味の話に戻したいです",
		"前の会話で出た旅行の話を思い出しました",
		"以前の相談のことですが、どうでしたか",
	)
	appendCases("long-inactivity-continuation", domconv.ThreadBoundaryKeep, 30*24*time.Hour,
		"ところで、前の話の続きをお願いします",
		"前回の話の続きに戻ります",
		"先ほどの質問の続きをお願いします",
		"久しぶりですが、さっきの件を続けたいです",
		"何日か空きましたが、前の回答を詳しくしてください",
		"数か月ぶりです。その件の続きを教えてください",
		"半年ほど経ちましたが、先ほどの相談を補足します",
		"一年ぶりですが、前の話の続きをお願いします",
		"しばらく空きました。前の質問に追加があります",
	)
	appendCases("past-topic-explicit-switch", domconv.ThreadBoundaryNew, time.Minute,
		"ところで別件です。新しい相談です",
		"久しぶりに別件です。新しい話をします",
		"前の話は置いて、改めて別件です",
	)
	for index, inactivityDays := range []int{1, 30, 90, 180, 365, 730, 1095} {
		cases[len(cases)-9+index].age = time.Duration(inactivityDays) * 24 * time.Hour
	}
	return cases
}

func TestThreadBoundaryLightweightQualitySetHas100Scenarios(t *testing.T) {
	detector := NewThreadBoundaryDetector()
	cases := threadBoundaryQualityCases()
	if len(cases) < 100 {
		t.Fatalf("quality scenario count = %d, want at least 100", len(cases))
	}
	latencies := make([]time.Duration, 0, len(cases))
	for _, tc := range cases {
		thread := makeDetectorTestThread("general", tc.age, "前の相談内容")
		result := detector.Detect(thread, tc.message)
		if result.Proposal != tc.want {
			t.Errorf("scenario %s Detect(%q) = %#v, want %q", tc.name, tc.message, result, tc.want)
		}
		started := time.Now()
		for repeat := 0; repeat < 500; repeat++ {
			_ = detector.Detect(thread, tc.message)
		}
		latencies = append(latencies, time.Since(started)/500)
	}
	t.Logf("in-memory rule quality set n=%d p50=%s p95=%s p99=%s max=%s (500 calls/scenario)", len(latencies), durationPercentile(latencies, 50), durationPercentile(latencies, 95), durationPercentile(latencies, 99), maxDuration(latencies))
}

func durationPercentile(samples []time.Duration, percentile int) time.Duration {
	ordered := append([]time.Duration(nil), samples...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	index := (percentile*len(ordered)+99)/100 - 1
	if index < 0 {
		index = 0
	}
	if index >= len(ordered) {
		index = len(ordered) - 1
	}
	return ordered[index]
}

func maxDuration(samples []time.Duration) time.Duration {
	var max time.Duration
	for _, sample := range samples {
		if sample > max {
			max = sample
		}
	}
	return max
}

func BenchmarkThreadBoundaryLightweightQualitySet100(b *testing.B) {
	detector := NewThreadBoundaryDetector()
	thread := makeDetectorTestThread("general", 2*time.Hour, "前の相談内容")
	cases := threadBoundaryQualityCases()
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		_ = detector.Detect(thread, cases[index%len(cases)].message)
	}
}
